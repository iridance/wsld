import { test, expect } from "@playwright/test";
import { addComposeContainer, group, open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("containers without compose labels render flat (no group rows)", async ({ page }) => {
  await open(page);
  await expect(row(page, "web")).toBeVisible();
  await expect(page.getByTestId("group-row")).toHaveCount(0);
});

test("compose containers are grouped under a project header with service names and counts", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "web", "nginx:1.27");
  await addComposeContainer(request, "shop", "api", "node:22");
  await addComposeContainer(request, "shop", "worker", "python:3.12", "exited");
  await addComposeContainer(request, "blog", "db", "postgres:16");
  await open(page);

  // 專案標頭：字母序 blog、shop；未分組容器在最後的「Ungrouped」標頭下
  const headers = page.getByTestId("group-row");
  await expect(headers).toHaveCount(3);
  await expect(headers.nth(0)).toHaveAttribute("data-project", "blog");
  await expect(headers.nth(1)).toHaveAttribute("data-project", "shop");
  await expect(headers.nth(2)).toHaveAttribute("data-project", "");
  await expect(group(page, "shop").getByTestId("group-count")).toHaveText("2/3");
  // 未分組：只顯示「-」與計數，沒有整組 Start／Stop
  await expect(group(page, "").getByTestId("group-name")).toHaveText("-");
  await expect(group(page, "").getByTestId("group-count")).toHaveText("1/2");
  await expect(group(page, "").getByTestId("group-start")).toHaveCount(0);
  await expect(group(page, "").getByTestId("group-stop")).toHaveCount(0);
  await expect(group(page, "shop").getByTestId("group-stop")).toBeVisible();
  await expect(group(page, "shop").getByTestId("group-stop").locator("svg.ico")).toHaveAttribute("data-icon", "stop");
  await expect(group(page, "shop").getByTestId("group-start").locator("svg.ico")).toHaveAttribute("data-icon", "play");
  await expect(group(page, "shop").getByTestId("group-info").locator("svg.ico")).toHaveAttribute("data-icon", "info");
  await expect(group(page, "shop").getByTestId("group-start")).toHaveText("");
  await expect(group(page, "shop").getByTestId("group-stop")).toHaveText("");
  expect(await group(page, "shop").getByTestId("group-start").evaluate((e) => getComputedStyle(e).borderStyle)).toBe("none");

  // 組內以 service 名顯示，完整容器名保留在 data-name／title
  const api = row(page, "shop-api-1");
  await expect(api).toHaveAttribute("data-project", "shop");
  await expect(api.getByTestId("ct-name")).toHaveText("api");
  await expect(api.getByTestId("ct-name")).toHaveAttribute("title", /shop-api-1/);
  await expect(row(page, "web").getByTestId("ct-name")).toHaveText("web");

  // 群組列緊跟在各自標頭之後
  const order = await page.locator('[data-testid="group-row"], [data-testid="container-row"]').evaluateAll((els) =>
    els.map((e) => e.getAttribute("data-project") + ":" + (e.getAttribute("data-name") ?? "#")),
  );
  expect(order).toEqual(["blog:#", "blog:blog-db-1", "shop:#", "shop:shop-api-1", "shop:shop-web-1", "shop:shop-worker-1", ":#", ":db", ":web"]);
});

test("group header collapses its rows and remembers the choice across reload", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "web", "nginx:1.27");
  await addComposeContainer(request, "shop", "api", "node:22");
  await open(page);
  await expect(row(page, "shop-web-1")).toBeVisible();

  await group(page, "shop").getByTestId("group-toggle").click();
  await expect(group(page, "shop")).toHaveAttribute("aria-expanded", "false");
  await expect(row(page, "shop-web-1")).toHaveCount(0);
  await expect(row(page, "shop-api-1")).toHaveCount(0);
  await expect(row(page, "web")).toBeVisible(); // 其他組不受影響

  await page.reload();
  await expect(group(page, "shop")).toHaveAttribute("aria-expanded", "false");
  await expect(row(page, "shop-web-1")).toHaveCount(0);

  await group(page, "shop").getByTestId("group-toggle").click();
  await expect(row(page, "shop-web-1")).toBeVisible();
});

test("group Stop/Start act on every container of the project", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "web", "nginx:1.27");
  await addComposeContainer(request, "shop", "api", "node:22");
  await open(page);

  await group(page, "shop").getByTestId("group-stop").click();
  await expect(row(page, "shop-web-1")).toHaveAttribute("data-state", "exited");
  await expect(row(page, "shop-api-1")).toHaveAttribute("data-state", "exited");
  await expect(row(page, "web")).toHaveAttribute("data-state", "running"); // 未分組不受影響
  await expect(group(page, "shop").getByTestId("group-count")).toHaveText("0/2");
  await expect(group(page, "shop").getByTestId("group-stop")).toBeDisabled();

  await group(page, "shop").getByTestId("group-start").click();
  await expect(row(page, "shop-web-1")).toHaveAttribute("data-state", "running");
  await expect(row(page, "shop-api-1")).toHaveAttribute("data-state", "running");
  await expect(group(page, "shop").getByTestId("group-count")).toHaveText("2/2");
});

test("group Remove deletes every container of the project after confirm", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "web", "nginx:1.27");
  await addComposeContainer(request, "shop", "api", "node:22", "exited");
  await open(page);

  const rm = group(page, "shop").getByTestId("group-rm");
  await expect(rm.locator("svg.ico")).toHaveAttribute("data-icon", "trash");
  await expect(rm).toHaveAttribute("aria-label", "Remove");
  await expect(group(page, "").getByTestId("group-rm")).toHaveCount(0);

  // 搜尋只顯示 web 時仍移除整個專案
  await page.getByTestId("ct-search").fill("web");
  await rm.click();
  await expect(page.getByTestId("confirm-dialog")).toContainText("shop");
  await expect(page.getByTestId("confirm-dialog")).toContainText("2");
  await page.getByTestId("confirm-ok").click();
  await page.getByTestId("ct-search").fill("");
  await expect(group(page, "shop")).toHaveCount(0);
  await expect(row(page, "shop-web-1")).toHaveCount(0);
  await expect(row(page, "shop-api-1")).toHaveCount(0);
  await expect(row(page, "web")).toBeVisible(); // 未分組不受影響
});

test("compose group header uses dark-friendly colors under Theme Dark", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "web", "nginx:1.27");
  await open(page);
  await page.getByTestId("nav-settings").click();
  await page.getByTestId("settings-theme-dark").check();
  await page.getByTestId("settings-save").click();
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");

  await page.getByTestId("nav-containers").click();
  const cell = group(page, "shop").locator("td").first();
  // 刷新會重建列表：evaluate 前後元素可能被換掉（computed style 變空字串），等到讀到連線中的元素
  const read = () => cell.evaluate((e) => {
    if (!e.isConnected) return null;
    const cs = getComputedStyle(e);
    const parse = (c: string) => {
      const m = c.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)/);
      return m ? [Number(m[1]), Number(m[2]), Number(m[3])] : [255, 255, 255];
    };
    const [br, bg, bb] = parse(cs.backgroundColor);
    const [fr, fg, fb] = parse(cs.color);
    const lum = (r: number, g: number, b: number) => (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255;
    return { bgLum: lum(br, bg, bb), fgLum: lum(fr, fg, fb) };
  });
  let colors = await read();
  await expect.poll(async () => (colors = colors ?? (await read()))).not.toBeNull();
  colors = colors!;
  expect(colors.bgLum).toBeLessThan(0.35); // 不可再是淺灰底
  expect(colors.fgLum).toBeGreaterThan(0.6); // 文字要夠亮
});
