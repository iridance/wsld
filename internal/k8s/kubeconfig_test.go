package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const k3sYAML = `apiVersion: v1
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
preferences: {}
users:
- name: default
  user:
    client-certificate-data: Q0VSVA==
    client-key-data: S0VZ
`

const userYAML = `# my own config
apiVersion: v1
kind: Config
current-context: prod
clusters:
- name: prod
  cluster:
    server: https://prod.example.com
contexts:
- name: prod
  context:
    cluster: prod
    user: me
users:
- name: me
  user:
    token: secret
`

type kubeconfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server string `yaml:"server"`
			CA     string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token string `yaml:"token"`
			Cert  string `yaml:"client-certificate-data"`
		} `yaml:"user"`
	} `yaml:"users"`
}

func parseKC(t *testing.T, b []byte) kubeconfig {
	t.Helper()
	var k kubeconfig
	if err := yaml.Unmarshal(b, &k); err != nil {
		t.Fatalf("output is not valid YAML: %v\n%s", err, b)
	}
	return k
}

func TestMergeIntoEmptyCreatesNamedEntries(t *testing.T) {
	out, err := MergeKubeconfig(nil, []byte(k3sYAML), "Ubuntu-26.04")
	if err != nil {
		t.Fatal(err)
	}
	k := parseKC(t, out)
	if k.CurrentContext != "wsld-Ubuntu-26.04" {
		t.Fatalf("current-context = %q (empty config should adopt it)", k.CurrentContext)
	}
	if len(k.Clusters) != 1 || k.Clusters[0].Name != "wsld-Ubuntu-26.04" || k.Clusters[0].Cluster.Server != "https://127.0.0.1:6443" {
		t.Fatalf("clusters = %+v", k.Clusters)
	}
	if len(k.Contexts) != 1 || k.Contexts[0].Context.Cluster != "wsld-Ubuntu-26.04" || k.Contexts[0].Context.User != "wsld-Ubuntu-26.04" {
		t.Fatalf("contexts = %+v", k.Contexts)
	}
	if len(k.Users) != 1 || k.Users[0].Name != "wsld-Ubuntu-26.04" || k.Users[0].User.Cert != "Q0VSVA==" {
		t.Fatalf("users = %+v", k.Users)
	}
}

func TestMergeKeepsExistingContextsAndCurrent(t *testing.T) {
	out, err := MergeKubeconfig([]byte(userYAML), []byte(k3sYAML), "d1")
	if err != nil {
		t.Fatal(err)
	}
	k := parseKC(t, out)
	if k.CurrentContext != "prod" {
		t.Fatalf("current-context changed to %q", k.CurrentContext)
	}
	if len(k.Clusters) != 2 || len(k.Contexts) != 2 || len(k.Users) != 2 {
		t.Fatalf("want 2 each, got %d/%d/%d", len(k.Clusters), len(k.Contexts), len(k.Users))
	}
	if k.Users[0].Name != "me" || k.Users[0].User.Token != "secret" {
		t.Fatalf("existing user damaged: %+v", k.Users[0])
	}
	if !strings.Contains(string(out), "# my own config") {
		t.Fatalf("comment lost:\n%s", out)
	}
}

func TestMergeIsIdempotentAndReplacesStaleEntry(t *testing.T) {
	once, err := MergeKubeconfig([]byte(userYAML), []byte(k3sYAML), "d1")
	if err != nil {
		t.Fatal(err)
	}
	renewed := strings.ReplaceAll(k3sYAML, "Q0VSVA==", "TkVX")
	twice, err := MergeKubeconfig(once, []byte(renewed), "d1")
	if err != nil {
		t.Fatal(err)
	}
	k := parseKC(t, twice)
	if len(k.Contexts) != 2 || len(k.Users) != 2 || len(k.Clusters) != 2 {
		t.Fatalf("duplicated entries: %d/%d/%d", len(k.Clusters), len(k.Contexts), len(k.Users))
	}
	for _, u := range k.Users {
		if u.Name == "wsld-d1" && u.User.Cert != "TkVX" {
			t.Fatalf("stale credentials kept: %+v", u)
		}
	}
}

func TestMergeTwoDistrosCoexist(t *testing.T) {
	a, _ := MergeKubeconfig(nil, []byte(k3sYAML), "a")
	b, err := MergeKubeconfig(a, []byte(k3sYAML), "b")
	if err != nil {
		t.Fatal(err)
	}
	k := parseKC(t, b)
	if len(k.Contexts) != 2 || k.CurrentContext != "wsld-a" {
		t.Fatalf("contexts=%d current=%q", len(k.Contexts), k.CurrentContext)
	}
}

func TestMergeForcesLoopbackServer(t *testing.T) {
	src := strings.ReplaceAll(k3sYAML, "https://127.0.0.1:6443", "https://172.20.3.68:6443")
	out, err := MergeKubeconfig(nil, []byte(src), "d")
	if err != nil {
		t.Fatal(err)
	}
	if k := parseKC(t, out); k.Clusters[0].Cluster.Server != "https://127.0.0.1:6443" {
		t.Fatalf("server = %q", k.Clusters[0].Cluster.Server)
	}
}

func TestMergeRejectsGarbage(t *testing.T) {
	if _, err := MergeKubeconfig(nil, []byte("not: [valid"), "d"); err == nil {
		t.Fatal("expected error for invalid k3s yaml")
	}
	if _, err := MergeKubeconfig([]byte("{{{"), []byte(k3sYAML), "d"); err == nil {
		t.Fatal("expected error for invalid existing yaml (must not overwrite)")
	}
	if _, err := MergeKubeconfig(nil, []byte("apiVersion: v1\nkind: Config\n"), "d"); err == nil {
		t.Fatal("expected error when k3s yaml has no cluster/user")
	}
}

func TestContextName(t *testing.T) {
	if ContextName("Ubuntu-26.04") != "wsld-Ubuntu-26.04" {
		t.Fatal(ContextName("Ubuntu-26.04"))
	}
}

func TestExportKubeconfigCreatesDirAndFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), ".kube", "config")
	if err := ExportKubeconfig(p, []byte(k3sYAML), "d1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if k := parseKC(t, b); k.CurrentContext != "wsld-d1" {
		t.Fatalf("current-context = %q", k.CurrentContext)
	}
	if _, err := os.Stat(p + ".wsld-bak"); err == nil {
		t.Fatal("no backup expected when there was no previous file")
	}
}

func TestExportKubeconfigBacksUpOnceAndMerges(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte(userYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExportKubeconfig(p, []byte(k3sYAML), "d1"); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(p + ".wsld-bak")
	if err != nil || string(bak) != userYAML {
		t.Fatalf("backup = %q, %v", bak, err)
	}
	if err := ExportKubeconfig(p, []byte(k3sYAML), "d1"); err != nil {
		t.Fatal(err)
	}
	bak2, _ := os.ReadFile(p + ".wsld-bak")
	if string(bak2) != userYAML {
		t.Fatal("backup of the original must not be overwritten by later exports")
	}
	b, _ := os.ReadFile(p)
	if k := parseKC(t, b); len(k.Contexts) != 2 || k.CurrentContext != "prod" {
		t.Fatalf("contexts=%d current=%q", len(k.Contexts), k.CurrentContext)
	}
}

func TestExportKubeconfigDoesNotTouchUnparsableFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(p, []byte("{{{ not yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ExportKubeconfig(p, []byte(k3sYAML), "d1"); err == nil {
		t.Fatal("expected error")
	}
	if b, _ := os.ReadFile(p); string(b) != "{{{ not yaml" {
		t.Fatalf("file was modified: %q", b)
	}
}

func TestDefaultKubeconfigPathHonorsEnv(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := DefaultKubeconfigPath(); got != filepath.Join(home, ".kube", "config") {
		t.Fatalf("got %q", got)
	}
}

func TestHasExportedContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if HasExportedContext(path, "Debian") {
		t.Fatal("missing file must be false")
	}
	if err := ExportKubeconfig(path, []byte(k3sYAML), "Debian"); err != nil {
		t.Fatal(err)
	}
	if !HasExportedContext(path, "Debian") {
		t.Fatal("exported context must be found")
	}
	if HasExportedContext(path, "Ubuntu-26.04") {
		t.Fatal("other distro must not match")
	}
	if err := os.WriteFile(path, []byte(": : not yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if HasExportedContext(path, "Debian") {
		t.Fatal("unparsable file must be false")
	}
}
