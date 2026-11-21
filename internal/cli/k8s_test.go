package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/config"
	"github.com/iridance/wsld/internal/k8s"
)

const r = "-d Ubuntu-26.04 -u root -- "

const nodeReadyJSON = `{"items":[{"metadata":{"name":"framework"},"status":{"nodeInfo":{"kubeletVersion":"v1.37.2+k3s1"},"addresses":[{"type":"InternalIP","address":"172.20.3.68"}],"conditions":[{"type":"Ready","status":"True"}]}}]}`

const k3sKubeconfig = `apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: Q0E=
    server: https://127.0.0.1:6443
  name: default
contexts:
- context:
    cluster: default
    user: default
  name: default
current-context: default
kind: Config
users:
- name: default
  user:
    client-certificate-data: Q0VSVA==
    client-key-data: S0VZ
`

func (h *harness) k8sReady() {
	h.app.K3sBundle = []byte("K3S-BUNDLE")
	h.app.KubeconfigPath = filepath.Join(h.cfgDir, "kube", "config")
	for k, v := range map[string]string{
		r + "test -x /usr/local/bin/k3s":                                        "",
		r + "systemctl is-active docker":                                        "active\n",
		r + "systemctl reset-failed k3s":                                        "",
		r + "systemctl start k3s":                                               "",
		r + "k3s kubectl get --raw /readyz":                                     "ok",
		r + "k3s kubectl get nodes -o json":                                     nodeReadyJSON,
		r + "cat /etc/rancher/k3s/k3s.yaml":                                     k3sKubeconfig,
		r + "/usr/local/bin/k3s-killall.sh":                                     "",
		r + "bash -c " + k8s.RemoveContainersScript:                             "",
		r + "systemctl is-active k3s":                                           "active\n",
		r + "systemctl show k3s -p MemoryCurrent --value":                       "439762944\n",
		r + "hostname -I":                                                       "172.20.3.68 172.17.0.1\n",
		r + "bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh install":   "",
		r + "bash -c cat > /tmp/wsld-k3s.sh && bash /tmp/wsld-k3s.sh uninstall": "",
	} {
		h.wsl.resp[k] = v
	}
}

func (h *harness) cfg(t *testing.T) config.Config {
	t.Helper()
	c, err := config.Load(h.app.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestK8sOnEnablesStartsAndLeavesWindowsKubeconfigAlone(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	if code := h.run("k8s", "on"); code != ExitOK {
		t.Fatalf("code %d, stderr=%s out=%s", code, h.errb, h.out)
	}
	if m, ok := h.cfg(t).K8sMachine(); !ok || m.Distro != "Ubuntu-26.04" {
		t.Fatalf("config must record k8s on: %+v %v", m, ok)
	}
	if _, err := os.Stat(h.app.KubeconfigPath); err == nil {
		t.Fatal("k8s on must not write the Windows kubeconfig")
	}
	if !strings.Contains(h.out.String(), "wsld k8s kubeconfig") {
		t.Fatalf("output should hint at the manual export: %s", h.out)
	}
}

func TestK8sOnRejectsSecondMachineNamingTheFirst(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	c := h.cfg(t)
	c.UpsertMachine(config.Machine{Distro: "Other", User: "u", APIEndpoint: "tcp://127.0.0.1:23751", K8s: true})
	if err := config.Save(h.app.ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	if code := h.run("k8s", "on"); code != ExitError {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(h.errb.String(), "Other") {
		t.Fatalf("error must name the current owner: %s", h.errb)
	}
	if m, _ := h.cfg(t).K8sMachine(); m.Distro != "Other" {
		t.Fatalf("owner must remain Other, got %q", m.Distro)
	}
}

func TestK8sOnFailureDoesNotPersistFlag(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	h.wsl.errs[r+"systemctl start k3s"] = errors.New("exit 1")
	if code := h.run("k8s", "on"); code == ExitOK {
		t.Fatal("expected failure")
	}
	if _, ok := h.cfg(t).K8sMachine(); ok {
		t.Fatal("failed start must not leave k8s = true")
	}
}

func TestK8sOffStopsAndClearsFlag(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	c := h.cfg(t)
	_ = c.SetK8s("Ubuntu-26.04", true)
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("k8s", "off"); code != ExitOK {
		t.Fatalf("code %d %s", code, h.errb)
	}
	if _, ok := h.cfg(t).K8sMachine(); ok {
		t.Fatal("flag must be cleared")
	}
	called := false
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "k3s-killall.sh") {
			called = true
		}
	}
	if !called {
		t.Fatalf("killall not run: %v", h.wsl.calls)
	}
}

func TestK8sOffWhenAlreadyOffIsNoop(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	if code := h.run("k8s", "off"); code != ExitOK {
		t.Fatalf("code %d", code)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "killall") {
			t.Fatal("must not run killall when no machine has k8s on")
		}
	}
}

func TestK8sStatusTextAndJSON(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	c := h.cfg(t)
	_ = c.SetK8s("Ubuntu-26.04", true)
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("k8s", "status"); code != ExitOK {
		t.Fatalf("code %d %s", code, h.errb)
	}
	for _, want := range []string{"ready", "v1.37.2+k3s1", "framework", "https://127.0.0.1:6443"} {
		if !strings.Contains(h.out.String(), want) {
			t.Fatalf("status missing %q:\n%s", want, h.out)
		}
	}
	h.out.Reset()
	if code := h.run("k8s", "status", "--json"); code != ExitOK {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(h.out.String(), `"state": "ready"`) {
		t.Fatalf("json: %s", h.out)
	}
}

func TestK8sStatusNoneEnabledSaysOff(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	if code := h.run("k8s", "status"); code != ExitOK {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(strings.ToLower(h.out.String()), "off") {
		t.Fatalf("out: %s", h.out)
	}
}

func TestK8sKubeconfigExportsWithoutStarting(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	c := h.cfg(t)
	_ = c.SetK8s("Ubuntu-26.04", true)
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("k8s", "kubeconfig"); code != ExitOK {
		t.Fatalf("code %d %s", code, h.errb)
	}
	if _, err := os.Stat(h.app.KubeconfigPath); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "systemctl start k3s") {
			t.Fatal("kubeconfig must not start k3s")
		}
	}
}

func TestK8sResetRequiresEnabledMachine(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	if code := h.run("k8s", "reset"); code != ExitError {
		t.Fatalf("code %d", code)
	}
}

func TestK8sResetNeedsConfirmationOrYes(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	h.wsl.resp[r+"bash -c "+k8s.WipeStateScript] = ""
	c := h.cfg(t)
	_ = c.SetK8s("Ubuntu-26.04", true)
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("k8s", "reset"); code != ExitError {
		t.Fatalf("without --yes and no tty confirmation must refuse, code %d", code)
	}
	if code := h.run("k8s", "reset", "--yes"); code != ExitOK {
		t.Fatalf("code %d %s", code, h.errb)
	}
}

func TestK8sUninstallClearsFlag(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	c := h.cfg(t)
	_ = c.SetK8s("Ubuntu-26.04", true)
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("k8s", "uninstall", "--yes"); code != ExitOK {
		t.Fatalf("code %d %s", code, h.errb)
	}
	if _, ok := h.cfg(t).K8sMachine(); ok {
		t.Fatal("flag must be cleared")
	}
}

func TestK8sUsageErrors(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("k8s"); code != ExitUsage {
		t.Fatalf("no subcommand: %d", code)
	}
	if code := h.run("k8s", "bogus"); code != ExitUsage {
		t.Fatalf("bogus: %d", code)
	}
}

func TestStartResumesK3sForActiveMachineWithK8s(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	c := h.cfg(t)
	_ = c.SetK8s("Ubuntu-26.04", true)
	_ = config.Save(h.app.ConfigPath, c)
	if code := h.run("start"); code != ExitOK {
		t.Fatalf("code %d %s", code, h.errb)
	}
	started := false
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "systemctl start k3s") {
			started = true
		}
	}
	if !started {
		t.Fatalf("wsld start must resume k3s: %v", h.wsl.calls)
	}
}

func TestStartLeavesK3sAloneWhenOff(t *testing.T) {
	h := newHarness(t, true)
	h.k8sReady()
	if code := h.run("start"); code != ExitOK {
		t.Fatalf("code %d", code)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "k3s") {
			t.Fatalf("k3s must not be touched when off: %s", c)
		}
	}
}
