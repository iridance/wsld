package gui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/iridance/wsld/internal/lan"
)

func withLAN(t *testing.T, e *env) (*lan.FakeHost, *FakeWSL) {
	t.Helper()
	fh := &lan.FakeHost{Dir: t.TempDir(), WinBuild: 26300}
	fw := NewFakeWSL()
	effective := lan.ModeNAT
	fw.OnShutdown = func() {
		b, _ := os.ReadFile(filepath.Join(fh.Dir, ".wslconfig"))
		if m := lan.GetKey(string(b), "wsl2", "networkingMode"); m != "" {
			effective = m
		} else {
			effective = lan.ModeNAT
		}
	}
	e.s.WSL = fw
	e.s.LAN = &lan.Manager{Host: fh, Mode: func(context.Context, string) (string, error) { return effective, nil }}
	return fh, fw
}

func decodeLAN(t *testing.T, b []byte) lan.Status {
	t.Helper()
	var st lan.Status
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return st
}

func TestLANUnavailable(t *testing.T) {
	e := newEnv(t, "")
	if resp, _ := e.req(t, "GET", "/api/lan", ""); resp.StatusCode != 501 {
		t.Fatalf("GET status %d", resp.StatusCode)
	}
	if resp, _ := e.req(t, "PUT", "/api/lan", `{"enabled":true}`); resp.StatusCode != 501 {
		t.Fatalf("PUT status %d", resp.StatusCode)
	}
	_, b := e.req(t, "GET", "/api/meta", "")
	var m Meta
	_ = json.Unmarshal(b, &m)
	if m.LANSupported {
		t.Fatal("lan_supported must be false without a manager")
	}
}

func TestLANStatus(t *testing.T) {
	e := newEnv(t, "")
	withLAN(t, e)
	resp, b := e.req(t, "GET", "/api/lan", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	st := decodeLAN(t, b)
	if st.Enabled || st.Effective != "nat" || len(st.Addresses) != 1 {
		t.Fatalf("st = %+v", st)
	}
	_, b = e.req(t, "GET", "/api/meta", "")
	var m Meta
	_ = json.Unmarshal(b, &m)
	if !m.LANSupported {
		t.Fatal("lan_supported must be true")
	}
}

func TestLANEnableWithRestart(t *testing.T) {
	e := newEnv(t, "")
	fh, fw := withLAN(t, e)
	starts := e.life.Starts()
	resp, b := e.req(t, "PUT", "/api/lan", `{"enabled":true,"restart":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	st := decodeLAN(t, b)
	if !st.Ready() {
		t.Fatalf("expected ready: %+v", st)
	}
	if fw.Shutdowns() != 1 || e.life.Starts() != starts+1 || len(fh.FirewallCalls) != 1 {
		t.Fatalf("shutdowns=%d starts=%d firewall=%v", fw.Shutdowns(), e.life.Starts(), fh.FirewallCalls)
	}
}

func TestLANEnableWithoutRestartIsPending(t *testing.T) {
	e := newEnv(t, "")
	_, fw := withLAN(t, e)
	_, b := e.req(t, "PUT", "/api/lan", `{"enabled":true}`)
	st := decodeLAN(t, b)
	if !st.Enabled || !st.RestartPending || fw.Shutdowns() != 0 {
		t.Fatalf("st=%+v shutdowns=%d", st, fw.Shutdowns())
	}
	// 之後停用（含重啟）→ 回到 nat
	_, b = e.req(t, "PUT", "/api/lan", `{"enabled":false,"restart":true}`)
	st = decodeLAN(t, b)
	if st.Enabled || st.RestartPending || st.Effective != "nat" || st.Firewall != lan.FirewallMissing {
		t.Fatalf("st=%+v", st)
	}
}

func TestLANErrors(t *testing.T) {
	e := newEnv(t, "")
	fh, fw := withLAN(t, e)
	if resp, _ := e.req(t, "PUT", "/api/lan", `nope`); resp.StatusCode != 400 {
		t.Fatalf("bad json: %d", resp.StatusCode)
	}
	fh.FirewallErr = errors.New("The operation was canceled by the user.")
	resp, b := e.req(t, "PUT", "/api/lan", `{"enabled":true,"restart":true}`)
	if resp.StatusCode != 500 || fw.Shutdowns() != 0 {
		t.Fatalf("uac cancel: %d %s shutdowns=%d", resp.StatusCode, b, fw.Shutdowns())
	}
	fh.FirewallErr = nil
	fh.WinBuild = 19045
	if resp, b := e.req(t, "PUT", "/api/lan", `{"enabled":true}`); resp.StatusCode != 400 {
		t.Fatalf("unsupported: %d %s", resp.StatusCode, b)
	}
}

func TestLANRestartOnlySkipsUAC(t *testing.T) {
	e := newEnv(t, "")
	fh, fw := withLAN(t, e)
	_, _ = e.req(t, "PUT", "/api/lan", `{"enabled":true}`) // 設定但未重啟
	if len(fh.FirewallCalls) != 1 {
		t.Fatalf("firewall=%v", fh.FirewallCalls)
	}
	_, b := e.req(t, "PUT", "/api/lan", `{"enabled":true,"restart":true}`)
	if st := decodeLAN(t, b); !st.Ready() {
		t.Fatalf("st=%+v", st)
	}
	if len(fh.FirewallCalls) != 1 || fw.Shutdowns() != 1 {
		t.Fatalf("restart-only must not re-run UAC: firewall=%v shutdowns=%d", fh.FirewallCalls, fw.Shutdowns())
	}
}
