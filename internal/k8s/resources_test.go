package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const podsJSON = `{"items":[
 {"metadata":{"name":"web-1","namespace":"default","creationTimestamp":"2026-11-21T06:00:00Z"},
  "spec":{"nodeName":"framework"},
  "status":{"phase":"Running","podIP":"10.42.0.13",
   "containerStatuses":[{"ready":true,"restartCount":2,"state":{"running":{}}},{"ready":false,"restartCount":1,"state":{"running":{}}}]}},
 {"metadata":{"name":"crash","namespace":"default","creationTimestamp":"2026-11-21T06:00:00Z"},
  "spec":{"nodeName":"framework"},
  "status":{"phase":"Running","containerStatuses":[{"ready":false,"restartCount":7,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},
 {"metadata":{"name":"bye","namespace":"kube-system","creationTimestamp":"2026-11-21T06:00:00Z","deletionTimestamp":"2026-11-21T06:30:00Z"},
  "spec":{"nodeName":"framework"},
  "status":{"phase":"Running","containerStatuses":[{"ready":true,"restartCount":0,"state":{"running":{}}}]}},
 {"metadata":{"name":"pending","namespace":"default","creationTimestamp":"2026-11-21T06:00:00Z"},
  "spec":{},"status":{"phase":"Pending"}}
]}`

func TestParsePods(t *testing.T) {
	ps, err := ParsePods(podsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 4 {
		t.Fatalf("len = %d", len(ps))
	}
	w := ps[0]
	if w.Name != "web-1" || w.Namespace != "default" || w.Status != "Running" || w.Ready != "1/2" || w.Restarts != 3 || w.Node != "framework" || w.IP != "10.42.0.13" {
		t.Fatalf("web-1 = %+v", w)
	}
	if want := time.Date(2026, 11, 21, 6, 0, 0, 0, time.UTC); !w.CreatedAt.Equal(want) {
		t.Fatalf("CreatedAt = %v", w.CreatedAt)
	}
	if ps[1].Status != "CrashLoopBackOff" || ps[1].Restarts != 7 {
		t.Fatalf("crash = %+v", ps[1])
	}
	if ps[2].Status != "Terminating" {
		t.Fatalf("deleting pod status = %q", ps[2].Status)
	}
	if ps[3].Status != "Pending" || ps[3].Ready != "0/0" {
		t.Fatalf("pending = %+v", ps[3])
	}
}

func TestParseDeployments(t *testing.T) {
	js := `{"items":[{"metadata":{"name":"web","namespace":"default","creationTimestamp":"2026-11-21T06:00:00Z"},
	 "spec":{"replicas":3},"status":{"readyReplicas":2,"updatedReplicas":3,"availableReplicas":2}},
	 {"metadata":{"name":"idle","namespace":"default","creationTimestamp":"2026-11-21T06:00:00Z"},
	 "spec":{"replicas":0},"status":{}}]}`
	ds, err := ParseDeployments(js)
	if err != nil {
		t.Fatal(err)
	}
	if ds[0].Ready != "2/3" || ds[0].UpToDate != 3 || ds[0].Available != 2 || ds[0].Replicas != 3 {
		t.Fatalf("web = %+v", ds[0])
	}
	if ds[1].Ready != "0/0" || ds[1].Replicas != 0 {
		t.Fatalf("idle = %+v", ds[1])
	}
}

func TestParseServices(t *testing.T) {
	js := `{"items":[
	 {"metadata":{"name":"poc","namespace":"default","creationTimestamp":"2026-11-21T06:00:00Z"},
	  "spec":{"type":"NodePort","clusterIP":"10.43.1.1","ports":[{"port":8080,"targetPort":8080,"nodePort":30080,"protocol":"TCP"}]}},
	 {"metadata":{"name":"dns","namespace":"kube-system","creationTimestamp":"2026-11-21T06:00:00Z"},
	  "spec":{"type":"ClusterIP","clusterIP":"10.43.0.10","ports":[{"name":"dns","port":53,"targetPort":"dns","protocol":"UDP"}]}}]}`
	ss, err := ParseServices(js)
	if err != nil {
		t.Fatal(err)
	}
	if ss[0].Type != "NodePort" || ss[0].ClusterIP != "10.43.1.1" || len(ss[0].Ports) != 1 || ss[0].Ports[0].NodePort != 30080 || ss[0].Ports[0].Port != 8080 {
		t.Fatalf("poc = %+v", ss[0])
	}
	if ss[1].Ports[0].Protocol != "UDP" || ss[1].Ports[0].NodePort != 0 {
		t.Fatalf("dns = %+v", ss[1])
	}
}

func TestParseNamespaces(t *testing.T) {
	ns, err := ParseNamespaces(`{"items":[{"metadata":{"name":"default"}},{"metadata":{"name":"kube-system"}}]}`)
	if err != nil || len(ns) != 2 || ns[0] != "default" || ns[1] != "kube-system" {
		t.Fatalf("%v %v", ns, err)
	}
}

func TestParseRejectsBadJSON(t *testing.T) {
	if _, err := ParsePods("nope"); err == nil {
		t.Fatal("want error")
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"default", "kube-system", "web-1", "a.b-c", "x"} {
		if !ValidName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-x", "x-", "A", "a b", "a;b", "--all", "../x", "a/b", strings.Repeat("a", 254)} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestListPodsAllNamespacesAndScoped(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl get pods -A -o json", resp{out: podsJSON})
	f.on(d+" k3s kubectl get pods -n default -o json", resp{out: `{"items":[]}`})
	m := newMgr(f)
	if ps, err := m.ListPods(context.Background(), ""); err != nil || len(ps) != 4 {
		t.Fatalf("%v %v", ps, err)
	}
	if ps, err := m.ListPods(context.Background(), "default"); err != nil || len(ps) != 0 {
		t.Fatalf("%v %v", ps, err)
	}
	if _, err := m.ListPods(context.Background(), "--all-namespaces"); err == nil {
		t.Fatal("invalid namespace must be rejected before running anything")
	}
}

func TestListDeploymentsServicesNamespaces(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl get deployments -A -o json", resp{out: `{"items":[]}`})
	f.on(d+" k3s kubectl get services -n kube-system -o json", resp{out: `{"items":[]}`})
	f.on(d+" k3s kubectl get namespaces -o json", resp{out: `{"items":[{"metadata":{"name":"default"}}]}`})
	m := newMgr(f)
	if _, err := m.ListDeployments(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ListServices(context.Background(), "kube-system"); err != nil {
		t.Fatal(err)
	}
	if ns, err := m.ListNamespaces(context.Background()); err != nil || len(ns) != 1 {
		t.Fatalf("%v %v", ns, err)
	}
}

func TestListErrorPropagates(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl get pods -A -o json", resp{err: errors.New("exit 1")})
	if _, err := newMgr(f).ListPods(context.Background(), ""); err == nil {
		t.Fatal("want error")
	}
}

func TestDeleteRestartScale(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl delete pod web-1 -n default", resp{})
	f.on(d+" k3s kubectl delete deployment web -n default", resp{})
	f.on(d+" k3s kubectl delete service poc -n default", resp{})
	f.on(d+" k3s kubectl rollout restart deployment/web -n default", resp{})
	f.on(d+" k3s kubectl scale deployment/web --replicas=3 -n default", resp{})
	m := newMgr(f)
	ctx := context.Background()
	for name, fn := range map[string]func() error{
		"pod":     func() error { return m.Delete(ctx, "pod", "default", "web-1") },
		"deploy":  func() error { return m.Delete(ctx, "deployment", "default", "web") },
		"service": func() error { return m.Delete(ctx, "service", "default", "poc") },
		"restart": func() error { return m.RestartDeployment(ctx, "default", "web") },
		"scale":   func() error { return m.ScaleDeployment(ctx, "default", "web", 3) },
	} {
		if err := fn(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMutationsValidateInputs(t *testing.T) {
	f := newFake()
	m := newMgr(f)
	ctx := context.Background()
	if err := m.Delete(ctx, "node", "default", "x"); err == nil {
		t.Error("only pod|deployment|service may be deleted")
	}
	if err := m.Delete(ctx, "pod", "default", "--all"); err == nil {
		t.Error("flag-like name must be rejected")
	}
	if err := m.Delete(ctx, "pod", "", "web"); err == nil {
		t.Error("namespace required")
	}
	if err := m.ScaleDeployment(ctx, "default", "web", -1); err == nil {
		t.Error("negative replicas must be rejected")
	}
	if err := m.ScaleDeployment(ctx, "default", "web", 1001); err == nil {
		t.Error("absurd replicas must be rejected")
	}
	if len(f.calls) != 0 {
		t.Fatalf("nothing may run on invalid input: %v", f.calls)
	}
}

func TestLogsAndExecCommands(t *testing.T) {
	got := LogsArgs("default", "web-1", "", 200, true)
	want := "k3s kubectl logs web-1 -n default --tail=200 --timestamps -f"
	if strings.Join(got, " ") != want {
		t.Fatalf("logs = %q", strings.Join(got, " "))
	}
	got = LogsArgs("default", "web-1", "side", 50, false)
	if strings.Join(got, " ") != "k3s kubectl logs web-1 -n default --tail=50 --timestamps -c side" {
		t.Fatalf("logs = %q", strings.Join(got, " "))
	}
	ex := ExecArgs("default", "web-1", "")
	if !strings.HasPrefix(strings.Join(ex, " "), "k3s kubectl exec -it web-1 -n default -- ") {
		t.Fatalf("exec = %q", strings.Join(ex, " "))
	}
	if ExecArgs("bad ns", "web-1", "") != nil || LogsArgs("default", "-x", "", 1, false) != nil {
		t.Fatal("invalid names must yield nil")
	}
}

func TestParseCarriesLabelsAndSelectorsForTheArchitectureView(t *testing.T) {
	ps, err := ParsePods(`{"items":[{"metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z","labels":{"app":"web","tier":"fe"}},"spec":{},"status":{"phase":"Running"}}]}`)
	if err != nil || len(ps) != 1 || ps[0].Labels["app"] != "web" || ps[0].Labels["tier"] != "fe" {
		t.Fatalf("pod labels = %+v err=%v", ps, err)
	}
	ds, err := ParseDeployments(`{"items":[{"metadata":{"name":"web","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},
	 "spec":{"replicas":2,"selector":{"matchLabels":{"app":"web"}},"template":{"spec":{"containers":[{"image":"shop/web:1.8.2"},{"image":"sidecar:1"}]}}},"status":{}}]}`)
	if err != nil || len(ds) != 1 || ds[0].Selector["app"] != "web" || ds[0].Image != "shop/web:1.8.2" {
		t.Fatalf("deployment = %+v err=%v", ds, err)
	}
	ss, err := ParseServices(`{"items":[{"metadata":{"name":"web-svc","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},"spec":{"type":"ClusterIP","clusterIP":"10.43.0.2","selector":{"app":"web"},"ports":[]}}]}`)
	if err != nil || len(ss) != 1 || ss[0].Selector["app"] != "web" {
		t.Fatalf("service selector = %+v err=%v", ss, err)
	}
}

func TestParsePodsCollectsConfigMapAndPVCReferences(t *testing.T) {
	ps, err := ParsePods(`{"items":[{"metadata":{"name":"api-1","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},
	 "spec":{"volumes":[{"name":"d","persistentVolumeClaim":{"claimName":"data"}},{"name":"c","configMap":{"name":"api-env"}},{"name":"x","emptyDir":{}}],
	  "containers":[{"envFrom":[{"configMapRef":{"name":"api-env"}},{"secretRef":{"name":"s"}}],"env":[{"name":"A","valueFrom":{"configMapKeyRef":{"name":"other","key":"k"}}}]}]},
	 "status":{"phase":"Running"}}]}`)
	if err != nil || len(ps) != 1 {
		t.Fatal(err)
	}
	if got := strings.Join(ps[0].ConfigMaps, ","); got != "api-env,other" {
		t.Fatalf("configmaps = %q", got)
	}
	if got := strings.Join(ps[0].PVCs, ","); got != "data" {
		t.Fatalf("pvcs = %q", got)
	}
}

func TestParseDeploymentStrategy(t *testing.T) {
	ds, err := ParseDeployments(`{"items":[{"metadata":{"name":"w","namespace":"n","creationTimestamp":"2026-11-21T06:00:00Z"},
	 "spec":{"replicas":1,"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxSurge":"25%","maxUnavailable":1}}},"status":{}}]}`)
	if err != nil || ds[0].Strategy != "RollingUpdate" || ds[0].MaxSurge != "25%" || ds[0].MaxUnavailable != "1" {
		t.Fatalf("%+v %v", ds, err)
	}
}

const extrasJSON = `{"items":[
 {"kind":"StatefulSet","metadata":{"name":"pg","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},
  "spec":{"replicas":2,"selector":{"matchLabels":{"app":"pg"}},"template":{"spec":{"containers":[{"image":"postgres:16"}]}}},"status":{"readyReplicas":1}},
 {"kind":"Ingress","metadata":{"name":"shop","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},
  "spec":{"rules":[{"host":"shop.local","http":{"paths":[{"path":"/","backend":{"service":{"name":"web-svc","port":{"number":80}}}},{"path":"/api","backend":{"service":{"name":"api-svc","port":{"name":"http"}}}}]}}]}},
 {"kind":"ConfigMap","metadata":{"name":"api-env","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},"data":{"A":"1","B":"2"},"binaryData":{"C":"x"}},
 {"kind":"ConfigMap","metadata":{"name":"kube-root-ca.crt","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},"data":{"ca.crt":"x"}},
 {"kind":"PersistentVolumeClaim","metadata":{"name":"data","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},
  "spec":{"storageClassName":"local-path"},"status":{"phase":"Bound","capacity":{"storage":"20Gi"}}},
 {"kind":"Lease","metadata":{"name":"framework","namespace":"kube-node-lease","creationTimestamp":"2026-11-21T06:00:00Z"}},
 {"kind":"Role","metadata":{"name":"system:controller:bootstrap-signer","namespace":"kube-public","creationTimestamp":"2026-11-21T06:00:00Z"}},
 {"kind":"ServiceAccount","metadata":{"name":"default","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"}},
 {"kind":"ServiceAccount","metadata":{"name":"deployer","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"}},
 {"kind":"Secret","metadata":{"name":"tls","namespace":"shop","creationTimestamp":"2026-11-21T06:00:00Z"},"data":{"tls.key":"c2VjcmV0"}}
]}`

func TestParseExtras(t *testing.T) {
	x, err := ParseExtras(extrasJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.StatefulSets) != 1 || x.StatefulSets[0].Ready != "1/2" || x.StatefulSets[0].Image != "postgres:16" || x.StatefulSets[0].Selector["app"] != "pg" {
		t.Fatalf("sts = %+v", x.StatefulSets)
	}
	if len(x.Ingresses) != 1 || len(x.Ingresses[0].Rules) != 2 {
		t.Fatalf("ingress = %+v", x.Ingresses)
	}
	if r := x.Ingresses[0].Rules[1]; r.Host != "shop.local" || r.Path != "/api" || r.Service != "api-svc" || r.Port != "http" {
		t.Fatalf("rule = %+v", r)
	}
	if r := x.Ingresses[0].Rules[0]; r.Port != "80" {
		t.Fatalf("rule0 = %+v", r)
	}
	if len(x.ConfigMaps) != 1 || x.ConfigMaps[0].Name != "api-env" || x.ConfigMaps[0].Keys != 3 {
		t.Fatalf("configmaps (kube-root-ca.crt is hidden) = %+v", x.ConfigMaps)
	}
	if len(x.PVCs) != 1 || x.PVCs[0].Status != "Bound" || x.PVCs[0].Size != "20Gi" || x.PVCs[0].StorageClass != "local-path" {
		t.Fatalf("pvcs = %+v", x.PVCs)
	}
	// Other：如實列出沒有專屬分頁的資源；每個 namespace 自動產生的 default ServiceAccount 略過
	var got []string
	for _, o := range x.Others {
		got = append(got, o.Kind+" "+o.Namespace+"/"+o.Name)
	}
	want := "Lease kube-node-lease/framework,Role kube-public/system:controller:bootstrap-signer,ServiceAccount shop/deployer,Secret shop/tls"
	if strings.Join(got, ",") != want {
		t.Fatalf("others = %v", got)
	}
}

func TestListExtrasAndDeploymentDetail(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl get statefulsets,ingresses,configmaps,persistentvolumeclaims,"+otherResources+" -n shop -o json", resp{out: extrasJSON})
	x, err := newMgr(f).ListExtras(context.Background(), "shop")
	if err != nil || len(x.PVCs) != 1 {
		t.Fatalf("%+v %v", x, err)
	}
	if _, err := newMgr(f).ListExtras(context.Background(), "-A"); err == nil {
		t.Fatal("invalid namespace must be rejected")
	}

	rs := `{"items":[
	 {"metadata":{"name":"api-1","namespace":"shop","creationTimestamp":"2026-11-10T06:00:00Z","annotations":{"deployment.kubernetes.io/revision":"1"},"ownerReferences":[{"kind":"Deployment","name":"api"}]},"spec":{"replicas":0,"template":{"spec":{"containers":[{"image":"shop/api:2.3"}]}}}},
	 {"metadata":{"name":"api-2","namespace":"shop","creationTimestamp":"2026-11-20T06:00:00Z","annotations":{"deployment.kubernetes.io/revision":"2"},"ownerReferences":[{"kind":"Deployment","name":"api"}]},"spec":{"replicas":3,"template":{"spec":{"containers":[{"image":"shop/api:2.4"}]}}}},
	 {"metadata":{"name":"web-1","namespace":"shop","creationTimestamp":"2026-11-20T06:00:00Z","annotations":{"deployment.kubernetes.io/revision":"9"},"ownerReferences":[{"kind":"Deployment","name":"web"}]},"spec":{"replicas":1,"template":{"spec":{"containers":[{"image":"w"}]}}}}]}`
	ev := `{"items":[
	 {"type":"Normal","reason":"Old","message":"old","count":1,"lastTimestamp":"2026-11-20T06:00:00Z","metadata":{}},
	 {"type":"Normal","reason":"ScalingReplicaSet","message":"Scaled up replica set api-2 to 3","count":2,"lastTimestamp":"2026-11-21T06:00:00Z","metadata":{}}]}`
	f.on(d+" k3s kubectl get replicasets -n shop -o json", resp{out: rs})
	f.on(d+" k3s kubectl get events -n shop --field-selector involvedObject.kind=Deployment,involvedObject.name=api -o json", resp{out: ev})
	det, err := newMgr(f).DeploymentDetail(context.Background(), "shop", "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(det.Revisions) != 2 || det.Revisions[0].Revision != 2 || !det.Revisions[0].Current || det.Revisions[0].Image != "shop/api:2.4" || det.Revisions[1].Revision != 1 || det.Revisions[1].Current {
		t.Fatalf("revisions (newest first, only this deployment) = %+v", det.Revisions)
	}
	if len(det.Events) != 2 || det.Events[0].Reason != "ScalingReplicaSet" || det.Events[0].Count != 2 {
		t.Fatalf("events newest first = %+v", det.Events)
	}
	if _, err := newMgr(f).DeploymentDetail(context.Background(), "shop", "--x"); err == nil {
		t.Fatal("invalid name must be rejected")
	}
}

// ---- Deploy／Apply ---------------------------------------------------------

func TestDeploySpecNormalize(t *testing.T) {
	ok, err := DeploySpec{Image: "myapp:latest", Name: "myapp", Port: 8080}.Normalize()
	if err != nil || ok.Namespace != "default" || ok.Replicas != 1 {
		t.Fatalf("defaults not applied: %+v %v", ok, err)
	}
	bad := []DeploySpec{
		{Image: "x", Name: "-flag"},
		{Image: "x", Name: "Has.Dot"},
		{Image: "--privileged", Name: "a"},
		{Image: "a b", Name: "a"},
		{Image: "x", Name: "a", Port: 70000},
		{Image: "x", Name: "a", Replicas: 101},
		{Image: "x", Name: "a", Namespace: "BAD NS"},
	}
	for _, s := range bad {
		if _, err := s.Normalize(); err == nil {
			t.Errorf("expected rejection for %+v", s)
		}
	}
}

func TestDeployAppliesManifestAndReadsNodePort(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl apply -f -", resp{})
	f.on(d+" k3s kubectl get service myapp -n default -o json", resp{out: `{"metadata":{"name":"myapp","namespace":"default"},"spec":{"type":"NodePort","ports":[{"port":8080,"nodePort":31234,"protocol":"TCP"}]}}`})
	res, err := newMgr(f).Deploy(context.Background(), DeploySpec{Image: "myapp:latest", Name: "myapp", Port: 8080, Replicas: 2})
	if err != nil {
		t.Fatal(err)
	}
	if res.NodePort != 31234 || res.Name != "myapp" || res.Namespace != "default" {
		t.Fatalf("result = %+v", res)
	}
	m := f.stdin[d+" k3s kubectl apply -f -"]
	for _, want := range []string{`"kind":"Deployment"`, `"kind":"Service"`, `"replicas":2`, `"image":"myapp:latest"`, `"imagePullPolicy":"IfNotPresent"`, `"type":"NodePort"`} {
		if !strings.Contains(m, want) {
			t.Errorf("manifest missing %s: %s", want, m)
		}
	}
}

func TestDeployWithoutPortCreatesNoService(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl apply -f -", resp{})
	res, err := newMgr(f).Deploy(context.Background(), DeploySpec{Image: "worker:1", Name: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if res.NodePort != 0 || strings.Contains(f.stdin[d+" k3s kubectl apply -f -"], `"kind":"Service"`) {
		t.Fatalf("unexpected service: %+v", res)
	}
}

func TestDeployCreatesNonDefaultNamespaceFirst(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl apply -f -", resp{})
	if _, err := newMgr(f).Deploy(context.Background(), DeploySpec{Image: "w:1", Name: "w", Namespace: "staging"}); err != nil {
		t.Fatal(err)
	}
	applies := 0
	for _, c := range f.calls {
		if strings.HasSuffix(c, "apply -f -") {
			applies++
		}
	}
	if applies != 2 {
		t.Fatalf("want namespace apply + workload apply, got %d: %v", applies, f.calls)
	}
}

func TestApplyRejectsEmptyAndOversized(t *testing.T) {
	m := newMgr(newFake())
	if _, err := m.Apply(context.Background(), []byte("  \n")); err == nil {
		t.Error("empty manifest must be rejected")
	}
	if _, err := m.Apply(context.Background(), make([]byte, MaxApplyBytes+1)); err == nil {
		t.Error("oversized manifest must be rejected")
	}
}

func TestApplyPipesManifestToKubectl(t *testing.T) {
	f := newFake()
	f.on(d+" k3s kubectl apply -f -", resp{out: "deployment.apps/x created\n"})
	out, err := newMgr(f).Apply(context.Background(), []byte("kind: Deployment\n"))
	if err != nil || !strings.Contains(out, "created") || f.stdin[d+" k3s kubectl apply -f -"] != "kind: Deployment\n" {
		t.Fatalf("out=%q err=%v stdin=%q", out, err, f.stdin[d+" k3s kubectl apply -f -"])
	}
}

func TestStartReportsStepsInOrder(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active docker", resp{out: "active\n"})
	f.on(d+" systemctl reset-failed k3s", resp{})
	f.on(d+" systemctl start k3s", resp{})
	f.on(d+" k3s kubectl get --raw /readyz", resp{out: "ok"})
	f.on(d+" k3s kubectl get nodes -o json", resp{out: nodesReady})
	var steps []string
	ctx := WithProgress(context.Background(), func(s string) { steps = append(steps, s) })
	if err := newMgr(f).Start(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, ",") != "engine,install,node" {
		t.Fatalf("steps = %v", steps)
	}
}
