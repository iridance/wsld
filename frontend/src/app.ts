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

/** 側欄視圖。 */
export type View = "containers" | "images" | "volumes" | "networks" | "machines";
const VIEWS: { id: View; icon: IconName; label: string }[] = [
  { id: "containers", icon: "containers", label: "gui.nav.containers" },
  { id: "images", icon: "images", label: "gui.nav.images" },
  { id: "volumes", icon: "volumes", label: "gui.nav.volumes" },
  { id: "networks", icon: "networks", label: "gui.nav.networks" },
  { id: "machines", icon: "machines", label: "gui.nav.machines" },
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
  paused: boolean;
  refreshCount: number;
  panel?: Panel;
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
    this.bindPause();
    void this.refresh();
    this.schedule();
    // 測試掛鉤
    (window as unknown as { __wsld: unknown }).__wsld = {
      state: () => this.st,
      termText: () => (this.st.panel?.kind === "term" ? this.st.panel.session?.text() ?? "" : ""),
    };
  }

  // ---- 刷新 -----------------------------------------------------------------

  private schedule(): void {
    if (this.timer) window.clearInterval(this.timer);
    const sec = Math.max(1, this.st.meta.config.refresh_interval_sec || 3);
    this.timer = window.setInterval(() => {
      if (!this.st.paused) void this.refresh();
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
    try {
      const status = await api.status();
      this.st.status = status;
      if (this.st.view === "machines") {
        // WSL 清單不依賴 Docker 引擎
        this.st.machines = await api.machines();
        this.st.lastError = undefined;
      } else if (status.ready) {
        await this.loadView();
        this.st.lastError = undefined;
      } else if (!this.st.starting) {
        // 引擎未就緒：保留列表但標示斷線
        this.st.lastError = status.APIError || t("gui.error.api");
      }
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      if (this.st.status) this.st.status = { ...this.st.status, ready: false, APIReachable: false };
    }
    this.st.refreshCount++;
    this.renderAll();
  }

  private async loadView(): Promise<void> {
    switch (this.st.view) {
      case "containers":
        this.st.containers = await api.containers();
        break;
      case "images":
        this.st.images = await api.images();
        break;
      case "volumes":
        this.st.volumes = await api.volumes();
        break;
      case "networks":
        this.st.networks = await api.networks();
        break;
      case "machines":
        this.st.machines = await api.machines();
        break;
    }
  }

  private setView(v: View): void {
    if (v === this.st.view) return;
    this.closePanel();
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
    try {
      this.st.status = await api.startEngine();
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

  /** 資源動作（刪除／prune）：確認 → 標 busy → 執行 → 通知／錯誤 → 刷新。 */
  private async resourceAction(key: string, confirmText: string, fn: () => Promise<string | undefined>): Promise<void> {
    if (!(await this.confirm(confirmText))) return;
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
    this.st.panel.session = openTerminal(host, c.Id, () => {
      /* 顯示已由 terminal.ts 寫入 */
    });
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
      el("div", { class: "banner", "data-testid": "error-banner", role: "alert", hidden: "" }),
      el("div", { class: "notice", "data-testid": "notice", role: "status", hidden: "" }),
      el("div", { class: "body" }, el("nav", { class: "side", "data-testid": "side-nav", "aria-label": "Views" }), el("main")),
      el("footer", { "data-testid": "status-bar" }),
      this.buildSettingsDialog(),
      this.buildConfirmDialog(),
      this.buildMachineConfigDialog(),
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
    this.renderFooter();
  }

  private renderHeader(): void {
    const h = this.root.querySelector("header")!;
    const ready = this.st.status?.ready === true;
    const badgeCls = this.st.starting ? "warn" : ready ? "ok" : "bad";
    const badgeTxt = this.st.starting ? t("gui.status.starting") : ready ? t("gui.status.connected") : t("gui.status.disconnected");

    const startBtn = btn({ class: "primary", "data-testid": "btn-start-engine" }, "power", t("gui.ensure"));
    startBtn.disabled = this.st.starting || ready;
    startBtn.onclick = () => void this.startEngine();

    const settingsBtn = btn({ "data-testid": "btn-settings" }, "settings", t("gui.settings"));
    settingsBtn.onclick = () => this.openSettings();

    // 標題列左側：連線中的 WSL distro（尚無 config 時退回 app 名稱），其後是連線狀態、端點、刷新間隔
    const distro = this.st.status?.Distro || this.st.meta.distro;
    h.replaceChildren(
      el("h1", { "data-testid": "header-distro", text: distro || t("gui.title"), title: distro ? `${t("gui.title")} — ${distro}` : t("gui.title") }),
      el("span", { class: `badge ${badgeCls}`, "data-testid": "status-connection", "data-state": this.st.starting ? "starting" : ready ? "connected" : "disconnected", text: badgeTxt }),
      el("span", { class: "meta", "data-testid": "status-endpoint", text: this.st.meta.api_endpoint }),
      el("span", {
        class: "meta",
        "data-testid": "status-refresh",
        "data-paused": String(this.st.paused),
        "data-count": String(this.st.refreshCount),
        text: this.st.paused ? t("gui.status.paused") : `↻ ${this.st.meta.config.refresh_interval_sec}s`,
      }),
      el("span", { class: "spacer" }),
      startBtn,
      settingsBtn,
    );
  }

  private renderBanner(): void {
    const b = this.root.querySelector<HTMLElement>('[data-testid="error-banner"]')!;
    if (this.st.lastError && !this.st.starting) {
      b.textContent = `${t("gui.error.api")} — ${this.st.lastError}`;
      b.hidden = false;
    } else {
      b.hidden = true;
      b.textContent = "";
    }
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

  /** 建立目前視圖的骨架（容器視圖含 panel），內容由 renderViewBody 填。 */
  private renderView(): void {
    const main = this.root.querySelector("main")!;
    main.dataset.view = this.st.view;
    main.setAttribute("data-testid", `view-${this.st.view}`);
    if (this.st.view === "containers") {
      main.replaceChildren(el("div", { class: "list", "data-testid": "container-list" }), el("section", { class: "panel", "data-testid": "panel", hidden: "" }));
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
  }

  private groupRow(g: Group, expanded: boolean): HTMLTableRowElement {
    const running = g.items.filter((c) => c.State === "running").length;
    const anyPending = g.items.some((c) => this.st.pending[c.Id]);
    const toggle = el("button", { class: "small ghost", "data-testid": "group-toggle", "aria-label": expanded ? "collapse" : "expand" });
    toggle.append(icon(expanded ? "chevron-down" : "chevron-right"));
    toggle.onclick = () => this.toggleGroup(g.project);
    const actions: HTMLElement[] = [];
    if (g.project) {
      // 只有 compose 專案才有整組 Start／Stop；未分組只是雜項集合
      const startBtn = btn({ class: "small", "data-testid": "group-start" }, "play", t("gui.start"));
      startBtn.disabled = anyPending || running === g.items.length;
      startBtn.onclick = () =>
        void this.actMany(g.items.filter((c) => c.State !== "running").map((c) => c.Id), "starting", (id) => api.startContainer(id));
      const stopBtn = btn({ class: "small", "data-testid": "group-stop" }, "stop", t("gui.stop"));
      stopBtn.disabled = anyPending || running === 0;
      stopBtn.onclick = () =>
        void this.actMany(g.items.filter((c) => c.State === "running").map((c) => c.Id), "stopping", (id) => api.stopContainer(id));
      actions.push(startBtn, stopBtn);
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

    const rowAttrs: Record<string, string> = { "data-testid": "container-row", "data-name": n, "data-state": c.State, "data-project": project };
    if (pending) rowAttrs["data-pending"] = pending;
    const stateText = pending ? t(`gui.ct.${pending}`) : c.State;
    return el(
      "tr",
      rowAttrs,
      el("td", { "data-testid": "ct-name", class: project ? "indent" : "", text: display, title: `${n}\n${c.Id}` }),
      el("td", { text: c.Image, title: c.Image }),
      el("td", {}, el("span", { class: `state ${pending ? "pending" : c.State}`, "data-testid": `state-${n}`, text: stateText })),
      el("td", { text: c.Status, title: c.Status }),
      el("td", { "data-testid": `ports-${n}`, text: ports(c), title: ports(c) }),
      el("td", { class: "actions" }, startBtn, stopBtn, logsBtn, termBtn),
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
    const add = btn({ "data-testid": "btn-add-machine" }, "plus", t("gui.machines.add"));
    add.disabled = !m?.available || this.st.busy.has("install");
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
      const cfg = iconBtn({ "data-testid": `btn-machine-config-${d.name}` }, "info", t("gui.machines.config"));
      cfg.onclick = () => void this.openMachineConfig(d.name);
      const flags: HTMLElement[] = [];
      if (d.active) flags.push(el("span", { class: "tag accent", "data-testid": "machine-flag-active", text: t("gui.machines.active"), title: this.st.meta.api_endpoint }));
      if (d.default) flags.push(el("span", { class: "tag", "data-testid": "machine-flag-default", text: t("gui.machines.default") }));
      tbody.append(
        el(
          "tr",
          { "data-testid": "machine-row", "data-name": d.name, "data-state": d.state.toLowerCase() },
          el("td", { "data-testid": "machine-name", text: d.name, title: d.name }),
          el("td", {}, el("span", { class: `state ${d.state.toLowerCase()}`, "data-testid": "machine-state", text: d.state })),
          el("td", { "data-testid": "machine-version", text: String(d.version) }),
          el("td", { class: "flags" }, ...flags),
          el("td", { class: "actions" }, cfg),
        ),
      );
    }
    list.replaceChildren(table);
  }

  private buildMachineConfigDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "machine-config-dialog", class: "wide" }) as HTMLDialogElement;
    const close = iconBtn({ "data-testid": "machine-config-close", type: "button" }, "close", t("gui.close"));
    close.onclick = () => d.close();
    d.append(
      el("div", { class: "dialog-head" }, el("h2", {}, el("span", { "data-testid": "machine-config-name" }), " — /etc/wsl.conf"), el("span", { class: "spacer" }), close),
      el("div", { class: "tag", "data-testid": "machine-config-systemd", "data-on": "false" }),
      el("pre", { class: "conf", "data-testid": "machine-config-conf" }),
    );
    return d;
  }

  private async openMachineConfig(name: string): Promise<void> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="machine-config-dialog"]')!;
    const nm = d.querySelector<HTMLElement>('[data-testid="machine-config-name"]')!;
    const sd = d.querySelector<HTMLElement>('[data-testid="machine-config-systemd"]')!;
    const pre = d.querySelector<HTMLElement>('[data-testid="machine-config-conf"]')!;
    nm.textContent = name;
    sd.textContent = "…";
    sd.dataset.on = "false";
    pre.textContent = "";
    if (!d.open) d.showModal();
    try {
      const c = await api.machineConfig(name);
      sd.dataset.on = String(c.systemd);
      sd.className = `tag ${c.systemd ? "ok" : ""}`;
      sd.textContent = c.systemd ? t("gui.machines.systemd_on") : t("gui.machines.systemd_off");
      pre.textContent = c.wsl_conf || t("gui.machines.no_conf");
    } catch (e) {
      pre.textContent = e instanceof ApiError ? e.message : String(e);
    }
  }

  private buildAddMachineDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "add-machine-dialog" }) as HTMLDialogElement;
    const sel = el("select", { "data-testid": "add-machine-select" }) as HTMLSelectElement;
    const err = el("div", { class: "field-error", "data-testid": "add-machine-error", hidden: "" });
    const install = btn({ class: "primary", type: "submit", "data-testid": "add-machine-install" }, "plus", t("gui.machines.install"));
    const cancel = btn({ type: "button", "data-testid": "add-machine-cancel" }, "close", t("gui.cancel"));
    cancel.onclick = () => d.close();
    const form = el("form", { method: "dialog" }, el("label", {}, t("gui.machines.add"), sel), err, el("div", { class: "row" }, cancel, install));
    form.noValidate = true;
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const nm = sel.value;
      if (!nm) return;
      install.disabled = true;
      install.querySelector(".label")!.textContent = t("gui.machines.installing");
      err.hidden = true;
      this.st.busy.add("install");
      try {
        await api.installMachine(nm);
        d.close();
        this.notify(fmt(t("gui.machines.installed"), nm, nm));
        await this.refresh();
      } catch (e) {
        err.textContent = e instanceof ApiError ? e.message : String(e);
        err.hidden = false;
      } finally {
        this.st.busy.delete("install");
        install.disabled = false;
        install.querySelector(".label")!.textContent = t("gui.machines.install");
      }
    };
    d.append(el("h2", { text: t("gui.machines.add") }), form);
    return d;
  }

  private async openAddMachine(): Promise<void> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="add-machine-dialog"]')!;
    const sel = d.querySelector<HTMLSelectElement>('[data-testid="add-machine-select"]')!;
    const err = d.querySelector<HTMLElement>('[data-testid="add-machine-error"]')!;
    const install = d.querySelector<HTMLButtonElement>('[data-testid="add-machine-install"]')!;
    err.hidden = true;
    sel.replaceChildren(el("option", { value: "", text: t("gui.machines.loading") }));
    sel.disabled = true;
    install.disabled = true;
    d.showModal();
    try {
      const online = await api.machinesOnline();
      sel.replaceChildren(...online.map((o) => el("option", { value: o.name, text: o.friendly && o.friendly !== o.name ? `${o.friendly} (${o.name})` : o.name })));
      sel.disabled = false;
      install.disabled = online.length === 0;
    } catch (e) {
      sel.replaceChildren();
      err.textContent = e instanceof ApiError ? e.message : String(e);
      err.hidden = false;
    }
  }

  // ---- 確認對話框 -------------------------------------------------------------

  private confirmResolve?: (ok: boolean) => void;

  private buildConfirmDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "confirm-dialog" }) as HTMLDialogElement;
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
    d.append(el("p", { "data-testid": "confirm-text" }), el("div", { class: "row" }, cancel, ok));
    return d;
  }

  private confirm(text: string): Promise<boolean> {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="confirm-dialog"]')!;
    d.querySelector('[data-testid="confirm-text"]')!.textContent = text;
    return new Promise((resolve) => {
      this.confirmResolve = resolve;
      d.showModal();
    });
  }

  // ---- 設定 -----------------------------------------------------------------

  private buildSettingsDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "settings-dialog" }) as HTMLDialogElement;
    const interval = el("input", { type: "number", min: "1", max: "3600", "data-testid": "settings-refresh" }) as HTMLInputElement;
    const shell = el("input", { type: "text", "data-testid": "settings-exec-shell", placeholder: "bash" }) as HTMLInputElement;
    const auto = el("input", { type: "checkbox", "data-testid": "settings-autostart" }) as HTMLInputElement;
    const save = btn({ class: "primary", type: "submit", "data-testid": "settings-save" }, "save", t("gui.save"));
    const close = btn({ type: "button", "data-testid": "settings-close" }, "close", t("gui.close"));
    close.onclick = () => d.close();
    const form = el(
      "form",
      { method: "dialog" },
      el("label", {}, t("gui.settings.refresh"), interval),
      el("label", {}, t("gui.settings.exec_shell"), shell),
      el("label", { class: "check" }, auto, t("gui.settings.autostart")),
      el("div", { class: "row" }, close, save),
    );
    form.noValidate = true; // 驗證交給後端（單一來源），錯誤顯示於 banner
    form.onsubmit = async (ev) => {
      ev.preventDefault();
      const ok = await this.saveSettings({
        refresh_interval_sec: Number(interval.value),
        exec_shell: shell.value.trim(),
        tray_autostart: auto.checked,
        lang: this.st.meta.config.lang,
      });
      if (ok) {
        d.close();
        this.renderHeader();
      }
    };
    d.append(el("h2", { text: t("gui.settings") }), form);
    d.addEventListener("wsld-open", () => {
      const c = this.st.meta.config;
      interval.value = String(c.refresh_interval_sec);
      shell.value = c.exec_shell;
      auto.checked = c.tray_autostart;
      auto.disabled = !this.st.meta.autostart_supported;
    });
    return d;
  }

  private openSettings(): void {
    const d = this.root.querySelector<HTMLDialogElement>('[data-testid="settings-dialog"]')!;
    d.dispatchEvent(new Event("wsld-open"));
    d.showModal();
  }
}

function name(c: Container): string {
  return (c.Names?.[0] ?? c.Id.slice(0, 12)).replace(/^\//, "");
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
  { key: "name", label: "gui.col.name", def: 150, min: 80, resizable: true },
  { key: "image", label: "gui.col.image", def: 170, min: 80, resizable: true },
  { key: "state", label: "gui.col.state", def: 85, min: 70, resizable: true },
  { key: "status", label: "gui.col.status", def: 140, min: 80, resizable: true },
  { key: "ports", label: "gui.col.ports", def: 130, min: 80, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 140, min: 130, resizable: false },
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
