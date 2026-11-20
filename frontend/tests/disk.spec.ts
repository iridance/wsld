import { test, expect, type Page } from "@playwright/test";
import { addComposeContainer, engine, group, open, reset } from "./helpers";

// 假資料（internal/gui/fake.go、fakewsl.go）：
//   容器可寫層 web 2 MB（running）、db 1 MB（exited）
//   映像 nginx:1.27 48 MB、redis:7 41 MB、<none>(ccc333ccc333) 9 MB
//   volume pgdata 120 MB、orphan 5 MB
//   VHDX：Ubuntu-26.04 8.5 GB、docker-desktop 1.2 GB；df / 6 GB／1 TB

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function go(page: Page, view: string) {
  await open(page);
  await page.getByTestId(`nav-${view}`).click();
  await expect(page.getByTestId(`view-${view}`)).toBeVisible();
}

/** 用量卡的圖例：依顯示順序回傳 [key, 大小文字, 占比]。 */
async function legend(page: Page): Promise<string[][]> {
  await expect(page.getByTestId("usage-card")).toBeVisible();
  return page.getByTestId("usage-part").evaluateAll((els) =>
    els.map((e) => [e.getAttribute("data-key") ?? "", e.querySelector("strong")?.textContent ?? "", e.querySelector(".muted")?.textContent ?? ""]),
  );
}

test("Images: usage card shows each image's size and share, with the total", async ({ page }) => {
  await go(page, "images");
  const card = page.getByTestId("usage-card");
  await expect(card).toBeVisible();
  await expect(card.getByTestId("usage-total")).toHaveText("93.5 MB");
  const parts = card.getByTestId("usage-part");
  await expect(parts).toHaveCount(3);
  // 依名稱排序，長條與圖例同一順序、依序取色槽
  await expect(parts.nth(0)).toContainText("ccc333ccc333");
  await expect(parts.nth(1)).toContainText("nginx:1.27");
  await expect(parts.nth(1)).toContainText("45.8 MB");
  await expect(parts.nth(1)).toContainText("49%");
  await expect(parts.nth(2)).toContainText("redis:7");
  const segs = card.getByTestId("usage-bar").locator(".disk-seg");
  await expect(segs).toHaveCount(3);
  await expect(segs.nth(0)).toHaveClass(/\bs1\b/);
  await expect(segs.nth(2)).toHaveClass(/\bs3\b/);
});

test("Images: more than five items keep the five largest and fold the rest into Other", async ({ page, request }) => {
  for (const [tag, size] of [["a:1", 5_000_000], ["b:1", 4_000_000], ["c:1", 3_000_000], ["big:1", 200_000_000]] as const) {
    expect((await request.post(`/__test/image?tag=${tag}&size=${size}&containers=0`)).ok()).toBeTruthy();
  }
  await go(page, "images");
  const card = page.getByTestId("usage-card");
  await expect(card.getByTestId("usage-part")).toHaveCount(6);
  const other = card.getByTestId("usage-part").nth(5);
  await expect(other).toHaveAttribute("data-key", "__other");
  await expect(other).toContainText("Other (2)");
  // 最小的兩個（b:1 4 MB、c:1 3 MB）被併入其他
  await expect(other).toHaveAttribute("data-size", "7000000");
  await expect(card.getByTestId("usage-bar").locator(".disk-seg.other")).toHaveCount(1);
});

test("Containers: usage card has one part per compose project and per standalone container", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "api", "node:22");
  await addComposeContainer(request, "shop", "worker", "node:22", "exited");
  await go(page, "containers");
  const card = page.getByTestId("usage-card");
  await expect(card.getByTestId("usage-total")).toHaveText("5.7 MB");
  await expect(card.locator('[data-testid="usage-part"][data-key="compose:shop"]')).toContainText("2.9 MB");
  await expect(card.getByTestId("usage-part")).toHaveCount(3); // shop、db、web
  await expect(group(page, "shop").getByTestId("group-size")).toHaveText("2.9 MB");
});

test("Containers: Size column shows the writable layer; subtitle shows the total", async ({ page }) => {
  await go(page, "containers");
  await expect(page.getByTestId("size-web")).toHaveAttribute("data-size", "2000000");
  await expect(page.getByTestId("size-web")).toHaveText("1.9 MB");
  await expect(page.getByTestId("size-web")).toHaveAttribute("title", /Writable layer 1\.9 MB · with image 47\.7 MB/);
  await expect(page.getByTestId("size-db")).toHaveText("976.6 KB");
  await expect(page.getByTestId("page-sub")).toHaveText("1 running · 1 stopped · 2.9 MB");
  const rows = await legend(page);
  expect(rows.map((r) => r.slice(1))).toEqual([
    ["976.6 KB", "33%"],
    ["1.9 MB", "67%"],
  ]);
});

test("Volumes: usage card, Size column and subtitle total; all update after removal", async ({ page }) => {
  await go(page, "volumes");
  const row = (n: string) => page.locator(`[data-testid="volume-row"][data-name="${n}"]`);
  await expect(row("pgdata").getByTestId("volume-size")).toHaveText("114.4 MB");
  await expect(row("orphan").getByTestId("volume-size")).toHaveText("4.8 MB");
  await expect(page.getByTestId("page-sub")).toHaveText("2 item(s) · 119.2 MB");
  expect(await legend(page)).toEqual([
    ["orphan", "4.8 MB", "4%"],
    ["pgdata", "114.4 MB", "96%"],
  ]);

  await page.getByTestId("btn-rm-volume-orphan").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("volume-row")).toHaveCount(1);
  await expect(page.getByTestId("page-sub")).toHaveText("1 item(s) · 114.4 MB");
  await expect(page.getByTestId("usage-part")).toHaveCount(1);
  await expect(page.getByTestId("usage-total")).toHaveText("114.4 MB");
});

test("Usage card is hidden on Networks and while the engine is down", async ({ page, request }) => {
  await go(page, "networks");
  await expect(page.getByTestId("usage-card")).toHaveCount(0);
  await engine(request, "down");
  await go(page, "volumes");
  await expect(page.getByTestId("engine-down")).toBeVisible();
  await expect(page.getByTestId("usage-card")).toHaveCount(0);
  await expect(page.getByTestId("volume-size")).toHaveCount(0);
});

test("Machines: usage card lists each machine's virtual disk; cards and subtitle show sizes", async ({ page }) => {
  await go(page, "machines");
  await expect(page.getByTestId("usage-total")).toHaveText("9.0 GB");
  expect(await legend(page)).toEqual([
    ["docker-desktop", "1.1 GB", "12%"],
    ["Ubuntu-26.04", "7.9 GB", "88%"],
  ]);
  const card = (n: string) => page.locator(`[data-testid="machine-row"][data-name="${n}"]`);
  await expect(card("Ubuntu-26.04").getByTestId("machine-disk")).toHaveText("7.9 GB");
  await expect(card("docker-desktop").getByTestId("machine-disk")).toHaveText("1.1 GB");
  await expect(page.getByTestId("page-sub")).toHaveText("2 item(s) · 9.0 GB total");
});

test("Machines Info: running distro shows filesystem usage bar and VHDX size; stopped shows only the VHDX size", async ({ page }) => {
  await go(page, "machines");
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  const info = page.getByTestId("info-drawer");
  await expect(info.getByTestId("info-disk-bar")).toBeVisible();
  await expect(info.getByTestId("info-disk-bar")).toHaveAttribute("data-pct", "0.6");
  await expect(info.getByTestId("info-disk-bar").locator(".usage-value")).toHaveText("5.6 GB / 931.3 GB (0.6%)");
  await expect(info.getByTestId("info-disk")).toContainText("7.9 GB");

  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await expect(info.getByTestId("info-title")).toHaveText("docker-desktop");
  await expect(info.getByTestId("info-disk-bar")).toHaveCount(0);
  await expect(info.getByTestId("info-disk")).toContainText("1.1 GB");
});
