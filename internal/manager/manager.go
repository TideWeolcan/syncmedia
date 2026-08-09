package manager

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	lastRestartErr error // 最近一次 Restart 的错误（nil 表示成功或未重启过）

	// 端口避让结果：启动时解析出的实际监听端口与展示备注
	actualSyncplayPort int
	actualWebPort      int
	syncplayPortNote   string // 实际端口说明（自动选择/被占避让），空=与配置一致
	webPortNote        string

	// epoch 代际计数：每轮 Start 递增。Start 的每步写入（m.srv/tunnelMgr/
	// webSrv/实际端口）前校验代际，过期代静默丢弃自身结果且不再调用 m.Stop()，
	// 防止旧代 Start 在 Stop/Start 轮换后覆盖新一代实例。
	epoch int

	// Restart 合并：restartRunning 表示已有重启循环在跑，期间的新请求只置
	// pendingRestart，由当前循环结束后接管，避免连续触发逐次全量 Stop+Start。
	restartRunning bool
	pendingRestart bool

	dataDir string // 数据目录（端口记忆等状态文件），NewManager 时解析

	cancel    context.CancelFunc // 取消当前 Start 的 context
	restartMu sync.Mutex         // 保证同一时刻仅一个重启循环执行
}

func NewManager(cfg *config.Config, logger *log.Logger) *Manager {
	if logger == nil {
		logger = log.Default()
	}
	m := &Manager{cfg: cfg, logger: logger}
	if dir, err := config.DataDir(); err == nil {
		m.dataDir = dir
	}
	return m
}

// Start launches all components and blocks until ctx is cancelled.
// 代际（epoch）语义：每次调用即新的一代。所有对 m.srv/tunnelMgr/webSrv/
// 实际端口的写入前都校验代际，过期代（被新一轮 Restart 取代）静默丢弃自身
// 结果并提前退出，且不再调用 m.Stop()，避免把新一代实例停掉。
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	m.epoch++
	epoch := m.epoch
	m.startTime = time.Now()
	// 快照当前配置：UpdateSettings 在 m.mu 下原地修改 m.cfg，此处拷贝出
	// 独立副本，本次 Start 生命周期内的读取不受后续设置更新影响。
	cfg := *m.cfg
	m.mu.Unlock()

	// 校验配置（如隧道类型）：覆盖命令行/设置更新的非法值，显式报错而非静默回落
	if err := cfg.Validate(); err != nil {
		return err
	}

	// 让 Stop() 能通过 m.cancel 取消本代 Start 的 ctx（Restart 的 Stop 依赖
	// 此取消在途 Start；直接经 main.go 启动同样受控）。Start 返回时释放。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.mu.Lock()
	m.cancel = cancel
	m.mu.Unlock()

	// 0. 解析实际端口（0=未设定 → 自动选择/被占避让并记忆）
	syncplayPort, webPort, syncplayAvoided, webAvoided := m.resolvePorts(&cfg)

	// 1. 先构造 WebServer（校验绑定/认证配置），避免坏配置下 syncplay 已监听造成泄漏
	var webSrv *WebServer
	if cfg.Web.Enabled {
		ws, err := NewWebServer(cfg.Web, m, m.logger)
		if err != nil {
			return fmt.Errorf("web 配置: %w", err)
		}
		webSrv = ws
	}

	// 2. Prepare TLS (if enabled) — 纯内存生成，不写盘
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

	if err := ctx.Err(); err != nil {
		return err
	}

	// 3. Start syncplay server
	srvConfig := syncplay.ServerConfig{
		Port:     syncplayPort,
		Bind:     cfg.Server.Bind,
		Password: cfg.Server.Password,
		GMConfig: gmConfig,
		Logger:   m.logger,
	}
	srv := syncplay.NewServer(srvConfig)
	if err := srv.Listen(syncplayPort); err != nil {
		return fmt.Errorf("syncplay 监听: %w", err)
	}
	// 注册前校验代际：过期则关闭本监听、不污染新一代
	m.mu.Lock()
	if m.epoch != epoch {
		m.mu.Unlock()
		_ = srv.Close()
		return context.Canceled
	}
	m.srv = srv
	m.actualSyncplayPort = syncplayPort
	m.syncplayPortNote = portNote(cfg.Server.Port, syncplayAvoided, 8999)
	m.mu.Unlock()
	go srv.Serve()
	m.logger.Printf("Syncplay 端口: %d%s", syncplayPort, m.syncplayPortNote)

	// Wait for syncplay to be ready
	if err := waitForPort(localCheckAddr(cfg.Server.Bind, syncplayPort), 5*time.Second); err != nil {
		return fmt.Errorf("syncplay 未就绪: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// 2. Start tunnel
	if cfg.Tunnel.Type != "none" {
		tunnelMgr := tunnel.NewManager(m.logger)
		m.setupTunnels(&cfg, tunnelMgr)

		if err := tunnelMgr.Start(ctx, syncplayPort); err != nil {
			// ctx 被取消（新一轮重启接管）：静默退出，交新一代清理
			if ctx.Err() != nil {
				return context.Canceled
			}
			m.logger.Printf("警告: 所有隧道均失败: %v（服务器仍在本地运行）", err)
		} else {
			m.mu.Lock()
			if m.epoch != epoch {
				m.mu.Unlock()
				_ = tunnelMgr.Stop()
				return context.Canceled
			}
			m.tunnelMgr = tunnelMgr
			m.publicAddr = tunnelMgr.PublicAddr()
			m.tunnelName = tunnelMgr.ActiveTunnelName()
			m.mu.Unlock()
			m.logger.Printf("隧道已激活: %s → %s", m.tunnelName, m.publicAddr)
		}
	}

	// 3. Start web UI (if enabled)
	if cfg.Web.Enabled {
		m.mu.Lock()
		if m.epoch != epoch {
			m.mu.Unlock()
			return context.Canceled
		}
		m.webSrv = webSrv
		m.actualWebPort = webPort
		m.webPortNote = portNote(cfg.Web.Port, webAvoided, 8080)
		m.mu.Unlock()
		go func() {
			if err := webSrv.Start(); err != nil && err != http.ErrServerClosed {
				m.logger.Printf("Web 服务器错误: %v", err)
			}
		}()
		m.logger.Printf("WebUI 启动，端口 %d", webPort)
	}

	// 4. Print summary
	m.printSummary(&cfg, syncplayPort, cfg.Server.Bind, webPort, cfg.Web.Enabled)

	// 5. Block until context cancelled
	<-ctx.Done()
	m.mu.Lock()
	current := m.epoch
	m.mu.Unlock()
	if current != epoch {
		// 过期代：新一代已接管，不得调用 m.Stop()
		return context.Canceled
	}
	m.Stop()
	return nil
}

// portNote 生成端口日志的避让/自动说明。
func portNote(configured int, avoided bool, preferred int) string {
	if avoided {
		if configured > 0 {
			return fmt.Sprintf("（配置端口 %d 被占用，已自动避让）", configured)
		}
		return fmt.Sprintf("（默认 %d 被占用，已自动避让）", preferred)
	}
	if configured == 0 {
		return "（未配置，自动选择）"
	}
	return ""
}

func (m *Manager) setupTunnels(cfg *config.Config, tunnelMgr *tunnel.Manager) {
	proxyURL := cfg.Network.ProxyURL
	bindIface := cfg.Network.BindInterface

	switch cfg.Tunnel.Type {
	case "bore":
		bt := tunnel.NewBoreNativeClient(cfg.Tunnel.Bore.Relay, cfg.Tunnel.Bore.Secret, m.logger, proxyURL, bindIface)
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
	case "none":
		// 不建隧道（配置已在 Start 中分流）
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
// stale values. 锁内仅快照，隧道阻塞方法在锁外调用。
func (m *Manager) Status() Status {
	m.mu.RLock()
	serverPort := m.actualSyncplayPort
	webPort := m.actualWebPort
	serverPortNote := m.syncplayPortNote
	webPortNote := m.webPortNote
	tlsEnabled := m.cfg.Server.TLS
	startTime := m.startTime
	tunnelMgr := m.tunnelMgr
	var restartErr string
	if m.lastRestartErr != nil {
		restartErr = m.lastRestartErr.Error()
	}
	m.mu.RUnlock()

	publicAddr, tunnelName := "", ""
	if tunnelMgr != nil {
		publicAddr = tunnelMgr.PublicAddr()
		tunnelName = tunnelMgr.ActiveTunnelName()
	}
	return Status{
		PublicAddr:      publicAddr,
		TunnelType:      tunnelName,
		ServerPort:      serverPort,
		ServerPortNote:  serverPortNote,
		TLSEnabled:      tlsEnabled,
		WebPort:         webPort,
		WebPortNote:     webPortNote,
		Uptime:          time.Since(startTime).Round(time.Second).String(),
		RestartError:    restartErr,
	}
}

type Status struct {
	PublicAddr     string `json:"publicAddr"`
	TunnelType     string `json:"tunnelType"`
	ServerPort     int    `json:"serverPort"`
	ServerPortNote string `json:"serverPortNote,omitempty"` // 端口说明（自动选择/被占避让）
	TLSEnabled     bool   `json:"tlsEnabled"`
	WebPort        int    `json:"webPort"`
	WebPortNote    string `json:"webPortNote,omitempty"`
	Uptime         string `json:"uptime"`
	RestartError   string `json:"restartError,omitempty"`
}

func (m *Manager) printSummary(cfg *config.Config, syncplayPort int, bind string, webPort int, webEnabled bool) {
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
		host := "127.0.0.1"
		if bind != "" && bind != "0.0.0.0" && bind != "::" {
			host = bind
		}
		m.logger.Printf("  本地地址: %s:%d", host, syncplayPort)
		m.logger.Println("  (隧道未建立，仅本地可用)")
	}
	m.logger.Printf("  TLS: %v", cfg.Server.TLS)
	if webEnabled {
		m.logger.Printf("  WebUI: http://127.0.0.1:%d", webPort)
	}
	m.logger.Println("═══════════════════════════════════════════")
}

// waitForPort waits until a TCP addr is accepting connections.
func waitForPort(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("端口 %s 超时", addr)
}

// localCheckAddr 返回就绪检查用的本地地址：绑定全接口/空时用回环地址探测。
func localCheckAddr(bind string, port int) string {
	switch bind {
	case "", "0.0.0.0":
		return fmt.Sprintf("127.0.0.1:%d", port)
	case "::":
		return fmt.Sprintf("[::1]:%d", port)
	default:
		return net.JoinHostPort(bind, strconv.Itoa(port))
	}
}

// resolvePorts 解析 Syncplay/Web 的实际监听端口（自动避让）并把结果记忆到
// 状态文件（data/ports.json），重启沿用不漂移。同时记录是否走了自动逻辑。
func (m *Manager) resolvePorts(cfg *config.Config) (syncplayPort, webPort int, syncplayAvoided, webAvoided bool) {
	var st config.PortState
	if m.dataDir != "" {
		var err error
		st, err = config.LoadPortState(m.dataDir)
		if err != nil {
			m.logger.Printf("读取端口状态失败（使用默认端口）: %v", err)
			st = config.PortState{}
		}
	}

	syncplayPort, syncplayAvoided = config.ResolvePort(cfg.Server.Port, config.PreferredSyncplayPort, st.SyncplayPort)
	if cfg.Web.Enabled {
		webPort, webAvoided = config.ResolvePort(cfg.Web.Port, config.PreferredWebPort, st.WebPort)
	}

	// 记忆实际端口：解析有效（>0）才写入，避免非法配置（如测试用 -1）污染状态文件
	if m.dataDir != "" && syncplayPort > 0 && (!cfg.Web.Enabled || webPort > 0) {
		newSt := config.PortState{SyncplayPort: syncplayPort}
		if cfg.Web.Enabled {
			newSt.WebPort = webPort
		}
		if err := config.SavePortState(m.dataDir, newSt); err != nil {
			m.logger.Printf("保存端口状态失败: %v", err)
		}
	}
	return syncplayPort, webPort, syncplayAvoided, webAvoided
}

// redactProxyURL 返回适合展示/日志的代理地址：userinfo 中的密码与 query 中的
// 敏感参数（token/password/secret 等）固定脱敏为 xxxxx。含 "@" 的字符串仅当
// url.Parse 成功且捕获到 userinfo 才走掩码路径，其余（解析失败、无 scheme 时
// userinfo 未被识别等）一律整体遮蔽，宁可全遮不可漏；不含 "@" 且解析失败的
// 原样返回。
func redactProxyURL(s string) string {
	if s == "" {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil {
		if strings.Contains(s, "@") {
			return "<redacted>"
		}
		return s
	}
	if u.User == nil && strings.Contains(s, "@") {
		// 含 "@" 但 url.Parse 未捕获 userinfo（如无 scheme 的 "user:pw@host"，
		// "user:" 被当作 scheme、剩余进 opaque）：无法判断是否含密码，宁可全遮
		return "<redacted>"
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			u.User = url.UserPassword(u.User.Username(), "xxxxx")
		}
	}
	redactSensitiveQuery(u)
	return u.String()
}

// sensitiveQueryKeys 查询串中的敏感参数名（大小写不敏感），脱敏时一并掩码，
// 防止凭据经 query 泄漏进日志/status。
var sensitiveQueryKeys = map[string]bool{
	"token":        true,
	"password":     true,
	"passwd":       true,
	"pwd":          true,
	"secret":       true,
	"key":          true,
	"api_key":      true,
	"apikey":       true,
	"access_token": true,
	"auth":         true,
	"authkey":      true,
	"session":      true,
}

// redactSensitiveQuery 将 u 的 query 中敏感参数的值替换为 xxxxx。
func redactSensitiveQuery(u *url.URL) {
	q := u.Query()
	changed := false
	for k := range q {
		if sensitiveQueryKeys[strings.ToLower(k)] {
			q.Set(k, "xxxxx")
			changed = true
		}
	}
	if changed {
		u.RawQuery = q.Encode()
	}
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

// Restart 请求重启服务。并发/连续调用会被合并：同一时刻仅一个重启循环在跑，
// 期间的新请求只置 pending 标志，由当前循环结束后接管，避免连续触发逐次
// 全量 Stop+Start。
func (m *Manager) Restart() {
	m.mu.Lock()
	if m.restartRunning {
		m.pendingRestart = true
		m.mu.Unlock()
		return
	}
	m.restartRunning = true
	m.mu.Unlock()
	go m.restartWorker()
}

// restartWorker 串行执行重启循环，处理合并后的 pending 请求。
func (m *Manager) restartWorker() {
	m.restartMu.Lock()
	defer m.restartMu.Unlock()

	for {
		m.mu.Lock()
		m.pendingRestart = false
		m.mu.Unlock()

		m.doRestart()

		// 退出检查与 restartRunning 复位在同一锁内，避免竞态丢请求：
		// 若期间又有新请求（pending=true），继续下一轮。
		m.mu.Lock()
		if !m.pendingRestart {
			m.restartRunning = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		m.logger.Printf("重启请求已合并，执行新一轮重启")
	}
}

// doRestart 执行一轮完整重启：停旧 → 换代 → 启新。
func (m *Manager) doRestart() {
	m.logger.Printf("正在重启...")

	// 代际递增必须先于 Stop：Stop 会取消上一代 Start 的 ctx，旧代醒来后
	// 依据 epoch 判定自己是旧代，不再调用 m.Stop()（否则会把新一代停掉）。
	m.mu.Lock()
	m.epoch++
	m.mu.Unlock()

	m.Stop()

	// 快照配置供本轮回启用（拷贝，独立于后续 UpdateSettings 的原地修改）
	m.mu.Lock()
	cfg := *m.cfg
	m.mu.Unlock()

	// NoProxy 开启时清除可能继承的代理环境变量。注意：拨号层（bore/frp
	// 原生实现）均通过构造参数内存传递 proxyURL，不依赖环境变量，因此
	// 不再把含密码的代理 URL 写入 ALL_PROXY（/proc/<pid>/environ 及子进程
	// 可见，属凭据泄漏面；见 M7 决策记录）。
	if cfg.Network.NoProxy {
		os.Unsetenv("HTTP_PROXY")
		os.Unsetenv("HTTPS_PROXY")
		os.Unsetenv("ALL_PROXY")
		os.Unsetenv("http_proxy")
		os.Unsetenv("https_proxy")
		os.Unsetenv("all_proxy")
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
		if err != context.Canceled {
			m.lastRestartErr = err
		}
		m.mu.Unlock()
		if err != nil && err != context.Canceled {
			m.logger.Printf("重启失败: %v", err)
		}
	}()
}
