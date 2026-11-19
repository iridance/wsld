// Package lan 管理「區網存取」：把 WSL 切到 mirrored 網路模式並開放 Hyper-V 防火牆，
// 讓容器 published 埠可從其他設備透過 Windows 內網 IP 連入。
//
// Docker API 仍只綁 127.0.0.1；mirrored 模式不會把 loopback 鏡像到區網，因此 API 不對外。
// 狀態的真實來源是系統本身（.wslconfig、wslinfo、防火牆規則），不寫進 config.toml。
package lan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"

	"github.com/iridance/wsld/internal/wsl"
)

// 網路模式值（.wslconfig networkingMode／wslinfo --networking-mode）。
const (
	ModeMirrored = "mirrored"
	ModeNAT      = "nat"
)

// MinBuild 是 mirrored 網路模式需要的最低 Windows build（Windows 11 22H2）。
const MinBuild = 22621

// WSLVMCreatorID 是 WSL 在 Hyper-V 防火牆的 VMCreatorId。
const WSLVMCreatorID = "{40E0AC32-46A5-438A-A0B2-2B479E8F2E90}"

// RuleNames 是 wsld 建立的 Hyper-V 防火牆規則名稱。
var RuleNames = []string{"wsld-lan-tcp", "wsld-lan-udp"}

// 防火牆狀態。
const (
	FirewallPresent = "present"
	FirewallMissing = "missing"
	FirewallUnknown = "unknown"
)

// ErrUnsupported 表示 Windows 版本不支援 mirrored 網路模式（或非 Windows）。
var ErrUnsupported = errors.New("LAN access requires Windows 11 22H2 (build 22621) or later")

// Probe 是一次查詢防火牆與網路設定檔的結果。
type Probe struct {
	// Rules 是已存在的 wsld 規則數；-1 表示無法查詢。
	Rules int
	// Profiles 是目前連線網路的 Windows 設定檔（Private／Public／DomainAuthenticated）。
	Profiles []string
}

// Host 抽象 Windows 端的副作用（真實實作在 host_windows.go；測試用 FakeHost）。
type Host interface {
	// ConfigPath 回傳 %USERPROFILE%\.wslconfig。
	ConfigPath() (string, error)
	// Build 回傳 Windows build；非 Windows 回 0。
	Build() uint32
	// SetFirewall 以系統管理員權限（UAC）建立或移除 wsld 的 Hyper-V 防火牆規則。
	SetFirewall(ctx context.Context, enable bool) error
	// Probe 查詢規則是否存在與網路設定檔（不需提權）。
	Probe(ctx context.Context) (Probe, error)
	// Interfaces 列出網卡與其 IPv4 位址。
	Interfaces() ([]Iface, error)
}

// Iface 是一張網卡的名稱與位址。
type Iface struct {
	Name  string
	Up    bool
	Addrs []net.IP
}

// Status 是區網存取的目前狀態。
type Status struct {
	Supported bool   `json:"supported"`
	Build     uint32 `json:"build"`
	// Configured 是 .wslconfig 的 networkingMode（空 = 預設 nat）。
	Configured string `json:"configured"`
	// Effective 是 active distro 內 wslinfo 回報的實際模式（空 = 未知）。
	Effective      string   `json:"effective"`
	Enabled        bool     `json:"enabled"`
	RestartPending bool     `json:"restartPending"`
	Firewall       string   `json:"firewall"`
	Profiles       []string `json:"profiles"`
	PublicNetwork  bool     `json:"publicNetwork"`
	Addresses      []string `json:"addresses"`
}

// Ready 表示區網存取已設定、已生效且防火牆規則存在。
func (s Status) Ready() bool {
	return s.Enabled && !s.RestartPending && s.Effective == ModeMirrored && s.Firewall == FirewallPresent
}

// Manager 實作區網存取的查詢與切換。
type Manager struct {
	Host Host
	// Mode 回傳 distro 內實際的網路模式（預設用 wslinfo）。
	Mode func(ctx context.Context, distro string) (string, error)
}

// NewManager 以真實 wsl.exe 查詢實際模式。
func NewManager(h Host, r wsl.Runner) *Manager {
	return &Manager{Host: h, Mode: func(ctx context.Context, distro string) (string, error) {
		return wsl.NetworkingMode(ctx, r, distro)
	}}
}

// Status 查詢狀態；distro 為空時不查實際模式。
func (m *Manager) Status(ctx context.Context, distro string) (Status, error) {
	st := Status{Build: m.Host.Build(), Firewall: FirewallUnknown, Profiles: []string{}, Addresses: []string{}}
	st.Supported = st.Build >= MinBuild
	content, err := m.readConfig()
	if err != nil {
		return st, err
	}
	st.Configured = strings.ToLower(GetKey(content, "wsl2", "networkingMode"))
	st.Enabled = st.Configured == ModeMirrored
	if distro != "" && m.Mode != nil {
		if mode, err := m.Mode(ctx, distro); err == nil {
			st.Effective = mode
		}
	}
	if st.Effective != "" {
		st.RestartPending = st.Enabled != (st.Effective == ModeMirrored)
	}
	if p, err := m.Host.Probe(ctx); err == nil {
		switch {
		case p.Rules < 0:
			st.Firewall = FirewallUnknown
		case p.Rules >= len(RuleNames):
			st.Firewall = FirewallPresent
		default:
			st.Firewall = FirewallMissing
		}
		if p.Profiles != nil {
			st.Profiles = p.Profiles
		}
		st.PublicNetwork = slices.Contains(p.Profiles, "Public")
	}
	if ifs, err := m.Host.Interfaces(); err == nil {
		st.Addresses = LANAddresses(ifs)
	}
	return st, nil
}

// Enable 開放 Hyper-V 防火牆（UAC）並在 .wslconfig 設 networkingMode=mirrored。
// 先處理防火牆：使用者取消 UAC 時不改 .wslconfig。生效需要 wsl --shutdown（由呼叫端決定）。
func (m *Manager) Enable(ctx context.Context) error {
	if m.Host.Build() < MinBuild {
		return ErrUnsupported
	}
	if err := m.Host.SetFirewall(ctx, true); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	return m.editConfig(func(c string) string { return SetKey(c, "wsl2", "networkingMode", ModeMirrored) })
}

// Disable 移除防火牆規則（UAC）並從 .wslconfig 移除 networkingMode（回到預設 nat）。
func (m *Manager) Disable(ctx context.Context) error {
	if err := m.Host.SetFirewall(ctx, false); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	return m.editConfig(func(c string) string { return RemoveKey(c, "wsl2", "networkingMode") })
}

func (m *Manager) readConfig() (string, error) {
	path, err := m.Host.ConfigPath()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if isUTF16(b) {
		return "", ErrUTF16Config
	}
	return string(b), nil
}

// editConfig 改寫 .wslconfig；第一次改寫前備份為 .wslconfig.wsld.bak。
func (m *Manager) editConfig(edit func(string) string) error {
	path, err := m.Host.ConfigPath()
	if err != nil {
		return err
	}
	orig, err := m.readConfig()
	if err != nil {
		return err
	}
	next := edit(orig)
	if next == orig {
		return nil
	}
	if orig != "" {
		bak := path + ".wsld.bak"
		if _, err := os.Stat(bak); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(bak, []byte(orig), 0o644); err != nil {
				return fmt.Errorf("backup .wslconfig: %w", err)
			}
		}
	}
	if err := os.WriteFile(path, []byte(next), 0o644); err != nil {
		return fmt.Errorf("write .wslconfig: %w", err)
	}
	return nil
}

// LANAddresses 挑出可給區網設備連線的 IPv4：啟用中、私有位址、排除 Hyper-V 虛擬網卡。
func LANAddresses(ifs []Iface) []string {
	out := []string{}
	for _, f := range ifs {
		if !f.Up || strings.HasPrefix(strings.ToLower(f.Name), "vethernet") {
			continue
		}
		for _, ip := range f.Addrs {
			v4 := ip.To4()
			if v4 == nil || v4.IsLoopback() || !v4.IsPrivate() {
				continue
			}
			s := v4.String()
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	return out
}

// SystemInterfaces 以 net.Interfaces 列出網卡（Host 實作共用）。
func SystemInterfaces() ([]Iface, error) {
	nifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(nifs))
	for _, n := range nifs {
		f := Iface{Name: n.Name, Up: n.Flags&net.FlagUp != 0 && n.Flags&net.FlagLoopback == 0}
		addrs, _ := n.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				f.Addrs = append(f.Addrs, ipn.IP)
			}
		}
		out = append(out, f)
	}
	return out, nil
}
