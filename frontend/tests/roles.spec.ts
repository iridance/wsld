import { test, expect } from "@playwright/test";
import { open, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("Machines: the first distro is the engine; a second distro can use it as a client", async ({ page, request }) => {
  await request.post("/__test/distro?name=Debian1");
  await open(page);
  await page.getByTestId("nav-machines").click();
  const u = page.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]');
  const d = page.locator('[data-testid="machine-row"][data-name="Debian1"]');
  await expect(page.getByTestId("machines-group-engine").locator('[data-name="Ubuntu-26.04"]')).toBeVisible(); // engine 在 Engine 群組
  await expect(page.getByTestId("btn-machine-role-Ubuntu-26.04")).toHaveCount(0); // engine 本身不提供角色按鈕
  await expect(d.getByTestId("machine-flag-client")).toHaveCount(0);

  await page.getByTestId("btn-machine-role-Debian1").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText(/shared engine|docker CLI/i);
  await dlg.getByTestId("confirm-ok").click();

  await expect(d.getByTestId("machine-flag-client")).toBeVisible();
  await expect(page.getByTestId("btn-machine-role-Debian1")).toHaveCount(0);
  await expect(page.getByTestId("machines-group-engine").locator('[data-name="Ubuntu-26.04"]')).toBeVisible();
});

test("Machines: Docker Desktop's internal distro never offers a role", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-machines").click();
  await expect(page.getByTestId("btn-machine-role-docker-desktop")).toHaveCount(0);
});

test("Machines: the engine sits alone in the first group and can be neither stopped nor removed", async ({ page, request }) => {
  await request.post("/__test/distro?name=Debian1");
  await open(page);
  await page.getByTestId("nav-machines").click();
  const eng = page.getByTestId("machines-group-engine");
  const others = page.getByTestId("machines-group-others");
  await expect(eng.getByTestId("machine-row")).toHaveCount(1);
  await expect(eng.locator('[data-testid="machine-row"][data-name="Ubuntu-26.04"]')).toBeVisible();
  await expect(others.getByTestId("machine-row")).toHaveCount(2); // Debian1、docker-desktop
  // 引擎組在上、其他在下
  const ey = (await eng.boundingBox())!.y;
  const oy = (await others.boundingBox())!.y;
  expect(ey).toBeLessThan(oy);

  await expect(page.getByTestId("btn-machine-stop-Ubuntu-26.04")).toHaveCount(0);
  await expect(page.getByTestId("btn-machine-start-Ubuntu-26.04")).toHaveCount(0); // 執行中：沒有電源鈕
  await expect(page.getByTestId("btn-machine-remove-Ubuntu-26.04")).toHaveCount(0);
  await expect(page.getByTestId("btn-machine-term-Ubuntu-26.04")).toBeVisible();
  // 其他發行版仍可停止／刪除
  await expect(page.getByTestId("btn-machine-remove-Debian1")).toBeVisible();
  await expect(page.getByTestId("btn-machine-start-Debian1")).toBeVisible();
});

test("Machines: the engine is a full-width stat card with its buttons on the right", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-machines").click();
  const eng = page.getByTestId("machines-group-engine");
  const card = eng.getByTestId("machine-row");
  const cb = (await card.boundingBox())!;
  const gb = (await eng.boundingBox())!;
  expect(cb.width).toBeGreaterThan(gb.width - 2); // 全寬
  await expect(card.getByTestId("machine-state")).toBeVisible();
  await expect(card.getByTestId("machine-version")).toHaveCount(0); // engine 不顯示 WSL 版本
  await expect(card.getByTestId("machine-flag-engine")).toHaveCount(0); // 群組標題已是 Engine，不再加 badge
  const tb = (await page.getByTestId("btn-machine-term-Ubuntu-26.04").boundingBox())!;
  expect(tb.x).toBeGreaterThan(cb.x + cb.width / 2); // 按鈕在右半邊
  await expect(card.getByTestId("machine-name")).toHaveClass(/sr-only/); // 不顯示 wsld-engine 名稱
  const ib = (await card.locator(".machine-icon").boundingBox())!;
  expect(ib.x).toBeLessThan(tb.x);
});

test("Machines: the API refuses to stop or remove the engine",async ({ request }) => {
  for (const [path, data] of [
    ["/api/machines/Ubuntu-26.04/stop", undefined],
    ["/api/machines/Ubuntu-26.04/unmanage", undefined],
    ["/api/machines/Ubuntu-26.04/unregister", { confirm: "Ubuntu-26.04" }],
  ] as const) {
    const r = await request.post(path, data ? { data } : {});
    expect(r.status(), path).toBe(409);
  }
});

test("Machines: while the engine is being created its card shows steps and a progress bar", async ({ page }) => {
  let snap: Record<string, unknown> = { active: true, distribution: "Ubuntu", name: "wsld-engine", phase: "downloading", downloaded: 90 * 1024 * 1024, total: 180 * 1024 * 1024, bytes_per_sec: 240 * 1024, elapsed_sec: 75 };
  await page.route("**/api/status", async (route) => {
    const r = await route.fetch();
    await route.fulfill({ json: { ...(await r.json()), starting: true, ready: false } });
  });
  await page.route("**/api/machines/install/progress", (route) => route.fulfill({ json: snap }));
  await open(page);
  await page.getByTestId("nav-machines").click();
  const card = page.getByTestId("engine-build");
  await expect(card).toBeVisible();
  await expect(page.getByTestId("machines-group-engine").getByTestId("engine-build")).toBeVisible();
  await expect(card.getByTestId("engine-build-step-download")).toHaveAttribute("data-status", "active");
  await expect(card.getByTestId("engine-build-step-docker")).toHaveAttribute("data-status", "todo");
  await expect(card.getByTestId("engine-build-progress")).toHaveAttribute("data-pct", "50");
  await expect(card.getByTestId("engine-build-progress-detail")).toContainText("90.0 MB / 180.0 MB");
  // 全寬
  const cb = (await card.boundingBox())!;
  const gb = (await page.getByTestId("machines-group-engine").boundingBox())!;
  expect(cb.width).toBeGreaterThan(gb.width - 2);

  snap = { ...snap, phase: "bootstrapping" };
  await expect(card.getByTestId("engine-build-step-download")).toHaveAttribute("data-status", "done");
  await expect(card.getByTestId("engine-build-step-register")).toHaveAttribute("data-status", "done");
  await expect(card.getByTestId("engine-build-step-docker")).toHaveAttribute("data-status", "active");
  await expect(card.getByTestId("engine-build-progress-label")).toHaveText(/Docker/);
  await expect(page.getByTestId("btn-add-machine")).toBeDisabled();
});
