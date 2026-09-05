import { api, ApiError, type Container, type Meta, type Settings, type Status } from "./api";
import { setMessages, t } from "./i18n";
import { openTerminal, type TerminalSession } from "./terminal";

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

    const startBtn = el("button", { class: "primary", "data-testid": "btn-start-engine", text: t("gui.ensure") });
    startBtn.disabled = this.st.starting || ready;
    startBtn.onclick = () => void this.startEngine();

    const refreshBtn = el("button", { "data-testid": "btn-refresh", text: t("gui.refresh") });
    refreshBtn.onclick = () => void this.refresh();

    const showAll = el("input", { type: "checkbox", "data-testid": "chk-show-all" }) as HTMLInputElement;
    showAll.checked = this.st.showAll;
    showAll.onchange = () => {
      this.st.showAll = showAll.checked;
      void this.refresh();
    };

    const settingsBtn = el("button", { "data-testid": "btn-settings", text: t("gui.settings") });
    settingsBtn.onclick = () => this.openSettings();

    h.replaceChildren(
      el("h1", { text: t("gui.title") }),
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

  private renderList(): void {
    const list = this.root.querySelector<HTMLElement>('[data-testid="container-list"]')!;
    if (this.st.containers.length === 0) {
      list.replaceChildren(el("div", { class: "empty", "data-testid": "empty", text: t("gui.empty") }));
      return;
    }
    const thead = el("thead", {}, el("tr", {}, ...["gui.col.name", "gui.col.image", "gui.col.state", "gui.col.status", "gui.col.ports", "gui.col.actions"].map((k) => el("th", { text: t(k) }))));
    const tbody = el("tbody");
    for (const c of this.st.containers) {
      const running = c.State === "running";
      const n = name(c);
      const pending = this.st.pending[c.Id];
      const startBtn = el("button", { class: "small", "data-testid": `btn-start-${n}`, text: t("gui.start") });
      startBtn.disabled = running || !!pending;
      startBtn.onclick = () => void this.actOn(c.Id, "starting", () => api.startContainer(c.Id));
      const stopBtn = el("button", { class: "small", "data-testid": `btn-stop-${n}`, text: t("gui.stop") });
      stopBtn.disabled = !running || !!pending;
      stopBtn.onclick = () => void this.actOn(c.Id, "stopping", () => api.stopContainer(c.Id));
      const logsBtn = el("button", { class: "small", "data-testid": `btn-logs-${n}`, text: t("gui.logs") });
      logsBtn.onclick = () => this.openLogs(c);
      const termBtn = el("button", { class: "small", "data-testid": `btn-term-${n}`, text: t("gui.terminal") });
      termBtn.disabled = !running;
      termBtn.onclick = () => this.openTerminal(c);

      const rowAttrs: Record<string, string> = { "data-testid": "container-row", "data-name": n, "data-state": c.State };
      if (pending) rowAttrs["data-pending"] = pending;
      const stateText = pending ? t(`gui.ct.${pending}`) : c.State;
      tbody.append(
        el(
          "tr",
          rowAttrs,
          el("td", { text: n, title: c.Id }),
          el("td", { text: c.Image }),
          el("td", {}, el("span", { class: `state ${pending ? "pending" : c.State}`, "data-testid": `state-${n}`, text: stateText })),
          el("td", { text: c.Status }),
          el("td", { "data-testid": `ports-${n}`, text: ports(c) }),
          el("td", { class: "actions" }, startBtn, stopBtn, logsBtn, termBtn),
        ),
      );
    }
    list.replaceChildren(el("table", {}, thead, tbody));
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
      el("span", { "data-testid": "status-version", text: `v${this.st.meta.version}` }),
    );
  }

  // ---- 設定 -----------------------------------------------------------------

  private buildSettingsDialog(): HTMLDialogElement {
    const d = el("dialog", { "data-testid": "settings-dialog" }) as HTMLDialogElement;
    const interval = el("input", { type: "number", min: "1", max: "3600", "data-testid": "settings-refresh" }) as HTMLInputElement;
    const shell = el("input", { type: "text", "data-testid": "settings-exec-shell", placeholder: "bash" }) as HTMLInputElement;
    const auto = el("input", { type: "checkbox", "data-testid": "settings-autostart" }) as HTMLInputElement;
    const save = el("button", { class: "primary", type: "submit", "data-testid": "settings-save", text: t("gui.save") });
    const close = el("button", { type: "button", "data-testid": "settings-close", text: t("gui.close") });
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
