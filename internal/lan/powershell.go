package lan

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf16"
)

// FirewallScript 產生（以系統管理員身分執行的）建立／移除 wsld Hyper-V 防火牆規則腳本。
// 啟用時只套用在「私人」網路設定檔，公用 Wi-Fi 不對外。
func FirewallScript(enable bool) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	for _, name := range RuleNames {
		fmt.Fprintf(&b, "Remove-NetFirewallHyperVRule -Name '%s' -ErrorAction SilentlyContinue\n", name)
	}
	if enable {
		for _, name := range RuleNames {
			proto := strings.ToUpper(strings.TrimPrefix(name, "wsld-lan-"))
			fmt.Fprintf(&b, "New-NetFirewallHyperVRule -Name '%s' -DisplayName 'wsld LAN access (%s)' "+
				"-Direction Inbound -VMCreatorId '%s' -Protocol %s -Action Allow -Profiles Private | Out-Null\n",
				name, proto, WSLVMCreatorID, proto)
		}
	}
	return b.String()
}

// ElevateScript 包一層：以 Start-Process -Verb RunAs 執行 inner（UAC），回傳其結束碼。
func ElevateScript(inner string) string {
	return "$ErrorActionPreference = 'Stop'\n" +
		"$p = Start-Process -FilePath 'powershell.exe' -Verb RunAs -Wait -PassThru -WindowStyle Hidden " +
		"-ArgumentList '-NoProfile','-NonInteractive','-ExecutionPolicy','Bypass','-EncodedCommand','" + EncodeCommand(inner) + "'\n" +
		"exit $p.ExitCode\n"
}

// ProbeScript 查詢 wsld 規則數與目前網路設定檔，輸出一行 JSON。
func ProbeScript() string {
	names := make([]string, len(RuleNames))
	for i, n := range RuleNames {
		names[i] = "'" + n + "'"
	}
	return "$r = -1\n" +
		"try { $r = @(Get-NetFirewallHyperVRule -Name " + strings.Join(names, ",") + " -ErrorAction SilentlyContinue).Count } catch { $r = -1 }\n" +
		"$p = @()\n" +
		"try { $p = @(Get-NetConnectionProfile | ForEach-Object { [string]$_.NetworkCategory }) } catch { }\n" +
		"ConvertTo-Json -Compress @{ rules = $r; profiles = $p }\n"
}

// ParseProbe 解析 ProbeScript 的輸出。
func ParseProbe(out string) (Probe, error) {
	var raw struct {
		Rules    int             `json:"rules"`
		Profiles json.RawMessage `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &raw); err != nil {
		return Probe{Rules: -1}, fmt.Errorf("parse probe: %w", err)
	}
	p := Probe{Rules: raw.Rules, Profiles: []string{}}
	// PowerShell 5 對單一元素陣列可能輸出字串而非陣列。
	var list []string
	if err := json.Unmarshal(raw.Profiles, &list); err == nil {
		p.Profiles = append(p.Profiles, list...)
	} else {
		var one string
		if err := json.Unmarshal(raw.Profiles, &one); err == nil && one != "" {
			p.Profiles = append(p.Profiles, one)
		}
	}
	return p, nil
}

// EncodeCommand 轉為 powershell -EncodedCommand 需要的 Base64(UTF-16LE)。
func EncodeCommand(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, len(u)*2)
	for i, c := range u {
		b[2*i] = byte(c)
		b[2*i+1] = byte(c >> 8)
	}
	return base64.StdEncoding.EncodeToString(b)
}
