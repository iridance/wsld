import { test, expect, type Page, type Locator } from "@playwright/test";
import { engine, open, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function setTheme(page: Page, theme: "dark" | "light"): Promise<void> {
  await page.getByTestId("nav-settings").click();
  await page.getByTestId(`settings-theme-${theme}`).check();
  await page.getByTestId("settings-save").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
}

/** 回傳背景／前景相對亮度（0–1）。 */
async function luminances(loc: Locator): Promise<{ bg: number; fg: number }> {
  return loc.evaluate((e) => {
    const cs = getComputedStyle(e);
    const parse = (c: string) => {
      const rgb = c.match(/rgba?\(\s*([\d.]+)\s*,\s*([\d.]+)\s*,\s*([\d.]+)/);
      if (rgb) return [Number(rgb[1]), Number(rgb[2]), Number(rgb[3])];
      const modern = c.match(/color\(\s*srgb\s+([\d.]+)\s+([\d.]+)\s+([\d.]+)/i);
      if (modern) return [Number(modern[1]) * 255, Number(modern[2]) * 255, Number(modern[3]) * 255];
      // 強制經 canvas 解析（涵蓋 color-mix 等）
      const ctx = document.createElement("canvas").getContext("2d");
      if (!ctx) return [255, 255, 255];
      ctx.fillStyle = "#ffffff";
      ctx.fillStyle = c;
      const hex = String(ctx.fillStyle);
      const h = hex.match(/^#([0-9a-f]{6})$/i);
      if (h) {
        const n = parseInt(h[1], 16);
        return [(n >> 16) & 255, (n >> 8) & 255, n & 255];
      }
      return [255, 255, 255];
    };
    const lum = (r: number, g: number, b: number) => (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
    const [br, bg, bb] = parse(cs.backgroundColor);
    const [fr, fg, fb] = parse(cs.color);
    return { bg: lum(br, bg, bb), fg: lum(fr, fg, fb) };
  });
}

test("dark theme: shell surfaces (top bar, distro chip, settings) stay dark with light text", async ({ page }) => {
  await open(page);
  await setTheme(page, "dark");

  for (const id of ["header", "distro-switch", "view-settings"] as const) {
    const lum = await luminances(page.getByTestId(id));
    expect(lum.bg, id).toBeLessThan(0.35);
    expect(lum.fg, id).toBeGreaterThan(0.45);
  }
  const input = await luminances(page.getByTestId("settings-refresh"));
  expect(input.bg).toBeLessThan(0.35);
  expect(input.fg).toBeGreaterThan(0.55);
});

test("dark theme: error banner and notice use theme tokens (not light-only pastels)", async ({ page, request }) => {
  await open(page);
  await setTheme(page, "dark");
  await page.getByTestId("nav-containers").click();
  await engine(request, "down");
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "disconnected", { timeout: 10_000 });
  await expect(page.getByTestId("error-banner")).toBeVisible();
  const banner = await luminances(page.getByTestId("error-banner"));
  expect(banner.bg).toBeLessThan(0.45);
  expect(banner.fg).toBeGreaterThan(0.55);

  await engine(request, "up");
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "connected", { timeout: 10_000 });
  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-use-docker-desktop").click();
  await expect(page.getByTestId("notice")).toBeVisible();
  const notice = await luminances(page.getByTestId("notice"));
  expect(notice.bg).toBeLessThan(0.45);
  expect(notice.fg).toBeGreaterThan(0.55);
});

test("dark theme: dialog and danger icon hover are not light-washed", async ({ page }) => {
  await open(page);
  await setTheme(page, "dark");

  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await expect(dlg).toBeVisible();
  const dialog = await luminances(dlg);
  expect(dialog.bg).toBeLessThan(0.35);
  expect(dialog.fg).toBeGreaterThan(0.55);
  await page.getByTestId("add-machine-cancel").click();

  await page.getByTestId("nav-containers").click();
  const danger = page.getByTestId("btn-rm-web");
  await danger.hover();
  const hoverBg = await danger.evaluate((e) => {
    const c = getComputedStyle(e).backgroundColor;
    const m = c.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
    if (!m) return 1;
    const [r, g, b] = [Number(m[1]), Number(m[2]), Number(m[3])];
    return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
  });
  expect(hoverBg).toBeLessThan(0.45);
});

test("dark theme: info drawer conf and logs stay readable (dark terminal surface)", async ({ page }) => {
  await open(page);
  await setTheme(page, "dark");
  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  const conf = await luminances(page.getByTestId("machine-config-conf"));
  expect(conf.bg).toBeLessThan(0.25);
  expect(conf.fg).toBeGreaterThan(0.7);

  await page.getByTestId("btn-info-close").click();
  await page.getByTestId("nav-containers").click();
  await page.getByTestId("btn-logs-web").click();
  const logs = await luminances(page.locator("pre.logs"));
  expect(logs.bg).toBeLessThan(0.25);
  expect(logs.fg).toBeGreaterThan(0.7);
});

test("light theme: banner remains light pastel with dark text", async ({ page, request }) => {
  await open(page);
  await setTheme(page, "light");
  await page.getByTestId("nav-containers").click();
  await engine(request, "down");
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "disconnected", { timeout: 10_000 });
  await expect(page.getByTestId("error-banner")).toBeVisible();
  const banner = await luminances(page.getByTestId("error-banner"));
  expect(banner.bg).toBeGreaterThan(0.7);
  expect(banner.fg).toBeLessThan(0.4);
});
