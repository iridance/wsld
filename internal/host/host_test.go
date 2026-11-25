package host

import (
	"slices"
	"strings"
	"testing"
)

func TestWSLShellArgsUsesScriptPTY(t *testing.T) {
	args := WSLShellArgs("Ubuntu-26.04", "irice", 100, 30)
	if args[0] != "-d" || args[1] != "Ubuntu-26.04" {
		t.Fatalf("distro = %v", args)
	}
	if !slices.Contains(args, "-u") || !slices.Contains(args, "irice") {
		t.Fatalf("user missing: %v", args)
	}
	if !slices.Contains(args, "--cd") || !slices.Contains(args, "~") {
		t.Fatalf("expected --cd ~ to avoid path translate: %v", args)
	}
	if !slices.Contains(args, "script") || !slices.Contains(args, "-qefc") {
		t.Fatalf("expected script -qefc for PTY: %v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "bash -li") || !strings.Contains(joined, "COLUMNS=100") || !strings.Contains(joined, "LINES=30") {
		t.Fatalf("inner shell = %s", joined)
	}
}

func TestWSLShellArgsDefaultsSizeAndOmitsEmptyUser(t *testing.T) {
	args := WSLShellArgs("Debian", "", 0, 0)
	if slices.Contains(args, "-u") {
		t.Fatalf("empty user should omit -u: %v", args)
	}
	// Debian 等非預設 distro 仍必須 --cd ~
	cdAt := slices.Index(args, "--cd")
	if cdAt < 0 || cdAt+1 >= len(args) || args[cdAt+1] != "~" {
		t.Fatalf("Debian must use --cd ~: %v", args)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "COLUMNS=80") || !strings.Contains(joined, "LINES=24") {
		t.Fatalf("default size: %s", joined)
	}
}

func TestOpenRejectsEmptyDistro(t *testing.T) {
	if err := OpenFolder(""); err == nil {
		t.Fatal("expected error")
	}
	if err := OpenTerminal(""); err == nil {
		t.Fatal("expected error")
	}
	if _, err := StartShell("", "", 80, 24); err == nil {
		t.Fatal("expected error")
	}
}

func TestWSLCommandArgsPlainPipe(t *testing.T) {
	args := WSLCommandArgs("Ubuntu-26.04", "root", []string{"k3s", "kubectl", "logs", "web-1", "-n", "default", "-f"}, false, 0, 0)
	want := []string{"-d", "Ubuntu-26.04", "-u", "root", "--cd", "~", "--", "k3s", "kubectl", "logs", "web-1", "-n", "default", "-f"}
	if !slices.Equal(args, want) {
		t.Fatalf("args = %v", args)
	}
}

func TestWSLCommandArgsTTYWrapsInScriptWithQuoting(t *testing.T) {
	args := WSLCommandArgs("Ubuntu-26.04", "", []string{"k3s", "kubectl", "exec", "-it", "web-1", "--", "sh", "-c", "command -v bash && exec bash || exec sh"}, true, 120, 40)
	if slices.Contains(args, "-u") {
		t.Fatalf("empty user should omit -u: %v", args)
	}
	i := slices.Index(args, "script")
	if i < 0 || args[i+1] != "-qefc" {
		t.Fatalf("expected script -qefc: %v", args)
	}
	inner := args[i+2]
	for _, want := range []string{"COLUMNS=120", "LINES=40", "stty cols 120 rows 40", "'command -v bash && exec bash || exec sh'", "exec k3s kubectl exec -it web-1 -- sh -c"} {
		if !strings.Contains(inner, want) {
			t.Fatalf("inner command missing %q:\n%s", want, inner)
		}
	}
	if args[len(args)-1] != "/dev/null" {
		t.Fatalf("script must log to /dev/null: %v", args)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{"plain": "plain", "a b": "'a b'", "it's": `'it'\''s'`, "": "''", "x;y": "'x;y'"}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFolderPathUsesTheUsersHome(t *testing.T) {
	cases := map[string]string{
		"":      `\\wsl$\Ubuntu\`,
		"irice": `\\wsl$\Ubuntu\home\irice`,
		"root":  `\\wsl$\Ubuntu\root`, // root 的家目錄是 /root，不是 /home/root
	}
	for user, want := range cases {
		if got := FolderPath("Ubuntu", user); got != want {
			t.Errorf("user %q: got %s, want %s", user, got, want)
		}
	}
}
