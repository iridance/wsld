package lan

import (
	"strings"
	"testing"
)

func TestEvaluateFirewall(t *testing.T) {
	const exe = `C:\Users\me\wsld\wsld-gui.exe`
	allow := func(app string, proto, profiles int) FwRule {
		return FwRule{App: app, Inbound: true, Allow: true, Enabled: true, Protocol: proto, Profiles: profiles}
	}
	both := FwProfilePrivate | FwProfilePublic
	cases := []struct {
		name  string
		rules []FwRule
		want  FirewallState
	}{
		{"no rules", nil, FirewallMissing},
		{"our rule", []FwRule{allow(exe, FwProtoTCP, both)}, FirewallAllowed},
		{"all profiles", []FwRule{allow(exe, FwProtoAny, 0x7FFFFFFF)}, FirewallAllowed},
		{"prompt public only (TCP+UDP)", []FwRule{allow(exe, FwProtoTCP, FwProfilePublic), allow(exe, 17, FwProfilePublic)}, FirewallMissing},
		{"union of two rules", []FwRule{allow(exe, FwProtoTCP, FwProfilePublic), allow(exe, FwProtoTCP, FwProfilePrivate)}, FirewallAllowed},
		{"UDP only", []FwRule{allow(exe, 17, both)}, FirewallMissing},
		{"other exe", []FwRule{allow(`D:\downloads\wsld-gui (3).exe`, FwProtoTCP, both)}, FirewallMissing},
		{"case and slashes differ", []FwRule{allow(`c:/users/ME/wsld/WSLD-GUI.EXE`, FwProtoTCP, both)}, FirewallAllowed},
		{"disabled", []FwRule{{App: exe, Inbound: true, Allow: true, Protocol: FwProtoTCP, Profiles: both}}, FirewallMissing},
		{"outbound", []FwRule{{App: exe, Allow: true, Enabled: true, Protocol: FwProtoTCP, Profiles: both}}, FirewallMissing},
		{"block wins", []FwRule{allow(exe, FwProtoTCP, both), {App: exe, Inbound: true, Enabled: true, Protocol: FwProtoTCP, Profiles: FwProfilePublic}}, FirewallBlocked},
		{"block on domain only is ignored", []FwRule{allow(exe, FwProtoTCP, both), {App: exe, Inbound: true, Enabled: true, Protocol: FwProtoTCP, Profiles: 1}}, FirewallAllowed},
		{"disabled block is ignored", []FwRule{allow(exe, FwProtoTCP, both), {App: exe, Inbound: true, Protocol: FwProtoTCP, Profiles: both}}, FirewallAllowed},
	}
	for _, c := range cases {
		if got := EvaluateFirewall(c.rules, exe); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestSamePath(t *testing.T) {
	t.Setenv("WSLD_TEST_HOME", `C:\Users\me`)
	cases := []struct {
		a, b string
		want bool
	}{
		{`C:\a\b.exe`, `c:\A\B.EXE`, true},
		{`C:\a\.\x\..\b.exe`, `C:\a\b.exe`, true},
		{`%WSLD_TEST_HOME%\wsld\wsld-gui.exe`, `C:\Users\me\wsld\wsld-gui.exe`, true},
		{`\\wsl.localhost\Ubuntu\home\me\dist\wsld-gui.exe`, `\\WSL.LOCALHOST\Ubuntu\home\me\dist\wsld-gui.exe`, true},
		{`\\wsl.localhost\Ubuntu\a.exe`, `\wsl.localhost\Ubuntu\a.exe`, false},
		{`C:\a\b.exe`, `C:\a\c.exe`, false},
		{"", "", false},
	}
	for _, c := range cases {
		if got := SamePath(c.a, c.b); got != c.want {
			t.Errorf("SamePath(%q, %q) = %v want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestPlanFirewall(t *testing.T) {
	const exe = `C:\Users\me\wsld\wsld-gui.exe`
	both := FwProfilePrivate | FwProfilePublic
	cases := []struct {
		name  string
		exe   string
		rules []FwRule
		want  FirewallMethod
	}{
		{"no rules", exe, nil, MethodNetsh},
		{"windows prompt allowed public only", exe, []FwRule{{Name: "wsld-gui", App: exe, Inbound: true, Allow: true, Enabled: true, Protocol: FwProtoTCP, Profiles: FwProfilePublic}}, MethodNetsh},
		{"enabled block rule", exe, []FwRule{{Name: "wsld-gui", App: exe, Inbound: true, Enabled: true, Protocol: FwProtoTCP, Profiles: both}}, MethodHelper},
		{"disabled block rule", exe, []FwRule{{Name: "wsld-gui", App: exe, Inbound: true, Protocol: FwProtoTCP, Profiles: both}}, MethodHelper},
		{"outbound block is ignored", exe, []FwRule{{Name: "x", App: exe, Enabled: true, Protocol: FwProtoTCP, Profiles: both}}, MethodNetsh},
		{"own rule exists (disabled)", exe, []FwRule{{Name: AppRuleName, App: exe, Inbound: true, Allow: true, Protocol: FwProtoTCP, Profiles: both}}, MethodHelper},
		{"other exe's block rule", exe, []FwRule{{Name: "x", App: `D:\other.exe`, Inbound: true, Enabled: true, Protocol: FwProtoTCP, Profiles: both}}, MethodNetsh},
		{"UNC dev build", `\\wsl.localhost\Ubuntu\home\me\dist\wsld-gui.exe`, nil, MethodHelper},
	}
	for _, c := range cases {
		if got := PlanFirewall(c.rules, c.exe); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}

func TestNetshAddRuleArgs(t *testing.T) {
	got := NetshAddRuleArgs(`C:\Program Files\wsld\wsld-gui.exe`)
	for _, want := range []string{
		`advfirewall firewall add rule `,
		`name="wsld-gui (LAN access)"`,
		`dir=in`,
		`action=allow`,
		`program="C:\Program Files\wsld\wsld-gui.exe"`,
		`protocol=TCP`,
		`profile=private,public`,
		`remoteip=localsubnet`,
		`enable=yes`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.HasPrefix(got, "netsh") {
		t.Errorf("args must not include the program: %s", got)
	}
}

func TestNetshPath(t *testing.T) {
	t.Setenv("SystemRoot", `D:\WINDOWS`)
	if got := NetshPath(); got != `D:\WINDOWS\System32\netsh.exe` {
		t.Fatalf("got %s", got)
	}
	t.Setenv("SystemRoot", "")
	if got := NetshPath(); got != `C:\Windows\System32\netsh.exe` {
		t.Fatalf("got %s", got)
	}
}
