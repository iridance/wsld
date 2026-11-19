package lan

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
)

// RemoveFirewallScript 產生（以系統管理員身分執行的）移除 wsld 0.9.0 Hyper-V 防火牆規則腳本。
func RemoveFirewallScript() string {
	var b strings.Builder
	for _, name := range RuleNames {
		fmt.Fprintf(&b, "Remove-NetFirewallHyperVRule -Name '%s' -ErrorAction SilentlyContinue\n", name)
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

// RulesScript 輸出仍存在的 wsld 規則數；無法查詢時輸出 -1。
func RulesScript() string {
	names := make([]string, len(RuleNames))
	for i, n := range RuleNames {
		names[i] = "'" + n + "'"
	}
	return "try { @(Get-NetFirewallHyperVRule -Name " + strings.Join(names, ",") +
		" -ErrorAction SilentlyContinue).Count } catch { -1 }\n"
}

// ParseRules 解析 RulesScript 的輸出；無法解析回 -1。
func ParseRules(out string) int {
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return -1
	}
	return n
}

// NetstatPortInUse 解析 `netstat.exe -ano -p TCP` 輸出：本機位址的埠等於 port 的任何一列（任何狀態）都算佔用。
func NetstatPortInUse(out string, port int) bool {
	suffix := fmt.Sprintf(":%d", port)
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 || !strings.EqualFold(f[0], "TCP") {
			continue
		}
		if strings.HasSuffix(f[1], suffix) {
			return true
		}
	}
	return false
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
