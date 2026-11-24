import { test, expect, type Locator, type Page } from "@playwright/test";
import { engine, listHold, listRelease, open, reset, setLang, waitListHeld } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

/** Add machine 的發行版是自繪下拉：點開再點選項。 */
async function pickDistro(scope: Page | Locator, name: string) {
  await scope.getByTestId("add-machine-select").click();
  await scope.getByTestId(`add-machine-select-option-${name}`).click();
}

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

test("Networks: lists built-in and user networks; prune and remove unused; builtin cannot be removed", async ({ page, request }) => {
  await request.post("/__test/network?name=lab&driver=macvlan");
  await request.post("/__test/network?name=busy&driver=bridge&in_use=1");
  await go(page, "networks");
  await expect(page.getByTestId("network-row")).toHaveCount(6);
  const lab = page.locator('[data-testid="network-row"][data-name="lab"]');
  await expect(lab.getByTestId("network-driver")).toHaveText("macvlan");
  await expect(lab.getByTestId("network-scope")).toHaveText("local");
  await expect(page.locator('[data-testid="network-row"][data-name="bridge"]')).toHaveAttribute("data-builtin", "true");
  await expect(lab).toHaveAttribute("data-builtin", "false");

  await expect(page.getByTestId("btn-rm-network-bridge")).toBeDisabled();
  await expect(page.getByTestId("btn-rm-network-lab")).toBeEnabled();

  await page.getByTestId("btn-rm-network-busy").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("error-banner")).toBeVisible();
  await expect(page.locator('[data-testid="network-row"][data-name="busy"]')).toBeVisible();

  await page.getByTestId("btn-rm-network-lab").click();
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("notice")).toContainText("lab");
  await expect(page.locator('[data-testid="network-row"][data-name="lab"]')).toHaveCount(0);

  await page.getByTestId("btn-prune-networks").click();
  await expect(page.getByTestId("confirm-text")).toContainText("networks not used");
  await page.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("notice")).toContainText("Removed");
  // shop_default 未使用被清掉；busy 使用中保留；內建保留
  await expect(page.locator('[data-testid="network-row"][data-name="shop_default"]')).toHaveCount(0);
  await expect(page.locator('[data-testid="network-row"][data-name="busy"]')).toBeVisible();
  await expect(page.locator('[data-testid="network-row"][data-name="bridge"]')).toBeVisible();
});

// ---- Machines ----------------------------------------------------------------

test("Machines: lists local WSL distros with state, version and role badges", async ({ page }) => {
  await go(page, "machines");
  await expect(page.getByTestId("machine-row")).toHaveCount(2);
  const u = page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]');
  await expect(u.getByTestId("machine-state")).toHaveText("Running");
  await expect(u.getByTestId("machine-version")).toHaveText("2");
  await expect(u.getByTestId("machine-flag-engine")).toBeVisible();
  await expect(u.getByTestId("machine-flag-default")).toBeVisible();
  const dd = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await expect(dd.getByTestId("machine-state")).toHaveText("Stopped");
  await expect(dd.getByTestId("machine-flag-engine")).toHaveCount(0);
});

test("Machines: card icon is the distro logo, generic icon when unrecognised", async ({ page }) => {
  await go(page, "machines");
  const u = page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]');
  await expect(u.locator('.machine-icon svg[data-distro="ubuntu"]')).toBeVisible();
  const dd = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await expect(dd.locator('.machine-icon svg[data-icon="machines"]')).toBeVisible();
});

test("Machines: the engine is labelled Engine and nothing offers Set as primary", async ({ page }) => {
  await go(page, "machines");
  const u = page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]');
  await expect(u.getByTestId("machine-flag-engine")).toHaveText("Engine");
  await expect(page.locator('[data-testid^="btn-machine-use-"]')).toHaveCount(0);
});

test("Machines: zh-TW keeps the Engine label", async ({ page, request }) => {
  await setLang(request, "zh-TW");
  await go(page, "machines");
  const u = page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]');
  await expect(u.getByTestId("machine-flag-engine")).toHaveText("Engine");
  await expect(page.locator('[data-testid^="btn-machine-use-"]')).toHaveCount(0);
});

test("Machines: Info opens a right-side drawer with wsl.conf and usage bars", async ({ page, request }) => {
  await request.put("/api/settings", {
    data: {
      refresh_interval_sec: 1,
      exec_shell: "",
      tray_autostart: false,
      lang: "en",
      theme: "system",
    },
  });
  await go(page, "machines");
  let configCalls = 0;
  await page.route("**/api/machines/Ubuntu-26.04/config", async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    configCalls++;
    if (configCalls === 1) {
      await route.continue();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        name: "Ubuntu-26.04",
        wsl_conf: "[boot]\nsystemd=true\n\n[user]\ndefault=irice\n",
        systemd: true,
        managed: true,
        ready: true,
        cpu_percent: 42.5,
        memory_used_mb: 4096,
        memory_total_mb: 8192,
      }),
    });
  });

  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  const drawer = page.getByTestId("info-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer).toHaveAttribute("data-kind", "machine");
  await expect(page.getByTestId("info-title")).toHaveText("Ubuntu-26.04");
  await expect(page.getByTestId("machine-config-systemd")).toContainText("systemd");
  await expect(page.getByTestId("machine-config-conf")).toContainText("[boot]");
  const cpuBar = page.getByTestId("info-cpu-bar");
  const memBar = page.getByTestId("info-mem-bar");
  await expect(cpuBar).toBeVisible();
  await expect(memBar).toBeVisible();
  await expect(cpuBar.locator(".usage-value")).toContainText("%");
  await expect(memBar.locator(".usage-value")).toContainText("MB");
  await expect(cpuBar).toHaveAttribute("data-pct", "80");
  await expect(memBar).toHaveAttribute("data-pct", "25");

  await expect(cpuBar).toHaveAttribute("data-pct", "42.5", { timeout: 5000 });
  await expect(cpuBar.locator(".usage-value")).toHaveText("42.5%");
  await expect(memBar.locator(".usage-value")).toContainText("4096/8192");
  await expect(memBar).toHaveAttribute("data-pct", "50");

  await page.getByTestId("btn-info-close").click();
  await expect(drawer).toBeHidden();

  // 停止中：不讀 wsl.conf（wsl -d 會把機器開起來）
  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await expect(page.getByTestId("machine-config-unavailable")).toHaveText("Start the machine to view /etc/wsl.conf");
  await expect(page.getByTestId("machine-config-conf")).toHaveCount(0);
  await expect(page.getByTestId("info-cpu-bar")).toHaveAttribute("data-pct", "0");
});

test("Machines: Add machine lists online distributions and installs the chosen one", async ({ page, request }) => {
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await expect(dlg).toBeVisible();
  const sel = dlg.getByTestId("add-machine-select");
  await expect(sel).toHaveAttribute("role", "combobox");
  await expect(sel).toHaveText(/Please select a distro|請選擇發行版/);
  await expect(sel).toHaveAttribute("data-value", "");
  await expect(dlg.getByTestId("add-machine-install")).toBeDisabled();
  await expect(dlg.getByTestId("add-machine-name")).toHaveValue("");

  // 自繪清單：在對話框內開啟、列出線上發行版；Esc 只關清單不關對話框
  await sel.click();
  const opts = dlg.getByTestId("add-machine-select-list").getByRole("option");
  await expect(opts).toHaveCount(3);
  await expect(opts.nth(1)).toHaveText("Debian GNU/Linux (Debian)");
  await page.keyboard.press("Escape");
  await expect(dlg.getByTestId("add-machine-select-list")).toBeHidden();
  await expect(dlg).toBeVisible();

  await pickDistro(dlg, "Debian");
  await expect(sel).toHaveAttribute("data-value", "Debian");
  await expect(sel).toHaveText("Debian GNU/Linux (Debian)");
  await expect(dlg.getByTestId("add-machine-name")).toHaveValue("Debian");
  await dlg.getByTestId("add-machine-name").fill("Debian1");
  await expect(dlg.getByTestId("add-machine-user")).toHaveValue("irice");
  await dlg.getByTestId("add-machine-password").fill("s3cret pw");
  // 確認密碼不一致 → 顯示錯誤且不可安裝
  await dlg.getByTestId("add-machine-password2").fill("s3cret");
  await expect(dlg.getByTestId("add-machine-password-error")).toBeVisible();
  await expect(dlg.getByTestId("add-machine-install")).toBeDisabled();
  await dlg.getByTestId("add-machine-password2").fill("s3cret pw");
  await expect(dlg.getByTestId("add-machine-password-error")).toBeHidden();
  await expect(dlg.getByTestId("add-machine-install")).toBeEnabled();
  await dlg.getByTestId("add-machine-install").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("Debian1 installed");
  await expect(page.getByTestId("notice")).not.toContainText("wsl -d");
  await expect(page.getByTestId("notice")).not.toContainText("no password");
  await expect(page.getByTestId("notice")).toContainText("joined the shared engine");
  // 建立後直接加入共用 engine：卡片是 Client，不再需要按「共用 engine」
  await expect(page.locator('[data-testid="machine-row"][data-name="Debian1"]').getByTestId("machine-flag-client")).toBeVisible();
  await expect(page.getByTestId("btn-machine-role-Debian1")).toHaveCount(0);
  await expect(page.locator('[data-testid="machine-row"][data-name="Debian1"]')).toBeVisible();
  expect(await (await request.get("/__test/wsl/installed")).json()).toEqual(["Debian1"]);
  const acct = await (await request.get("/__test/wsl/account?distro=Debian1&user=irice")).json();
  expect(acct.password).toBe("s3cret pw");
  expect(acct.wsl_conf).toContain("default=irice");
});

test("Machines: Add machine validates the Linux user and allows an empty password", async ({ page, request }) => {
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await pickDistro(dlg, "Debian");
  await dlg.getByTestId("add-machine-name").fill("Debian5");
  await expect(dlg.getByTestId("add-machine-password")).toHaveAttribute("type", "password");
  await expect(dlg.getByTestId("add-machine-password-hint")).toBeVisible();

  for (const bad of ["Root", "root", "1abc", "a b"]) {
    await dlg.getByTestId("add-machine-user").fill(bad);
    await expect(dlg.getByTestId("add-machine-user-error")).toBeVisible();
    await expect(dlg.getByTestId("add-machine-install")).toBeDisabled();
  }
  await dlg.getByTestId("add-machine-user").fill("");
  await expect(dlg.getByTestId("add-machine-install")).toBeDisabled();
  await dlg.getByTestId("add-machine-user").fill("dev_1");
  await expect(dlg.getByTestId("add-machine-user-error")).toBeHidden();
  await expect(dlg.getByTestId("add-machine-install")).toBeEnabled();
  await dlg.getByTestId("add-machine-install").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("dev_1 has no password");
  const acct = await (await request.get("/__test/wsl/account?distro=Debian5&user=dev_1")).json();
  expect(acct.password_set).toBe(false);
  expect(acct.wsl_conf).toContain("default=dev_1");

  // 重開對話框：密碼欄已清空、帳號回到預設
  await page.getByTestId("btn-add-machine").click();
  await expect(dlg.getByTestId("add-machine-password")).toHaveValue("");
  await expect(dlg.getByTestId("add-machine-user")).toHaveValue("irice");
});

test("Machines: Add machine disables Cancel while installing", async ({ page }) => {
  await go(page, "machines");
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await pickDistro(dlg, "Ubuntu");
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
  // 安裝無法中止：開始後安裝按鈕直接隱藏
  await expect(dlg.getByTestId("add-machine-install")).toBeHidden();
  await expect(dlg.getByTestId("add-machine-select")).toBeDisabled();
  release();
  await done;
  await expect(dlg).toBeHidden();
});

test("Machines: Add machine shows download progress while installing", async ({ page }) => {
  // 約等於 1000×640 WebView 視窗扣掉標題列後的可視區
  await page.setViewportSize({ width: 1000, height: 600 });
  await go(page, "machines");
  await expect(page.getByTestId("btn-add-machine")).toContainText(/Add machine|新增機器/);
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  await expect(dlg.locator("h2")).toHaveText(/Add machine|新增機器/);
  await pickDistro(dlg, "Ubuntu");
  const progress = dlg.getByTestId("add-machine-progress");
  await expect(progress).toBeHidden();

  let snap: Record<string, unknown> = { active: true, distribution: "Ubuntu", name: "Ubuntu", phase: "preparing", downloaded: 0, total: 0, bytes_per_sec: 0, elapsed_sec: 3 };
  await page.route("**/api/machines/install/progress", (route) => route.fulfill({ json: snap }));
  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/api/machines/install", async (route) => {
    await gate;
    await route.continue();
  });

  const done = dlg.getByTestId("add-machine-install").click();
  await expect(progress).toBeVisible();
  await expect(progress).toHaveAttribute("data-phase", "preparing");
  await expect(progress).toHaveAttribute("data-indeterminate", "true");
  await expect(dlg.getByTestId("add-machine-progress-detail")).toContainText("0:03");

  // 已知總大小：百分比、已下載／總量、速度、經過時間
  snap = { ...snap, phase: "downloading", downloaded: 90 * 1024 * 1024, total: 180 * 1024 * 1024, bytes_per_sec: 240 * 1024, elapsed_sec: 75 };
  await expect(progress).toHaveAttribute("data-phase", "downloading");
  await expect(progress).toHaveAttribute("data-pct", "50");
  await expect(progress).not.toHaveAttribute("data-indeterminate", "true");
  const detail = dlg.getByTestId("add-machine-progress-detail");
  await expect(detail).toContainText("90.0 MB / 180.0 MB");
  await expect(detail).toContainText("240.0 KB/s");
  await expect(detail).toContainText("1:15");
  // 進度區塊出現時對話框不應出現捲軸：按鈕列與密碼提示讓出高度，取消改為右上角圖示
  await expect(dlg.getByTestId("add-machine-install")).toBeHidden();
  await expect(dlg.getByTestId("add-machine-password-hint")).toBeHidden();
  await expect(dlg.getByTestId("add-machine-cancel")).toBeDisabled();
  expect(await dlg.evaluate((d) => d.scrollHeight <= d.clientHeight)).toBe(true);

  // 總大小未知：不定進度，仍顯示已下載量
  snap = { ...snap, total: 0 };
  await expect(progress).toHaveAttribute("data-indeterminate", "true");
  await expect(detail).toContainText("90.0 MB");
  await expect(detail).not.toContainText("/ 180.0 MB");

  snap = { ...snap, phase: "installing", downloaded: 180 * 1024 * 1024, total: 180 * 1024 * 1024, elapsed_sec: 130 };
  await expect(progress).toHaveAttribute("data-phase", "installing");
  await expect(dlg.getByTestId("add-machine-progress-label")).toHaveText(/Registering|註冊/);

  release();
  await done;
  await expect(dlg).toBeHidden();
  // 下次開啟對話框時進度區塊應已重置
  await page.getByTestId("btn-add-machine").click();
  await expect(dlg.getByTestId("add-machine-progress")).toBeHidden();
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
  await pickDistro(page, "Ubuntu");
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

test("Machines: Start / Stop a non-primary distro toggles its state without a confirm", async ({ page }) => {
  await go(page, "machines");
  const row = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await expect(row).toHaveAttribute("data-state", "stopped");
  await expect(page.getByTestId("btn-machine-stop-docker-desktop")).toHaveCount(0);

  let release!: () => void;
  const gate = new Promise<void>((r) => {
    release = r;
  });
  await page.route("**/api/machines/docker-desktop/start", async (route) => {
    await gate;
    await route.continue();
  });
  const click = page.getByTestId("btn-machine-start-docker-desktop").click();
  await expect(row).toHaveAttribute("data-pending", "starting");
  await expect(row.getByTestId("machine-state")).toHaveText("Starting…");
  release();
  await click;
  await expect(row).toHaveAttribute("data-state", "running");
  await expect(page.getByTestId("btn-machine-start-docker-desktop")).toHaveCount(0);

  await page.getByTestId("btn-machine-stop-docker-desktop").click();
  await expect(page.getByTestId("confirm-dialog")).toBeHidden();
  await expect(row).toHaveAttribute("data-state", "stopped");
  // 非主要 distro 不影響引擎
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
});

test("Machines: a non-engine distro can be stopped and started without confirmation; the engine keeps running", async ({ page }) => {
  await go(page, "machines");
  const dd = page.locator('[data-testid="machine-row"][data-name="docker-desktop"]');
  await page.getByTestId("btn-machine-start-docker-desktop").click();
  await expect(dd).toHaveAttribute("data-state", "running");
  await page.getByTestId("btn-machine-stop-docker-desktop").click();
  await expect(dd).toHaveAttribute("data-state", "stopped");
  await expect(page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]')).toHaveAttribute("data-state", "running");
  await expect(page.getByTestId("status-connection")).toHaveAttribute("data-state", "connected");
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
