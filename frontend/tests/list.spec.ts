import { test, expect } from "@playwright/test";
import { actionDelay, engine, open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("top bar carries distro, connection and version; Settings carries github and language; page title sits in the content", async ({ page }) => {
  await open(page);
  const nav = page.getByTestId("header");
  await expect(nav.getByTestId("header-distro")).toHaveText("Ubuntu-26.04");
  await expect(nav.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
  await expect(nav.getByTestId("status-version")).toHaveText("vtesthost");
  await expect(nav.getByTestId("status-github")).toHaveCount(0);
  await expect(nav.getByTestId("lang-select")).toHaveCount(0);
  const header = page.getByTestId("header");
  await expect(page.getByTestId("view-containers").getByTestId("page-title")).toHaveText("Containers");
  await expect(header.getByTestId("btn-open-folder")).toBeVisible();
  await expect(header.getByTestId("status-endpoint")).toHaveCount(0);
  await expect(header.getByTestId("btn-settings")).toHaveCount(0);
  // 已連線時不佔頁首空間
  await expect(header.getByTestId("btn-start-engine")).toBeHidden();
  await expect(page.getByTestId("status-refresh")).toHaveCount(0);
  await expect(page.getByTestId("status-bar")).toHaveCount(0);
  // 左下角舊資訊已整併到上方
  await expect(page.getByTestId("status-distro")).toHaveCount(0);
  await expect(page.getByTestId("status-api")).toHaveCount(0);
  // 不再有「顯示已停止」與手動刷新
  await expect(page.getByTestId("chk-show-all")).toHaveCount(0);
  await expect(page.getByTestId("btn-refresh")).toHaveCount(0);
  await page.getByTestId("nav-settings").click();
  const about = page.getByTestId("settings-about");
  const gh = about.getByTestId("status-github");
  await expect(gh).toHaveAttribute("href", "https://github.com/iridance/wsld");
  await expect(gh.locator("svg.ico")).toHaveAttribute("data-icon", "github");
  await expect(about).toContainText("vtesthost");
  await expect(page.getByTestId("view-settings").getByTestId("lang-select")).toBeVisible();
});

test("layout: list fills the content column down to the window bottom (banner hidden)", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  await expect(page.getByTestId("error-banner")).toBeHidden();
  const list = await page.getByTestId("container-list").boundingBox();
  expect(list).not.toBeNull();
  // 只留內容區 24px 底邊距
  expect(640 - (list!.y + list!.height)).toBeLessThanOrEqual(32);
  // 列表緊接在磁碟用量卡下方，其餘高度全給列表
  const slot = await page.getByTestId("disk-slot").boundingBox();
  expect(slot).not.toBeNull();
  expect(list!.y - (slot!.y + slot!.height)).toBeLessThanOrEqual(16);
  expect(list!.height).toBeGreaterThan(330);
});

test("layout: notice floats at the bottom of the content column without shifting the list", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  await page.getByTestId("nav-images").click();
  await expect(page.getByTestId("image-row").first()).toBeVisible();
  const listBefore = await page.getByTestId("images-list").boundingBox();
  expect(listBefore).not.toBeNull();

  await page.getByTestId("btn-rm-image-bbb222bbb222").click();
  await page.getByTestId("confirm-ok").click();
  const notice = page.getByTestId("notice");
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("redis:7");

  const listAfter = await page.getByTestId("images-list").boundingBox();
  const noticeBox = await notice.boundingBox();
  const header = await page.getByTestId("header").boundingBox();
  expect(listAfter).not.toBeNull();
  expect(noticeBox).not.toBeNull();
  // 疊在內容底部（不蓋頂部列），不推擠列表
  expect(noticeBox!.y).toBeGreaterThanOrEqual(header!.y + header!.height);
  expect(640 - (noticeBox!.y + noticeBox!.height)).toBeLessThanOrEqual(24);
  expect(Math.round(listAfter!.y)).toBe(Math.round(listBefore!.y));
  expect(Math.round(listAfter!.height)).toBe(Math.round(listBefore!.height));
});

test("layout: not-ready banner floats at the bottom without shifting the list", async ({ page, request }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  // use 會自動 init；改以測試鉤子把 active profile 標成未就緒
  expect((await request.post("/__test/active-not-ready")).ok()).toBeTruthy();
  await open(page);
  await page.getByTestId("nav-machines").click();
  const banner = page.getByTestId("error-banner");
  await expect(banner).toBeVisible();
  await expect(banner).toHaveAttribute("data-kind", "not-ready");

  const list = await page.getByTestId("machines-list").boundingBox();
  const toastsBox = await page.getByTestId("toasts").boundingBox();
  const bannerBox = await banner.boundingBox();
  expect(list).not.toBeNull();
  expect(toastsBox).not.toBeNull();
  expect(bannerBox).not.toBeNull();
  // toasts 整疊貼在視窗底部；列表仍延伸到底（被覆蓋而非擠壓）
  expect(Math.round(toastsBox!.y + toastsBox!.height)).toBe(640);
  expect(bannerBox!.y).toBeGreaterThan(640 - 140);
  expect(640 - (list!.y + list!.height)).toBeLessThanOrEqual(32);
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

test("Start engine failure stays in the banner across refresh polls (also on Machines)", async ({ page, request }) => {
  await engine(request, "down");
  await lifecycleBroken(request);
  await open(page);
  await page.getByTestId("btn-start-engine").click();
  await expect(page.getByTestId("error-banner")).toContainText("systemd");
  const st = () => page.evaluate(() => (window as unknown as { __wsld: { state(): { refreshCount: number } } }).__wsld.state());
  const count = (await st()).refreshCount;
  await expect.poll(async () => (await st()).refreshCount, { timeout: 10_000 }).toBeGreaterThan(count + 1);
  await expect(page.getByTestId("error-banner")).toContainText("systemd");

  await page.getByTestId("nav-machines").click();
  const c2 = (await st()).refreshCount;
  await expect.poll(async () => (await st()).refreshCount, { timeout: 10_000 }).toBeGreaterThan(c2 + 1);
  await expect(page.getByTestId("error-banner")).toContainText("systemd");
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

test("defaults to English; the Settings language switch changes labels and persists", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page).toHaveTitle("wsld");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("Start engine");
  await expect(page.getByTestId("lang-select")).toHaveAttribute("data-value", "en");

  await page.getByTestId("lang-select").click();
  await page.getByTestId("lang-select-option-zh-TW").click();
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("nav-containers")).toHaveText("容器");
  await expect(page.getByTestId("status-connection")).toHaveText("已連線");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-TW");

  await page.reload(); // 存在 config，不是只在記憶體
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("lang-select")).toHaveAttribute("data-value", "zh-TW");

  await page.getByTestId("lang-select").click();
  await page.getByTestId("lang-select-option-en").click();
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

test("container toolbar: status filter and search narrow the list; header shows the running/stopped summary", async ({ page }) => {
  await open(page);
  await expect(row(page, "web")).toBeVisible();
  await expect(page.getByTestId("page-sub")).toHaveText("1 running · 1 stopped · 2.9 MB");
  await expect(page.getByTestId("ct-filter-all")).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByTestId("ct-filter-running")).toHaveText("Running 1");

  await page.getByTestId("ct-filter-running").click();
  await expect(page.getByTestId("ct-filter-running")).toHaveAttribute("aria-pressed", "true");
  await expect(row(page, "web")).toBeVisible();
  await expect(row(page, "db")).toHaveCount(0);

  await page.getByTestId("ct-filter-stopped").click();
  await expect(row(page, "db")).toBeVisible();
  await expect(row(page, "web")).toHaveCount(0);

  await page.getByTestId("ct-filter-all").click();
  await page.getByTestId("ct-search").fill("nginx");
  await expect(row(page, "web")).toBeVisible();
  await expect(row(page, "db")).toHaveCount(0);
  // 自動刷新後篩選仍保留
  await page.evaluate(() => (window as unknown as { __wsld: { refresh(): Promise<void> } }).__wsld.refresh());
  await expect(row(page, "db")).toHaveCount(0);
  await expect(page.getByTestId("ct-search")).toHaveValue("nginx");

  await page.getByTestId("ct-search").fill("no-such-thing");
  await expect(page.getByTestId("empty-filtered")).toBeVisible();
  await page.getByTestId("ct-search").fill("");
  await expect(row(page, "db")).toBeVisible();
});

test("row shows the docker status line under the name; opening Info marks the row as selected", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("ct-status-web")).not.toBeEmpty();
  await page.getByTestId("btn-info-web").click();
  await expect(row(page, "web")).toHaveAttribute("aria-selected", "true");
  await expect(row(page, "db")).not.toHaveAttribute("aria-selected", "true");
  await page.getByTestId("btn-info-close").click();
  await expect(row(page, "web")).not.toHaveAttribute("aria-selected", "true");
});

test("container panel has Logs / Terminal tabs for the same container", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-logs-web").click();
  const panel = page.getByTestId("panel");
  await expect(panel).toHaveAttribute("data-kind", "logs");
  await expect(page.getByTestId("panel-tab-logs")).toHaveAttribute("aria-selected", "true");
  await page.getByTestId("panel-tab-term").click();
  await expect(panel).toHaveAttribute("data-kind", "term");
  await expect(page.getByTestId("panel-tab-term")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByTestId("panel-title")).toContainText("web");
  await page.getByTestId("panel-tab-logs").click();
  await expect(panel).toHaveAttribute("data-kind", "logs");
  await expect(page.getByTestId("logs-content")).toContainText("GET / 200");

  // 已停止的容器不能開終端分頁
  await page.getByTestId("btn-logs-db").click();
  await expect(page.getByTestId("panel-tab-term")).toBeDisabled();
});
