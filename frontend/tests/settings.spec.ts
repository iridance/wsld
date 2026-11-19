import { test, expect } from "@playwright/test";
import { open, reset, setLang } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("settings page shows radio theme and autostart toggle left-aligned", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("view-settings")).toBeVisible();
  await expect(page.getByTestId("settings-refresh")).toHaveValue("3");
  await expect(page.getByTestId("settings-exec-shell")).toHaveValue("");

  const auto = page.getByTestId("settings-autostart");
  await expect(auto).toHaveAttribute("role", "switch");
  await expect(auto).toHaveAttribute("aria-checked", "false");
  await expect(auto).toBeEnabled();
  await expect(page.getByTestId("settings-autostart-label")).toHaveText("Start at login");

  await expect(page.getByTestId("settings-theme-light")).toHaveAttribute("type", "radio");
  await expect(page.getByTestId("settings-theme-dark")).toHaveAttribute("type", "radio");
  await expect(page.getByTestId("settings-theme-system")).toBeChecked();

  const form = page.getByTestId("settings-form");
  const align = await form.evaluate((e) => getComputedStyle(e).alignItems || getComputedStyle(e).textAlign);
  // 表單靠左：不置中
  const box = await form.boundingBox();
  const list = await page.getByTestId("settings-list").boundingBox();
  expect(box).not.toBeNull();
  expect(list).not.toBeNull();
  expect(box!.x).toBeLessThanOrEqual(list!.x + 24);
  expect(align).not.toBe("center");
});

test("saving settings persists refresh interval, exec shell, autostart and theme", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-refresh").fill("5");
  await page.getByTestId("settings-exec-shell").fill("zsh");
  await page.getByTestId("settings-autostart").click();
  await expect(page.getByTestId("settings-autostart")).toHaveAttribute("aria-checked", "true");
  await page.getByTestId("settings-theme-dark").check();
  await page.getByTestId("settings-save").click();

  const saved = await (await request.get("/api/settings")).json();
  expect(saved).toEqual({
    refresh_interval_sec: 5,
    exec_shell: "zsh",
    tray_autostart: true,
    lang: "en",
    theme: "dark",
  });
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

  await page.reload();
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-refresh")).toHaveValue("5");
  await expect(page.getByTestId("settings-exec-shell")).toHaveValue("zsh");
  await expect(page.getByTestId("settings-autostart")).toHaveAttribute("aria-checked", "true");
  await expect(page.getByTestId("settings-theme-dark")).toBeChecked();
});

test("autostart label is 開機自啟動 in zh-TW", async ({ page, request }) => {
  await setLang(request, "zh-TW");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await expect(page.getByTestId("settings-autostart-label")).toHaveText("開機自啟動");
});

test("invalid refresh interval is rejected and shown in the banner", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-refresh").fill("0");
  await page.getByTestId("settings-save").click();
  await expect(page.getByTestId("view-settings")).toBeVisible();
  await expect(page.getByTestId("error-banner")).toContainText("refresh_interval_sec");
});

test("header has no Settings button or tcp path; folder/term icons have border", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("btn-settings")).toHaveCount(0);
  await expect(page.getByTestId("status-endpoint")).toHaveCount(0);
  const folder = page.getByTestId("btn-open-folder");
  expect(await folder.evaluate((e) => getComputedStyle(e).borderStyle)).not.toBe("none");
});

test("settings content width follows the window width", async ({ page }) => {
  for (const width of [900, 1600]) {
    await page.setViewportSize({ width, height: 800 });
    await open(page);
    await page.getByTestId("nav-settings").click();
    const form = (await page.getByTestId("settings-form").boundingBox())!;
    const list = (await page.getByTestId("settings-list").boundingBox())!;
    // 表單填滿設定區（不固定在 880px 等上限）
    expect(form.width).toBeGreaterThan(list.width - 4);
    expect(form.width).toBeGreaterThan(width - 120);
  }
});

test("auto refresh does not rebuild Settings: open dropdown and unsaved edits survive a poll", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-exec-shell").fill("zsh");
  await page.getByTestId("lang-select").click();
  await expect(page.getByTestId("lang-select-list")).toBeVisible();
  // 等兩次輪詢（/api/status）真的發生並完成
  for (let i = 0; i < 2; i++) {
    await page.waitForResponse((r) => r.url().endsWith("/api/status"), { timeout: 10_000 });
  }
  await page.waitForTimeout(200);
  await expect(page.getByTestId("lang-select-list")).toBeVisible();
  await expect(page.getByTestId("lang-select")).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("settings-exec-shell")).toHaveValue("zsh");
});
