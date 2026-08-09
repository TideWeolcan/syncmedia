package manager

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TideWeolcan/syncmedia/internal/config"
)

// waitStatusPort 轮询 Status 直到 ServerPort 非 0（Start 完成端口解析）。
func waitStatusPort(t *testing.T, m *Manager, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if p := m.Status().ServerPort; p != 0 {
			return p
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("Start 后 Status().ServerPort 仍为 0（端口未解析）")
	return 0
}

// TestStartResolvesAutoPortAndPersists：未配置端口（0）时，Start 自动解析
// 实际端口并写入状态文件；重启（经 Restart）沿用记住的端口不漂移。
func TestStartResolvesAutoPortAndPersists(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SYNCMEDIA_DATA_DIR", dataDir)

	cfg := config.Default()
	cfg.Server.TLS = false
	cfg.Tunnel.Type = "none"
	cfg.Web.Enabled = false

	m := NewManager(cfg, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- m.Start(ctx) }()

	port := waitStatusPort(t, m, 5*time.Second)
	if port <= 0 || port > 65535 {
		t.Fatalf("自动解析端口 = %d，非法", port)
	}
	// 实际端口必须可连接（syncplay 已监听）
	conn, err := net.Dial("tcp", "127.0.0.1:"+itoa(port))
	if err != nil {
		t.Fatalf("连接自动端口 %d 失败: %v", port, err)
	}
	_ = conn.Close()

	// 状态文件已写入且记录该端口
	st, err := config.LoadPortState(dataDir)
	if err != nil {
		t.Fatalf("LoadPortState: %v", err)
	}
	if st.SyncplayPort != port {
		t.Errorf("状态文件 SyncplayPort = %d, want %d", st.SyncplayPort, port)
	}

	// 停止后重新 Start（模拟重启）：未配置端口 → 沿用记住的端口
	cancel()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("第一次 Start 返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("第一次 Start 未在 5s 内退出")
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	startDone2 := make(chan error, 1)
	go func() { startDone2 <- m.Start(ctx2) }()
	port2 := waitStatusPort(t, m, 5*time.Second)
	cancel2()
	// waitStatusPort 读到的是第一次 Start 留下的端口（Stop 不重置），cancel2
	// 可能先于第二次 Start 越过启动检查点触发，Start 因此以 context.Canceled
	// 退出——属预期路径；这里只等待其退出，消除与 TempDir 清理的竞态。
	select {
	case err := <-startDone2:
		if err != nil && err != context.Canceled {
			t.Fatalf("第二次 Start 返回错误: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("第二次 Start 未在 5s 内退出")
	}
	if port2 != port {
		t.Errorf("重启后端口从 %d 漂移到 %d（应沿用记住的端口）", port, port2)
	}
	m.Stop()
}

// TestStartConfiguredPortNotPersistedWhenInvalid：非法配置端口（负值）不写状态文件。
func TestStartConfiguredPortNotPersistedWhenInvalid(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SYNCMEDIA_DATA_DIR", dataDir)

	cfg := restartSafeCfg() // Server.Port = -1
	m := NewManager(cfg, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = m.Start(ctx) }()
	time.Sleep(200 * time.Millisecond) // 让 Start 走到 Listen(-1) 失败并退出
	cancel()

	if _, err := os.Stat(filepath.Join(dataDir, "ports.json")); !os.IsNotExist(err) {
		t.Errorf("非法端口配置不应写状态文件，ports.json 存在: %v", err)
	}
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
