// Package lifecycle 負責 WSL distro／dockerd 的生命週期：status、start、init（SPEC §5.4）。
package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/irice/wsl-docker/internal/config"
	"github.com/irice/wsl-docker/internal/i18n"
	"github.com/irice/wsl-docker/internal/wsl"
)

var (
	// ErrNotReady：WSL／引擎未就緒（退出碼 3）。
	ErrNotReady = errors.New("not ready")
	// ErrAPI：Docker API 無法連線（退出碼 4）。
	ErrAPI = errors.New("api unreachable")
)

// Pinger 抽象 dockerapi.Client.Ping。
type Pinger interface {
	Ping(ctx context.Context) error
}

// Manager 綜合 wsl runner、設定與 API client。
type Manager struct {
	WSL    wsl.Runner
	Cfg    config.Config
	API    Pinger
	Bundle []byte    // scripts.BootstrapBundle()
	Output io.Writer // init 時串流 bootstrap 輸出；nil 則丟棄

	PingAttempts int                 // 預設 20
	PingInterval time.Duration       // 預設 500ms
	Sleep        func(time.Duration) // 測試可注入
}

// Status 是 wsld status 的資料模型。
type Status struct {
	Distro        string
	DistroFound   bool
	DistroRunning bool
	DistroVersion int
	SystemdActive bool
	DockerActive  bool
	APIEndpoint   string
	APIReachable  bool
	APIError      string
}

// Ready 表示整體可用。
func (s Status) Ready() bool {
	return s.DistroFound && s.SystemdActive && s.DockerActive && s.APIReachable
}

func (m *Manager) sleep(d time.Duration) {
	if m.Sleep != nil {
		m.Sleep(d)
		return
	}
	time.Sleep(d)
}

func (m *Manager) pingAttempts() int {
	if m.PingAttempts > 0 {
		return m.PingAttempts
	}
	return 20
}

func (m *Manager) pingInterval() time.Duration {
	if m.PingInterval > 0 {
		return m.PingInterval
	}
	return 500 * time.Millisecond
}

// Status 盡力收集狀態，不回傳錯誤（細節寫入欄位）。
func (m *Manager) Status(ctx context.Context) Status {
	st := Status{Distro: m.Cfg.Distro, APIEndpoint: m.Cfg.APIEndpoint}
	if list, err := wsl.ListDistros(ctx, m.WSL); err == nil {
		if d, ok := wsl.FindDistro(list, m.Cfg.Distro); ok {
			st.DistroFound = true
			st.DistroRunning = strings.EqualFold(d.State, "Running")
			st.DistroVersion = d.Version
		}
	}
	if st.DistroFound {
		st.SystemdActive = m.systemdActive(ctx)
		if st.SystemdActive {
			st.DockerActive = m.dockerActive(ctx)
		}
	}
	if m.API != nil {
		if err := m.API.Ping(ctx); err != nil {
			st.APIError = err.Error()
		} else {
			st.APIReachable = true
		}
	}
	return st
}

// systemctl is-system-running：running／degraded 都算可用（degraded 時 exit 1）。
func (m *Manager) systemdActive(ctx context.Context) bool {
	out, _, _ := wsl.RunIn(ctx, m.WSL, m.Cfg.Distro, "", nil, "systemctl", "is-system-running")
	s := strings.TrimSpace(string(out))
	return s == "running" || s == "degraded"
}

func (m *Manager) dockerActive(ctx context.Context) bool {
	out, _, _ := wsl.RunIn(ctx, m.WSL, m.Cfg.Distro, "", nil, "systemctl", "is-active", "docker")
	return strings.TrimSpace(string(out)) == "active"
}

// Start：喚醒 distro → 確認 systemd → 啟動 docker.service（root）→ 等 API。
func (m *Manager) Start(ctx context.Context) (Status, error) {
	list, err := wsl.ListDistros(ctx, m.WSL)
	if err != nil {
		return Status{}, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.wsl_unavailable", err))
	}
	d, ok := wsl.FindDistro(list, m.Cfg.Distro)
	if !ok {
		return Status{}, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.distro_missing", m.Cfg.Distro))
	}
	if d.Version != 2 {
		return Status{}, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.distro_wsl1", d.Name, d.Version, d.Name))
	}
	if err := wsl.Wake(ctx, m.WSL, m.Cfg.Distro); err != nil {
		return Status{}, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.wake_failed", m.Cfg.Distro, err))
	}
	if !m.systemdActive(ctx) {
		return Status{}, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.systemd_off", m.Cfg.Distro))
	}
	if !m.dockerActive(ctx) {
		if _, stderr, err := wsl.RunIn(ctx, m.WSL, m.Cfg.Distro, "root", nil, "systemctl", "start", "docker"); err != nil {
			return Status{}, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.docker_start_failed", err, strings.TrimSpace(string(stderr))))
		}
		// 等待 active
		for i := 0; i < m.pingAttempts(); i++ {
			if m.dockerActive(ctx) {
				break
			}
			m.sleep(m.pingInterval())
		}
	}
	var lastErr error
	for i := 0; i < m.pingAttempts(); i++ {
		if lastErr = m.API.Ping(ctx); lastErr == nil {
			break
		}
		m.sleep(m.pingInterval())
	}
	if lastErr != nil {
		return m.Status(ctx), fmt.Errorf("%w: %s", ErrAPI, i18n.T("lc.api_no_response", m.Cfg.APIEndpoint, lastErr))
	}
	return m.Status(ctx), nil
}

// InitOptions 為 wsld init 的參數；空值代表自動。
type InitOptions struct {
	Distro        string
	User          string
	Port          int
	NoCompose     bool
	InstallDistro string // 無 Ubuntu 時要安裝的名稱；空→"Ubuntu"
	SkipInstall   bool
}

var portRe = regexp.MustCompile(`(?m)^WSLD_API_PORT=(\d+)\s*$`)

// ParseBootstrapPort 從 bootstrap 輸出取出選到的埠。
func ParseBootstrapPort(out string) (int, bool) {
	m := portRe.FindStringSubmatch(out)
	if m == nil {
		return 0, false
	}
	p, err := strconv.Atoi(m[1])
	return p, err == nil
}

// Init：選 distro（沿用 Ubuntu／安裝）→ 偵測使用者 → 以 root 執行內嵌 bootstrap → 回傳新設定（呼叫端負責 Save）。
func (m *Manager) Init(ctx context.Context, opts InitOptions) (config.Config, error) {
	cfg := m.Cfg
	list, err := wsl.ListDistros(ctx, m.WSL)
	if err != nil {
		return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.wsl_unavailable_init", err))
	}

	var d wsl.Distro
	if opts.Distro != "" {
		found, ok := wsl.FindDistro(list, opts.Distro)
		if !ok {
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_distro_missing", opts.Distro))
		}
		if found.Version != 2 {
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_distro_wsl1", found.Name, found.Version))
		}
		d = found
	} else {
		found, ok := wsl.PickUbuntu(list)
		if !ok {
			name := opts.InstallDistro
			if name == "" {
				name = "Ubuntu"
			}
			if opts.SkipInstall {
				return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_no_ubuntu", name))
			}
			m.print(i18n.T("lc.init_installing", name))
			if _, _, err := m.WSL.Run(ctx, nil, "--install", "-d", name, "--no-launch"); err != nil {
				return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_install_failed", name, err))
			}
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_relaunch", name, name))
		}
		d = found
	}
	cfg.Distro = d.Name

	user := opts.User
	if user == "" {
		out, _, err := wsl.RunIn(ctx, m.WSL, d.Name, "", nil, "id", "-un")
		if err != nil {
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_user_detect", d.Name, err))
		}
		user = strings.TrimSpace(wsl.DecodeOutput(out))
		if user == "" || user == "root" {
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_user_root", d.Name))
		}
	}
	cfg.User = user

	port := opts.Port
	if port == 0 {
		if p, err := cfg.APIPort(); err == nil {
			port = p
		} else {
			port = config.DefaultPort
		}
	}

	args := []string{"bash", "-s", "--", "--user", user, "--port", strconv.Itoa(port)}
	if opts.NoCompose {
		args = append(args, "--no-compose")
	}
	m.print(i18n.T("lc.init_running", d.Name))
	out, stderr, err := wsl.RunIn(ctx, m.WSL, d.Name, "root", bytes.NewReader(m.Bundle), args...)
	m.write(out)
	m.write(stderr)
	if err != nil {
		switch wsl.ExitCode(err) {
		case 3:
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_exit3", d.Name))
		case 4:
			return cfg, fmt.Errorf("%w: %s", ErrAPI, i18n.T("lc.init_exit4", d.Name))
		default:
			return cfg, errors.New(i18n.T("lc.init_failed", err))
		}
	}
	chosen, ok := ParseBootstrapPort(string(out))
	if !ok {
		return cfg, errors.New(i18n.T("lc.init_no_port"))
	}
	if chosen != port {
		m.print(i18n.T("lc.init_port_changed", port, chosen))
	}
	cfg.APIEndpoint = config.EndpointForPort(chosen)
	return cfg, nil
}

func (m *Manager) print(s string) {
	if m.Output != nil {
		_, _ = io.WriteString(m.Output, s)
	}
}

func (m *Manager) write(b []byte) {
	if m.Output != nil && len(b) > 0 {
		_, _ = m.Output.Write(b)
	}
}
