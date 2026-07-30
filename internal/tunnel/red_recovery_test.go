package tunnel

import (
	"context"
	"net"
	"testing"
	"time"
)

// 本文件是 TUNNEL_HEALTH_AND_RECOVERY 的 RED 测试：
// 只使用原始公开行为（Start/WaitReady/Stop/Name 与 Manager 各方法）
// 加 relay "host:port" seam，不引用任何新增 API。
// 修复前它们以行为断言失败（证明缺陷），修复后必须通过。

// TestBoreStaleAddrAfterControlLoss 验证缺陷 a 的地址过期面：
// bore 控制连接断开（且无法恢复）后，Manager.PublicAddr()
// 不得继续暴露已失效的公网地址。
func TestBoreStaleAddrAfterControlLoss(t *testing.T) {
	srv := newFakeBoreServer(t)
	b := NewBoreNativeClient(srv.addr(), "", testLogger(), "", "")
	m := NewManager(testLogger())
	m.AddTunnel(b)

	if err := m.Start(freeTCPPort(t)); err != nil {
		t.Fatalf("Manager.Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop() })

	if got := m.PublicAddr(); got == "" {
		t.Fatal("就绪后 Manager.PublicAddr() 不应为空")
	}

	// 断开控制连接并拒绝后续连接：隧道此后不具备任何转发能力
	srv.setReject(true)
	srv.closeAllConns()

	waitFor(t, 3*time.Second, func() bool { return m.PublicAddr() == "" },
		"控制连接断开后 Manager.PublicAddr() 仍返回过期地址（应变为空）")
}

// TestBoreReconnectsAfterControlLoss 验证缺陷 a 的重连缺失面：
// 控制连接断开后，客户端应在预算内向 relay 发起新的控制连接。
func TestBoreReconnectsAfterControlLoss(t *testing.T) {
	srv := newFakeBoreServer(t)
	b := NewBoreNativeClient(srv.addr(), "", testLogger(), "", "")

	if err := b.Start(freeTCPPort(t)); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady 失败: %v", err)
	}
	if n := srv.connCount(); n != 1 {
		t.Fatalf("就绪后控制连接数 = %d，期望 1", n)
	}

	srv.closeAllConns()

	// 修复版默认退避 500ms 起步，5 秒预算内必然观察到至少一次重连尝试
	waitFor(t, 5*time.Second, func() bool { return srv.connCount() >= 2 },
		"控制连接断开后 5s 内未观察到任何重连尝试")
}

// TestFRPNoFalseReadyWithoutRealServer 验证缺陷 b：
// frp 服务器不可用（accept 后立即断开）时，WaitReady 不得成功返回地址。
func TestFRPNoFalseReadyWithoutRealServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	serverPort := ln.Addr().(*net.TCPAddr).Port

	f := NewFRPNativeClient("127.0.0.1", serverPort, "", freeTCPPort(t), testLogger())
	if err := f.Start(freeTCPPort(t)); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	t.Cleanup(func() { _ = f.Stop() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr, err := f.WaitReady(ctx)
	if err == nil {
		t.Fatalf("frp 服务器不可用，WaitReady 却成功返回伪地址 %q", addr)
	}
}
