package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/dockerapi"
	"github.com/iridance/wsld/internal/wsl"
)

func TestImagesListRemovePrune(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, http.MethodGet, "/api/images", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	var imgs []dockerapi.Image
	_ = json.Unmarshal(b, &imgs)
	if len(imgs) != 3 {
		t.Fatalf("default fake should expose 3 images, got %+v", imgs)
	}

	// 使用中的映像：不 force → 409 conflict
	resp, b = e.req(t, http.MethodDelete, "/api/images/nginx:1.27", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("in-use image delete = %d %s", resp.StatusCode, b)
	}
	var ae apiError
	_ = json.Unmarshal(b, &ae)
	if ae.Code != "conflict" {
		t.Fatalf("code = %q", ae.Code)
	}
	// 未使用映像：204
	resp, b = e.req(t, http.MethodDelete, "/api/images/redis:7", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete unused = %d %s", resp.StatusCode, b)
	}
	// prune 移除剩下未使用的（dangling）
	resp, b = e.req(t, http.MethodPost, "/api/images/prune", "")
	if resp.StatusCode != 200 {
		t.Fatalf("prune = %d %s", resp.StatusCode, b)
	}
	var rep dockerapi.PruneReport
	_ = json.Unmarshal(b, &rep)
	if rep.Count != 1 || rep.SpaceReclaimed <= 0 {
		t.Fatalf("prune report = %+v", rep)
	}
	_, b = e.req(t, http.MethodGet, "/api/images", "")
	_ = json.Unmarshal(b, &imgs)
	if len(imgs) != 1 || imgs[0].Name() != "nginx:1.27" {
		t.Fatalf("after prune = %+v", imgs)
	}
	// 引擎離線 → 502 api
	e.engine.SetDown(true)
	resp, _ = e.req(t, http.MethodGet, "/api/images", "")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("down = %d", resp.StatusCode)
	}
}

func TestVolumesListRemovePrune(t *testing.T) {
	e := newEnv(t, "")
	_, b := e.req(t, http.MethodGet, "/api/volumes", "")
	var vols []dockerapi.Volume
	_ = json.Unmarshal(b, &vols)
	if len(vols) != 2 || vols[0].Name != "orphan" || vols[1].Name != "pgdata" {
		t.Fatalf("volumes = %+v", vols)
	}
	resp, _ := e.req(t, http.MethodDelete, "/api/volumes/pgdata", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("in-use volume delete = %d", resp.StatusCode)
	}
	resp, _ = e.req(t, http.MethodDelete, "/api/volumes/orphan", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	e.engine.AddVolume("scratch", false)
	resp, b = e.req(t, http.MethodPost, "/api/volumes/prune", "")
	var rep dockerapi.PruneReport
	_ = json.Unmarshal(b, &rep)
	if resp.StatusCode != 200 || rep.Count != 1 {
		t.Fatalf("prune = %d %+v", resp.StatusCode, rep)
	}
	_, b = e.req(t, http.MethodGet, "/api/volumes", "")
	_ = json.Unmarshal(b, &vols)
	if len(vols) != 1 || vols[0].Name != "pgdata" {
		t.Fatalf("after prune = %+v", vols)
	}
}

func TestNetworksList(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, http.MethodGet, "/api/networks", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var nets []dockerapi.Network
	_ = json.Unmarshal(b, &nets)
	if len(nets) != 4 || nets[0].Name != "bridge" {
		t.Fatalf("networks = %+v", nets)
	}
}

func TestNetworksRemovePrune(t *testing.T) {
	e := newEnv(t, "")
	// 內建網路不可刪
	resp, b := e.req(t, http.MethodDelete, "/api/networks/bridge", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("builtin delete = %d %s", resp.StatusCode, b)
	}
	// 使用中
	e.engine.AddNetwork("busy", "bridge", true)
	resp, b = e.req(t, http.MethodDelete, "/api/networks/busy", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("in-use delete = %d %s", resp.StatusCode, b)
	}
	// 未使用自訂網路
	resp, b = e.req(t, http.MethodDelete, "/api/networks/shop_default", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete unused = %d %s", resp.StatusCode, b)
	}
	e.engine.AddNetwork("scratch", "bridge", false)
	resp, b = e.req(t, http.MethodPost, "/api/networks/prune", "")
	var rep dockerapi.PruneReport
	_ = json.Unmarshal(b, &rep)
	if resp.StatusCode != 200 || rep.Count != 1 {
		t.Fatalf("prune = %d %+v %s", resp.StatusCode, rep, b)
	}
	_, b = e.req(t, http.MethodGet, "/api/networks", "")
	var after []dockerapi.Network
	_ = json.Unmarshal(b, &after)
	// bridge/host/none + busy（使用中）
	if len(after) != 4 {
		t.Fatalf("after prune = %+v", after)
	}
	names := map[string]bool{}
	for _, n := range after {
		names[n.Name] = true
	}
	if !names["busy"] || names["scratch"] || names["shop_default"] {
		t.Fatalf("after prune names = %v", names)
	}
}

func TestMachinesWithoutWSL(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, http.MethodGet, "/api/machines", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	var m MachinesResponse
	_ = json.Unmarshal(b, &m)
	if m.Available || len(m.Machines) != 0 {
		t.Fatalf("without WSL runner machines should be unavailable: %+v", m)
	}
}

func TestMachinesListConfigOnlineInstall(t *testing.T) {
	defer wsl.BypassSampleSleepForTest()()
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w

	resp, b := e.req(t, http.MethodGet, "/api/machines", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	var m MachinesResponse
	_ = json.Unmarshal(b, &m)
	if !m.Available || len(m.Machines) != 2 {
		t.Fatalf("machines = %+v", m)
	}
	if m.Machines[0].Name != "Ubuntu-26.04" || !m.Machines[0].Active || !m.Machines[0].Default || m.Machines[0].State != "Running" || m.Machines[0].Version != 2 {
		t.Fatalf("machines[0] = %+v", m.Machines[0])
	}
	if m.Machines[1].Name != "docker-desktop" || m.Machines[1].Active {
		t.Fatalf("machines[1] = %+v", m.Machines[1])
	}

	resp, b = e.req(t, http.MethodGet, "/api/machines/Ubuntu-26.04/config", "")
	if resp.StatusCode != 200 {
		t.Fatalf("config = %d %s", resp.StatusCode, b)
	}
	var cfg MachineConfig
	_ = json.Unmarshal(b, &cfg)
	if cfg.Name != "Ubuntu-26.04" || !cfg.Systemd || cfg.WSLConf == "" {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.CPUPercent < 79 || cfg.CPUPercent > 81 || cfg.MemoryUsedMB != 2048 || cfg.MemoryTotalMB != 8192 {
		t.Fatalf("usage = cpu=%v mem=%d/%d", cfg.CPUPercent, cfg.MemoryUsedMB, cfg.MemoryTotalMB)
	}
	resp, b = e.req(t, http.MethodGet, "/api/machines/docker-desktop/config", "")
	_ = json.Unmarshal(b, &cfg)
	if resp.StatusCode != 200 || cfg.Systemd || cfg.WSLConf != "" {
		t.Fatalf("distro without wsl.conf = %d %+v", resp.StatusCode, cfg)
	}
	if cfg.CPUPercent != 0 || cfg.MemoryUsedMB != 0 || cfg.MemoryTotalMB != 0 {
		t.Fatalf("stopped usage should be 0: %+v", cfg)
	}
	resp, _ = e.req(t, http.MethodGet, "/api/machines/nope/config", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown distro = %d", resp.StatusCode)
	}

	resp, b = e.req(t, http.MethodGet, "/api/machines/online", "")
	var online []struct{ Name, Friendly string }
	_ = json.Unmarshal(b, &online)
	if resp.StatusCode != 200 || len(online) != 3 || online[1].Name != "Debian" {
		t.Fatalf("online = %d %+v", resp.StatusCode, online)
	}

	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"name":"Debian"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("install = %d %s", resp.StatusCode, b)
	}
	if got := w.Installed(); len(got) != 1 || got[0] != "Debian" {
		t.Fatalf("installed = %v", got)
	}
	// 安裝後清單應包含新 distro（fake 會加入 Stopped 狀態）
	_, b = e.req(t, http.MethodGet, "/api/machines", "")
	_ = json.Unmarshal(b, &m)
	if len(m.Machines) != 3 || m.Machines[2].Name != "Debian" || m.Machines[2].State != "Stopped" {
		t.Fatalf("after install = %+v", m.Machines)
	}
	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"distribution":"Debian","name":"Debian1"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("install custom name = %d %s", resp.StatusCode, b)
	}
	if got := w.Installed(); len(got) != 2 || got[1] != "Debian1" {
		t.Fatalf("installed custom = %v", got)
	}
	_, b = e.req(t, http.MethodGet, "/api/machines", "")
	_ = json.Unmarshal(b, &m)
	foundCustom := false
	for _, row := range m.Machines {
		if row.Name == "Debian1" {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatalf("after custom install = %+v", m.Machines)
	}
	// 帶 user／password：建立使用者、chpasswd（stdin）、寫入 [user] default
	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"distribution":"Debian","name":"Debian2","user":"bob","password":"p:w d"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("install with user = %d %s", resp.StatusCode, b)
	}
	var inst struct {
		User        string `json:"user"`
		PasswordSet bool   `json:"password_set"`
	}
	_ = json.Unmarshal(b, &inst)
	if inst.User != "bob" || !inst.PasswordSet || strings.Contains(string(b), "p:w d") {
		t.Fatalf("install with user resp = %s", b)
	}
	if pw, ok := w.Password("Debian2", "bob"); !ok || pw != "p:w d" {
		t.Fatalf("password = %q %v", pw, ok)
	}
	if conf := w.Conf("Debian2"); conf != "[user]\ndefault=bob\n" {
		t.Fatalf("wsl.conf = %q", conf)
	}
	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"distribution":"Debian","name":"Debian3","user":"carol"}`)
	_ = json.Unmarshal(b, &inst)
	if resp.StatusCode != 200 || inst.User != "carol" || inst.PasswordSet {
		t.Fatalf("install without password = %d %s", resp.StatusCode, b)
	}
	if _, ok := w.Password("Debian3", "carol"); ok {
		t.Fatal("empty password must not call chpasswd")
	}
	n := len(w.Installed())
	for _, bad := range []string{`{"name":"Debian4","user":"Root"}`, `{"name":"Debian4","user":"bob","password":"a\nb"}`} {
		if resp, _ := e.req(t, http.MethodPost, "/api/machines/install", bad); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d", bad, resp.StatusCode)
		}
	}
	if len(w.Installed()) != n {
		t.Fatal("invalid user/password must be rejected before installing")
	}
	resp, b = e.req(t, http.MethodGet, "/api/machines/install/defaults", "")
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"user":"`) {
		t.Fatalf("defaults = %d %s", resp.StatusCode, b)
	}
	resp, _ = e.req(t, http.MethodPost, "/api/machines/install", `{"name":"bad name"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid name = %d", resp.StatusCode)
	}
	w.FailInstall = true
	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"name":"Alpine"}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed install = %d %s", resp.StatusCode, b)
	}
}

func TestMachineUnmanageUnregister(t *testing.T) {
	e := newEnv(t, "")
	e.s.WSL = NewFakeWSL()
	c, _ := e.s.LoadConfig()
	c.UpsertMachine(config.Machine{Distro: "docker-desktop", User: "u", APIEndpoint: c.APIEndpoint})
	_ = c.SetClient("docker-desktop")
	if err := e.s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}

	resp, _ := e.req(t, http.MethodPost, "/api/machines/docker-desktop/unmanage", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unmanage = %d", resp.StatusCode)
	}
	c, _ = e.s.LoadConfig()
	if _, ok := c.FindMachine("docker-desktop"); ok {
		t.Fatal("should be unmanaged")
	}

	resp, b := e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/unregister", `{"confirm":"nope"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad confirm = %d %s", resp.StatusCode, b)
	}
	// add second machine then unregister it
	c, _ = e.s.LoadConfig()
	c.UpsertMachine(config.Machine{Distro: "docker-desktop", User: "x", APIEndpoint: "tcp://127.0.0.1:23752"})
	_ = e.s.SaveConfig(c)
	resp, _ = e.req(t, http.MethodPost, "/api/machines/docker-desktop/unregister", `{"confirm":"docker-desktop"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unregister = %d", resp.StatusCode)
	}
	c, _ = e.s.LoadConfig()
	if _, ok := c.FindMachine("docker-desktop"); ok {
		t.Fatal("profile should be gone")
	}
}

func TestMachineStartStop(t *testing.T) {
	e := newEnv(t, "")
	fw := NewFakeWSL()
	e.s.WSL = fw
	state := func(name string) string {
		t.Helper()
		_, b := e.req(t, http.MethodGet, "/api/machines", "")
		var mr MachinesResponse
		_ = json.Unmarshal(b, &mr)
		for _, m := range mr.Machines {
			if m.Name == name {
				return m.State
			}
		}
		t.Fatalf("machine %q not listed: %s", name, b)
		return ""
	}

	// 非主要 distro：啟動 → Running、停止 → Stopped，不影響引擎
	resp, b := e.req(t, http.MethodPost, "/api/machines/docker-desktop/start", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start = %d %s", resp.StatusCode, b)
	}
	if s := state("docker-desktop"); s != "Running" {
		t.Fatalf("after start = %q", s)
	}
	resp, b = e.req(t, http.MethodPost, "/api/machines/docker-desktop/stop", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop = %d %s", resp.StatusCode, b)
	}
	if s := state("docker-desktop"); s != "Stopped" {
		t.Fatalf("after stop = %q", s)
	}
	if e.engine.Down {
		t.Fatal("stopping a non-primary distro must not take the engine down")
	}
	// 已停止再停止：冪等
	resp, _ = e.req(t, http.MethodPost, "/api/machines/docker-desktop/stop", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop again = %d", resp.StatusCode)
	}

	// 主要 distro：停止 → 引擎斷線；啟動 → 引擎回來
	resp, _ = e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/stop", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop primary = %d", resp.StatusCode)
	}
	if s := state("Ubuntu-26.04"); s != "Stopped" || !e.engine.Down {
		t.Fatalf("after stop primary: state=%q down=%v", s, e.engine.Down)
	}
	resp, _ = e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/start", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start primary = %d", resp.StatusCode)
	}
	if s := state("Ubuntu-26.04"); s != "Running" || e.engine.Down {
		t.Fatalf("after start primary: state=%q down=%v", s, e.engine.Down)
	}

	// 錯誤：不存在 → 404；名稱不合法 → 400；沒有 WSL → 501
	resp, _ = e.req(t, http.MethodPost, "/api/machines/Nope/start", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing = %d", resp.StatusCode)
	}
	resp, _ = e.req(t, http.MethodPost, "/api/machines/bad%20name/stop", "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid = %d", resp.StatusCode)
	}
	e.s.WSL = nil
	resp, _ = e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/start", "")
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("no wsl = %d", resp.StatusCode)
	}
}

func TestDiskUsageEndpoint(t *testing.T) {
	e := newEnv(t, "")
	resp, b := e.req(t, http.MethodGet, "/api/disk", "")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d %s", resp.StatusCode, b)
	}
	var d DiskResponse
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	s := d.Summary
	if s.Images.Count != 3 || s.Images.Size != 98_000_000 || s.Containers.Count != 2 || s.Volumes.Count != 2 || s.BuildCache.Count == 0 {
		t.Fatalf("summary = %+v", s)
	}
	if s.Total != s.Images.Size+s.Containers.Size+s.Volumes.Size+s.BuildCache.Size || s.Reclaimable <= 0 {
		t.Fatalf("totals = %+v", s)
	}
	webID := strings.ReplaceAll(e.engine.idFor("web"), " ", "0")
	if d.Containers[webID].SizeRw <= 0 || d.Containers[webID].SizeRootFs < d.Containers[webID].SizeRw {
		t.Fatalf("containers = %+v", d.Containers)
	}
	if d.Volumes["pgdata"].Size <= 0 || d.Volumes["pgdata"].RefCount != 1 || d.Volumes["orphan"].RefCount != 0 {
		t.Fatalf("volumes = %+v", d.Volumes)
	}

	// 快取：引擎狀態改變但沒透過 API → 仍回快取；透過 API 刪除 → 失效
	e.engine.AddVolume("extra", false)
	_, b = e.req(t, http.MethodGet, "/api/disk", "")
	_ = json.Unmarshal(b, &d)
	if d.Summary.Volumes.Count != 2 {
		t.Fatalf("expected cached volume count 2, got %+v", d.Summary.Volumes)
	}
	if resp, _ := e.req(t, http.MethodDelete, "/api/volumes/orphan", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete orphan = %d", resp.StatusCode)
	}
	_, b = e.req(t, http.MethodGet, "/api/disk", "")
	d = DiskResponse{}
	_ = json.Unmarshal(b, &d)
	if d.Summary.Volumes.Count != 2 || d.Volumes["extra"].Size <= 0 || d.Volumes["orphan"].Size != 0 {
		t.Fatalf("after delete = %+v / %+v", d.Summary.Volumes, d.Volumes)
	}

	// 引擎離線 → 502（快取先失效）
	e.s.invalidateDisk()
	e.engine.SetDown(true)
	if resp, _ := e.req(t, http.MethodGet, "/api/disk", ""); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("down = %d", resp.StatusCode)
	}
}

func TestMachinesDiskUsage(t *testing.T) {
	defer wsl.BypassSampleSleepForTest()()
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w
	e.s.DistroDisk = w.VHDXSize

	_, b := e.req(t, http.MethodGet, "/api/machines", "")
	var m MachinesResponse
	_ = json.Unmarshal(b, &m)
	if len(m.Machines) != 2 || m.Machines[0].DiskBytes != 8_500_000_000 || m.Machines[1].DiskBytes != 1_200_000_000 {
		t.Fatalf("disk_bytes = %+v", m.Machines)
	}

	// 執行中：VHDX 大小＋發行版內 df
	_, b = e.req(t, http.MethodGet, "/api/machines/Ubuntu-26.04/config", "")
	var cfg MachineConfig
	_ = json.Unmarshal(b, &cfg)
	if cfg.DiskBytes != 8_500_000_000 || cfg.DiskUsedBytes != 6_000_000_000 || cfg.DiskTotalBytes != 1_000_000_000_000 {
		t.Fatalf("running disk = %+v", cfg)
	}
	// 停止中：只有 VHDX 大小，不跑 df（不喚醒）
	cfg = MachineConfig{}
	_, b = e.req(t, http.MethodGet, "/api/machines/docker-desktop/config", "")
	_ = json.Unmarshal(b, &cfg)
	if cfg.DiskBytes != 1_200_000_000 || cfg.DiskUsedBytes != 0 || cfg.DiskTotalBytes != 0 {
		t.Fatalf("stopped disk = %+v", cfg)
	}

	// 取不到大小（非 Windows／登錄檔缺）→ 省略欄位
	e.s.DistroDisk = func(string) (int64, error) { return 0, errors.New("unsupported") }
	_, b = e.req(t, http.MethodGet, "/api/machines", "")
	if strings.Contains(string(b), "disk_bytes") {
		t.Fatalf("disk_bytes should be omitted: %s", b)
	}
}

func TestMachineInstallWithDiskLimit(t *testing.T) {
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w
	e.s.DistroDiskLimit = w.VHDXLimit

	resp, b := e.req(t, http.MethodGet, "/api/machines/install/defaults", "")
	var def struct {
		VHDSizeGB int `json:"vhd_size_gb"`
		MinGB     int `json:"vhd_min_gb"`
		MaxGB     int `json:"vhd_max_gb"`
	}
	_ = json.Unmarshal(b, &def)
	if resp.StatusCode != 200 || def.VHDSizeGB != wsl.DefaultVHDSizeGB || def.MinGB != wsl.MinVHDSizeGB || def.MaxGB != wsl.MaxVHDSizeGB {
		t.Fatalf("defaults = %d %s", resp.StatusCode, b)
	}

	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"distribution":"Debian","name":"small","vhd_size_gb":60}`)
	if resp.StatusCode != 200 {
		t.Fatalf("install = %d %s", resp.StatusCode, b)
	}
	if got := w.LastInstallArgs(); !strings.Contains(got, "--vhd-size 60GB") {
		t.Fatalf("install args = %q", got)
	}
	// 0／省略 = WSL 預設（不帶 --vhd-size）
	resp, b = e.req(t, http.MethodPost, "/api/machines/install", `{"distribution":"Debian","name":"big"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("install default = %d %s", resp.StatusCode, b)
	}
	if got := w.LastInstallArgs(); strings.Contains(got, "--vhd-size") {
		t.Fatalf("install args = %q", got)
	}
	limits := map[string]int64{}
	_, b = e.req(t, http.MethodGet, "/api/machines", "")
	var m MachinesResponse
	_ = json.Unmarshal(b, &m)
	for _, row := range m.Machines {
		limits[row.Name] = row.DiskLimitBytes
	}
	if limits["small"] != 60<<30 || limits["big"] != 1<<40 {
		t.Fatalf("limits = %v", limits)
	}
	n := len(w.Installed())
	for _, bad := range []string{`{"name":"x1","vhd_size_gb":4}`, `{"name":"x2","vhd_size_gb":99999}`, `{"name":"x3","vhd_size_gb":-1}`} {
		if resp, _ := e.req(t, http.MethodPost, "/api/machines/install", bad); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s = %d", bad, resp.StatusCode)
		}
	}
	if len(w.Installed()) != n {
		t.Fatal("invalid size must be rejected before installing")
	}
}

func TestMachineDiskResize(t *testing.T) {
	defer wsl.BypassSampleSleepForTest()()
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w
	e.s.DistroDiskLimit = w.VHDXLimit
	machine := func(name string) Machine {
		t.Helper()
		_, b := e.req(t, http.MethodGet, "/api/machines", "")
		var mr MachinesResponse
		_ = json.Unmarshal(b, &mr)
		for _, m := range mr.Machines {
			if m.Name == name {
				return m
			}
		}
		t.Fatalf("machine %q not listed: %s", name, b)
		return Machine{}
	}
	type resizeResp struct {
		SizeGB    int      `json:"size_gb"`
		Shutdown  bool     `json:"shutdown"`
		Restarted []string `json:"restarted"`
		Code      string   `json:"code"`
		Error     string   `json:"error"`
		Running   []string `json:"running"`
	}
	resize := func(name, body string) (int, resizeResp) {
		t.Helper()
		resp, b := e.req(t, http.MethodPost, "/api/machines/"+name+"/resize", body)
		var out resizeResp
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}

	// 從未啟動過的機器（VHD 未掛載）：直接調整，不關 WSL
	code, out := resize("docker-desktop", `{"size_gb":60}`)
	if code != 200 || out.Shutdown || len(out.Restarted) != 0 {
		t.Fatalf("resize detached = %d %+v", code, out)
	}
	if m := machine("docker-desktop"); m.DiskLimitBytes != 60<<30 || m.State != "Stopped" {
		t.Fatalf("after resize detached = %+v", m)
	}
	if w.Shutdowns() != 0 {
		t.Fatal("must not shut WSL down when the VHD is free")
	}

	// 執行中的主要機器：未同意關閉 WSL → 409，列出會被停止的機器，什麼都不動
	code, out = resize("Ubuntu-26.04", `{"size_gb":80}`)
	if code != http.StatusConflict || out.Code != "needs_shutdown" || len(out.Running) != 1 || out.Running[0] != "Ubuntu-26.04" {
		t.Fatalf("resize running without consent = %d %+v", code, out)
	}
	if w.Shutdowns() != 0 || machine("Ubuntu-26.04").State != "Running" {
		t.Fatal("nothing may be stopped before consent")
	}

	// 同意：wsl --shutdown → 調整 → 原本執行中的機器開回來（主要機器的引擎也回來）
	code, out = resize("Ubuntu-26.04", `{"size_gb":80,"shutdown":true}`)
	if code != 200 || !out.Shutdown || len(out.Restarted) != 1 || out.Restarted[0] != "Ubuntu-26.04" {
		t.Fatalf("resize with shutdown = %d %+v", code, out)
	}
	if got := w.ResizeCalls(); got[len(got)-1] != "Ubuntu-26.04 80GB stopped detached" {
		t.Fatalf("resize must run after WSL released the VHD: %v", got)
	}
	if m := machine("Ubuntu-26.04"); m.DiskLimitBytes != 80<<30 || m.State != "Running" {
		t.Fatalf("after resize = %+v", m)
	}
	if w.Shutdowns() != 1 || e.engine.Down {
		t.Fatalf("shutdowns=%d engineDown=%v", w.Shutdowns(), e.engine.Down)
	}

	// 已停止但 VHD 仍掛著（啟動過再 terminate）：直接調整失敗 → 409 needs_shutdown
	if resp, _ := e.req(t, http.MethodPost, "/api/machines/docker-desktop/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatal("start docker-desktop")
	}
	if resp, _ := e.req(t, http.MethodPost, "/api/machines/docker-desktop/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatal("stop docker-desktop")
	}
	code, out = resize("docker-desktop", `{"size_gb":70}`)
	if code != http.StatusConflict || out.Code != "needs_shutdown" || len(out.Running) != 1 || out.Running[0] != "Ubuntu-26.04" {
		t.Fatalf("attached stopped = %d %+v", code, out)
	}
	code, out = resize("docker-desktop", `{"size_gb":70,"shutdown":true}`)
	if code != 200 || len(out.Restarted) != 1 || out.Restarted[0] != "Ubuntu-26.04" {
		t.Fatalf("attached stopped with shutdown = %d %+v", code, out)
	}
	if m := machine("docker-desktop"); m.DiskLimitBytes != 70<<30 || m.State != "Stopped" {
		t.Fatalf("stopped machine must stay stopped: %+v", m)
	}

	// wsl.exe 失敗（例如縮太小）：回 502 並把原本執行中的機器開回來
	w.FailResize = true
	code, out = resize("Ubuntu-26.04", `{"size_gb":8,"shutdown":true}`)
	if code != http.StatusBadGateway || !strings.Contains(out.Error, "too small") {
		t.Fatalf("failed resize = %d %+v", code, out)
	}
	if m := machine("Ubuntu-26.04"); m.State != "Running" || m.DiskLimitBytes != 80<<30 {
		t.Fatalf("after failed resize = %+v", m)
	}
	w.FailResize = false

	// wsld-gui 從 WSL 檔案系統執行：拒絕關閉 WSL（會失去自己的執行檔）
	e.s.SelfInWSL = true
	n := w.Shutdowns()
	code, out = resize("Ubuntu-26.04", `{"size_gb":90,"shutdown":true}`)
	if code != http.StatusConflict || out.Code != "self_in_wsl" || w.Shutdowns() != n {
		t.Fatalf("self in wsl = %d %+v", code, out)
	}
	e.s.SelfInWSL = false

	for _, tc := range []struct {
		path, body string
		code       int
	}{
		{"/api/machines/Ubuntu-26.04/resize", `{"size_gb":4}`, http.StatusBadRequest},
		{"/api/machines/Ubuntu-26.04/resize", `{}`, http.StatusBadRequest},
		{"/api/machines/nope/resize", `{"size_gb":60}`, http.StatusNotFound},
		{"/api/machines/bad%20name/resize", `{"size_gb":60}`, http.StatusBadRequest},
	} {
		if resp, b := e.req(t, http.MethodPost, tc.path, tc.body); resp.StatusCode != tc.code {
			t.Fatalf("%s %s = %d %s", tc.path, tc.body, resp.StatusCode, b)
		}
	}
}

func TestMachineConfigDoesNotWakeDuringResize(t *testing.T) {
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w
	e.s.diskBusy.Store(true)
	defer e.s.diskBusy.Store(false)
	_, b := e.req(t, http.MethodGet, "/api/machines/docker-desktop/config", "")
	var cfg MachineConfig
	_ = json.Unmarshal(b, &cfg)
	if cfg.Name != "docker-desktop" {
		t.Fatalf("config = %s", b)
	}
	if w.Ran("-d docker-desktop") {
		t.Fatal("config must not run commands inside a distro while a resize is in progress")
	}
}

func TestMachineDiskResizeTooSmall(t *testing.T) {
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w
	e.s.DistroDiskLimit = w.VHDXLimit
	w.SetFSMinBlocks("Ubuntu-26.04", 18390839) // ≈ 70.2 GiB → 建議最小 72 GB
	w.SetFSMinBlocks("docker-desktop", 18390839)

	// 執行中：resize-info 回報最小值
	resp, b := e.req(t, http.MethodGet, "/api/machines/Ubuntu-26.04/resize-info", "")
	var info struct {
		Running bool `json:"running"`
		MinGB   int  `json:"min_gb"`
	}
	_ = json.Unmarshal(b, &info)
	if resp.StatusCode != 200 || !info.Running || info.MinGB != 72 {
		t.Fatalf("resize-info running = %d %s", resp.StatusCode, b)
	}
	// 停止中：不進 distro 估計（不喚醒），min_gb = 0
	resp, b = e.req(t, http.MethodGet, "/api/machines/docker-desktop/resize-info", "")
	info.MinGB, info.Running = -1, true
	_ = json.Unmarshal(b, &info)
	if resp.StatusCode != 200 || info.Running || info.MinGB != 0 || w.Ran("-d docker-desktop") {
		t.Fatalf("resize-info stopped = %d %s", resp.StatusCode, b)
	}

	var out struct {
		Code  string `json:"code"`
		Error string `json:"error"`
		MinGB int    `json:"min_gb"`
	}
	// 執行中且小於最小值：在關閉 WSL 之前就拒絕
	resp, b = e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/resize", `{"size_gb":60,"shutdown":true}`)
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != http.StatusBadRequest || out.Code != "too_small" || out.MinGB != 72 || !strings.Contains(out.Error, "72") {
		t.Fatalf("too small running = %d %s", resp.StatusCode, b)
	}
	if w.Shutdowns() != 0 || len(w.ResizeCalls()) != 0 {
		t.Fatal("a doomed resize must not shut WSL down or call wsl --manage")
	}
	// 停止中無法預估：交給 WSL，失敗時回易讀訊息
	resp, b = e.req(t, http.MethodPost, "/api/machines/docker-desktop/resize", `{"size_gb":60}`)
	out = struct {
		Code  string `json:"code"`
		Error string `json:"error"`
		MinGB int    `json:"min_gb"`
	}{}
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != http.StatusUnprocessableEntity || out.Code != "too_small" || out.MinGB != 72 || strings.Contains(out.Error, "e2fsck") {
		t.Fatalf("too small stopped = %d %s", resp.StatusCode, b)
	}
	// 夠大就成功
	resp, b = e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/resize", `{"size_gb":72,"shutdown":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("resize to minimum = %d %s", resp.StatusCode, b)
	}
}

func TestMachineDiskCompact(t *testing.T) {
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w
	e.s.DistroDisk = w.VHDXSize
	type compactResp struct {
		Shutdown    bool     `json:"shutdown"`
		Restarted   []string `json:"restarted"`
		BeforeBytes int64    `json:"before_bytes"`
		AfterBytes  int64    `json:"after_bytes"`
		Code        string   `json:"code"`
		Running     []string `json:"running"`
	}
	compact := func(name, body string) (int, compactResp) {
		t.Helper()
		resp, b := e.req(t, http.MethodPost, "/api/machines/"+name+"/compact", body)
		var out compactResp
		_ = json.Unmarshal(b, &out)
		return resp.StatusCode, out
	}

	// VHD 未被占用：直接壓縮，回報前後大小
	code, out := compact("docker-desktop", `{}`)
	if code != 200 || out.Shutdown || out.BeforeBytes != 1_200_000_000 || out.AfterBytes != 1_000_000_000 {
		t.Fatalf("compact detached = %d %+v", code, out)
	}
	if w.Shutdowns() != 0 {
		t.Fatal("must not shut WSL down when the VHD is free")
	}

	// 執行中：未同意 → 409 列出會被停止的機器；同意 → 關 WSL、壓縮、重啟
	code, out = compact("Ubuntu-26.04", `{}`)
	if code != http.StatusConflict || out.Code != "needs_shutdown" || len(out.Running) != 1 {
		t.Fatalf("compact running without consent = %d %+v", code, out)
	}
	if got := w.CompactCalls(); len(got) != 1 {
		t.Fatalf("no compact may run before consent: %v", got)
	}
	code, out = compact("Ubuntu-26.04", `{"shutdown":true}`)
	if code != 200 || !out.Shutdown || len(out.Restarted) != 1 || out.BeforeBytes != 8_500_000_000 || out.AfterBytes != 4_250_000_000 {
		t.Fatalf("compact with shutdown = %d %+v", code, out)
	}
	if w.Shutdowns() != 1 || e.engine.Down {
		t.Fatalf("shutdowns=%d down=%v", w.Shutdowns(), e.engine.Down)
	}

	// wsld-gui 在 WSL 內：拒絕關機
	e.s.SelfInWSL = true
	code, out = compact("Ubuntu-26.04", `{"shutdown":true}`)
	if code != http.StatusConflict || out.Code != "self_in_wsl" || w.Shutdowns() != 1 {
		t.Fatalf("self in wsl = %d %+v", code, out)
	}
	e.s.SelfInWSL = false

	// 磁碟作業進行中：第二個請求 409
	e.s.diskBusy.Store(true)
	if code, _ := compact("docker-desktop", `{}`); code != http.StatusConflict {
		t.Fatalf("concurrent = %d", code)
	}
	e.s.diskBusy.Store(false)

	if code, _ := compact("nope", `{}`); code != http.StatusNotFound {
		t.Fatalf("unknown = %d", code)
	}
}

func TestMachineDiskResizeAlsoCompacts(t *testing.T) {
	e := newEnv(t, "")
	w := NewFakeWSL()
	e.s.WSL = w
	e.s.DistroDisk = w.VHDXSize
	e.s.DistroDiskLimit = w.VHDXLimit
	resp, b := e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/resize", `{"size_gb":60,"shutdown":true}`)
	var out struct {
		Compacted   bool  `json:"compacted"`
		BeforeBytes int64 `json:"before_bytes"`
		AfterBytes  int64 `json:"after_bytes"`
	}
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != 200 || !out.Compacted || out.BeforeBytes != 8_500_000_000 || out.AfterBytes != 4_250_000_000 {
		t.Fatalf("resize = %d %s", resp.StatusCode, b)
	}
	// 壓縮在同一次關機中、重啟之前執行
	if got := w.CompactCalls(); len(got) != 1 || got[0] != "Ubuntu-26.04 detached" || w.Shutdowns() != 1 {
		t.Fatalf("compacts=%v shutdowns=%d", got, w.Shutdowns())
	}
	// 調整失敗就不壓縮
	w.FailResize = true
	if resp, _ := e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/resize", `{"size_gb":50,"shutdown":true}`); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("failed resize = %d", resp.StatusCode)
	}
	if got := w.CompactCalls(); len(got) != 1 {
		t.Fatalf("no compact after a failed resize: %v", got)
	}
}

// 開停止中機器的 Info（與前端每 3 秒的輪詢）不可執行 wsl -d：那會把機器開起來。
func TestMachineConfigDoesNotWakeStoppedDistro(t *testing.T) {
	e := newEnv(t, "")
	w := NewFakeWSL()
	w.SetConf("docker-desktop", "[boot]\nsystemd=true\n")
	e.s.WSL = w
	for i := 0; i < 3; i++ {
		resp, b := e.req(t, http.MethodGet, "/api/machines/docker-desktop/config", "")
		if resp.StatusCode != 200 {
			t.Fatalf("config = %d %s", resp.StatusCode, b)
		}
		var cfg MachineConfig
		_ = json.Unmarshal(b, &cfg)
		if cfg.Running || !cfg.WSLConfUnavailable || cfg.WSLConf != "" || cfg.Systemd {
			t.Fatalf("stopped config = %+v", cfg)
		}
	}
	if w.Ran("-d docker-desktop") {
		t.Fatal("config ran a command inside a stopped distro")
	}
	if st := fakeState(t, w, "docker-desktop"); st != "Stopped" {
		t.Fatalf("docker-desktop woke up: %s", st)
	}

	// 執行中：照常讀 wsl.conf
	_, b := e.req(t, http.MethodGet, "/api/machines/Ubuntu-26.04/config", "")
	var cfg MachineConfig
	_ = json.Unmarshal(b, &cfg)
	if !cfg.Running || cfg.WSLConfUnavailable || !strings.Contains(cfg.WSLConf, "[boot]") {
		t.Fatalf("running config = %+v", cfg)
	}
}

func TestPrimaryKeepAlive(t *testing.T) {
	e := newEnv(t, "")
	fw := NewFakeWSL()
	e.s.WSL = fw
	e.s.KeepAlive = &wsl.KeepAlive{Spawner: fw}
	held := func() []string {
		t.Helper()
		return e.s.KeepAlive.Held()
	}
	waitHeld := func(want ...string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			got := held()
			if strings.Join(got, ",") == strings.Join(want, ",") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("held = %v, want %v", got, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	list := func() { e.req(t, http.MethodGet, "/api/machines", "") }

	// 主要機器已在執行：狀態輪詢補掛保活
	list()
	waitHeld("Ubuntu-26.04")

	// 非主要機器啟動：不保活
	e.req(t, http.MethodPost, "/api/machines/docker-desktop/start", "")
	waitHeld("Ubuntu-26.04")

	// 使用者停止主要機器：放掉保活，輪詢也不會把它重掛（否則會把剛停掉的機器叫醒）
	e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/stop", "")
	waitHeld()
	list()
	list()
	if len(held()) != 0 {
		t.Fatalf("poll must not re-hold a user-stopped primary: %v", held())
	}
	for _, s := range fw.Spawns() {
		if strings.Contains(s, "docker-desktop") {
			t.Fatalf("non-primary must never be kept alive: %v", fw.Spawns())
		}
	}

	// 再按啟動：回到保活
	e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/start", "")
	waitHeld("Ubuntu-26.04")

	// wsl --shutdown 會結束 session；之後使用者開了終端（機器回到 Running）→ 輪詢補掛
	_ = wsl.Shutdown(context.Background(), fw)
	waitHeld()
	_ = wsl.Wake(context.Background(), fw, "Ubuntu-26.04")
	list()
	waitHeld("Ubuntu-26.04")
}

// 只有 engine 保活；執行中的 client 不保活。
func TestOnlyEngineIsKeptAlive(t *testing.T) {
	e := newEnv(t, "")
	fw := NewFakeWSL()
	e.s.WSL = fw
	e.s.KeepAlive = &wsl.KeepAlive{Spawner: fw}
	cfg := e.s.cfg()
	cfg.UpsertMachine(config.Machine{Distro: "Debian1", User: "u", APIEndpoint: cfg.APIEndpoint})
	if err := cfg.SetClient("Debian1"); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	fw.AddDistro("Debian1", "Running")

	e.req(t, http.MethodGet, "/api/machines", "")
	deadline := time.Now().Add(2 * time.Second)
	for strings.Join(e.s.KeepAlive.Held(), ",") != "Ubuntu-26.04" {
		if time.Now().After(deadline) {
			t.Fatalf("held = %v, want only the engine", e.s.KeepAlive.Held())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestMachineRoleEngineAndClient(t *testing.T) {
	e := newEnv(t, "")
	if err := config.Save(e.cfgP, config.Default()); err != nil { // 首次使用：尚無任何 machine
		t.Fatal(err)
	}
	fw := NewFakeWSL()
	fw.AddDistro("Debian1", "Running")
	e.s.WSL = fw
	role := func(name, body string) int {
		resp, _ := e.req(t, http.MethodPost, "/api/machines/"+name+"/role", body)
		return resp.StatusCode
	}

	if got := role("Ubuntu-26.04", `{"role":"bogus"}`); got != http.StatusBadRequest {
		t.Fatalf("bogus role = %d", got)
	}
	if got := role("Nope", `{"role":"engine"}`); got != http.StatusNotFound {
		t.Fatalf("unknown distro = %d", got)
	}
	if got := role("Debian1", `{"role":"client"}`); got != http.StatusConflict {
		t.Fatalf("client without engine = %d", got)
	}
	if got := role("Ubuntu-26.04", `{"role":"engine"}`); got != http.StatusOK {
		t.Fatalf("engine = %d", got)
	}
	if got := role("Debian1", `{"role":"engine"}`); got != http.StatusConflict {
		t.Fatalf("second engine = %d", got)
	}
	if got := role("Debian1", `{"role":"client"}`); got != http.StatusOK {
		t.Fatalf("client = %d", got)
	}

	_, b := e.req(t, http.MethodGet, "/api/machines", "")
	var m MachinesResponse
	_ = json.Unmarshal(b, &m)
	roles := map[string]string{}
	for _, x := range m.Machines {
		roles[x.Name] = x.Role
	}
	if roles["Ubuntu-26.04"] != "engine" || roles["Debian1"] != "client" || roles["docker-desktop"] != "" {
		t.Fatalf("roles = %v", roles)
	}
	c, _ := e.s.LoadConfig()
	if c.Active != "Ubuntu-26.04" {
		t.Fatalf("making a client must not steal active, got %q", c.Active)
	}
	if cm, _ := c.FindMachine("Debian1"); cm.APIEndpoint != c.APIEndpoint {
		t.Fatalf("client shares the engine endpoint: %q vs %q", cm.APIEndpoint, c.APIEndpoint)
	}
}
