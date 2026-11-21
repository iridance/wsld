package gui

import (
	"context"
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/wsl"
)

func fakeState(t *testing.T, f *FakeWSL, name string) string {
	t.Helper()
	list, err := wsl.ListDistros(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := wsl.FindDistro(list, name)
	if !ok {
		t.Fatalf("no distro %s", name)
	}
	return d.State
}

// 實機上任何 wsl -d <name> 命令都會把停止中的 distro 開起來；FakeWSL 必須照做，測試才抓得到誤喚醒。
func TestFakeWSLAnyDistroCommandWakes(t *testing.T) {
	for _, cmd := range [][]string{
		{"cat", "/etc/wsl.conf"},
		{"cat", "/proc/stat"},
		{"df", "-B1", "--output=used,size", "/"},
		{"systemctl", "stop", "docker"},
		{"true"},
	} {
		f := NewFakeWSL()
		if st := fakeState(t, f, "docker-desktop"); st != "Stopped" {
			t.Fatalf("initial state = %s", st)
		}
		_, _, _ = wsl.RunIn(context.Background(), f, "docker-desktop", "root", nil, cmd...)
		if st := fakeState(t, f, "docker-desktop"); st != "Running" {
			t.Fatalf("%v: state = %s, want Running", cmd, st)
		}
		if !f.Attached("docker-desktop") {
			t.Fatalf("%v: VHD should be attached after waking", cmd)
		}
	}
}

func TestFakeWSLWakeExempt(t *testing.T) {
	f := NewFakeWSL()
	f.WakeExempt = func(args []string) bool {
		return strings.Join(args, " ") == "-d docker-desktop -u root -- cat /etc/wsl.conf"
	}
	_, _, _ = wsl.RunIn(context.Background(), f, "docker-desktop", "root", nil, "cat", "/etc/wsl.conf")
	if st := fakeState(t, f, "docker-desktop"); st != "Stopped" {
		t.Fatalf("exempt command woke distro: %s", st)
	}
	_, _, _ = wsl.RunIn(context.Background(), f, "docker-desktop", "root", nil, "cat", "/proc/stat")
	if st := fakeState(t, f, "docker-desktop"); st != "Running" {
		t.Fatalf("non-exempt command should wake: %s", st)
	}
}

// 不存在的 distro 不會被掛上或改狀態。
func TestFakeWSLUnknownDistroNotWoken(t *testing.T) {
	f := NewFakeWSL()
	if _, _, err := wsl.RunIn(context.Background(), f, "nope", "root", nil, "true"); err == nil {
		t.Fatal("unknown distro should fail")
	}
	if f.Attached("nope") {
		t.Fatal("unknown distro attached")
	}
}
