package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultValues(t *testing.T) {
	c := Default()
	if c.APIEndpoint != "tcp://127.0.0.1:23750" {
		t.Fatalf("APIEndpoint = %q", c.APIEndpoint)
	}
	if c.RefreshIntervalSec != 3 || c.APITimeoutSec != 10 || c.TrayAutostart {
		t.Fatalf("unexpected defaults: %+v", c)
	}
}

func TestLoadAppliesDefaultsForMissingOptionalFields(t *testing.T) {
	p := writeTemp(t, `
distro = "Ubuntu-26.04"
user = "irice"
api_endpoint = "tcp://127.0.0.1:23751"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Distro != "Ubuntu-26.04" || c.User != "irice" || c.APIEndpoint != "tcp://127.0.0.1:23751" {
		t.Fatalf("parsed = %+v", c)
	}
	if c.RefreshIntervalSec != 3 || c.APITimeoutSec != 10 || c.ExecShell != "" {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestLoadOptionalFields(t *testing.T) {
	p := writeTemp(t, `
distro = "Ubuntu"
user = "bob"
api_endpoint = "tcp://127.0.0.1:23750"
exec_shell = "zsh"
refresh_interval_sec = 5
tray_autostart = true
api_timeout_sec = 20
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExecShell != "zsh" || c.RefreshIntervalSec != 5 || !c.TrayAutostart || c.APITimeoutSec != 20 {
		t.Fatalf("parsed = %+v", c)
	}
}

func TestLoadMissingFileReturnsErrNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if !IsNotFound(err) {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestLoadRejectsNonLoopbackEndpoint(t *testing.T) {
	p := writeTemp(t, `
distro = "Ubuntu"
user = "bob"
api_endpoint = "tcp://0.0.0.0:2375"
`)
	_, err := Load(p)
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("want loopback validation error, got %v", err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		ok   bool
	}{
		{"valid", func(c *Config) {}, true},
		{"empty distro", func(c *Config) { c.Distro = "" }, false},
		{"empty user", func(c *Config) { c.User = "" }, false},
		{"unix endpoint rejected", func(c *Config) { c.APIEndpoint = "unix:///var/run/docker.sock" }, false},
		{"localhost ok", func(c *Config) { c.APIEndpoint = "tcp://localhost:23750" }, true},
		{"ipv6 loopback ok", func(c *Config) { c.APIEndpoint = "tcp://[::1]:23750" }, true},
		{"no port", func(c *Config) { c.APIEndpoint = "tcp://127.0.0.1" }, false},
		{"refresh zero", func(c *Config) { c.RefreshIntervalSec = 0 }, false},
		{"timeout zero", func(c *Config) { c.APITimeoutSec = 0 }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Distro, c.User = "Ubuntu", "bob"
			tc.mut(&c)
			err := c.Validate()
			if (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestSaveRoundTripAndCreatesDir(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	c := Default()
	c.Distro, c.User = "Ubuntu-26.04", "irice"
	c.APIEndpoint = "tcp://127.0.0.1:23752"
	c.ExecShell = "fish"
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got != c {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, c)
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	c := Default() // 缺 distro/user
	if err := Save(filepath.Join(t.TempDir(), "c.toml"), c); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestAPIPortAndHTTPBase(t *testing.T) {
	c := Default()
	c.APIEndpoint = "tcp://127.0.0.1:23751"
	port, err := c.APIPort()
	if err != nil || port != 23751 {
		t.Fatalf("APIPort = %d, %v", port, err)
	}
	if got := c.HTTPBaseURL(); got != "http://127.0.0.1:23751" {
		t.Fatalf("HTTPBaseURL = %q", got)
	}
}

func TestEndpointForPort(t *testing.T) {
	if got := EndpointForPort(23755); got != "tcp://127.0.0.1:23755" {
		t.Fatalf("got %q", got)
	}
}

func TestDefaultPathUsesAPPDATAOnWindowsLayout(t *testing.T) {
	t.Setenv("APPDATA", `C:\Users\irice\AppData\Roaming`)
	got := DefaultPathFor("windows", func(k string) string { return os.Getenv(k) })
	want := filepath.Join(`C:\Users\irice\AppData\Roaming`, "wsl-docker", "config.toml")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDefaultPathFallsBackToXDGOnOtherOS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got := DefaultPathFor("linux", func(k string) string { return os.Getenv(k) })
	if got != filepath.Join("/tmp/xdg", "wsl-docker", "config.toml") {
		t.Fatalf("got %q", got)
	}
}

func TestDefaultPathHonoursOverrideEnv(t *testing.T) {
	t.Setenv("WSLD_CONFIG", "/x/y.toml")
	if got := DefaultPathFor("windows", os.Getenv); got != "/x/y.toml" {
		t.Fatalf("got %q", got)
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
