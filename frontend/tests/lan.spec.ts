import { test, expect, type APIRequestContext } from "@playwright/test";
import { open, reset, setLang } from "./helpers";

async function lanEnv(request: APIRequestContext, q: string): Promise<void> {
  expect((await request.post(`/__test/lan?${q}`)).ok()).toBeTruthy();
}

async function lanState(request: APIRequestContext): Promise<{ wslconfig: string; firewall_calls: boolean[] | null; shutdowns: number }> {
  const r = await request.get("/__test/lan");
  expect(r.ok()).toBeTruthy();
  return r.json();
}

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("network section shows LAN access off by default", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  const group = page.getByTestId("settings-lan-group");
  await expect(group).toBeVisible();
  await expect(page.getByTestId("settings-lan-label")).toHaveText("LAN access");
  const toggle = page.getByTestId("settings-lan");
  await expect(toggle).toHaveAttribute("role", "switch");
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  await expect(toggle).toBeEnabled();
  await expect(page.getByTestId("settings-lan-status")).toContainText("Off");
  await expect(page.getByTestId("settings-lan-addresses")).toHaveCount(0);
});

test("enabling LAN access confirms, applies, restarts WSL and shows addresses", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();

  const dialog = page.getByTestId("confirm-dialog");
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId("confirm-text")).toContainText("mirrored");
  await expect(page.getByTestId("confirm-text")).toContainText("127.0.0.1");
  await expect(page.getByTestId("confirm-text")).toContainText("ALL WSL distros restart");
  const ok = page.getByTestId("confirm-ok");
  await expect(ok).toHaveText("Apply");
  await expect(ok).not.toHaveClass(/danger/);
  await ok.click();

  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("settings-lan-status")).toContainText("On");
  await expect(page.getByTestId("settings-lan-addresses")).toContainText("http://192.168.1.20:<port>");
  await expect(page.getByTestId("notice")).toContainText("LAN access enabled");

  const st = await lanState(request);
  expect(st.wslconfig).toContain("networkingMode=mirrored");
  expect(st.firewall_calls).toEqual([true]);
  expect(st.shutdowns).toBe(1);
});

test("canceling the confirm dialog changes nothing", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await page.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "false");
  const st = await lanState(request);
  expect(st.wslconfig).toBe("");
  expect(st.firewall_calls ?? []).toEqual([]);
  expect(st.shutdowns).toBe(0);
});

test("delete dialogs keep their danger button after a LAN confirm", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await page.getByTestId("confirm-cancel").click();
  await page.getByTestId("nav-images").click();
  await page.getByTestId("btn-prune-images").click();
  await expect(page.getByTestId("confirm-ok")).toHaveClass(/danger/);
  await expect(page.getByTestId("confirm-ok")).toHaveText("Confirm");
});

test("UAC cancel shows the error and keeps LAN off", async ({ page, request }) => {
  await lanEnv(request, "fail=1");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("error-banner")).toContainText("canceled by the user");
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "false");
  expect((await lanState(request)).shutdowns).toBe(0);
});

test("public network and unsupported Windows build show warnings", async ({ page, request }) => {
  await lanEnv(request, "build=19045");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-lan")).toBeDisabled();
  await expect(page.getByTestId("settings-lan-warn")).toContainText("build 19045");

  await lanEnv(request, "build=26300&profile=Public");
  await page.getByTestId("nav-containers").click();
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("settings-lan-warn")).toContainText("Public");
});

test("disabling LAN access returns to off", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-lan").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "true");

  await page.getByTestId("settings-lan").click();
  await expect(page.getByTestId("confirm-text")).toContainText("Disable LAN access?");
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("settings-lan")).toHaveAttribute("aria-checked", "false");
  await expect(page.getByTestId("settings-lan-status")).toContainText("Off");
  const st = await lanState(request);
  expect(st.wslconfig).not.toContain("networkingMode");
  expect(st.firewall_calls).toEqual([true, false]);
  expect(st.shutdowns).toBe(2);
});

test("LAN section is translated in zh-TW", async ({ page, request }) => {
  await setLang(request, "zh-TW");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-lan-label")).toHaveText("區網存取");
  await expect(page.getByTestId("settings-lan-status")).toContainText("關閉");
});
