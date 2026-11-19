package gui

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
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
	e.s.LAN = &lan.Manager{Host: le.host, Exe: `C:\wsld\wsld-gui.exe`, Sleep: func(time.Duration) {}}
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

func TestLANFirewallStatus(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	le.host.AppState = lan.FirewallMissing
	// 授權屬於 app 層級：區網存取關閉時也回報，並附上授權方式與 exe 路徑（說明對話框用）
	st := decodeLAN(t, mustBody(e.req(t, "GET", "/api/lan", "")))
	if st.Firewall != lan.FirewallMissing || st.FirewallMethod != lan.MethodNetsh || st.Exe != `C:\wsld\wsld-gui.exe` {
		t.Fatalf("firewall must be reported while off: %+v", st)
	}
	st = decodeLAN(t, mustBody(e.req(t, "PUT", "/api/lan", `{"enabled":true}`)))
	if !st.Enabled || st.Firewall != lan.FirewallMissing || le.host.AllowCalls != 0 {
		t.Fatalf("enable must only save the setting, never elevate: %+v allow=%d", st, le.host.AllowCalls)
	}
	resp, b := e.req(t, "POST", "/api/lan/firewall", "")
	if resp.StatusCode != 200 || decodeLAN(t, b).Firewall != lan.FirewallAllowed || le.host.AllowCalls != 1 {
		t.Fatalf("status=%d body=%s allow=%d", resp.StatusCode, b, le.host.AllowCalls)
	}
	if el := le.host.Elevated[0]; !strings.HasSuffix(el.File, `\System32\netsh.exe`) {
		t.Fatalf("missing rule must elevate netsh: %+v", el)
	}
}

func TestLANFirewallBlockedUsesHelper(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	le.host.AppState = lan.FirewallBlocked
	if st := decodeLAN(t, mustBody(e.req(t, "GET", "/api/lan", ""))); st.FirewallMethod != lan.MethodHelper {
		t.Fatalf("blocked must use the helper: %+v", st)
	}
	if resp, b := e.req(t, "POST", "/api/lan/firewall", ""); resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	if el := le.host.Elevated[0]; el.File != `C:\wsld\wsld-gui.exe` || el.Params != lan.AllowFirewallFlag {
		t.Fatalf("elevated = %+v", el)
	}
}

// 未授權時不在內網 IP 監聽：避免 Windows 自己跳「安全性警訊」（按取消會產生封鎖規則）；授權後立即開始轉發。
func TestLANNoListenUntilFirewallAllowed(t *testing.T) {
	for _, state := range []lan.FirewallState{lan.FirewallMissing, lan.FirewallBlocked} {
		e := newEnv(t, "")
		le := withLAN(t, e)
		le.host.AppState = state
		st := decodeLAN(t, mustBody(e.req(t, "PUT", "/api/lan", `{"enabled":true}`)))
		if !st.Enabled || len(st.Forwards) != 0 || len(le.listen) != 0 {
			t.Fatalf("%s: must not listen: st=%+v listen=%v", state, st, le.listen)
		}
		e.s.SyncLAN(t.Context())
		if len(le.listen) != 0 {
			t.Fatalf("%s: background sync listened: %v", state, le.listen)
		}
		st = decodeLAN(t, mustBody(e.req(t, "POST", "/api/lan/firewall", "")))
		if st.Firewall != lan.FirewallAllowed || len(st.Forwards) != 1 || strings.Join(le.listen, ",") != "192.168.1.20:8080" {
			t.Fatalf("%s: after allow: st=%+v listen=%v", state, st, le.listen)
		}
	}
	// 查不到狀態（被原則管理、COM 失敗）時照常轉發
	e := newEnv(t, "")
	le := withLAN(t, e)
	le.host.AppState = lan.FirewallUnknown
	if st := decodeLAN(t, mustBody(e.req(t, "PUT", "/api/lan", `{"enabled":true}`))); len(st.Forwards) != 1 {
		t.Fatalf("unknown must still forward: %+v", st)
	}
}

// 背景同步沿用快取的防火牆狀態（逐條讀規則較慢）；GET /api/lan 會強制重新讀取。
func TestLANFirewallStateCached(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	le.host.AppState = lan.FirewallMissing
	_, _ = e.req(t, "PUT", "/api/lan", `{"enabled":true}`)
	le.host.Set(func(h *lan.FakeHost) { h.AppState = lan.FirewallAllowed }) // 例如使用者在 wf.msc 手動加了規則
	e.s.SyncLAN(t.Context())
	if len(le.listen) != 0 {
		t.Fatalf("background sync must use the cached state: %v", le.listen)
	}
	if st := decodeLAN(t, mustBody(e.req(t, "GET", "/api/lan", ""))); st.Firewall != lan.FirewallAllowed || len(st.Forwards) != 1 {
		t.Fatalf("GET must refresh: %+v", st)
	}
}

func TestFirewallPendingWhileUACOpen(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	le.host.AppState = lan.FirewallMissing
	le.host.AllowHold = make(chan struct{})
	first := make(chan int, 1)
	go func() {
		resp, _ := e.req(t, "POST", "/api/lan/firewall", "")
		first <- resp.StatusCode
	}()
	deadline := time.Now().Add(2 * time.Second)
	for !decodeLAN(t, mustBody(e.req(t, "GET", "/api/lan", ""))).FirewallPending {
		if time.Now().After(deadline) {
			t.Fatal("firewall_pending never reported")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// 第二次按下（例如另一個視窗）：排在第一個請求後面，對方授權完成就不再跳第二次 UAC
	second := make(chan int, 1)
	go func() {
		resp, _ := e.req(t, "POST", "/api/lan/firewall", "")
		second <- resp.StatusCode
	}()
	close(le.host.AllowHold)
	if a, b := <-first, <-second; a != 200 || b != 200 {
		t.Fatalf("status = %d, %d", a, b)
	}
	st := decodeLAN(t, mustBody(e.req(t, "GET", "/api/lan", "")))
	if st.FirewallPending || st.Firewall != lan.FirewallAllowed || le.host.AllowCalls != 1 {
		t.Fatalf("st=%+v allow=%d", st, le.host.AllowCalls)
	}
}

func TestLANFirewallCanceledUAC(t *testing.T) {
	e := newEnv(t, "")
	le := withLAN(t, e)
	le.host.AppState = lan.FirewallBlocked
	le.host.AllowErr = lan.ErrUACCanceled
	resp, b := e.req(t, "POST", "/api/lan/firewall", "")
	var ae apiError
	_ = json.Unmarshal(b, &ae)
	if resp.StatusCode != 409 || ae.Code != "canceled" || !strings.Contains(ae.Error, "UAC") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, b)
	}
	_, _ = e.req(t, "PUT", "/api/lan", `{"enabled":true}`)
	if st := decodeLAN(t, mustBody(e.req(t, "GET", "/api/lan", ""))); st.Firewall != lan.FirewallBlocked {
		t.Fatalf("firewall=%q", st.Firewall)
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

func mustBody(_ *http.Response, b []byte) []byte { return b }
