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
  await expect(page.getByTestId("k8s-pod-row")).toHaveCount(2); // kube-system 預設隱藏
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
  await expect(tools).toHaveCount(7); // 同 Docker engine 卡（關閉＋Info／資料夾／終端／釋放磁碟空間）＋重新啟動、重置叢集
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

async function goReady(page: Page, request: APIRequestContext) {
  await k8s(request, "installed=1&running=1&pods=1");
  await goK8s(page);
  await expect(page.getByTestId("k8s-card")).toBeVisible();
  await expect(page.getByTestId("k8s-pod-row").first()).toBeVisible();
}

test("Pods are grouped by namespace; system namespaces are hidden until toggled", async ({ page, request }) => {
  await goReady(page, request);
  const pods = page.getByTestId("k8s-pods");
  await expect(pods.getByTestId("k8s-ns-row")).toHaveCount(1);
  await expect(pods.locator('[data-testid="k8s-ns-row"][data-namespace="default"]')).toBeVisible();
  await page.getByTestId("k8s-show-system").check();
  await expect(pods.getByTestId("k8s-ns-row")).toHaveCount(2);
  await expect(page.getByTestId("k8s-pod-row")).toHaveCount(3);
  // 收合 namespace
  const ns = pods.locator('[data-testid="k8s-ns-row"][data-namespace="default"]');
  await ns.getByTestId("group-toggle").click();
  await expect(page.locator('[data-testid="k8s-pod-row"][data-namespace="default"]')).toHaveCount(0);
  await expect(ns).toHaveAttribute("aria-expanded", "false");
  // Services 分頁共用同一個開關
  await page.getByTestId("k8s-tab-services").click();
  await expect(page.locator('[data-testid="k8s-svc-row"][data-name="kube-dns"]')).toBeVisible();
  await page.getByTestId("k8s-show-system").uncheck();
  await expect(page.locator('[data-testid="k8s-svc-row"][data-name="kube-dns"]')).toHaveCount(0);
});

test("Pods and Services are separate tabs; the choice is remembered and counts follow the system toggle", async ({ page, request }) => {
  await goReady(page, request);
  await expect(page.getByTestId("k8s-tab-pods")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByTestId("k8s-tab-pods").locator(".ui-tab-count")).toHaveText("2");
  await expect(page.getByTestId("k8s-tab-services").locator(".ui-tab-count")).toHaveText("3");
  await expect(page.getByTestId("page-sub")).toHaveText("2 pod(s) · 3 service(s)");
  await expect(page.getByTestId("k8s-services")).toHaveCount(0);
  await page.getByTestId("k8s-tab-services").click();
  await expect(page.getByTestId("k8s-services")).toBeVisible();
  await expect(page.getByTestId("k8s-pods")).toHaveCount(0);
  await expect(page.getByTestId("k8s-card")).toBeVisible(); // 叢集卡兩個分頁都有
  await page.getByTestId("k8s-show-system").check();
  await expect(page.getByTestId("k8s-tab-services").locator(".ui-tab-count")).toHaveText("4");
  await expect(page.getByTestId("page-sub")).toHaveText("3 pod(s) · 4 service(s)");
  await page.reload();
  await expect(page.getByTestId("k8s-tab-services")).toHaveAttribute("aria-selected", "true");
  await expect(page.getByTestId("k8s-services")).toBeVisible();
});

test("Pod rows show containers (init marked), labels, CPU and memory", async ({ page, request }) => {
  await goReady(page, request);
  const web = page.locator('[data-testid="k8s-pod-row"][data-name="web-747498c8dc-xxztk"]');
  await expect(web.getByTestId("k8s-pod-containers")).toHaveText("migrate (init) · nginx · sidecar");
  await expect(web.getByTestId("k8s-pod-labels")).toContainText("app=web");
  await expect(web.getByTestId("k8s-pod-labels")).not.toContainText("pod-template-hash");
  await expect(web.getByTestId("k8s-pod-cpu")).toHaveText("1.5%");
  await expect(web.getByTestId("k8s-pod-mem")).toHaveText(/24/);
  const worker = page.locator('[data-testid="k8s-pod-row"][data-name="worker-5d9f7c6b4-q2wkp"]');
  await expect(worker.getByTestId("k8s-pod-cpu")).toHaveText("–");
  // CrashLoopBackOff 的容器沒在跑：不能開終端，但可以看日誌
  await expect(worker.locator('[data-testid^="btn-k8s-term-"]')).toBeDisabled();
  await expect(worker.locator('[data-testid^="btn-k8s-logs-"]')).toBeEnabled();
});

test("Pod logs and terminal pick a container when the pod has several", async ({ page, request }) => {
  await goReady(page, request);
  await page.getByTestId("btn-k8s-logs-web-747498c8dc-xxztk").click();
  const menu = page.getByTestId("k8s-container-menu");
  await expect(menu.getByTestId("k8s-container-item")).toHaveText(["migrate (init)", "nginx", "sidecar"]);
  await menu.locator('[data-container="nginx"]').click();
  await expect(page.getByTestId("panel")).toBeVisible();
  await expect(page.getByTestId("panel")).toContainText("nginx · web-747498c8dc-xxztk (default)");
  await page.getByTestId("btn-k8s-term-web-747498c8dc-xxztk").click();
  // terminal 只列執行中的容器（init 已結束）
  await expect(page.getByTestId("k8s-container-menu").getByTestId("k8s-container-item")).toHaveText(["nginx", "sidecar"]);
  // 單一容器的 pod 直接開
  await page.keyboard.press("Escape");
  await expect(page.getByTestId("k8s-container-menu")).toHaveCount(0);
  await page.getByTestId("btn-k8s-logs-worker-5d9f7c6b4-q2wkp").click();
  await expect(page.getByTestId("panel")).toContainText("worker · worker-5d9f7c6b4-q2wkp (default)");
});

test("Deleting a pod asks first and removes it", async ({ page, request }) => {
  await goReady(page, request);
  await page.getByTestId("btn-k8s-delete-worker-5d9f7c6b4-q2wkp").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("default/worker-5d9f7c6b4-q2wkp");
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.locator('[data-testid="k8s-pod-row"][data-name="worker-5d9f7c6b4-q2wkp"]')).toHaveCount(0);
});

test("Services are grouped by namespace with their backing pods and the addresses Windows can open", async ({ page, request }) => {
  await goReady(page, request);
  await page.getByTestId("k8s-tab-services").click();
  const svcs = page.getByTestId("k8s-services");
  await expect(svcs.getByTestId("k8s-ns-row")).toHaveCount(1);
  await expect(svcs.locator('[data-testid="k8s-ns-row"][data-namespace="default"]').getByTestId("group-count")).toHaveText("3");
  await expect(page.getByTestId("k8s-svc-row")).toHaveCount(3);
  const web = page.locator('[data-testid="k8s-svc-row"][data-name="web"]');
  await expect(web.getByTestId("k8s-svc-type")).toHaveText("LoadBalancer");
  await expect(web.getByTestId("k8s-svc-ports")).toHaveText("8080:31080/TCP");
  await expect(web.getByTestId("k8s-svc-ports")).toHaveAttribute("title", "8080 → 80 (NodePort 31080)/TCP");
  await expect(web.getByTestId("k8s-svc-url")).toHaveText("localhost:8080");
  // 後端 pods：selector app=web 選到 1 個 Ready 的 pod；worker 的 pod 在 CrashLoopBackOff
  await expect(web.getByTestId("k8s-svc-pods")).toHaveText("1/1");
  await expect(web.getByTestId("k8s-svc-pods")).toHaveClass(/running/);
  const worker = page.locator('[data-testid="k8s-svc-row"][data-name="worker"]');
  await expect(worker.getByTestId("k8s-svc-pods")).toHaveText("0/1");
  await expect(worker.getByTestId("k8s-svc-pods")).toHaveClass(/failed/);
  await expect(worker.getByTestId("k8s-svc-access")).toHaveText("Cluster only");
  // kubernetes service 沒有 selector
  await expect(page.locator('[data-testid="k8s-svc-row"][data-name="kubernetes"]').getByTestId("k8s-svc-pods")).toHaveText("–");
  // 點後端 pods 跳到 Pods 分頁
  await web.getByTestId("k8s-svc-pods").click();
  await expect(page.getByTestId("k8s-tab-pods")).toHaveAttribute("aria-selected", "true");
  await expect(page.locator('[data-testid="k8s-pod-row"][data-name="web-747498c8dc-xxztk"]')).toBeVisible();
});

test("Forwarding a ClusterIP service adds a localhost address to its row and to Port forwards", async ({ page, request }) => {
  await goReady(page, request);
  await page.getByTestId("k8s-tab-services").click();
  const worker = page.locator('[data-testid="k8s-svc-row"][data-name="worker"]');
  await expect(page.getByTestId("k8s-forwards")).toBeHidden();
  await worker.getByTestId("btn-k8s-forward-svc-worker").click();
  const dlg = page.getByTestId("k8s-forward-dialog");
  await expect(dlg.getByTestId("k8s-forward-title")).toContainText("default/svc/worker");
  await expect(dlg.getByTestId("k8s-forward-remote")).toHaveValue("9000");
  await expect(dlg.getByTestId("k8s-forward-local")).toHaveValue("9000");
  await dlg.getByTestId("k8s-forward-local").fill("19000");
  await dlg.getByTestId("k8s-forward-apply").click();
  await expect(dlg).toBeHidden();
  await expect(worker.getByTestId("k8s-svc-fwd-url")).toHaveText("localhost:19000");
  const fwd = page.locator('[data-testid="k8s-fwd-row"][data-port="19000"]');
  await expect(fwd).toBeVisible();
  await expect(fwd).toContainText("default/svc/worker:9000");
  await fwd.getByTestId("btn-k8s-fwd-stop-19000").click();
  await expect(page.getByTestId("k8s-forwards")).toBeHidden();
  await expect(worker.getByTestId("k8s-svc-access")).toHaveText("Cluster only");
});

test("Restart walks its steps; Reset cluster asks first and clears the pods", async ({ page, request }) => {
  await goReady(page, request);
  await k8s(request, "delay=200");
  await page.getByTestId("btn-k8s-restart").click();
  const progress = page.getByTestId("k8s-progress");
  await expect(progress).toHaveAttribute("data-op", "restarting");
  await expect(progress.getByTestId("k8s-step-stop")).toBeVisible();
  await expect(progress.getByTestId("k8s-step-bootstrap")).toHaveCount(0);
  await expect(page.getByTestId("k8s-card")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("k8s-pod-row")).toHaveCount(2);

  await page.getByTestId("btn-k8s-reset").click();
  const dlg = page.getByTestId("confirm-dialog");
  await expect(dlg.getByTestId("confirm-text")).toContainText("persistent volume");
  await dlg.getByTestId("confirm-ok").click();
  await expect(page.getByTestId("k8s-progress")).toHaveAttribute("data-op", "resetting");
  await expect(page.getByTestId("k8s-step-wipe")).toBeVisible();
  await expect(page.getByTestId("k8s-card")).toBeVisible({ timeout: 15_000 });
  await expect(page.getByTestId("k8s-pods-empty")).toBeVisible();
});

test("Info merges the wsld context into ~/.kube/config", async ({ page, request }) => {
  await k8s(request, "delay=0&pods=1");
  await goK8s(page);
  await page.getByTestId("btn-k8s-enable").click(); // 開啟時寫出 Windows 端 kubeconfig
  await expect(page.getByTestId("k8s-card")).toBeVisible({ timeout: 15_000 });
  await page.getByTestId("btn-machine-config-k8s-engine").click();
  const drawer = page.getByTestId("info-drawer");
  await expect(drawer.getByTestId("k8s-current-context")).toHaveAttribute("data-context", "");
  await drawer.getByTestId("btn-k8s-merge").click();
  await expect(drawer.getByTestId("k8s-current-context")).toHaveAttribute("data-context", "wsld");
  await expect(drawer.getByTestId("btn-k8s-merge")).toHaveCount(0);
});

test("Apply YAML: pasted manifest is applied and the output stays in the dialog; Compose files are refused", async ({ page, request }) => {
  await goReady(page, request);
  await page.getByTestId("btn-k8s-apply").click();
  const dlg = page.getByTestId("apply-dialog");
  await dlg.getByTestId("apply-submit").click();
  await expect(dlg.getByTestId("apply-error")).toContainText("Paste a manifest");
  await dlg.getByTestId("apply-text").fill("services:\n  web:\n    image: nginx\n");
  await dlg.getByTestId("apply-submit").click();
  await expect(dlg.getByTestId("apply-error")).toContainText("Import");
  await dlg.getByTestId("apply-text").fill("fail-apply");
  await dlg.getByTestId("apply-submit").click();
  await expect(dlg.getByTestId("apply-error")).toContainText("fail-apply");
  await dlg.getByTestId("apply-text").fill("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: x\n");
  await dlg.getByTestId("apply-submit").click();
  await expect(dlg.getByTestId("apply-output")).toHaveText("deployment.apps/applied created");
  await expect(page.getByTestId("notice")).toContainText("Applied");
  await expect(page.getByTestId("btn-k8s-deploy")).toHaveCount(0); // 只有 Import 與 Apply YAML
  await dlg.getByTestId("apply-cancel").click();
  await expect(dlg).toBeHidden();
});

test("Import: browse WSL to a Compose file, preview services and warnings, deploy the stack, then delete it", async ({ page, request }) => {
  await goReady(page, request);
  await page.getByTestId("btn-k8s-import").click();
  const dlg = page.getByTestId("import-dialog");
  await expect(dlg).toBeVisible();
  await expect(dlg.getByTestId("import-kind")).toContainText("WSL");
  await expect(dlg.getByTestId("import-source")).toContainText("Ubuntu-26.04");
  await expect(dlg.getByTestId("import-source")).not.toContainText("k8s-engine");
  await expect(dlg.getByTestId("import-engine-hint")).toBeVisible();
  await expect(dlg.getByTestId("import-path")).toHaveText("/home/irice");
  await dlg.getByTestId("import-item-projects").click();
  await dlg.getByTestId("import-item-shop").click();
  await expect(dlg.getByTestId("import-item-.env")).toHaveCount(0);
  await expect(dlg.getByTestId("import-next")).toBeDisabled();
  await dlg.getByTestId("import-item-docker-compose.yml").click();
  await dlg.getByTestId("import-next").click();

  await expect(dlg.getByTestId("import-stack-name")).toHaveValue("shop");
  await expect(dlg.getByTestId("import-read")).toContainText(".env");
  await expect(dlg.getByTestId("import-order")).toContainText("db → api → web");
  await expect(dlg.getByTestId("import-warnings")).toContainText("healthcheck");
  await dlg.getByTestId("import-view-yaml").click();
  await expect(dlg.getByTestId("import-yaml")).toContainText("POSTGRES_PASSWORD");
  await expect(dlg.getByTestId("import-yaml")).not.toContainText("s3cret");
  await expect(dlg.getByTestId("import-yaml")).toContainText("/home/irice/projects/shop/init"); // bind mount → hostPath
  await dlg.getByTestId("import-submit").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("Stack shop deployed (3 services)");
  const ns = page.locator('[data-testid="k8s-ns-row"][data-namespace="shop"]');
  await expect(ns).toBeVisible();
  await ns.getByTestId("btn-k8s-stack-delete-shop").click();
  await page.getByTestId("confirm-dialog").getByTestId("confirm-ok").click();
  await expect(ns).toHaveCount(0);
});

test("Import: a Kubernetes manifest is applied instead of converted; Windows drives are a separate location", async ({ page, request }) => {
  await goReady(page, request);
  await page.getByTestId("btn-k8s-import").click();
  const dlg = page.getByTestId("import-dialog");
  await dlg.getByTestId("import-item-projects").click();
  await dlg.getByTestId("import-item-shop").click();
  await dlg.getByTestId("import-item-k8s-extra.yaml").dblclick();
  await expect(dlg.getByTestId("import-manifest")).toBeVisible();
  await dlg.getByTestId("import-submit").click();
  await expect(dlg).toBeHidden();
  await expect(page.getByTestId("notice")).toContainText("Applied");
  await page.getByTestId("btn-k8s-import").click();
  await dlg.getByTestId("import-kind").click();
  await page.getByTestId("import-kind-list").getByText("Windows").click();
  await expect(dlg.getByTestId("import-path")).toHaveText("C:\\Users\\irice");
});
