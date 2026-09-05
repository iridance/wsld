import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { api } from "./api";

export interface TerminalSession {
  term: Terminal;
  close(): void;
  /** 供測試讀取目前緩衝區文字。 */
  text(): string;
}

/** 在 host 內開 xterm 並以 WebSocket 連到容器 exec。 */
export function openTerminal(host: HTMLElement, containerId: string, onClosed: (reason: string) => void): TerminalSession {
  const term = new Terminal({ cursorBlink: true, convertEol: false, fontSize: 13, theme: { background: "#1e1e1e" } });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open(host);
  fit.fit();

  const ws = new WebSocket(api.execUrl(containerId, term.cols, term.rows));
  ws.binaryType = "arraybuffer";
  const enc = new TextEncoder();
  const dec = new TextDecoder();

  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) term.write(dec.decode(new Uint8Array(ev.data)));
    else term.write(String(ev.data));
  };
  ws.onclose = (ev) => {
    term.write(`\r\n\x1b[90m[${ev.reason || "closed"}]\x1b[0m\r\n`);
    onClosed(ev.reason || "closed");
  };
  ws.onerror = () => onClosed("error");

  const dataSub = term.onData((d) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
  });
  const resizeSub = term.onResize(({ cols, rows }) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols, rows }));
  });
  const onWin = () => fit.fit();
  window.addEventListener("resize", onWin);

  return {
    term,
    close() {
      window.removeEventListener("resize", onWin);
      dataSub.dispose();
      resizeSub.dispose();
      try {
        ws.close();
      } catch {
        /* ignore */
      }
      term.dispose();
    },
    text() {
      const buf = term.buffer.active;
      const lines: string[] = [];
      for (let i = 0; i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? "");
      return lines.join("\n");
    },
  };
}
