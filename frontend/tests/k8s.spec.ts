import { test, expect, type APIRequestContext, type Page } from "@playwright/test";
import { group, open, reset } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function k8s(request: APIRequestContext, q: string): Promise<void> {
  expect((await request.post(`/__test/k8s?${q}`)).ok()).toBeTruthy();
}

async function goK8s(page: Page) {
  await open(page);
  await page.getByTestId("nav-kubernetes").click();
  await expect(page.getByTestId("view-kubernetes")).toBeVisible();
}

test("Nav: Kubernetes sits right of Machines behind its own separator", async ({ page }) => {
  await open(page);
  const kids = page.getByTestId("top-nav").locator(":scope > *");
  const ids = await kids.evaluateAll((els) => els.map((e) => e.getAttribute("data-testid") ?? (e.classList.contains("tab-sep") ? "sep" : "?")));
  expect(ids).toEqual(["nav-containers", "nav-images", "nav-volumes", "nav-networks", "sep", "nav-machines", "sep", "nav-kubernetes"]);
});

test("Kubernetes is off by default: hero explains the dedicated distro and offers to turn it on", async ({ page }) => {
  await goK8s(page);
  const hero = page.getByTestId("k8s-hero");
  await expect(hero).toBeVisible();
  await expect(hero).toContainText("k8s-engine");
  await expect(page.getByTestId("btn-k8s-enable")).toBeEnabled();
  await expect(page.getByTestId("btn-k8s-disable")).toHaveCount(0);
});

test("Turning Kubernetes on walks the bootstrap steps and ends on the status card with pods", async ({ page, request }) => {
  await k8s(request, "delay=250&pods=1");
  await goK8s(page);
  await page.getByTestId("btn-k8s-enable").click();
  const progress = page.getByTestId("k8s-progress");
  await expect(progress).toBeVisible();
  await expect(progress.getByTestId("k8s-steps").locator("li")).toHaveCount(7);
  await expect(progress.locator('[data-status="active"]')).toHaveCount(1);
  await expect(page.getByTestId("k8s-card")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("notice")).toBeHidden(); // 開啟成功不跳錯誤
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "ready");
  await expect(page.getByTestId("k8s-version")).toHaveText("v1.37.2+k3s1");
  await expect(page.getByTestId("k8s-endpoint")).toHaveText("https://127.0.0.1:26443");
  await expect(page.getByTestId("k8s-kubeconfig")).toHaveCount(0); // kubeconfig 收在 Info 裡
  await expect(page.getByTestId("k8s-limits")).toContainText("port-forward");
  await expect(page.getByTestId("k8s-pod-row")).toHaveCount(3);
  const crash = page.locator('[data-testid="k8s-pod-row"][data-name="worker-5d9f7c6b4-q2wkp"]');
  await expect(crash.getByTestId("k8s-pod-status")).toHaveText("CrashLoopBackOff");
  await expect(crash.getByTestId("k8s-pod-status")).toHaveClass(/failed/);
});

test("A failed step is marked and Retry turns Kubernetes on", async ({ page, request }) => {
  await k8s(request, "fail=bootstrap");
  await goK8s(page);
  await page.getByTestId("btn-k8s-enable").click();
  await expect(page.getByTestId("k8s-error")).toContainText("bootstrap");
  await expect(page.getByTestId("k8s-step-bootstrap")).toHaveAttribute("data-status", "failed");
  await expect(page.getByTestId("k8s-step-distro")).toHaveAttribute("data-status", "done");
  await k8s(request, "fail=");
  await page.getByTestId("btn-k8s-retry").click();
  await expect(page.getByTestId("k8s-card")).toBeVisible({ timeout: 15_000 });
});

test("Turning Kubernetes off asks first and returns to the hero", async ({ page, request }) => {
  await k8s(request, "installed=1&running=1");
  await goK8s(page);
  await expect(page.getByTestId("k8s-card")).toBeVisible();
  await page.getByTestId("btn-k8s-disable").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("kept");
  await dlg.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("k8s-card")).toBeVisible();
  await page.getByTestId("btn-k8s-disable").click();
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("k8s-hero")).toBeVisible({ timeout: 10_000 });
});

test("Machines does not list the k8s distro; Kubernetes shows it as an engine card", async ({ page, request }) => {
  await k8s(request, "installed=1&running=1");
  await open(page);
  await page.getByTestId("nav-machines").click();
  await expect(page.locator('[data-testid="machine-row"]').first()).toBeVisible();
  await expect(page.locator('[data-testid="machine-row"][data-name="k8s-engine"]')).toHaveCount(0);
  await page.getByTestId("nav-kubernetes").click();
  const group = page.getByTestId("k8s-group-engine");
  await expect(group.locator(".machine-group-title")).toHaveText("Kubernetes engine");
  const card = group.getByTestId("k8s-card");
  await expect(card).toHaveClass(/engine-card/);
  await expect(card.locator(".stat-label")).toContainText(["State", "Kubernetes version", "Disk", "Endpoint"]);
  await expect(card.getByTestId("k8s-disk")).toBeVisible();
  const tools = card.locator(".machine-tools button");
  await expect(tools).toHaveCount(5); // 同 Docker engine 卡：關閉＋Info／資料夾／終端／釋放磁碟空間
  await expect(card.locator(".machine-tools").getByTestId("btn-k8s-disable")).toBeVisible();
});

test("Kubernetes engine card tools act on the k8s distro (folder, terminal, Info)", async ({ page, request }) => {
  await k8s(request, "installed=1&running=1");
  await goK8s(page);
  await page.getByTestId("btn-machine-folder-k8s-engine").click();
  await expect.poll(async () => ((await (await request.get("/__test/window")).json()) as { actions: string[] }).actions).toContain("open-folder:k8s-engine");
  await page.getByTestId("btn-machine-term-k8s-engine").click();
  await expect(page.getByTestId("panel")).toBeVisible();
  await expect(page.getByTestId("terminal-xterm")).toBeVisible();
  await page.getByTestId("btn-machine-config-k8s-engine").click();
  await expect(page.getByTestId("info-drawer")).toBeVisible();
  const drawer = page.getByTestId("info-drawer");
  await expect(drawer).toContainText("k8s-engine");
  await expect(drawer.getByTestId("info-machine-endpoint")).toHaveText("https://127.0.0.1:26443");
  await expect(drawer.getByTestId("k8s-kubeconfig")).toContainText("kubeconfig.yaml");
  await expect(drawer.getByTestId("k8s-kubeconfig-wsl")).toHaveText("/mnt/wsl/wsld/k8s/kubeconfig");
  await expect(drawer.getByTestId("btn-copy-k8s-kubeconfig")).toBeVisible();
});

test("Containers: pod containers are grouped under Kubernetes, pause containers hidden, sandbox ports kept", async ({ page, request }) => {
  expect((await request.post("/__test/container?pod=web-7d4&ns=default&container=nginx&image=nginx:alpine&port=18080")).ok()).toBeTruthy();
  await open(page);
  const g = group(page, "kubernetes");
  await expect(g).toBeVisible();
  await expect(g.getByTestId("group-name")).toHaveText("Kubernetes");
  await expect(g.getByTestId("group-start")).toHaveCount(0); // kubelet 管理：沒有整組啟停
  const rows = page.locator('[data-testid="container-row"]', { hasText: "nginx · web-7d4 (default)" });
  await expect(rows).toHaveCount(1);
  await expect(page.locator('[data-testid="container-row"]', { hasText: "pause" })).toHaveCount(0);
  // sandbox 的 published 埠（LoadBalancer／hostPort）併到應用容器：瀏覽器按鈕可用、指向 localhost:18080
  const browser = rows.first().locator('[data-testid^="btn-browser-"]');
  await expect(browser).toBeEnabled();
  await expect(browser).toHaveAttribute("title", /localhost:18080/);
});
