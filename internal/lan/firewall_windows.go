//go:build windows

package lan

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	ole "github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
	"golang.org/x/sys/windows"
)

// 透過 Windows 防火牆的 COM API（HNetCfg.FwPolicy2，與「具有進階安全性的 Windows Defender 防火牆」同一套）讀寫規則。
// 讀取不需提權；寫入一般由提權的 netsh.exe 進行（Manager.AllowFirewall），
// 有封鎖規則或既有 wsld 規則時才在以 UAC 啟動的「wsld-gui.exe --allow-firewall」行程中進行（AddAppRule）。

const (
	fwDirIn      = 1
	fwActionDeny = 0
	fwActionOK   = 1
	sFalse       = 0x00000001 // CoInitializeEx：此執行緒已初始化
	rpcChanged   = 0x80010106 // RPC_E_CHANGED_MODE：此執行緒已用其他 apartment 初始化
)

// withFwPolicy 在鎖定的 OS 執行緒上初始化 COM、建立 FwPolicy2，並把 Rules 集合交給 fn。
func withFwPolicy(fn func(rules *ole.IDispatch) error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitializeEx(0, ole.COINIT_APARTMENTTHREADED); err != nil {
		var oe *ole.OleError
		switch {
		case errors.As(err, &oe) && oe.Code() == sFalse:
			defer ole.CoUninitialize()
		case errors.As(err, &oe) && oe.Code() == rpcChanged:
			// 沿用既有的 apartment，不可 CoUninitialize
		default:
			return fmt.Errorf("CoInitializeEx: %w", err)
		}
	} else {
		defer ole.CoUninitialize()
	}
	unk, err := oleutil.CreateObject("HNetCfg.FwPolicy2")
	if err != nil {
		return fmt.Errorf("HNetCfg.FwPolicy2: %w", err)
	}
	defer unk.Release()
	pol, err := unk.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer pol.Release()
	rv, err := oleutil.GetProperty(pol, "Rules")
	if err != nil {
		return fmt.Errorf("Rules: %w", err)
	}
	defer rv.Clear()
	return fn(rv.ToIDispatch())
}

func propString(d *ole.IDispatch, name string) string {
	v, err := oleutil.GetProperty(d, name)
	if err != nil {
		return ""
	}
	defer v.Clear()
	s, _ := v.Value().(string)
	return s
}

func propInt(d *ole.IDispatch, name string) int {
	v, err := oleutil.GetProperty(d, name)
	if err != nil {
		return 0
	}
	defer v.Clear()
	switch n := v.Value().(type) {
	case int32:
		return int(n)
	case int64:
		return int(n)
	case int:
		return n
	case uint32:
		return int(n)
	}
	return 0
}

func propBool(d *ole.IDispatch, name string) bool {
	v, err := oleutil.GetProperty(d, name)
	if err != nil {
		return false
	}
	defer v.Clear()
	b, _ := v.Value().(bool)
	return b
}

// forEachAppRule 對 ApplicationName 等於 exe 的每條規則呼叫 fn（其他規則不讀其餘欄位，以免逐條查詢過慢）。
func forEachAppRule(rules *ole.IDispatch, exe string, fn func(r *ole.IDispatch, info FwRule) error) error {
	return oleutil.ForEach(rules, func(v *ole.VARIANT) error {
		defer v.Clear()
		r := v.ToIDispatch()
		if r == nil {
			return nil
		}
		app := propString(r, "ApplicationName")
		if !SamePath(app, exe) {
			return nil
		}
		info := FwRule{
			Name:     propString(r, "Name"),
			App:      app,
			Inbound:  propInt(r, "Direction") == fwDirIn,
			Allow:    propInt(r, "Action") == fwActionOK,
			Enabled:  propBool(r, "Enabled"),
			Protocol: propInt(r, "Protocol"),
			Profiles: propInt(r, "Profiles"),
		}
		return fn(r, info)
	})
}

// ReadAppRules 讀出 ApplicationName 等於 exe 的防火牆規則（不需提權）。
func ReadAppRules(exe string) ([]FwRule, error) {
	var out []FwRule
	err := withFwPolicy(func(rules *ole.IDispatch) error {
		return forEachAppRule(rules, exe, func(_ *ole.IDispatch, info FwRule) error {
			out = append(out, info)
			return nil
		})
	})
	return out, err
}

// AddAppRule（須以系統管理員身分執行）替 exe 建立或更新 wsld 的對內允許規則：
// TCP、私人＋公用網路、只接受同網段；同時停用針對 exe 的對內封鎖規則（封鎖優先於允許）。
func AddAppRule(exe string) error {
	return withFwPolicy(func(rules *ole.IDispatch) error {
		var own *ole.IDispatch
		err := forEachAppRule(rules, exe, func(r *ole.IDispatch, info FwRule) error {
			if info.Inbound && !info.Allow && info.Enabled {
				if _, err := oleutil.PutProperty(r, "Enabled", false); err != nil {
					return fmt.Errorf("disable block rule %q: %w", info.Name, err)
				}
			}
			if own == nil && info.Name == AppRuleName {
				r.AddRef()
				own = r
			}
			return nil
		})
		if err != nil {
			return err
		}
		add := own == nil
		if add {
			unk, err := oleutil.CreateObject("HNetCfg.FWRule")
			if err != nil {
				return fmt.Errorf("HNetCfg.FWRule: %w", err)
			}
			defer unk.Release()
			if own, err = unk.QueryInterface(ole.IID_IDispatch); err != nil {
				return err
			}
		}
		defer own.Release()
		// Protocol 須先於埠／位址設定；Direction 須在加入集合前設定
		for _, p := range []struct {
			name string
			val  any
		}{
			{"Name", AppRuleName},
			{"Description", AppRuleDescription},
			{"Grouping", AppRuleGroup},
			{"ApplicationName", exe},
			{"Protocol", int32(FwProtoTCP)},
			{"Direction", int32(fwDirIn)},
			{"Action", int32(fwActionOK)},
			{"Profiles", int32(FwProfilePrivate | FwProfilePublic)},
			{"RemoteAddresses", AppRuleRemote},
			{"InterfaceTypes", "All"},
			{"Enabled", true},
		} {
			if _, err := oleutil.PutProperty(own, p.name, p.val); err != nil {
				return fmt.Errorf("set %s: %w", p.name, err)
			}
		}
		if add {
			if _, err := oleutil.CallMethod(rules, "Add", own); err != nil {
				return fmt.Errorf("add rule: %w", err)
			}
		}
		return nil
	})
}

// HRESULTOf 取出 COM 錯誤碼（供 --allow-firewall 行程當結束碼回報給父行程）；非 COM 錯誤回 1。
func HRESULTOf(err error) uint32 {
	var oe *ole.OleError
	if errors.As(err, &oe) && oe.Code() != 0 {
		return uint32(oe.Code())
	}
	return 1
}

// AppRules 實作 Host。
func (SystemHost) AppRules(_ context.Context, exe string) ([]FwRule, error) { return ReadAppRules(exe) }

// shellExecuteInfo 對應 SHELLEXECUTEINFOW。
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.HWND
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     uintptr
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    uintptr
	dwHotKey     uint32
	hIcon        uintptr
	hProcess     windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
)

var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// RunElevated 實作 Host：以 ShellExecuteEx「runas」啟動 file params（UAC），等它結束並取回結束碼。
func (SystemHost) RunElevated(ctx context.Context, path, args string) error {
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	params, err := windows.UTF16PtrFromString(args)
	if err != nil {
		return err
	}
	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		hwnd:         windows.GetForegroundWindow(), // UAC 視窗以使用者剛點擊的 wsld-gui 為父視窗，不會只在工作列閃爍
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: params,
		nShow:        windows.SW_HIDE,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))
	if r, _, e := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei))); r == 0 {
		if errors.Is(e, windows.ERROR_CANCELLED) {
			return ErrUACCanceled
		}
		return fmt.Errorf("ShellExecuteEx runas: %w", e)
	}
	if sei.hProcess == 0 {
		return errors.New("elevated process handle unavailable")
	}
	defer windows.CloseHandle(sei.hProcess)
	for {
		ev, err := windows.WaitForSingleObject(sei.hProcess, 200)
		if err != nil {
			return err
		}
		if ev == windows.WAIT_OBJECT_0 {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	var code uint32
	if err := windows.GetExitCodeProcess(sei.hProcess, &code); err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("adding the firewall rule failed (exit 0x%08X)", code)
	}
	return nil
}
