//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// IsAdmin 检查当前进程是否具备 Administrator 管理员特权
func IsAdmin() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY,
		2,
		windows.SECURITY_BUILTIN_DOMAIN_RID,
		windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0,
		&sid,
	)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)

	token := windows.Token(0)
	member, err := token.IsMember(sid)
	if err != nil {
		return false
	}
	return member
}

// RerunAsAdmin 以管理员权限重新启动自身，并退出当前普通进程
func RerunAsAdmin() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	var args []string
	for _, arg := range os.Args[1:] {
		if arg != "--no-elevate" {
			args = append(args, arg)
		}
	}
	args = append(args, "--no-elevate")
	argsStr := windows.ComposeCommandLine(args)

	verbPtr, _ := windows.UTF16PtrFromString("runas")
	exePtr, _ := windows.UTF16PtrFromString(exe)
	argsPtr, _ := windows.UTF16PtrFromString(argsStr)

	// 正确获取当前工作目录，杜绝从 System32 启动导致 config.json 丢失
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		cwd = filepath.Dir(exe)
	}
	cwdPtr, _ := windows.UTF16PtrFromString(cwd)

	err = windows.ShellExecute(0, verbPtr, exePtr, argsPtr, cwdPtr, windows.SW_NORMAL)
	if err != nil {
		return fmt.Errorf("请求 Windows 管理员权限失败: %w", err)
	}

	os.Exit(0)
	return nil
}
