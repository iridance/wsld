package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/iridance/wsld/internal/wsl"
)

// FakeWSL 模擬 wsl.exe：支援 --list --verbose／--list --online／--install／--manage --resize／cat /etc/wsl.conf／/proc。
// 供 gui 測試與 wsld-gui-testhost 的 Machines 視圖使用。
type FakeWSL struct {
	mu          sync.Mutex
	distros     []wsl.Distro
	confs       map[string]string // distro → /etc/wsl.conf 內容（缺 = 檔案不存在）
	online      []wsl.OnlineDistro
	installed   []string
	FailInstall bool
	// FailResize 為 true 時 --manage --resize 失敗（模擬縮小到放不下資料）。
	FailResize  bool
	limits      map[string]int64 // distro → 虛擬磁碟上限（缺 = WSL 預設 1 TB）
	lastInstall string
	resizes     []string // "<distro> <size> <state> <attached|detached>"
	// attached：VHD 仍掛在 WSL VM 上的 distro。啟動即掛上；--terminate 不會卸下（與實機相同），只有 --shutdown 會。
	attached map[string]bool
	calls    []string
	// fsMinBlocks：根檔案系統能縮到的最小 4 KiB 區塊數（resize2fs -P；缺 = 2 GiB）
	fsMinBlocks map[string]int64
	sizes       map[string]int64 // vhdx 檔案大小覆寫（--compact 後）
	compacts    []string         // "<distro> <attached|detached>"
	// InstallHold 非 nil 時 --install 會等到 channel 關閉（測試進行中狀態）。
	InstallHold chan struct{}
	procStatN   map[string]int               // distro → /proc/stat 讀取次數（交替兩組假資料）
	users       map[string]map[string]bool   // distro → useradd 建立的使用者
	passwords   map[string]map[string]string // distro → user → chpasswd 設定的密碼
	shutdowns   int
	// WakeExempt 非 nil 且回傳 true 時，該次 -d 命令不把 distro 開起來（測試明確豁免用）。
	// 預設（nil）與實機相同：任何 wsl -d <name> 命令都會喚醒 distro。
	WakeExempt func(args []string) bool
}

// Attached 回傳 distro 的 VHD 是否掛在 WSL VM 上（測試用）。
func (f *FakeWSL) Attached(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attached[name]
}

// SetConf 設定 distro 的 /etc/wsl.conf 內容（測試用）。
func (f *FakeWSL) SetConf(distro, conf string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confs[distro] = conf
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
	f.FailResize = false
	f.limits = map[string]int64{}
	f.lastInstall = ""
	f.resizes = nil
	f.attached = map[string]bool{"Ubuntu-26.04": true}
	f.calls = nil
	f.fsMinBlocks = map[string]int64{}
	f.sizes = map[string]int64{}
	f.compacts = nil
	f.WakeExempt = nil
	f.procStatN = map[string]int{}
	f.users = map[string]map[string]bool{}
	f.passwords = map[string]map[string]string{}
}

// VHDXSize 模擬 wsl.VHDXSize：Ubuntu-26.04 8.5 GB、docker-desktop 1.2 GB、其他（新安裝）1.5 GB。
func (f *FakeWSL) VHDXSize(name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.distros {
		if !strings.EqualFold(d.Name, name) {
			continue
		}
		if n, ok := f.sizes[d.Name]; ok {
			return n, nil
		}
		switch d.Name {
		case "Ubuntu-26.04":
			return 8_500_000_000, nil
		case "docker-desktop":
			return 1_200_000_000, nil
		}
		return 1_500_000_000, nil
	}
	return 0, errors.New("fake wsl: no distro " + name)
}

// VHDXLimit 模擬 wsl.VHDXLimit：預設 1 TB，--install --vhd-size／--manage --resize 會改。
func (f *FakeWSL) VHDXLimit(name string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.distros {
		if strings.EqualFold(d.Name, name) {
			if n, ok := f.limits[d.Name]; ok {
				return n, nil
			}
			return 1 << 40, nil
		}
	}
	return 0, errors.New("fake wsl: no distro " + name)
}

// LastInstallArgs 回傳最後一次 --install 的完整參數（測試用）。
func (f *FakeWSL) LastInstallArgs() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastInstall
}

// CompactCalls 回傳每次 --manage --compact 的「distro 掛載狀態」（測試用）。
func (f *FakeWSL) CompactCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.compacts...)
}

// SetFSMinBlocks 設定 distro 根檔案系統能縮到的最小區塊數（測試用）。
func (f *FakeWSL) SetFSMinBlocks(distro string, blocks int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fsMinBlocks[distro] = blocks
}

func (f *FakeWSL) minBlocks(distro string) int64 {
	if n, ok := f.fsMinBlocks[distro]; ok {
		return n
	}
	return 2 << 30 / 4096
}

// Ran 回傳是否有以 prefix 開頭的 wsl.exe 呼叫（測試用）。
func (f *FakeWSL) Ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// ResizeCalls 回傳每次 --manage --resize 的「distro 大小 呼叫當下狀態」（測試用）。
func (f *FakeWSL) ResizeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.resizes...)
}

// parseGB 解析 "60GB" 之類的 MemoryString（只支援 GB，與 wsld 送出的格式一致）。
func parseGB(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSuffix(strings.ToUpper(s), "GB"), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n << 30, true
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
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.mu.Unlock()
	if len(args) == 1 && args[0] == "--shutdown" {
		f.mu.Lock()
		for i := range f.distros {
			f.distros[i].State = "Stopped"
		}
		f.attached = map[string]bool{}
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
		f.lastInstall = joined
		distro, local := "", ""
		var limit int64
		for i := 1; i < len(args); i++ {
			switch args[i] {
			case "--vhd-size":
				if i+1 < len(args) {
					i++
					n, ok := parseGB(args[i])
					if !ok {
						return nil, []byte("invalid --vhd-size"), &wsl.ExitError{Code: 1}
					}
					limit = n
				}
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
		if limit > 0 {
			f.limits[local] = limit
		}
		return []byte("Installing: " + local + "\r\n"), nil, nil
	case len(args) == 4 && args[0] == "--manage" && args[2] == "--resize":
		idx := -1
		for i := range f.distros {
			if strings.EqualFold(f.distros[i].Name, args[1]) {
				idx = i
			}
		}
		if idx < 0 {
			return nil, []byte("There is no distribution with the supplied name."), &wsl.ExitError{Code: 1}
		}
		d := f.distros[idx]
		att := "detached"
		if f.attached[d.Name] {
			att = "attached"
		}
		f.resizes = append(f.resizes, d.Name+" "+args[3]+" "+strings.ToLower(d.State)+" "+att)
		if strings.EqualFold(d.State, "Running") || f.attached[d.Name] {
			// 與實機一致：訊息在 stdout
			return []byte("The operation could not be completed because the VHD is currently in use. To force WSL to stop use: wsl.exe --shutdown\r\nError code: Wsl/Service/WSL_E_DISTRO_NOT_STOPPED\r\n"), nil, &wsl.ExitError{Code: -1}
		}
		if f.FailResize {
			return nil, []byte("The disk is too small for the data."), &wsl.ExitError{Code: 1}
		}
		n, ok := parseGB(args[3])
		if !ok {
			return nil, []byte("invalid size"), &wsl.ExitError{Code: 1}
		}
		if min := f.minBlocks(d.Name); n < min*4096 {
			msg := fmt.Sprintf("e2fsck 1.47.0 (5-Feb-2023)\r\nPass 1: Checking inodes, blocks, and sizes\r\nresize2fs 1.47.0 (5-Feb-2023)\r\n/usr/sbin/resize2fs: New size smaller than minimum (%d)\r\nThe disk could not be resized.\r\nError code: Wsl/Service/E_FAIL\r\n", min)
			return []byte(msg), []byte(msg), &wsl.ExitError{Code: -1}
		}
		f.limits[d.Name] = n
		return nil, nil, nil
	case len(args) == 3 && args[0] == "--manage" && args[2] == "--compact":
		name := ""
		for _, d := range f.distros {
			if strings.EqualFold(d.Name, args[1]) {
				name = d.Name
			}
		}
		if name == "" {
			return nil, []byte("There is no distribution with the supplied name."), &wsl.ExitError{Code: 1}
		}
		att := "detached"
		if f.attached[name] {
			att = "attached"
		}
		f.compacts = append(f.compacts, name+" "+att)
		if f.attached[name] {
			return []byte("The operation could not be completed because the VHD is currently in use.\r\nError code: Wsl/Service/WSL_E_DISTRO_NOT_STOPPED\r\n"), nil, &wsl.ExitError{Code: -1}
		}
		// 壓縮：檔案大小減半，最小 1 GB
		f.mu.Unlock()
		cur, _ := f.VHDXSize(name)
		f.mu.Lock()
		next := cur / 2
		if next < 1_000_000_000 {
			next = 1_000_000_000
		}
		f.sizes[name] = next
		return nil, nil, nil
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
		delete(f.limits, name)
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
		idx := -1
		for i := range f.distros {
			if strings.EqualFold(f.distros[i].Name, name) {
				idx = i
			}
		}
		if idx < 0 {
			return nil, []byte("There is no distribution with the supplied name."), &wsl.ExitError{Code: 1}
		}
		// 與實機相同：wsl -d <name> 執行任何命令都會把停止中的 distro 開起來並掛上 VHD
		if f.WakeExempt == nil || !f.WakeExempt(args) {
			f.distros[idx].State = "Running"
			f.attached[f.distros[idx].Name] = true
		}
		if strings.HasSuffix(joined, "-- true") {
			return nil, nil, nil // wsl.Wake
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
		if strings.HasSuffix(joined, "-- df -B1 --output=used,size /") {
			return []byte("        Used     1B-blocks\n6000000000 1000000000000\n"), nil, nil
		}
		if strings.HasSuffix(joined, "-- findmnt -n -o SOURCE /") {
			return []byte("/dev/sdd\n"), nil, nil
		}
		if strings.HasSuffix(joined, "-- stat -f -c %S /") {
			return []byte("4096\n"), nil, nil
		}
		if strings.HasSuffix(joined, "-- resize2fs -P /dev/sdd") {
			return []byte(fmt.Sprintf("resize2fs 1.47.2 (1-Jan-2025)\nEstimated minimum size of the filesystem: %d\n", f.minBlocks(name))), nil, nil
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
