package lan

import "testing"

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
