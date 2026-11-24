import { test, expect, type Page } from "@playwright/test";
import { addContainer, group, k8sScenario, k8sState, open, reset, row, setLang } from "./helpers";

test.beforeEach(async ({ request }) => {
  await reset(request);
});

async function goK8s(page: Page, view: "apps" | "namespaces" = "namespaces") {
  // Ready 時預設是 Apps 視圖；舊的 namespace／分頁測試固定在 Namespaces 視圖
  await page.addInitScript((v) => localStorage.setItem("wsld.k8s.view", JSON.stringify(v)), view);
  await open(page);
  await page.getByTestId("nav-kubernetes").click();
  await expect(page.getByTestId("view-kubernetes")).toBeVisible();
}

/** 切到 Workloads／System 頁籤（kube-* 在 System）。 */
async function showGroup(page: Page, group: "workloads" | "system") {
  await page.getByTestId(`k8s-group-${group}`).click();
  await expect(page.getByTestId(`k8s-group-${group}`)).toHaveAttribute("aria-selected", "true");
}

/** 點 namespace 卡片開啟右側面板（可指定分頁）。 */
async function openNs(page: Page, ns: string, tab?: "architecture" | "pods" | "workloads" | "services" | "config" | "storage" | "others") {
  await showGroup(page, ns.startsWith("kube-") ? "system" : "workloads");
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
  await expect(page.getByTestId("k8s-hero")).toHaveAttribute("data-state", "not-installed");
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
  await expect(page.getByTestId("k8s-f-api")).toHaveText("https://127.0.0.1:26443");
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
  await expect(page.getByTestId("k8s-hero")).toHaveAttribute("data-state", "off", { timeout: 10_000 });
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
  await showGroup(page, "system");
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
  await openNs(page, "kube-system");
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
  await expect(page.getByTestId("k8s-hero")).toContainText("在你的機器上跑 Kubernetes");
  await expect(page.getByTestId("btn-k8s-start")).toContainText("啟用 Kubernetes");
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
  await expect(page.getByTestId("k8s-hero")).toBeVisible(); // 未啟用：中央 hero，沒有資訊卡
  await expect(page.getByTestId("k8s-card")).toHaveCount(0);
  await expect(page.getByTestId("kubernetes-list")).toBeHidden(); // 未安裝：沒有資源可列
  await startCluster(page);
  const card = page.getByTestId("k8s-card");
  await expect(card).toBeVisible();
  expect(await card.evaluate((e) => !!e.closest('[data-testid="kubernetes-list"]'))).toBe(false);
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

test("Workloads and System are separate tabs; system namespaces list what is really inside", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  // 預設在 Workloads：只有 default（kube-* 不在這裡），並給空狀態提示
  await expect(page.getByTestId("k8s-group-workloads")).toContainText("1");
  await expect(page.getByTestId("k8s-group-system")).toContainText("3");
  await expect(page.locator(".k8s-ns-card")).toHaveCount(1);
  await expect(page.getByTestId("k8s-ns-card-default").getByTestId("k8s-ns-kind")).toHaveText("Default");
  await expect(page.getByTestId("k8s-ns-hint")).toBeVisible();
  // 健康狀態正常時不顯示徽章
  await expect(page.getByTestId("k8s-ns-card-default").getByTestId("k8s-ns-health")).toHaveCount(0);
  await page.getByTestId("k8s-group-system").click();
  await expect(page.locator(".k8s-ns-card")).toHaveCount(3);
  await expect(page.getByTestId("k8s-ns-hint")).toHaveCount(0);
  // kube-system 有工作負載：畫流程；kube-public／kube-node-lease 沒有：直接列出實際內容
  await expect(page.getByTestId("k8s-ns-card-kube-system").getByTestId("k8s-ns-node-pods")).toHaveText("3/3");
  const pub = page.getByTestId("k8s-ns-card-kube-public");
  await expect(pub.getByTestId("k8s-ns-node-pods")).toHaveCount(0);
  await expect(pub.getByTestId("k8s-ns-items")).toHaveText("Role 1RoleBinding 1");
  await expect(page.getByTestId("k8s-ns-card-kube-node-lease").getByTestId("k8s-ns-items")).toHaveText("Lease 1");
  // 選取的頁籤會記住
  await page.reload();
  await expect(page.getByTestId("k8s-group-system")).toHaveAttribute("aria-selected", "true");
  // 側欄：架構圖是空的時，直接列出 Role／RoleBinding；Other 分頁有同樣的資源
  await page.getByTestId("k8s-ns-card-kube-public").click();
  await expect(page.getByTestId("arch-only-others")).toBeVisible();
  await expect(page.getByTestId("other-row")).toHaveCount(2);
  await page.getByTestId("k8s-tabs-others").click();
  await expect(page.getByTestId("other-row").first().getByTestId("other-kind")).toHaveText("Role");
  // 切回 Workloads：已開的 system namespace 側欄關閉
  await page.getByTestId("k8s-group-workloads").click();
  await expect(page.getByTestId("k8s-side")).toBeHidden();
});

test("namespaces are shown as flow cards: Service → Deployment → Pods counts per namespace", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await expect(page.locator(".k8s-ns-card")).toHaveCount(1);
  const def = page.getByTestId("k8s-ns-card-default");
  await expect(def.getByTestId("k8s-ns-name")).toHaveText("default");
  await expect(def.getByTestId("k8s-ns-node-services")).toHaveText("2");
  await expect(def.getByTestId("k8s-ns-node-workloads")).toHaveText("1");
  await expect(def.getByTestId("k8s-ns-node-pods")).toHaveText("1/1");
  await expect(def).toHaveAttribute("data-health", "ok");
  // Ingress 節點只在該 namespace 有 Ingress 時出現（本地常用 NodePort，不必佔一格）；對外的 NodePort 另標在卡片上
  await expect(def.getByTestId("k8s-ns-node-ingresses")).toHaveText("1");
  await showGroup(page, "system");
  const sys = page.getByTestId("k8s-ns-card-kube-system");
  await expect(sys.getByTestId("k8s-ns-node-pods")).toHaveText("3/3");
  await expect(sys.getByTestId("k8s-ns-node-ingresses")).toHaveCount(0);
  await expect(sys.getByTestId("k8s-ns-exposed")).toHaveCount(0);
  await showGroup(page, "workloads");
  await expect(def.getByTestId("k8s-ns-exposed")).toContainText("NodePort 30080");
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

// ---- 單節點流程：hero、啟用步驟、空狀態、Deploy、Apply -----------------------------

test("Off shows the hero with the add-context option on by default; the toolbar has no second Start", async ({ page }) => {
  await goK8s(page);
  await expect(page.getByTestId("k8s-hero")).toContainText("Run Kubernetes on your machine");
  await expect(page.getByTestId("k8s-opt-add-context")).toBeChecked();
  await expect(page.getByTestId("k8s-f-machine")).toContainText("Ubuntu-26.04");
  await expect(page.getByTestId("btn-k8s-start")).toHaveCount(1);
  await expect(page.getByTestId("btn-k8s-deploy")).toHaveCount(0); // 未就緒時不提供 Deploy
});

test("enabling shows a step checklist that advances and ends in Ready", async ({ page, request }) => {
  await k8sScenario(request, { delay: 900 });
  await goK8s(page);
  await page.getByTestId("btn-k8s-start").click();
  const steps = page.getByTestId("k8s-steps");
  await expect(steps).toBeVisible();
  await expect(steps.locator("li")).toHaveCount(4); // engine / install / node / context
  await expect(steps.locator('li[data-status="active"]')).toHaveCount(1);
  await expect(page.getByTestId("k8s-step-node")).toHaveAttribute("data-status", "active", { timeout: 10_000 });
  await expect(page.getByTestId("k8s-step-engine")).toHaveAttribute("data-status", "done");
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "ready", { timeout: 15_000 });
  await expect(page.getByTestId("k8s-steps")).toHaveCount(0);
});

test("unchecking add-context drops that step and the choice is remembered", async ({ page, request }) => {
  await k8sScenario(request, { delay: 600 });
  await goK8s(page);
  await page.getByTestId("k8s-opt-add-context").uncheck();
  await page.getByTestId("btn-k8s-start").click();
  await expect(page.getByTestId("k8s-steps").locator("li")).toHaveCount(3);
  await expect(page.getByTestId("k8s-step-context")).toHaveCount(0);
  await expect(page.getByTestId("k8s-state")).toHaveAttribute("data-state", "ready", { timeout: 15_000 });
  await page.reload();
  await page.getByTestId("nav-kubernetes").click();
  await page.getByTestId("btn-k8s-stop").click();
  await expect(page.getByTestId("k8s-hero")).toBeVisible({ timeout: 10_000 });
  await expect(page.getByTestId("k8s-opt-add-context")).not.toBeChecked();
});

test("a failed enable names the step it stopped at and offers Retry", async ({ page, request }) => {
  await k8sScenario(request, { fail: true });
  await goK8s(page);
  await page.getByTestId("btn-k8s-start").click();
  await expect(page.getByTestId("k8s-error")).toContainText("did not become ready", { timeout: 10_000 });
  await expect(page.getByTestId("k8s-failed-step")).toContainText("Wait for node to be Ready");
  await expect(page.getByTestId("btn-k8s-start")).toContainText("Retry");
});

test("Ready with no user workloads invites a first deploy; Deploy creates it and shows the localhost URL", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  await expect(page.getByTestId("k8s-deploy-empty")).toHaveCount(0); // 範例叢集已有 poc-web
  await page.getByTestId("btn-k8s-reset").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(page.getByTestId("k8s-deploy-empty")).toBeVisible({ timeout: 10_000 });
  await page.getByTestId("btn-k8s-deploy-first").click();
  const dlg = page.getByTestId("deploy-dialog");
  await expect(dlg).toBeVisible();
  await dlg.getByTestId("deploy-submit").click();
  await expect(dlg.getByTestId("deploy-error")).toContainText("Enter an image");
  await dlg.getByTestId("deploy-image").fill("registry.local:5000/team/myapp:latest");
  await expect(dlg.getByTestId("deploy-name")).toHaveValue("myapp"); // 由 image 推出名稱
  await dlg.getByTestId("deploy-port").fill("99999");
  await dlg.getByTestId("deploy-submit").click();
  await expect(dlg.getByTestId("deploy-error")).toContainText("Port must be");
  await dlg.getByTestId("deploy-port").fill("8080");
  await dlg.getByTestId("deploy-replicas").fill("2");
  await dlg.getByTestId("deploy-submit").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("myapp deployed");
  await expect(page.getByTestId("notice")).toContainText("http://localhost:");
  await expect(page.getByTestId("k8s-deploy-empty")).toHaveCount(0);
  await expect(page.locator('[data-testid="k8s-app-row"][data-name="myapp"]')).toBeVisible();
  await page.getByTestId("k8s-view-namespaces").click();
  await expect(page.getByTestId("k8s-ns-card-default").getByTestId("k8s-ns-node-pods")).toHaveText("2/2");
});

test("Deploy dialog: fixed localhost port is the default; a taken port moves to the next one and says so", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  const dlg = page.getByTestId("deploy-dialog");
  const deploy = async (image: string, port: string, host: string) => {
    await page.getByTestId("btn-k8s-deploy").click();
    await expect(dlg.getByTestId("deploy-expose-loadbalancer")).toBeChecked();
    await expect(dlg.getByTestId("deploy-hostport-field")).toBeVisible();
    await dlg.getByTestId("deploy-image").fill(image);
    await dlg.getByTestId("deploy-port").fill(port);
    await dlg.getByTestId("deploy-hostport").fill(host);
    await dlg.getByTestId("deploy-submit").click();
    await expect(dlg).toBeHidden();
  };
  await deploy("shop:1", "3000", "8080");
  await expect(page.getByTestId("notice")).toContainText("shop deployed — http://localhost:8080");
  await deploy("blog:1", "3000", "8080");
  await expect(page.getByTestId("notice")).toContainText("http://localhost:8081");
  await expect(page.getByTestId("notice")).toContainText("port 8080 was in use");
});

test("Deploy dialog: expose modes show or hide the localhost port and require a container port", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("btn-k8s-deploy").click();
  const dlg = page.getByTestId("deploy-dialog");
  await dlg.getByTestId("deploy-image").fill("svc:1");
  await dlg.getByTestId("deploy-submit").click();
  await expect(dlg.getByTestId("deploy-error")).toContainText("container port"); // 預設要對外，缺 port
  await dlg.getByLabel("Random NodePort").check();
  await expect(dlg.getByTestId("deploy-hostport-field")).toBeHidden();
  await dlg.getByLabel("Cluster only").check();
  await dlg.getByTestId("deploy-submit").click(); // 不對外：不需要 port
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("svc deployed");
  await expect(page.getByTestId("notice")).not.toContainText("localhost");
});

test("toolbar Deploy and Copy context appear only when Ready", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await expect(page.getByTestId("btn-k8s-deploy")).toBeVisible();
  await expect(page.getByTestId("btn-k8s-copy-context")).toBeVisible();
  await page.getByTestId("btn-k8s-copy-context").click();
  await expect(page.getByTestId("notice")).toContainText("wsld-Ubuntu-26.04");
  await page.getByTestId("btn-k8s-deploy").click();
  await expect(page.getByTestId("deploy-dialog")).toBeVisible();
  await page.getByTestId("deploy-cancel").click();
  await expect(page.getByTestId("deploy-dialog")).toBeHidden();
});

test("Apply YAML file sends the file to kubectl apply and reports the result", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  await page.getByTestId("btn-k8s-reset").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(page.getByTestId("k8s-deploy-empty")).toBeVisible({ timeout: 10_000 });
  const chooser = page.waitForEvent("filechooser");
  await page.getByTestId("btn-k8s-apply-yaml").click();
  await (await chooser).setFiles({ name: "app.yaml", mimeType: "text/yaml", buffer: Buffer.from("apiVersion: apps/v1\nkind: Deployment\n") });
  await expect(page.getByTestId("notice")).toContainText("Applied");
});

// ---- Apps 視圖 -----------------------------------------------------------------

const appRow = (page: Page, name: string) => page.locator(`[data-testid="k8s-app-row"][data-name="${name}"]`);

test("Apps is the default Ready view: one row per app with status and a clickable localhost URL; system apps are hidden", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  await expect(page.getByTestId("k8s-view-apps")).toHaveAttribute("aria-selected", "true");
  const row = appRow(page, "poc-web");
  await expect(row).toBeVisible();
  await expect(row.getByTestId("app-status")).toContainText("1/1 Running");
  await expect(row.getByTestId("app-url-link")).toHaveText("localhost:30080 ↗"); // NodePort Service 併入同一列
  await expect(page.locator('[data-testid="k8s-app-row"][data-ns^="kube-"]')).toHaveCount(0);
  await page.getByTestId("k8s-show-system").check();
  await expect(page.locator('[data-testid="k8s-app-row"][data-ns^="kube-"]').first()).toBeVisible();
});

test("an app row expands to its pods and scale controls", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  const row = appRow(page, "poc-web");
  await row.getByTestId("btn-app-expand").click();
  await expect(page.getByTestId("app-detail")).toBeVisible();
  await expect(page.getByTestId("app-pod")).toHaveCount(1);
  await page.getByTestId("btn-replicas-inc-poc-web").click();
  await page.getByTestId("btn-scale-poc-web").click();
  await expect(page.getByTestId("app-pod")).toHaveCount(2, { timeout: 10_000 });
  await page.getByTestId("btn-pod-logs-" + (await page.getByTestId("app-pod").first().getAttribute("data-name"))).click();
  await expect(page.getByTestId("panel")).toBeVisible();
});

test("an expanded app row collapses again when its toggle is clicked", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  const row = appRow(page, "poc-web");
  await row.getByTestId("btn-app-expand").click();
  await expect(row.getByTestId("app-detail")).toBeVisible();
  await row.getByTestId("btn-app-expand").click(); // 按鈕仍有焦點且 aria-expanded=true：重繪不能被略過
  await expect(row.getByTestId("app-detail")).toHaveCount(0);
  await expect(row).toHaveAttribute("data-expanded", "false");
});

test("an unexposed app offers Add URL; a fixed localhost port becomes its link, a taken port moves on", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  // 兩個不對外的 app
  for (const [img, name] of [["a:1", "alpha"], ["b:1", "beta"]]) {
    await page.getByTestId("btn-k8s-deploy").click();
    const dlg = page.getByTestId("deploy-dialog");
    await dlg.getByTestId("deploy-image").fill(img);
    await dlg.getByTestId("deploy-name").fill(name);
    await dlg.getByLabel("Cluster only").check();
    await dlg.getByTestId("deploy-submit").click();
    await expect(dlg).toBeHidden();
  }
  await expect(appRow(page, "alpha").getByTestId("btn-app-add-url")).toBeVisible();
  const expose = page.getByTestId("expose-dialog");
  await appRow(page, "alpha").getByTestId("btn-app-add-url").click();
  await expect(expose).toBeVisible();
  await expect(expose.getByTestId("expose-mode-loadbalancer")).toBeChecked();
  await expose.getByTestId("expose-hostport").fill("8080");
  await expose.getByTestId("expose-port").fill("3000");
  await expose.getByTestId("expose-submit").click();
  await expect(expose).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("alpha is now at http://localhost:8080");
  await expect(appRow(page, "alpha").getByTestId("app-url-link")).toHaveText("localhost:8080 ↗");
  // 同一個埠再加一次：順延並說明
  await appRow(page, "beta").getByTestId("btn-app-add-url").click();
  await expose.getByTestId("expose-hostport").fill("8080");
  await expose.getByTestId("expose-port").fill("3000");
  await expose.getByTestId("expose-submit").click();
  await expect(page.getByTestId("notice")).toContainText("http://localhost:8081");
  await expect(page.getByTestId("notice")).toContainText("port 8080 was in use");
});

test("Change URL switches an exposed app from NodePort to a fixed localhost port", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  const row = appRow(page, "poc-web");
  await row.getByTestId("btn-app-edit-url").click();
  const expose = page.getByTestId("expose-dialog");
  await expect(expose.getByTestId("expose-title")).toContainText("Change the URL of poc-web");
  await expect(expose.getByTestId("expose-mode-nodeport")).toBeChecked();
  await expose.getByTestId("expose-mode-loadbalancer").check();
  await expose.getByTestId("expose-hostport").fill("9090");
  await expose.getByTestId("expose-port").fill("8080");
  await expose.getByTestId("expose-submit").click();
  await expect(expose).toBeHidden();
  await expect(row.getByTestId("app-url-link")).toHaveCount(1);
  await expect(row.getByTestId("app-url-link")).toHaveText("localhost:9090 ↗");
});

test("Delete app removes the Deployment and the Service that points at it, after confirmation", async ({ page }) => {
  await goK8s(page, "apps");
  await startCluster(page);
  const row = appRow(page, "poc-web");
  await row.getByTestId("btn-app-delete").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-cancel").click();
  await expect(row).toBeVisible();
  await row.getByTestId("btn-app-delete").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(row).toHaveCount(0, { timeout: 10_000 });
  await expect(page.getByTestId("k8s-deploy-empty")).toBeVisible();
  await page.getByTestId("k8s-view-namespaces").click();
  await expect(page.getByTestId("k8s-ns-card-default").getByTestId("k8s-ns-node-services")).toHaveText("1"); // 只剩內建的 kubernetes Service
});

test("Import: pick WSL, browse to a Compose file, preview shows services, warnings and masked env, then Deploy", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("btn-k8s-import").click();
  const dlg = page.getByTestId("import-dialog");
  await expect(dlg).toBeVisible();
  // 預設 WSL＋第一台 distro，從家目錄開始
  await expect(dlg.getByTestId("import-kind")).toContainText("WSL");
  await expect(dlg.getByTestId("import-source")).toContainText("Ubuntu-26.04");
  await expect(dlg.getByTestId("import-owner-hint")).toContainText("Kubernetes runs in Ubuntu-26.04");
  await expect(dlg.getByTestId("import-path")).toHaveText("/home/irice");
  await dlg.getByTestId("import-item-projects").click();
  await dlg.getByTestId("import-item-shop").click();
  // 隱藏檔（.env）不列出，只有資料夾與 yaml
  await expect(dlg.getByTestId("import-item-.env")).toHaveCount(0);
  await expect(dlg.getByTestId("import-item-api")).toBeVisible();
  await expect(dlg.getByTestId("import-next")).toBeDisabled();
  await dlg.getByTestId("import-item-docker-compose.yml").click();
  await dlg.getByTestId("import-next").click();

  await expect(dlg.getByTestId("import-stack-name")).toHaveValue("shop");
  await expect(dlg.getByTestId("import-read")).toContainText(".env");
  await expect(dlg.getByTestId("import-order")).toContainText("db → api → web");
  await expect(dlg.getByTestId("import-svc-web")).toContainText("localhost:3000");
  await expect(dlg.getByTestId("import-svc-db")).toContainText("2 volume");
  await expect(dlg.getByTestId("import-warnings")).toContainText("healthcheck");
  await expect(dlg.getByTestId("import-warnings")).toContainText("bind mount ~/cache"); // ~ 無法對應；./init 則轉成 hostPath
  await expect(dlg.getByTestId("import-plain-env")).toBeVisible();
  await dlg.getByTestId("import-view-yaml").click();
  await expect(dlg.getByTestId("import-yaml")).toContainText("POSTGRES_PASSWORD");
  await expect(dlg.getByTestId("import-yaml")).not.toContainText("s3cret"); // 機密值遮蔽
  await expect(dlg.getByTestId("import-yaml")).toContainText('"path": "/home/irice/projects/shop/init"'); // bind mount → hostPath
  await expect(dlg.getByTestId("import-yaml")).toContainText('"workingDir": "/app"');

  // 改 namespace 會重新轉換
  await dlg.getByTestId("import-namespace").fill("shop-dev");
  await dlg.getByTestId("import-namespace").blur();
  await expect(dlg.getByTestId("import-yaml")).toContainText('"namespace": "shop-dev"');

  await dlg.getByTestId("import-submit").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("Stack shop deployed (3 services)");
  await expect(page.getByTestId("notice")).toContainText("web → http://localhost:3000");
  await page.getByTestId("k8s-view-namespaces").click();
  await expect(page.getByTestId("k8s-ns-card-shop-dev")).toBeVisible();
});

test("Import: a Kubernetes manifest is applied instead of converted; Windows is a separate location with drives", async ({ page }) => {
  await goK8s(page);
  await startCluster(page);
  await page.getByTestId("btn-k8s-import").click();
  const dlg = page.getByTestId("import-dialog");
  await dlg.getByTestId("import-item-projects").click();
  await dlg.getByTestId("import-item-shop").click();
  await dlg.getByTestId("import-item-k8s-extra.yaml").dblclick(); // 雙擊檔案等同下一步
  await expect(dlg.getByTestId("import-manifest")).toBeVisible();
  await expect(dlg.getByTestId("import-stack-name")).toBeHidden();
  await dlg.getByTestId("import-back").click();

  await dlg.getByTestId("import-kind").click();
  await page.getByTestId("import-kind-list").getByText("Windows").click();
  await expect(dlg.getByTestId("import-source")).toContainText("C:\\");
  await expect(dlg.getByTestId("import-path")).toHaveText("C:\\Users\\irice");
  await dlg.getByTestId("import-item-projects").click();
  await expect(dlg.getByTestId("import-path")).toHaveText("C:\\Users\\irice\\projects");
  await dlg.getByTestId("import-up").click();
  await expect(dlg.getByTestId("import-path")).toHaveText("C:\\Users\\irice");
  // 下次開啟記住上次的位置
  await dlg.getByTestId("import-cancel").click();
  await page.getByTestId("btn-k8s-import").click();
  await expect(dlg.getByTestId("import-kind")).toContainText("Windows");
});
