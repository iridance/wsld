package k8s

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/wsl"
)

// fakeRunner 依參數比對回應；未設定的命令一律成功、無輸出。記錄每次呼叫。
type fakeRunner struct {
	mu      sync.Mutex
	distros map[string]string // name → state
	calls   []string
	// on：子字串 → 回應（第一個符合的生效）。
	on []rule
}

type rule struct {
	sub  string
	out  string
	code int
	// times：>0 時只生效這麼多次。
	times int
}

func newFake() *fakeRunner {
	return &fakeRunner{distros: map[string]string{"docker-engine": "Running"}}
}

func (f *fakeRunner) when(sub, out string, code int) *fakeRunner {
	f.on = append(f.on, rule{sub: sub, out: out, code: code})
	return f
}

func (f *fakeRunner) once(sub, out string, code int) *fakeRunner {
	f.on = append(f.on, rule{sub: sub, out: out, code: code, times: 1})
	return f
}

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	if line == "--list --verbose" {
		var b strings.Builder
		b.WriteString("  NAME STATE VERSION\n")
		for n, s := range f.distros {
			b.WriteString("  " + n + " " + s + " 2\n")
		}
		return []byte(b.String()), nil, nil
	}
	if strings.HasPrefix(line, "--install") {
		for i, a := range args {
			if a == "--name" && i+1 < len(args) {
				f.distros[args[i+1]] = "Stopped"
			}
		}
		return nil, nil, nil
	}
	if strings.HasPrefix(line, "--terminate ") {
		f.distros[strings.TrimPrefix(line, "--terminate ")] = "Stopped"
		return nil, nil, nil
	}
	if len(args) > 1 && args[0] == "-d" {
		f.distros[args[1]] = "Running" // 任何 -d 命令都會喚醒 distro
	}
	for i := range f.on {
		r := &f.on[i]
		if r.times < 0 || !strings.Contains(line, r.sub) {
			continue
		}
		if r.times > 0 {
			r.times--
			if r.times == 0 {
				r.times = -1
			}
		}
		if r.code != 0 {
			return []byte(r.out), []byte("boom"), &wsl.ExitError{Code: r.code}
		}
		return []byte(r.out), nil, nil
	}
	return nil, nil, nil
}

func (f *fakeRunner) called(sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

func (f *fakeRunner) index(sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

const readyNodes = `{"items":[{"metadata":{"name":"k8s-engine"},"status":{"nodeInfo":{"kubeletVersion":"v1.37.2+k3s1"},"addresses":[{"type":"InternalIP","address":"172.20.3.68"}],"conditions":[{"type":"Ready","status":"True"}]}}]}`

const k3sYAML = `apiVersion: v1
clusters:
- cluster:
    server: https://127.0.0.1:26443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
users:
- name: default
  user: {}
`

func mgr(f *fakeRunner) *Manager {
	return &Manager{WSL: f, Distro: "k8s-engine", EngineDistro: "docker-engine", APIPort: 26443, Bundle: []byte("#!/bin/bash\n"),
		ReadyAttempts: 3, ReadyInterval: time.Millisecond, Sleep: func(time.Duration) {}}
}

func TestStatusDoesNotWakeAStoppedDistro(t *testing.T) {
	f := newFake()
	m := mgr(f)
	if st := m.Status(context.Background()); st.State != StateAbsent {
		t.Fatalf("no distro → absent, got %s", st.State)
	}
	f.distros["k8s-engine"] = "Stopped"
	if st := m.Status(context.Background()); st.State != StateStopped {
		t.Fatalf("stopped distro → stopped, got %s", st.State)
	}
	if f.called("-d k8s-engine") != 0 {
		t.Fatal("status must not run commands in a stopped distro")
	}
}

func TestStatusMapsScriptExitCodes(t *testing.T) {
	cases := []struct {
		out  string
		code int
		want State
	}{
		{"", 9, StateNotInstalled},
		{"", 8, StateOff},
		{"", 1, StateStarting},
		{readyNodes, 0, StateReady},
		{strings.Replace(readyNodes, `"True"`, `"False"`, 1), 0, StateStarting},
	}
	for _, c := range cases {
		f := newFake().when(statusScript, c.out, c.code)
		f.distros["k8s-engine"] = "Running"
		st := mgr(f).Status(context.Background())
		if st.State != c.want {
			t.Fatalf("exit %d → %s, want %s", c.code, st.State, c.want)
		}
		if c.want == StateReady && (st.Version != "v1.37.2+k3s1" || st.NodeName != "k8s-engine" || st.Endpoint != "https://127.0.0.1:26443") {
			t.Fatalf("ready status fields: %+v", st)
		}
	}
}

func TestEnableCreatesDistroInstallsSharesStartsAndExportsKubeconfig(t *testing.T) {
	f := newFake().
		when("systemctl is-active docker", "active\n", 0).
		when(statusScript, readyNodes, 0).
		when("cat /etc/rancher/k3s/k3s.yaml", k3sYAML, 0)
	m := mgr(f)
	var steps []string
	m.Progress = func(s string) { steps = append(steps, s) }
	var installed []string
	m.Install = func(_ context.Context, base, name string, size int) error {
		installed = append(installed, base, name)
		if size != DefaultVHDSizeGB {
			t.Fatalf("vhd size %d", size)
		}
		f.distros[name] = "Stopped"
		return nil
	}
	kc, err := m.Enable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(steps, ",") != strings.Join(Steps, ",") {
		t.Fatalf("steps = %v", steps)
	}
	if strings.Join(installed, " ") != "Debian k8s-engine" {
		t.Fatalf("install = %v", installed)
	}
	boot := f.index("-d k8s-engine -u root -- bash -c cat > /tmp/wsld-k8s.sh && bash /tmp/wsld-k8s.sh install k8s-engine 26443")
	share := f.index("-d docker-engine -u root -- bash -c cat > /tmp/wsld-k8s.sh && bash /tmp/wsld-k8s.sh share-engine")
	start := f.index("-d k8s-engine -u root -- systemctl start k3s")
	if boot < 0 || share < boot || start < share {
		t.Fatalf("order boot=%d share=%d start=%d\n%v", boot, share, start, f.calls)
	}
	s := string(kc)
	if strings.Contains(s, "default") || !strings.Contains(s, "current-context: wsld") || !strings.Contains(s, "  name: wsld") {
		t.Fatalf("kubeconfig not renamed:\n%s", s)
	}
}

func TestEnableStopsAtTheEngineStepWhenDockerIsDown(t *testing.T) {
	f := newFake().when("systemctl is-active docker", "inactive\n", 3)
	_, err := mgr(f).Enable(context.Background())
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepEngine {
		t.Fatalf("want engine step error, got %v", err)
	}
	if f.called("--install") != 0 {
		t.Fatal("must not create the distro without a running engine")
	}
}

func TestBootstrapRestartsTheDistroOnceWhenSystemdWasJustEnabled(t *testing.T) {
	f := newFake().
		when("systemctl is-active docker", "active\n", 0).
		once("wsld-k8s.sh install", "", 3).
		when(statusScript, readyNodes, 0).
		when("cat /etc/rancher", k3sYAML, 0)
	f.distros["k8s-engine"] = "Running"
	if _, err := mgr(f).Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.called("--terminate k8s-engine") != 1 || f.called("wsld-k8s.sh install") != 2 {
		t.Fatalf("want one restart and a rerun:\n%v", f.calls)
	}
}

func TestEnableReportsTheReadyStepWhenTheNodeNeverComesUp(t *testing.T) {
	f := newFake().when("systemctl is-active docker", "active\n", 0).when(statusScript, "", 1)
	f.distros["k8s-engine"] = "Running"
	_, err := mgr(f).Enable(context.Background())
	var se *StepError
	if !errors.As(err, &se) || se.Step != StepReady {
		t.Fatalf("want ready step error, got %v", err)
	}
}

func TestStartSkipsInstallWhenAlreadyInstalled(t *testing.T) {
	f := newFake().
		when("systemctl is-active docker", "active\n", 0).
		once(statusScript, "", 8).
		when(statusScript, readyNodes, 0).
		when("cat /etc/rancher", k3sYAML, 0)
	f.distros["k8s-engine"] = "Running"
	if _, err := mgr(f).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.called("wsld-k8s.sh install") != 0 {
		t.Fatal("installed k3s must not be reinstalled on start")
	}
	if f.called("share-engine") != 1 || f.called("systemctl start k3s") != 1 {
		t.Fatalf("start must share and start:\n%v", f.calls)
	}
}

func TestStopRemovesPodContainersBeforeReleasingMounts(t *testing.T) {
	f := newFake()
	f.distros["k8s-engine"] = "Running"
	if err := mgr(f).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	stop := f.index("-d k8s-engine -u root -- systemctl stop k3s")
	rm := f.index("-d docker-engine -u root -- bash -c " + removePodsScript)
	mounts := f.index("-d k8s-engine -u root -- systemctl stop wsld-cri-dockerd wsld-k8s-mounts")
	if stop < 0 || rm < stop || mounts < rm {
		t.Fatalf("order stop=%d rm=%d mounts=%d\n%v", stop, rm, mounts, f.calls)
	}
}

func TestStopAndDisableLeaveAStoppedDistroAlone(t *testing.T) {
	f := newFake()
	f.distros["k8s-engine"] = "Stopped"
	if err := mgr(f).Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.called("-d k8s-engine") != 0 {
		t.Fatal("must not wake a stopped k8s-engine")
	}
	if f.called("unshare-engine") != 1 {
		t.Fatal("disable still stops the engine share")
	}
}

func TestScriptsAvoidOuterShellMetacharacters(t *testing.T) {
	for _, s := range []string{statusScript, removePodsScript} {
		if strings.ContainsAny(s, "$`\\\"") {
			t.Fatalf("script contains characters the outer shell expands: %q", s)
		}
	}
}

func TestParsePodsStatusColumn(t *testing.T) {
	js := `{"items":[
 {"metadata":{"namespace":"kube-system","name":"coredns-1","creationTimestamp":"2026-11-25T07:51:00Z"},"spec":{"containers":[{}]},
  "status":{"phase":"Running","podIP":"172.17.0.2","containerStatuses":[{"ready":true,"restartCount":2,"state":{"running":{}}}]}},
 {"metadata":{"namespace":"default","name":"web","creationTimestamp":"2026-11-25T07:52:00Z"},"spec":{"containers":[{},{}]},
  "status":{"phase":"Running","containerStatuses":[{"ready":true,"restartCount":0,"state":{}},{"ready":false,"restartCount":5,"state":{"waiting":{"reason":"CrashLoopBackOff"}}}]}},
 {"metadata":{"namespace":"kube-system","name":"helm-install","creationTimestamp":"2026-11-25T07:51:00Z"},"spec":{"containers":[{}]},
  "status":{"phase":"Succeeded","containerStatuses":[{"ready":false,"restartCount":0,"state":{"terminated":{"reason":"Completed"}}}]}},
 {"metadata":{"namespace":"default","name":"gone","creationTimestamp":"2026-11-25T07:52:00Z","deletionTimestamp":"2026-11-25T08:00:00Z"},"spec":{"containers":[{}]},
  "status":{"phase":"Running"}}
]}`
	pods, err := ParsePods(js)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, p := range pods {
		got = append(got, p.Namespace+"/"+p.Name+" "+p.Status+" "+p.Ready)
	}
	want := "default/gone Terminating 0/1|default/web CrashLoopBackOff 1/2|kube-system/coredns-1 Running 1/1|kube-system/helm-install Completed 0/1"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %v", got)
	}
	if pods[2].Restarts != 2 || pods[2].IP != "172.17.0.2" || pods[1].Restarts != 5 {
		t.Fatalf("restarts/ip: %+v", pods)
	}
}

func TestRenameKubeconfigOnlyTouchesDefaultNames(t *testing.T) {
	in := k3sYAML + "# default stays in comments\n"
	out := string(RenameKubeconfig([]byte(in), "wsld"))
	if strings.Count(out, "wsld") != 6 || !strings.Contains(out, "# default stays") {
		t.Fatalf("rename:\n%s", out)
	}
}
