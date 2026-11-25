package gui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/k8s"
)

type k8sEnv struct {
	*env
	k8s  *FakeK8s
	wsl  *FakeWSL
	kube string
}

func newK8sEnv(t *testing.T) *k8sEnv {
	t.Helper()
	e := newEnv(t, "")
	ke := &k8sEnv{env: e, k8s: NewFakeK8s(), wsl: NewFakeWSL(), kube: filepath.Join(t.TempDir(), "kubeconfig.yaml")}
	e.s.WSL = ke.wsl
	e.s.K8sFor = ke.k8s.For
	e.s.KubeconfigPath = ke.kube
	e.s.InstallK8sDistro = func(_ context.Context, _, name string, _ int) error {
		ke.wsl.AddDistro(name, "Running")
		return nil
	}
	return ke
}

func (e *k8sEnv) get(t *testing.T) K8sResponse {
	t.Helper()
	resp, b := e.req(t, http.MethodGet, "/api/k8s", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/k8s: %d %s", resp.StatusCode, b)
	}
	var r K8sResponse
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// idle 等背景作業結束。
func (e *k8sEnv) idle(t *testing.T) K8sResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if r := e.get(t); r.Op == "" {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("k8s op did not finish")
	return K8sResponse{}
}

func (e *k8sEnv) cfg(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load(e.cfgP)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestK8sIsOffByDefaultAndDoesNotProbe(t *testing.T) {
	e := newK8sEnv(t)
	r := e.get(t)
	if !r.Available || r.Enabled || r.Op != "" || r.Status.State != k8s.StateOff {
		t.Fatalf("default: %+v", r)
	}
	if r.Distro != "k8s-engine" || strings.Join(r.Steps, ",") != strings.Join(k8s.Steps, ",") || r.Status.Endpoint != "https://127.0.0.1:26443" {
		t.Fatalf("fields: %+v", r)
	}
	if r.KubeconfigPath != e.kube || r.SharedKubeconfig != k8s.SharedKubeconfig {
		t.Fatalf("paths: %+v", r)
	}
}

func TestK8sEnableRunsBootstrapAndSavesConfig(t *testing.T) {
	e := newK8sEnv(t)
	resp, b := e.req(t, http.MethodPost, "/api/k8s/enable", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("enable: %d %s", resp.StatusCode, b)
	}
	r := e.idle(t)
	if r.Error != "" || !r.Enabled || r.Status.State != k8s.StateReady {
		t.Fatalf("after enable: %+v", r)
	}
	c := e.cfg(t)
	if m, ok := c.K8sMachine(); !c.K8s || !ok || m.Distro != "k8s-engine" {
		t.Fatalf("config: k8s=%v machines=%+v", c.K8s, c.Machines)
	}
	if c.Active != "Ubuntu-26.04" {
		t.Fatalf("active must stay the engine, got %q", c.Active)
	}
	if e.wsl.State("k8s-engine") == "" {
		t.Fatal("the k8s-engine distro must be created")
	}
	kc, err := os.ReadFile(e.kube)
	if err != nil || !strings.Contains(string(kc), "current-context: wsld") {
		t.Fatalf("kubeconfig: %q %v", kc, err)
	}
	if !shouldKeepAlive(c, "k8s-engine") || !shouldKeepAlive(c, "Ubuntu-26.04") {
		t.Fatal("engine and k8s-engine are both kept alive while k8s is on")
	}
}

func TestK8sEnableFailureReportsTheStepAndRetrySucceeds(t *testing.T) {
	e := newK8sEnv(t)
	e.k8s.Set(func(f *FakeK8s) { f.SetFailStep(k8s.StepBootstrap) })
	e.req(t, http.MethodPost, "/api/k8s/enable", "")
	r := e.idle(t)
	if r.Enabled || r.ErrorStep != k8s.StepBootstrap || !strings.Contains(r.Error, "bootstrap") {
		t.Fatalf("failure: %+v", r)
	}
	if e.cfg(t).K8s {
		t.Fatal("a failed enable must not leave k8s on")
	}
	e.k8s.Set(func(f *FakeK8s) { f.SetFailStep("") })
	e.req(t, http.MethodPost, "/api/k8s/enable", "")
	if r := e.idle(t); !r.Enabled || r.Error != "" {
		t.Fatalf("retry: %+v", r)
	}
}

func TestK8sReportsTheCurrentStepAndRejectsASecondOp(t *testing.T) {
	e := newK8sEnv(t)
	e.k8s.Set(func(f *FakeK8s) { f.SetDelay(80 * time.Millisecond) })
	e.req(t, http.MethodPost, "/api/k8s/enable", "")
	var r K8sResponse
	for i := 0; i < 50; i++ { // 背景作業回報第一個步驟前可能先被讀到
		if r = e.get(t); r.Step != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r.Op != "enabling" || r.Step == "" {
		t.Fatalf("in progress: %+v", r)
	}
	if resp, _ := e.req(t, http.MethodPost, "/api/k8s/enable", ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second enable must be 409, got %d", resp.StatusCode)
	}
	if resp, _ := e.req(t, http.MethodPost, "/api/k8s/disable", ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("disable while enabling must be 409, got %d", resp.StatusCode)
	}
	e.idle(t)
}

func TestK8sDisableTurnsOffAndStopsKeepingTheDistroAlive(t *testing.T) {
	e := newK8sEnv(t)
	e.req(t, http.MethodPost, "/api/k8s/enable", "")
	e.idle(t)
	resp, _ := e.req(t, http.MethodPost, "/api/k8s/disable", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("disable: %d", resp.StatusCode)
	}
	r := e.idle(t)
	if r.Enabled || r.Error != "" {
		t.Fatalf("after disable: %+v", r)
	}
	if _, _, d := e.k8s.Counts(); d != 1 {
		t.Fatalf("disable calls = %d", d)
	}
	c := e.cfg(t)
	if c.K8s || shouldKeepAlive(c, "k8s-engine") {
		t.Fatal("k8s off: not kept alive")
	}
	if m, ok := c.K8sMachine(); !ok || m.Distro != "k8s-engine" {
		t.Fatal("the k8s distro stays registered (data kept for next time)")
	}
}

func TestK8sDistroIsProtectedWhileOn(t *testing.T) {
	e := newK8sEnv(t)
	e.wsl.AddDistro("Ubuntu-26.04", "Running")
	e.req(t, http.MethodPost, "/api/k8s/enable", "")
	e.idle(t)
	for _, p := range []string{"/api/machines/k8s-engine/stop", "/api/machines/k8s-engine/unmanage", "/api/machines/k8s-engine/unregister"} {
		resp, b := e.req(t, http.MethodPost, p, `{"confirm":"k8s-engine"}`)
		if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "Kubernetes") {
			t.Fatalf("%s: %d %s", p, resp.StatusCode, b)
		}
	}
	e.req(t, http.MethodPost, "/api/k8s/disable", "")
	e.idle(t)
	if resp, b := e.req(t, http.MethodPost, "/api/machines/k8s-engine/stop", ""); resp.StatusCode == http.StatusConflict {
		t.Fatalf("k8s off: stop allowed, got %d %s", resp.StatusCode, b)
	}
}

func TestK8sPodsOnlyWhileOn(t *testing.T) {
	e := newK8sEnv(t)
	resp, b := e.req(t, http.MethodGet, "/api/k8s/pods", "")
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(b)) != "[]" {
		t.Fatalf("off: %d %s", resp.StatusCode, b)
	}
	e.k8s.Set(func(f *FakeK8s) {
		f.SetPods([]k8s.Pod{{Namespace: "default", Name: "web", Status: "Running", Ready: "1/1"}})
	})
	e.req(t, http.MethodPost, "/api/k8s/enable", "")
	e.idle(t)
	_, b = e.req(t, http.MethodGet, "/api/k8s/pods", "")
	var pods []k8s.Pod
	if err := json.Unmarshal(b, &pods); err != nil || len(pods) != 1 || pods[0].Name != "web" {
		t.Fatalf("pods: %s %v", b, err)
	}
}

func TestSuperviseBringsUpAnEnabledK8sThatIsNotRunning(t *testing.T) {
	e := newK8sEnv(t)
	c := e.cfg(t)
	c.K8s = true
	if err := c.SetK8sMachine("k8s-engine"); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(e.cfgP, c); err != nil {
		t.Fatal(err)
	}
	e.k8s.Set(func(f *FakeK8s) { f.SetInstalled(true) })
	now := time.Now()
	e.s.superviseK8s(now)
	r := e.idle(t)
	if r.Status.State != k8s.StateReady {
		t.Fatalf("after supervise: %+v", r)
	}
	if en, st, _ := e.k8s.Counts(); en != 0 || st != 1 {
		t.Fatalf("installed k8s is started, not re-enabled: enables=%d starts=%d", en, st)
	}
	// 節流：30 秒內不再檢查
	e.k8s.Set(func(f *FakeK8s) { f.SetRunning(false) })
	e.s.superviseK8s(now.Add(time.Second))
	if _, st, _ := e.k8s.Counts(); st != 1 {
		t.Fatalf("supervise must be throttled, starts=%d", st)
	}
}

func TestSuperviseLeavesK8sAloneWhenOff(t *testing.T) {
	e := newK8sEnv(t)
	e.s.superviseK8s(time.Now())
	if en, st, _ := e.k8s.Counts(); en+st != 0 || e.get(t).Op != "" {
		t.Fatal("k8s off: supervise does nothing")
	}
}

func TestMachinesHidesTheK8sDistroButK8sReportsItsDisk(t *testing.T) {
	e := newK8sEnv(t)
	e.s.DistroDisk = func(name string) (int64, error) { return int64(len(name)) * 1000, nil }
	e.s.DistroDiskLimit = func(string) (int64, error) { return 60 << 30, nil }
	e.req(t, http.MethodPost, "/api/k8s/enable", "")
	r := e.idle(t)
	if r.DiskBytes != int64(len("k8s-engine"))*1000 || r.DiskLimitBytes != 60<<30 {
		t.Fatalf("disk: %+v", r)
	}
	_, b := e.req(t, http.MethodGet, "/api/machines", "")
	if strings.Contains(string(b), "k8s-engine") {
		t.Fatalf("machines must not list the k8s distro: %s", b)
	}
}
