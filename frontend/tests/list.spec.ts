import { test, expect } from "@playwright/test";
import { actionDelay, engine, open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("header carries distro and connection badge; footer carries version and language", async ({ page }) => {
  await open(page);
  const header = page.getByTestId("header");
  await expect(header.getByTestId("header-distro")).toHaveText("Ubuntu-26.04");
  await expect(header.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
  await expect(header.getByTestId("status-endpoint")).toHaveCount(0);
  await expect(header.getByTestId("btn-settings")).toHaveCount(0);
  await expect(page.getByTestId("status-refresh")).toHaveCount(0);
  const footer = page.getByTestId("status-bar");
  await expect(footer.getByTestId("status-version")).toHaveText("vtesthost");
  await expect(footer.getByTestId("lang-select")).toBeVisible();
  // 左下角舊資訊已整併到上方
  await expect(page.getByTestId("status-distro")).toHaveCount(0);
  await expect(page.getByTestId("status-api")).toHaveCount(0);
  // 不再有「顯示已停止」與手動刷新
  await expect(page.getByTestId("chk-show-all")).toHaveCount(0);
  await expect(page.getByTestId("btn-refresh")).toHaveCount(0);
});

test("layout: status bar sticks to the bottom and list fills the rest (banner hidden)", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  await expect(page.getByTestId("error-banner")).toBeHidden();
  const footer = await page.getByTestId("status-bar").boundingBox();
  const list = await page.getByTestId("container-list").boundingBox();
  expect(footer).not.toBeNull();
  expect(list).not.toBeNull();
  expect(Math.round(footer!.y + footer!.height)).toBe(640);
  expect(footer!.height).toBeLessThan(60);
  expect(list!.height).toBeGreaterThan(400);
});

test("layout: notice pins above the status bar without shifting the list", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  const listBefore = await page.getByTestId("container-list").boundingBox();
  const footerBefore = await page.getByTestId("status-bar").boundingBox();
  expect(listBefore).not.toBeNull();
  expect(footerBefore).not.toBeNull();

  await page.getByTestId("nav-images").click();
  await page.getByTestId("btn-rm-image-bbb222bbb222").click();
  await page.getByTestId("confirm-ok").click();
  const notice = page.getByTestId("notice");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("redis:7");

  const listAfter = await page.getByTestId("images-list").boundingBox();
  const footerAfter = await page.getByTestId("status-bar").boundingBox();
  const noticeBox = await notice.boundingBox();
  expect(listAfter).not.toBeNull();
  expect(footerAfter).not.toBeNull();
  expect(noticeBox).not.toBeNull();
  // footer 不因 notice 上移；notice 貼在 status bar 上方
  expect(Math.round(footerAfter!.y)).toBe(Math.round(footerBefore!.y));
  expect(Math.round(noticeBox!.y + noticeBox!.height)).toBe(Math.round(footerAfter!.y));
  // 列表區不被 notice 擠壓（高度仍接近滿版）
  expect(listAfter!.height).toBeGreaterThan(350);
});

test("layout: not-ready banner pins above the status bar without shifting the list", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  const listBefore = await page.getByTestId("container-list").boundingBox();
  const footerBefore = await page.getByTestId("status-bar").boundingBox();
  expect(listBefore).not.toBeNull();
  expect(footerBefore).not.toBeNull();

  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-use-docker-desktop").click();
  const banner = page.getByTestId("error-banner");
  await expect(banner).toBeVisible();
  await expect(banner).toHaveAttribute("data-kind", "not-ready");

  const listAfter = await page.getByTestId("machines-list").boundingBox();
  const footerAfter = await page.getByTestId("status-bar").boundingBox();
  const toastsBox = await page.getByTestId("toasts").boundingBox();
  const bannerBox = await banner.boundingBox();
  expect(listAfter).not.toBeNull();
  expect(footerAfter).not.toBeNull();
  expect(toastsBox).not.toBeNull();
  expect(bannerBox).not.toBeNull();
  // footer 不動；toasts（banner ± 成功 notice）整疊貼在 status bar 上方
  expect(Math.round(footerAfter!.y)).toBe(Math.round(footerBefore!.y));
  expect(Math.round(toastsBox!.y + toastsBox!.height)).toBe(Math.round(footerAfter!.y));
  expect(bannerBox!.y).toBeGreaterThan(footerAfter!.y - 120);
  // 列表高度不被 banner 擠壓（與切換前接近）
  expect(Math.abs(listAfter!.height - listBefore!.height)).toBeLessThan(80);
});

test("lists running and stopped containers together", async ({ page }) => {
  await open(page);
  await expect(row(page, "web")).toBeVisible();
  await expect(row(page, "web")).toHaveAttribute("data-state", "running");
  await expect(row(page, "web")).toContainText("nginx:1.27");
  await expect(row(page, "db")).toBeVisible();
  await expect(row(page, "db")).toHaveAttribute("data-state", "exited");
});

test("slow Stop shows a pending state immediately and disables the row's actions", async ({ page, request }) => {
  await actionDelay(request, 1500); // docker stop 寬限期可達 10s；UI 必須即時回饋
  await open(page);
  await expect(row(page, "web")).toHaveAttribute("data-state", "running");

  await page.getByTestId("btn-stop-web").click();
  await expect(row(page, "web")).toHaveAttribute("data-pending", "stopping");
  await expect(page.getByTestId("state-web")).toHaveText("Stopping…");
  await expect(page.getByTestId("btn-stop-web")).toBeDisabled();
  await expect(page.getByTestId("btn-start-web")).toBeDisabled();
  await expect(page.getByTestId("error-banner")).toBeHidden();

  await expect(row(page, "web")).toHaveAttribute("data-state", "exited", { timeout: 5000 });
  await expect(row(page, "web")).not.toHaveAttribute("data-pending", /.+/);
  await expect(page.getByTestId("state-web")).toHaveText("exited");
  await expect(page.getByTestId("error-banner")).toBeHidden();
});

test("Stop and Start buttons change container state", async ({ page }) => {
  await open(page);
  await expect(row(page, "web")).toHaveAttribute("data-state", "running");
  await expect(page.getByTestId("btn-start-web")).toBeDisabled();

  await page.getByTestId("btn-stop-web").click();
  await expect(row(page, "web")).toHaveAttribute("data-state", "exited");
  await expect(page.getByTestId("btn-stop-web")).toBeDisabled();

  await page.getByTestId("btn-start-db").click();
  await expect(row(page, "db")).toHaveAttribute("data-state", "running");
  await expect(page.getByTestId("btn-term-db")).toBeEnabled();
});

test("auto refresh picks up changes within the configured interval", async ({ page, request }) => {
  await open(page);
  await request.post("/__test/container?name=auto&image=busybox&state=running");
  await expect(row(page, "auto")).toBeVisible({ timeout: 10_000 });
});

test("engine down shows Disconnected badge, error banner and enables Start engine; starting reconnects", async ({ page, request }) => {
  await open(page);
  await expect(page.getByTestId("btn-start-engine")).toBeDisabled();

  await engine(request, "down");
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "disconnected", { timeout: 10_000 });
  await expect(page.getByTestId("error-banner")).toBeVisible();
  await expect(page.getByTestId("error-banner")).toContainText("Cannot reach Docker API");
  await expect(page.getByTestId("btn-start-engine")).toBeEnabled();

  await page.getByTestId("btn-start-engine").click();
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
  await expect(page.getByTestId("error-banner")).toBeHidden();
  await expect(row(page, "web")).toBeVisible();

  const win = await (await request.get("/__test/window")).json();
  expect(win.starts).toBe(1);
});

test("Start engine failure surfaces the not-ready message", async ({ page, request }) => {
  await engine(request, "down");
  await lifecycleBroken(request);
  await open(page);
  await expect(page.getByTestId("btn-start-engine")).toBeEnabled();
  await page.getByTestId("btn-start-engine").click();
  await expect(page.getByTestId("error-banner")).toContainText("systemd");
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "disconnected");
});

async function lifecycleBroken(request: Parameters<typeof engine>[0]) {
  await request.post("/__test/lifecycle/broken");
}

test("refresh pauses on blur and resumes on focus", async ({ page }) => {
  await open(page);
  const st = () => page.evaluate(() => (window as unknown as { __wsld: { state(): { paused: boolean; refreshCount: number } } }).__wsld.state());
  await expect.poll(async () => (await st()).paused).toBe(false);

  await page.evaluate(() => {
    Object.defineProperty(document, "hasFocus", { value: () => false, configurable: true });
    window.dispatchEvent(new Event("blur"));
  });
  await expect.poll(async () => (await st()).paused).toBe(true);
  const count = (await st()).refreshCount;
  await page.waitForTimeout(3500);
  expect((await st()).refreshCount).toBe(count);

  await page.evaluate(() => {
    Object.defineProperty(document, "hasFocus", { value: () => true, configurable: true });
    window.dispatchEvent(new Event("focus"));
  });
  await expect.poll(async () => (await st()).paused).toBe(false);
  await expect.poll(async () => (await st()).refreshCount).toBeGreaterThan(count);
});

test("defaults to English; the status-bar language switch changes labels and persists", async ({ page }) => {
  await open(page);
  await expect(page).toHaveTitle("wsld");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("Start engine");
  await expect(page.getByTestId("lang-select")).toHaveValue("en");

  await page.getByTestId("lang-select").selectOption("zh-TW");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("nav-containers")).toHaveText("容器");
  await expect(page.getByTestId("status-connection")).toHaveText("已連線");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-TW");

  await page.reload(); // 存在 config，不是只在記憶體
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("lang-select")).toHaveValue("zh-TW");

  await page.getByTestId("lang-select").selectOption("en");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("Start engine");
});

test("every button carries a semantic icon; row actions are icon-only with an accessible name", async ({ page }) => {
  await open(page);
  const expected: Record<string, string> = {
    "btn-start-engine": "power",
    "btn-open-folder": "folder",
    "btn-open-terminal": "terminal",
    "btn-start-web": "play",
    "btn-stop-web": "stop",
    "btn-logs-web": "logs",
    "btn-term-web": "terminal",
    "btn-info-web": "info",
    "btn-rm-web": "trash",
  };
  for (const [tid, icon] of Object.entries(expected)) {
    const b = page.getByTestId(tid);
    await expect(b.locator("svg.ico").first()).toHaveAttribute("data-icon", icon);
    expect(await b.evaluate((e) => e.firstElementChild?.tagName.toLowerCase())).toBe("svg");
    await expect(b.locator("svg.ico")).toHaveAttribute("aria-hidden", "true");
  }
  for (const [tid, label] of [
    ["btn-start-web", "Start"],
    ["btn-stop-web", "Stop"],
    ["btn-logs-web", "Logs"],
    ["btn-term-web", "Terminal"],
    ["btn-info-web", "Info"],
    ["btn-rm-web", "Remove"],
  ]) {
    const b = page.getByTestId(tid);
    await expect(b).toHaveText("");
    await expect(b).toHaveAttribute("aria-label", label);
    await expect(b).toHaveAttribute("title", label);
    expect(await b.evaluate((e) => getComputedStyle(e).borderStyle)).toBe("none");
  }
  await expect(page.getByTestId("btn-start-engine")).toHaveText("Start engine");
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-save").locator("svg.ico")).toHaveAttribute("data-icon", "save");
});

test("header shows the connected WSL distro name next to the connection badge", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("header-distro")).toHaveText("Ubuntu-26.04");
  await expect(page.getByTestId("header-distro")).toBeVisible();
});
