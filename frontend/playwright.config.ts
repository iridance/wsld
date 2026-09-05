import { defineConfig } from "@playwright/test";

const port = 34199;
export const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: "./tests",
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["list"], ["html", { open: "never" }]] : "list",
  use: {
    baseURL,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: {
    // 由 repo 根目錄啟動 Go 測試主機（服務 frontend/dist + FakeEngine）
    command: `cd .. && go run ./cmd/wsld-gui-testhost -addr 127.0.0.1:${port} -dist frontend/dist`,
    url: `${baseURL}/api/meta`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
  projects: [{ name: "chromium", use: { browserName: "chromium" } }],
});
