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

/** 點 namespace 卡片開啟右側面板（可指定分頁）。 */
async function openNs(page: Page, ns: string, tab?: "architecture" | "pods" | "workloads" | "services" | "config" | "storage") {
  await page.getByTestId(`k8s-ns-card-${ns}`).click();
  await expect(page.getByTestId("k8s-side")).toBeVisible();
  await expect(page.getByTestId("k8s-side-title")).toHaveText(ns);
  if (tab) await page.getByTestId(`k8s-tabs-${tab}`).click();
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
  const defaultPods = page.getByTestId("k8s-ns-card-default").getByTestId("k8s-ns-node-pods");
  await expect(defaultPods).toHaveText("1/1");
  await page.getByTestId("btn-k8s-reset").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("All cluster data");
  await dlg.getByTestId("confirm-cancel").click();
  await expect(defaultPods).toHaveText("1/1");
  await page.getByTestId("btn-k8s-reset").click();
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "ready", { timeout: 10_000 });
  await expect(defaultPods).toHaveText("0/0");
  await expect(page.getByTestId("k8s-ns-card-kube-system").getByTestId("k8s-ns-node-pods")).toHaveText("3/3");
});

test("Pods tab: lists the namespace's pods with status, ready and restarts (no namespace column)", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default", "pods");
  await expect(page.getByTestId("pod-row")).toHaveCount(1);
  const web = page.locator('[data-testid="pod-row"]', { hasText: "poc-web" });
  await expect(web.getByTestId("pod-status")).toHaveText("Running");
  await expect(web.getByTestId("pod-ready")).toHaveText("1/1");
  await expect(page.getByTestId("pod-ns-cell")).toHaveCount(0);
  // 切到另一個 namespace：面板內容跟著換
  await page.getByTestId("k8s-ns-card-kube-system").click();
  await expect(page.getByTestId("k8s-side-title")).toHaveText("kube-system");
  await expect(page.getByTestId("pod-row")).toHaveCount(3);
  await expect(page.locator('[data-testid="pod-row"]', { hasText: "metrics-server" }).getByTestId("pod-restarts")).toHaveText("1");
});

test("Pod logs stream live in the bottom panel; terminal runs kubectl exec", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default", "pods");
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
  await openNs(page, "default", "pods");
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
  await openNs(page, "default", "workloads");
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

  await page.getByTestId("k8s-tabs-workloads").click();
  await page.getByTestId("btn-restart-poc-web").click();
  await expect(page.getByTestId("notice")).toContainText("Restarting poc-web");

  await page.getByTestId("btn-deploy-rm-poc-web").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(page.locator('[data-testid="deploy-row"][data-name="poc-web"]')).toHaveCount(0);
});

test("Deployment scale input rejects out-of-range values", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default", "workloads");
  const input = page.getByTestId("deploy-replicas-poc-web");
  await input.fill("101");
  await expect(page.getByTestId("btn-scale-poc-web")).toBeDisabled();
  await input.fill("-1");
  await expect(page.getByTestId("btn-scale-poc-web")).toBeDisabled();
});

test("Services tab: NodePort service opens in the browser; kubernetes service cannot be deleted", async ({ page, request }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default", "services");
  const poc = page.locator('[data-testid="svc-row"][data-name="poc-web"]');
  await expect(poc.getByTestId("svc-type")).toHaveText("NodePort");
  await expect(poc.getByTestId("svc-ports")).toHaveText("8080:30080/TCP");
  await poc.getByTestId("btn-svc-open-poc-web").click();
  await expect.poll(async () => ((await (await request.get("/__test/window")).json()) as { actions: string[] }).actions).toContain("open-url:http://localhost:30080");
  // ClusterIP 服務沒有瀏覽器鈕；內建 kubernetes 服務不可刪
  await expect(page.locator('[data-testid="svc-row"][data-name="kubernetes"]').getByTestId("btn-svc-open-kubernetes")).toHaveCount(0);
  await expect(page.getByTestId("btn-svc-rm-kubernetes")).toBeDisabled();
  await page.getByTestId("btn-svc-rm-poc-web").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(page.locator('[data-testid="svc-row"][data-name="poc-web"]')).toHaveCount(0);
});

test("the chosen tab is remembered across reloads", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default", "services");
  await expect(page.getByTestId("svc-row").first()).toBeVisible();
  await page.reload();
  await expect(page.getByTestId("view-kubernetes")).toBeVisible();
  await openNs(page, "default");
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
  await openNs(page, "default");
  await expect(page.getByTestId("k8s-tabs-pods")).toHaveText("Pods");
  await expect(page.getByTestId("k8s-tabs-architecture")).toHaveText("Architecture");
  await expect(page.getByTestId("k8s-tabs-workloads")).toHaveText("Workloads");
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

// ---- namespace flow cards、右側面板與架構圖 -----------------------------------------

test("namespaces are shown as flow cards: Service → Deployment → Pods counts per namespace", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await expect(page.locator(".k8s-ns-card")).toHaveCount(2);
  const def = page.getByTestId("k8s-ns-card-default");
  await expect(def.getByTestId("k8s-ns-name")).toHaveText("default");
  await expect(def.getByTestId("k8s-ns-node-services")).toHaveText("2");
  await expect(def.getByTestId("k8s-ns-node-workloads")).toHaveText("1");
  await expect(def.getByTestId("k8s-ns-node-pods")).toHaveText("1/1");
  await expect(def).toHaveAttribute("data-health", "ok");
  const sys = page.getByTestId("k8s-ns-card-kube-system");
  await expect(sys.getByTestId("k8s-ns-node-pods")).toHaveText("3/3");
  // Ingress 節點只在該 namespace 有 Ingress 時出現（本地常用 NodePort，不必佔一格）；對外的 NodePort 另標在卡片上
  await expect(def.getByTestId("k8s-ns-node-ingresses")).toHaveText("1");
  await expect(sys.getByTestId("k8s-ns-node-ingresses")).toHaveCount(0);
  await expect(def.getByTestId("k8s-ns-exposed")).toContainText("NodePort 30080");
  await expect(sys.getByTestId("k8s-ns-exposed")).toHaveCount(0);
  // 側欄還沒開；沒有舊的分頁／namespace 下拉
  await expect(page.getByTestId("k8s-side")).toBeHidden();
  await expect(page.getByTestId("k8s-ns")).toHaveCount(0);
});

test("clicking a card opens a side panel at half width; the close button and a second click on the same card close it", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("k8s-ns-card-default").click();
  const side = page.getByTestId("k8s-side");
  await expect(side).toBeVisible();
  await expect(page.getByTestId("k8s-ns-card-default")).toHaveAttribute("aria-pressed", "true");
  const body = (await page.getByTestId("k8s-body").boundingBox())!;
  const box = (await side.boundingBox())!;
  expect(box.width / body.width).toBeGreaterThan(0.45);
  expect(box.width / body.width).toBeLessThan(0.56);
  await expect(page.getByTestId("k8s-tabs-architecture")).toHaveAttribute("aria-pressed", "true"); // 預設架構圖
  await page.getByTestId("k8s-side-close").click();
  await expect(side).toBeHidden();
  await page.getByTestId("k8s-ns-card-default").click();
  await expect(side).toBeVisible();
  await page.getByTestId("k8s-ns-card-default").click();
  await expect(side).toBeHidden();
});

test("architecture view links a Service to its Deployment and the Deployment to its Pods", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default");
  await expect(page.locator('[data-testid="arch-svc"][data-name="poc-web"]')).toBeVisible();
  await expect(page.locator('[data-testid="arch-deploy"][data-name="poc-web"]')).toBeVisible();
  await expect(page.locator('[data-testid="arch-pods"][data-owner="poc-web"] [data-testid="arch-pod"]')).toHaveCount(1);
  await expect(page.locator('[data-testid="arch-edge"][data-from="svc:poc-web"][data-to="dep:poc-web"]')).toHaveCount(1);
  await expect(page.locator('[data-testid="arch-edge"][data-from="dep:poc-web"]')).toHaveCount(1); // → pods
  // 沒有 selector 的 kubernetes Service 單獨存在、沒有連線
  await expect(page.locator('[data-testid="arch-svc"][data-name="kubernetes"]')).toBeVisible();
  await expect(page.locator('[data-testid="arch-edge"][data-from="svc:kubernetes"]')).toHaveCount(0);
  // 連線確實畫出來（path 有路徑）
  expect(await page.locator('[data-testid="arch-edge"]').first().getAttribute("d")).toMatch(/^M/);
});

test("architecture view: pods without a Deployment are grouped separately; scaling updates the diagram", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "kube-system");
  await expect(page.locator('[data-testid="arch-pods"][data-owner="coredns"] [data-testid="arch-pod"]')).toHaveCount(1);
  await expect(page.locator('[data-testid="arch-edge"][data-from="svc:kube-dns"][data-to="dep:coredns"]')).toHaveCount(1);
  await expect(page.locator('[data-testid="arch-pods"][data-owner=""] [data-testid="arch-pod"]')).toHaveCount(2);
  await openNs(page, "default", "workloads");
  await page.getByTestId("deploy-replicas-poc-web").fill("3");
  await page.getByTestId("btn-scale-poc-web").click();
  await expect(page.getByTestId("notice")).toContainText("scaled to 3");
  await page.getByTestId("k8s-tabs-architecture").click();
  await expect(page.locator('[data-testid="arch-pods"][data-owner="poc-web"] [data-testid="arch-pod"]')).toHaveCount(3);
});

test("clicking a pod in the architecture view opens its logs in the bottom panel", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default");
  await page.getByTestId("arch-pod").first().click();
  await expect(page.getByTestId("panel")).toBeVisible();
  await expect(page.getByTestId("logs-content")).toContainText("listening on :8080");
  await expect(page.getByTestId("k8s-side")).toBeVisible(); // 側欄不受影響
});

test("Deployments tab is a card list: image, ready bar, and a −/+ stepper that only applies on confirm", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default", "workloads");
  const card = page.locator('[data-testid="deploy-row"][data-name="poc-web"]');
  await expect(card).toHaveClass(/deploy-card/);
  await expect(card.getByTestId("deploy-image")).toHaveText("poc/web:1.0");
  await expect(card.getByTestId("deploy-bar")).toBeVisible();
  await expect(card).toContainText("Deployment");
  const input = page.getByTestId("deploy-replicas-poc-web");
  await page.getByTestId("btn-replicas-inc-poc-web").click();
  await expect(input).toHaveValue("2");
  await expect(page.getByTestId("btn-scale-poc-web")).toBeEnabled();
  await expect(card.getByTestId("deploy-ready")).toHaveText("1/1"); // 還沒套用，叢集不變
  await page.getByTestId("btn-replicas-dec-poc-web").click();
  await expect(input).toHaveValue("1");
  await expect(page.getByTestId("btn-scale-poc-web")).toBeDisabled();
  await page.getByTestId("btn-replicas-dec-poc-web").click();
  await page.getByTestId("btn-replicas-dec-poc-web").click();
  await expect(input).toHaveValue("0"); // 不會低於 0
});

// ---- 設計稿對齊：Ingress、StatefulSet、掛載、各分頁、Deployment 詳情 ---------------------

async function startShop(page: Page, request: import("@playwright/test").APIRequestContext) {
  await k8sScenario(request, { shop: true });
  await goK8s(page);
  await startCluster(page);
}

test("cards show Ingress → Service → Workloads → Pods with a ConfigMap/PVC footer", async ({ page, request }) => {
  await startShop(page, request);
  const shop = page.getByTestId("k8s-ns-card-shop");
  await expect(shop.getByTestId("k8s-ns-node-ingresses")).toHaveText("1");
  await expect(shop.getByTestId("k8s-ns-exposed")).toHaveCount(0); // shop 走 Ingress、沒有 NodePort
  await expect(shop.getByTestId("k8s-ns-node-services")).toHaveText("2");
  await expect(shop.getByTestId("k8s-ns-node-workloads")).toHaveText("3"); // web、api、postgres
  await expect(shop.getByTestId("k8s-ns-node-pods")).toHaveText("6/6");
  await expect(shop.getByTestId("k8s-ns-foot")).toHaveText("ConfigMap 1 · PVC 1");
});

test("architecture view draws Ingress → Service → Deployment → Pods, StatefulSets, and dashed ConfigMap/PVC mounts", async ({ page, request }) => {
  await startShop(page, request);
  await openNs(page, "shop");
  await expect(page.locator('[data-testid="arch-ing"][data-name="shop"]')).toBeVisible();
  await expect(page.locator('[data-testid="arch-edge"][data-from="ing:shop"][data-to="svc:web-svc"]')).toHaveCount(1);
  await expect(page.locator('[data-testid="arch-edge"][data-from="ing:shop"][data-to="svc:api-svc"]')).toHaveCount(1);
  await expect(page.locator('[data-testid="arch-edge"][data-from="svc:api-svc"][data-to="dep:api"]')).toHaveCount(1);
  await expect(page.locator('[data-testid="arch-sts"][data-name="postgres"]')).toBeVisible();
  await expect(page.locator('[data-testid="arch-pods"][data-owner="api"] [data-testid="arch-pod"]')).toHaveCount(3);
  await expect(page.locator('[data-testid="arch-mount"][data-name="api-env"]')).toBeVisible();
  const cm = page.locator('[data-testid="arch-edge"][data-from="cm:api-env"][data-to="pods:api"]');
  await expect(cm).toHaveCount(1);
  await expect(cm).toHaveClass(/mount/);
  await expect(page.locator('[data-testid="arch-edge"][data-from="pvc:postgres-data"][data-to="pods:postgres"]')).toHaveCount(1);
  // 掛載連線從下往上：路徑起點在 ConfigMap 方塊頂部、終點在 Pods 群組底部
  const box = async (sel: string) => (await page.locator(sel).boundingBox())!;
  const mountBox = await box('[data-testid="arch-mount"][data-name="api-env"]');
  const podsBox = await box('[data-testid="arch-pods"][data-owner="api"]');
  expect(mountBox.y).toBeGreaterThan(podsBox.y + podsBox.height);
});

test("Pods tab: search and status filter; each pod shows its owner workload", async ({ page, request }) => {
  await startShop(page, request);
  await openNs(page, "shop", "pods");
  await expect(page.getByTestId("pod-row")).toHaveCount(6);
  await expect(page.locator('[data-testid="pod-row"][data-name="api-5c4b8d-j7qz"]').getByTestId("pod-owner")).toContainText("api");
  await page.getByTestId("k8s-pod-search").fill("web-");
  await expect(page.getByTestId("pod-row")).toHaveCount(2);
  await page.getByTestId("k8s-pod-search").fill("");
  await page.getByTestId("k8s-pod-status").click();
  await page.getByTestId("k8s-pod-status-option-notready").click();
  await expect(page.getByTestId("empty")).toBeVisible(); // 全部都就緒
});

test("Workloads tab lists Deployments with a stepper and StatefulSets as read-only cards", async ({ page, request }) => {
  await startShop(page, request);
  await openNs(page, "shop", "workloads");
  await expect(page.getByTestId("deploy-row")).toHaveCount(2);
  const sts = page.locator('[data-testid="sts-row"][data-name="postgres"]');
  await expect(sts).toContainText("StatefulSet");
  await expect(sts.getByTestId("sts-ready")).toHaveText("1/1");
  await expect(sts.locator("input")).toHaveCount(0);
});

test("Services tab shows the target workload, endpoint count and the Ingress routes", async ({ page, request }) => {
  await startShop(page, request);
  await openNs(page, "shop", "services");
  const api = page.locator('[data-testid="svc-row"][data-name="api-svc"]');
  await expect(api.getByTestId("svc-target")).toHaveText("→ api");
  await expect(api.getByTestId("svc-endpoints")).toHaveText("3");
  await expect(page.getByTestId("ingress-route")).toHaveCount(2);
  await expect(page.locator('[data-testid="ingress-route"][data-service="api-svc"]')).toContainText("shop.local/api");
  await expect(page.locator('[data-testid="ingress-route"][data-service="api-svc"]')).toContainText("api-svc:8080");
});

test("Config and Storage tabs list ConfigMaps and PVCs with who uses them", async ({ page, request }) => {
  await startShop(page, request);
  await openNs(page, "shop", "config");
  const cm = page.locator('[data-testid="cm-row"][data-name="api-env"]');
  await expect(cm.getByTestId("cm-keys")).toHaveText("6");
  await expect(cm.getByTestId("cm-used")).toHaveText("api");
  await page.getByTestId("k8s-tabs-storage").click();
  const pvc = page.locator('[data-testid="pvc-row"][data-name="postgres-data"]');
  await expect(pvc.getByTestId("pvc-status")).toHaveText("Bound");
  await expect(pvc.getByTestId("pvc-size")).toHaveText("20Gi");
  await expect(pvc.getByTestId("pvc-used")).toHaveText("postgres");
});

test("Deployment detail: opens from the architecture view or the Workloads tab with strategy, history, pods and events", async ({ page, request }) => {
  await startShop(page, request);
  await openNs(page, "shop");
  await page.locator('[data-testid="arch-deploy"][data-name="api"]').click();
  const d = page.getByTestId("k8s-detail");
  await expect(d).toBeVisible();
  await expect(page.getByTestId("k8s-side-title")).toHaveText("api");
  await expect(page.getByTestId("detail-replicas")).toHaveText("3 / 3");
  await expect(page.getByTestId("detail-strategy")).toHaveText("RollingUpdate");
  await expect(d).toContainText("maxSurge 25%");
  await expect(page.getByTestId("detail-image")).toHaveText("shop/api:2.4.0");
  await expect(page.getByTestId("detail-env")).toContainText("api-env");
  await expect(page.getByTestId("detail-rev")).toHaveCount(2);
  await expect(page.locator('[data-testid="detail-rev"][data-current="true"]')).toContainText("#2");
  await expect(page.getByTestId("detail-pod")).toHaveCount(3);
  await expect(page.getByTestId("detail-event").first()).toContainText("ScalingReplicaSet");
  // 詳情內可 scale：先調整輸入、按套用
  await page.getByTestId("btn-replicas-inc-api").click();
  await page.getByTestId("btn-scale-api").click();
  await expect(page.getByTestId("notice")).toContainText("api scaled to 4");
  await expect(page.getByTestId("detail-replicas")).toHaveText("4 / 4");
  // 返回回到分頁，Workloads 卡片也能進入詳情
  await page.getByTestId("k8s-detail-back").click();
  await expect(page.getByTestId("k8s-detail")).toHaveCount(0);
  await page.getByTestId("k8s-tabs-workloads").click();
  await page.locator('[data-testid="deploy-row"][data-name="web"]').getByTestId("deploy-name").click();
  await expect(page.getByTestId("k8s-side-title")).toHaveText("web");
});

test("architecture view marks NodePort services as exposed entry points", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await openNs(page, "default");
  const poc = page.locator('[data-testid="arch-svc"][data-name="poc-web"]');
  await expect(poc).toHaveClass(/exposed/);
  await expect(poc.getByTestId("arch-exposed")).toHaveText("NodePort 30080");
  await expect(page.locator('[data-testid="arch-svc"][data-name="kubernetes"]')).not.toHaveClass(/exposed/);
});
