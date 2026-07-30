package tunnel

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeBoreServer 在 127.0.0.1 动态端口上模拟 bore relay 的控制协议：
// 读取 NUL 分隔的 JSON 帧，收到 {"Hello":0} 时回复 {"Hello":<分配端口>}\x00。
// 分配端口只是协议字段（测试不真正使用数据面），每次 Hello 递增以便区分新旧地址。
// 记录每次控制连接的到达时间，支持主动断开现有连接与拒绝新连接。
type fakeBoreServer struct {
	t  *testing.T
	ln net.Listener

	mu        sync.Mutex
	conns     []net.Conn
	connTimes []time.Time
	reject    bool
	nextPort  int
	closed    bool
}

func newFakeBoreServer(t *testing.T) *fakeBoreServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake bore server 监听失败: %v", err)
	}
	s := &fakeBoreServer{t: t, ln: ln, nextPort: 40001}
	go s.acceptLoop()
	t.Cleanup(s.close)
	return s
}

// addr 返回 "127.0.0.1:port" 形式的控制地址（作为 relay 传给客户端）。
func (s *fakeBoreServer) addr() string {
	return s.ln.Addr().String()
}

func (s *fakeBoreServer) acceptLoop() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.connTimes = append(s.connTimes, time.Now())
		reject := s.reject
		if !reject {
			s.conns = append(s.conns, c)
		}
		s.mu.Unlock()
		if reject {
			c.Close()
			continue
		}
		go s.handleConn(c)
	}
}

func (s *fakeBoreServer) handleConn(c net.Conn) {
	r := bufio.NewReader(c)
	for {
		frame, err := r.ReadBytes(0x00)
		if err != nil {
			return
		}
		frame = frame[:len(frame)-1]
		var m map[string]interface{}
		if err := json.Unmarshal(frame, &m); err != nil {
			continue
		}
		if _, ok := m["Hello"]; ok {
			s.mu.Lock()
			p := s.nextPort
			s.nextPort++
			s.mu.Unlock()
			resp, _ := json.Marshal(map[string]int{"Hello": p})
			_, _ = c.Write(append(resp, 0x00))
		}
	}
}

// connCount 返回已到达的控制连接总数（含被拒绝的）。
func (s *fakeBoreServer) connCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.connTimes)
}

// connTimesSnapshot 返回每次连接到达时间的拷贝。
func (s *fakeBoreServer) connTimesSnapshot() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Time, len(s.connTimes))
	copy(out, s.connTimes)
	return out
}

// setReject 控制之后到达的连接是否被拒绝（accept 后立即关闭，仍计数）。
func (s *fakeBoreServer) setReject(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reject = v
}

// closeAllConns 关闭当前所有已建立的控制连接（模拟断线）。
func (s *fakeBoreServer) closeAllConns() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}

// close 关闭 listener 与全部连接，保证测试结束后所有 goroutine 退出。
func (s *fakeBoreServer) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.ln.Close()
	s.closeAllConns()
}

// waitFor 以 20ms 间隔轮询 cond 直到成立；超时则以 msg Fatal。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}

// testLogger 返回丢弃输出的 logger，保持测试输出干净。
func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// freeTCPPort 动态获取一个当前空闲的 TCP 端口号（监听后立即关闭复用其端口号）。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return p
}
