package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/iridance/wsld/internal/wsl"
)

// FakeWSL 模擬 wsl.exe：支援 --list --verbose／--list --online／--install／cat /etc/wsl.conf／/proc。
// 供 gui 測試與 wsld-gui-testhost 的 Machines 視圖使用。
type FakeWSL struct {
	mu          sync.Mutex
	distros     []wsl.Distro
	confs       map[string]string // distro → /etc/wsl.conf 內容（缺 = 檔案不存在）
	online      []wsl.OnlineDistro
	installed   []string
	FailInstall bool
	// InstallHold 非 nil 時 --install 會等到 channel 關閉（測試進行中狀態）。
	InstallHold chan struct{}
	procStatN   map[string]int               // distro → /proc/stat 讀取次數（交替兩組假資料）
	users       map[string]map[string]bool   // distro → useradd 建立的使用者
	passwords   map[string]map[string]string // distro → user → chpasswd 設定的密碼
	shutdowns   int
}

// Shutdowns 回傳 wsl --shutdown 的次數。
func (f *FakeWSL) Shutdowns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shutdowns
}

// NewFakeWSL 建立含 Ubuntu-26.04（Running、預設、systemd）與 docker-desktop（Stopped）的假環境。
func NewFakeWSL() *FakeWSL {
	f := &FakeWSL{}
	f.Reset()
	return f
}

// Reset 回到初始狀態。
func (f *FakeWSL) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns = 0
	f.distros = []wsl.Distro{
		{Name: "Ubuntu-26.04", State: "Running", Version: 2, Default: true},
		{Name: "docker-desktop", State: "Stopped", Version: 2},
	}
	f.confs = map[string]string{
		"Ubuntu-26.04": "[boot]\nsystemd=true\n\n[user]\ndefault=irice\n",
	}
	f.online = []wsl.OnlineDistro{
		{Name: "Ubuntu", Friendly: "Ubuntu"},
		{Name: "Debian", Friendly: "Debian GNU/Linux"},
		{Name: "Ubuntu-24.04", Friendly: "Ubuntu 24.04 LTS"},
	}
	f.installed = nil
	f.FailInstall = false
	f.procStatN = map[string]int{}
	f.users = map[string]map[string]bool{}
	f.passwords = map[string]map[string]string{}
}

// Password 回傳 chpasswd 為 distro 內 user 設定的密碼（測試用）。
func (f *FakeWSL) Password(distro, user string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pw, ok := f.passwords[distro][user]
	return pw, ok
}

// Conf 回傳 distro 的 /etc/wsl.conf 內容（測試用）。
func (f *FakeWSL) Conf(distro string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.confs[distro]
}

// Installed 回傳已呼叫 --install 的名稱。
func (f *FakeWSL) Installed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.installed...)
}

// Run 實作 wsl.Runner。輸出以 UTF-8 回傳（DecodeOutput 可直接處理）。
func (f *FakeWSL) Run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	if len(args) > 0 && args[0] == "--install" {
		f.mu.Lock()
		hold := f.InstallHold
		f.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
	}
	if len(args) == 1 && args[0] == "--shutdown" {
		f.mu.Lock()
		for i := range f.distros {
			f.distros[i].State = "Stopped"
		}
		f.shutdowns++
		f.mu.Unlock()
		return nil, nil, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(args, " ")
	switch {
	case joined == "--list --verbose":
		var b strings.Builder
		b.WriteString("  NAME              STATE           VERSION\r\n")
		for _, d := range f.distros {
			mark := "  "
			if d.Default {
				mark = "* "
			}
			fmt.Fprintf(&b, "%s%-18s%-16s%d\r\n", mark, d.Name, d.State, d.Version)
		}
		return []byte(b.String()), nil, nil
	case joined == "--list --online":
		var b strings.Builder
		b.WriteString("The following is a list of valid distributions that can be installed.\r\n\r\nNAME                            FRIENDLY NAME\r\n")
		for _, d := range f.online {
			fmt.Fprintf(&b, "%-32s%s\r\n", d.Name, d.Friendly)
		}
		return []byte(b.String()), nil, nil
	case args[0] == "--install":
		distro, local := "", ""
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "-d", "--distribution":
				if i+1 < len(args) {
					i++
					distro = args[i]
				}
			case "--name":
				if i+1 < len(args) {
					i++
					local = args[i]
				}
			}
		}
		if distro == "" {
			return nil, []byte("missing distribution"), &wsl.ExitError{Code: 1}
		}
		if local == "" {
			local = distro
		}
		if f.FailInstall {
			return nil, []byte("Installation failed"), &wsl.ExitError{Code: 1, Stderr: "Installation failed"}
		}
		f.installed = append(f.installed, local)
		f.distros = append(f.distros, wsl.Distro{Name: local, State: "Stopped", Version: 2})
		return []byte("Installing: " + local + "\r\n"), nil, nil
	case len(args) == 2 && args[0] == "--unregister":
		name := args[1]
		out := f.distros[:0]
		found := false
		for _, d := range f.distros {
			if strings.EqualFold(d.Name, name) {
				found = true
				continue
			}
			out = append(out, d)
		}
		if !found {
			return nil, []byte("There is no distribution with the supplied name."), &wsl.ExitError{Code: 1}
		}
		f.distros = out
		delete(f.confs, name)
		return nil, nil, nil
	case len(args) == 2 && args[0] == "--terminate":
		for i := range f.distros {
			if strings.EqualFold(f.distros[i].Name, args[1]) {
				f.distros[i].State = "Stopped"
			}
		}
		return nil, nil, nil
	case len(args) >= 2 && args[0] == "-d":
		name := args[1]
		found := false
		for _, d := range f.distros {
			if strings.EqualFold(d.Name, name) {
				found = true
			}
		}
		if !found {
			return nil, []byte("There is no distribution with the supplied name."), &wsl.ExitError{Code: 1}
		}
		if strings.HasSuffix(joined, "-- true") {
			// wsl.Wake：真實 wsl.exe 執行任何命令都會把 distro 開起來
			for i := range f.distros {
				if strings.EqualFold(f.distros[i].Name, name) {
					f.distros[i].State = "Running"
				}
			}
			return nil, nil, nil
		}
		if strings.HasSuffix(joined, "-- cat /etc/wsl.conf") {
			conf, ok := f.confs[name]
			if !ok {
				return nil, []byte("cat: /etc/wsl.conf: No such file or directory"), &wsl.ExitError{Code: 1}
			}
			return []byte(conf), nil, nil
		}
		if strings.HasSuffix(joined, "-- cat /proc/stat") {
			n := f.procStatN[name]
			f.procStatN[name] = n + 1
			// 奇偶交替：idleΔ/totalΔ → ~80% 與 ~20%，便於測試非寫死常數
			if n%2 == 0 {
				return []byte("cpu  100 0 50 850 0 0 0 0 0 0\n"), nil, nil
			}
			return []byte("cpu  160 0 70 870 0 0 0 0 0 0\n"), nil, nil
		}
		if strings.HasSuffix(joined, "-- cat /proc/meminfo") {
			return []byte("MemTotal:       8388608 kB\nMemAvailable:   6291456 kB\n"), nil, nil
		}
		if strings.HasSuffix(joined, "-- test -e /etc/wsl.conf") {
			if _, ok := f.confs[name]; !ok {
				return nil, nil, &wsl.ExitError{Code: 1}
			}
			return nil, nil, nil
		}
		if strings.HasSuffix(joined, "-- sh -c cat > /etc/wsl.conf") && stdin != nil {
			b, _ := io.ReadAll(stdin)
			f.confs[name] = string(b)
			return nil, nil, nil
		}
		if i := strings.Index(joined, "-- id -u "); i >= 0 {
			u := joined[i+len("-- id -u "):]
			if !f.users[name][u] {
				return nil, []byte("id: '" + u + "': no such user"), &wsl.ExitError{Code: 1}
			}
			return []byte("1000\n"), nil, nil
		}
		if i := strings.Index(joined, "-- useradd -m -s /bin/bash "); i >= 0 {
			if f.users[name] == nil {
				f.users[name] = map[string]bool{}
			}
			f.users[name][joined[i+len("-- useradd -m -s /bin/bash "):]] = true
			return nil, nil, nil
		}
		if strings.HasSuffix(joined, "-- chpasswd") && stdin != nil {
			b, _ := io.ReadAll(stdin)
			u, pw, _ := strings.Cut(strings.TrimRight(string(b), "\n"), ":")
			if f.passwords[name] == nil {
				f.passwords[name] = map[string]string{}
			}
			f.passwords[name][u] = pw
			return nil, nil, nil
		}
		// systemctl stop docker 等 best-effort 命令
		return nil, nil, nil
	}
	return nil, nil, errors.New("fake wsl: unsupported args " + joined)
}
