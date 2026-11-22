// 與 internal/gui Server 溝通的唯一資料路徑（Wails 與測試主機共用）。

export interface Port {
  IP: string;
  PrivatePort: number;
  PublicPort: number;
  Type: string;
}

export interface Container {
  Id: string;
  Names: string[];
  Image: string;
  State: string;
  Status: string;
  Ports: Port[];
  Labels?: Record<string, string> | null;
}

export interface ContainerInfo {
  name: string;
  id: string;
  image: string;
  state: string;
  created: string;
  started: string;
  command: string;
  ports: string;
  networks: { name: string; ip: string }[];
  mounts: { source: string; dest: string; mode: string }[];
  labels: Record<string, string> | null;
  restart_policy: string;
}

export interface Image {
  Id: string;
  RepoTags: string[] | null;
  Created: number; // unix seconds
  Size: number;
  Containers: number; // -1 = daemon 未計算
}

export interface Volume {
  Name: string;
  Driver: string;
  Mountpoint: string;
  CreatedAt: string;
  Labels: Record<string, string> | null;
  Scope: string;
}

export interface Network {
  Id: string;
  Name: string;
  Created: string;
  Scope: string;
  Driver: string;
  Internal: boolean;
  Labels: Record<string, string> | null;
}

/** GET /api/disk：docker system df 摘要（各類 size／reclaimable，bytes）。 */
export interface DiskCategory {
  count: number;
  size: number;
  reclaimable: number;
  unknown?: number; // daemon 未統計大小的項目數（volume）
}

export interface DiskSummary {
  images: DiskCategory;
  containers: DiskCategory;
  volumes: DiskCategory;
  build_cache: DiskCategory;
  total: number;
  reclaimable: number;
}

export interface DiskInfo {
  summary: DiskSummary;
  containers: Record<string, { size_rw: number; size_root_fs: number }>;
  volumes: Record<string, { size: number; ref_count: number }>; // size = -1：未統計
}

export interface PruneReport {
  count: number;
  space_reclaimed: number;
}

export interface Machine {
  name: string;
  state: string;
  version: number;
  default: boolean;
  active: boolean;
  managed?: boolean;
  ready?: boolean;
  api_endpoint?: string;
  disk_bytes?: number; // ext4.vhdx 在 Windows 上的大小；取不到則省略
  disk_limit_bytes?: number; // 虛擬磁碟容量上限（VHDX 標頭）；取不到則省略
}

export interface MachinesResponse {
  available: boolean;
  machines: Machine[];
}

export interface MachineConfig {
  name: string;
  wsl_conf: string;
  /** 停止中（或調整磁碟中）不讀 wsl.conf：wsl -d 會把機器開起來 */
  wsl_conf_unavailable?: boolean;
  running?: boolean;
  systemd: boolean;
  api_endpoint?: string;
  user?: string;
  managed?: boolean;
  ready?: boolean;
  memory?: string;
  processors?: string;
  cpu_percent?: number;
  memory_used_mb?: number;
  memory_total_mb?: number;
  disk_bytes?: number;
  disk_used_bytes?: number; // 執行中：df /
  disk_total_bytes?: number;
  disk_limit_bytes?: number; // 虛擬磁碟容量上限
}

export interface InstallDefaults {
  user: string;
  vhd_size_gb?: number; // 新增機器預設磁碟上限（GB）
  vhd_min_gb?: number;
  vhd_max_gb?: number;
}

export interface OnlineDistro {
  name: string;
  friendly: string;
}

/** GET /api/machines/install/progress：新增機器（wsl --install）的進度。total=0 表示總大小未知。 */
export interface InstallProgress {
  active: boolean;
  distribution?: string;
  name?: string;
  phase?: "preparing" | "downloading" | "installing";
  downloaded: number;
  total: number;
  bytes_per_sec: number;
  elapsed_sec: number;
}

export interface Status {
  Distro: string;
  DistroFound: boolean;
  DistroRunning: boolean;
  SystemdActive: boolean;
  DockerActive: boolean;
  APIEndpoint: string;
  APIReachable: boolean;
  APIError: string;
  ready: boolean;
  starting: boolean;
}

export interface Settings {
  refresh_interval_sec: number;
  exec_shell: string;
  tray_autostart: boolean;
  lang?: string;
  theme?: string;
}

export interface Meta {
  version: string;
  lang: string;
  messages: Record<string, string>;
  config: Settings;
  distro: string;
  api_endpoint: string;
  active_ready?: boolean;
  autostart_supported: boolean;
  lan_supported?: boolean;
}

/** 一個區網轉發（內網 IP:埠 → 容器）。 */
export interface LANForward {
  address: string;
  port: number;
  container: string;
  error?: string;
}

/** 容器頁用的轉發清單（GET /api/lan/forwards）。 */
export interface LANForwards {
  enabled: boolean;
  forwards: LANForward[];
}

/** 區網存取狀態（GET/PUT /api/lan）。 */
export interface LANStatus {
  enabled: boolean;
  addresses: string[];
  forwards: LANForward[];
  /** WSL 仍是 0.9.0 留下的 mirrored 模式（需還原 NAT） */
  mirrored: boolean;
  /** wsld-gui.exe 的 Windows 防火牆對內授權（與區網存取開關無關；missing／blocked 時不在內網 IP 監聽） */
  firewall: "allowed" | "missing" | "blocked" | "unknown" | "";
  /** 授權時提權的程式：netsh（Windows 內建）或 helper（wsld-gui 自己，處理封鎖規則） */
  firewall_method?: "netsh" | "helper";
  /** 規則綁定的 wsld-gui.exe 路徑 */
  exe?: string;
  /** 正在等使用者回應防火牆的 UAC */
  firewall_pending?: boolean;
}

export type K8sState = "stopped" | "not-installed" | "off" | "starting" | "ready" | "error" | "unknown";

export interface K8sStatus {
  distro: string;
  state: K8sState;
  installed: boolean;
  active: boolean;
  ready: boolean;
  version?: string;
  node_name?: string;
  node_ready: boolean;
  node_ip?: string;
  ip_mismatch?: boolean;
  memory_bytes?: number;
  endpoint: string;
  context: string;
}

export type K8sStep = "engine" | "install" | "node" | "context";

export interface K8sDeployRequest {
  image: string;
  name: string;
  namespace?: string;
  port?: number;
  replicas?: number;
}

export interface K8sDeployResult {
  namespace: string;
  name: string;
  node_port?: number;
}

export interface K8sInfo {
  /** 目前開啟 k8s 的 machine；沒有人開啟時為按啟動會使用的 primary machine */
  machine: string;
  enabled: boolean;
  /** 背景作業進行中：starting | stopping | resetting；空 = 閒置 */
  op?: "starting" | "stopping" | "resetting";
  /** starting 作業目前所在步驟（steps 之一）；steps 是完整順序 */
  step?: K8sStep;
  steps: K8sStep[];
  /** 上一次作業失敗時所在步驟（與 error 同時出現） */
  error_step?: K8sStep;
  error?: string;
  warning?: string;
  status: K8sStatus;
  kubeconfig_path: string;
}

export interface K8sPod {
  name: string;
  namespace: string;
  status: string;
  ready: string;
  restarts: number;
  node?: string;
  ip?: string;
  labels?: Record<string, string>;
  /** 此 Pod 掛載或引用的 ConfigMap／PVC 名稱 */
  config_maps?: string[];
  pvcs?: string[];
  created_at: string;
}

export interface K8sDeployment {
  name: string;
  namespace: string;
  ready: string;
  replicas: number;
  up_to_date: number;
  available: number;
  selector?: Record<string, string>;
  image?: string;
  strategy?: string;
  max_surge?: string;
  max_unavailable?: string;
  created_at: string;
}

export interface K8sStatefulSet {
  name: string;
  namespace: string;
  ready: string;
  replicas: number;
  selector?: Record<string, string>;
  image?: string;
  created_at: string;
}

export interface K8sIngressRule {
  host?: string;
  path: string;
  service: string;
  port: string;
}

export interface K8sIngress {
  name: string;
  namespace: string;
  rules: K8sIngressRule[];
  created_at: string;
}

export interface K8sConfigMap {
  name: string;
  namespace: string;
  keys: number;
  created_at: string;
}

export interface K8sPVC {
  name: string;
  namespace: string;
  status: string;
  size?: string;
  storage_class?: string;
  created_at: string;
}

/** GUI 沒有專屬分頁的資源（DaemonSet／Job／CronJob／Lease／ServiceAccount／Secret／Role／RoleBinding）。 */
export interface K8sOther {
  kind: string;
  name: string;
  namespace: string;
  created_at: string;
}

export interface K8sExtras {
  statefulsets: K8sStatefulSet[];
  ingresses: K8sIngress[];
  configmaps: K8sConfigMap[];
  pvcs: K8sPVC[];
  others: K8sOther[];
}

export interface K8sRevision {
  revision: number;
  image: string;
  created_at: string;
  current: boolean;
}

export interface K8sEvent {
  type: string;
  reason: string;
  message: string;
  count: number;
  last_seen: string;
}

export interface K8sDeploymentDetail {
  revisions: K8sRevision[];
  events: K8sEvent[];
}

export interface K8sServicePort {
  name?: string;
  port: number;
  node_port?: number;
  protocol: string;
}

export interface K8sService {
  name: string;
  namespace: string;
  type: string;
  cluster_ip: string;
  ports: K8sServicePort[];
  selector?: Record<string, string>;
  created_at: string;
}

export type K8sKind = "pods" | "deployments" | "services";

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    /** 錯誤回應的完整 JSON（例如 resize 的 needs_shutdown 附帶 running） */
    public detail: Record<string, unknown> = {},
  ) {
    super(message);
  }
}

declare global {
  interface Window {
    __WSLD__?: { base: string; token: string };
  }
}

const cfg = window.__WSLD__ ?? { base: "", token: "" };

function headers(): Record<string, string> {
  const h: Record<string, string> = { "Content-Type": "application/json" };
  if (cfg.token) h["Authorization"] = `Bearer ${cfg.token}`;
  return h;
}

async function call<T>(method: string, path: string, body?: unknown, asText = false, signal?: AbortSignal): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(cfg.base + path, {
      method,
      headers: headers(),
      body: body === undefined ? undefined : typeof body === "string" ? body : JSON.stringify(body),
      signal,
    });
  } catch (e) {
    if (signal?.aborted || (e instanceof DOMException && e.name === "AbortError")) throw e;
    throw new ApiError(0, "network", String(e));
  }
  if (!resp.ok) {
    let msg = resp.statusText;
    let code = "error";
    let detail: Record<string, unknown> = {};
    try {
      const j = await resp.json();
      msg = j.error ?? msg;
      code = j.code ?? code;
      if (j && typeof j === "object") detail = j;
    } catch {
      /* ignore */
    }
    throw new ApiError(resp.status, code, msg, detail);
  }
  if (asText) return (await resp.text()) as unknown as T;
  if (resp.status === 204) return undefined as unknown as T;
  return (await resp.json()) as T;
}

export function wsUrl(path: string, params: Record<string, string | number> = {}): string {
  const base = cfg.base || window.location.origin;
  const u = new URL(path, base);
  for (const [k, v] of Object.entries(params)) u.searchParams.set(k, String(v));
  if (cfg.token) u.searchParams.set("token", cfg.token);
  u.protocol = u.protocol === "https:" ? "wss:" : "ws:";
  return u.toString();
}

function nsQuery(ns: string): string {
  return ns ? `?ns=${encodeURIComponent(ns)}` : "";
}

export const api = {
  meta: () => call<Meta>("GET", "/api/meta"),
  status: (signal?: AbortSignal) => call<Status>("GET", "/api/status", undefined, false, signal),
  startEngine: () => call<Status>("POST", "/api/engine/start"),
  containers: (signal?: AbortSignal) => call<Container[]>("GET", "/api/containers?all=1", undefined, false, signal),
  images: (signal?: AbortSignal) => call<Image[]>("GET", "/api/images", undefined, false, signal),
  removeImage: (ref: string, force = false) => call<void>("DELETE", `/api/images/${encodeURIComponent(ref)}${force ? "?force=1" : ""}`),
  pruneImages: () => call<PruneReport>("POST", "/api/images/prune"),
  disk: (signal?: AbortSignal) => call<DiskInfo>("GET", "/api/disk", undefined, false, signal),
  volumes: (signal?: AbortSignal) => call<Volume[]>("GET", "/api/volumes", undefined, false, signal),
  removeVolume: (name: string, force = false) => call<void>("DELETE", `/api/volumes/${encodeURIComponent(name)}${force ? "?force=1" : ""}`),
  pruneVolumes: () => call<PruneReport>("POST", "/api/volumes/prune"),
  networks: (signal?: AbortSignal) => call<Network[]>("GET", "/api/networks", undefined, false, signal),
  removeNetwork: (idOrName: string) => call<void>("DELETE", `/api/networks/${encodeURIComponent(idOrName)}`),
  pruneNetworks: () => call<PruneReport>("POST", "/api/networks/prune"),
  machines: (signal?: AbortSignal) => call<MachinesResponse>("GET", "/api/machines", undefined, false, signal),
  machineConfig: (name: string) => call<MachineConfig>("GET", `/api/machines/${encodeURIComponent(name)}/config`),
  machinesOnline: () => call<OnlineDistro[]>("GET", "/api/machines/online"),
  installProgress: () => call<InstallProgress>("GET", "/api/machines/install/progress"),
  installDefaults: () => call<InstallDefaults>("GET", "/api/machines/install/defaults"),
  /** vhdSizeGB：磁碟上限（GB）；0 = WSL 預設 */
  installMachine: (distribution: string, name: string, user: string, password: string, vhdSizeGB = 0) =>
    call<{ ok: boolean; name: string; distribution: string; user: string; password_set: boolean }>(
      "POST",
      "/api/machines/install",
      { distribution, name, user, password, vhd_size_gb: vhdSizeGB },
    ),
  /** 壓縮 VHDX，把不再使用的空間還給 Windows；shutdown 同 resizeMachine */
  compactMachine: (name: string, shutdown = false) =>
    call<{ ok: boolean; name: string; shutdown: boolean; restarted: string[]; before_bytes: number; after_bytes: number }>(
      "POST",
      `/api/machines/${encodeURIComponent(name)}/compact`,
      { shutdown },
    ),
  /** 執行中時估計檔案系統能縮到的最小上限（停止中不喚醒，min_gb = 0） */
  resizeInfo: (name: string) =>
    call<{ name: string; running: boolean; min_gb: number }>("GET", `/api/machines/${encodeURIComponent(name)}/resize-info`),
  /** shutdown：同意以 wsl --shutdown 釋放 VHD（所有機器會停止，完成後原本執行中的再啟動） */
  resizeMachine: (name: string, sizeGB: number, shutdown = false) =>
    call<{ ok: boolean; name: string; size_gb: number; shutdown: boolean; restarted: string[]; compacted?: boolean; before_bytes?: number; after_bytes?: number }>(
      "POST",
      `/api/machines/${encodeURIComponent(name)}/resize`,
      { size_gb: sizeGB, shutdown },
    ),
  useMachine: (name: string) => call<{ active: string; ready: boolean }>("POST", `/api/machines/${encodeURIComponent(name)}/use`),
  startMachine: (name: string) => call<void>("POST", `/api/machines/${encodeURIComponent(name)}/start`),
  stopMachine: (name: string) => call<void>("POST", `/api/machines/${encodeURIComponent(name)}/stop`),
  unmanageMachine: (name: string) => call<void>("POST", `/api/machines/${encodeURIComponent(name)}/unmanage`),
  unregisterMachine: (name: string, confirm: string) =>
    call<void>("POST", `/api/machines/${encodeURIComponent(name)}/unregister`, { confirm }),
  startContainer: (id: string) => call<void>("POST", `/api/containers/${encodeURIComponent(id)}/start`),
  stopContainer: (id: string) => call<void>("POST", `/api/containers/${encodeURIComponent(id)}/stop`),
  removeContainer: (id: string, force = true) =>
    call<void>("DELETE", `/api/containers/${encodeURIComponent(id)}${force ? "?force=1" : ""}`),
  containerInfo: (id: string) => call<ContainerInfo>("GET", `/api/containers/${encodeURIComponent(id)}`),
  logs: (id: string, tail = 100) =>
    call<string>("GET", `/api/containers/${encodeURIComponent(id)}/logs?tail=${tail}`, undefined, true),
  getSettings: () => call<Settings>("GET", "/api/settings"),
  putSettings: (s: Settings) => call<Settings>("PUT", "/api/settings", s),
  getLAN: () => call<LANStatus>("GET", "/api/lan"),
  putLAN: (enabled: boolean) => call<LANStatus>("PUT", "/api/lan", { enabled }),
  restoreNAT: () => call<LANStatus>("POST", "/api/lan/restore-nat"),
  allowFirewall: () => call<LANStatus>("POST", "/api/lan/firewall"),
  lanForwards: (signal?: AbortSignal) => call<LANForwards>("GET", "/api/lan/forwards", undefined, false, signal),
  openLAN: (url: string) => call<void>("POST", "/api/lan/open", { url }),
  openLocal: (port: number) => call<{ url: string }>("POST", "/api/open-local", { port }),
  window: (action: "hide" | "show" | "quit") => call<void>("POST", `/api/window/${action}`),
  distroAction: (action: "open-folder" | "open-terminal") => call<void>("POST", `/api/distro/${action}`),
  openRepo: () => call<{ url: string }>("POST", "/api/open-repo"),
  k8s: (signal?: AbortSignal) => call<K8sInfo>("GET", "/api/k8s", undefined, false, signal),
  k8sOn: (opts: { machine?: string; switch?: boolean; addContext?: boolean } = {}) => {
    const q = new URLSearchParams();
    if (opts.addContext) q.set("add_context", "1");
    if (opts.machine) q.set("machine", opts.machine);
    if (opts.switch) q.set("switch", "1");
    const qs = q.toString();
    return call<K8sInfo>("POST", `/api/k8s/on${qs ? `?${qs}` : ""}`);
  },
  k8sOff: () => call<K8sInfo>("POST", "/api/k8s/off"),
  k8sReset: () => call<K8sInfo>("POST", "/api/k8s/reset"),
  k8sKubeconfig: () => call<{ path: string; context: string }>("POST", "/api/k8s/kubeconfig"),
  k8sNamespaces: (signal?: AbortSignal) => call<string[]>("GET", "/api/k8s/namespaces", undefined, false, signal),
  k8sPods: (ns: string, signal?: AbortSignal) => call<K8sPod[]>("GET", `/api/k8s/pods${nsQuery(ns)}`, undefined, false, signal),
  k8sDeployments: (ns: string, signal?: AbortSignal) => call<K8sDeployment[]>("GET", `/api/k8s/deployments${nsQuery(ns)}`, undefined, false, signal),
  k8sServices: (ns: string, signal?: AbortSignal) => call<K8sService[]>("GET", `/api/k8s/services${nsQuery(ns)}`, undefined, false, signal),
  k8sExtras: (ns: string, signal?: AbortSignal) => call<K8sExtras>("GET", `/api/k8s/extras${nsQuery(ns)}`, undefined, false, signal),
  k8sDeploymentDetail: (ns: string, name: string, signal?: AbortSignal) =>
    call<K8sDeploymentDetail>("GET", `/api/k8s/deployments/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/detail`, undefined, false, signal),
  k8sDelete: (kind: K8sKind, ns: string, name: string) =>
    call<void>("DELETE", `/api/k8s/${kind}/${encodeURIComponent(ns)}/${encodeURIComponent(name)}`),
  k8sRestart: (ns: string, name: string) =>
    call<void>("POST", `/api/k8s/deployments/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/restart`),
  k8sScale: (ns: string, name: string, replicas: number) =>
    call<void>("POST", `/api/k8s/deployments/${encodeURIComponent(ns)}/${encodeURIComponent(name)}/scale`, { replicas }),
  k8sDeploy: (req: K8sDeployRequest) => call<K8sDeployResult>("POST", "/api/k8s/deploy", req),
  k8sApply: (manifest: string) => call<{ output: string }>("POST", "/api/k8s/apply", manifest),
  k8sOpen: (port: number) => call<{ url: string }>("POST", "/api/k8s/open", { port }),
  k8sLogsUrl: (ns: string, pod: string, tail = 100) =>
    wsUrl(`/api/k8s/pods/${encodeURIComponent(ns)}/${encodeURIComponent(pod)}/logs/stream`, { tail }),
  k8sExecUrl: (ns: string, pod: string, cols: number, rows: number) =>
    wsUrl(`/api/k8s/pods/${encodeURIComponent(ns)}/${encodeURIComponent(pod)}/exec`, { cols, rows }),
  logsStreamUrl: (id: string, tail = 100) =>
    wsUrl(`/api/containers/${encodeURIComponent(id)}/logs/stream`, { tail }),
  execUrl: (id: string, cols: number, rows: number) =>
    wsUrl(`/api/containers/${encodeURIComponent(id)}/exec`, { cols, rows }),
  machineExecUrl: (name: string, cols: number, rows: number) =>
    wsUrl(`/api/machines/${encodeURIComponent(name)}/exec`, { cols, rows }),
};
