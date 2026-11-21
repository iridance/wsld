import { expect, type Page, type APIRequestContext } from "@playwright/test";

export async function reset(request: APIRequestContext): Promise<void> {
  const r = await request.post("/__test/reset");
  expect(r.ok()).toBeTruthy();
}

export async function engine(request: APIRequestContext, state: "up" | "down"): Promise<void> {
  expect((await request.post(`/__test/engine/${state}`)).ok()).toBeTruthy();
}

/** 讓 fake 的 stop/start 延遲 ms 毫秒回應（模擬 docker stop 寬限期）。 */
export async function actionDelay(request: APIRequestContext, ms: number): Promise<void> {
  expect((await request.post(`/__test/action-delay?ms=${ms}`)).ok()).toBeTruthy();
}

/** 讓下一次 ListImages 快照後阻塞，直到 listRelease（測刷新競態）。 */
export async function listHold(request: APIRequestContext): Promise<void> {
  expect((await request.post("/__test/list-hold")).ok()).toBeTruthy();
}

export async function listRelease(request: APIRequestContext): Promise<void> {
  expect((await request.post("/__test/list-release")).ok()).toBeTruthy();
}

export async function waitListHeld(request: APIRequestContext): Promise<void> {
  await expect.poll(async () => {
    const r = await request.get("/__test/list-held");
    expect(r.ok()).toBeTruthy();
    return ((await r.json()) as { waiting: boolean }).waiting;
  }).toBe(true);
}

export async function lifecycle(request: APIRequestContext, state: "ok" | "broken"): Promise<void> {
  expect((await request.post(`/__test/lifecycle/${state}`)).ok()).toBeTruthy();
}

export async function pushLog(request: APIRequestContext, name: string, line: string): Promise<void> {
  expect((await request.post(`/__test/log?name=${name}&line=${encodeURIComponent(line)}`)).ok()).toBeTruthy();
}

export async function setLang(request: APIRequestContext, lang: "en" | "zh-TW"): Promise<void> {
  expect((await request.post(`/__test/lang/${lang}`)).ok()).toBeTruthy();
}

export async function open(page: Page): Promise<void> {
  await page.goto("/");
  await expect(page.getByTestId("status-connection")).toBeVisible();
}

export async function addContainer(request: APIRequestContext, name: string, image: string, state = "running"): Promise<void> {
  expect((await request.post(`/__test/container?name=${encodeURIComponent(name)}&image=${encodeURIComponent(image)}&state=${state}`)).ok()).toBeTruthy();
}

export async function addComposeContainer(request: APIRequestContext, project: string, service: string, image: string, state = "running"): Promise<void> {
  expect((await request.post(`/__test/container?project=${project}&service=${service}&image=${encodeURIComponent(image)}&state=${state}`)).ok()).toBeTruthy();
}

export function group(page: Page, project: string) {
  return page.locator(`[data-testid="group-row"][data-project="${project}"]`);
}

export function row(page: Page, name: string) {
  return page.locator(`[data-testid="container-row"][data-name="${name}"]`);
}

/** k8s 假叢集：delay＝每個動作延遲 ms、fail＝啟動失敗、installed＝預先標記該 distro 已安裝 k3s。 */
export async function k8sScenario(request: APIRequestContext, opts: { delay?: number; fail?: boolean; installed?: string } = {}): Promise<void> {
  const q = new URLSearchParams();
  if (opts.delay) q.set("delay", String(opts.delay));
  if (opts.fail) q.set("fail", "1");
  if (opts.installed) q.set("installed", opts.installed);
  expect((await request.post(`/__test/k8s?${q.toString()}`)).ok()).toBeTruthy();
}

export async function k8sState(request: APIRequestContext): Promise<{ running: string; calls: string[] }> {
  const r = await request.get("/__test/k8s");
  expect(r.ok()).toBeTruthy();
  return (await r.json()) as { running: string; calls: string[] };
}
