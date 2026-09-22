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
	"syscall"
	"time"

	"autonexus/internal/config"
	"autonexus/internal/engine"
	"autonexus/internal/executor"
	"autonexus/internal/server"
)

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件存储路径")
	portFlag := flag.Int("port", 0, "自定义服务端口 (默认使用配置文件设置)")
	noElevate := flag.Bool("no-elevate", false, "禁止自动尝试请求 Windows 管理员特权")
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

	// 6. 优雅停机与信号监听
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	<-quit

	fmt.Println("\n正在关闭 AutoNexus 服务...")
	_ = eng.StopQueue()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("HTTP 服务停机超时: %v", err)
	}
	fmt.Println("AutoNexus 服务已安全退出。")
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
	fmt.Println(" 控制: 按 Ctrl+C 触发安全退出与进程清理")
	fmt.Println("==================================================")
}
