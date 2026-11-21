import { test, expect } from "@playwright/test";
import { addComposeContainer, group, open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("container Info opens a right-side drawer with state and ports; columns are gone from the row", async ({ page }) => {
  await open(page);
  await expect(page.locator('th[data-col="status"]')).toHaveCount(0);
  await expect(page.locator('th[data-col="ports"]')).toHaveCount(0);
  await expect(row(page, "web").getByTestId("ports-web")).toHaveCount(0);

  const drawer = page.getByTestId("info-drawer");
  await expect(drawer).toBeHidden();

  await page.getByTestId("btn-info-web").click();
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute("data-kind", "container");
  await expect(drawer).toHaveAttribute("data-name", "web");
  await expect(page.getByTestId("info-title")).toContainText("web");
  await expect(page.getByTestId("info-state")).toContainText("running");
  await expect(page.getByTestId("info-status")).toHaveCount(0);
  await expect(page.getByTestId("info-ports")).toContainText("80");
  await expect(page.getByTestId("info-command")).toContainText("nginx");
  await expect(page.getByTestId("info-networks")).toContainText("bridge");
  await expect(page.getByTestId("info-mounts")).toContainText("/app");
  await expect(page.getByTestId("info-restart")).toHaveText("unless-stopped");

  // 從右側滑出：貼在列表右側、寬度約 340
  const box = await drawer.boundingBox();
  const list = await page.getByTestId("container-list").boundingBox();
  expect(box).not.toBeNull();
  expect(list).not.toBeNull();
  expect(box!.x).toBeGreaterThan(list!.x + list!.width / 2);
  expect(box!.width).toBeGreaterThan(280);
  expect(box!.width).toBeLessThanOrEqual(360);

  await page.getByTestId("btn-info-close").click();
  await expect(drawer).toBeHidden();
});

test("container Info keeps scroll position across auto refresh", async ({ page }) => {
  await open(page);
  await page.addStyleTag({ content: '.info-body { max-height: 120px !important; }' });
  await page.getByTestId("btn-info-web").click();
  const body = page.locator('[data-testid="info-drawer"] .info-body');
  await expect(page.getByTestId("info-state")).toBeVisible();
  await expect(page.getByTestId("info-mounts")).toBeVisible();

  await expect.poll(async () => body.evaluate((el) => (el as HTMLElement).scrollHeight - (el as HTMLElement).clientHeight)).toBeGreaterThan(40);
  await body.evaluate((el) => {
    (el as HTMLElement).dataset.scrollProbe = "1";
    (el as HTMLElement).scrollTop = 80;
  });
  await expect.poll(async () => body.evaluate((el) => (el as HTMLElement).scrollTop)).toBe(80);

  const before = await page.evaluate(() => (window as unknown as { __wsld: { state(): { refreshCount: number } } }).__wsld.state().refreshCount);
  await page.evaluate(() => (window as unknown as { __wsld: { refresh(): Promise<void> } }).__wsld.refresh());
  await expect.poll(async () => page.evaluate(() => (window as unknown as { __wsld: { state(): { refreshCount: number } } }).__wsld.state().refreshCount)).toBeGreaterThan(before);

  // 列表輪詢不重建 Info；同一節點仍在且捲動保留
  await expect(body).toHaveAttribute("data-scroll-probe", "1");
  await expect.poll(async () => body.evaluate((el) => (el as HTMLElement).scrollTop)).toBe(80);
});

test("Remove running container asks for confirmation and deletes the row", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-rm-web").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("web");
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.locator('[data-testid="container-row"][data-name="web"]')).toHaveCount(0);
});

test("compose group actions are icon-only and Info opens project drawer", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "web", "nginx:1.27");
  await addComposeContainer(request, "shop", "api", "node:22", "exited");
  await open(page);

  for (const [tid, icon, label] of [
    ["group-start", "play", "Start"],
    ["group-stop", "stop", "Stop"],
    ["group-info", "info", "Info"],
  ] as const) {
    const b = group(page, "shop").getByTestId(tid);
    await expect(b.locator("svg.ico")).toHaveAttribute("data-icon", icon);
    await expect(b).toHaveText("");
    await expect(b).toHaveAttribute("aria-label", label);
    expect(await b.evaluate((e) => getComputedStyle(e).borderStyle)).toBe("none");
  }

  await group(page, "shop").getByTestId("group-info").click();
  const drawer = page.getByTestId("info-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute("data-kind", "compose");
  await expect(drawer).toHaveAttribute("data-project", "shop");
  await expect(page.getByTestId("info-title")).toContainText("shop");
  await expect(page.getByTestId("info-count")).toHaveText("1/2");
});

test("header distro has open-folder and open-terminal icon buttons; refresh interval is not shown", async ({ page, request }) => {
  await open(page);
  await expect(page.getByTestId("status-refresh")).toHaveCount(0);

  const folder = page.getByTestId("btn-open-folder");
  const term = page.getByTestId("btn-open-terminal");
  await expect(folder).toHaveText("");
  await expect(term).toHaveText("");
  await expect(folder).toHaveAttribute("aria-label", "Open folder");
  await expect(term).toHaveAttribute("aria-label", "Open terminal");
  await expect(folder.locator("svg.ico")).toHaveAttribute("data-icon", "folder");
  await expect(term.locator("svg.ico")).toHaveAttribute("data-icon", "terminal");
  expect(await folder.evaluate((e) => getComputedStyle(e).borderStyle)).not.toBe("none");

  await folder.click();
  await term.click();
  const win = await (await request.get("/__test/window")).json();
  expect(win.actions).toEqual(expect.arrayContaining(["open-folder", "open-terminal"]));
});

// 實機上任何 wsl -d 都會把停止中的機器開起來：開 Info／輪詢都不可讀 wsl.conf 或取樣用量。
test("machine Info on a stopped machine does not start it; it shows wsl.conf once the machine is started", async ({ page, request }) => {
  await request.put("/api/settings", {
    data: { refresh_interval_sec: 1, exec_shell: "", tray_autostart: false, lang: "en", theme: "system" },
  });
  await open(page);
  await page.getByTestId("nav-machines").click();
  const card = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await expect(card).toHaveAttribute("data-state", "stopped");
  let configCalls = 0;
  page.on("request", (r) => {
    if (r.method() === "GET" && r.url().endsWith("/api/machines/docker-desktop/config")) configCalls++;
  });

  await page.getByTestId("btn-machine-config-docker-desktop").click();
  const drawer = page.getByTestId("info-drawer");
  await expect(drawer.getByTestId("info-title")).toHaveText("docker-desktop");
  await expect(drawer.getByTestId("machine-config-unavailable")).toHaveText("Start the machine to view /etc/wsl.conf");
  await expect(drawer.getByTestId("machine-config-systemd")).toHaveCount(0);
  await expect(drawer.getByTestId("machine-config-conf")).toHaveCount(0);

  // 數個輪詢週期後仍停止，且停止中不再打 config API
  await page.waitForTimeout(3500);
  await expect(card).toHaveAttribute("data-state", "stopped");
  expect(configCalls).toBe(1);
  const m = await (await request.get("/api/machines")).json();
  expect(m.machines.find((x: { name: string }) => x.name === "docker-desktop").state).toBe("Stopped");

  // 啟動後 Info 自動換成實際的 wsl.conf
  await page.getByTestId("btn-machine-start-docker-desktop").click();
  await expect(card).toHaveAttribute("data-state", "running");
  await expect(drawer.getByTestId("machine-config-conf")).toHaveText("(no /etc/wsl.conf)", { timeout: 5000 });
  await expect(drawer.getByTestId("machine-config-systemd")).toBeVisible();
  await expect(drawer.getByTestId("machine-config-unavailable")).toHaveCount(0);

  // 再停止：回到「啟動後才能查看」
  await page.getByTestId("btn-machine-stop-docker-desktop").click();
  await expect(card).toHaveAttribute("data-state", "stopped");
  await expect(drawer.getByTestId("machine-config-unavailable")).toBeVisible({ timeout: 5000 });
});
