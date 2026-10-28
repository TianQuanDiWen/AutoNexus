//go:build windows

package desktop

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Config 桌面窗口与系统托盘初始化配置
type Config struct {
	Title       string // 窗口标题
	URL         string // 本地访问地址
	LANURL      string // 局域网访问地址 (用于一键复制到剪贴板)
	Width       uint   // 初始宽度
	Height      uint   // 初始高度
	Debug       bool   // 是否启用开发者工具
	Theme       string // 界面主题: auto(跟随系统)/dark/light
	OnStopQueue func() // 托盘菜单“急停所有任务”回调
	OnExit      func() // 托盘菜单“彻底退出”回调
}

// App 桌面宿主实例
type App struct {
	cfg          Config
	w            webview2.WebView
	hwnd         uintptr
	oldWndProc   uintptr
	hIcon        uintptr
	balloonShown bool
	mu           sync.Mutex
	isQuitting   bool
}

var activeApp atomic.Pointer[App]

func setWindowLongPtr(hwnd uintptr, nIndex int, newLong uintptr) (uintptr, error) {
	if procSetWindowLongPtrW.Find() == nil {
		r, _, err := procSetWindowLongPtrW.Call(hwnd, uintptr(int64(nIndex)), newLong)
		return r, err
	}
	proc := user32.NewProc("SetWindowLongW")
	r, _, err := proc.Call(hwnd, uintptr(int64(nIndex)), newLong)
	return r, err
}

func globalWndProc(hwnd, msg, wp, lp uintptr) uintptr {
	app := activeApp.Load()
	if app != nil && app.hwnd == hwnd {
		return app.handleWndProc(hwnd, uint32(msg), wp, lp)
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wp, lp)
	return r
}

func (a *App) handleWndProc(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
	switch msg {
	case WM_CLOSE:
		if a.isQuitting {
			break
		}
		// 拦截点叉事件：隐藏窗口转入后台托盘
		_, _, _ = procShowWindow.Call(hwnd, SW_HIDE)
		if !a.balloonShown {
			a.ShowBalloon(a.cfg.Title, "已最小化至系统托盘，调度任务不受影响。\n单击托盘图标可重新打开。")
			a.balloonShown = true
		}
		return 0

	case WM_TRAYICON:
		switch lp {
		case WM_LBUTTONDBLCLK, WM_LBUTTONUP:
			a.ShowAndRestore()
		case WM_RBUTTONUP:
			a.showContextMenu()
		}
		return 0

	case WM_DESTROY:
		a.removeTrayIcon()
	}

	r, _, _ := procCallWindowProcW.Call(a.oldWndProc, hwnd, uintptr(msg), wp, lp)
	return r
}

// ShowAndRestore 唤出并置顶主界面窗口
func (a *App) ShowAndRestore() {
	_, _, _ = procShowWindow.Call(a.hwnd, SW_RESTORE)
	_, _, _ = procSetForegroundWindow.Call(a.hwnd)
}

func (a *App) addTrayIcon() {
	var nid NOTIFYICONDATA
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = a.hwnd
	nid.UID = 1
	nid.UFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP
	nid.UCallbackMessage = WM_TRAYICON
	nid.HIcon = a.hIcon
	tip, _ := windows.UTF16FromString(a.cfg.Title)
	copy(nid.SzTip[:], tip)
	_, _, _ = procShellNotifyIconW.Call(NIM_ADD, uintptr(unsafe.Pointer(&nid)))
}

// ShowBalloon 在任务栏托盘弹出 Windows 系统提示气泡
func (a *App) ShowBalloon(title, info string) {
	var nid NOTIFYICONDATA
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = a.hwnd
	nid.UID = 1
	nid.UFlags = NIF_INFO
	nid.DwInfoFlags = NIIF_INFO
	copy(nid.SzInfoTitle[:], windows.StringToUTF16(title))
	copy(nid.SzInfo[:], windows.StringToUTF16(info))
	_, _, _ = procShellNotifyIconW.Call(NIM_MODIFY, uintptr(unsafe.Pointer(&nid)))
}

func (a *App) removeTrayIcon() {
	var nid NOTIFYICONDATA
	nid.CbSize = uint32(unsafe.Sizeof(nid))
	nid.HWnd = a.hwnd
	nid.UID = 1
	_, _, _ = procShellNotifyIconW.Call(NIM_DELETE, uintptr(unsafe.Pointer(&nid)))
}

func (a *App) showContextMenu() {
	hMenu, _, _ := procCreatePopupMenu.Call()
	if hMenu == 0 {
		return
	}
	defer procDestroyMenu.Call(hMenu)

	titlePtr, _ := windows.UTF16PtrFromString("🖥️  打开主界面")
	_, _, _ = procAppendMenuW.Call(hMenu, MF_STRING, ID_MENU_SHOW, uintptr(unsafe.Pointer(titlePtr)))

	if a.cfg.LANURL != "" {
		lanPtr, _ := windows.UTF16PtrFromString("🌐  复制局域网访问地址")
		_, _, _ = procAppendMenuW.Call(hMenu, MF_STRING, ID_MENU_COPY_LAN, uintptr(unsafe.Pointer(lanPtr)))
	}

	_, _, _ = procAppendMenuW.Call(hMenu, MF_SEPARATOR, 0, 0)

	stopPtr, _ := windows.UTF16PtrFromString("🛑  一键急停所有任务")
	_, _, _ = procAppendMenuW.Call(hMenu, MF_STRING, ID_MENU_STOP_ALL, uintptr(unsafe.Pointer(stopPtr)))

	_, _, _ = procAppendMenuW.Call(hMenu, MF_SEPARATOR, 0, 0)

	quitPtr, _ := windows.UTF16PtrFromString("🚪  彻底退出")
	_, _, _ = procAppendMenuW.Call(hMenu, MF_STRING, ID_MENU_QUIT, uintptr(unsafe.Pointer(quitPtr)))

	var pt POINT
	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))

	// TrackPopupMenu 前必须激活当前窗口，确保在菜单外点击时菜单能自动消失
	_, _, _ = procSetForegroundWindow.Call(a.hwnd)

	cmd, _, _ := procTrackPopupMenuEx.Call(hMenu, TPM_RETURNCMD|TPM_NONOTIFY|TPM_RIGHTBUTTON, uintptr(pt.X), uintptr(pt.Y), a.hwnd, 0)

	switch cmd {
	case ID_MENU_SHOW:
		a.ShowAndRestore()
	case ID_MENU_COPY_LAN:
		if a.cfg.LANURL != "" {
			_ = SetClipboardText(a.cfg.LANURL)
			a.ShowBalloon("局域网地址已复制", a.cfg.LANURL+"\n可在手机或同局域网设备浏览器中打开。")
		}
	case ID_MENU_STOP_ALL:
		if a.cfg.OnStopQueue != nil {
			go a.cfg.OnStopQueue()
		}
		a.ShowBalloon("已发送急停指令", "正在终止当前运行的所有任务与自动化排队。")
	case ID_MENU_QUIT:
		a.Exit()
	}
}

// Exit 彻底退出桌面客户端与托盘
func (a *App) Exit() {
	a.mu.Lock()
	if a.isQuitting {
		a.mu.Unlock()
		return
	}
	a.isQuitting = true
	a.mu.Unlock()

	a.removeTrayIcon()

	if a.cfg.OnExit != nil {
		a.cfg.OnExit()
	}

	if a.w != nil {
		a.w.Dispatch(func() {
			a.w.Terminate()
		})
	}
}

// systemPrefersDark 读取 Windows 个性化设置中的应用主题偏好；读取失败时默认视为深色
func systemPrefersDark() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return true
	}
	defer k.Close()
	val, _, err := k.GetIntegerValue("AppsUseLightTheme")
	if err != nil {
		return true
	}
	return val == 0
}

// applyChrome 将原生标题栏/边框着色融入当前界面主题：深色走应用底色，浅色走系统浅色栏。
// 标题栏/边框着色为 Windows 11 特性，旧系统静默忽略；深色模式属性覆盖到 Windows 10 1809+ (含 19 号旧属性号)
func applyChrome(hwnd uintptr, dark bool) {
	enable := uint32(0)
	if dark {
		enable = 1
	}
	tryAttr := func(attr uintptr) bool {
		ret, _, _ := procDwmSetWindowAttribute.Call(hwnd, attr, uintptr(unsafe.Pointer(&enable)), unsafe.Sizeof(enable))
		return ret == 0
	}
	if !tryAttr(DWMWA_USE_IMMERSIVE_DARK_MODE) {
		tryAttr(DWMWA_USE_IMMERSIVE_DARK_MODE_OLD)
	}
	// COLORREF 布局为 0x00BBGGRR
	caption := uint32(0x00161210) // 深色标题栏=应用底色 #101216
	border := uint32(0x00382c26)  // 深色边框=面板描边 #262c38
	if !dark {
		caption = 0x00f6f4f3 // 浅色标题栏=#f3f4f6
		border = 0x00dbd5d1  // 浅色边框=#d1d5db
	}
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_CAPTION_COLOR, uintptr(unsafe.Pointer(&caption)), unsafe.Sizeof(caption))
	procDwmSetWindowAttribute.Call(hwnd, DWMWA_BORDER_COLOR, uintptr(unsafe.Pointer(&border)), unsafe.Sizeof(border))
}

// Run 启动 WebView2 桌面窗口与系统托盘事件循环 (必须在 OS 主线程运行)
func Run(cfg Config) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if cfg.Width == 0 {
		cfg.Width = 1280
	}
	if cfg.Height == 0 {
		cfg.Height = 820
	}
	if cfg.Title == "" {
		cfg.Title = "AutoNexus 自动化总控平台"
	}

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug: cfg.Debug,
		WindowOptions: webview2.WindowOptions{
			Title:  cfg.Title,
			Width:  cfg.Width,
			Height: cfg.Height,
			Center: true,
		},
	})
	if w == nil {
		return errors.New("failed to initialize webview2, please ensure Microsoft Edge WebView2 runtime is installed")
	}
	defer w.Destroy()

	hwnd := uintptr(w.Window())
	dark := cfg.Theme == "dark" || (cfg.Theme != "light" && systemPrefersDark())
	applyChrome(hwnd, dark)
	hIcon, _, _ := procLoadIconW.Call(0, uintptr(IDI_APPLICATION))

	app := &App{
		cfg:   cfg,
		w:     w,
		hwnd:  hwnd,
		hIcon: hIcon,
	}

	activeApp.Store(app)

	// 子类化窗口过程以拦截 WM_CLOSE 和处理托盘通知 WM_TRAYICON
	newCallback := windows.NewCallback(globalWndProc)
	oldProc, _ := setWindowLongPtr(hwnd, GWLP_WNDPROC, newCallback)
	app.oldWndProc = oldProc

	// 添加系统托盘图标
	app.addTrayIcon()

	// 网页端把解析后的主题 (含跟随系统的实时变化) 推送到原生侧，标题栏即刻同步
	_ = w.Bind("__autonexusSetTheme", func(theme string) {
		if theme == "dark" || theme == "light" {
			applyChrome(hwnd, theme == "dark")
		}
	})

	// 导航至本地 Web 服务
	w.Navigate(cfg.URL)

	// 运行 Windows UI 消息循环
	w.Run()

	// 退出后清理
	app.removeTrayIcon()
	activeApp.Store(nil)

	return nil
}

// TerminateActiveApp 如果当前桌面窗口正在运行，外部请求终止
func TerminateActiveApp() {
	app := activeApp.Load()
	if app != nil {
		app.Exit()
	}
}
