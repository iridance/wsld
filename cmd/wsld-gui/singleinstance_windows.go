//go:build windows

package main

import (
	"context"
	"errors"

	"golang.org/x/sys/windows"
)

const (
	singleInstanceMutex = `Local\wsld-gui-single`
	singleInstanceEvent = `Local\wsld-gui-show`
)

// singleInstance 以命名 Mutex 保證 wsld-gui 只跑一個程序（一個視窗 + 一個 systray）。
type singleInstance struct {
	mutex windows.Handle
	event windows.Handle
}

// acquireSingleInstance 嘗試成為唯一實例。
// primary=false 表示已有實例在跑（呼叫端應喚醒對方後退出）。
func acquireSingleInstance() (si *singleInstance, primary bool, err error) {
	mutexName, err := windows.UTF16PtrFromString(singleInstanceMutex)
	if err != nil {
		return nil, false, err
	}
	mutex, err := windows.CreateMutex(nil, false, mutexName)
	if err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			if mutex != 0 {
				_ = windows.CloseHandle(mutex)
			}
			return nil, false, nil
		}
		return nil, false, err
	}

	eventName, err := windows.UTF16PtrFromString(singleInstanceEvent)
	if err != nil {
		_ = windows.CloseHandle(mutex)
		return nil, false, err
	}
	event, err := windows.CreateEvent(nil, 0, 0, eventName) // auto-reset, nonsignaled
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.CloseHandle(mutex)
		return nil, false, err
	}
	return &singleInstance{mutex: mutex, event: event}, true, nil
}

// Release 釋放鎖（程序結束時呼叫）。
func (s *singleInstance) Release() {
	if s == nil {
		return
	}
	if s.event != 0 {
		_ = windows.CloseHandle(s.event)
		s.event = 0
	}
	if s.mutex != 0 {
		_ = windows.CloseHandle(s.mutex)
		s.mutex = 0
	}
}

// WatchShow 監聽「第二實例請求開窗」事件，直到 ctx 取消。
func (s *singleInstance) WatchShow(ctx context.Context, show func()) {
	if s == nil || s.event == 0 || show == nil {
		return
	}
	go func() {
		for {
			if ctx.Err() != nil {
				return
			}
			r, err := windows.WaitForSingleObject(s.event, 500)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				continue
			}
			if r == uint32(windows.WAIT_OBJECT_0) {
				show()
			}
		}
	}()
}

// notifyExistingInstance 請已運行的實例顯示主視窗。
func notifyExistingInstance() {
	eventName, err := windows.UTF16PtrFromString(singleInstanceEvent)
	if err != nil {
		return
	}
	event, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, eventName)
	if err != nil {
		return
	}
	defer windows.CloseHandle(event)
	_ = windows.SetEvent(event)
}
