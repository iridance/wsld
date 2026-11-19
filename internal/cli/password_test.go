package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNewUserPasswordFromStdin(t *testing.T) {
	a := &App{Stdin: strings.NewReader("s3cret pw\r\nignored\n"), Stderr: &bytes.Buffer{}}
	pw, err := a.newUserPassword(true)("bob")
	if err != nil || pw != "s3cret pw" {
		t.Fatalf("pw = %q, %v", pw, err)
	}
	a = &App{Stdin: strings.NewReader(""), Stderr: &bytes.Buffer{}}
	if pw, err := a.newUserPassword(true)("bob"); err != nil || pw != "" {
		t.Fatalf("empty stdin: %q, %v", pw, err)
	}
}

func TestNewUserPasswordPromptConfirms(t *testing.T) {
	answers := []string{"a", "b", "pw", "pw"}
	var prompts []string
	errb := &bytes.Buffer{}
	a := &App{Stderr: errb, ReadPassword: func(p string) (string, error) {
		prompts = append(prompts, p)
		v := answers[0]
		answers = answers[1:]
		return v, nil
	}}
	pw, err := a.newUserPassword(false)("bob")
	if err != nil || pw != "pw" {
		t.Fatalf("pw = %q, %v", pw, err)
	}
	if len(prompts) != 4 || !strings.Contains(prompts[0], "bob") || !strings.Contains(errb.String(), "match") {
		t.Fatalf("prompts = %q err = %q", prompts, errb.String())
	}
}

func TestNewUserPasswordGivesUpAfterMismatches(t *testing.T) {
	n := 0
	a := &App{Stderr: &bytes.Buffer{}, ReadPassword: func(string) (string, error) {
		n++
		return strings.Repeat("x", n), nil
	}}
	if _, err := a.newUserPassword(false)("bob"); err == nil {
		t.Fatal("expected error after repeated mismatch")
	}
}

func TestNewUserPasswordSkipsWithoutTerminalOrEmpty(t *testing.T) {
	a := &App{Stderr: &bytes.Buffer{}, ReadPassword: func(string) (string, error) { return "", ErrNoTerminal }}
	if pw, err := a.newUserPassword(false)("bob"); err != nil || pw != "" {
		t.Fatalf("no tty: %q, %v", pw, err)
	}
	calls := 0
	a = &App{Stderr: &bytes.Buffer{}, ReadPassword: func(string) (string, error) { calls++; return "", nil }}
	if pw, err := a.newUserPassword(false)("bob"); err != nil || pw != "" || calls != 1 {
		t.Fatalf("empty: %q, %v, calls=%d", pw, err, calls)
	}
	a = &App{Stderr: &bytes.Buffer{}, ReadPassword: func(string) (string, error) { return "", errors.New("boom") }}
	if _, err := a.newUserPassword(false)("bob"); err == nil {
		t.Fatal("read error should propagate")
	}
	a = &App{Stderr: &bytes.Buffer{}}
	if pw, err := a.newUserPassword(false)("bob"); err != nil || pw != "" {
		t.Fatalf("nil reader: %q, %v", pw, err)
	}
}
