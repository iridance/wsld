import type { K8sConfigMap, K8sDeployment, K8sIngress, K8sOther, K8sPod, K8sPVC, K8sService, K8sStatefulSet } from "./api";

/** 架構圖與 namespace 卡片所需的資源（全部 namespace 的清單，依 namespace 過濾）。 */
export interface K8sResources {
  pods: K8sPod[];
  deployments: K8sDeployment[];
  services: K8sService[];
  statefulsets: K8sStatefulSet[];
  ingresses: K8sIngress[];
  configmaps: K8sConfigMap[];
  pvcs: K8sPVC[];
  others: K8sOther[];
}

/** `kube-` 開頭是 Kubernetes 保留給內建 namespace 的前綴（使用者不能建立）。 */
export const isSystemNs = (ns: string): boolean => ns.startsWith("kube-");

export type NsHealth = "ok" | "warn" | "error" | "empty";

export interface NsSummary {
  namespace: string;
  ingresses: number;
  services: number;
  /** Deployment + StatefulSet */
  workloads: number;
  pods: number;
  podsReady: number;
  configMaps: number;
  pvcs: number;
  /** 沒有專屬分頁的資源，依種類計數（Lease、Role…），讓卡片如實顯示 namespace 裡有什麼 */
  others: { kind: string; count: number }[];
  /** 對外的 NodePort（TCP，去重、由小到大）；本地常不經 Ingress、直接用 NodePort 進入 */
  nodePorts: number[];
  health: NsHealth;
}

/** Pod 是否算「就緒」：Running／Completed 且所有容器 ready。 */
function podReady(p: K8sPod): boolean {
  // 已跑完的 Job Pod 容器都已結束，ready 為 0/N，仍算正常
  if (p.status === "Succeeded" || p.status === "Completed") return true;
  const [r, n] = p.ready.split("/").map(Number);
  return p.status === "Running" && r === n;
}

function podFailing(p: K8sPod): boolean {
  if (["Running", "Succeeded", "Completed", "Pending", "ContainerCreating", "PodInitializing", "Terminating"].includes(p.status) || p.status.startsWith("Init:")) return false;
  return true;
}

const inNs = <T extends { namespace: string }>(xs: T[], ns: string) => xs.filter((x) => x.namespace === ns);

/** Service 的 TCP NodePort 清單。 */
export function nodePortsOf(s: K8sService): number[] {
  return s.ports.filter((p) => p.node_port && (p.protocol === "" || p.protocol === "TCP")).map((p) => p.node_port as number);
}

function otherCounts(xs: K8sOther[]): { kind: string; count: number }[] {
  const m = new Map<string, number>();
  for (const x of xs) m.set(x.kind, (m.get(x.kind) ?? 0) + 1);
  return [...m].map(([kind, count]) => ({ kind, count })).sort((a, b) => a.kind.localeCompare(b.kind));
}

export function summarizeNamespace(ns: string, r: K8sResources): NsSummary {
  const pods = inNs(r.pods, ns);
  const podsReady = pods.filter(podReady).length;
  const s: NsSummary = {
    namespace: ns,
    ingresses: inNs(r.ingresses, ns).length,
    services: inNs(r.services, ns).length,
    workloads: inNs(r.deployments, ns).length + inNs(r.statefulsets, ns).length,
    pods: pods.length,
    podsReady,
    configMaps: inNs(r.configmaps, ns).length,
    pvcs: inNs(r.pvcs, ns).length,
    others: otherCounts(inNs(r.others, ns)),
    nodePorts: [...new Set(inNs(r.services, ns).flatMap(nodePortsOf))].sort((a, b) => a - b),
    health: "ok",
  };
  if (pods.some(podFailing)) s.health = "error";
  else if (podsReady < pods.length) s.health = "warn";
  else if (pods.length + s.services + s.workloads + s.ingresses + s.configMaps + s.pvcs + s.others.length === 0) s.health = "empty";
  return s;
}

/** 非空 selector 的每個鍵值都出現在 labels 內才算符合（與 k8s 語意一致；空 selector 不匹配任何東西）。 */
export function matches(selector: Record<string, string> | undefined, labels: Record<string, string> | undefined): boolean {
  const entries = Object.entries(selector ?? {});
  if (entries.length === 0) return false;
  return entries.every(([k, v]) => labels?.[k] === v);
}

export interface Workload {
  kind: "Deployment" | "StatefulSet";
  name: string;
  /** 圖上的節點 id：`dep:<name>`／`sts:<name>` */
  id: string;
  ready: string;
  replicas: number;
  image?: string;
  selector?: Record<string, string>;
}

export interface GraphColumn {
  /** undefined = 不屬於任何 Deployment／StatefulSet 的 Pod（系統元件、Job…） */
  workload?: Workload;
  pods: K8sPod[];
}

export interface GraphEdge {
  /** 節點 id：`ing:`／`svc:`／`dep:`／`sts:`／`pods:<workload>`／`cm:`／`pvc:` 開頭 */
  from: string;
  to: string;
  label?: string;
  /** ConfigMap／PVC 掛載（畫成虛線） */
  mount?: boolean;
}

export interface MountNode {
  id: string;
  kind: "ConfigMap" | "PVC";
  name: string;
  /** 例如 "6 keys"、"20Gi · Bound" */
  detail: string;
}

export interface NamespaceGraph {
  ingresses: K8sIngress[];
  services: K8sService[];
  columns: GraphColumn[];
  mounts: MountNode[];
  edges: GraphEdge[];
}

/** Ingress →（規則）→ Service →（selector）→ Deployment／StatefulSet →（selector）→ Pod ←（掛載）← ConfigMap／PVC。
 *  同一個 Pod 只歸到第一個符合的 workload。 */
export function buildNamespaceGraph(ns: string, r: K8sResources): NamespaceGraph {
  const ingresses = inNs(r.ingresses, ns);
  const services = inNs(r.services, ns);
  const pods = inNs(r.pods, ns);
  const workloads: Workload[] = [
    ...inNs(r.deployments, ns).map((d): Workload => ({ kind: "Deployment", name: d.name, id: `dep:${d.name}`, ready: d.ready, replicas: d.replicas, image: d.image, selector: d.selector })),
    ...inNs(r.statefulsets, ns).map((d): Workload => ({ kind: "StatefulSet", name: d.name, id: `sts:${d.name}`, ready: d.ready, replicas: d.replicas, image: d.image, selector: d.selector })),
  ];

  const taken = new Set<string>();
  const columns: GraphColumn[] = workloads.map((w) => {
    const mine = pods.filter((p) => !taken.has(p.name) && matches(w.selector, p.labels));
    mine.forEach((p) => taken.add(p.name));
    return { workload: w, pods: mine };
  });
  const rest = pods.filter((p) => !taken.has(p.name));
  if (rest.length) columns.push({ pods: rest });

  const edges: GraphEdge[] = [];
  // Ingress → Service（同一個 Ingress 對同一個 Service 的多條規則合成一條線；指向不存在 Service 的規則不畫）
  for (const ing of ingresses) {
    const byService = new Map<string, string[]>();
    for (const rule of ing.rules) {
      if (!services.some((s) => s.name === rule.service)) continue;
      const labels = byService.get(rule.service) ?? [];
      labels.push(`${rule.host ?? ""}${rule.path}`);
      byService.set(rule.service, labels);
    }
    for (const [service, labels] of byService) edges.push({ from: `ing:${ing.name}`, to: `svc:${service}`, label: labels.join(", ") });
  }
  // Service → workload
  for (const s of services) {
    for (const c of columns) {
      const w = c.workload;
      if (!w) continue;
      // 有 Pod 時看 Pod 標籤（Service selector 可能含 template 才有的標籤）；沒有 Pod 時退回 workload selector
      const linked = c.pods.length ? c.pods.some((p) => matches(s.selector, p.labels)) : matches(s.selector, w.selector);
      if (linked) edges.push({ from: `svc:${s.name}`, to: w.id });
    }
  }
  // ConfigMap／PVC → 使用它的 workload 的 Pod 群組（沒人使用的也列出，只是不連線）
  const configmaps = inNs(r.configmaps, ns);
  const pvcs = inNs(r.pvcs, ns);
  const mounts: MountNode[] = [
    ...configmaps.map((c): MountNode => ({ id: `cm:${c.name}`, kind: "ConfigMap", name: c.name, detail: `${c.keys} keys` })),
    ...pvcs.map((p): MountNode => ({ id: `pvc:${p.name}`, kind: "PVC", name: p.name, detail: [p.size, p.status].filter(Boolean).join(" · ") })),
  ];
  for (const c of columns) {
    const target = `pods:${c.workload?.name ?? ""}`;
    const cms = new Set(c.pods.flatMap((p) => p.config_maps ?? []));
    const claims = new Set(c.pods.flatMap((p) => p.pvcs ?? []));
    for (const cm of configmaps) if (cms.has(cm.name)) edges.push({ from: `cm:${cm.name}`, to: target, mount: true });
    for (const pvc of pvcs) if (claims.has(pvc.name)) edges.push({ from: `pvc:${pvc.name}`, to: target, mount: true });
  }
  return { ingresses, services, columns, mounts, edges };
}

/** 依名稱找出 Pod 所屬的 workload 名稱（沒有回空字串）；Pods／Config／Storage 分頁的「Owner／Used by」欄用。 */
export function ownerOf(g: NamespaceGraph, pod: K8sPod): string {
  return g.columns.find((c) => c.pods.some((p) => p.name === pod.name))?.workload?.name ?? "";
}

/** 使用某個 ConfigMap 或 PVC 的 workload 名稱（去重）。 */
export function usedBy(g: NamespaceGraph, id: string): string[] {
  const out: string[] = [];
  for (const e of g.edges) {
    if (e.mount && e.from === id) {
      const name = e.to.slice("pods:".length);
      if (name && !out.includes(name)) out.push(name);
    }
  }
  return out;
}

/** 符合 Service selector 的 Pod 數（Endpoints 欄）。 */
export function endpointCount(s: K8sService, pods: K8sPod[]): number {
  return pods.filter((p) => p.namespace === s.namespace && matches(s.selector, p.labels)).length;
}

/** Service 指向的 workload 名稱（Services 分頁「→ target」）。 */
export function serviceTargets(g: NamespaceGraph, s: K8sService): string[] {
  return g.edges.filter((e) => e.from === `svc:${s.name}`).map((e) => e.to.slice(e.to.indexOf(":") + 1));
}
