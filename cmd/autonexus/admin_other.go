//go:build !windows

package main

// IsAdmin 非 Windows 平台占位
func IsAdmin() bool {
	return true
}

// RerunAsAdmin 非 Windows 平台占位
func RerunAsAdmin() error {
	return nil
}
