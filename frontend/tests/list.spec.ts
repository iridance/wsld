import { test, expect } from "@playwright/test";
import { actionDelay, engine, open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("shows status bar with distro, endpoint and connected badge", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
  await expect(page.getByTestId("status-distro")).toContainText("Ubuntu-26.04");
  await expect(page.getByTestId("status-endpoint")).toHaveText("tcp://127.0.0.1:23750");
  await expect(page.getByTestId("status-api")).toHaveText("Connected");
  await expect(page.getByTestId("status-version")).toHaveText("vtesthost");
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

test("lists running containers by default; 'Show stopped' reveals exited ones", async ({ page }) => {
  await open(page);
  await expect(row(page, "web")).toBeVisible();
  await expect(row(page, "web")).toHaveAttribute("data-state", "running");
  await expect(row(page, "web")).toContainText("nginx:1.27");
  await expect(row(page, "web").getByTestId("ports-web")).toHaveText("8080→80/tcp"); // IPv4/IPv6 去重
  await expect(row(page, "db")).toHaveCount(0);

  await page.getByTestId("chk-show-all").check();
  await expect(row(page, "db")).toBeVisible();
  await expect(row(page, "db")).toHaveAttribute("data-state", "exited");
});

test("slow Stop shows a pending state immediately and disables the row's actions", async ({ page, request }) => {
  await actionDelay(request, 1500); // docker stop 寬限期可達 10s；UI 必須即時回饋
  await open(page);
  await page.getByTestId("chk-show-all").check();
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
  await page.getByTestId("chk-show-all").check();
  await expect(row(page, "web")).toHaveAttribute("data-state", "running");
  await expect(page.getByTestId("btn-start-web")).toBeDisabled();

  await page.getByTestId("btn-stop-web").click();
  await expect(row(page, "web")).toHaveAttribute("data-state", "exited");
  await expect(page.getByTestId("btn-stop-web")).toBeDisabled();

  await page.getByTestId("btn-start-db").click();
  await expect(row(page, "db")).toHaveAttribute("data-state", "running");
  await expect(page.getByTestId("btn-term-db")).toBeEnabled();
});

test("Refresh button re-fetches the list", async ({ page, request }) => {
  await open(page);
  await expect(row(page, "cache")).toHaveCount(0);
  await request.post("/__test/container?name=cache&image=redis:7&state=running");
  await page.getByTestId("btn-refresh").click();
  await expect(row(page, "cache")).toBeVisible();
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
  await page.getByTestId("btn-refresh").click();
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "disconnected");
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
  await expect(page.getByTestId("status-refresh")).toHaveAttribute("data-paused", "false");

  await page.evaluate(() => {
    Object.defineProperty(document, "hasFocus", { value: () => false, configurable: true });
    window.dispatchEvent(new Event("blur"));
  });
  await expect(page.getByTestId("status-refresh")).toHaveAttribute("data-paused", "true");
  await expect(page.getByTestId("status-refresh")).toHaveText("Refresh paused");
  const count = Number(await page.getByTestId("status-refresh").getAttribute("data-count"));
  await page.waitForTimeout(3500);
  expect(Number(await page.getByTestId("status-refresh").getAttribute("data-count"))).toBe(count);

  await page.evaluate(() => {
    Object.defineProperty(document, "hasFocus", { value: () => true, configurable: true });
    window.dispatchEvent(new Event("focus"));
  });
  await expect(page.getByTestId("status-refresh")).toHaveAttribute("data-paused", "false");
  await expect
    .poll(async () => Number(await page.getByTestId("status-refresh").getAttribute("data-count")))
    .toBeGreaterThan(count);
});

test("defaults to English; the status-bar language switch changes labels and persists", async ({ page }) => {
  await open(page);
  await expect(page).toHaveTitle("wsld");
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("Start engine");
  await expect(page.getByTestId("lang-select")).toHaveValue("en");

  await page.getByTestId("lang-select").selectOption("zh-TW");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("btn-refresh")).toHaveText("刷新");
  await expect(page.getByTestId("status-connection")).toHaveText("已連線");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-TW");

  await page.reload(); // 存在 config，不是只在記憶體
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("lang-select")).toHaveValue("zh-TW");

  await page.getByTestId("lang-select").selectOption("en");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("Start engine");
});

test("every button carries a semantic icon before its label", async ({ page }) => {
  await open(page);
  await page.getByTestId("chk-show-all").check();
  const expected: Record<string, string> = {
    "btn-start-engine": "power",
    "btn-refresh": "refresh",
    "btn-settings": "settings",
    "btn-start-web": "play",
    "btn-stop-web": "stop",
    "btn-logs-web": "logs",
    "btn-term-web": "terminal",
  };
  for (const [tid, icon] of Object.entries(expected)) {
    const b = page.getByTestId(tid);
    await expect(b.locator("svg.ico").first()).toHaveAttribute("data-icon", icon);
    // icon 在文字前面，且不進入可存取名稱
    expect(await b.evaluate((e) => e.firstElementChild?.tagName.toLowerCase())).toBe("svg");
    await expect(b.locator("svg.ico")).toHaveAttribute("aria-hidden", "true");
  }
  await expect(page.getByTestId("btn-start-web")).toHaveText("Start");
  await page.getByTestId("btn-settings").click();
  await expect(page.getByTestId("settings-save").locator("svg.ico")).toHaveAttribute("data-icon", "save");
  await expect(page.getByTestId("settings-close").locator("svg.ico")).toHaveAttribute("data-icon", "close");
});

test("header shows the connected WSL distro name next to the connection badge", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("header-distro")).toHaveText("Ubuntu-26.04");
  await expect(page.getByTestId("header-distro")).toBeVisible();
});
