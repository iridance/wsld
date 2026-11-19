package gui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/i18n"
	"github.com/iridance/wsld/internal/lan"
	"github.com/iridance/wsld/internal/wsl"
)

// Resources 是 Engine 的資源子集（images／volumes／networks），*dockerapi.Client 滿足。
type Resources interface {
	ListImages(ctx context.Context) ([]dockerapi.Image, error)
	RemoveImage(ctx context.Context, ref string, force bool) error
	PruneImages(ctx context.Context) (dockerapi.PruneReport, error)
	ListVolumes(ctx context.Context) ([]dockerapi.Volume, error)
	RemoveVolume(ctx context.Context, name string, force bool) error
	PruneVolumes(ctx context.Context) (dockerapi.PruneReport, error)
	ListNetworks(ctx context.Context) ([]dockerapi.Network, error)
	RemoveNetwork(ctx context.Context, idOrName string) error
	PruneNetworks(ctx context.Context) (dockerapi.PruneReport, error)
}

// ---- images -----------------------------------------------------------------

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	v, err := s.engine().ListImages(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if v == nil {
		v = []dockerapi.Image{}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleImageRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.engine().RemoveImage(r.Context(), r.PathValue("ref"), r.URL.Query().Get("force") == "1"); err != nil {
		writeEngineErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleImagesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.engine().PruneImages(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- volumes ----------------------------------------------------------------

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	v, err := s.engine().ListVolumes(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if v == nil {
		v = []dockerapi.Volume{}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleVolumeRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.engine().RemoveVolume(r.Context(), r.PathValue("name"), r.URL.Query().Get("force") == "1"); err != nil {
		writeEngineErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleVolumesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.engine().PruneVolumes(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- networks ---------------------------------------------------------------

func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	v, err := s.engine().ListNetworks(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if v == nil {
		v = []dockerapi.Network{}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleNetworkRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.engine().RemoveNetwork(r.Context(), r.PathValue("id")); err != nil {
		writeEngineErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNetworksPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.engine().PruneNetworks(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- machines（本機 WSL 發行版）--------------------------------------------

// Machine 是 /api/machines 的一列。
type Machine struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Version     int    `json:"version"`
	Default     bool   `json:"default"`
	Active      bool   `json:"active"`
	Managed     bool   `json:"managed"`
	Ready       bool   `json:"ready"`
	APIEndpoint string `json:"api_endpoint,omitempty"`
}

// MachinesResponse 是 /api/machines 回應；Available=false 表示此環境沒有 wsl.exe（例如測試主機未注入）。
type MachinesResponse struct {
	Available bool      `json:"available"`
	Machines  []Machine `json:"machines"`
}

// MachineConfig 是 /api/machines/{name}/config 回應。
type MachineConfig struct {
	Name          string  `json:"name"`
	WSLConf       string  `json:"wsl_conf"`
	Systemd       bool    `json:"systemd"`
	APIEndpoint   string  `json:"api_endpoint,omitempty"`
	User          string  `json:"user,omitempty"`
	Managed       bool    `json:"managed"`
	Ready         bool    `json:"ready"`
	Memory        string  `json:"memory,omitempty"`     // .wslconfig
	Processors    string  `json:"processors,omitempty"` // .wslconfig
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMB  int64   `json:"memory_used_mb"`
	MemoryTotalMB int64   `json:"memory_total_mb"`
}

func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request) {
	if s.WSL == nil {
		writeJSON(w, http.StatusOK, MachinesResponse{Machines: []Machine{}})
		return
	}
	list, err := wsl.ListDistros(r.Context(), s.WSL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	cfg := s.cfg()
	out := make([]Machine, 0, len(list))
	for _, d := range list {
		m := Machine{Name: d.Name, State: d.State, Version: d.Version, Default: d.Default, Active: strings.EqualFold(d.Name, cfg.Active)}
		if pm, ok := cfg.FindMachine(d.Name); ok {
			m.Managed = true
			m.Ready = pm.Ready()
			m.APIEndpoint = pm.APIEndpoint
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, MachinesResponse{Available: true, Machines: out})
}

func (s *Server) handleMachineConfig(w http.ResponseWriter, r *http.Request) {
	if s.WSL == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("wsl unavailable"))
		return
	}
	name := r.PathValue("name")
	list, err := wsl.ListDistros(r.Context(), s.WSL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	d, ok := wsl.FindDistro(list, name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("distro not found: "+name))
		return
	}
	out, _, err := wsl.RunIn(r.Context(), s.WSL, d.Name, "root", nil, "cat", "/etc/wsl.conf")
	conf := ""
	if err == nil {
		conf = wsl.DecodeOutput(out)
	}
	mc := MachineConfig{Name: d.Name, WSLConf: conf, Systemd: wslConfSystemd(conf)}
	if pm, ok := s.cfg().FindMachine(d.Name); ok {
		mc.Managed = true
		mc.Ready = pm.Ready()
		mc.APIEndpoint = pm.APIEndpoint
		mc.User = pm.User
	}
	mem, procs := readWSLConfigLimits()
	mc.Memory, mc.Processors = mem, procs
	if strings.EqualFold(d.State, "Running") {
		cpu, used, total := wsl.SampleDistroUsage(r.Context(), s.WSL, d.Name)
		mc.CPUPercent = cpu
		mc.MemoryUsedMB = int64(used)
		mc.MemoryTotalMB = int64(total)
	}
	writeJSON(w, http.StatusOK, mc)
}

// wslConfSystemd 讀 wsl.conf 的 [boot] systemd 設定。
func wslConfSystemd(conf string) bool {
	section := ""
	for _, line := range strings.Split(conf, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.ToLower(strings.Trim(t, "[]"))
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok || section != "boot" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "systemd") {
			return strings.EqualFold(strings.TrimSpace(v), "true")
		}
	}
	return false
}

func (s *Server) handleMachinesOnline(w http.ResponseWriter, r *http.Request) {
	if s.WSL == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("wsl unavailable"))
		return
	}
	list, err := wsl.ListOnline(r.Context(), s.WSL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	type item struct {
		Name     string `json:"name"`
		Friendly string `json:"friendly"`
	}
	out := make([]item, 0, len(list))
	for _, d := range list {
		out = append(out, item{d.Name, d.Friendly})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMachineInstall(w http.ResponseWriter, r *http.Request) {
	if s.WSL == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("wsl unavailable"))
		return
	}
	var body struct {
		// Distribution 為線上目錄名稱（select）；Name 為本機自訂名稱（--name）。
		Distribution string `json:"distribution"`
		Name         string `json:"name"`
		// User／Password：安裝後建立的 Linux 使用者（空 User = 舊行為，留給「設為主要」建立）。
		// Password 只轉交 chpasswd（stdin），不記錄、不寫入設定。
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	dist := body.Distribution
	if dist == "" {
		dist = body.Name // 舊客戶端只傳 name
	}
	local := body.Name
	if local == "" {
		local = dist
	}
	if !wsl.ValidDistroName(dist) || !wsl.ValidDistroName(local) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid distro name"))
		return
	}
	user := strings.TrimSpace(body.User)
	if user != "" && !wsl.ValidLinuxUsername(user) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("gui.machines.user_invalid")))
		return
	}
	if !wsl.ValidLinuxPassword(body.Password) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("gui.machines.password_invalid")))
		return
	}
	done, ok := s.trackInstall(r.Context(), dist, local)
	if !ok {
		writeErr(w, http.StatusConflict, "conflict", errors.New("another distro install is in progress"))
		return
	}
	defer done()
	if err := wsl.Install(r.Context(), s.WSL, dist, local); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	if user != "" {
		// --no-launch 跳過 OOBE：在此建立使用者、設密碼並設為預設登入者
		if err := wsl.EnsureUser(r.Context(), s.WSL, local, user, body.Password); err != nil {
			writeErr(w, http.StatusBadGateway, "error", fmt.Errorf("%s: %w", i18n.T("gui.machines.user_failed", local), err))
			return
		}
		if err := wsl.SetDefaultUser(r.Context(), s.WSL, local, user); err != nil {
			writeErr(w, http.StatusBadGateway, "error", fmt.Errorf("%s: %w", i18n.T("gui.machines.user_failed", local), err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": local, "distribution": dist,
		"user": user, "password_set": user != "" && body.Password != "",
	})
}

// handleInstallDefaults 提供新增機器對話框的預設值（Linux 使用者名稱依目前 Windows 使用者）。
func (s *Server) handleInstallDefaults(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"user": wsl.SuggestLinuxUser()})
}

func (s *Server) handleMachineUse(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !wsl.ValidDistroName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid distro name"))
		return
	}
	cfg := s.cfg()
	if m, ok := cfg.FindMachine(name); ok {
		_ = cfg.SetActive(m.Distro)
	} else {
		cfg.EnsureActive(config.Machine{Distro: name})
	}
	if s.SaveConfig == nil {
		writeErr(w, http.StatusInternalServerError, "error", errors.New("config not writable"))
		return
	}
	if err := s.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	s.BindActive(cfg)

	m, _ := cfg.ActiveMachine()
	needBringUp := !m.Ready()
	if !needBringUp && s.engine() != nil {
		if err := s.engine().Ping(r.Context()); err != nil {
			needBringUp = true
		}
	}
	if needBringUp {
		// 首次設為主要，或 endpoint／API 不通：Init（重選可用埠）+ Start。
		s.startMu.Lock()
		if s.starting {
			s.startMu.Unlock()
			writeJSON(w, http.StatusAccepted, map[string]any{"active": cfg.Active, "ready": false, "starting": true})
			return
		}
		s.starting = true
		s.startMu.Unlock()
		defer func() {
			s.startMu.Lock()
			s.starting = false
			s.startMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if _, err := s.bringUpActive(ctx); err != nil {
			writeEngineErr(w, err)
			return
		}
		cfg = s.cfg()
		m, _ = cfg.ActiveMachine()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"active":       cfg.Active,
		"ready":        m.Ready(),
		"api_endpoint": m.APIEndpoint,
	})
}

// findDistro 驗證名稱並在 wsl --list 中找到 distro；找不到時已寫好錯誤回應。
func (s *Server) findDistro(w http.ResponseWriter, r *http.Request) (wsl.Distro, bool) {
	name := r.PathValue("name")
	if !wsl.ValidDistroName(name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid distro name"))
		return wsl.Distro{}, false
	}
	if s.WSL == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("wsl unavailable"))
		return wsl.Distro{}, false
	}
	list, err := wsl.ListDistros(r.Context(), s.WSL)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return wsl.Distro{}, false
	}
	d, ok := wsl.FindDistro(list, name)
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("distro not found: "+name))
		return wsl.Distro{}, false
	}
	return d, true
}

// handleMachineStart 啟動 distro（wsl -d <name> -- true）。已納管的 distro 由 systemd 自動帶起 docker.service。
func (s *Server) handleMachineStart(w http.ResponseWriter, r *http.Request) {
	d, ok := s.findDistro(w, r)
	if !ok {
		return
	}
	if err := wsl.Wake(r.Context(), s.WSL, d.Name); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	if fl, ok := s.life().(*FakeLifecycle); ok && fl.Engine != nil && strings.EqualFold(d.Name, s.cfg().Active) {
		fl.Engine.SetDown(false) // testhost：主要 distro 開機 → 引擎跟著起來
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMachineStop 停止 distro（wsl --terminate）。已納管的先 systemctl stop docker，讓容器正常結束。
func (s *Server) handleMachineStop(w http.ResponseWriter, r *http.Request) {
	d, ok := s.findDistro(w, r)
	if !ok {
		return
	}
	if !strings.EqualFold(d.State, "Running") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if _, managed := s.cfg().FindMachine(d.Name); managed {
		_ = wsl.StopDocker(r.Context(), s.WSL, d.Name)
	}
	if err := wsl.Terminate(r.Context(), s.WSL, d.Name); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	if fl, ok := s.life().(*FakeLifecycle); ok && fl.Engine != nil && strings.EqualFold(d.Name, s.cfg().Active) {
		fl.Engine.SetDown(true) // testhost：主要 distro 停止 → 引擎斷線
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMachineUnmanage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg := s.cfg()
	if _, ok := cfg.FindMachine(name); !ok {
		writeErr(w, http.StatusNotFound, "not_found", errors.New("machine not managed: "+name))
		return
	}
	if s.WSL != nil {
		_ = wsl.StopDocker(r.Context(), s.WSL, name)
	}
	cfg.RemoveMachine(name)
	if s.SaveConfig == nil {
		writeErr(w, http.StatusInternalServerError, "error", errors.New("config not writable"))
		return
	}
	if err := s.SaveConfig(cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "error", err)
		return
	}
	s.BindActive(cfg)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMachineUnregister(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Confirm string `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Confirm == "" || body.Confirm != name {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("confirm must equal distro name"))
		return
	}
	if s.WSL == nil {
		writeErr(w, http.StatusNotImplemented, "error", errors.New("wsl unavailable"))
		return
	}
	cfg := s.cfg()
	_ = wsl.StopDocker(r.Context(), s.WSL, name)
	if err := wsl.Unregister(r.Context(), s.WSL, name); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	cfg.RemoveMachine(name)
	if s.SaveConfig != nil {
		if err := s.SaveConfig(cfg); err != nil {
			writeErr(w, http.StatusInternalServerError, "error", err)
			return
		}
	}
	s.BindActive(cfg)
	w.WriteHeader(http.StatusNoContent)
}

// readWSLConfigLimits 讀取使用者家目錄的 .wslconfig memory／processors（沒有則空）。
func readWSLConfigLimits() (memory, processors string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".wslconfig"))
	if err != nil {
		return "", ""
	}
	return lan.GetKey(string(data), "wsl2", "memory"), lan.GetKey(string(data), "wsl2", "processors")
}
