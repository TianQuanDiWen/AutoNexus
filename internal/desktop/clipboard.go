//go:build windows

package desktop

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// SetClipboardText 将指定文本安全写入 Windows 系统剪贴板
func SetClipboardText(text string) error {
	utf16, err := windows.UTF16FromString(text)
	if err != nil {
		return err
	}

	const GMEM_MOVEABLE = 0x0002
	const CF_UNICODETEXT = 13

	size := uintptr(len(utf16) * 2)
	hMem, _, err := procGlobalAlloc.Call(GMEM_MOVEABLE, size)
	if hMem == 0 {
		return err
	}

	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return err
	}

	copy((*[1 << 20]byte)(unsafe.Pointer(ptr))[:size], (*[1 << 20]byte)(unsafe.Pointer(&utf16[0]))[:size])
	_, _, _ = procGlobalUnlock.Call(hMem)

	r, _, err := procOpenClipboard.Call(0)
	if r == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return err
	}
	defer procCloseClipboard.Call()

	_, _, _ = procEmptyClipboard.Call()
	res, _, err := procSetClipboardData.Call(CF_UNICODETEXT, hMem)
	if res == 0 {
		_, _, _ = procGlobalFree.Call(hMem)
		return err
	}
	return nil
}
