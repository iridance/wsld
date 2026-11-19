package lan

import (
	"context"
	"errors"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func newTestManager(t *testing.T) (*Manager, *FakeHost) {
	t.Helper()
	h := &FakeHost{Dir: t.TempDir()}
	return &Manager{Host: h, Sleep: func(time.Duration) {}}, h
}

func writeConfig(t *testing.T, h *FakeHost, s string) string {
	t.Helper()
	p, _ := h.ConfigPath()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLegacyNotMirrored(t *testing.T) {
	m, h := newTestManager(t)
	h.Rules = 2 // 規則只在 mirrored 時才查
	l, err := m.Legacy(context.Background())
	if err != nil || l.Mirrored || l.Rules != 0 {
		t.Fatalf("l=%+v err=%v", l, err)
	}
	writeConfig(t, h, "[wsl2]\nnetworkingMode=nat\n")
	if l, _ := m.Legacy(context.Background()); l.Mirrored {
		t.Fatal("nat is not mirrored")
	}
}

func TestLegacyMirroredWithRules(t *testing.T) {
	m, h := newTestManager(t)
	writeConfig(t, h, "[wsl2]\r\nNetworkingMode = Mirrored\r\n")
	h.Rules = 2
	l, err := m.Legacy(context.Background())
	if err != nil || !l.Mirrored || l.Rules != 2 {
		t.Fatalf("l=%+v err=%v", l, err)
	}
}

func TestRestoreNATRemovesRulesAndKey(t *testing.T) {
	m, h := newTestManager(t)
	p := writeConfig(t, h, "[wsl2]\r\nmemory=4GB\r\nnetworkingMode=mirrored\r\n")
	h.Rules = 2
	if err := m.RestoreNAT(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "[wsl2]\r\nmemory=4GB\r\n" {
		t.Fatalf(".wslconfig = %q", b)
	}
	bak, _ := os.ReadFile(p + ".wsld.bak")
	if !strings.Contains(string(bak), "networkingMode=mirrored") {
		t.Fatalf("backup = %q", bak)
	}
	if h.RemoveCalls != 1 || h.Rules != 0 {
		t.Fatalf("remove calls=%d rules=%d", h.RemoveCalls, h.Rules)
	}
}

func TestRestoreNATWithoutRulesSkipsUAC(t *testing.T) {
	m, h := newTestManager(t)
	writeConfig(t, h, "[wsl2]\nnetworkingMode=mirrored\n")
	if err := m.RestoreNAT(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.RemoveCalls != 0 {
		t.Fatal("no wsld rules → no UAC")
	}
}

func TestRestoreNATUnknownRulesStillRemoves(t *testing.T) {
	m, h := newTestManager(t)
	writeConfig(t, h, "[wsl2]\nnetworkingMode=mirrored\n")
	h.Rules = -1
	_ = m.RestoreNAT(context.Background())
	if h.RemoveCalls != 1 {
		t.Fatal("unknown rule state must try removal")
	}
}

func TestRestoreNATCanceledUACLeavesConfig(t *testing.T) {
	m, h := newTestManager(t)
	p := writeConfig(t, h, "[wsl2]\nnetworkingMode=mirrored\n")
	h.Rules = 2
	h.FirewallErr = errors.New("The operation was canceled by the user.")
	if err := m.RestoreNAT(context.Background()); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v", err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "[wsl2]\nnetworkingMode=mirrored\n" {
		t.Fatalf(".wslconfig changed: %q", b)
	}
}

func TestUTF16ConfigRejected(t *testing.T) {
	m, h := newTestManager(t)
	writeConfig(t, h, "\xff\xfe[\x00w\x00")
	if _, err := m.Legacy(context.Background()); !errors.Is(err, ErrUTF16Config) {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitPortFree(t *testing.T) {
	m, h := newTestManager(t)
	var slept int
	m.Sleep = func(time.Duration) { slept++ }
	h.PortBusyPolls = 3
	if !m.WaitPortFree(context.Background(), 23750, PortWaitTimeout) {
		t.Fatal("port should become free")
	}
	if slept != 3 || len(h.PortChecks) != 4 || h.PortChecks[0] != 23750 {
		t.Fatalf("slept=%d checks=%v", slept, h.PortChecks)
	}
}

func TestWaitPortFreeTimesOut(t *testing.T) {
	m, h := newTestManager(t)
	h.PortBusyPolls = 1000
	if m.WaitPortFree(context.Background(), 23750, 10*time.Second) {
		t.Fatal("must time out while the port stays busy")
	}
	if n := len(h.PortChecks); n < 5 || n > 7 {
		t.Fatalf("expected ~6 polls for 10s at 2s steps, got %d", n)
	}
	if !m.WaitPortFree(context.Background(), 0, time.Second) {
		t.Fatal("port 0 means nothing to wait for")
	}
}

func TestLANAddresses(t *testing.T) {
	ifs := []Iface{
		{Name: "Wi-Fi", Up: true, Addrs: []net.IP{net.ParseIP("192.168.1.20"), net.ParseIP("fe80::1")}},
		{Name: "Ethernet", Up: true, Addrs: []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("169.254.3.3")}},
		{Name: "vEthernet (WSL (Hyper-V firewall))", Up: true, Addrs: []net.IP{net.ParseIP("172.20.0.1")}},
		{Name: "Down", Up: false, Addrs: []net.IP{net.ParseIP("192.168.9.9")}},
		{Name: "Tailscale", Up: true, Addrs: []net.IP{net.ParseIP("100.64.0.2")}},
		{Name: "Loopback", Up: true, Addrs: []net.IP{net.ParseIP("127.0.0.1")}},
	}
	if got := LANAddresses(ifs); !slices.Equal(got, []string{"192.168.1.20", "10.0.0.5"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFirewallInfo(t *testing.T) {
	m, h := newTestManager(t)
	if fi := m.FirewallInfo(context.Background()); fi.State != FirewallUnknown {
		t.Fatalf("no exe must be unknown: %+v", fi)
	}
	m.Exe = `C:\wsld\wsld-gui.exe`
	h.AppState = FirewallMissing
	if fi := m.FirewallInfo(context.Background()); fi.State != FirewallMissing || fi.Method != MethodNetsh {
		t.Fatalf("missing: %+v", fi)
	}
	h.AppState = FirewallBlocked
	if fi := m.FirewallInfo(context.Background()); fi.State != FirewallBlocked || fi.Method != MethodHelper {
		t.Fatalf("blocked: %+v", fi)
	}
	h.AppState = FirewallUnknown
	if fi := m.FirewallInfo(context.Background()); fi.State != FirewallUnknown {
		t.Fatalf("unknown: %+v", fi)
	}
}

func TestAllowFirewallUsesNetshWhenNoRules(t *testing.T) {
	t.Setenv("SystemRoot", `C:\Windows`)
	m, h := newTestManager(t)
	m.Exe = `C:\wsld\wsld-gui.exe`
	h.AppState = FirewallMissing
	if err := m.AllowFirewall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.Elevated) != 1 || h.Elevated[0].File != `C:\Windows\System32\netsh.exe` ||
		!strings.Contains(h.Elevated[0].Params, `program="C:\wsld\wsld-gui.exe"`) {
		t.Fatalf("elevated = %+v", h.Elevated)
	}
	if fi := m.FirewallInfo(context.Background()); fi.State != FirewallAllowed {
		t.Fatalf("after allow: %+v", fi)
	}
}

func TestAllowFirewallUsesHelperWhenBlocked(t *testing.T) {
	m, h := newTestManager(t)
	m.Exe = `C:\wsld\wsld-gui.exe`
	h.AppState = FirewallBlocked
	if err := m.AllowFirewall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(h.Elevated) != 1 || h.Elevated[0].File != m.Exe || h.Elevated[0].Params != AllowFirewallFlag {
		t.Fatalf("elevated = %+v", h.Elevated)
	}
}

func TestAllowFirewallCanceled(t *testing.T) {
	m, h := newTestManager(t)
	m.Exe = `C:\wsld\wsld-gui.exe`
	h.AppState, h.AllowErr = FirewallMissing, ErrUACCanceled
	if err := m.AllowFirewall(context.Background()); !errors.Is(err, ErrUACCanceled) {
		t.Fatalf("err = %v", err)
	}
	if fi := m.FirewallInfo(context.Background()); fi.State != FirewallMissing {
		t.Fatalf("state must stay missing: %+v", fi)
	}
	m.Exe = ""
	if err := m.AllowFirewall(context.Background()); err == nil {
		t.Fatal("empty exe must fail")
	}
}
