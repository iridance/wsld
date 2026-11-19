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
	c.infoChecks()
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
	dial := c.d.Dial
	if dial == nil {
		dial = func(ctx context.Context, addr string) error {
			conn, err := (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", addr)
			if err == nil {
				conn.Close()
			}
			return err
		}
	}
	if dial(ctx, addr) == nil {
		c.add(Result{ID: "api_ping", Title: i18n.T("doctor.api.title"), Level: Fail, Category: "api",
			Detail: i18n.T("doctor.api.not_docker", cfg.APIEndpoint, err),
			Hint:   i18n.T("doctor.api.not_docker_hint")})
		return
	}
	c.add(Result{ID: "api_ping", Title: i18n.T("doctor.api.title"), Level: Fail, Category: "api",
		Detail: i18n.T("doctor.api.unreachable", cfg.APIEndpoint, err),
		Hint:   i18n.T("doctor.api.unreachable_hint")})
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
