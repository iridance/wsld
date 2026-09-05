package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/irice/wsl-docker/internal/dockerapi"
	"github.com/irice/wsl-docker/internal/lifecycle"
)

// FakeEngine 是記憶體內的 Docker API 假實作，供 Go 測試與 Playwright 測試主機使用。
type FakeEngine struct {
	mu         sync.Mutex
	Down       bool
	containers map[string]*dockerapi.Container
	logs       map[string][]string
	subs       map[string][]chan string
	execEcho   bool
	// LastResize 為最後一次 resize 的 rows/cols。
	LastResize [2]int
}

// NewFakeEngine 建立含兩個示範容器的假引擎。
func NewFakeEngine() *FakeEngine {
	f := &FakeEngine{}
	f.Reset()
	return f
}

// Reset 回到初始狀態（web running、db exited、兩行 web 日誌），供測試主機 /__test/reset。
func (f *FakeEngine) Reset() {
	f.mu.Lock()
	for _, subs := range f.subs {
		for _, ch := range subs {
			close(ch)
		}
	}
	f.Down = false
	f.containers = map[string]*dockerapi.Container{}
	f.logs = map[string][]string{}
	f.subs = map[string][]chan string{}
	f.execEcho = true
	f.LastResize = [2]int{}
	f.mu.Unlock()
	f.AddContainer("web", "nginx:1.27", "running", []dockerapi.Port{{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"}})
	f.AddContainer("db", "postgres:16", "exited", nil)
	f.AppendLog("web", "nginx started", "GET / 200")
}

func (f *FakeEngine) idFor(name string) string {
	return fmt.Sprintf("%-64s", "id-"+name)[:64]
}

// AddContainer 新增容器。
func (f *FakeEngine) AddContainer(name, image, state string, ports []dockerapi.Port) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := strings.ReplaceAll(f.idFor(name), " ", "0")
	status := "Up 5 minutes"
	if state != "running" {
		status = "Exited (0) 2 hours ago"
	}
	f.containers[id] = &dockerapi.Container{ID: id, Names: []string{"/" + name}, Image: image, State: state, Status: status, Ports: ports, Created: time.Now().Unix()}
	return id
}

// AppendLog 追加日誌並推給 follow 訂閱者。
func (f *FakeEngine) AppendLog(name string, lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.findLocked(name)
	f.logs[id] = append(f.logs[id], lines...)
	for _, ch := range f.subs[id] {
		for _, l := range lines {
			select {
			case ch <- l:
			default:
			}
		}
	}
}

func (f *FakeEngine) findLocked(ref string) string {
	if _, ok := f.containers[ref]; ok {
		return ref
	}
	for id, c := range f.containers {
		if c.Name() == ref || strings.HasPrefix(id, ref) {
			return id
		}
	}
	return ""
}

func (f *FakeEngine) down() error {
	if f.Down {
		return fmt.Errorf("%w: connection refused", dockerapi.ErrUnreachable)
	}
	return nil
}

// Ping 實作 Engine。
func (f *FakeEngine) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.down()
}

// ListContainers 實作 Engine。
func (f *FakeEngine) ListContainers(_ context.Context, all bool) ([]dockerapi.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return nil, err
	}
	out := []dockerapi.Container{}
	for _, c := range f.containers {
		if all || c.State == "running" {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// StartContainer 實作 Engine。
func (f *FakeEngine) StartContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return err
	}
	c := f.containers[f.findLocked(id)]
	if c == nil {
		return &dockerapi.APIError{Status: 404, Message: "No such container: " + id}
	}
	c.State, c.Status = "running", "Up 1 second"
	return nil
}

// StopContainer 實作 Engine。
func (f *FakeEngine) StopContainer(_ context.Context, id string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return err
	}
	c := f.containers[f.findLocked(id)]
	if c == nil {
		return &dockerapi.APIError{Status: 404, Message: "No such container: " + id}
	}
	c.State, c.Status = "exited", "Exited (0) 1 second ago"
	return nil
}

// Logs 實作 Engine：非 follow 回尾部；follow 先回尾部再持續推送。
func (f *FakeEngine) Logs(ctx context.Context, id string, opts dockerapi.LogsOptions) (io.ReadCloser, error) {
	f.mu.Lock()
	if err := f.down(); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	cid := f.findLocked(id)
	if cid == "" {
		f.mu.Unlock()
		return nil, &dockerapi.APIError{Status: 404, Message: "No such container: " + id}
	}
	lines := append([]string(nil), f.logs[cid]...)
	if opts.Tail > 0 && len(lines) > opts.Tail {
		lines = lines[len(lines)-opts.Tail:]
	}
	if !opts.Follow {
		f.mu.Unlock()
		s := strings.Join(lines, "\n")
		if s != "" {
			s += "\n"
		}
		return io.NopCloser(strings.NewReader(s)), nil
	}
	ch := make(chan string, 64)
	f.subs[cid] = append(f.subs[cid], ch)
	f.mu.Unlock()
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for _, l := range lines {
			if _, err := io.WriteString(pw, l+"\n"); err != nil {
				return
			}
		}
		for {
			select {
			case <-ctx.Done():
				return
			case l, ok := <-ch:
				if !ok {
					return
				}
				if _, err := io.WriteString(pw, l+"\n"); err != nil {
					return
				}
			}
		}
	}()
	return &pipeCloser{pr, func() {
		f.mu.Lock()
		subs := f.subs[cid]
		for i, c := range subs {
			if c == ch {
				f.subs[cid] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		f.mu.Unlock()
	}}, nil
}

type pipeCloser struct {
	*io.PipeReader
	onClose func()
}

func (p *pipeCloser) Close() error {
	p.onClose()
	return p.PipeReader.Close()
}

// Inspect 實作 Engine。
func (f *FakeEngine) Inspect(_ context.Context, id string) (dockerapi.ContainerDetail, error) {
	var d dockerapi.ContainerDetail
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.containers[f.findLocked(id)]
	if c == nil {
		return d, &dockerapi.APIError{Status: 404, Message: "No such container"}
	}
	d.ID, d.Name, d.Config.Image, d.State.Status = c.ID, c.Names[0], c.Image, c.State
	d.State.Running = c.State == "running"
	return d, nil
}

// ExecCreate 實作 Engine：exec id 記錄容器與命令。
func (f *FakeEngine) ExecCreate(_ context.Context, id string, cmd []string, _ bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return "", err
	}
	cid := f.findLocked(id)
	if cid == "" {
		return "", &dockerapi.APIError{Status: 404, Message: "No such container: " + id}
	}
	if f.containers[cid].State != "running" {
		return "", &dockerapi.APIError{Status: 409, Message: "Container " + id + " is not running"}
	}
	return "exec-" + cid[:8] + "-" + strings.Join(cmd, "_"), nil
}

// ExecStart 實作 Engine：回傳一個會回顯輸入的假 shell。
func (f *FakeEngine) ExecStart(ctx context.Context, execID string, _ bool) (net.Conn, error) {
	if !strings.HasPrefix(execID, "exec-") {
		return nil, &dockerapi.APIError{Status: 404, Message: "No such exec instance"}
	}
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.WriteString(server, "fake-shell$ ")
		buf := make([]byte, 1024)
		var line []byte
		for {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			for _, b := range buf[:n] {
				switch b {
				case '\r', '\n':
					cmd := strings.TrimSpace(string(line))
					line = line[:0]
					if cmd == "exit" {
						_, _ = io.WriteString(server, "\r\nbye\r\n")
						return
					}
					_, _ = io.WriteString(server, "\r\nyou typed: "+cmd+"\r\nfake-shell$ ")
				case 0x7f:
					if len(line) > 0 {
						line = line[:len(line)-1]
						_, _ = io.WriteString(server, "\b \b")
					}
				default:
					line = append(line, b)
					if f.execEcho {
						_, _ = server.Write([]byte{b})
					}
				}
			}
		}
	}()
	return client, nil
}

// ExecResize 實作 Engine（記錄最後尺寸供測試）。
func (f *FakeEngine) ExecResize(_ context.Context, _ string, rows, cols int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.LastResize = [2]int{rows, cols}
	return nil
}

// ResizeState 回傳最後一次 resize（rows, cols）。
func (f *FakeEngine) ResizeState() [2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.LastResize
}

// SetDown 切換引擎可用性。
func (f *FakeEngine) SetDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Down = down
}

var _ Engine = (*FakeEngine)(nil)

// FakeLifecycle 假生命週期：Start 會把引擎設為可用。
type FakeLifecycle struct {
	mu       sync.Mutex
	Engine   *FakeEngine
	Distro   string
	Endpoint string
	Broken   bool // Start 失敗（模擬 systemd 未啟用）
	starts   int
}

// Status 實作 Lifecycle。
func (l *FakeLifecycle) Status(context.Context) lifecycle.Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	up := !l.Engine.Down
	return lifecycle.Status{
		Distro: l.Distro, DistroFound: true, DistroRunning: up, DistroVersion: 2,
		SystemdActive: !l.Broken, DockerActive: up, APIEndpoint: l.Endpoint, APIReachable: up,
		APIError: map[bool]string{true: "", false: "connection refused"}[up],
	}
}

// Start 實作 Lifecycle。
func (l *FakeLifecycle) Start(ctx context.Context) (lifecycle.Status, error) {
	l.mu.Lock()
	l.starts++
	broken := l.Broken
	l.mu.Unlock()
	if broken {
		return l.Status(ctx), fmt.Errorf("%w: systemd is not enabled in %q", lifecycle.ErrNotReady, l.Distro)
	}
	time.Sleep(50 * time.Millisecond)
	l.Engine.mu.Lock()
	l.Engine.Down = false
	l.Engine.mu.Unlock()
	return l.Status(ctx), nil
}

// Starts 回傳 Start 被呼叫次數。
func (l *FakeLifecycle) Starts() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}

// FakeAutostart 記憶體內 autostart。
type FakeAutostart struct {
	mu sync.Mutex
	on bool
}

// Enabled 實作 Autostart。
func (a *FakeAutostart) Enabled() (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.on, nil
}

// Set 實作 Autostart。
func (a *FakeAutostart) Set(on bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.on = on
	return nil
}

var errUnsupported = errors.New("autostart not supported on this platform")

// UnsupportedAutostart 用於非 Windows。
type UnsupportedAutostart struct{}

// Enabled 實作 Autostart。
func (UnsupportedAutostart) Enabled() (bool, error) { return false, nil }

// Set 實作 Autostart。
func (UnsupportedAutostart) Set(bool) error { return errUnsupported }
