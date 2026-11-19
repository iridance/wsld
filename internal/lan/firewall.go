package lan

import (
	"errors"
	"os"
	"path"
	"regexp"
	"strings"
)

// FirewallState 是 wsld-gui.exe 在 Windows 防火牆的對內授權狀態（私人＋公用網路）。
type FirewallState string

const (
	// FirewallAllowed：已有啟用中的對內允許規則涵蓋私人與公用網路（TCP）。
	FirewallAllowed FirewallState = "allowed"
	// FirewallMissing：沒有規則，或允許規則未涵蓋私人＋公用（例如 Windows 視窗只勾了一種）。
	FirewallMissing FirewallState = "missing"
	// FirewallBlocked：有啟用中的對內封鎖規則（例如在 Windows 視窗按了取消）；封鎖優先於允許。
	FirewallBlocked FirewallState = "blocked"
	// FirewallUnknown：無法查詢（非 Windows、COM 失敗）。
	FirewallUnknown FirewallState = "unknown"
)

// 防火牆規則常數（對應 INetFwRule 的列舉值）。
const (
	FwProfilePrivate = 0x2
	FwProfilePublic  = 0x4
	FwProtoTCP       = 6
	FwProtoAny       = 256
)

// AppRuleName／AppRuleGroup 是 wsld 為 wsld-gui.exe 建立的規則名稱與群組。
const (
	AppRuleName  = "wsld-gui (LAN access)"
	AppRuleGroup = "wsld"
	// AppRuleRemote：只接受同網段（LocalSubnet）的連線。
	AppRuleRemote = "LocalSubnet"
)

// AllowFirewallFlag 是 wsld-gui.exe 以系統管理員身分（UAC）執行時的參數：只加防火牆規則後結束。
const AllowFirewallFlag = "--allow-firewall"

// ErrUACCanceled 表示使用者在 UAC 視窗按了「否」。
var ErrUACCanceled = errors.New("canceled at the Windows UAC prompt")

// FwRule 是判斷授權狀態需要的防火牆規則欄位。
type FwRule struct {
	Name     string
	App      string
	Inbound  bool
	Allow    bool
	Enabled  bool
	Protocol int
	Profiles int
}

// EvaluateFirewall 依規則判斷 exe 的對內授權狀態：
// 有啟用中的封鎖規則（私人或公用、TCP 或任何協定）→ blocked；允許規則的 profile 聯集涵蓋私人＋公用 → allowed；否則 missing。
func EvaluateFirewall(rules []FwRule, exe string) FirewallState {
	const want = FwProfilePrivate | FwProfilePublic
	allowed := 0
	for _, r := range rules {
		if !r.Enabled || !r.Inbound || !SamePath(r.App, exe) {
			continue
		}
		if r.Protocol != FwProtoTCP && r.Protocol != FwProtoAny {
			continue
		}
		if !r.Allow {
			if r.Profiles&want != 0 {
				return FirewallBlocked
			}
			continue
		}
		allowed |= r.Profiles
	}
	if allowed&want == want {
		return FirewallAllowed
	}
	return FirewallMissing
}

// FirewallMethod 是加入規則時要提權執行的程式。
type FirewallMethod string

const (
	// MethodNetsh：提權 Windows 內建的 netsh.exe 加入規則（UAC 顯示已驗證的「網路命令殼層／Microsoft Windows」，詳細資料列出整條規則）。
	MethodNetsh FirewallMethod = "netsh"
	// MethodHelper：提權 wsld-gui.exe --allow-firewall（COM）；能一次停用封鎖規則並更新既有規則，netsh 一行指令做不到。
	MethodHelper FirewallMethod = "helper"
)

// AppRuleDescription 是規則的描述（netsh 與 COM 共用）。
const AppRuleDescription = "Lets devices on the same network reach container ports forwarded by wsld-gui (LAN access)."

// PlanFirewall 決定加入規則的方式：有針對 exe 的對內封鎖規則、已有 wsld 自己的規則（netsh add 會重複），
// 或 exe 位於 UNC 路徑（dev build）時用 helper；其餘用 netsh。
func PlanFirewall(rules []FwRule, exe string) FirewallMethod {
	if strings.HasPrefix(strings.ReplaceAll(exe, "/", `\`), `\\`) {
		return MethodHelper
	}
	for _, r := range rules {
		if !SamePath(r.App, exe) {
			continue
		}
		if r.Name == AppRuleName || (r.Inbound && !r.Allow) {
			return MethodHelper
		}
	}
	return MethodNetsh
}

// NetshAddRuleArgs 是提權 netsh.exe 時的參數（不含程式本身）：與 AddAppRule 相同的規則（TCP、私人＋公用、僅同網段）。
func NetshAddRuleArgs(exe string) string {
	return `advfirewall firewall add rule name="` + AppRuleName + `" dir=in action=allow program="` + exe +
		`" protocol=TCP profile=private,public remoteip=localsubnet interfacetype=any enable=yes description="` +
		AppRuleDescription + `"`
}

// NetshPath 回傳 %SystemRoot%\System32\netsh.exe（以完整路徑提權，不依賴 PATH）。
func NetshPath() string {
	root := strings.TrimRight(os.Getenv("SystemRoot"), `\`)
	if root == "" {
		root = `C:\Windows`
	}
	return root + `\System32\netsh.exe`
}

var envRef = regexp.MustCompile(`%([^%]+)%`)

// SamePath 比較兩個 Windows 路徑（不分大小寫、展開 %VAR%、統一斜線）。
func SamePath(a, b string) bool {
	na, nb := normPath(a), normPath(b)
	return na != "" && na == nb
}

func normPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = envRef.ReplaceAllStringFunc(p, func(m string) string {
		if v, ok := os.LookupEnv(m[1 : len(m)-1]); ok {
			return v
		}
		return m
	})
	p = strings.ReplaceAll(p, `\`, "/")
	prefix := ""
	if strings.HasPrefix(p, "//") { // UNC：\\server\share\...
		prefix, p = `\\`, strings.TrimLeft(p, "/")
	}
	p = path.Clean(p)
	return strings.ToLower(prefix + strings.ReplaceAll(p, "/", `\`))
}
