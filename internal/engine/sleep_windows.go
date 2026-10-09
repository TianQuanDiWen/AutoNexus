//go:build windows

package engine

import "golang.org/x/sys/windows"

var (
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
)

const (
	esSystemRequired   = 0x00000001
	esAwaymodeRequired = 0x00000040
	esContinuous       = 0x80000000
)

// PreventSleep 阻止 Windows 系统进入休眠/睡眠状态 (允许正常熄灭屏幕以省电)
func PreventSleep() {
	_, _, _ = procSetThreadExecutionState.Call(uintptr(esContinuous | esSystemRequired | esAwaymodeRequired))
}

// AllowSleep 恢复 Windows 默认休眠策略
func AllowSleep() {
	_, _, _ = procSetThreadExecutionState.Call(uintptr(esContinuous))
}
