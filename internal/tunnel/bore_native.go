package tunnel

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

const (
	boreControlPort        = 7835
	boreTimeout            = 3 * time.Second
	boreControlReadTimeout = 30 * time.Second // 控制连接读超时：静默丢包时触发重连
)

// BoreNativeClient 用 Go 原生实现 bore 协议，不依赖外部二进制。
// 就绪后持续监控控制连接：断线自动按有界指数退避重连，并同步更新
// 状态与公共地址（断线期间地址清空，不暴露过期地址）。
type BoreNativeClient struct {
	relay         string // 中继地址，"host" 或 "host:port"（无端口时用默认控制端口），默认 "bore.pub"
	localPort     int    // 本地服务端口
	secret        string // 可选共享密钥
	proxyURL      string // 代理地址（如 socks5://127.0.0.1:1080）
	bindInterface string // 绑定网卡绕过 VPN（如 wlan0）
	logger        *log.Logger

	// 重连退避参数（unexported，测试可注入小值）
	backoffBase       time.Duration // 首次重连前的等待，默认 500ms，之后每次 ×2
	backoffMax        time.Duration // 退避上限，默认 8s
	degradedThreshold int           // 连续重连失败达该次数进入 degraded，默认 5

	mu         sync.Mutex
	state      State
	publicAddr string        // 当前公共地址 "host:port"，仅 ready 时非空
	publicPort int           // 分配的公共端口
	ctrlConn   net.Conn      // 当前控制连接
	closed     bool
	firstErr   error         // 首次连接（starting 阶段）失败的错误
	stateCh    chan struct{} // 每次状态变更时 close 并更换，用于唤醒 WaitReady

	done chan struct{}
}

func NewBoreNativeClient(relay, secret string, logger *log.Logger, proxyURL, bindInterface string) *BoreNativeClient {
	if relay == "" {
		relay = "bore.pub"
	}
	if logger == nil {
		logger = log.New(os.Stderr, "[bore] ", log.LstdFlags)
	}
	return &BoreNativeClient{
		relay:             relay,
		secret:            secret,
		proxyURL:          proxyURL,
		bindInterface:     bindInterface,
		logger:            logger,
		backoffBase:       500 * time.Millisecond,
		backoffMax:        8 * time.Second,
		degradedThreshold: 5,
		state:             StateStarting,
		stateCh:           make(chan struct{}),
		done:              make(chan struct{}),
	}
}

func (b *BoreNativeClient) Name() string { return "bore" }

// controlAddr 返回控制连接地址。relay 可以是 "host" 或 "host:port"，
// 无端口时使用默认控制端口 boreControlPort。
func (b *BoreNativeClient) controlAddr() string {
	if _, _, err := net.SplitHostPort(b.relay); err == nil {
		return b.relay
	}
	return fmt.Sprintf("%s:%d", b.relay, boreControlPort)
}

// relayHost 返回 relay 的主机部分（去掉可选的控制端口）。
func (b *BoreNativeClient) relayHost() string {
	if h, _, err := net.SplitHostPort(b.relay); err == nil {
		return h
	}
	return b.relay
}

func (b *BoreNativeClient) Start(localPort int) error {
	b.localPort = localPort
	go b.run()
	return nil
}

// State 返回隧道当前健康状态。
func (b *BoreNativeClient) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// PublicAddr 返回当前实时公共地址；仅 ready 状态时非空。
func (b *BoreNativeClient) PublicAddr() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != StateReady {
		return ""
	}
	return b.publicAddr
}

// setStateLocked 更新状态与地址，并唤醒所有 WaitReady 等待者。
// 调用方必须持有 b.mu。
func (b *BoreNativeClient) setStateLocked(s State, addr string) {
	b.state = s
	b.publicAddr = addr
	close(b.stateCh)
	b.stateCh = make(chan struct{})
}

// WaitReady 等待隧道进入 ready 状态并返回当前公共地址。可重复调用：
// 已就绪立即返回；已停止或首次连接失败返回错误；ctx 到期返回 ctx.Err()。
func (b *BoreNativeClient) WaitReady(ctx context.Context) (string, error) {
	for {
		b.mu.Lock()
		st, addr, ch, firstErr := b.state, b.publicAddr, b.stateCh, b.firstErr
		b.mu.Unlock()

		switch st {
		case StateReady:
			return addr, nil
		case StateStopped:
			return "", fmt.Errorf("bore 已停止")
		}
		if firstErr != nil {
			return "", firstErr
		}

		select {
		case <-ch:
		case <-ctx.Done():
			return "", ctx.Err()
		case <-b.done:
			return "", fmt.Errorf("bore 已停止")
		}
	}
}

func (b *BoreNativeClient) Stop() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	conn := b.ctrlConn
	b.setStateLocked(StateStopped, "")
	b.mu.Unlock()

	close(b.done)
	if conn != nil {
		conn.Close()
	}
	return nil
}

// run 是主循环：首次连接失败即报错退出、不重试（Manager 依赖此语义做
// fallback）；就绪后控制连接断开则进入重连退避循环，直到 Stop。
func (b *BoreNativeClient) run() {
	conn, err := b.connectOnce()
	if err != nil {
		b.logger.Printf("错误: %v", err)
		b.mu.Lock()
		b.firstErr = err
		if !b.closed {
			b.setStateLocked(StateDegraded, "")
		}
		b.mu.Unlock()
		return
	}

	for {
		b.controlLoop(conn)
		conn.Close()

		// 控制连接断开：清空对外地址并进入重连。closed 判断必须与
		// 状态转换在同一锁内，否则 Stop() 可能恰在检查后、转换前
		// 完成，stopped 终态会被覆盖为 reconnecting。
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		b.setStateLocked(StateReconnecting, "")
		b.mu.Unlock()
		b.logger.Printf("控制连接断开，进入重连")

		conn = b.reconnectLoop()
		if conn == nil {
			return // Stop 触发退出
		}
	}
}

// connectOnce 建立一条控制连接并完成握手（可选认证 + Hello 分配端口）。
// 成功后记录连接、更新公共地址并置 ready。
func (b *BoreNativeClient) connectOnce() (net.Conn, error) {
	addr := b.controlAddr()
	b.logger.Printf("连接到中继 %s ...", addr)

	ctx, cancel := context.WithTimeout(context.Background(), boreTimeout)
	defer cancel()
	conn, err := DialContext(ctx, "tcp", addr, b.proxyURL, b.bindInterface)
	if err != nil {
		return nil, fmt.Errorf("连接中继失败: %w", err)
	}

	// 握手期间整体 30 秒超时（读+写）
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// 可选认证
	if b.secret != "" {
		if err := b.doAuth(conn); err != nil {
			conn.Close()
			return nil, fmt.Errorf("认证失败: %w", err)
		}
		// doAuth 结束时会清空 deadline，重设握手超时
		conn.SetDeadline(time.Now().Add(30 * time.Second))
	}

	// 发送 Hello(0) — 让服务器分配端口
	if err := b.writeMsg(conn, map[string]int{"Hello": 0}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送 Hello 失败: %w", err)
	}

	// 读取分配的端口
	msg, err := b.readMsg(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取 Hello 响应失败: %w", err)
	}

	var p int
	if port, ok := msg["Hello"]; ok {
		// Hello 的值可能是 float64（JSON number）
		switch v := port.(type) {
		case float64:
			p = int(v)
		case int:
			p = v
		default:
			conn.Close()
			return nil, fmt.Errorf("意外的 Hello 响应类型: %T", port)
		}
	} else if errMsg, ok := msg["Error"]; ok {
		conn.Close()
		return nil, fmt.Errorf("服务器错误: %v", errMsg)
	} else {
		conn.Close()
		return nil, fmt.Errorf("意外的响应: %v", msg)
	}

	// 握手完成：清除整个 deadline（读+写），控制循环长期阻塞读心跳，
	// 之后的写操作（如 Accept 响应路径）不得残留写超时
	conn.SetDeadline(time.Time{})

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		conn.Close()
		return nil, fmt.Errorf("bore 已停止")
	}
	b.ctrlConn = conn
	b.publicPort = p
	b.setStateLocked(StateReady, fmt.Sprintf("%s:%d", b.relayHost(), p))
	b.mu.Unlock()

	b.logger.Printf("分配的公共端口: %d", p)
	return conn, nil
}

// reconnectLoop 按有界指数退避反复尝试重连，直到成功（返回新控制连接）
// 或 Stop（返回 nil）。连续失败达 degradedThreshold 次后进入 degraded，
// 之后按 backoffMax 间隔继续尝试，成功则由 connectOnce 置回 ready。
func (b *BoreNativeClient) reconnectLoop() net.Conn {
	backoff := b.backoffBase
	failures := 0
	for {
		select {
		case <-b.done:
			return nil
		case <-time.After(backoff):
		}

		conn, err := b.connectOnce()
		if err == nil {
			return conn
		}
		if b.isClosed() {
			return nil
		}

		failures++
		b.logger.Printf("重连失败（第 %d 次）: %v", failures, err)
		if failures >= b.degradedThreshold {
			b.mu.Lock()
			if b.state == StateReconnecting {
				b.setStateLocked(StateDegraded, "")
			}
			b.mu.Unlock()
			backoff = b.backoffMax
		} else {
			backoff *= 2
			if backoff > b.backoffMax {
				backoff = b.backoffMax
			}
		}
	}
}

// controlLoop 读取服务器消息（Heartbeat 或 Connection），
// 直到连接断开或 Stop。返回即视为控制连接不再可用。
func (b *BoreNativeClient) controlLoop(conn net.Conn) {
	for {
		select {
		case <-b.done:
			return
		default:
		}

		// 中继静默丢包（无 RST/FIN）时 readMsg 会永久阻塞：设读超时，
		// 超过该时长无任何消息即视为控制连接失效，触发重连并清空地址。
		conn.SetReadDeadline(time.Now().Add(boreControlReadTimeout))
		msg, err := b.readMsg(conn)
		if err != nil {
			if b.isClosed() {
				return
			}
			b.logger.Printf("控制连接读取错误: %v", err)
			return
		}

		// 检查 Heartbeat（bare string）
		if _, ok := msg["__string__"]; ok {
			s := msg["__string__"].(string)
			if s == "Heartbeat" {
				continue // 心跳，忽略
			}
			b.logger.Printf("收到未知字符串消息: %s", s)
			continue
		}

		// Connection
		if connUUID, ok := msg["Connection"]; ok {
			uuid, _ := connUUID.(string)
			b.logger.Printf("新连接到达: %s", uuid)
			go b.handleDataConnection(uuid)
			continue
		}

		// Error
		if errMsg, ok := msg["Error"]; ok {
			b.logger.Printf("服务器错误: %v", errMsg)
			return
		}

		b.logger.Printf("未处理的消息: %v", msg)
	}
}

// handleDataConnection 处理一个转发的 TCP 连接。
func (b *BoreNativeClient) handleDataConnection(uuid string) {
	addr := b.controlAddr()
	ctx, cancel := context.WithTimeout(context.Background(), boreTimeout)
	defer cancel()
	relayConn, err := DialContext(ctx, "tcp", addr, b.proxyURL, b.bindInterface)
	if err != nil {
		b.logger.Printf("数据连接失败 (%s): %v", uuid, err)
		return
	}
	defer relayConn.Close()

	// 可选认证
	if b.secret != "" {
		if err := b.doAuth(relayConn); err != nil {
			b.logger.Printf("数据连接认证失败 (%s): %v", uuid, err)
			return
		}
	}

	// 发送 Accept
	if err := b.writeMsg(relayConn, map[string]string{"Accept": uuid}); err != nil {
		b.logger.Printf("发送 Accept 失败 (%s): %v", uuid, err)
		return
	}

	// 连接本地服务
	localAddr := fmt.Sprintf("127.0.0.1:%d", b.localPort)
	localConn, err := net.DialTimeout("tcp", localAddr, 5*time.Second)
	if err != nil {
		b.logger.Printf("连接本地服务失败 (%s): %v", uuid, err)
		return
	}
	defer localConn.Close()

	b.logger.Printf("转发已建立: %s ↔ %s", uuid, localAddr)

	// 双向转发
	done := make(chan struct{}, 2)
	go func() {
		io.Copy(localConn, relayConn)
		done <- struct{}{}
	}()
	go func() {
		io.Copy(relayConn, localConn)
		done <- struct{}{}
	}()

	<-done
}

// doAuth 执行 HMAC-SHA256 认证握手。
func (b *BoreNativeClient) doAuth(conn net.Conn) error {
	conn.SetDeadline(time.Now().Add(boreTimeout))
	defer conn.SetDeadline(time.Time{})

	// 读取 Challenge
	msg, err := b.readMsg(conn)
	if err != nil {
		return fmt.Errorf("读取 Challenge: %w", err)
	}

	challengeUUID, ok := msg["Challenge"].(string)
	if !ok {
		return fmt.Errorf("期望 Challenge，得到: %v", msg)
	}

	// 计算 HMAC
	// key = SHA256(secret), msg = uuid string bytes
	key := sha256.Sum256([]byte(b.secret))
	h := hmac.New(sha256.New, key[:])
	h.Write([]byte(challengeUUID))
	tag := hex.EncodeToString(h.Sum(nil))

	// 发送 Authenticate
	return b.writeMsg(conn, map[string]string{"Authenticate": tag})
}

// --- NUL-delimited JSON framing ---

// writeMsg 发送一个 JSON 消息 + NUL 分隔符。
func (b *BoreNativeClient) writeMsg(conn net.Conn, msg interface{}) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, 0x00)
	_, err = conn.Write(data)
	return err
}

// readMsg 读取一个 NUL 分隔的 JSON 消息。
// 返回一个 map，bare string 消息用 "__string__" key 表示。
func (b *BoreNativeClient) readMsg(conn net.Conn) (map[string]interface{}, error) {
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 1)

	for {
		n, err := conn.Read(tmp)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			continue
		}
		if tmp[0] == 0x00 {
			break
		}
		buf = append(buf, tmp[0])
		if len(buf) > 256 {
			return nil, fmt.Errorf("帧超过最大长度 256")
		}
	}

	if len(buf) == 0 {
		return nil, fmt.Errorf("空帧")
	}

	// 先尝试解析为 JSON 对象
	result := make(map[string]interface{})
	if err := json.Unmarshal(buf, &result); err == nil {
		return result, nil
	}

	// 可能是 bare string（如 "Heartbeat"）
	var s string
	if err := json.Unmarshal(buf, &s); err == nil {
		result["__string__"] = s
		return result, nil
	}

	return nil, fmt.Errorf("无法解析 JSON: %s", string(buf))
}

func (b *BoreNativeClient) isClosed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.closed
}
