package lan

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestRemoveFirewallScript(t *testing.T) {
	s := RemoveFirewallScript()
	if strings.Count(s, "Remove-NetFirewallHyperVRule") != 2 || !strings.Contains(s, "'wsld-lan-tcp'") || !strings.Contains(s, "'wsld-lan-udp'") {
		t.Fatalf("script:\n%s", s)
	}
	if strings.Contains(s, "New-NetFirewall") {
		t.Fatal("must only remove rules")
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

func TestRulesScriptAndParse(t *testing.T) {
	if s := RulesScript(); !strings.Contains(s, "'wsld-lan-tcp','wsld-lan-udp'") || !strings.Contains(s, "catch { -1 }") {
		t.Fatal(s)
	}
	for in, want := range map[string]int{"2\r\n": 2, "0": 0, "-1\n": -1, "": -1, "oops": -1} {
		if got := ParseRules(in); got != want {
			t.Errorf("%q → %d, want %d", in, got, want)
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

func TestNetstatPortInUse(t *testing.T) {
	out := "\r\n使用中連線\r\n\r\n  協定   本機位址               外部位址               狀態           PID\r\n" +
		"  TCP    0.0.0.0:135            0.0.0.0:0              LISTENING       1234\r\n" +
		"  TCP    127.0.0.1:23750        127.0.0.1:64158        TIME_WAIT       0\r\n" +
		"  TCP    127.0.0.1:64158        127.0.0.1:23751        TIME_WAIT       0\r\n" +
		"  TCP    [::1]:23752            [::]:0                 LISTENING       42\r\n"
	cases := map[int]bool{23750: true, 23752: true, 23751: false, 2375: false, 135: true, 64158: true, 8080: false}
	for port, want := range cases {
		if got := NetstatPortInUse(out, port); got != want {
			t.Errorf("port %d: got %v want %v", port, got, want)
		}
	}
}
