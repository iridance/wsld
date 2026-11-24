package doctor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/wsl"
)

type fakeRunner struct {
	resp map[string]struct {
		out string
		err error
	}
}

func (f *fakeRunner) Run(_ context.Context, _ io.Reader, args ...string) ([]byte, []byte, error) {
	r, ok := f.resp[strings.Join(args, " ")]
	if !ok {
		return nil, nil, errors.New("fake: unexpected " + strings.Join(args, " "))
	}
	return []byte(r.out), nil, r.err
}

type fakePinger struct{ err error }

func (p fakePinger) Ping(context.Context) error { return p.err }

func utf16(s string) string {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xFE})
	for _, r := range s {
		b.WriteByte(byte(r))
		b.WriteByte(byte(r >> 8))
	}
	return b.String()
}

type kv = struct {
	out string
	err error
}

func healthy() map[string]kv {
	return map[string]kv{
		"--list --verbose":                                                      {out: utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n")},
		"-d Ubuntu-26.04 -- true":                                               {},
		"-d Ubuntu-26.04 -- systemctl is-system-running":                        {out: "running\n"},
		"-d Ubuntu-26.04 -- systemctl is-active docker":                         {out: "active\n"},
		"-d Ubuntu-26.04 -- ss -Hltn":                                           {out: "LISTEN 0 4096 127.0.0.1:23750 0.0.0.0:*\nLISTEN 0 4096 0.0.0.0:22 0.0.0.0:*\n"},
		"-d Ubuntu-26.04 -- cat /etc/docker/daemon.json":                        {out: `{"hosts":["unix:///var/run/docker.sock","tcp://127.0.0.1:23750"]}`},
		"-d Ubuntu-26.04 -- loginctl show-user irice --property=Linger --value": {out: "yes\n"},
	}
}

func deps(r map[string]kv, ping error, procs []string) Deps {
	cfg := config.Default()
	cfg.Distro, cfg.User = "Ubuntu-26.04", "irice"
	return Deps{
		WSL:       &fakeRunner{resp: r},
		Cfg:       cfg,
		API:       fakePinger{err: ping},
		Processes: func(context.Context) ([]string, error) { return procs, nil },
		Dial:      func(context.Context, string) error { return errors.New("refused") },
	}
}

func find(rep Report, id string) Result {
	for _, r := range rep.Results {
		if r.ID == id {
			return r
		}
	}
	return Result{ID: "missing:" + id}
}

func TestHealthyReport(t *testing.T) {
	rep := Run(context.Background(), deps(healthy(), nil, nil))
	for _, id := range []string{"wsl", "distro", "wake", "systemd", "docker_service", "api_ping", "loopback_only", "linger", "docker_desktop"} {
		if r := find(rep, id); r.Level != OK {
			t.Errorf("%s: level=%v detail=%q", id, r.Level, r.Detail)
		}
	}
	if find(rep, "repo_location").Level != Info {
		t.Error("repo_location should be Info")
	}
	if rep.ExitCode() != 0 {
		t.Fatalf("exit = %d", rep.ExitCode())
	}
}

func TestWSLMissingSkipsDependentChecks(t *testing.T) {
	r := map[string]kv{"--list --verbose": {err: errors.New("not found")}}
	rep := Run(context.Background(), deps(r, dockerapi.ErrUnreachable, nil))
	if find(rep, "wsl").Level != Fail {
		t.Fatal("wsl should fail")
	}
	if d := find(rep, "distro"); d.Level != Fail || !strings.Contains(d.Detail, "skipped") {
		t.Fatalf("distro = %+v", d)
	}
	if rep.ExitCode() != 3 {
		t.Fatalf("exit = %d", rep.ExitCode())
	}
}

func TestDistroWSL1Fails(t *testing.T) {
	r := healthy()
	r["--list --verbose"] = kv{out: utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 1\r\n")}
	rep := Run(context.Background(), deps(r, nil, nil))
	if d := find(rep, "distro"); d.Level != Fail || !strings.Contains(d.Hint, "--set-version") {
		t.Fatalf("distro = %+v", d)
	}
}

func TestSystemdOffFailsWithWslConfHint(t *testing.T) {
	r := healthy()
	r["-d Ubuntu-26.04 -- systemctl is-system-running"] = kv{out: "offline\n", err: &wsl.ExitError{Code: 1}}
	rep := Run(context.Background(), deps(r, nil, nil))
	if d := find(rep, "systemd"); d.Level != Fail || !strings.Contains(d.Hint, "systemd=true") {
		t.Fatalf("systemd = %+v", d)
	}
	if rep.ExitCode() != 3 {
		t.Fatalf("exit = %d", rep.ExitCode())
	}
}

func TestAPIUnreachableExit4(t *testing.T) {
	r := healthy()
	rep := Run(context.Background(), deps(r, dockerapi.ErrUnreachable, nil))
	d := find(rep, "api_ping")
	if d.Level != Fail || !strings.Contains(d.Hint, "wsld start") {
		t.Fatalf("api_ping = %+v", d)
	}
	if rep.ExitCode() != 4 {
		t.Fatalf("exit = %d", rep.ExitCode())
	}
}

func TestAPIPortOccupiedByOtherService(t *testing.T) {
	dp := deps(healthy(), errors.New(`unexpected ping response "<html>"`), nil)
	dp.Dial = func(context.Context, string) error { return nil } // 埠有人聽但不是 docker
	rep := Run(context.Background(), dp)
	d := find(rep, "api_ping")
	if d.Level != Fail || !strings.Contains(d.Detail, "not Docker") && !strings.Contains(d.Detail, "非 Docker") {
		t.Fatalf("api_ping = %+v", d)
	}
}

func TestLoopbackViolationDetected(t *testing.T) {
	r := healthy()
	r["-d Ubuntu-26.04 -- ss -Hltn"] = kv{out: "LISTEN 0 4096 0.0.0.0:23750 0.0.0.0:*\n"}
	rep := Run(context.Background(), deps(r, nil, nil))
	if d := find(rep, "loopback_only"); d.Level != Fail || !strings.Contains(d.Detail, "0.0.0.0:23750") {
		t.Fatalf("loopback_only = %+v", d)
	}
}

func TestLoopbackViolationInDaemonJSON(t *testing.T) {
	r := healthy()
	r["-d Ubuntu-26.04 -- cat /etc/docker/daemon.json"] = kv{out: `{"hosts":["tcp://0.0.0.0:2375"]}`}
	rep := Run(context.Background(), deps(r, nil, nil))
	if d := find(rep, "loopback_only"); d.Level != Fail {
		t.Fatalf("loopback_only = %+v", d)
	}
}

func TestIPv6LoopbackAccepted(t *testing.T) {
	r := healthy()
	r["-d Ubuntu-26.04 -- ss -Hltn"] = kv{out: "LISTEN 0 4096 [::1]:23750 [::]:*\n"}
	rep := Run(context.Background(), deps(r, nil, nil))
	if d := find(rep, "loopback_only"); d.Level != OK {
		t.Fatalf("loopback_only = %+v", d)
	}
}

func TestLingerOffWarns(t *testing.T) {
	r := healthy()
	r["-d Ubuntu-26.04 -- loginctl show-user irice --property=Linger --value"] = kv{out: "no\n"}
	rep := Run(context.Background(), deps(r, nil, nil))
	if d := find(rep, "linger"); d.Level != Warn || !strings.Contains(d.Hint, "enable-linger") {
		t.Fatalf("linger = %+v", d)
	}
	if rep.ExitCode() != 0 {
		t.Fatal("warnings must not change exit code")
	}
}

func TestDockerDesktopProcessWarns(t *testing.T) {
	rep := Run(context.Background(), deps(healthy(), nil, []string{"explorer.exe", "Docker Desktop.exe", "com.docker.backend.exe"}))
	if d := find(rep, "docker_desktop"); d.Level != Warn || !strings.Contains(d.Detail, "Docker Desktop.exe") {
		t.Fatalf("docker_desktop = %+v", d)
	}
}

func TestDockerDesktopDistroRunningWarns(t *testing.T) {
	r := healthy()
	r["--list --verbose"] = kv{out: utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n  docker-desktop Running 2\r\n")}
	rep := Run(context.Background(), deps(r, nil, nil))
	if d := find(rep, "docker_desktop"); d.Level != Warn || !strings.Contains(d.Detail, "docker-desktop") {
		t.Fatalf("docker_desktop = %+v", d)
	}
}

func TestParseTasklistCSV(t *testing.T) {
	csv := "\"System Idle Process\",\"0\",\"Services\",\"0\",\"8 K\"\r\n\"Docker Desktop.exe\",\"1234\",\"Console\",\"1\",\"100,000 K\"\r\n"
	got := parseTasklistCSV(csv)
	if len(got) != 2 || got[1] != "Docker Desktop.exe" {
		t.Fatalf("got %v", got)
	}
}

func TestReportSummaryCounts(t *testing.T) {
	r := healthy()
	r["-d Ubuntu-26.04 -- loginctl show-user irice --property=Linger --value"] = kv{out: "no\n"}
	rep := Run(context.Background(), deps(r, nil, nil))
	ok, warn, fail := rep.Counts()
	if warn != 1 || fail != 0 || ok == 0 {
		t.Fatalf("counts = %d %d %d", ok, warn, fail)
	}
}

func TestLANCheck(t *testing.T) {
	cases := []struct {
		name    string
		access  bool
		legacy  lan.Legacy
		level   Level
		present bool
		want    string
	}{
		{"off and nat", false, lan.Legacy{}, OK, false, ""},
		{"on", true, lan.Legacy{}, Info, true, "wsld-gui"},
		{"mirrored left over", true, lan.Legacy{Mirrored: true, Rules: 2}, Warn, true, "restore-nat"},
		{"mirrored while off", false, lan.Legacy{Mirrored: true}, Warn, true, "restore-nat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := deps(healthy(), nil, nil)
			d.Cfg.LANAccess = c.access
			d.LAN = func(context.Context) (lan.Legacy, error) { return c.legacy, nil }
			rep := Run(context.Background(), d)
			r := find(rep, "lan")
			if !c.present {
				if r.ID != "missing:lan" {
					t.Fatalf("unexpected %+v", r)
				}
				return
			}
			if r.Level != c.level || !strings.Contains(r.Detail+r.Hint, c.want) {
				t.Fatalf("got %+v", r)
			}
			if rep.ExitCode() != 0 {
				t.Fatal("LAN results must not change the exit code")
			}
		})
	}
}

// ---- k8s -------------------------------------------------------------------

const dk = "-d Ubuntu-26.04 -u root -- "

// ---- 專用 engine / client ----

const engSock = "/mnt/wsl/wsld/docker.sock"

// engineEnv：engine = docker-engine（active），client = Debian1（Running 或 Stopped）。
func engineEnv(clientState string) (map[string]kv, Deps) {
	r := map[string]kv{
		"--list --verbose":                                                       {out: utf16("  NAME STATE VERSION\r\n* docker-engine Running 2\r\n  Debian1 " + clientState + " 2\r\n")},
		"-d docker-engine -- true":                                               {},
		"-d docker-engine -- systemctl is-system-running":                        {out: "running\n"},
		"-d docker-engine -- systemctl is-active docker":                         {out: "active\n"},
		"-d docker-engine -- ss -Hltn":                                           {out: "LISTEN 0 4096 127.0.0.1:23750 0.0.0.0:*\n"},
		"-d docker-engine -- cat /etc/docker/daemon.json":                        {out: `{"hosts":["unix:///var/run/docker.sock","unix://` + engSock + `","tcp://127.0.0.1:23750"]}`},
		"-d docker-engine -- loginctl show-user irice --property=Linger --value": {out: "yes\n"},
		"-d docker-engine -- test -S " + engSock:                                 {},
	}
	cfg := config.Default()
	cfg.UpsertMachine(config.Machine{Distro: "docker-engine", User: "irice", APIEndpoint: "tcp://127.0.0.1:23750"})
	cfg.UpsertMachine(config.Machine{Distro: "Debian1", User: "bob", APIEndpoint: "tcp://127.0.0.1:23750"})
	_ = cfg.SetEngine("docker-engine")
	_ = cfg.SetClient("Debian1")
	_ = cfg.SetActive("docker-engine")
	return r, Deps{WSL: &fakeRunner{resp: r}, Cfg: cfg, API: fakePinger{},
		Dial: func(context.Context, string) error { return errors.New("refused") }}
}

func TestNoEngineMeansNoEngineChecks(t *testing.T) {
	rep := Run(context.Background(), deps(healthy(), nil, nil))
	for _, r := range rep.Results {
		if r.ID == "engine_socket" || strings.HasPrefix(r.ID, "client_") {
			t.Fatalf("unexpected %s", r.ID)
		}
	}
}

func TestEngineSocketPresentIsOK(t *testing.T) {
	_, d := engineEnv("Stopped")
	rep := Run(context.Background(), d)
	if r := find(rep, "engine_socket"); r.ID != "engine_socket" || r.Level != OK {
		t.Fatalf("engine_socket = %+v", r)
	}
	if rep.ExitCode() != 0 {
		t.Fatalf("exit = %d", rep.ExitCode())
	}
}

func TestEngineSocketMissingWarns(t *testing.T) {
	r, d := engineEnv("Stopped")
	r["-d docker-engine -- test -S "+engSock] = kv{err: errors.New("exit 1")}
	rep := Run(context.Background(), d)
	got := find(rep, "engine_socket")
	if got.Level != Warn || !strings.Contains(got.Hint, "wsld") {
		t.Fatalf("engine_socket = %+v", got)
	}
}

func TestMainChecksTargetEngineEvenIfActiveIsClient(t *testing.T) {
	_, d := engineEnv("Stopped")
	_ = d.Cfg.SetActive("Debian1")
	rep := Run(context.Background(), d)
	if r := find(rep, "docker_service"); r.Level != OK {
		t.Fatalf("docker_service must be checked on the engine: %+v", r)
	}
}

func TestClientWithContextAndNoLocalDockerIsOK(t *testing.T) {
	r, d := engineEnv("Running")
	r["-d Debian1 -u bob -- docker context show"] = kv{out: "wsld\n"}
	r["-d Debian1 -- systemctl is-active docker"] = kv{out: "inactive\n", err: errors.New("exit 3")}
	rep := Run(context.Background(), d)
	if got := find(rep, "client_Debian1"); got.ID != "client_Debian1" || got.Level != OK {
		t.Fatalf("client = %+v", got)
	}
}

func TestClientWrongContextWarns(t *testing.T) {
	r, d := engineEnv("Running")
	r["-d Debian1 -u bob -- docker context show"] = kv{out: "default\n"}
	r["-d Debian1 -- systemctl is-active docker"] = kv{out: "inactive\n", err: errors.New("exit 3")}
	got := find(Run(context.Background(), d), "client_Debian1")
	if got.Level != Warn || !strings.Contains(got.Hint, "--role client") {
		t.Fatalf("client = %+v", got)
	}
}

func TestClientWithLocalDockerdRunningWarns(t *testing.T) {
	r, d := engineEnv("Running")
	r["-d Debian1 -u bob -- docker context show"] = kv{out: "wsld\n"}
	r["-d Debian1 -- systemctl is-active docker"] = kv{out: "active\n"}
	got := find(Run(context.Background(), d), "client_Debian1")
	if got.Level != Warn || !strings.Contains(got.Hint, "disable") {
		t.Fatalf("client = %+v", got)
	}
}

func TestStoppedClientIsNotWoken(t *testing.T) {
	_, d := engineEnv("Stopped") // fakeRunner 對未知指令回錯誤：任何 -d Debian1 呼叫都會讓測試察覺
	got := find(Run(context.Background(), d), "client_Debian1")
	if got.Level != Info {
		t.Fatalf("stopped client must be Info, got %+v", got)
	}
}

// ---- 曝露（/mnt/wsl/distro/<名稱>）----

const (
	dFindmnt = "-d docker-engine -- findmnt -rn -o TARGET"
	dTools   = "-d Debian1 -- test -x /usr/local/sbin/wsld-expose"
	dShim    = "-d Debian1 -- grep -q DISTRO_DEFAULT /usr/local/bin/docker"
)

func TestStaleExposureIsReported(t *testing.T) {
	r, d := engineEnv("Stopped")
	r[dFindmnt] = kv{out: "/\n/mnt/wsl/distro/Debian1\n/mnt/wsl/distro/Debian1/proc\n"}
	got := find(Run(context.Background(), d), "expose_stale")
	if got.ID != "expose_stale" || got.Level != Warn || !strings.Contains(got.Hint, "wsld expose gc") || !strings.Contains(got.Detail, "Debian1") {
		t.Fatalf("expose_stale = %+v", got)
	}
}

func TestExposureOfARunningClientIsNotStale(t *testing.T) {
	r, d := engineEnv("Running")
	r[dFindmnt] = kv{out: "/\n/mnt/wsl/distro/Debian1\n"}
	r["-d Debian1 -u bob -- docker context show"] = kv{out: "wsld\n"}
	r["-d Debian1 -- systemctl is-active docker"] = kv{out: "inactive\n", err: errors.New("exit 3")}
	if got := find(Run(context.Background(), d), "expose_stale"); got.ID == "expose_stale" {
		t.Fatalf("a running client's exposure is normal: %+v", got)
	}
}

func TestNoExposureChecksWhenFindmntIsUnavailable(t *testing.T) {
	_, d := engineEnv("Stopped")
	if got := find(Run(context.Background(), d), "expose_stale"); got.ID == "expose_stale" {
		t.Fatalf("unexpected %+v", got)
	}
}

func TestExposureToolsPresentOnARunningClientIsOK(t *testing.T) {
	r, d := engineEnv("Running")
	r["-d Debian1 -u bob -- docker context show"] = kv{out: "wsld\n"}
	r["-d Debian1 -- systemctl is-active docker"] = kv{out: "inactive\n", err: errors.New("exit 3")}
	r[dTools], r[dShim] = kv{}, kv{}
	got := find(Run(context.Background(), d), "expose_tools_Debian1")
	if got.ID != "expose_tools_Debian1" || got.Level != OK {
		t.Fatalf("tools = %+v", got)
	}
}

func TestMissingExposureToolsWarnWithTheReinitHint(t *testing.T) {
	r, d := engineEnv("Running")
	r["-d Debian1 -u bob -- docker context show"] = kv{out: "wsld\n"}
	r["-d Debian1 -- systemctl is-active docker"] = kv{out: "inactive\n", err: errors.New("exit 3")}
	r[dTools] = kv{err: errors.New("exit 1")}
	got := find(Run(context.Background(), d), "expose_tools_Debian1")
	if got.Level != Warn || !strings.Contains(got.Hint, "--role client") {
		t.Fatalf("tools = %+v", got)
	}
}

func TestExposureToolsAreCheckedEvenWhileALocalDockerdStillRuns(t *testing.T) {
	r, d := engineEnv("Running")
	r["-d Debian1 -u bob -- docker context show"] = kv{out: "wsld\n"}
	r["-d Debian1 -- systemctl is-active docker"] = kv{out: "active\n"} // 本機 dockerd 仍在跑 → client_ 警告
	r[dTools], r[dShim] = kv{}, kv{}
	rep := Run(context.Background(), d)
	if got := find(rep, "client_Debian1"); got.Level != Warn {
		t.Fatalf("client = %+v", got)
	}
	if got := find(rep, "expose_tools_Debian1"); got.ID != "expose_tools_Debian1" || got.Level != OK {
		t.Fatalf("the tools check must not be skipped by the local dockerd warning: %+v", got)
	}
}
