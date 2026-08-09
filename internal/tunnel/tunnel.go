package tunnel

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

// State 表示隧道的健康状态。
type State string

const (
	StateStarting     State = "starting"     // 已启动，尚未完成首次就绪
	StateReady        State = "ready"        // 隧道就绪，公网地址有效
	StateReconnecting State = "reconnecting" // 连接丢失，正在重连/等待恢复
	StateDegraded     State = "degraded"     // 持续无法恢复，仍在低频重试/观测
	StateStopped      State = "stopped"      // 已停止（终态）
)

// Tunnel 抽象反向隧道，不同实现（bore、frp 等）统一此接口。
type Tunnel interface {
	// Start 启动隧道，绑定到指定的本地端口。
	Start(localPort int) error
	// WaitReady 阻塞等待隧道就绪，返回公共访问地址。可重复调用：
	// 已就绪立即返回当前地址；已停止或首次连接失败返回错误；ctx 到期返回 ctx.Err()。
	WaitReady(ctx context.Context) (publicAddr string, err error)
	// Stop 停止隧道。
	Stop() error
	// Name 返回隧道名称（如 "bore"、"frp"）。
	Name() string
	// State 返回隧道当前健康状态。
	State() State
	// PublicAddr 返回当前实时公共地址；仅 State() == StateReady 时非空。
	PublicAddr() string
}

// Manager 管理多个隧道，按注册顺序依次尝试，第一个成功的成为活跃隧道。
type Manager struct {
	mu      sync.Mutex
	tunnels []Tunnel
	active  Tunnel
	logger  *log.Logger
}

// NewManager 创建隧道管理器。logger 为 nil 时使用默认 logger。
func NewManager(logger *log.Logger) *Manager {
	if logger == nil {
		logger = log.New(os.Stderr, "[tunnel] ", log.LstdFlags)
	}
	return &Manager{logger: logger}
}

// AddTunnel 添加一个隧道到管理器（按添加顺序确定优先级）。
func (m *Manager) AddTunnel(t Tunnel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tunnels = append(m.tunnels, t)
}

// Start 依次尝试所有注册的隧道，第一个 Start + WaitReady 都成功的成为活跃隧道。
// ctx 约束整体等待：ctx 取消（如重启换代）时立即中止等待，不阻塞在 WaitReady 上。
func (m *Manager) Start(ctx context.Context, localPort int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.tunnels) == 0 {
		return fmt.Errorf("没有注册任何隧道")
	}

	var errs []string
	for _, t := range m.tunnels {
		m.logger.Printf("尝试启动隧道: %s", t.Name())

		if err := t.Start(localPort); err != nil {
			errs = append(errs, fmt.Sprintf("%s 启动失败: %v", t.Name(), err))
			m.logger.Printf("%s 启动失败: %v", t.Name(), err)
			continue
		}

		// 等待隧道就绪（ctx 未取消时最长 30 秒）
		waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		addr, err := t.WaitReady(waitCtx)
		cancel()
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s 就绪失败: %v", t.Name(), err))
			m.logger.Printf("%s 就绪失败: %v", t.Name(), err)
			_ = t.Stop()
			continue
		}

		m.active = t
		m.logger.Printf("隧道 %s 就绪，公共地址: %s", t.Name(), addr)
		return nil
	}

	return fmt.Errorf("所有隧道均启动失败: %s", errs)
}

// PublicAddr 返回当前活跃隧道的实时公共地址；
// 无活跃隧道，或活跃隧道未就绪（重连中/降级/已停止）时返回空字符串。
func (m *Manager) PublicAddr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return ""
	}
	if m.active.State() != StateReady {
		return ""
	}
	return m.active.PublicAddr()
}

// ActiveTunnelName 返回当前活跃隧道的名称，无活跃隧道时返回空字符串。
func (m *Manager) ActiveTunnelName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return ""
	}
	return m.active.Name()
}

// Stop 停止当前活跃隧道。
func (m *Manager) Stop() error {
	m.mu.Lock()
	active := m.active
	m.active = nil
	m.mu.Unlock()

	if active == nil {
		return nil
	}
	return active.Stop()
}
