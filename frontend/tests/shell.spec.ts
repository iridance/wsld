import { test, expect } from "@playwright/test";
import { open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("side panel lists the six views with large icons; Containers is active by default", async ({ page }) => {
  await open(page);
  const nav = page.getByTestId("side-nav");
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
  expect(navBox!.x).toBeLessThan(viewBox!.x); // 側欄在左
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

test("layout: side nav spans the full height on the left; header sits on top of the content column; no footer", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  const header = (await page.getByTestId("header").boundingBox())!;
  const nav = (await page.getByTestId("side-nav").boundingBox())!;
  expect(nav.x).toBe(0);
  expect(Math.round(nav.y)).toBe(0);
  expect(Math.round(nav.height)).toBe(640);
  expect(Math.round(header.y)).toBe(0);
  expect(Math.round(header.x)).toBe(Math.round(nav.x + nav.width));
  await expect(page.getByTestId("status-bar")).toHaveCount(0);
  // 頁首顯示目前視圖名稱
  await expect(page.getByTestId("page-title")).toHaveText("Containers");
  await page.getByTestId("nav-images").click();
  await expect(page.getByTestId("page-title")).toHaveText("Images");
});

test("side nav top shows the brand mark and the active distro with engine state; clicking it opens Machines", async ({ page }) => {
  await open(page);
  const nav = page.getByTestId("side-nav");
  await expect(nav.getByTestId("brand-mark")).toBeVisible();
  const sw = nav.getByTestId("distro-switch");
  await expect(sw.getByTestId("header-distro")).toHaveText("Ubuntu-26.04");
  await expect(sw.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
  await expect(sw.getByTestId("engine-dot")).toHaveClass(/\bok\b/);
  await sw.click();
  await expect(page.getByTestId("nav-machines")).toHaveAttribute("aria-current", "page");
  await expect(page.getByTestId("view-machines")).toBeVisible();
});

test("language dropdown has no border but still looks clickable", async ({ page }) => {
  await open(page);
  const sel = page.getByTestId("lang-select");
  const style = await sel.evaluate((e) => {
    const cs = getComputedStyle(e);
    return { border: cs.borderStyle, cursor: cs.cursor, bg: cs.backgroundColor };
  });
  expect(style.border).toBe("none");
  expect(style.cursor).toBe("pointer");
  expect(style.bg).toMatch(/rgba\(0, 0, 0, 0\)|transparent/);
});
