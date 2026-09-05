package gui

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/irice/wsl-docker/internal/dockerapi"
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
	resp, b = e.req(t, http.MethodGet, "/api/machines/docker-desktop/config", "")
	_ = json.Unmarshal(b, &cfg)
	if resp.StatusCode != 200 || cfg.Systemd || cfg.WSLConf != "" {
		t.Fatalf("distro without wsl.conf = %d %+v", resp.StatusCode, cfg)
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
