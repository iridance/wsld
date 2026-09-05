import "./style.css";
import { api } from "./api";
import { App } from "./app";

async function boot(): Promise<void> {
  const root = document.getElementById("app")!;
  try {
    const meta = await api.meta();
    new App(root, meta);
  } catch (e) {
    root.innerHTML = `<div class="banner" data-testid="boot-error" role="alert">wsld backend unreachable: ${String(e)}</div>`;
  }
}

void boot();
