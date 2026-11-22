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

	"github.com/coder/websocket"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/k8s"
)

type k8sEnv struct {
	*env
	cluster *FakeK8sCluster
	stream  *FakeCommandStream
	kube    string
}

func newK8sEnv(t *testing.T) *k8sEnv {
	t.Helper()
	e := newEnv(t, "tok")
	k := &k8sEnv{env: e, cluster: NewFakeK8sCluster(), stream: &FakeCommandStream{}, kube: filepath.Join(t.TempDir(), "kube", "config")}
	e.s.K8sFor = func(d string) K8s { return k.cluster.For(d) }
	e.s.K8sStream = k.stream
	e.s.KubeconfigPath = k.kube
	return k
}

func (k *k8sEnv) addMachine(t *testing.T, name string, k8sOn bool) {
	t.Helper()
	c, _ := config.Load(k.cfgP)
	c.UpsertMachine(config.Machine{Distro: name, User: "u", APIEndpoint: "tcp://127.0.0.1:23751", K8s: k8sOn})
	if err := config.Save(k.cfgP, c); err != nil {
		t.Fatal(err)
	}
}

func (k *k8sEnv) cfg(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load(k.cfgP)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (k *k8sEnv) get(t *testing.T) K8sResponse {
	t.Helper()
	resp, b := k.req(t, "GET", "/api/k8s", "")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /api/k8s = %d %s", resp.StatusCode, b)
	}
	var r K8sResponse
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// waitIdle 等背景作業結束。
func (k *k8sEnv) waitIdle(t *testing.T) K8sResponse {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if r := k.get(t); r.Op == "" {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("k8s operation did not finish")
	return K8sResponse{}
}

func (k *k8sEnv) enable(t *testing.T) {
	t.Helper()
	if resp, b := k.req(t, "POST", "/api/k8s/on", ""); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("on = %d %s", resp.StatusCode, b)
	}
	if r := k.waitIdle(t); r.Error != "" || !r.Enabled || r.Status.State != k8s.StateReady {
		t.Fatalf("enable failed: %+v", r)
	}
}

func TestK8sUnavailableWithoutBackend(t *testing.T) {
	e := newEnv(t, "tok")
	for _, c := range [][2]string{{"GET", "/api/k8s"}, {"POST", "/api/k8s/on"}, {"GET", "/api/k8s/pods"}} {
		if resp, _ := e.req(t, c[0], c[1], ""); resp.StatusCode != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", c[0], c[1], resp.StatusCode)
		}
	}
}

func TestK8sRequiresToken(t *testing.T) {
	k := newK8sEnv(t)
	req, _ := http.NewRequest("GET", k.srv.URL+"/api/k8s", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestK8sInitialStateIsNotInstalledOnPrimary(t *testing.T) {
	k := newK8sEnv(t)
	r := k.get(t)
	if r.Machine != "Ubuntu-26.04" || r.Enabled || r.Status.State != k8s.StateNotInstalled || r.KubeconfigPath != k.kube {
		t.Fatalf("%+v", r)
	}
}

func TestK8sOnStartsInBackgroundThenPersistsWithoutExporting(t *testing.T) {
	k := newK8sEnv(t)
	k.cluster.Delay = 60 * time.Millisecond
	resp, b := k.req(t, "POST", "/api/k8s/on", "")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("on = %d %s", resp.StatusCode, b)
	}
	var first K8sResponse
	_ = json.Unmarshal(b, &first)
	if first.Op != "starting" {
		t.Fatalf("response must show the op in progress: %+v", first)
	}
	if r := k.get(t); r.Op != "starting" {
		t.Fatalf("poll should see starting: %+v", r)
	}
	r := k.waitIdle(t)
	if !r.Enabled || r.Status.State != k8s.StateReady || r.Error != "" {
		t.Fatalf("%+v", r)
	}
	if m, ok := k.cfg(t).K8sMachine(); !ok || m.Distro != "Ubuntu-26.04" {
		t.Fatal("flag must be persisted after success")
	}
	if _, err := os.Stat(k.kube); err == nil {
		t.Fatal("starting k8s must not write the Windows kubeconfig")
	}
}

func TestK8sOnFailureReportsErrorAndDoesNotPersist(t *testing.T) {
	k := newK8sEnv(t)
	k.cluster.FailStart = true
	k.req(t, "POST", "/api/k8s/on", "")
	r := k.waitIdle(t)
	if r.Error == "" || r.Enabled {
		t.Fatalf("%+v", r)
	}
	if _, ok := k.cfg(t).K8sMachine(); ok {
		t.Fatal("failed start must not set the flag")
	}
}

func TestK8sSecondOperationWhileBusyConflicts(t *testing.T) {
	k := newK8sEnv(t)
	k.cluster.Delay = 100 * time.Millisecond
	k.req(t, "POST", "/api/k8s/on", "")
	if resp, _ := k.req(t, "POST", "/api/k8s/off", ""); resp.StatusCode != http.StatusConflict {
		// off 在 owner 尚未寫入前是 no-op（200）；reset 才會遇到 409
		if resp2, _ := k.req(t, "POST", "/api/k8s/on", ""); resp2.StatusCode != http.StatusConflict {
			t.Fatalf("second on while busy = %d", resp2.StatusCode)
		}
	}
	k.waitIdle(t)
}

func TestK8sOnRejectsOtherOwnerUnlessSwitch(t *testing.T) {
	k := newK8sEnv(t)
	k.addMachine(t, "Other", true)
	resp, b := k.req(t, "POST", "/api/k8s/on", "")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "Other") {
		t.Fatalf("on = %d %s", resp.StatusCode, b)
	}
	if m, _ := k.cfg(t).K8sMachine(); m.Distro != "Other" {
		t.Fatal("owner must stay Other")
	}
}

func TestK8sSwitchMovesClusterToTarget(t *testing.T) {
	k := newK8sEnv(t)
	k.addMachine(t, "Other", true)
	k.cluster.installed["Other"], k.cluster.running = true, "Other"
	if resp, b := k.req(t, "POST", "/api/k8s/on?switch=1", ""); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	r := k.waitIdle(t)
	if r.Error != "" || r.Machine != "Ubuntu-26.04" || r.Status.State != k8s.StateReady {
		t.Fatalf("%+v", r)
	}
	if m, _ := k.cfg(t).K8sMachine(); m.Distro != "Ubuntu-26.04" {
		t.Fatalf("owner = %q", m.Distro)
	}
	calls := strings.Join(k.cluster.Called(), ",")
	if strings.Index(calls, "stop:Other") > strings.Index(calls, "start:Ubuntu-26.04") || !strings.Contains(calls, "stop:Other") {
		t.Fatalf("must stop the old owner before starting the new one: %s", calls)
	}
}

func TestK8sOnUnknownOrUninitialisedMachine(t *testing.T) {
	k := newK8sEnv(t)
	if resp, _ := k.req(t, "POST", "/api/k8s/on?machine=Nope", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown = %d", resp.StatusCode)
	}
	c := k.cfg(t)
	c.UpsertMachine(config.Machine{Distro: "Fresh"})
	_ = config.Save(k.cfgP, c)
	if resp, _ := k.req(t, "POST", "/api/k8s/on?machine=Fresh", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("uninitialised = %d", resp.StatusCode)
	}
}

func TestK8sOffStopsAndClearsFlag(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	if resp, b := k.req(t, "POST", "/api/k8s/off", ""); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	r := k.waitIdle(t)
	if r.Enabled || r.Status.State != k8s.StateOff || r.Error != "" {
		t.Fatalf("%+v", r)
	}
	if _, ok := k.cfg(t).K8sMachine(); ok {
		t.Fatal("flag must be cleared")
	}
}

func TestK8sOffWhenNothingEnabledIsNoop(t *testing.T) {
	k := newK8sEnv(t)
	if resp, _ := k.req(t, "POST", "/api/k8s/off", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if len(k.cluster.Called()) != 0 {
		t.Fatal("nothing should run")
	}
}

func TestK8sResetRestoresCleanClusterAndRequiresEnabled(t *testing.T) {
	k := newK8sEnv(t)
	if resp, _ := k.req(t, "POST", "/api/k8s/reset", ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("reset when off = %d", resp.StatusCode)
	}
	k.enable(t)
	k.req(t, "POST", "/api/k8s/deployments/default/poc-web/scale", `{"replicas":3}`)
	k.req(t, "POST", "/api/k8s/reset", "")
	r := k.waitIdle(t)
	if r.Error != "" || r.Status.State != k8s.StateReady {
		t.Fatalf("%+v", r)
	}
	_, b := k.req(t, "GET", "/api/k8s/deployments?ns=default", "")
	if strings.Contains(string(b), "poc-web") {
		t.Fatalf("reset must remove user workloads: %s", b)
	}
	if _, err := os.Stat(k.kube); err == nil {
		t.Fatal("reset must not create the Windows kubeconfig when it was never exported")
	}
	// 曾匯出過 → 重置後同步更新（憑證已換新）
	if resp, b := k.req(t, "POST", "/api/k8s/kubeconfig", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("export = %d %s", resp.StatusCode, b)
	}
	if err := os.WriteFile(k.kube, []byte(strings.Replace(mustRead(t, k.kube), k8s.APIServer(k8s.DefaultAPIPort), "https://stale.invalid:1", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	k.req(t, "POST", "/api/k8s/reset", "")
	k.waitIdle(t)
	if strings.Contains(mustRead(t, k.kube), "stale.invalid") {
		t.Fatal("reset must refresh an already-exported kubeconfig")
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestK8sKubeconfigEndpoint(t *testing.T) {
	k := newK8sEnv(t)
	if resp, _ := k.req(t, "POST", "/api/k8s/kubeconfig", ""); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("never installed = %d (reading k3s.yaml must fail)", resp.StatusCode)
	}
	k.enable(t)
	_ = os.Remove(k.kube)
	resp, b := k.req(t, "POST", "/api/k8s/kubeconfig", "")
	if resp.StatusCode != 200 || !strings.Contains(string(b), "wsld-Ubuntu-26.04") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if _, err := os.Stat(k.kube); err != nil {
		t.Fatal("kubeconfig file must be written")
	}
	// k3s 關閉後 k3s.yaml 仍在：匯出與狀態無關
	k.req(t, "POST", "/api/k8s/off", "")
	if r := k.waitIdle(t); r.Enabled {
		t.Fatalf("k8s should be off: %+v", r)
	}
	_ = os.Remove(k.kube)
	if resp, b := k.req(t, "POST", "/api/k8s/kubeconfig", ""); resp.StatusCode != 200 {
		t.Fatalf("export while k3s is off = %d %s", resp.StatusCode, b)
	}
	if _, err := os.Stat(k.kube); err != nil {
		t.Fatal("kubeconfig must be written even when k3s is off")
	}
}

func TestK8sResourceListsRequireEnabled(t *testing.T) {
	k := newK8sEnv(t)
	for _, p := range []string{"namespaces", "pods", "deployments", "services", "extras", "deployments/default/poc-web/detail"} {
		if resp, _ := k.req(t, "GET", "/api/k8s/"+p, ""); resp.StatusCode != http.StatusConflict {
			t.Errorf("%s when off = %d, want 409", p, resp.StatusCode)
		}
	}
}

func TestK8sLists(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	var ns []string
	_, b := k.req(t, "GET", "/api/k8s/namespaces", "")
	_ = json.Unmarshal(b, &ns)
	if len(ns) != 4 {
		t.Fatalf("ns = %s", b)
	}
	var pods []k8s.Pod
	_, b = k.req(t, "GET", "/api/k8s/pods?ns=default", "")
	_ = json.Unmarshal(b, &pods)
	if len(pods) != 1 || pods[0].Name == "" || pods[0].Namespace != "default" {
		t.Fatalf("pods = %s", b)
	}
	_, b = k.req(t, "GET", "/api/k8s/pods", "")
	_ = json.Unmarshal(b, &pods)
	if len(pods) != 4 {
		t.Fatalf("all pods = %d", len(pods))
	}
	var svcs []k8s.Service
	_, b = k.req(t, "GET", "/api/k8s/services?ns=default", "")
	_ = json.Unmarshal(b, &svcs)
	if len(svcs) != 2 {
		t.Fatalf("svcs = %s", b)
	}
	var deps []k8s.Deployment
	_, b = k.req(t, "GET", "/api/k8s/deployments?ns=default", "")
	_ = json.Unmarshal(b, &deps)
	if len(deps) != 1 || deps[0].Name != "poc-web" {
		t.Fatalf("deps = %s", b)
	}
}

func TestK8sListRejectsBadNamespace(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	if resp, _ := k.req(t, "GET", "/api/k8s/pods?ns=--all-namespaces", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestK8sDeleteRestartScale(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	var pods []k8s.Pod
	_, b := k.req(t, "GET", "/api/k8s/pods?ns=default", "")
	_ = json.Unmarshal(b, &pods)
	old := pods[0].Name
	if resp, _ := k.req(t, "DELETE", "/api/k8s/pods/default/"+old, ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete pod = %d", resp.StatusCode)
	}
	_, b = k.req(t, "GET", "/api/k8s/pods?ns=default", "")
	_ = json.Unmarshal(b, &pods)
	if len(pods) != 1 || pods[0].Name == old {
		t.Fatalf("deployment should recreate the pod under a new name: %s", b)
	}
	if resp, _ := k.req(t, "POST", "/api/k8s/deployments/default/poc-web/restart", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("restart = %d", resp.StatusCode)
	}
	if resp, _ := k.req(t, "POST", "/api/k8s/deployments/default/poc-web/scale", `{"replicas":2}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("scale = %d", resp.StatusCode)
	}
	_, b = k.req(t, "GET", "/api/k8s/pods?ns=default", "")
	_ = json.Unmarshal(b, &pods)
	if len(pods) != 2 {
		t.Fatalf("scale 2 → %d pods", len(pods))
	}
	if resp, _ := k.req(t, "DELETE", "/api/k8s/services/default/poc-web", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete service = %d", resp.StatusCode)
	}
	if resp, _ := k.req(t, "DELETE", "/api/k8s/deployments/default/poc-web", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete deployment = %d", resp.StatusCode)
	}
}

func TestK8sMutationValidation(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"DELETE", "/api/k8s/nodes/default/x", "", 400},
		{"DELETE", "/api/k8s/pods/default/--all", "", 400},
		{"DELETE", "/api/k8s/pods/Bad_NS/x", "", 400},
		{"POST", "/api/k8s/deployments/default/poc-web/scale", `{"replicas":-1}`, 400},
		{"POST", "/api/k8s/deployments/default/poc-web/scale", `{"replicas":101}`, 400},
		{"POST", "/api/k8s/deployments/default/poc-web/scale", `not json`, 400},
		{"POST", "/api/k8s/deployments/default/--x/restart", "", 400},
		{"DELETE", "/api/k8s/pods/default/does-not-exist", "", 502},
	}
	for _, c := range cases {
		if resp, b := k.req(t, c.method, c.path, c.body); resp.StatusCode != c.want {
			t.Errorf("%s %s = %d (%s), want %d", c.method, c.path, resp.StatusCode, b, c.want)
		}
	}
}

func TestK8sOpenNodePortOnlyForRealServicePorts(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	var opened []string
	k.s.OnOpenURL = func(u string) error { opened = append(opened, u); return nil }
	if resp, b := k.req(t, "POST", "/api/k8s/open", `{"port":30080}`); resp.StatusCode != 200 || !strings.Contains(string(b), "localhost:30080") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if len(opened) != 1 || !strings.HasSuffix(opened[0], ":30080") {
		t.Fatalf("opened = %v", opened)
	}
	for _, body := range []string{`{"port":8080}`, `{"port":0}`, `{"port":22}`, `bad`} {
		if resp, _ := k.req(t, "POST", "/api/k8s/open", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s = %d, want 400", body, resp.StatusCode)
		}
	}
	if len(opened) != 1 {
		t.Fatalf("must not open arbitrary ports: %v", opened)
	}
}

func TestK8sLogsStreamUsesKubectlLogsOnOwner(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	c := k.ws(t, "/api/k8s/pods/default/poc-web-1/logs/stream?tail=50")
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil || !strings.Contains(string(data), "listening on :8080") {
		t.Fatalf("%q %v", data, err)
	}
	cmds := k.stream.OpenedCommands()
	if len(cmds) != 1 {
		t.Fatalf("opened %v", cmds)
	}
	got := strings.Join(cmds[0], " ")
	if got != "Ubuntu-26.04 k3s kubectl logs poc-web-1 -n default --tail=50 --timestamps -f" {
		t.Fatalf("command = %q", got)
	}
}

func TestK8sExecIsInteractiveKubectlExec(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	c := k.ws(t, "/api/k8s/pods/default/poc-web-1/exec?cols=100&rows=30")
	defer c.CloseNow()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, data, err := c.Read(ctx); err != nil || !strings.Contains(string(data), "fake-shell$") {
		t.Fatalf("%q %v", data, err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatal(err)
	}
	var all string
	for !strings.Contains(all, "you typed: ls") {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("%q %v", all, err)
		}
		all += string(data)
	}
	if got := strings.Join(k.stream.OpenedCommands()[0], " "); !strings.HasPrefix(got, "Ubuntu-26.04 k3s kubectl exec -it poc-web-1 -n default --") {
		t.Fatalf("command = %q", got)
	}
}

func TestK8sStreamRejectsBadNamesAndDisabled(t *testing.T) {
	k := newK8sEnv(t)
	url := "/api/k8s/pods/default/poc-web-1/logs/stream"
	if resp, _ := k.req(t, "GET", url, ""); resp.StatusCode != http.StatusConflict {
		t.Fatalf("disabled = %d", resp.StatusCode)
	}
	k.enable(t)
	if resp, _ := k.req(t, "GET", "/api/k8s/pods/default/--x/logs/stream", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad pod = %d", resp.StatusCode)
	}
	if resp, _ := k.req(t, "GET", "/api/k8s/pods/default/poc-web-1/exec?container=a%3Bb", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad container = %d", resp.StatusCode)
	}
	if n := len(k.stream.OpenedCommands()); n != 0 {
		t.Fatalf("nothing may be opened for invalid input, got %d", n)
	}
}

func TestEngineStartResumesK3sWhenEnabledOnPrimary(t *testing.T) {
	k := newK8sEnv(t)
	c := k.cfg(t)
	_ = c.SetK8s("Ubuntu-26.04", true)
	_ = config.Save(k.cfgP, c)
	if resp, b := k.req(t, "POST", "/api/engine/start", ""); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	r := k.waitIdle(t)
	if r.Status.State != k8s.StateReady || r.Error != "" {
		t.Fatalf("k3s should be resumed: %+v", r)
	}
}

func TestEngineStartLeavesK3sAloneWhenOff(t *testing.T) {
	k := newK8sEnv(t)
	if resp, _ := k.req(t, "POST", "/api/engine/start", ""); resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	time.Sleep(30 * time.Millisecond)
	if len(k.cluster.Called()) != 0 {
		t.Fatalf("k3s must not be touched: %v", k.cluster.Called())
	}
}

// scriptedStatus 依序回傳預先排好的狀態，其餘方法沿用內嵌的 K8s。
type scriptedStatus struct {
	K8s
	seq []k8s.State
}

func (s *scriptedStatus) Status(context.Context) k8s.Status {
	st := k8s.Status{Distro: "Ubuntu-26.04", State: s.seq[0]}
	if len(s.seq) > 1 {
		s.seq = s.seq[1:]
	}
	return st
}

// wsl.exe 偶爾逾時（Unknown）時，徽章沿用上一次確定的狀態，不在啟動中／未啟動間閃動。
func TestK8sUnknownStatusKeepsLastKnown(t *testing.T) {
	k := newK8sEnv(t)
	sc := &scriptedStatus{K8s: k.cluster.For("Ubuntu-26.04"), seq: []k8s.State{k8s.StateStarting, k8s.StateUnknown, k8s.StateReady, k8s.StateUnknown}}
	k.s.K8sFor = func(string) K8s { return sc }
	for i, want := range []k8s.State{k8s.StateStarting, k8s.StateStarting, k8s.StateReady, k8s.StateReady} {
		if got := k.get(t).Status.State; got != want {
			t.Fatalf("poll %d: state = %q, want %q", i, got, want)
		}
	}
}

func TestK8sUnknownStatusWithoutHistoryStaysUnknown(t *testing.T) {
	k := newK8sEnv(t)
	sc := &scriptedStatus{K8s: k.cluster.For("Ubuntu-26.04"), seq: []k8s.State{k8s.StateUnknown}}
	k.s.K8sFor = func(string) K8s { return sc }
	if got := k.get(t).Status.State; got != k8s.StateUnknown {
		t.Fatalf("state = %q", got)
	}
}

func TestK8sExtrasAndDeploymentDetail(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	k.cluster.SeedShop()
	var x k8s.Extras
	_, b := k.req(t, "GET", "/api/k8s/extras?ns=shop", "")
	if err := json.Unmarshal(b, &x); err != nil || len(x.StatefulSets) != 1 || len(x.Ingresses) != 1 || len(x.Ingresses[0].Rules) != 2 || len(x.ConfigMaps) != 1 || len(x.PVCs) != 1 {
		t.Fatalf("shop extras = %s (%v)", b, err)
	}
	_, b = k.req(t, "GET", "/api/k8s/extras", "")
	_ = json.Unmarshal(b, &x)
	if len(x.Ingresses) != 2 { // default 的 poc-web + shop
		t.Fatalf("all-namespace extras = %s", b)
	}
	if resp, _ := k.req(t, "GET", "/api/k8s/extras?ns=--all-namespaces", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad ns = %d", resp.StatusCode)
	}
	var d k8s.DeploymentDetail
	_, b = k.req(t, "GET", "/api/k8s/deployments/shop/api/detail", "")
	if err := json.Unmarshal(b, &d); err != nil || len(d.Revisions) != 2 || !d.Revisions[0].Current || len(d.Events) != 1 {
		t.Fatalf("detail = %s (%v)", b, err)
	}
	if resp, _ := k.req(t, "GET", "/api/k8s/deployments/shop/--x/detail", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad name = %d", resp.StatusCode)
	}
	if resp, _ := k.req(t, "GET", "/api/k8s/deployments/shop/nope/detail", ""); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("missing deployment = %d", resp.StatusCode)
	}
}

// ---- 單節點流程：步驟進度、add_context、Deploy／Apply ---------------------

func TestK8sOnReportsStepsAndFailedStep(t *testing.T) {
	k := newK8sEnv(t)
	k.cluster.Delay = 40 * time.Millisecond
	k.req(t, "POST", "/api/k8s/on", "")
	seen := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		r := k.get(t)
		if r.Op == "" {
			break
		}
		if len(r.Steps) != 4 || r.Steps[0] != k8s.StepEngine || r.Steps[3] != k8s.StepContext {
			t.Fatalf("steps = %v", r.Steps)
		}
		seen[r.Step] = true
		time.Sleep(5 * time.Millisecond)
	}
	k.waitIdle(t)
	if !seen[k8s.StepInstall] && !seen[k8s.StepNode] {
		t.Fatalf("polling should observe at least one in-flight step: %v", seen)
	}

	k2 := newK8sEnv(t)
	k2.cluster.FailStart = true
	k2.req(t, "POST", "/api/k8s/on", "")
	r := k2.waitIdle(t)
	if r.Error == "" || r.ErrorStep != k8s.StepNode || r.Step != "" {
		t.Fatalf("failure must name the step it stopped at: %+v", r)
	}
}

func TestK8sOnAddContextMergesKubeconfig(t *testing.T) {
	k := newK8sEnv(t)
	k.req(t, "POST", "/api/k8s/on?add_context=1", "")
	r := k.waitIdle(t)
	if r.Error != "" || r.Warning != "" {
		t.Fatalf("%+v", r)
	}
	if !k8s.HasExportedContext(k.kube, "Ubuntu-26.04") {
		t.Fatal("add_context=1 must merge the context into the Windows kubeconfig")
	}
}

func TestK8sDeployCreatesWorkloadAndReturnsNodePort(t *testing.T) {
	k := newK8sEnv(t)
	k.enable(t)
	resp, b := k.req(t, "POST", "/api/k8s/deploy", `{"image":"myapp:latest","name":"myapp","port":8080,"replicas":2}`)
	if resp.StatusCode != 200 {
		t.Fatalf("deploy = %d %s", resp.StatusCode, b)
	}
	var res k8s.DeployResult
	_ = json.Unmarshal(b, &res)
	if res.Name != "myapp" || res.Namespace != "default" || res.NodePort == 0 {
		t.Fatalf("%+v", res)
	}
	var pods []k8s.Pod
	_, b = k.req(t, "GET", "/api/k8s/pods?ns=default", "")
	_ = json.Unmarshal(b, &pods)
	n := 0
	for _, p := range pods {
		if strings.HasPrefix(p.Name, "myapp-") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 myapp pods, got %d", n)
	}
}

func TestK8sDeployValidationAndRequiresEnabled(t *testing.T) {
	k := newK8sEnv(t)
	if resp, _ := k.req(t, "POST", "/api/k8s/deploy", `{"image":"a","name":"a"}`); resp.StatusCode != http.StatusConflict {
		t.Fatalf("disabled deploy = %d", resp.StatusCode)
	}
	k.enable(t)
	for _, body := range []string{`{`, `{"image":"--privileged","name":"a"}`, `{"image":"a","name":"Bad.Name"}`, `{"image":"a","name":"a","port":99999}`, `{"image":"a","name":"a","replicas":500}`} {
		if resp, b := k.req(t, "POST", "/api/k8s/deploy", body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s → %d %s", body, resp.StatusCode, b)
		}
	}
}

func TestK8sApply(t *testing.T) {
	k := newK8sEnv(t)
	if resp, _ := k.req(t, "POST", "/api/k8s/apply", "kind: Deployment"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("disabled apply = %d", resp.StatusCode)
	}
	k.enable(t)
	resp, b := k.req(t, "POST", "/api/k8s/apply", "apiVersion: apps/v1\nkind: Deployment\n")
	if resp.StatusCode != 200 || !strings.Contains(string(b), "created") {
		t.Fatalf("apply = %d %s", resp.StatusCode, b)
	}
	if resp, _ := k.req(t, "POST", "/api/k8s/apply", "  \n"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty = %d", resp.StatusCode)
	}
	if resp, _ := k.req(t, "POST", "/api/k8s/apply", "this is invalid"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("kubectl error must surface as 502, got %d", resp.StatusCode)
	}
	if resp, _ := k.req(t, "POST", "/api/k8s/apply", strings.Repeat("x", k8s.MaxApplyBytes+10)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d", resp.StatusCode)
	}
}
