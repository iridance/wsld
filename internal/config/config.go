// Package config 讀寫 %APPDATA%\wsld\config.toml（SPEC §5.2）。
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

const (
	DefaultPort               = 23750
	DefaultRefreshIntervalSec = 3
	DefaultAPITimeoutSec      = 10
	DefaultTheme              = "system"
	appDirName                = "wsld"
	legacyAppDirName          = "wsl-docker" // 更名前的設定目錄
	fileName                  = "config.toml"
	// EnvOverride 可指定 config 路徑（測試／多環境）。
	EnvOverride = "WSLD_CONFIG"
)

// Machine 是單一 WSL distro 的 profile。
type Machine struct {
	Distro      string `toml:"distro"`
	User        string `toml:"user"`
	APIEndpoint string `toml:"api_endpoint"`
}

// Ready 表示已 init（有 user 與合法 endpoint）。
func (m Machine) Ready() bool {
	if m.Distro == "" || m.User == "" || m.APIEndpoint == "" {
		return false
	}
	_, _, err := splitEndpoint(m.APIEndpoint)
	return err == nil
}

// Config 對應 config.toml（0.5：多 machine＋單一 active）。
type Config struct {
	Active             string    `toml:"active"`
	Machines           []Machine `toml:"machines"`
	ExecShell          string    `toml:"exec_shell,omitempty"`
	RefreshIntervalSec int       `toml:"refresh_interval_sec"`
	TrayAutostart      bool      `toml:"tray_autostart"`
	APITimeoutSec      int       `toml:"api_timeout_sec"`
	Lang               string    `toml:"lang,omitempty"`  // "" = en；GUI 右下角切換後寫回
	Theme              string    `toml:"theme,omitempty"` // light | dark | system；"" = system

	// 自 active machine 衍生，不寫入檔案（供 lifecycle／doctor 沿用欄位存取）。
	Distro      string `toml:"-"`
	User        string `toml:"-"`
	APIEndpoint string `toml:"-"`
}

// fileShape 同時接受舊扁平與新多 profile 格式。
type fileShape struct {
	Active             string    `toml:"active"`
	Machines           []Machine `toml:"machines"`
	ExecShell          string    `toml:"exec_shell"`
	RefreshIntervalSec int       `toml:"refresh_interval_sec"`
	TrayAutostart      bool      `toml:"tray_autostart"`
	APITimeoutSec      int       `toml:"api_timeout_sec"`
	Lang               string    `toml:"lang"`
	Theme              string    `toml:"theme"`
	// Legacy flat keys
	Distro      string `toml:"distro"`
	User        string `toml:"user"`
	APIEndpoint string `toml:"api_endpoint"`
}

// LangOrDefault 回傳語系，空值為 "en"。
func (c Config) LangOrDefault() string {
	if c.Lang == "" {
		return "en"
	}
	return c.Lang
}

// ThemeOrDefault 回傳 theme，空值為 "system"。
func (c Config) ThemeOrDefault() string {
	if c.Theme == "" {
		return DefaultTheme
	}
	return c.Theme
}

// Default 回傳僅含預設值的設定（active／machines 為空，需由 init／use 填入）。
func Default() Config {
	return Config{
		APIEndpoint:        EndpointForPort(DefaultPort),
		RefreshIntervalSec: DefaultRefreshIntervalSec,
		APITimeoutSec:      DefaultAPITimeoutSec,
	}
}

// EndpointForPort 產生 loopback tcp endpoint。
func EndpointForPort(port int) string {
	return fmt.Sprintf("tcp://127.0.0.1:%d", port)
}

var errNotFound = errors.New("config not found")

// IsNotFound 判斷 Load 是否因檔案不存在失敗。
func IsNotFound(err error) bool { return errors.Is(err, errNotFound) }

// Load 讀取並驗證設定；舊扁平格式自動遷移為 active＋machines。
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%w: %s", errNotFound, path)
		}
		return Config{}, err
	}
	var raw fileShape
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	c := Default()
	c.Active = raw.Active
	c.Machines = raw.Machines
	c.ExecShell = raw.ExecShell
	c.TrayAutostart = raw.TrayAutostart
	c.Lang = raw.Lang
	c.Theme = raw.Theme
	if raw.RefreshIntervalSec != 0 {
		c.RefreshIntervalSec = raw.RefreshIntervalSec
	}
	if raw.APITimeoutSec != 0 {
		c.APITimeoutSec = raw.APITimeoutSec
	}
	if len(c.Machines) == 0 && raw.Distro != "" {
		ep := raw.APIEndpoint
		if ep == "" {
			ep = EndpointForPort(DefaultPort)
		}
		c.Machines = []Machine{{
			Distro:      raw.Distro,
			User:        raw.User,
			APIEndpoint: ep,
		}}
		c.Active = raw.Distro
	}
	c.syncDerived()
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// saveShape 只含 0.5 欄位（不寫舊扁平鍵）。
type saveShape struct {
	Active             string    `toml:"active"`
	Machines           []Machine `toml:"machines"`
	ExecShell          string    `toml:"exec_shell,omitempty"`
	RefreshIntervalSec int       `toml:"refresh_interval_sec"`
	TrayAutostart      bool      `toml:"tray_autostart"`
	APITimeoutSec      int       `toml:"api_timeout_sec"`
	Lang               string    `toml:"lang,omitempty"`
	Theme              string    `toml:"theme,omitempty"`
}

// Save 驗證後寫入新格式（會建立目錄）。
func Save(path string, c Config) error {
	c.normalizeFromDerived()
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out := saveShape{
		Active:             c.Active,
		Machines:           c.Machines,
		ExecShell:          c.ExecShell,
		RefreshIntervalSec: c.RefreshIntervalSec,
		TrayAutostart:      c.TrayAutostart,
		APITimeoutSec:      c.APITimeoutSec,
		Lang:               c.Lang,
		Theme:              c.Theme,
	}
	data, err := toml.Marshal(out)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UpsertMachine 新增或覆寫同名 distro profile。
func (c *Config) UpsertMachine(m Machine) {
	for i := range c.Machines {
		if strings.EqualFold(c.Machines[i].Distro, m.Distro) {
			c.Machines[i] = m
			c.syncDerived()
			return
		}
	}
	c.Machines = append(c.Machines, m)
	c.syncDerived()
}

// RemoveMachine 刪除 profile；若刪的是 active 則清空 active（呼叫端可另選）。
func (c *Config) RemoveMachine(distro string) bool {
	out := c.Machines[:0]
	found := false
	for _, m := range c.Machines {
		if strings.EqualFold(m.Distro, distro) {
			found = true
			continue
		}
		out = append(out, m)
	}
	if !found {
		return false
	}
	c.Machines = out
	if strings.EqualFold(c.Active, distro) {
		c.Active = ""
		c.Distro, c.User, c.APIEndpoint = "", "", ""
		if len(c.Machines) > 0 {
			_ = c.SetActive(c.Machines[0].Distro)
		}
	} else {
		c.syncDerived()
	}
	return true
}

// SetActive 切換 active；distro 必須已在 machines 中。
func (c *Config) SetActive(distro string) error {
	for _, m := range c.Machines {
		if strings.EqualFold(m.Distro, distro) {
			c.Active = m.Distro
			c.syncDerived()
			return nil
		}
	}
	return fmt.Errorf("machine %q not found", distro)
}

// EnsureActive 寫入／更新 profile 並設為 active（use 占位或 init 補齊）。
func (c *Config) EnsureActive(m Machine) {
	c.UpsertMachine(m)
	_ = c.SetActive(m.Distro)
}

// ActiveMachine 回傳目前 active profile。
func (c Config) ActiveMachine() (Machine, bool) {
	if c.Active == "" {
		return Machine{}, false
	}
	for _, m := range c.Machines {
		if strings.EqualFold(m.Distro, c.Active) {
			return m, true
		}
	}
	return Machine{}, false
}

// FindMachine 依名稱尋找 profile。
func (c Config) FindMachine(distro string) (Machine, bool) {
	for _, m := range c.Machines {
		if strings.EqualFold(m.Distro, distro) {
			return m, true
		}
	}
	return Machine{}, false
}

func (c *Config) syncDerived() {
	if m, ok := c.ActiveMachine(); ok {
		c.Distro = m.Distro
		c.User = m.User
		c.APIEndpoint = m.APIEndpoint
		return
	}
	if c.Active == "" && c.Distro == "" {
		return
	}
}

// normalizeFromDerived：測試／舊呼叫端只填 Distro／User／APIEndpoint 時，補成 machines。
func (c *Config) normalizeFromDerived() {
	if len(c.Machines) == 0 && c.Distro != "" {
		ep := c.APIEndpoint
		if ep == "" {
			ep = EndpointForPort(DefaultPort)
		}
		c.Machines = []Machine{{Distro: c.Distro, User: c.User, APIEndpoint: ep}}
		c.Active = c.Distro
	}
	c.syncDerived()
}

// Validate 檢查 active／machines 與 loopback 限制（占位 machine 允許空 user／endpoint）。
func (c Config) Validate() error {
	if len(c.Machines) == 0 {
		if c.Active != "" {
			return errors.New("active set but no machines")
		}
		if c.Distro != "" {
			tmp := c
			tmp.normalizeFromDerived()
			return tmp.Validate()
		}
		// 空設定（全部 unmanage 後）仍可儲存。
		return c.validateGlobals()
	}
	if c.Active == "" {
		return errors.New("active is required")
	}
	if _, ok := c.ActiveMachine(); !ok {
		return fmt.Errorf("active %q not in machines", c.Active)
	}
	seen := map[string]bool{}
	for _, m := range c.Machines {
		if m.Distro == "" {
			return errors.New("machine distro is required")
		}
		key := strings.ToLower(m.Distro)
		if seen[key] {
			return fmt.Errorf("duplicate machine %q", m.Distro)
		}
		seen[key] = true
		if m.APIEndpoint != "" {
			host, _, err := splitEndpoint(m.APIEndpoint)
			if err != nil {
				return err
			}
			if !isLoopbackHost(host) {
				return fmt.Errorf("api_endpoint host %q is not loopback (only 127.0.0.1/localhost/::1 allowed)", host)
			}
		}
	}
	return c.validateGlobals()
}

func (c Config) validateGlobals() error {
	if c.RefreshIntervalSec <= 0 {
		return errors.New("refresh_interval_sec must be > 0")
	}
	if c.APITimeoutSec <= 0 {
		return errors.New("api_timeout_sec must be > 0")
	}
	switch c.ThemeOrDefault() {
	case "light", "dark", "system":
	default:
		return fmt.Errorf("theme must be light, dark, or system, got %q", c.Theme)
	}
	return nil
}

// APIPort 解析 active 的 api_endpoint，並要求 tcp + loopback host。
func (c Config) APIPort() (int, error) {
	ep := c.APIEndpoint
	if ep == "" {
		if m, ok := c.ActiveMachine(); ok {
			ep = m.APIEndpoint
		}
	}
	host, port, err := splitEndpoint(ep)
	if err != nil {
		return 0, err
	}
	if !isLoopbackHost(host) {
		return 0, fmt.Errorf("api_endpoint host %q is not loopback (only 127.0.0.1/localhost/::1 allowed)", host)
	}
	return port, nil
}

// HTTPBaseURL 把 tcp:// endpoint 轉為 http:// base。
func (c Config) HTTPBaseURL() string {
	host, port, err := splitEndpoint(c.APIEndpoint)
	if err != nil {
		return ""
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func splitEndpoint(ep string) (host string, port int, err error) {
	u, err := url.Parse(ep)
	if err != nil {
		return "", 0, fmt.Errorf("api_endpoint: %w", err)
	}
	if u.Scheme != "tcp" {
		return "", 0, fmt.Errorf("api_endpoint must use tcp:// scheme, got %q", u.Scheme)
	}
	host = u.Hostname()
	if host == "" {
		return "", 0, errors.New("api_endpoint missing host")
	}
	p := u.Port()
	if p == "" {
		return "", 0, errors.New("api_endpoint missing port")
	}
	port, err = strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("api_endpoint invalid port %q", p)
	}
	return host, port, nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// ExecCommand 回傳容器內要執行的命令：explicit 優先 → exec_shell → bash→sh 自動退回（SPEC §2 Terminal）。
func (c Config) ExecCommand(explicit []string) []string {
	if len(explicit) > 0 {
		return explicit
	}
	if c.ExecShell != "" {
		return []string{c.ExecShell}
	}
	return []string{"sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash || exec sh"}
}

// DefaultPath 回傳目前 OS 的預設設定檔路徑。
func DefaultPath() string { return DefaultPathFor(runtime.GOOS, os.Getenv) }

// DefaultPathFor 便於測試：goos + env 查詢函式。
func DefaultPathFor(goos string, getenv func(string) string) string {
	if p := getenv(EnvOverride); p != "" {
		return p
	}
	return filepath.Join(configBaseDir(goos, getenv), appDirName, fileName)
}

// MigrateLegacyAppDir 若仍存在舊目錄 %APPDATA%\wsl-docker（或 XDG 同等路徑）
// 且新目錄尚未建立，則整目錄更名為 wsld。WSLD_CONFIG 覆寫時不動作。
func MigrateLegacyAppDir() error {
	return MigrateLegacyAppDirFor(runtime.GOOS, os.Getenv)
}

// MigrateLegacyAppDirFor 便於測試。
func MigrateLegacyAppDirFor(goos string, getenv func(string) string) error {
	if getenv(EnvOverride) != "" {
		return nil
	}
	base := configBaseDir(goos, getenv)
	if base == "" {
		return nil
	}
	oldDir := filepath.Join(base, legacyAppDirName)
	newDir := filepath.Join(base, appDirName)
	if _, err := os.Stat(newDir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(oldDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.Rename(oldDir, newDir)
}

func configBaseDir(goos string, getenv func(string) string) string {
	if goos == "windows" {
		base := getenv("APPDATA")
		if base == "" {
			base = filepath.Join(getenv("USERPROFILE"), "AppData", "Roaming")
		}
		return base
	}
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(getenv("HOME"), ".config")
	}
	return base
}
