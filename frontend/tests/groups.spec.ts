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
