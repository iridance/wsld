import { test, expect, type Locator, type Page } from "@playwright/test";
import { open, reset } from "./helpers";

// 假資料（internal/gui/fakewsl.go）：所有 distro 預設磁碟上限 1 TB；
// Ubuntu-26.04 執行中（主要）、docker-desktop 已停止。

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function machines(page: Page) {
  await open(page);
  await page.getByTestId("nav-machines").click();
  await expect(page.getByTestId("view-machines")).toBeVisible();
}

async function pickDistro(scope: Locator, name: string) {
  await scope.getByTestId("add-machine-select").click();
  await scope.getByTestId(`add-machine-select-option-${name}`).click();
}

const card = (page: Page, name: string) => page.locator(`[data-testid="machine-row"][data-name="${name}"]`);

test("Add machine: disk limit defaults to 60 GB and is passed to wsl --install", async ({ page, request }) => {
  await machines(page);
  await page.getByTestId("btn-add-machine").click();
  const dlg = page.getByTestId("add-machine-dialog");
  const size = dlg.getByTestId("add-machine-vhd-size");
  await expect(size).toHaveValue("60");
  await expect(dlg.getByTestId("add-machine-vhd-hint")).toContainText("1 TB");
  await pickDistro(dlg, "Debian");
  await dlg.getByTestId("add-machine-name").fill("small");

  // 超出範圍 → 顯示錯誤且不能安裝
  for (const bad of ["4", "99999", "abc", "12.5"]) {
    await size.fill(bad);
    await expect(dlg.getByTestId("add-machine-vhd-error")).toBeVisible();
    await expect(size).toHaveAttribute("aria-invalid", "true");
    await expect(dlg.getByTestId("add-machine-install")).toBeDisabled();
  }
  await size.fill("60");
  await expect(dlg.getByTestId("add-machine-vhd-error")).toBeHidden();
  await expect(dlg.getByTestId("add-machine-install")).toBeEnabled();
  await dlg.getByTestId("add-machine-install").click();
  await expect(dlg).toBeHidden();
  expect((await (await request.get("/__test/wsl/last-install")).json()).args).toContain("--vhd-size 60GB");
  await expect(card(page, "small").getByTestId("machine-disk-limit")).toHaveText("60.0 GB");
  await expect(card(page, "Ubuntu-26.04").getByTestId("machine-disk-limit")).toHaveText("1.0 TB");

  // 清空 = WSL 預設（不帶 --vhd-size）
  await page.getByTestId("btn-add-machine").click();
  await expect(size).toHaveValue("60");
  await pickDistro(dlg, "Debian");
  await dlg.getByTestId("add-machine-name").fill("big");
  await size.fill("");
  await expect(dlg.getByTestId("add-machine-install")).toBeEnabled();
  await dlg.getByTestId("add-machine-install").click();
  await expect(dlg).toBeHidden();
  expect((await (await request.get("/__test/wsl/last-install")).json()).args).not.toContain("--vhd-size");
  await expect(card(page, "big").getByTestId("machine-disk-limit")).toHaveText("1.0 TB");
});

test("Machine info: resizing the running primary machine asks to shut WSL down first", async ({ page, request }) => {
  await machines(page);
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  await expect(page.getByTestId("info-disk-limit")).toHaveText("1.0 TB");
  await page.getByTestId("btn-machine-resize").click();
  const dlg = page.getByTestId("resize-dialog");
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId("resize-title")).toContainText("Ubuntu-26.04");
  const size = dlg.getByTestId("resize-size");
  await expect(size).toHaveValue("1024");
  // 執行中：一開始就說明要關閉 WSL，列出會被停止的機器
  await expect(dlg.getByTestId("resize-running")).toBeVisible();
  await expect(dlg.getByTestId("resize-shutdown")).toBeVisible();
  await expect(dlg.getByTestId("resize-will-stop")).toHaveText("Will be stopped and started again: Ubuntu-26.04");
  await expect(dlg.getByTestId("resize-apply")).toHaveText("Shut down WSL and apply");
  await expect(dlg.getByTestId("resize-shrink")).toBeHidden();
  // 沒變 → 不能套用
  await expect(dlg.getByTestId("resize-apply")).toBeDisabled();
  await size.fill("2");
  await expect(dlg.getByTestId("resize-error")).toBeVisible();
  await expect(dlg.getByTestId("resize-apply")).toBeDisabled();
  await size.fill("60");
  await expect(dlg.getByTestId("resize-error")).toBeHidden();
  await expect(dlg.getByTestId("resize-shrink")).toBeVisible();
  await expect(dlg.getByTestId("resize-apply")).toBeEnabled();
  await dlg.getByTestId("resize-apply").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("Ubuntu-26.04 disk limit set to 60 GB");
  await expect(page.getByTestId("info-disk-limit")).toHaveText("60.0 GB");
  await expect(card(page, "Ubuntu-26.04")).toHaveAttribute("data-state", "running");
  await expect(card(page, "Ubuntu-26.04").getByTestId("machine-disk-limit")).toHaveText("60.0 GB");

  // wsld-gui 從 WSL 內執行：拒絕關閉 WSL，說明原因
  await request.post("/__test/wsl/self-in-wsl?on=1");
  await page.getByTestId("btn-machine-resize").click();
  await size.fill("80");
  await dlg.getByTestId("resize-apply").click();
  await expect(dlg.getByTestId("resize-error")).toContainText("running from inside WSL");
  await expect(dlg).toBeVisible();
  await expect(card(page, "Ubuntu-26.04")).toHaveAttribute("data-state", "running");
});

test("Machine info: a stopped machine whose disk WSL still holds needs a second, confirmed click", async ({ page }) => {
  await machines(page);
  // 啟動後再停止：WSL 仍掛著它的 VHD
  await page.getByTestId("btn-machine-start-docker-desktop").click();
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "running");
  await page.getByTestId("btn-machine-stop-docker-desktop").click();
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "stopped");

  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await page.getByTestId("btn-machine-resize").click();
  const dlg = page.getByTestId("resize-dialog");
  await expect(dlg.getByTestId("resize-running")).toBeHidden();
  await expect(dlg.getByTestId("resize-shutdown")).toBeHidden();
  await expect(dlg.getByTestId("resize-apply")).toHaveText("Apply");
  await dlg.getByTestId("resize-size").fill("70");
  await dlg.getByTestId("resize-apply").click();
  // 後端回 needs_shutdown：對話框留著，改成要求確認
  await expect(dlg.getByTestId("resize-shutdown")).toBeVisible();
  await expect(dlg.getByTestId("resize-will-stop")).toHaveText("Will be stopped and started again: Ubuntu-26.04");
  await expect(dlg.getByTestId("resize-apply")).toHaveText("Shut down WSL and apply");
  await expect(dlg.getByTestId("resize-error")).toBeHidden();
  await expect(page.getByTestId("info-disk-limit")).toHaveText("1.0 TB");
  await dlg.getByTestId("resize-apply").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("info-disk-limit")).toHaveText("70.0 GB");
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "stopped");
  await expect(card(page, "Ubuntu-26.04")).toHaveAttribute("data-state", "running");
});

test("Machine info: resizing a stopped machine shows no restart warning and reports wsl errors", async ({ page, request }) => {
  await machines(page);
  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await page.getByTestId("btn-machine-resize").click();
  const dlg = page.getByTestId("resize-dialog");
  await expect(dlg.getByTestId("resize-running")).toBeHidden();
  await expect(dlg.getByTestId("resize-shutdown")).toBeHidden();
  const size = dlg.getByTestId("resize-size");

  await request.post("/__test/wsl/fail-resize?on=1");
  await size.fill("8");
  await dlg.getByTestId("resize-apply").click();
  await expect(dlg.getByTestId("resize-error")).toContainText("too small");
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId("resize-apply")).toBeEnabled();

  await request.post("/__test/wsl/fail-resize?on=0");
  await size.fill("2048");
  await expect(dlg.getByTestId("resize-shrink")).toBeHidden();
  await dlg.getByTestId("resize-apply").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("info-disk-limit")).toHaveText("2.0 TB");
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "stopped");

  // 取消：不送出
  await page.getByTestId("btn-machine-resize").click();
  await size.fill("100");
  await dlg.getByTestId("resize-cancel").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("info-disk-limit")).toHaveText("2.0 TB");
});

test("Resize: the filesystem minimum is shown and enforced before WSL is shut down", async ({ page, request }) => {
  // 實機數字：1 TB ext4、約 30 GB 資料 → resize2fs 最小 18390839 區塊 ≈ 70.2 GiB → 建議最小 72 GB
  await request.post("/__test/wsl/fs-min?distro=Ubuntu-26.04&blocks=18390839");
  await request.post("/__test/wsl/fs-min?distro=docker-desktop&blocks=18390839");
  await machines(page);
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  await page.getByTestId("btn-machine-resize").click();
  const dlg = page.getByTestId("resize-dialog");
  const size = dlg.getByTestId("resize-size");
  await expect(dlg.getByTestId("resize-min")).toHaveText("Smallest possible for this machine: about 72 GB");
  await size.fill("60");
  await expect(dlg.getByTestId("resize-error")).toContainText("at least about 72 GB");
  await expect(dlg.getByTestId("resize-apply")).toBeDisabled();
  await size.fill("72");
  await expect(dlg.getByTestId("resize-error")).toBeHidden();
  await dlg.getByTestId("resize-apply").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("info-disk-limit")).toHaveText("72.0 GB");

  // 停止中估不出最小值：交給 WSL，失敗時顯示易讀訊息（不是 e2fsck 輸出），之後記住最小值
  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await page.getByTestId("btn-machine-resize").click();
  await expect(dlg.getByTestId("resize-min")).toBeHidden();
  await size.fill("60");
  await dlg.getByTestId("resize-apply").click();
  await expect(dlg.getByTestId("resize-error")).toContainText("at least about 72 GB");
  await expect(dlg.getByTestId("resize-error")).not.toContainText("e2fsck");
  await expect(dlg.getByTestId("resize-min")).toHaveText("Smallest possible for this machine: about 72 GB");
  await expect(dlg.getByTestId("resize-apply")).toBeDisabled();
});

test("Free up disk space: a free disk is compacted right away", async ({ page }) => {
  await page.clock.install();
  await machines(page);
  await expect(card(page, "docker-desktop").getByTestId("machine-disk")).toHaveText("1.1 GB");
  await page.getByTestId("btn-machine-compact-docker-desktop").click();
  await expect(page.getByTestId("confirm-dialog")).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("docker-desktop: freed 190.7 MB (now 953.7 MB on Windows)");
  await expect(card(page, "docker-desktop").getByTestId("machine-disk")).toHaveText("953.7 MB");
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "stopped");
  // 結果通知不自動消失（作業可能要一分鐘，使用者常已切走），按 ✕ 才關
  await page.clock.fastForward(30_000);
  await expect(page.getByTestId("notice")).toBeVisible();
  await page.getByTestId("notice-close").click();
  await expect(page.getByTestId("notice")).toBeHidden();
});

test("Ordinary notices still disappear on their own and have no close button", async ({ page }) => {
  await page.clock.install();
  await machines(page);
  await page.getByTestId("btn-machine-use-docker-desktop").click();
  await expect(page.getByTestId("notice")).toBeVisible();
  await expect(page.getByTestId("notice-close")).toHaveCount(0);
  await page.clock.fastForward(9_000);
  await expect(page.getByTestId("notice")).toBeHidden();
});

test("Resize result notice stays until closed", async ({ page }) => {
  await page.clock.install();
  await machines(page);
  await page.getByTestId("btn-machine-config-docker-desktop").click();
  await page.getByTestId("btn-machine-resize").click();
  const dlg = page.getByTestId("resize-dialog");
  await dlg.getByTestId("resize-size").fill("80");
  await dlg.getByTestId("resize-apply").click();
  await expect(page.getByTestId("notice")).toContainText("docker-desktop disk limit set to 80 GB");
  await page.clock.fastForward(30_000);
  await expect(page.getByTestId("notice")).toBeVisible();
  await expect(page.getByTestId("notice-close")).toBeVisible();
});

test("Free up disk space: a running machine asks to shut WSL down, lists what stops, and restarts it", async ({ page, request }) => {
  await machines(page);
  const confirm = page.getByTestId("confirm-dialog");
  await page.getByTestId("btn-machine-compact-Ubuntu-26.04").click();
  await expect(confirm).toBeVisible();
  await expect(confirm.getByTestId("confirm-text")).toContainText("Will be stopped and started again: Ubuntu-26.04");
  await expect(confirm.getByTestId("confirm-ok")).toHaveText("Shut down WSL and free up space");
  // 取消：什麼都不做
  await confirm.getByTestId("confirm-cancel").click();
  await expect(confirm).toBeHidden();
  await expect(card(page, "Ubuntu-26.04").getByTestId("machine-disk")).toHaveText("7.9 GB");

  await page.getByTestId("btn-machine-compact-Ubuntu-26.04").click();
  await confirm.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("notice")).toContainText("Ubuntu-26.04: freed 4.0 GB (now 4.0 GB on Windows)");
  await expect(card(page, "Ubuntu-26.04")).toHaveAttribute("data-state", "running");
  await expect(card(page, "Ubuntu-26.04").getByTestId("machine-disk")).toHaveText("4.0 GB");

  // wsld-gui 從 WSL 內執行：拒絕並說明
  await request.post("/__test/wsl/self-in-wsl?on=1");
  await page.getByTestId("btn-machine-compact-Ubuntu-26.04").click();
  await confirm.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("error-banner")).toContainText("running from inside WSL");
  await expect(card(page, "Ubuntu-26.04")).toHaveAttribute("data-state", "running");
});

test("Free up disk space: a stopped machine whose disk WSL still holds asks after the first try", async ({ page }) => {
  await machines(page);
  await page.getByTestId("btn-machine-start-docker-desktop").click();
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "running");
  await page.getByTestId("btn-machine-stop-docker-desktop").click();
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "stopped");
  await page.getByTestId("btn-machine-compact-docker-desktop").click();
  const confirm = page.getByTestId("confirm-dialog");
  await expect(confirm).toBeVisible();
  await expect(confirm.getByTestId("confirm-text")).toContainText("Will be stopped and started again: Ubuntu-26.04");
  await confirm.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("notice")).toContainText("docker-desktop: freed");
  await expect(card(page, "Ubuntu-26.04")).toHaveAttribute("data-state", "running");
  await expect(card(page, "docker-desktop")).toHaveAttribute("data-state", "stopped");
});

test("Resize also frees up space in the same WSL shutdown", async ({ page }) => {
  await machines(page);
  await page.getByTestId("btn-machine-config-Ubuntu-26.04").click();
  await page.getByTestId("btn-machine-resize").click();
  const dlg = page.getByTestId("resize-dialog");
  await dlg.getByTestId("resize-size").fill("60");
  await dlg.getByTestId("resize-apply").click();
  await expect(page.getByTestId("notice")).toContainText("Ubuntu-26.04 disk limit set to 60 GB and 4.0 GB freed");
  await expect(card(page, "Ubuntu-26.04").getByTestId("machine-disk")).toHaveText("4.0 GB");
});
