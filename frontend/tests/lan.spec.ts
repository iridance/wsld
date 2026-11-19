import { test, expect, type APIRequestContext } from "@playwright/test";
import { open, reset, setLang } from "./helpers";

async function lanEnv(request: APIRequestContext, q: string): Promise<void> {
  expect((await request.post(`/__test/lan?${q}`)).ok()).toBeTruthy();
}

async function lanState(request: APIRequestContext): Promise<{ wslconfig: string; firewall_removes: number; shutdowns: number }> {
  const r = await request.get("/__test/lan");
  expect(r.ok()).toBeTruthy();
  return r.json();
}

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("network section shows LAN access off by default", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("ct-lan-web")).toHaveCount(0);
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

test("turning LAN access on shows addresses under the container on the Containers page", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();

  // 不經 wsld 確認框：直接生效（Windows 自己會跳防火牆確認）
  await expect(page.getByTestId("confirm-dialog")).not.toBeVisible();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("settings-lan-status")).toContainText("forwarding 1 address");
  await expect(page.getByTestId("notice")).toContainText("allow wsld-gui on Private networks");
  // 設定頁不再列位址
  await expect(page.getByTestId("ct-lan-link")).toHaveCount(0);
  expect((await lanState(request)).shutdowns).toBe(0); // 不重啟 WSL

  await page.getByTestId("nav-containers").click();
  const lan = page.getByTestId("ct-lan-web");
  await expect(lan).toBeVisible();
  await expect(lan).toContainText("LAN");
  const link = lan.getByTestId("ct-lan-link");
  await expect(link).toHaveText("192.168.1.20:8080");
  await expect(link).toHaveAttribute("href", "http://192.168.1.20:8080");
  // 位址在該服務列的名稱欄內
  await expect(page.locator('[data-testid="container-row"][data-name="web"]').getByTestId("ct-lan-link")).toHaveCount(1);

  // 點擊以系統瀏覽器開啟
  await link.click();
  await expect
    .poll(async () => ((await (await request.get("/__test/window")).json()).actions as string[]).includes("open-url:http://192.168.1.20:8080"))
    .toBe(true);

  // Info 也列出區網位址
  await page.getByTestId("btn-info-web").click();
  await expect(page.getByTestId("info-lan")).toContainText("http://192.168.1.20:8080");
});

test("turning LAN access off removes the addresses", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "false");
  await expect(page.getByTestId("settings-lan-status")).toContainText("Off");
  await page.getByTestId("nav-containers").click();
  await expect(page.locator('[data-testid="container-row"][data-name="web"]')).toBeVisible();
  await expect(page.getByTestId("ct-lan-web")).toHaveCount(0);
});

test("a port that cannot be bound shows its error under the container", async ({ page, request }) => {
  await lanEnv(request, "busy=8080");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("settings-lan-status")).toContainText("could not listen on 1 address");
  await page.getByTestId("nav-containers").click();
  const err = page.getByTestId("ct-lan-web").getByTestId("ct-lan-error");
  await expect(err).toHaveText("192.168.1.20:8080");
  await expect(err).toHaveAttribute("title", /Only one usage of each socket address/);
  await expect(page.getByTestId("ct-lan-web").getByTestId("ct-lan-link")).toHaveCount(0);
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

test("LAN section is translated in zh-TW", async ({ page, request }) => {
  await setLang(request, "zh-TW");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-lan-label")).toHaveText("區網存取");
  await expect(page.getByTestId("settings-lan-status")).toContainText("關閉");
});
