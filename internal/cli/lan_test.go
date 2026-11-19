package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/lan"
)

func withLAN(h *harness, t *testing.T) *lan.FakeHost {
	t.Helper()
	fh := &lan.FakeHost{Dir: t.TempDir()}
	h.app.LAN = &lan.Manager{Host: fh, Sleep: func(time.Duration) {}}
	return fh
}

func writeMirrored(t *testing.T, fh *lan.FakeHost) string {
	t.Helper()
	p := filepath.Join(fh.Dir, ".wslconfig")
	if err := os.WriteFile(p, []byte("[wsl2]\r\nmemory=4GB\r\nnetworkingMode=mirrored\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fh.Rules = 2
	return p
}

func TestLanStatusOff(t *testing.T) {
	h := newHarness(t, true)
	withLAN(h, t)
	if code := h.run("lan", "status"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	out := h.out.String()
	if !strings.Contains(out, "LAN access: off") || !strings.Contains(out, "192.168.1.20") || strings.Contains(out, "other devices") || strings.Contains(out, "mirrored") {
		t.Fatalf("out:\n%s", out)
	}
}

func TestLanOnOffSavesConfig(t *testing.T) {
	h := newHarness(t, true)
	withLAN(h, t)
	if code := h.run("lan", "on"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	c, _ := config.Load(h.app.ConfigPath)
	if !c.LANAccess {
		t.Fatal("lan_access must be saved")
	}
	out := h.out.String()
	for _, want := range []string{"Windows firewall", "LAN access: on", "http://192.168.1.20:<published port>", "wsld-gui"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if slices.Contains(h.wsl.calls, "--shutdown") {
		t.Fatal("on must not restart WSL")
	}
	h.out.Reset()
	if code := h.run("lan", "off"); code != 0 {
		t.Fatalf("code=%d", code)
	}
	c, _ = config.Load(h.app.ConfigPath)
	if c.LANAccess || !strings.Contains(h.out.String(), "LAN access: off") {
		t.Fatalf("off: %+v\n%s", c, h.out)
	}
}

func TestLanOnRequiresConfig(t *testing.T) {
	h := newHarness(t, false)
	withLAN(h, t)
	if code := h.run("lan", "on"); code != ExitNotReady {
		t.Fatalf("code=%d", code)
	}
}

func TestLanStatusJSONAndMirroredWarning(t *testing.T) {
	h := newHarness(t, true)
	fh := withLAN(h, t)
	writeMirrored(t, fh)
	if code := h.run("lan", "status", "--json"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	var st lanStatusJSON
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil || !st.Mirrored || st.Enabled || len(st.Addresses) != 1 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	h.out.Reset()
	_ = h.run("lan", "status")
	if !strings.Contains(h.out.String(), "wsld lan restore-nat") {
		t.Fatalf("out:\n%s", h.out)
	}
}

func TestLanRestoreNATWithYes(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["--shutdown"] = ""
	fh := withLAN(h, t)
	p := writeMirrored(t, fh)
	fh.PortBusyPolls = 2
	// 先讓 active 已 init（有 endpoint）
	if code := h.run("start"); code != 0 {
		t.Fatalf("start: %d %s", code, h.errb)
	}
	h.wsl.calls = nil
	if code := h.run("lan", "restore-nat", "--yes"); code != 0 {
		t.Fatalf("code=%d err=%s out=%s", code, h.errb, h.out)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "[wsl2]\r\nmemory=4GB\r\n" || fh.RemoveCalls != 1 {
		t.Fatalf(".wslconfig=%q removes=%d", b, fh.RemoveCalls)
	}
	shutdown := slices.Index(h.wsl.calls, "--shutdown")
	wake := slices.Index(h.wsl.calls, "-d Ubuntu-26.04 -- true")
	if shutdown < 0 || wake < shutdown {
		t.Fatalf("expected shutdown then wake, calls=%v", h.wsl.calls)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "wsld-bootstrap.sh") {
			t.Fatalf("restart must not re-run bootstrap: %v", h.wsl.calls)
		}
	}
	if len(fh.PortChecks) != 3 || fh.PortChecks[0] != 23750 {
		t.Fatalf("must wait for API port: %v", fh.PortChecks)
	}
	out := h.out.String()
	for _, want := range []string{"UAC", "release API port 23750", "engine ready"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLanRestoreNATDeclinedRestart(t *testing.T) {
	h := newHarness(t, true)
	h.app.Stdin = strings.NewReader("n\n")
	fh := withLAN(h, t)
	writeMirrored(t, fh)
	if code := h.run("lan", "restore-nat"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if slices.Contains(h.wsl.calls, "--shutdown") || !strings.Contains(h.out.String(), "Not applied yet") {
		t.Fatalf("calls=%v out=%s", h.wsl.calls, h.out)
	}
}

func TestLanRestoreNATUACCanceled(t *testing.T) {
	h := newHarness(t, true)
	fh := withLAN(h, t)
	p := writeMirrored(t, fh)
	fh.FirewallErr = errors.New("The operation was canceled by the user.")
	if code := h.run("lan", "restore-nat", "--yes"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "mirrored") || slices.Contains(h.wsl.calls, "--shutdown") || !strings.Contains(h.errb.String(), "canceled") {
		t.Fatalf(".wslconfig=%q calls=%v stderr=%s", b, h.wsl.calls, h.errb)
	}
}

func TestLanUsageErrors(t *testing.T) {
	h := newHarness(t, true)
	withLAN(h, t)
	for _, args := range [][]string{{"lan"}, {"lan", "maybe"}, {"lan", "on", "extra"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
	h.app.LAN = nil
	if code := h.run("lan", "status"); code != ExitError {
		t.Fatalf("nil LAN: code=%d", code)
	}
}
