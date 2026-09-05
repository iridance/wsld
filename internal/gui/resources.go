package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/irice/wsl-docker/internal/dockerapi"
	"github.com/irice/wsl-docker/internal/wsl"
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
}

// ---- images -----------------------------------------------------------------

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.ListImages(r.Context())
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
	if err := s.Engine.RemoveImage(r.Context(), r.PathValue("ref"), r.URL.Query().Get("force") == "1"); err != nil {
		writeEngineErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleImagesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Engine.PruneImages(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- volumes ----------------------------------------------------------------

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.ListVolumes(r.Context())
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
	if err := s.Engine.RemoveVolume(r.Context(), r.PathValue("name"), r.URL.Query().Get("force") == "1"); err != nil {
		writeEngineErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleVolumesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Engine.PruneVolumes(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- networks ---------------------------------------------------------------

func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	v, err := s.Engine.ListNetworks(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if v == nil {
		v = []dockerapi.Network{}
	}
	writeJSON(w, http.StatusOK, v)
}

// ---- machines（本機 WSL 發行版）--------------------------------------------

// Machine 是 /api/machines 的一列。
type Machine struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Version int    `json:"version"`
	Default bool   `json:"default"`
	Active  bool   `json:"active"` // 為 config.toml 的 distro（wsld 管理的機器）
}

// MachinesResponse 是 /api/machines 回應；Available=false 表示此環境沒有 wsl.exe（例如測試主機未注入）。
type MachinesResponse struct {
	Available bool      `json:"available"`
	Machines  []Machine `json:"machines"`
}

// MachineConfig 是 /api/machines/{name}/config 回應。
type MachineConfig struct {
	Name    string `json:"name"`
	WSLConf string `json:"wsl_conf"` // /etc/wsl.conf 原文；不存在為空字串
	Systemd bool   `json:"systemd"`  // 由 wsl.conf [boot] systemd=true 判定
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
	active := s.cfg().Distro
	out := make([]Machine, 0, len(list))
	for _, d := range list {
		out = append(out, Machine{Name: d.Name, State: d.State, Version: d.Version, Default: d.Default, Active: strings.EqualFold(d.Name, active)})
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
	// cat 失敗（檔案不存在）視為空設定；其他錯誤（distro 無法啟動）也只回空字串，避免 UI 卡死。
	out, _, err := wsl.RunIn(r.Context(), s.WSL, d.Name, "root", nil, "cat", "/etc/wsl.conf")
	conf := ""
	if err == nil {
		conf = wsl.DecodeOutput(out)
	}
	writeJSON(w, http.StatusOK, MachineConfig{Name: d.Name, WSLConf: conf, Systemd: wslConfSystemd(conf)})
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
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "usage", err)
		return
	}
	if !wsl.ValidDistroName(body.Name) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New("invalid distro name"))
		return
	}
	if err := wsl.Install(r.Context(), s.WSL, body.Name); err != nil {
		writeErr(w, http.StatusBadGateway, "error", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": body.Name})
}
