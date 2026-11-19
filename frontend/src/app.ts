import {
  api,
  ApiError,
  type Container,
  type Image,
  type InstallProgress,
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

type Panel = { kind: "logs"; id: string; name: string; live: boolean; ws?: WebSocket } | { kind: "term"; id: string; name: string; session?: TerminalSession };
type InfoDrawer =
  | { kind: "container"; id: string; name: string; detail?: import("./api").ContainerInfo }
  | { kind: "compose"; project: string }
  | { kind: "machine"; name: string; detail?: import("./api").MachineConfig };

/** 側欄視圖。 */
export type View = "containers" | "images" | "volumes" | "networks" | "machines" | "settings";
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
  lastError?: string;
  /** 最近一次「啟動引擎／設為目前」失敗的原因；引擎就緒前輪詢不覆蓋，避免真正原因被「無法連線 API」蓋掉 */
  bringUpError?: string;
  notice?: string;
  starting: boolean;
  /** 正在切換的 target distro 名稱；有值時 header 顯示 Loading／Switching */
  switching?: string;
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
  lanApplying?: "toggle" | "restore";
  /** 容器頁的區網轉發（隨容器清單刷新；未開啟區網存取時為空） */
  lanForwards: LANForward[];
  /** 區網存取已開啟（容器列顯示瀏覽器按鈕） */
  lanEnabled: boolean;
  imgFilter: ImgFilter;
  /** 進行中的資源動作（images/volumes 刪除、prune、machine install） */
  busy: Set<string>;
}

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
      paused: false,
      refreshCount: 0,
      pending: {},
      busy: new Set(),
      ctFilter: "all",
      ctQuery: "",
      imgFilter: "all",
      lanForwards: [],
      lanEnabled: false,
    };
    if (!VIEWS.some((v) => v.id === this.st.view)) this.st.view = "containers";
    this.build();
    this.applyTheme(this.st.meta.config.theme || "system");
    this.bindPause();
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
      if (!this.st.paused && this.st.busy.size === 0) void this.refresh();
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
    try {
      const status = await api.status(signal);
      if (signal.aborted || gen !== this.refreshGen) return;
      this.st.status = status;
      if (status.ready) this.st.bringUpError = undefined;
      if (this.st.view === "machines" || this.st.view === "settings") {
        // WSL 清單／設定不依賴 Docker 引擎
        if (this.st.view === "machines") this.st.machines = await api.machines(signal);
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
        // 用 i18n 前綴；細節走 status.APIError（renderBanner 會拼成「前綴 — 細節」）
        this.st.lastError = this.st.bringUpError ?? t("gui.error.api");
      }
    } catch (e) {
      if (signal.aborted || gen !== this.refreshGen) return;
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      if (this.st.status) this.st.status = { ...this.st.status, ready: false, APIReachable: false };
    }
    if (signal.aborted || gen !== this.refreshGen) return;
    this.st.refreshCount++;
    this.renderAll(true);
  }

  private async loadView(signal?: AbortSignal): Promise<void> {
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

  private setView(v: View): void {
    if (v === this.st.view) return;
    this.closePanel();
    this.closeInfo();
    this.st.view = v;
    if (v === "settings" && !this.st.lanApplying) this.st.lan = undefined; // 每次進設定頁重新查詢區網狀態
    saveJSON(LS_VIEW, v);
    this.renderNav();
    this.renderHeader();
    this.renderView();
    void this.refresh();
  }

  // ---- 動作 -----------------------------------------------------------------

  private async startEngine(): Promise<void> {
    this.st.starting = true;
    this.st.lastError = undefined;
    this.st.bringUpError = undefined;
    this.renderHeader();
    this.renderBanner();
    this.renderViewBody(); // Machines 列顯示 Starting…
    try {
      this.st.status = await api.startEngine();
      // Start 可能順便完成未就緒 distro 的 init／寫入 profile
      this.st.meta = await api.meta();
      setMessages(this.st.meta.messages);
      await this.loadView();
    } catch (e) {
      this.st.lastError = this.st.bringUpError = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.starting = false;
      this.renderAll();
    }
  }

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

  private notify(msg: string): void {
    this.st.notice = msg;
    this.renderNotice();
    if (this.noticeTimer) window.clearTimeout(this.noticeTimer);
    this.noticeTimer = window.setTimeout(() => {
      this.st.notice = undefined;
      this.renderNotice();
    }, 8000);
  }

  private openLogs(c: Container): void {
    this.closePanel();
    this.st.panel = { kind: "logs", id: c.Id, name: name(c), live: false };
    this.renderPanel();
    void this.loadLogs();
  }

  private async loadLogs(): Promise<void> {
    const p = this.st.panel;
    if (!p || p.kind !== "logs") return;
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
      const ws = new WebSocket(api.logsStreamUrl(p.id, 100));
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
      this.buildAddMachineDialog(),
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
  private engineState(): { state: "switching" | "starting" | "connected" | "disconnected"; text: string; dot: string } {
    if (this.st.switching) return { state: "switching", text: t("gui.status.switching"), dot: "warn" };
    if (this.st.starting) return { state: "starting", text: t("gui.status.starting"), dot: "warn" };
    if (this.st.status?.ready === true) return { state: "connected", text: t("gui.status.connected"), dot: "ok" };
    return { state: "disconnected", text: t("gui.status.disconnected"), dot: "off" };
  }

  /** 頂部列右側：發行版晶片（引擎狀態）＋資料夾／終端＋需要時才出現的 Start engine＋Settings；
   *  同時更新內容區頂端的頁標題與摘要。 */
  private renderHeader(): void {
    const actions = this.root.querySelector<HTMLElement>('[data-testid="top-actions"]')!;
    const ready = this.st.status?.ready === true;
    const switching = this.st.switching;
    const busy = !!switching || this.st.starting;

    const startBtn = btn({ class: "primary", "data-testid": "btn-start-engine" }, "power", t("gui.ensure"));
    // 未 init 的 active：即使舊引擎還連得上，也要能按 Start 觸發自動設定
    const needsStart = !ready || this.st.meta.active_ready === false;
    startBtn.disabled = busy || !needsStart;
    // 已連線就不佔頁首空間；啟動中保留（停用）讓使用者看到進度
    startBtn.hidden = !needsStart && !busy;
    startBtn.onclick = () => void this.startEngine();

    const distro = switching || this.st.status?.Distro || this.st.meta.distro;
    const hasDistro = !!distro && !switching;
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
      startBtn,
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
    );
  }

  /** 頁首副標：容器頁顯示執行中／已停止數量；資源頁顯示項目數（映像含總大小）；未連線時留空。 */
  private pageSubtitle(): string {
    const ready = this.st.status?.ready === true;
    switch (this.st.view) {
      case "containers": {
        if (!ready) return "";
        const running = this.st.containers.filter((c) => c.State === "running").length;
        return fmt(t("gui.ct.summary"), String(running), String(this.st.containers.length - running));
      }
      case "images":
        return ready ? fmt(t("gui.sub.items_size"), String(this.st.images.length), humanSize(this.st.images.reduce((a, im) => a + im.Size, 0))) : "";
      case "volumes":
        return ready ? fmt(t("gui.sub.items"), String(this.st.volumes.length)) : "";
      case "networks":
        return ready ? fmt(t("gui.sub.items"), String(this.st.networks.length)) : "";
      case "machines":
        return this.st.machines?.available ? fmt(t("gui.sub.items"), String(this.st.machines.machines.length)) : "";
      default:
        return "";
    }
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

  /** 引擎未就緒時取代空列表：說明＋啟動引擎＋由 status 推得的檢查清單。 */
  private engineDownState(): HTMLElement {
    const s = this.st.status;
    const distro = s?.Distro || this.st.meta.distro || t("gui.title");
    const starting = this.st.starting;
    const cta = btn({ class: "primary", "data-testid": "btn-engine-cta" }, "power", starting ? t("gui.status.starting") : t("gui.ensure"));
    cta.disabled = starting || !!this.st.switching;
    cta.onclick = () => void this.startEngine();
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
      cta,
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
    const switching = this.st.switching;
    const distro = switching || this.st.status?.Distro || this.st.meta.distro;
    const eng = this.engineState();
    const nameRow: (Node | string)[] = [
      el("span", { class: "distro-name", "data-testid": "header-distro", text: distro || t("gui.title"), title: distro ? `${t("gui.title")} — ${distro}` : t("gui.title") }),
    ];
    if (switching) nameRow.push(el("span", { class: "header-loading", "data-testid": "header-loading", text: t("gui.status.loading") }));
    const b = el("button", {
      type: "button",
      class: "distro-switch",
      "data-testid": "distro-switch",
      "aria-label": `${t("gui.nav.machines")}: ${distro || t("gui.title")} — ${eng.text}`,
      ...(switching || this.st.starting ? { "aria-busy": "true" } : {}),
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
    if (this.st.lastError && !this.st.starting && !this.st.switching) {
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
    n.textContent = this.st.notice ?? "";
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
    } else if (this.st.view === "machines") {
      main.replaceChildren(
        pageBar(toolbar()),
        el("div", { class: "list", "data-testid": "machines-list" }),
        el("section", { class: "panel", "data-testid": "panel", hidden: "" }),
      );
    } else if (this.st.view === "images") {
      main.replaceChildren(pageBar(toolbar()), el("div", { class: "disk-slot", "data-testid": "disk-slot" }), el("div", { class: "list", "data-testid": "images-list" }));
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
  };
  private collapsed: Set<string> = new Set(loadJSON<string[]>(LS_COLLAPSED, []));

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
    if (g.project) {
      // 只有 compose 專案才有整組 Start／Stop／Info；未分組只是雜項集合
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
        el("span", { class: "group-name", "data-testid": "group-name", text: g.project || "-" }),
        el("span", { class: "group-count", "data-testid": "group-count", text: `${running}/${g.items.length}` }),
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
    const lanBtn = this.lanButton(c);
    return el(
      "tr",
      rowAttrs,
      el("td", { class: `name-cell ${project ? "indent" : ""}`.trim() },
        el("span", { class: "ct-name", "data-testid": "ct-name", text: display, title: `${n}\n${c.Id}` }),
        el("span", { class: "ct-sub", "data-testid": `ct-status-${n}`, text: c.Status || c.State }),
      ),
      el("td", { text: c.Image, title: c.Image }),
      el("td", {}, el("span", { class: `state ${pending ? "pending" : c.State}`, "data-testid": `state-${n}`, text: stateText })),
      el("td", { class: "actions" }, startBtn, stopBtn, logsBtn, termBtn, ...(lanBtn ? [lanBtn] : []), infoBtn, rmBtn),
    );
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
    if (c) {
      title.textContent = p.name;
      const tabs = el("div", { class: "tabs", role: "tablist", "data-testid": "panel-tabs" });
      for (const [kind, ico, label] of [["logs", "logs", t("gui.logs")], ["term", "terminal", t("gui.terminal")]] as const) {
        const tab = btn({ type: "button", role: "tab", class: "tab", "data-testid": `panel-tab-${kind}`, "aria-selected": String(p.kind === kind) }, ico, label);
        if (kind === "term" && c.State !== "running") tab.disabled = true;
        tab.onclick = () => {
          if (this.st.panel?.kind === kind) return;
          if (kind === "logs") this.openLogs(c);
          else this.openTerminal(c);
        };
        tabs.append(tab);
      }
      kids.push(tabs);
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
          el("div", { class: `tag ${d.systemd ? "ok" : ""}`, "data-testid": "machine-config-systemd", text: d.systemd ? t("gui.machines.systemd_on") : t("gui.machines.systemd_off") }),
          el("pre", { class: "conf", "data-testid": "machine-config-conf", text: d.wsl_conf || t("gui.machines.no_conf") }),
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
    const disk = this.root.querySelector<HTMLElement>('[data-testid="disk-slot"]');
    disk?.replaceChildren(...(ready && this.st.images.length ? [this.diskUsage(this.st.images)] : []));

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

  /** 映像磁碟用量：使用中／未使用／懸空（／daemon 未統計）依大小分段，顏色以明度區分。 */
  private diskUsage(images: Image[]): HTMLElement {
    const sizes: Record<ImgKind, number> = { inuse: 0, unused: 0, dangling: 0, unknown: 0 };
    const counts: Record<ImgKind, number> = { inuse: 0, unused: 0, dangling: 0, unknown: 0 };
    for (const im of images) {
      const k = imageKind(im);
      sizes[k] += im.Size;
      counts[k]++;
    }
    const total = Object.values(sizes).reduce((a, b) => a + b, 0) || 1;
    const order: ImgKind[] = ["inuse", "unused", "dangling", "unknown"];
    const bar = el("div", { class: "disk-bar", role: "img", "data-testid": "disk-bar" });
    const legend = el("div", { class: "disk-legend" });
    const aria: string[] = [];
    for (const k of order) {
      if (!counts[k]) continue;
      const seg = el("span", { class: `disk-seg ${k}`, "data-kind": k });
      seg.style.width = `${(sizes[k] / total) * 100}%`;
      bar.append(seg);
      legend.append(
        el("span", { class: "disk-key", "data-testid": `disk-${k}` },
          el("span", { class: `disk-swatch ${k}`, "aria-hidden": "true" }),
          t(KIND_LABEL[k]),
          el("strong", { text: humanSize(sizes[k]) }),
          el("span", { class: "muted", text: `· ${counts[k]}` }),
        ),
      );
      aria.push(`${t(KIND_LABEL[k])} ${humanSize(sizes[k])}`);
    }
    bar.setAttribute("aria-label", aria.join(", "));
    return el("section", { class: "disk-card", "data-testid": "disk-usage", "aria-label": t("gui.disk.title") },
      el("div", { class: "disk-head" },
        el("strong", { text: t("gui.disk.title") }),
        el("span", { class: "muted", "data-testid": "disk-total", text: humanSize(images.reduce((a, im) => a + im.Size, 0)) }),
      ),
      bar,
      legend,
    );
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
          el("td", { "data-testid": "volume-mountpoint", class: "mono muted", text: v.Mountpoint, title: v.Mountpoint }),
          el("td", { class: "muted", text: fmtISO(v.CreatedAt) }),
          el("td", { class: "actions" }, rm),
        ),
      );
    }
    list.replaceChildren(table);
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
    const removingAny = [...this.st.busy].some((k) => k.startsWith("remove:"));
    const add = btn({ class: "primary", "data-testid": "btn-add-machine" }, "plus", t("gui.machines.add"));
    add.disabled = !m?.available || this.st.busy.has("install") || removingAny || !!this.st.switching || this.st.starting;
    add.onclick = () => this.openAddMachine();
    bar.replaceChildren(el("span", { class: "spacer" }), add);

    if (m && !m.available) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.machines.unavailable") }));
      return;
    }
    if (!m || m.machines.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty.machines") }));
      return;
    }
    const grid = el("div", { class: "machine-grid" });
    for (const d of m.machines) {
      const removing = this.st.busy.has(`remove:${d.name}`);
      const switching = this.st.switching === d.name;
      const starting = this.st.starting && d.active;
      const pending = removing ? "removing" : switching ? "switching" : starting ? "starting" : undefined;
      const locked = !!pending || removingAny || !!this.st.switching || this.st.starting;
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
      const rm = iconBtn({ "data-testid": `btn-machine-remove-${d.name}`, class: "danger" }, "trash", t("gui.machines.remove"));
      rm.disabled = locked;
      rm.onclick = () =>
        void this.resourceAction(
          `remove:${d.name}`,
          `${t("gui.machines.unregister_prompt")}\n${d.name}`,
          async () => {
            await api.unregisterMachine(d.name, d.name);
            this.hardCutActive();
            this.st.meta = await api.meta();
            setMessages(this.st.meta.messages);
            return fmt(t("gui.removed"), d.name);
          },
          d.name,
        );
      tools.push(cfg, folder, term, rm);

      const footKids: HTMLElement[] = [];
      if (!d.active) {
        const use = btn({ "data-testid": `btn-machine-use-${d.name}` }, "save", t("gui.machines.use"));
        use.disabled = locked;
        use.onclick = () => void this.useMachine(d.name);
        footKids.push(use);
      }
      footKids.push(el("span", { class: "spacer" }), ...tools);

      const flags: HTMLElement[] = [];
      if (d.active) flags.push(el("span", { class: "tag accent", "data-testid": "machine-flag-active", text: t("gui.machines.active"), title: d.api_endpoint || this.st.meta.api_endpoint }));
      if (d.managed && !d.ready) flags.push(el("span", { class: "tag warn", "data-testid": "machine-flag-not-ready", text: t("status.not_ready") }));
      if (d.default) flags.push(el("span", { class: "tag", "data-testid": "machine-flag-default", text: t("gui.machines.default") }));
      if (d.managed === false) flags.push(el("span", { class: "tag warn", "data-testid": "machine-flag-unmanaged", text: t("gui.machines.unmanaged") }));
      const stateText =
        pending === "removing"
          ? t("gui.machines.removing")
          : pending === "switching"
            ? t("gui.status.switching")
            : pending === "starting"
              ? t("gui.status.starting")
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
      if (d.active && (d.api_endpoint || this.st.meta.api_endpoint)) {
        meta.push(el("span", { class: "muted mono", "data-testid": "machine-endpoint", text: ` · ${d.api_endpoint || this.st.meta.api_endpoint}` }));
      }
      grid.append(
        el("article", attrs,
          el("div", { class: "machine-head" },
            el("span", { class: "machine-icon", "aria-hidden": "true" }, icon("machines")),
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
    list.replaceChildren(grid);
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
  }

  private async useMachine(name: string): Promise<void> {
    if (this.st.switching) return;
    this.st.switching = name;
    this.st.lastError = undefined;
    this.st.bringUpError = undefined;
    this.renderHeader();
    this.renderBanner();
    this.renderViewBody(); // 鎖住其他「設為目前」
    try {
      await api.useMachine(name);
      this.hardCutActive();
      this.st.meta = await api.meta();
      setMessages(this.st.meta.messages);
      this.notify(fmt(t("gui.machines.used"), name));
      await this.refresh();
    } catch (e) {
      this.st.lastError = this.st.bringUpError = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.switching = undefined;
      this.renderAll();
    }
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
    try {
      const detail = await api.machineConfig(name);
      if (this.st.info?.kind !== "machine" || this.st.info.name !== name) return;
      // 只就地更新 CPU／記憶體兩個元件，不重建整塊 Info
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
    const userErr = el("div", { class: "field-error", "data-testid": "add-machine-user-error", hidden: "", text: t("gui.machines.user_invalid") });
    const pwErr = el("div", { class: "field-error", "data-testid": "add-machine-password-error", hidden: "" });
    const err = el("div", { class: "field-error", "data-testid": "add-machine-error", hidden: "" });
    const progress = installProgressBlock();
    const install = btn({ class: "primary", type: "submit", "data-testid": "add-machine-install" }, "plus", t("gui.machines.install"));
    const cancel = btn({ type: "button", "data-testid": "add-machine-cancel" }, "close", t("gui.cancel"));
    // 與後端 wsl.ValidLinuxUsername／ValidLinuxPassword 一致
    const userOk = () => /^[a-z_][a-z0-9_-]{0,31}$/.test(user.value.trim()) && user.value.trim() !== "root";
    const pwProblem = (): string => {
      if (/[\r\n\0]/.test(pw.value)) return t("gui.machines.password_invalid");
      if (pw2.value !== "" && pw.value !== pw2.value) return t("gui.machines.password_mismatch");
      return "";
    };
    const syncInstallEnabled = () => {
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
        this.st.busy.has("install");
    };
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
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.machines.name") }), textField(name)),
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.machines.linux_user") }), textField(user), userErr),
      el("label", { class: "field" }, el("span", { class: "field-label", text: t("gui.machines.password") }), textField(pw)),
      el(
        "label",
        { class: "field" },
        el("span", { class: "field-label", text: t("gui.machines.password_confirm") }),
        textField(pw2),
        pwErr,
      ),
      el("div", { class: "field-hint", "data-testid": "add-machine-password-hint", text: t("gui.machines.password_hint") }),
      progress,
      err,
      el("div", { class: "row" }, cancel, install),
    );
    form.noValidate = true;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const dist = sel.value.trim();
      const nm = name.value.trim();
      const usr = user.value.trim();
      if (!dist || !nm || !userOk() || pwProblem() !== "" || pw.value !== pw2.value || this.st.busy.has("install")) return;
      const fields = [name, user, pw, pw2];
      install.disabled = true;
      cancel.disabled = true;
      sel.setDisabled(true);
      for (const f of fields) f.disabled = true;
      install.querySelector(".label")!.textContent = t("gui.machines.installing");
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
        const res = await api.installMachine(dist, nm, usr, pw.value);
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
        install.querySelector(".label")!.textContent = t("gui.machines.install");
        syncInstallEnabled();
        this.renderViewBody();
      }
    };
    d.append(el("h2", { text: t("gui.machines.add") }), form);
    return d;
  }

  private async openAddMachine(): Promise<void> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="add-machine-dialog"]')!;
    const sel = this.addMachineDistro!;
    const name = d.querySelector<HTMLInputElement>('[data-testid="add-machine-name"]')!;
    const user = d.querySelector<HTMLInputElement>('[data-testid="add-machine-user"]')!;
    const pws = d.querySelectorAll<HTMLInputElement>('[data-testid="add-machine-password"], [data-testid="add-machine-password2"]');
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
    cancel.disabled = false;
    sel.setOptions([], "", t("gui.machines.loading"));
    sel.setDisabled(true);
    install.disabled = true;
    d.showModal();
    try {
      const [online, defaults] = await Promise.all([
        api.machinesOnline(),
        api.installDefaults().catch(() => ({ user: "" })),
      ]);
      user.value = defaults.user || "";
      sel.setOptions(
        online.map((o) => [o.name, o.friendly && o.friendly !== o.name ? `${o.friendly} (${o.name})` : o.name] as const),
        "",
        t("gui.machines.select_placeholder"),
      );
      sel.setDisabled(false);
      name.disabled = false;
      user.disabled = false;
      for (const p of pws) p.disabled = false;
      install.disabled = true;
    } catch (e) {
      sel.setOptions([], "", t("gui.machines.select_placeholder"));
      err.textContent = e instanceof ApiError ? e.message : String(e);
      err.hidden = false;
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

  /** 容器列的「在瀏覽器開啟（區網）」按鈕；區網存取關閉時不顯示（各列一致，圖示對齊）。
   *  一個位址直接開啟；多個位址（多個 published 埠）跳小選單；沒有位址時停用；全部監聽失敗時紅色並以 title 說明原因。 */
  private lanButton(c: Container): HTMLButtonElement | null {
    if (!this.st.lanEnabled) return null;
    const n = name(c);
    const mine = this.st.lanForwards.filter((f) => f.container === n);
    const urls = mine.filter((f) => !f.error).map(lanURL);
    const failed = mine.filter((f) => f.error);
    let label = t("gui.lan.none_for_ct");
    if (urls.length) label = fmt(t("gui.lan.open"), urls.join("\n"));
    else if (failed.length) label = failed.map((f) => `${lanURL(f)} — ${f.error}`).join("\n");
    const b = iconBtn({ "data-testid": `btn-lan-${n}`, class: failed.length && !urls.length ? "warn" : "" }, "globe", label);
    b.disabled = urls.length === 0;
    b.onclick = (e) => {
      e.stopPropagation();
      if (urls.length === 1) this.openLANURL(urls[0]);
      else this.toggleLANMenu(b, urls);
    };
    return b;
  }

  private openLANURL(url: string): void {
    void api.openLAN(url).catch((err) => {
      this.st.lastError = err instanceof ApiError ? err.message : String(err);
      this.renderBanner();
    });
  }

  /** 多個區網位址時的小選單（掛在按鈕下方；點外面或選一項即關閉）。 */
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
        this.openLANURL(u);
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

  /** 開／關區網存取：立即生效（wsld-gui 開始在內網 IP 監聽；Windows 會自己跳防火牆的私人／公用確認）。 */
  private async setLAN(enabled: boolean): Promise<void> {
    this.st.lanApplying = "toggle";
    this.renderViewBody();
    try {
      this.st.lan = await api.putLAN(enabled);
      this.notify(t(enabled ? "gui.lan.done_on" : "gui.lan.done_off"));
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
    else if (s && s.forwards.length === 0) statusText = t("gui.lan.status.idle");
    else if (s && okForwards.length === 0) statusText = t("gui.lan.status.failed").replace("%d", String(s.forwards.length));
    else if (s) statusText = t("gui.lan.status.on").replace("%d", String(okForwards.length));
    const status = el("span", { "data-testid": "settings-lan-status", text: statusText });

    const extra: HTMLElement[] = [];
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
  { key: "image", label: "gui.col.image", def: 260, min: 80, resizable: true },
  { key: "state", label: "gui.col.state", def: 130, min: 90, resizable: true },
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
  { key: "mountpoint", label: "gui.col.mountpoint", def: 320, min: 120, resizable: true },
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
const LABEL_PROJECT = "com.docker.compose.project";
const LABEL_SERVICE = "com.docker.compose.service";

interface Group {
  project: string; // "" = 未分組
  items: Container[];
}

/** 依 compose 專案分組；完全沒有 compose 容器時回傳 null（維持平面列表）。 */
function groupContainers(cs: Container[]): Group[] | null {
  const byProject = new Map<string, Container[]>();
  let anyCompose = false;
  for (const c of cs) {
    const p = c.Labels?.[LABEL_PROJECT] ?? "";
    if (p) anyCompose = true;
    (byProject.get(p) ?? byProject.set(p, []).get(p)!).push(c);
  }
  if (!anyCompose) return null;
  const groups: Group[] = [...byProject.entries()]
    .filter(([p]) => p !== "")
    .sort(([a], [b]) => a.localeCompare(b))
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
