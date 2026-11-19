import { test, expect, type APIRequestContext } from "@playwright/test";
import { open, reset, setLang } from "./helpers";

async function lanEnv(request: APIRequestContext, q: string): Promise<void> {
  expect((await request.post(`/__test/lan?${q}`)).ok()).toBeTruthy();
}

type Elevation = { file: string; params: string };

async function lanState(request: APIRequestContext): Promise<{
  wslconfig: string;
  firewall_removes: number;
  firewall_allows: number;
  firewall_elevated: Elevation[];
  shutdowns: number;
}> {
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

  // 不經 wsld 確認框：直接生效；防火牆已授權時不顯示說明對話框、不跳 UAC
  await expect(page.getByTestId("confirm-dialog")).not.toBeVisible();
  await expect(page.getByTestId("fw-dialog")).not.toBeVisible();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("settings-lan-status")).toContainText("forwarding 1 address");
  await expect(page.getByTestId("notice")).toContainText("LAN access on");
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

test("turning LAN access on explains the firewall rule before UAC, then forwards once allowed", async ({ page, request }) => {
  await lanEnv(request, "fw=missing");
  await open(page);
  await page.getByTestId("nav-settings").click();
  // 啟動與進設定頁都不提權
  await expect(page.getByTestId("fw-dialog")).not.toBeVisible();
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0); // 關閉時不提示
  expect((await lanState(request)).firewall_allows).toBe(0);

  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  const dialog = page.getByTestId("fw-dialog");
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId("fw-dialog-title")).toHaveText("Allow LAN connections through Windows Firewall");
  const rule = page.getByTestId("fw-dialog-rule");
  await expect(rule).toContainText("wsld-gui (LAN access)");
  await expect(rule).toContainText("C:\\wsld\\wsld-gui.exe");
  await expect(rule).toContainText("TCP");
  await expect(rule).toContainText("Private + Public");
  await expect(rule).toContainText("same subnet only");
  const method = page.getByTestId("fw-dialog-method");
  await expect(method).toHaveAttribute("data-method", "netsh");
  await expect(method).toContainText("Network Command Shell (netsh.exe)");
  await expect(method).toContainText("Microsoft Windows");
  // 對話框開著時尚未提權，轉發暫停
  expect((await lanState(request)).firewall_allows).toBe(0);
  await expect(page.getByTestId("settings-lan-status")).toContainText("paused until wsld-gui is allowed");

  await page.getByTestId("fw-dialog-continue").click();
  await expect(dialog).not.toBeVisible();
  await expect(page.getByTestId("notice")).toContainText("Firewall rule added");
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0);
  await expect(page.getByTestId("settings-lan-status")).toContainText("forwarding 1 address");
  const st = await lanState(request);
  expect(st.firewall_allows).toBe(1);
  expect(st.firewall_elevated[0].file).toMatch(/\\System32\\netsh\.exe$/);
  expect(st.firewall_elevated[0].params).toContain("remoteip=localsubnet");
});

test("Not now keeps LAN access on with a paused warning; the button reopens the explanation", async ({ page, request }) => {
  await lanEnv(request, "fw=missing");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("fw-dialog")).toBeVisible();
  await page.getByTestId("fw-dialog-later").click();
  await expect(page.getByTestId("fw-dialog")).not.toBeVisible();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  expect((await lanState(request)).firewall_allows).toBe(0);
  const warn = page.getByTestId("settings-lan-fw-warn");
  await expect(warn).toHaveAttribute("data-state", "missing");
  await expect(warn).toContainText("forwarding is paused");
  await expect(warn).toContainText("moving or renaming the file");
  // 沒有轉發 → 容器列沒有地球按鈕
  await page.getByTestId("nav-containers").click();
  await expect(page.getByTestId("btn-lan-web")).toHaveCount(0);
  await page.getByTestId("nav-settings").click();

  await page.getByTestId("settings-lan-fw-allow").click();
  await expect(page.getByTestId("fw-dialog")).toBeVisible();
  expect((await lanState(request)).firewall_allows).toBe(0);
  await page.getByTestId("fw-dialog-continue").click();
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0);
  expect((await lanState(request)).firewall_allows).toBe(1);
  await page.getByTestId("nav-containers").click();
  await expect(page.getByTestId("btn-lan-web")).toBeVisible();
});

test("a blocked wsld-gui is fixed by wsld-gui itself and the dialog says so", async ({ page, request }) => {
  await lanEnv(request, "fw=blocked");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  const method = page.getByTestId("fw-dialog-method");
  await expect(method).toHaveAttribute("data-method", "helper");
  await expect(method).toContainText("UAC prompt shows wsld-gui");
  await expect(method).toContainText("does not start a second copy");
  await page.getByTestId("fw-dialog-continue").click();
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0);
  const st = await lanState(request);
  expect(st.firewall_elevated).toEqual([{ file: "C:\\wsld\\wsld-gui.exe", params: "--allow-firewall" }]);
});

test("the firewall request shows as waiting in Settings until UAC is answered", async ({ page, request }) => {
  await lanEnv(request, "fw=missing&fwhold=1");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await page.getByTestId("fw-dialog-continue").click();
  const warn = page.getByTestId("settings-lan-fw-warn");
  await expect(warn).toHaveAttribute("data-state", "pending");
  await expect(warn).toContainText("Waiting for UAC");
  await expect(warn.getByTestId("settings-lan-fw-allow")).toBeDisabled();
  await lanEnv(request, "fwhold=0");
  await expect(page.getByTestId("settings-lan-fw-warn")).toHaveCount(0);
  expect((await lanState(request)).firewall_allows).toBe(1);
});

test("retrying the firewall after a canceled UAC keeps the warning and error, then clears on success", async ({ page, request }) => {
  await lanEnv(request, "fw=blocked&fwfail=1");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await page.getByTestId("fw-dialog-continue").click();
  await expect(page.getByTestId("error-banner")).toContainText("Canceled at the UAC prompt");
  const warn = page.getByTestId("settings-lan-fw-warn");
  await expect(warn).toHaveAttribute("data-state", "blocked");

  await lanEnv(request, "fwfail=0");
  await page.getByTestId("settings-lan-fw-allow").click();
  await page.getByTestId("fw-dialog-continue").click();
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
  await expect(page.getByTestId("fw-dialog-title")).toHaveText("允許區網連線通過 Windows 防火牆");
  await expect(page.getByTestId("fw-dialog-rule")).toContainText("私人＋公用");
  await expect(page.getByTestId("fw-dialog-method")).toContainText("網路命令殼層");
  await expect(page.getByTestId("fw-dialog-continue")).toContainText("繼續");
  await page.getByTestId("fw-dialog-later").click();
  await expect(page.getByTestId("settings-lan-fw-warn")).toContainText("轉發已暫停");
  await expect(page.getByTestId("settings-lan-fw-allow")).toContainText("授權防火牆");
});
