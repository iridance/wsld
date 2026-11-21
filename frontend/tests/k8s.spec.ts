import { test, expect, type Page } from "@playwright/test";
import { addContainer, group, k8sScenario, k8sState, open, reset, row, setLang } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function goK8s(page: Page) {
  await open(page);
  await page.getByTestId("nav-kubernetes").click();
  await expect(page.getByTestId("view-kubernetes")).toBeVisible();
}

/** 一鍵啟動並等到就緒（假叢集啟動很快，但仍走背景作業＋輪詢）。 */
async function startCluster(page: Page) {
  await page.getByTestId("btn-k8s-start").click();
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "ready", { timeout: 10_000 });
}

test("Kubernetes nav sits next to Machines and shows the not-installed state with an explanation", async ({ page }) => {
  await goK8s(page);
  await expect(page.getByTestId("page-title")).toHaveText("Kubernetes");
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "not-installed");
  await expect(page.getByTestId("k8s-hint")).toContainText("downloads and installs k3s");
  await expect(page.getByTestId("btn-k8s-start")).toBeEnabled();
  await expect(page.getByTestId("btn-k8s-reset")).toBeDisabled();
  await expect(page.getByTestId("btn-k8s-kubeconfig")).toBeDisabled();
  // 導覽順序：… Machines | Kubernetes
  const order = await page.locator('[data-testid="top-nav"] button').evaluateAll((els) => els.map((e) => e.getAttribute("data-view")));
  expect(order.indexOf("kubernetes")).toBe(order.indexOf("machines") + 1);
});

test("one-click start shows progress, then Ready with cluster info", async ({ page, request }) => {
  await k8sScenario(request, { delay: 1500 });
  await goK8s(page);
  await page.getByTestId("btn-k8s-start").click();
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "starting");
  await expect(page.getByTestId("k8s-state")).toHaveText("Starting…");
  await expect(page.getByTestId("btn-k8s-start")).toBeDisabled(); // 作業進行中不可重複
  await expect(page.getByTestId("page-sub")).toHaveText("Starting…");
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "ready", { timeout: 10_000 });
  await expect(page.getByTestId("k8s-f-machine")).toHaveText("Ubuntu-26.04");
  await expect(page.getByTestId("k8s-f-version")).toHaveText("v1.37.2+k3s1");
  await expect(page.getByTestId("k8s-f-node")).toContainText("framework");
  await expect(page.getByTestId("k8s-f-api")).toHaveText("https://127.0.0.1:6443");
  await expect(page.getByTestId("k8s-f-context")).toHaveText("wsld-Ubuntu-26.04");
  // 資訊卡不顯示 kubeconfig 路徑
  await expect(page.getByTestId("k8s-f-kubeconfig")).toHaveCount(0);
  await expect(page.getByTestId("k8s-f-memory")).toHaveText("419.4 MB");
  await expect(page.getByTestId("page-sub")).toContainText("Ready");
  // 啟動鈕變成停止鈕；重置與匯出可用
  await expect(page.getByTestId("btn-k8s-stop")).toBeEnabled();
  await expect(page.getByTestId("btn-k8s-reset")).toBeEnabled();
  await expect(page.getByTestId("btn-k8s-kubeconfig")).toBeEnabled();
});

test("one-click stop returns to Off and keeps k3s installed", async ({ page, request }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("btn-k8s-stop").click();
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "off", { timeout: 10_000 });
  await expect(page.getByTestId("k8s-hint")).toContainText("k3s is off");
  await expect(page.getByTestId("btn-k8s-start")).toBeEnabled();
  expect((await k8sState(request)).running).toBe("");
  // 匯出只讀機器內的 k3s.yaml，與 k3s 是否啟動無關
  await expect(page.getByTestId("btn-k8s-kubeconfig")).toBeEnabled();
  await page.getByTestId("btn-k8s-kubeconfig").click();
  await expect(page.getByTestId("notice")).toContainText("wsld-Ubuntu-26.04");
});

test("a failed start surfaces the error on the card and nothing stays enabled", async ({ page, request }) => {
  await k8sScenario(request, { fail: true });
  await goK8s(page);
  await page.getByTestId("btn-k8s-start").click();
  await expect(page.getByTestId("k8s-error")).toContainText("did not become ready", { timeout: 10_000 });
  await expect(page.getByTestId("btn-k8s-start")).toBeEnabled();
  await expect(page.getByTestId("btn-k8s-reset")).toBeDisabled();
});

test("Export kubeconfig reports the merged file and context", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("btn-k8s-kubeconfig").click();
  await expect(page.getByTestId("notice")).toContainText("wsld-Ubuntu-26.04");
});

test("Reset asks for confirmation, can be cancelled, and wipes user workloads", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await expect(page.getByTestId("pod-row").filter({ hasText: "poc-web" })).toHaveCount(1);
  await page.getByTestId("btn-k8s-reset").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("All cluster data");
  await dlg.getByTestId("confirm-cancel").click();
  await expect(page.getByTestId("pod-row").filter({ hasText: "poc-web" })).toHaveCount(1);
  await page.getByTestId("btn-k8s-reset").click();
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "ready", { timeout: 10_000 });
  await expect(page.getByTestId("pod-row").filter({ hasText: "poc-web" })).toHaveCount(0);
  await expect(page.getByTestId("pod-row").filter({ hasText: "coredns" })).toHaveCount(1);
});

test("Pods tab: lists pods with status, ready, restarts and namespace filter", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await expect(page.getByTestId("pod-row")).toHaveCount(4);
  const web = page.locator('[data-testid="pod-row"]', { hasText: "poc-web" });
  await expect(web.getByTestId("pod-status")).toHaveText("Running");
  await expect(web.getByTestId("pod-ready")).toHaveText("1/1");
  await expect(web.getByTestId("pod-ns-cell")).toHaveText("default");
  await expect(page.locator('[data-testid="pod-row"]', { hasText: "metrics-server" }).getByTestId("pod-restarts")).toHaveText("1");
  // namespace 篩選：default 只剩一個，且不再顯示 namespace 欄
  await page.getByTestId("k8s-ns").click();
  await page.getByTestId("k8s-ns-option-default").click();
  await expect(page.getByTestId("pod-row")).toHaveCount(1);
  await expect(page.getByTestId("pod-ns-cell")).toHaveCount(0);
  await page.getByTestId("k8s-ns").click();
  await page.getByTestId("k8s-ns-option-kube-system").click();
  await expect(page.getByTestId("pod-row")).toHaveCount(3);
});

test("Pod logs stream live in the bottom panel; terminal runs kubectl exec", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  const web = page.locator('[data-testid="pod-row"]', { hasText: "poc-web" });
  const name = (await web.getByTestId("pod-name").textContent())!;
  await web.getByTestId(`btn-pod-logs-${name}`).click();
  await expect(page.getByTestId("panel")).toBeVisible();
  await expect(page.getByTestId("panel-title")).toHaveText(name);
  await expect(page.getByTestId("logs-content")).toContainText("listening on :8080");
  await expect(page.getByTestId("logs-live-toggle")).toBeChecked();
  // 同一個 Pod 切到終端
  await page.getByTestId("panel-tab-term").click();
  await expect(page.getByTestId("terminal-xterm")).toBeVisible();
  await expect.poll(() => page.evaluate(() => (window as unknown as { __wsld: { termText: () => string } }).__wsld.termText())).toContain("fake-shell$");
  await page.getByTestId("btn-panel-close").click();
  await expect(page.getByTestId("panel")).toBeHidden();
});

test("Deleting a pod asks for confirmation; its deployment recreates it under a new name", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  const web = page.locator('[data-testid="pod-row"]', { hasText: "poc-web" });
  const old = (await web.getByTestId("pod-name").textContent())!;
  await web.getByTestId(`btn-pod-rm-${old}`).click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText(old);
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("notice")).toContainText(old);
  await expect(page.locator('[data-testid="pod-row"]', { hasText: "poc-web" })).toHaveCount(1);
  await expect(page.locator(`[data-testid="pod-row"][data-name="${old}"]`)).toHaveCount(0);
});

test("Deployments tab: scale to 2 adds a pod; rollout restart renames pods; delete removes it", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("k8s-tabs-deployments").click();
  const row = page.locator('[data-testid="deploy-row"][data-name="poc-web"]');
  await expect(row.getByTestId("deploy-ready")).toHaveText("1/1");
  const input = page.getByTestId("deploy-replicas-poc-web");
  const apply = page.getByTestId("btn-scale-poc-web");
  await expect(apply).toBeDisabled(); // 與目前相同
  await input.fill("2");
  await expect(apply).toBeEnabled();
  await apply.click();
  await expect(page.getByTestId("notice")).toContainText("poc-web scaled to 2");
  await expect(row.getByTestId("deploy-ready")).toHaveText("2/2");
  await page.getByTestId("k8s-tabs-pods").click();
  await expect(page.locator('[data-testid="pod-row"]', { hasText: "poc-web" })).toHaveCount(2);

  await page.getByTestId("k8s-tabs-deployments").click();
  await page.getByTestId("btn-restart-poc-web").click();
  await expect(page.getByTestId("notice")).toContainText("Restarting poc-web");

  await page.getByTestId("btn-deploy-rm-poc-web").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(page.locator('[data-testid="deploy-row"][data-name="poc-web"]')).toHaveCount(0);
});

test("Deployment scale input rejects out-of-range values", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("k8s-tabs-deployments").click();
  const input = page.getByTestId("deploy-replicas-poc-web");
  await input.fill("101");
  await expect(page.getByTestId("btn-scale-poc-web")).toBeDisabled();
  await input.fill("-1");
  await expect(page.getByTestId("btn-scale-poc-web")).toBeDisabled();
});

test("Services tab: NodePort service opens in the browser; kubernetes service cannot be deleted", async ({ page, request }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("k8s-tabs-services").click();
  const poc = page.locator('[data-testid="svc-row"][data-name="poc-web"]');
  await expect(poc.getByTestId("svc-type")).toHaveText("NodePort");
  await expect(poc.getByTestId("svc-ports")).toHaveText("8080:30080/TCP");
  await poc.getByTestId("btn-svc-open-poc-web").click();
  await expect.poll(async () => ((await (await request.get("/__test/window")).json()) as { actions: string[] }).actions).toContain("open-url:http://localhost:30080");
  // ClusterIP 服務沒有瀏覽器鈕；內建 kubernetes 服務不可刪
  await expect(page.locator('[data-testid="svc-row"][data-name="kube-dns"]').getByTestId("btn-svc-open-kube-dns")).toHaveCount(0);
  await expect(page.getByTestId("btn-svc-rm-kubernetes")).toBeDisabled();
  await page.getByTestId("btn-svc-rm-poc-web").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(page.locator('[data-testid="svc-row"][data-name="poc-web"]')).toHaveCount(0);
});

test("the chosen tab is remembered across reloads", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("k8s-tabs-services").click();
  await expect(page.getByTestId("svc-row").first()).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("view-kubernetes")).toBeVisible();
  await expect(page.getByTestId("k8s-tabs-services")).toHaveAttribute("aria-pressed", "true");
});

test("Kubernetes page is localized (zh-TW)", async ({ page, request }) => {
  await setLang(request, "zh-TW");
  await goK8s(page);
  await expect(page.getByTestId("k8s-state")).toHaveText("尚未安裝");
  await expect(page.getByTestId("btn-k8s-start")).toContainText("啟動");
  await expect(page.getByTestId("btn-k8s-reset")).toContainText("重置");
  await startCluster(page);
  await expect(page.getByTestId("k8s-state")).toHaveText("就緒");
  await expect(page.getByTestId("k8s-tabs-pods")).toHaveText("Pods");
});

test("Containers: k3s containers (k8s_*) fold into one collapsed group without start/stop", async ({ page, request }) => {
  await addContainer(request, "k8s_POD_poc-web-abc_default_uid1_0", "rancher/mirrored-pause:3.6");
  await addContainer(request, "k8s_web_poc-web-abc_default_uid1_0", "poc-web:local");
  await addContainer(request, "plain-redis", "redis:7");
  await open(page);
  const g = group(page, "k8s:k3s");
  await expect(g).toHaveCount(1);
  await expect(g.getByTestId("group-name")).toHaveText("Kubernetes (k3s)");
  await expect(g.getByTestId("group-count")).toHaveText("2/2");
  await expect(g.getByTestId("group-start")).toHaveCount(0);
  await expect(g.getByTestId("group-stop")).toHaveCount(0);
  // 預設收合：k3s 容器列不顯示，其他容器照常
  await expect(row(page, "k8s_web_poc-web-abc_default_uid1_0")).toHaveCount(0);
  await expect(row(page, "plain-redis")).toBeVisible();
  // 展開後可見，並記住選擇
  await g.getByTestId("group-toggle").click();
  await expect(row(page, "k8s_web_poc-web-abc_default_uid1_0")).toBeVisible();
  await page.reload();
  await expect(row(page, "k8s_web_poc-web-abc_default_uid1_0")).toBeVisible();
});

test("Containers: no k3s containers means no k3s group", async ({ page, request }) => {
  await addContainer(request, "plain-redis", "redis:7");
  await open(page);
  await expect(group(page, "k8s:k3s")).toHaveCount(0);
});

test("the cluster card is a standalone card above the list panel; no empty panel when there is nothing to list", async ({ page }) => {
  await goK8s(page);
  const card = page.getByTestId("k8s-card");
  await expect(card).toBeVisible();
  expect(await card.evaluate((e) => !!e.closest('[data-testid="kubernetes-list"]'))).toBe(false);
  await expect(page.getByTestId("kubernetes-list")).toBeHidden(); // 未安裝：沒有資源可列
  await startCluster(page);
  await expect(page.getByTestId("kubernetes-list")).toBeVisible();
  const c = (await card.boundingBox())!;
  const l = (await page.getByTestId("kubernetes-list").boundingBox())!;
  expect(c.y + c.height).toBeLessThanOrEqual(l.y); // 卡片在列表面板之上、不重疊
});

test("top nav never scrolls: all seven tabs stay reachable at common window widths", async ({ page }) => {
  for (const w of [1000, 1280, 1366, 1440, 1920]) {
    await page.setViewportSize({ width: w, height: 800 });
    await goK8s(page);
    const m = await page.getByTestId("top-nav").evaluate((n) => ({ client: n.clientWidth, scroll: n.scrollWidth, left: n.scrollLeft }));
    expect(m.scroll, `nav overflows at ${w}px`).toBeLessThanOrEqual(m.client);
    expect(m.left).toBe(0);
    await expect(page.getByTestId("nav-containers")).toBeInViewport();
  }
});
