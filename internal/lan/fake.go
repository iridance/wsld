package lan

import (
	"context"
	"errors"
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
	// AppState 決定 AppRules 回傳的規則（空字串 = FirewallAllowed）：
	// allowed → wsld 規則；missing → 無規則；blocked → Windows 視窗按取消產生的封鎖規則；unknown → 查詢失敗。
	AppState FirewallState
	// AllowErr 非 nil 時 RunElevated 回傳它（例如 ErrUACCanceled）。
	AllowErr error
	// AllowCalls 記錄 RunElevated 的呼叫次數。
	AllowCalls int
	// Elevated 記錄 RunElevated 的每次呼叫。
	Elevated []Elevation
	// AllowHold 非 nil 時 RunElevated 等到它關閉才繼續（模擬使用者還沒回應 UAC）。
	AllowHold chan struct{}
}

// Elevation 是 FakeHost.RunElevated 收到的一次提權。
type Elevation struct {
	File   string `json:"file"`
	Params string `json:"params"`
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

// AppRules 實作 Host：依 AppState 產生對應的規則。
func (f *FakeHost) AppRules(_ context.Context, exe string) ([]FwRule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	both := FwProfilePrivate | FwProfilePublic
	switch f.AppState {
	case "", FirewallAllowed:
		return []FwRule{{Name: AppRuleName, App: exe, Inbound: true, Allow: true, Enabled: true, Protocol: FwProtoTCP, Profiles: both}}, nil
	case FirewallBlocked:
		return []FwRule{{Name: "wsld-gui", App: exe, Inbound: true, Enabled: true, Protocol: FwProtoTCP, Profiles: both}}, nil
	case FirewallMissing:
		return nil, nil
	}
	return nil, errors.New("firewall unavailable")
}

// RunElevated 實作 Host：記錄呼叫；成功後狀態變為 FirewallAllowed。
func (f *FakeHost) RunElevated(ctx context.Context, file, params string) error {
	f.mu.Lock()
	hold := f.AllowHold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AllowCalls++
	f.Elevated = append(f.Elevated, Elevation{File: file, Params: params})
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
