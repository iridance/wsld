package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/irice/wsl-docker/internal/wsl"
)

// FakeWSL 模擬 wsl.exe：支援 --list --verbose／--list --online／--install／cat /etc/wsl.conf。
// 供 gui 測試與 wsld-gui-testhost 的 Machines 視圖使用。
type FakeWSL struct {
	mu          sync.Mutex
	distros     []wsl.Distro
	confs       map[string]string // distro → /etc/wsl.conf 內容（缺 = 檔案不存在）
	online      []wsl.OnlineDistro
	installed   []string
	FailInstall bool
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
}

// Installed 回傳已呼叫 --install 的名稱。
func (f *FakeWSL) Installed() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.installed...)
}

// Run 實作 wsl.Runner。輸出以 UTF-8 回傳（DecodeOutput 可直接處理）。
func (f *FakeWSL) Run(_ context.Context, _ io.Reader, args ...string) ([]byte, []byte, error) {
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
	case len(args) == 4 && args[0] == "--install" && args[1] == "-d":
		if f.FailInstall {
			return nil, []byte("Installation failed"), &wsl.ExitError{Code: 1, Stderr: "Installation failed"}
		}
		f.installed = append(f.installed, args[2])
		f.distros = append(f.distros, wsl.Distro{Name: args[2], State: "Stopped", Version: 2})
		return []byte("Installing: " + args[2] + "\r\n"), nil, nil
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
		if strings.HasSuffix(joined, "-- cat /etc/wsl.conf") {
			conf, ok := f.confs[name]
			if !ok {
				return nil, []byte("cat: /etc/wsl.conf: No such file or directory"), &wsl.ExitError{Code: 1}
			}
			return []byte(conf), nil, nil
		}
		return nil, nil, nil
	}
	return nil, nil, errors.New("fake wsl: unsupported args " + joined)
}
