import { test, expect } from "@playwright/test";
import { open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("side panel lists the five views with large icons; Containers is active by default", async ({ page }) => {
  await open(page);
  const nav = page.getByTestId("side-nav");
  await expect(nav).toBeVisible();
  const items: [string, string, string][] = [
    ["nav-containers", "containers", "Containers"],
    ["nav-images", "images", "Images"],
    ["nav-volumes", "volumes", "Volumes"],
    ["nav-networks", "networks", "Networks"],
    ["nav-machines", "machines", "Machines"],
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

test("layout: header on top, footer at the bottom, side nav full height between them", async ({ page }) => {
  await page.setViewportSize({ width: 1000, height: 640 });
  await open(page);
  const header = (await page.getByTestId("header").boundingBox())!;
  const footer = (await page.getByTestId("status-bar").boundingBox())!;
  const nav = (await page.getByTestId("side-nav").boundingBox())!;
  expect(Math.round(header.y)).toBe(0);
  expect(Math.round(footer.y + footer.height)).toBe(640);
  expect(Math.round(nav.y)).toBe(Math.round(header.y + header.height));
  expect(Math.round(nav.y + nav.height)).toBe(Math.round(footer.y));
  expect(nav.x).toBe(0);
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
