package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/lifecycle"
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
	// ActionDelay 讓 Start/Stop 延遲回應（模擬 docker stop 寬限期）。
	ActionDelay time.Duration
	// listHold：下一次 ListImages 先快照後阻塞，直到 ReleaseListHold（測刷新競態）。
	listHold        chan struct{}
	listHoldArmed   bool
	listHoldWaiting bool

	mounts   map[string][]dockerapi.ContainerMount // 容器 id → inspect 的 Mounts（測 bind 曝露用）
	images   []dockerapi.Image
	volumes  map[string]*fakeVolume
	networks []dockerapi.Network
}

type fakeVolume struct {
	dockerapi.Volume
	InUse bool
	Size  int64 // /system/df UsageData.Size
}

// SetActionDelay 設定 Start/Stop 的延遲。
func (f *FakeEngine) SetActionDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ActionDelay = d
}

// ArmListHold 讓下一次 ListImages 快照後阻塞，直到 ReleaseListHold。
func (f *FakeEngine) ArmListHold() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listHold != nil {
		close(f.listHold)
	}
	f.listHold = make(chan struct{})
	f.listHoldArmed = true
}

// ReleaseListHold 放行被 ArmListHold 卡住的 ListImages。
func (f *FakeEngine) ReleaseListHold() {
	f.mu.Lock()
	ch := f.listHold
	f.listHold = nil
	f.listHoldArmed = false
	f.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// ListHoldWaiting 表示目前有 ListImages 卡在 hold 上。
func (f *FakeEngine) ListHoldWaiting() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listHoldWaiting
}

func (f *FakeEngine) sleepAction(ctx context.Context) {
	f.mu.Lock()
	d := f.ActionDelay
	f.mu.Unlock()
	if d <= 0 {
		return
	}
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
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
	f.mounts = nil
	f.logs = map[string][]string{}
	f.subs = map[string][]chan string{}
	f.execEcho = true
	f.LastResize = [2]int{}
	f.ActionDelay = 0
	if f.listHold != nil {
		close(f.listHold)
	}
	f.listHold = nil
	f.listHoldArmed = false
	f.listHoldWaiting = false
	f.mu.Unlock()
	// dual-stack 發佈埠會回傳 IPv4 與 IPv6 兩筆，前端須去重
	f.AddContainer("web", "nginx:1.27", "running", []dockerapi.Port{
		{IP: "0.0.0.0", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
		{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
	})
	f.AddContainer("db", "postgres:16", "exited", nil)
	f.AppendLog("web", "nginx started", "GET / 200")

	f.mu.Lock()
	f.images = []dockerapi.Image{
		{ID: "sha256:aaa111aaa111aaa111", RepoTags: []string{"nginx:1.27"}, Created: 1757000000, Size: 48_000_000, Containers: 1},
		{ID: "sha256:bbb222bbb222bbb222", RepoTags: []string{"redis:7"}, Created: 1756000000, Size: 41_000_000, Containers: 0},
		{ID: "sha256:ccc333ccc333ccc333", RepoTags: nil, Created: 1755000000, Size: 9_000_000, Containers: 0},
	}
	f.volumes = map[string]*fakeVolume{}
	f.networks = []dockerapi.Network{
		{ID: "n1", Name: "bridge", Driver: "bridge", Scope: "local", Created: "2026-09-01T00:00:00Z"},
		{ID: "n2", Name: "host", Driver: "host", Scope: "local", Created: "2026-09-01T00:00:00Z"},
		{ID: "n3", Name: "none", Driver: "null", Scope: "local", Created: "2026-09-01T00:00:00Z"},
		{ID: "n4", Name: "shop_default", Driver: "bridge", Scope: "local", Created: "2026-09-02T00:00:00Z", Labels: map[string]string{"com.docker.compose.project": "shop"}},
	}
	f.mu.Unlock()
	f.AddVolume("pgdata", true)
	f.AddVolume("orphan", false)
	f.mu.Lock()
	f.volumes["pgdata"].Size = 120_000_000
	f.volumes["orphan"].Size = 5_000_000
	f.mu.Unlock()
}

// AddVolume 新增 volume；inUse=true 時 Remove/Prune 會拒絕。
func (f *FakeEngine) AddVolume(name string, inUse bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumes[name] = &fakeVolume{Volume: dockerapi.Volume{
		Name: name, Driver: "local", Scope: "local", Mountpoint: "/var/lib/docker/volumes/" + name + "/_data", CreatedAt: "2026-09-03T00:00:00Z",
	}, InUse: inUse, Size: 1_000_000}
}

// AddImage 新增映像；containers 為使用中的容器數。
func (f *FakeEngine) AddImage(tag string, size int64, containers int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var tags []string
	if tag != "" {
		tags = []string{tag}
	}
	f.images = append(f.images, dockerapi.Image{ID: fmt.Sprintf("sha256:%012x%012x", len(f.images)+1, size), RepoTags: tags, Created: 1754000000, Size: size, Containers: containers})
}

// AddNetwork 新增使用者網路；inUse 時帶一個假 endpoint（不可刪／prune）。
func (f *FakeEngine) AddNetwork(name, driver string, inUse bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := dockerapi.Network{ID: fmt.Sprintf("n%d", len(f.networks)+1), Name: name, Driver: driver, Scope: "local", Created: "2026-09-04T00:00:00Z"}
	if inUse {
		n.Containers = map[string]json.RawMessage{"c1": json.RawMessage(`{}`)}
	}
	f.networks = append(f.networks, n)
}

func conflict(msg string) error {
	return &dockerapi.APIError{Status: 409, Message: msg}
}

// ListImages 實作 Resources。
func (f *FakeEngine) ListImages(ctx context.Context) ([]dockerapi.Image, error) {
	f.mu.Lock()
	if err := f.down(); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	out := append([]dockerapi.Image{}, f.images...)
	var hold <-chan struct{}
	if f.listHoldArmed {
		hold = f.listHold
		f.listHoldArmed = false
		f.listHoldWaiting = true
	}
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			f.mu.Lock()
			f.listHoldWaiting = false
			f.mu.Unlock()
			return nil, ctx.Err()
		}
		f.mu.Lock()
		f.listHoldWaiting = false
		f.mu.Unlock()
	}
	return out, nil
}

// RemoveImage 實作 Resources：ref 比對 tag、ID 或短 ID。
func (f *FakeEngine) RemoveImage(_ context.Context, ref string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return err
	}
	for i, img := range f.images {
		if img.Name() == ref || img.ID == ref || img.ShortID() == ref || strings.TrimPrefix(img.ID, "sha256:") == ref {
			if img.Containers > 0 && !force {
				return conflict("conflict: unable to remove repository reference " + ref + " (must force) - container is using its referenced image")
			}
			f.images = append(f.images[:i], f.images[i+1:]...)
			return nil
		}
	}
	return &dockerapi.APIError{Status: 404, Message: "No such image: " + ref}
}

// PruneImages 實作 Resources：移除 Containers==0 的映像。
func (f *FakeEngine) PruneImages(context.Context) (dockerapi.PruneReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return dockerapi.PruneReport{}, err
	}
	var kept []dockerapi.Image
	rep := dockerapi.PruneReport{}
	for _, img := range f.images {
		if img.Containers == 0 {
			rep.Count++
			rep.SpaceReclaimed += img.Size
			continue
		}
		kept = append(kept, img)
	}
	f.images = kept
	return rep, nil
}

// ListVolumes 實作 Resources（依名稱排序）。
func (f *FakeEngine) ListVolumes(context.Context) ([]dockerapi.Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return nil, err
	}
	out := []dockerapi.Volume{}
	for _, v := range f.volumes {
		out = append(out, v.Volume)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RemoveVolume 實作 Resources。
func (f *FakeEngine) RemoveVolume(_ context.Context, name string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return err
	}
	v, ok := f.volumes[name]
	if !ok {
		return &dockerapi.APIError{Status: 404, Message: "get " + name + ": no such volume"}
	}
	if v.InUse && !force {
		return conflict("remove " + name + ": volume is in use")
	}
	delete(f.volumes, name)
	return nil
}

// PruneVolumes 實作 Resources：移除未使用的 volume。
func (f *FakeEngine) PruneVolumes(context.Context) (dockerapi.PruneReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return dockerapi.PruneReport{}, err
	}
	rep := dockerapi.PruneReport{}
	for name, v := range f.volumes {
		if !v.InUse {
			delete(f.volumes, name)
			rep.Count++
			rep.SpaceReclaimed += 1024
		}
	}
	return rep, nil
}

// ListNetworks 實作 Resources。
func (f *FakeEngine) ListNetworks(context.Context) ([]dockerapi.Network, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return nil, err
	}
	return append([]dockerapi.Network{}, f.networks...), nil
}

// RemoveNetwork 實作 Resources：內建網路與使用中不可刪。
func (f *FakeEngine) RemoveNetwork(_ context.Context, idOrName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return err
	}
	for i, n := range f.networks {
		if n.Name != idOrName && n.ID != idOrName {
			continue
		}
		if n.Builtin() {
			return &dockerapi.APIError{Status: 403, Message: "operation not supported for predefined " + n.Name + " network"}
		}
		if n.ContainerCount() > 0 {
			return conflict("network " + n.Name + " has active endpoints")
		}
		f.networks = append(f.networks[:i], f.networks[i+1:]...)
		return nil
	}
	return &dockerapi.APIError{Status: 404, Message: "network " + idOrName + " not found"}
}

// PruneNetworks 實作 Resources：移除未使用的非內建網路。
func (f *FakeEngine) PruneNetworks(context.Context) (dockerapi.PruneReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return dockerapi.PruneReport{}, err
	}
	var kept []dockerapi.Network
	rep := dockerapi.PruneReport{}
	for _, n := range f.networks {
		if n.Builtin() || n.ContainerCount() > 0 {
			kept = append(kept, n)
			continue
		}
		rep.Count++
	}
	f.networks = kept
	return rep, nil
}

// SetBinds 讓名為 name 的容器帶有指定來源的 bind 掛載（inspect 的 Mounts）。
func (f *FakeEngine) SetBinds(name string, sources ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.findLocked(name)
	if id == "" {
		return
	}
	if f.mounts == nil {
		f.mounts = map[string][]dockerapi.ContainerMount{}
	}
	var ms []dockerapi.ContainerMount
	for i, src := range sources {
		ms = append(ms, dockerapi.ContainerMount{Type: "bind", Source: src, Destination: fmt.Sprintf("/mnt%d", i), RW: true})
	}
	f.mounts[id] = ms
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

// AddComposeContainer 新增帶 compose 標籤的容器（名稱依 compose 慣例 <project>-<service>-1）。
func (f *FakeEngine) AddComposeContainer(project, service, image, state string) string {
	id := f.AddContainer(project+"-"+service+"-1", image, state, nil)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.containers[id].Labels = map[string]string{
		dockerapi.LabelComposeProject: project,
		dockerapi.LabelComposeService: service,
	}
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
// ServerVersion 回固定版本，讓 Machines 頁的 engine card 有版本可顯示。
func (f *FakeEngine) ServerVersion(context.Context) (dockerapi.Version, error) {
	return dockerapi.Version{Version: "27.3.1"}, nil
}

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
func (f *FakeEngine) StartContainer(ctx context.Context, id string) error {
	f.sleepAction(ctx)
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
func (f *FakeEngine) StopContainer(ctx context.Context, id string, _ int) error {
	f.sleepAction(ctx)
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

// RemoveContainer 實作 Engine。
func (f *FakeEngine) RemoveContainer(ctx context.Context, id string, force bool) error {
	f.sleepAction(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return err
	}
	cid := f.findLocked(id)
	c := f.containers[cid]
	if c == nil {
		return &dockerapi.APIError{Status: 404, Message: "No such container: " + id}
	}
	if c.State == "running" && !force {
		return &dockerapi.APIError{Status: 409, Message: "container is running: stop before remove or use force"}
	}
	delete(f.containers, cid)
	delete(f.logs, cid)
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
	d.Created = time.Unix(c.Created, 0).UTC().Format(time.RFC3339Nano)
	if d.State.Running {
		d.State.StartedAt = d.Created
	}
	d.Config.Cmd = []string{"nginx", "-g", "daemon off;"}
	d.Config.Labels = c.Labels
	d.HostConfig.RestartPolicy.Name = "unless-stopped"
	d.NetworkSettings.Networks = map[string]dockerapi.ContainerNetworkEndpoint{
		"bridge": {IPAddress: "172.17.0.2"},
	}
	if len(c.Ports) > 0 {
		d.NetworkSettings.Ports = map[string][]dockerapi.PortBinding{}
		for _, p := range c.Ports {
			key := fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
			d.NetworkSettings.Ports[key] = []dockerapi.PortBinding{{HostIP: p.IP, HostPort: strconv.Itoa(int(p.PublicPort))}}
		}
	}
	d.Mounts = []dockerapi.ContainerMount{{Type: "bind", Source: "/home/irice/app", Destination: "/app", Mode: "rw", RW: true}}
	if ms, ok := f.mounts[c.ID]; ok {
		d.Mounts = ms // SetBinds 指定的掛載（測曝露用）
	}
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
	f.mu.Lock()
	echo := f.execEcho
	f.mu.Unlock()
	return OpenFakeShell(echo), nil
}

// OpenFakeShell 開啟回顯假 shell（容器 exec 與 machine terminal 共用）。
func OpenFakeShell(echo bool) net.Conn {
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
					if echo {
						_, _ = server.Write([]byte{b})
					}
				}
			}
		}
	}()
	return client
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

// ResetStarts 歸零 Start 次數（testhost /__test/reset 用）。
func (l *FakeLifecycle) ResetStarts() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.starts = 0
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

// fakeContainerSizeRw 依狀態給固定可寫層大小（執行中 2 MB、其他 1 MB）。
func fakeContainerSizeRw(state string) int64 {
	if state == "running" {
		return 2_000_000
	}
	return 1_000_000
}

// DiskUsage 實作 Resources：以假資料組出 /system/df（映像不共用層、固定建置快取）。
func (f *FakeEngine) DiskUsage(context.Context) (dockerapi.DiskUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.down(); err != nil {
		return dockerapi.DiskUsage{}, err
	}
	du := dockerapi.DiskUsage{
		Images:     []dockerapi.DFImage{},
		Containers: []dockerapi.DFContainer{},
		Volumes:    []dockerapi.DFVolume{},
		BuildCache: []dockerapi.DFBuildCache{
			{ID: "bc1", Size: 30_000_000, InUse: false},
			{ID: "bc2", Size: 10_000_000, InUse: true},
		},
	}
	imgSize := map[string]int64{}
	for _, im := range f.images {
		du.LayersSize += im.Size
		du.Images = append(du.Images, dockerapi.DFImage{ID: im.ID, Size: im.Size, Containers: im.Containers})
		for _, tag := range im.RepoTags {
			imgSize[tag] = im.Size
		}
	}
	for _, c := range f.containers {
		rw := fakeContainerSizeRw(c.State)
		du.Containers = append(du.Containers, dockerapi.DFContainer{ID: c.ID, Names: c.Names, State: c.State, SizeRw: rw, SizeRootFs: rw + imgSize[c.Image]})
	}
	sort.Slice(du.Containers, func(i, j int) bool { return du.Containers[i].ID < du.Containers[j].ID })
	for _, v := range f.volumes {
		dv := dockerapi.DFVolume{Name: v.Name}
		dv.UsageData.Size = v.Size
		if v.InUse {
			dv.UsageData.RefCount = 1
		}
		du.Volumes = append(du.Volumes, dv)
	}
	sort.Slice(du.Volumes, func(i, j int) bool { return du.Volumes[i].Name < du.Volumes[j].Name })
	return du, nil
}
