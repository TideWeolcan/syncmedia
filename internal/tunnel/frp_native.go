package tunnel

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/fatedier/frp/client"
	"github.com/fatedier/frp/pkg/config/source"
	v1 "github.com/fatedier/frp/pkg/config/v1"
)

// FRPNativeClient 用 frp Go 库原生实现隧道，不依赖外部 frpc 二进制。
// 就绪信号来自轮询 frp 客户端上报的代理状态（Phase=="running" 才算
// 真正就绪），并持续监控健康度：frp 库自身负责断线重连，这里只观测
// 并同步更新状态与公共地址（非 running 期间地址清空）。
type FRPNativeClient struct {
	server     string // frp 服务器地址
	serverPort int    // frp 服务器端口
	token      string // 可选认证 token
	localPort  int    // 本地服务端口
	remotePort int    // 远程端口（0=使用本地端口）
	proxyName  string // 代理名（构造时生成并固定，供状态查询使用）
	logger     *log.Logger

	// 监控参数（unexported，测试可注入小值）
	pollInterval  time.Duration // 状态轮询间隔，默认 200ms
	degradedAfter time.Duration // 非 running 持续该时长后进入 degraded，默认 15s

	// statusFn 返回代理状态（phase、frps 回报的远程地址、是否查询到）。
	// 构造时为 nil；run() 建好 frp 服务后仅在仍为 nil 时安装默认实现
	//（包住 svc.StatusExporter().GetProxyStatus），测试注入的不被覆盖。
	statusFn func() (phase string, remoteAddr string, ok bool)

	mu         sync.Mutex
	state      State
	publicAddr string // 当前公共地址 "server:port"，仅 ready 时非空
	svc        *client.Service
	closed     bool
	firstErr   error         // 启动阶段（尚未 ready 过）失败的错误
	stateCh    chan struct{} // 每次状态变更时 close 并更换，用于唤醒 WaitReady

	done chan struct{}
}

func NewFRPNativeClient(server string, serverPort int, token string, remotePort int, logger *log.Logger) *FRPNativeClient {
	if serverPort == 0 {
		serverPort = 7000
	}
	if logger == nil {
		logger = log.New(os.Stderr, "[frp] ", log.LstdFlags)
	}
	return &FRPNativeClient{
		server:        server,
		serverPort:    serverPort,
		token:         token,
		remotePort:    remotePort,
		proxyName:     fmt.Sprintf("syncmedia-%d", time.Now().Unix()%100000),
		logger:        logger,
		pollInterval:  200 * time.Millisecond,
		degradedAfter: 15 * time.Second,
		state:         StateStarting,
		stateCh:       make(chan struct{}),
		done:          make(chan struct{}),
	}
}

func (f *FRPNativeClient) Name() string { return "frp" }

func (f *FRPNativeClient) Start(localPort int) error {
	f.localPort = localPort
	go f.run()
	return nil
}

// State 返回隧道当前健康状态。
func (f *FRPNativeClient) State() State {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state
}

// PublicAddr 返回当前实时公共地址；仅 ready 状态时非空。
func (f *FRPNativeClient) PublicAddr() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateReady {
		return ""
	}
	return f.publicAddr
}

// setStateLocked 更新状态与地址，并唤醒所有 WaitReady 等待者。
// 调用方必须持有 f.mu。
func (f *FRPNativeClient) setStateLocked(s State, addr string) {
	f.state = s
	f.publicAddr = addr
	close(f.stateCh)
	f.stateCh = make(chan struct{})
}

// WaitReady 等待隧道进入 ready 状态并返回当前公共地址。可重复调用：
// 已就绪立即返回；已停止或启动阶段失败返回错误；ctx 到期返回 ctx.Err()。
func (f *FRPNativeClient) WaitReady(ctx context.Context) (string, error) {
	for {
		f.mu.Lock()
		st, addr, ch, firstErr := f.state, f.publicAddr, f.stateCh, f.firstErr
		f.mu.Unlock()

		switch st {
		case StateReady:
			return addr, nil
		case StateStopped:
			return "", fmt.Errorf("frp 已停止")
		}
		if firstErr != nil {
			return "", firstErr
		}

		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		case <-f.done:
			return "", fmt.Errorf("frp 已停止")
		}
	}
}

func (f *FRPNativeClient) Stop() error {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	svc := f.svc
	f.setStateLocked(StateStopped, "")
	f.mu.Unlock()

	close(f.done)
	if svc != nil {
		svc.Close()
	}
	return nil
}

// failFirst 记录启动阶段的失败并置 degraded（不重试，Manager 依此 fallback）。
func (f *FRPNativeClient) failFirst(err error) {
	f.logger.Printf("错误: %v", err)
	f.mu.Lock()
	f.firstErr = err
	if !f.closed {
		f.setStateLocked(StateDegraded, "")
	}
	f.mu.Unlock()
}

// deriveAddr 从 frps 回报的 RemoteAddr（可能形如 ":P"、"0.0.0.0:P"、
// "[::]:P"）推导对外公共地址；解析不出端口时回退 server:remotePort 拼接。
func (f *FRPNativeClient) deriveAddr(remoteAddr string) string {
	if _, portStr, err := net.SplitHostPort(remoteAddr); err == nil {
		if p, err := strconv.Atoi(portStr); err == nil && p > 0 {
			return fmt.Sprintf("%s:%d", f.server, p)
		}
	}
	rp := f.remotePort
	if rp == 0 {
		rp = f.localPort
	}
	return fmt.Sprintf("%s:%d", f.server, rp)
}

func (f *FRPNativeClient) run() {
	// 1. 客户端通用配置
	common := &v1.ClientCommonConfig{}
	common.ServerAddr = f.server
	common.ServerPort = f.serverPort
	common.Auth.Token = f.token
	common.Auth.Method = "token"
	// 登录失败后重试而不是退出
	loginFailExit := false
	common.LoginFailExit = &loginFailExit
	// 传输层加密：frpc↔frps 之间 TLS 加密
	tlsEnable := true
	common.Transport.TLS.Enable = &tlsEnable

	// 2. TCP 代理配置
	remotePort := f.remotePort
	if remotePort == 0 {
		remotePort = f.localPort // 使用和本地相同的端口（用户需确保 frps 允许）
	}

	proxyCfg := &v1.TCPProxyConfig{}
	proxyCfg.Name = f.proxyName
	proxyCfg.Type = "tcp"
	proxyCfg.LocalIP = "127.0.0.1"
	proxyCfg.LocalPort = f.localPort
	proxyCfg.RemotePort = remotePort
	// 代理数据应用层加密 + 压缩
	proxyCfg.Transport.UseEncryption = true
	proxyCfg.Transport.UseCompression = true

	f.logger.Printf("连接到 frp 服务器 %s:%d (远程端口 %d)", f.server, f.serverPort, remotePort)

	// 3. 创建配置源
	configSource := source.NewConfigSource()
	if err := configSource.ReplaceAll([]v1.ProxyConfigurer{proxyCfg}, nil); err != nil {
		f.failFirst(fmt.Errorf("配置代理失败: %w", err))
		return
	}
	aggregator := source.NewAggregator(configSource)

	// 4. 创建 frp 服务
	svc, err := client.NewService(client.ServiceOptions{
		Common:                 common,
		ConfigSourceAggregator: aggregator,
	})
	if err != nil {
		f.failFirst(fmt.Errorf("创建 frp 服务失败: %w", err))
		return
	}

	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		svc.Close()
		return
	}
	f.svc = svc
	// 默认状态查询实现（测试注入的不被覆盖）
	if f.statusFn == nil {
		exporter := svc.StatusExporter()
		name := f.proxyName
		f.statusFn = func() (string, string, bool) {
			ws, ok := exporter.GetProxyStatus(name)
			if !ok {
				return "", "", false
			}
			return ws.Phase, ws.RemoteAddr, true
		}
	}
	f.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	// run 退出（含 svc.Run 自行出错返回）即取消 ctx，回收下面的辅助 goroutine
	defer cancel()
	go func() {
		select {
		case <-f.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	// 5. 启动状态监控循环（驱动 starting→ready→reconnecting→degraded）
	go f.monitor()

	// 6. 阻塞运行；frp 库内部自行处理断线重连
	if err := svc.Run(ctx); err != nil {
		f.mu.Lock()
		if !f.closed && f.state == StateStarting {
			f.firstErr = fmt.Errorf("frp 服务错误: %w", err)
			f.setStateLocked(StateDegraded, "")
		}
		f.mu.Unlock()
		f.logger.Printf("frp 服务退出: %v", err)
	}
}

// monitor 周期轮询 statusFn 驱动状态机：starting 阶段出现 running →
// ready（地址从 RemoteAddr 推导）；ready 后掉出 running → reconnecting
//（清地址，等待 frp 库自行恢复）；非 running 持续 ≥ degradedAfter →
// degraded（继续观测）；恢复 running → ready（地址可能变化，重新推导）。
// Stop 后退出。测试可注入 statusFn 后独立启动本方法（不经 run()）。
func (f *FRPNativeClient) monitor() {
	ticker := time.NewTicker(f.pollInterval)
	defer ticker.Stop()

	var notRunningSince time.Time

	for {
		select {
		case <-f.done:
			return
		case <-ticker.C:
		}

		f.mu.Lock()
		fn := f.statusFn
		f.mu.Unlock()
		if fn == nil {
			continue
		}

		phase, remoteAddr, ok := fn()
		running := ok && phase == "running"

		f.mu.Lock()
		if f.state == StateStopped {
			f.mu.Unlock()
			return
		}
		if running {
			notRunningSince = time.Time{}
			addr := f.deriveAddr(remoteAddr)
			if f.state != StateReady || f.publicAddr != addr {
				f.setStateLocked(StateReady, addr)
				f.logger.Printf("frp 隧道就绪: %s", addr)
			}
		} else {
			if notRunningSince.IsZero() {
				notRunningSince = time.Now()
			}
			switch f.state {
			case StateReady:
				f.logger.Printf("frp 代理掉出 running (phase=%q)，等待恢复", phase)
				f.setStateLocked(StateReconnecting, "")
			case StateReconnecting:
				if time.Since(notRunningSince) >= f.degradedAfter {
					f.logger.Printf("frp 代理持续未恢复，进入 degraded")
					f.setStateLocked(StateDegraded, "")
				}
			}
			// starting/degraded 保持现状，继续观测
		}
		f.mu.Unlock()
	}
}
