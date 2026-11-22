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

// ---- fake wsl.Runner ------------------------------------------------------

type fakeRunner struct {
	mu    sync.Mutex // Status 會並行查詢
	calls []string
	stdin map[string]string
	// resp：args 串接 → 依序回應（耗盡後重複最後一個）；沒有 key 視為未預期並回錯。
	resp map[string][]resp
}

type resp struct {
	out string
	err error
}

func newFake() *fakeRunner { return &fakeRunner{resp: map[string][]resp{}, stdin: map[string]string{}} }

func (f *fakeRunner) on(args string, rs ...resp) { f.resp[args] = rs }

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, key)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin[key] = string(b)
	}
	rs, ok := f.resp[key]
	if !ok || len(rs) == 0 {
		return nil, nil, errors.New("fake: unexpected " + key)
	}
	r := rs[0]
	if len(rs) > 1 {
		f.resp[key] = rs[1:]
	}
	return []byte(r.out), nil, r.err
}

func (f *fakeRunner) idx(sub string) int {
	for i, c := range f.calls {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

const d = "-d Ubuntu-26.04 -u root --"

func (f *fakeRunner) running() {
	f.on("--list --verbose", resp{out: "  NAME            STATE           VERSION\n* Ubuntu-26.04    Running         2\n"})
}

func newMgr(f *fakeRunner) *Manager {
	return &Manager{
		WSL:           f,
		Distro:        "Ubuntu-26.04",
		Bundle:        []byte("#!/bin/bash\necho bundle\n"),
		ReadyAttempts: 3,
		Sleep:         func(time.Duration) {},
		PortBusy:      func(int) bool { return false },
	}
}

const nodesReady = `{"items":[{"metadata":{"name":"framework"},"status":{"nodeInfo":{"kubeletVersion":"v1.37.2+k3s1"},"addresses":[{"type":"InternalIP","address":"172.20.3.68"},{"type":"Hostname","address":"framework"}],"conditions":[{"type":"MemoryPressure","status":"False"},{"type":"Ready","status":"True"}]}}]}`
const nodesNotReady = `{"items":[{"metadata":{"name":"framework"},"status":{"nodeInfo":{"kubeletVersion":"v1.37.2+k3s1"},"addresses":[{"type":"InternalIP","address":"172.20.3.68"}],"conditions":[{"type":"Ready","status":"False"}]}}]}`

// ---- Status ---------------------------------------------------------------

func TestStatusDistroStoppedDoesNotWake(t *testing.T) {
	f := newFake()
	f.on("--list --verbose", resp{out: "  NAME            STATE           VERSION\n* Ubuntu-26.04    Stopped         2\n"})
	st := newMgr(f).Status(context.Background())
	if st.State != StateStopped {
		t.Fatalf("State = %q", st.State)
	}
	if len(f.calls) != 1 {
		t.Fatalf("must not run anything in a stopped distro: %v", f.calls)
	}
}

func TestStatusNotInstalled(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{err: &wsl.ExitError{Code: 1}})
	st := newMgr(f).Status(context.Background())
	if st.State != StateNotInstalled || st.Installed {
		t.Fatalf("%+v", st)
	}
}

func TestStatusOff(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "inactive\n", err: errors.New("exit 3")})
	st := newMgr(f).Status(context.Background())
	if st.State != StateOff || !st.Installed || st.Active {
		t.Fatalf("%+v", st)
	}
}

func TestStatusReadyCollectsDetails(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "active\n"})
	f.on(d+" k3s kubectl get --raw /readyz", resp{out: "ok"})
	f.on(d+" k3s kubectl get nodes -o json", resp{out: nodesReady})
	f.on(d+" systemctl show k3s -p MemoryCurrent --value", resp{out: "439762944\n"})
	f.on(d+" hostname -I", resp{out: "172.20.3.68 172.17.0.1 10.42.0.0\n"})
	st := newMgr(f).Status(context.Background())
	if st.State != StateReady || !st.Ready {
		t.Fatalf("%+v", st)
	}
	if st.NodeName != "framework" || !st.NodeReady || st.Version != "v1.37.2+k3s1" || st.NodeIP != "172.20.3.68" {
		t.Fatalf("node info: %+v", st)
	}
	if st.MemoryBytes != 439762944 {
		t.Fatalf("MemoryBytes = %d", st.MemoryBytes)
	}
	if st.Endpoint != "https://127.0.0.1:26443" || st.Context != "wsld-Ubuntu-26.04" {
		t.Fatalf("endpoint/context: %+v", st)
	}
	if st.IPMismatch {
		t.Fatal("node IP equals eth0, no mismatch")
	}
}

func TestStatusStartingWhenActiveButNotReady(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "active\n"})
	f.on(d+" k3s kubectl get --raw /readyz", resp{err: errors.New("exit 1")})
	st := newMgr(f).Status(context.Background())
	if st.State != StateStarting {
		t.Fatalf("State = %q", st.State)
	}
}

func TestStatusNodeNotReadyIsStarting(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "active\n"})
	f.on(d+" k3s kubectl get --raw /readyz", resp{out: "ok"})
	f.on(d+" k3s kubectl get nodes -o json", resp{out: nodesNotReady})
	f.on(d+" systemctl show k3s -p MemoryCurrent --value", resp{out: "[not set]\n"})
	f.on(d+" hostname -I", resp{out: "172.20.3.99 \n"})
	st := newMgr(f).Status(context.Background())
	if st.State != StateStarting || st.NodeReady {
		t.Fatalf("%+v", st)
	}
	if !st.IPMismatch {
		t.Fatal("node IP 172.20.3.68 != eth0 172.20.3.99 should flag mismatch")
	}
	if st.MemoryBytes != 0 {
		t.Fatalf("[not set] must parse to 0, got %d", st.MemoryBytes)
	}
}

func TestStatusFailedUnitIsError(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "failed\n", err: errors.New("exit 3")})
	st := newMgr(f).Status(context.Background())
	if st.State != StateError {
		t.Fatalf("State = %q", st.State)
	}
}

func TestStatusActivatingIsStarting(t *testing.T) {
	for _, word := range []string{"activating", "reloading"} {
		f := newFake()
		f.running()
		f.on(d+" test -x /usr/local/bin/k3s", resp{})
		f.on(d+" systemctl is-active k3s", resp{out: word + "\n", err: errors.New("exit 3")})
		f.on(d+" k3s kubectl get --raw /readyz", resp{err: errors.New("exit 1")})
		if st := newMgr(f).Status(context.Background()); st.State != StateStarting {
			t.Fatalf("%s: State = %q", word, st.State)
		}
	}
}

func TestStatusDeactivatingIsOff(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active k3s", resp{out: "deactivating\n", err: errors.New("exit 3")})
	if st := newMgr(f).Status(context.Background()); st.State != StateOff {
		t.Fatalf("State = %q", st.State)
	}
}

// wsl.exe 逾時（沒有輸出、非 ExitError）不能被當成「未啟動／未安裝／已停止」。
func TestStatusQueryFailuresAreUnknown(t *testing.T) {
	timeout := errors.New("WSAETIMEDOUT")
	t.Run("is-active", func(t *testing.T) {
		f := newFake()
		f.running()
		f.on(d+" test -x /usr/local/bin/k3s", resp{})
		f.on(d+" systemctl is-active k3s", resp{err: timeout})
		if st := newMgr(f).Status(context.Background()); st.State != StateUnknown {
			t.Fatalf("State = %q", st.State)
		}
	})
	t.Run("test -x", func(t *testing.T) {
		f := newFake()
		f.running()
		f.on(d+" test -x /usr/local/bin/k3s", resp{err: timeout})
		if st := newMgr(f).Status(context.Background()); st.State != StateUnknown {
			t.Fatalf("State = %q", st.State)
		}
	})
	t.Run("list", func(t *testing.T) {
		f := newFake()
		f.on("--list --verbose", resp{err: timeout})
		if st := newMgr(f).Status(context.Background()); st.State != StateUnknown {
			t.Fatalf("State = %q", st.State)
		}
	})
}

// ---- Install / Start / Stop / Reset ----------------------------------------

func TestInstallPipesBundleThenRunsInstall(t *testing.T) {
	f := newFake()
	f.on(d+" bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh install 26443", resp{out: "ok\n"})
	if err := newMgr(f).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.stdin[d+" bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh install 26443"]; !strings.Contains(got, "echo bundle") {
		t.Fatalf("bundle not piped: %q", got)
	}
}

func TestInstallFailureSurfaces(t *testing.T) {
	f := newFake()
	f.on(d+" bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh install 26443", resp{err: errors.New("exit 3")})
	if err := newMgr(f).Install(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestStartInstallsWhenMissingThenStartsAndWaits(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{err: errors.New("exit 1")})
	f.on(d+" bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh install 26443", resp{})
	f.on(d+" systemctl is-active docker", resp{out: "active\n"})
	f.on(d+" systemctl reset-failed k3s", resp{})
	f.on(d+" systemctl start k3s", resp{})
	f.on(d+" k3s kubectl get --raw /readyz", resp{err: errors.New("exit 1")}, resp{out: "ok"})
	f.on(d+" k3s kubectl get nodes -o json", resp{out: nodesNotReady}, resp{out: nodesReady})
	if err := newMgr(f).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !(f.idx("wsld-k3s.sh install 26443") < f.idx("systemctl start k3s")) {
		t.Fatalf("install must precede start: %v", f.calls)
	}
}

func TestStartStartsDockerFirstWhenInactive(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active docker", resp{out: "inactive\n", err: errors.New("exit 3")})
	f.on(d+" systemctl reset-failed docker", resp{})
	f.on(d+" systemctl start docker", resp{})
	f.on(d+" systemctl reset-failed k3s", resp{})
	f.on(d+" systemctl start k3s", resp{})
	f.on(d+" k3s kubectl get --raw /readyz", resp{out: "ok"})
	f.on(d+" k3s kubectl get nodes -o json", resp{out: nodesReady})
	if err := newMgr(f).Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !(f.idx("systemctl start docker") < f.idx("systemctl start k3s")) {
		t.Fatalf("docker must start before k3s: %v", f.calls)
	}
}

func TestStartTimesOutWhenNeverReady(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active docker", resp{out: "active\n"})
	f.on(d+" systemctl reset-failed k3s", resp{})
	f.on(d+" systemctl start k3s", resp{})
	f.on(d+" k3s kubectl get --raw /readyz", resp{err: errors.New("exit 1")})
	err := newMgr(f).Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "ready") {
		t.Fatalf("want not-ready error, got %v", err)
	}
}

func TestStartErrorsWhenSystemctlFails(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active docker", resp{out: "active\n"})
	f.on(d+" systemctl reset-failed k3s", resp{})
	f.on(d+" systemctl start k3s", resp{err: errors.New("exit 1")})
	if err := newMgr(f).Start(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestStopRunsKillallThenRemovesK8sContainers(t *testing.T) {
	f := newFake()
	f.on(d+" /usr/local/bin/k3s-killall.sh", resp{})
	f.on(d+" bash -c "+RemoveContainersScript, resp{})
	if err := newMgr(f).Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	// k3s-killall.sh 不處理 --docker 的 pod 容器（實機驗證：off 後 k8s_* 仍在跑），必須另外移除
	if !(f.idx("k3s-killall.sh") >= 0 && f.idx("k3s-killall.sh") < f.idx("docker ps -aq")) {
		t.Fatalf("killall must run before containers are removed: %v", f.calls)
	}
}

func TestStopSucceedsEvenIfContainerCleanupFails(t *testing.T) {
	f := newFake()
	f.on(d+" /usr/local/bin/k3s-killall.sh", resp{})
	f.on(d+" bash -c "+RemoveContainersScript, resp{err: errors.New("exit 1")})
	if err := newMgr(f).Stop(context.Background()); err != nil {
		t.Fatalf("cleanup is best-effort (docker may be down): %v", err)
	}
}

func TestStopFailsWhenKillallFails(t *testing.T) {
	f := newFake()
	f.on(d+" /usr/local/bin/k3s-killall.sh", resp{err: errors.New("exit 1")})
	if err := newMgr(f).Stop(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// wsl.exe 把 `-- cmd args` 交給外層 shell，含空白的參數以雙引號包起來——雙引號內 `$`（變數／命令替換）、
// 反引號、`\`、`"` 仍會被外層 shell 處理。實機：RemoveContainersScript 原本寫成 `ids=$(docker ps …)`，
// 外層 shell 先執行了命令替換，把容器 ID 當指令執行（「command not found」），容器沒被移除。
func TestBashCScriptsAvoidOuterShellMetacharacters(t *testing.T) {
	for name, script := range map[string]string{"RemoveContainersScript": RemoveContainersScript, "WipeStateScript": WipeStateScript} {
		if strings.ContainsAny(script, "$`\\\"") {
			t.Errorf("%s must not contain $ ` \\ or \" (wsl.exe's outer shell would expand them): %s", name, script)
		}
	}
}

func TestWipeStateScriptHandlesKubeletMountpoint(t *testing.T) {
	// 實機：/var/lib/kubelet 是獨立掛載點（ext4），rm -rf 該目錄會 "Device or resource busy"，
	// 所以只能清空內容而不能刪目錄本身。
	if strings.Contains(WipeStateScript, "rm -rf /var/lib/kubelet") {
		t.Fatalf("must not rm the kubelet dir itself: %s", WipeStateScript)
	}
	for _, want := range []string{"/var/lib/rancher/k3s", "/etc/rancher/k3s", "find /var/lib/kubelet -mindepth 1"} {
		if !strings.Contains(WipeStateScript, want) {
			t.Fatalf("wipe script missing %q: %s", want, WipeStateScript)
		}
	}
}

func TestResetWipesStateThenRestarts(t *testing.T) {
	f := newFake()
	f.running()
	f.on(d+" /usr/local/bin/k3s-killall.sh", resp{})
	f.on(d+" bash -c "+RemoveContainersScript, resp{})
	f.on(d+" bash -c "+WipeStateScript, resp{})
	f.on(d+" test -x /usr/local/bin/k3s", resp{})
	f.on(d+" systemctl is-active docker", resp{out: "active\n"})
	f.on(d+" systemctl reset-failed k3s", resp{})
	f.on(d+" systemctl start k3s", resp{})
	f.on(d+" k3s kubectl get --raw /readyz", resp{out: "ok"})
	f.on(d+" k3s kubectl get nodes -o json", resp{out: nodesReady})
	if err := newMgr(f).Reset(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !(f.idx("k3s-killall.sh") < f.idx("find /var/lib/kubelet") && f.idx("find /var/lib/kubelet") < f.idx("systemctl start k3s")) {
		t.Fatalf("order wrong: %v", f.calls)
	}
}

func TestUninstallRunsBundle(t *testing.T) {
	f := newFake()
	f.on(d+" bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh uninstall", resp{})
	if err := newMgr(f).Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestKubeconfigReadsK3sYaml(t *testing.T) {
	f := newFake()
	f.on(d+" cat /etc/rancher/k3s/k3s.yaml", resp{out: "apiVersion: v1\n"})
	b, err := newMgr(f).Kubeconfig(context.Background())
	if err != nil || string(b) != "apiVersion: v1\n" {
		t.Fatalf("%q %v", b, err)
	}
}

func TestParseMeminfo(t *testing.T) {
	total, avail, err := ParseMeminfo("MemTotal:        4012032 kB\nMemFree:          100000 kB\nMemAvailable:    1930000 kB\nBuffers: 1 kB\n")
	if err != nil {
		t.Fatal(err)
	}
	if total != 4012032*1024 || avail != 1930000*1024 {
		t.Fatalf("total=%d avail=%d", total, avail)
	}
	if _, _, err := ParseMeminfo("garbage"); err == nil {
		t.Fatal("expected error")
	}
}

func TestManagerMemoryReadsProcMeminfo(t *testing.T) {
	f := newFake()
	f.on("-d Ubuntu-26.04 -- cat /proc/meminfo", resp{out: "MemTotal: 2048 kB\nMemAvailable: 1024 kB\n"})
	total, avail, err := newMgr(f).Memory(context.Background())
	if err != nil || total != 2048*1024 || avail != 1024*1024 {
		t.Fatalf("%d %d %v", total, avail, err)
	}
}

func TestPickAPIPortSkipsBusyPorts(t *testing.T) {
	m := &Manager{PortBusy: func(p int) bool { return p == DefaultAPIPort || p == DefaultAPIPort+1 }}
	if got := m.pickAPIPort(); got != DefaultAPIPort+2 {
		t.Fatalf("pickAPIPort = %d", got)
	}
	m = &Manager{APIPort: 30000, PortBusy: func(int) bool { return false }}
	if got := m.pickAPIPort(); got != 30000 {
		t.Fatalf("custom port = %d", got)
	}
	m = &Manager{APIPort: 80} // 小於 1024 視為未設定
	if got := m.apiPort(); got != DefaultAPIPort {
		t.Fatalf("privileged port should fall back, got %d", got)
	}
}

func TestInstallPassesPickedPort(t *testing.T) {
	f := newFake()
	m := newMgr(f)
	m.APIPort = 30001
	m.PortBusy = func(p int) bool { return p == 30001 }
	d := "-d Ubuntu-26.04 -u root --"
	f.on(d+" bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh install 30002", resp{})
	if err := m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
}
