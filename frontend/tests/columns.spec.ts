import { test, expect, type Page } from "@playwright/test";
import { addContainer, open, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function colWidth(page: Page, col: string): Promise<number> {
  const th = page.locator(`th[data-col="${col}"]`);
  await th.waitFor({ state: "visible" }); // 第一次刷新完成前列表是「沒有容器」佔位
  const box = await th.boundingBox();
  expect(box).not.toBeNull();
  return box!.width;
}

test("column widths do not change when content changes", async ({ page, request }) => {
  await page.setViewportSize({ width: 1100, height: 640 });
  await open(page);
  const before = { name: await colWidth(page, "name"), image: await colWidth(page, "image"), status: await colWidth(page, "status") };

  // 塞一個超長名稱／映像的容器，等它出現在列表
  const longName = "a-very-very-very-very-long-container-name-that-would-widen-the-column";
  await addContainer(request, longName, "registry.example.internal:5000/team/some/really/long/image/path:2026.09.06-rc1");
  await page.getByTestId("btn-refresh").click();
  await expect(page.locator(`[data-testid="container-row"][data-name="${longName}"]`)).toBeVisible();

  expect(await colWidth(page, "name")).toBeCloseTo(before.name, 0);
  expect(await colWidth(page, "image")).toBeCloseTo(before.image, 0);
  expect(await colWidth(page, "status")).toBeCloseTo(before.status, 0);

  // 內容以省略號截斷而不是撐開欄位
  const cell = page.locator(`[data-testid="container-row"][data-name="${longName}"] [data-testid="ct-name"]`);
  const overflow = await cell.evaluate((e) => getComputedStyle(e).textOverflow);
  expect(overflow).toBe("ellipsis");
});

test("columns can be resized by dragging the header handle and the width persists", async ({ page }) => {
  await page.setViewportSize({ width: 1100, height: 640 });
  await open(page);
  const before = await colWidth(page, "name");

  const handle = page.locator('th[data-col="name"] [data-testid="col-resizer"]');
  const hb = (await handle.boundingBox())!;
  await page.mouse.move(hb.x + hb.width / 2, hb.y + hb.height / 2);
  await page.mouse.down();
  await page.mouse.move(hb.x + hb.width / 2 + 80, hb.y + hb.height / 2, { steps: 8 });
  await page.mouse.up();

  const after = await colWidth(page, "name");
  expect(after - before).toBeGreaterThan(70);
  expect(after - before).toBeLessThan(90);

  // 刷新（重繪列表）與重新載入頁面都要保留
  await page.getByTestId("btn-refresh").click();
  await expect.poll(() => colWidth(page, "name")).toBeCloseTo(after, 0);
  await page.reload();
  await expect(page.getByTestId("status-connection")).toBeVisible();
  expect(await colWidth(page, "name")).toBeCloseTo(after, 0);
});
