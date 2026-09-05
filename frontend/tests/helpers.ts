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
