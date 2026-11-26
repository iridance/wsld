package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/k8s"
)

func TestK8sStatusWhenOffDoesNotTouchWSL(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("k8s", "status"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if out := h.out.String(); !strings.Contains(out, "Kubernetes: off") || !strings.Contains(out, "k8s-engine") || !strings.Contains(out, "https://127.0.0.1:26443") {
		t.Fatalf("out:\n%s", out)
	}
	if len(h.wsl.calls) != 0 {
		t.Fatalf("k8s off: no wsl.exe calls, got %v", h.wsl.calls)
	}
	h.out.Reset()
	if code := h.run("k8s", "status", "--json"); code != 0 {
		t.Fatalf("json code=%d", code)
	}
	var v struct {
		Enabled bool `json:"enabled"`
		Status  struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &v); err != nil || v.Enabled || v.Status.State != "off" {
		t.Fatalf("json: %s %v", h.out, err)
	}
}

func TestK8sOffSavesConfigWithoutAK8sDistro(t *testing.T) {
	h := newHarness(t, true)
	c, _ := config.Load(h.app.ConfigPath)
	c.K8s = true
	if err := config.Save(h.app.ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	if code := h.run("k8s", "off"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if c, _ := config.Load(h.app.ConfigPath); c.K8s {
		t.Fatal("k8s must be saved off")
	}
}

func TestK8sNeedsASubcommand(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("k8s"); code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("k8s", "bogus"); code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
}

func TestKubeconfigPathSitsBesideTheConfig(t *testing.T) {
	if got := KubeconfigPath("/x/wsld/config.toml"); got != "/x/wsld/kubeconfig.yaml" {
		t.Fatalf("got %q", got)
	}
}

func TestK8sResetNeedsYesAndClusterCommandsNeedK8sOn(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("k8s", "reset"); code != ExitUsage {
		t.Fatalf("reset without --yes: code=%d", code)
	}
	for _, args := range [][]string{{"k8s", "pods"}, {"k8s", "svc"}, {"k8s", "restart"}, {"k8s", "reset", "--yes"}, {"k8s", "forward", "ls"}, {"kubectl", "get", "pods"}} {
		h.errb.Reset()
		if code := h.run(args...); code != ExitNotReady {
			t.Fatalf("%v while off: code=%d err=%s", args, code, h.errb)
		}
	}
	if len(h.wsl.calls) != 0 {
		t.Fatalf("k8s off: no wsl.exe calls, got %v", h.wsl.calls)
	}
}

func TestK8sForwardUsage(t *testing.T) {
	h := newHarness(t, true)
	c, _ := config.Load(h.app.ConfigPath)
	c.K8s = true
	if err := config.Save(h.app.ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"k8s", "forward", "deploy/web", "8080"}, {"k8s", "forward", "svc/web", "x"}, {"k8s", "forward", "a", "b", "c"}, {"k8s", "forward", "rm", "x"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
}

func TestFormatServicePorts(t *testing.T) {
	got := FormatServicePorts([]k8s.ServicePort{{Port: 80, NodePort: 30080, Protocol: "TCP"}, {Port: 53, Protocol: "UDP"}})
	if got != "80:30080/TCP,53/UDP" {
		t.Fatalf("got %q", got)
	}
	if FormatServicePorts(nil) != "-" {
		t.Fatal("empty ports")
	}
}

func TestComposeLocation(t *testing.T) {
	cases := map[string][3]string{
		`\\wsl.localhost\Ubuntu\home\a\shop`: {"wsl", "Ubuntu", "/home/a/shop"},
		`\\wsl$\Arch\srv`:                    {"wsl", "Arch", "/srv"},
		`C:\Users\a\shop`:                    {"windows", "", `C:\Users\a\shop`},
		"/home/a/shop":                       {"wsl", "", "/home/a/shop"},
	}
	for in, want := range cases {
		k, s, d := composeLocation(in)
		if [3]string{k, s, d} != want {
			t.Errorf("%s → %s %s %s", in, k, s, d)
		}
	}
}

func TestK8sComposeDryRunAndApplyRejectsCompose(t *testing.T) {
	h := newHarness(t, true)
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	compose := filepath.Join(dir, "docker-compose.yml")
	_ = os.WriteFile(compose, []byte("services:\n  db:\n    image: postgres:16\n    environment:\n      POSTGRES_PASSWORD: ${PW}\n  api:\n    image: shop/api\n    ports: [\"3000:3000\"]\n    depends_on: [db]\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("PW=s3cret\n"), 0o644)
	if code := h.run("k8s", "compose", "-f", compose, "--dry-run"); code != 0 {
		t.Fatalf("dry-run: code=%d err=%s", code, h.errb)
	}
	out := h.out.String()
	if !strings.Contains(out, `"namespace": "shop"`) || !strings.Contains(out, "s3cret") || strings.Index(out, `"name": "db"`) > strings.Index(out, `"name": "api"`) {
		t.Fatalf("manifest:\n%s", out)
	}
	if len(h.wsl.calls) != 0 {
		t.Fatalf("dry-run must not touch WSL: %v", h.wsl.calls)
	}
	if code := h.run("k8s", "apply", "-f", compose); code != ExitUsage {
		t.Fatalf("apply compose: code=%d", code)
	}
	if code := h.run("k8s", "deploy", "nginx"); code != ExitUsage {
		t.Fatalf("deploy must be gone: code=%d", code)
	}
	if code := h.run("k8s", "stack", "-f", "x.json"); code != ExitUsage {
		t.Fatalf("stack -f must be gone: code=%d", code)
	}
}
