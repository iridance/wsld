package wsl

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16le(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, len(u)*2+2)
	b = append(b, 0xFF, 0xFE)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

func TestDecodeOutputUTF16WithBOM(t *testing.T) {
	got := DecodeOutput(utf16le("  NAME  STATE\r\n* Ubuntu Running 2\r\n"))
	if !strings.Contains(got, "Ubuntu Running 2") || strings.ContainsRune(got, '\x00') {
		t.Fatalf("decoded = %q", got)
	}
}

func TestDecodeOutputUTF16WithoutBOM(t *testing.T) {
	raw := utf16le("Ubuntu")[2:]
	if got := DecodeOutput(raw); got != "Ubuntu" {
		t.Fatalf("got %q", got)
	}
}

func TestDecodeOutputPassesThroughUTF8(t *testing.T) {
	if got := DecodeOutput([]byte("hello 世界\n")); got != "hello 世界\n" {
		t.Fatalf("got %q", got)
	}
}

func TestParseList(t *testing.T) {
	txt := "  NAME              STATE           VERSION\r\n* Ubuntu-26.04      Running         2\r\n  docker-desktop    Stopped         2\r\n  Legacy            Stopped         1\r\n"
	got := ParseList(txt)
	if len(got) != 3 {
		t.Fatalf("len = %d: %+v", len(got), got)
	}
	if got[0] != (Distro{Name: "Ubuntu-26.04", State: "Running", Version: 2, Default: true}) {
		t.Fatalf("got[0] = %+v", got[0])
	}
	if got[2].Version != 1 || got[2].Default {
		t.Fatalf("got[2] = %+v", got[2])
	}
}

func TestParseListEmpty(t *testing.T) {
	if got := ParseList("Windows Subsystem for Linux has no installed distributions.\r\n"); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestPickUbuntu(t *testing.T) {
	list := []Distro{
		{Name: "Debian", State: "Running", Version: 2, Default: true},
		{Name: "Ubuntu-22.04", State: "Stopped", Version: 2},
		{Name: "Ubuntu-26.04", State: "Running", Version: 2},
		{Name: "Ubuntu", State: "Stopped", Version: 1},
	}
	if d, ok := PickUbuntu(list); !ok || d.Name != "Ubuntu-22.04" {
		t.Fatalf("PickUbuntu = %+v, %v", d, ok)
	}
	list[2].Default, list[0].Default = true, false
	if d, _ := PickUbuntu(list); d.Name != "Ubuntu-26.04" {
		t.Fatalf("should prefer default Ubuntu, got %+v", d)
	}
	if _, ok := PickUbuntu([]Distro{{Name: "Ubuntu", Version: 1}}); ok {
		t.Fatal("WSL1 Ubuntu must not be picked")
	}
}

func TestFindDistro(t *testing.T) {
	list := []Distro{{Name: "Ubuntu-26.04"}, {Name: "Debian"}}
	if d, ok := FindDistro(list, "ubuntu-26.04"); !ok || d.Name != "Ubuntu-26.04" {
		t.Fatalf("case-insensitive find failed: %+v %v", d, ok)
	}
	if _, ok := FindDistro(list, "Nope"); ok {
		t.Fatal("should not find")
	}
}

// fakeRunner 以 args 串接為 key 回傳結果。
type fakeRunner struct {
	calls [][]string
	stdin []string
	out   map[string]result
}

type result struct {
	stdout, stderr string
	err            error
}

func (f *fakeRunner) Run(_ context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	f.calls = append(f.calls, args)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = append(f.stdin, string(b))
	}
	r, ok := f.out[strings.Join(args, " ")]
	if !ok {
		return nil, nil, errors.New("fake: unexpected args " + strings.Join(args, " "))
	}
	return []byte(r.stdout), []byte(r.stderr), r.err
}

func TestListDistrosUsesRunner(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"--list --verbose": {stdout: string(utf16le("  NAME STATE VERSION\r\n* Ubuntu Running 2\r\n"))},
	}}
	got, err := ListDistros(context.Background(), fr)
	if err != nil || len(got) != 1 || got[0].Name != "Ubuntu" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRunInBuildsArgs(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{
		"-d Ubuntu -u root -- systemctl is-active docker": {stdout: "active\n"},
		"-d Ubuntu -- true": {},
	}}
	out, _, err := RunIn(context.Background(), fr, "Ubuntu", "root", nil, "systemctl", "is-active", "docker")
	if err != nil || strings.TrimSpace(string(out)) != "active" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, _, err := RunIn(context.Background(), fr, "Ubuntu", "", nil, "true"); err != nil {
		t.Fatal(err)
	}
}

func TestRunInPipesStdin(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{"-d Ubuntu -u root -- bash -s -- --user bob": {}}}
	_, _, err := RunIn(context.Background(), fr, "Ubuntu", "root", strings.NewReader("echo hi"), "bash", "-s", "--", "--user", "bob")
	if err != nil || len(fr.stdin) != 1 || fr.stdin[0] != "echo hi" {
		t.Fatalf("stdin = %v err=%v", fr.stdin, err)
	}
}

func TestExitCodeExtraction(t *testing.T) {
	if ExitCode(nil) != 0 {
		t.Fatal("nil → 0")
	}
	if ExitCode(&ExitError{Code: 3}) != 3 {
		t.Fatal("ExitError code")
	}
	if ExitCode(errors.New("x")) != -1 {
		t.Fatal("unknown → -1")
	}
}

func TestParseOnline(t *testing.T) {
	txt := "The following is a list of valid distributions that can be installed.\r\nInstall using 'wsl.exe --install <Distro>'.\r\n\r\nNAME                            FRIENDLY NAME\r\nUbuntu                          Ubuntu\r\nDebian                          Debian GNU/Linux\r\nUbuntu-24.04                    Ubuntu 24.04 LTS\r\n"
	got := ParseOnline(txt)
	if len(got) != 3 {
		t.Fatalf("len = %d: %+v", len(got), got)
	}
	if got[1] != (OnlineDistro{Name: "Debian", Friendly: "Debian GNU/Linux"}) || got[2].Friendly != "Ubuntu 24.04 LTS" {
		t.Fatalf("got = %+v", got)
	}
}

func TestListOnlineAndInstall(t *testing.T) {
	r := &fakeRunner{out: map[string]result{
		"--list --online":                                {stdout: "NAME    FRIENDLY NAME\r\nDebian  Debian GNU/Linux\r\n"},
		"--install -d Debian --no-launch":                {stdout: "Installing: Debian\r\n"},
		"--install -d Debian --name Debian1 --no-launch": {stdout: "Installing: Debian as Debian1\r\n"},
	}}
	got, err := ListOnline(context.Background(), r)
	if err != nil || len(got) != 1 || got[0].Name != "Debian" {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := Install(context.Background(), r, "Debian", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), r, "Debian", "Debian1", 0); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), r, "", "", 0); err == nil {
		t.Fatal("empty distribution must be rejected")
	}
	if err := Install(context.Background(), r, "bad name", "", 0); err == nil {
		t.Fatal("names with spaces must be rejected")
	}
	if err := Install(context.Background(), r, "Debian", "bad name", 0); err == nil {
		t.Fatal("local names with spaces must be rejected")
	}
}

func TestShutdown(t *testing.T) {
	fr := &fakeRunner{out: map[string]result{"--shutdown": {}}}
	if err := Shutdown(context.Background(), fr); err != nil {
		t.Fatal(err)
	}
	if len(fr.calls) != 1 || strings.Join(fr.calls[0], " ") != "--shutdown" {
		t.Fatalf("calls = %v", fr.calls)
	}
}
