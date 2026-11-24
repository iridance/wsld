import { test, expect } from "@playwright/test";
import { open, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
  await request.post("/__test/distro?name=Debian1&state=Running");
  await request.post("/__test/expose?distro=Debian1&on=1");
});

const row = (page: import("@playwright/test").Page, name: string) => page.locator(`[data-testid="machine-row"][data-name="${name}"]`);

async function machines(page: import("@playwright/test").Page) {
  await open(page);
  await page.getByTestId("nav-machines").click();
  await expect(row(page, "Debian1")).toBeVisible();
}

test("Machines: an exposed distro is tagged and offers a release button; the engine and others do not", async ({ page }) => {
  await machines(page);
  await expect(row(page, "Debian1").getByTestId("machine-flag-exposed")).toBeVisible();
  await expect(page.getByTestId("btn-machine-expose-release-Debian1")).toBeVisible();
  await expect(row(page, "Ubuntu-26.04").getByTestId("machine-flag-exposed")).toHaveCount(0);
  await expect(page.getByTestId("btn-machine-expose-release-Ubuntu-26.04")).toHaveCount(0);
  await expect(page.getByTestId("btn-machine-expose-release-docker-desktop")).toHaveCount(0);
});

test("Machines: releasing an exposure nobody uses needs no confirmation", async ({ page }) => {
  await machines(page);
  await page.getByTestId("btn-machine-expose-release-Debian1").click();
  await expect(row(page, "Debian1").getByTestId("machine-flag-exposed")).toHaveCount(0);
  await expect(page.getByTestId("notice")).toContainText("Debian1");
});

test("Machines: releasing an exposure that running containers use lists them, and stops them on confirm", async ({ page, request }) => {
  await request.post("/__test/expose?container=web&src=/mnt/wsl/distro/Debian1/home/u/app");
  await machines(page);
  await page.getByTestId("btn-machine-expose-release-Debian1").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("web");
  await dlg.getByTestId("confirm-cancel").click();
  await expect(row(page, "Debian1").getByTestId("machine-flag-exposed")).toBeVisible();

  await page.getByTestId("btn-machine-expose-release-Debian1").click();
  await page.getByTestId("confirm-ok").click();
  await expect(row(page, "Debian1").getByTestId("machine-flag-exposed")).toHaveCount(0);
  const cs = (await (await request.get("/api/containers?all=1")).json()) as { Names: string[]; State: string }[];
  expect(cs.find((c) => c.Names[0] === "/web")?.State).not.toBe("running");
});

test("Machines: stopping an exposed distro whose files are in use asks first, and cancelling changes nothing", async ({ page, request }) => {
  await request.post("/__test/expose?container=web&src=/mnt/wsl/distro/Debian1/home/u/app");
  await machines(page);
  await page.getByTestId("btn-machine-stop-Debian1").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("web");
  await dlg.getByTestId("confirm-cancel").click();
  await expect(row(page, "Debian1")).toHaveAttribute("data-state", "running");
  await expect(row(page, "Debian1").getByTestId("machine-flag-exposed")).toBeVisible();

  await page.getByTestId("btn-machine-stop-Debian1").click();
  await page.getByTestId("confirm-ok").click();
  await expect(row(page, "Debian1")).toHaveAttribute("data-state", "stopped");
  await expect(row(page, "Debian1").getByTestId("machine-flag-exposed")).toHaveCount(0);
});
