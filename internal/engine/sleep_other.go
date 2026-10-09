//go:build !windows

package engine

// PreventSleep 非 Windows 平台空实现
func PreventSleep() {}

// AllowSleep 非 Windows 平台空实现
func AllowSleep() {}
