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

func TestLoadMigratesFlatConfigToClientPlusDedicatedEngine(t *testing.T) {
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
	if c.Lang != "zh-TW" {
		t.Fatalf("lang = %q", c.Lang)
	}
	m, ok := c.FindMachine("Ubuntu-26.04")
	if !ok || m.User != "irice" || m.APIEndpoint != "tcp://127.0.0.1:23751" || m.Role != RoleClient || !m.Ready() {
		t.Fatalf("legacy machine must survive as a ready client: %+v %v", m, ok)
	}
	e, ok := c.EngineMachine()
	if !ok || e.Distro != DefaultEngineDistro || e.Ready() {
		t.Fatalf("a not-yet-created dedicated engine placeholder is expected, got %+v %v", e, ok)
	}
	if c.Active != DefaultEngineDistro || c.Distro != DefaultEngineDistro {
		t.Fatalf("Active = %q Distro = %q", c.Active, c.Distro)
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
role = "engine"
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
	_ = c.SetEngine("A")
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
active = "Ubuntu-26.04"
[[machines]]
distro = "Ubuntu-26.04"
user = "irice"
api_endpoint = "tcp://127.0.0.1:23751"
role = "engine"
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
	_ = c.SetEngine("Ubuntu-26.04")
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

func TestBusyAndNextAPIPortSkipsOtherMachines(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: EndpointForPort(DefaultPort)})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: EndpointForPort(DefaultPort + 1)})
	busy := c.BusyAPIPorts("C")
	if len(busy) != 2 || busy[0] != DefaultPort || busy[1] != DefaultPort+1 {
		t.Fatalf("BusyAPIPorts = %v", busy)
	}
	if got := c.NextAPIPort(""); got != DefaultPort+2 {
		t.Fatalf("NextAPIPort = %d", got)
	}
	// 重 init B：略過自己，下一個可從 23751 起（仍被 A 佔 23750）
	if got := c.NextAPIPort("B"); got != DefaultPort+1 {
		t.Fatalf("NextAPIPort skip B = %d", got)
	}
	if len(c.BusyAPIPorts("B")) != 1 || c.BusyAPIPorts("B")[0] != DefaultPort {
		t.Fatalf("Busy skip B = %v", c.BusyAPIPorts("B"))
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

func TestLANAccessRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.Distro, c.User = "Ubuntu-26.04", "irice"
	c.LANAccess = true
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "lan_access = true") {
		t.Fatalf("saved:\n%s", b)
	}
	got, err := Load(p)
	if err != nil || !got.LANAccess {
		t.Fatalf("got %+v, %v", got, err)
	}
	got.LANAccess = false
	_ = Save(p, got)
	b, _ = os.ReadFile(p)
	if strings.Contains(string(b), "lan_access") {
		t.Fatalf("false must be omitted:\n%s", b)
	}
}

func TestK8sRoundTripAndOmitWhenOff(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750", K8s: true})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23751"})
	_ = c.SetEngine("A")
	c.EnsureActive(c.Machines[0])
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Count(string(raw), "k8s = true") != 1 {
		t.Fatalf("expected exactly one k8s = true:\n%s", raw)
	}
	if strings.Contains(string(raw), "k8s = false") {
		t.Fatalf("k8s=false must be omitted:\n%s", raw)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got.K8sMachine()
	if !ok || m.Distro != "A" {
		t.Fatalf("K8sMachine = %+v, %v", m, ok)
	}
}

func TestValidateRejectsTwoK8sMachines(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750", K8s: true})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23751", K8s: true})
	c.EnsureActive(c.Machines[0])
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "k8s") {
		t.Fatalf("want k8s conflict error, got %v", err)
	}
}

func TestK8sMachineNoneEnabled(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	if _, ok := c.K8sMachine(); ok {
		t.Fatal("no machine has k8s enabled")
	}
}

func TestSetK8sMovesNothingAndRejectsSecond(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750", K8s: true})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23751"})
	c.EnsureActive(c.Machines[0])
	if err := c.SetK8s("B", true); err == nil || !strings.Contains(err.Error(), "A") {
		t.Fatalf("expected conflict naming A, got %v", err)
	}
	if err := c.SetK8s("A", false); err != nil {
		t.Fatal(err)
	}
	if err := c.SetK8s("b", true); err != nil {
		t.Fatalf("case-insensitive enable after A off: %v", err)
	}
	if m, _ := c.K8sMachine(); m.Distro != "B" {
		t.Fatalf("K8sMachine = %+v", m)
	}
	if err := c.SetK8s("nope", true); err == nil {
		t.Fatal("unknown machine must error")
	}
}

// 實機：wsld start → Init 以「新的」Machine（K8s=false）呼叫 UpsertMachine，覆寫掉既有 profile，
// 使 k8s = true 被悄悄清掉、k3s 不再恢復。K8s 旗標只能經 SetK8s 改變。
func TestUpsertMachinePreservesK8sFlag(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	if err := c.SetK8s("A", true); err != nil {
		t.Fatal(err)
	}
	c.UpsertMachine(Machine{Distro: "a", User: "u2", APIEndpoint: "tcp://127.0.0.1:23760"}) // Init 重寫 profile
	m, ok := c.FindMachine("A")
	if !ok || !m.K8s || m.User != "u2" || m.APIEndpoint != "tcp://127.0.0.1:23760" {
		t.Fatalf("profile fields must update but k8s must survive: %+v", m)
	}
	if got, ok := c.K8sMachine(); !ok || !strings.EqualFold(got.Distro, "A") {
		t.Fatalf("K8sMachine = %+v %v", got, ok)
	}
}

func TestEnsureActivePreservesK8sFlag(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	_ = c.SetK8s("A", true)
	c.EnsureActive(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23751"})
	if m, _ := c.FindMachine("A"); !m.K8s {
		t.Fatal("EnsureActive (used by Init) dropped the k8s flag")
	}
}

func TestK8sAPIPortRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.K8sAPIPort = 30443
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.K8sAPIPort != 30443 {
		t.Fatalf("load = %d, %v", got.K8sAPIPort, err)
	}
	c.K8sAPIPort = 80
	if err := c.Validate(); err == nil {
		t.Fatal("port 80 should be rejected")
	}
}

func TestEngineMachineNoneByDefault(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	if _, ok := c.EngineMachine(); ok {
		t.Fatal("no engine by default")
	}
	if m, _ := c.FindMachine("A"); m.IsEngine() {
		t.Fatal("default role is not engine")
	}
}

func TestSetEngineAllowsOnlyOne(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "wsld-engine", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23751"})
	c.EnsureActive(c.Machines[0])
	if err := c.SetEngine("wsld-engine"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetEngine("B"); err == nil || !strings.Contains(err.Error(), "wsld-engine") {
		t.Fatalf("second engine must be rejected naming the first, got %v", err)
	}
	if m, ok := c.EngineMachine(); !ok || m.Distro != "wsld-engine" || m.Role != RoleEngine {
		t.Fatalf("EngineMachine = %+v %v", m, ok)
	}
	if err := c.SetEngine("nope"); err == nil {
		t.Fatal("unknown machine must error")
	}
	if err := c.ClearEngine(); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.EngineMachine(); ok {
		t.Fatal("engine cleared")
	}
}

func TestValidateRejectsTwoEngines(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750", Role: RoleEngine})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23751", Role: RoleEngine})
	c.EnsureActive(c.Machines[0])
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "engine") {
		t.Fatalf("want engine conflict, got %v", err)
	}
}

func TestValidateRejectsUnknownRole(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750", Role: "bogus"})
	c.EnsureActive(c.Machines[0])
	if err := c.Validate(); err == nil {
		t.Fatal("unknown role must be rejected")
	}
}

func TestValidateK8sMustBeOnEngineWhenEngineExists(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "E", User: "u", APIEndpoint: "tcp://127.0.0.1:23750", Role: RoleEngine})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23751", Role: RoleClient, K8s: true})
	c.EnsureActive(c.Machines[0])
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "k8s") {
		t.Fatalf("k8s on a client must be rejected, got %v", err)
	}
}

func TestUpsertMachinePreservesRole(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "E", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	if err := c.SetEngine("E"); err != nil {
		t.Fatal(err)
	}
	c.UpsertMachine(Machine{Distro: "e", User: "u2", APIEndpoint: "tcp://127.0.0.1:23760"}) // Init 重寫 profile
	if m, _ := c.FindMachine("E"); m.Role != RoleEngine || m.User != "u2" {
		t.Fatalf("role must survive upsert: %+v", m)
	}
}

func TestRoleRoundTripAndOmitWhenEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	c := Default()
	c.UpsertMachine(Machine{Distro: "E", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23751"})
	c.EnsureActive(c.Machines[0])
	if err := c.SetEngine("E"); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Count(string(raw), "role") != 1 {
		t.Fatalf("role written only for engine:\n%s", raw)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := got.EngineMachine(); !ok || m.Distro != "E" {
		t.Fatalf("round trip lost engine: %+v", got.Machines)
	}
}

func TestSetK8sMustBeOnEngineWhenEngineExists(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "E", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	c.UpsertMachine(Machine{Distro: "B", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	_ = c.SetEngine("E")
	_ = c.SetClient("B")
	c.EnsureActive(c.Machines[0])
	if err := c.SetK8s("B", true); err == nil || !strings.Contains(err.Error(), "E") {
		t.Fatalf("k8s on a client must be rejected naming the engine, got %v", err)
	}
	if err := c.SetK8s("E", true); err != nil {
		t.Fatalf("k8s on the engine must work: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestSetEngineRefusedWhileK8sRunsElsewhere(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	c.UpsertMachine(Machine{Distro: "E", User: "u", APIEndpoint: "tcp://127.0.0.1:23751"})
	_ = c.SetK8s("A", true)
	c.EnsureActive(c.Machines[0])
	if err := c.SetEngine("E"); err == nil || !strings.Contains(err.Error(), "k8s") {
		t.Fatalf("want k8s conflict, got %v", err)
	}
	if _, ok := c.EngineMachine(); ok {
		t.Fatal("engine must not be set on failure")
	}
	if err := c.SetEngine("A"); err != nil {
		t.Fatalf("promoting the k8s machine itself is fine: %v", err)
	}
}

func TestK8sTargetPrefersEngineOverActive(t *testing.T) {
	c := Default()
	c.UpsertMachine(Machine{Distro: "A", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	c.UpsertMachine(Machine{Distro: "E", User: "u", APIEndpoint: "tcp://127.0.0.1:23750"})
	c.EnsureActive(c.Machines[0])
	if m, ok := c.K8sTarget(); !ok || m.Distro != "A" {
		t.Fatalf("no engine → active, got %+v %v", m, ok)
	}
	_ = c.SetEngine("E")
	if m, ok := c.K8sTarget(); !ok || m.Distro != "E" {
		t.Fatalf("engine → engine, got %+v %v", m, ok)
	}
}

// ---- 舊設定（獨立模式、可切換 active）載入時遷移成 engine + client ----

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadMigratesEveryReadyMachineToAClientAndAddsADedicatedEngine(t *testing.T) {
	p := writeCfg(t, `active = "B"
[[machines]]
distro = "A"
user = "u"
api_endpoint = "tcp://127.0.0.1:23750"
[[machines]]
distro = "B"
user = "u"
api_endpoint = "tcp://127.0.0.1:23751"
[[machines]]
distro = "Placeholder"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := c.EngineMachine()
	if !ok || e.Distro != "wsld-engine" || e.Ready() {
		t.Fatalf("a dedicated, not-yet-created engine is expected, got %+v %v", e, ok)
	}
	for _, n := range []string{"A", "B"} {
		if m, _ := c.FindMachine(n); m.Role != RoleClient {
			t.Fatalf("%s must become a client: %+v", n, m)
		}
	}
	if ph, _ := c.FindMachine("Placeholder"); ph.Role != "" {
		t.Fatalf("never-initialised placeholder keeps no role: %+v", ph)
	}
	if c.Active != "wsld-engine" {
		t.Fatalf("Active = %q", c.Active)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMigrationClearsK8sFlagsBecauseK3sMustLiveOnTheEngine(t *testing.T) {
	p := writeCfg(t, `active = "A"
[[machines]]
distro = "A"
user = "u"
api_endpoint = "tcp://127.0.0.1:23750"
[[machines]]
distro = "K"
user = "u"
api_endpoint = "tcp://127.0.0.1:23751"
k8s = true
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, on := c.K8sMachine(); on {
		t.Fatal("k8s flags must be cleared during migration")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMigrationReusesAnExistingWsldEngineMachine(t *testing.T) {
	p := writeCfg(t, `active = "A"
[[machines]]
distro = "A"
user = "u"
api_endpoint = "tcp://127.0.0.1:23750"
[[machines]]
distro = "wsld-engine"
user = "u"
api_endpoint = "tcp://127.0.0.1:23751"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := c.EngineMachine(); !ok || e.Distro != "wsld-engine" || !e.Ready() {
		t.Fatalf("an existing wsld-engine must be reused: %+v %v", e, ok)
	}
	if n := len(c.Machines); n != 2 {
		t.Fatalf("no duplicate profile expected, got %d", n)
	}
	if a, _ := c.FindMachine("A"); a.Role != RoleClient {
		t.Fatalf("A = %+v", a)
	}
}

func TestLoadMigrationIsIdempotentAndKeepsExistingEngine(t *testing.T) {
	p := writeCfg(t, `active = "A"
[[machines]]
distro = "A"
user = "u"
api_endpoint = "tcp://127.0.0.1:23750"
role = "client"
[[machines]]
distro = "E"
user = "u"
api_endpoint = "tcp://127.0.0.1:23751"
role = "engine"
`)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if e, _ := c.EngineMachine(); e.Distro != "E" {
		t.Fatalf("an existing engine must not be replaced: %q", e.Distro)
	}
	if a, _ := c.FindMachine("A"); a.Role != RoleClient {
		t.Fatalf("A = %+v", a)
	}
}

func TestEmptyConfigStaysEmpty(t *testing.T) {
	c, err := Load(writeCfg(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.EngineMachine(); ok || len(c.Machines) != 0 {
		t.Fatalf("empty config must stay empty: %+v", c)
	}
}
