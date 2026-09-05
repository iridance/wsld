import { test, expect } from "@playwright/test";
import { engine, open, reset, row, setLang } from "./helpers";

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

test("follows OS language: zh-TW renders Traditional Chinese labels", async ({ page, request }) => {
  await setLang(request, "zh-TW");
  await open(page);
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("btn-refresh")).toHaveText("刷新");
  await expect(page.getByTestId("status-connection")).toHaveText("已連線");
  await expect(page.locator("html")).toHaveAttribute("lang", "zh-TW");
  await setLang(request, "en");
});
