package manager

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/TideWeolcan/syncmedia/internal/config"
	"github.com/TideWeolcan/syncmedia/internal/syncplay"
	"github.com/TideWeolcan/syncmedia/internal/tunnel"
	"github.com/tjfoc/gmsm/gmtls"
)

// Manager orchestrates the syncplay server, tunnel, and web UI.
type Manager struct {
	cfg       *config.Config
	srv       *syncplay.Server
	tunnelMgr *tunnel.Manager
	webSrv    *WebServer
	logger    *log.Logger

	mu             sync.RWMutex
	publicAddr     string // Start 时的一次性快照，仅供启动日志/printSummary；Status 走 tunnelMgr 实时值
	tunnelName     string // 同上
	startTime      time.Time
	lastRestartErr error  // 最近一次 Restart 的错误（nil 表示成功或未重启过）

	cancel    context.CancelFunc // 取消当前 Start 的 context
	restartMu sync.Mutex         // 保证同一时刻仅一个 Restart 执行
}

func NewManager(cfg *config.Config, logger *log.Logger) *Manager {
	if logger == nil {
		logger = log.Default()
	}
	return &Manager{cfg: cfg, logger: logger}
}

// Start launches all components and blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.startTime = time.Now()
	// 快照当前配置：UpdateSettings 在 m.mu 下原地修改 m.cfg，此处拷贝出
	// 独立副本，本次 Start 生命周期内的读取不受后续设置更新影响。
	cfg := *m.cfg
	m.mu.Unlock()

	// 0. 先构造 WebServer（校验绑定/认证配置），避免坏配置下 syncplay 已监听造成泄漏
	var webSrv *WebServer
	if cfg.Web.Enabled {
		ws, err := NewWebServer(cfg.Web, m, m.logger)
		if err != nil {
			return fmt.Errorf("web 配置: %w", err)
		}
		webSrv = ws
	}

	// 1. Prepare TLS (if enabled) — 纯内存生成，不写盘
	var gmConfig *gmtls.Config
	if cfg.Server.TLS {
		certs, err := syncplay.GenerateCertSet()
		if err != nil {
			return fmt.Errorf("TLS 证书: %w", err)
		}
		gmConfig, err = syncplay.NewAutoSwitchConfig(certs)
		if err != nil {
			return fmt.Errorf("TLS 配置: %w", err)
		}
		m.logger.Printf("TLS/国密 证书已生成（内存）")
	}

	// 2. Start syncplay server
	srvConfig := syncplay.ServerConfig{
		Port:     cfg.Server.Port,
		Password: cfg.Server.Password,
		GMConfig: gmConfig,
		Logger:   m.logger,
	}
	srv := syncplay.NewServer(srvConfig)
	if err := srv.Listen(cfg.Server.Port); err != nil {
		return fmt.Errorf("syncplay 监听: %w", err)
	}
	m.mu.Lock()
	m.srv = srv
	m.mu.Unlock()
	go srv.Serve()
	m.logger.Printf("syncplay 服务器启动，端口 %d", cfg.Server.Port)

	// Wait for syncplay to be ready
	if err := waitForPort(cfg.Server.Port, 5*time.Second); err != nil {
		return fmt.Errorf("syncplay 未就绪: %w", err)
	}

	// 3. Start tunnel
	if cfg.Tunnel.Type != "none" {
		tunnelMgr := tunnel.NewManager(m.logger)
		m.setupTunnels(&cfg, tunnelMgr)

		if err := tunnelMgr.Start(cfg.Server.Port); err != nil {
			m.logger.Printf("警告: 所有隧道均失败: %v（服务器仍在本地运行）", err)
		} else {
			m.mu.Lock()
			m.tunnelMgr = tunnelMgr
			m.publicAddr = tunnelMgr.PublicAddr()
			m.tunnelName = tunnelMgr.ActiveTunnelName()
			m.mu.Unlock()
			m.logger.Printf("隧道已激活: %s → %s", m.tunnelName, m.publicAddr)
		}
	}

	// 4. Start web UI (if enabled)
	if cfg.Web.Enabled {
		m.mu.Lock()
		m.webSrv = webSrv
		m.mu.Unlock()
		go func() {
			if err := webSrv.Start(); err != nil {
				m.logger.Printf("Web 服务器错误: %v", err)
			}
		}()
		m.logger.Printf("WebUI 启动，端口 %d", cfg.Web.Port)
	}

	// 5. Print summary
	m.printSummary(&cfg)

	// 6. Block until context cancelled
	<-ctx.Done()
	m.Stop()
	return nil
}

func (m *Manager) setupTunnels(cfg *config.Config, tunnelMgr *tunnel.Manager) {
	proxyURL := cfg.Network.ProxyURL
	bindIface := cfg.Network.BindInterface

	switch cfg.Tunnel.Type {
	case "bore":
		bt := tunnel.NewBoreNativeClient(cfg.Tunnel.Bore.Relay, "", m.logger, proxyURL, bindIface)
		tunnelMgr.AddTunnel(bt)
	case "frp":
		ft := tunnel.NewFRPNativeClient(
			cfg.Tunnel.FRP.Server,
			cfg.Tunnel.FRP.Port,
			cfg.Tunnel.FRP.Token,
			cfg.Tunnel.FRP.RemotePort,
			m.logger,
		)
		tunnelMgr.AddTunnel(ft)
	default:
		bt := tunnel.NewBoreNativeClient(cfg.Tunnel.Bore.Relay, "", m.logger, proxyURL, bindIface)
		tunnelMgr.AddTunnel(bt)
	}
}

func (m *Manager) Stop() {
	m.logger.Printf("正在关闭...")
	// 取消 Restart 创建的 context（如果有）
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	tunnelMgr := m.tunnelMgr
	srv := m.srv
	webSrv := m.webSrv
	m.mu.Unlock()
	if tunnelMgr != nil {
		_ = tunnelMgr.Stop()
	}
	if srv != nil {
		_ = srv.Close()
	}
	if webSrv != nil {
		webSrv.Stop()
	}
	m.logger.Printf("已停止")
}

// Status returns the current status for the web UI. PublicAddr/TunnelType
// are read live from the tunnel manager (nil manager or no ready tunnel →
// empty), never from the Start-time snapshot, so Stop/Restart cannot leak
// stale values.
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	publicAddr, tunnelName := "", ""
	if m.tunnelMgr != nil {
		publicAddr = m.tunnelMgr.PublicAddr()
		tunnelName = m.tunnelMgr.ActiveTunnelName()
	}
	var restartErr string
	if m.lastRestartErr != nil {
		restartErr = m.lastRestartErr.Error()
	}
	return Status{
		PublicAddr:   publicAddr,
		TunnelType:  tunnelName,
		ServerPort:  m.cfg.Server.Port,
		TLSEnabled:  m.cfg.Server.TLS,
		WebPort:     m.cfg.Web.Port,
		Uptime:      time.Since(m.startTime).Round(time.Second).String(),
		RestartError: restartErr,
	}
}

type Status struct {
	PublicAddr   string `json:"publicAddr"`
	TunnelType   string `json:"tunnelType"`
	ServerPort   int    `json:"serverPort"`
	TLSEnabled   bool   `json:"tlsEnabled"`
	WebPort      int    `json:"webPort"`
	Uptime       string `json:"uptime"`
	RestartError string `json:"restartError,omitempty"`
}

func (m *Manager) printSummary(cfg *config.Config) {
	m.logger.Println("═══════════════════════════════════════════")
	m.logger.Println("  SyncMedia 已启动")
	m.logger.Println("═══════════════════════════════════════════")
	m.mu.RLock()
	addr := m.publicAddr
	m.mu.RUnlock()
	if addr != "" {
		m.logger.Printf("  公共地址: %s", addr)
		m.logger.Printf("  朋友在 Kazumi 中填入: %s", addr)
	} else {
		m.logger.Printf("  本地地址: 127.0.0.1:%d", cfg.Server.Port)
		m.logger.Println("  (隧道未建立，仅本地可用)")
	}
	m.logger.Printf("  TLS: %v", cfg.Server.TLS)
	if cfg.Web.Enabled {
		m.logger.Printf("  WebUI: http://127.0.0.1:%d", cfg.Web.Port)
	}
	m.logger.Println("═══════════════════════════════════════════")
}

// waitForPort waits until a TCP port is accepting connections.
func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("端口 %d 超时", port)
}

// redactProxyURL 返回适合展示/日志的代理地址：userinfo 中的密码固定脱敏为
// xxxxx。含 "@" 的字符串仅当 url.Parse 成功且捕获到 userinfo 才走掩码路径，
// 其余（解析失败、无 scheme 时 userinfo 未被识别等）一律整体遮蔽，
// 宁可全遮不可漏；不含 "@" 的原样返回。
func redactProxyURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		if strings.Contains(s, "@") {
			return "<redacted>"
		}
		return s
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return s
	}
	u.User = url.UserPassword(u.User.Username(), "xxxxx")
	return u.String()
}

// GetSettings 返回当前可配置项（代理密码脱敏）。
func (m *Manager) GetSettings() SettingsResponse {
	m.mu.RLock()
	cfg := *m.cfg
	m.mu.RUnlock()
	return SettingsResponse{
		TunnelType:    cfg.Tunnel.Type,
		ProxyURL:      redactProxyURL(cfg.Network.ProxyURL),
		BindInterface: cfg.Network.BindInterface,
		NoProxy:       cfg.Network.NoProxy,
		TLSEnabled:    cfg.Server.TLS,
	}
}

// UpdateSettings 更新配置并触发重启。m.cfg 始终是 NewManager 传入的同一
// 对象，修改在 m.mu 下进行；Start/Restart 用锁内拷贝的快照，不受影响。
func (m *Manager) UpdateSettings(req SettingsRequest) {
	m.mu.Lock()
	if req.TunnelType != "" {
		m.cfg.Tunnel.Type = req.TunnelType
	}
	// UI 回存 GetSettings 返回的脱敏串时视为未修改，保留真实值；
	// 其余情况照常赋值（空串仍表示清空）。
	cur := m.cfg.Network.ProxyURL
	if req.ProxyURL == "" || cur == "" || req.ProxyURL != redactProxyURL(cur) {
		m.cfg.Network.ProxyURL = req.ProxyURL
	}
	m.cfg.Network.BindInterface = req.BindInterface
	if req.NoProxy != nil {
		m.cfg.Network.NoProxy = *req.NoProxy
	}
	m.logger.Printf("设置已更新: tunnel=%s proxy=%s iface=%s noProxy=%v",
		m.cfg.Tunnel.Type, redactProxyURL(m.cfg.Network.ProxyURL), m.cfg.Network.BindInterface, m.cfg.Network.NoProxy)
	m.mu.Unlock()
	go m.Restart()
}

// Restart 重启所有服务。并发调用时仅一个执行，其余等待后返回。
func (m *Manager) Restart() {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()

	m.logger.Printf("正在重启...")
	m.Stop()

	// 快照配置供本轮回启用（拷贝，独立于后续 UpdateSettings 的原地修改）
	m.mu.Lock()
	cfg := *m.cfg
	m.mu.Unlock()

	// 根据 NoProxy 配置管理代理环境变量
	if cfg.Network.NoProxy {
		os.Unsetenv("HTTP_PROXY")
		os.Unsetenv("HTTPS_PROXY")
		os.Unsetenv("ALL_PROXY")
		os.Unsetenv("http_proxy")
		os.Unsetenv("https_proxy")
		os.Unsetenv("all_proxy")
	} else if cfg.Network.ProxyURL != "" {
		// NoProxy 未启用且配置了代理：确保环境变量能被 net/http 的 ProxyFromEnvironment 读取
		os.Setenv("ALL_PROXY", cfg.Network.ProxyURL)
		os.Setenv("all_proxy", cfg.Network.ProxyURL)
	}

	m.mu.Lock()
	m.srv = nil
	m.tunnelMgr = nil
	m.webSrv = nil
	m.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()
	go func() {
		err := m.Start(ctx)
		m.mu.Lock()
		m.lastRestartErr = err
		m.mu.Unlock()
		if err != nil {
			m.logger.Printf("重启失败: %v", err)
		}
	}()
}
