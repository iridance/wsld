import { test, expect, type APIRequestContext } from "@playwright/test";
import { open, reset, setLang } from "./helpers";

async function lanEnv(request: APIRequestContext, q: string): Promise<void> {
  expect((await request.post(`/__test/lan?${q}`)).ok()).toBeTruthy();
}

async function lanState(request: APIRequestContext): Promise<{ wslconfig: string; firewall_removes: number; firewall_allows: number; shutdowns: number }> {
  const r = await request.get("/__test/lan");
  expect(r.ok()).toBeTruthy();
  return r.json();
}

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function enableLAN(page: import("@playwright/test").Page): Promise<void> {
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await page.getByTestId("nav-containers").click();
}

async function openedURLs(request: APIRequestContext): Promise<string[]> {
  const win = await (await request.get("/__test/window")).json();
  return (win.actions as string[]).filter((a) => a.startsWith("open-url:"));
}

test("network section shows LAN access off by default", async ({ page }) => {
  await open(page);
  await expect(page.locator('[data-testid="container-row"][data-name="web"]')).toBeVisible();
  await expect(page.getByTestId("btn-lan-web")).toHaveCount(0); // 關閉時不顯示按鈕
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-lan-group")).toBeVisible();
  await expect(page.getByTestId("settings-lan-label")).toHaveText("LAN access");
  const toggle = page.getByTestId("settings-lan");
  await expect(toggle).toHaveAttribute("role", "switch");
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  await expect(toggle).toBeEnabled();
  await expect(page.getByTestId("settings-lan-status")).toContainText("Off");
  await expect(page.getByTestId("settings-lan-mirrored")).toHaveCount(0);
});

test("turning LAN access on adds a globe button that opens the container in the browser", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();

  // 不經 wsld 確認框：直接生效；防火牆已授權時不跳 UAC
  await expect(page.getByTestId("confirm-dialog")).not.toBeVisible();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("settings-lan-status")).toContainText("forwarding 1 address");
  await expect(page.getByTestId("notice")).toContainText("LAN access on");
  await expect(page.getByTestId("settings-lan-firewall")).toHaveAttribute("data-state", "allowed");
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0);
  const st = await lanState(request);
  expect(st.shutdowns).toBe(0); // 不重啟 WSL
  expect(st.firewall_allows).toBe(0);

  await page.getByTestId("nav-containers").click();
  const row = page.locator('[data-testid="container-row"][data-name="web"]');
  const btn = row.getByTestId("btn-lan-web");
  await expect(btn).toBeEnabled();
  await expect(btn).toHaveAttribute("title", /http:\/\/192\.168\.1\.20:8080/);
  // 只是按鈕，名稱欄不再多一行位址
  await expect(row.locator("td.name-cell")).not.toContainText("192.168.1.20");
  // 位在 Terminal 與 Info 之間
  const order = await row.locator("td.actions button").evaluateAll((bs) => bs.map((b) => b.getAttribute("data-testid")));
  expect(order.indexOf("btn-lan-web")).toBe(order.indexOf("btn-term-web") + 1);
  expect(order.indexOf("btn-info-web")).toBe(order.indexOf("btn-lan-web") + 1);

  await btn.click();
  await expect.poll(() => openedURLs(request)).toEqual(["open-url:http://192.168.1.20:8080"]);

  // 沒有 published 埠的容器：按鈕停用（各列圖示對齊）
  await expect(page.getByTestId("btn-lan-db")).toBeDisabled();

  // Info 仍列出區網位址
  await page.getByTestId("btn-info-web").click();
  await expect(page.getByTestId("info-lan")).toContainText("http://192.168.1.20:8080");
});

test("a container with several published ports opens a menu to pick the address", async ({ page, request }) => {
  expect((await request.post("/__test/container?name=app&image=node:22&state=running&ports=3000,3001")).ok()).toBeTruthy();
  await open(page);
  await enableLAN(page);
  await page.getByTestId("btn-lan-app").click();
  const items = page.getByTestId("lan-menu-item");
  await expect(items).toHaveText(["192.168.1.20:3000", "192.168.1.20:3001"]);
  await items.nth(1).click();
  await expect(page.getByTestId("lan-menu")).toHaveCount(0);
  await expect.poll(() => openedURLs(request)).toEqual(["open-url:http://192.168.1.20:3001"]);

  // 點外面關閉選單
  await page.getByTestId("btn-lan-app").click();
  await expect(page.getByTestId("lan-menu")).toBeVisible();
  await page.getByTestId("page-title").click();
  await expect(page.getByTestId("lan-menu")).toHaveCount(0);
});

test("turning LAN access off removes the globe buttons", async ({ page }) => {
  await open(page);
  await enableLAN(page);
  await expect(page.getByTestId("btn-lan-web")).toBeVisible();
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "false");
  await expect(page.getByTestId("settings-lan-status")).toContainText("Off");
  await page.getByTestId("nav-containers").click();
  await expect(page.locator('[data-testid="container-row"][data-name="web"]')).toBeVisible();
  await expect(page.getByTestId("btn-lan-web")).toHaveCount(0);
});

test("a port that cannot be bound turns the globe red with the reason", async ({ page, request }) => {
  await lanEnv(request, "busy=8080");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan-status")).toContainText("could not listen on 1 address");
  await page.getByTestId("nav-containers").click();
  const btn = page.getByTestId("btn-lan-web");
  await expect(btn).toBeDisabled();
  await expect(btn).toHaveClass(/warn/);
  await expect(btn).toHaveAttribute("title", /Only one usage of each socket address/);
});

test("no published ports shows the idle status", async ({ page, request }) => {
  expect((await request.post("/api/containers/web/stop")).ok()).toBeTruthy();
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("settings-lan-status")).toContainText("no running container publishes a port");
});

test("mirrored mode from 0.9.0 shows a warning and Restore NAT", async ({ page, request }) => {
  await lanEnv(request, "mirrored=1");
  await open(page);
  await page.getByTestId("nav-settings").click();
  const warn = page.getByTestId("settings-lan-mirrored");
  await expect(warn).toBeVisible();
  await expect(warn).toContainText("mirrored");

  await page.getByTestId("settings-lan-restore").click();
  await expect(page.getByTestId("confirm-text")).toContainText("UAC");
  await expect(page.getByTestId("confirm-text")).toContainText("ALL WSL distros restart");
  const ok = page.getByTestId("confirm-ok");
  await expect(ok).toHaveText("Restore NAT");
  await expect(ok).not.toHaveClass(/danger/);
  await ok.click();

  await expect(page.getByTestId("settings-lan-mirrored")).toHaveCount(0);
  await expect(page.getByTestId("notice")).toContainText("back on NAT");
  const st = await lanState(request);
  expect(st.wslconfig).not.toContain("networkingMode");
  expect(st.wslconfig).toContain("memory=4GB");
  expect(st.firewall_removes).toBe(1);
  expect(st.shutdowns).toBe(1);
});

test("Restore NAT canceled at UAC shows the error and keeps the warning", async ({ page, request }) => {
  await lanEnv(request, "mirrored=1&fail=1");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan-restore").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("error-banner")).toContainText("canceled by the user");
  await expect(page.getByTestId("settings-lan-mirrored")).toBeVisible();
  expect((await lanState(request)).shutdowns).toBe(0);
});

test("delete dialogs keep their danger button after a Restore NAT confirm", async ({ page, request }) => {
  await lanEnv(request, "mirrored=1");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan-restore").click();
  await page.getByTestId("confirm-cancel").click();
  await page.getByTestId("nav-images").click();
  await page.getByTestId("btn-prune-images").click();
  await expect(page.getByTestId("confirm-ok")).toHaveClass(/danger/);
  await expect(page.getByTestId("confirm-ok")).toHaveText("Confirm");
});

test("turning LAN access on asks UAC to allow wsld-gui through the firewall when it is not allowed yet", async ({ page, request }) => {
  await lanEnv(request, "fw=missing");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-lan-firewall")).toHaveCount(0); // 關閉時不顯示
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("notice")).toContainText("Firewall rule added");
  await expect(page.getByTestId("settings-lan-firewall")).toHaveAttribute("data-state", "allowed");
  await expect(page.getByTestId("settings-lan-firewall")).toContainText("Allowed");
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0);
  expect((await lanState(request)).firewall_allows).toBe(1);
});

test("canceling the firewall UAC keeps LAN on with a warning and an Allow button that retries", async ({ page, request }) => {
  await lanEnv(request, "fw=blocked&fwfail=1");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("error-banner")).toContainText("Canceled at the UAC prompt");
  await expect(page.getByTestId("settings-lan-firewall")).toHaveAttribute("data-state", "blocked");
  const warn = page.getByTestId("settings-lan-fw-warn");
  await expect(warn).toBeVisible();
  await expect(warn).toContainText("moving or renaming the file");

  await lanEnv(request, "fwfail=0");
  await warn.getByTestId("settings-lan-fw-allow").click();
  await expect(page.getByTestId("settings-lan-firewall")).toHaveAttribute("data-state", "allowed");
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0);
  expect((await lanState(request)).firewall_allows).toBe(2);
});

test("LAN section is translated in zh-TW", async ({ page, request }) => {
  await setLang(request, "zh-TW");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-lan-label")).toHaveText("區網存取");
  await expect(page.getByTestId("settings-lan-status")).toContainText("關閉");
  await lanEnv(request, "fw=missing&fwfail=1");
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan-firewall")).toContainText("未授權");
  await expect(page.getByTestId("settings-lan-fw-allow")).toContainText("授權防火牆");
});
