import { test, expect } from "@playwright/test";
import { open, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("settings dialog shows current values with autostart off by default", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-settings").click();
  const dlg = page.getByTestId("settings-dialog");
  await expect(dlg).toBeVisible();
  await expect(page.getByTestId("settings-refresh")).toHaveValue("3");
  await expect(page.getByTestId("settings-exec-shell")).toHaveValue("");
  await expect(page.getByTestId("settings-autostart")).not.toBeChecked();
  await expect(page.getByTestId("settings-autostart")).toBeEnabled();
  await page.getByTestId("settings-close").click();
  await expect(dlg).toBeHidden();
});

test("saving settings persists refresh interval, exec shell and autostart", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("btn-settings").click();
  await page.getByTestId("settings-refresh").fill("5");
  await page.getByTestId("settings-exec-shell").fill("zsh");
  await page.getByTestId("settings-autostart").check();
  await page.getByTestId("settings-save").click();
  await expect(page.getByTestId("settings-dialog")).toBeHidden();

  const saved = await (await request.get("/api/settings")).json();
  expect(saved).toEqual({ refresh_interval_sec: 5, exec_shell: "zsh", tray_autostart: true, lang: "en" });

  await page.reload();
  await page.getByTestId("btn-settings").click();
  await expect(page.getByTestId("settings-refresh")).toHaveValue("5");
  await expect(page.getByTestId("settings-exec-shell")).toHaveValue("zsh");
  await expect(page.getByTestId("settings-autostart")).toBeChecked();
});

test("invalid refresh interval is rejected and shown in the banner", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-settings").click();
  await page.getByTestId("settings-refresh").fill("0");
  await page.getByTestId("settings-save").click();
  await expect(page.getByTestId("settings-dialog")).toBeVisible();
  await page.getByTestId("settings-close").click();
  await expect(page.getByTestId("error-banner")).toContainText("refresh_interval_sec");
});
