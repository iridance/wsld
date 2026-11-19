package gui

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/lan"
)

type lanEnv struct {
	host   *lan.FakeHost
	wsl    *FakeWSL
	listen []string
}

func withLAN(t *testing.T, e *env) *lanEnv {
	t.Helper()
	le := &lanEnv{host: &lan.FakeHost{Dir: t.TempDir()}, wsl: NewFakeWSL()}
	e.s.WSL = le.wsl
	e.s.LAN = &lan.Manager{Host: le.host, Sleep: func(time.Duration) {}}
	e.s.Forwarder = &lan.Forwarder{Listen: func(network, addr string) (net.Listener, error) {
		le.listen = append(le.listen, addr)
		return net.Listen("tcp", "127.0.0.1:0") // 測試不綁 192.168.x.x
	}}
	t.Cleanup(e.s.Forwarder.Close)
	return le
}

func decodeLAN(t *testing.T, b []byte) LANStatus {
	t.Helper()
	var st LANStatus
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatalf("decode %s: %v", b, err)
	}
	return st
}

func TestLANUnavailable(t *testing.T) {
	e := newEnv(t, "")
	for _, c := range [][2]string{{"GET", "/api/lan"}, {"PUT", "/api/lan"}, {"POST", "/api/lan/restore-nat"}} {
		if resp, _ := e.req(t, c[0], c[1], `{"enabled":true}`); resp.StatusCode != 501 {
			t.Fatalf("%s %s: status %d", c[0], c[1], resp.StatusCode)
		}
	}
	_, b := e.req(t, "GET", "/api/meta", "")
	var m Meta
	_ = json.Unmarshal(b, &m)
	if m.LANSupported {
		t.Fatal("lan_supported must be false without a forwarder")
	}
}

func TestLANOffByDefault(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	resp, b := e.req(t, "GET", "/api/lan", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	st := decodeLAN(t, b)
	if st.Enabled || st.Mirrored || len(st.Forwards) != 0 || len(st.Addresses) != 1 || len(le.listen) != 0 {
		t.Fatalf("st=%+v listen=%v", st, le.listen)
	}
	_, b = e.req(t, "GET", "/api/meta", "")
	var m Meta
	_ = json.Unmarshal(b, &m)
	if !m.LANSupported {
		t.Fatal("lan_supported must be true")
	}
}

func TestLANEnableForwardsPublishedPorts(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	resp, b := e.req(t, "PUT", "/api/lan", `{"enabled":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	st := decodeLAN(t, b)
	// FakeEngine 預設 web 容器 0.0.0.0:8080
	if !st.Enabled || len(st.Forwards) != 1 || st.Forwards[0].Port != 8080 || st.Forwards[0].Container != "web" ||
		st.Forwards[0].Address != "192.168.1.20" || st.Forwards[0].Error != "" {
		t.Fatalf("st=%+v", st)
	}
	if strings.Join(le.listen, ",") != "192.168.1.20:8080" {
		t.Fatalf("listen=%v", le.listen)
	}
	c, err := config.Load(e.cfgP)
	if err != nil || !c.LANAccess {
		t.Fatalf("lan_access not saved: %+v %v", c, err)
	}
	// 新容器 published 埠 → 背景同步後加入
	e.engine.AddContainer("api", "node:22", "running", []dockerapi.Port{{IP: "0.0.0.0", PrivatePort: 3000, PublicPort: 3000, Type: "tcp"}})
	e.s.SyncLAN(t.Context())
	if fw := e.s.Forwarder.Forwards(); len(fw) != 2 || fw[0].Port != 3000 || fw[0].Container != "api" {
		t.Fatalf("forwards=%+v", fw)
	}
	// 關閉
	_, b = e.req(t, "PUT", "/api/lan", `{"enabled":false}`)
	if st := decodeLAN(t, b); st.Enabled || len(st.Forwards) != 0 {
		t.Fatalf("st=%+v", st)
	}
}

func TestLANEngineDownKeepsForwards(t *testing.T) {
	e := newEnv(t, "")
	withLAN(t, e)
	_, _ = e.req(t, "PUT", "/api/lan", `{"enabled":true}`)
	e.engine.SetDown(true)
	e.s.SyncLAN(t.Context())
	if fw := e.s.Forwarder.Forwards(); len(fw) != 1 {
		t.Fatalf("engine down must not drop forwards: %+v", fw)
	}
}

func TestLANBadJSON(t *testing.T) {
	e := newEnv(t, "")
	withLAN(t, e)
	if resp, _ := e.req(t, "PUT", "/api/lan", `nope`); resp.StatusCode != 400 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestLANRestoreNAT(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	p := filepath.Join(le.host.Dir, ".wslconfig")
	_ = os.WriteFile(p, []byte("[wsl2]\nnetworkingMode=mirrored\n"), 0o644)
	le.host.Rules = 2
	le.host.PortBusyPolls = 2
	_, b := e.req(t, "GET", "/api/lan", "")
	if st := decodeLAN(t, b); !st.Mirrored {
		t.Fatalf("expected mirrored: %+v", st)
	}
	starts := e.life.Starts()
	resp, b := e.req(t, "POST", "/api/lan/restore-nat", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if st := decodeLAN(t, b); st.Mirrored {
		t.Fatalf("still mirrored: %+v", st)
	}
	got, _ := os.ReadFile(p)
	if strings.Contains(string(got), "networkingMode") {
		t.Fatalf(".wslconfig = %q", got)
	}
	if le.host.RemoveCalls != 1 || le.wsl.Shutdowns() != 1 || e.life.Starts() != starts+1 {
		t.Fatalf("remove=%d shutdowns=%d starts=%d", le.host.RemoveCalls, le.wsl.Shutdowns(), e.life.Starts())
	}
	if len(le.host.PortChecks) != 3 || le.host.PortChecks[0] != 23750 {
		t.Fatalf("must wait for API port before starting: %v", le.host.PortChecks)
	}
}

func TestLANRestoreNATCanceledUAC(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	_ = os.WriteFile(filepath.Join(le.host.Dir, ".wslconfig"), []byte("[wsl2]\nnetworkingMode=mirrored\n"), 0o644)
	le.host.Rules = 2
	le.host.FirewallErr = errors.New("The operation was canceled by the user.")
	resp, b := e.req(t, "POST", "/api/lan/restore-nat", "")
	if resp.StatusCode != 500 || !strings.Contains(string(b), "canceled") || le.wsl.Shutdowns() != 0 {
		t.Fatalf("status=%d body=%s shutdowns=%d", resp.StatusCode, b, le.wsl.Shutdowns())
	}
}

func TestLANForwardsEndpoint(t *testing.T) {
	e := newEnv(t, "")
	withLAN(t, e)
	_, b := e.req(t, "GET", "/api/lan/forwards", "")
	var off LANForwards
	_ = json.Unmarshal(b, &off)
	if off.Enabled || len(off.Forwards) != 0 {
		t.Fatalf("off=%+v", off)
	}
	_, _ = e.req(t, "PUT", "/api/lan", `{"enabled":true}`)
	resp, b := e.req(t, "GET", "/api/lan/forwards", "")
	var on LANForwards
	if err := json.Unmarshal(b, &on); err != nil || resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	if !on.Enabled || len(on.Forwards) != 1 || on.Forwards[0].Container != "web" || on.Forwards[0].Port != 8080 {
		t.Fatalf("on=%+v", on)
	}
}

func TestLANOpenOnlyForwardedURLs(t *testing.T) {
	e := newEnv(t, "")
	withLAN(t, e)
	_, _ = e.req(t, "PUT", "/api/lan", `{"enabled":true}`)
	e.window = nil
	if resp, b := e.req(t, "POST", "/api/lan/open", `{"url":"http://192.168.1.20:8080"}`); resp.StatusCode != 204 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	if len(e.window) != 1 || e.window[0] != "open-url:http://192.168.1.20:8080" {
		t.Fatalf("window=%v", e.window)
	}
	for _, bad := range []string{`{"url":"http://192.168.1.20:9999"}`, `{"url":"file:///C:/Windows"}`, `{"url":"http://evil.example"}`, `nope`} {
		if resp, _ := e.req(t, "POST", "/api/lan/open", bad); resp.StatusCode != 400 {
			t.Fatalf("%s: status %d", bad, resp.StatusCode)
		}
	}
	if len(e.window) != 1 {
		t.Fatalf("rejected URLs must not open: %v", e.window)
	}
}
