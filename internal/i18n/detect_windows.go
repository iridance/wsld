//go:build windows

package i18n

import (
	"os"
	"syscall"
)

// Detect 以 GetUserDefaultUILanguage 取得 Windows UI 語系。
func Detect() Lang {
	var id uint16
	if proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultUILanguage"); proc.Find() == nil {
		r, _, _ := proc.Call()
		id = uint16(r)
	}
	return DetectFrom(id, os.Getenv)
}
