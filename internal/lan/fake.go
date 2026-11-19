package lan

import (
	"context"
	"net"
	"path/filepath"
	"sync"
)

// FakeHost 是測試／testhost 用的 Host：.wslconfig 寫在 Dir，防火牆只記錄狀態。
type FakeHost struct {
	mu sync.Mutex
	// Dir 是放 .wslconfig 的目錄。
	Dir string
	// Rules 目前存在的 wsld 規則數。
	Rules int
	// Ifaces 網卡清單（nil 則給一張 192.168.1.20 的 Wi-Fi）。
	Ifaces []Iface
	// FirewallErr 非 nil 時 RemoveFirewallRules 回傳它（模擬取消 UAC）。
	FirewallErr error
	// RemoveCalls 記錄 RemoveFirewallRules 的呼叫次數。
	RemoveCalls int
	// PortBusyPolls：PortInUse 前幾次回報「仍佔用」（模擬 TIME_WAIT）。
	PortBusyPolls int
	// PortChecks 記錄 PortInUse 被呼叫的埠。
	PortChecks []int
	// AppState 是 AppFirewall 回傳的狀態（空字串 = FirewallAllowed）。
	AppState FirewallState
	// AllowErr 非 nil 時 AllowApp 回傳它（例如 ErrUACCanceled）。
	AllowErr error
	// AllowCalls 記錄 AllowApp 的呼叫次數。
	AllowCalls int
}

// ConfigPath 實作 Host。
func (f *FakeHost) ConfigPath() (string, error) { return filepath.Join(f.Dir, ".wslconfig"), nil }

// RemoveFirewallRules 實作 Host。
func (f *FakeHost) RemoveFirewallRules(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemoveCalls++
	if f.FirewallErr != nil {
		return f.FirewallErr
	}
	f.Rules = 0
	return nil
}

// FirewallRules 實作 Host。
func (f *FakeHost) FirewallRules(context.Context) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Rules
}

// Interfaces 實作 Host。
func (f *FakeHost) Interfaces() ([]Iface, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Ifaces != nil {
		return f.Ifaces, nil
	}
	return []Iface{{Name: "Wi-Fi", Up: true, Addrs: []net.IP{net.ParseIP("192.168.1.20")}}}, nil
}

// PortInUse 實作 Host。
func (f *FakeHost) PortInUse(_ context.Context, port int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PortChecks = append(f.PortChecks, port)
	if f.PortBusyPolls > 0 {
		f.PortBusyPolls--
		return true, nil
	}
	return false, nil
}

// AppFirewall 實作 Host。
func (f *FakeHost) AppFirewall(context.Context, string) (FirewallState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.AppState == "" {
		return FirewallAllowed, nil
	}
	return f.AppState, nil
}

// AllowApp 實作 Host：成功後狀態變為 FirewallAllowed。
func (f *FakeHost) AllowApp(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AllowCalls++
	if f.AllowErr != nil {
		return f.AllowErr
	}
	f.AppState = FirewallAllowed
	return nil
}

// Set 在鎖內修改 FakeHost（testhost 控制端點用）。
func (f *FakeHost) Set(fn func(*FakeHost)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}
