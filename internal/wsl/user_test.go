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
		"-d Debian1 -u root -- usermod -aG sudo irice":        {err: errors.New("no sudo")},
		"-d Debian1 -u root -- usermod -aG wheel irice":       {err: errors.New("no wheel")},
	}}
	if err := EnsureUser(context.Background(), fr, "Debian1", "irice", ""); err != nil {
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
	if err := EnsureUser(context.Background(), fr, "Debian1", "irice", ""); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("calls = %v", fr.calls)
	}
}

func TestEnsureUserSetsPasswordViaStdin(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Debian1 -u root -- id -u irice":                   {err: errors.New("no such user")},
		"-d Debian1 -u root -- useradd -m -s /bin/bash irice": {},
		"-d Debian1 -u root -- chpasswd":                      {},
		"-d Debian1 -u root -- usermod -aG sudo irice":        {},
		"-d Debian1 -u root -- usermod -aG wheel irice":       {err: errors.New("no wheel")},
	}}
	if err := EnsureUser(context.Background(), fr, "Debian1", "irice", "s3:cr et"); err != nil {
		t.Fatal(err)
	}
	if len(fr.stdin) != 1 || fr.stdin[0] != "irice:s3:cr et\n" {
		t.Fatalf("stdin = %q", fr.stdin)
	}
	for _, c := range fr.calls {
		if strings.Contains(strings.Join(c, " "), "s3:cr") {
			t.Fatalf("password leaked into argv: %v", c)
		}
	}
}

func TestEnsureUserExistingKeepsPassword(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Debian1 -u root -- id -u irice": {stdout: "1000\n"},
	}}
	if err := EnsureUser(context.Background(), fr, "Debian1", "irice", "pw"); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 || len(fr.stdin) != 0 {
		t.Fatalf("calls = %v stdin = %v", fr.calls, fr.stdin)
	}
}

func TestEnsureUserRejectsMultilinePassword(t *testing.T) {
	fr := &fakeRunner{}
	if err := EnsureUser(context.Background(), fr, "Debian1", "irice", "a\nroot:x"); err == nil {
		t.Fatal("expected error")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("calls = %v", fr.calls)
	}
}

func TestEnsureUserChpasswdFailure(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Debian1 -u root -- id -u irice":                   {err: errors.New("no such user")},
		"-d Debian1 -u root -- useradd -m -s /bin/bash irice": {},
		"-d Debian1 -u root -- chpasswd":                      {err: errors.New("exit 1"), stderr: "chpasswd: PAM failure"},
	}}
	err := EnsureUser(context.Background(), fr, "Debian1", "irice", "pw")
	if err == nil || !strings.Contains(err.Error(), "PAM failure") || strings.Contains(err.Error(), "pw\n") {
		t.Fatalf("err = %v", err)
	}
}

func TestWithDefaultUser(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "[user]\ndefault=bob\n"},
		{"[boot]\nsystemd=true\n", "[boot]\nsystemd=true\n\n[user]\ndefault=bob\n"},
		{"[user]\ndefault=alice\n[boot]\nsystemd=true\n", "[user]\ndefault=bob\n[boot]\nsystemd=true\n"},
		{"[boot]\nsystemd=true\n[User]\n# hi\n", "[boot]\nsystemd=true\n[User]\ndefault=bob\n# hi\n"},
		{"[network]\ndefault = x\r\n", "[network]\ndefault = x\n\n[user]\ndefault=bob\n"},
	}
	for _, c := range cases {
		if got := WithDefaultUser(c.in, "bob"); got != c.want {
			t.Fatalf("WithDefaultUser(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSetDefaultUserMergesAndTerminates(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Debian1 -u root -- test -e /etc/wsl.conf":     {},
		"-d Debian1 -u root -- cat /etc/wsl.conf":         {stdout: "[boot]\nsystemd=true\n"},
		"-d Debian1 -u root -- sh -c cat > /etc/wsl.conf": {},
		"--terminate Debian1":                             {},
	}}
	if err := SetDefaultUser(context.Background(), fr, "Debian1", "bob"); err != nil {
		t.Fatal(err)
	}
	if len(fr.stdin) != 1 || fr.stdin[0] != "[boot]\nsystemd=true\n\n[user]\ndefault=bob\n" {
		t.Fatalf("stdin = %q", fr.stdin)
	}
	if last := strings.Join(fr.calls[len(fr.calls)-1], " "); last != "--terminate Debian1" {
		t.Fatalf("last call = %q", last)
	}
}

func TestSetDefaultUserRefusesWhenUnreadable(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Debian1 -u root -- test -e /etc/wsl.conf": {},
		"-d Debian1 -u root -- cat /etc/wsl.conf":     {err: errors.New("denied")},
	}}
	if err := SetDefaultUser(context.Background(), fr, "Debian1", "bob"); err == nil {
		t.Fatal("expected error")
	}
	for _, c := range fr.calls {
		if strings.Contains(strings.Join(c, " "), "cat > /etc/wsl.conf") {
			t.Fatal("must not overwrite unreadable wsl.conf")
		}
	}
}
