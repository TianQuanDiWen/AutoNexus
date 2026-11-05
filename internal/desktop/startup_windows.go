//go:build windows

package desktop

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowTextLengthW     = user32.NewProc("GetWindowTextLengthW")
	procGetWindowTextW           = user32.NewProc("GetWindowTextW")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procShowWindowAsync          = user32.NewProc("ShowWindowAsync")
	procMessageBoxW              = user32.NewProc("MessageBoxW")
)

const (
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
)

// RestoreExistingWindow restores a uniquely matching top-level window. It
// deliberately enumerates hidden windows because GetMainWindowHandle-style
// process lookup often returns zero after the app is minimized to the tray.
func RestoreExistingWindow(title string) (ExistingWindowResult, error) {
	var candidates []windowCandidate
	callback := windows.NewCallback(func(hwnd, _ uintptr) uintptr {
		length, _, _ := procGetWindowTextLengthW.Call(hwnd)
		if length == 0 || length > 1024 {
			return 1
		}
		buf := make([]uint16, length+1)
		procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		pid := uint32(0)
		procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
		candidates = append(candidates, windowCandidate{
			hwnd:  hwnd,
			title: windows.UTF16ToString(buf),
			pid:   pid,
		})
		return 1
	})
	result, _, callErr := procEnumWindows.Call(callback, 0)
	if result == 0 {
		return ExistingWindowNotFound, fmt.Errorf("EnumWindows returned FALSE (last error: %v)", callErr)
	}

	hwnd, count := selectUniqueWindow(candidates, title, windows.GetCurrentProcessId())
	if count == 0 {
		return ExistingWindowNotFound, nil
	}
	if count > 1 {
		return ExistingWindowAmbiguous, nil
	}

	if shown, _, callErr := procShowWindowAsync.Call(hwnd, SW_RESTORE); shown == 0 {
		return ExistingWindowNotFound, fmt.Errorf("ShowWindowAsync returned FALSE (last error: %v)", callErr)
	}
	procSetForegroundWindow.Call(hwnd)
	return ExistingWindowRestored, nil
}

// ShowStartupError displays startup failures even when the executable was
// built with the Windows GUI subsystem and has no visible console.
func ShowStartupError(title, message string) {
	titlePtr, titleErr := windows.UTF16PtrFromString(title)
	messagePtr, messageErr := windows.UTF16PtrFromString(message)
	if titleErr != nil || messageErr != nil {
		return
	}
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(messagePtr)), uintptr(unsafe.Pointer(titlePtr)), mbOK|mbIconError|mbSetForeground)
}
