package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/lan"
)

func withLAN(h *harness, t *testing.T, mode string) *lan.FakeHost {
	t.Helper()
	fh := &lan.FakeHost{Dir: t.TempDir(), WinBuild: 26300}
	h.app.LAN = &lan.Manager{Host: fh, Mode: func(context.Context, string) (string, error) { return mode, nil }}
	return fh
}

func readWSLConfig(t *testing.T, fh *lan.FakeHost) string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(fh.Dir, ".wslconfig"))
	return string(b)
}

func TestLanStatusOff(t *testing.T) {
	h := newHarness(t, true)
	withLAN(h, t, "nat")
	if code := h.run("lan", "status"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	out := h.out.String()
	for _, want := range []string{"LAN access: off", "(not set → nat)", "effective mode:            nat", "192.168.1.20"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "other devices") {
		t.Fatal("connect hint only when ready")
	}
}

func TestLanStatusJSON(t *testing.T) {
	h := newHarness(t, true)
	fh := withLAN(h, t, "mirrored")
	fh.Rules = 2
	_ = os.WriteFile(filepath.Join(fh.Dir, ".wslconfig"), []byte("[wsl2]\nnetworkingMode=mirrored\n"), 0o644)
	if code := h.run("lan", "status", "--json"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	var st lan.Status
	if err := json.Unmarshal(h.out.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Ready() || st.Effective != "mirrored" {
		t.Fatalf("st = %+v", st)
	}
}

func TestLanOnWithYesRestartsAndStarts(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["--shutdown"] = ""
	fh := withLAN(h, t, "mirrored")
	if code := h.run("lan", "on", "--yes"); code != 0 {
		t.Fatalf("code=%d err=%s out=%s", code, h.errb, h.out)
	}
	if got := readWSLConfig(t, fh); got != "[wsl2]\nnetworkingMode=mirrored\n" {
		t.Fatalf(".wslconfig = %q", got)
	}
	if !slices.Equal(fh.FirewallCalls, []bool{true}) {
		t.Fatalf("firewall = %v", fh.FirewallCalls)
	}
	shutdown := slices.Index(h.wsl.calls, "--shutdown")
	wake := slices.Index(h.wsl.calls, "-d Ubuntu-26.04 -- true")
	if shutdown < 0 || wake < shutdown {
		t.Fatalf("expected shutdown then wake, calls=%v", h.wsl.calls)
	}
	out := h.out.String()
	for _, want := range []string{"UAC", "127.0.0.1", "engine ready", "LAN access: on", "http://192.168.1.20:<published port>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLanOnDeclinedRestart(t *testing.T) {
	h := newHarness(t, true)
	h.app.Stdin = strings.NewReader("n\n")
	withLAN(h, t, "nat")
	if code := h.run("lan", "on"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if slices.Contains(h.wsl.calls, "--shutdown") {
		t.Fatal("must not shut down WSL without confirmation")
	}
	out := h.out.String()
	if !strings.Contains(out, "[y/N]") || !strings.Contains(out, "Not applied yet") || !strings.Contains(out, "not applied yet — run wsl --shutdown") {
		t.Fatalf("out:\n%s", out)
	}
}

func TestLanOnConfirmedRestart(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["--shutdown"] = ""
	h.app.Stdin = strings.NewReader("y\n")
	withLAN(h, t, "mirrored")
	if code := h.run("lan", "on"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if !slices.Contains(h.wsl.calls, "--shutdown") {
		t.Fatal("expected wsl --shutdown")
	}
}

func TestLanOnUACCanceled(t *testing.T) {
	h := newHarness(t, true)
	fh := withLAN(h, t, "nat")
	fh.FirewallErr = errors.New("The operation was canceled by the user.")
	if code := h.run("lan", "on", "--yes"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	if readWSLConfig(t, fh) != "" || slices.Contains(h.wsl.calls, "--shutdown") {
		t.Fatal("nothing must change when UAC is canceled")
	}
	if !strings.Contains(h.errb.String(), "canceled") {
		t.Fatalf("stderr: %s", h.errb)
	}
}

func TestLanOnUnsupportedBuild(t *testing.T) {
	h := newHarness(t, true)
	fh := withLAN(h, t, "nat")
	fh.WinBuild = 19045
	if code := h.run("lan", "on", "--yes"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(h.errb.String(), "build 19045") {
		t.Fatalf("stderr: %s", h.errb)
	}
}

func TestLanOff(t *testing.T) {
	h := newHarness(t, true)
	h.wsl.resp["--shutdown"] = ""
	fh := withLAN(h, t, "nat")
	fh.Rules = 2
	_ = os.WriteFile(filepath.Join(fh.Dir, ".wslconfig"), []byte("[wsl2]\r\nmemory=4GB\r\nnetworkingMode=mirrored\r\n"), 0o644)
	if code := h.run("lan", "off", "--yes"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if got := readWSLConfig(t, fh); got != "[wsl2]\r\nmemory=4GB\r\n" {
		t.Fatalf(".wslconfig = %q", got)
	}
	if !slices.Equal(fh.FirewallCalls, []bool{false}) || !strings.Contains(h.out.String(), "LAN access: off") {
		t.Fatalf("firewall=%v out=%s", fh.FirewallCalls, h.out)
	}
}

func TestLanOnWithoutConfigOnlyShutsDown(t *testing.T) {
	h := newHarness(t, false)
	h.wsl.resp["--shutdown"] = ""
	withLAN(h, t, "")
	if code := h.run("lan", "on", "--yes"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if slices.Contains(h.wsl.calls, "-d Ubuntu-26.04 -- true") {
		t.Fatal("no active machine → must not start one")
	}
}

func TestLanUsageErrors(t *testing.T) {
	h := newHarness(t, true)
	withLAN(h, t, "nat")
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
