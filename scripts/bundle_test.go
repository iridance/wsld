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
