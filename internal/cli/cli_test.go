package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/i18n"
)

// ---- fakes -----------------------------------------------------------------

type fakeRunner struct {
	mu     sync.Mutex
	resp   map[string]string
	errs   map[string]error
	calls  []string
	onCall func(key string)
}

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.mu.Unlock()
	if f.onCall != nil {
		f.onCall(key)
	}
	if stdin != nil {
		_, _ = io.Copy(io.Discard, stdin)
	}
	if err, ok := f.errs[key]; ok {
		return []byte(f.resp[key]), nil, err
	}
	out, ok := f.resp[key]
	if !ok {
		return nil, nil, errors.New("fake: unexpected " + key)
	}
	return []byte(out), nil, nil
}

type fakeAPI struct {
	pingErr    error
	pingSeq    []error // 非空時依序回傳（耗盡後回 pingErr）；模擬「引擎原本沒起來，bootstrap 後才通」
	pings      int
	containers []dockerapi.Container
	started    []string
	stopped    []string
	logs       string
	listErr    error
	inspectTTY bool
	mounts     map[string][]dockerapi.ContainerMount // 容器 id → inspect 的 Mounts
}

func (a *fakeAPI) Ping(context.Context) error {
	a.pings++
	if a.pings <= len(a.pingSeq) {
		return a.pingSeq[a.pings-1]
	}
	return a.pingErr
}
func (a *fakeAPI) ListContainers(_ context.Context, all bool) ([]dockerapi.Container, error) {
	if a.listErr != nil {
		return nil, a.listErr
	}
	if all {
		return a.containers, nil
	}
	var out []dockerapi.Container
	for _, c := range a.containers {
		if c.State == "running" {
			out = append(out, c)
		}
	}
	return out, nil
}
func (a *fakeAPI) StartContainer(_ context.Context, id string) error {
	if id == "missing" {
		return &dockerapi.APIError{Status: 404, Message: "No such container"}
	}
	a.started = append(a.started, id)
	return nil
}
func (a *fakeAPI) StopContainer(_ context.Context, id string, _ int) error {
	a.stopped = append(a.stopped, id)
	return nil
}
func (a *fakeAPI) Logs(_ context.Context, id string, opts dockerapi.LogsOptions) (io.ReadCloser, error) {
	if a.pingErr != nil {
		return nil, a.pingErr
	}
	return io.NopCloser(strings.NewReader(a.logs)), nil
}
func (a *fakeAPI) Inspect(_ context.Context, id string) (dockerapi.ContainerDetail, error) {
	var d dockerapi.ContainerDetail
	d.ID = id
	d.Config.Tty = a.inspectTTY
	d.Mounts = a.mounts[id]
	return d, nil
}

func utf16(s string) string {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE})
	for _, r := range s {
		b.WriteByte(byte(r))
		b.WriteByte(byte(r >> 8))
	}
	return b.String()
}

type harness struct {
	app    *App
	out    *bytes.Buffer
	errb   *bytes.Buffer
	api    *fakeAPI
	wsl    *fakeRunner
	cfgDir string
	execs  [][]string
}

func newHarness(t *testing.T, withConfig bool) *harness {
	t.Helper()
	i18n.Set(i18n.EN)
	h := &harness{out: &bytes.Buffer{}, errb: &bytes.Buffer{}, api: &fakeAPI{}, cfgDir: t.TempDir()}
	h.wsl = &fakeRunner{resp: map[string]string{
		"--list --verbose":                                                      utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n"),
		"-d Ubuntu-26.04 -- true":                                               "",
		"-d Ubuntu-26.04 -- systemctl is-system-running":                        "running\n",
		"-d Ubuntu-26.04 -- systemctl is-active docker":                         "active\n",
		"-d Ubuntu-26.04 -- ss -Hltn":                                           "LISTEN 0 4096 127.0.0.1:23750 0.0.0.0:*\n",
		"-d Ubuntu-26.04 -- cat /etc/docker/daemon.json":                        `{"hosts":["unix:///var/run/docker.sock","tcp://127.0.0.1:23750"]}`,
		"-d Ubuntu-26.04 -- loginctl show-user irice --property=Linger --value": "yes\n",
		"-d Ubuntu-26.04 -- id -un":                                             "irice\n",
		"-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750 --role engine": "WSLD_API_PORT=23750\n",
	}, errs: map[string]error{}}
	cfgPath := filepath.Join(h.cfgDir, "config.toml")
	if withConfig {
		c := config.Default()
		c.UpsertMachine(config.Machine{Distro: "Ubuntu-26.04", User: "irice", APIEndpoint: config.EndpointForPort(config.DefaultPort)})
		_ = c.SetEngine("Ubuntu-26.04") // 這台就是共用 engine
		_ = c.SetActive("Ubuntu-26.04")
		if err := config.Save(cfgPath, c); err != nil {
			t.Fatal(err)
		}
	}
	h.app = &App{
		Stdout:     h.out,
		Stderr:     h.errb,
		ConfigPath: cfgPath,
		WSL:        h.wsl,
		NewAPI:     func(config.Config) API { return h.api },
		Processes:  func(context.Context) ([]string, error) { return nil, nil },
		Dial:       func(context.Context, string) error { return errors.New("refused") },
		Bundle:     []byte("BUNDLE"),
		Exec: func(_ context.Context, name string, args ...string) error {
			h.execs = append(h.execs, append([]string{name}, args...))
			return nil
		},
		Version:      "test",
		PingInterval: time.Millisecond,
	}
	return h
}

func (h *harness) run(args ...string) int { return h.app.Run(context.Background(), args) }

// ---- tests ------------------------------------------------------------------

func TestHelpAndUnknownCommand(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run(); code != 2 || !strings.Contains(h.errb.String(), "Usage") {
		t.Fatalf("no args: code=%d err=%q", code, h.errb.String())
	}
	h = newHarness(t, true)
	if code := h.run("help"); code != 0 || !strings.Contains(h.out.String(), "Usage") {
		t.Fatalf("help: code=%d", code)
	}
	h = newHarness(t, true)
	if code := h.run("bogus"); code != 2 || !strings.Contains(h.errb.String(), "unknown command") {
		t.Fatalf("bogus: code=%d err=%q", code, h.errb.String())
	}
}

func TestVersion(t *testing.T) {
	h := newHarness(t, false)
	if code := h.run("version"); code != 0 || !strings.Contains(h.out.String(), "test") {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
}

func TestCommandsRequireConfigExceptInit(t *testing.T) {
	for _, cmd := range []string{"status", "doctor"} {
		h := newHarness(t, false)
		if code := h.run(cmd); code != 3 || !strings.Contains(h.errb.String(), "wsld init") {
			t.Fatalf("%s without config: code=%d err=%q", cmd, code, h.errb.String())
		}
	}
}

func TestInitWritesConfig(t *testing.T) {
	h := newHarness(t, false)
	if code := h.run("init"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb.String())
	}
	c, err := config.Load(h.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Distro != "Ubuntu-26.04" || c.User != "irice" || c.APIEndpoint != "tcp://127.0.0.1:23750" {
		t.Fatalf("cfg = %+v", c)
	}
	if !strings.Contains(h.out.String(), "config written") {
		t.Fatalf("out = %q", h.out.String())
	}
}

func TestInitPortChangeIsPersisted(t *testing.T) {
	h := newHarness(t, false)
	h.wsl.resp["-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750 --role engine"] = "WSLD_API_PORT=23751\n"
	if code := h.run("init"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb.String())
	}
	c, _ := config.Load(h.app.ConfigPath)
	if c.APIEndpoint != "tcp://127.0.0.1:23751" {
		t.Fatalf("endpoint = %s", c.APIEndpoint)
	}
}

func TestInitFlags(t *testing.T) {
	h := newHarness(t, false)
	h.wsl.resp["-d Ubuntu-26.04 -u root -- id -u bob"] = "1000\n"
	h.wsl.resp["-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user bob --port 24000 --no-compose --role engine"] = "WSLD_API_PORT=24000\n"
	if code := h.run("init", "--user", "bob", "--port", "24000", "--no-compose"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb.String())
	}
	if code := newHarness(t, false).run("init", "--bogus"); code != 2 {
		t.Fatalf("bad flag code=%d", code)
	}
}

func TestInitNotReadyExit3(t *testing.T) {
	h := newHarness(t, false)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n")
	h.wsl.resp["--install -d Ubuntu --vhd-size 60GB --no-launch"] = ""
	if code := h.run("init"); code != 3 {
		t.Fatalf("code=%d err=%s", code, h.errb.String())
	}
}

func TestInitInstallDiskLimitFlag(t *testing.T) {
	h := newHarness(t, false)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n")
	h.wsl.resp["--install -d Ubuntu --vhd-size 120GB --no-launch"] = ""
	if code := h.run("init", "--vhd-size", "120"); code != 3 {
		t.Fatalf("code=%d err=%s", code, h.errb.String())
	}
	// 0 = WSL 預設（不帶 --vhd-size）
	h = newHarness(t, false)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n")
	h.wsl.resp["--install -d Ubuntu --no-launch"] = ""
	if code := h.run("init", "--vhd-size", "0"); code != 3 {
		t.Fatalf("code=%d err=%s", code, h.errb.String())
	}
	h = newHarness(t, false)
	if code := h.run("init", "--vhd-size", "4"); code != 2 {
		t.Fatalf("invalid size code=%d err=%s", code, h.errb.String())
	}
}

func TestResizeDiskRunningMachineNeedsShutdownFlag(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("resize-disk", "Ubuntu-26.04", "60"); code != 3 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	if !strings.Contains(h.errb.String(), "--shutdown") || !strings.Contains(h.errb.String(), "Ubuntu-26.04") {
		t.Fatalf("err should explain --shutdown and list running machines: %q", h.errb.String())
	}
	for _, c := range h.wsl.calls {
		if strings.HasPrefix(c, "--shutdown") || strings.HasPrefix(c, "--terminate") || strings.Contains(c, "systemctl stop") {
			t.Fatalf("nothing may be stopped without --shutdown; calls=%v", h.wsl.calls)
		}
	}
}

func TestResizeDiskWithShutdownRestartsRunningMachines(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["-d Ubuntu-26.04 -u root -- systemctl stop docker"] = ""
	h.wsl.resp["--shutdown"] = ""
	h.wsl.resp["--manage Ubuntu-26.04 --resize 60GB"] = ""
	if code := h.run("resize-disk", "--shutdown", "Ubuntu-26.04", "60"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	want := []string{"systemctl stop docker", "--shutdown", "--manage Ubuntu-26.04 --resize 60GB", "-d Ubuntu-26.04 -- true"}
	joined := strings.Join(h.wsl.calls, "\n")
	last := -1
	for _, w := range want {
		i := strings.Index(joined, w)
		if i < 0 || i < last {
			t.Fatalf("expected %q in order; calls:\n%s", w, joined)
		}
		last = i
	}
	if !strings.Contains(h.out.String(), "60 GB") {
		t.Fatalf("out=%q", h.out.String())
	}
	// 旗標放在後面也可以
	h = newHarness(t, true)
	h.wsl.resp["-d Ubuntu-26.04 -u root -- systemctl stop docker"] = ""
	h.wsl.resp["--shutdown"] = ""
	h.wsl.resp["--manage Ubuntu-26.04 --resize 60GB"] = ""
	if code := h.run("resize-disk", "Ubuntu-26.04", "60", "--shutdown"); code != 0 {
		t.Fatalf("trailing flag code=%d err=%q", code, h.errb.String())
	}
}

func TestResizeDiskTooSmallIsRefusedBeforeShutdown(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["-d Ubuntu-26.04 -u root -- findmnt -n -o SOURCE /"] = "/dev/sdd\n"
	h.wsl.resp["-d Ubuntu-26.04 -u root -- stat -f -c %S /"] = "4096\n"
	h.wsl.resp["-d Ubuntu-26.04 -u root -- resize2fs -P /dev/sdd"] = "Estimated minimum size of the filesystem: 18390785\n"
	if code := h.run("resize-disk", "--shutdown", "Ubuntu-26.04", "60"); code != 1 || !strings.Contains(h.errb.String(), "72") {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	for _, c := range h.wsl.calls {
		if c == "--shutdown" || strings.HasPrefix(c, "--manage") {
			t.Fatalf("must not shut down or resize; calls=%v", h.wsl.calls)
		}
	}
	// 停止中（無法預估）：WSL 回報太小 → 易讀訊息
	h = newHarness(t, true)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n  dev Stopped 2\r\n")
	h.wsl.resp["--manage dev --resize 60GB"] = "e2fsck 1.47.0\r\n/usr/sbin/resize2fs: New size smaller than minimum (18390839)\r\n"
	h.wsl.errs = map[string]error{"--manage dev --resize 60GB": errors.New("exit 255")}
	if code := h.run("resize-disk", "dev", "60"); code != 1 || !strings.Contains(h.errb.String(), "72") || strings.Contains(h.errb.String(), "e2fsck") {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
}

func TestResizeDiskStoppedDetachedNeedsNoShutdown(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n  dev Stopped 2\r\n")
	h.wsl.resp["--manage dev --resize 80GB"] = ""
	if code := h.run("resize-disk", "dev", "80"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	joined := strings.Join(h.wsl.calls, "\n")
	if strings.Contains(joined, "--shutdown") || strings.Contains(joined, "-d dev -- true") {
		t.Fatalf("free VHD: no shutdown, no wake; calls:\n%s", joined)
	}
}

func TestResizeDiskStoppedButAttachedNeedsShutdown(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n  dev Stopped 2\r\n")
	h.wsl.resp["--manage dev --resize 80GB"] = "Error code: Wsl/Service/WSL_E_DISTRO_NOT_STOPPED\r\n"
	h.wsl.errs = map[string]error{"--manage dev --resize 80GB": errors.New("exit 255")}
	if code := h.run("resize-disk", "dev", "80"); code != 3 || !strings.Contains(h.errb.String(), "--shutdown") {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
}

func TestResizeDiskErrors(t *testing.T) {
	for _, args := range [][]string{{"resize-disk"}, {"resize-disk", "Ubuntu-26.04"}, {"resize-disk", "Ubuntu-26.04", "abc"}, {"resize-disk", "Ubuntu-26.04", "4"}, {"resize-disk", "a", "b", "c"}, {"resize-disk", "--bogus", "a", "60"}} {
		h := newHarness(t, true)
		if code := h.run(args...); code != 2 {
			t.Fatalf("%v: code=%d err=%q", args, code, h.errb.String())
		}
	}
	h := newHarness(t, true)
	if code := h.run("resize-disk", "Ghost", "60"); code != 3 {
		t.Fatalf("missing distro code=%d err=%q", code, h.errb.String())
	}
	// wsld.exe 從 WSL 檔案系統執行：拒絕關閉 WSL
	h = newHarness(t, true)
	h.app.SelfInWSL = true
	if code := h.run("resize-disk", "--shutdown", "Ubuntu-26.04", "60"); code != 2 {
		t.Fatalf("self in wsl code=%d err=%q", code, h.errb.String())
	}
	// wsl.exe 失敗：回 1，仍把原本執行中的機器開回來
	h = newHarness(t, true)
	h.wsl.resp["-d Ubuntu-26.04 -u root -- systemctl stop docker"] = ""
	h.wsl.resp["--shutdown"] = ""
	h.wsl.errs = map[string]error{"--manage Ubuntu-26.04 --resize 8GB": errors.New("too small")}
	if code := h.run("resize-disk", "--shutdown", "Ubuntu-26.04", "8"); code != 1 {
		t.Fatalf("failed resize code=%d err=%q", code, h.errb.String())
	}
	if !strings.Contains(strings.Join(h.wsl.calls, "\n"), "-d Ubuntu-26.04 -- true") {
		t.Fatalf("should wake the machine again; calls=%v", h.wsl.calls)
	}
}

func TestUnregisterRequiresConfirmName(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("unregister", "Ubuntu-26.04"); code != 2 {
		t.Fatalf("missing confirm: code=%d", code)
	}
	h = newHarness(t, true)
	if code := h.run("unregister", "Ubuntu-26.04", "--confirm", "Wrong"); code != 2 {
		t.Fatalf("wrong confirm: code=%d err=%q", code, h.errb.String())
	}
}

func TestUnregisterRemovesDistroAndProfile(t *testing.T) {
	h := newHarness(t, true)
	c, _ := config.Load(h.app.ConfigPath)
	c.UpsertMachine(config.Machine{Distro: "Ubuntu-22.04", User: "bob", APIEndpoint: "tcp://127.0.0.1:23751"})
	_ = c.SetActive("Ubuntu-26.04")
	_ = config.Save(h.app.ConfigPath, c)
	h.wsl.resp["-d Ubuntu-22.04 -u root -- systemctl stop docker"] = ""
	h.wsl.resp["--unregister Ubuntu-22.04"] = ""
	if code := h.run("unregister", "Ubuntu-22.04", "--confirm", "Ubuntu-22.04"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	c, _ = config.Load(h.app.ConfigPath)
	if _, ok := c.FindMachine("Ubuntu-22.04"); ok {
		t.Fatal("profile should be gone")
	}
	joined := strings.Join(h.wsl.calls, "\n")
	if !strings.Contains(joined, "--unregister Ubuntu-22.04") {
		t.Fatalf("calls:\n%s", joined)
	}
}

// 需要引擎的指令會自動把尚未 init 的 engine profile 帶起來（不再有獨立的 start）。
func TestEngineCommandAutoInitsPlaceholderEngine(t *testing.T) {
	h := newHarness(t, true)
	c := config.Default()
	c.UpsertMachine(config.Machine{Distro: "Ubuntu-26.04"}) // 占位：未 init
	_ = c.SetEngine("Ubuntu-26.04")
	_ = c.SetActive("Ubuntu-26.04")
	if err := config.Save(h.app.ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	if code := h.run("ps"); code != 0 {
		t.Fatalf("code=%d err=%q out=%q", code, h.errb.String(), h.out.String())
	}
	c, err := config.Load(h.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := c.FindMachine("Ubuntu-26.04")
	if !ok || !m.Ready() || !m.IsEngine() {
		t.Fatalf("want a ready engine after ps, got %+v", m)
	}
}

// 引擎已就緒時不碰 WSL（快速路徑）。
func TestEngineCommandSkipsBringUpWhenAPIIsUp(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("ps"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	if len(h.wsl.calls) != 0 {
		t.Fatalf("ready engine must not be touched: %v", h.wsl.calls)
	}
}

// 沒有任何 config 時，需要引擎的指令首次使用就自動建立 docker-engine。
func TestEngineCommandCreatesWsldEngineOnFirstUse(t *testing.T) {
	h := newHarness(t, false)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n  docker-engine Running 2\r\n")
	h.wsl.resp["-d docker-engine -u root -- id -u irice"] = "1000\n"
	h.wsl.resp["-d docker-engine -- id -un"] = "irice\n"
	h.wsl.resp["-d docker-engine -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750 --role engine"] = "WSLD_API_PORT=23750\n"
	h.wsl.resp["-d docker-engine -- true"] = ""
	h.wsl.resp["-d docker-engine -- systemctl is-system-running"] = "running\n"
	h.wsl.resp["-d docker-engine -- systemctl is-active docker"] = "active\n"
	h.api.pingSeq = []error{errors.New("down")}
	if code := h.run("ps"); code != 0 {
		t.Fatalf("code=%d err=%q out=%q", code, h.errb.String(), h.out.String())
	}
	c, err := config.Load(h.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := c.EngineMachine(); !ok || e.Distro != "docker-engine" || !e.Ready() {
		t.Fatalf("engine = %+v %v", e, ok)
	}
}

func TestCompactDisk(t *testing.T) {
	// VHD 未被占用：直接壓縮，報告釋放量
	h := newHarness(t, true)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n  dev Stopped 2\r\n")
	h.wsl.resp["--manage dev --compact"] = ""
	sizes := map[string]int64{"dev": 8 << 30}
	h.app.DiskSize = func(n string) (int64, error) { return sizes[n], nil }
	h.wsl.onCall = func(key string) {
		if key == "--manage dev --compact" {
			sizes["dev"] = 3 << 30
		}
	}
	if code := h.run("compact-disk", "dev"); code != 0 || !strings.Contains(h.out.String(), "5.0 GB") {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.errb.String())
	}
	// 執行中：未帶 --shutdown → 說明並結束，不停任何東西
	h = newHarness(t, true)
	if code := h.run("compact-disk", "Ubuntu-26.04"); code != 3 || !strings.Contains(h.errb.String(), "--shutdown") {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	// 帶 --shutdown：關 WSL、壓縮、重啟
	h = newHarness(t, true)
	h.wsl.resp["-d Ubuntu-26.04 -u root -- systemctl stop docker"] = ""
	h.wsl.resp["--shutdown"] = ""
	h.wsl.resp["--manage Ubuntu-26.04 --compact"] = ""
	if code := h.run("compact-disk", "--shutdown", "Ubuntu-26.04"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	joined := strings.Join(h.wsl.calls, "\n")
	for _, w := range []string{"--shutdown", "--manage Ubuntu-26.04 --compact", "-d Ubuntu-26.04 -- true"} {
		if !strings.Contains(joined, w) {
			t.Fatalf("missing %q; calls:\n%s", w, joined)
		}
	}
	for _, args := range [][]string{{"compact-disk"}, {"compact-disk", "a", "b"}, {"compact-disk", "--bogus", "a"}} {
		h := newHarness(t, true)
		if code := h.run(args...); code != 2 {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
}

func TestResizeDiskAlsoCompacts(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n  dev Stopped 2\r\n")
	h.wsl.resp["--manage dev --resize 80GB"] = ""
	h.wsl.resp["--manage dev --compact"] = ""
	if code := h.run("resize-disk", "dev", "80"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	joined := strings.Join(h.wsl.calls, "\n")
	if i, j := strings.Index(joined, "--resize 80GB"), strings.Index(joined, "--manage dev --compact"); i < 0 || j < i {
		t.Fatalf("compact must follow resize; calls:\n%s", joined)
	}
}

func TestEngineCannotBeUnmanagedOrUnregistered(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("unmanage", "Ubuntu-26.04"); code != ExitError || !strings.Contains(h.errb.String(), "engine") {
		t.Fatalf("unmanage: code=%d err=%q", code, h.errb.String())
	}
	h.errb.Reset()
	if code := h.run("unregister", "Ubuntu-26.04", "--confirm", "Ubuntu-26.04"); code != ExitError || !strings.Contains(h.errb.String(), "engine") {
		t.Fatalf("unregister: code=%d err=%q", code, h.errb.String())
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "--unregister") || strings.Contains(c, "stop docker") {
			t.Fatalf("the engine must not be touched: %s", c)
		}
	}
	if _, ok := h.cfg(t).EngineMachine(); !ok {
		t.Fatal("engine profile must stay")
	}
}

// 引擎只是停著（冷啟動）時不需要重跑 bootstrap：喚醒並等 API 就好。重跑會重啟 dockerd，而且很慢。
func TestColdEngineIsWokenWithoutRerunningTheBootstrap(t *testing.T) {
	h := newHarness(t, true)
	h.api.pingSeq = []error{errors.New("connection refused"), errors.New("connection refused")} // 引擎沒開：ensureEngine 與後續探測都失敗，Start 喚醒後才通
	if code := h.run("ps"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "wsld-bootstrap.sh") {
			t.Fatalf("a stopped engine must not be re-bootstrapped: %s", c)
		}
	}
}

// docker 已經在跑卻一直連不上（埠被占／endpoint 錯）時，才重跑 Init 選可用埠並再試一次。
func TestUnreachableEngineAfterStartRerunsInitOnceAndRetries(t *testing.T) {
	h := newHarness(t, true)
	down := errors.New("connection refused")
	h.api.pingSeq = make([]error, 21) // ensureEngine 的探測＋第一次 Start 的 20 次等待都失敗
	for i := range h.api.pingSeq {
		h.api.pingSeq[i] = down
	}
	if code := h.run("ps"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	n := 0
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "wsld-bootstrap.sh") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("bootstrap must run exactly once after the first Start failed, ran %d times", n)
	}
}

func (h *harness) cfg(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load(h.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
