package cli

import (
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/dockerapi"
)

const (
	eFindmnt = "-d Ubuntu-26.04 -- findmnt -rn -o TARGET"
	eUmount  = "-d Ubuntu-26.04 -u root -- umount /mnt/wsl/distro/Debian1"
)

func exposeHarness(t *testing.T, state string) *harness {
	t.Helper()
	h := newHarness(t, true)
	h.wsl.resp[eFindmnt] = "/\n/mnt/c\n/mnt/wsl/distro/Debian1\n/mnt/wsl/distro/Debian1/proc\n"
	h.wsl.resp[eUmount] = ""
	h.wsl.resp["--list --verbose"] = utf16("  NAME STATE VERSION\r\n* Ubuntu-26.04 Running 2\r\n  Debian1 " + state + " 2\r\n")
	h.api.containers = []dockerapi.Container{{ID: "c1", Names: []string{"/web"}, State: "running"}}
	h.api.mounts = map[string][]dockerapi.ContainerMount{"c1": {{Type: "bind", Source: "/mnt/wsl/distro/Debian1/home/u/app", Destination: "/app"}}}
	return h
}

func (h *harness) umounted() bool {
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "umount") {
			return true
		}
	}
	return false
}

func TestExposeStatusListsMountsAndTheirContainers(t *testing.T) {
	h := exposeHarness(t, "Running")
	if code := h.run("expose", "status"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	out := h.out.String()
	if !strings.Contains(out, "Debian1") || !strings.Contains(out, "web") {
		t.Fatalf("out = %s", out)
	}
}

func TestExposeStatusFlagsAStaleMount(t *testing.T) {
	h := exposeHarness(t, "Stopped")
	h.api.containers = nil
	if code := h.run("expose", "status"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if !strings.Contains(strings.ToLower(h.out.String()), "stale") {
		t.Fatalf("out = %s", h.out)
	}
}

func TestExposeReleaseRefusesWhileContainersRunThenStopsThemWithTheFlag(t *testing.T) {
	h := exposeHarness(t, "Running")
	if code := h.run("expose", "release", "Debian1"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	if !strings.Contains(h.errb.String(), "--stop-containers") || h.umounted() {
		t.Fatalf("must explain the flag and not unmount: err=%q calls=%v", h.errb, h.wsl.calls)
	}
	h.errb.Reset()
	if code := h.run("expose", "release", "Debian1", "--stop-containers"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if len(h.api.stopped) != 1 || h.api.stopped[0] != "c1" || !h.umounted() {
		t.Fatalf("stopped=%v umounted=%v", h.api.stopped, h.umounted())
	}
}

func TestExposeGCReleasesUnusedMounts(t *testing.T) {
	h := exposeHarness(t, "Running")
	h.api.containers = nil
	if code := h.run("expose", "gc"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if !h.umounted() || !strings.Contains(h.out.String(), "Debian1") {
		t.Fatalf("out=%q calls=%v", h.out, h.wsl.calls)
	}
}

func TestUnregisterReleasesAnUnusedExposureFirstAndRefusesAUsedOne(t *testing.T) {
	h := exposeHarness(t, "Running")
	h.wsl.resp["--unregister Debian1"] = ""
	if code := h.run("unregister", "Debian1", "--confirm", "Debian1"); code != ExitError {
		t.Fatalf("used exposure: code=%d err=%s", code, h.errb)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "--unregister") {
			t.Fatalf("must not unregister while containers use it: %s", c)
		}
	}
	h = exposeHarness(t, "Running")
	h.api.containers = nil
	h.wsl.resp["--unregister Debian1"] = ""
	if code := h.run("unregister", "Debian1", "--confirm", "Debian1"); code != 0 {
		t.Fatalf("unused exposure: code=%d err=%s", code, h.errb)
	}
	iu, ir := -1, -1
	for i, c := range h.wsl.calls {
		if strings.Contains(c, "umount") {
			iu = i
		}
		if strings.Contains(c, "--unregister") {
			ir = i
		}
	}
	if iu < 0 || ir < 0 || iu > ir {
		t.Fatalf("release must precede unregister: %v", h.wsl.calls)
	}
}
