package gui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/iridance/wsld/internal/wsl"
)

const exposeRoot = "/mnt/wsl/distro"

// exposeEnv：engine = Ubuntu-26.04（newEnv 預設），client Debian1 執行中且已曝露。
func exposeEnv(t *testing.T) (*env, *FakeWSL) {
	t.Helper()
	e := newEnv(t, "")
	fw := NewFakeWSL()
	fw.AddDistro("Debian1", "Running")
	fw.SetExposed("Debian1", true)
	e.s.WSL = fw
	e.s.KeepAlive = &wsl.KeepAlive{Spawner: fw}
	return e, fw
}

func TestSuperviseReleasesAnExposureNoContainerUses(t *testing.T) {
	e, fw := exposeEnv(t)
	e.s.superviseOnce(time.Now())
	if got := fw.Exposed(); len(got) != 0 {
		t.Fatalf("an unused exposure must be released, still exposed: %v", got)
	}
}

func TestSuperviseKeepsAnExposureARunningContainerUses(t *testing.T) {
	e, fw := exposeEnv(t)
	e.engine.SetBinds("web", exposeRoot+"/Debian1/home/u/app")
	e.s.superviseOnce(time.Now())
	if got := fw.Exposed(); len(got) != 1 || got[0] != "Debian1" {
		t.Fatalf("exposure must stay while a container uses it: %v", got)
	}
}

func TestSuperviseReleasesAStaleExposureOfAStoppedDistro(t *testing.T) {
	e, fw := exposeEnv(t)
	e.engine.SetBinds("web", exposeRoot+"/Debian1/home/u/app")
	fw.SetState("Debian1", "Stopped")
	e.s.superviseOnce(time.Now())
	if got := fw.Exposed(); len(got) != 0 {
		t.Fatalf("a stale exposure must be released: %v", got)
	}
}

func TestExposureGCIsThrottled(t *testing.T) {
	e, fw := exposeEnv(t)
	now := time.Now()
	e.engine.SetBinds("web", exposeRoot+"/Debian1/home/u/app")
	e.s.superviseOnce(now) // 第一次：保留
	fw.SetExposed("Other", true)
	fw.AddDistro("Other", "Running")
	e.s.superviseOnce(now.Add(5 * time.Second)) // 節流：不處理
	if got := strings.Join(fw.Exposed(), ","); got != "Debian1,Other" {
		t.Fatalf("GC must not run within the throttle window: %s", got)
	}
	e.s.superviseOnce(now.Add(time.Minute))
	if got := strings.Join(fw.Exposed(), ","); got != "Debian1" {
		t.Fatalf("after the window the unused one goes: %s", got)
	}
}

func TestStoppingAClientWithRunningContainersNeedsConfirmationThenReleases(t *testing.T) {
	e, fw := exposeEnv(t)
	e.engine.SetBinds("web", exposeRoot+"/Debian1/home/u/app")

	resp, b := e.req(t, http.MethodPost, "/api/machines/Debian1/stop", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stop = %d %s", resp.StatusCode, b)
	}
	var body struct {
		Code       string `json:"code"`
		Containers []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"containers"`
	}
	_ = json.Unmarshal(b, &body)
	if body.Code != "in_use" || len(body.Containers) != 1 || body.Containers[0].Name != "web" {
		t.Fatalf("body = %s", b)
	}
	if got := fw.Exposed(); len(got) != 1 {
		t.Fatalf("nothing may change before confirmation: %v", got)
	}

	resp, b = e.req(t, http.MethodPost, "/api/machines/Debian1/stop?release=1", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("confirmed stop = %d %s", resp.StatusCode, b)
	}
	if got := fw.Exposed(); len(got) != 0 {
		t.Fatalf("exposure must be released before the distro stops: %v", got)
	}
	if fw.State("Debian1") != "Stopped" {
		t.Fatalf("Debian1 = %s", fw.State("Debian1"))
	}
	if cs, _ := e.engine.ListContainers(nil, true); cs != nil {
		for _, c := range cs {
			if c.Names[0] == "/web" && c.State == "running" {
				t.Fatal("the container using the distro must have been stopped")
			}
		}
	}
	if i, j := fw.CallIndex("umount "+exposeRoot+"/Debian1"), fw.CallIndex("--terminate Debian1"); i < 0 || j < 0 || i > j {
		t.Fatalf("release must come before terminate (umount=%d terminate=%d)", i, j)
	}
}

func TestStoppingAnUnexposedOrUnusedClientNeedsNoConfirmation(t *testing.T) {
	e, fw := exposeEnv(t)
	resp, b := e.req(t, http.MethodPost, "/api/machines/Debian1/stop", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop = %d %s", resp.StatusCode, b)
	}
	if got := fw.Exposed(); len(got) != 0 {
		t.Fatalf("exposure must be gone: %v", got)
	}
}

func TestUnregisteringAClientReleasesItsExposureFirst(t *testing.T) {
	e, fw := exposeEnv(t)
	e.engine.SetBinds("web", exposeRoot+"/Debian1/home/u/app")
	resp, b := e.req(t, http.MethodPost, "/api/machines/Debian1/unregister", `{"confirm":"Debian1"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("unregister while in use = %d %s", resp.StatusCode, b)
	}
	resp, b = e.req(t, http.MethodPost, "/api/machines/Debian1/unregister?release=1", `{"confirm":"Debian1"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("confirmed unregister = %d %s", resp.StatusCode, b)
	}
	if got := fw.Exposed(); len(got) != 0 {
		t.Fatalf("exposure left behind: %v", got)
	}
}

func TestStartingAContainerExposesItsSourceDistroFirst(t *testing.T) {
	e, fw := exposeEnv(t)
	fw.SetExposed("Debian1", false)
	id := e.engine.AddContainer("app", "alpine", "exited", nil)
	e.engine.SetBinds("app", exposeRoot+"/Debian1/home/u/app")
	resp, b := e.req(t, http.MethodPost, "/api/containers/"+id+"/start", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start = %d %s", resp.StatusCode, b)
	}
	if got := fw.Exposed(); len(got) != 1 || got[0] != "Debian1" {
		t.Fatalf("the source distro must be exposed before the start: %v", got)
	}
}

func TestStartingAContainerWhoseSourceDistroIsStoppedIsRefusedWithoutWakingIt(t *testing.T) {
	e, fw := exposeEnv(t)
	fw.SetExposed("Debian1", false)
	fw.SetState("Debian1", "Stopped")
	id := e.engine.AddContainer("app", "alpine", "exited", nil)
	e.engine.SetBinds("app", exposeRoot+"/Debian1/home/u/app")
	resp, b := e.req(t, http.MethodPost, "/api/containers/"+id+"/start", "")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "distro_stopped") {
		t.Fatalf("start = %d %s", resp.StatusCode, b)
	}
	if fw.State("Debian1") != "Stopped" {
		t.Fatal("must not wake the source distro")
	}
}

func TestExposureEndpointsReportAndRelease(t *testing.T) {
	e, fw := exposeEnv(t)
	e.engine.SetBinds("web", exposeRoot+"/Debian1/home/u/app")
	_, b := e.req(t, http.MethodGet, "/api/expose", "")
	var rep struct {
		Entries []struct {
			Distro     string `json:"distro"`
			Mounted    bool   `json:"mounted"`
			Stale      bool   `json:"stale"`
			Containers []struct {
				Name string `json:"name"`
			} `json:"containers"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(b, &rep)
	if len(rep.Entries) != 1 || rep.Entries[0].Distro != "Debian1" || !rep.Entries[0].Mounted || len(rep.Entries[0].Containers) != 1 {
		t.Fatalf("report = %s", b)
	}
	resp, _ := e.req(t, http.MethodPost, "/api/machines/Debian1/expose/release", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("release while a running container uses it = %d", resp.StatusCode)
	}
	resp, _ = e.req(t, http.MethodPost, "/api/machines/Debian1/expose/release?stop=1", "")
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("release with stop = %d", resp.StatusCode)
	}
	if len(fw.Exposed()) != 0 {
		t.Fatalf("still exposed: %v", fw.Exposed())
	}
}

func TestMachinesListShowsWhichDistrosAreExposed(t *testing.T) {
	e, _ := exposeEnv(t)
	e.engine.SetBinds("web", exposeRoot+"/Debian1/home/u/app")
	e.s.superviseOnce(time.Now()) // GC 順便更新快取
	_, b := e.req(t, http.MethodGet, "/api/machines", "")
	var m MachinesResponse
	_ = json.Unmarshal(b, &m)
	got := map[string]bool{}
	for _, x := range m.Machines {
		got[x.Name] = x.Exposed
	}
	if !got["Debian1"] || got["Ubuntu-26.04"] {
		t.Fatalf("exposed = %v", got)
	}
}
