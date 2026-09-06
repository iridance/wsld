import {
  api,
  ApiError,
  type Container,
  type Image,
  type MachinesResponse,
  type Meta,
  type Network,
  type Settings,
  type Status,
  type Volume,
} from "./api";
import { setMessages, t } from "./i18n";
import { openTerminal, type TerminalSession } from "./terminal";
import { icon, type IconName } from "./icons";

type Panel = { kind: "logs"; id: string; name: string; live: boolean; ws?: WebSocket } | { kind: "term"; id: string; name: string; session?: TerminalSession };
type InfoDrawer =
  | { kind: "container"; id: string; name: string; detail?: import("./api").ContainerInfo }
  | { kind: "compose"; project: string }
  | { kind: "machine"; name: string; detail?: import("./api").MachineConfig };

/** 側欄視圖。 */
export type View = "containers" | "images" | "volumes" | "networks" | "machines" | "settings";
const VIEWS: { id: View; icon: IconName; label: string }[] = [
  { id: "containers", icon: "containers", label: "gui.nav.containers" },
  { id: "images", icon: "images", label: "gui.nav.images" },
  { id: "volumes", icon: "volumes", label: "gui.nav.volumes" },
  { id: "networks", icon: "networks", label: "gui.nav.networks" },
  { id: "machines", icon: "machines", label: "gui.nav.machines" },
  { id: "settings", icon: "settings", label: "gui.nav.settings" },
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
      if (this.st.view === "machines" || this.st.view === "settings") {
        // WSL 清單／設定不依賴 Docker 引擎
        if (this.st.view === "machines") this.st.machines = await api.machines(signal);
        if (signal.aborted || gen !== this.refreshGen) return;
        this.st.lastError = undefined;
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
        this.st.lastError = status.APIError || t("gui.error.api");
      }
    } catch (e) {
      if (signal.aborted || gen !== this.refreshGen) return;
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      if (this.st.status) this.st.status = { ...this.st.status, ready: false, APIReachable: false };
    }
    if (signal.aborted || gen !== this.refreshGen) return;
    this.st.refreshCount++;
    this.renderAll();
  }

  private async loadView(signal?: AbortSignal): Promise<void> {
    switch (this.st.view) {
      case "containers":
        this.st.containers = await api.containers(signal);
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
    saveJSON(LS_VIEW, v);
    this.renderNav();
    this.renderView();
    void this.refresh();
  }

  // ---- 動作 -----------------------------------------------------------------

  private async startEngine(): Promise<void> {
    this.st.starting = true;
    this.st.lastError = undefined;
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
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
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
    this.st.info = undefined;
    this.renderInfoDrawer();
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
      el("header", { "data-testid": "header" }),
      el("div", { class: "body" },
        el("nav", { class: "side", "data-testid": "side-nav", "aria-label": "Views" }),
        el("main"),
        el("aside", { class: "info-drawer", "data-testid": "info-drawer", hidden: "" }),
      ),
      el("div", { class: "dock", "data-testid": "dock" },
        el("div", { class: "toasts", "data-testid": "toasts" },
          el("div", { class: "banner", "data-testid": "error-banner", role: "alert", hidden: "" }),
          el("div", { class: "notice", "data-testid": "notice", role: "status", hidden: "" }),
        ),
        el("footer", { "data-testid": "status-bar" }),
      ),
      this.buildConfirmDialog(),
      this.buildAddMachineDialog(),
    );
    this.renderNav();
    this.renderView();
    this.renderAll();
  }

  private renderAll(): void {
    this.renderHeader();
    this.renderBanner();
    this.renderNotice();
    this.renderViewBody();
    this.renderInfoDrawer();
    this.renderFooter();
  }

  private renderHeader(): void {
    const h = this.root.querySelector("header")!;
    const ready = this.st.status?.ready === true;
    const switching = this.st.switching;
    const busy = !!switching || this.st.starting;
    const badgeCls = switching || this.st.starting ? "warn" : ready ? "ok" : "bad";
    const badgeTxt = switching
      ? t("gui.status.switching")
      : this.st.starting
        ? t("gui.status.starting")
        : ready
          ? t("gui.status.connected")
          : t("gui.status.disconnected");
    const badgeState = switching ? "switching" : this.st.starting ? "starting" : ready ? "connected" : "disconnected";

    const startBtn = btn({ class: "primary", "data-testid": "btn-start-engine" }, "power", t("gui.ensure"));
    // 未 init 的 active：即使舊引擎還連得上，也要能按 Start 觸發自動設定
    const needsStart = !ready || this.st.meta.active_ready === false;
    startBtn.disabled = busy || !needsStart;
    startBtn.onclick = () => void this.startEngine();

    const distro = switching || this.st.status?.Distro || this.st.meta.distro;
    const hasDistro = !!distro && !switching;
    const folderBtn = iconBtn({ class: "bordered", "data-testid": "btn-open-folder" }, "folder", t("gui.open_folder"));
    folderBtn.disabled = !hasDistro;
    folderBtn.onclick = () => void this.distroAction("open-folder");
    const termBtn = iconBtn({ class: "bordered", "data-testid": "btn-open-terminal" }, "terminal", t("gui.open_terminal"));
    termBtn.disabled = !hasDistro;
    termBtn.onclick = () => void this.distroAction("open-terminal");

    const distroKids: (Node | string)[] = [
      el("h1", { "data-testid": "header-distro", text: distro || t("gui.title"), title: distro ? `${t("gui.title")} — ${distro}` : t("gui.title") }),
    ];
    if (switching) {
      distroKids.push(el("span", { class: "header-loading", "data-testid": "header-loading", text: t("gui.status.loading") }));
    }
    distroKids.push(folderBtn, termBtn);

    h.replaceChildren(
      el("div", { class: "distro-actions", "data-testid": "distro-actions", ...(busy ? { "aria-busy": "true" } : {}) }, ...distroKids),
      el("span", { class: `badge ${badgeCls}`, "data-testid": "status-connection", "data-state": badgeState, text: badgeTxt }),
      el("span", { class: "spacer" }),
      startBtn,
    );
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

  private renderNav(): void {
    const nav = this.root.querySelector<HTMLElement>('[data-testid="side-nav"]')!;
    nav.replaceChildren(
      ...VIEWS.map((v) => {
        const b = el("button", { class: "nav", "data-testid": `nav-${v.id}`, "data-view": v.id });
        if (v.id === this.st.view) b.setAttribute("aria-current", "page");
        b.append(icon(v.icon), el("span", { class: "label", text: t(v.label) }));
        b.onclick = () => this.setView(v.id);
        return b;
      }),
    );
  }

  /** 建立目前視圖的骨架（containers／machines 含底部 panel）。 */
  private renderView(): void {
    const main = this.root.querySelector("main")!;
    main.dataset.view = this.st.view;
    main.setAttribute("data-testid", `view-${this.st.view}`);
    if (this.st.view === "containers") {
      main.replaceChildren(el("div", { class: "list", "data-testid": "container-list" }), el("section", { class: "panel", "data-testid": "panel", hidden: "" }));
    } else if (this.st.view === "machines") {
      main.replaceChildren(
        el("div", { class: "toolbar", "data-testid": "toolbar" }),
        el("div", { class: "list", "data-testid": "machines-list" }),
        el("section", { class: "panel", "data-testid": "panel", hidden: "" }),
      );
    } else {
      main.replaceChildren(el("div", { class: "toolbar", "data-testid": "toolbar" }), el("div", { class: "list", "data-testid": `${this.st.view}-list` }));
    }
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

  private renderFooter(): void {
    const f = this.root.querySelector<HTMLElement>('[data-testid="status-bar"]')!;
    f.replaceChildren(el("span", { "data-testid": "status-version", text: `v${this.st.meta.version}` }), el("span", { class: "spacer" }), this.langSelect());
  }

  /** 右下角語言切換：寫入 config（後端與 Tray 一併切換），再重新載入 meta 重繪。 */
  private langSelect(): HTMLElement {
    const sel = el("select", { "data-testid": "lang-select", "aria-label": t("gui.lang"), class: "lang" }) as HTMLSelectElement;
    for (const [v, label] of [["en", "English"], ["zh-TW", "繁體中文"]]) {
      const o = el("option", { value: v, text: label }) as HTMLOptionElement;
      o.selected = v === this.st.meta.lang;
      sel.append(o);
    }
    sel.onchange = () => void this.switchLang(sel.value);
    return el("label", { class: "lang-wrap" }, sel);
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

  private renderList(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="container-list"]');
    if (!list) return;
    if (this.st.containers.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty") }));
      this.renderInfoDrawer();
      return;
    }
    const { table, tbody } = this.table("containers", COLS);
    const groups = groupContainers(this.st.containers);
    if (!groups) {
      for (const c of this.st.containers) tbody.append(this.containerRow(c, "", name(c)));
    } else {
      for (const g of groups) {
        const expanded = !this.collapsed.has(g.project);
        tbody.append(this.groupRow(g, expanded));
        if (!expanded) continue;
        for (const c of g.items) tbody.append(this.containerRow(c, g.project, g.project ? c.Labels?.[LABEL_SERVICE] || name(c) : name(c)));
      }
    }
    list.replaceChildren(table);
    this.renderInfoDrawer();
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
    const stateText = pending ? t(`gui.ct.${pending}`) : c.State;
    return el(
      "tr",
      rowAttrs,
      el("td", { "data-testid": "ct-name", class: project ? "indent" : "", text: display, title: `${n}\n${c.Id}` }),
      el("td", { text: c.Image, title: c.Image }),
      el("td", {}, el("span", { class: `state ${pending ? "pending" : c.State}`, "data-testid": `state-${n}`, text: stateText })),
      el("td", { class: "actions" }, startBtn, stopBtn, logsBtn, termBtn, infoBtn, rmBtn),
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
    const kids: (Node | string)[] = [title, el("span", { class: "spacer" })];
    if (p.kind === "logs") {
      const live = el("input", { type: "checkbox", "data-testid": "logs-live-toggle" }) as HTMLInputElement;
      live.checked = p.live;
      live.onchange = () => this.setLive(live.checked);
      kids.push(el("label", { class: "check" }, live, t("gui.live")));
    }
    kids.push(close);
    head.replaceChildren(...kids);
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
      aside.replaceChildren();
      return;
    }
    aside.hidden = false;
    aside.setAttribute("data-kind", info.kind);
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
          infoField(t("gui.col.state"), d.state),
          infoField(t("gui.col.status"), d.status || c?.Status || "—", "info-status"),
          infoField(t("gui.info.created"), d.created || "—"),
          infoField(t("gui.info.started"), d.started || "—"),
          infoField(t("gui.info.command"), d.command || "—", "info-command"),
          infoField(t("gui.col.ports"), d.ports || ports(c!) || "—", "info-ports"),
          infoField(t("gui.info.networks"), nets, "info-networks"),
          infoField(t("gui.info.mounts"), mounts, "info-mounts"),
          infoField(t("gui.info.restart"), d.restart_policy || "—", "info-restart"),
        );
        const labels = d.labels ?? {};
        const keys = Object.keys(labels);
        if (keys.length) {
          const det = el("details", { "data-testid": "info-labels" }, el("summary", { text: `${t("gui.info.labels")} (${keys.length})` }));
          const pre = el("pre", { class: "conf" });
          pre.textContent = keys.sort().map((k) => `${k}=${labels[k]}`).join("\n");
          det.append(pre);
          body.append(det);
        }
      } else if (c) {
        body.append(
          infoField(t("gui.col.image"), c.Image),
          infoField(t("gui.col.state"), c.State),
          infoField(t("gui.col.status"), c.Status, "info-status"),
          infoField(t("gui.col.ports"), ports(c) || "—", "info-ports"),
          infoField(t("gui.col.id"), c.Id),
          el("p", { class: "muted", text: "…" }),
        );
      } else {
        body.append(el("p", { class: "muted", text: t("gui.empty") }));
      }
      aside.replaceChildren(head, body);
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
        ),
      );
    }
    aside.replaceChildren(head, body);
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
    bar.replaceChildren(el("span", { class: "count", text: `${t("gui.nav.images")} · ${this.st.images.length}` }), el("span", { class: "spacer" }), prune);

    if (!ready || this.st.images.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty.images") }));
      return;
    }
    const { table, tbody } = this.table("images", IMAGE_COLS);
    const imgs = [...this.st.images].sort((a, b) => imageName(a).localeCompare(imageName(b)));
    for (const im of imgs) {
      const short = shortID(im.Id);
      const nm = imageName(im);
      const ref = nm === "<none>" ? short : nm;
      const rm = iconBtn({ "data-testid": `btn-rm-image-${short}`, class: "danger" }, "trash", t("gui.remove"));
      rm.disabled = this.st.busy.has(`image:${ref}`);
      rm.onclick = () =>
        void this.resourceAction(`image:${ref}`, fmt(t("gui.confirm.remove"), ref), async () => {
          await api.removeImage(ref);
          return fmt(t("gui.removed"), ref);
        });
      tbody.append(
        el(
          "tr",
          { "data-testid": "image-row", "data-ref": ref },
          el("td", { "data-testid": "image-name", text: nm, title: (im.RepoTags ?? []).join("\n") || nm }),
          el("td", { "data-testid": "image-id", class: "mono", text: short, title: im.Id }),
          el("td", { "data-testid": "image-size", text: humanSize(im.Size) }),
          el("td", { text: fmtUnix(im.Created) }),
          el("td", { "data-testid": "image-inuse", text: im.Containers < 0 ? "–" : String(im.Containers) }),
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
    bar.replaceChildren(el("span", { class: "count", text: `${t("gui.nav.volumes")} · ${this.st.volumes.length}` }), el("span", { class: "spacer" }), prune);

    if (!ready || this.st.volumes.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty.volumes") }));
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
          el("td", { "data-testid": "volume-name", text: v.Name, title: v.Name }),
          el("td", { "data-testid": "volume-driver", text: v.Driver }),
          el("td", { "data-testid": "volume-mountpoint", class: "mono", text: v.Mountpoint, title: v.Mountpoint }),
          el("td", { text: fmtISO(v.CreatedAt) }),
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
    bar.replaceChildren(el("span", { class: "count", text: `${t("gui.nav.networks")} · ${this.st.networks.length}` }));
    if (!ready || this.st.networks.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty.networks") }));
      return;
    }
    const { table, tbody } = this.table("networks", NETWORK_COLS);
    for (const n of [...this.st.networks].sort((a, b) => a.Name.localeCompare(b.Name))) {
      const builtin = n.Name === "bridge" || n.Name === "host" || n.Name === "none";
      tbody.append(
        el(
          "tr",
          { "data-testid": "network-row", "data-name": n.Name, "data-builtin": String(builtin) },
          el("td", { "data-testid": "network-name", class: builtin ? "muted" : "", text: n.Name, title: n.Name }),
          el("td", { "data-testid": "network-driver", text: n.Driver }),
          el("td", { "data-testid": "network-scope", text: n.Scope }),
          el("td", { "data-testid": "network-id", class: "mono", text: shortID(n.Id), title: n.Id }),
          el("td", { text: fmtISO(n.Created) }),
        ),
      );
    }
    list.replaceChildren(table);
  }

  // ---- Machines（本機 WSL）----------------------------------------------------

  private renderMachines(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="machines-list"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !bar) return;
    const m = this.st.machines;
    const removingAny = [...this.st.busy].some((k) => k.startsWith("remove:"));
    const add = btn({ "data-testid": "btn-add-machine" }, "plus", t("gui.machines.add"));
    add.disabled = !m?.available || this.st.busy.has("install") || removingAny || !!this.st.switching || this.st.starting;
    add.onclick = () => this.openAddMachine();
    bar.replaceChildren(el("span", { class: "count", text: `${t("gui.nav.machines")} · ${m?.machines.length ?? 0}` }), el("span", { class: "spacer" }), add);

    if (m && !m.available) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.machines.unavailable") }));
      return;
    }
    if (!m || m.machines.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty.machines") }));
      return;
    }
    const { table, tbody } = this.table("machines", MACHINE_COLS);
    for (const d of m.machines) {
      const removing = this.st.busy.has(`remove:${d.name}`);
      const switching = this.st.switching === d.name;
      const starting = this.st.starting && d.active;
      const pending = removing ? "removing" : switching ? "switching" : starting ? "starting" : undefined;
      const locked = !!pending || removingAny || !!this.st.switching || this.st.starting;
      const actions: HTMLElement[] = [];
      if (!d.active) {
        const use = iconBtn({ "data-testid": `btn-machine-use-${d.name}` }, "save", t("gui.machines.use"));
        use.disabled = locked;
        use.onclick = () => void this.useMachine(d.name);
        actions.push(use);
      }
      const cfg = iconBtn({ "data-testid": `btn-machine-config-${d.name}` }, "info", t("gui.machines.info"));
      cfg.disabled = locked;
      cfg.onclick = () => void this.openMachineInfo(d.name);
      actions.push(cfg);
      const folder = iconBtn({ "data-testid": `btn-machine-folder-${d.name}` }, "folder", t("gui.machines.folder"));
      folder.disabled = locked;
      folder.onclick = () => void this.distroAction("open-folder");
      actions.push(folder);
      const term = iconBtn({ "data-testid": `btn-machine-term-${d.name}` }, "terminal", t("gui.machines.terminal"));
      term.disabled = locked;
      term.onclick = () => this.openMachineTerminal(d.name);
      actions.push(term);
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
      actions.push(rm);
      const flags: HTMLElement[] = [];
      if (d.active) flags.push(el("span", { class: "tag accent", "data-testid": "machine-flag-active", text: t("gui.machines.active"), title: d.api_endpoint || this.st.meta.api_endpoint }));
      if (d.managed && !d.ready) flags.push(el("span", { class: "tag", "data-testid": "machine-flag-not-ready", text: t("status.not_ready") }));
      if (d.default) flags.push(el("span", { class: "tag", "data-testid": "machine-flag-default", text: t("gui.machines.default") }));
      const stateText =
        pending === "removing"
          ? t("gui.machines.removing")
          : pending === "switching"
            ? t("gui.status.switching")
            : pending === "starting"
              ? t("gui.status.starting")
              : d.state;
      const rowAttrs: Record<string, string> = {
        "data-testid": "machine-row",
        "data-name": d.name,
        "data-state": pending ?? d.state.toLowerCase(),
        "data-active": d.active ? "1" : "0",
      };
      if (pending) rowAttrs["data-pending"] = pending;
      tbody.append(
        el(
          "tr",
          rowAttrs,
          el("td", { "data-testid": "machine-name", text: d.name, title: d.name }),
          el("td", {}, el("span", { class: `state ${pending ? "pending" : d.state.toLowerCase()}`, "data-testid": "machine-state", text: stateText })),
          el("td", { "data-testid": "machine-version", text: String(d.version) }),
          el("td", { class: "flags" }, ...flags),
          el("td", { class: "actions" }, ...actions),
        ),
      );
    }
    list.replaceChildren(table);
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
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
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
      this.st.info = { ...this.st.info, detail };
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

  private buildAddMachineDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "add-machine-dialog" }) as HTMLDialogElement;
    const sel = el("select", { "data-testid": "add-machine-select" }) as HTMLSelectElement;
    const name = el("input", {
      type: "text",
      "data-testid": "add-machine-name",
      autocomplete: "off",
      spellcheck: "false",
    }) as HTMLInputElement;
    const err = el("div", { class: "field-error", "data-testid": "add-machine-error", hidden: "" });
    const install = btn({ class: "primary", type: "submit", "data-testid": "add-machine-install" }, "plus", t("gui.machines.install"));
    const cancel = btn({ type: "button", "data-testid": "add-machine-cancel" }, "close", t("gui.cancel"));
    const syncInstallEnabled = () => {
      install.disabled = !sel.value.trim() || !name.value.trim() || this.st.busy.has("install");
    };
    sel.onchange = () => {
      if (sel.value) name.value = sel.value;
      syncInstallEnabled();
    };
    name.oninput = () => syncInstallEnabled();
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
      el("label", {}, t("gui.machines.select"), sel),
      el("label", {}, t("gui.machines.name"), name),
      err,
      el("div", { class: "row" }, cancel, install),
    );
    form.noValidate = true;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const dist = sel.value.trim();
      const nm = name.value.trim();
      if (!dist || !nm || this.st.busy.has("install")) return;
      install.disabled = true;
      cancel.disabled = true;
      sel.disabled = true;
      name.disabled = true;
      install.querySelector(".label")!.textContent = t("gui.machines.installing");
      err.hidden = true;
      this.st.busy.add("install");
      this.renderViewBody();
      try {
        await api.installMachine(dist, nm);
        d.close();
        this.notify(fmt(t("gui.machines.installed"), nm, nm));
        await this.refresh();
      } catch (e) {
        err.textContent = e instanceof ApiError ? e.message : String(e);
        err.hidden = false;
      } finally {
        this.st.busy.delete("install");
        cancel.disabled = false;
        sel.disabled = false;
        name.disabled = false;
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
    const sel = d.querySelector<HTMLSelectElement>('[data-testid="add-machine-select"]')!;
    const name = d.querySelector<HTMLInputElement>('[data-testid="add-machine-name"]')!;
    const err = d.querySelector<HTMLElement>('[data-testid="add-machine-error"]')!;
    const install = d.querySelector<HTMLButtonElement>('[data-testid="add-machine-install"]')!;
    const cancel = d.querySelector<HTMLButtonElement>('[data-testid="add-machine-cancel"]')!;
    err.hidden = true;
    name.value = "";
    name.disabled = true;
    cancel.disabled = false;
    sel.replaceChildren(el("option", { value: "", text: t("gui.machines.loading") }));
    sel.disabled = true;
    install.disabled = true;
    d.showModal();
    try {
      const online = await api.machinesOnline();
      const opts = [
        el("option", { value: "", text: t("gui.machines.select_placeholder") }),
        ...online.map((o) =>
          el("option", {
            value: o.name,
            text: o.friendly && o.friendly !== o.name ? `${o.friendly} (${o.name})` : o.name,
          }),
        ),
      ];
      sel.replaceChildren(...opts);
      sel.disabled = false;
      name.disabled = false;
      install.disabled = true;
    } catch (e) {
      sel.replaceChildren(el("option", { value: "", text: t("gui.machines.select_placeholder") }));
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
      "data-testid": "confirm-typed",
      autocomplete: "off",
      spellcheck: "false",
      hidden: "",
    }) as HTMLInputElement;
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
    };
    d.append(el("p", { "data-testid": "confirm-text" }), typed, el("div", { class: "row" }, cancel, ok));
    return d;
  }

  private confirm(text: string, requireType?: string): Promise<boolean> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="confirm-dialog"]')!;
    d.querySelector('[data-testid="confirm-text"]')!.textContent = text;
    const typed = d.querySelector<HTMLInputElement>('[data-testid="confirm-typed"]')!;
    const ok = d.querySelector<HTMLButtonElement>('[data-testid="confirm-ok"]')!;
    typed.value = "";
    if (requireType) {
      typed.hidden = false;
      typed.dataset.require = requireType;
      typed.placeholder = requireType;
      ok.disabled = true;
    } else {
      typed.hidden = true;
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

  private renderSettings(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="settings-list"]');
    const bar = this.root.querySelector<HTMLElement>('[data-testid="toolbar"]');
    if (!list || !bar) return;
    bar.replaceChildren(el("span", { class: "count", text: t("gui.settings") }));
    const c = this.st.meta.config;
    const interval = el("input", { type: "number", min: "1", max: "3600", "data-testid": "settings-refresh", value: String(c.refresh_interval_sec) }) as HTMLInputElement;
    const shell = el("input", { type: "text", "data-testid": "settings-exec-shell", placeholder: "bash", value: c.exec_shell || "" }) as HTMLInputElement;

    const auto = el("button", {
      type: "button",
      class: "toggle",
      role: "switch",
      "data-testid": "settings-autostart",
      "aria-checked": c.tray_autostart ? "true" : "false",
    }) as HTMLButtonElement;
    auto.disabled = !this.st.meta.autostart_supported;
    auto.onclick = () => {
      const on = auto.getAttribute("aria-checked") !== "true";
      auto.setAttribute("aria-checked", on ? "true" : "false");
    };

    const themeVal = c.theme || "system";
    const themeRadios = el("div", { class: "radio-group", "data-testid": "settings-theme", role: "radiogroup", "aria-label": t("gui.settings.theme") });
    for (const [v, key] of [
      ["light", "gui.settings.theme.light"],
      ["dark", "gui.settings.theme.dark"],
      ["system", "gui.settings.theme.system"],
    ] as const) {
      const id = `settings-theme-${v}`;
      const radio = el("input", { type: "radio", name: "settings-theme", value: v, id, "data-testid": id }) as HTMLInputElement;
      radio.checked = themeVal === v;
      themeRadios.append(el("label", { class: "radio", for: id }, radio, t(key)));
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
    list.replaceChildren(
      el(
        "form",
        { class: "settings-form", "data-testid": "settings-form" },
        el("label", {}, t("gui.settings.refresh"), interval),
        el("label", {}, t("gui.settings.exec_shell"), shell),
        el("div", { class: "settings-row" }, el("span", { "data-testid": "settings-autostart-label", text: t("gui.settings.autostart") }), auto),
        el("fieldset", { class: "settings-fieldset" }, el("legend", { text: t("gui.settings.theme") }), themeRadios),
        el("div", { class: "row settings-actions" }, save),
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
  // 總和 < 預設視窗（1000px − 側欄 96px），避免一開就出現水平捲軸
  { key: "name", label: "gui.col.name", def: 220, min: 80, resizable: true },
  { key: "image", label: "gui.col.image", def: 260, min: 80, resizable: true },
  { key: "state", label: "gui.col.state", def: 100, min: 70, resizable: true },
	{ key: "actions", label: "gui.col.actions", def: 210, min: 180, resizable: false },
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
  { key: "created", label: "gui.col.created", def: 150, min: 90, resizable: false },
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
