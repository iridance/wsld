import type { K8sDeployment, K8sPod, K8sService } from "./api";
import { isSystemNs, matches, nodePortsOf, type K8sResources } from "./k8sGraph";

/** App 列上的一個可點網址（來自 LoadBalancer 或 NodePort Service）。 */
export interface AppUrl {
  port: number;
  href: string; // http://localhost:8080
  label: string; // localhost:8080
  kind: "loadbalancer" | "nodeport";
}

export type AppStatus = "running" | "pending" | "error" | "stopped";

/**
 * 一個 app：一個 Deployment＋指向它的 Services＋它的 Pods 合成一列。
 * 使用者面對「app 與網址」，不必先分辨 Deployment／Service／Pod。
 */
export interface App {
  namespace: string;
  name: string;
  image: string;
  deployment: K8sDeployment;
  pods: K8sPod[];
  services: K8sService[];
  urls: AppUrl[];
  status: AppStatus;
  /** 非 running 時的原因（Pod 狀態，如 CrashLoopBackOff）；running 為空 */
  reason: string;
  /** 例如 "2/2" */
  ready: string;
}

const FAILING = new Set(["CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "Error", "CreateContainerConfigError", "InvalidImageName", "OOMKilled", "Evicted"]);

/** localhost 網址（由後端以預設瀏覽器開啟 http://localhost:<port>）。 */
function urlFor(port: number, kind: AppUrl["kind"]): AppUrl {
  return { port, href: `http://localhost:${port}`, label: `localhost:${port}`, kind };
}

/** 一個 Service 從 Windows 能開的網址（TCP；ClusterIP 與 ExternalName 沒有）。 */
export function serviceUrls(s: K8sService): AppUrl[] {
  const tcp = (p: { protocol: string }) => p.protocol === "" || p.protocol === "TCP";
  if (s.type === "LoadBalancer") return s.ports.filter(tcp).map((p) => urlFor(p.port, "loadbalancer"));
  if (s.type === "NodePort") return nodePortsOf(s).map((p) => urlFor(p, "nodeport"));
  return [];
}

function statusOf(d: K8sDeployment, pods: K8sPod[]): { status: AppStatus; reason: string } {
  if (d.replicas === 0) return { status: "stopped", reason: "" };
  const bad = pods.find((p) => FAILING.has(p.status) || p.status.startsWith("Init:Error"));
  if (bad) return { status: "error", reason: bad.status };
  if (d.available >= d.replicas) return { status: "running", reason: "" };
  return { status: "pending", reason: pods.find((p) => p.status !== "Running")?.status ?? "" };
}

/**
 * 由全部資源組出 apps（以 Deployment 為單位）。
 * Service 屬於 app 的條件：selector 符合其 Pod 的 labels（沒有 Pod 時改比對 Deployment 的 selector）。
 * showSystem = false 時略過系統 namespace（kube-*）。
 */
export function buildApps(res: K8sResources, showSystem: boolean): App[] {
  const apps: App[] = [];
  for (const d of res.deployments) {
    if (!showSystem && isSystemNs(d.namespace)) continue;
    const pods = res.pods.filter((p) => p.namespace === d.namespace && matches(d.selector, p.labels));
    const services = res.services.filter((s) => {
      if (s.namespace !== d.namespace || !s.selector || Object.keys(s.selector).length === 0) return false;
      return pods.length ? pods.some((p) => matches(s.selector, p.labels)) : matches(s.selector, d.selector);
    });
    const urls = services.flatMap(serviceUrls);
    const { status, reason } = statusOf(d, pods);
    apps.push({ namespace: d.namespace, name: d.name, image: d.image ?? "", deployment: d, pods, services, urls, status, reason, ready: d.ready });
  }
  // 使用者的 app 在前（非系統 namespace），再依 namespace、名稱排序
  return apps.sort((a, b) => Number(isSystemNs(a.namespace)) - Number(isSystemNs(b.namespace)) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name));
}
