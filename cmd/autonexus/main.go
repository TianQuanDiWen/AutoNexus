package main

import (
	"context"
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
			return
		}
	}

	// 1. 初始化配置
	cfgMgr, err := config.NewManager(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置文件失败: %v", err)
	}

	cfg := cfgMgr.Get()
	port := cfg.Port
	if *portFlag > 0 {
		port = *portFlag
	}

	// 2. 初始化核心组件
	broadcaster := executor.NewBroadcaster(1000)
	runner := executor.NewRunner(broadcaster)
	eng := engine.NewEngine(cfgMgr, runner, broadcaster)
	srv := server.NewServer(cfgMgr, eng, broadcaster)

	// 3. 构建 HTTP 监听
	listenAddr := fmt.Sprintf("%s:%d", cfg.Host, port)
	httpServer := &http.Server{
		Addr:    listenAddr,
		Handler: srv.Handler(),
	}

	// 4. 打印欢迎面板与局域网 IP 提示
	printBanner(port)

	// 5. 启动 HTTP 服务
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	// 6. 优雅停机信号监听
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	lanURL := getPrimaryLANURL(port)

	if *noUI {
		// 纯后台模式：阻塞等待系统信号
		<-ctx.Done()
	} else {
		// 桌面 UI 模式：由主线程承载 WebView2 窗口与托盘消息循环
		go func() {
			<-ctx.Done()
			desktop.TerminateActiveApp()
		}()

		dCfg := desktop.Config{
			Title:  "AutoNexus 自动化总控平台",
			URL:    fmt.Sprintf("http://127.0.0.1:%d", port),
			LANURL: lanURL,
			Width:  1300,
			Height: 840,
			Debug:  *debugUI,
			OnStopQueue: func() {
				_ = eng.StopQueue()
			},
		}

		if err := desktop.Run(dCfg); err != nil {
			fmt.Printf("启动桌面原生窗口失败 (%v)，自动降级为无头后台服务...\n", err)
			<-ctx.Done()
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

func printBanner(port int) {
	fmt.Println("==================================================")
	fmt.Println("      ⚡ AutoNexus 自动化任务批量调度总控服务      ")
	fmt.Println("==================================================")
	fmt.Printf(" 本地访问:    http://127.0.0.1:%d\n", port)

	// 扫描可用局域网 IPv4
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
				if ipNet.IP.To4() != nil {
					fmt.Printf(" 局域网手机:  http://%s:%d\n", ipNet.IP.String(), port)
				}
			}
		}
	}
	fmt.Println("--------------------------------------------------")
	if IsAdmin() {
		fmt.Println(" 运行权限:    [管理员权限 Administrator] (全套自动化静默无弹窗)")
	} else {
		fmt.Println(" 运行权限:    [普通权限] (如子任务需提权可能被 UAC 拦截)")
	}
	fmt.Println(" 提示: 手机或平板处于同一 Wi-Fi 下直接浏览器打开即可")
	fmt.Println(" 控制: 支持桌面独立窗口与右下角托盘常驻，按 Ctrl+C 安全退出")
	fmt.Println("==================================================")
}
