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
