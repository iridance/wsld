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
