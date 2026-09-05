import { test, expect, type Page } from "@playwright/test";
import { engine, open, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function go(page: Page, view: string) {
  await open(page);
  await page.getByTestId(`nav-${view}`).click();
  await expect(page.getByTestId(`view-${view}`)).toBeVisible();
}

// ---- Images ------------------------------------------------------------------

test("Images: lists repo:tag, short id, size, in-use count; <none> for dangling", async ({ page }) => {
  await go(page, "images");
  const rows = page.getByTestId("image-row");
  await expect(rows).toHaveCount(3);
  const nginx = page.locator('[data-testid="image-row"][data-ref="nginx:1.27"]');
  await expect(nginx.getByTestId("image-name")).toHaveText("nginx:1.27");
  await expect(nginx.getByTestId("image-id")).toHaveText("aaa111aaa111");
  await expect(nginx.getByTestId("image-size")).toHaveText("45.8 MB");
  await expect(nginx.getByTestId("image-inuse")).toHaveText("1");
  const dangling = page.locator('[data-testid="image-row"][data-ref="ccc333ccc333"]');
  await expect(dangling.getByTestId("image-name")).toHaveText("<none>");
});

test("Images: removing an unused image asks for confirmation and removes the row; in-use removal surfaces a conflict", async ({ page }) => {
  await go(page, "images");
  await page.getByTestId("btn-rm-image-bbb222bbb222").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId("confirm-text")).toContainText("redis:7");
  await dlg.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("image-row")).toHaveCount(3); // 取消不動作

  await page.getByTestId("btn-rm-image-bbb222bbb222").click();
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("image-row")).toHaveCount(2);
  await expect(page.getByTestId("notice")).toContainText("redis:7");

  await page.getByTestId("btn-rm-image-aaa111aaa111").click();
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("error-banner")).toBeVisible();
  await expect(page.getByTestId("error-banner")).toContainText("must force");
  await expect(page.getByTestId("image-row")).toHaveCount(2);
});

test("Images: Prune removes everything not used by a container and reports what was reclaimed", async ({ page }) => {
  await go(page, "images");
  await page.getByTestId("btn-prune-images").click();
  await expect(page.getByTestId("confirm-dialog").getByTestId("confirm-text")).toContainText("not used by any container");
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("image-row")).toHaveCount(1);
  await expect(page.getByTestId("notice")).toContainText("Removed 2 item(s)");
  await expect(page.getByTestId("notice")).toContainText("47.7 MB");
});

test("Images: engine down shows the error banner and an empty list", async ({ page, request }) => {
  await engine(request, "down");
  await go(page, "images");
  await expect(page.getByTestId("error-banner")).toBeVisible();
  await expect(page.getByTestId("image-row")).toHaveCount(0);
  await expect(page.getByTestId("btn-prune-images")).toBeDisabled();
});

// ---- Volumes -----------------------------------------------------------------

test("Volumes: lists name, driver, mountpoint; remove and prune with confirmation", async ({ page }) => {
  await go(page, "volumes");
  await expect(page.getByTestId("volume-row")).toHaveCount(2);
  const pg = page.locator('[data-testid="volume-row"][data-name="pgdata"]');
  await expect(pg.getByTestId("volume-driver")).toHaveText("local");
  await expect(pg.getByTestId("volume-mountpoint")).toHaveText("/var/lib/docker/volumes/pgdata/_data");

  // 使用中的 volume：後端 409 → banner
  await page.getByTestId("btn-rm-volume-pgdata").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("error-banner")).toContainText("in use");
  await expect(page.getByTestId("volume-row")).toHaveCount(2);

  await page.getByTestId("btn-rm-volume-orphan").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("volume-row")).toHaveCount(1);
  await expect(page.getByTestId("error-banner")).toBeHidden();

  await page.getByTestId("btn-prune-volumes").click();
  await expect(page.getByTestId("confirm-text")).toContainText("Data in them will be lost");
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("notice")).toContainText("Removed 0 item(s)");
  await expect(page.getByTestId("volume-row")).toHaveCount(1); // pgdata 使用中，保留
});

// ---- Networks ----------------------------------------------------------------

test("Networks: lists built-in and user networks with driver and scope, no destructive actions", async ({ page, request }) => {
  await request.post("/__test/network?name=lab&driver=macvlan");
  await go(page, "networks");
  await expect(page.getByTestId("network-row")).toHaveCount(5);
  const lab = page.locator('[data-testid="network-row"][data-name="lab"]');
  await expect(lab.getByTestId("network-driver")).toHaveText("macvlan");
  await expect(lab.getByTestId("network-scope")).toHaveText("local");
  await expect(page.locator('[data-testid="network-row"][data-name="bridge"]')).toHaveAttribute("data-builtin", "true");
  await expect(lab).toHaveAttribute("data-builtin", "false");
  await expect(page.getByTestId("view-networks").locator("button")).toHaveCount(0);
});

// ---- Machines ----------------------------------------------------------------

test("Machines: lists local WSL distros with state, version and role badges", async ({ page }) => {
  await go(page, "machines");
  await expect(page.getByTestId("machine-row")).toHaveCount(2);
  const u = page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]');
  await expect(u.getByTestId("machine-state")).toHaveText("Running");
  await expect(u.getByTestId("machine-version")).toHaveText("2");
  await expect(u.getByTestId("machine-flag-active")).toBeVisible();
  await expect(u.getByTestId("machine-flag-default")).toBeVisible();
  const dd = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await expect(dd.getByTestId("machine-state")).toHaveText("Stopped");
  await expect(dd.getByTestId("machine-flag-active")).toHaveCount(0);
});

test("Machines: the settings button shows /etc/wsl.conf and the systemd flag", async ({ page }) => {
  await go(page, "machines");
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  const dlg = page.getByTestId("machine-config-dialog");
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId("machine-config-name")).toHaveText("Ubuntu-26.04");
  await expect(dlg.getByTestId("machine-config-systemd")).toHaveAttribute("data-on", "true");
  await expect(dlg.getByTestId("machine-config-conf")).toContainText("[boot]");
  await expect(dlg.getByTestId("machine-config-conf")).toContainText("systemd=true");
  await dlg.getByTestId("machine-config-close").click();
  await expect(dlg).toBeHidden();

  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await expect(dlg.getByTestId("machine-config-systemd")).toHaveAttribute("data-on", "false");
  await expect(dlg.getByTestId("machine-config-conf")).toContainText("(no /etc/wsl.conf)");
});

test("Machines: Add distro lists online distributions and installs the chosen one", async ({ page, request }) => {
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await expect(dlg).toBeVisible();
  const sel = dlg.getByTestId("add-machine-select");
  await expect(sel.locator("option")).toHaveCount(3);
  await expect(sel.locator("option").nth(1)).toHaveText("Debian GNU/Linux (Debian)");
  await sel.selectOption("Debian");
  await dlg.getByTestId("add-machine-install").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("Debian installed");
  await expect(page.getByTestId("notice")).toContainText("wsl -d Debian");
  await expect(page.locator('[data-testid="machine-row"][data-name="Debian"]')).toBeVisible();
  expect(await (await request.get("/__test/wsl/installed")).json()).toEqual(["Debian"]);
});

test("Machines: a failed install keeps the dialog open and shows the error", async ({ page, request }) => {
  await request.post("/__test/wsl/fail-install");
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  await page.getByTestId("add-machine-select").selectOption("Ubuntu");
  await page.getByTestId("add-machine-install").click();
  await expect(page.getByTestId("add-machine-dialog")).toBeVisible();
  await expect(page.getByTestId("add-machine-error")).toContainText("Installation failed");
  await page.getByTestId("add-machine-cancel").click();
  await expect(page.getByTestId("add-machine-dialog")).toBeHidden();
});

test("Machines view works while the Docker engine is down", async ({ page, request }) => {
  await engine(request, "down");
  await go(page, "machines");
  await expect(page.getByTestId("machine-row")).toHaveCount(2);
});
