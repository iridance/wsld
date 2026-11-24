package gui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/wsl"
)

func composeEnv(t *testing.T) *k8sEnv {
	t.Helper()
	k := newK8sEnv(t)
	k.s.WSL = NewFakeWSL()
	k.s.Files = NewFakeFileHost()
	return k
}

func TestFSSourcesAndList(t *testing.T) {
	k := composeEnv(t)
	_, b := k.req(t, "GET", "/api/fs/sources", "")
	var src FileSources
	_ = json.Unmarshal(b, &src)
	if len(src.Drives) != 2 || len(src.WSL) != 1 || src.WSL[0].Name != "Ubuntu-26.04" || !src.WSL[0].Running {
		t.Fatalf("sources = %s (docker-desktop must be hidden)", b)
	}
	resp, b := k.req(t, "GET", "/api/fs/list?kind=wsl&name=Ubuntu-26.04", "")
	var l FileListing
	_ = json.Unmarshal(b, &l)
	if resp.StatusCode != 200 || l.Path != "/home/irice" || l.Parent != "/home" {
		t.Fatalf("home listing = %d %s", resp.StatusCode, b)
	}
	_, b = k.req(t, "GET", "/api/fs/list?kind=wsl&name=Ubuntu-26.04&path=/home/irice/projects/shop", "")
	_ = json.Unmarshal(b, &l)
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Name)
	}
	// 資料夾在前、只留 .yml／.yaml、隱藏的 .env 不列出
	if strings.Join(names, ",") != "api,web,docker-compose.yml,k8s-extra.yaml" {
		t.Fatalf("entries = %v", names)
	}
	_, b = k.req(t, "GET", "/api/fs/list?kind=windows&name=C:\\", "")
	_ = json.Unmarshal(b, &l)
	if l.Path != `C:\Users\irice` || l.Parent != `C:\Users` {
		t.Fatalf("windows home = %s", b)
	}
	if resp, _ := k.req(t, "GET", "/api/fs/list?kind=wsl&name=docker-desktop", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("internal distro = %d", resp.StatusCode)
	}
	if resp, _ := k.req(t, "GET", "/api/fs/list?kind=wsl&name=Ubuntu-26.04&path=relative", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("relative path = %d", resp.StatusCode)
	}
}

func TestFSStoppedDistroNeedsConsent(t *testing.T) {
	k := composeEnv(t)
	fw := k.s.WSL.(*FakeWSL)
	fw.mu.Lock()
	fw.distros = append(fw.distros, wsl.Distro{Name: "Debian", State: "Stopped", Version: 2})
	fw.mu.Unlock()
	resp, b := k.req(t, "GET", "/api/fs/list?kind=wsl&name=Debian", "")
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), `"stopped"`) {
		t.Fatalf("stopped = %d %s", resp.StatusCode, b)
	}
	if resp, _ := k.req(t, "GET", "/api/fs/list?kind=wsl&name=Debian&start=1", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("with consent = %d", resp.StatusCode)
	}
}

func TestComposePreview(t *testing.T) {
	k := composeEnv(t)
	req := func(path string) (int, ComposePreview, string) {
		resp, b := k.req(t, "POST", "/api/k8s/compose/preview", `{"kind":"wsl","name":"Ubuntu-26.04","path":"`+path+`"}`)
		var p ComposePreview
		_ = json.Unmarshal(b, &p)
		return resp.StatusCode, p, string(b)
	}
	code, p, raw := req("/home/irice/projects/shop/docker-compose.yml")
	if code != 200 || p.Kind != "compose" || p.Spec == nil || p.Spec.Name != "shop" || strings.Join(p.Order, ",") != "db,api,web" {
		t.Fatalf("preview = %d %s", code, raw)
	}
	if strings.Join(p.ReadFiles, ",") != ".env" || !strings.Contains(p.YAML, `"s3cret"`) || !strings.Contains(p.YAML, "DB_HOST") {
		t.Fatalf(".env must be read from the compose folder: %s", raw)
	}
	joined := strings.Join(p.Warnings, "\n")
	if !strings.Contains(joined, "healthcheck") || !strings.Contains(joined, "bind mount ~/cache") || strings.Contains(joined, "./init") {
		t.Fatalf("warnings = %v (a relative bind mount must convert, ~ must warn)", p.Warnings)
	}
	if !strings.Contains(p.YAML, `"path": "/home/irice/projects/shop/init"`) || !strings.Contains(p.YAML, `"workingDir": "/app"`) {
		t.Fatalf("bind mount must become a hostPath next to the compose file: %s", p.YAML)
	}
	code, p, _ = req("/home/irice/projects/shop/k8s-extra.yaml")
	if code != 200 || p.Kind != "manifest" || !strings.Contains(p.Manifest, "ConfigMap") {
		t.Fatalf("manifest = %d %+v", code, p)
	}
	if code, _, _ = req("/home/irice/projects/shop/nope.yml"); code != http.StatusNotFound {
		t.Fatalf("missing = %d", code)
	}
	if code, _, _ = req("/home/irice/projects/shop/.env"); code != http.StatusBadRequest {
		t.Fatalf("non-yaml = %d", code)
	}
}

func TestConfinedReaderRejectsEscapes(t *testing.T) {
	m := &MemFS{Files: map[string]string{"/p/.env": "A=1", "/secret": "x"}}
	rd := confinedReader(t.Context(), m, "/p")
	if _, err := rd(".env"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../secret", "/secret", "..", `..\secret`, "C:/x"} {
		if _, err := rd(bad); err == nil || !strings.Contains(err.Error(), "inside") {
			t.Errorf("%q must be rejected, got %v", bad, err)
		}
	}
}

func TestImportOnlyReadsTheMachineRunningKubernetes(t *testing.T) {
	k := composeEnv(t)
	fw := k.s.WSL.(*FakeWSL)
	fw.mu.Lock()
	fw.distros = append(fw.distros, wsl.Distro{Name: "Other", State: "Running", Version: 2})
	fw.mu.Unlock()
	// 沒有任何 machine 開 k8s：不限制
	_, b := k.req(t, "GET", "/api/fs/sources", "")
	var src FileSources
	_ = json.Unmarshal(b, &src)
	if len(src.WSL) != 2 || src.Owner != "" {
		t.Fatalf("no owner = %s", b)
	}
	k.enable(t) // k3s 開在 Ubuntu-26.04
	_, b = k.req(t, "GET", "/api/fs/sources", "")
	src = FileSources{}
	_ = json.Unmarshal(b, &src)
	if len(src.WSL) != 1 || src.WSL[0].Name != "Ubuntu-26.04" || src.Owner != "Ubuntu-26.04" {
		t.Fatalf("owner only = %s", b)
	}
	resp, b := k.req(t, "GET", "/api/fs/list?kind=wsl&name=Other", "")
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), "where Kubernetes runs (Ubuntu-26.04)") {
		t.Fatalf("other machine = %d %s", resp.StatusCode, b)
	}
	if resp, _ := k.req(t, "POST", "/api/k8s/compose/preview", `{"kind":"wsl","name":"Other","path":"/x/compose.yml"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("preview other machine = %d", resp.StatusCode)
	}
}
