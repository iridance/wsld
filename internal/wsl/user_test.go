package wsl

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSanitizeLinuxUsername(t *testing.T) {
	cases := map[string]string{
		`HOST\irice`: "irice",
		"Alice":      "alice",
		"bob-1":      "bob-1",
		"123bob":     "u123bob",
		"":           "wsluser",
		"---":        "wsluser",
		"root":       "wsluser",
	}
	for in, want := range cases {
		if got := SanitizeLinuxUsername(in); got != want {
			t.Fatalf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidLinuxUsername(t *testing.T) {
	if !ValidLinuxUsername("irice") || !ValidLinuxUsername("_x") || ValidLinuxUsername("root") || ValidLinuxUsername("1x") {
		t.Fatal("ValidLinuxUsername mismatch")
	}
}

func TestEnsureUserCreatesWhenMissing(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Debian1 -u root -- id -u irice":                   {err: errors.New("no such user")},
		"-d Debian1 -u root -- useradd -m -s /bin/bash irice": {},
		"-d Debian1 -u root -- usermod -aG sudo irice":         {err: errors.New("no sudo")},
		"-d Debian1 -u root -- usermod -aG wheel irice":        {err: errors.New("no wheel")},
	}}
	if err := EnsureUser(context.Background(), fr, "Debian1", "irice"); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) < 2 || strings.Join(fr.calls[1], " ") != "-d Debian1 -u root -- useradd -m -s /bin/bash irice" {
		t.Fatalf("calls = %v", fr.calls)
	}
}

func TestEnsureUserNoopWhenExists(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Debian1 -u root -- id -u irice": {stdout: "1000\n"},
	}}
	if err := EnsureUser(context.Background(), fr, "Debian1", "irice"); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("calls = %v", fr.calls)
	}
}
