import { test, expect, type Locator } from "@playwright/test";
import { open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

const appearance = (loc: Locator) => loc.evaluate((e) => getComputedStyle(e).appearance);

test("checkbox, radio, select and number are drawn by wsld, not the browser", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-logs-web").click();
  const live = page.getByTestId("logs-live-toggle");
  expect(await appearance(live)).toBe("none");
  // 勾選後填入強調色、勾由 ::after 畫出
  await live.check();
  const checked = await live.evaluate((e) => ({ bg: getComputedStyle(e).backgroundColor, after: getComputedStyle(e, "::after").content }));
  expect(checked.bg).not.toMatch(/rgba\(0, 0, 0, 0\)|transparent/);
  expect(checked.after).toBe('""');

  await page.getByTestId("nav-settings").click();
  expect(await appearance(page.getByTestId("settings-theme-system"))).toBe("none");
  // 語言下拉是自繪 combobox（不是原生 <select>）
  await expect(page.getByTestId("lang-select")).toHaveAttribute("role", "combobox");
  await expect(page.getByTestId("view-settings").locator("select")).toHaveCount(0);
  expect(await appearance(page.getByTestId("settings-refresh"))).toBe("textfield");
  // 文字欄位外框在 .tf，內層 input 無框
  const shell = page.getByTestId("settings-exec-shell");
  expect(await shell.evaluate((e) => getComputedStyle(e).borderTopStyle)).toBe("none");
  expect(await shell.evaluate((e) => e.parentElement!.classList.contains("tf"))).toBe(true);
});

test("number field: − / + step the value, − disables at the minimum, out of range marks the field", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  const input = page.getByTestId("settings-refresh");
  const field = input.locator("xpath=..");
  const dec = page.getByTestId("settings-refresh-dec");
  const inc = page.getByTestId("settings-refresh-inc");
  await expect(input).toHaveValue("3");
  await inc.click();
  await expect(input).toHaveValue("4");
  await input.fill("2");
  await dec.click();
  await expect(input).toHaveValue("1");
  await expect(dec).toBeDisabled();
  await input.fill("0");
  await expect(field).toHaveClass(/\bis-error\b/);
  await input.fill("5");
  await expect(field).not.toHaveClass(/\bis-error\b/);
  await expect(dec).toBeEnabled();
});

test("search: clear button appears with text and resets the list; Esc clears too", async ({ page }) => {
  await open(page);
  const search = page.getByTestId("ct-search");
  const clear = page.getByTestId("ct-search-clear");
  await expect(clear).toBeHidden();
  await search.fill("nginx");
  await expect(clear).toBeVisible();
  await expect(row(page, "db")).toHaveCount(0);
  await clear.click();
  await expect(search).toHaveValue("");
  await expect(clear).toBeHidden();
  await expect(row(page, "db")).toBeVisible();
  await search.fill("nginx");
  await search.press("Escape");
  await expect(search).toHaveValue("");
});

test("autostart switch shows its state as text next to it", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  const sw = page.getByTestId("settings-autostart");
  const state = page.getByTestId("settings-autostart-state");
  await expect(sw).toHaveAttribute("aria-checked", "false");
  await expect(state).toHaveText("Off");
  await sw.click();
  await expect(state).toHaveText("On");
});

test("typed confirmation shows a check only on an exact match", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-remove-docker-desktop").click();
  const typed = page.getByTestId("confirm-typed");
  const match = page.getByTestId("confirm-match");
  await expect(page.getByTestId("confirm-typed-field")).toBeVisible();
  await typed.fill("docker-deskto");
  await expect(match).toBeHidden();
  await expect(page.getByTestId("confirm-ok")).toBeDisabled();
  await typed.fill("docker-desktop");
  await expect(match).toBeVisible();
  await expect(page.getByTestId("confirm-ok")).toBeEnabled();
  await page.getByTestId("confirm-cancel").click();

  // 不需打字的確認不顯示欄位
  await page.getByTestId("nav-images").click();
  await page.getByTestId("btn-rm-image-bbb222bbb222").click();
  await expect(page.getByTestId("confirm-typed-field")).toBeHidden();
  await page.getByTestId("confirm-cancel").click();
});

test("dropdown: the open list is drawn by wsld and works with mouse and keyboard", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-settings").click();
  const dd = page.getByTestId("lang-select");
  const list = page.getByTestId("lang-select-list");
  await expect(dd).toHaveAttribute("data-value", "en");
  await expect(dd).toHaveText("English");
  await expect(list).toBeHidden();

  // 滑鼠：開啟→清單在欄位下方、與欄位同寬以上；目前值有勾；點外面關閉
  await dd.click();
  await expect(dd).toHaveAttribute("aria-expanded", "true");
  await expect(list).toBeVisible();
  await expect(list).toHaveAttribute("role", "listbox");
  await expect(page.getByTestId("lang-select-option-en")).toHaveAttribute("aria-selected", "true");
  const f = (await dd.locator("xpath=..").boundingBox())!;
  const l = (await list.boundingBox())!;
  expect(l.y).toBeGreaterThanOrEqual(f.y + f.height);
  expect(l.width).toBeGreaterThanOrEqual(f.width - 1);
  await page.getByTestId("settings-form").locator("h2").first().click();
  await expect(list).toBeHidden();
  await expect(dd).toHaveAttribute("aria-expanded", "false");

  // 鍵盤：Esc 關閉不改值；↓ 移動、Enter 選取
  await dd.focus();
  await page.keyboard.press("ArrowDown");
  await expect(list).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(list).toBeHidden();
  await expect(dd).toHaveAttribute("data-value", "en");
  await page.keyboard.press("Enter");
  await page.keyboard.press("ArrowDown");
  await expect(page.getByTestId("lang-select-option-zh-TW")).toHaveClass(/active/);
  await page.keyboard.press("Enter");
  await expect(page.getByTestId("btn-start-engine")).toHaveText("啟動引擎");
  await expect(page.getByTestId("lang-select")).toHaveAttribute("data-value", "zh-TW");
});
