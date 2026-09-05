// Package config 讀寫 %APPDATA%\wsl-docker\config.toml（SPEC §5.2）。
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

	"github.com/BurntSushi/toml"
)

const (
	DefaultPort               = 23750
	DefaultRefreshIntervalSec = 3
	DefaultAPITimeoutSec      = 10
	appDirName                = "wsl-docker"
	fileName                  = "config.toml"
	// EnvOverride 可指定 config 路徑（測試／多環境）。
	EnvOverride = "WSLD_CONFIG"
)

// Config 對應 config.toml。
type Config struct {
	Distro             string `toml:"distro"`
	User               string `toml:"user"`
	APIEndpoint        string `toml:"api_endpoint"`
	ExecShell          string `toml:"exec_shell,omitempty"`
	RefreshIntervalSec int    `toml:"refresh_interval_sec"`
	TrayAutostart      bool   `toml:"tray_autostart"`
	APITimeoutSec      int    `toml:"api_timeout_sec"`
}

// Default 回傳僅含預設值的設定（distro／user 為空，需由 init 填入）。
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

// Load 讀取並驗證設定；缺少的可選欄位補預設值。
func Load(path string) (Config, error) {
	c := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("%w: %s", errNotFound, path)
		}
		return Config{}, err
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.RefreshIntervalSec == 0 {
		c.RefreshIntervalSec = DefaultRefreshIntervalSec
	}
	if c.APITimeoutSec == 0 {
		c.APITimeoutSec = DefaultAPITimeoutSec
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Save 驗證後寫入（會建立目錄）。
func Save(path string, c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Validate 檢查必填欄位與 loopback 限制（SPEC §4.2 禁止非 loopback）。
func (c Config) Validate() error {
	if c.Distro == "" {
		return errors.New("distro is required")
	}
	if c.User == "" {
		return errors.New("user is required")
	}
	if _, err := c.APIPort(); err != nil {
		return err
	}
	if c.RefreshIntervalSec <= 0 {
		return errors.New("refresh_interval_sec must be > 0")
	}
	if c.APITimeoutSec <= 0 {
		return errors.New("api_timeout_sec must be > 0")
	}
	return nil
}

// APIPort 解析 api_endpoint，並要求 tcp + loopback host。
func (c Config) APIPort() (int, error) {
	host, port, err := splitEndpoint(c.APIEndpoint)
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
	var base string
	if goos == "windows" {
		base = getenv("APPDATA")
		if base == "" {
			base = filepath.Join(getenv("USERPROFILE"), "AppData", "Roaming")
		}
	} else {
		base = getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(getenv("HOME"), ".config")
		}
	}
	return filepath.Join(base, appDirName, fileName)
}
