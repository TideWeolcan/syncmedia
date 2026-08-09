package tunnel

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"
)

// 本文件测试 GREEN 阶段新增的状态 API：State()/PublicAddr()、
// bore 重连退避状态机、frp statusFn 注入状态机、Manager live 地址委托。

// --- bore 状态机 ---

// startReadyBore 建立一个已就绪的 bore 客户端（注入退避参数），返回初始地址。
func startReadyBore(t *testing.T, srv *fakeBoreServer, base, max time.Duration, threshold int) (*BoreNativeClient, string) {
	t.Helper()
	b := NewBoreNativeClient(srv.addr(), "", testLogger(), "", "")
	b.backoffBase = base
	b.backoffMax = max
	b.degradedThreshold = threshold
	if err := b.Start(freeTCPPort(t)); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr, err := b.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady 失败: %v", err)
	}
	if got := b.State(); got != StateReady {
		t.Fatalf("就绪后 State() = %q，期望 %q", got, StateReady)
	}
	if got := b.PublicAddr(); got != addr {
		t.Fatalf("就绪后 PublicAddr() = %q，期望 %q", got, addr)
	}
	return b, addr
}

// TestBoreStateReconnectingAfterDisconnect：断线后进入 reconnecting 且地址清空。
func TestBoreStateReconnectingAfterDisconnect(t *testing.T) {
	srv := newFakeBoreServer(t)
	// threshold 给大值，让状态停留在 reconnecting 便于观察
	b, _ := startReadyBore(t, srv, 20*time.Millisecond, 40*time.Millisecond, 100)

	srv.setReject(true)
	srv.closeAllConns()

	waitFor(t, 3*time.Second, func() bool { return b.State() == StateReconnecting },
		"断线后未进入 reconnecting")
	if got := b.PublicAddr(); got != "" {
		t.Fatalf("reconnecting 期间 PublicAddr() = %q，期望空", got)
	}
}

// TestBoreStateDegradedThenRecover：连续失败达阈值进入 degraded；
// 服务恢复后回到 ready 且拿到新地址；Stop 后进入 stopped 且不再发起连接。
func TestBoreStateDegradedThenRecover(t *testing.T) {
	srv := newFakeBoreServer(t)
	b, addr1 := startReadyBore(t, srv, 10*time.Millisecond, 40*time.Millisecond, 3)

	srv.setReject(true)
	srv.closeAllConns()

	waitFor(t, 3*time.Second, func() bool { return b.State() == StateDegraded },
		"连续重连失败达阈值后未进入 degraded")
	if got := b.PublicAddr(); got != "" {
		t.Fatalf("degraded 期间 PublicAddr() = %q，期望空", got)
	}

	// 恢复接受连接 → 回到 ready，fake server 端口递增所以是新地址
	srv.setReject(false)
	waitFor(t, 3*time.Second, func() bool { return b.State() == StateReady },
		"服务恢复后未回到 ready")
	addr2 := b.PublicAddr()
	if addr2 == "" {
		t.Fatal("恢复 ready 后 PublicAddr() 为空")
	}
	if addr2 == addr1 {
		t.Fatalf("恢复后应拿到新分配地址，仍是 %q", addr2)
	}

	// Stop → stopped 终态，退避循环立刻退出，不再有新连接到达
	if err := b.Stop(); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if got := b.State(); got != StateStopped {
		t.Fatalf("Stop 后 State() = %q，期望 %q", got, StateStopped)
	}
	if got := b.PublicAddr(); got != "" {
		t.Fatalf("Stop 后 PublicAddr() = %q，期望空", got)
	}
	n := srv.connCount()
	time.Sleep(150 * time.Millisecond) // > backoffMax(40ms)，若循环未停会有新尝试
	if got := srv.connCount(); got != n {
		t.Fatalf("Stop 后仍有新连接到达: %d -> %d", n, got)
	}
}

// TestBoreBackoffBounded：退避有界——相邻重连尝试间隔不超过
// backoffMax+宽裕量，且预算内尝试次数达到阈值以上。
func TestBoreBackoffBounded(t *testing.T) {
	srv := newFakeBoreServer(t)
	b, _ := startReadyBore(t, srv, 10*time.Millisecond, 40*time.Millisecond, 3)
	_ = b

	srv.setReject(true)
	srv.closeAllConns()

	// 初始 1 次 + 至少 3 次重连尝试
	waitFor(t, 3*time.Second, func() bool { return srv.connCount() >= 4 },
		"预算内重连尝试次数不足 3 次")

	times := srv.connTimesSnapshot()
	// times[0] 是初始连接；从第二次重连起检查相邻间隔有界
	for i := 2; i < len(times); i++ {
		gap := times[i].Sub(times[i-1])
		if gap > 40*time.Millisecond+500*time.Millisecond {
			t.Fatalf("重连尝试 %d->%d 间隔 %v 超过 backoffMax+500ms", i-1, i, gap)
		}
	}
}

// TestBoreWaitReadyRepeatable：WaitReady 可重复调用；Stop 后返回错误。
func TestBoreWaitReadyRepeatable(t *testing.T) {
	srv := newFakeBoreServer(t)
	b, addr := startReadyBore(t, srv, 10*time.Millisecond, 40*time.Millisecond, 3)

	// 已就绪时重复调用立即返回同一地址
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		got, err := b.WaitReady(ctx)
		cancel()
		if err != nil {
			t.Fatalf("第 %d 次重复 WaitReady 失败: %v", i+1, err)
		}
		if got != addr {
			t.Fatalf("第 %d 次重复 WaitReady = %q，期望 %q", i+1, got, addr)
		}
	}

	if err := b.Stop(); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := b.WaitReady(ctx); err == nil {
		t.Fatal("Stop 后 WaitReady 应返回错误")
	}
}

// TestBoreFirstConnectFailFast：首次连接失败保持原语义——
// WaitReady 报错、不重试，状态置 degraded（Manager fallback 依赖此语义）。
func TestBoreFirstConnectFailFast(t *testing.T) {
	// 没人监听的端口，连接被拒绝
	b := NewBoreNativeClient("127.0.0.1:"+strconv.Itoa(freeTCPPort(t)), "", testLogger(), "", "")
	if err := b.Start(freeTCPPort(t)); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.WaitReady(ctx); err == nil {
		t.Fatal("首连失败时 WaitReady 应返回错误")
	}
	if ctx.Err() != nil {
		t.Fatal("WaitReady 应由首连错误返回，而不是等到 ctx 超时")
	}
	waitFor(t, time.Second, func() bool { return b.State() == StateDegraded },
		"首连失败后状态应为 degraded")
}

// --- frp statusFn 注入状态机（不经 Start()，直接驱动 monitor()）---

// fakeStatus 是可并发更新的 statusFn 注入源。
type fakeStatus struct {
	mu    sync.Mutex
	phase string
	addr  string
	ok    bool
}

func (s *fakeStatus) set(phase, addr string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.addr, s.ok = phase, addr, ok
}

func (s *fakeStatus) get() (string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase, s.addr, s.ok
}

// TestFRPStateTransitionsWithInjectedStatus：
// starting → ready（RemoteAddr 推导地址）→ reconnecting（清地址）→ ready（新地址）。
func TestFRPStateTransitionsWithInjectedStatus(t *testing.T) {
	st := &fakeStatus{}
	f := NewFRPNativeClient("127.0.0.1", 17000, "", 555, testLogger())
	f.pollInterval = 10 * time.Millisecond
	f.statusFn = st.get
	go f.monitor()
	t.Cleanup(func() { _ = f.Stop() })

	// 查询不到状态时保持 starting、无地址
	time.Sleep(80 * time.Millisecond)
	if got := f.State(); got != StateStarting {
		t.Fatalf("初始 State() = %q，期望 %q", got, StateStarting)
	}
	if got := f.PublicAddr(); got != "" {
		t.Fatalf("starting 期间 PublicAddr() = %q，期望空", got)
	}

	// running(":12345") → ready，地址从 RemoteAddr 端口推导
	st.set("running", ":12345", true)
	waitFor(t, 2*time.Second, func() bool { return f.State() == StateReady },
		"phase=running 后未进入 ready")
	if got := f.PublicAddr(); got != "127.0.0.1:12345" {
		t.Fatalf("ready 后 PublicAddr() = %q，期望 127.0.0.1:12345", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if addr, err := f.WaitReady(ctx); err != nil || addr != "127.0.0.1:12345" {
		cancel()
		t.Fatalf("WaitReady = (%q, %v)，期望 (127.0.0.1:12345, nil)", addr, err)
	}
	cancel()

	// 掉出 running → reconnecting、地址清空（degradedAfter 默认 15s 不会触发）
	st.set("check failed", "", true)
	waitFor(t, 2*time.Second, func() bool { return f.State() == StateReconnecting },
		"phase 掉出 running 后未进入 reconnecting")
	if got := f.PublicAddr(); got != "" {
		t.Fatalf("reconnecting 期间 PublicAddr() = %q，期望空", got)
	}

	// 恢复 running（新端口，宿主形式 0.0.0.0:P）→ ready、新地址
	st.set("running", "0.0.0.0:23456", true)
	waitFor(t, 2*time.Second, func() bool { return f.State() == StateReady },
		"phase 恢复 running 后未回到 ready")
	if got := f.PublicAddr(); got != "127.0.0.1:23456" {
		t.Fatalf("恢复后 PublicAddr() = %q，期望 127.0.0.1:23456", got)
	}
}

// TestFRPDegradedAfterSustainedOutage：非 running 持续超过 degradedAfter
// 进入 degraded；恢复 running 后回到 ready；Stop 后 stopped。
func TestFRPDegradedAfterSustainedOutage(t *testing.T) {
	st := &fakeStatus{}
	st.set("running", ":700", true)
	f := NewFRPNativeClient("127.0.0.1", 17000, "", 555, testLogger())
	f.pollInterval = 10 * time.Millisecond
	f.degradedAfter = 50 * time.Millisecond
	f.statusFn = st.get
	go f.monitor()
	t.Cleanup(func() { _ = f.Stop() })

	waitFor(t, 2*time.Second, func() bool { return f.State() == StateReady },
		"未进入 ready")

	st.set("closed", "", true)
	waitFor(t, 2*time.Second, func() bool { return f.State() == StateDegraded },
		"长期非 running 未进入 degraded")
	if got := f.PublicAddr(); got != "" {
		t.Fatalf("degraded 期间 PublicAddr() = %q，期望空", got)
	}

	// degraded 恢复 → ready
	st.set("running", ":701", true)
	waitFor(t, 2*time.Second, func() bool { return f.State() == StateReady },
		"degraded 后恢复 running 未回到 ready")
	if got := f.PublicAddr(); got != "127.0.0.1:701" {
		t.Fatalf("恢复后 PublicAddr() = %q，期望 127.0.0.1:701", got)
	}

	if err := f.Stop(); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	if got := f.State(); got != StateStopped {
		t.Fatalf("Stop 后 State() = %q，期望 %q", got, StateStopped)
	}
	if got := f.PublicAddr(); got != "" {
		t.Fatalf("Stop 后 PublicAddr() = %q，期望空", got)
	}
}

// TestFRPDeriveAddr：RemoteAddr 各种形态的端口解析与回退拼接。
func TestFRPDeriveAddr(t *testing.T) {
	f := NewFRPNativeClient("1.2.3.4", 7000, "", 999, testLogger())
	cases := []struct {
		remoteAddr string
		want       string
	}{
		{":555", "1.2.3.4:555"},
		{"0.0.0.0:556", "1.2.3.4:556"},
		{"[::]:557", "1.2.3.4:557"},
		{"", "1.2.3.4:999"},        // 解析失败 → server:remotePort
		{"garbage", "1.2.3.4:999"}, // 解析失败 → server:remotePort
	}
	for _, c := range cases {
		if got := f.deriveAddr(c.remoteAddr); got != c.want {
			t.Fatalf("deriveAddr(%q) = %q，期望 %q", c.remoteAddr, got, c.want)
		}
	}

	// remotePort=0 时回退使用 localPort
	f2 := NewFRPNativeClient("1.2.3.4", 7000, "", 0, testLogger())
	f2.localPort = 8080
	if got := f2.deriveAddr("bad"); got != "1.2.3.4:8080" {
		t.Fatalf("remotePort=0 时 deriveAddr = %q，期望 1.2.3.4:8080", got)
	}
}

// --- Manager live 地址委托 ---

// TestManagerLiveAddrRecoveryAndStop：active 就绪时返回 live 地址；
// 断线后返回空；恢复后返回新地址；Stop 后返回空。
func TestManagerLiveAddrRecoveryAndStop(t *testing.T) {
	srv := newFakeBoreServer(t)
	b := NewBoreNativeClient(srv.addr(), "", testLogger(), "", "")
	b.backoffBase = 10 * time.Millisecond
	b.backoffMax = 40 * time.Millisecond
	b.degradedThreshold = 3

	m := NewManager(testLogger())
	m.AddTunnel(b)
	if err := m.Start(context.Background(), freeTCPPort(t)); err != nil {
		t.Fatalf("Manager.Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	addr1 := m.PublicAddr()
	if addr1 == "" {
		t.Fatal("就绪后 Manager.PublicAddr() 为空")
	}
	if got := m.ActiveTunnelName(); got != "bore" {
		t.Fatalf("ActiveTunnelName() = %q，期望 bore", got)
	}

	// 断线 → live 地址清空
	srv.setReject(true)
	srv.closeAllConns()
	waitFor(t, 2*time.Second, func() bool { return m.PublicAddr() == "" },
		"断线后 Manager.PublicAddr() 未清空")

	// 恢复 → 新 live 地址（fake server 端口递增）
	srv.setReject(false)
	waitFor(t, 2*time.Second, func() bool { return m.PublicAddr() != "" },
		"恢复后 Manager.PublicAddr() 未恢复")
	if got := m.PublicAddr(); got == addr1 {
		t.Fatalf("恢复后应为新分配地址，仍是 %q", got)
	}

	// Stop → 全部清空
	if err := m.Stop(); err != nil {
		t.Fatalf("Manager.Stop 失败: %v", err)
	}
	if got := m.PublicAddr(); got != "" {
		t.Fatalf("Stop 后 Manager.PublicAddr() = %q，期望空", got)
	}
	if got := m.ActiveTunnelName(); got != "" {
		t.Fatalf("Stop 后 ActiveTunnelName() = %q，期望空", got)
	}
}
