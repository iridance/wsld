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

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/wsl"
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
	// NewAPI 在 Start 自動 bootstrap 後重綁 Pinger（埠可能變更）；可選。
	NewAPI func(cfg config.Config) Pinger

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
	// 只在 distro 已執行時探測 systemd／docker：對停止中的 distro 跑 wsl -d 會把它喚醒，
	// GUI 每幾秒輪詢一次 → 使用者停止的 distro 會立刻被叫醒。
	if st.DistroRunning {
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

// DockerUnitPresent 判斷 distro 是否已安裝 docker.service（未安裝時 Start 會自動 bootstrap）。
func (m *Manager) DockerUnitPresent(ctx context.Context) bool {
	_, _, err := wsl.RunIn(ctx, m.WSL, m.Cfg.Distro, "", nil, "systemctl", "cat", "docker.service")
	return err == nil
}

// Start：喚醒 distro → 確認 systemd → 啟動 docker.service（缺 unit 則自動 Init）→ 等 API。
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
		if !m.DockerUnitPresent(ctx) {
			if len(m.Bundle) == 0 {
				return Status{}, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.docker_missing", m.Cfg.Distro))
			}
			m.print(i18n.T("lc.start_bootstrap", m.Cfg.Distro))
			newCfg, err := m.Init(ctx, InitOptions{Distro: m.Cfg.Distro, User: m.Cfg.User})
			if err != nil {
				return m.Status(ctx), err
			}
			m.Cfg = newCfg
			if m.NewAPI != nil {
				m.API = m.NewAPI(newCfg)
			}
		} else if _, stderr, err := m.startDocker(ctx); err != nil {
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

// startDocker：先 reset-failed（之前連續失敗觸發 start limit 時，systemctl start 會直接拒絕），再 start。
func (m *Manager) startDocker(ctx context.Context) ([]byte, []byte, error) {
	_, _, _ = wsl.RunIn(ctx, m.WSL, m.Cfg.Distro, "root", nil, "systemctl", "reset-failed", "docker")
	return wsl.RunIn(ctx, m.WSL, m.Cfg.Distro, "root", nil, "systemctl", "start", "docker")
}

// InitOptions 為 wsld init 的參數；空值代表自動。
type InitOptions struct {
	Distro        string
	User          string
	Port          int
	NoCompose     bool
	InstallDistro string // 無 Ubuntu 時要安裝的名稱；空→"Ubuntu"
	SkipInstall   bool
	// Password 於需要「新建」Linux 使用者時呼叫，回傳其密碼（空 = 不設，帳號維持無密碼）。
	// nil 則不詢問。既有使用者的密碼一律不動。
	Password func(user string) (string, error)
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
	needEnsure := user != ""
	if user == "" {
		out, _, err := wsl.RunIn(ctx, m.WSL, d.Name, "", nil, "id", "-un")
		if err != nil {
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_user_detect", d.Name, err))
		}
		user = strings.TrimSpace(wsl.DecodeOutput(out))
		if user == "" || user == "root" {
			user = wsl.SuggestLinuxUser()
			needEnsure = true
			m.print(i18n.T("lc.init_creating_user", d.Name, user))
		}
	}
	if needEnsure {
		pw, creating := "", false
		if opts.Password != nil && !wsl.UserExists(ctx, m.WSL, d.Name, user) {
			creating = true
			if pw, err = opts.Password(user); err != nil {
				return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_user_create", d.Name, err))
			}
		}
		if err := wsl.EnsureUser(ctx, m.WSL, d.Name, user, pw); err != nil {
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_user_create", d.Name, err))
		}
		if creating && pw == "" {
			m.print(i18n.T("lc.init_user_no_password", user, d.Name, user))
		}
	}
	if user == "" || user == "root" || !wsl.ValidLinuxUsername(user) {
		return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_user_root", d.Name, d.Name))
	}
	cfg.User = user

	port := opts.Port
	if port == 0 {
		// 重 init：優先沿用本 distro 既有埠；新 machine：避開其他 profile 已登記埠。
		if m, ok := cfg.FindMachine(d.Name); ok && m.APIEndpoint != "" {
			tmp := config.Config{APIEndpoint: m.APIEndpoint}
			if p, err := tmp.APIPort(); err == nil {
				port = p
			}
		}
		if port == 0 {
			port = cfg.NextAPIPort(d.Name)
		}
	}
	busy := cfg.BusyAPIPorts(d.Name)

	bootArgs := []string{"--user", user, "--port", strconv.Itoa(port)}
	if len(busy) > 0 {
		parts := make([]string, len(busy))
		for i, p := range busy {
			parts[i] = strconv.Itoa(p)
		}
		bootArgs = append(bootArgs, "--busy-ports", strings.Join(parts, ","))
	}
	if opts.NoCompose {
		bootArgs = append(bootArgs, "--no-compose")
	}
	m.print(i18n.T("lc.init_running", d.Name))
	// 先把 bundle 寫入 distro 暫存再執行，避免 `bash -s`+stdin 被 systemctl 等指令吞掉腳本。
	var b strings.Builder
	b.WriteString("cat > /tmp/wsld-bootstrap.sh && bash /tmp/wsld-bootstrap.sh")
	for _, a := range bootArgs {
		b.WriteByte(' ')
		b.WriteString(shellQuote(a))
	}
	out, stderr, err := wsl.RunIn(ctx, m.WSL, d.Name, "root", bytes.NewReader(m.Bundle), "bash", "-c", b.String())
	m.write(out)
	m.write(stderr)
	if err != nil {
		switch wsl.ExitCode(err) {
		case 3:
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_exit3", d.Name))
		case 4:
			return cfg, fmt.Errorf("%w: %s", ErrAPI, i18n.T("lc.init_exit4", d.Name))
		case 5:
			return cfg, fmt.Errorf("%w: %s", ErrNotReady, i18n.T("lc.init_unsupported", d.Name))
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
	cfg.EnsureActive(config.Machine{
		Distro:      d.Name,
		User:        user,
		APIEndpoint: config.EndpointForPort(chosen),
	})
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

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' || r == '/' || r == ':'
		if !ok {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}
