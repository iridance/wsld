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
	if c.ThemeOrDefault() != "system" {
		t.Fatalf("ThemeOrDefault = %q", c.ThemeOrDefault())
	}
}

func TestLoadMigratesFlatConfigToMachines(t *testing.T) {
	p := writeTemp(t, `
distro = "Ubuntu-26.04"
user = "irice"
api_endpoint = "tcp://127.0.0.1:23751"
lang = "zh-TW"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Active != "Ubuntu-26.04" {
		t.Fatalf("Active = %q", c.Active)
	}
	if len(c.Machines) != 1 {
		t.Fatalf("Machines = %+v", c.Machines)
	}
	m := c.Machines[0]
	if m.Distro != "Ubuntu-26.04" || m.User != "irice" || m.APIEndpoint != "tcp://127.0.0.1:23751" {
		t.Fatalf("machine = %+v", m)
	}
	if c.Distro != m.Distro || c.User != m.User || c.APIEndpoint != m.APIEndpoint {
		t.Fatalf("derived not synced: %+v", c)
	}
	if !m.Ready() {
		t.Fatal("migrated machine should be Ready")
	}
}

func TestLoadMultiMachineConfig(t *testing.T) {
	p := writeTemp(t, `
active = "Ubuntu-22.04"
theme = "dark"
refresh_interval_sec = 5

[[machines]]
distro = "Ubuntu-26.04"
user = "irice"
api_endpoint = "tcp://127.0.0.1:23750"

[[machines]]
distro = "Ubuntu-22.04"
user = "bob"
api_endpoint = "tcp://127.0.0.1:23751"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Active != "Ubuntu-22.04" || c.Theme != "dark" || c.RefreshIntervalSec != 5 {
		t.Fatalf("got %+v", c)
	}
	if c.Distro != "Ubuntu-22.04" || c.User != "bob" || c.APIEndpoint != "tcp://127.0.0.1:23751" {
		t.Fatalf("derived = distro=%q user=%q ep=%q", c.Distro, c.User, c.APIEndpoint)
	}
	if len(c.Machines) != 2 {
		t.Fatalf("Machines len = %d", len(c.Machines))
	}
}

func TestPlaceholderMachineAllowed(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "Ubuntu-Extra"})
	if err := c.SetActive("Ubuntu-Extra"); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("placeholder should validate: %v", err)
	}
	m, ok := c.ActiveMachine()
	if !ok || m.Ready() {
		t.Fatalf("want unready placeholder, got ok=%v m=%+v", ok, m)
	}
}

func TestSetActiveAndSaveWritesNewFormatOnly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u1", APIEndpoint: "tcp://127.0.0.1:23750"})
	c.UpsertMachine(Machine{Distro: "B", User: "u2", APIEndpoint: "tcp://127.0.0.1:23751"})
	if err := c.SetActive("B"); err != nil {
		t.Fatal(err)
	}
	c.Theme = "light"
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, "active") || !strings.Contains(body, "[[machines]]") {
		t.Fatalf("want new format, got:\n%s", body)
	}
	head := body
	if i := strings.Index(body, "[[machines]]"); i >= 0 {
		head = body[:i]
	}
	for _, key := range []string{"distro =", "user =", "api_endpoint ="} {
		if strings.Contains(head, key) {
			t.Fatalf("flat key %q leaked before machines:\n%s", key, body)
		}
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active != "B" || got.Distro != "B" || got.Theme != "light" {
		t.Fatalf("reload = %+v", got)
	}
}

func TestUpsertMachineUpdatesExisting(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "Ubuntu", User: "", APIEndpoint: ""})
	c.UpsertMachine(Machine{Distro: "Ubuntu", User: "irice", APIEndpoint: "tcp://127.0.0.1:23750"})
	_ = c.SetActive("Ubuntu")
	if len(c.Machines) != 1 || !c.Machines[0].Ready() {
		t.Fatalf("Machines = %+v", c.Machines)
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
lang = "zh-TW"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.ExecShell != "zsh" || c.RefreshIntervalSec != 5 || !c.TrayAutostart || c.APITimeoutSec != 20 || c.Lang != "zh-TW" {
		t.Fatalf("parsed = %+v", c)
	}
}

func TestLangDefaultsToEnglish(t *testing.T) {
	if Default().Lang != "" || Default().LangOrDefault() != "en" {
		t.Fatalf("default lang should be empty→en, got %q", Default().Lang)
	}
	c := Config{Lang: "zh-TW"}
	if c.LangOrDefault() != "zh-TW" {
		t.Fatalf("LangOrDefault = %q", c.LangOrDefault())
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
		{"empty active", func(c *Config) { c.Active = "" }, false},
		{"active missing from machines", func(c *Config) { c.Active = "Nope" }, false},
		{"empty machine distro", func(c *Config) { c.Machines[0].Distro = ""; c.Active = "" }, false},
		{"unix endpoint rejected", func(c *Config) { c.Machines[0].APIEndpoint = "unix:///var/run/docker.sock"; c.syncDerived() }, false},
		{"localhost ok", func(c *Config) { c.Machines[0].APIEndpoint = "tcp://localhost:23750"; c.syncDerived() }, true},
		{"ipv6 loopback ok", func(c *Config) { c.Machines[0].APIEndpoint = "tcp://[::1]:23750"; c.syncDerived() }, true},
		{"no port", func(c *Config) { c.Machines[0].APIEndpoint = "tcp://127.0.0.1"; c.syncDerived() }, false},
		{"refresh zero", func(c *Config) { c.RefreshIntervalSec = 0 }, false},
		{"timeout zero", func(c *Config) { c.APITimeoutSec = 0 }, false},
		{"bad theme", func(c *Config) { c.Theme = "neon" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.UpsertMachine(Machine{Distro: "Ubuntu", User: "bob", APIEndpoint: EndpointForPort(DefaultPort)})
			_ = c.SetActive("Ubuntu")
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
	c.UpsertMachine(Machine{Distro: "Ubuntu-26.04", User: "irice", APIEndpoint: "tcp://127.0.0.1:23752"})
	_ = c.SetActive("Ubuntu-26.04")
	c.ExecShell = "fish"
	c.Theme = "dark"
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Active != c.Active || got.ExecShell != c.ExecShell || got.Theme != c.Theme {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, c)
	}
	if got.Distro != "Ubuntu-26.04" || got.User != "irice" || got.APIEndpoint != "tcp://127.0.0.1:23752" {
		t.Fatalf("derived = %+v", got)
	}
	if len(got.Machines) != 1 {
		t.Fatalf("Machines = %+v", got.Machines)
	}
}

func TestSaveRefusesInvalid(t *testing.T) {
	c := Default()
	c.Theme = "neon"
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
	want := filepath.Join(`C:\Users\irice\AppData\Roaming`, "wsld", "config.toml")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestDefaultPathFallsBackToXDGOnOtherOS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	got := DefaultPathFor("linux", func(k string) string { return os.Getenv(k) })
	if got != filepath.Join("/tmp/xdg", "wsld", "config.toml") {
		t.Fatalf("got %q", got)
	}
}

func TestDefaultPathHonoursOverrideEnv(t *testing.T) {
	t.Setenv("WSLD_CONFIG", "/x/y.toml")
	if got := DefaultPathFor("windows", os.Getenv); got != "/x/y.toml" {
		t.Fatalf("got %q", got)
	}
}

func TestMigrateLegacyAppDirRenamesOldDir(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, legacyAppDirName)
	newDir := filepath.Join(root, appDirName)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, fileName), []byte("active = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return root
		}
		return ""
	}
	if err := MigrateLegacyAppDirFor("linux", getenv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("legacy dir still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, fileName)); err != nil {
		t.Fatalf("migrated config missing: %v", err)
	}
}

func TestMigrateLegacyAppDirSkipsWhenNewExists(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, legacyAppDirName)
	newDir := filepath.Join(root, appDirName)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "old.toml"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, fileName), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return root
		}
		return ""
	}
	if err := MigrateLegacyAppDirFor("linux", getenv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("legacy dir should remain when new exists: %v", err)
	}
}

func TestMigrateLegacyAppDirSkipsOverrideEnv(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, legacyAppDirName)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	getenv := func(k string) string {
		switch k {
		case EnvOverride:
			return filepath.Join(root, "custom.toml")
		case "XDG_CONFIG_HOME":
			return root
		}
		return ""
	}
	if err := MigrateLegacyAppDirFor("linux", getenv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldDir); err != nil {
		t.Fatalf("should not migrate when override set: %v", err)
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
