// Package doctor 實作 SPEC §5.6 的標準診斷組。
package doctor

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/irice/wsl-docker/internal/config"
	"github.com/irice/wsl-docker/internal/wsl"
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
	c.add(Result{ID: id, Title: title, Level: Fail, Category: category, Detail: "skipped: 前置檢查失敗"})
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
		c.add(Result{ID: "wsl", Title: "wsl.exe 可用", Level: Fail, Category: "engine",
			Detail: err.Error(), Hint: "以系統管理員執行 scripts/01-enable-wsl.ps1 並重開機"})
		for _, s := range [][3]string{{"distro", "目標發行版為 WSL2", "engine"}, {"wake", "可喚醒發行版", "engine"},
			{"systemd", "systemd 已啟用", "engine"}, {"docker_service", "docker.service active", "engine"},
			{"loopback_only", "API 只綁 loopback", "engine"}, {"linger", "linger 已啟用", "engine"}} {
			c.skip(s[0], s[1], s[2])
		}
		c.checkAPI(ctx)
		c.checkDesktop(ctx, nil)
		c.infoChecks()
		return
	}
	c.add(Result{ID: "wsl", Title: "wsl.exe 可用", Level: OK, Detail: fmt.Sprintf("%d 個發行版", len(list))})

	// 2. distro
	distro, found := wsl.FindDistro(list, cfg.Distro)
	switch {
	case !found:
		c.add(Result{ID: "distro", Title: "目標發行版為 WSL2", Level: Fail, Category: "engine",
			Detail: fmt.Sprintf("config 指定 %q 不存在", cfg.Distro), Hint: "執行 wsld init 或修正 config.toml 的 distro"})
	case distro.Version != 2:
		c.add(Result{ID: "distro", Title: "目標發行版為 WSL2", Level: Fail, Category: "engine",
			Detail: fmt.Sprintf("%s 是 WSL%d", distro.Name, distro.Version), Hint: fmt.Sprintf("wsl --set-version %s 2", distro.Name)})
	default:
		c.add(Result{ID: "distro", Title: "目標發行版為 WSL2", Level: OK, Detail: fmt.Sprintf("%s (%s)", distro.Name, distro.State)})
	}
	engineOK := found && distro.Version == 2

	// 3. wake
	if engineOK {
		if _, err := c.runIn(ctx, "", "true"); err != nil {
			c.add(Result{ID: "wake", Title: "可喚醒發行版", Level: Fail, Category: "engine", Detail: err.Error(),
				Hint: "wsl --shutdown 後重試；或檢查 wsl -d " + cfg.Distro})
			engineOK = false
		} else {
			c.add(Result{ID: "wake", Title: "可喚醒發行版", Level: OK})
		}
	} else {
		c.skip("wake", "可喚醒發行版", "engine")
	}

	// 4. systemd
	systemdOK := false
	if engineOK {
		out, _ := c.runIn(ctx, "", "systemctl", "is-system-running")
		if out == "running" || out == "degraded" {
			systemdOK = true
			c.add(Result{ID: "systemd", Title: "systemd 已啟用", Level: OK, Detail: out})
		} else {
			c.add(Result{ID: "systemd", Title: "systemd 已啟用", Level: Fail, Category: "engine",
				Detail: fmt.Sprintf("systemctl is-system-running → %q", out),
				Hint:   "/etc/wsl.conf 加入 [boot] systemd=true，然後 wsl --shutdown"})
		}
	} else {
		c.skip("systemd", "systemd 已啟用", "engine")
	}

	// 5. docker.service
	if systemdOK {
		out, _ := c.runIn(ctx, "", "systemctl", "is-active", "docker")
		if out == "active" {
			c.add(Result{ID: "docker_service", Title: "docker.service active", Level: OK})
		} else {
			c.add(Result{ID: "docker_service", Title: "docker.service active", Level: Fail, Category: "engine",
				Detail: fmt.Sprintf("systemctl is-active docker → %q", out),
				Hint:   "wsld start；或 wsl -d " + cfg.Distro + " -u root -- systemctl start docker；未安裝則 wsld init"})
		}
	} else {
		c.skip("docker_service", "docker.service active", "engine")
	}

	// 6. api
	c.checkAPI(ctx)

	// 7. loopback only
	if engineOK {
		c.checkLoopback(ctx)
	} else {
		c.skip("loopback_only", "API 只綁 loopback", "engine")
	}

	// 8. linger
	if systemdOK {
		out, _ := c.runIn(ctx, "", "loginctl", "show-user", cfg.User, "--property=Linger", "--value")
		if out == "yes" {
			c.add(Result{ID: "linger", Title: "linger 已啟用", Level: OK})
		} else {
			c.add(Result{ID: "linger", Title: "linger 已啟用", Level: Warn,
				Detail: fmt.Sprintf("loginctl Linger=%q（使用者 %s）", out, cfg.User),
				Hint:   "wsl -d " + cfg.Distro + " -u root -- loginctl enable-linger " + cfg.User})
		}
	} else {
		c.add(Result{ID: "linger", Title: "linger 已啟用", Level: Warn, Detail: "skipped: systemd 未就緒"})
	}

	// 9. desktop
	c.checkDesktop(ctx, list)
	c.infoChecks()
}

func (c *checker) checkAPI(ctx context.Context) {
	cfg := c.d.Cfg
	if c.d.API == nil {
		c.add(Result{ID: "api_ping", Title: "Docker API _ping", Level: Fail, Category: "api", Detail: "no client"})
		return
	}
	err := c.d.API.Ping(ctx)
	if err == nil {
		c.add(Result{ID: "api_ping", Title: "Docker API _ping", Level: OK, Detail: cfg.APIEndpoint})
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
		c.add(Result{ID: "api_ping", Title: "Docker API _ping", Level: Fail, Category: "api",
			Detail: fmt.Sprintf("%s 有服務監聽但不是 Docker（not Docker）：%v", cfg.APIEndpoint, err),
			Hint:   "埠被其他程式占用；重新執行 wsld init 讓 bootstrap 換埠，或修改 config.toml 的 api_endpoint"})
		return
	}
	c.add(Result{ID: "api_ping", Title: "Docker API _ping", Level: Fail, Category: "api",
		Detail: fmt.Sprintf("%s 無回應：%v", cfg.APIEndpoint, err),
		Hint:   "執行 wsld start；仍失敗則確認 /etc/docker/daemon.json 的 hosts 含 tcp://127.0.0.1:<port> 且與 config 一致"})
}

var hostRe = regexp.MustCompile(`tcp://([^"'\s]+)`)

func (c *checker) checkLoopback(ctx context.Context) {
	cfg := c.d.Cfg
	port, err := cfg.APIPort()
	if err != nil {
		c.add(Result{ID: "loopback_only", Title: "API 只綁 loopback", Level: Fail, Category: "engine", Detail: err.Error()})
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
		c.add(Result{ID: "loopback_only", Title: "API 只綁 loopback", Level: Fail, Category: "engine",
			Detail: "非 loopback 監聽：" + strings.Join(bad, ", "),
			Hint:   "daemon.json 的 hosts 只允許 unix socket 與 tcp://127.0.0.1:<port>；修正後 systemctl restart docker"})
		return
	}
	c.add(Result{ID: "loopback_only", Title: "API 只綁 loopback", Level: OK, Detail: fmt.Sprintf("127.0.0.1:%d", port)})
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
		c.add(Result{ID: "docker_desktop", Title: "Docker Desktop 未同時運行", Level: Warn,
			Detail: strings.Join(hits, ", "),
			Hint:   "建議不要雙引擎混用：關閉 Docker Desktop（或停用其 WSL integration），CLI/GUI 只看 wsld 管理的引擎"})
		return
	}
	c.add(Result{ID: "docker_desktop", Title: "Docker Desktop 未同時運行", Level: OK})
}

func (c *checker) infoChecks() {
	cfg := c.d.Cfg
	c.add(Result{ID: "config", Title: "設定", Level: Info,
		Detail: fmt.Sprintf("distro=%s user=%s api_endpoint=%s", cfg.Distro, cfg.User, cfg.APIEndpoint),
		Hint:   "檔案：" + config.DefaultPath()})
	c.add(Result{ID: "repo_location", Title: "專案位置", Level: Info,
		Detail: "原始碼請放在 WSL 的 $HOME（例如 ~/Projects），不要放 C:\\（/mnt/c）",
		Hint:   "跨檔案系統 bind mount 慢且權限行為不同"})
}

// WindowsProcesses 以 tasklist 取得程序名（非 Windows 回傳 nil）。
func WindowsProcesses(ctx context.Context) ([]string, error) {
	if runtime.GOOS != "windows" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "tasklist.exe", "/FO", "CSV", "/NH").Output()
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
