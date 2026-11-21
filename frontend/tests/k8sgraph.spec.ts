import { test, expect } from "@playwright/test";
import { buildNamespaceGraph, summarizeNamespace, type K8sResources } from "../src/k8sGraph";
import type { K8sDeployment, K8sPod, K8sService } from "../src/api";

const t0 = "2026-11-21T06:00:00Z";
const pod = (name: string, ns: string, labels: Record<string, string>, status = "Running", ready = "1/1", extra: Partial<K8sPod> = {}): K8sPod => ({ name, namespace: ns, status, ready, restarts: 0, labels, created_at: t0, ...extra });
const dep = (name: string, ns: string, selector: Record<string, string>, ready = "1/1"): K8sDeployment => ({ name, namespace: ns, ready, replicas: 1, up_to_date: 1, available: 1, selector, created_at: t0 });
const svc = (name: string, ns: string, selector?: Record<string, string>): K8sService => ({ name, namespace: ns, type: "ClusterIP", cluster_ip: "10.0.0.1", ports: [], selector, created_at: t0 });
const res = (r: Partial<K8sResources>): K8sResources => ({ pods: [], deployments: [], services: [], statefulsets: [], ingresses: [], configmaps: [], pvcs: [], ...r });

test("summary counts only the namespace; workloads = deployments + statefulsets", () => {
  const s = summarizeNamespace(
    "shop",
    res({
      pods: [pod("a", "shop", {}), pod("b", "shop", {}, "Running", "0/1"), pod("x", "other", {})],
      deployments: [dep("web", "shop", {})],
      statefulsets: [{ name: "pg", namespace: "shop", ready: "1/1", replicas: 1, created_at: t0 }],
      services: [svc("web", "shop"), svc("o", "other")],
      ingresses: [{ name: "i", namespace: "shop", rules: [], created_at: t0 }],
      configmaps: [{ name: "c", namespace: "shop", keys: 1, created_at: t0 }],
      pvcs: [{ name: "p", namespace: "shop", status: "Bound", created_at: t0 }],
    }),
  );
  expect(s).toMatchObject({ pods: 2, podsReady: 1, workloads: 2, services: 1, ingresses: 1, configMaps: 1, pvcs: 1, health: "warn" });
});

test("health: failing pod → error, all ready → ok, nothing → empty", () => {
  expect(summarizeNamespace("n", res({ pods: [pod("a", "n", {}, "CrashLoopBackOff", "0/1")] })).health).toBe("error");
  expect(summarizeNamespace("n", res({ pods: [pod("a", "n", {})] })).health).toBe("ok");
  expect(summarizeNamespace("n", res({ pods: [pod("a", "n", {}, "Pending", "0/1")] })).health).toBe("warn");
  expect(summarizeNamespace("n", res({})).health).toBe("empty");
});

test("graph links services → deployments by selector and deployments → pods by labels", () => {
  const g = buildNamespaceGraph(
    "shop",
    res({
      services: [svc("web-svc", "shop", { app: "web" }), svc("api-svc", "shop", { app: "api" }), svc("headless", "shop")],
      deployments: [dep("web", "shop", { app: "web" }), dep("api", "shop", { app: "api" })],
      pods: [pod("web-1", "shop", { app: "web" }), pod("web-2", "shop", { app: "web", extra: "1" }), pod("api-1", "shop", { app: "api" }), pod("job-1", "shop", { job: "x" })],
    }),
  );
  expect(g.edges).toEqual(
    expect.arrayContaining([
      { from: "svc:web-svc", to: "dep:web" },
      { from: "svc:api-svc", to: "dep:api" },
    ]),
  );
  expect(g.edges.filter((e) => e.from.startsWith("svc:headless"))).toHaveLength(0);
  expect(g.columns.map((c) => [c.workload?.name, c.pods.map((p) => p.name)])).toEqual([
    ["web", ["web-1", "web-2"]],
    ["api", ["api-1"]],
    [undefined, ["job-1"]],
  ]);
  expect(g.services.map((s) => s.name)).toEqual(["web-svc", "api-svc", "headless"]);
});

test("a service with several matching deployments links to each; empty selectors never match", () => {
  const g = buildNamespaceGraph("n", res({ services: [svc("s", "n", { app: "x" }), svc("none", "n", {})], deployments: [dep("a", "n", { app: "x" }), dep("b", "n", { app: "x", v: "2" })] }));
  expect(g.edges.map((e) => `${e.from}>${e.to}`).sort()).toEqual(["svc:s>dep:a", "svc:s>dep:b"]);
});

test("statefulsets are workload columns too, linked from services by selector", () => {
  const g = buildNamespaceGraph(
    "n",
    res({
      services: [svc("pg", "n", { app: "pg" })],
      statefulsets: [{ name: "pg", namespace: "n", ready: "1/1", replicas: 1, selector: { app: "pg" }, image: "postgres:16", created_at: t0 }],
      pods: [pod("pg-0", "n", { app: "pg" })],
    }),
  );
  expect(g.columns).toHaveLength(1);
  expect(g.columns[0].workload).toMatchObject({ kind: "StatefulSet", name: "pg", id: "sts:pg", image: "postgres:16" });
  expect(g.edges).toContainEqual({ from: "svc:pg", to: "sts:pg" });
});

test("ingress rules link to their services (deduplicated, with the paths)", () => {
  const g = buildNamespaceGraph(
    "shop",
    res({
      services: [svc("web", "shop", { app: "w" })],
      ingresses: [{ name: "shop", namespace: "shop", rules: [{ host: "a", path: "/", service: "web", port: "80" }, { host: "a", path: "/x", service: "web", port: "80" }, { host: "a", path: "/gone", service: "missing", port: "80" }], created_at: t0 }],
    }),
  );
  expect(g.ingresses.map((i) => i.name)).toEqual(["shop"]);
  expect(g.edges.filter((e) => e.from === "ing:shop")).toEqual([{ from: "ing:shop", to: "svc:web", label: "a/, a/x" }]); // 不存在的 Service 不畫線
});

test("config maps and pvcs used by pods become mount edges into that workload's pod group", () => {
  const g = buildNamespaceGraph(
    "n",
    res({
      deployments: [dep("api", "n", { app: "api" })],
      pods: [pod("api-1", "n", { app: "api" }, "Running", "1/1", { config_maps: ["env"], pvcs: ["data"] }), pod("api-2", "n", { app: "api" }, "Running", "1/1", { config_maps: ["env"] })],
      configmaps: [{ name: "env", namespace: "n", keys: 3, created_at: t0 }, { name: "unused", namespace: "n", keys: 1, created_at: t0 }],
      pvcs: [{ name: "data", namespace: "n", status: "Bound", created_at: t0 }],
    }),
  );
  expect(g.mounts.map((m) => m.id).sort()).toEqual(["cm:env", "cm:unused", "pvc:data"]);
  const edges = g.edges.filter((e) => e.mount).map((e) => `${e.from}>${e.to}`).sort();
  expect(edges).toEqual(["cm:env>pods:api", "pvc:data>pods:api"]); // 沒人用的 ConfigMap 只出現、不連線
});

test("summary lists the NodePorts a namespace exposes (sorted, TCP only, deduplicated)", () => {
  const np = (name: string, ports: { port: number; node_port?: number; protocol: string }[]): K8sService => ({ ...svc(name, "n"), type: "NodePort", ports });
  const s = summarizeNamespace("n", res({ services: [np("a", [{ port: 80, node_port: 30090, protocol: "TCP" }]), np("b", [{ port: 81, node_port: 30080, protocol: "TCP" }, { port: 53, node_port: 30053, protocol: "UDP" }]), np("c", [{ port: 80, node_port: 30090, protocol: "TCP" }])] }));
  expect(s.nodePorts).toEqual([30080, 30090]);
  expect(summarizeNamespace("n", res({ services: [svc("x", "n")] })).nodePorts).toEqual([]);
});
