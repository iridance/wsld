import { test, expect, type Page } from "@playwright/test";
import { engine, listHold, listRelease, open, reset, waitListHeld } from "./helpers";

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

test("Images: prune list stays pruned even if a slower in-flight refresh returns stale data", async ({ page, request }) => {
  await go(page, "images");
  await expect(page.getByTestId("image-row")).toHaveCount(3);

  await listHold(request);
  // 不要 await refresh：ListImages 會被 hold 住；prune 後的刷新應 abort 它並立刻重抓
  await page.evaluate(() => {
    void (window as unknown as { __wsld: { refresh(): Promise<void> } }).__wsld.refresh();
  });
  await waitListHeld(request);

  await page.getByTestId("btn-prune-images").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("notice")).toContainText("Removed 2 item(s)");
  await expect(page.getByTestId("image-row")).toHaveCount(1);

  await listRelease(request);
  await page.waitForTimeout(200);
  await expect(page.getByTestId("image-row")).toHaveCount(1);
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

test("Machines: Info opens a right-side drawer with wsl.conf and usage", async ({ page }) => {
  await go(page, "machines");
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  const drawer = page.getByTestId("info-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute("data-kind", "machine");
  await expect(page.getByTestId("info-title")).toHaveText("Ubuntu-26.04");
  await expect(page.getByTestId("machine-config-systemd")).toContainText("systemd");
  await expect(page.getByTestId("machine-config-conf")).toContainText("[boot]");
  await expect(page.getByTestId("info-usage")).toContainText("CPU");
  await page.getByTestId("btn-info-close").click();
  await expect(drawer).toBeHidden();

  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await expect(page.getByTestId("machine-config-conf")).toContainText("(no /etc/wsl.conf)");
});

test("Machines: Add distro lists online distributions and installs the chosen one", async ({ page, request }) => {
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await expect(dlg).toBeVisible();
  const sel = dlg.getByTestId("add-machine-select");
  await expect(sel.locator("option")).toHaveCount(4);
  await expect(sel.locator("option").nth(0)).toHaveText(/Please select a distro|請選擇發行版/);
  await expect(sel.locator("option").nth(2)).toHaveText("Debian GNU/Linux (Debian)");
  await expect(dlg.getByTestId("add-machine-install")).toBeDisabled();
  await expect(dlg.getByTestId("add-machine-name")).toHaveValue("");

  await sel.selectOption("Debian");
  await expect(dlg.getByTestId("add-machine-name")).toHaveValue("Debian");
  await dlg.getByTestId("add-machine-name").fill("Debian1");
  await expect(dlg.getByTestId("add-machine-install")).toBeEnabled();
  await dlg.getByTestId("add-machine-install").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("Debian1 installed");
  await expect(page.getByTestId("notice")).toContainText("wsl -d Debian1");
  await expect(page.getByTestId("notice")).toContainText("Start engine");
  await expect(page.locator('[data-testid="machine-row"][data-name="Debian1"]')).toBeVisible();
  expect(await (await request.get("/__test/wsl/installed")).json()).toEqual(["Debian1"]);
});

test("Machines: Add distro disables Cancel while installing", async ({ page }) => {
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await dlg.getByTestId("add-machine-select").selectOption("Ubuntu");
  await expect(dlg.getByTestId("add-machine-name")).toHaveValue("Ubuntu");

  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/api/machines/install", async (route) => {
    await gate;
    await route.continue();
  });

  const done = dlg.getByTestId("add-machine-install").click();
  await expect(dlg.getByTestId("add-machine-cancel")).toBeDisabled();
  await expect(dlg.getByTestId("add-machine-install")).toBeDisabled();
  release();
  await done;
  await expect(dlg).toBeHidden();
});

test("Machines: Remove distro uses trash icon, requires typing the name, and unregisters the WSL distro", async ({ page }) => {
  await go(page, "machines");
  await expect(page.getByTestId("machine-row")).toHaveCount(2);

  const rm = page.getByTestId("btn-machine-remove-docker-desktop");
  await expect(rm).toHaveAttribute("aria-label", /Remove distro|刪除發行版/);
  await expect(rm.locator('[data-icon="trash"]')).toBeVisible();
  await rm.click();

  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId("confirm-text")).toContainText("permanent");
  await expect(dlg.getByTestId("confirm-ok")).toBeDisabled();
  await dlg.getByTestId("confirm-typed").fill("wrong");
  await expect(dlg.getByTestId("confirm-ok")).toBeDisabled();
  await dlg.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("machine-row")).toHaveCount(2);

  await rm.click();
  await dlg.getByTestId("confirm-typed").fill("docker-desktop");
  await expect(dlg.getByTestId("confirm-ok")).toBeEnabled();
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("machine-row")).toHaveCount(1);
  await expect(page.locator('[data-testid="machine-row"][data-name="docker-desktop"]')).toHaveCount(0);
  await expect(page.getByTestId("notice")).toContainText("docker-desktop");
});

test("Machines: Start engine shows Starting… pending on the active row until start finishes", async ({ page, request }) => {
  await go(page, "machines");
  await engine(request, "down");
  await expect(page.getByTestId("btn-start-engine")).toBeEnabled({ timeout: 10_000 });

  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/api/engine/start", async (route) => {
    await gate;
    await route.continue();
  });

  const click = page.getByTestId("btn-start-engine").click();
  const row = page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]');
  await expect(row).toHaveAttribute("data-pending", "starting");
  await expect(row.getByTestId("machine-state")).toHaveText("Starting…");
  await expect(page.getByTestId("btn-machine-term-Ubuntu-26.04")).toBeDisabled();
  await expect(page.getByTestId("btn-machine-config-Ubuntu-26.04")).toBeDisabled();
  await expect(page.getByTestId("btn-add-machine")).toBeDisabled();
  await expect(page.locator('[data-testid="machine-row"][data-name="docker-desktop"]')).not.toHaveAttribute("data-pending", /.+/);

  release();
  await click;
  await expect(row).not.toHaveAttribute("data-pending", /.+/);
  await expect(row.getByTestId("machine-state")).toHaveText("Running");
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
});

test("Machines: Set as current shows Switching… pending on the target row until use finishes", async ({ page }) => {
  await go(page, "machines");

  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/api/machines/*/use", async (route) => {
    await gate;
    await route.continue();
  });

  const click = page.getByTestId("btn-machine-use-docker-desktop").click();
  const row = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await expect(row).toHaveAttribute("data-pending", "switching");
  await expect(row.getByTestId("machine-state")).toHaveText("Switching…");
  await expect(page.getByTestId("btn-machine-use-docker-desktop")).toBeDisabled();

  release();
  await click;
  await expect(row).not.toHaveAttribute("data-pending", /.+/);
  await expect(row).toHaveAttribute("data-active", "1");
});

test("Machines: Remove distro shows Removing… pending on the row until unregister finishes", async ({ page }) => {
  await go(page, "machines");

  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/api/machines/*/unregister", async (route) => {
    await gate;
    await route.continue();
  });

  await page.getByTestId("btn-machine-remove-docker-desktop").click();
  await page.getByTestId("confirm-typed").fill("docker-desktop");
  const done = page.getByTestId("confirm-ok").click();

  const row = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await expect(row).toHaveAttribute("data-pending", "removing");
  await expect(row.getByTestId("machine-state")).toHaveText("Removing…");
  await expect(page.getByTestId("btn-machine-remove-docker-desktop")).toBeDisabled();
  await expect(page.getByTestId("btn-machine-term-docker-desktop")).toBeDisabled();
  await expect(page.getByTestId("btn-machine-config-docker-desktop")).toBeDisabled();
  await expect(page.getByTestId("btn-add-machine")).toBeDisabled();

  release();
  await done;
  await expect(row).toHaveCount(0);
  await expect(page.getByTestId("notice")).toContainText("docker-desktop");
});

test("Machines: a failed install keeps the dialog open and shows the error", async ({ page, request }) => {
  await request.post("/__test/wsl/fail-install");
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  await page.getByTestId("add-machine-select").selectOption("Ubuntu");
  await expect(page.getByTestId("add-machine-name")).toHaveValue("Ubuntu");
  await page.getByTestId("add-machine-install").click();
  await expect(page.getByTestId("add-machine-dialog")).toBeVisible();
  await expect(page.getByTestId("add-machine-error")).toContainText("Installation failed");
  await expect(page.getByTestId("add-machine-cancel")).toBeEnabled();
  await page.getByTestId("add-machine-cancel").click();
  await expect(page.getByTestId("add-machine-dialog")).toBeHidden();
});

test("Machines view works while the Docker engine is down", async ({ page, request }) => {
  await engine(request, "down");
  await go(page, "machines");
  await expect(page.getByTestId("machine-row")).toHaveCount(2);
});

test("Machines: Terminal opens bottom panel exec session like containers", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-term-Ubuntu-26.04").click();
  const panel = page.getByTestId("panel");
  await expect(panel).toBeVisible();
  await expect(panel).toHaveAttribute("data-kind", "term");
  await expect(page.getByTestId("panel-title")).toContainText("Ubuntu-26.04");
  await expect(page.locator(".xterm")).toBeVisible();

  const termText = () => page.evaluate(() => (window as unknown as { __wsld: { termText(): string } }).__wsld.termText());
  await expect.poll(termText).toContain("fake-shell$");

  await page.keyboard.type("pwd");
  await page.keyboard.press("Enter");
  await expect.poll(termText).toContain("you typed: pwd");

  await page.getByTestId("btn-panel-close").click();
  await expect(panel).toBeHidden();
});

test("Machines: Set as current shows header Loading/Switching until complete", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-machines").click();

  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/api/machines/*/use", async (route) => {
    await gate;
    await route.continue();
  });

  const click = page.getByTestId("btn-machine-use-docker-desktop").click();
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "switching");
  await expect(page.getByTestId("status-connection")).toHaveText("Switching…");
  await expect(page.getByTestId("header-loading")).toHaveText("Loading…");
  await expect(page.getByTestId("header-distro")).toHaveText("docker-desktop");
  await expect(page.getByTestId("btn-start-engine")).toBeDisabled();
  await expect(page.getByTestId("btn-open-folder")).toBeDisabled();
  await expect(page.getByTestId("btn-open-terminal")).toBeDisabled();

  release();
  await click;
  await expect(page.getByTestId("header-loading")).toHaveCount(0);
  await expect(page.getByTestId("status-connection")).not.toHaveAttribute("data-state", "switching");
  await expect(page.getByTestId("header-distro")).toHaveText("docker-desktop");
  await expect(page.getByTestId("notice")).toContainText("docker-desktop");
});

test("Machines: Set as current switches active and hard-cuts panels", async ({ page }) => {
  await open(page);
  // open a panel first
  await page.getByTestId("btn-logs-web").click();
  await expect(page.getByTestId("panel")).toBeVisible();
  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-use-docker-desktop").click();
  await expect(page.locator('[data-testid="machine-row"][data-name="docker-desktop"][data-active="1"]')).toBeVisible();
  await expect(page.getByTestId("notice")).toContainText("docker-desktop");
  await expect(page.getByTestId("panel")).toBeHidden();
  await expect(page.getByTestId("header-distro")).toHaveText("docker-desktop");
  await expect(page.getByTestId("error-banner")).toContainText("not initialized");
});

test("Machines: Start engine after switching distro auto-inits and clears not-ready banner", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-machines").click();
  await page.getByTestId("btn-machine-use-docker-desktop").click();
  await expect(page.getByTestId("error-banner")).toHaveAttribute("data-kind", "not-ready");
  await expect(page.getByTestId("btn-start-engine")).toBeEnabled();
  await page.getByTestId("btn-start-engine").click();
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "connected", { timeout: 15_000 });
  await expect(page.getByTestId("error-banner")).toBeHidden();
  await expect(page.getByTestId("header-distro")).toHaveText("docker-desktop");
  await expect(page.getByTestId("btn-start-engine")).toBeDisabled();
});
