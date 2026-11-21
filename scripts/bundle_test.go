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

func TestK3sBundleIsSelfContainedAndParses(t *testing.T) {
	b := K3sBundle()
	s := string(b)
	if strings.Count(s, "#!/usr/bin/env bash") != 1 || !strings.HasPrefix(s, "#!/usr/bin/env bash\n") {
		t.Fatal("bundle must have exactly one leading shebang")
	}
	if strings.Contains(s, "SCRIPT_DIR") || strings.Contains(s, "\n. \"$") {
		t.Fatal("bundle must not source external lib")
	}
	for _, want := range []string{"wsld_k3s_install_exec()", "wsld_render_k3s_dropin()", "install_k3s()", "uninstall_k3s()"} {
		if !strings.Contains(s, want) {
			t.Fatalf("bundle missing %s", want)
		}
	}
	if strings.Index(s, "wsld_k3s_install_exec()") > strings.Index(s, `case "${1:-}" in`) {
		t.Fatal("lib must precede main script")
	}
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = bytes.NewReader(b)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
}

func TestK3sBundleUsageErrorWithoutArgs(t *testing.T) {
	cmd := exec.Command("bash", "-s", "--")
	cmd.Stdin = bytes.NewReader(K3sBundle())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit without args:\n%s", out)
	}
}

// 回歸：`curl … | sh - </dev/null` 會讓 sh 的 stdin 被 /dev/null 取代（管線被蓋掉），
// 安裝腳本讀到空內容、curl 以 23 結束——實機 wsld k8s on 首次安裝曾因此失敗。
func TestK3sInstallDoesNotRedirectStdinOfPipedInstaller(t *testing.T) {
	for _, line := range strings.Split(string(K3sBundle()), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue // 註解會引用這個反例
		}
		if strings.Contains(line, "|") && strings.Contains(line, "sh -") && strings.Contains(line, "</dev/null") {
			t.Fatalf("installer pipe must not redirect sh stdin: %q", line)
		}
	}
	if !strings.Contains(string(K3sBundle()), "curl -sfL -o") {
		t.Fatal("installer should be downloaded to a file before running")
	}
}
