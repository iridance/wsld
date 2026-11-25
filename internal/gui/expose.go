package gui

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/expose"
	"github.com/iridance/wsld/internal/wsl"
)

// exposeGCEvery：Supervise 每次輪詢都會經過，曝露的 GC 最多每 30 秒做一次（每次都要問 engine 與 Docker）。
const exposeGCEvery = 30 * time.Second

// exposeMgr 回傳曝露管理器；沒有 WSL、Docker API 或 engine 時回 nil（曝露功能不適用）。
func (s *Server) exposeMgr() *expose.Manager {
	if s.WSL == nil {
		return nil
	}
	eng := s.engine()
	if eng == nil {
		return nil
	}
	em, ok := s.cfg().EngineMachine()
	if !ok {
		return nil
	}
	return &expose.Manager{WSL: s.WSL, Engine: eng, EngineDistro: em.Distro, Keep: exposeKeep(s.cfg())}
}

// exposeKeep：k8s-engine 的曝露由它自己的 unit 管理，GC 不可在它執行中放掉。
func exposeKeep(cfg config.Config) []string {
	if m, ok := cfg.K8sMachine(); ok {
		return []string{m.Distro}
	}
	return nil
}

func (s *Server) setExposed(names []string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	s.exposeMu.Lock()
	s.exposed = set
	s.exposeMu.Unlock()
}

func (s *Server) isExposed(name string) bool {
	s.exposeMu.Lock()
	defer s.exposeMu.Unlock()
	return s.exposed[name]
}

// gcExposure：放掉沒人用的曝露與殘留（來源 distro 已停止）的掛載，並更新 Machines 頁用的快取。
func (s *Server) gcExposure(now time.Time) {
	s.exposeMu.Lock()
	if !s.lastExposeGC.IsZero() && now.Sub(s.lastExposeGC) < exposeGCEvery {
		s.exposeMu.Unlock()
		return
	}
	s.lastExposeGC = now
	s.exposeMu.Unlock()
	m := s.exposeMgr()
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	list, err := wsl.ListDistros(ctx, s.WSL)
	if err != nil {
		return
	}
	_, _ = m.GC(ctx, list)
	if mounted, err := m.Mounted(ctx); err == nil {
		s.setExposed(mounted)
	}
}

// RefreshExposure 立刻重讀曝露掛載並更新快取（不做 GC）；程式啟動與 testhost 用。
func (s *Server) RefreshExposure() {
	m := s.exposeMgr()
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if mounted, err := m.Mounted(ctx); err == nil {
		s.setExposed(mounted)
	}
}

type usingJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

func usingList(u []expose.Using) []usingJSON {
	out := make([]usingJSON, 0, len(u))
	for _, c := range u {
		out = append(out, usingJSON{ID: c.ID, Name: c.Name, State: c.State})
	}
	return out
}

// releaseExposure 在終止／刪除 client 之前放掉它的曝露（沒曝露就什麼都不做）。
// 仍有執行中的容器在用：需 ?release=1（或 ?stop=1）確認，確認後先停這些容器再放掉；否則回 409 in_use 與容器清單。
// 回傳 true 表示已寫出回應（被擋下或失敗），呼叫端應直接 return。
func (s *Server) releaseExposure(w http.ResponseWriter, r *http.Request, name string) bool {
	m := s.exposeMgr()
	if m == nil {
		return false
	}
	ctx := r.Context()
	mounted, err := m.Mounted(ctx)
	if err != nil || !contains(mounted, name) {
		return false
	}
	users, err := m.Users(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return true
	}
	var running []expose.Using
	for _, u := range users[name] {
		if u.State == "running" {
			running = append(running, u)
		}
	}
	q := r.URL.Query()
	confirmed := q.Get("release") == "1" || q.Get("stop") == "1"
	if len(running) > 0 && !confirmed {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      (&expose.InUseError{Distro: name, Containers: running}).Error(),
			"code":       "in_use",
			"containers": usingList(running),
		})
		return true
	}
	if len(running) > 0 {
		if _, err := m.StopUsers(ctx, name); err != nil {
			writeErr(w, http.StatusBadGateway, "error", err)
			return true
		}
	}
	if err := m.Release(ctx, name, true); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return true
	}
	if mounted, err := m.Mounted(ctx); err == nil {
		s.setExposed(mounted)
	}
	return false
}

// releaseStaleBeforeDiskOp：要對一個「已停止」的 distro 壓縮／調整大小之前，把它殘留的曝露放掉（避免同一份 VHD 被接兩次）。
func (s *Server) releaseStaleBeforeDiskOp(ctx context.Context, name string) {
	m := s.exposeMgr()
	if m == nil {
		return
	}
	list, err := wsl.ListDistros(ctx, s.WSL)
	if err != nil {
		return
	}
	if d, ok := wsl.FindDistro(list, name); ok && strings.EqualFold(d.State, "Running") {
		return // 執行中的 distro 做磁碟操作會走 --shutdown，掛載會一併消失
	}
	if mounted, err := m.Mounted(ctx); err == nil && contains(mounted, name) {
		_ = m.Release(ctx, name, true)
		s.setExposed(without(mounted, name))
	}
}

// handleExposeReport GET /api/expose：目前哪些 distro 曝露中、被哪些容器引用、是否殘留。
func (s *Server) handleExposeReport(w http.ResponseWriter, r *http.Request) {
	m := s.exposeMgr()
	if m == nil {
		writeJSON(w, http.StatusOK, map[string]any{"available": false, "entries": []any{}})
		return
	}
	list, err := wsl.ListDistros(r.Context(), s.WSL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	rep, err := m.Report(r.Context(), list)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	type entry struct {
		Distro     string      `json:"distro"`
		Mounted    bool        `json:"mounted"`
		Running    bool        `json:"running"`
		Stale      bool        `json:"stale"`
		Containers []usingJSON `json:"containers"`
	}
	out := make([]entry, 0, len(rep))
	for _, e := range rep {
		out = append(out, entry{Distro: e.Distro, Mounted: e.Mounted, Running: e.Running, Stale: e.Stale, Containers: usingList(e.Containers)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"available": true, "entries": out})
}

// handleExposeRelease POST /api/machines/{name}/expose/release：手動釋放（?stop=1 一併停掉用到它的容器）。
func (s *Server) handleExposeRelease(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !wsl.ValidDistroName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid distro name"))
		return
	}
	if s.exposeMgr() == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("exposure is not available"))
		return
	}
	if s.releaseExposure(w, r, name) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ensureExposedForStart 在啟動舊容器之前補曝露它引用的 client。回傳 true 表示已寫出回應。
func (s *Server) ensureExposedForStart(w http.ResponseWriter, r *http.Request, id string) bool {
	m := s.exposeMgr()
	if m == nil {
		return false
	}
	d, err := s.engine().Inspect(r.Context(), id)
	if err != nil || len(expose.DistrosOf(d)) == 0 {
		return false // 沒引用曝露路徑（或查不到）：照原本流程，讓 StartContainer 自己報錯
	}
	list, err := wsl.ListDistros(r.Context(), s.WSL)
	if err != nil {
		return false
	}
	if err := m.EnsureFor(r.Context(), id, list); err != nil {
		var stopped *expose.DistroStoppedError
		if errors.As(err, &stopped) {
			writeErr(w, http.StatusConflict, "distro_stopped", err)
			return true
		}
		writeErr(w, http.StatusBadGateway, "error", err)
		return true
	}
	return false
}

func contains(list []string, n string) bool {
	for _, x := range list {
		if x == n {
			return true
		}
	}
	return false
}

func without(list []string, n string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if x != n {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
