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
  await expect(u.getByTestId("machine-flag-engine")).toBeVisible();
  await expect(page.getByTestId("btn-machine-role-Ubuntu-26.04")).toHaveCount(0); // engine 本身不提供角色按鈕
  await expect(d.getByTestId("machine-flag-client")).toHaveCount(0);

  await page.getByTestId("btn-machine-role-Debian1").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText(/shared engine|docker CLI/i);
  await dlg.getByTestId("confirm-ok").click();

  await expect(d.getByTestId("machine-flag-client")).toBeVisible();
  await expect(page.getByTestId("btn-machine-role-Debian1")).toHaveCount(0);
  await expect(u.getByTestId("machine-flag-engine")).toBeVisible();
});

test("Machines: Docker Desktop's internal distro never offers a role", async ({ page }) => {
  await open(page);
  await page.getByTestId("nav-machines").click();
  await expect(page.getByTestId("btn-machine-role-docker-desktop")).toHaveCount(0);
});
