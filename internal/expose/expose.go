// Package expose 管理「曝露」：client 把自己的根目錄以非遞迴 bind 掛到共用的
// /mnt/wsl/distro/<名稱>，讓 wsld engine 以相同的絕對路徑存取 client 的檔案（bind mount 用）。
//
// 掛載會傳播到所有 distro，所以只要有人持有它，那份 VHD 就被接著；因此曝露必須有受管理的生命週期：
// 用不到就放掉（GC）、停止／壓縮／刪除 client 前先放掉、client 已停止卻殘留的掛載一律清掉。
// 卸載一律是一般的 umount（由 engine 或該 distro 執行，會乾淨地傳播給所有 peer），絕不遞迴、不 lazy。
package expose

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/wsl"
)

// Root 是共用的曝露根。
const Root = "/mnt/wsl/distro"

// helper 是 client 內由 bootstrap 安裝的 root 小程式。
const helper = "/usr/local/sbin/wsld-expose"

var nameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// MountPath 是某 distro 的曝露掛載點。
func MountPath(distro string) string { return Root + "/" + distro }

// Engine 是需要的 Docker API 子集（gui.Engine 滿足）。
type Engine interface {
	ListContainers(ctx context.Context, all bool) ([]dockerapi.Container, error)
	Inspect(ctx context.Context, id string) (dockerapi.ContainerDetail, error)
	StopContainer(ctx context.Context, id string, timeoutSec int) error
}

// Using 是引用某個曝露路徑的容器（含已停止的：重新啟動時路徑必須存在）。
type Using struct {
	ID    string
	Name  string
	State string
}

// InUseError 表示仍有容器引用該 distro 的曝露路徑。
type InUseError struct {
	Distro     string
	Containers []Using
}

func (e *InUseError) Error() string {
	names := make([]string, len(e.Containers))
	for i, c := range e.Containers {
		names[i] = c.Name
	}
	return fmt.Sprintf("%s is exposed to the engine and used by %d container(s): %s", e.Distro, len(e.Containers), strings.Join(names, ", "))
}

// Entry 是 Report 的單列。
type Entry struct {
	Distro     string
	Mounted    bool
	Running    bool // 來源 distro 目前是否 Running
	Stale      bool // 掛載存在但來源 distro 不在 Running
	Containers []Using
}

// Manager 綜合 wsl.exe 與 Docker API。
type Manager struct {
	WSL          wsl.Runner
	Engine       Engine
	EngineDistro string // 跑 dockerd 的 distro：在其中以 root 卸載（傳播給所有 peer）
	// Keep：GC 不放掉的 distro（仍在執行時）。k8s-engine 的曝露由它自己的 unit 管理：kubelet 的狀態目錄就在曝露路徑下，
	// pod 還沒建立（沒有容器引用）時被 GC 放掉，kubelet 會把資料寫進 /mnt/wsl 的 tmpfs。
	Keep []string
}

func (m *Manager) keep(name string) bool {
	for _, k := range m.Keep {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func validName(n string) error {
	if !nameRe.MatchString(n) {
		return fmt.Errorf("invalid distro name %q", n)
	}
	return nil
}

// Mounted 回傳目前存在曝露掛載的 distro 名稱（排序）。只認 Root 底下的直接子層，子掛載點不算。
func (m *Manager) Mounted(ctx context.Context) ([]string, error) {
	out, _, err := wsl.RunIn(ctx, m.WSL, m.EngineDistro, "", nil, "findmnt", "-rn", "-o", "TARGET")
	if err != nil {
		return nil, fmt.Errorf("list mounts in %s: %w", m.EngineDistro, err)
	}
	var names []string
	for _, line := range strings.Split(wsl.DecodeOutput(out), "\n") {
		t := strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(t, Root+"/")
		if !ok || rest == "" || strings.Contains(rest, "/") {
			continue
		}
		names = append(names, rest)
	}
	sort.Strings(names)
	return names, nil
}

// Users 回傳每個 distro 被哪些容器引用（容器 bind 來源在該 distro 的曝露路徑底下；含已停止的）。
func (m *Manager) Users(ctx context.Context) (map[string][]Using, error) {
	list, err := m.Engine.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	res := map[string][]Using{}
	for _, c := range list {
		d, err := m.Engine.Inspect(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, mt := range d.Mounts {
			if mt.Type != "bind" {
				continue
			}
			rest, ok := strings.CutPrefix(mt.Source, Root+"/")
			if !ok || rest == "" {
				continue
			}
			name, _, _ := strings.Cut(rest, "/")
			if seen[name] {
				continue
			}
			seen[name] = true
			n := ""
			if len(c.Names) > 0 {
				n = strings.TrimPrefix(c.Names[0], "/")
			}
			res[name] = append(res[name], Using{ID: c.ID, Name: n, State: c.State})
		}
	}
	return res, nil
}

// DistroStoppedError 表示容器引用的來源 distro 已停止（啟動容器前不能把它叫醒：先請使用者啟動它）。
type DistroStoppedError struct{ Distro string }

func (e *DistroStoppedError) Error() string {
	return fmt.Sprintf("distro %s is stopped; start it first so the container can reach its files", e.Distro)
}

// DistrosOf 回傳一個容器的 bind 掛載所引用的、在曝露路徑底下的 distro 名稱（去重、排序）。
func DistrosOf(d dockerapi.ContainerDetail) []string {
	seen := map[string]bool{}
	var out []string
	for _, mt := range d.Mounts {
		if mt.Type != "bind" {
			continue
		}
		rest, ok := strings.CutPrefix(mt.Source, Root+"/")
		if !ok || rest == "" {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// EnsureFor 在啟動一個舊容器之前，補上它引用的曝露（已掛載就什麼都不做）。
// 來源 distro 已停止則回 *DistroStoppedError，不會喚醒它。
func (m *Manager) EnsureFor(ctx context.Context, containerID string, list []wsl.Distro) error {
	d, err := m.Engine.Inspect(ctx, containerID)
	if err != nil {
		return err
	}
	need := DistrosOf(d)
	if len(need) == 0 {
		return nil
	}
	mounted, err := m.Mounted(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, n := range mounted {
		have[n] = true
	}
	for _, n := range need {
		if have[n] {
			continue
		}
		if dd, ok := wsl.FindDistro(list, n); !ok || !strings.EqualFold(dd.State, "Running") {
			return &DistroStoppedError{Distro: n}
		}
		if err := m.Expose(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// Report 彙整曝露狀態：已掛載的（標出殘留）與只被容器引用但未掛載的。
func (m *Manager) Report(ctx context.Context, list []wsl.Distro) ([]Entry, error) {
	mounted, err := m.Mounted(ctx)
	if err != nil {
		return nil, err
	}
	users, err := m.Users(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, n := range mounted {
		names[n] = true
	}
	isMounted := map[string]bool{}
	for _, n := range mounted {
		isMounted[n] = true
	}
	for n := range users {
		names[n] = true
	}
	var all []string
	for n := range names {
		all = append(all, n)
	}
	sort.Strings(all)
	out := make([]Entry, 0, len(all))
	for _, n := range all {
		running := false
		if d, ok := wsl.FindDistro(list, n); ok && strings.EqualFold(d.State, "Running") {
			running = true
		}
		out = append(out, Entry{Distro: n, Mounted: isMounted[n], Running: running, Stale: isMounted[n] && !running, Containers: users[n]})
	}
	return out, nil
}

// Expose 在 distro 內（root）執行固定的 helper 來曝露它（給 GUI 啟動舊容器前補曝露用）。
func (m *Manager) Expose(ctx context.Context, distro string) error {
	if err := validName(distro); err != nil {
		return err
	}
	if _, stderr, err := wsl.RunIn(ctx, m.WSL, distro, "root", nil, helper, "expose"); err != nil {
		return fmt.Errorf("expose %s: %w: %s", distro, err, strings.TrimSpace(wsl.DecodeOutput(stderr)))
	}
	return nil
}

// Release 放掉 distro 的曝露。force=false 時，仍有容器引用就回 *InUseError（呼叫端先停容器或確認）。
// 先在 engine 內以 root 做一般的 umount；engine 不通時退而求其次請該 distro 自己的 helper 卸載。
func (m *Manager) Release(ctx context.Context, distro string, force bool) error {
	if err := validName(distro); err != nil {
		return err
	}
	if !force {
		users, err := m.Users(ctx)
		if err != nil {
			return err
		}
		if u := users[distro]; len(u) > 0 {
			return &InUseError{Distro: distro, Containers: u}
		}
	}
	_, stderr, err := wsl.RunIn(ctx, m.WSL, m.EngineDistro, "root", nil, "umount", MountPath(distro))
	if err == nil {
		return nil
	}
	first := fmt.Errorf("umount %s: %w: %s", MountPath(distro), err, strings.TrimSpace(wsl.DecodeOutput(stderr)))
	if _, _, err2 := wsl.RunIn(ctx, m.WSL, distro, "root", nil, helper, "release"); err2 == nil {
		return nil
	}
	return first
}

// GC 放掉不再需要的曝露：殘留的（來源 distro 不在 Running）一律放；沒有任何容器引用的也放。
// 回傳已放掉的 distro 名稱。沒有任何曝露掛載時不會呼叫 Docker API。
func (m *Manager) GC(ctx context.Context, list []wsl.Distro) ([]string, error) {
	mounted, err := m.Mounted(ctx)
	if err != nil || len(mounted) == 0 {
		return nil, err
	}
	users, err := m.Users(ctx)
	if err != nil {
		return nil, err
	}
	var released []string
	var errs []error
	for _, n := range mounted {
		running := false
		if d, ok := wsl.FindDistro(list, n); ok && strings.EqualFold(d.State, "Running") {
			running = true
		}
		if running && (len(users[n]) > 0 || m.keep(n)) {
			continue
		}
		if err := m.Release(ctx, n, true); err != nil {
			errs = append(errs, err)
			continue
		}
		released = append(released, n)
	}
	return released, errors.Join(errs...)
}

// StopUsers 停止引用該 distro 曝露路徑的執行中容器，回傳停了幾個（停止／壓縮／刪除 client 前使用）。
func (m *Manager) StopUsers(ctx context.Context, distro string) (int, error) {
	if err := validName(distro); err != nil {
		return 0, err
	}
	users, err := m.Users(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users[distro] {
		if u.State != "running" {
			continue
		}
		if err := m.Engine.StopContainer(ctx, u.ID, 10); err != nil {
			return n, fmt.Errorf("stop %s: %w", u.Name, err)
		}
		n++
	}
	return n, nil
}
