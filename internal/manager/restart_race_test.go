package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/TideWeolcan/syncmedia/internal/config"
)

// TestRestartConcurrentNoPanic 验证 5 个 goroutine 并发调用 Restart 不会
// panic（nil-pointer / 双重 Start），且结束后 Manager 仍可正常 Stop。
func TestRestartConcurrentNoPanic(t *testing.T) {
	// 端口自动避让会把实际端口写入数据目录的状态文件：
	// 隔离到临时目录，避免测试污染仓库/可执行文件旁的 data/
	t.Setenv("SYNCMEDIA_DATA_DIR", t.TempDir())

	cfg := config.Default()
	cfg.Server.Port = 0    // 0=未设定，走自动避让
	cfg.Server.TLS = false
	cfg.Tunnel.Type = "none"
	cfg.Web.Enabled = false

	m := NewManager(cfg, discardLogger())

	// 先正常启动一次，使内部组件就绪（模拟稳态）
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = m.Start(ctx)
	}()
	// 等待 syncplay 就绪
	time.Sleep(200 * time.Millisecond)

	// 5 个 goroutine 并发 Restart
	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			m.Restart()
		}()
	}
	wg.Wait()

	// 等后台 Start goroutine 有机会运行
	time.Sleep(300 * time.Millisecond)

	// 取消原始 ctx（如果 Start 仍被阻塞的话）
	cancel()

	// Manager 必须仍可正常 Stop 而不 panic
	m.Stop()
}
