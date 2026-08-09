package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/TideWeolcan/syncmedia/internal/config"
	"github.com/TideWeolcan/syncmedia/internal/manager"
	"github.com/TideWeolcan/syncmedia/pkg/version"
)

func main() {
	// Subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version":
			fmt.Println(version.String())
			return
		case "start":
			runStart(os.Args[2:])
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}
	// Default: start
	runStart(os.Args[1:])
}

func runStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)

	var (
		configPath    string
		serverPort    int
		webPort       int
		noTLS         bool
		tunnelType    string
		frpServer     string
		proxyURL      string
		bindInterface string
		noProxy       bool
	)

	fs.StringVar(&configPath, "config", "", "配置文件路径 (默认: ./config.yaml)")
	fs.IntVar(&serverPort, "port", 0, "syncplay 服务器端口 (默认: 8999，被占自动避让)")
	fs.IntVar(&webPort, "web-port", 0, "WebUI 端口 (默认: 8080，被占自动避让)")
	fs.BoolVar(&noTLS, "no-tls", false, "禁用 TLS (明文模式)")
	fs.StringVar(&tunnelType, "tunnel", "", "隧道类型: bore | frp | none (默认: bore)")
	fs.StringVar(&frpServer, "frp-server", "", "frp 服务器地址")
	fs.StringVar(&proxyURL, "proxy", "", "网络代理 (如 socks5://127.0.0.1:1080)")
	fs.StringVar(&bindInterface, "bind-interface", "", "绑定网卡绕过 VPN (如 wlan0)")
	fs.BoolVar(&noProxy, "no-proxy", false, "绕过系统代理")

	fs.Usage = func() { printUsage() }
	fs.Parse(args)

	// Determine config file path
	if configPath == "" {
		if _, err := os.Stat("config.yaml"); err == nil {
			configPath = "config.yaml"
		}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// Apply CLI overrides
	var tlsFlag *bool
	if noTLS {
		t := false
		tlsFlag = &t
	}
	var noProxyFlag *bool
	if noProxy {
		noProxyFlag = &noProxy
	}
	cfg.ApplyCLIOverrides(serverPort, webPort, tlsFlag, tunnelType, frpServer, proxyURL, bindInterface, noProxyFlag)

	// Setup signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		fmt.Printf("\n收到信号 %v，正在关闭...\n", sig)
		cancel()
	}()

	fmt.Println(version.String())

	mgr := manager.NewManager(cfg, nil)
	if err := mgr.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "运行失败: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	writeUsage(os.Stderr)
}

// writeUsage renders the full usage text to w; the option list must cover
// every flag defined in runStart.
func writeUsage(w io.Writer) {
	fmt.Fprintf(w, `syncmedia — 基于 syncplay 的同步观影服务器 + 隧道穿透

用法:
  syncmedia [start] [选项]
  syncmedia version

子命令:
  start     启动服务 (默认)
  version   显示版本信息

选项:
  --config <path>      配置文件路径 (默认: ./config.yaml)
  --port <port>        syncplay 服务器端口 (默认: 8999，被占自动避让)
  --web-port <port>    WebUI 端口 (默认: 8080，被占自动避让)
  --no-tls             禁用 TLS (明文模式)
  --tunnel <type>      隧道类型: bore | frp | none (默认: bore)
  --frp-server <addr>  frp 服务器地址
  --proxy <url>        网络代理 (如 socks5://127.0.0.1:1080)
  --bind-interface <iface> 绑定网卡绕过 VPN (如 wlan0)
  --no-proxy           绕过系统代理

示例:
  syncmedia                              # 默认启动 (bore 隧道 + TLS)
  syncmedia start --tunnel frp --frp-server example.com
  syncmedia start --no-tls               # 明文模式
  syncmedia version                      # 查看版本
`)
}
