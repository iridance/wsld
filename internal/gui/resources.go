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
	DiskUsage(ctx context.Context) (dockerapi.DiskUsage, error)
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
	s.invalidateDisk()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleImagesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.engine().PruneImages(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	s.invalidateDisk()
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
	s.invalidateDisk()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleVolumesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.engine().PruneVolumes(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	s.invalidateDisk()
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
	s.invalidateDisk()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNetworksPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.engine().PruneNetworks(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	s.invalidateDisk()
	writeJSON(w, http.StatusOK, rep)
}

// ---- disk usage（docker system df）----------------------------------------

// defaultDiskCacheTTL：/system/df 會對 volume 跑 du，避免每次列表刷新都重算。
const defaultDiskCacheTTL = 15 * time.Second

// ContainerDisk 是單一容器的大小（SizeRw＝可寫層；SizeRootFs＝含映像的虛擬大小）。
type ContainerDisk struct {
	SizeRw     int64 `json:"size_rw"`
	SizeRootFs int64 `json:"size_root_fs"`
}

// VolumeDisk 是單一 volume 的大小；Size == -1 表示 daemon 未統計。
type VolumeDisk struct {
	Size     int64 `json:"size"`
	RefCount int64 `json:"ref_count"`
}

// DiskResponse 是 /api/disk 回應：四類摘要＋逐容器／逐 volume 大小。
type DiskResponse struct {
	Summary    dockerapi.DiskSummary    `json:"summary"`
	Containers map[string]ContainerDisk `json:"containers"`
	Volumes    map[string]VolumeDisk    `json:"volumes"`
}

func diskResponse(du dockerapi.DiskUsage) DiskResponse {
	out := DiskResponse{
		Summary:    du.Summarize(),
		Containers: make(map[string]ContainerDisk, len(du.Containers)),
		Volumes:    make(map[string]VolumeDisk, len(du.Volumes)),
	}
	for _, c := range du.Containers {
		out.Containers[c.ID] = ContainerDisk{SizeRw: c.SizeRw, SizeRootFs: c.SizeRootFs}
	}
	for _, v := range du.Volumes {
		out.Volumes[v.Name] = VolumeDisk{Size: v.UsageData.Size, RefCount: v.UsageData.RefCount}
	}
	return out
}

func (s *Server) diskTTL() time.Duration {
	if s.DiskCacheTTL == 0 {
		return defaultDiskCacheTTL
	}
	return s.DiskCacheTTL
}

// invalidateDisk 讓下一次 /api/disk 重新查詢（刪除／prune／啟停後呼叫）。
func (s *Server) invalidateDisk() {
	s.diskMu.Lock()
	s.diskAt = time.Time{}
	s.diskFor = nil
	s.diskMu.Unlock()
}

func (s *Server) handleDisk(w http.ResponseWriter, r *http.Request) {
	eng := s.engine()
	s.diskMu.Lock()
	if s.diskFor == eng && !s.diskAt.IsZero() && time.Since(s.diskAt) < s.diskTTL() {
		v := s.diskVal
		s.diskMu.Unlock()
		writeJSON(w, http.StatusOK, v)
		return
	}
	s.diskMu.Unlock()
	du, err := eng.DiskUsage(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	v := diskResponse(du)
	s.diskMu.Lock()
	s.diskVal, s.diskAt, s.diskFor = v, time.Now(), eng
	s.diskMu.Unlock()
	writeJSON(w, http.StatusOK, v)
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
	DiskBytes   int64  `json:"disk_bytes,omitempty"` // 虛擬磁碟（ext4.vhdx）在 Windows 上的大小；取不到則省略
	// DiskLimitBytes 是虛擬磁碟的容量上限（VHDX 標頭；取不到則省略）
	DiskLimitBytes int64 `json:"disk_limit_bytes,omitempty"`
}

// MachinesResponse 是 /api/machines 回應；Available=false 表示此環境沒有 wsl.exe（例如測試主機未注入）。
type MachinesResponse struct {
	Available bool      `json:"available"`
	Machines  []Machine `json:"machines"`
}

// MachineConfig 是 /api/machines/{name}/config 回應。
type MachineConfig struct {
	Name    string `json:"name"`
	WSLConf string `json:"wsl_conf"`
	// WSLConfUnavailable：機器停止中（或正在調整磁碟）時不讀 /etc/wsl.conf——wsl -d 會把機器開起來；
	// UI 顯示「啟動機器後才能查看」，Systemd 也因此未知（false）。
	WSLConfUnavailable bool    `json:"wsl_conf_unavailable,omitempty"`
	Running            bool    `json:"running"`
	Systemd            bool    `json:"systemd"`
	APIEndpoint        string  `json:"api_endpoint,omitempty"`
	User               string  `json:"user,omitempty"`
	Managed            bool    `json:"managed"`
	Ready              bool    `json:"ready"`
	Memory             string  `json:"memory,omitempty"`     // .wslconfig
	Processors         string  `json:"processors,omitempty"` // .wslconfig
	CPUPercent         float64 `json:"cpu_percent"`
	MemoryUsedMB       int64   `json:"memory_used_mb"`
	MemoryTotalMB      int64   `json:"memory_total_mb"`
	DiskBytes          int64   `json:"disk_bytes,omitempty"`       // ext4.vhdx 檔案大小
	DiskUsedBytes      int64   `json:"disk_used_bytes,omitempty"`  // 執行中：df / used
	DiskTotalBytes     int64   `json:"disk_total_bytes,omitempty"` // 執行中：df / size
	DiskLimitBytes     int64   `json:"disk_limit_bytes,omitempty"` // 虛擬磁碟容量上限（VHDX 標頭）
}

// distroDisk 回傳 distro 虛擬磁碟大小；未注入或失敗回 0（欄位省略）。
func (s *Server) distroDisk(name string) int64 {
	if s.DistroDisk == nil {
		return 0
	}
	n, err := s.DistroDisk(name)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// distroDiskLimit 回傳 distro 虛擬磁碟的容量上限；未注入或失敗回 0（欄位省略）。
func (s *Server) distroDiskLimit(name string) int64 {
	if s.DistroDiskLimit == nil {
		return 0
	}
	n, err := s.DistroDiskLimit(name)
	if err != nil || n < 0 {
		return 0
	}
	return n
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
		m := Machine{Name: d.Name, State: d.State, Version: d.Version, Default: d.Default, Active: strings.EqualFold(d.Name, cfg.Active), DiskBytes: s.distroDisk(d.Name), DiskLimitBytes: s.distroDiskLimit(d.Name)}
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
	// 只對執行中的 distro 執行命令：wsl -d 會把停止中的機器開起來（開 Info／每 3 秒輪詢都會）；
	// 調整磁碟期間也不進 distro，否則會叫醒剛關掉的 WSL、重新掛上 VHD。
	running := strings.EqualFold(d.State, "Running")
	canExec := running && !s.diskBusy.Load()
	mc := MachineConfig{Name: d.Name, Running: running, WSLConfUnavailable: !canExec}
	if canExec {
		if out, _, err := wsl.RunIn(r.Context(), s.WSL, d.Name, "root", nil, "cat", "/etc/wsl.conf"); err == nil {
			mc.WSLConf = wsl.DecodeOutput(out)
		}
		mc.Systemd = wslConfSystemd(mc.WSLConf)
	}
	if pm, ok := s.cfg().FindMachine(d.Name); ok {
		mc.Managed = true
		mc.Ready = pm.Ready()
		mc.APIEndpoint = pm.APIEndpoint
		mc.User = pm.User
	}
	mem, procs := readWSLConfigLimits()
	mc.Memory, mc.Processors = mem, procs
	mc.DiskBytes = s.distroDisk(d.Name)
	mc.DiskLimitBytes = s.distroDiskLimit(d.Name)
	if canExec {
		cpu, used, total := wsl.SampleDistroUsage(r.Context(), s.WSL, d.Name)
		mc.CPUPercent = cpu
		mc.MemoryUsedMB = int64(used)
		mc.MemoryTotalMB = int64(total)
		mc.DiskUsedBytes, mc.DiskTotalBytes = wsl.SampleDistroDisk(r.Context(), s.WSL, d.Name)
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
		// VHDSizeGB：虛擬磁碟容量上限（GB）；0／省略 = WSL 預設（1 TB）。
		VHDSizeGB int `json:"vhd_size_gb"`
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
	if body.VHDSizeGB != 0 && !wsl.ValidVHDSizeGB(body.VHDSizeGB) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("gui.machines.vhd_invalid", wsl.MinVHDSizeGB, wsl.MaxVHDSizeGB)))
		return
	}
	done, ok := s.trackInstall(r.Context(), dist, local)
	if !ok {
		writeErr(w, http.StatusConflict, "conflict", errors.New("another distro install is in progress"))
		return
	}
	defer done()
	if err := wsl.Install(r.Context(), s.WSL, dist, local, body.VHDSizeGB); err != nil {
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

// handleInstallDefaults 提供新增機器對話框的預設值（Linux 使用者名稱依目前 Windows 使用者、磁碟上限）。
func (s *Server) handleInstallDefaults(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user":        wsl.SuggestLinuxUser(),
		"vhd_size_gb": wsl.DefaultVHDSizeGB,
		"vhd_min_gb":  wsl.MinVHDSizeGB,
		"vhd_max_gb":  wsl.MaxVHDSizeGB,
	})
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

// handleMachineResize 調整 distro 虛擬磁碟的容量上限（wsl --manage --resize），成功後在同一個空檔壓縮 VHDX
// （縮小會搬動資料，Windows 上的檔案反而變大）。VHD 被 WSL 占用時的流程見 runDiskOp。
func (s *Server) handleMachineResize(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SizeGB   int  `json:"size_gb"`
		Shutdown bool `json:"shutdown"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if !wsl.ValidVHDSizeGB(body.SizeGB) {
		writeErr(w, http.StatusBadRequest, "usage", errors.New(i18n.T("gui.machines.vhd_invalid", wsl.MinVHDSizeGB, wsl.MaxVHDSizeGB)))
		return
	}
	d, ok := s.findDistro(w, r)
	if !ok {
		return
	}
	// 執行中可先估計檔案系統能縮到多小：放不下就在關閉 WSL 之前拒絕
	if strings.EqualFold(d.State, "Running") {
		if min, err := wsl.MinDiskGB(r.Context(), s.WSL, d.Name); err == nil && body.SizeGB < min {
			writeTooSmall(w, http.StatusBadRequest, d.Name, body.SizeGB, min)
			return
		}
	}
	if !s.diskBusy.CompareAndSwap(false, true) {
		writeErr(w, http.StatusConflict, "conflict", errors.New(i18n.T("gui.machines.disk_busy")))
		return
	}
	defer s.diskBusy.Store(false)
	before := s.distroDisk(d.Name)
	compacted := false
	res, err := s.runDiskOp(r.Context(), body.Shutdown, d.Name, func(ctx context.Context) error {
		if err := wsl.ResizeDisk(ctx, s.WSL, d.Name, body.SizeGB); err != nil {
			return err
		}
		compacted = wsl.CompactDisk(ctx, s.WSL, d.Name) == nil // best-effort
		return nil
	})
	s.invalidateDisk()
	if err != nil {
		s.writeDiskOpErr(w, d.Name, body.SizeGB, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": d.Name, "size_gb": body.SizeGB, "shutdown": res.Shutdown, "restarted": res.Restarted,
		"compacted": compacted, "before_bytes": before, "after_bytes": s.distroDisk(d.Name),
	})
}

// handleMachineCompact 壓縮 distro 的 VHDX（wsl --manage --compact），把已不再使用的空間還給 Windows。
func (s *Server) handleMachineCompact(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Shutdown bool `json:"shutdown"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	d, ok := s.findDistro(w, r)
	if !ok {
		return
	}
	if !s.diskBusy.CompareAndSwap(false, true) {
		writeErr(w, http.StatusConflict, "conflict", errors.New(i18n.T("gui.machines.disk_busy")))
		return
	}
	defer s.diskBusy.Store(false)
	before := s.distroDisk(d.Name)
	res, err := s.runDiskOp(r.Context(), body.Shutdown, d.Name, func(ctx context.Context) error {
		return wsl.CompactDisk(ctx, s.WSL, d.Name)
	})
	s.invalidateDisk()
	if err != nil {
		s.writeDiskOpErr(w, d.Name, 0, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "name": d.Name, "shutdown": res.Shutdown, "restarted": res.Restarted,
		"before_bytes": before, "after_bytes": s.distroDisk(d.Name),
	})
}

// runDiskOp 在 distro 的 VHD 未被 WSL 占用時執行 op（wsl.WithFreeVHD）：
// 未執行且未被占用就直接做；否則需 consent（使用者同意 wsl --shutdown）：停各機 docker → 關 WSL → op → 重啟原本執行中的機器。
func (s *Server) runDiskOp(ctx context.Context, consent bool, name string, op func(context.Context) error) (wsl.FreeVHDResult, error) {
	cfg := s.cfg()
	fl, _ := s.life().(*FakeLifecycle)
	return wsl.WithFreeVHD(ctx, s.WSL, name, wsl.FreeVHDOptions{
		Consent: consent, SelfInWSL: s.SelfInWSL, Primary: cfg.Active,
		BeforeShutdown: func(running []string) {
			for _, n := range running {
				if _, managed := cfg.FindMachine(n); managed {
					_ = wsl.StopDocker(ctx, s.WSL, n)
				}
			}
			if fl != nil && fl.Engine != nil {
				fl.Engine.SetDown(true) // testhost：WSL 關閉 → 引擎斷線
			}
		},
		AfterWake: func(n string) {
			if fl != nil && fl.Engine != nil && strings.EqualFold(n, cfg.Active) {
				fl.Engine.SetDown(false)
			}
		},
	}, op)
}

// writeDiskOpErr 把 runDiskOp 的錯誤轉成 API 回應。
func (s *Server) writeDiskOpErr(w http.ResponseWriter, name string, sizeGB int, err error) {
	var ns *wsl.NeedsShutdownError
	var ts *wsl.TooSmallError
	var re *wsl.RestartError
	switch {
	case errors.As(err, &ns):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": i18n.T("gui.machines.disk_needs_shutdown"), "code": "needs_shutdown", "running": ns.Running,
		})
	case errors.Is(err, wsl.ErrSelfInWSL):
		writeErr(w, http.StatusConflict, "self_in_wsl", errors.New(i18n.T("gui.machines.resize_self_in_wsl")))
	case errors.As(err, &ts):
		writeTooSmall(w, http.StatusUnprocessableEntity, name, sizeGB, ts.MinGB)
	case errors.As(err, &re):
		writeErr(w, http.StatusBadGateway, "error", fmt.Errorf("%s: %w", i18n.T("gui.machines.resize_restart_failed", re.Name), re.Err))
	default:
		writeErr(w, http.StatusBadGateway, "error", err)
	}
}

// writeTooSmall 回報磁碟上限小於檔案系統所需（附建議最小值 min_gb）。
func writeTooSmall(w http.ResponseWriter, status int, name string, sizeGB, minGB int) {
	writeJSON(w, status, map[string]any{
		"error": i18n.T("gui.machines.resize_too_small", name, sizeGB, minGB), "code": "too_small", "min_gb": minGB,
	})
}

// handleMachineResizeInfo 回報調整對話框需要的資訊：執行中時估計能縮到的最小上限（停止中不喚醒，min_gb=0）。
func (s *Server) handleMachineResizeInfo(w http.ResponseWriter, r *http.Request) {
	d, ok := s.findDistro(w, r)
	if !ok {
		return
	}
	running := strings.EqualFold(d.State, "Running")
	min := 0
	if running && !s.diskBusy.Load() {
		if n, err := wsl.MinDiskGB(r.Context(), s.WSL, d.Name); err == nil {
			min = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": d.Name, "running": running, "min_gb": min})
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
