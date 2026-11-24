import {
  api,
  ApiError,
  type ComposePreview,
  type Container,
  type FileEntry,
  type FileSources,
  type DiskInfo,
  type InstallDefaults,
  type MachineConfig,
  type Image,
  type InstallProgress,
  type K8sDeployment,
  type K8sInfo,
  type K8sStep,
  type K8sPod,
  type K8sService,
  type K8sExtras,
  type K8sDeploymentDetail,
  type LANForward,
  type LANStatus,
  type MachinesResponse,
  type Meta,
  type Network,
  type Settings,
  type Status,
  type Volume,
} from "./api";
import { setMessages, t } from "./i18n";
import { openTerminal, type TerminalSession } from "./terminal";
import { brandMark, icon, type IconName } from "./icons";
import { distroLogo } from "./distroLogos";
import { buildApps, type App as K8sApp } from "./k8sApps";
import { buildNamespaceGraph, endpointCount, isSystemNs, nodePortsOf, ownerOf, serviceTargets, summarizeNamespace, usedBy, type K8sResources } from "./k8sGraph";

/** k8s：Kubernetes 頁的 Pod 面板（logs／exec 走 /api/k8s/pods/...），其餘為容器／machine 面板。 */
type PanelK8s = { ns: string; pod: string };
type Panel =
  | { kind: "logs"; id: string; name: string; live: boolean; ws?: WebSocket; k8s?: PanelK8s }
  | { kind: "term"; id: string; name: string; session?: TerminalSession; k8s?: PanelK8s };
type InfoDrawer =
  | { kind: "container"; id: string; name: string; detail?: import("./api").ContainerInfo }
  | { kind: "compose"; project: string }
  | { kind: "machine"; name: string; detail?: import("./api").MachineConfig };

/** 側欄視圖。 */
export type View = "containers" | "images" | "volumes" | "networks" | "machines" | "kubernetes" | "settings";
type K8sTab = "architecture" | "pods" | "workloads" | "services" | "config" | "storage" | "others";
type K8sGroup = "workloads" | "system";
const K8S_TABS: K8sTab[] = ["architecture", "pods", "workloads", "services", "config", "storage", "others"];
type CtFilter = "all" | "running" | "stopped";
type ImgKind = "inuse" | "unused" | "dangling" | "unknown";
type ImgFilter = "all" | "inuse" | "unused" | "dangling";
const IMG_FILTERS: { id: ImgFilter; label: string }[] = [
  { id: "all", label: "gui.filter.all" },
  { id: "inuse", label: "gui.filter.in_use" },
  { id: "unused", label: "gui.filter.unused" },
  { id: "dangling", label: "gui.filter.dangling" },
];
const CT_FILTERS: { id: CtFilter; label: string }[] = [
  { id: "all", label: "gui.filter.all" },
  { id: "running", label: "gui.filter.running" },
  { id: "stopped", label: "gui.filter.stopped" },
];

/** section：側欄分組（resources＝Docker 資源、system＝WSL 機器、foot＝底部設定）。 */
const VIEWS: { id: View; icon: IconName; label: string; section: "resources" | "system" | "foot" }[] = [
  { id: "containers", icon: "containers", label: "gui.nav.containers", section: "resources" },
  { id: "images", icon: "images", label: "gui.nav.images", section: "resources" },
  { id: "volumes", icon: "volumes", label: "gui.nav.volumes", section: "resources" },
  { id: "networks", icon: "networks", label: "gui.nav.networks", section: "resources" },
  { id: "machines", icon: "machines", label: "gui.nav.machines", section: "system" },
  { id: "kubernetes", icon: "kubernetes", label: "gui.nav.kubernetes", section: "system" },
  { id: "settings", icon: "settings", label: "gui.nav.settings", section: "foot" },
];

interface State {
  meta: Meta;
  status?: Status;
  view: View;
  containers: Container[];
  images: Image[];
  volumes: Volume[];
  networks: Network[];
  machines?: MachinesResponse;
  /** docker system df（容器／映像／volume 頁一起查；失敗時保留上一次） */
  disk?: DiskInfo;
  lastError?: string;
  /** 最近一次「啟動引擎／設為主要」失敗的原因；引擎就緒前輪詢不覆蓋，避免真正原因被「無法連線 API」蓋掉 */
  bringUpError?: string;
  notice?: string;
  /** 通知不自動消失（長時間作業的結果，使用者可能已切走）；需按 ✕ 關閉 */
  noticeSticky?: boolean;
  starting: boolean;
  /** 已完成首次載入的頁面；未載入前顯示對齊資料版面的 loading 佔位，載入後才切成實際內容／空狀態 */
  loaded: Set<View>;
  /** 後端自行帶起引擎（程式啟動自動連線）中：畫面同樣顯示 Starting… */
  autoStarting?: boolean;
  /** 正在切換的 target distro 名稱；有值時 header 顯示 Loading／Switching */
  paused: boolean;
  refreshCount: number;
  panel?: Panel;
  info?: InfoDrawer;
  /** 進行中的容器動作（id → 動作），用於即時回饋；docker stop 可能等 10s 寬限期 */
  pending: Record<string, "starting" | "stopping">;
  /** 容器列表篩選：狀態分段＋名稱／映像搜尋（僅前端，不影響 API） */
  ctFilter: CtFilter;
  ctQuery: string;
  /** 區網存取狀態（進入設定頁時查詢一次；undefined = 尚未查詢） */
  lan?: LANStatus;
  lanLoading?: boolean;
  /** 套用中：toggle＝儲存並同步轉發；restore＝還原 NAT（等待 UAC／WSL 重啟） */
  lanApplying?: "toggle" | "restore" | "firewall";
  /** 容器頁的區網轉發（隨容器清單刷新；未開啟區網存取時為空） */
  lanForwards: LANForward[];
  /** 區網存取已開啟（容器列顯示瀏覽器按鈕） */
  lanEnabled: boolean;
  imgFilter: ImgFilter;
  /** 頁標題旁磁碟用量鈕的明細是否展開（換頁即收起） */
  usageOpen: boolean;
  /** 進行中的資源動作（images/volumes 刪除、prune、machine install） */
  busy: Set<string>;
  /** Kubernetes 頁：/api/k8s 狀態與目前分頁的資源列表（k3s 就緒時才載入） */
  k8s?: K8sInfo;
  k8sTab: K8sTab;
  /** 空字串 = 所有 namespace */
  k8sNs: string;
  k8sNamespaces: string[];
  k8sPods: K8sPod[];
  k8sDeployments: K8sDeployment[];
  k8sServices: K8sService[];
  k8sExtras: K8sExtras;
  /** 側欄的 Deployment 詳情（點 Deployment 進入）與其 rollout 歷史／事件 */
  k8sDetail?: { ns: string; name: string };
  k8sDetailData?: K8sDeploymentDetail;
  k8sPodQuery: string;
  k8sPodFilter: "" | "notready";
}

/** SVG 元素建立（命名空間不同於 HTML）。 */
const svgEl = (tag: string, attrs: Record<string, string> = {}): SVGElement => {
  const n = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(attrs)) n.setAttribute(k, v);
  return n;
};

const el = <K extends keyof HTMLElementTagNameMap>(tag: K, attrs: Record<string, string> = {}, ...children: (Node | string)[]) => {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (k === "text") e.textContent = v;
    else e.setAttribute(k, v);
  }
  e.append(...children);
  return e;
};

/** 帶語義圖示的按鈕：<button><svg/><span class="label">text</span></button> */
const btn = (attrs: Record<string, string>, ico: IconName, label: string): HTMLButtonElement => {
  const { text: _ignored, ...rest } = attrs;
  const b = el("button", rest);
  b.append(icon(ico), el("span", { class: "label", text: label }));
  return b;
};

/** 共用 tab 元件（樣式對齊頂部 nav：灰字、hover 底色、選取時粗體＋accent 底線）。
 *  每個 tab：id（組 data-testid `${testid}-${id}`）、label、可選 icon／count 膠囊／警示紅點／disabled。
 *  選取以 aria-selected 表示；點擊由 onSelect 處理（重複點選中的 tab 也會觸發，由呼叫端決定要不要忽略）。 */
interface TabSpec { id: string; label: string; icon?: IconName; count?: number; dot?: string; disabled?: boolean; testid?: string }
const tabs = (opts: { testid: string; selected: string; items: TabSpec[]; onSelect: (id: string) => void; bordered?: boolean; label?: string }): HTMLElement => {
  const bar = el("div", { class: `ui-tabs${opts.bordered ? " bordered" : ""}`, role: "tablist", "data-testid": opts.testid });
  if (opts.label) bar.setAttribute("aria-label", opts.label);
  for (const it of opts.items) {
    const b = el("button", { type: "button", role: "tab", class: "ui-tab", "data-testid": it.testid ?? `${opts.testid}-${it.id}`, "aria-selected": String(it.id === opts.selected) });
    if (it.icon) b.append(icon(it.icon));
    b.append(el("span", { class: "label", text: it.label }));
    if (it.count !== undefined) b.append(el("span", { class: "ui-tab-count", text: String(it.count) }));
    if (it.dot) b.append(el("span", { class: "ui-tab-dot", "data-testid": `${it.testid ?? `${opts.testid}-${it.id}`}-dot`, title: it.dot }));
    b.disabled = !!it.disabled;
    b.onclick = () => opts.onSelect(it.id);
    bar.append(b);
  }
  return bar;
};

/** 只有圖示的按鈕：名稱交給 aria-label／title（列表操作、工具列）。 */
const iconBtn = (attrs: Record<string, string>, ico: IconName, label: string): HTMLButtonElement => {
  const b = el("button", { ...attrs, class: `icon ${attrs.class ?? ""}`.trim(), "aria-label": label, title: label });
  b.append(icon(ico));
  return b;
};

/** 文字欄位外框（設計稿「Input controls」）：邊框／焦點底線／錯誤／停用由 .tf 處理，內層 input 無框。 */
const textField = (input: HTMLInputElement, ...extra: (Node | string)[]): HTMLElement => {
  const f = el("span", { class: "tf" }, input, ...extra);
  if (input.classList.contains("mono")) f.classList.add("mono");
  return f;
};

/** 自繪下拉（設計稿「Input controls」ComboBox）：外框同 .tf.select，展開清單也自繪，不用 OS 原生彈出清單。
 *  按鈕 role=combobox、目前值在 data-value；清單 role=listbox 以 fixed 定位（不被捲動容器裁切），下方空間不足時往上開。
 *  鍵盤：Enter／Space／↑↓ 展開，↑↓／Home／End 移動，Enter／Space 選取，Esc／Tab 關閉。
 *  目前值不在選項內時顯示 placeholder（淡色）；setOptions 可換選項（例如非同步載入），setDisabled 停用。
 *  注意：不要包在 <label> 裡（點清單項目會被 label 轉成再點一次按鈕），標題用 aria-labelledby。 */
type ImportDialog = HTMLDialogElement & { begin(): Promise<void> };
type DropdownOption = readonly [value: string, label: string];
interface Dropdown {
  el: HTMLElement;
  button: HTMLButtonElement;
  readonly value: string;
  setOptions(options: readonly DropdownOption[], value?: string, placeholder?: string): void;
  setDisabled(disabled: boolean): void;
}
const dropdown = (
  attrs: { testid: string; id?: string; labelledby?: string; placeholder?: string },
  initial: readonly DropdownOption[],
  value: string,
  onChange: (v: string) => void,
): Dropdown => {
  const trigger = el("button", {
    type: "button",
    role: "combobox",
    "aria-haspopup": "listbox",
    "aria-expanded": "false",
    "data-testid": attrs.testid,
    ...(attrs.id ? { id: attrs.id } : {}),
    ...(attrs.labelledby ? { "aria-labelledby": attrs.labelledby } : {}),
  }) as HTMLButtonElement;
  const text = el("span", { class: "dd-value" });
  const chev = icon("chevron-down");
  chev.classList.add("chev");
  trigger.append(text, chev);
  const list = el("ul", { role: "listbox", class: "dd-list", tabindex: "-1", "data-testid": `${attrs.testid}-list` });
  list.hidden = true;
  const listId = `${attrs.testid}-listbox`;
  list.id = listId;
  trigger.setAttribute("aria-controls", listId);
  const field = el("span", { class: "tf select dd" }, trigger, list);

  let options: readonly DropdownOption[] = [];
  let items: HTMLLIElement[] = [];
  let placeholder = attrs.placeholder ?? "";
  let current = value;
  let active = -1;

  const sync = () => {
    const i = options.findIndex(([v]) => v === current);
    text.textContent = i >= 0 ? options[i][1] : placeholder;
    text.classList.toggle("placeholder", i < 0);
    trigger.dataset.value = i >= 0 ? current : "";
    items.forEach((li, j) => li.setAttribute("aria-selected", String(j === i)));
  };
  const setActive = (i: number) => {
    if (!items.length) return;
    active = Math.max(0, Math.min(items.length - 1, i));
    items.forEach((li, j) => li.classList.toggle("active", j === active));
    trigger.setAttribute("aria-activedescendant", items[active].id);
    items[active].scrollIntoView({ block: "nearest" });
  };
  const place = () => {
    const r = field.getBoundingClientRect();
    const below = window.innerHeight - r.bottom - 12;
    const above = r.top - 12;
    list.style.minWidth = `${r.width}px`;
    list.style.left = `${r.left}px`;
    list.style.maxHeight = "";
    const h = list.offsetHeight;
    const up = h > below && above > below;
    list.style.maxHeight = `${Math.min(280, Math.max(up ? above : below, 96))}px`;
    list.style.top = up ? "" : `${r.bottom + 4}px`;
    list.style.bottom = up ? `${window.innerHeight - r.top + 4}px` : "";
  };
  const onOutside = (e: Event) => {
    if (!field.contains(e.target as Node)) close();
  };
  const onViewport = (e: Event) => {
    if (e.target !== list) close(); // 清單自己捲動不關
  };
  const open = () => {
    if (!list.hidden || trigger.disabled || !items.length) return;
    list.hidden = false;
    trigger.setAttribute("aria-expanded", "true");
    field.classList.add("open");
    place();
    setActive(Math.max(0, options.findIndex(([v]) => v === current)));
    document.addEventListener("mousedown", onOutside, true);
    window.addEventListener("resize", onViewport);
    window.addEventListener("scroll", onViewport, true);
  };
  const close = () => {
    if (list.hidden) return;
    list.hidden = true;
    trigger.setAttribute("aria-expanded", "false");
    trigger.removeAttribute("aria-activedescendant");
    field.classList.remove("open");
    document.removeEventListener("mousedown", onOutside, true);
    window.removeEventListener("resize", onViewport);
    window.removeEventListener("scroll", onViewport, true);
  };
  const pick = (i: number) => {
    close();
    const v = options[i]?.[0];
    if (v === undefined || v === current) return;
    current = v;
    sync();
    onChange(v);
  };
  const setOptions = (next: readonly DropdownOption[], v = current, ph?: string) => {
    close();
    options = next;
    if (ph !== undefined) placeholder = ph;
    current = v;
    items = options.map(([ov, label], i) => {
      const li = el("li", { role: "option", id: `${listId}-${i}`, "data-value": ov, "data-testid": `${attrs.testid}-option-${ov}` }) as HTMLLIElement;
      const check = icon("check");
      check.classList.add("dd-check");
      li.append(el("span", { text: label }), check);
      li.onmousemove = () => setActive(i);
      li.onmousedown = (e) => e.preventDefault(); // 不讓按鈕失焦
      li.onclick = () => pick(i);
      return li;
    });
    list.replaceChildren(...items);
    sync();
  };
  trigger.onclick = () => (list.hidden ? open() : close());
  trigger.onblur = () => close();
  trigger.onkeydown = (e) => {
    const shut = list.hidden;
    switch (e.key) {
      case "ArrowDown":
      case "ArrowUp":
        e.preventDefault();
        if (shut) open();
        else setActive(active + (e.key === "ArrowDown" ? 1 : -1));
        break;
      case "Home":
      case "End":
        if (shut) return;
        e.preventDefault();
        setActive(e.key === "Home" ? 0 : items.length - 1);
        break;
      case "Enter":
      case " ":
        e.preventDefault();
        if (shut) open();
        else pick(active);
        break;
      case "Escape":
        if (shut) return; // 關著時交給外層（例如關對話框）
        e.preventDefault();
        e.stopPropagation();
        close();
        break;
      case "Tab":
        close();
        break;
    }
  };
  setOptions(initial, value);
  return {
    el: field,
    button: trigger,
    get value() {
      return trigger.dataset.value ?? "";
    },
    setOptions,
    setDisabled(disabled: boolean) {
      if (disabled) close();
      trigger.disabled = disabled;
    },
  };
};

/** 數字：隱藏原生 spinner，改為欄內 −／＋ 與單位；超出 min／max 標成錯誤。 */
const numberField = (input: HTMLInputElement, unit: string): HTMLElement => {
  const f = el("span", { class: "tf num" });
  const step = (dir: 1 | -1) => {
    const label = t(dir > 0 ? "gui.increase" : "gui.decrease");
    const b = el("button", { type: "button", class: "tf-btn", tabindex: "-1", "aria-label": label, title: label, "data-testid": `${input.dataset.testid ?? "num"}-${dir > 0 ? "inc" : "dec"}` }) as HTMLButtonElement;
    b.append(icon(dir > 0 ? "plus" : "minus"));
    b.onclick = () => {
      if (input.value === "" || !Number.isFinite(Number(input.value))) input.value = input.min || "0";
      else if (dir > 0) input.stepUp();
      else input.stepDown();
      input.dispatchEvent(new Event("input", { bubbles: true }));
    };
    return b;
  };
  const dec = step(-1);
  const inc = step(1);
  const sync = () => {
    const v = Number(input.value);
    dec.disabled = input.disabled || (input.min !== "" && v <= Number(input.min));
    inc.disabled = input.disabled || (input.max !== "" && v >= Number(input.max));
    f.classList.toggle("is-error", !input.checkValidity());
  };
  input.addEventListener("input", sync);
  f.append(input, el("span", { class: "suffix", "aria-hidden": "true", text: unit }), el("span", { class: "stepper" }, dec, inc));
  sync();
  return f;
};

export class App {
  private st: State;
  private timer?: number;
  private noticeTimer?: number;
  private usageTimer?: number;
  private refreshAbort?: AbortController;
  /** 有請求還在飛：定時輪詢不得打斷它（wsl.exe 慢於輪詢間隔時，打斷＝每次都被殺掉、永遠等不到結果）。 */
  private refreshInFlight = false;
  private refreshGen = 0;
  private root: HTMLElement;

  constructor(root: HTMLElement, meta: Meta) {
    this.root = root;
    setMessages(meta.messages);
    document.documentElement.lang = meta.lang;
    document.title = t("gui.title");
    this.st = {
      meta,
      view: loadJSON<View>(LS_VIEW, "containers"),
      containers: [],
      images: [],
      volumes: [],
      networks: [],
      starting: false,
      loaded: new Set<View>(),
      paused: false,
      refreshCount: 0,
      pending: {},
      busy: new Set(),
      ctFilter: "all",
      ctQuery: "",
      imgFilter: "all",
      usageOpen: false,
      lanForwards: [],
      lanEnabled: false,
      k8sTab: normalizeK8sTab(loadJSON<string>(LS_K8S_TAB, "architecture")),
      k8sNs: "",
      k8sNamespaces: [],
      k8sPods: [],
      k8sDeployments: [],
      k8sServices: [],
      k8sExtras: emptyK8sExtras(),
      k8sPodQuery: "",
      k8sPodFilter: "",
    };
    if (!VIEWS.some((v) => v.id === this.st.view)) this.st.view = "containers";
    this.build();
    this.applyTheme(this.st.meta.config.theme || "system");
    this.bindPause();
    this.bindUsageDismiss();
    void this.refresh();
    this.schedule();
    // 測試掛鉤
    (window as unknown as { __wsld: unknown }).__wsld = {
      state: () => this.st,
      refresh: () => this.refresh(),
      termText: () => (this.st.panel?.kind === "term" ? this.st.panel.session?.text() ?? "" : ""),
    };
  }

  // ---- 刷新 -----------------------------------------------------------------

  private schedule(): void {
    if (this.timer) window.clearInterval(this.timer);
    const sec = Math.max(1, this.st.meta.config.refresh_interval_sec || 3);
    this.timer = window.setInterval(() => {
      if (!this.st.paused && this.st.busy.size === 0 && !this.refreshInFlight) void this.refresh();
    }, sec * 1000);
  }

  private bindPause(): void {
    const update = () => {
      const paused = document.hidden || !document.hasFocus();
      if (paused !== this.st.paused) {
        this.st.paused = paused;
        this.renderHeader();
        if (!paused) void this.refresh();
      }
    };
    window.addEventListener("blur", update);
    window.addEventListener("focus", update);
    document.addEventListener("visibilitychange", update);
  }

  async refresh(): Promise<void> {
    this.refreshAbort?.abort();
    const ac = new AbortController();
    this.refreshAbort = ac;
    const gen = ++this.refreshGen;
    const { signal } = ac;
    this.refreshInFlight = true;
    try {
      // Kubernetes 頁不依賴 Docker 引擎狀態：與 /api/k8s 並行，不讓兩段 wsl.exe 查詢串行相加
      const k8sLoad = this.st.view === "kubernetes" ? this.loadKubernetes(signal) : undefined;
      k8sLoad?.catch(() => {}); // 錯誤留給下面 await 處理，避免 status 先拋錯時成為未處理的 rejection
      const status = await api.status(signal);
      if (signal.aborted || gen !== this.refreshGen) return;
      this.st.status = status;
      // 自動帶起 engine 的失敗原因（後端 Supervise 記錄）；就緒後清除
      this.st.bringUpError = status.ready ? undefined : status.start_error;
      if (status.starting && !this.st.starting) {
        this.st.starting = this.st.autoStarting = true;
        this.renderHeader();
      } else if (!status.starting && this.st.autoStarting) {
        this.st.starting = this.st.autoStarting = false;
        this.st.meta = await api.meta();
        if (signal.aborted || gen !== this.refreshGen) return;
      }
      if (this.st.view === "machines" || this.st.view === "settings" || this.st.view === "kubernetes") {
        // WSL 清單／設定／k3s 不依賴 Docker 引擎
        if (this.st.view === "machines") this.st.machines = await api.machines(signal);
        if (this.st.view === "kubernetes") await (k8sLoad ?? this.loadKubernetes(signal));
        if (signal.aborted || gen !== this.refreshGen) return;
        this.st.lastError = this.st.starting ? undefined : this.st.bringUpError;
      } else if (status.ready) {
        await this.loadView(signal);
        if (signal.aborted || gen !== this.refreshGen) return;
        this.st.lastError = undefined;
      } else if (!this.st.starting) {
        // 引擎未就緒：清空資源列表，避免仍顯示上一台 machine 的資料
        this.st.containers = [];
        this.st.images = [];
        this.st.volumes = [];
        this.st.networks = [];
        this.st.disk = undefined;
        // 用 i18n 前綴；細節走 status.APIError（renderBanner 會拼成「前綴 — 細節」）
        this.st.lastError = this.st.bringUpError ?? t("gui.error.api");
      }
    } catch (e) {
      if (signal.aborted || gen !== this.refreshGen) return;
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      if (this.st.status) this.st.status = { ...this.st.status, ready: false, APIReachable: false };
    } finally {
      if (gen === this.refreshGen) this.refreshInFlight = false;
    }
    if (signal.aborted || gen !== this.refreshGen) return;
    this.st.loaded.add(this.st.view);
    this.st.refreshCount++;
    this.renderAll(true);
  }

  private async loadView(signal?: AbortSignal): Promise<void> {
    if (this.st.view === "containers" || this.st.view === "volumes") await this.loadDisk(signal);
    switch (this.st.view) {
      case "containers":
        this.st.containers = await api.containers(signal);
        await this.loadLANForwards(signal);
        break;
      case "images":
        this.st.images = await api.images(signal);
        break;
      case "volumes":
        this.st.volumes = await api.volumes(signal);
        break;
      case "networks":
        this.st.networks = await api.networks(signal);
        break;
      case "machines":
        this.st.machines = await api.machines(signal);
        break;
    }
  }

  /** 磁碟用量是附加資訊：查詢失敗不影響列表，保留上一次的值。 */
  private async loadDisk(signal?: AbortSignal): Promise<void> {
    try {
      this.st.disk = await api.disk(signal);
    } catch {
      /* 保留上一次 */
    }
  }

  private setView(v: View): void {
    if (v === this.st.view) return;
    this.closePanel();
    this.closeInfo();
    this.st.view = v;
    this.st.usageOpen = false;
    this.usageParts = [];
    if (v === "settings" && !this.st.lanApplying) this.st.lan = undefined; // 每次進設定頁重新查詢區網狀態
    saveJSON(LS_VIEW, v);
    this.renderNav();
    this.renderHeader();
    this.renderView();
    void this.refresh();
  }

  // ---- 動作 -----------------------------------------------------------------

  private async act(fn: () => Promise<void>): Promise<void> {
    let err: string | undefined;
    try {
      await fn();
    } catch (e) {
      err = e instanceof ApiError ? e.message : String(e);
    }
    await this.refreshKeeping(err);
  }

  /** 刷新後仍保留動作錯誤（refresh 成功時會清掉連線錯誤，但使用者要看到動作為何失敗）。 */
  private async refreshKeeping(err: string | undefined): Promise<void> {
    await this.refresh();
    if (err) {
      this.st.lastError = err;
      this.renderBanner();
    }
  }

  /** 容器 Start/Stop：先標記 pending 立即重繪，完成後清除再刷新。 */
  private async actOn(id: string, kind: "starting" | "stopping", fn: () => Promise<void>): Promise<void> {
    this.st.pending[id] = kind;
    this.renderList();
    try {
      await this.act(fn);
    } finally {
      delete this.st.pending[id];
      this.renderList();
    }
  }

  /** 資源動作（刪除／prune）：確認 → 標 busy → 執行 → 通知／錯誤 → 刷新。
   *  requireType：須在確認框輸入完全相符字串才可確定（unregister）。 */
  private async resourceAction(
    key: string,
    confirmText: string,
    fn: () => Promise<string | undefined>,
    requireType?: string,
  ): Promise<void> {
    if (!(await this.confirm(confirmText, requireType))) return;
    this.st.busy.add(key);
    this.renderViewBody();
    let err: string | undefined;
    try {
      const msg = await fn();
      if (msg) this.notify(msg);
    } catch (e) {
      err = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.busy.delete(key);
    }
    await this.refreshKeeping(err);
  }

  /** sticky：不自動消失、顯示 ✕（調整大小／釋放空間這類要等上一分鐘的結果）；否則 8 秒後消失。 */
  private notify(msg: string, opts: { sticky?: boolean } = {}): void {
    this.st.notice = msg;
    this.st.noticeSticky = !!opts.sticky;
    this.renderNotice();
    if (this.noticeTimer) window.clearTimeout(this.noticeTimer);
    this.noticeTimer = undefined;
    if (opts.sticky) return;
    this.noticeTimer = window.setTimeout(() => this.dismissNotice(), 8000);
  }

  private dismissNotice(): void {
    if (this.noticeTimer) window.clearTimeout(this.noticeTimer);
    this.noticeTimer = undefined;
    this.st.notice = undefined;
    this.st.noticeSticky = false;
    this.renderNotice();
  }

  private openLogs(c: Container): void {
    this.closePanel();
    this.st.panel = { kind: "logs", id: c.Id, name: name(c), live: false };
    this.renderPanel();
    void this.loadLogs();
  }

  private async loadLogs(): Promise<void> {
    const p = this.st.panel;
    if (!p || p.kind !== "logs" || p.k8s) return; // Pod 日誌只走串流（setLive）
    const pre = this.root.querySelector<HTMLPreElement>('[data-testid="logs-content"]');
    if (!pre) return;
    try {
      pre.textContent = await api.logs(p.id, 100);
    } catch (e) {
      pre.textContent = e instanceof ApiError ? e.message : String(e);
    }
    pre.scrollTop = pre.scrollHeight;
  }

  private setLive(on: boolean): void {
    const p = this.st.panel;
    if (!p || p.kind !== "logs") return;
    p.live = on;
    if (p.ws) {
      p.ws.close();
      p.ws = undefined;
    }
    if (on) {
      const pre = this.root.querySelector<HTMLPreElement>('[data-testid="logs-content"]');
      if (pre) pre.textContent = "";
      const ws = new WebSocket(p.k8s ? api.k8sLogsUrl(p.k8s.ns, p.k8s.pod, 200) : api.logsStreamUrl(p.id, 100));
      ws.onmessage = (ev) => {
        if (!pre) return;
        pre.textContent += String(ev.data);
        pre.scrollTop = pre.scrollHeight;
      };
      p.ws = ws;
    } else {
      void this.loadLogs();
    }
    this.renderPanelHead();
  }

  private openTerminal(c: Container): void {
    this.closePanel();
    this.st.panel = { kind: "term", id: c.Id, name: name(c) };
    this.renderPanel();
    const host = this.root.querySelector<HTMLElement>('[data-testid="terminal-xterm"]');
    if (!host || this.st.panel.kind !== "term") return;
    this.st.panel.session = openTerminal(host, (cols, rows) => api.execUrl(c.Id, cols, rows), () => {
      /* 顯示已由 terminal.ts 寫入 */
    });
    this.st.panel.session.term.focus();
  }

  private openPodLogs(ns: string, pod: string): void {
    this.closePanel();
    this.st.panel = { kind: "logs", id: `k8s:${ns}/${pod}`, name: pod, live: false, k8s: { ns, pod } };
    this.renderPanel();
    this.setLive(true); // Pod 日誌預設即時追蹤
  }

  private openPodTerminal(ns: string, pod: string): void {
    this.closePanel();
    this.st.panel = { kind: "term", id: `k8s:${ns}/${pod}`, name: pod, k8s: { ns, pod } };
    this.renderPanel();
    const host = this.root.querySelector<HTMLElement>('[data-testid="terminal-xterm"]');
    if (!host || this.st.panel.kind !== "term") return;
    this.st.panel.session = openTerminal(host, (cols, rows) => api.k8sExecUrl(ns, pod, cols, rows), () => {});
    this.st.panel.session.term.focus();
  }

  private openMachineTerminal(distro: string): void {
    this.closePanel();
    this.st.panel = { kind: "term", id: `machine:${distro}`, name: distro };
    this.renderPanel();
    const host = this.root.querySelector<HTMLElement>('[data-testid="terminal-xterm"]');
    if (!host || this.st.panel.kind !== "term") return;
    this.st.panel.session = openTerminal(host, (cols, rows) => api.machineExecUrl(distro, cols, rows), () => {});
    this.st.panel.session.term.focus();
  }

  private closePanel(): void {
    const p = this.st.panel;
    if (!p) return;
    if (p.kind === "logs") p.ws?.close();
    if (p.kind === "term") p.session?.close();
    this.st.panel = undefined;
    this.renderPanel();
  }

  private openContainerInfo(c: Container): void {
    this.st.info = { kind: "container", id: c.Id, name: name(c) };
    this.renderInfoDrawer();
    this.renderList(); // 選取標示
    void api.containerInfo(c.Id).then((detail) => {
      if (this.st.info?.kind === "container" && this.st.info.id === c.Id) {
        this.st.info = { ...this.st.info, detail };
        this.renderInfoDrawer();
      }
    }).catch((e) => {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    });
  }

  private openComposeInfo(project: string): void {
    this.st.info = { kind: "compose", project };
    this.renderInfoDrawer();
  }

  private closeInfo(): void {
    if (!this.st.info) return;
    this.stopMachineUsagePoll();
    const wasContainer = this.st.info.kind === "container";
    this.st.info = undefined;
    this.renderInfoDrawer();
    if (wasContainer) this.renderList(); // 取消選取標示
  }

  private async saveSettings(s: Settings): Promise<boolean> {
    try {
      const saved = await api.putSettings(s);
      this.st.meta.config = saved;
      this.schedule();
      this.st.lastError = undefined;
      this.renderBanner();
      return true;
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
      return false;
    }
  }

  // ---- 渲染：外殼 -----------------------------------------------------------

  private build(): void {
    this.root.innerHTML = "";
    this.root.append(
      el("header", { class: "topbar", "data-testid": "header" },
        el("div", { class: "brand" },
          brandMark(),
          el("span", { class: "brand-name", text: t("gui.title") }),
          el("span", { class: "brand-ver", "data-testid": "status-version", text: `v${this.st.meta.version}` }),
        ),
        el("nav", { class: "tabs", "data-testid": "top-nav", "aria-label": "Views" }),
        el("div", { class: "top-actions", "data-testid": "top-actions" }),
      ),
      el("div", { class: "shell" },
        el("div", { class: "body" },
          el("main"),
          el("aside", { class: "info-drawer", "data-testid": "info-drawer", hidden: "" }),
        ),
        el("div", { class: "dock", "data-testid": "dock" },
          el("div", { class: "toasts", "data-testid": "toasts" },
            el("div", { class: "banner", "data-testid": "error-banner", role: "alert", hidden: "" }),
            el("div", { class: "notice", "data-testid": "notice", role: "status", hidden: "" }),
          ),
        ),
      ),
      this.buildConfirmDialog(),
      this.buildFirewallDialog(),
      this.buildAddMachineDialog(),
      this.buildResizeDialog(),
      this.buildDeployDialog(),
      this.buildImportDialog(),
      this.buildExposeDialog(),
    );
    this.renderNav();
    this.renderView();
    this.renderAll();
  }

  /** fromRefresh：由輪詢刷新呼叫時不重建設定頁——設定頁內容不來自輪詢資料，
   *  重建會收掉展開中的下拉、清掉尚未儲存的輸入與焦點；設定頁只在明確事件（進頁、切語言、區網動作等）時重繪。 */
  private renderAll(fromRefresh = false): void {
    this.renderHeader();
    this.renderBanner();
    this.renderNotice();
    if (!(fromRefresh && this.st.view === "settings")) this.renderViewBody();
    // Info 抽屜不隨列表輪詢重建；僅 machine 的 CPU／記憶體由 usage poll 就地更新
  }

  /** 引擎連線狀態（側欄發行版卡片與 Start engine 共用）。 */
  private engineState(): { state: "starting" | "connected" | "disconnected"; text: string; dot: string } {
    if (this.st.starting) return { state: "starting", text: t("gui.status.starting"), dot: "warn" };
    if (this.st.status?.ready === true) return { state: "connected", text: t("gui.status.connected"), dot: "ok" };
    return { state: "disconnected", text: t("gui.status.disconnected"), dot: "off" };
  }

  /** 頂部列右側：發行版晶片（引擎狀態）＋資料夾／終端＋需要時才出現的 Start engine＋Settings；
   *  同時更新內容區頂端的頁標題與摘要。 */
  private renderHeader(): void {
    const actions = this.root.querySelector<HTMLElement>('[data-testid="top-actions"]')!;
    const busy = this.st.starting;

    const distro = this.st.status?.Distro || this.st.meta.distro;
    const hasDistro = !!distro;
    const folderBtn = iconBtn({ class: "bordered", "data-testid": "btn-open-folder" }, "folder", t("gui.open_folder"));
    folderBtn.disabled = !hasDistro;
    folderBtn.onclick = () => void this.distroAction("open-folder");
    const termBtn = iconBtn({ class: "bordered", "data-testid": "btn-open-terminal" }, "terminal", t("gui.open_terminal"));
    termBtn.disabled = !hasDistro;
    termBtn.onclick = () => void this.distroAction("open-terminal");

    // Settings 是視圖之一，但放在頂部列最右側的齒輪
    const settings = VIEWS.find((v) => v.id === "settings")!;
    const gear = el("button", { type: "button", class: "nav icon-nav", "data-testid": "nav-settings", "data-view": "settings", title: t(settings.label) });
    if (this.st.view === "settings") gear.setAttribute("aria-current", "page");
    gear.append(icon(settings.icon), el("span", { class: "label sr-only", text: t(settings.label) }));
    gear.onclick = () => this.setView("settings");

    actions.replaceChildren(
      el("div", { "data-testid": "distro-slot" }),
      el("span", { class: "top-sep", "aria-hidden": "true" }),
      el("div", { class: "distro-actions", "data-testid": "distro-actions", ...(busy ? { "aria-busy": "true" } : {}) }, folderBtn, termBtn),
      gear,
    );
    this.renderDistroSwitch();
    this.renderPageHead();
  }

  /** 內容區頂端：目前視圖標題＋摘要（與工具列同一列）。 */
  private renderPageHead(): void {
    const head = this.root.querySelector<HTMLElement>('[data-testid="page-head"]');
    if (!head) return;
    const view = VIEWS.find((v) => v.id === this.st.view)!;
    head.replaceChildren(
      el("h1", { "data-testid": "page-title", text: t(view.label) }),
      el("span", { class: "page-sub", "data-testid": "page-sub", text: this.pageSubtitle() }),
      el("span", { class: "usage-slot", "data-testid": "usage-slot" }),
    );
    this.renderDiskSlot(this.usageParts);
  }

  /** 頁首副標：容器頁顯示執行中／已停止數量；資源頁顯示項目數；未連線時留空（大小在旁邊的磁碟用量鈕）。 */
  private pageSubtitle(): string {
    const ready = this.st.status?.ready === true;
    switch (this.st.view) {
      case "containers": {
        if (!ready) return "";
        const running = this.st.containers.filter((c) => c.State === "running").length;
        return fmt(t("gui.ct.summary"), String(running), String(this.st.containers.length - running));
      }
      case "images":
        return ready ? fmt(t("gui.sub.items"), String(this.st.images.length)) : "";
      case "volumes":
        return ready ? fmt(t("gui.sub.items"), String(this.st.volumes.length)) : "";
      case "networks":
        return ready ? fmt(t("gui.sub.items"), String(this.st.networks.length)) : "";
      case "machines":
        return this.st.machines?.available ? fmt(t("gui.sub.items"), String(this.st.machines.machines.length)) : "";
      case "kubernetes":
        return this.k8sSubtitle();
      default:
        return "";
    }
  }

  /** 容器頁的佔用分項：compose 專案整組一項，未分組的容器各一項（尚未取得 df 時為空）。 */
  private containerUsageParts(): UsagePart[] {
    const d = this.st.disk;
    if (!d || this.st.status?.ready !== true) return [];
    const size = (c: Container) => d.containers[c.Id]?.size_rw ?? 0;
    const parts: UsagePart[] = [];
    const groups = groupContainers(this.st.containers) ?? [{ project: "", items: this.st.containers }];
    for (const g of groups) {
      if (g.project) parts.push({ key: `compose:${g.project}`, label: g.project, size: g.items.reduce((a, c) => a + size(c), 0) });
      else for (const c of g.items) parts.push({ key: c.Id, label: name(c), size: size(c) });
    }
    return parts;
  }

  /** 最近一次的用量分項：點外面收起明細、重繪頁標題時不必重建列表。 */
  private usageParts: UsagePart[] = [];

  /** 頁標題旁的磁碟用量鈕（迷你長條＋總量），明細在點開的浮出清單；沒有大小可顯示時清空。
   *  列表每次刷新都會重繪：焦點原本在鈕上就還給新的鈕，鍵盤操作不會斷。 */
  private renderDiskSlot(parts: UsagePart[]): void {
    this.usageParts = parts;
    const slot = this.root.querySelector<HTMLElement>('[data-testid="usage-slot"]');
    if (!slot) return;
    const hadFocus = slot.contains(document.activeElement);
    const meter = usageButton(parts, this.st.usageOpen, (open) => {
      this.st.usageOpen = open;
      this.renderDiskSlot(this.usageParts);
    });
    if (!meter) this.st.usageOpen = false;
    slot.replaceChildren(...(meter ? [meter] : []));
    if (hadFocus) slot.querySelector<HTMLElement>('[data-testid="usage-btn"]')?.focus();
  }

  /** 點用量鈕與明細以外的地方就收起明細。 */
  private bindUsageDismiss(): void {
    document.addEventListener(
      "click",
      (e) => {
        if (!this.st.usageOpen) return;
        const wrap = this.root.querySelector(".usage-wrap");
        if (wrap && e.target instanceof Node && wrap.contains(e.target)) return;
        this.st.usageOpen = false;
        this.renderDiskSlot(this.usageParts);
      },
      true,
    );
  }

  /** 分段篩選（SegmentedControl）：aria-pressed 標示目前選項。 */
  private segmented(testId: string, items: { id: string; label: string }[], current: string, pick: (id: string) => void): HTMLElement {
    const seg = el("div", { class: "segmented", role: "group", "data-testid": testId });
    for (const it of items) {
      const b = el("button", { type: "button", "data-testid": `${testId}-${it.id}`, "aria-pressed": String(it.id === current), text: it.label });
      b.onclick = () => pick(it.id);
      seg.append(b);
    }
    return seg;
  }

  // ---- Loading 佔位（首次載入前，版面對齊實際資料）------------------------------

  /** 表格 loading 佔位：同樣的欄寬與表頭，列內放灰色條；末欄（動作）留白。 */
  private skeletonTable(view: View, cols: Col[], rows = 5): HTMLElement {
    const { table, tbody } = this.table(view, cols);
    const widths = [62, 78, 48, 70, 56];
    for (let i = 0; i < rows; i++) {
      const tr = el("tr", { "data-testid": "skeleton-row" });
      cols.forEach((c, j) => {
        tr.append(el("td", {}, c.key === "actions" ? "" : el("span", { class: "skel", style: `width:${widths[(i + j) % widths.length]}%` })));
      });
      tbody.append(tr);
    }
    table.setAttribute("aria-busy", "true");
    table.dataset.testid = "skeleton";
    return table;
  }

  /** Machines loading 佔位：與 machine-card 同格線的卡片。 */
  private skeletonCards(n: number): HTMLElement {
    const grid = el("div", { class: "machine-grid", "data-testid": "skeleton", "aria-busy": "true" });
    for (let i = 0; i < n; i++) {
      grid.append(
        el("div", { class: "machine-card" },
          el("div", { class: "machine-head" }, el("span", { class: "skel skel-icon" }), el("div", { class: "skel-stack" }, el("span", { class: "skel", style: "width:55%" }), el("span", { class: "skel", style: "width:35%" }))),
          el("span", { class: "skel", style: "width:80%" }),
          el("span", { class: "skel", style: "width:65%" }),
        ),
      );
    }
    return grid;
  }

  /** Kubernetes 叢集資訊卡 loading 佔位。 */
  private skeletonK8sCard(): HTMLElement {
    const grid = el("dl", { class: "k8s-fields" });
    for (let i = 0; i < 4; i++) grid.append(el("div", { class: "k8s-f" }, el("span", { class: "skel", style: "width:50px" }), el("span", { class: "skel", style: "width:120px" })));
    return el("section", { class: "k8s-card", "data-testid": "skeleton", "aria-busy": "true" }, el("div", { class: "k8s-head" }, el("span", { class: "skel", style: "width:90px" })), grid);
  }

  /** 引擎未就緒時取代空列表：說明＋啟動引擎＋由 status 推得的檢查清單。 */
  private engineDownState(): HTMLElement {
    const s = this.st.status;
    const distro = s?.Distro || this.st.meta.distro || t("gui.title");
    const starting = this.st.starting;
    const checks: [string, boolean | undefined, string?][] = [
      ["gui.check.distro", s?.DistroFound, distro],
      ["gui.check.running", s?.DistroRunning],
      ["gui.check.systemd", s?.SystemdActive],
      ["gui.check.docker", s?.DockerActive],
      ["gui.check.api", s?.APIReachable, s?.APIEndpoint || this.st.meta.api_endpoint],
    ];
    const list = el("ul", { class: "checks", "data-testid": "engine-checks" });
    for (const [key, ok, detail] of checks) {
      list.append(
        el("li", { "data-ok": String(!!ok) },
          el("span", { class: `dot ${ok ? "ok" : "bad"}`, "aria-hidden": "true" }),
          el("span", { class: "check-label", text: t(key) }),
          el("span", { class: "check-detail", text: detail ?? "" }),
        ),
      );
    }
    return el("div", { class: "engine-down", "data-testid": "engine-down" },
      el("div", { class: "engine-down-icon", "aria-hidden": "true" }, icon("power")),
      el("h2", { text: starting ? t("gui.down.starting") : t("gui.down.title") }),
      el("p", { text: fmt(t("gui.down.body"), distro) }),
      ...(!starting && s?.start_error ? [el("p", { class: "engine-down-error", "data-testid": "engine-down-error", text: s.start_error })] : []),
      list,
    );
  }

  /** 列表空狀態：引擎未就緒（已取得 status）→ 啟動卡片；否則一般「沒有…」。 */
  private emptyState(text: string): HTMLElement {
    if (this.st.status && this.st.status.ready !== true) return this.engineDownState();
    return el("div", { class: "empty", "data-testid": "empty", text });
  }

  /** 側欄頂部：目前發行版＋引擎狀態；點擊前往 Machines。 */
  private renderDistroSwitch(): void {
    const slot = this.root.querySelector<HTMLElement>('[data-testid="distro-slot"]');
    if (!slot) return;
    const distro = this.st.status?.Distro || this.st.meta.distro;
    const eng = this.engineState();
    const nameRow: (Node | string)[] = [
      el("span", { class: "distro-name", "data-testid": "header-distro", text: distro || t("gui.title"), title: distro ? `${t("gui.title")} — ${distro}` : t("gui.title") }),
    ];
    const b = el("button", {
      type: "button",
      class: "distro-switch",
      "data-testid": "distro-switch",
      "aria-label": `${t("gui.nav.machines")}: ${distro || t("gui.title")} — ${eng.text}`,
      ...(this.st.starting ? { "aria-busy": "true" } : {}),
    });
    const port = (this.st.meta.api_endpoint || "").match(/:(\d+)$/)?.[1];
    b.append(
      el("span", { class: `dot ${eng.dot}`, "data-testid": "engine-dot" }),
      ...nameRow,
      el("span", { class: "distro-status", "data-state": eng.state },
        el("span", { "data-testid": "status-connection", "data-state": eng.state, text: eng.text }),
        eng.state === "connected" && port ? ` · :${port}` : "",
      ),
    );
    b.onclick = () => this.setView("machines");
    slot.replaceChildren(b);
  }

  private async distroAction(action: "open-folder" | "open-terminal"): Promise<void> {
    try {
      await api.distroAction(action);
      this.st.lastError = undefined;
      this.renderBanner();
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    }
  }

  private renderBanner(): void {
    const b = this.root.querySelector<HTMLElement>('[data-testid="error-banner"]')!;
    // 動作錯誤優先（例如 Start 失敗），不要被 not-ready 橫幅蓋掉
    if (this.st.lastError && !this.st.starting) {
      const prefix = t("gui.error.api");
      b.textContent = this.st.lastError === prefix || this.st.lastError.startsWith("docker api unreachable")
        ? `${prefix} — ${this.st.lastError === prefix ? (this.st.status?.APIError ?? prefix) : this.st.lastError}`
        : this.st.lastError;
      b.hidden = false;
      b.dataset.kind = "error";
      return;
    }
    if (this.st.meta.active_ready === false && this.st.meta.distro) {
      b.textContent = t("gui.machines.not_ready");
      b.hidden = false;
      b.dataset.kind = "not-ready";
      return;
    }
    b.hidden = true;
    b.textContent = "";
    delete b.dataset.kind;
  }

  private renderNotice(): void {
    const n = this.root.querySelector<HTMLElement>('[data-testid="notice"]')!;
    n.hidden = !this.st.notice;
    const sticky = !!this.st.notice && !!this.st.noticeSticky;
    n.classList.toggle("sticky", sticky);
    const text = el("span", { "data-testid": "notice-text", text: this.st.notice ?? "" });
    if (!sticky) {
      n.replaceChildren(text);
      return;
    }
    const close = iconBtn({ "data-testid": "notice-close", class: "notice-close" }, "close", t("gui.close"));
    close.onclick = () => this.dismissNotice();
    n.replaceChildren(text, close);
  }

  /** 頂部分頁：資源（容器／映像／volume／網路）｜系統（Machines）；Settings 齒輪在右側（renderHeader）。
   *  視窗窄於 1100px 時只剩圖示（CSS），名稱留在 title／文字內容供輔助技術。 */
  private renderNav(): void {
    const nav = this.root.querySelector<HTMLElement>('[data-testid="top-nav"]')!;
    const item = (v: (typeof VIEWS)[number]) => {
      const b = el("button", { type: "button", class: "nav", "data-testid": `nav-${v.id}`, "data-view": v.id, title: t(v.label) });
      if (v.id === this.st.view) b.setAttribute("aria-current", "page");
      b.append(icon(v.icon), el("span", { class: "label", text: t(v.label) }));
      b.onclick = () => this.setView(v.id);
      return b;
    };
    const section = (s: "resources" | "system") => VIEWS.filter((v) => v.section === s).map(item);
    nav.replaceChildren(...section("resources"), el("span", { class: "tab-sep", "aria-hidden": "true" }), ...section("system"));
  }

  /** 建立目前視圖的骨架（containers／machines 含底部 panel）。 */
  private renderView(): void {
    const main = this.root.querySelector("main")!;
    main.dataset.view = this.st.view;
    main.setAttribute("data-testid", `view-${this.st.view}`);
    // 頁標題＋摘要與工具列同一列（工具列靠右，窄時換行）
    const pageBar = (toolbar: HTMLElement) => el("div", { class: "page-bar" }, el("div", { class: "page-head", "data-testid": "page-head" }), toolbar);
    const toolbar = () => el("div", { class: "toolbar", "data-testid": "toolbar" });
    if (this.st.view === "containers") {
      main.replaceChildren(
        pageBar(this.containerToolbar()),
        el("div", { class: "list", "data-testid": "container-list" }),
        el("section", { class: "panel", "data-testid": "panel", hidden: "" }),
      );
    } else if (this.st.view === "machines" || this.st.view === "kubernetes") {
      main.replaceChildren(
        pageBar(toolbar()),
        // Kubernetes：叢集資訊卡獨立在列表面板之外（自己的卡片），資源列表才放進 .list
        ...(this.st.view === "kubernetes" ? [el("div", { class: "k8s-top", "data-testid": "k8s-top" })] : []),
        this.st.view === "kubernetes"
          ? el("div", { class: "k8s-body", "data-testid": "k8s-body" }, el("div", { class: "list", "data-testid": "kubernetes-list" }), el("section", { class: "k8s-side", "data-testid": "k8s-side", hidden: "" }))
          : el("div", { class: "list", "data-testid": `${this.st.view}-list` }),
        el("section", { class: "panel", "data-testid": "panel", hidden: "" }),
      );
    } else {
      main.replaceChildren(pageBar(toolbar()), el("div", { class: "list", "data-testid": `${this.st.view}-list` }));
    }
    this.renderPageHead();
    this.renderViewBody();
  }

  private renderViewBody(): void {
    switch (this.st.view) {
      case "containers":
        this.renderList();
        break;
      case "images":
        this.renderImages();
        break;
      case "volumes":
        this.renderVolumes();
        break;
      case "networks":
        this.renderNetworks();
        break;
      case "machines":
        this.renderMachines();
        break;
      case "kubernetes":
        this.renderKubernetes();
        break;
      case "settings":
        this.renderSettings();
        break;
    }
  }

  /** Settings「關於」的 GitHub 連結：開啟官方 repo（經後端 BrowserOpenURL／系統瀏覽器）。 */
  private githubLink(): HTMLElement {
    const a = el("a", {
      class: "button-link",
      href: "https://github.com/iridance/wsld",
      target: "_blank",
      rel: "noopener noreferrer",
      "data-testid": "status-github",
      "aria-label": t("gui.github"),
      title: t("gui.github"),
    }) as HTMLAnchorElement;
    a.append(icon("github"), "GitHub");
    a.onclick = (e) => {
      e.preventDefault();
      void api.openRepo().catch((err) => {
        this.st.lastError = err instanceof ApiError ? err.message : String(err);
        this.renderBanner();
      });
    };
    return a;
  }

  /** Settings「外觀」的語言切換：立即寫入 config（後端與 Tray 一併切換），再重新載入 meta 重繪。 */
  private langSelect(): HTMLElement {
    const langs = [["en", "English"], ["zh-TW", "繁體中文"]] as const;
    return dropdown({ testid: "lang-select", id: "settings-lang" }, langs, this.st.meta.lang, (v) => void this.switchLang(v)).el;
  }

  private async switchLang(lang: string): Promise<void> {
    const ok = await this.saveSettings({ ...this.st.meta.config, lang });
    if (!ok) return;
    try {
      const meta = await api.meta();
      this.st.meta = meta;
      setMessages(meta.messages);
      document.documentElement.lang = meta.lang;
      document.title = t("gui.title");
      this.closePanel();
      this.build(); // 全部重繪（含對話框文字）
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    }
  }

  // ---- 表格共用：固定版面、可拖曳欄寬、localStorage --------------------------

  private colWidths: Record<string, Record<string, number>> = {
    containers: loadJSON(LS_COLS, {}),
    images: loadJSON(`${LS_COLS}.images`, {}),
    volumes: loadJSON(`${LS_COLS}.volumes`, {}),
    networks: loadJSON(`${LS_COLS}.networks`, {}),
    machines: loadJSON(`${LS_COLS}.machines`, {}),
    kubernetes: loadJSON(`${LS_COLS}.kubernetes`, {}),
  };
  private collapsed: Set<string> = this.initialCollapsed();

  /** k3s 容器群組預設收合（只首次；之後依使用者的展開／收合記錄）。 */
  private initialCollapsed(): Set<string> {
    const set = new Set(loadJSON<string[]>(LS_COLLAPSED, []));
    if (!loadJSON<boolean>(LS_K8S_GROUP_SEEN, false)) {
      set.add(K8S_GROUP);
      saveJSON(LS_COLLAPSED, [...set]);
      saveJSON(LS_K8S_GROUP_SEEN, true);
    }
    return set;
  }

  private widthOf(view: View, col: Col): number {
    return Math.max(col.min, this.colWidths[view][col.key] ?? col.def);
  }

  private applyColWidths(view: View, cols: Col[], table: HTMLTableElement): void {
    // 可調欄位給明確像素寬；最後的操作欄不設寬度，吸收剩餘空間（table-layout: fixed 的行為），
    // 所以拖曳某欄 +N px 時，只有那一欄與操作欄改變。
    let total = 0;
    table.querySelectorAll<HTMLTableColElement>("col").forEach((c) => {
      const col = cols.find((x) => x.key === c.dataset.col)!;
      const w = this.widthOf(view, col);
      if (col.resizable) c.style.width = `${w}px`;
      total += w;
    });
    table.style.width = "100%";
    table.style.minWidth = `${total}px`;
  }

  private startResize(ev: MouseEvent, view: View, cols: Col[], col: Col, table: HTMLTableElement): void {
    ev.preventDefault();
    const startX = ev.clientX;
    const startW = this.widthOf(view, col);
    const move = (e: MouseEvent) => {
      this.colWidths[view][col.key] = Math.max(col.min, startW + (e.clientX - startX));
      this.applyColWidths(view, cols, table);
    };
    const up = () => {
      document.removeEventListener("mousemove", move);
      document.removeEventListener("mouseup", up);
      document.body.classList.remove("resizing");
      saveJSON(view === "containers" ? LS_COLS : `${LS_COLS}.${view}`, this.colWidths[view]);
    };
    document.body.classList.add("resizing");
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
  }

  /** 建立含 colgroup／thead（可拖曳）的固定版面表格，回傳 table 與 tbody。 */
  private table(view: View, cols: Col[]): { table: HTMLTableElement; tbody: HTMLTableSectionElement } {
    const table = el("table", { class: "fixed" });
    const colgroup = el("colgroup", {}, ...cols.map((c) => el("col", { "data-col": c.key })));
    const headRow = el("tr");
    for (const col of cols) {
      const th = el("th", { "data-col": col.key, text: t(col.label) });
      if (col.resizable) {
        const handle = el("div", { class: "col-resizer", "data-testid": "col-resizer", role: "separator", "aria-orientation": "vertical" });
        handle.onmousedown = (e) => this.startResize(e, view, cols, col, table);
        th.append(handle);
      }
      headRow.append(th);
    }
    const tbody = el("tbody");
    table.append(colgroup, el("thead", {}, headRow), tbody);
    this.applyColWidths(view, cols, table);
    return { table, tbody };
  }

  // ---- 容器列表（compose 分組）------------------------------------------------

  private toggleGroup(project: string): void {
    if (this.collapsed.has(project)) this.collapsed.delete(project);
    else this.collapsed.add(project);
    saveJSON(LS_COLLAPSED, [...this.collapsed]);
    this.renderList();
  }

  /** 對多個容器同時執行動作（群組 Start／Stop），全部標 pending，結束後只刷新一次。 */
  private async actMany(ids: string[], kind: "starting" | "stopping", fn: (id: string) => Promise<void>): Promise<void> {
    for (const id of ids) this.st.pending[id] = kind;
    this.renderList();
    try {
      const results = await Promise.allSettled(ids.map(fn));
      const failed = results.find((r): r is PromiseRejectedResult => r.status === "rejected");
      this.st.lastError = failed ? (failed.reason instanceof ApiError ? failed.reason.message : String(failed.reason)) : undefined;
      await this.refresh();
    } finally {
      for (const id of ids) delete this.st.pending[id];
      this.renderList();
    }
  }

  /** 容器工具列：搜尋（名稱／映像／compose 服務）＋狀態分段篩選。只在切換視圖時建立，避免輸入時失焦。 */
  private containerToolbar(): HTMLElement {
    const search = el("input", {
      type: "search",
      class: "search-input",
      "data-testid": "ct-search",
      placeholder: t("gui.search.containers"),
      "aria-label": t("gui.search.containers"),
      autocomplete: "off",
      spellcheck: "false",
    }) as HTMLInputElement;
    search.value = this.st.ctQuery;
    const clear = el("button", { type: "button", class: "tf-btn", "data-testid": "ct-search-clear", "aria-label": t("gui.search.clear"), title: t("gui.search.clear") }) as HTMLButtonElement;
    clear.append(icon("close"));
    const syncClear = () => (clear.hidden = !search.value);
    search.oninput = () => {
      this.st.ctQuery = search.value;
      syncClear();
      this.renderList();
    };
    const reset = () => {
      search.value = "";
      search.dispatchEvent(new Event("input"));
      search.focus();
    };
    clear.onclick = reset;
    search.onkeydown = (e) => {
      if (e.key === "Escape" && search.value) {
        e.preventDefault();
        reset();
      }
    };
    syncClear();
    const seg = el("div", { class: "segmented", role: "group", "data-testid": "ct-filter" });
    for (const f of CT_FILTERS) {
      const b = el("button", { type: "button", "data-testid": `ct-filter-${f.id}`, "data-filter": f.id });
      b.onclick = () => {
        this.st.ctFilter = f.id;
        this.renderList();
      };
      seg.append(b);
    }
    const lead = icon("search");
    lead.classList.add("lead");
    const searchBox = el("label", { class: "tf search" }, lead, search, clear);
    return el("div", { class: "toolbar", "data-testid": "toolbar" }, searchBox, seg);
  }

  /** 依目前篩選／搜尋過濾容器。 */
  private visibleContainers(): Container[] {
    const q = this.st.ctQuery.trim().toLowerCase();
    return this.st.containers.filter((c) => {
      const running = c.State === "running";
      if (this.st.ctFilter === "running" && !running) return false;
      if (this.st.ctFilter === "stopped" && running) return false;
      if (!q) return true;
      return [name(c), c.Image, c.Labels?.[LABEL_SERVICE] ?? "", c.Labels?.[LABEL_PROJECT] ?? ""].some((s) => s.toLowerCase().includes(q));
    });
  }

  /** 分段鈕：標籤附數量、aria-pressed 標示目前篩選。 */
  private syncFilterButtons(): void {
    const running = this.st.containers.filter((c) => c.State === "running").length;
    const counts: Record<CtFilter, number> = { all: this.st.containers.length, running, stopped: this.st.containers.length - running };
    for (const f of CT_FILTERS) {
      const b = this.root.querySelector<HTMLButtonElement>(`[data-testid="ct-filter-${f.id}"]`);
      if (!b) continue;
      b.textContent = `${t(f.label)} ${counts[f.id]}`;
      b.setAttribute("aria-pressed", String(this.st.ctFilter === f.id));
    }
    const sub = this.root.querySelector<HTMLElement>('[data-testid="page-sub"]');
    if (sub) sub.textContent = this.pageSubtitle();
    // 引擎未就緒時搜尋／篩選沒有意義；保留空間（visibility）避免列表跳動
    const bar = this.root.querySelector<HTMLElement>('main[data-view="containers"] [data-testid="toolbar"]');
    if (bar) bar.style.visibility = this.st.status && this.st.status.ready !== true ? "hidden" : "";
  }

  private renderList(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="container-list"]');
    if (!list) return;
    this.syncFilterButtons();
    this.renderDiskSlot(this.containerUsageParts());
    if (!this.st.loaded.has("containers")) {
      list.replaceChildren(this.skeletonTable("containers", COLS));
      return;
    }
    if (this.st.containers.length === 0) {
      list.replaceChildren(this.emptyState(t("gui.empty")));
      return;
    }
    const shown = this.visibleContainers();
    if (shown.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty-filtered", text: t("gui.empty.filtered") }));
      return;
    }
    const { table, tbody } = this.table("containers", COLS);
    const groups = groupContainers(shown);
    if (!groups) {
      for (const c of shown) tbody.append(this.containerRow(c, "", name(c)));
    } else {
      for (const g of groups) {
        const expanded = !this.collapsed.has(g.project);
        tbody.append(this.groupRow(g, expanded));
        if (!expanded) continue;
        for (const c of g.items) tbody.append(this.containerRow(c, g.project, g.project ? c.Labels?.[LABEL_SERVICE] || name(c) : name(c)));
      }
    }
    list.replaceChildren(table);
  }

  private groupRow(g: Group, expanded: boolean): HTMLTableRowElement {
    const running = g.items.filter((c) => c.State === "running").length;
    const anyPending = g.items.some((c) => this.st.pending[c.Id]);
    const toggle = el("button", { class: "small ghost", "data-testid": "group-toggle", "aria-label": expanded ? "collapse" : "expand" });
    toggle.append(icon(expanded ? "chevron-down" : "chevron-right"));
    toggle.onclick = () => this.toggleGroup(g.project);
    const actions: HTMLElement[] = [];
    if (g.project && g.project !== K8S_GROUP) {
      // 只有 compose 專案才有整組 Start／Stop／Info；未分組與 k3s 群組只是集合（k3s 容器交給 k3s 管理）
      const startBtn = iconBtn({ "data-testid": "group-start" }, "play", t("gui.start"));
      startBtn.disabled = anyPending || running === g.items.length;
      startBtn.onclick = () =>
        void this.actMany(g.items.filter((c) => c.State !== "running").map((c) => c.Id), "starting", (id) => api.startContainer(id));
      const stopBtn = iconBtn({ "data-testid": "group-stop" }, "stop", t("gui.stop"));
      stopBtn.disabled = anyPending || running === 0;
      stopBtn.onclick = () =>
        void this.actMany(g.items.filter((c) => c.State === "running").map((c) => c.Id), "stopping", (id) => api.stopContainer(id));
      const infoBtn = iconBtn({ "data-testid": "group-info" }, "info", t("gui.info"));
      infoBtn.onclick = () => this.openComposeInfo(g.project);
      actions.push(startBtn, stopBtn, infoBtn);
    }

    const cell = el(
      "td",
      { colspan: String(COLS.length) },
      el(
        "div",
        { class: "group-bar" },
        toggle,
        el("span", { class: "group-name", "data-testid": "group-name", text: g.project === K8S_GROUP ? t("gui.ct.k8s_group") : g.project || "-" }),
        el("span", { class: "group-count", "data-testid": "group-count", text: `${running}/${g.items.length}` }),
        ...(this.st.disk
          ? [el("span", { class: "group-size muted", "data-testid": "group-size", text: humanSize(g.items.reduce((a, c) => a + (this.st.disk?.containers[c.Id]?.size_rw ?? 0), 0)) })]
          : []),
        el("span", { class: "spacer" }),
        ...actions,
      ),
    );
    return el("tr", { class: "group", "data-testid": "group-row", "data-project": g.project, "aria-expanded": String(expanded) }, cell);
  }

  private containerRow(c: Container, project: string, display: string): HTMLTableRowElement {
    const running = c.State === "running";
    const n = name(c);
    const pending = this.st.pending[c.Id];
    const startBtn = iconBtn({ "data-testid": `btn-start-${n}` }, "play", t("gui.start"));
    startBtn.disabled = running || !!pending;
    startBtn.onclick = () => void this.actOn(c.Id, "starting", () => api.startContainer(c.Id));
    const stopBtn = iconBtn({ "data-testid": `btn-stop-${n}` }, "stop", t("gui.stop"));
    stopBtn.disabled = !running || !!pending;
    stopBtn.onclick = () => void this.actOn(c.Id, "stopping", () => api.stopContainer(c.Id));
    const logsBtn = iconBtn({ "data-testid": `btn-logs-${n}` }, "logs", t("gui.logs"));
    logsBtn.onclick = () => this.openLogs(c);
    const termBtn = iconBtn({ "data-testid": `btn-term-${n}` }, "terminal", t("gui.terminal"));
    termBtn.disabled = !running;
    termBtn.onclick = () => this.openTerminal(c);
    const infoBtn = iconBtn({ "data-testid": `btn-info-${n}` }, "info", t("gui.info"));
    infoBtn.onclick = () => this.openContainerInfo(c);
    const rmBtn = iconBtn({ "data-testid": `btn-rm-${n}`, class: "danger" }, "trash", t("gui.remove"));
    rmBtn.disabled = !!pending;
    rmBtn.onclick = () =>
      void this.resourceAction(`rm:${c.Id}`, fmt(t("gui.confirm.remove_ct"), n), async () => {
        await api.removeContainer(c.Id, true);
        if (this.st.info?.kind === "container" && this.st.info.id === c.Id) this.closeInfo();
        if (this.st.panel && this.st.panel.id === c.Id) this.closePanel();
        return fmt(t("gui.removed"), n);
      });

    const rowAttrs: Record<string, string> = { "data-testid": "container-row", "data-name": n, "data-state": c.State, "data-project": project };
    if (pending) rowAttrs["data-pending"] = pending;
    // 右側詳情正在顯示這個容器：整列標示為選取
    if (this.st.info?.kind === "container" && this.st.info.id === c.Id) {
      rowAttrs.class = "sel";
      rowAttrs["aria-selected"] = "true";
    }
    const stateText = pending ? t(`gui.ct.${pending}`) : c.State;
    const browserBtn = this.browserButton(c);
    return el(
      "tr",
      rowAttrs,
      el("td", { class: `name-cell ${project ? "indent" : ""}`.trim() },
        el("span", { class: "ct-name", "data-testid": "ct-name", text: display, title: `${n}\n${c.Id}` }),
        el("span", { class: "ct-sub", "data-testid": `ct-status-${n}`, text: c.Status || c.State }),
      ),
      el("td", { text: c.Image, title: c.Image }),
      el("td", {}, el("span", { class: `state ${pending ? "pending" : c.State}`, "data-testid": `state-${n}`, text: stateText })),
      this.containerSizeCell(c, n),
      el("td", { class: "actions" }, startBtn, stopBtn, logsBtn, termBtn, browserBtn, infoBtn, rmBtn),
    );
  }

  private containerSizeCell(c: Container, n: string): HTMLElement {
    const d = this.st.disk?.containers[c.Id];
    if (!d) return el("td", { "data-testid": `size-${n}`, class: "num muted", text: "–" });
    return el("td", {
      "data-testid": `size-${n}`,
      "data-size": String(d.size_rw),
      class: "num",
      text: humanSize(d.size_rw),
      title: fmt(t("gui.ct.size_title"), humanSize(d.size_rw), humanSize(d.size_root_fs)),
    });
  }

  private renderPanel(): void {
    const sec = this.root.querySelector<HTMLElement>('[data-testid="panel"]');
    const main = this.root.querySelector("main")!;
    if (!sec) return;
    const p = this.st.panel;
    if (!p) {
      sec.hidden = true;
      sec.replaceChildren();
      main.classList.remove("with-panel");
      return;
    }
    sec.hidden = false;
    main.classList.add("with-panel");
    const head = el("div", { class: "panel-head", "data-testid": "panel-head" });
    const body =
      p.kind === "logs"
        ? el("pre", { class: "logs", "data-testid": "logs-content" })
        : el("div", { class: "xterm-host", "data-testid": "terminal-xterm" });
    sec.setAttribute("data-kind", p.kind);
    sec.replaceChildren(head, body);
    this.renderPanelHead();
  }

  private renderPanelHead(): void {
    const p = this.st.panel;
    const head = this.root.querySelector<HTMLElement>('[data-testid="panel-head"]');
    if (!p || !head) return;
    const title = el("strong", { "data-testid": "panel-title", text: `${p.kind === "logs" ? t("gui.logs") : t("gui.terminal")} — ${p.name}` });
    const close = iconBtn({ class: "small", "data-testid": "btn-panel-close" }, "close", t("gui.close"));
    close.onclick = () => this.closePanel();
    const kids: (Node | string)[] = [title];
    // 容器面板（有分頁時標題只放名稱，類型由分頁表示）：日誌／終端分頁切換（同一個容器）；machine 終端沒有日誌
    const c = this.st.containers.find((x) => x.Id === p.id);
    if (p.k8s) {
      // Pod 面板：日誌／終端分頁（同一個 Pod）
      const { ns, pod } = p.k8s;
      title.textContent = pod;
      kids.push(tabs({
        testid: "panel-tabs", selected: p.kind,
        items: [{ id: "logs", testid: "panel-tab-logs", icon: "logs", label: t("gui.logs") }, { id: "term", testid: "panel-tab-term", icon: "terminal", label: t("gui.terminal") }],
        onSelect: (kind) => {
          if (this.st.panel?.kind === kind) return;
          if (kind === "logs") this.openPodLogs(ns, pod);
          else this.openPodTerminal(ns, pod);
        },
      }));
    } else if (c) {
      title.textContent = p.name;
      kids.push(tabs({
        testid: "panel-tabs", selected: p.kind,
        items: [{ id: "logs", testid: "panel-tab-logs", icon: "logs", label: t("gui.logs") }, { id: "term", testid: "panel-tab-term", icon: "terminal", label: t("gui.terminal"), disabled: c.State !== "running" }],
        onSelect: (kind) => {
          if (this.st.panel?.kind === kind) return;
          if (kind === "logs") this.openLogs(c);
          else this.openTerminal(c);
        },
      }));
    }
    kids.push(el("span", { class: "spacer" }));
    if (p.kind === "logs") {
      const live = el("input", { type: "checkbox", "data-testid": "logs-live-toggle" }) as HTMLInputElement;
      live.checked = p.live;
      live.onchange = () => this.setLive(live.checked);
      kids.push(el("label", { class: "check" }, live, t("gui.live")));
    }
    kids.push(close);
    head.replaceChildren(...kids);
  }

  /** 同一 Info 面板被自動刷新重建時，用來還原捲動／摺疊狀態。 */
  private infoDrawerKey(info: InfoDrawer): string {
    if (info.kind === "container") return `container:${info.id}`;
    if (info.kind === "machine") return `machine:${info.name}`;
    return `compose:${info.project}`;
  }

  private renderInfoDrawer(): void {
    const aside = this.root.querySelector<HTMLElement>('[data-testid="info-drawer"]');
    if (!aside) return;
    const info = this.st.info;
    if (!info) {
      aside.hidden = true;
      aside.removeAttribute("data-kind");
      aside.removeAttribute("data-name");
      aside.removeAttribute("data-project");
      aside.removeAttribute("data-scroll-key");
      aside.replaceChildren();
      return;
    }
    const key = this.infoDrawerKey(info);
    const same = aside.getAttribute("data-scroll-key") === key;
    const prevBody = aside.querySelector<HTMLElement>(".info-body");
    const scrollTop = same && prevBody ? prevBody.scrollTop : 0;
    const labelsOpen = same && !!aside.querySelector<HTMLDetailsElement>('[data-testid="info-labels"]')?.open;

    aside.hidden = false;
    aside.setAttribute("data-kind", info.kind);
    aside.setAttribute("data-scroll-key", key);
    if (info.kind === "container") {
      aside.setAttribute("data-name", info.name);
      aside.removeAttribute("data-project");
      const c = this.st.containers.find((x) => x.Id === info.id);
      const head = el("div", { class: "info-head" },
        el("strong", { "data-testid": "info-title", text: info.name }),
        el("span", { class: "spacer" }),
        (() => {
          const close = iconBtn({ "data-testid": "btn-info-close" }, "close", t("gui.close"));
          close.onclick = () => this.closeInfo();
          return close;
        })(),
      );
      const body = el("div", { class: "info-body" });
      const d = info.detail;
      if (d) {
        const nets = (d.networks ?? []).map((n) => `${n.name}${n.ip ? ` (${n.ip})` : ""}`).join(", ") || "—";
        const mounts = (d.mounts ?? []).map((m) => `${m.source} → ${m.dest}`).join("\n") || "—";
        body.append(
          infoField(t("gui.info.name"), d.name || info.name),
          infoField(t("gui.col.id"), d.id, "info-id"),
          infoField(t("gui.col.image"), d.image),
          infoField(t("gui.col.state"), d.state, "info-state"),
          infoField(t("gui.info.created"), d.created || "—"),
          infoField(t("gui.info.started"), d.started || "—"),
          infoField(t("gui.info.command"), d.command || "—", "info-command"),
          infoField(t("gui.col.ports"), d.ports || ports(c!) || "—", "info-ports"),
          ...(c && this.lanText(c) ? [infoField(t("gui.lan.tag"), this.lanText(c), "info-lan")] : []),
          infoField(t("gui.info.networks"), nets, "info-networks"),
          infoField(t("gui.info.mounts"), mounts, "info-mounts"),
          infoField(t("gui.info.restart"), d.restart_policy || "—", "info-restart"),
        );
        const labels = d.labels ?? {};
        const keys = Object.keys(labels);
        if (keys.length) {
          const det = el("details", { "data-testid": "info-labels" }, el("summary", { text: `${t("gui.info.labels")} (${keys.length})` }));
          if (labelsOpen) det.open = true;
          const pre = el("pre", { class: "conf" });
          pre.textContent = keys.sort().map((k) => `${k}=${labels[k]}`).join("\n");
          det.append(pre);
          body.append(det);
        }
      } else if (c) {
        body.append(
          infoField(t("gui.col.image"), c.Image),
          infoField(t("gui.col.state"), c.State, "info-state"),
          infoField(t("gui.col.ports"), ports(c) || "—", "info-ports"),
          ...(this.lanText(c) ? [infoField(t("gui.lan.tag"), this.lanText(c), "info-lan")] : []),
          infoField(t("gui.col.id"), c.Id),
          el("p", { class: "muted", text: "…" }),
        );
      } else {
        body.append(el("p", { class: "muted", text: t("gui.empty") }));
      }
      aside.replaceChildren(head, body);
      restoreInfoScroll(body, scrollTop);
      return;
    }
    if (info.kind === "machine") {
      aside.setAttribute("data-name", info.name);
      aside.removeAttribute("data-project");
      const head = el("div", { class: "info-head" },
        el("strong", { "data-testid": "info-title", text: info.name }),
        el("span", { class: "spacer" }),
        (() => {
          const close = iconBtn({ "data-testid": "btn-info-close" }, "close", t("gui.close"));
          close.onclick = () => this.closeInfo();
          return close;
        })(),
      );
      const body = el("div", { class: "info-body" });
      const d = info.detail;
      if (d) {
        body.append(
          infoField(t("gui.info.name"), d.name),
          infoField(t("gui.machines.user"), d.user || "—"),
          infoField(t("gui.machines.endpoint"), d.api_endpoint || "—", "info-machine-endpoint"),
          infoField(t("gui.machines.wslconfig"), `memory=${d.memory || "—"}; processors=${d.processors || "—"}`, "info-wslconfig"),
          usageMeter(t("gui.machines.cpu"), formatCPU(d.cpu_percent ?? 0), clampPct(d.cpu_percent ?? 0), "info-cpu-bar"),
          usageMeter(t("gui.machines.mem"), formatMem(d.memory_used_mb ?? 0, d.memory_total_mb ?? 0), memPct(d.memory_used_mb ?? 0, d.memory_total_mb ?? 0), "info-mem-bar"),
          ...((d.disk_total_bytes ?? 0) > 0
            ? [usageMeter(t("gui.machines.disk_fs"), formatDisk(d.disk_used_bytes ?? 0, d.disk_total_bytes ?? 0), memPct(d.disk_used_bytes ?? 0, d.disk_total_bytes ?? 0), "info-disk-bar")]
            : []),
          infoField(t("gui.machines.disk_hint"), (d.disk_bytes ?? 0) > 0 ? humanSize(d.disk_bytes ?? 0) : "—", "info-disk"),
          this.diskLimitField(info.name, d),
          ...(d.wsl_conf_unavailable
            ? [el("p", { class: "muted", "data-testid": "machine-config-unavailable", text: d.running ? "…" : t("gui.machines.conf_stopped") })]
            : [
              el("div", { class: `tag ${d.systemd ? "ok" : ""}`, "data-testid": "machine-config-systemd", text: d.systemd ? t("gui.machines.systemd_on") : t("gui.machines.systemd_off") }),
              el("pre", { class: "conf", "data-testid": "machine-config-conf", text: d.wsl_conf || t("gui.machines.no_conf") }),
            ]),
        );
      } else {
        body.append(el("p", { class: "muted", text: "…" }));
      }
      aside.replaceChildren(head, body);
      restoreInfoScroll(body, scrollTop);
      return;
    }
    aside.setAttribute("data-project", info.project);
    aside.removeAttribute("data-name");
    const items = this.st.containers.filter((c) => c.Labels?.[LABEL_PROJECT] === info.project);
    const running = items.filter((c) => c.State === "running").length;
    const head = el("div", { class: "info-head" },
      el("strong", { "data-testid": "info-title", text: info.project }),
      el("span", { class: "spacer" }),
      (() => {
        const close = iconBtn({ "data-testid": "btn-info-close" }, "close", t("gui.close"));
        close.onclick = () => this.closeInfo();
        return close;
      })(),
    );
    const body = el("div", { class: "info-body" },
      el("div", { class: "info-field" },
        el("dt", { text: t("gui.col.state") }),
        el("dd", { "data-testid": "info-count", text: `${running}/${items.length}` }),
      ),
    );
    for (const c of items) {
      const svc = c.Labels?.[LABEL_SERVICE] || name(c);
      body.append(
        el("div", { class: "info-service" },
          el("div", { class: "info-service-name", text: svc }),
          el("div", { class: `state ${c.State}`, text: c.State }),
          el("div", { class: "muted", text: c.Status }),
          el("div", { class: "muted", text: ports(c) || "—" }),
          ...(this.lanText(c) ? [el("div", { class: "muted mono", "data-testid": "info-service-lan", text: this.lanText(c) })] : []),
        ),
      );
    }
    aside.replaceChildren(head, body);
    restoreInfoScroll(body, scrollTop);
  }

  // ---- Images -----------------------------------------------------------------

  private renderImages(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="images-list"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !bar) return;
    const ready = this.st.status?.ready === true;
    const prune = btn({ "data-testid": "btn-prune-images" }, "broom", t("gui.prune"));
    prune.disabled = !ready || this.st.busy.has("prune-images");
    prune.onclick = () =>
      void this.resourceAction("prune-images", t("gui.confirm.prune_images"), async () => {
        const rep = await api.pruneImages();
        return fmt(t("gui.pruned"), String(rep.count), humanSize(rep.space_reclaimed));
      });

    const counts: Record<ImgFilter, number> = { all: this.st.images.length, inuse: 0, unused: 0, dangling: 0 };
    for (const im of this.st.images) {
      const k = imageKind(im);
      if (k !== "unknown") counts[k]++;
    }
    const seg = this.segmented(
      "img-filter",
      IMG_FILTERS.map((f) => ({ id: f.id, label: `${t(f.label)} ${counts[f.id]}` })),
      this.st.imgFilter,
      (id) => {
        this.st.imgFilter = id as ImgFilter;
        this.renderImages();
      },
    );
    bar.replaceChildren(seg, prune);
    // 磁碟用量在標題列下方獨佔一列
    this.renderDiskSlot(ready ? this.st.images.map((im) => ({ key: im.Id, label: imageName(im) === "<none>" ? shortID(im.Id) : imageName(im), size: im.Size })) : []);

    if (!this.st.loaded.has("images")) {
      list.replaceChildren(this.skeletonTable("images", IMAGE_COLS));
      return;
    }
    if (!ready || this.st.images.length === 0) {
      list.replaceChildren(this.emptyState(t("gui.empty.images")));
      return;
    }
    const imgs = [...this.st.images]
      .filter((im) => this.st.imgFilter === "all" || imageKind(im) === this.st.imgFilter)
      .sort((a, b) => imageName(a).localeCompare(imageName(b)));
    if (imgs.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty-filtered", text: t("gui.empty.filtered_items") }));
      return;
    }
    const { table, tbody } = this.table("images", IMAGE_COLS);
    for (const im of imgs) {
      const short = shortID(im.Id);
      const nm = imageName(im);
      const ref = nm === "<none>" ? short : nm;
      const kind = imageKind(im);
      const rm = iconBtn({ "data-testid": `btn-rm-image-${short}`, class: "danger" }, "trash", t("gui.remove"));
      rm.disabled = this.st.busy.has(`image:${ref}`);
      rm.onclick = () =>
        void this.resourceAction(`image:${ref}`, fmt(t("gui.confirm.remove"), ref), async () => {
          await api.removeImage(ref);
          return fmt(t("gui.removed"), ref);
        });
      const inuse = el("td", {},
        el("span", { class: `tag ${KIND_TAG[kind]}`, "data-testid": "image-inuse", "data-kind": kind, title: t(KIND_LABEL[kind]), text: im.Containers < 0 ? "–" : String(im.Containers) }),
      );
      tbody.append(
        el(
          "tr",
          { "data-testid": "image-row", "data-ref": ref, "data-kind": kind },
          el("td", { "data-testid": "image-name", class: "strong", text: nm, title: (im.RepoTags ?? []).join("\n") || nm }),
          el("td", { "data-testid": "image-id", class: "mono muted", text: short, title: im.Id }),
          el("td", { "data-testid": "image-size", class: "num", text: humanSize(im.Size) }),
          el("td", { class: "muted", text: fmtUnix(im.Created) }),
          inuse,
          el("td", { class: "actions" }, rm),
        ),
      );
    }
    list.replaceChildren(table);
  }

  // ---- Volumes ----------------------------------------------------------------

  private renderVolumes(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="volumes-list"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !bar) return;
    const ready = this.st.status?.ready === true;
    const prune = btn({ "data-testid": "btn-prune-volumes" }, "broom", t("gui.prune"));
    prune.disabled = !ready || this.st.busy.has("prune-volumes");
    prune.onclick = () =>
      void this.resourceAction("prune-volumes", t("gui.confirm.prune_volumes"), async () => {
        const rep = await api.pruneVolumes();
        return fmt(t("gui.pruned"), String(rep.count), humanSize(rep.space_reclaimed));
      });
    bar.replaceChildren(el("span", { class: "spacer" }), prune);
    const vd = this.st.disk?.volumes;
    this.renderDiskSlot(ready && vd ? this.st.volumes.map((v) => ({ key: v.Name, label: v.Name, size: vd[v.Name]?.size ?? 0 })) : []);

    if (!this.st.loaded.has("volumes")) {
      list.replaceChildren(this.skeletonTable("volumes", VOLUME_COLS));
      return;
    }
    if (!ready || this.st.volumes.length === 0) {
      list.replaceChildren(this.emptyState(t("gui.empty.volumes")));
      return;
    }
    const { table, tbody } = this.table("volumes", VOLUME_COLS);
    for (const v of [...this.st.volumes].sort((a, b) => a.Name.localeCompare(b.Name))) {
      const rm = iconBtn({ "data-testid": `btn-rm-volume-${v.Name}`, class: "danger" }, "trash", t("gui.remove"));
      rm.disabled = this.st.busy.has(`volume:${v.Name}`);
      rm.onclick = () =>
        void this.resourceAction(`volume:${v.Name}`, fmt(t("gui.confirm.remove"), v.Name), async () => {
          await api.removeVolume(v.Name);
          return fmt(t("gui.removed"), v.Name);
        });
      tbody.append(
        el(
          "tr",
          { "data-testid": "volume-row", "data-name": v.Name },
          el("td", { "data-testid": "volume-name", class: "strong", text: v.Name, title: v.Name }),
          el("td", { "data-testid": "volume-driver", text: v.Driver }),
          this.volumeSizeCell(v.Name),
          el("td", { "data-testid": "volume-mountpoint", class: "mono muted", text: v.Mountpoint, title: v.Mountpoint }),
          el("td", { class: "muted", text: fmtISO(v.CreatedAt) }),
          el("td", { class: "actions" }, rm),
        ),
      );
    }
    list.replaceChildren(table);
  }

  private volumeSizeCell(name: string): HTMLElement {
    const d = this.st.disk?.volumes[name];
    const text = !d || d.size < 0 ? "–" : humanSize(d.size);
    return el("td", { "data-testid": "volume-size", class: "num", text, ...(d && d.size >= 0 ? { "data-size": String(d.size) } : {}) });
  }

  // ---- Networks ---------------------------------------------------------------

  private renderNetworks(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="networks-list"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !bar) return;
    const ready = this.st.status?.ready === true;
    const prune = btn({ "data-testid": "btn-prune-networks" }, "broom", t("gui.prune"));
    prune.disabled = !ready || this.st.busy.has("prune-networks");
    prune.onclick = () =>
      void this.resourceAction("prune-networks", t("gui.confirm.prune_networks"), async () => {
        const rep = await api.pruneNetworks();
        return fmt(t("gui.pruned"), String(rep.count), humanSize(rep.space_reclaimed));
      });
    bar.replaceChildren(el("span", { class: "spacer" }), prune);

    if (!this.st.loaded.has("networks")) {
      list.replaceChildren(this.skeletonTable("networks", NETWORK_COLS));
      return;
    }
    if (!ready || this.st.networks.length === 0) {
      list.replaceChildren(this.emptyState(t("gui.empty.networks")));
      return;
    }
    const { table, tbody } = this.table("networks", NETWORK_COLS);
    for (const n of [...this.st.networks].sort((a, b) => a.Name.localeCompare(b.Name))) {
      const builtin = n.Name === "bridge" || n.Name === "host" || n.Name === "none";
      const rm = iconBtn({ "data-testid": `btn-rm-network-${n.Name}`, class: "danger" }, "trash", t("gui.remove"));
      rm.disabled = builtin || this.st.busy.has(`network:${n.Name}`);
      if (!builtin) {
        rm.onclick = () =>
          void this.resourceAction(`network:${n.Name}`, fmt(t("gui.confirm.remove"), n.Name), async () => {
            await api.removeNetwork(n.Name);
            return fmt(t("gui.removed"), n.Name);
          });
      }
      tbody.append(
        el(
          "tr",
          { "data-testid": "network-row", "data-name": n.Name, "data-builtin": String(builtin) },
          el("td", { "data-testid": "network-name", class: builtin ? "muted" : "strong", text: n.Name, title: n.Name }),
          el("td", { "data-testid": "network-driver", text: n.Driver }),
          el("td", { "data-testid": "network-scope", text: n.Scope }),
          el("td", { "data-testid": "network-id", class: "mono muted", text: shortID(n.Id), title: n.Id }),
          el("td", { class: "muted", text: fmtISO(n.Created) }),
          el("td", { class: "actions" }, rm),
        ),
      );
    }
    list.replaceChildren(table);
  }

  // ---- Machines（本機 WSL）：卡片 ---------------------------------------------

  private renderMachines(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="machines-list"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !bar) return;
    list.classList.add("cards");
    const m = this.st.machines;
    const removingAny = [...this.st.busy].some((k) => k.startsWith("remove:") || k.startsWith("role:"));
    const hasEngine = m?.available ? m.machines.some((x) => x.role === "engine") : false;
    const add = btn({ class: "primary", "data-testid": "btn-add-machine" }, "plus", t("gui.machines.add"));
    add.disabled = !m?.available || this.st.busy.has("install") || removingAny || this.st.starting;
    add.onclick = () => this.openAddMachine();
    bar.replaceChildren(el("span", { class: "spacer" }), add);
    this.renderDiskSlot((m?.available ? m.machines : []).map((d) => ({ key: d.name, label: d.name, size: d.disk_bytes ?? 0 })));

    if (!m) {
      list.replaceChildren(this.skeletonCards(3));
      return;
    }
    if (!m.available) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.machines.unavailable") }));
      return;
    }
    if (m.machines.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty.machines") }));
      return;
    }
    // Engine 單獨一組放最上面；其餘機器依原順序在下一組
    const engineGrid = el("div", { class: "machine-grid" });
    const otherGrid = el("div", { class: "machine-grid" });
    for (const d of m.machines) {
      const isEngine = d.role === "engine";
      const grid = isEngine ? engineGrid : otherGrid;
      const removing = this.st.busy.has(`remove:${d.name}`);
      const starting = (this.st.starting && d.active) || this.st.busy.has(`power-on:${d.name}`);
      const stopping = this.st.busy.has(`power-off:${d.name}`);
      const resizing = this.st.busy.has(`resize:${d.name}`);
      const compacting = this.st.busy.has(`compact:${d.name}`);
      const configuring = this.st.busy.has(`role:${d.name}`);
      const pending = configuring ? "configuring" : removing ? "removing" : starting ? "starting" : stopping ? "stopping" : resizing ? "resizing" : compacting ? "compacting" : undefined;
      const locked = !!pending || removingAny || this.st.starting;
      const tools: HTMLElement[] = [];
      const cfg = iconBtn({ "data-testid": `btn-machine-config-${d.name}` }, "info", t("gui.machines.info"));
      cfg.disabled = locked;
      cfg.onclick = () => void this.openMachineInfo(d.name);
      const folder = iconBtn({ "data-testid": `btn-machine-folder-${d.name}` }, "folder", t("gui.machines.folder"));
      folder.disabled = locked;
      folder.onclick = () => void this.distroAction("open-folder");
      const term = iconBtn({ "data-testid": `btn-machine-term-${d.name}` }, "terminal", t("gui.machines.terminal"));
      term.disabled = locked;
      term.onclick = () => this.openMachineTerminal(d.name);
      const compact = iconBtn({ "data-testid": `btn-machine-compact-${d.name}` }, "shrink", t("gui.machines.compact"));
      compact.disabled = locked || [...this.st.busy].some((k) => k.startsWith("compact:") || k.startsWith("resize:"));
      compact.onclick = () => void this.compactMachine(d.name, d.state.toLowerCase() === "running");
      const rm = iconBtn({ "data-testid": `btn-machine-remove-${d.name}`, class: "danger" }, "trash", t("gui.machines.remove"));
      rm.disabled = locked;
      rm.onclick = () =>
        void this.resourceAction(
          `remove:${d.name}`,
          `${t("gui.machines.unregister_prompt")}\n${d.name}`,
          async () => {
            if ((await this.withExposureConfirm((release) => api.unregisterMachine(d.name, d.name, release))) === CANCELLED) return undefined;
            this.hardCutActive();
            this.st.meta = await api.meta();
            setMessages(this.st.meta.messages);
            return fmt(t("gui.removed"), d.name);
          },
          d.name,
        );
      const running = d.state.toLowerCase() === "running";
      const power = running
        ? iconBtn({ "data-testid": `btn-machine-stop-${d.name}` }, "stop", t("gui.machines.stop"))
        : iconBtn({ "data-testid": `btn-machine-start-${d.name}` }, "play", t("gui.machines.start"));
      power.disabled = locked;
      power.onclick = () => void this.machinePower(d.name, running ? "off" : "on", d.active);
      // Engine 是所有 distro 共用的 dockerd：不能停止、不能刪除（只在它沒在跑時留「啟動」）
      const exposeRel = iconBtn({ "data-testid": `btn-machine-expose-release-${d.name}` }, "close", t("gui.machines.expose_release"));
      exposeRel.disabled = locked || this.st.busy.has(`expose:${d.name}`);
      exposeRel.onclick = () => void this.releaseExposureOf(d.name);
      tools.push(...(isEngine && running ? [] : [power]), ...(d.exposed ? [exposeRel] : []), cfg, folder, term, compact, ...(isEngine ? [] : [rm]));

      const footKids: HTMLElement[] = [];
      // engine／client 角色：沒有 engine 時可把任一機器設為 engine；有 engine 後其他機器可改為 client（共用 engine 的 docker）
      const internal = d.name.toLowerCase().startsWith("docker-desktop");
      if (!internal && d.role !== "engine" && d.role !== "client" && d.version === 2) {
        const role: "engine" | "client" = hasEngine ? "client" : "engine";
        const roleBtn = btn({ "data-testid": `btn-machine-role-${d.name}` }, "machines", t(role === "engine" ? "gui.machines.make_engine" : "gui.machines.make_client"));
        roleBtn.disabled = locked;
        roleBtn.onclick = () =>
          void this.resourceAction(
            `role:${d.name}`,
            `${t(role === "engine" ? "gui.machines.make_engine_prompt" : "gui.machines.make_client_prompt")}\n${d.name}`,
            async () => {
              await api.setMachineRole(d.name, role);
              this.st.meta = await api.meta();
              setMessages(this.st.meta.messages);
              return fmt(t("gui.machines.role_done"), d.name, t(role === "engine" ? "gui.machines.role_engine" : "gui.machines.role_client"));
            },
          );
        footKids.push(roleBtn);
      }
      footKids.push(el("span", { class: "spacer" }), ...tools);

      const flags: HTMLElement[] = [];
      if (d.exposed) flags.push(el("span", { class: "tag warn", "data-testid": "machine-flag-exposed", text: t("gui.machines.exposed"), title: t("gui.machines.exposed_tip") }));
      if (d.role === "engine") flags.push(el("span", { class: "tag accent", "data-testid": "machine-flag-engine", text: t("gui.machines.role_engine"), title: t("gui.machines.role_engine_tip") }));
      if (d.role === "client") flags.push(el("span", { class: "tag", "data-testid": "machine-flag-client", text: t("gui.machines.role_client"), title: t("gui.machines.role_client_tip") }));
      if (d.managed && !d.ready) flags.push(el("span", { class: "tag warn", "data-testid": "machine-flag-not-ready", text: t("status.not_ready") }));
      if (d.default) flags.push(el("span", { class: "tag", "data-testid": "machine-flag-default", text: t("gui.machines.default") }));
      if (d.managed === false) flags.push(el("span", { class: "tag warn", "data-testid": "machine-flag-unmanaged", text: t("gui.machines.unmanaged") }));
      const stateText =
        pending === "configuring"
          ? t("gui.machines.configuring")
          : pending === "removing"
          ? t("gui.machines.removing")
            : pending === "starting"
              ? t("gui.status.starting")
              : pending === "stopping"
                ? t("gui.ct.stopping")
                : pending === "resizing"
                  ? t("gui.machines.resizing")
                  : pending === "compacting"
                    ? t("gui.machines.compacting")
                    : d.state;
      const attrs: Record<string, string> = {
        class: `machine-card${d.active ? " active" : ""}`,
        "data-testid": "machine-row",
        "data-name": d.name,
        "data-state": pending ?? d.state.toLowerCase(),
        "data-active": d.active ? "1" : "0",
        "aria-label": d.name,
      };
      if (pending) attrs["data-pending"] = pending;
      const meta: (Node | string)[] = [
        el("span", { class: `state ${pending ? "pending" : d.state.toLowerCase()}`, "data-testid": "machine-state", text: stateText }),
        el("span", { class: "muted" }, " · WSL ", el("span", { "data-testid": "machine-version", text: String(d.version) })),
      ];
      if ((d.disk_bytes ?? 0) > 0) {
        meta.push(el("span", { class: "muted", title: t("gui.machines.disk_hint") }, ` · ${t("gui.machines.disk")} `,
          el("span", { "data-testid": "machine-disk", "data-size": String(d.disk_bytes), text: humanSize(d.disk_bytes ?? 0) }),
          ...((d.disk_limit_bytes ?? 0) > 0
            ? [" / ", el("span", { "data-testid": "machine-disk-limit", title: t("gui.machines.disk_limit"), text: humanSize(d.disk_limit_bytes ?? 0) })]
            : [])));
      }
      if (d.active && (d.api_endpoint || this.st.meta.api_endpoint)) {
        meta.push(el("span", { class: "muted mono", "data-testid": "machine-endpoint", text: ` · ${d.api_endpoint || this.st.meta.api_endpoint}` }));
      }
      grid.append(
        el("article", attrs,
          el("div", { class: "machine-head" },
            el("span", { class: "machine-icon", "aria-hidden": "true" }, distroLogo(d.name) ?? icon("machines")),
            el("div", { class: "machine-title" },
              el("div", { class: "machine-name-row" },
                el("h2", { class: "machine-name", "data-testid": "machine-name", text: d.name, title: d.name }),
                ...flags,
              ),
              el("div", { class: "machine-meta" }, ...meta),
            ),
          ),
          el("div", { class: "machine-foot" }, ...footKids),
        ),
      );
    }
    if (!engineGrid.hasChildNodes()) {
      list.replaceChildren(otherGrid); // 還沒有 engine（首次設定中）：維持單一清單
      return;
    }
    const group = (id: string, titleKey: string, g: HTMLElement) =>
      el("section", { class: "machine-group", "data-testid": `machines-group-${id}` }, el("h3", { class: "machine-group-title", text: t(titleKey) }), g);
    list.replaceChildren(
      group("engine", "gui.machines.group_engine", engineGrid),
      ...(otherGrid.hasChildNodes() ? [group("others", "gui.machines.group_others", otherGrid)] : []),
    );
  }

  /** 啟動／停止 distro。停止主要 distro 會連帶停掉 Docker，先確認。 */
  /** 呼叫 fn(release=false)；伺服器回 409 in_use（有執行中的容器在用這個 distro 的曝露檔案）時，列出容器請使用者確認，
   *  確認後以 release=true 重試（伺服器會先停那些容器再釋放曝露）。取消則回傳 CANCELLED。 */
  private async withExposureConfirm<T>(fn: (release: boolean) => Promise<T>): Promise<T | typeof CANCELLED> {
    try {
      return await fn(false);
    } catch (e) {
      if (!(e instanceof ApiError) || e.code !== "in_use") throw e;
      const cs = (e.detail.containers as { name: string }[] | undefined) ?? [];
      const names = cs.map((c) => c.name).join(", ");
      if (!(await this.confirm(fmt(t("gui.machines.expose_in_use"), names), undefined, { label: t("gui.machines.expose_stop_release"), icon: "stop" }))) return CANCELLED;
      return await fn(true);
    }
  }

  private async releaseExposureOf(name: string): Promise<void> {
    const key = `expose:${name}`;
    this.st.busy.add(key);
    this.renderViewBody();
    let err: string | undefined;
    try {
      const r = await this.withExposureConfirm((stop) => api.releaseExposure(name, stop));
      if (r !== CANCELLED) this.notify(fmt(t("gui.machines.expose_released"), name));
    } catch (e) {
      err = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.busy.delete(key);
    }
    await this.refreshKeeping(err);
  }

  private async machinePower(name: string, to: "on" | "off", primary: boolean): Promise<void> {
    if (to === "off" && primary) {
      const ok = await this.confirm(t("gui.machines.stop_primary_prompt"), undefined, { label: t("gui.machines.stop"), icon: "stop" });
      if (!ok) return;
    }
    const key = `power-${to}:${name}`;
    this.st.busy.add(key);
    this.renderViewBody();
    let err: string | undefined;
    try {
      if (to === "on") await api.startMachine(name);
      else await this.withExposureConfirm((release) => api.stopMachine(name, release));
    } catch (e) {
      err = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.busy.delete(key);
    }
    await this.refreshKeeping(err);
  }

  private hardCutActive(): void {
    this.closePanel();
    this.stopMachineUsagePoll();
    this.st.info = undefined;
    this.st.pending = {};
    this.st.containers = [];
    this.st.images = [];
    this.st.volumes = [];
    this.st.networks = [];
    this.st.disk = undefined;
  }

  private async openMachineInfo(name: string): Promise<void> {
    this.stopMachineUsagePoll();
    this.st.info = { kind: "machine", name };
    this.renderInfoDrawer();
    try {
      const detail = await api.machineConfig(name);
      if (this.st.info?.kind === "machine" && this.st.info.name === name) {
        this.st.info = { kind: "machine", name, detail };
        this.renderInfoDrawer();
        this.startMachineUsagePoll(name);
      }
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    }
  }

  private stopMachineUsagePoll(): void {
    if (this.usageTimer) {
      window.clearInterval(this.usageTimer);
      this.usageTimer = undefined;
    }
  }

  private startMachineUsagePoll(name: string): void {
    this.stopMachineUsagePoll();
    const sec = Math.max(1, this.st.meta.config.refresh_interval_sec || 3);
    this.usageTimer = window.setInterval(() => {
      if (this.st.info?.kind !== "machine" || this.st.info.name !== name) {
        this.stopMachineUsagePoll();
        return;
      }
      void this.pollMachineUsage(name);
    }, sec * 1000);
  }

  private async pollMachineUsage(name: string): Promise<void> {
    const shown = this.st.info?.kind === "machine" && this.st.info.name === name ? this.st.info.detail : undefined;
    const m = this.st.machines?.machines.find((x) => x.name === name);
    // 停止中且 Info 已顯示停止狀態：沒有用量可取，不打 API（避免任何會叫醒機器的取樣）
    if (m && m.state.toLowerCase() !== "running" && shown && !shown.running) return;
    try {
      const detail = await api.machineConfig(name);
      if (this.st.info?.kind !== "machine" || this.st.info.name !== name) return;
      // 啟動／停止後 wsl.conf 由不可讀變可讀（或反之）：重建 Info；否則只就地更新用量元件
      if (shown && !!shown.wsl_conf_unavailable !== !!detail.wsl_conf_unavailable) {
        this.st.info = { kind: "machine", name, detail };
        this.renderInfoDrawer();
        return;
      }
      this.updateMachineUsageBars(detail);
    } catch {
      // 輪詢失敗不打斷 Info；下次再試
    }
  }

  private updateMachineUsageBars(d: import("./api").MachineConfig): void {
    const cpu = this.root.querySelector<HTMLElement>('[data-testid="info-cpu-bar"]');
    const mem = this.root.querySelector<HTMLElement>('[data-testid="info-mem-bar"]');
    if (cpu) {
      const fill = cpu.querySelector<HTMLElement>(".usage-fill");
      const label = cpu.querySelector<HTMLElement>(".usage-value");
      const pct = clampPct(d.cpu_percent ?? 0);
      if (fill) fill.style.width = `${pct}%`;
      if (label) label.textContent = formatCPU(d.cpu_percent ?? 0);
      cpu.setAttribute("data-pct", String(pct));
    }
    if (mem) {
      const fill = mem.querySelector<HTMLElement>(".usage-fill");
      const label = mem.querySelector<HTMLElement>(".usage-value");
      const pct = memPct(d.memory_used_mb ?? 0, d.memory_total_mb ?? 0);
      if (fill) fill.style.width = `${pct}%`;
      if (label) label.textContent = formatMem(d.memory_used_mb ?? 0, d.memory_total_mb ?? 0);
      mem.setAttribute("data-pct", String(pct));
    }
    const disk = this.root.querySelector<HTMLElement>('[data-testid="info-disk-bar"]');
    if (disk && (d.disk_total_bytes ?? 0) > 0) {
      const pct = memPct(d.disk_used_bytes ?? 0, d.disk_total_bytes ?? 0);
      const fill = disk.querySelector<HTMLElement>(".usage-fill");
      const label = disk.querySelector<HTMLElement>(".usage-value");
      if (fill) fill.style.width = `${pct}%`;
      if (label) label.textContent = formatDisk(d.disk_used_bytes ?? 0, d.disk_total_bytes ?? 0);
      disk.setAttribute("data-pct", String(pct));
    }
  }

  private addMachineDistro?: Dropdown;

  private buildAddMachineDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "add-machine-dialog" }) as HTMLDialogElement;
    const sel = dropdown({ testid: "add-machine-select", labelledby: "add-machine-select-label" }, [], "", (v) => {
      if (v) name.value = v;
      syncInstallEnabled();
    });
    this.addMachineDistro = sel;
    const name = el("input", {
      type: "text",
      "data-testid": "add-machine-name",
      autocomplete: "off",
      spellcheck: "false",
    }) as HTMLInputElement;
    const user = el("input", {
      type: "text",
      class: "mono",
      "data-testid": "add-machine-user",
      autocomplete: "off",
      spellcheck: "false",
      autocapitalize: "off",
    }) as HTMLInputElement;
    const pw = el("input", { type: "password", "data-testid": "add-machine-password", autocomplete: "new-password" }) as HTMLInputElement;
    const pw2 = el("input", { type: "password", "data-testid": "add-machine-password2", autocomplete: "new-password" }) as HTMLInputElement;
    const vhd = el("input", {
      type: "text",
      inputmode: "numeric",
      class: "mono",
      "data-testid": "add-machine-vhd-size",
      autocomplete: "off",
      spellcheck: "false",
    }) as HTMLInputElement;
    const vhdErr = el("div", { class: "field-error", "data-testid": "add-machine-vhd-error", hidden: "" });
    const vhdHint = el("div", { class: "field-hint", "data-testid": "add-machine-vhd-hint", text: t("gui.machines.vhd_size_hint") });
    const userErr = el("div", { class: "field-error", "data-testid": "add-machine-user-error", hidden: "", text: t("gui.machines.user_invalid") });
    const pwErr = el("div", { class: "field-error", "data-testid": "add-machine-password-error", hidden: "" });
    const err = el("div", { class: "field-error", "data-testid": "add-machine-error", hidden: "" });
    const progress = installProgressBlock();
    const install = btn({ class: "primary", type: "submit", "data-testid": "add-machine-install" }, "plus", t("gui.machines.install"));
    // 取消放在標題列右上角；安裝開始後按鈕列整列隱藏（安裝無法中止），把高度讓給進度區塊
    const cancel = iconBtn({ type: "button", "data-testid": "add-machine-cancel" }, "close", t("gui.cancel"));
    const hint = el("div", { class: "field-hint", "data-testid": "add-machine-password-hint", text: t("gui.machines.password_hint") });
    const actions = el("div", { class: "row" }, install);
    // 與後端 wsl.ValidLinuxUsername／ValidLinuxPassword 一致
    const userOk = () => /^[a-z_][a-z0-9_-]{0,31}$/.test(user.value.trim()) && user.value.trim() !== "root";
    const pwProblem = (): string => {
      if (/[\r\n\0]/.test(pw.value)) return t("gui.machines.password_invalid");
      if (pw2.value !== "" && pw.value !== pw2.value) return t("gui.machines.password_mismatch");
      return "";
    };
    // 空 = WSL 預設（0）；否則須為範圍內整數，與後端 wsl.ValidVHDSizeGB 一致
    const vhdGB = (): number | undefined => parseVHDSize(vhd.value, this.vhdRange);
    const syncInstallEnabled = () => {
      const vBad = vhdGB() === undefined;
      vhd.setAttribute("aria-invalid", String(vBad));
      vhdErr.textContent = fmt(t("gui.machines.vhd_invalid"), String(this.vhdRange[0]), String(this.vhdRange[1]));
      vhdErr.hidden = !vBad;
      const uBad = user.value.trim() !== "" && !userOk();
      user.setAttribute("aria-invalid", String(uBad));
      userErr.hidden = !uBad;
      const p = pwProblem();
      pw2.setAttribute("aria-invalid", String(p !== ""));
      pwErr.textContent = p;
      pwErr.hidden = p === "";
      install.disabled =
        !sel.value.trim() ||
        !name.value.trim() ||
        !userOk() ||
        p !== "" ||
        pw.value !== pw2.value ||
        vBad ||
        this.st.busy.has("install");
    };
    vhd.oninput = () => syncInstallEnabled();
    name.oninput = () => syncInstallEnabled();
    user.oninput = () => syncInstallEnabled();
    pw.oninput = () => syncInstallEnabled();
    pw2.oninput = () => syncInstallEnabled();
    cancel.onclick = () => {
      if (this.st.busy.has("install")) return;
      d.close();
    };
    d.addEventListener("cancel", (ev) => {
      if (this.st.busy.has("install")) ev.preventDefault();
    });
    const form = el(
      "form",
      { method: "dialog", class: "add-machine-form" },
      // 下拉不能包在 <label> 裡（點清單項目會被轉成再點一次按鈕）
      el("div", { class: "field" }, el("span", { class: "field-label", id: "add-machine-select-label", text: t("gui.machines.select") }), sel.el),
      // 自訂名稱與磁碟上限同一列，不增加對話框高度（安裝中仍需放得下進度區塊）
      el("div", { class: "field-row" },
        el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.machines.name") }), textField(name)),
        el("label", { class: "field vhd" }, el("span", { class: "field-label", text: t("gui.machines.vhd_size") }), textField(vhd)),
      ),
      vhdErr,
      vhdHint,
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.machines.linux_user") }), textField(user), userErr),
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.machines.password") }), textField(pw)),
      el(
        "label",
        { class: "field" },
        el("span", { class: "field-label", text: t("gui.machines.password_confirm") }),
        textField(pw2),
        pwErr,
      ),
      hint,
      progress,
      err,
      actions,
    );
    form.noValidate = true;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const dist = sel.value.trim();
      const nm = name.value.trim();
      const usr = user.value.trim();
      const size = vhdGB();
      if (!dist || !nm || !userOk() || pwProblem() !== "" || pw.value !== pw2.value || size === undefined || this.st.busy.has("install")) return;
      const fields = [name, user, pw, pw2, vhd];
      install.disabled = true;
      cancel.disabled = true;
      sel.setDisabled(true);
      for (const f of fields) f.disabled = true;
      hint.hidden = true;
      vhdHint.hidden = true;
      actions.hidden = true;
      err.hidden = true;
      this.st.busy.add("install");
      this.renderViewBody();
      // wsl.exe 在 pipe 下不輸出進度：輪詢後端觀察到的下載量
      let polling = true;
      updateInstallProgress(progress, { active: true, phase: "preparing", downloaded: 0, total: 0, bytes_per_sec: 0, elapsed_sec: 0 });
      progress.hidden = false;
      const poll = async () => {
        try {
          const p = await api.installProgress();
          if (polling && p.active) updateInstallProgress(progress, p);
        } catch {
          /* 進度僅供參考，失敗不影響安裝 */
        }
      };
      void poll();
      const timer = window.setInterval(poll, 1000);
      try {
        const res = await api.installMachine(dist, nm, usr, pw.value, size);
        // 密碼不留在 DOM：成功即清掉
        pw.value = "";
        pw2.value = "";
        d.close();
        this.notify(
          res.user && !res.password_set
            ? fmt(t("gui.machines.installed_no_password"), nm, res.user)
            : fmt(t("gui.machines.installed"), nm),
        );
        await this.refresh();
      } catch (e) {
        err.textContent = e instanceof ApiError ? e.message : String(e);
        err.hidden = false;
      } finally {
        polling = false;
        window.clearInterval(timer);
        progress.hidden = true;
        this.st.busy.delete("install");
        cancel.disabled = false;
        sel.setDisabled(false);
        for (const f of fields) f.disabled = false;
        hint.hidden = false;
        vhdHint.hidden = false;
        actions.hidden = false;
        syncInstallEnabled();
        this.renderViewBody();
      }
    };
    d.append(el("div", { class: "dialog-head" }, el("h2", { text: t("gui.machines.add") }), cancel), form);
    return d;
  }

  private async openAddMachine(): Promise<void> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="add-machine-dialog"]')!;
    const sel = this.addMachineDistro!;
    const name = d.querySelector<HTMLInputElement>('[data-testid="add-machine-name"]')!;
    const user = d.querySelector<HTMLInputElement>('[data-testid="add-machine-user"]')!;
    const pws = d.querySelectorAll<HTMLInputElement>('[data-testid="add-machine-password"], [data-testid="add-machine-password2"]');
    const vhd = d.querySelector<HTMLInputElement>('[data-testid="add-machine-vhd-size"]')!;
    const err = d.querySelector<HTMLElement>('[data-testid="add-machine-error"]')!;
    const install = d.querySelector<HTMLButtonElement>('[data-testid="add-machine-install"]')!;
    const cancel = d.querySelector<HTMLButtonElement>('[data-testid="add-machine-cancel"]')!;
    err.hidden = true;
    name.value = "";
    name.disabled = true;
    user.value = "";
    user.disabled = true;
    user.setAttribute("aria-invalid", "false");
    d.querySelector<HTMLElement>('[data-testid="add-machine-user-error"]')!.hidden = true;
    d.querySelector<HTMLElement>('[data-testid="add-machine-password-error"]')!.hidden = true;
    for (const p of pws) {
      p.value = "";
      p.disabled = true;
      p.setAttribute("aria-invalid", "false");
    }
    vhd.value = String(DEFAULT_VHD_GB);
    vhd.disabled = true;
    vhd.setAttribute("aria-invalid", "false");
    d.querySelector<HTMLElement>('[data-testid="add-machine-vhd-error"]')!.hidden = true;
    cancel.disabled = false;
    sel.setOptions([], "", t("gui.machines.loading"));
    sel.setDisabled(true);
    install.disabled = true;
    d.showModal();
    try {
      const [online, defaults] = await Promise.all([
        api.machinesOnline(),
        api.installDefaults().catch((): InstallDefaults => ({ user: "" })),
      ]);
      user.value = defaults.user || "";
      if (defaults.vhd_min_gb && defaults.vhd_max_gb) this.vhdRange = [defaults.vhd_min_gb, defaults.vhd_max_gb];
      vhd.value = defaults.vhd_size_gb ? String(defaults.vhd_size_gb) : "";
      sel.setOptions(
        online.map((o) => [o.name, o.friendly && o.friendly !== o.name ? `${o.friendly} (${o.name})` : o.name] as const),
        "",
        t("gui.machines.select_placeholder"),
      );
      sel.setDisabled(false);
      name.disabled = false;
      user.disabled = false;
      vhd.disabled = false;
      for (const p of pws) p.disabled = false;
      install.disabled = true;
    } catch (e) {
      sel.setOptions([], "", t("gui.machines.select_placeholder"));
      err.textContent = e instanceof ApiError ? e.message : String(e);
      err.hidden = false;
    }
  }

  /** 釋放磁碟空間（wsl --manage --compact）。執行中（或後端回 needs_shutdown）時，先確認關閉 WSL 並列出會被停止的機器。 */
  private async compactMachine(name: string, running: boolean): Promise<void> {
    const runningNow = () => (this.st.machines?.machines ?? []).filter((m) => m.state.toLowerCase() === "running").map((m) => m.name);
    const confirmShutdown = (willStop: string[]) =>
      this.confirm(
        `${fmt(t("gui.machines.compact_prompt"), name)}\n\n${t("gui.machines.disk_needs_shutdown")}\n${
          willStop.length > 0 ? fmt(t("gui.machines.resize_will_stop"), willStop.join("、")) : t("gui.machines.resize_none_running")
        }`,
        undefined,
        { label: t("gui.machines.compact_shutdown_apply"), icon: "shrink", danger: true },
      );
    let consent = false;
    if (running) {
      if (!(await confirmShutdown(runningNow()))) return;
      consent = true;
    }
    const key = `compact:${name}`;
    let err: string | undefined;
    for (;;) {
      this.st.busy.add(key);
      // Info 輪詢會在 distro 內執行命令，把剛關掉的 WSL 叫醒：作業期間暫停
      this.stopMachineUsagePoll();
      this.renderViewBody();
      try {
        const res = await api.compactMachine(name, consent);
        const freed = res.before_bytes - res.after_bytes;
        this.notify(
          freed > 0
            ? fmt(t("gui.machines.compacted"), name, humanSize(freed), humanSize(res.after_bytes))
            : fmt(t("gui.machines.compact_nothing"), name, humanSize(res.after_bytes)),
          { sticky: true },
        );
        break;
      } catch (e) {
        this.st.busy.delete(key);
        if (e instanceof ApiError && e.code === "needs_shutdown" && !consent) {
          const list = Array.isArray(e.detail.running) ? e.detail.running.map(String) : runningNow();
          if (await confirmShutdown(list)) {
            consent = true;
            continue;
          }
          break;
        }
        err = e instanceof ApiError ? e.message : String(e);
        break;
      } finally {
        this.st.busy.delete(key);
      }
    }
    await this.refreshKeeping(err);
    if (this.st.info?.kind === "machine") await this.openMachineInfo(this.st.info.name);
  }

  // ---- 磁碟上限 ---------------------------------------------------------------

  /** 磁碟上限允許範圍（GB）；由 /api/machines/install/defaults 更新 */
  private vhdRange: [number, number] = [VHD_MIN_GB, VHD_MAX_GB];
  /** needShutdown：需經 wsl --shutdown 才能調整（目標執行中，或後端回 needs_shutdown）；willStop：會被停止的機器 */
  private resizeTarget?: { name: string; currentGB: number; running: boolean; needShutdown: boolean; willStop: string[]; minGB: number };
  private resizeSync?: () => void;

  /** Info 的「磁碟上限」欄：容量＋調整鈕。 */
  private diskLimitField(name: string, d: MachineConfig): HTMLElement {
    const limit = d.disk_limit_bytes ?? 0;
    const m = this.st.machines?.machines.find((x) => x.name === name);
    const edit = iconBtn({ "data-testid": "btn-machine-resize" }, "edit", t("gui.machines.resize"));
    edit.disabled = this.st.busy.has(`resize:${name}`);
    edit.onclick = () => this.openResize(name, limit, (m?.state ?? "").toLowerCase() === "running");
    return el("div", { class: "info-field" },
      el("dt", { text: t("gui.machines.disk_limit"), title: t("gui.machines.disk_limit_hint") }),
      el("dd", { class: "with-action" },
        el("span", { "data-testid": "info-disk-limit", text: limit > 0 ? humanSize(limit) : "—" }),
        edit,
      ),
    );
  }

  private buildResizeDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "resize-dialog" }) as HTMLDialogElement;
    const title = el("h2", { "data-testid": "resize-title" });
    const size = el("input", {
      type: "text",
      inputmode: "numeric",
      class: "mono",
      "data-testid": "resize-size",
      autocomplete: "off",
      spellcheck: "false",
    }) as HTMLInputElement;
    const err = el("div", { class: "field-error", "data-testid": "resize-error", hidden: "" });
    const running = el("p", { class: "field-hint", "data-testid": "resize-running", text: t("gui.machines.resize_running") });
    const willStop = el("strong", { "data-testid": "resize-will-stop" });
    const shutdownBox = el("div", { class: "resize-shutdown", "data-testid": "resize-shutdown", role: "note" },
      el("p", { text: t("gui.machines.resize_needs_shutdown") }),
      el("p", {}, willStop),
    );
    const shrink = el("p", { class: "field-hint warn", "data-testid": "resize-shrink", text: t("gui.machines.resize_shrink") });
    const minHint = el("div", { class: "field-hint", "data-testid": "resize-min", hidden: "" });
    const apply = btn({ class: "primary", type: "submit", "data-testid": "resize-apply" }, "save", t("gui.machines.resize_apply"));
    const cancel = btn({ type: "button", "data-testid": "resize-cancel" }, "close", t("gui.cancel"));
    const busy = () => !!this.resizeTarget && this.st.busy.has(`resize:${this.resizeTarget.name}`);
    const applyLabel = () =>
      busy() ? t("gui.machines.resizing") : this.resizeTarget?.needShutdown ? t("gui.machines.resize_shutdown_apply") : t("gui.machines.resize_apply");
    const sync = () => {
      const tg = this.resizeTarget;
      const n = parseVHDSize(size.value, this.vhdRange);
      const invalid = size.value.trim() === "" || n === undefined;
      // 小於檔案系統所需：關閉 WSL 之前就擋下
      const tooSmall = !invalid && !!tg && tg.minGB > 0 && (n ?? 0) < tg.minGB;
      const bad = invalid || tooSmall;
      size.setAttribute("aria-invalid", String(bad));
      if (invalid) {
        err.textContent = fmt(t("gui.machines.vhd_invalid"), String(this.vhdRange[0]), String(this.vhdRange[1]));
        err.hidden = false;
      } else if (tooSmall && tg) {
        err.textContent = fmt(t("gui.machines.resize_too_small"), tg.name, String(n), String(tg.minGB));
        err.hidden = false;
      } else if (err.dataset.kind !== "server") {
        err.hidden = true;
      }
      minHint.hidden = !tg || tg.minGB <= 0;
      if (tg && tg.minGB > 0) minHint.textContent = fmt(t("gui.machines.resize_min_hint"), String(tg.minGB));
      running.hidden = !tg?.running;
      shutdownBox.hidden = !tg?.needShutdown;
      willStop.textContent = tg && tg.willStop.length > 0
        ? fmt(t("gui.machines.resize_will_stop"), tg.willStop.join("、"))
        : t("gui.machines.resize_none_running");
      shrink.hidden = bad || !tg || tg.currentGB <= 0 || (n ?? 0) >= tg.currentGB;
      apply.disabled = bad || busy() || (!!tg && n === tg.currentGB);
      apply.classList.toggle("danger", !!tg?.needShutdown);
      apply.querySelector(".label")!.textContent = applyLabel();
    };
    size.oninput = () => {
      delete err.dataset.kind;
      sync();
    };
    cancel.onclick = () => {
      if (!busy()) d.close();
    };
    d.addEventListener("cancel", (ev) => {
      if (busy()) ev.preventDefault();
    });
    const form = el("form", { method: "dialog", class: "add-machine-form" },
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.machines.vhd_size") }), textField(size), err),
      minHint,
      running,
      shutdownBox,
      shrink,
      el("div", { class: "row" }, cancel, apply),
    );
    form.noValidate = true;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const tg = this.resizeTarget;
      const n = parseVHDSize(size.value, this.vhdRange);
      if (!tg || !n || busy()) return;
      const key = `resize:${tg.name}`;
      this.st.busy.add(key);
      // Info 輪詢會在 distro 內執行命令，把剛關掉的 WSL 叫醒：調整期間暫停
      this.stopMachineUsagePoll();
      size.disabled = true;
      cancel.disabled = true;
      err.hidden = true;
      delete err.dataset.kind;
      sync();
      this.renderViewBody();
      try {
        const res = await api.resizeMachine(tg.name, n, tg.needShutdown);
        this.st.busy.delete(key);
        d.close();
        const freed = (res.before_bytes ?? 0) - (res.after_bytes ?? 0);
        this.notify(
          res.compacted && freed > 0
            ? fmt(t("gui.machines.resized_freed"), tg.name, String(n), humanSize(freed))
            : fmt(t("gui.machines.resized"), tg.name, String(n)),
          { sticky: true },
        );
      } catch (e) {
        this.st.busy.delete(key);
        if (e instanceof ApiError && e.code === "too_small" && typeof e.detail.min_gb === "number") tg.minGB = e.detail.min_gb;
        if (e instanceof ApiError && e.code === "needs_shutdown") {
          // 第二步：讓使用者看過會被停止的機器後，再按一次「關閉 WSL 並套用」
          tg.needShutdown = true;
          const list = e.detail.running;
          if (Array.isArray(list)) tg.willStop = list.map(String);
        } else {
          err.textContent = e instanceof ApiError ? e.message : String(e);
          err.dataset.kind = "server";
          err.hidden = false;
        }
      } finally {
        this.st.busy.delete(key);
        size.disabled = false;
        cancel.disabled = false;
        sync();
        await this.refresh();
        if (this.st.info?.kind === "machine" && this.st.info.name === tg.name) await this.openMachineInfo(tg.name);
        this.renderViewBody();
      }
    };
    this.resizeSync = sync;
    d.append(el("div", { class: "dialog-head" }, title), form);
    return d;
  }

  private openResize(name: string, limitBytes: number, running: boolean): void {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="resize-dialog"]')!;
    const currentGB = limitBytes > 0 ? Math.round(limitBytes / 2 ** 30) : 0;
    const willStop = (this.st.machines?.machines ?? []).filter((m) => m.state.toLowerCase() === "running").map((m) => m.name);
    this.resizeTarget = { name, currentGB, running, needShutdown: running, willStop, minGB: 0 };
    d.querySelector('[data-testid="resize-title"]')!.textContent = `${t("gui.machines.resize")} · ${name}`;
    const size = d.querySelector<HTMLInputElement>('[data-testid="resize-size"]')!;
    size.value = currentGB > 0 ? String(currentGB) : "";
    const err = d.querySelector<HTMLElement>('[data-testid="resize-error"]')!;
    delete err.dataset.kind;
    err.hidden = true;
    this.resizeSync?.();
    if (size.value === "") err.hidden = true; // 尚未輸入時不先顯示錯誤
    d.showModal();
    size.focus();
    size.select();
    // 執行中：向後端要檔案系統能縮到的最小值（resize2fs -P，約 1 秒）
    if (running) {
      const tg = this.resizeTarget;
      void api.resizeInfo(name).then((info) => {
        if (this.resizeTarget !== tg || !tg) return;
        tg.minGB = info.min_gb || 0;
        this.resizeSync?.();
      }).catch(() => {
        /* 估不出來就交給 WSL 判斷 */
      });
    }
  }

  // ---- 確認對話框 -------------------------------------------------------------

  private confirmResolve?: (ok: boolean) => void;

  private buildConfirmDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "confirm-dialog" }) as HTMLDialogElement;
    const typed = el("input", {
      type: "text",
      class: "mono",
      "data-testid": "confirm-typed",
      autocomplete: "off",
      spellcheck: "false",
    }) as HTMLInputElement;
    const match = icon("check");
    match.classList.add("match");
    match.setAttribute("data-testid", "confirm-match");
    // 外框跟著 input 顯示／隱藏；名稱完全相符時顯示綠色勾
    const typedField = textField(typed, match);
    typedField.classList.add("confirm-field");
    typedField.setAttribute("data-testid", "confirm-typed-field");
    typedField.hidden = true;
    const ok = btn({ class: "primary danger", type: "button", "data-testid": "confirm-ok" }, "trash", t("gui.confirm"));
    const cancel = btn({ type: "button", "data-testid": "confirm-cancel" }, "close", t("gui.cancel"));
    ok.onclick = () => {
      d.close();
      this.confirmResolve?.(true);
      this.confirmResolve = undefined;
    };
    cancel.onclick = () => d.close();
    d.addEventListener("close", () => {
      this.confirmResolve?.(false);
      this.confirmResolve = undefined;
    });
    typed.oninput = () => {
      const need = typed.dataset.require || "";
      if (need) ok.disabled = typed.value !== need;
      typedField.classList.toggle("matched", !!need && typed.value === need);
    };
    d.append(el("p", { "data-testid": "confirm-text" }), typedField, el("div", { class: "row" }, cancel, ok));
    return d;
  }

  private confirm(
    text: string,
    requireType?: string,
    okOpts: { label?: string; icon?: IconName; danger?: boolean } = {},
  ): Promise<boolean> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="confirm-dialog"]')!;
    d.querySelector('[data-testid="confirm-text"]')!.textContent = text;
    const typed = d.querySelector<HTMLInputElement>('[data-testid="confirm-typed"]')!;
    const ok = d.querySelector<HTMLButtonElement>('[data-testid="confirm-ok"]')!;
    // 確定鈕預設為刪除樣式；非破壞性動作（例如區網存取）可換文字／圖示
    ok.replaceChildren(icon(okOpts.icon ?? "trash"), el("span", { class: "label", text: okOpts.label ?? t("gui.confirm") }));
    ok.classList.toggle("danger", okOpts.danger ?? true);
    const typedField = d.querySelector<HTMLElement>('[data-testid="confirm-typed-field"]')!;
    typed.value = "";
    typedField.classList.remove("matched");
    if (requireType) {
      typedField.hidden = false;
      typed.dataset.require = requireType;
      typed.placeholder = requireType;
      ok.disabled = true;
    } else {
      typedField.hidden = true;
      delete typed.dataset.require;
      typed.placeholder = "";
      ok.disabled = false;
    }
    return new Promise((resolve) => {
      this.confirmResolve = resolve;
      d.showModal();
      if (requireType) typed.focus();
    });
  }

  // ---- Kubernetes（本機 k3s）------------------------------------------------

  private k8sPollTimer?: number;
  private k8sListError?: string;
  private k8sAddContext: boolean = loadJSON<boolean>(LS_K8S_ADDCTX, true);
  /** Ready 時的主視圖：Apps（預設）或 Namespaces（進階：namespace 卡片與側欄分頁） */
  private k8sView: "apps" | "namespaces" = loadJSON<string>(LS_K8S_VIEW, "apps") === "namespaces" ? "namespaces" : "apps";
  private k8sShowSystem: boolean = loadJSON<boolean>(LS_K8S_SHOWSYS, false);
  private k8sExpandedApps = new Set<string>();
  private k8sGroup: K8sGroup = loadJSON<string>(LS_K8S_GROUP, "workloads") === "system" ? "system" : "workloads";

  /** /api/k8s 狀態；k3s 就緒（且沒有進行中的作業）時再載入 namespace 與目前分頁的資源。 */
  private async loadKubernetes(signal?: AbortSignal): Promise<void> {
    const info = await api.k8s(signal);
    if (signal?.aborted) return;
    this.st.k8s = info;
    this.k8sListError = undefined;
    if (this.k8sNeedsPoll()) this.pollK8sWhilePending();
    if (!info.enabled || info.status.state !== "ready" || info.op) {
      this.st.k8sNamespaces = [];
      this.st.k8sPods = [];
      this.st.k8sDeployments = [];
      this.st.k8sServices = [];
      this.st.k8sExtras = emptyK8sExtras();
      return;
    }
    try {
      // 卡片與架構圖都需要全部 namespace 的資源；側欄的表格再依 namespace 過濾。
      // 每個請求都是一次 wsl.exe + kubectl，全部並行（namespace 清單不依賴其他結果）。
      // extras（ConfigMap／PVC 等）一律走 kubectl，慢又只是附加資訊：不讓它拖住 Apps／Pods 的顯示，晚到再補畫。
      const extras = api.k8sExtras("", signal);
      extras.catch(() => {});
      [this.st.k8sNamespaces, this.st.k8sPods, this.st.k8sDeployments, this.st.k8sServices] = await Promise.all([api.k8sNamespaces(signal), api.k8sPods("", signal), api.k8sDeployments("", signal), api.k8sServices("", signal)]);
      void extras.then((x) => {
        if (signal?.aborted || this.st.view !== "kubernetes") return;
        this.st.k8sExtras = x;
        this.renderKubernetes();
      }).catch(() => {});
      if (this.st.k8sNs && !this.st.k8sNamespaces.includes(this.st.k8sNs)) this.st.k8sNs = "";
      if (this.st.k8sDetail) {
        const d = this.st.k8sDetail;
        this.st.k8sDetailData = await api.k8sDeploymentDetail(d.ns, d.name, signal).catch(() => this.st.k8sDetailData);
      }
    } catch (e) {
      if (signal?.aborted || (e instanceof DOMException && e.name === "AbortError")) throw e;
      this.k8sListError = e instanceof ApiError ? e.message : String(e);
    }
  }

  /** 作業進行中，或 k3s 已啟動但 node 尚未 Ready（state=starting）時都需要輪詢。 */
  private k8sNeedsPoll(): boolean {
    const k = this.st.k8s;
    return !!k && (!!k.op || (k.enabled && k.status.state === "starting"));
  }

  /** 背景作業（啟動／停止／重置）進行中時加快輪詢，完成後自動停止。 */
  private pollK8sWhilePending(): void {
    if (this.k8sPollTimer !== undefined) return;
    this.k8sPollTimer = window.setTimeout(async () => {
      this.k8sPollTimer = undefined;
      if (this.st.view !== "kubernetes") return;
      await this.refresh();
      if (this.k8sNeedsPoll()) this.pollK8sWhilePending();
    }, 700);
  }

  private k8sSubtitle(): string {
    const k = this.st.k8s;
    if (!k) return "";
    if (k.op) return t(`gui.k8s.op.${k.op}`);
    const label = t(`gui.k8s.state.${k.status.state}`);
    return k.status.version ? `${label} · ${k.status.version}` : label;
  }

  private activeDistro(): string {
    return this.st.status?.Distro || this.st.meta.distro || "";
  }

  /** 啟動／停止：後端以背景作業進行，回應已含 op；之後輪詢到完成。 */
  private async k8sRun(fn: () => Promise<K8sInfo>): Promise<void> {
    let err: string | undefined;
    try {
      this.st.k8s = await fn();
      this.st.lastError = undefined;
    } catch (e) {
      err = e instanceof ApiError ? e.message : String(e);
    }
    this.renderPageHead();
    this.renderKubernetes();
    if (err) {
      this.st.lastError = err;
      this.renderBanner();
      return;
    }
    this.pollK8sWhilePending();
  }

  /** 不需確認的資源動作（restart／scale）：標 busy → 執行 → 通知 → 刷新。 */
  private async k8sMutate(key: string, fn: () => Promise<void>, doneMsg: string): Promise<void> {
    this.st.busy.add(key);
    this.renderKubernetes();
    let err: string | undefined;
    try {
      await fn();
      this.notify(doneMsg);
    } catch (e) {
      err = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.busy.delete(key);
    }
    await this.refreshKeeping(err);
  }

  private renderKubernetes(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="kubernetes-list"]');
    const top = this.root.querySelector<HTMLElement>('[data-testid="k8s-top"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !top || !bar) return;
    // 使用者正在操作清單內的輸入（replicas 欄位、namespace 下拉展開）時不重建，避免每次輪詢吃掉輸入
    const ae = document.activeElement;
    const sideEl = this.root.querySelector<HTMLElement>('[data-testid="k8s-side"]');
    if (ae && (list.contains(ae) || sideEl?.contains(ae)) && (ae.tagName === "INPUT" || ae.getAttribute("aria-expanded") === "true")) return;
    const k = this.st.k8s;
    const op = k?.op;
    const state = k?.status.state;
    const running = state === "ready" || state === "starting";
    const busyOp = !!op;

    const kube = btn({ "data-testid": "btn-k8s-kubeconfig" }, "download", t("gui.k8s.kubeconfig"));
    // 匯出只是讀機器內的檔案：不看 k3s 是否就緒／啟動中／已關閉，只在作業進行中、尚未安裝或機器停止時停用。
    kube.disabled = !k?.machine || busyOp || state === "not-installed" || state === "stopped";
    kube.onclick = () => void this.k8sExportKubeconfig();
    const reset = btn({ "data-testid": "btn-k8s-reset" }, "reset", t("gui.k8s.reset"));
    reset.disabled = !k?.enabled || busyOp;
    reset.onclick = () => {
      void this.confirm(fmt(t("gui.k8s.confirm.reset"), k?.machine ?? ""), undefined, { label: t("gui.k8s.reset"), icon: "reset" }).then((ok) => {
        if (ok) void this.k8sRun(() => api.k8sReset());
      });
    };
    const toggle = running
      ? btn({ "data-testid": "btn-k8s-stop" }, "stop", t("gui.k8s.stop"))
      : btn({ class: "primary", "data-testid": "btn-k8s-start" }, "play", t("gui.k8s.start"));
    toggle.disabled = !k || busyOp;
    toggle.onclick = () => void this.k8sRun(() => (running ? api.k8sOff() : api.k8sOn({ addContext: this.k8sAddContext })));
    // 關閉狀態由頁面中央的 hero 負責啟用，工具列不再放第二個啟動鈕
    const heroMode = !!k && !k.op && !k.enabled;
    const ready = !!k && k.enabled && state === "ready" && !busyOp;
    const deploy = btn({ "data-testid": "btn-k8s-deploy" }, "plus", t("gui.k8s.deploy"));
    deploy.onclick = () => void this.openDeploy();
    const stack = btn({ "data-testid": "btn-k8s-import" }, "layers", t("gui.k8s.import"));
    stack.onclick = () => void this.openImport();
    const copyCtx = btn({ "data-testid": "btn-k8s-copy-context" }, "kubernetes", t("gui.k8s.copy_context"));
    copyCtx.title = k?.status.context ?? "";
    copyCtx.onclick = () => void this.k8sCopyContext();
    bar.replaceChildren(
      el("span", { class: "spacer" }),
      ...(ready ? [deploy, stack, copyCtx] : []),
      kube,
      reset,
      ...(heroMode ? [] : [toggle]),
    );

    if (!k) {
      top.replaceChildren(this.skeletonK8sCard());
      list.style.display = "none";
      list.replaceChildren();
      this.renderK8sSide();
      return;
    }
    top.replaceChildren(heroMode ? this.k8sHero(k) : this.k8sCard(k));
    // 沒有資源可列時（未安裝／已關閉／啟動中）不顯示空白的列表面板
    if (k.enabled && state === "ready" && !op) {
      list.style.display = "";
      list.classList.add("cards"); // 容器透明，Apps 卡片與 Namespaces 面板各自浮在頁面上
      list.replaceChildren(this.k8sResources());
    } else {
      list.style.display = "none";
      list.replaceChildren();
    }
    this.renderK8sSide();
  }

  private async k8sExportKubeconfig(): Promise<void> {
    try {
      const r = await api.k8sKubeconfig();
      this.notify(fmt(t("gui.k8s.kubeconfig_done"), r.path, r.context));
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    }
  }

  /** 叢集資訊卡：狀態徽章＋欄位＋提示（未安裝／已關閉／另一台持有／錯誤）。 */
  private k8sCard(k: K8sInfo): HTMLElement {
    const s = k.status;
    const stateKey = k.op ? `op.${k.op}` : `state.${s.state}`;
    const cls = k.op || s.state === "starting" || s.state === "unknown" ? "pending" : s.state === "ready" ? "running" : s.state === "error" ? "failed" : "exited";
    const badge = el("span", { class: `state ${cls}`, "data-testid": "k8s-state", "data-state": k.op ?? s.state, text: t(`gui.k8s.${stateKey}`) });
    const fields: [string, string, string][] = [["machine", t("gui.k8s.f.machine"), k.machine]];
    if (s.state === "ready") {
      fields.push(["version", t("gui.k8s.f.version"), s.version ?? ""]);
      fields.push(["node", t("gui.k8s.f.node"), [s.node_name, s.node_ip].filter(Boolean).join(" · ")]);
      fields.push(["api", t("gui.k8s.f.api"), s.endpoint]);
      fields.push(["context", t("gui.k8s.f.context"), s.context]);
      if (s.memory_bytes) fields.push(["memory", t("gui.k8s.f.memory"), humanSize(s.memory_bytes)]);
    }
    const grid = el("dl", { class: "k8s-fields", "data-testid": "k8s-fields" });
    for (const [key, label, value] of fields) {
      grid.append(el("div", { class: "k8s-f" }, el("dt", { text: label }), el("dd", { "data-testid": `k8s-f-${key}`, class: key === "machine" ? "" : "mono", text: value, title: value })));
    }
    const card = el("section", { class: "k8s-card", "data-testid": "k8s-card" }, el("div", { class: "k8s-head" }, badge), grid);

    const hint = (text: string, testid = "k8s-hint", cls2 = "k8s-hint") => card.append(el("p", { class: cls2, "data-testid": testid, text }));
    if (!k.op) {
      if (s.state === "not-installed") hint(fmt(t("gui.k8s.hint.not_installed"), k.machine));
      else if (s.state === "off") hint(fmt(t("gui.k8s.hint.off"), k.machine));
      else if (s.state === "stopped") hint(fmt(t("gui.k8s.hint.stopped"), k.machine));
      else if (s.state === "error") hint(fmt(t("gui.k8s.hint.error"), k.machine), "k8s-hint", "k8s-hint bad");
    } else if (k.op === "starting" && s.state === "not-installed") {
      hint(fmt(t("gui.k8s.hint.installing"), k.machine));
    }
    if (k.op === "starting") card.append(this.k8sSteps(k));
    if (s.ip_mismatch) hint(t("gui.k8s.hint.ip_mismatch"), "k8s-ip-mismatch", "k8s-hint bad");
    if (k.error) hint(k.error, "k8s-error", "k8s-hint bad");
    if (k.warning) hint(k.warning, "k8s-warning", "k8s-hint warn");
    return card;
  }

  /** 啟用步驟清單：完成 ✓、進行中 …、之後 ○；失敗時標出停在哪一步。 */
  private k8sSteps(k: K8sInfo): HTMLElement {
    const steps: K8sStep[] = (k.steps ?? []).filter((x) => x !== "context" || this.k8sAddContext);
    const cur = k.op === "starting" ? steps.indexOf(k.step ?? "engine") : k.error_step ? steps.indexOf(k.error_step) : -1;
    const failed = !k.op && !!k.error;
    const list = el("ol", { class: "k8s-steps", "data-testid": "k8s-steps" });
    steps.forEach((step, i) => {
      const status = failed ? (i < cur ? "done" : i === cur ? "failed" : "todo") : i < cur ? "done" : i === cur ? "active" : "todo";
      list.append(el("li", { class: `k8s-step ${status}`, "data-testid": `k8s-step-${step}`, "data-status": status },
        el("span", { class: "k8s-step-mark", "aria-hidden": "true", text: status === "done" ? "✓" : status === "failed" ? "✕" : status === "active" ? "…" : "○" }),
        el("span", { text: t(`gui.k8s.step.${step}`) }),
      ));
    });
    return list;
  }

  /** 關閉狀態的 hero：說明、啟用鈕、選項；失敗時顯示停在哪一步並改為 Retry。 */
  private k8sHero(k: K8sInfo): HTMLElement {
    const s = k.status;
    const failed = !!k.error;
    const enable = btn({ class: "primary", "data-testid": "btn-k8s-start" }, "play", failed ? t("gui.k8s.retry") : t("gui.k8s.hero.enable"));
    enable.onclick = () => void this.k8sRun(() => api.k8sOn({ addContext: this.k8sAddContext }));
    const ctx = el("input", { type: "checkbox", "data-testid": "k8s-opt-add-context" }) as HTMLInputElement;
    ctx.checked = this.k8sAddContext;
    ctx.onchange = () => {
      this.k8sAddContext = ctx.checked;
      saveJSON(LS_K8S_ADDCTX, ctx.checked);
    };
    const hero = el("section", { class: "k8s-hero", "data-testid": "k8s-hero", "data-state": s.state },
      icon("kubernetes"),
      el("h2", { text: t("gui.k8s.hero.title") }),
      el("p", { class: "k8s-hero-body", text: t("gui.k8s.hero.body") }),
      enable,
      el("p", { class: "k8s-hero-facts", text: t("gui.k8s.hero.facts") }),
      el("label", { class: "k8s-opt" }, ctx, el("span", { text: t("gui.k8s.opt.add_context") })),
      el("p", { class: "k8s-hero-facts", "data-testid": "k8s-f-machine", text: fmt(t("gui.k8s.hero.machine"), k.machine) }),
    );
    const hint = (text: string, testid = "k8s-hint", cls = "k8s-hint") => hero.append(el("p", { class: cls, "data-testid": testid, text }));
    if (s.state === "not-installed") hint(fmt(t("gui.k8s.hint.not_installed"), k.machine));
    else if (s.state === "off") hint(fmt(t("gui.k8s.hint.off"), k.machine));
    else if (s.state === "stopped") hint(fmt(t("gui.k8s.hint.stopped"), k.machine));
    else if (s.state === "error") hint(fmt(t("gui.k8s.hint.error"), k.machine), "k8s-hint", "k8s-hint bad");
    if (failed) {
      hint(k.error ?? "", "k8s-error", "k8s-hint bad");
      if (k.error_step) hint(fmt(t("gui.k8s.failed_at"), t(`gui.k8s.step.${k.error_step}`)), "k8s-failed-step", "k8s-hint bad");
    }
    if (k.warning) hint(k.warning, "k8s-warning", "k8s-hint warn");
    return hero;
  }

  /** Ready 但還沒有任何使用者 workload：邀請部署第一個 app。 */
  private k8sDeployEmpty(): HTMLElement {
    const deploy = btn({ class: "primary", "data-testid": "btn-k8s-deploy-first" }, "plus", t("gui.k8s.deploy"));
    deploy.onclick = () => void this.openDeploy();
    const apply = btn({ "data-testid": "btn-k8s-apply-yaml" }, "download", t("gui.k8s.apply_yaml"));
    apply.onclick = () => this.k8sPickYaml();
    const stack = btn({ "data-testid": "btn-k8s-import-first" }, "layers", t("gui.k8s.import"));
    stack.onclick = () => void this.openImport();
    return el("section", { class: "k8s-deploy-empty", "data-testid": "k8s-deploy-empty" },
      el("h3", { text: t("gui.k8s.deploy.title") }),
      el("p", { text: t("gui.k8s.deploy.body") }),
      el("div", { class: "row" }, deploy, stack, apply),
    );
  }

  private async k8sCopyContext(): Promise<void> {
    const ctx = this.st.k8s?.status.context ?? "";
    if (!ctx) return;
    try {
      await navigator.clipboard.writeText(`kubectl config use-context ${ctx}`);
    } catch {
      /* 剪貼簿不可用時仍顯示指令，使用者可手動複製 */
    }
    this.notify(fmt(t("gui.k8s.context_copied"), ctx));
  }

  /** 選一個 YAML／JSON 檔，交給 kubectl apply。 */
  private k8sPickYaml(): void {
    const input = el("input", { type: "file", accept: ".yaml,.yml,.json", "data-testid": "k8s-apply-file" }) as HTMLInputElement;
    input.onchange = async () => {
      const f = input.files?.[0];
      if (!f) return;
      if (f.size > 1 << 20) {
        this.st.lastError = t("gui.k8s.apply_too_large");
        this.renderBanner();
        return;
      }
      await this.k8sMutate("k8s:apply", async () => {
        const r = await api.k8sApply(await f.text());
        this.k8sApplyOutput = r.output;
      }, "");
      if (this.k8sApplyOutput) this.notify(fmt(t("gui.k8s.apply_done"), this.k8sApplyOutput.split("\n").join("; ")));
      this.k8sApplyOutput = undefined;
    };
    input.click();
  }
  private k8sApplyOutput?: string;

  private buildDeployDialog(): HTMLDialogElement {
    const d = el("dialog", { class: "deploy-dialog", "data-testid": "deploy-dialog" }) as HTMLDialogElement;
    const mk = (testid: string, extra: Record<string, string> = {}) => el("input", { type: "text", class: "mono", autocomplete: "off", spellcheck: "false", "data-testid": testid, ...extra }) as HTMLInputElement;
    const image = mk("deploy-image", { list: "deploy-images", placeholder: "myapp:latest" });
    const datalist = el("datalist", { id: "deploy-images" });
    const name = mk("deploy-name", { placeholder: "myapp" });
    const port = mk("deploy-port", { inputmode: "numeric", placeholder: "8080" });
    const replicas = mk("deploy-replicas", { inputmode: "numeric" });
    replicas.value = "1";
    const hostPort = mk("deploy-hostport", { inputmode: "numeric", placeholder: t("gui.k8s.deploy.same_as_container") });
    type Expose = "loadbalancer" | "nodeport" | "none";
    const exposeRadios = {} as Record<Expose, HTMLInputElement>;
    const exposeGroup = el("div", { class: "expose-group cols", role: "radiogroup", "aria-label": t("gui.k8s.deploy.expose"), "data-testid": "deploy-expose" });
    const exposeOpts: [Expose, string, string][] = [
      ["loadbalancer", "gui.k8s.deploy.expose_lb", "gui.k8s.deploy.expose_lb_d"],
      ["nodeport", "gui.k8s.deploy.expose_np", "gui.k8s.deploy.expose_np_d"],
      ["none", "gui.k8s.deploy.expose_none", "gui.k8s.deploy.expose_none_d"],
    ];
    for (const [v, label, hint] of exposeOpts) {
      const radio = el("input", { type: "radio", name: "deploy-expose", value: v, id: `deploy-expose-${v}`, "data-testid": `deploy-expose-${v}` }) as HTMLInputElement;
      radio.checked = v === "loadbalancer";
      exposeRadios[v] = radio;
      exposeGroup.append(el("label", { class: "expose-opt", for: radio.id }, radio, el("span", {}, el("b", { text: t(label) }), el("small", { text: t(hint) }))));
    }
    const hostField = el("label", { class: "field", "data-testid": "deploy-hostport-field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.host_port") }), textField(hostPort));
    const exposeMode = (): Expose => (Object.keys(exposeRadios) as Expose[]).find((k) => exposeRadios[k].checked) ?? "loadbalancer";
    const syncExpose = () => {
      hostField.hidden = exposeMode() !== "loadbalancer";
    };
    for (const r of Object.values(exposeRadios)) r.onchange = () => {
      syncExpose();
      err.hidden = true;
    };
    const err = el("div", { class: "field-error", "data-testid": "deploy-error", hidden: "" });
    const submit = btn({ class: "primary", type: "submit", "data-testid": "deploy-submit" }, "play", t("gui.k8s.deploy.submit"));
    const cancel = btn({ type: "button", "data-testid": "deploy-cancel" }, "close", t("gui.cancel"));
    let nameEdited = false;
    const NAME_RE = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?$/;
    /** 由 image 推一個合法名稱：去掉 registry 路徑與 tag。 */
    const suggest = (img: string) => img.split("/").pop()!.split(":")[0]!.split("@")[0]!.toLowerCase().replace(/[^a-z0-9-]/g, "-").replace(/^-+|-+$/g, "").slice(0, 63);
    image.oninput = () => {
      if (!nameEdited) name.value = suggest(image.value.trim());
      err.hidden = true;
    };
    name.oninput = () => {
      nameEdited = true;
      err.hidden = true;
    };
    const validate = (): string => {
      if (!/^[A-Za-z0-9][A-Za-z0-9._/:@-]*$/.test(image.value.trim())) return t("gui.k8s.deploy.err_image");
      if (!NAME_RE.test(name.value.trim()) || name.value.trim().length > 63) return t("gui.k8s.deploy.err_name");
      const p = port.value.trim();
      if (p !== "" && !(/^\d+$/.test(p) && +p >= 1 && +p <= 65535)) return t("gui.k8s.deploy.err_port");
      if (exposeMode() !== "none" && p === "") return t("gui.k8s.deploy.err_port_req");
      const h = hostPort.value.trim();
      if (exposeMode() === "loadbalancer" && h !== "" && !(/^\d+$/.test(h) && +h >= 1 && +h <= 65535)) return t("gui.k8s.deploy.err_port");
      if (!/^\d+$/.test(replicas.value.trim()) || +replicas.value < 1 || +replicas.value > 100) return t("gui.k8s.deploy.err_replicas");
      return "";
    };
    cancel.onclick = () => d.close();
    const form = el("form", { method: "dialog", class: "add-machine-form" },
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.image") }), textField(image), datalist),
      el("div", { class: "deploy-grid name-row" },
        el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.name") }), textField(name)),
        el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.replicas") }), textField(replicas)),
      ),
      el("div", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.expose") }), exposeGroup),
      el("div", { class: "deploy-grid" },
        el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.port") }), textField(port)),
        hostField,
      ),
      el("p", { class: "field-hint", text: t("gui.k8s.deploy.hint") }),
      err,
      el("div", { class: "row" }, cancel, submit),
    );
    form.noValidate = true;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const bad = validate();
      if (bad) {
        err.textContent = bad;
        err.hidden = false;
        return;
      }
      submit.disabled = true;
      submit.querySelector(".label")!.textContent = t("gui.k8s.deploy.deploying");
      try {
        const mode = exposeMode();
        const res = await api.k8sDeploy({
          image: image.value.trim(),
          name: name.value.trim(),
          port: port.value.trim() ? +port.value : undefined,
          expose: mode,
          host_port: mode === "loadbalancer" && hostPort.value.trim() ? +hostPort.value : undefined,
          replicas: +replicas.value,
        });
        d.close();
        let msg = fmt(t("gui.k8s.deploy.done"), res.name);
        if (res.expose === "loadbalancer" && res.host_port) {
          msg = res.port_changed
            ? fmt(t("gui.k8s.deploy.done_moved"), res.name, String(res.host_port), hostPort.value.trim() || port.value.trim())
            : fmt(t("gui.k8s.deploy.done_url"), res.name, String(res.host_port));
        } else if (res.node_port) {
          msg = fmt(t("gui.k8s.deploy.done_url"), res.name, String(res.node_port));
        }
        this.notify(msg, { sticky: true });
        await this.refresh();
      } catch (e) {
        err.textContent = e instanceof ApiError ? e.message : String(e);
        err.hidden = false;
      } finally {
        submit.disabled = false;
        submit.querySelector(".label")!.textContent = t("gui.k8s.deploy.submit");
      }
    };
    d.addEventListener("close", () => {
      nameEdited = false;
      exposeRadios.loadbalancer.checked = true;
      hostPort.value = "";
      syncExpose();
    });
    syncExpose();
    d.append(el("div", { class: "dialog-head" }, el("h2", { text: t("gui.k8s.deploy.dialog") })), form);
    return d;
  }

  /** Import：先選來源（Windows 或 WSL distro）與檔案，再預覽 Compose 轉換結果與警告，最後 Deploy。
   *  選到 k8s manifest（沒有 services）時改走 kubectl apply。檔案瀏覽與讀取都在後端（含同資料夾的 .env）。 */
  private buildImportDialog(): ImportDialog {
    const d = el("dialog", { class: "deploy-dialog import-dialog", "data-testid": "import-dialog" }) as ImportDialog;
    const SRC_KEY = "wsld.import.src";
    let sources: FileSources = { drives: [], wsl: [] };
    let kind = "wsl";
    let source = "";
    let dir = "";
    let parent = "";
    let selected = "";
    let preview: ComposePreview | undefined;
    let seq = 0;
    const sep = () => (kind === "windows" ? "\\" : "/");
    const join = (base: string, name: string) => base.replace(/[\\/]+$/, "") + sep() + name;
    const mk = (testid: string) => el("input", { type: "text", class: "mono", autocomplete: "off", spellcheck: "false", "data-testid": testid }) as HTMLInputElement;
    const errBox = el("div", { class: "field-error", "data-testid": "import-error", hidden: "" });
    const showErr = (m: string) => {
      errBox.textContent = m;
      errBox.hidden = !m;
    };
    const msg = (e: unknown) => (e instanceof ApiError ? e.message : String(e));

    // ---- 步驟 1：來源與檔案
    const kindLabel = el("span", { class: "field-label", id: "import-kind-label", text: t("gui.k8s.import.kind") });
    const srcLabel = el("span", { class: "field-label", id: "import-source-label" });
    const kindDd = dropdown({ testid: "import-kind", labelledby: "import-kind-label" }, [], kind, (v) => {
      kind = v;
      source = "";
      fillSources();
      void load("");
    });
    const srcDd = dropdown({ testid: "import-source", labelledby: "import-source-label" }, [], "", (v) => {
      source = v;
      void load("");
    });
    const ownerHint = el("p", { class: "field-hint", "data-testid": "import-owner-hint", hidden: "" });
    const crumb = el("div", { class: "import-path mono", "data-testid": "import-path" });
    const list = el("div", { class: "import-list", "data-testid": "import-list" });
    const stoppedBtn = btn({ type: "button", "data-testid": "import-start" }, "play", t("gui.k8s.import.start_browse"));
    const stopped = el("div", { class: "import-stopped", "data-testid": "import-stopped", hidden: "" }, el("span", { text: t("gui.k8s.import.stopped_note") }), stoppedBtn);
    const step1 = el("div", { class: "import-step" },
      el("div", { class: "deploy-grid" }, el("div", { class: "field" }, kindLabel, kindDd.el), el("div", { class: "field" }, srcLabel, srcDd.el)),
      ownerHint, crumb, list, stopped,
      el("p", { class: "field-hint", text: t("gui.k8s.import.hint") }),
    );

    const fillSources = () => {
      const opts: DropdownOption[] = kind === "wsl"
        ? sources.wsl.map((x): DropdownOption => [x.name, x.running ? x.name : `${x.name} (${t("gui.k8s.import.stopped_tag")})`])
        : sources.drives.map((x): DropdownOption => [x, x]);
      if (!opts.some(([v]) => v === source)) source = opts[0]?.[0] ?? "";
      srcDd.setOptions(opts, source, t("gui.k8s.import.no_source"));
      srcLabel.textContent = kind === "wsl" ? t("gui.k8s.import.distro") : t("gui.k8s.import.drive");
      ownerHint.hidden = !(kind === "wsl" && sources.owner);
      ownerHint.textContent = sources.owner ? fmt(t("gui.k8s.import.owner_hint"), sources.owner) : "";
    };

    const renderList = (entries: FileEntry[]) => {
      const rows: HTMLElement[] = [];
      if (parent) {
        const up = el("button", { type: "button", class: "import-item dim", "data-testid": "import-up" }, icon("chevron-left"), el("span", { text: t("gui.k8s.import.up") }));
        up.onclick = () => void load(parent);
        rows.push(up);
      }
      for (const e of entries) {
        const full = join(dir, e.name);
        const row = el("button", { type: "button", class: `import-item${full === selected ? " on" : ""}`, "data-testid": `import-item-${e.name}`, "data-kind": e.dir ? "dir" : "file" },
          icon(e.dir ? "folder" : "file"), el("span", { text: e.name }), el("small", { text: e.dir ? "" : humanSize(e.size ?? 0) }));
        row.onclick = () => {
          if (e.dir) return void load(full);
          selected = full;
          list.querySelectorAll(".import-item.on").forEach((x) => x.classList.remove("on"));
          row.classList.add("on");
          crumb.textContent = full;
          syncButtons();
        };
        row.ondblclick = () => {
          if (!e.dir) void toPreview();
        };
        rows.push(row);
      }
      if (!entries.length) rows.push(el("div", { class: "import-empty", text: t("gui.k8s.import.empty") }));
      list.replaceChildren(...rows);
    };

    const load = async (path: string, start = false) => {
      const mine = ++seq;
      stopped.hidden = true;
      showErr("");
      selected = "";
      syncButtons();
      if (!source) {
        list.replaceChildren(el("div", { class: "import-empty", text: t("gui.k8s.import.no_source") }));
        crumb.textContent = "";
        return;
      }
      try {
        const r = await api.fsList(kind, source, path, start);
        if (mine !== seq) return;
        dir = r.path;
        parent = r.parent;
        crumb.textContent = dir;
        renderList(r.entries);
        saveJSON(SRC_KEY, { kind, source });
      } catch (e) {
        if (mine !== seq) return;
        list.replaceChildren();
        if (e instanceof ApiError && e.code === "stopped") stopped.hidden = false;
        else showErr(msg(e));
      }
    };
    stoppedBtn.onclick = () => {
      void api.fsSources().then((s) => (sources = s)).catch(() => undefined);
      void load("", true);
    };

    // ---- 步驟 2：預覽
    const nameIn = mk("import-stack-name");
    const nsIn = mk("import-namespace");
    const readLine = el("div", { class: "import-read", "data-testid": "import-read" });
    const svcBox = el("div", { class: "import-services", "data-testid": "import-services" });
    const warnBox = el("div", { class: "import-warn", "data-testid": "import-warnings", hidden: "" });
    const plainNote = el("p", { class: "field-hint", "data-testid": "import-plain-env", text: t("gui.k8s.import.plain_env") });
    const orderLine = el("div", { class: "import-order", "data-testid": "import-order" });
    const yamlBox = el("pre", { class: "import-yaml mono", "data-testid": "import-yaml", hidden: "" });
    const manifestNote = el("p", { class: "import-manifest", "data-testid": "import-manifest", text: t("gui.k8s.import.manifest_note"), hidden: "" });
    const composeParts = el("div", { class: "import-step" },
      el("div", { class: "deploy-grid" },
        el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.import.stack_name") }), textField(nameIn)),
        el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.import.namespace") }), textField(nsIn))),
      readLine, el("div", { class: "field-label", text: t("gui.k8s.import.services") }), svcBox, orderLine, warnBox, plainNote, yamlBox);
    const step2 = el("div", { class: "import-step" }, manifestNote, composeParts);

    const mask = (y: string) => y.replace(/("name": "[^"]*(?:PASSWORD|SECRET|TOKEN|KEY)[^"]*",\s*"value": )"[^"]*"/gi, '$1"••••"');
    const renderPreview = () => {
      const p = preview!;
      manifestNote.hidden = p.kind !== "manifest";
      composeParts.hidden = p.kind !== "compose";
      if (p.kind !== "compose" || !p.spec) return;
      readLine.textContent = fmt(t("gui.k8s.import.read"), [selectedName(), ...p.read_files].join(", "));
      const byName = new Map(p.spec.services.map((s) => [s.name, s]));
      svcBox.replaceChildren(...(p.order ?? []).map((n) => {
        const s = byName.get(n)!;
        const bits = [s.image];
        if (s.depends_on?.length) bits.push(fmt(t("gui.k8s.import.depends"), s.depends_on.join(", ")));
        if (s.env?.length) bits.push(fmt(t("gui.k8s.import.env_n"), String(s.env.length)));
        if (s.volumes?.length) bits.push(fmt(t("gui.k8s.import.volumes_n"), String(s.volumes.length)));
        if ((s.replicas ?? 1) > 1) bits.push(`replicas ${s.replicas}`);
        const exposure = s.expose === "loadbalancer" ? `localhost:${s.host_port}` : s.expose === "nodeport" ? "NodePort" : s.port ? `${t("gui.k8s.import.cluster_only")} :${s.port}` : t("gui.k8s.import.cluster_only");
        return el("div", { class: "import-svc", "data-testid": `import-svc-${n}` }, el("b", { text: n }), el("span", { text: bits.join(" · ") }), el("small", { text: exposure }));
      }));
      orderLine.textContent = `${t("gui.k8s.import.order")}: ${(p.order ?? []).join(" → ")}`;
      warnBox.hidden = p.warnings.length === 0;
      warnBox.replaceChildren(el("b", { text: fmt(t("gui.k8s.import.warnings"), String(p.warnings.length)) }), ...p.warnings.map((w) => el("div", { text: w })));
      plainNote.hidden = !p.spec.services.some((s) => s.env?.length);
      yamlBox.textContent = mask(p.yaml ?? "");
    };
    const selectedName = () => selected.split(/[\\/]/).pop() ?? selected;

    // ---- 流程
    const cancel = btn({ type: "button", "data-testid": "import-cancel" }, "close", t("gui.cancel"));
    const next = btn({ class: "primary", type: "button", "data-testid": "import-next" }, "chevron-right", t("gui.k8s.import.next"));
    const back = btn({ type: "button", "data-testid": "import-back" }, "chevron-left", t("gui.k8s.import.back"));
    const yamlToggle = btn({ type: "button", "data-testid": "import-view-yaml" }, "file", t("gui.k8s.import.view_yaml"));
    const submit = btn({ class: "primary", type: "button", "data-testid": "import-submit" }, "play", t("gui.k8s.import.deploy"));
    let step: 1 | 2 = 1;
    const syncButtons = () => {
      step1.hidden = step !== 1;
      step2.hidden = step !== 2;
      next.hidden = step !== 1;
      next.disabled = !selected;
      back.hidden = step !== 2;
      submit.hidden = step !== 2;
      yamlToggle.hidden = step !== 2 || preview?.kind !== "compose";
      submit.querySelector(".label")!.textContent = preview?.kind === "manifest" ? t("gui.k8s.import.apply") : t("gui.k8s.import.deploy");
    };
    const fetchPreview = async () => {
      showErr("");
      try {
        const r = await api.composePreview({ kind, name: source, path: selected, stack_name: nameIn.value.trim() || undefined, namespace: nsIn.value.trim() || undefined });
        preview = r;
        if (r.kind === "compose" && r.spec) {
          nameIn.value = r.spec.name;
          nsIn.value = r.spec.namespace ?? r.spec.name;
        }
        renderPreview();
        return true;
      } catch (e) {
        showErr(msg(e));
        return false;
      }
    };
    const toPreview = async () => {
      if (!selected) return;
      nameIn.value = "";
      nsIn.value = "";
      if (!(await fetchPreview())) return;
      step = 2;
      syncButtons();
    };
    next.onclick = () => void toPreview();
    back.onclick = () => {
      step = 1;
      showErr("");
      syncButtons();
    };
    cancel.onclick = () => d.close();
    nameIn.onchange = () => void fetchPreview();
    nsIn.onchange = () => void fetchPreview();
    yamlToggle.onclick = () => {
      yamlBox.hidden = !yamlBox.hidden;
      yamlToggle.querySelector(".label")!.textContent = yamlBox.hidden ? t("gui.k8s.import.view_yaml") : t("gui.k8s.import.hide_yaml");
    };
    submit.onclick = async () => {
      if (!preview) return;
      submit.disabled = true;
      const label = submit.querySelector(".label")!;
      const prev = label.textContent;
      label.textContent = t("gui.k8s.import.deploying");
      try {
        if (preview.kind === "manifest") {
          const r = await api.k8sApply(preview.manifest ?? "");
          d.close();
          this.notify(fmt(t("gui.k8s.apply_done"), r.output.split("\n").join("; ")));
        } else {
          const res = await api.k8sStack(preview.spec!);
          d.close();
          const urls = res.services.map((r) => (r.host_port ? `${r.name} → http://localhost:${r.host_port}` : r.node_port ? `${r.name} → http://localhost:${r.node_port}` : "")).filter(Boolean);
          this.notify([fmt(t("gui.k8s.import.done"), res.name, String(res.services.length)), ...urls].join("; "), { sticky: true });
        }
        await this.refresh();
      } catch (e) {
        showErr(msg(e));
      } finally {
        submit.disabled = false;
        label.textContent = prev;
      }
    };

    d.begin = async () => {
      step = 1;
      preview = undefined;
      selected = "";
      showErr("");
      try {
        sources = await api.fsSources();
      } catch (e) {
        sources = { drives: [], wsl: [] };
        showErr(msg(e));
      }
      const kinds: DropdownOption[] = [];
      if (sources.wsl.length) kinds.push(["wsl", "WSL"]);
      if (sources.drives.length) kinds.push(["windows", "Windows"]);
      const last = loadJSON<{ kind: string; source: string }>(SRC_KEY, { kind: "", source: "" });
      kind = kinds.some(([v]) => v === last.kind) ? last.kind : (kinds[0]?.[0] ?? "wsl");
      source = last.kind === kind ? last.source : "";
      kindDd.setOptions(kinds, kind, t("gui.k8s.import.no_source"));
      fillSources();
      syncButtons();
      d.showModal();
      await load("");
    };
    d.append(
      el("div", { class: "dialog-head" }, el("h2", { text: t("gui.k8s.import.title") })),
      el("div", { class: "add-machine-form" }, step1, step2, errBox, el("div", { class: "row" }, yamlToggle, el("span", { class: "spacer" }), cancel, back, next, submit)),
    );
    syncButtons();
    return d;
  }

  private async openImport(): Promise<void> {
    await this.root.querySelector<ImportDialog>('[data-testid="import-dialog"]')!.begin();
  }

  private async openDeploy(): Promise<void> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="deploy-dialog"]')!;
    for (const id of ["deploy-image", "deploy-name", "deploy-port"]) d.querySelector<HTMLInputElement>(`[data-testid="${id}"]`)!.value = "";
    d.querySelector<HTMLInputElement>('[data-testid="deploy-replicas"]')!.value = "1";
    d.querySelector<HTMLElement>('[data-testid="deploy-error"]')!.hidden = true;
    const list = d.querySelector<HTMLElement>("#deploy-images")!;
    try {
      const imgs = await api.images();
      const tags = imgs.flatMap((i) => i.RepoTags ?? []).filter((x) => x && x !== "<none>:<none>");
      list.replaceChildren(...[...new Set(tags)].map((tag) => el("option", { value: tag })));
    } catch {
      /* 取不到本機映像清單時仍可手動輸入 */
    }
    d.showModal();
    d.querySelector<HTMLInputElement>('[data-testid="deploy-image"]')!.focus();
  }

  /** 全部 namespace 的資源（卡片、架構圖、各分頁共用）。 */
  private k8sRes(): K8sResources {
    const x = this.st.k8sExtras;
    return { pods: this.st.k8sPods, deployments: this.st.k8sDeployments, services: this.st.k8sServices, statefulsets: x.statefulsets, ingresses: x.ingresses, configmaps: x.configmaps, pvcs: x.pvcs, others: x.others };
  }

  /** Namespace flow card 格：每個 namespace 一張卡，內容是 Ingress → Service → Workload → Pods 的數量流程。 */
  private k8sResources(): HTMLElement {
    const wrap = el("div", { class: "k8s-resources", "data-testid": "k8s-resources" });
    if (this.k8sListError) {
      wrap.append(el("div", { class: "empty", "data-testid": "k8s-list-error", text: this.k8sListError }));
      return wrap;
    }
    if (this.st.k8sNamespaces.length === 0) {
      wrap.append(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty.k8s_namespaces") }));
      return wrap;
    }
    // 頁籤：Workloads（default 與自建）／System（kube-*，Kubernetes 內建）。各自佔滿內容區，不互搶空間。
    const res = this.k8sRes();
    const viewBar = tabs({
      testid: "k8s-view-tabs",
      selected: this.k8sView,
      items: (["apps", "namespaces"] as const).map((id) => ({ id, testid: `k8s-view-${id}`, label: t(`gui.k8s.view.${id}`) })),
      onSelect: (id) => {
        this.k8sView = id === "namespaces" ? "namespaces" : "apps";
        saveJSON(LS_K8S_VIEW, this.k8sView);
        this.renderKubernetes();
      },
    });
    wrap.append(this.k8sView === "apps" ? el("div", { class: "k8s-viewbar" }, viewBar, this.k8sShowSystemToggle()) : viewBar);
    if (this.k8sView === "apps") {
      wrap.append(this.k8sAppsView());
      return wrap;
    }
    const groups: Record<K8sGroup, string[]> = {
      workloads: this.st.k8sNamespaces.filter((ns) => !isSystemNs(ns)),
      system: this.st.k8sNamespaces.filter(isSystemNs),
    };
    const sysAbnormal = groups.system.some((ns) => {
      const h = summarizeNamespace(ns, res).health;
      return h === "warn" || h === "error";
    });
    const bar = tabs({
      testid: "k8s-group-tabs",
      selected: this.k8sGroup,
      bordered: true,
      items: (["workloads", "system"] as K8sGroup[]).map((id) => ({
        id, testid: `k8s-group-${id}`, label: t(`gui.k8s.group.${id}`), count: groups[id].length,
        dot: id === "system" && sysAbnormal ? t("gui.k8s.group.abnormal") : undefined,
      })),
      onSelect: (id) => this.k8sSelectGroup(id as K8sGroup),
    });
    const panel = el("div", { class: "k8s-ns-panel" }, bar);
    wrap.append(panel);
    const grid = el("div", { class: "k8s-ns-grid", "data-testid": "k8s-ns-grid" });
    for (const ns of groups[this.k8sGroup]) grid.append(this.k8sNsCard(ns));
    if (this.k8sGroup === "workloads" && groups.workloads.length === 1 && groups.workloads[0] === "default") {
      grid.append(el("div", { class: "k8s-ns-hint", "data-testid": "k8s-ns-hint" }, el("strong", { text: t("gui.k8s.ns.hint_title") }), el("span", { text: t("gui.k8s.ns.hint_body") })));
    }
    panel.append(grid);
    return wrap;
  }

  // ---- Apps 視圖（預設）：一個 Deployment＋它的 Service／Pods 合成一列 -------------------

  private k8sAppsView(): HTMLElement {
    const wrap = el("div", { class: "k8s-apps", "data-testid": "k8s-apps" });
    const apps = buildApps(this.k8sRes(), this.k8sShowSystem);
    if (apps.length === 0) {
      wrap.append(this.k8sDeployEmpty());
      return wrap;
    }
    const cards = el("div", { class: "k8s-app-list", role: "list" });
    for (const a of apps) cards.append(this.k8sAppCard(a));
    wrap.append(cards);
    return wrap;
  }

  /** 顯示系統元件的開關：與 Apps／Namespaces 頁籤同一列，靠右。 */
  private k8sShowSystemToggle(): HTMLElement {
    const showSys = el("input", { type: "checkbox", "data-testid": "k8s-show-system" }) as HTMLInputElement;
    showSys.checked = this.k8sShowSystem;
    showSys.onchange = () => {
      this.k8sShowSystem = showSys.checked;
      saveJSON(LS_K8S_SHOWSYS, this.k8sShowSystem);
      showSys.blur(); // 輪詢重繪會略過仍有 input 焦點的清單
      this.renderKubernetes();
    };
    return el("label", { class: "k8s-opt k8s-showsys" }, showSys, t("gui.k8s.apps.show_system"));
  }

  private k8sAppCard(a: K8sApp): HTMLElement {
    const key = `k8s-app:${a.namespace}/${a.name}`;
    const id = `${a.namespace}/${a.name}`;
    const open = this.k8sExpandedApps.has(id);
    const toggle = el("button", { type: "button", class: "app-toggle", "data-testid": "btn-app-expand", "aria-expanded": String(open), "aria-label": open ? t("gui.k8s.apps.collapse") : t("gui.k8s.apps.expand"), text: open ? "▾" : "▸" });
    toggle.onclick = () => {
      if (open) this.k8sExpandedApps.delete(id);
      else this.k8sExpandedApps.add(id);
      toggle.blur(); // renderKubernetes 會略過仍聚焦且 aria-expanded=true 的元素，不先失焦就無法折疊
      this.renderKubernetes();
    };
    const statusText = a.status === "stopped" ? t("gui.k8s.apps.status.stopped") : fmt(t(`gui.k8s.apps.status.${a.status}`), a.ready);
    const status = el("span", { class: `app-status ${a.status}`, "data-testid": "app-status", "data-status": a.status, text: a.reason ? `${statusText} · ${a.reason}` : statusText });

    const urlCell = el("span", { class: "app-urls", "data-testid": "app-urls" });
    for (const u of a.urls) {
      const link = el("a", { href: u.href, class: "app-url", "data-testid": "app-url-link", "data-port": String(u.port), text: `${u.label} ↗` });
      link.onclick = (ev) => {
        ev.preventDefault();
        void api.k8sOpen(u.port).catch((err) => {
          this.st.lastError = err instanceof ApiError ? err.message : String(err);
          this.renderBanner();
        });
      };
      urlCell.append(link);
    }
    const addUrl = el("button", { type: "button", class: "app-add-url", "data-testid": a.urls.length ? "btn-app-edit-url" : "btn-app-add-url", text: a.urls.length ? t("gui.k8s.apps.edit_url") : `＋ ${t("gui.k8s.apps.add_url")}` });
    addUrl.onclick = () => void this.openExpose(a);
    urlCell.append(addUrl);

    const restart = iconBtn({ "data-testid": "btn-app-restart" }, "refresh", t("gui.k8s.restart"));
    restart.disabled = this.st.busy.has(key);
    restart.onclick = () => void this.k8sMutate(key, () => api.k8sRestart(a.namespace, a.name), fmt(t("gui.k8s.restarted"), a.name));
    const rm = iconBtn({ "data-testid": "btn-app-delete", class: "danger" }, "trash", t("gui.k8s.apps.delete"));
    rm.disabled = this.st.busy.has(key);
    rm.onclick = () => void this.k8sDeleteApp(a);

    const card = el("div", { class: "k8s-app-card k8s-app-row", role: "listitem", "data-testid": "k8s-app-row", "data-name": a.name, "data-ns": a.namespace, "data-status": a.status, "data-expanded": String(open) },
      el("div", { class: "k8s-app-main" },
        el("span", { class: "app-name-cell" }, toggle,
          el("span", { class: "app-name-wrap" },
            el("span", { class: "app-name", "data-testid": "app-name", text: a.name }),
            el("span", { class: "app-sub muted", "data-testid": "app-sub", text: [a.image, a.namespace].filter(Boolean).join(" · "), title: a.image }),
          )),
        status, urlCell, el("span", { class: "app-actions" }, restart, rm),
      ),
    );
    if (open) card.append(this.k8sAppDetail(a));
    return card;
  }

  /** 展開的一列：副本數調整與 Pods（Logs／Exec）。 */
  private k8sAppDetail(a: K8sApp): HTMLElement {
    const { stepper, apply } = this.k8sScaleControls(a.deployment);
    const pods = a.pods.length
      ? a.pods.map((p) => {
          const logs = iconBtn({ "data-testid": `btn-pod-logs-${p.name}` }, "logs", t("gui.logs"));
          logs.onclick = () => this.openPodLogs(p.namespace, p.name);
          const term = iconBtn({ "data-testid": `btn-pod-term-${p.name}` }, "terminal", t("gui.terminal"));
          term.disabled = p.status !== "Running";
          term.onclick = () => this.openPodTerminal(p.namespace, p.name);
          return el("div", { class: "app-pod", "data-testid": "app-pod", "data-name": p.name },
            el("span", { class: "mono", text: p.name, title: p.name }),
            el("span", { class: `state ${podStateClass(p.status)}`, text: p.status }),
            el("span", { class: "muted mono", text: p.ip ?? "" }),
            el("span", { class: "app-actions" }, logs, term),
          );
        })
      : [el("div", { class: "muted", text: t("gui.k8s.apps.no_pods") })];
    return el("div", { class: "k8s-app-detail", "data-testid": "app-detail" },
      el("div", { class: "app-scale" }, el("span", { class: "muted", text: t("gui.k8s.replicas") }), stepper, apply),
      ...pods,
    );
  }

  /** 刪除 app：Deployment 與「只指向這個 app」的 Service 一起刪（共用的 Service 保留）。 */
  private async k8sDeleteApp(a: K8sApp): Promise<void> {
    const key = `k8s-app:${a.namespace}/${a.name}`;
    const others = buildApps(this.k8sRes(), true).filter((x) => x.namespace === a.namespace && x.name !== a.name);
    const own = a.services.filter((sv) => !others.some((o) => o.services.some((s2) => s2.name === sv.name)));
    await this.resourceAction(key, fmt(t("gui.k8s.apps.confirm_delete"), a.name), async () => {
      await api.k8sDelete("deployments", a.namespace, a.name);
      for (const sv of own) await api.k8sDelete("services", sv.namespace, sv.name);
      if (this.st.k8sDetail?.name === a.name) this.st.k8sDetail = undefined;
      this.k8sExpandedApps.delete(`${a.namespace}/${a.name}`);
      return fmt(t("gui.k8s.apps.deleted"), a.name);
    });
  }

  private buildExposeDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "expose-dialog" }) as HTMLDialogElement;
    const mk = (testid: string, extra: Record<string, string> = {}) => el("input", { type: "text", class: "mono", autocomplete: "off", spellcheck: "false", "data-testid": testid, ...extra }) as HTMLInputElement;
    const hostPort = mk("expose-hostport", { inputmode: "numeric", placeholder: t("gui.k8s.deploy.same_as_container") });
    const port = mk("expose-port", { inputmode: "numeric", placeholder: t("gui.k8s.expose.auto_port") });
    const err = el("div", { class: "field-error", "data-testid": "expose-error", hidden: "" });
    const title = el("h2", { "data-testid": "expose-title" });
    type Mode = "loadbalancer" | "nodeport";
    const radios = {} as Record<Mode, HTMLInputElement>;
    const group = el("div", { class: "expose-group", role: "radiogroup", "aria-label": t("gui.k8s.deploy.expose") });
    for (const [v, label, hint] of [["loadbalancer", "gui.k8s.deploy.expose_lb", "gui.k8s.deploy.expose_lb_d"], ["nodeport", "gui.k8s.deploy.expose_np", "gui.k8s.deploy.expose_np_d"]] as [Mode, string, string][]) {
      const r = el("input", { type: "radio", name: "expose-mode", value: v, id: `expose-mode-${v}`, "data-testid": `expose-mode-${v}` }) as HTMLInputElement;
      radios[v] = r;
      group.append(el("label", { class: "expose-opt", for: r.id }, r, el("span", {}, el("b", { text: t(label) }), el("small", { text: t(hint) }))));
    }
    const hostField = el("label", { class: "field", "data-testid": "expose-hostport-field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.host_port") }), textField(hostPort));
    const mode = (): Mode => (radios.nodeport.checked ? "nodeport" : "loadbalancer");
    const sync = () => {
      hostField.hidden = mode() !== "loadbalancer";
    };
    for (const r of Object.values(radios)) r.onchange = () => {
      sync();
      err.hidden = true;
    };
    const submit = btn({ class: "primary", type: "submit", "data-testid": "expose-submit" }, "check", t("gui.k8s.expose.submit"));
    const cancel = btn({ type: "button", "data-testid": "expose-cancel" }, "close", t("gui.cancel"));
    cancel.onclick = () => d.close();
    const valid = (v: string) => v === "" || (/^\d+$/.test(v) && +v >= 1 && +v <= 65535);
    const form = el("form", { method: "dialog", class: "add-machine-form" },
      el("div", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.deploy.expose") }), group),
      hostField,
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.k8s.expose.container_port") }), textField(port)),
      err,
      el("div", { class: "row" }, cancel, submit),
    );
    form.noValidate = true;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const target = this.k8sExposeTarget;
      if (!target) return;
      if (!valid(port.value.trim()) || (mode() === "loadbalancer" && !valid(hostPort.value.trim()))) {
        err.textContent = t("gui.k8s.deploy.err_port");
        err.hidden = false;
        return;
      }
      submit.disabled = true;
      try {
        const res = await api.k8sExpose({
          namespace: target.namespace,
          name: target.name,
          expose: mode(),
          port: port.value.trim() ? +port.value : undefined,
          host_port: mode() === "loadbalancer" && hostPort.value.trim() ? +hostPort.value : undefined,
        });
        d.close();
        const shown = res.expose === "loadbalancer" ? res.host_port : res.node_port;
        if (shown) {
          this.notify(res.port_changed ? fmt(t("gui.k8s.expose.done_moved"), res.name, String(shown), hostPort.value.trim() || port.value.trim()) : fmt(t("gui.k8s.expose.done"), res.name, String(shown)), { sticky: true });
        }
        await this.refresh();
      } catch (e) {
        err.textContent = e instanceof ApiError ? e.message : String(e);
        err.hidden = false;
      } finally {
        submit.disabled = false;
      }
    };
    d.append(el("div", { class: "dialog-head" }, title), form);
    // openExpose 需要設定標題與預設值
    (d as HTMLDialogElement & { prep?: (a: K8sApp) => void }).prep = (a: K8sApp) => {
      const lb = a.services.find((sv) => sv.type === "LoadBalancer");
      const np = a.services.find((sv) => sv.type === "NodePort");
      title.textContent = fmt(t(a.urls.length ? "gui.k8s.expose.title_edit" : "gui.k8s.expose.title"), a.name);
      radios.loadbalancer.checked = !np || !!lb || !a.urls.length;
      radios.nodeport.checked = !radios.loadbalancer.checked;
      hostPort.value = lb?.ports[0] ? String(lb.ports[0].port) : "";
      port.value = "";
      err.hidden = true;
      sync();
    };
    return d;
  }

  private k8sExposeTarget?: K8sApp;

  private async openExpose(a: K8sApp): Promise<void> {
    const d = this.root.querySelector<HTMLDialogElement & { prep?: (a: K8sApp) => void }>('[data-testid="expose-dialog"]')!;
    this.k8sExposeTarget = a;
    d.prep?.(a);
    d.showModal();
  }

  private k8sNsCard(ns: string): HTMLElement {
    const sum = summarizeNamespace(ns, this.k8sRes());
    const node = (kind: string, label: string, value: string, n: number) =>
      el("div", { class: `ns-node${n === 0 ? " zero" : ""}`, "data-kind": kind },
        el("span", { class: "ns-node-label", text: label }),
        el("span", { class: "ns-node-value", "data-testid": `k8s-ns-node-${kind}`, text: value }),
      );
    const arrow = () => el("span", { class: "ns-arrow", "aria-hidden": "true", text: "→" });
    const open = this.st.k8sNs === ns;
    // 系統 namespace 沒有 Service／Workload／Pod 時，流程圖只會是一排 0：改為直接列出裡面實際有的東西
    const flowless = isSystemNs(ns) && sum.ingresses + sum.services + sum.workloads + sum.pods === 0;
    const chips = [...sum.others.map((o) => [o.kind, o.count] as const), ...(sum.configMaps ? [["ConfigMap", sum.configMaps] as const] : []), ...(sum.pvcs ? [["PVC", sum.pvcs] as const] : [])];
    const flowlessBody = el("span", { class: "ns-items", "data-testid": "k8s-ns-items" },
      ...(chips.length ? chips.map(([k, n]) => el("span", { class: "ns-chip", "data-kind": k, text: `${k} ${n}` })) : [el("span", { class: "muted", text: t("gui.k8s.health.empty") })]),
    );
    const card = el("button", { type: "button", class: "k8s-ns-card", "data-testid": `k8s-ns-card-${ns}`, "data-ns": ns, "data-health": sum.health, "aria-pressed": String(open) },
      el("span", { class: "ns-head" },
        el("span", { class: "ns-name", "data-testid": "k8s-ns-name", text: ns, title: ns }),
        el("span", { class: "ns-head-tags" },
          // 健康狀態只在異常時標示；正常不顯示，避免每張卡都貼狀態
          ...(sum.health === "warn" || sum.health === "error" ? [el("span", { class: `ns-health ${sum.health}`, "data-testid": "k8s-ns-health", text: t(`gui.k8s.health.${sum.health}`) })] : []),
          el("span", { class: "ns-kind", "data-testid": "k8s-ns-kind", text: t(`gui.k8s.ns.kind.${ns === "default" ? "default" : isSystemNs(ns) ? "system" : "user"}`) }),
        ),
      ),
      ...(flowless ? [flowlessBody] : [el("span", { class: "ns-flow" },
        ...(sum.ingresses > 0 ? [node("ingresses", t("gui.k8s.node.ingresses"), String(sum.ingresses), sum.ingresses), arrow()] : []),
        node("services", t("gui.k8s.node.services"), String(sum.services), sum.services),
        arrow(),
        node("workloads", t("gui.k8s.node.workloads"), String(sum.workloads), sum.workloads),
        arrow(),
        node("pods", t("gui.k8s.node.pods"), `${sum.podsReady}/${sum.pods}`, sum.pods),
      )]),
      ...(sum.nodePorts.length ? [el("span", { class: "ns-exposed", "data-testid": "k8s-ns-exposed", text: `NodePort ${sum.nodePorts.join(", ")}`, title: t("gui.k8s.exposed") })] : []),
      ...(flowless ? [] : [el("span", { class: "ns-foot muted", "data-testid": "k8s-ns-foot", text: fmt(t("gui.k8s.ns.foot"), String(sum.configMaps), String(sum.pvcs)) })]),
      ...(!flowless && sum.others.length ? [el("span", { class: "ns-others muted", "data-testid": "k8s-ns-others", text: sum.others.map((o) => `${o.kind} ${o.count}`).join(" · "), title: sum.others.map((o) => `${o.kind} ${o.count}`).join("\n") })] : []),
    );
    card.onclick = () => this.k8sSelectNs(ns);
    return card;
  }

  private k8sEmpty(key: string): HTMLElement {
    return el("div", { class: "empty", "data-testid": "empty", text: t(key) });
  }

  // ---- Pods 分頁 -----------------------------------------------------------------

  private k8sPodsTab(ns: string): HTMLElement {
    const wrap = el("div", { class: "k8s-resources" });
    const pods = this.st.k8sPods.filter((p) => p.namespace === ns);
    if (pods.length === 0) return wrap.append(this.k8sEmpty("gui.empty.k8s_pods")), wrap;
    const g = buildNamespaceGraph(ns, this.k8sRes());
    const search = el("input", { type: "search", class: "k8s-search", placeholder: t("gui.k8s.pods.search"), value: this.st.k8sPodQuery, "data-testid": "k8s-pod-search", "aria-label": t("gui.k8s.pods.search") }) as HTMLInputElement;
    const filter = dropdown({ testid: "k8s-pod-status" }, [["", t("gui.k8s.pods.status_all")], ["notready", t("gui.k8s.pods.status_notready")]] as DropdownOption[], this.st.k8sPodFilter, (v) => {
      this.st.k8sPodFilter = v as "" | "notready";
      fill();
    });
    const host = el("div", { class: "k8s-table-host" });
    const fill = () => {
      const q = this.st.k8sPodQuery.trim().toLowerCase();
      const rows = pods.filter((p) => (!q || p.name.toLowerCase().includes(q)) && (this.st.k8sPodFilter !== "notready" || !(p.status === "Running" && p.ready.split("/")[0] === p.ready.split("/")[1])));
      if (rows.length === 0) return host.replaceChildren(this.k8sEmpty("gui.empty.k8s_pods"));
      const { table: tbl, tbody } = this.k8sTable("pods", K8S_POD_COLS);
      for (const p of rows) {
        const key = `k8s-pod:${p.namespace}/${p.name}`;
        const logs = iconBtn({ "data-testid": `btn-pod-logs-${p.name}` }, "logs", t("gui.logs"));
        logs.onclick = () => this.openPodLogs(p.namespace, p.name);
        const term = iconBtn({ "data-testid": `btn-pod-term-${p.name}` }, "terminal", t("gui.terminal"));
        term.disabled = p.status !== "Running";
        term.onclick = () => this.openPodTerminal(p.namespace, p.name);
        const rm = iconBtn({ "data-testid": `btn-pod-rm-${p.name}`, class: "danger" }, "trash", t("gui.remove"));
        rm.disabled = this.st.busy.has(key);
        rm.onclick = () =>
          void this.resourceAction(key, fmt(t("gui.k8s.confirm.delete"), "pod", p.name), async () => {
            await api.k8sDelete("pods", p.namespace, p.name);
            if (this.st.panel?.k8s?.pod === p.name) this.closePanel();
            return fmt(t("gui.k8s.deleted"), p.name);
          });
        const owner = ownerOf(g, p);
        tbody.append(
          el("tr", { "data-testid": "pod-row", "data-name": p.name, "data-status": p.status },
            el("td", { class: "name-cell two-line" },
              el("span", { class: "ct-name mono", "data-testid": "pod-name", text: p.name, title: p.name }),
              el("span", { class: "sub muted", "data-testid": "pod-owner", text: [owner || t("gui.k8s.pods.no_owner"), p.node].filter(Boolean).join(" · ") }),
            ),
            el("td", {}, el("span", { class: `state ${podStateClass(p.status)}`, "data-testid": "pod-status", text: p.status })),
            this.k8sCell("pod-ready", p.ready),
            this.k8sCell("pod-restarts", String(p.restarts), p.restarts > 0 ? "warn-text" : ""),
            this.k8sCell("pod-age", ageText(p.created_at), "muted"),
            el("td", { class: "actions" }, logs, term, rm),
          ),
        );
      }
      host.replaceChildren(tbl);
    };
    search.addEventListener("input", () => {
      this.st.k8sPodQuery = search.value;
      fill();
    });
    fill();
    wrap.append(el("div", { class: "k8s-toolbar" }, search, filter.el), host);
    return wrap;
  }

  private k8sTable(tab: string, base: Col[]) {
    return this.table("kubernetes", base.filter((c) => c.key !== "namespace").map((c) => ({ ...c, key: `${tab}-${c.key}` })));
  }

  private k8sCell(testid: string, text: string, extra = ""): HTMLElement {
    return el("td", { "data-testid": testid, class: extra, text, title: text });
  }

  // ---- Workloads 分頁（Deployment／StatefulSet 卡片）-------------------------------

  /** 副本數調整器：−／＋ 只改輸入值，按「套用」才真的 scale（避免連點就直接改動叢集）；另含 rollout restart。 */
  private k8sScaleControls(d: K8sDeployment): { stepper: HTMLElement; apply: HTMLButtonElement; restart: HTMLButtonElement } {
    const key = `k8s-dep:${d.namespace}/${d.name}`;
    const input = el("input", { type: "number", min: "0", max: "100", step: "1", value: String(d.replicas), "data-testid": `deploy-replicas-${d.name}`, "aria-label": t("gui.k8s.replicas") }) as HTMLInputElement;
    const apply = iconBtn({ "data-testid": `btn-scale-${d.name}` }, "check", t("gui.k8s.scale"));
    const sync = () => {
      const n = Number(input.value);
      apply.disabled = this.st.busy.has(key) || input.value === "" || !input.checkValidity() || n === d.replicas;
    };
    input.addEventListener("input", sync);
    const step = (delta: number) => {
      input.value = String(Math.min(100, Math.max(0, (Number(input.value) || 0) + delta)));
      sync();
    };
    const dec = el("button", { type: "button", class: "stepper-btn", "data-testid": `btn-replicas-dec-${d.name}`, "aria-label": t("gui.decrease"), text: "−" });
    const inc = el("button", { type: "button", class: "stepper-btn", "data-testid": `btn-replicas-inc-${d.name}`, "aria-label": t("gui.increase"), text: "+" });
    dec.onclick = () => step(-1);
    inc.onclick = () => step(1);
    sync();
    apply.onclick = () => {
      const n = Number(input.value);
      void this.k8sMutate(key, () => api.k8sScale(d.namespace, d.name, n), fmt(t("gui.k8s.scaled"), d.name, String(n)));
    };
    const restart = iconBtn({ "data-testid": `btn-restart-${d.name}` }, "refresh", t("gui.k8s.restart"));
    restart.disabled = this.st.busy.has(key);
    restart.onclick = () => void this.k8sMutate(key, () => api.k8sRestart(d.namespace, d.name), fmt(t("gui.k8s.restarted"), d.name));
    return { stepper: el("span", { class: "stepper" }, dec, el("span", { class: "tf num replicas" }, input), inc), apply, restart };
  }

  private k8sDeleteDeployment(d: K8sDeployment): HTMLButtonElement {
    const key = `k8s-dep:${d.namespace}/${d.name}`;
    const rm = iconBtn({ "data-testid": `btn-deploy-rm-${d.name}`, class: "danger" }, "trash", t("gui.remove"));
    rm.disabled = this.st.busy.has(key);
    rm.onclick = () =>
      void this.resourceAction(key, fmt(t("gui.k8s.confirm.delete"), "deployment", d.name), async () => {
        await api.k8sDelete("deployments", d.namespace, d.name);
        if (this.st.k8sDetail?.name === d.name) this.st.k8sDetail = undefined;
        return fmt(t("gui.k8s.deleted"), d.name);
      });
    return rm;
  }

  private readyBar(ready: string): HTMLElement {
    const [rdy, want] = ready.split("/").map(Number);
    const pct = want > 0 ? Math.min(100, Math.round((rdy / want) * 100)) : 0;
    const full = want > 0 && rdy === want;
    return el("span", { class: `deploy-bar${full || want === 0 ? "" : " warn"}`, "data-testid": "deploy-bar", role: "img", "aria-label": ready }, el("span", { style: `width:${pct}%` }));
  }

  private k8sWorkloadsTab(ns: string): HTMLElement {
    const wrap = el("div", { class: "k8s-resources" });
    const deployments = this.st.k8sDeployments.filter((d) => d.namespace === ns);
    const sets = this.st.k8sExtras.statefulsets.filter((s) => s.namespace === ns);
    if (deployments.length + sets.length === 0) return wrap.append(this.k8sEmpty("gui.empty.k8s_workloads")), wrap;
    const list = el("div", { class: "deploy-cards", "data-testid": "deploy-cards" });
    for (const d of deployments) {
      const c = this.k8sScaleControls(d);
      const open = el("button", { type: "button", class: "ct-name link", "data-testid": "deploy-name", text: d.name, title: d.name });
      open.onclick = () => this.k8sOpenDetail(ns, d.name);
      list.append(
        el("div", { class: "deploy-card", "data-testid": "deploy-row", "data-name": d.name },
          el("div", { class: "deploy-main" },
            el("div", { class: "deploy-title" }, open, el("span", { class: "kind-chip", text: "Deployment" })),
            d.image ? el("div", { class: "mono muted deploy-image", "data-testid": "deploy-image", text: d.image, title: d.image }) : el("div"),
            el("div", { class: "deploy-ready-line" },
              this.readyBar(d.ready),
              el("span", { class: "deploy-ready", "data-testid": "deploy-ready", text: d.ready }),
              el("span", { class: "muted", text: fmt(t("gui.k8s.deploy.meta"), String(d.up_to_date), String(d.available), ageText(d.created_at)) }),
            ),
          ),
          el("div", { class: "deploy-actions" }, c.stepper, c.apply, c.restart, this.k8sDeleteDeployment(d)),
        ),
      );
    }
    for (const s of sets) {
      list.append(
        el("div", { class: "deploy-card", "data-testid": "sts-row", "data-name": s.name },
          el("div", { class: "deploy-main" },
            el("div", { class: "deploy-title" }, el("span", { class: "ct-name", "data-testid": "sts-name", text: s.name, title: s.name }), el("span", { class: "kind-chip", text: "StatefulSet" })),
            s.image ? el("div", { class: "mono muted deploy-image", text: s.image, title: s.image }) : el("div"),
            el("div", { class: "deploy-ready-line" }, this.readyBar(s.ready), el("span", { class: "deploy-ready", "data-testid": "sts-ready", text: s.ready }), el("span", { class: "muted", text: ageText(s.created_at) })),
          ),
        ),
      );
    }
    wrap.append(list);
    return wrap;
  }

  // ---- Services 分頁（含 Ingress 路由）---------------------------------------------

  private k8sServicesTab(ns: string): HTMLElement {
    const wrap = el("div", { class: "k8s-resources" });
    const services = this.st.k8sServices.filter((s) => s.namespace === ns);
    if (services.length === 0) return wrap.append(this.k8sEmpty("gui.empty.k8s_services")), wrap;
    const g = buildNamespaceGraph(ns, this.k8sRes());
    const { table: tbl, tbody } = this.k8sTable("services", K8S_SERVICE_COLS);
    for (const sv of services) {
      const key = `k8s-svc:${sv.namespace}/${sv.name}`;
      const ports = sv.ports.map((p) => `${p.port}${p.node_port ? `:${p.node_port}` : ""}/${p.protocol}`).join(", ");
      const np = sv.ports.find((p) => p.node_port && (p.protocol === "" || p.protocol === "TCP"));
      const kids: HTMLElement[] = [];
      if (np?.node_port) {
        const port = np.node_port;
        const open = iconBtn({ "data-testid": `btn-svc-open-${sv.name}` }, "globe", t("gui.k8s.open"));
        open.onclick = () =>
          void api.k8sOpen(port).catch((err) => {
            this.st.lastError = err instanceof ApiError ? err.message : String(err);
            this.renderBanner();
          });
        kids.push(open);
      }
      const builtin = sv.namespace === "default" && sv.name === "kubernetes";
      const rm = iconBtn({ "data-testid": `btn-svc-rm-${sv.name}`, class: "danger" }, "trash", t("gui.remove"));
      rm.disabled = builtin || this.st.busy.has(key);
      if (!builtin) {
        rm.onclick = () =>
          void this.resourceAction(key, fmt(t("gui.k8s.confirm.delete"), "service", sv.name), async () => {
            await api.k8sDelete("services", sv.namespace, sv.name);
            return fmt(t("gui.k8s.deleted"), sv.name);
          });
      }
      kids.push(rm);
      const targets = serviceTargets(g, sv);
      tbody.append(
        el("tr", { "data-testid": "svc-row", "data-name": sv.name },
          el("td", { class: "name-cell two-line" },
            el("span", { class: "ct-name", "data-testid": "svc-name", text: sv.name, title: sv.name }),
            el("span", { class: "sub muted", "data-testid": "svc-target", text: targets.length ? `→ ${targets.join(", ")}` : "" }),
          ),
          el("td", {}, el("span", { class: "kind-chip svc", "data-testid": "svc-type", text: sv.type })),
          this.k8sCell("svc-ip", sv.cluster_ip, "mono muted"),
          this.k8sCell("svc-ports", ports, "mono"),
          this.k8sCell("svc-endpoints", sv.selector && Object.keys(sv.selector).length ? String(endpointCount(sv, this.st.k8sPods)) : "–"),
          this.k8sCell("svc-age", ageText(sv.created_at), "muted"),
          el("td", { class: "actions" }, ...kids),
        ),
      );
    }
    wrap.append(tbl);
    const rules = this.st.k8sExtras.ingresses.filter((i) => i.namespace === ns).flatMap((i) => i.rules.map((r) => ({ ing: i.name, ...r })));
    if (rules.length) {
      wrap.append(
        el("h3", { class: "k8s-section", text: t("gui.k8s.ingress_routes") }),
        el("div", { class: "route-list", "data-testid": "ingress-routes" },
          ...rules.map((r) =>
            el("div", { class: "route", "data-testid": "ingress-route", "data-service": r.service },
              el("span", { class: "kind-chip ing", text: "Ingress" }),
              el("span", { class: "mono route-path", text: `${r.host ?? ""}${r.path}` }),
              el("span", { class: "ns-arrow", "aria-hidden": "true", text: "→" }),
              el("span", { class: "mono route-target", text: `${r.service}:${r.port}` }),
            ),
          ),
        ),
      );
    }
    return wrap;
  }

  // ---- Config／Storage 分頁 ----------------------------------------------------------

  private k8sConfigTab(ns: string): HTMLElement {
    const wrap = el("div", { class: "k8s-resources" });
    const items = this.st.k8sExtras.configmaps.filter((c) => c.namespace === ns);
    if (items.length === 0) return wrap.append(this.k8sEmpty("gui.empty.k8s_config")), wrap;
    const g = buildNamespaceGraph(ns, this.k8sRes());
    const { table: tbl, tbody } = this.k8sTable("config", K8S_CONFIG_COLS);
    for (const c of items) {
      tbody.append(
        el("tr", { "data-testid": "cm-row", "data-name": c.name },
          el("td", { class: "name-cell" }, el("span", { class: "ct-name", "data-testid": "cm-name", text: c.name, title: c.name })),
          el("td", {}, el("span", { class: "kind-chip cm", text: "ConfigMap" })),
          this.k8sCell("cm-keys", String(c.keys)),
          this.k8sCell("cm-used", usedBy(g, `cm:${c.name}`).join(", ") || "–"),
          this.k8sCell("cm-age", ageText(c.created_at), "muted"),
        ),
      );
    }
    wrap.append(tbl);
    return wrap;
  }

  private k8sStorageTab(ns: string): HTMLElement {
    const wrap = el("div", { class: "k8s-resources" });
    const items = this.st.k8sExtras.pvcs.filter((c) => c.namespace === ns);
    if (items.length === 0) return wrap.append(this.k8sEmpty("gui.empty.k8s_storage")), wrap;
    const g = buildNamespaceGraph(ns, this.k8sRes());
    const { table: tbl, tbody } = this.k8sTable("storage", K8S_STORAGE_COLS);
    for (const p of items) {
      tbody.append(
        el("tr", { "data-testid": "pvc-row", "data-name": p.name },
          el("td", { class: "name-cell" }, el("span", { class: "ct-name", "data-testid": "pvc-name", text: p.name, title: p.name })),
          el("td", {}, el("span", { class: `state ${p.status === "Bound" ? "running" : p.status === "Pending" ? "pending" : "failed"}`, "data-testid": "pvc-status", text: p.status })),
          this.k8sCell("pvc-size", p.size ?? "–"),
          this.k8sCell("pvc-class", p.storage_class ?? "–", "mono"),
          this.k8sCell("pvc-used", usedBy(g, `pvc:${p.name}`).join(", ") || "–"),
          this.k8sCell("pvc-age", ageText(p.created_at), "muted"),
        ),
      );
    }
    wrap.append(tbl);
    return wrap;
  }

  /** 沒有專屬分頁的資源（Lease、Role、Secret…）的唯讀表；沒有時回 null。 */
  private k8sOthersTable(ns: string): HTMLElement | null {
    const items = this.st.k8sExtras.others.filter((c) => c.namespace === ns);
    if (items.length === 0) return null;
    const { table: tbl, tbody } = this.k8sTable("others", K8S_OTHER_COLS);
    for (const o of [...items].sort((a, b) => a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name))) {
      tbody.append(
        el("tr", { "data-testid": "other-row", "data-name": o.name, "data-kind": o.kind },
          el("td", { class: "name-cell" }, el("span", { class: "ct-name", "data-testid": "other-name", text: o.name, title: o.name })),
          el("td", {}, el("span", { class: "kind-chip cm", "data-testid": "other-kind", text: o.kind })),
          this.k8sCell("other-age", ageText(o.created_at), "muted"),
        ),
      );
    }
    return tbl;
  }

  /** Other 分頁。 */
  private k8sOthersTab(ns: string): HTMLElement {
    const wrap = el("div", { class: "k8s-resources" });
    const tbl = this.k8sOthersTable(ns);
    if (!tbl) return wrap.append(this.k8sEmpty("gui.empty.k8s_others")), wrap;
    wrap.append(tbl);
    return wrap;
  }

  // ---- 右側面板 ------------------------------------------------------------------------

  /** 右側面板（寬度 50%）：選到 namespace 時顯示「Architecture／Pods／Workloads／Services／Config／Storage」；點 Deployment 進入詳情。 */
  private renderK8sSide(): void {
    const side = this.root.querySelector<HTMLElement>('[data-testid="k8s-side"]');
    const body = this.root.querySelector<HTMLElement>('[data-testid="k8s-body"]');
    if (!side || !body) return;
    const k = this.st.k8s;
    const ns = this.st.k8sNs;
    const visible = !!ns && !!k && k.enabled && k.status.state === "ready" && !k.op && !this.k8sListError;
    side.hidden = !visible;
    body.classList.toggle("with-side", visible);
    if (!visible) {
      side.replaceChildren();
      return;
    }
    const sum = summarizeNamespace(ns, this.k8sRes());
    const close = iconBtn({ "data-testid": "k8s-side-close" }, "close", t("gui.k8s.side.close"));
    close.onclick = () => this.k8sSelectNs("");
    const detail = this.st.k8sDetail && this.st.k8sDetail.ns === ns ? this.st.k8sDeployments.find((d) => d.namespace === ns && d.name === this.st.k8sDetail?.name) : undefined;
    if (this.st.k8sDetail && !detail) this.st.k8sDetail = undefined; // 該 Deployment 已不存在或換了 namespace
    if (detail) {
      const back = iconBtn({ "data-testid": "k8s-detail-back" }, "chevron-left", t("gui.k8s.detail.back"));
      back.onclick = () => {
        this.st.k8sDetail = undefined;
        this.renderK8sSide();
      };
      const crumbs = el("div", { class: "k8s-crumbs" }, back, el("span", { class: "muted", text: ns }), el("span", { class: "muted", text: "/" }), el("span", { class: "muted", text: t("gui.k8s.tab.workloads") }), el("span", { class: "muted", text: "/" }), el("strong", { "data-testid": "k8s-side-title", text: detail.name }));
      side.replaceChildren(el("div", { class: "k8s-side-head" }, crumbs, close), el("div", { class: "k8s-side-body", "data-testid": "k8s-side-body" }, this.k8sDeploymentDetail(detail)));
      return;
    }
    const head = el("div", { class: "k8s-side-head" },
      el("div", { class: "k8s-side-titles" },
        el("strong", { "data-testid": "k8s-side-title", text: ns }),
        ...(sum.health === "warn" || sum.health === "error" ? [el("span", { class: `ns-health ${sum.health}`, text: t(`gui.k8s.health.${sum.health}`) })] : []),
        el("span", { class: "muted", "data-testid": "k8s-side-sub", text: fmt(t("gui.k8s.side.sub"), String(sum.pods), String(sum.workloads), String(sum.services)) }),
      ),
      close,
    );
    const tabs = this.segmented(
      "k8s-tabs",
      K8S_TABS.map((id) => ({ id, label: t(`gui.k8s.tab.${id}`) })),
      this.st.k8sTab,
      (id) => {
        this.st.k8sTab = id as K8sTab;
        saveJSON(LS_K8S_TAB, id);
        this.renderK8sSide();
      },
    );
    const content = el("div", { class: "k8s-side-body", "data-testid": "k8s-side-body" });
    const tab = this.st.k8sTab;
    content.append(tab === "architecture" ? this.k8sArchitecture(ns) : tab === "pods" ? this.k8sPodsTab(ns) : tab === "workloads" ? this.k8sWorkloadsTab(ns) : tab === "services" ? this.k8sServicesTab(ns) : tab === "config" ? this.k8sConfigTab(ns) : tab === "storage" ? this.k8sStorageTab(ns) : this.k8sOthersTab(ns));
    side.replaceChildren(head, el("div", { class: "k8s-side-tabs" }, tabs), content);
    if (tab === "architecture") this.layoutArchitecture(content);
  }

  /** 選取（或再點一次取消）namespace，更新卡片選取狀態與側欄。 */
  /** 切換 Workloads／System 頁籤；已開的 namespace 不在新頁籤時一併關閉側欄。 */
  private k8sSelectGroup(g: K8sGroup): void {
    if (this.k8sGroup === g) return;
    this.k8sGroup = g;
    saveJSON(LS_K8S_GROUP, g);
    if (this.st.k8sNs && isSystemNs(this.st.k8sNs) !== (g === "system")) {
      this.st.k8sNs = "";
      this.st.k8sDetail = undefined;
    }
    this.renderKubernetes();
  }

  private k8sSelectNs(ns: string): void {
    this.st.k8sNs = this.st.k8sNs === ns ? "" : ns;
    this.st.k8sDetail = undefined;
    this.renderKubernetes();
  }

  private k8sOpenDetail(ns: string, name: string): void {
    this.st.k8sDetail = { ns, name };
    this.st.k8sDetailData = undefined;
    this.renderK8sSide();
    void this.refreshK8sDetail();
  }

  /** 抓 rollout 歷史與事件；失敗時詳情頁仍顯示其他區塊。 */
  private async refreshK8sDetail(): Promise<void> {
    const d = this.st.k8sDetail;
    if (!d) return;
    try {
      const data = await api.k8sDeploymentDetail(d.ns, d.name);
      if (this.st.k8sDetail?.name !== d.name || this.st.k8sDetail.ns !== d.ns) return;
      this.st.k8sDetailData = data;
    } catch {
      this.st.k8sDetailData = { revisions: [], events: [] };
    }
    this.renderK8sSide();
  }

  // ---- Deployment 詳情 ---------------------------------------------------------------

  private k8sDeploymentDetail(d: K8sDeployment): HTMLElement {
    const g = buildNamespaceGraph(d.namespace, this.k8sRes());
    const col = g.columns.find((c) => c.workload?.id === `dep:${d.name}`);
    const pods = col?.pods ?? [];
    const [rdy, want] = d.ready.split("/").map(Number);
    const available = want === 0 || rdy === want;
    const c = this.k8sScaleControls(d);
    const root = el("div", { class: "k8s-detail", "data-testid": "k8s-detail" });
    const card = (label: string, ...kids: (Node | string)[]) => el("div", { class: "detail-card" }, el("span", { class: "muted", text: label }), ...kids);
    const cms = [...new Set(pods.flatMap((p) => p.config_maps ?? []))];
    const strategy = d.strategy
      ? [d.strategy, d.max_surge ? `maxSurge ${d.max_surge}` : "", d.max_unavailable ? `maxUnavailable ${d.max_unavailable}` : ""].filter(Boolean)
      : [];
    root.append(
      el("div", { class: "detail-title" },
        el("strong", { class: "detail-name", text: d.name }),
        el("span", { class: "kind-chip", text: "Deployment" }),
        el("span", { class: `ns-health ${available ? "ok" : "warn"}`, "data-testid": "detail-status", text: t(available ? "gui.k8s.detail.available" : "gui.k8s.detail.unavailable") }),
      ),
      el("div", { class: "deploy-actions detail-actions" }, c.stepper, c.apply, c.restart, this.k8sDeleteDeployment(d)),
      el("div", { class: "detail-cards" },
        card(t("gui.k8s.detail.replicas"), el("strong", { class: "detail-big", "data-testid": "detail-replicas", text: `${rdy} / ${d.replicas}` }), this.readyBar(d.ready)),
        card(t("gui.k8s.detail.strategy"), el("strong", { "data-testid": "detail-strategy", text: strategy[0] ?? "–" }), el("span", { class: "muted", text: strategy.slice(1).join(" · ") })),
        card(t("gui.k8s.detail.image"), el("strong", { class: "mono", "data-testid": "detail-image", text: d.image ?? "–", title: d.image ?? "" }), el("span", { class: "muted", "data-testid": "detail-env", text: cms.length ? fmt(t("gui.k8s.detail.env"), cms.join(", ")) : "" })),
      ),
    );
    const data = this.st.k8sDetailData;
    const section = (title: string, testid: string, rows: HTMLElement[], emptyKey: string) =>
      el("section", { class: "detail-section" }, el("h3", { class: "k8s-section", text: title }), el("div", { class: "detail-rows", "data-testid": testid }, ...(rows.length ? rows : [el("div", { class: "muted detail-empty", text: data ? t(emptyKey) : t("gui.loading") })])));
    root.append(
      section(
        t("gui.k8s.detail.history"),
        "detail-history",
        (data?.revisions ?? []).map((r) =>
          el("div", { class: "detail-row", "data-testid": "detail-rev", "data-current": String(r.current) },
            el("strong", { class: "rev-no", text: `#${r.revision}` }),
            el("span", { class: "mono rev-image", text: r.image }),
            el("span", { class: "muted", text: ageText(r.created_at) }),
            el("span", { class: `kind-chip${r.current ? "" : " muted-chip"}`, text: t(r.current ? "gui.k8s.detail.current" : "gui.k8s.detail.previous") }),
          ),
        ),
        "gui.k8s.detail.no_history",
      ),
      section(
        fmt(t("gui.k8s.detail.pods"), String(pods.length)),
        "detail-pods",
        pods.map((p) => {
          const logs = iconBtn({ "data-testid": `btn-detail-pod-logs-${p.name}` }, "logs", t("gui.logs"));
          logs.onclick = () => this.openPodLogs(p.namespace, p.name);
          return el("div", { class: "detail-row", "data-testid": "detail-pod", "data-name": p.name },
            el("span", { class: "mono rev-image", text: p.name }),
            el("span", { class: p.restarts > 0 ? "warn-text" : "muted", text: fmt(t("gui.k8s.detail.restarts"), String(p.restarts)) }),
            el("span", { class: `state ${podStateClass(p.status)}`, text: p.status }),
            logs,
          );
        }),
        "gui.empty.k8s_pods",
      ),
      section(
        t("gui.k8s.detail.events"),
        "detail-events",
        (data?.events ?? []).map((e) =>
          el("div", { class: "detail-row", "data-testid": "detail-event" },
            el("strong", { class: "event-reason", text: e.reason }),
            el("span", { class: "event-msg", text: e.message, title: e.message }),
            el("span", { class: "muted", text: ageText(e.last_seen) }),
          ),
        ),
        "gui.k8s.detail.no_events",
      ),
    );
    return root;
  }

  // ---- 架構圖 ---------------------------------------------------------------------------

  /** 架構圖：Ingress → Service → workload（每個一欄，下接其 Pods）← ConfigMap／PVC；連線以 SVG 疊在上面。 */
  private k8sArchitecture(ns: string): HTMLElement {
    const g = buildNamespaceGraph(ns, this.k8sRes());
    const root = el("div", { class: "arch", "data-testid": "arch" });
    if (g.ingresses.length + g.services.length + g.columns.length + g.mounts.length === 0) {
      // 圖是空的但 namespace 裡有 Lease／Role 等資源：直接列出，不要只說「沒有資源」
      const others = this.k8sOthersTable(ns);
      if (others) root.append(el("p", { class: "muted", "data-testid": "arch-only-others", text: t("gui.k8s.arch.only_others") }), others);
      else root.append(this.k8sEmpty("gui.k8s.arch.empty"));
      return root;
    }
    const node = (cls: string, testid: string, name: string, nodeId: string, kind: string, sub: string, tag: "div" | "button" = "div") =>
      el(tag, { class: `arch-node ${cls}`, "data-testid": testid, "data-name": name, "data-node": nodeId, ...(tag === "button" ? { type: "button" } : {}) },
        el("span", { class: "arch-kind", text: kind }),
        el("strong", { text: name, title: name }),
        el("span", { class: "muted", text: sub }),
      );
    const rows: HTMLElement[] = [];
    if (g.ingresses.length) {
      rows.push(el("div", { class: "arch-row" }, ...g.ingresses.map((i) => node("ing", "arch-ing", i.name, `ing:${i.name}`, "Ingress", [...new Set(i.rules.map((r) => r.host).filter(Boolean))].join(", ")))));
    }
    if (g.services.length) {
      rows.push(
        el("div", { class: "arch-row arch-services" }, ...g.services.map((s) => {
          const n = node("svc", "arch-svc", s.name, `svc:${s.name}`, "Service", [s.type, s.ports.map((p) => `${p.port}/${p.protocol}`).join(", ")].filter(Boolean).join(" · "));
          const exposed = nodePortsOf(s);
          if (exposed.length) {
            n.classList.add("exposed");
            n.append(el("span", { class: "arch-exposed", "data-testid": "arch-exposed", text: `NodePort ${exposed.join(", ")}`, title: t("gui.k8s.exposed") }));
          }
          return n;
        })),
      );
    }
    const cols = el("div", { class: "arch-cols" }, ...g.columns.map((c) => {
      const w = c.workload;
      const col = el("div", { class: "arch-col" });
      if (w) {
        const isDep = w.kind === "Deployment";
        const n = node(isDep ? "dep" : "sts", isDep ? "arch-deploy" : "arch-sts", w.name, w.id, w.kind, [w.ready, w.image].filter(Boolean).join(" · "), isDep ? "button" : "div");
        if (isDep) n.onclick = () => this.k8sOpenDetail(ns, w.name);
        col.append(n);
      }
      const group = el("div", { class: "arch-node pods", "data-testid": "arch-pods", "data-owner": w?.name ?? "", "data-node": `pods:${w?.name ?? ""}` },
        el("span", { class: "arch-kind", text: w ? "Pods" : t("gui.k8s.arch.orphans") }),
        ...c.pods.map((p) => {
          const chip = el("button", { type: "button", class: "arch-pod", "data-testid": "arch-pod", "data-name": p.name, title: `${p.name} · ${p.status}` },
            el("span", { class: `state ${podStateClass(p.status)}` }),
            el("span", { class: "mono", text: p.name }),
          );
          chip.onclick = () => this.openPodLogs(p.namespace, p.name);
          return chip;
        }),
      );
      if (c.pods.length === 0) group.append(el("span", { class: "muted", text: t("gui.k8s.arch.no_pods") }));
      col.append(group);
      return col;
    }));
    rows.push(cols);
    if (g.mounts.length) {
      rows.push(el("div", { class: "arch-row arch-mounts" }, ...g.mounts.map((m) => node(m.kind === "ConfigMap" ? "cm" : "pvc", "arch-mount", m.name, m.id, m.kind === "ConfigMap" ? "ConfigMap" : "PVC", m.detail))));
    }
    const edges = [...g.edges];
    for (const c of g.columns) if (c.workload && c.pods.length) edges.push({ from: c.workload.id, to: `pods:${c.workload.name}` });
    root.dataset.edges = JSON.stringify(edges);
    root.append(svgEl("svg", { class: "arch-edges", "aria-hidden": "true" }) as unknown as Node, ...rows);
    return root;
  }

  /** 依實際版面量測各方塊位置，畫出曲線連線（往下或往上）；視窗縮放時重畫。 */
  private layoutArchitecture(content: HTMLElement): void {
    const root = content.querySelector<HTMLElement>(".arch");
    const svg = root?.querySelector<SVGSVGElement>(".arch-edges");
    if (!root || !svg) return;
    const draw = () => {
      if (!root.isConnected) return;
      const origin = root.getBoundingClientRect();
      svg.setAttribute("width", String(root.scrollWidth));
      svg.setAttribute("height", String(root.scrollHeight));
      const pos = (id: string) => {
        const n = root.querySelector<HTMLElement>(`[data-node="${CSS.escape(id)}"]`);
        if (!n) return undefined;
        const r = n.getBoundingClientRect();
        return { x: r.left - origin.left + r.width / 2, top: r.top - origin.top, bottom: r.bottom - origin.top };
      };
      const edges = JSON.parse(root.dataset.edges ?? "[]") as { from: string; to: string; label?: string; mount?: boolean }[];
      svg.replaceChildren(
        ...edges.flatMap((e) => {
          const a = pos(e.from);
          const b = pos(e.to);
          if (!a || !b) return [];
          // 起點在上方 → 由 a 底部連到 b 頂部；起點在下方（掛載）→ 由 a 頂部連到 b 底部
          const down = a.bottom <= b.top + 1;
          const [y1, y2] = down ? [a.bottom, b.top] : [a.top, b.bottom];
          const my = (y1 + y2) / 2;
          const path = svgEl("path", { class: `arch-edge${e.mount ? " mount" : ""}`, "data-testid": "arch-edge", "data-from": e.from, "data-to": e.to, d: `M ${a.x} ${y1} C ${a.x} ${my}, ${b.x} ${my}, ${b.x} ${y2}`, fill: "none" });
          if (e.label) {
            const title = svgEl("title");
            title.textContent = e.label;
            path.append(title);
          }
          return [path];
        }),
      );
    };
    requestAnimationFrame(draw);
    new ResizeObserver(draw).observe(root);
  }

  // ---- 設定側欄 --------------------------------------------------------------

  private applyTheme(theme: string): void {
    const root = document.documentElement;
    if (theme === "system" || !theme) root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", theme);
  }

  /** 容器頁的區網轉發；未開啟或查詢失敗時清空（不影響容器清單）。 */
  private async loadLANForwards(signal?: AbortSignal): Promise<void> {
    let enabled = false;
    let forwards: LANForward[] = [];
    if (this.st.meta.lan_supported) {
      try {
        const r = await api.lanForwards(signal);
        enabled = r.enabled;
        forwards = r.enabled ? r.forwards : [];
      } catch (e) {
        if (signal?.aborted) throw e;
      }
    }
    this.st.lanEnabled = enabled;
    this.st.lanForwards = forwards;
  }

  /** 容器列的「在瀏覽器開啟」按鈕；常駐（各列一致，圖示對齊）。
   *  有成功的區網轉發時開區網位址；否則（區網存取關閉、該容器沒有轉發或轉發失敗）用 http://localhost:<published 埠> 開啟。
   *  一個位址直接開啟；多個位址（多個 published 埠）跳小選單；沒有位址時停用；區網轉發全部失敗時紅色並以 title 說明原因。 */
  private browserButton(c: Container): HTMLButtonElement {
    const n = name(c);
    const mine = this.st.lanEnabled ? this.st.lanForwards.filter((f) => f.container === n) : [];
    const lanURLs = mine.filter((f) => !f.error).map(lanURL);
    const failed = mine.filter((f) => f.error);
    const urls = lanURLs.length ? lanURLs : localURLs(c);
    const lines = [...(urls.length ? [fmt(t("gui.browser.open"), urls.join("\n"))] : [t("gui.browser.none")])];
    if (!lanURLs.length && failed.length) lines.push(...failed.map((f) => `${t("gui.lan.tag")}: ${lanURL(f)} — ${f.error}`));
    const b = iconBtn({ "data-testid": `btn-browser-${n}`, class: failed.length && !lanURLs.length ? "warn" : "" }, "globe", lines.join("\n"));
    b.disabled = urls.length === 0;
    b.onclick = (e) => {
      e.stopPropagation();
      if (urls.length === 1) this.openBrowserURL(urls[0]);
      else this.toggleLANMenu(b, urls);
    };
    return b;
  }

  /** 區網位址經 /api/lan/open、localhost 經 /api/open-local（後端各自驗證）。 */
  private openBrowserURL(url: string): void {
    const local = /^http:\/\/localhost:(\d+)$/.exec(url);
    const p = local ? api.openLocal(Number(local[1])) : api.openLAN(url);
    void p.catch((err) => {
      this.st.lastError = err instanceof ApiError ? err.message : String(err);
      this.renderBanner();
    });
  }

  /** 多個位址時的小選單（掛在按鈕下方；點外面或選一項即關閉）。 */
  private toggleLANMenu(anchor: HTMLElement, urls: string[]): void {
    const existing = document.querySelector<HTMLElement>('[data-testid="lan-menu"]');
    existing?.remove();
    if (existing && existing.dataset.for === anchor.dataset.testid) return;
    const menu = el("div", { class: "lan-menu", role: "menu", "data-testid": "lan-menu", "data-for": anchor.dataset.testid ?? "" });
    for (const u of urls) {
      const item = el("button", { type: "button", role: "menuitem", class: "mono", "data-testid": "lan-menu-item", text: u.replace(/^http:\/\//, "") }) as HTMLButtonElement;
      item.onclick = (e) => {
        e.stopPropagation();
        menu.remove();
        this.openBrowserURL(u);
      };
      menu.append(item);
    }
    const r = anchor.getBoundingClientRect();
    menu.style.top = `${r.bottom + 4}px`;
    menu.style.left = `${Math.max(8, r.right - 220)}px`;
    document.body.append(menu);
    const close = (ev: Event) => {
      if (ev.target instanceof Node && menu.contains(ev.target)) return;
      menu.remove();
      document.removeEventListener("click", close, true);
    };
    setTimeout(() => document.addEventListener("click", close, true));
  }

  /** Info 用：該容器的區網位址文字（每行一個；失敗的附原因）。 */
  private lanText(c: Container): string {
    return this.st.lanForwards
      .filter((f) => f.container === name(c))
      .map((f) => (f.error ? `${lanURL(f)} — ${f.error}` : lanURL(f)))
      .join("\n");
  }

  // ---- 區網存取（設定頁「網路」） ---------------------------------------------

  private async loadLAN(): Promise<void> {
    if (this.st.lanLoading) return;
    this.st.lanLoading = true;
    try {
      this.st.lan = await api.getLAN();
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    } finally {
      this.st.lanLoading = false;
    }
    if (this.st.view === "settings") this.renderViewBody();
  }

  /** 開／關區網存取：寫設定並立即生效。開啟時若 wsld-gui 尚未通過防火牆，接著顯示說明對話框（使用者確認後才跳 UAC）。 */
  private async setLAN(enabled: boolean): Promise<void> {
    this.st.lanApplying = "toggle";
    this.renderViewBody();
    let ok = false;
    try {
      this.st.lan = await api.putLAN(enabled);
      ok = true;
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    } finally {
      this.st.lanApplying = undefined;
    }
    if (ok) this.notify(t(enabled ? "gui.lan.done_on" : "gui.lan.done_off"));
    this.renderViewBody();
    const fw = this.st.lan?.firewall;
    if (ok && enabled && (fw === "missing" || fw === "blocked")) await this.askFirewall();
  }

  // ---- 防火牆授權說明對話框 ----------------------------------------------------

  private firewallResolve?: (ok: boolean) => void;

  /** UAC 本身看不出在授權什麼：先由 wsld 說明要加的規則，以及接下來 UAC 會顯示哪個程式。 */
  private buildFirewallDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "fw-dialog", "aria-labelledby": "fw-dialog-title" }) as HTMLDialogElement;
    const ok = btn({ class: "primary", type: "button", "data-testid": "fw-dialog-continue" }, "shield", t("gui.lan.fw.continue"));
    const later = btn({ type: "button", "data-testid": "fw-dialog-later" }, "close", t("gui.lan.fw.later"));
    ok.onclick = () => {
      this.firewallResolve?.(true);
      this.firewallResolve = undefined;
      d.close();
    };
    later.onclick = () => d.close();
    d.addEventListener("close", () => {
      this.firewallResolve?.(false);
      this.firewallResolve = undefined;
    });
    d.append(
      el("h2", { id: "fw-dialog-title", "data-testid": "fw-dialog-title" }),
      el("p", { "data-testid": "fw-dialog-intro" }),
      el("dl", { class: "fw-rule", "data-testid": "fw-dialog-rule" }),
      el("p", { class: "fw-method", "data-testid": "fw-dialog-method" }),
      el("div", { class: "row" }, later, ok),
    );
    return d;
  }

  /** 顯示說明對話框；按「繼續」才以 UAC 加入規則。 */
  private async askFirewall(): Promise<void> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="fw-dialog"]');
    const s = this.st.lan;
    if (!d || !s || d.open) return;
    const method = s.firewall_method === "helper" ? "helper" : "netsh";
    d.querySelector('[data-testid="fw-dialog-title"]')!.textContent = t("gui.lan.fw.title");
    d.querySelector('[data-testid="fw-dialog-intro"]')!.textContent = t("gui.lan.fw.intro");
    const rows: [string, string][] = [
      ["gui.lan.fw.rule.name", "wsld-gui (LAN access)"],
      ["gui.lan.fw.rule.program", s.exe || "wsld-gui.exe"],
      ["gui.lan.fw.rule.protocol", "TCP"],
      ["gui.lan.fw.rule.networks", t("gui.lan.fw.rule.networks_value")],
      ["gui.lan.fw.rule.remote", t("gui.lan.fw.rule.remote_value")],
    ];
    d.querySelector('[data-testid="fw-dialog-rule"]')!.replaceChildren(
      ...rows.flatMap(([k, v]) => [el("dt", { text: t(k) }), el("dd", { class: k.endsWith("program") ? "mono" : "", text: v })]),
    );
    const m = d.querySelector<HTMLElement>('[data-testid="fw-dialog-method"]')!;
    m.dataset.method = method;
    m.textContent = t(`gui.lan.fw.method.${method}`);
    d.querySelector('[data-testid="fw-dialog-continue"]')!.replaceChildren(icon("shield"), el("span", { class: "label", text: t("gui.lan.fw.continue") }));
    d.querySelector('[data-testid="fw-dialog-later"]')!.replaceChildren(icon("close"), el("span", { class: "label", text: t("gui.lan.fw.later") }));
    const go = await new Promise<boolean>((resolve) => {
      this.firewallResolve = resolve;
      d.showModal();
    });
    if (go) await this.allowFirewall();
  }

  /** 以 UAC 替 wsld-gui.exe 加入對內允許規則（私人＋公用網路、TCP、僅同網段）；只在說明對話框按「繼續」後呼叫。 */
  private async allowFirewall(): Promise<void> {
    this.st.lanApplying = "firewall";
    this.st.lastError = undefined;
    this.renderBanner();
    this.renderViewBody();
    try {
      this.st.lan = await api.allowFirewall();
      this.notify(t("gui.lan.fw.done"));
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    } finally {
      this.st.lanApplying = undefined;
    }
    this.renderViewBody();
  }

  /** 還原 NAT：清掉 0.9.0 的 mirrored 設定（UAC）→ 重啟 WSL（等 API 埠釋放後再起引擎）。 */
  private async restoreNAT(): Promise<void> {
    if (!(await this.confirm(t("gui.lan.confirm_restore"), undefined, { label: t("gui.lan.restore"), icon: "power", danger: false }))) return;
    this.st.lanApplying = "restore";
    this.st.busy.add("lan"); // 暫停輪詢：WSL 重啟期間 API 本來就不通
    this.st.lastError = undefined;
    this.renderBanner();
    this.renderViewBody();
    let failed = false;
    try {
      this.st.lan = await api.restoreNAT();
      this.notify(t("gui.lan.done_restore"));
    } catch (e) {
      failed = true;
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.lanApplying = undefined;
      this.st.busy.delete("lan");
    }
    if (failed) {
      // 不立即刷新：設定頁的輪詢會清掉 lastError，使用者看不到失敗原因（例如取消 UAC）
      this.renderBanner();
      this.renderViewBody();
      return;
    }
    await this.refresh();
    this.renderViewBody(); // 輪詢刷新不重建設定頁，還原完成要自己重繪
  }

  /** 防火牆 UAC 還在等使用者回應（例如離開設定頁後再回來）：留在設定頁時每 1.5 秒重查，回應後更新提示。 */
  private lanPendingTimer?: number;
  private pollLANWhilePending(): void {
    if (this.lanPendingTimer !== undefined) return;
    this.lanPendingTimer = window.setTimeout(() => {
      this.lanPendingTimer = undefined;
      if (this.st.view === "settings" && !this.st.lanApplying) void this.loadLAN();
    }, 1500);
  }

  private lanSection(row: (label: Node, hint: string, control: Node) => HTMLElement): HTMLElement {
    const s = this.st.lan;
    if (!s && !this.st.lanLoading && !this.st.lanApplying) void this.loadLAN();
    const applying = this.st.lanApplying;
    const on = !!s?.enabled;
    const toggle = el("button", {
      type: "button",
      class: "toggle",
      role: "switch",
      "data-testid": "settings-lan",
      "aria-checked": on ? "true" : "false",
    }) as HTMLButtonElement;
    toggle.setAttribute("aria-label", t("gui.settings.lan"));
    toggle.disabled = !s || !!applying;
    const state = el("span", { class: "switch-state", "aria-hidden": "true", text: t(on ? "gui.on" : "gui.off") });
    toggle.onclick = () => void this.setLAN(!on);

    const okForwards = (s?.forwards ?? []).filter((f) => !f.error);
    let statusText = t("gui.lan.status.loading");
    if (applying === "restore") statusText = t("gui.lan.status.restoring");
    else if (s && !s.enabled) statusText = t("gui.lan.status.off");
    else if (s && s.mirrored) statusText = t("gui.lan.status.blocked");
    else if (s && (s.firewall === "missing" || s.firewall === "blocked")) statusText = t("gui.lan.status.firewall");
    else if (s && s.forwards.length === 0) statusText = t("gui.lan.status.idle");
    else if (s && okForwards.length === 0) statusText = t("gui.lan.status.failed").replace("%d", String(s.forwards.length));
    else if (s) statusText = t("gui.lan.status.on").replace("%d", String(okForwards.length));
    const status = el("span", { "data-testid": "settings-lan-status", text: statusText });

    // 區網存取開啟、wsld-gui 尚未通過防火牆時提示（轉發暫停）；按鈕先開說明對話框，確認後才跳 UAC
    const extra: HTMLElement[] = [];
    const fw = s?.enabled && !s.mirrored ? s.firewall : "";
    const fwWaiting = applying === "firewall" || !!s?.firewall_pending;
    if (s?.firewall_pending && applying !== "firewall") this.pollLANWhilePending();
    if (s?.enabled && !s.mirrored && (fwWaiting || fw === "missing" || fw === "blocked")) {
      const box = el("div", { class: "lan-warn", "data-testid": "settings-lan-fw-warn", "data-state": fwWaiting ? "pending" : fw, role: "note" },
        el("p", { text: t(fwWaiting ? "gui.lan.fw.allowing" : "gui.lan.fw.warn") }));
      const allow = btn({ type: "button", "data-testid": "settings-lan-fw-allow" }, "shield", t("gui.lan.fw.allow"));
      allow.disabled = !!applying || fwWaiting;
      allow.onclick = () => void this.askFirewall();
      box.append(el("div", {}, allow));
      extra.push(box);
    }
    if (s?.mirrored) {
      const box = el("div", { class: "lan-warn", "data-testid": "settings-lan-mirrored", role: "note" }, el("p", { text: t("gui.lan.warn.mirrored") }));
      const restore = btn({ type: "button", "data-testid": "settings-lan-restore" }, "power", t("gui.lan.restore"));
      restore.disabled = !!applying;
      restore.onclick = () => void this.restoreNAT();
      box.append(el("div", {}, restore));
      extra.push(box);
    }
    return el("section", { class: "set-group", "data-testid": "settings-lan-group" },
      el("h2", { text: t("gui.settings.group.network") }),
      row(el("span", { "data-testid": "settings-lan-label", text: t("gui.settings.lan") }), t("gui.settings.lan_hint"), el("span", { class: "switch" }, state, toggle)),
      row(el("span", { text: t("gui.settings.lan_status") }), t("gui.settings.lan_status_hint"), status),
      ...extra,
    );
  }

  private renderSettings(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="settings-list"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !bar) return;
    const c = this.st.meta.config;
    const interval = el("input", { type: "number", min: "1", max: "3600", "data-testid": "settings-refresh", value: String(c.refresh_interval_sec) }) as HTMLInputElement;
    const shell = el("input", { type: "text", class: "mono", "data-testid": "settings-exec-shell", placeholder: "bash → sh", spellcheck: "false", value: c.exec_shell || "" }) as HTMLInputElement;

    const auto = el("button", {
      type: "button",
      class: "toggle",
      role: "switch",
      "data-testid": "settings-autostart",
      "aria-checked": c.tray_autostart ? "true" : "false",
    }) as HTMLButtonElement;
    auto.setAttribute("aria-label", t("gui.settings.autostart"));
    auto.disabled = !this.st.meta.autostart_supported;
    // 狀態文字（開／關）放在開關左側，不只靠顏色
    const autoState = el("span", { class: "switch-state", "data-testid": "settings-autostart-state", "aria-hidden": "true", text: t(c.tray_autostart ? "gui.on" : "gui.off") });
    auto.onclick = () => {
      const on = auto.getAttribute("aria-checked") !== "true";
      auto.setAttribute("aria-checked", on ? "true" : "false");
      autoState.textContent = t(on ? "gui.on" : "gui.off");
    };

    const themeVal = c.theme || "system";
    const themeRadios = el("div", { class: "radio-group", "data-testid": "settings-theme", role: "radiogroup", "aria-labelledby": "settings-theme-label" });
    for (const [v, key] of [
      ["light", "gui.settings.theme.light"],
      ["dark", "gui.settings.theme.dark"],
      ["system", "gui.settings.theme.system"],
    ] as const) {
      const id = `settings-theme-${v}`;
      const radio = el("input", { type: "radio", name: "settings-theme", value: v, id, "data-testid": id }) as HTMLInputElement;
      radio.checked = themeVal === v;
      themeRadios.append(el("label", { class: "check", for: id }, radio, t(key)));
    }

    const save = btn({ class: "primary", "data-testid": "settings-save" }, "save", t("gui.save"));
    save.onclick = async () => {
      const picked = themeRadios.querySelector<HTMLInputElement>('input[name="settings-theme"]:checked');
      const theme = picked?.value || "system";
      const ok = await this.saveSettings({
        refresh_interval_sec: Number(interval.value),
        exec_shell: shell.value.trim(),
        tray_autostart: auto.getAttribute("aria-checked") === "true",
        lang: this.st.meta.config.lang,
        theme,
      });
      if (ok) {
        this.applyTheme(theme);
        this.notify(t("gui.save"));
      }
    };
    // SettingsCard：左邊標題＋說明，右邊控制項；分「外觀」「行為」兩組
    const row = (label: Node, hint: string, control: Node) =>
      el("div", { class: "set-row" }, el("div", { class: "set-text" }, label, el("span", { class: "set-hint", text: hint })), el("div", { class: "set-control" }, control));
    interval.id = "settings-refresh";
    shell.id = "settings-exec-shell";
    // Save 在頁標題列右側（工具列）
    bar.replaceChildren(save);
    list.classList.add("cards");
    const brand = brandMark();
    brand.removeAttribute("data-testid");
    list.replaceChildren(
      el(
        "form",
        { class: "settings-form", "data-testid": "settings-form" },
        el("section", { class: "set-group" },
          el("h2", { text: t("gui.settings.group.look") }),
          row(el("span", { id: "settings-theme-label", text: t("gui.settings.theme") }), t("gui.settings.theme_hint"), themeRadios),
          row(el("label", { for: "settings-lang", text: t("gui.lang") }), t("gui.settings.lang_hint"), this.langSelect()),
        ),
        el("section", { class: "set-group" },
          el("h2", { text: t("gui.settings.group.behave") }),
          row(el("span", { "data-testid": "settings-autostart-label", text: t("gui.settings.autostart") }), t("gui.settings.auto_hint"), el("span", { class: "switch" }, autoState, auto)),
          row(el("label", { for: "settings-refresh", text: t("gui.settings.refresh") }), t("gui.settings.refresh_hint"), numberField(interval, t("gui.unit.sec"))),
          row(el("label", { for: "settings-exec-shell", text: t("gui.settings.exec_shell") }), t("gui.settings.shell_hint"), textField(shell)),
        ),
        ...(this.st.meta.lan_supported ? [this.lanSection(row)] : []),
        el("section", { class: "set-group" },
          el("h2", { text: t("gui.settings.group.about") }),
          el("div", { class: "set-row", "data-testid": "settings-about" },
            brand,
            el("div", { class: "set-text" },
              el("span", {}, `${t("gui.title")} `, el("span", { class: "muted", text: `v${this.st.meta.version}` })),
              el("span", { class: "set-hint", text: t("gui.about.desc") }),
            ),
            el("div", { class: "set-control" }, this.githubLink()),
          ),
        ),
      ),
    );
  }
}

function name(c: Container): string {
  return (c.Names?.[0] ?? c.Id.slice(0, 12)).replace(/^\//, "");
}

function infoField(label: string, value: string, testId?: string): HTMLElement {
  const ddAttrs: Record<string, string> = { text: value, title: value };
  if (testId) ddAttrs["data-testid"] = testId;
  return el("div", { class: "info-field" }, el("dt", { text: label }), el("dd", ddAttrs));
}

/** 刷新重建 Info body 後還原捲動（同步 + 下一幀，避免 layout 把位置清掉）。 */
function restoreInfoScroll(body: HTMLElement, scrollTop: number): void {
  if (scrollTop <= 0) return;
  body.scrollTop = scrollTop;
  requestAnimationFrame(() => {
    body.scrollTop = scrollTop;
  });
}

function clampPct(n: number): number {
  if (!Number.isFinite(n) || n < 0) return 0;
  if (n > 100) return 100;
  return Math.round(n * 10) / 10;
}

function memPct(used: number, total: number): number {
  if (!total || total <= 0) return 0;
  return clampPct((used / total) * 100);
}

function formatCPU(pct: number): string {
  const v = clampPct(pct);
  return `${Number.isInteger(v) ? v : v.toFixed(1)}%`;
}

function formatMem(used: number, total: number): string {
  const pct = memPct(used, total);
  return `${used}/${total} MB (${Number.isInteger(pct) ? pct : pct.toFixed(1)}%)`;
}

function formatDisk(used: number, total: number): string {
  const pct = memPct(used, total);
  return `${humanSize(used)} / ${humanSize(total)} (${Number.isInteger(pct) ? pct : pct.toFixed(1)}%)`;
}

/** 新增機器對話框的下載進度區塊（沿用 usage-meter 的 track／fill 樣式）。 */
function installProgressBlock(): HTMLElement {
  const fill = el("div", { class: "usage-fill" });
  fill.style.width = "0%";
  return el(
    "div",
    { class: "install-progress", "data-testid": "add-machine-progress", role: "status", "aria-live": "polite", hidden: "" },
    el("div", { class: "usage-head" },
      el("span", { class: "usage-label", "data-testid": "add-machine-progress-label" }),
      el("span", { class: "usage-value", "data-testid": "add-machine-progress-pct" }),
    ),
    el("div", { class: "usage-track" }, fill),
    el("div", { class: "install-progress-detail", "data-testid": "add-machine-progress-detail" }),
  );
}

function updateInstallProgress(block: HTMLElement, p: InstallProgress): void {
  const phase = p.phase ?? "installing";
  const known = phase === "downloading" && p.total > 0;
  const pct = known ? clampPct(Math.floor((p.downloaded / p.total) * 100)) : phase === "installing" && p.total > 0 ? 100 : 0;
  block.setAttribute("data-phase", phase);
  block.setAttribute("data-pct", String(pct));
  if (known) block.removeAttribute("data-indeterminate");
  else block.setAttribute("data-indeterminate", "true");
  block.querySelector<HTMLElement>(".usage-fill")!.style.width = `${pct}%`;
  block.querySelector<HTMLElement>('[data-testid="add-machine-progress-label"]')!.textContent = t(`gui.machines.progress_${phase}`);
  block.querySelector<HTMLElement>('[data-testid="add-machine-progress-pct"]')!.textContent = known ? `${pct}%` : "";
  const parts: string[] = [];
  if (phase === "downloading" && p.downloaded > 0) {
    parts.push(p.total > 0 ? `${humanSize(p.downloaded)} / ${humanSize(p.total)}` : humanSize(p.downloaded));
    if (p.bytes_per_sec > 0) parts.push(`${humanSize(p.bytes_per_sec)}/s`);
  }
  parts.push(fmt(t("gui.machines.progress_elapsed"), fmtElapsed(p.elapsed_sec)));
  block.querySelector<HTMLElement>('[data-testid="add-machine-progress-detail"]')!.textContent = parts.join(" · ");
}

/** 秒數 → m:ss（滿一小時 → h:mm:ss）。 */
function fmtElapsed(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = String(s % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${ss}` : `${m}:${ss}`;
}

function usageMeter(label: string, valueText: string, pct: number, testId: string): HTMLElement {
  const fill = el("div", { class: "usage-fill" });
  fill.style.width = `${pct}%`;
  const track = el("div", { class: "usage-track" }, fill);
  const head = el("div", { class: "usage-head" },
    el("span", { class: "usage-label", text: label }),
    el("span", { class: "usage-value", text: valueText }),
  );
  return el("div", { class: "usage-meter", "data-testid": testId, "data-pct": String(pct) }, head, track);
}

// ---- 欄位與分組 -----------------------------------------------------------

interface Col {
  key: string;
  label: string;
  def: number;
  min: number;
  resizable: boolean;
}

const COLS: Col[] = [
  // 總和 < 預設視窗內容寬（1100px − 側欄 232px − 邊距 48px），避免一開就出現水平捲軸
  { key: "name", label: "gui.col.name", def: 220, min: 80, resizable: true },
  { key: "image", label: "gui.col.image", def: 230, min: 80, resizable: true },
  { key: "state", label: "gui.col.state", def: 130, min: 90, resizable: true },
  { key: "size", label: "gui.col.size", def: 90, min: 70, resizable: true },
	{ key: "actions", label: "gui.col.actions", def: 240, min: 210, resizable: false },
];

const IMAGE_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 260, min: 100, resizable: true },
  { key: "id", label: "gui.col.id", def: 120, min: 90, resizable: true },
  { key: "size", label: "gui.col.size", def: 90, min: 70, resizable: true },
  { key: "created", label: "gui.col.created", def: 160, min: 90, resizable: true },
  { key: "inuse", label: "gui.col.in_use", def: 80, min: 60, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 60, min: 50, resizable: false },
];

const VOLUME_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 220, min: 100, resizable: true },
  { key: "driver", label: "gui.col.driver", def: 90, min: 60, resizable: true },
  { key: "size", label: "gui.col.size", def: 90, min: 70, resizable: true },
  { key: "mountpoint", label: "gui.col.mountpoint", def: 300, min: 120, resizable: true },
  { key: "created", label: "gui.col.created", def: 160, min: 90, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 60, min: 50, resizable: false },
];

const NETWORK_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 220, min: 100, resizable: true },
  { key: "driver", label: "gui.col.driver", def: 100, min: 60, resizable: true },
  { key: "scope", label: "gui.col.scope", def: 90, min: 60, resizable: true },
  { key: "id", label: "gui.col.id", def: 120, min: 90, resizable: true },
  { key: "created", label: "gui.col.created", def: 150, min: 90, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 60, min: 50, resizable: false },
];

const K8S_POD_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 260, min: 120, resizable: true },
  { key: "namespace", label: "gui.col.namespace", def: 130, min: 90, resizable: true },
  { key: "status", label: "gui.col.state", def: 150, min: 110, resizable: true },
  { key: "ready", label: "gui.col.ready", def: 80, min: 64, resizable: true },
  { key: "restarts", label: "gui.col.restarts", def: 100, min: 84, resizable: true },
  { key: "age", label: "gui.col.age", def: 60, min: 50, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 130, min: 100, resizable: false },
];

const K8S_SERVICE_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 200, min: 100, resizable: true },
  { key: "namespace", label: "gui.col.namespace", def: 130, min: 90, resizable: true },
  { key: "type", label: "gui.col.type", def: 110, min: 80, resizable: true },
  { key: "ip", label: "gui.col.cluster_ip", def: 120, min: 90, resizable: true },
  { key: "ports", label: "gui.col.ports", def: 160, min: 90, resizable: true },
  { key: "endpoints", label: "gui.col.endpoints", def: 100, min: 70, resizable: true },
  { key: "age", label: "gui.col.age", def: 60, min: 50, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 80, min: 60, resizable: false },
];

const K8S_CONFIG_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 240, min: 120, resizable: true },
  { key: "type", label: "gui.col.type", def: 110, min: 80, resizable: true },
  { key: "keys", label: "gui.col.keys", def: 70, min: 50, resizable: true },
  { key: "used", label: "gui.col.used_by", def: 160, min: 90, resizable: true },
  { key: "age", label: "gui.col.age", def: 60, min: 50, resizable: true },
];

const K8S_OTHER_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 280, min: 120, resizable: true },
  { key: "type", label: "gui.col.type", def: 130, min: 80, resizable: true },
  { key: "age", label: "gui.col.age", def: 60, min: 50, resizable: true },
];

const K8S_STORAGE_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 200, min: 120, resizable: true },
  { key: "status", label: "gui.col.state", def: 100, min: 80, resizable: true },
  { key: "size", label: "gui.col.size", def: 80, min: 60, resizable: true },
  { key: "class", label: "gui.col.storage_class", def: 120, min: 80, resizable: true },
  { key: "used", label: "gui.col.used_by", def: 140, min: 90, resizable: true },
  { key: "age", label: "gui.col.age", def: 60, min: 50, resizable: true },
];

function emptyK8sExtras(): K8sExtras {
  return { statefulsets: [], ingresses: [], configmaps: [], pvcs: [], others: [] };
}

/** 舊版存的分頁名（deployments）與無效值一律收斂到有效分頁。 */
function normalizeK8sTab(v: string): K8sTab {
  if (v === "deployments") return "workloads";
  return (K8S_TABS as string[]).includes(v) ? (v as K8sTab) : "architecture";
}

/** Pod 狀態 → 狀態點樣式（與容器列共用 .state）。 */
function podStateClass(status: string): string {
  if (status === "Running" || status === "Succeeded" || status === "Completed") return "running";
  if (["Pending", "ContainerCreating", "PodInitializing", "Terminating"].includes(status) || status.startsWith("Init:")) return "pending";
  return "failed";
}

/** kubectl 風格的年齡：45s／12m／5h／3d。 */
function ageText(iso: string): string {
  const t0 = new Date(iso).getTime();
  if (Number.isNaN(t0)) return "";
  const sec = Math.max(0, (Date.now() - t0) / 1000);
  if (sec < 60) return `${Math.floor(sec)}s`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m`;
  if (sec < 48 * 3600) return `${Math.floor(sec / 3600)}h`;
  return `${Math.floor(sec / 86400)}d`;
}

const MACHINE_COLS: Col[] = [
  { key: "name", label: "gui.col.name", def: 240, min: 100, resizable: true },
  { key: "state", label: "gui.col.state", def: 110, min: 70, resizable: true },
  { key: "version", label: "gui.col.version", def: 70, min: 50, resizable: true },
  { key: "flags", label: "gui.col.flags", def: 160, min: 80, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 60, min: 50, resizable: false },
];

const LS_COLS = "wsld.colWidths";
const LS_COLLAPSED = "wsld.collapsedGroups";
const LS_VIEW = "wsld.view";
const LS_K8S_TAB = "wsld.k8s.tab";
const LS_K8S_GROUP = "wsld.k8s.group";
const LS_K8S_ADDCTX = "wsld.k8s.addcontext";
const LS_K8S_VIEW = "wsld.k8s.view";
const LS_K8S_SHOWSYS = "wsld.k8s.showsystem";
const LS_K8S_GROUP_SEEN = "wsld.k8s.groupSeen";
const LABEL_PROJECT = "com.docker.compose.project";
const LABEL_SERVICE = "com.docker.compose.service";

/** k3s（--docker）建立的容器（k8s_<容器>_<pod>_<ns>_…）摺成一個虛擬群組；不是 compose 專案。 */
const K8S_GROUP = "k8s:k3s";
const LABEL_K8S_POD = "io.kubernetes.pod.name";
const isK8sContainer = (c: Container): boolean => name(c).startsWith("k8s_") || !!c.Labels?.[LABEL_K8S_POD];

interface Group {
  project: string; // "" = 未分組；K8S_GROUP = k3s 容器
  items: Container[];
}

/** 依 compose 專案分組；完全沒有 compose 容器時回傳 null（維持平面列表）。 */
function groupContainers(cs: Container[]): Group[] | null {
  const byProject = new Map<string, Container[]>();
  let anyCompose = false;
  for (const c of cs) {
    let p = c.Labels?.[LABEL_PROJECT] ?? "";
    if (!p && isK8sContainer(c)) p = K8S_GROUP;
    if (p) anyCompose = true;
    (byProject.get(p) ?? byProject.set(p, []).get(p)!).push(c);
  }
  if (!anyCompose) return null;
  const groups: Group[] = [...byProject.entries()]
    .filter(([p]) => p !== "")
    // k3s 群組排在 compose 專案之後、未分組之前
    .sort(([a], [b]) => (a === K8S_GROUP ? 1 : 0) - (b === K8S_GROUP ? 1 : 0) || a.localeCompare(b))
    .map(([project, items]) => ({ project, items: items.sort((a, b) => (a.Labels?.[LABEL_SERVICE] ?? name(a)).localeCompare(b.Labels?.[LABEL_SERVICE] ?? name(b))) }));
  const rest = byProject.get("");
  if (rest) groups.push({ project: "", items: rest.sort((a, b) => name(a).localeCompare(name(b))) });
  return groups;
}

function loadJSON<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : fallback;
  } catch {
    return fallback;
  }
}

function saveJSON(key: string, v: unknown): void {
  try {
    localStorage.setItem(key, JSON.stringify(v));
  } catch {
    /* ignore quota / privacy errors */
  }
}

/** 區網轉發的 URL（IPv6 加中括號）。 */
function lanURL(f: LANForward): string {
  return `http://${f.address.includes(":") ? `[${f.address}]` : f.address}:${f.port}`;
}

/** 執行中容器已發佈 TCP 埠的 localhost 網址（去重、依埠排序）。 */
function localURLs(c: Container): string[] {
  if (c.State !== "running") return [];
  const ps = new Set((c.Ports ?? []).filter((p) => p.PublicPort && (!p.Type || p.Type === "tcp")).map((p) => p.PublicPort));
  return [...ps].sort((a, b) => a - b).map((p) => `http://localhost:${p}`);
}

/** 已發佈埠；dual-stack（0.0.0.0 與 ::）會各回一筆，去重後依對外埠排序。 */
function ports(c: Container): string {
  const seen = new Set<string>();
  return (c.Ports ?? [])
    .filter((p) => p.PublicPort)
    .sort((a, b) => a.PublicPort - b.PublicPort)
    .map((p) => `${p.PublicPort}→${p.PrivatePort}/${p.Type}`)
    .filter((s) => !seen.has(s) && seen.add(s))
    .join(", ");
}

// ---- 格式化 -----------------------------------------------------------------

/** 依序以 args 取代 %s／%d（與 Go 端 i18n.T 的訊息格式相容）。 */
/** 與後端 wsl.MinVHDSizeGB／MaxVHDSizeGB／DefaultVHDSizeGB 一致（defaults API 取不到時使用） */
const VHD_MIN_GB = 8;
const VHD_MAX_GB = 16384;
const DEFAULT_VHD_GB = 60;

/** 磁碟上限輸入：空 → 0（WSL 預設）；範圍內整數 → 該值；其他 → undefined（無效）。 */
function parseVHDSize(raw: string, [min, max]: [number, number]): number | undefined {
  const v = raw.trim();
  if (v === "") return 0;
  if (!/^\d+$/.test(v)) return undefined;
  const n = Number(v);
  return n >= min && n <= max ? n : undefined;
}

/** withExposureConfirm 的取消標記 */
const CANCELLED = Symbol("cancelled");

function fmt(s: string, ...args: string[]): string {
  let i = 0;
  return s.replace(/%[sd]/g, () => args[i++] ?? "");
}

function shortID(id: string): string {
  return id.replace(/^sha256:/, "").slice(0, 12);
}

/** 映像使用狀態：daemon 未計算（Containers = -1）時歸為 unknown，不硬猜。 */
function imageKind(im: Image): ImgKind {
  if (im.Containers > 0) return "inuse";
  if (imageName(im) === "<none>") return "dangling";
  if (im.Containers < 0) return "unknown";
  return "unused";
}

const KIND_LABEL: Record<ImgKind, string> = {
  inuse: "gui.filter.in_use",
  unused: "gui.filter.unused",
  dangling: "gui.filter.dangling",
  unknown: "gui.filter.unknown",
};
const KIND_TAG: Record<ImgKind, string> = { inuse: "accent", unused: "", dangling: "warn", unknown: "" };

function imageName(im: Image): string {
  const tag = im.RepoTags?.[0];
  return !tag || tag === "<none>:<none>" ? "<none>" : tag;
}

interface UsagePart {
  key: string;
  label: string;
  size: number;
}

/** 長條最多幾段有自己的顏色（--series-1..5）；其餘併為「其他」。 */
const USAGE_TOP = 5;

/** 頁標題旁的磁碟用量鈕：120px 迷你長條＋總量，固定一列高、不隨項目數變高；
 *  點開浮出明細（每項一行：色塊、名稱、大小、占比），Esc／點外面收起。
 *  前 5 大各一段（依名稱排序後依序取 --series-1..5，長條與明細同一順序：大小些微變動不會換色，
 *  相鄰兩段也一定是相鄰色槽＝調色盤的 CVD 安全順序），其餘併為「其他」。滑過分段有 title。 */
function usageButton(parts: UsagePart[], open: boolean, onToggle: (open: boolean) => void): HTMLElement | undefined {
  const items = parts.filter((p) => p.size > 0).sort((a, b) => b.size - a.size || a.label.localeCompare(b.label));
  const total = items.reduce((a, p) => a + p.size, 0);
  if (!total) return undefined;
  const top = items.slice(0, USAGE_TOP).sort((a, b) => a.label.localeCompare(b.label));
  const rest = items.slice(USAGE_TOP);
  const segs = top.map((p, i) => ({ ...p, cls: `s${i + 1}` }));
  if (rest.length) {
    segs.push({ key: "__other", label: fmt(t("gui.disk.other"), String(rest.length)), size: rest.reduce((a, p) => a + p.size, 0), cls: "other" });
  }
  const pct = (n: number) => {
    const v = (n / total) * 100;
    return v < 1 ? "<1%" : `${Math.round(v)}%`;
  };
  const bar = el("span", { class: "disk-bar", "data-testid": "usage-bar", "aria-hidden": "true" });
  for (const p of segs) {
    const seg = el("span", { class: `disk-seg ${p.cls}`, "data-key": p.key, title: `${p.label} · ${humanSize(p.size)} · ${pct(p.size)}` });
    seg.style.width = `${(p.size / total) * 100}%`;
    bar.append(seg);
  }
  const btn = el("button", {
    type: "button",
    class: "usage-btn",
    "data-testid": "usage-btn",
    "aria-expanded": String(open),
    "aria-controls": "usage-pop",
    "aria-label": fmt(t("gui.disk.show"), humanSize(total)),
  }, bar, el("strong", { "data-testid": "usage-total", "data-size": String(total), text: humanSize(total) }), icon("chevron-down"));
  btn.onclick = () => onToggle(!open);
  const wrap = el("span", { class: "usage-wrap" }, btn);
  wrap.onkeydown = (e) => {
    if (e.key !== "Escape" || !open) return;
    e.preventDefault();
    e.stopPropagation();
    onToggle(false);
  };
  if (open) {
    const list = el("div", { class: "usage-list" });
    for (const p of segs) {
      list.append(
        el("span", { class: "usage-part", "data-testid": "usage-part", "data-key": p.key, "data-size": String(p.size) },
          el("span", { class: `disk-swatch ${p.cls}`, "aria-hidden": "true" }),
          el("span", { class: "usage-name", text: p.label, title: p.label }),
          el("strong", { text: humanSize(p.size) }),
          el("span", { class: "muted", text: pct(p.size) }),
        ),
      );
    }
    wrap.append(
      el("div", { class: "usage-pop", id: "usage-pop", role: "group", "data-testid": "usage-pop", "aria-label": t("gui.disk.title") },
        el("div", { class: "usage-pop-head" }, el("strong", { text: t("gui.disk.title") }), el("span", { class: "muted", text: humanSize(total) })),
        list,
      ),
    );
  }
  return wrap;
}

/** 二進位單位（與 docker CLI 一致的 1024 進位），保留一位小數。 */
function humanSize(n: number): string {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${u === 0 ? v : v.toFixed(1)} ${units[u]}`;
}

function fmtDate(d: Date): string {
  if (Number.isNaN(d.getTime())) return "";
  const p = (x: number) => String(x).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

function fmtUnix(sec: number): string {
  return sec ? fmtDate(new Date(sec * 1000)) : "";
}

function fmtISO(s: string): string {
  return s ? fmtDate(new Date(s)) : "";
}
