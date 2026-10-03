//go:build !windows

package desktop

import "errors"

// Config 桌面窗口初始化配置 (非 Windows 平台桩定义)
type Config struct {
	Title       string
	URL         string
	LANURL      string
	Width       uint
	Height      uint
	Debug       bool
	OnStopQueue func()
	OnExit      func()
}

// Run 在非 Windows 平台返回不支持错误，触发自动降级无头后台服务
func Run(cfg Config) error {
	return errors.New("桌面 GUI 仅支持在 Windows 平台运行")
}

// TerminateActiveApp 非 Windows 平台空实现
func TerminateActiveApp() {}
