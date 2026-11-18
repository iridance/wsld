import { readFileSync } from "node:fs";
import { test, type Page } from "@playwright/test";
import { open, reset } from "./helpers";

// README 截圖（docs/images/*.png）；只在 `make readme-shots`（README_SHOTS=1）時執行，平常測試略過。
test.skip(!process.env.README_SHOTS, "README_SHOTS=1 才產生 README 截圖");
test.use({ viewport: { width: 1280, height: 900 }, deviceScaleFactor: 1 });

const out = "../docs/images";

/** 測試主機版本是 "testhost"；截圖改顯示 package.json 的版號。 */
async function showReleaseVersion(page: Page): Promise<void> {
  const { version } = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8")) as { version: string };
  await page.route("**/api/meta", async (route) => {
    const res = await route.fetch();
    const meta = await res.json();
    meta.version = version;
    await route.fulfill({ response: res, json: meta });
  });
}

test("README screenshots", async ({ page, request }) => {
  await reset(request);
  for (const [name, image, state, project, service] of [
    ["shop-api-1", "node:22-alpine", "running", "shop", "api"],
    ["shop-worker-1", "node:22-alpine", "exited", "shop", "worker"],
    ["cache", "redis:7", "running", "", ""],
  ]) {
    const q = new URLSearchParams({ name, image, state, ...(project ? { project, service } : {}) });
    await request.post(`/__test/container?${q}`);
  }
  await showReleaseVersion(page);
  await open(page);

  await page.getByTestId("btn-logs-web").click();
  await page.waitForTimeout(600);
  await page.mouse.move(0, 890);
  await page.screenshot({ path: `${out}/gui-containers.png` });
  await page.getByTestId("btn-panel-close").click();

  await page.setViewportSize({ width: 1280, height: 780 });
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  await page.waitForTimeout(1500);
  await page.mouse.move(0, 770);
  await page.screenshot({ path: `${out}/gui-machines-dark.png` });

  await page.evaluate(() => document.documentElement.removeAttribute("data-theme"));
  await page.getByTestId("nav-settings").click();
  await page.waitForTimeout(400);
  await page.screenshot({ path: `${out}/gui-settings.png` });
});
