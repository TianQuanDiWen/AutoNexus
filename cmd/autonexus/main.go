package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"autonexus/internal/config"
	"autonexus/internal/desktop"
	"autonexus/internal/engine"
	"autonexus/internal/executor"
	"autonexus/internal/server"
)

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "config.json", "配置文件存储路径")
	portFlag := flag.Int("port", 0, "自定义服务端口 (默认使用配置文件设置)")
	noElevate := flag.Bool("no-elevate", false, "禁止自动尝试请求 Windows 管理员特权")
	noUI := flag.Bool("no-ui", false, "无头后台运行模式 (不创建桌面独立窗口与托盘)")
	debugUI := flag.Bool("debug-ui", false, "启用桌面端 WebView2 开发者工具与右键菜单")
	flag.Parse()

	// 自动检查并提升为 Windows 管理员权限，确保拉起的自动化子任务与游戏能够继承特权，彻底消除 UAC 弹窗
	if !*noElevate && !IsAdmin() {
		fmt.Println("检测到当前以普通权限运行，正在以管理员特权启动总控服务，以确保整套自动化流程零弹窗打扰...")
		if err := RerunAsAdmin(); err != nil {
			fmt.Printf("自动提权启动失败，降级以当前权限继续运行: %v\n", err)
		} else {
			return nil
		}
	}

	// 1. 初始化配置
	cfgMgr, err := config.NewManager(*cfgPath)
	if err != nil {
		message := fmt.Sprintf("加载配置文件失败: %v", err)
		if noticeErr := startupNotice(message, *noUI); noticeErr != nil {
			return noticeErr
		}
		return nil
	}

	cfg := cfgMgr.Get()
	port := cfg.Port
	if *portFlag > 0 {
		port = *portFlag
	}

	// 2. Bind synchronously before constructing the engine/runner. A second
	// launch must not create scheduler state when this address is already owned.
	listenAddr := fmt.Sprintf("%s:%d", cfg.Host, port)
	var broadcaster *executor.Broadcaster
	var eng *engine.Engine
	var srv *server.Server
	listener, err := bindBeforeRuntime(listenAddr, func(net.Listener) error {
		broadcaster = executor.NewBroadcaster(1000)
		runner := executor.NewRunner(broadcaster)
		eng = engine.NewEngine(cfgMgr, runner, broadcaster)
		srv = server.NewServer(cfgMgr, eng, broadcaster)
		return nil
	})
	if err != nil {
		localURL := fmt.Sprintf("http://127.0.0.1:%d", port)
		if isAutoNexusService(&http.Client{Timeout: 800 * time.Millisecond}, localURL) {
			result := desktop.ExistingWindowNotFound
			var restoreErr error
			if !*noUI {
				result, restoreErr = desktop.RestoreExistingWindow(desktopWindowTitle)
				if restoreErr == nil && result == desktop.ExistingWindowRestored {
					return nil
				}
			}
			message := fmt.Sprintf("AutoNexus 已在运行，地址：%s。", localURL)
			switch {
			case *noUI:
				message += "当前为无界面模式，未尝试操作现有窗口。"
			case restoreErr != nil:
				message += "唤起现有窗口时出错；请在浏览器中打开该地址。"
			case result == desktop.ExistingWindowAmbiguous:
				message += "检测到多个同名窗口，为避免选错窗口，请在浏览器中打开该地址。"
			default:
				message += "没有找到唯一匹配的桌面窗口，请在浏览器中打开该地址。"
			}
			return startupNotice(message, *noUI)
		}
		message := fmt.Sprintf("AutoNexus 无法监听 %s。请检查端口是否被占用，以及主机地址是否有效。\n\n详细信息：%v", listenAddr, err)
		return startupNotice(message, *noUI)
	}

	// 3. Build the HTTP server around the already-bound socket.
	httpServer := &http.Server{
		Addr:              listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 4. 打印欢迎面板与局域网 IP 提示
	printBanner(port, &cfg)

	// 5. Start HTTP serving. Errors are delivered to the main flow rather than
	// calling log.Fatalf from a goroutine (which would silently os.Exit in GUI mode).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	serveErrors := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrors <- err
		}
		close(serveErrors)
	}()

	var runErr error
	if *noUI {
		// 纯后台模式：阻塞等待系统信号
		select {
		case <-ctx.Done():
		case err, ok := <-serveErrors:
			if ok {
				runErr = fmt.Errorf("HTTP 服务异常退出: %w", err)
			}
		}
	} else {
		// 桌面 UI 模式：由主线程承载 WebView2 窗口与托盘消息循环
		go func() {
			<-ctx.Done()
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for !desktop.TerminateActiveApp() {
				select {
				case <-deadline.C:
					return
				case <-ticker.C:
				}
			}
		}()
		serverFailure := make(chan error, 1)
		go func() {
			err, ok := <-serveErrors
			if !ok {
				return
			}
			log.Printf("HTTP 服务异常退出: %v", err)
			serverFailure <- err
			desktop.ShowStartupError("AutoNexus 服务错误", fmt.Sprintf("HTTP 服务异常退出：%v", err))
			stop()
		}()

		dCfg := desktop.Config{
			Title: "AutoNexus 自动化总控平台",
			URL:   fmt.Sprintf("http://127.0.0.1:%d", port),
			Theme: cfg.Theme,
			// 托盘每次弹出菜单时实时读取配置：运行时切换局域网开关即刻反映到地址入口显隐
			OnLANURL: func() string {
				c := cfgMgr.Get()
				if !c.LANEnabled {
					return ""
				}
				return getPrimaryLANURL(port)
			},
			Width:  1300,
			Height: 840,
			Debug:  *debugUI,
			OnStopQueue: func() {
				_ = eng.StopQueue()
			},
		}

		if err := desktop.Run(dCfg); err != nil {
			message := fmt.Sprintf("启动桌面原生窗口失败：%v", err)
			desktop.ShowStartupError("AutoNexus 启动失败", message)
			runErr = errors.New(message)
		} else {
			select {
			case err := <-serverFailure:
				runErr = fmt.Errorf("HTTP 服务异常退出: %w", err)
			default:
			}
		}
	}

	fmt.Println("\n正在关闭 AutoNexus 服务...")
	_ = eng.StopQueue()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP 服务停机超时: %v", err)
	}
	fmt.Println("AutoNexus 服务已安全退出。")
	return runErr
}

func getPrimaryLANURL(port int) string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var fallbackIP string
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ip4 := ipNet.IP.To4(); ip4 != nil {
				ipStr := ip4.String()
				if strings.HasPrefix(ipStr, "192.168.") || strings.HasPrefix(ipStr, "10.") {
					return fmt.Sprintf("http://%s:%d", ipStr, port)
				}
				if fallbackIP == "" {
					fallbackIP = ipStr
				}
			}
		}
	}
	if fallbackIP != "" {
		return fmt.Sprintf("http://%s:%d", fallbackIP, port)
	}
	return ""
}

func printBanner(port int, cfg *config.Config) {
	fmt.Println("==================================================")
	fmt.Println("      ⚡ AutoNexus 自动化任务批量调度总控服务      ")
	fmt.Println("==================================================")
	fmt.Printf(" 本地访问:    http://127.0.0.1:%d\n", port)

	if cfg.LANEnabled {
		// 扫描可用局域网 IPv4
		addrs, err := net.InterfaceAddrs()
		if err == nil {
			for _, addr := range addrs {
				if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
					if ipNet.IP.To4() != nil {
						fmt.Printf(" 局域网访问:  http://%s:%d\n", ipNet.IP.String(), port)
					}
				}
			}
		}
		if cfg.AuthToken == "" {
			fmt.Println(" ⚠ 安全提醒:  局域网访问已开放且未设置访问密码，建议在网页「设置」中配置")
		}
		fmt.Println(" 提示: 手机或平板处于同一 Wi-Fi 下直接浏览器打开即可")
	} else {
		fmt.Println(" 局域网访问:  已关闭 (仅本机可打开控制台)")
	}
	fmt.Println("--------------------------------------------------")
	if IsAdmin() {
		fmt.Println(" 运行权限:    [管理员权限 Administrator] (全套自动化静默无弹窗)")
	} else {
		fmt.Println(" 运行权限:    [普通权限] (如子任务需提权可能被 UAC 拦截)")
	}
	fmt.Println(" 控制: 支持桌面独立窗口与右下角托盘常驻，按 Ctrl+C 安全退出")
	fmt.Println("==================================================")
}
