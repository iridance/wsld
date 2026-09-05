import { test, expect } from "@playwright/test";
import { open, pushLog, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

test("Logs panel shows last lines (Live off by default) and Live streams new lines", async ({ page, request }) => {
  await open(page);
  await page.getByTestId("btn-logs-web").click();
  const panel = page.getByTestId("panel");
  await expect(panel).toBeVisible();
  await expect(panel).toHaveAttribute("data-kind", "logs");
  await expect(page.getByTestId("panel-title")).toContainText("web");
  await expect(page.getByTestId("logs-live-toggle")).not.toBeChecked();
  await expect(page.getByTestId("logs-content")).toContainText("GET / 200");

  // 非 Live：新行不會自動出現
  await pushLog(request, "web", "STATIC-SHOULD-NOT-APPEAR");
  await page.waitForTimeout(500);
  await expect(page.getByTestId("logs-content")).not.toContainText("STATIC-SHOULD-NOT-APPEAR");

  await page.getByTestId("logs-live-toggle").check();
  await expect(page.getByTestId("logs-content")).toContainText("STATIC-SHOULD-NOT-APPEAR"); // tail 重載
  await pushLog(request, "web", "LIVE-LINE-1");
  await expect(page.getByTestId("logs-content")).toContainText("LIVE-LINE-1");

  await page.getByTestId("btn-panel-close").click();
  await expect(panel).toBeHidden();
});

test("Logs default tail is 100 lines", async ({ page, request }) => {
  for (let i = 1; i <= 120; i++) await pushLog(request, "web", `L${i}`);
  await open(page);
  await page.getByTestId("btn-logs-web").click();
  await expect(page.getByTestId("logs-content")).toContainText("L120");
  const text = await page.getByTestId("logs-content").textContent();
  const lines = (text ?? "").trim().split("\n");
  expect(lines.length).toBe(100);
  expect(lines[0]).toBe("L21");
  expect(lines[99]).toBe("L120");
});

test("Terminal opens exec session, echoes typed command, and closes", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("btn-term-web")).toBeEnabled();
  await page.getByTestId("btn-term-web").click();
  const panel = page.getByTestId("panel");
  await expect(panel).toHaveAttribute("data-kind", "term");
  await expect(page.getByTestId("panel-title")).toContainText("web");
  await expect(page.locator(".xterm")).toBeVisible();

  const termText = () => page.evaluate(() => (window as unknown as { __wsld: { termText(): string } }).__wsld.termText());
  await expect.poll(termText).toContain("fake-shell$");

  await page.keyboard.type("ls -la");
  await page.keyboard.press("Enter");
  await expect.poll(termText).toContain("you typed: ls -la");

  await page.keyboard.type("exit");
  await page.keyboard.press("Enter");
  await expect.poll(termText).toContain("bye");

  await page.getByTestId("btn-panel-close").click();
  await expect(panel).toBeHidden();
});

test("Terminal button is disabled for stopped containers", async ({ page }) => {
  await open(page);
  await expect(page.getByTestId("btn-term-db")).toBeDisabled();
});

test("opening Terminal replaces an open Logs panel", async ({ page }) => {
  await open(page);
  await page.getByTestId("btn-logs-web").click();
  await expect(page.getByTestId("panel")).toHaveAttribute("data-kind", "logs");
  await page.getByTestId("btn-term-web").click();
  await expect(page.getByTestId("panel")).toHaveAttribute("data-kind", "term");
});
