// Package doctor 實作 SPEC §5.6 的標準診斷組。
package doctor

import (
	"github.com/iridance/wsld/internal/procutil"

	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/expose"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/wsl"
)

// Level 為檢查結果等級。
type Level int

const (
	OK Level = iota
	Info
	Warn
	Fail
)

func (l Level) String() string {
	switch l {
	case OK:
		return "OK"
	case Info:
		return "INFO"
	case Warn:
		return "WARN"
	default:
		return "FAIL"
	}
}

// Result 為單項檢查結果。
type Result struct {
	ID     string
	Title  string
	Level  Level
	Detail string
	Hint   string
	// Category 決定失敗時的退出碼：engine→3、api→4
	Category string
}

// Report 為完整報告。
type Report struct {
	Results []Result
}

// Counts 回傳 ok/warn/fail 數（Info 計入 ok）。
func (r Report) Counts() (ok, warn, fail int) {
	for _, x := range r.Results {
		switch x.Level {
		case Warn:
			warn++
		case Fail:
			fail++
		default:
			ok++
		}
	}
	return
}

// ExitCode：有 engine 類 Fail → 3；僅 api 類 Fail → 4；否則 0。
func (r Report) ExitCode() int {
	code := 0
	for _, x := range r.Results {
		if x.Level != Fail {
			continue
		}
		if x.Category == "api" {
			if code == 0 {
				code = 4
			}
		} else {
			return 3
		}
	}
	return code
}

// Pinger 抽象 dockerapi.Client.Ping。
type Pinger interface{ Ping(context.Context) error }

// Deps 為 Run 的依賴（皆可以 fake 取代）。
type Deps struct {
	WSL       wsl.Runner
	Cfg       config.Config
	API       Pinger
	Processes func(ctx context.Context) ([]string, error)  // Windows 程序名；nil 則略過 Desktop 檢查
	Dial      func(ctx context.Context, addr string) error // TCP 連線探測；nil 用 net.Dialer
	// LAN 查詢 0.9.0 mirrored 遺留設定；nil 則略過。
	LAN func(ctx context.Context) (lan.Legacy, error)
}

// Run 執行全部檢查。
func Run(ctx context.Context, d Deps) Report {
	c := &checker{d: d}
	c.run(ctx)
	return c.rep
}

type checker struct {
	d   Deps
	rep Report
}

func (c *checker) add(r Result) { c.rep.Results = append(c.rep.Results, r) }

func (c *checker) skip(id, title, category string) {
	c.add(Result{ID: id, Title: title, Level: Fail, Category: category, Detail: i18n.T("doctor.skipped")})
}

func (c *checker) runIn(ctx context.Context, user string, cmd ...string) (string, error) {
	out, _, err := wsl.RunIn(ctx, c.d.WSL, c.d.Cfg.Distro, user, nil, cmd...)
	return strings.TrimSpace(wsl.DecodeOutput(out)), err
}

func (c *checker) run(ctx context.Context) {
	// 有專用 engine 時，dockerd／systemd／linger／API 等主檢查都針對 engine（client 沒有 daemon）。
	if e, ok := c.d.Cfg.EngineMachine(); ok {
		_ = c.d.Cfg.SetActive(e.Distro)
	}
	cfg := c.d.Cfg

	// 1. wsl
	list, err := wsl.ListDistros(ctx, c.d.WSL)
	if err != nil {
		c.add(Result{ID: "wsl", Title: i18n.T("doctor.wsl.title"), Level: Fail, Category: "engine",
			Detail: err.Error(), Hint: i18n.T("doctor.wsl.hint")})
		for _, s := range [][3]string{{"distro", "doctor.distro.title", "engine"}, {"wake", "doctor.wake.title", "engine"},
			{"systemd", "doctor.systemd.title", "engine"}, {"docker_service", "doctor.docker_service.title", "engine"},
			{"loopback_only", "doctor.loopback.title", "engine"}, {"linger", "doctor.linger.title", "engine"}} {
			c.skip(s[0], i18n.T(s[1]), s[2])
		}
		c.checkAPI(ctx)
		c.checkDesktop(ctx, nil)
		c.infoChecks()
		return
	}
	c.add(Result{ID: "wsl", Title: i18n.T("doctor.wsl.title"), Level: OK, Detail: i18n.T("doctor.wsl.detail", len(list))})

	// 2. distro
	distro, found := wsl.FindDistro(list, cfg.Distro)
	switch {
	case !found:
		c.add(Result{ID: "distro", Title: i18n.T("doctor.distro.title"), Level: Fail, Category: "engine",
			Detail: i18n.T("doctor.distro.missing", cfg.Distro), Hint: i18n.T("doctor.distro.missing_hint")})
	case distro.Version != 2:
		c.add(Result{ID: "distro", Title: i18n.T("doctor.distro.title"), Level: Fail, Category: "engine",
			Detail: i18n.T("doctor.distro.wsl1", distro.Name, distro.Version), Hint: fmt.Sprintf("wsl --set-version %s 2", distro.Name)})
	default:
		c.add(Result{ID: "distro", Title: i18n.T("doctor.distro.title"), Level: OK, Detail: fmt.Sprintf("%s (%s)", distro.Name, distro.State)})
	}
	engineOK := found && distro.Version == 2

	// 3. wake
	if engineOK {
		if _, err := c.runIn(ctx, "", "true"); err != nil {
			c.add(Result{ID: "wake", Title: i18n.T("doctor.wake.title"), Level: Fail, Category: "engine", Detail: err.Error(),
				Hint: i18n.T("doctor.wake.hint", cfg.Distro)})
			engineOK = false
		} else {
			c.add(Result{ID: "wake", Title: i18n.T("doctor.wake.title"), Level: OK})
		}
	} else {
		c.skip("wake", i18n.T("doctor.wake.title"), "engine")
	}

	// 4. systemd
	systemdOK := false
	if engineOK {
		out, _ := c.runIn(ctx, "", "systemctl", "is-system-running")
		if out == "running" || out == "degraded" {
			systemdOK = true
			c.add(Result{ID: "systemd", Title: i18n.T("doctor.systemd.title"), Level: OK, Detail: out})
		} else {
			c.add(Result{ID: "systemd", Title: i18n.T("doctor.systemd.title"), Level: Fail, Category: "engine",
				Detail: fmt.Sprintf("systemctl is-system-running → %q", out),
				Hint:   i18n.T("doctor.systemd.hint")})
		}
	} else {
		c.skip("systemd", i18n.T("doctor.systemd.title"), "engine")
	}

	// 5. docker.service
	if systemdOK {
		out, _ := c.runIn(ctx, "", "systemctl", "is-active", "docker")
		if out == "active" {
			c.add(Result{ID: "docker_service", Title: i18n.T("doctor.docker_service.title"), Level: OK})
		} else {
			c.add(Result{ID: "docker_service", Title: i18n.T("doctor.docker_service.title"), Level: Fail, Category: "engine",
				Detail: fmt.Sprintf("systemctl is-active docker → %q", out),
				Hint:   i18n.T("doctor.docker_service.hint", cfg.Distro)})
		}
	} else {
		c.skip("docker_service", i18n.T("doctor.docker_service.title"), "engine")
	}

	// 6. api
	c.checkAPI(ctx)

	// 7. loopback only
	if engineOK {
		c.checkLoopback(ctx)
	} else {
		c.skip("loopback_only", i18n.T("doctor.loopback.title"), "engine")
	}

	// 8. linger
	if systemdOK {
		out, _ := c.runIn(ctx, "", "loginctl", "show-user", cfg.User, "--property=Linger", "--value")
		if out == "yes" {
			c.add(Result{ID: "linger", Title: i18n.T("doctor.linger.title"), Level: OK})
		} else {
			c.add(Result{ID: "linger", Title: i18n.T("doctor.linger.title"), Level: Warn,
				Detail: i18n.T("doctor.linger.detail", out, cfg.User),
				Hint:   "wsl -d " + cfg.Distro + " -u root -- loginctl enable-linger " + cfg.User})
		}
	} else {
		c.add(Result{ID: "linger", Title: i18n.T("doctor.linger.title"), Level: Warn, Detail: i18n.T("doctor.linger.skipped")})
	}

	// 9. desktop
	c.checkDesktop(ctx, list)
	// 10. 區網存取（僅已啟用時）
	c.checkLAN(ctx)
	// 11. 專用 engine：共用 socket 與各 client
	c.checkEngine(ctx, list)
	// 12. Kubernetes（僅已開啟時）
	c.checkK8s(ctx, list)
	c.infoChecks()
}

// checkK8s：Kubernetes 開啟時檢查 k8s-engine、engine 的容器目錄分享與 API 埠。問題一律只警告（不影響 Docker）。
// 停止中的 k8s-engine 不叫醒它。
func (c *checker) checkK8s(ctx context.Context, list []wsl.Distro) {
	cfg := c.d.Cfg
	if !cfg.K8s {
		return
	}
	name := cfg.K8sDistro()
	if d, ok := wsl.FindDistro(list, name); !ok {
		c.add(Result{ID: "k8s_distro", Title: i18n.T("doctor.k8s_distro.title"), Level: Warn, Detail: i18n.T("doctor.k8s_distro.missing", name), Hint: "wsld k8s on"})
		return
	} else if !strings.EqualFold(d.State, "Running") {
		c.add(Result{ID: "k8s_distro", Title: i18n.T("doctor.k8s_distro.title"), Level: Warn, Detail: i18n.T("doctor.k8s_distro.stopped", name), Hint: "wsld k8s on"})
		return
	}
	c.add(Result{ID: "k8s_distro", Title: i18n.T("doctor.k8s_distro.title"), Level: OK, Detail: name})
	const share = "/mnt/wsl/wsld/k8s/containers"
	if _, err := c.runIn(ctx, "root", "mountpoint", "-q", share); err != nil {
		c.add(Result{ID: "k8s_share", Title: i18n.T("doctor.k8s_share.title"), Level: Warn, Detail: i18n.T("doctor.k8s_share.missing", share), Hint: "wsld k8s on"})
	} else {
		c.add(Result{ID: "k8s_share", Title: i18n.T("doctor.k8s_share.title"), Level: OK, Detail: share})
	}
	addr := fmt.Sprintf("127.0.0.1:%d", cfg.K8sAPIPortOrDefault())
	if err := c.dial()(ctx, addr); err != nil {
		c.add(Result{ID: "k8s_api", Title: i18n.T("doctor.k8s_api.title"), Level: Warn, Detail: i18n.T("doctor.k8s_api.down", addr, err),
			Hint: "wsl -d " + name + " -u root -- journalctl -u k3s -n 50"})
	} else {
		c.add(Result{ID: "k8s_api", Title: i18n.T("doctor.k8s_api.title"), Level: OK, Detail: addr})
	}
}

// checkEngine：已設定專用 engine 時，檢查共用 socket 與每台 client。問題一律只警告（主檢查已涵蓋 engine 本身）。
// 停止中的 client 不叫醒它（wsl -d 會喚醒 distro），只回報 Info。
func (c *checker) checkEngine(ctx context.Context, list []wsl.Distro) {
	eng, ok := c.d.Cfg.EngineMachine()
	if !ok {
		return
	}
	const sock = "/mnt/wsl/wsld/docker.sock"
	if _, err := c.runIn(ctx, "", "test", "-S", sock); err != nil {
		c.add(Result{ID: "engine_socket", Title: i18n.T("doctor.engine_socket.title"), Level: Warn,
			Detail: i18n.T("doctor.engine_socket.missing", sock, eng.Distro), Hint: "wsld start"})
	} else {
		c.add(Result{ID: "engine_socket", Title: i18n.T("doctor.engine_socket.title"), Level: OK, Detail: sock})
	}
	for _, m := range c.d.Cfg.Machines {
		if m.Role != config.RoleClient {
			continue
		}
		id, title := "client_"+m.Distro, i18n.T("doctor.client.title", m.Distro)
		if d, found := wsl.FindDistro(list, m.Distro); !found || !strings.EqualFold(d.State, "Running") {
			c.add(Result{ID: id, Title: title, Level: Info, Detail: i18n.T("doctor.client.stopped")})
			continue
		}
		out, _, _ := wsl.RunIn(ctx, c.d.WSL, m.Distro, m.User, nil, "docker", "context", "show")
		if ctxName := strings.TrimSpace(wsl.DecodeOutput(out)); ctxName != "wsld" {
			c.add(Result{ID: id, Title: title, Level: Warn, Detail: i18n.T("doctor.client.context", ctxName),
				Hint: "wsld init --distro " + m.Distro + " --role client"})
			continue
		}
		// 曝露工具：wsld-expose（root helper）與 docker shim 都要在，docker compose 的 bind mount 才會自動曝露
		_, _, errTool := wsl.RunIn(ctx, c.d.WSL, m.Distro, "", nil, "test", "-x", "/usr/local/sbin/wsld-expose")
		_, _, errShim := wsl.RunIn(ctx, c.d.WSL, m.Distro, "", nil, "grep", "-q", "DISTRO_DEFAULT", "/usr/local/bin/docker")
		toolsID, toolsTitle := "expose_tools_"+m.Distro, i18n.T("doctor.expose_tools.title", m.Distro)
		if errTool != nil || errShim != nil {
			c.add(Result{ID: toolsID, Title: toolsTitle, Level: Warn, Detail: i18n.T("doctor.expose_tools.missing"),
				Hint: "wsld init --distro " + m.Distro + " --role client"})
		} else {
			c.add(Result{ID: toolsID, Title: toolsTitle, Level: OK})
		}
		out, _, _ = wsl.RunIn(ctx, c.d.WSL, m.Distro, "", nil, "systemctl", "is-active", "docker")
		if strings.TrimSpace(wsl.DecodeOutput(out)) == "active" {
			c.add(Result{ID: id, Title: title, Level: Warn, Detail: i18n.T("doctor.client.local_dockerd"),
				Hint: "wsl -d " + m.Distro + " -u root -- systemctl disable --now docker docker.socket"})
			continue
		}
		c.add(Result{ID: id, Title: title, Level: OK})
	}
	c.checkStaleExposure(ctx, eng, list)
}

// checkStaleExposure：掛載還在、來源 distro 卻不在 Running，代表那份 VHD 仍被接著（再壓縮／調整大小會變成同一份被接兩次）。
// 讀不到掛載清單（engine 內 findmnt 失敗）就不報。
func (c *checker) checkStaleExposure(ctx context.Context, eng config.Machine, list []wsl.Distro) {
	m := &expose.Manager{WSL: c.d.WSL, EngineDistro: eng.Distro}
	mounted, err := m.Mounted(ctx)
	if err != nil {
		return
	}
	var stale []string
	for _, n := range mounted {
		if d, ok := wsl.FindDistro(list, n); !ok || !strings.EqualFold(d.State, "Running") {
			stale = append(stale, n)
		}
	}
	if len(stale) > 0 {
		c.add(Result{ID: "expose_stale", Title: i18n.T("doctor.expose_stale.title"), Level: Warn,
			Detail: i18n.T("doctor.expose_stale.detail", strings.Join(stale, ", ")), Hint: "wsld expose gc"})
	}
}

// checkLAN：區網存取開啟時列出（由 wsld-gui 轉發）；偵測到 mirrored 模式（0.9.0 遺留）時警告，
// 因為 mirrored 下容器埠直接佔用 Windows 的埠，轉發器無法監聽。
func (c *checker) checkLAN(ctx context.Context) {
	var legacy lan.Legacy
	if c.d.LAN != nil {
		legacy, _ = c.d.LAN(ctx)
	}
	switch {
	case legacy.Mirrored:
		c.add(Result{ID: "lan", Title: i18n.T("doctor.lan.title"), Level: Warn,
			Detail: i18n.T("doctor.lan.mirrored"), Hint: i18n.T("doctor.lan.mirrored_hint")})
	case c.d.Cfg.LANAccess:
		c.add(Result{ID: "lan", Title: i18n.T("doctor.lan.title"), Level: Info, Detail: i18n.T("doctor.lan.on")})
	}
}

func (c *checker) checkAPI(ctx context.Context) {
	cfg := c.d.Cfg
	if c.d.API == nil {
		c.add(Result{ID: "api_ping", Title: i18n.T("doctor.api.title"), Level: Fail, Category: "api", Detail: "no client"})
		return
	}
	err := c.d.API.Ping(ctx)
	if err == nil {
		c.add(Result{ID: "api_ping", Title: i18n.T("doctor.api.title"), Level: OK, Detail: cfg.APIEndpoint})
		return
	}
	// 分辨：埠沒人聽 vs 有人聽但不是 Docker
	addr := strings.TrimPrefix(cfg.HTTPBaseURL(), "http://")
	if c.dial()(ctx, addr) == nil {
		c.add(Result{ID: "api_ping", Title: i18n.T("doctor.api.title"), Level: Fail, Category: "api",
			Detail: i18n.T("doctor.api.not_docker", cfg.APIEndpoint, err),
			Hint:   i18n.T("doctor.api.not_docker_hint")})
		return
	}
	c.add(Result{ID: "api_ping", Title: i18n.T("doctor.api.title"), Level: Fail, Category: "api",
		Detail: i18n.T("doctor.api.unreachable", cfg.APIEndpoint, err),
		Hint:   i18n.T("doctor.api.unreachable_hint")})
}

// dial 回傳 TCP 連線探測（Deps.Dial，未注入則用 net.Dialer）。
func (c *checker) dial() func(ctx context.Context, addr string) error {
	if c.d.Dial != nil {
		return c.d.Dial
	}
	return func(ctx context.Context, addr string) error {
		conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			conn.Close()
		}
		return err
	}
}

var hostRe = regexp.MustCompile(`tcp://([^"'\s]+)`)

func (c *checker) checkLoopback(ctx context.Context) {
	cfg := c.d.Cfg
	port, err := cfg.APIPort()
	if err != nil {
		c.add(Result{ID: "loopback_only", Title: i18n.T("doctor.loopback.title"), Level: Fail, Category: "engine", Detail: err.Error()})
		return
	}
	var bad []string

	if out, err := c.runIn(ctx, "", "ss", "-Hltn"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			f := strings.Fields(line)
			if len(f) < 4 {
				continue
			}
			local := f[3]
			host, p := splitHostPort(local)
			if p != fmt.Sprint(port) {
				continue
			}
			if !isLoopback(host) {
				bad = append(bad, local)
			}
		}
	}
	if out, err := c.runIn(ctx, "", "cat", "/etc/docker/daemon.json"); err == nil {
		for _, m := range hostRe.FindAllStringSubmatch(out, -1) {
			host, _ := splitHostPort(m[1])
			if !isLoopback(host) {
				bad = append(bad, "daemon.json: tcp://"+m[1])
			}
		}
	}
	if len(bad) > 0 {
		c.add(Result{ID: "loopback_only", Title: i18n.T("doctor.loopback.title"), Level: Fail, Category: "engine",
			Detail: i18n.T("doctor.loopback.bad", strings.Join(bad, ", ")),
			Hint:   i18n.T("doctor.loopback.hint")})
		return
	}
	c.add(Result{ID: "loopback_only", Title: i18n.T("doctor.loopback.title"), Level: OK, Detail: fmt.Sprintf("127.0.0.1:%d", port)})
}

func splitHostPort(s string) (host, port string) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, ""
	}
	host = strings.Trim(s[:i], "[]")
	port = s[i+1:]
	return
}

func isLoopback(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

var desktopProcs = []string{"docker desktop.exe", "com.docker.backend.exe", "com.docker.build.exe", "com.docker.proxy.exe"}

func (c *checker) checkDesktop(ctx context.Context, list []wsl.Distro) {
	var hits []string
	if c.d.Processes != nil {
		if procs, err := c.d.Processes(ctx); err == nil {
			for _, p := range procs {
				for _, d := range desktopProcs {
					if strings.EqualFold(p, d) {
						hits = append(hits, p)
					}
				}
			}
		}
	}
	for _, d := range list {
		if strings.EqualFold(d.Name, "docker-desktop") && strings.EqualFold(d.State, "Running") {
			hits = append(hits, "WSL distro docker-desktop (Running)")
		}
	}
	if len(hits) > 0 {
		c.add(Result{ID: "docker_desktop", Title: i18n.T("doctor.desktop.title"), Level: Warn,
			Detail: strings.Join(hits, ", "),
			Hint:   i18n.T("doctor.desktop.hint")})
		return
	}
	c.add(Result{ID: "docker_desktop", Title: i18n.T("doctor.desktop.title"), Level: OK})
}

func (c *checker) infoChecks() {
	cfg := c.d.Cfg
	c.add(Result{ID: "config", Title: i18n.T("doctor.config.title"), Level: Info,
		Detail: fmt.Sprintf("distro=%s user=%s api_endpoint=%s", cfg.Distro, cfg.User, cfg.APIEndpoint),
		Hint:   i18n.T("doctor.config.hint", config.DefaultPath())})
	c.add(Result{ID: "repo_location", Title: i18n.T("doctor.repo.title"), Level: Info,
		Detail: i18n.T("doctor.repo.detail"),
		Hint:   i18n.T("doctor.repo.hint")})
}

// WindowsProcesses 以 tasklist 取得程序名（非 Windows 回傳 nil）。
func WindowsProcesses(ctx context.Context) ([]string, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "tasklist.exe", "/FO", "CSV", "/NH")
	procutil.HideConsole(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseTasklistCSV(string(out)), nil
}

func parseTasklistCSV(s string) []string {
	var names []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "\"") {
			continue
		}
		end := strings.Index(line[1:], "\"")
		if end < 0 {
			continue
		}
		names = append(names, line[1:1+end])
	}
	return names
}
