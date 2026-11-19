package lan

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestFirewallScriptEnable(t *testing.T) {
	s := FirewallScript(true)
	for _, want := range []string{
		"Remove-NetFirewallHyperVRule -Name 'wsld-lan-tcp' -ErrorAction SilentlyContinue",
		"New-NetFirewallHyperVRule -Name 'wsld-lan-tcp'",
		"New-NetFirewallHyperVRule -Name 'wsld-lan-udp'",
		"-VMCreatorId '" + WSLVMCreatorID + "'",
		"-Protocol TCP", "-Protocol UDP",
		"-Direction Inbound", "-Action Allow", "-Profiles Private",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in\n%s", want, s)
		}
	}
	if strings.Contains(s, "Public") || strings.Contains(s, "Any") {
		t.Fatal("rules must be scoped to the Private profile only")
	}
}

func TestFirewallScriptDisable(t *testing.T) {
	s := FirewallScript(false)
	if strings.Contains(s, "New-NetFirewallHyperVRule") {
		t.Fatal("disable must not create rules")
	}
	if strings.Count(s, "Remove-NetFirewallHyperVRule") != 2 {
		t.Fatalf("expected two removals:\n%s", s)
	}
}

func TestElevateScriptUsesRunAs(t *testing.T) {
	s := ElevateScript("Write-Output hi")
	for _, want := range []string{"-Verb RunAs", "-Wait", "-PassThru", "exit $p.ExitCode", EncodeCommand("Write-Output hi")} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q", want)
		}
	}
}

func TestEncodeCommandUTF16LE(t *testing.T) {
	b, err := base64.StdEncoding.DecodeString(EncodeCommand("ab€"))
	if err != nil {
		t.Fatal(err)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	if string(utf16.Decode(u)) != "ab€" {
		t.Fatalf("round trip failed: %v", u)
	}
}

func TestParseProbe(t *testing.T) {
	cases := []struct {
		in       string
		rules    int
		profiles []string
	}{
		{`{"rules":2,"profiles":["Private","Public"]}`, 2, []string{"Private", "Public"}},
		{`{"profiles":"Private","rules":0}` + "\r\n", 0, []string{"Private"}},
		{`{"rules":-1,"profiles":[]}`, -1, []string{}},
		{`{"rules":1,"profiles":null}`, 1, []string{}},
	}
	for _, c := range cases {
		p, err := ParseProbe(c.in)
		if err != nil || p.Rules != c.rules || strings.Join(p.Profiles, ",") != strings.Join(c.profiles, ",") {
			t.Fatalf("%s → %+v, %v", c.in, p, err)
		}
	}
	if p, err := ParseProbe("garbage"); err == nil || p.Rules != -1 {
		t.Fatal("garbage must fail with rules=-1")
	}
}

func TestProbeScriptNamesRules(t *testing.T) {
	s := ProbeScript()
	if !strings.Contains(s, "'wsld-lan-tcp','wsld-lan-udp'") || !strings.Contains(s, "Get-NetConnectionProfile") {
		t.Fatal(s)
	}
}
