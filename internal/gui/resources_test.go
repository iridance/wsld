package gui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

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

func TestMachineUseUnmanageUnregister(t *testing.T) {
	e := newEnv(t, "")
	e.s.WSL = NewFakeWSL()

	resp, b := e.req(t, http.MethodPost, "/api/machines/docker-desktop/use", "")
	if resp.StatusCode != 200 {
		t.Fatalf("use = %d %s", resp.StatusCode, b)
	}
	c, err := e.s.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if c.Active != "docker-desktop" {
		t.Fatalf("active = %q", c.Active)
	}
	m, ok := c.FindMachine("docker-desktop")
	if !ok || !m.Ready() {
		t.Fatalf("first use should init+start, got ok=%v m=%+v", ok, m)
	}
	if e.life.Distro != "docker-desktop" {
		t.Fatalf("lifecycle not rebound: %q", e.life.Distro)
	}
	if e.life.Starts() < 1 {
		t.Fatal("expected Start during first use")
	}
	_, b = e.req(t, http.MethodGet, "/api/meta", "")
	var meta Meta
	_ = json.Unmarshal(b, &meta)
	if meta.Distro != "docker-desktop" || !meta.ActiveReady {
		t.Fatalf("meta = %+v", meta)
	}

	// re-use primary (already managed+ready)
	resp, _ = e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/use", "")
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	c, _ = e.s.LoadConfig()
	um, _ := c.FindMachine("Ubuntu-26.04")
	if !um.Ready() {
		t.Fatalf("Ubuntu should remain ready: %+v machines=%+v", um, c.Machines)
	}

	resp, _ = e.req(t, http.MethodPost, "/api/machines/docker-desktop/unmanage", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unmanage = %d", resp.StatusCode)
	}
	c, _ = e.s.LoadConfig()
	if _, ok := c.FindMachine("docker-desktop"); ok {
		t.Fatal("should be unmanaged")
	}

	resp, b = e.req(t, http.MethodPost, "/api/machines/Ubuntu-26.04/unregister", `{"confirm":"nope"}`)
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
