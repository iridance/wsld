import { test, expect } from "@playwright/test";
import { addComposeContainer, group, open, reset, row } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("container Info opens a right-side drawer with status and ports; columns are gone from the row", async ({ page }) => {
  await open(page);
  await expect(page.locator('th[data-col="status"]')).toHaveCount(0);
  await expect(page.locator('th[data-col="ports"]')).toHaveCount(0);
  await expect(row(page, "web").getByTestId("ports-web")).toHaveCount(0);

  const drawer = page.getByTestId("info-drawer");
  await expect(drawer).toBeHidden();

  await page.getByTestId("btn-info-web").click();
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute("data-kind", "container");
  await expect(drawer).toHaveAttribute("data-name", "web");
  await expect(page.getByTestId("info-title")).toContainText("web");
  await expect(page.getByTestId("info-status")).toContainText("running");
  await expect(page.getByTestId("info-ports")).toContainText("80");
  await expect(page.getByTestId("info-command")).toContainText("nginx");
  await expect(page.getByTestId("info-networks")).toContainText("bridge");
  await expect(page.getByTestId("info-mounts")).toContainText("/app");
  await expect(page.getByTestId("info-restart")).toHaveText("unless-stopped");

  // 從右側滑出：貼在列表右側、寬度約 340
  const box = await drawer.boundingBox();
  const list = await page.getByTestId("container-list").boundingBox();
  expect(box).not.toBeNull();
  expect(list).not.toBeNull();
  expect(box!.x).toBeGreaterThan(list!.x + list!.width / 2);
  expect(box!.width).toBeGreaterThan(280);
  expect(box!.width).toBeLessThanOrEqual(360);

  await page.getByTestId("btn-info-close").click();
  await expect(drawer).toBeHidden();
});

test("Remove running container asks for confirmation and deletes the row", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-rm-web").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("web");
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.locator('[data-testid="container-row"][data-name="web"]')).toHaveCount(0);
});

test("compose group actions are icon-only and Info opens project drawer", async ({ page, request }) => {
  await addComposeContainer(request, "shop", "web", "nginx:1.27");
  await addComposeContainer(request, "shop", "api", "node:22", "exited");
  await open(page);

  for (const [tid, icon, label] of [
    ["group-start", "play", "Start"],
    ["group-stop", "stop", "Stop"],
    ["group-info", "info", "Info"],
  ] as const) {
    const b = group(page, "shop").getByTestId(tid);
    await expect(b.locator("svg.ico")).toHaveAttribute("data-icon", icon);
    await expect(b).toHaveText("");
    await expect(b).toHaveAttribute("aria-label", label);
    expect(await b.evaluate((e) => getComputedStyle(e).borderStyle)).toBe("none");
  }

  await group(page, "shop").getByTestId("group-info").click();
  const drawer = page.getByTestId("info-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute("data-kind", "compose");
  await expect(drawer).toHaveAttribute("data-project", "shop");
  await expect(page.getByTestId("info-title")).toContainText("shop");
  await expect(page.getByTestId("info-count")).toHaveText("1/2");
});

test("header distro has open-folder and open-terminal icon buttons; refresh interval is not shown", async ({ page, request }) => {
  await open(page);
  await expect(page.getByTestId("status-refresh")).toHaveCount(0);

  const folder = page.getByTestId("btn-open-folder");
  const term = page.getByTestId("btn-open-terminal");
  await expect(folder).toHaveText("");
  await expect(term).toHaveText("");
  await expect(folder).toHaveAttribute("aria-label", "Open folder");
  await expect(term).toHaveAttribute("aria-label", "Open terminal");
  await expect(folder.locator("svg.ico")).toHaveAttribute("data-icon", "folder");
  await expect(term.locator("svg.ico")).toHaveAttribute("data-icon", "terminal");
  expect(await folder.evaluate((e) => getComputedStyle(e).borderStyle)).not.toBe("none");

  await folder.click();
  await term.click();
  const win = await (await request.get("/__test/window")).json();
  expect(win.actions).toEqual(expect.arrayContaining(["open-folder", "open-terminal"]));
});
