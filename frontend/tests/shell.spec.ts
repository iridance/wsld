import { test, expect } from "@playwright/test";
import { open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("top bar lists the six views with large icons; Containers is active by default", async ({ page }) => {
  await open(page);
  const nav = page.getByTestId("header");
  await expect(nav).toBeVisible();
  const items: [string, string, string][] = [
    ["nav-containers", "containers", "Containers"],
    ["nav-images", "images", "Images"],
    ["nav-volumes", "volumes", "Volumes"],
    ["nav-networks", "networks", "Networks"],
    ["nav-machines", "machines", "Machines"],
    ["nav-settings", "settings", "Settings"],
  ];
  for (const [tid, ico, label] of items) {
    const b = nav.getByTestId(tid);
    await expect(b).toHaveText(label);
    await expect(b.locator("svg.ico")).toHaveAttribute("data-icon", ico);
    const box = await b.locator("svg.ico").boundingBox();
    expect(box!.width).toBeGreaterThanOrEqual(20); // 「大型 icon」
  }
  await expect(nav.getByTestId("nav-containers")).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("view-containers")).toBeVisible();
  const navBox = await nav.boundingBox();
  const viewBox = await page.getByTestId("view-containers").boundingBox();
  expect(navBox!.y + navBox!.height).toBeLessThanOrEqual(viewBox!.y); // 導覽在上
  expect(Math.round(viewBox!.x)).toBe(0); // 內容滿寬
  // 資源分頁在 top-nav，Settings 是右側齒輪（文字只給輔助技術）
  await expect(page.getByTestId("top-nav").getByTestId("nav-machines")).toBeVisible();
  await expect(page.getByTestId("top-actions").getByTestId("nav-settings").locator(".label")).toHaveClass(/sr-only/);
});

test("clicking a nav item switches the view and the active marker", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-images").click();
  await expect(page.getByTestId("nav-images")).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("nav-containers")).not.toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("view-images")).toBeVisible();
  await expect(page.getByTestId("view-containers")).toHaveCount(0);

  await page.getByTestId("nav-containers").click();
  await expect(page.getByTestId("view-containers")).toBeVisible();
  await expect(row(page, "web")).toBeVisible();
});

test("switching views closes an open logs panel", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-logs-web").click();
  await expect(page.getByTestId("panel")).toBeVisible();
  await page.getByTestId("nav-volumes").click();
  await expect(page.getByTestId("panel")).toHaveCount(0);
  await page.getByTestId("nav-containers").click();
  await expect(page.getByTestId("panel")).toBeHidden();
});

test("layout: top bar spans the full width; content uses the whole window width; no side nav or footer", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  const header = (await page.getByTestId("header").boundingBox())!;
  const view = (await page.getByTestId("view-containers").boundingBox())!;
  expect(Math.round(header.x)).toBe(0);
  expect(Math.round(header.y)).toBe(0);
  expect(Math.round(header.width)).toBe(1000);
  expect(Math.round(view.y)).toBe(Math.round(header.y + header.height));
  expect(Math.round(view.width)).toBe(1000);
  await expect(page.getByTestId("side-nav")).toHaveCount(0);
  await expect(page.getByTestId("status-bar")).toHaveCount(0);
  // 頁標題在內容區頂端，與工具列同一列
  await expect(page.getByTestId("page-title")).toHaveText("Containers");
  const title = (await page.getByTestId("page-title").boundingBox())!;
  const search = (await page.getByTestId("ct-search").boundingBox())!;
  expect(Math.abs(title.y + title.height / 2 - (search.y + search.height / 2))).toBeLessThan(12);
  await page.getByTestId("nav-images").click();
  await expect(page.getByTestId("page-title")).toHaveText("Images");
});

test("narrow window: tabs collapse to icons but keep their names", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  const tab = page.getByTestId("nav-images");
  await expect(tab.locator(".label")).toBeHidden();
  await expect(tab).toHaveAttribute("title", "Images");
  // 七個分頁加標籤約需 1400px；更窄就只剩圖示（否則導覽會橫向捲動，把前面的分頁捲出畫面）
  await page.setViewportSize({ width: 1500, height: 720 });
  await expect(tab.locator(".label")).toBeVisible();
  await page.setViewportSize({ width: 1280, height: 720 });
  await expect(tab.locator(".label")).toBeHidden();
});

test("top bar shows the brand mark and the active distro with engine state; clicking it opens Machines", async ({ page }) => {
  await open(page);
  const nav = page.getByTestId("header");
  await expect(nav.getByTestId("brand-mark")).toBeVisible();
  const sw = nav.getByTestId("distro-switch");
  await expect(sw.getByTestId("header-distro")).toHaveText("Ubuntu-26.04");
  await expect(sw.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
  await expect(sw.getByTestId("engine-dot")).toHaveClass(/\bok\b/);
  await sw.click();
  await expect(page.getByTestId("nav-machines")).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("view-machines")).toBeVisible();
});

test("language select lives in Settings and is drawn by wsld (no native arrow)", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("lang-select")).toHaveCount(0);
  await page.getByTestId("nav-settings").click();
  const sel = page.getByTestId("lang-select");
  await expect(sel).toBeVisible();
  await expect(sel).toHaveAttribute("role", "combobox");
  await expect(sel.locator("xpath=..").locator("svg.chev")).toBeVisible();
});
