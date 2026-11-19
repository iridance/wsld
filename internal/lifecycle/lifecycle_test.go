package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/wsl"
)

// ---- fakes -----------------------------------------------------------------

type call struct {
	args  string
	stdin string
}

type resp struct {
	out string
	err error
}

type fakeRunner struct {
	calls []call
	// 依序回應；key 為 args 串接。若 value 是 slice 則依呼叫次序消耗。
	resp map[string][]resp
}

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	key := strings.Join(args, " ")
	var in string
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	f.calls = append(f.calls, call{args: key, stdin: in})
	rs, ok := f.resp[key]
	if !ok || len(rs) == 0 {
		return nil, nil, errors.New("fake: unexpected " + key)
	}
	r := rs[0]
	if len(rs) > 1 {
		f.resp[key] = rs[1:]
	}
	if r.err != nil {
		return []byte(r.out), nil, r.err
	}
	return []byte(r.out), nil, nil
}

func (f *fakeRunner) called(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c.args, sub) {
			return true
		}
	}
	return false
}

type fakePinger struct {
	errs []error // 依序回傳；耗盡後回最後一個
	n    int
}

func (p *fakePinger) Ping(context.Context) error {
	p.n++
	if len(p.errs) == 0 {
		return nil
	}
	if p.n-1 < len(p.errs) {
		return p.errs[p.n-1]
	}
	return p.errs[len(p.errs)-1]
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

const listUbuntuRunning = "  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n"
const listUbuntuStopped = "  NAME STATE VERSION\r\n* Ubuntu-26.04 Stopped 2\r\n"

func newMgr(fr *fakeRunner, p Pinger) *Manager {
	cfg := config.Default()
	cfg.Distro, cfg.User = "Ubuntu-26.04", "irice"
	return &Manager{WSL: fr, Cfg: cfg, API: p, Bundle: []byte("#!/bin/bash\necho bundle"), Sleep: func(time.Duration) {}}
}

// ---- Status -----------------------------------------------------------------

func TestStatusAllGreen(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose": {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- systemctl is-system-running": {{out: "running\n"}},
		"-d Ubuntu-26.04 -- systemctl is-active docker":  {{out: "active\n"}},
	}}
	st := newMgr(fr, &fakePinger{}).Status(context.Background())
	if !st.DistroFound || !st.DistroRunning || st.DistroVersion != 2 || !st.SystemdActive || !st.DockerActive || !st.APIReachable {
		t.Fatalf("status = %+v", st)
	}
	if st.APIEndpoint != "tcp://127.0.0.1:23750" {
		t.Fatalf("endpoint = %q", st.APIEndpoint)
	}
}

func TestStatusDistroMissingStopsEarly(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose": {{out: utf16("  NAME STATE VERSION\r\n* Debian Running 2\r\n")}},
	}}
	st := newMgr(fr, &fakePinger{}).Status(context.Background())
	if st.DistroFound || st.SystemdActive || st.DockerActive {
		t.Fatalf("status = %+v", st)
	}
	if fr.called("systemctl") {
		t.Fatal("should not probe systemd when distro missing")
	}
	if !st.APIReachable {
		t.Fatal("API probe should still run (engine may be elsewhere) ")
	}
}

func TestStatusDegradedSystemdCountsAsActive(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose": {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- systemctl is-system-running": {{out: "degraded\n", err: &wsl.ExitError{Code: 1}}},
		"-d Ubuntu-26.04 -- systemctl is-active docker":  {{out: "inactive\n", err: &wsl.ExitError{Code: 3}}},
	}}
	st := newMgr(fr, &fakePinger{errs: []error{dockerapi.ErrUnreachable}}).Status(context.Background())
	if !st.SystemdActive || st.DockerActive || st.APIReachable {
		t.Fatalf("status = %+v", st)
	}
}

// ---- Start ------------------------------------------------------------------

func TestStartWakesStartsDockerAndWaitsForPing(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                                  {{out: utf16(listUbuntuStopped)}},
		"-d Ubuntu-26.04 -- true":                           {{}},
		"-d Ubuntu-26.04 -- systemctl is-system-running":    {{out: "running\n"}},
		"-d Ubuntu-26.04 -- systemctl is-active docker":     {{out: "inactive\n", err: &wsl.ExitError{Code: 3}}, {out: "active\n"}},
		"-d Ubuntu-26.04 -- systemctl cat docker.service":   {{out: "# /lib/systemd/system/docker.service\n"}},
		"-d Ubuntu-26.04 -u root -- systemctl start docker": {{}},
	}}
	p := &fakePinger{errs: []error{dockerapi.ErrUnreachable, dockerapi.ErrUnreachable, nil}}
	st, err := newMgr(fr, p).Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !fr.called("-u root -- systemctl start docker") {
		t.Fatal("docker should have been started as root")
	}
	if !st.APIReachable || p.n < 3 { // 兩次失敗 + 成功（最終 Status 可能再 ping）
		t.Fatalf("status=%+v pings=%d", st, p.n)
	}
}

func TestStartFailsNotReadyWhenDistroMissing(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose": {{out: utf16("  NAME STATE VERSION\r\n")}},
	}}
	_, err := newMgr(fr, &fakePinger{}).Start(context.Background())
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "wsld init") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartFailsNotReadyWhenSystemdOff(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                               {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- true":                        {{}},
		"-d Ubuntu-26.04 -- systemctl is-system-running": {{out: "offline\n", err: &wsl.ExitError{Code: 1}}},
	}}
	_, err := newMgr(fr, &fakePinger{}).Start(context.Background())
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "systemd") {
		t.Fatalf("err = %v", err)
	}
}

func TestStartFailsAPIWhenPingNeverSucceeds(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                               {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- true":                        {{}},
		"-d Ubuntu-26.04 -- systemctl is-system-running": {{out: "running\n"}},
		"-d Ubuntu-26.04 -- systemctl is-active docker":  {{out: "active\n"}},
	}}
	m := newMgr(fr, &fakePinger{errs: []error{dockerapi.ErrUnreachable}})
	m.PingAttempts = 3
	_, err := m.Start(context.Background())
	if !errors.Is(err, ErrAPI) {
		t.Fatalf("err = %v", err)
	}
}

func TestStartBootstrapsWhenDockerUnitMissing(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                               {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- true":                        {{}},
		"-d Ubuntu-26.04 -- systemctl is-system-running": {{out: "running\n"}},
		"-d Ubuntu-26.04 -- systemctl is-active docker": {
			{out: "inactive\n", err: &wsl.ExitError{Code: 3}},
			{out: "active\n"},
		},
		"-d Ubuntu-26.04 -- systemctl cat docker.service": {
			{out: "", err: &wsl.ExitError{Code: 1}},
		},
		"-d Ubuntu-26.04 -u root -- id -u irice": {{out: "1000\n"}},
		"-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750": {
			{out: "WSLD_API_PORT=23750\n"},
		},
	}}
	p := &fakePinger{}
	m := newMgr(fr, p)
	m.Bundle = []byte("BUNDLE")
	m.PingAttempts = 3
	st, err := m.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !fr.called("-u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750") {
		t.Fatalf("expected bootstrap; calls=%v", fr.calls)
	}
	if !st.APIReachable {
		t.Fatalf("status=%+v", st)
	}
}

func TestStartDoesNotBootstrapWhenUnitPresentButInactive(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                               {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- true":                        {{}},
		"-d Ubuntu-26.04 -- systemctl is-system-running": {{out: "running\n"}},
		"-d Ubuntu-26.04 -- systemctl is-active docker": {
			{out: "inactive\n", err: &wsl.ExitError{Code: 3}},
			{out: "active\n"},
		},
		"-d Ubuntu-26.04 -- systemctl cat docker.service": {
			{out: "# /lib/systemd/system/docker.service\n"},
		},
		"-d Ubuntu-26.04 -u root -- systemctl start docker": {{}},
	}}
	st, err := newMgr(fr, &fakePinger{}).Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fr.called("wsld-bootstrap") {
		t.Fatal("should not bootstrap when unit exists")
	}
	if !fr.called("-u root -- systemctl start docker") {
		t.Fatal("should systemctl start docker")
	}
	if !st.APIReachable {
		t.Fatalf("status=%+v", st)
	}
}

// ---- Init -------------------------------------------------------------------

func TestInitPicksUbuntuDetectsUserRunsBundleAndParsesPort(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":          {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- id -un": {{out: "irice\n"}},
		"-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750": {{out: "[wsld] ...\nWSLD_API_PORT=23751\nWSLD_API_ENDPOINT=tcp://127.0.0.1:23751\n"}},
	}}
	var out bytes.Buffer
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("BUNDLE"), Output: &out, Sleep: func(time.Duration) {}}
	cfg, err := m.Init(context.Background(), InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Distro != "Ubuntu-26.04" || cfg.User != "irice" || cfg.APIEndpoint != "tcp://127.0.0.1:23751" {
		t.Fatalf("cfg = %+v", cfg)
	}
	last := fr.calls[len(fr.calls)-1]
	if last.stdin != "BUNDLE" {
		t.Fatalf("bundle not piped: %q", last.stdin)
	}
	if !strings.Contains(out.String(), "WSLD_API_PORT=23751") {
		t.Fatal("bootstrap output should be echoed to Output")
	}
}

func TestInitPassesBusyPortsFromOtherMachines(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":     {{out: utf16("  NAME STATE VERSION\r\n  Debian1 Stopped 2\r\n* Ubuntu-26.04 Running 2\r\n")}},
		"-d Debian1 -- id -un": {{out: "bob\n"}},
		"-d Debian1 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user bob --port 23751 --busy-ports 23750": {{out: "WSLD_API_PORT=23752\n"}},
	}}
	cfg := config.Default()
	cfg.UpsertMachine(config.Machine{Distro: "Ubuntu-26.04", User: "irice", APIEndpoint: config.EndpointForPort(23750)})
	_ = cfg.SetActive("Ubuntu-26.04")
	m := &Manager{WSL: fr, Cfg: cfg, API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	got, err := m.Init(context.Background(), InitOptions{Distro: "Debian1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.APIEndpoint != "tcp://127.0.0.1:23752" || got.Distro != "Debian1" {
		t.Fatalf("cfg = %+v", got)
	}
	if !fr.called("--busy-ports 23750") {
		t.Fatalf("expected --busy-ports; calls=%v", fr.calls)
	}
}

func TestInitHonoursExplicitDistroUserPort(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                     {{out: utf16("  NAME STATE VERSION\r\n  Ubuntu-22.04 Stopped 2\r\n* Ubuntu-26.04 Running 2\r\n")}},
		"-d Ubuntu-22.04 -u root -- id -u bob": {{out: "1000\n"}},
		"-d Ubuntu-22.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user bob --port 24000": {{out: "WSLD_API_PORT=24000\n"}},
	}}
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	cfg, err := m.Init(context.Background(), InitOptions{Distro: "Ubuntu-22.04", User: "bob", Port: 24000})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Distro != "Ubuntu-22.04" || cfg.User != "bob" || cfg.APIEndpoint != "tcp://127.0.0.1:24000" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestInitCreatesUserWhenDefaultIsRoot(t *testing.T) {
	old := wsl.CurrentUsername
	wsl.CurrentUsername = func() (string, error) { return `PC\irice`, nil }
	defer func() { wsl.CurrentUsername = old }()

	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                                    {{out: utf16("  NAME STATE VERSION\r\n* Debian1 Stopped 2\r\n")}},
		"-d Debian1 -- id -un":                                {{out: "root\n"}},
		"-d Debian1 -u root -- id -u irice":                   {{err: errors.New("no such user")}},
		"-d Debian1 -u root -- useradd -m -s /bin/bash irice": {{}},
		"-d Debian1 -u root -- usermod -aG sudo irice":        {{}},
		"-d Debian1 -u root -- usermod -aG wheel irice":       {{err: errors.New("no wheel")}},
		"-d Debian1 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750": {{out: "WSLD_API_PORT=23750\n"}},
	}}
	var out bytes.Buffer
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Output: &out, Sleep: func(time.Duration) {}}
	cfg, err := m.Init(context.Background(), InitOptions{Distro: "Debian1"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "irice" || cfg.Distro != "Debian1" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if !fr.called("useradd -m -s /bin/bash irice") {
		t.Fatal("should useradd")
	}
	if !strings.Contains(out.String(), "irice") {
		t.Fatalf("should log creating user: %q", out.String())
	}
}

func TestInitAsksPasswordOnlyWhenCreatingUser(t *testing.T) {
	old := wsl.CurrentUsername
	wsl.CurrentUsername = func() (string, error) { return `PC\irice`, nil }
	defer func() { wsl.CurrentUsername = old }()

	newRunner := func(exists bool) *fakeRunner {
		idResp := resp{err: errors.New("no such user")}
		if exists {
			idResp = resp{out: "1000\n"}
		}
		return &fakeRunner{resp: map[string][]resp{
			"--list --verbose":                                    {{out: utf16("  NAME STATE VERSION\r\n* Debian1 Stopped 2\r\n")}},
			"-d Debian1 -- id -un":                                {{out: "root\n"}},
			"-d Debian1 -u root -- id -u irice":                   {idResp},
			"-d Debian1 -u root -- useradd -m -s /bin/bash irice": {{}},
			"-d Debian1 -u root -- chpasswd":                      {{}},
			"-d Debian1 -u root -- usermod -aG sudo irice":        {{}},
			"-d Debian1 -u root -- usermod -aG wheel irice":       {{err: errors.New("no wheel")}},
			"-d Debian1 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750": {{out: "WSLD_API_PORT=23750\n"}},
		}}
	}

	// 新建：詢問一次，密碼經 stdin 給 chpasswd
	fr := newRunner(false)
	asked := 0
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	_, err := m.Init(context.Background(), InitOptions{Distro: "Debian1", Password: func(u string) (string, error) {
		asked++
		if u != "irice" {
			t.Fatalf("asked for %q", u)
		}
		return "hunter2", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if asked != 1 {
		t.Fatalf("asked = %d", asked)
	}
	var chp string
	for _, c := range fr.calls {
		if strings.HasSuffix(c.args, "-- chpasswd") {
			chp = c.stdin
		}
		if strings.Contains(c.args, "hunter2") {
			t.Fatalf("password in argv: %s", c.args)
		}
	}
	if chp != "irice:hunter2\n" {
		t.Fatalf("chpasswd stdin = %q", chp)
	}

	// 已存在：不詢問、不 chpasswd
	fr = newRunner(true)
	m = &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	_, err = m.Init(context.Background(), InitOptions{Distro: "Debian1", Password: func(string) (string, error) {
		t.Fatal("must not ask for existing user")
		return "", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if fr.called("chpasswd") || fr.called("useradd") {
		t.Fatal("existing user must be left alone")
	}

	// 新建但留空：不 chpasswd，並提示如何補設
	fr = newRunner(false)
	var out bytes.Buffer
	m = &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Output: &out, Sleep: func(time.Duration) {}}
	if _, err = m.Init(context.Background(), InitOptions{Distro: "Debian1", Password: func(string) (string, error) { return "", nil }}); err != nil {
		t.Fatal(err)
	}
	if fr.called("chpasswd") || !strings.Contains(out.String(), "passwd irice") {
		t.Fatalf("empty password: chpasswd=%v out=%q", fr.called("chpasswd"), out.String())
	}
}

func TestInitExplicitDistroMustExist(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose": {{out: utf16(listUbuntuRunning)}},
	}}
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	_, err := m.Init(context.Background(), InitOptions{Distro: "Fedora"})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v", err)
	}
}

func TestInitInstallsUbuntuWhenNoneAndAsksForRelaunch(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":                {{out: utf16("  NAME STATE VERSION\r\n")}},
		"--install -d Ubuntu --no-launch": {{}},
	}}
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	_, err := m.Init(context.Background(), InitOptions{})
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "Ubuntu") {
		t.Fatalf("err = %v", err)
	}
	if !fr.called("--install -d Ubuntu --no-launch") {
		t.Fatal("should attempt wsl --install")
	}
}

func TestInitBootstrapExit3MapsToNotReady(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":          {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- id -un": {{out: "irice\n"}},
		"-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750": {{out: "[wsld] WARN: systemd...", err: &wsl.ExitError{Code: 3}}},
	}}
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	_, err := m.Init(context.Background(), InitOptions{})
	if !errors.Is(err, ErrNotReady) || !strings.Contains(err.Error(), "wsl --shutdown") {
		t.Fatalf("err = %v", err)
	}
}

func TestInitBootstrapExit5MapsToUnsupported(t *testing.T) {
	fr := &fakeRunner{resp: map[string][]resp{
		"--list --verbose":          {{out: utf16(listUbuntuRunning)}},
		"-d Ubuntu-26.04 -- id -un": {{out: "irice\n"}},
		"-d Ubuntu-26.04 -u root -- bash -c cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh --user irice --port 23750": {{out: "[wsld] ERROR: ...", err: &wsl.ExitError{Code: 5}}},
	}}
	m := &Manager{WSL: fr, Cfg: config.Default(), API: &fakePinger{}, Bundle: []byte("B"), Sleep: func(time.Duration) {}}
	_, err := m.Init(context.Background(), InitOptions{})
	if !errors.Is(err, ErrNotReady) || strings.Contains(err.Error(), "wsl --shutdown") || !strings.Contains(err.Error(), "openSUSE") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseBootstrapPort(t *testing.T) {
	if p, ok := ParseBootstrapPort("x\nWSLD_API_PORT=23752\r\ny"); !ok || p != 23752 {
		t.Fatalf("got %d %v", p, ok)
	}
	if _, ok := ParseBootstrapPort("nothing"); ok {
		t.Fatal("should not parse")
	}
}
