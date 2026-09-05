import { api, ApiError, type Container, type Meta, type Settings, type Status } from "./api";
import { setMessages, t } from "./i18n";
import { openTerminal, type TerminalSession } from "./terminal";
import { icon, type IconName } from "./icons";

type Panel = { kind: "logs"; id: string; name: string; live: boolean; ws?: WebSocket } | { kind: "term"; id: string; name: string; session?: TerminalSession };

interface State {
  meta: Meta;
  status?: Status;
  containers: Container[];
  showAll: boolean;
  lastError?: string;
  starting: boolean;
  paused: boolean;
  refreshCount: number;
  panel?: Panel;
  /** 進行中的容器動作（id → 動作），用於即時回饋；docker stop 可能等 10s 寬限期 */
  pending: Record<string, "starting" | "stopping">;
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

export class App {
  private st: State;
  private timer?: number;
  private root: HTMLElement;

  constructor(root: HTMLElement, meta: Meta) {
    this.root = root;
    setMessages(meta.messages);
    document.documentElement.lang = meta.lang;
    document.title = t("gui.title");
    this.st = { meta, containers: [], showAll: false, starting: false, paused: false, refreshCount: 0, pending: {} };
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
        this.renderFooter();
        if (!paused) void this.refresh();
      }
    };
    window.addEventListener("blur", update);
    window.addEventListener("focus", update);
    document.addEventListener("visibilitychange", update);
  }

  async refresh(): Promise<void> {
    try {
      const [status, containers] = await Promise.all([api.status(), this.st.status?.ready === false && !this.st.starting ? Promise.resolve(this.st.containers) : api.containers(this.st.showAll)]);
      this.st.status = status;
      if (status.ready) {
        this.st.containers = containers;
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

  // ---- 動作 -----------------------------------------------------------------

  private async startEngine(): Promise<void> {
    this.st.starting = true;
    this.st.lastError = undefined;
    this.renderHeader();
    this.renderBanner();
    try {
      this.st.status = await api.startEngine();
      this.st.containers = await api.containers(this.st.showAll);
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
    } finally {
      this.st.starting = false;
      this.renderAll();
    }
  }

  private async act(fn: () => Promise<void>): Promise<void> {
    try {
      await fn();
      this.st.lastError = undefined;
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
    }
    await this.refresh();
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

  // ---- 渲染 -----------------------------------------------------------------

  private build(): void {
    this.root.innerHTML = "";
    this.root.append(
      el("header", { "data-testid": "header" }),
      el("div", { class: "banner", "data-testid": "error-banner", role: "alert", hidden: "" }),
      el("main", {}, el("div", { class: "list", "data-testid": "container-list" }), el("section", { class: "panel", "data-testid": "panel", hidden: "" })),
      el("footer", { "data-testid": "status-bar" }),
      this.buildSettingsDialog(),
    );
    this.renderAll();
  }

  private renderAll(): void {
    this.renderHeader();
    this.renderBanner();
    this.renderList();
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

    const refreshBtn = btn({ "data-testid": "btn-refresh" }, "refresh", t("gui.refresh"));
    refreshBtn.onclick = () => void this.refresh();

    const showAll = el("input", { type: "checkbox", "data-testid": "chk-show-all" }) as HTMLInputElement;
    showAll.checked = this.st.showAll;
    showAll.onchange = () => {
      this.st.showAll = showAll.checked;
      void this.refresh();
    };

    const settingsBtn = btn({ "data-testid": "btn-settings" }, "settings", t("gui.settings"));
    settingsBtn.onclick = () => this.openSettings();

    // 標題列左側：連線中的 WSL distro（尚無 config 時退回 app 名稱）
    const distro = this.st.status?.Distro || this.st.meta.distro;
    h.replaceChildren(
      el("h1", { "data-testid": "header-distro", text: distro || t("gui.title"), title: distro ? `${t("gui.title")} — ${distro}` : t("gui.title") }),
      el("span", { class: `badge ${badgeCls}`, "data-testid": "status-connection", "data-state": this.st.starting ? "starting" : ready ? "connected" : "disconnected", text: badgeTxt }),
      el("span", { class: "spacer" }),
      el("label", { class: "check" }, showAll, t("gui.show_all")),
      refreshBtn,
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

  // ---- 列表：欄寬（固定版面、可拖曳、localStorage）與 compose 分組 ---------------

  private colWidths: Record<string, number> = loadJSON(LS_COLS, {});
  private collapsed: Set<string> = new Set(loadJSON<string[]>(LS_COLLAPSED, []));

  private widthOf(col: Col): number {
    return Math.max(col.min, this.colWidths[col.key] ?? col.def);
  }

  private applyColWidths(table: HTMLTableElement): void {
    // 可調欄位給明確像素寬；最後的操作欄不設寬度，吸收剩餘空間（table-layout: fixed 的行為），
    // 所以拖曳某欄 +N px 時，只有那一欄與操作欄改變。
    let total = 0;
    table.querySelectorAll<HTMLTableColElement>("col").forEach((c) => {
      const col = COLS.find((x) => x.key === c.dataset.col)!;
      const w = this.widthOf(col);
      if (col.resizable) c.style.width = `${w}px`;
      total += w;
    });
    table.style.width = "100%";
    table.style.minWidth = `${total}px`;
  }

  private startResize(ev: MouseEvent, col: Col, table: HTMLTableElement): void {
    ev.preventDefault();
    const startX = ev.clientX;
    const startW = this.widthOf(col);
    const move = (e: MouseEvent) => {
      this.colWidths[col.key] = Math.max(col.min, startW + (e.clientX - startX));
      this.applyColWidths(table);
    };
    const up = () => {
      document.removeEventListener("mousemove", move);
      document.removeEventListener("mouseup", up);
      document.body.classList.remove("resizing");
      saveJSON(LS_COLS, this.colWidths);
    };
    document.body.classList.add("resizing");
    document.addEventListener("mousemove", move);
    document.addEventListener("mouseup", up);
  }

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
    const list = this.root.querySelector<HTMLElement>('[data-testid="container-list"]')!;
    if (this.st.containers.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty") }));
      return;
    }
    const table = el("table", { class: "fixed" });
    const colgroup = el("colgroup", {}, ...COLS.map((c) => el("col", { "data-col": c.key })));
    const headRow = el("tr");
    for (const col of COLS) {
      const th = el("th", { "data-col": col.key, text: t(col.label) });
      if (col.resizable) {
        const handle = el("div", { class: "col-resizer", "data-testid": "col-resizer", role: "separator", "aria-orientation": "vertical" });
        handle.onmousedown = (e) => this.startResize(e, col, table);
        th.append(handle);
      }
      headRow.append(th);
    }
    const tbody = el("tbody");

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
    table.append(colgroup, el("thead", {}, headRow), tbody);
    this.applyColWidths(table);
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
    const startBtn = btn({ class: "small", "data-testid": `btn-start-${n}` }, "play", t("gui.start"));
    startBtn.disabled = running || !!pending;
    startBtn.onclick = () => void this.actOn(c.Id, "starting", () => api.startContainer(c.Id));
    const stopBtn = btn({ class: "small", "data-testid": `btn-stop-${n}` }, "stop", t("gui.stop"));
    stopBtn.disabled = !running || !!pending;
    stopBtn.onclick = () => void this.actOn(c.Id, "stopping", () => api.stopContainer(c.Id));
    const logsBtn = btn({ class: "small", "data-testid": `btn-logs-${n}` }, "logs", t("gui.logs"));
    logsBtn.onclick = () => this.openLogs(c);
    const termBtn = btn({ class: "small", "data-testid": `btn-term-${n}` }, "terminal", t("gui.terminal"));
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
    const sec = this.root.querySelector<HTMLElement>('[data-testid="panel"]')!;
    const main = this.root.querySelector("main")!;
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
    sec.setAttribute("data-testid", "panel");
    sec.replaceChildren(head, body);
    this.renderPanelHead();
  }

  private renderPanelHead(): void {
    const p = this.st.panel;
    const head = this.root.querySelector<HTMLElement>('[data-testid="panel-head"]');
    if (!p || !head) return;
    const title = el("strong", { "data-testid": "panel-title", text: `${p.kind === "logs" ? t("gui.logs") : t("gui.terminal")} — ${p.name}` });
    const close = el("button", { class: "small", "data-testid": "btn-panel-close", text: t("gui.close") });
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

  private renderFooter(): void {
    const f = this.root.querySelector<HTMLElement>('[data-testid="status-bar"]')!;
    const s = this.st.status;
    const conn = s?.ready ? t("gui.status.connected") : t("gui.status.disconnected");
    f.replaceChildren(
      el("span", { "data-testid": "status-distro", text: `${t("status.distro").replace("%s", "")}${this.st.meta.distro}` }),
      el("span", { "data-testid": "status-endpoint", text: this.st.meta.api_endpoint }),
      el("span", { class: s?.ready ? "ok" : "bad", "data-testid": "status-api", text: conn }),
      el("span", { "data-testid": "status-refresh", "data-paused": String(this.st.paused), "data-count": String(this.st.refreshCount), text: this.st.paused ? t("gui.status.paused") : `↻ ${this.st.meta.config.refresh_interval_sec}s` }),
      el("span", { class: "spacer" }),
      this.langSelect(),
      el("span", { "data-testid": "status-version", text: `v${this.st.meta.version}` }),
    );
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
      this.build(); // 全部重繪（含設定對話框文字）
      this.renderPanel();
    } catch (e) {
      this.st.lastError = e instanceof ApiError ? e.message : String(e);
      this.renderBanner();
    }
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
        this.renderFooter();
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
  // 總和 925px < 預設視窗 1000px，避免一開就出現水平捲軸
  { key: "name", label: "gui.col.name", def: 150, min: 80, resizable: true },
  { key: "image", label: "gui.col.image", def: 170, min: 80, resizable: true },
  { key: "state", label: "gui.col.state", def: 85, min: 70, resizable: true },
  { key: "status", label: "gui.col.status", def: 140, min: 80, resizable: true },
  { key: "ports", label: "gui.col.ports", def: 130, min: 80, resizable: true },
  { key: "actions", label: "gui.col.actions", def: 250, min: 250, resizable: false },
];

const LS_COLS = "wsld.colWidths";
const LS_COLLAPSED = "wsld.collapsedGroups";
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
