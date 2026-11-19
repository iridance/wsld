package lan

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func newTestManager(t *testing.T, mode string) (*Manager, *FakeHost) {
	t.Helper()
	h := &FakeHost{Dir: t.TempDir(), WinBuild: 26300}
	m := &Manager{Host: h, Mode: func(context.Context, string) (string, error) {
		if mode == "" {
			return "", errors.New("unknown")
		}
		return mode, nil
	}}
	return m, h
}

func writeConfig(t *testing.T, h *FakeHost, s string) string {
	t.Helper()
	p, _ := h.ConfigPath()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStatusDefaultNAT(t *testing.T) {
	m, _ := newTestManager(t, "nat")
	st, err := m.Status(context.Background(), "Ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Supported || st.Enabled || st.RestartPending || st.Effective != "nat" || st.Firewall != FirewallMissing {
		t.Fatalf("st = %+v", st)
	}
	if !slices.Equal(st.Addresses, []string{"192.168.1.20"}) || st.PublicNetwork {
		t.Fatalf("st = %+v", st)
	}
}

func TestEnableWritesConfigAndFirewall(t *testing.T) {
	m, h := newTestManager(t, "nat")
	p := writeConfig(t, h, "[wsl2]\r\nmemory=4GB\r\n")
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "[wsl2]\r\nmemory=4GB\r\nnetworkingMode=mirrored\r\n" {
		t.Fatalf(".wslconfig = %q", b)
	}
	bak, err := os.ReadFile(p + ".wsld.bak")
	if err != nil || string(bak) != "[wsl2]\r\nmemory=4GB\r\n" {
		t.Fatalf("backup = %q, %v", bak, err)
	}
	if !slices.Equal(h.FirewallCalls, []bool{true}) {
		t.Fatalf("firewall calls = %v", h.FirewallCalls)
	}
	st, _ := m.Status(context.Background(), "Ubuntu")
	if !st.Enabled || !st.RestartPending || st.Firewall != FirewallPresent || st.Ready() {
		t.Fatalf("st = %+v", st)
	}
}

func TestEnableWithoutExistingConfig(t *testing.T) {
	m, h := newTestManager(t, "")
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, _ := h.ConfigPath()
	b, _ := os.ReadFile(p)
	if string(b) != "[wsl2]\nnetworkingMode=mirrored\n" {
		t.Fatalf(".wslconfig = %q", b)
	}
	if _, err := os.Stat(p + ".wsld.bak"); !os.IsNotExist(err) {
		t.Fatal("no backup expected for a new file")
	}
}

func TestEnableBackupOnlyOnce(t *testing.T) {
	m, h := newTestManager(t, "")
	p := writeConfig(t, h, "[wsl2]\nmemory=1GB\n")
	_ = m.Enable(context.Background())
	_ = m.Disable(context.Background())
	_ = m.Enable(context.Background())
	bak, _ := os.ReadFile(p + ".wsld.bak")
	if string(bak) != "[wsl2]\nmemory=1GB\n" {
		t.Fatalf("backup overwritten: %q", bak)
	}
}

func TestEnableCanceledUACLeavesConfig(t *testing.T) {
	m, h := newTestManager(t, "nat")
	p := writeConfig(t, h, "[wsl2]\nmemory=1GB\n")
	h.FirewallErr = errors.New("The operation was canceled by the user.")
	if err := m.Enable(context.Background()); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err = %v", err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "[wsl2]\nmemory=1GB\n" {
		t.Fatalf(".wslconfig changed: %q", b)
	}
}

func TestEnableUnsupportedBuild(t *testing.T) {
	m, h := newTestManager(t, "nat")
	h.WinBuild = 19045
	if err := m.Enable(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if len(h.FirewallCalls) != 0 {
		t.Fatal("firewall must not be touched")
	}
	st, _ := m.Status(context.Background(), "")
	if st.Supported {
		t.Fatal("must be unsupported")
	}
}

func TestDisableRemovesKeyAndRules(t *testing.T) {
	m, h := newTestManager(t, "mirrored")
	p := writeConfig(t, h, "[wsl2]\nnetworkingMode=mirrored\nmemory=1GB\n")
	h.Rules = 2
	st, _ := m.Status(context.Background(), "Ubuntu")
	if !st.Ready() {
		t.Fatalf("expected ready: %+v", st)
	}
	if err := m.Disable(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "[wsl2]\nmemory=1GB\n" {
		t.Fatalf(".wslconfig = %q", b)
	}
	st, _ = m.Status(context.Background(), "Ubuntu")
	if st.Enabled || !st.RestartPending || st.Firewall != FirewallMissing {
		t.Fatalf("st = %+v", st)
	}
}

func TestStatusPublicNetworkAndUnknownFirewall(t *testing.T) {
	m, h := newTestManager(t, "")
	h.NetProfiles = []string{"Public"}
	h.Rules = -1
	st, _ := m.Status(context.Background(), "Ubuntu")
	if !st.PublicNetwork || st.Firewall != FirewallUnknown || st.Effective != "" || st.RestartPending {
		t.Fatalf("st = %+v", st)
	}
}

func TestUTF16ConfigRejected(t *testing.T) {
	m, h := newTestManager(t, "")
	writeConfig(t, h, "\xff\xfe[\x00w\x00")
	if err := m.Enable(context.Background()); !errors.Is(err, ErrUTF16Config) {
		t.Fatalf("err = %v", err)
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
	got := LANAddresses(ifs)
	if !slices.Equal(got, []string{"192.168.1.20", "10.0.0.5"}) {
		t.Fatalf("got %v", got)
	}
}

func TestFakeHostConfigPath(t *testing.T) {
	h := &FakeHost{Dir: "/x"}
	if p, _ := h.ConfigPath(); p != filepath.Join("/x", ".wslconfig") {
		t.Fatal(p)
	}
}
