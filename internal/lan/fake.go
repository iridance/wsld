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
	// WinBuild 是回報的 Windows build。
	WinBuild uint32
	// Rules 目前存在的規則數。
	Rules int
	// NetProfiles 目前網路設定檔。
	NetProfiles []string
	// Ifaces 網卡清單（nil 則給一張 192.168.1.20 的 Wi-Fi）。
	Ifaces []Iface
	// FirewallErr 非 nil 時 SetFirewall 回傳它（模擬取消 UAC）。
	FirewallErr error
	// FirewallCalls 記錄 SetFirewall 的參數。
	FirewallCalls []bool
}

// ConfigPath 實作 Host。
func (f *FakeHost) ConfigPath() (string, error) { return filepath.Join(f.Dir, ".wslconfig"), nil }

// Build 實作 Host。
func (f *FakeHost) Build() uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.WinBuild
}

// SetFirewall 實作 Host。
func (f *FakeHost) SetFirewall(_ context.Context, enable bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.FirewallCalls = append(f.FirewallCalls, enable)
	if f.FirewallErr != nil {
		return f.FirewallErr
	}
	if enable {
		f.Rules = len(RuleNames)
	} else {
		f.Rules = 0
	}
	return nil
}

// Probe 實作 Host。
func (f *FakeHost) Probe(context.Context) (Probe, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	profiles := f.NetProfiles
	if profiles == nil {
		profiles = []string{"Private"}
	}
	return Probe{Rules: f.Rules, Profiles: append([]string{}, profiles...)}, nil
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

// Set 在鎖內修改 FakeHost（testhost 控制端點用）。
func (f *FakeHost) Set(fn func(*FakeHost)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}
