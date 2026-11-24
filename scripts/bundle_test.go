package scripts

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestBootstrapBundleIsSelfContained(t *testing.T) {
	b := BootstrapBundle()
	s := string(b)
	if !strings.HasPrefix(s, "#!/usr/bin/env bash\n") {
		t.Fatalf("missing shebang: %q", s[:40])
	}
	if strings.Count(s, "#!/usr/bin/env bash") != 1 {
		t.Fatal("bundle must contain exactly one shebang")
	}
	if strings.Contains(s, "SCRIPT_DIR") || strings.Contains(s, ". \"$") || strings.Contains(s, "\nsource ") {
		t.Fatal("bundle must not source external lib")
	}
	for _, fn := range []string{"wsld_parse_args()", "wsld_pick_free_port()", "wsld_render_daemon_json()"} {
		if !strings.Contains(s, fn) {
			t.Fatalf("bundle missing %s", fn)
		}
	}
	if !strings.Contains(s, "WSLD_API_PORT=") {
		t.Fatal("bundle missing WSLD_API_PORT output")
	}
	// lib 必須在主腳本之前（函式先定義）
	if strings.Index(s, "wsld_parse_args()") > strings.Index(s, `wsld_parse_args "$@"`) {
		t.Fatal("lib must precede main script")
	}
}

func TestBootstrapBundleEmbedsDistrosTable(t *testing.T) {
	s := string(BootstrapBundle())
	i := strings.Index(s, "WSLD_DISTROS_TABLE='")
	if i < 0 {
		t.Fatal("bundle missing WSLD_DISTROS_TABLE")
	}
	if i > strings.Index(s, "wsld_distros_table()") {
		t.Fatal("WSLD_DISTROS_TABLE must be defined before lib")
	}
	for _, row := range DistroRows() {
		if !strings.Contains(s, row.OSID) {
			t.Fatalf("bundle missing distro %s", row.OSID)
		}
	}
}

func TestDistroRowsWellFormed(t *testing.T) {
	if strings.Contains(distrosTable, "'") {
		t.Fatal("distros.tsv must not contain single quotes (embedded in a single-quoted shell string)")
	}
	rows := DistroRows()
	if len(rows) == 0 {
		t.Fatal("no rows")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.OSID] {
			t.Fatalf("duplicate os_id %s", r.OSID)
		}
		seen[r.OSID] = true
		switch r.Family {
		case "debian", "arch", "suse", "rhel", "azure":
		default:
			t.Fatalf("%s: bad family %q", r.OSID, r.Family)
		}
		if len(r.WSLNames) == 0 {
			t.Fatalf("%s: no wsl names", r.OSID)
		}
	}
}

// wsl --list --online（2026-11-19 快照）。新增發行版時一併補進 distros.tsv。
var wslOnlineSnapshot = []string{
	"Ubuntu", "Ubuntu-26.04", "Ubuntu-24.04", "Ubuntu-22.04",
	"openSUSE-Tumbleweed", "openSUSE-Leap-16.1",
	"RLCPlus-9", "RLCPlus-10",
	"SUSE-Linux-Enterprise-15-SP7", "SUSE-Linux-Enterprise-16.1", "SUSE-Linux-Enterprise-15-SP6",
	"kali-linux", "Debian",
	"AlmaLinux-8", "AlmaLinux-9", "AlmaLinux-Kitten-10", "AlmaLinux-10",
	"archlinux", "FedoraLinux-45", "FedoraLinux-44", "eLxr", "AzureLinux-4",
	"OracleLinux_7_9", "OracleLinux_8_10", "OracleLinux_9_5",
}

func TestDistroForWSLNameCoversOnlineCatalogue(t *testing.T) {
	for _, name := range wslOnlineSnapshot {
		if _, ok := DistroForWSLName(name); !ok {
			t.Errorf("%s not classified in distros.tsv", name)
		}
	}
	if r, ok := DistroForWSLName("kali-linux"); !ok || r.Family != "debian" || r.DockerRepo != "debian" || r.DockerCodename != "trixie" {
		t.Fatalf("kali-linux: %+v", r)
	}
	if r, ok := DistroForWSLName("Ubuntu-24.04"); !ok || !r.Supported() {
		t.Fatalf("Ubuntu-24.04 should be supported: %+v", r)
	}
	if r, ok := DistroForWSLName("FedoraLinux-45"); !ok || r.Supported() {
		t.Fatalf("FedoraLinux-45 should be unsupported: %+v", r)
	}
	if _, ok := DistroForWSLName("my-custom-name"); ok {
		t.Fatal("unknown name must not match")
	}
}

func TestBootstrapBundleParsesWithBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command(bash, "-n")
	cmd.Stdin = bytes.NewReader(BootstrapBundle())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n failed: %v\n%s", err, out)
	}
}

func TestBootstrapBundleHelpRunsWithoutLib(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	cmd := exec.Command(bash, "-s", "--", "--help")
	cmd.Stdin = bytes.NewReader(BootstrapBundle())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--port") {
		t.Fatalf("--help: %v\n%s", err, out)
	}
}
