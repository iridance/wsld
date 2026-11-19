// Package lan 提供「區網存取」：wsld-gui 在 Windows 的內網 IP 上轉發容器 published 埠（Forwarder），
// 讓其他設備可透過這台電腦的 IP 連到容器。WSL 維持預設 NAT；Docker API 不轉發，仍只綁 127.0.0.1。
//
// 另含 0.9.0 遺留設定的清理：0.9.0 以 WSL mirrored 網路＋Hyper-V 防火牆規則實作，
// mirrored 模式下容器埠直接佔用 Windows 的埠，轉發器無法監聽，因此需先還原為 NAT（Manager.RestoreNAT）。
package lan

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"time"
)

// ModeMirrored 是 .wslconfig networkingMode 的 mirrored 值。
const ModeMirrored = "mirrored"

// RuleNames 是 wsld 0.9.0 建立的 Hyper-V 防火牆規則名稱。
var RuleNames = []string{"wsld-lan-tcp", "wsld-lan-udp"}

// Host 抽象 Windows 端的副作用（真實實作在 host_windows.go；測試用 FakeHost）。
type Host interface {
	// ConfigPath 回傳 %USERPROFILE%\.wslconfig。
	ConfigPath() (string, error)
	// RemoveFirewallRules 以系統管理員權限（UAC）移除 wsld 的 Hyper-V 防火牆規則。
	RemoveFirewallRules(ctx context.Context) error
	// FirewallRules 回傳仍存在的 wsld 規則數；-1 表示無法查詢（不需提權）。
	FirewallRules(ctx context.Context) int
	// Interfaces 列出網卡與其 IPv4 位址。
	Interfaces() ([]Iface, error)
	// PortInUse 回報 Windows 上是否仍有任何 TCP 連線／監聽佔用此本機埠（含 TIME_WAIT）。
	PortInUse(ctx context.Context, port int) (bool, error)
}

// Iface 是一張網卡的名稱與位址。
type Iface struct {
	Name  string
	Up    bool
	Addrs []net.IP
}

// Legacy 描述 0.9.0 mirrored 模式的遺留設定。
type Legacy struct {
	// Mirrored 表示 .wslconfig 的 networkingMode=mirrored（轉發器在此模式下無法使用）。
	Mirrored bool `json:"mirrored"`
	// Rules 是仍存在的 wsld 防火牆規則數（-1 = 未知）。
	Rules int `json:"rules"`
}

// Manager 處理遺留設定與 WSL 重啟時的埠等待。
type Manager struct {
	Host Host
	// Sleep 供 WaitPortFree 等待（測試可替換；nil = time.Sleep）。
	Sleep func(time.Duration)
}

// Legacy 讀取 .wslconfig 與防火牆規則。
func (m *Manager) Legacy(ctx context.Context) (Legacy, error) {
	content, err := m.readConfig()
	if err != nil {
		return Legacy{Rules: -1}, err
	}
	l := Legacy{Mirrored: strings.EqualFold(GetKey(content, "wsl2", "networkingMode"), ModeMirrored), Rules: 0}
	if l.Mirrored {
		l.Rules = m.Host.FirewallRules(ctx)
	}
	return l, nil
}

// RestoreNAT 移除 wsld 的防火牆規則（存在或無法確認時需 UAC），再從 .wslconfig 移除 networkingMode。
// 先處理防火牆：使用者取消 UAC 時不改 .wslconfig。生效需要 wsl --shutdown（由呼叫端決定）。
func (m *Manager) RestoreNAT(ctx context.Context) error {
	if m.Host.FirewallRules(ctx) != 0 {
		if err := m.Host.RemoveFirewallRules(ctx); err != nil {
			return fmt.Errorf("firewall: %w", err)
		}
	}
	return m.editConfig(func(c string) string { return RemoveKey(c, "wsl2", "networkingMode") })
}

// PortWaitTimeout 是 wsl --shutdown 後等 Windows 釋放 API 埠的上限（TIME_WAIT 預設 120 秒）。
const PortWaitTimeout = 150 * time.Second

// WaitPortFree 等到 Windows 上沒有連線佔用 port（例如網路模式切換後留下的 TIME_WAIT）。
// 埠未釋放前 dockerd／WSL localhost 轉發可能綁定失敗（address already in use）。
// 回傳 true 表示已空出；逾時或無法查詢回傳 false（呼叫端照樣嘗試啟動）。
func (m *Manager) WaitPortFree(ctx context.Context, port int, timeout time.Duration) bool {
	if port <= 0 {
		return true
	}
	sleep := m.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	const step = 2 * time.Second
	for waited := time.Duration(0); ; waited += step {
		busy, err := m.Host.PortInUse(ctx, port)
		if err != nil {
			return false
		}
		if !busy {
			return true
		}
		if waited >= timeout || ctx.Err() != nil {
			return false
		}
		sleep(step)
	}
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
