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

const dNodeReady = `{"items":[{"metadata":{"name":"framework"},"status":{"nodeInfo":{"kubeletVersion":"v1.37.2+k3s1"},"addresses":[{"type":"InternalIP","address":"172.20.3.68"}],"conditions":[{"type":"Ready","status":"True"}]}}]}`

func k8sDeps(r map[string]kv) Deps {
	d := deps(r, nil, nil)
	d.Cfg.UpsertMachine(config.Machine{Distro: "Ubuntu-26.04", User: "irice", APIEndpoint: "tcp://127.0.0.1:23750", K8s: true})
	d.Cfg.EnsureActive(d.Cfg.Machines[0])
	return d
}

func k8sHealthy() map[string]kv {
	r := healthy()
	r[dk+"test -x /usr/local/bin/k3s"] = kv{}
	r[dk+"systemctl is-active k3s"] = kv{out: "active\n"}
	r[dk+"k3s kubectl get --raw /readyz"] = kv{out: "ok"}
	r[dk+"k3s kubectl get nodes -o json"] = kv{out: dNodeReady}
	r[dk+"systemctl show k3s -p MemoryCurrent --value"] = kv{out: "439762944\n"}
	r[dk+"hostname -I"] = kv{out: "172.20.3.68 172.17.0.1\n"}
	r["-d Ubuntu-26.04 -- cat /proc/meminfo"] = kv{out: "MemTotal: 4012032 kB\nMemAvailable: 1930000 kB\n"}
	return r
}

func TestK8sChecksAbsentWhenNoMachineEnablesIt(t *testing.T) {
	rep := Run(context.Background(), deps(healthy(), nil, nil))
	for _, r := range rep.Results {
		if strings.HasPrefix(r.ID, "k8s") {
			t.Fatalf("unexpected k8s check %q when k8s is off everywhere", r.ID)
		}
	}
}

func TestK8sHealthy(t *testing.T) {
	rep := Run(context.Background(), k8sDeps(k8sHealthy()))
	if r := find(rep, "k8s"); r.Level != OK || !strings.Contains(r.Detail, "v1.37.2+k3s1") {
		t.Fatalf("k8s = %+v", r)
	}
	for _, id := range []string{"k8s_ip", "k8s_memory"} {
		if r := find(rep, id); r.Level != OK {
			t.Errorf("%s = %+v", id, r)
		}
	}
	if rep.ExitCode() != 0 {
		t.Fatalf("exit = %d", rep.ExitCode())
	}
}

func TestK8sConfiguredOnButStopped(t *testing.T) {
	r := k8sHealthy()
	r[dk+"systemctl is-active k3s"] = kv{out: "inactive\n", err: errors.New("exit 3")}
	rep := Run(context.Background(), k8sDeps(r))
	k := find(rep, "k8s")
	if k.Level != Warn || !strings.Contains(k.Hint, "wsld k8s on") {
		t.Fatalf("k8s = %+v", k)
	}
	if rep.ExitCode() != 0 {
		t.Fatal("k8s problems must never change the exit code (k3s is optional)")
	}
}

func TestK8sIPMismatchWarns(t *testing.T) {
	r := k8sHealthy()
	r[dk+"hostname -I"] = kv{out: "172.20.9.9 172.17.0.1\n"}
	rep := Run(context.Background(), k8sDeps(r))
	if x := find(rep, "k8s_ip"); x.Level != Warn || !strings.Contains(x.Hint, "wsld k8s off") {
		t.Fatalf("k8s_ip = %+v", x)
	}
}

func TestK8sLowMemoryWarns(t *testing.T) {
	r := k8sHealthy()
	r["-d Ubuntu-26.04 -- cat /proc/meminfo"] = kv{out: "MemTotal: 4012032 kB\nMemAvailable: 300000 kB\n"}
	rep := Run(context.Background(), k8sDeps(r))
	if x := find(rep, "k8s_memory"); x.Level != Warn {
		t.Fatalf("k8s_memory = %+v", x)
	}
}

func TestK8sPort6443BusyWhileNotReadyWarns(t *testing.T) {
	r := k8sHealthy()
	r[dk+"systemctl is-active k3s"] = kv{out: "inactive\n", err: errors.New("exit 3")}
	d := k8sDeps(r)
	d.Dial = func(_ context.Context, addr string) error {
		if addr == "127.0.0.1:6443" {
			return nil
		}
		return errors.New("refused")
	}
	rep := Run(context.Background(), d)
	if x := find(rep, "k8s_ports"); x.Level != Warn || !strings.Contains(x.Detail, "6443") {
		t.Fatalf("k8s_ports = %+v", x)
	}
}

func TestK8sPorts80And443AreInformational(t *testing.T) {
	d := k8sDeps(k8sHealthy())
	d.Dial = func(_ context.Context, addr string) error {
		if addr == "127.0.0.1:80" || addr == "127.0.0.1:443" {
			return nil
		}
		return errors.New("refused")
	}
	rep := Run(context.Background(), d)
	if x := find(rep, "k8s_ports"); x.Level != Info || !strings.Contains(x.Detail, "80") {
		t.Fatalf("k8s_ports = %+v", x)
	}
}
