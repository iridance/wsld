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
}

export interface MachinesResponse {
  available: boolean;
  machines: Machine[];
}

export interface MachineConfig {
  name: string;
  wsl_conf: string;
  systemd: boolean;
}

export interface OnlineDistro {
  name: string;
  friendly: string;
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
  lang?: string; // en | zh-TW
}

export interface Meta {
  version: string;
  lang: string;
  messages: Record<string, string>;
  config: Settings;
  distro: string;
  api_endpoint: string;
  autostart_supported: boolean;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
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

async function call<T>(method: string, path: string, body?: unknown, asText = false): Promise<T> {
  let resp: Response;
  try {
    resp = await fetch(cfg.base + path, {
      method,
      headers: headers(),
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (e) {
    throw new ApiError(0, "network", String(e));
  }
  if (!resp.ok) {
    let msg = resp.statusText;
    let code = "error";
    try {
      const j = await resp.json();
      msg = j.error ?? msg;
      code = j.code ?? code;
    } catch {
      /* ignore */
    }
    throw new ApiError(resp.status, code, msg);
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

export const api = {
  meta: () => call<Meta>("GET", "/api/meta"),
  status: () => call<Status>("GET", "/api/status"),
  startEngine: () => call<Status>("POST", "/api/engine/start"),
  containers: () => call<Container[]>("GET", "/api/containers?all=1"),
  images: () => call<Image[]>("GET", "/api/images"),
  removeImage: (ref: string, force = false) => call<void>("DELETE", `/api/images/${encodeURIComponent(ref)}${force ? "?force=1" : ""}`),
  pruneImages: () => call<PruneReport>("POST", "/api/images/prune"),
  volumes: () => call<Volume[]>("GET", "/api/volumes"),
  removeVolume: (name: string, force = false) => call<void>("DELETE", `/api/volumes/${encodeURIComponent(name)}${force ? "?force=1" : ""}`),
  pruneVolumes: () => call<PruneReport>("POST", "/api/volumes/prune"),
  networks: () => call<Network[]>("GET", "/api/networks"),
  machines: () => call<MachinesResponse>("GET", "/api/machines"),
  machineConfig: (name: string) => call<MachineConfig>("GET", `/api/machines/${encodeURIComponent(name)}/config`),
  machinesOnline: () => call<OnlineDistro[]>("GET", "/api/machines/online"),
  installMachine: (name: string) => call<{ ok: boolean; name: string }>("POST", "/api/machines/install", { name }),
  startContainer: (id: string) => call<void>("POST", `/api/containers/${encodeURIComponent(id)}/start`),
  stopContainer: (id: string) => call<void>("POST", `/api/containers/${encodeURIComponent(id)}/stop`),
  logs: (id: string, tail = 100) =>
    call<string>("GET", `/api/containers/${encodeURIComponent(id)}/logs?tail=${tail}`, undefined, true),
  getSettings: () => call<Settings>("GET", "/api/settings"),
  putSettings: (s: Settings) => call<Settings>("PUT", "/api/settings", s),
  window: (action: "hide" | "show" | "quit") => call<void>("POST", `/api/window/${action}`),
  distroAction: (action: "open-folder" | "open-terminal") => call<void>("POST", `/api/distro/${action}`),
  logsStreamUrl: (id: string, tail = 100) =>
    wsUrl(`/api/containers/${encodeURIComponent(id)}/logs/stream`, { tail }),
  execUrl: (id: string, cols: number, rows: number) =>
    wsUrl(`/api/containers/${encodeURIComponent(id)}/exec`, { cols, rows }),
};
