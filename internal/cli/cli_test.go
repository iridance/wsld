package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/i18n"
)

// ---- fakes -----------------------------------------------------------------

type fakeRunner struct {
	resp  map[string]string
	errs  map[string]error
	calls []string
}

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
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
	containers []dockerapi.Container
	started    []string
	stopped    []string
	logs       string
	listErr    error
	inspectTTY bool
}

func (a *fakeAPI) Ping(context.Context) error { return a.pingErr }
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
		"-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750":       "WSLD_API_PORT=23750\n",
	}, errs: map[string]error{}}
	cfgPath := filepath.Join(h.cfgDir, "config.toml")
	if withConfig {
		c := config.Default()
		c.Distro, c.User = "Ubuntu-26.04", "irice"
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
	for _, cmd := range []string{"start", "status", "ps", "doctor"} {
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
	h.wsl.resp["-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750"] = "WSLD_API_PORT=23751\n"
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
	h.wsl.resp["-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user bob --port 24000 --no-compose"] = "WSLD_API_PORT=24000\n"
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
	h.wsl.resp["--install -d Ubuntu --no-launch"] = ""
	if code := h.run("init"); code != 3 {
		t.Fatalf("code=%d err=%s", code, h.errb.String())
	}
}

func TestStartReady(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("start"); code != 0 || !strings.Contains(h.out.String(), "engine ready") {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.errb.String())
	}
}

func TestStartAPIFailureExit4(t *testing.T) {
	h := newHarness(t, true)
	h.api.pingErr = dockerapi.ErrUnreachable
	if code := h.run("start"); code != 4 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
}

func TestStatusTextAndJSON(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("status"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	out := h.out.String()
	for _, want := range []string{"Ubuntu-26.04", "systemd", "docker.service", "tcp://127.0.0.1:23750", "READY"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
	h = newHarness(t, true)
	if code := h.run("status", "--json"); code != 0 || !strings.Contains(h.out.String(), `"APIReachable": true`) {
		t.Fatalf("json: code=%d out=%q", code, h.out.String())
	}
}

func TestStatusExitCodesReflectReadiness(t *testing.T) {
	h := newHarness(t, true)
	h.api.pingErr = dockerapi.ErrUnreachable
	if code := h.run("status"); code != 4 {
		t.Fatalf("api down: code=%d", code)
	}
	h = newHarness(t, true)
	h.wsl.resp["-d Ubuntu-26.04 -- systemctl is-active docker"] = "inactive\n"
	if code := h.run("status"); code != 3 {
		t.Fatalf("docker inactive: code=%d", code)
	}
}

func TestPsListsRunningByDefaultAndAllWithFlag(t *testing.T) {
	h := newHarness(t, true)
	h.api.containers = []dockerapi.Container{
		{ID: "aaaaaaaaaaaaaaaa", Names: []string{"/web"}, Image: "nginx", State: "running", Status: "Up 1h"},
		{ID: "bbbbbbbbbbbbbbbb", Names: []string{"/db"}, Image: "postgres:16", State: "exited", Status: "Exited (0)"},
	}
	if code := h.run("ps"); code != 0 {
		t.Fatal(code)
	}
	if !strings.Contains(h.out.String(), "web") || strings.Contains(h.out.String(), "db") {
		t.Fatalf("ps default should show running only:\n%s", h.out.String())
	}
	h.out.Reset()
	if code := h.run("ps", "-a"); code != 0 || !strings.Contains(h.out.String(), "db") || !strings.Contains(h.out.String(), "aaaaaaaaaaaa") {
		t.Fatalf("ps -a:\n%s", h.out.String())
	}
}

func TestPsJSONAndEmpty(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("ps"); code != 0 || !strings.Contains(h.out.String(), "no containers") {
		t.Fatalf("empty: %q", h.out.String())
	}
	h = newHarness(t, true)
	h.api.containers = []dockerapi.Container{{ID: "a", Names: []string{"/x"}, State: "running"}}
	if code := h.run("ps", "--json"); code != 0 || !strings.Contains(h.out.String(), `"Id": "a"`) {
		t.Fatalf("json: %q", h.out.String())
	}
}

func TestPsAPIDownExit4(t *testing.T) {
	h := newHarness(t, true)
	h.api.listErr = dockerapi.ErrUnreachable
	if code := h.run("ps"); code != 4 || !strings.Contains(h.errb.String(), "Docker API") {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
}

func TestLogsDefaultTail100(t *testing.T) {
	h := newHarness(t, true)
	h.api.logs = "line1\nline2\n"
	if code := h.run("logs", "abc"); code != 0 || h.out.String() != "line1\nline2\n" {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
	if code := newHarness(t, true).run("logs"); code != 2 {
		t.Fatalf("missing id should be usage error, got %d", code)
	}
}

func TestStartStopContainer(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("start-ct", "abc"); code != 0 || len(h.api.started) != 1 || !strings.Contains(h.out.String(), "started abc") {
		t.Fatalf("code=%d started=%v", code, h.api.started)
	}
	if code := h.run("stop-ct", "abc", "--time", "3"); code != 0 || len(h.api.stopped) != 1 {
		t.Fatalf("code=%d stopped=%v", code, h.api.stopped)
	}
	h = newHarness(t, true)
	if code := h.run("start-ct", "missing"); code != 1 || !strings.Contains(h.errb.String(), "No such container") {
		t.Fatalf("404 → exit 1: code=%d err=%q", code, h.errb.String())
	}
}

func TestExecUsesWslDockerExecWithShellFallback(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("exec", "abc"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	if len(h.execs) != 1 {
		t.Fatalf("execs = %v", h.execs)
	}
	got := strings.Join(h.execs[0], " ")
	for _, want := range []string{"wsl.exe", "-d Ubuntu-26.04", "docker exec -it abc", "bash", "sh"} {
		if !strings.Contains(got, want) {
			t.Errorf("exec cmd missing %q: %s", want, got)
		}
	}
}

func TestExecHonoursConfigShellAndExplicitCommand(t *testing.T) {
	h := newHarness(t, true)
	c, _ := config.Load(h.app.ConfigPath)
	c.ExecShell = "zsh"
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("exec", "abc"); code != 0 {
		t.Fatal(code)
	}
	if got := strings.Join(h.execs[0], " "); !strings.HasSuffix(got, "docker exec -it abc zsh") {
		t.Fatalf("got %s", got)
	}
	h.execs = nil
	if code := h.run("exec", "abc", "ls", "-la"); code != 0 {
		t.Fatal(code)
	}
	if got := strings.Join(h.execs[0], " "); !strings.HasSuffix(got, "docker exec -it abc ls -la") {
		t.Fatalf("got %s", got)
	}
}

func TestDoctorTextAndExit(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("doctor"); code != 0 {
		t.Fatalf("code=%d\n%s%s", code, h.out.String(), h.errb.String())
	}
	if !strings.Contains(h.out.String(), "[OK]") || !strings.Contains(h.out.String(), "doctor:") {
		t.Fatalf("out=%s", h.out.String())
	}
	h = newHarness(t, true)
	h.wsl.resp["-d Ubuntu-26.04 -- systemctl is-active docker"] = "inactive\n"
	if code := h.run("doctor"); code != 3 || !strings.Contains(h.out.String(), "[FAIL]") {
		t.Fatalf("code=%d", code)
	}
	h = newHarness(t, true)
	if code := h.run("doctor", "--json"); code != 0 || !strings.Contains(h.out.String(), `"ID": "api_ping"`) {
		t.Fatalf("json: %s", h.out.String())
	}
}

func TestLangOverrideZh(t *testing.T) {
	h := newHarness(t, true)
	h.app.Lang = i18n.ZhTW
	if code := h.run("help"); code != 0 || !strings.Contains(h.out.String(), "用法") {
		t.Fatalf("zh help: %s", h.out.String())
	}
}

func TestUseFirstTimeInitsAndStarts(t *testing.T) {
	h := newHarness(t, false)
	if code := h.run("use", "Ubuntu-26.04"); code != 0 {
		t.Fatalf("code=%d err=%q out=%q", code, h.errb.String(), h.out.String())
	}
	c, err := config.Load(h.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.Active != "Ubuntu-26.04" || c.User != "irice" || c.APIEndpoint == "" {
		t.Fatalf("cfg = %+v", c)
	}
	m, ok := c.FindMachine("Ubuntu-26.04")
	if !ok || !m.Ready() {
		t.Fatalf("want ready machine, got ok=%v m=%+v", ok, m)
	}
	joined := strings.Join(h.wsl.calls, "\n")
	if !strings.Contains(joined, "wsld-bootstrap") {
		t.Fatalf("expected bootstrap on first use, calls:\n%s", joined)
	}
	if !strings.Contains(h.out.String(), "engine ready") && !strings.Contains(h.out.String(), "READY") {
		t.Fatalf("out=%q", h.out.String())
	}
}

func TestUseExistingMachineDoesNotClearEndpoint(t *testing.T) {
	h := newHarness(t, true)
	c, _ := config.Load(h.app.ConfigPath)
	c.UpsertMachine(config.Machine{Distro: "Ubuntu-22.04", User: "bob", APIEndpoint: "tcp://127.0.0.1:23751"})
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("use", "Ubuntu-22.04"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	c, _ = config.Load(h.app.ConfigPath)
	if c.APIEndpoint != "tcp://127.0.0.1:23751" || c.User != "bob" {
		t.Fatalf("cfg = %+v", c)
	}
}

func TestUseRequiresDistroArg(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("use"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestUnmanageStopsDockerAndRemovesProfile(t *testing.T) {
	h := newHarness(t, true)
	c, _ := config.Load(h.app.ConfigPath)
	c.UpsertMachine(config.Machine{Distro: "Ubuntu-22.04", User: "bob", APIEndpoint: "tcp://127.0.0.1:23751"})
	_ = c.SetActive("Ubuntu-26.04")
	_ = config.Save(h.app.ConfigPath, c)
	h.wsl.resp["-d Ubuntu-22.04 -u root -- systemctl stop docker"] = ""
	if code := h.run("unmanage", "Ubuntu-22.04"); code != 0 {
		t.Fatalf("code=%d err=%q", code, h.errb.String())
	}
	c, _ = config.Load(h.app.ConfigPath)
	if _, ok := c.FindMachine("Ubuntu-22.04"); ok {
		t.Fatal("profile should be removed")
	}
	if c.Active != "Ubuntu-26.04" {
		t.Fatalf("active should stay primary, got %q", c.Active)
	}
	joined := strings.Join(h.wsl.calls, "\n")
	if !strings.Contains(joined, "systemctl stop docker") {
		t.Fatalf("expected stop docker, calls:\n%s", joined)
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

func TestUseFailsWhenDistroMissing(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("use", "Ghost"); code != 3 {
		t.Fatalf("code=%d err=%q out=%q", code, h.errb.String(), h.out.String())
	}
	if !strings.Contains(h.errb.String(), "Ghost") && !strings.Contains(h.out.String()+h.errb.String(), "does not exist") {
		// Init returns init_distro_missing
		if !strings.Contains(h.errb.String(), "does not exist") && !strings.Contains(h.errb.String(), "not found") && !strings.Contains(h.errb.String(), "不存在") {
			t.Fatalf("err=%q", h.errb.String())
		}
	}
}

func TestStartAutoInitsPlaceholder(t *testing.T) {
	h := newHarness(t, true)
	c := config.Default()
	c.EnsureActive(config.Machine{Distro: "Ubuntu-26.04"}) // placeholder
	if err := config.Save(h.app.ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	if code := h.run("start"); code != 0 {
		t.Fatalf("code=%d err=%q out=%q", code, h.errb.String(), h.out.String())
	}
	c, err := config.Load(h.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := c.FindMachine("Ubuntu-26.04")
	if !ok || !m.Ready() {
		t.Fatalf("want ready after start, got %+v", m)
	}
}
