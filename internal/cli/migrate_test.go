package cli

import (
	"strings"
	"testing"

	"github.com/iridance/wsld/internal/config"
)

const (
	mPre     = "-d Debian1 -u bob -- "
	mSave    = mPre + "bash -c set -o pipefail; docker --context default save 'nginx:1.27' | docker --context wsld load"
	mVolPG   = mPre + "bash -c set -o pipefail; docker --context default run --rm -v 'pgdata':/from alpine tar -C /from -cf - . | docker --context wsld run --rm -i -v 'pgdata':/to alpine tar -C /to -xf -"
	mInfo    = mPre + "docker --context default info"
	mImages  = mPre + "docker --context default image ls --format {{.Repository}}:{{.Tag}}"
	mVolumes = mPre + "docker --context default volume ls -q"
)

func migrateHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, true)
	c := h.cfg(t)
	c.UpsertMachine(config.Machine{Distro: "Debian1", User: "bob", APIEndpoint: c.APIEndpoint})
	if err := c.SetClient("Debian1"); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(h.app.ConfigPath, c); err != nil {
		t.Fatal(err)
	}
	h.wsl.resp[mInfo] = ""
	h.wsl.resp[mImages] = "nginx:1.27\n"
	h.wsl.resp[mVolumes] = "pgdata\n"
	h.wsl.resp[mSave] = "Loaded image: nginx:1.27\n"
	h.wsl.resp[mVolPG] = ""
	return h
}

func TestMigrateCopiesEveryClientsDataToTheEngine(t *testing.T) {
	h := migrateHarness(t)
	if code := h.run("migrate"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	out := h.out.String()
	if !strings.Contains(out, "Debian1") || !strings.Contains(out, "1 image") || !strings.Contains(out, "1 volume") {
		t.Fatalf("out = %s", out)
	}
	if !strings.Contains(out, "systemctl disable --now docker") {
		t.Fatalf("must hint how to retire the local dockerd: %s", out)
	}
}

func TestMigrateDryRunCopiesNothing(t *testing.T) {
	h := migrateHarness(t)
	if code := h.run("migrate", "--dry-run"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "save") || strings.Contains(c, "tar -C") {
			t.Fatalf("dry run copied: %s", c)
		}
	}
}

func TestMigrateOptionsSelectWhatIsCopied(t *testing.T) {
	h := migrateHarness(t)
	if code := h.run("migrate", "--volumes=false"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	for _, c := range h.wsl.calls {
		if strings.Contains(c, "volume ls") || strings.Contains(c, "tar -C") {
			t.Fatalf("volumes were disabled: %s", c)
		}
	}
}

func TestMigrateRejectsTheEngineAndUnknownDistros(t *testing.T) {
	h := migrateHarness(t)
	if code := h.run("migrate", "--distro", "Ubuntu-26.04"); code != ExitError {
		t.Fatalf("engine as source: code=%d", code)
	}
	h = migrateHarness(t)
	if code := h.run("migrate", "--distro", "Ghost"); code != ExitError {
		t.Fatalf("unknown distro: code=%d", code)
	}
}

func TestMigrateWithoutClientsSaysSo(t *testing.T) {
	h := newHarness(t, true)
	if code := h.run("migrate"); code != 0 {
		t.Fatalf("code=%d err=%s", code, h.errb)
	}
	if !strings.Contains(h.out.String(), "no client") {
		t.Fatalf("out = %s", h.out)
	}
}
