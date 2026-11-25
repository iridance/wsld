package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/config"
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
