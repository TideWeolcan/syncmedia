package config

import (
	"net"
	"path/filepath"
	"testing"
)

// occupyPort 监听一个临时端口并保持占用，返回该端口；测试结束自动释放。
func occupyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("占用临时端口失败: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// freePort 获取一个当前空闲的端口号（监听后立即释放；存在极小竞态窗口，可接受）。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("获取空闲端口失败: %v", err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

// TestResolvePortPreferredWhenFree：首选端口空闲时直接使用，不避让。
func TestResolvePortPreferredWhenFree(t *testing.T) {
	p := freePort(t)
	got, avoided := ResolvePort(0, p, 0)
	if avoided {
		t.Errorf("首选端口 %d 空闲却被避让", p)
	}
	if got != p {
		t.Errorf("ResolvePort(0, %d, 0) = %d, want %d", p, got, p)
	}
}

// TestResolvePortConfiguredUsed：用户显式配置端口时优先使用（不查 remembered）。
func TestResolvePortConfiguredUsed(t *testing.T) {
	p := freePort(t)
	got, avoided := ResolvePort(p, 8999, 12345)
	if avoided || got != p {
		t.Errorf("ResolvePort(%d, ...) = (%d, %v), want (%d, false)", p, got, avoided, p)
	}
}

// TestResolvePortOccupiedAvoids：首选/配置端口被占用时避让到别的可用端口。
func TestResolvePortOccupiedAvoids(t *testing.T) {
	busy := occupyPort(t)
	got, avoided := ResolvePort(0, busy, 0)
	if !avoided {
		t.Errorf("端口 %d 被占用却未避让", busy)
	}
	if got == busy {
		t.Errorf("避让后仍返回被占用端口 %d", got)
	}
	if !PortAvailable(got) {
		t.Errorf("避让结果 %d 不可用", got)
	}

	// configured > 0 同样避让
	got2, avoided2 := ResolvePort(busy, busy, 0)
	if !avoided2 || got2 == busy {
		t.Errorf("configured=%d 被占用时应避让，得到 (%d, %v)", busy, got2, avoided2)
	}
}

// TestResolvePortRememberedUsed：未配置时优先沿用上次记住的端口。
func TestResolvePortRememberedUsed(t *testing.T) {
	// 首选端口故意被占用，确保结果只能来自 remembered
	busy := occupyPort(t)
	remembered := freePort(t)
	got, avoided := ResolvePort(0, busy, remembered)
	if avoided {
		t.Errorf("记住的端口 %d 空闲却被避让", remembered)
	}
	if got != remembered {
		t.Errorf("ResolvePort(0, %d, %d) = %d, want %d（应沿用记住的端口）", busy, remembered, got, remembered)
	}
}

// TestResolvePortRememberedOccupiedAvoids：记住的端口被占用时避让。
func TestResolvePortRememberedOccupiedAvoids(t *testing.T) {
	busy := occupyPort(t)
	got, avoided := ResolvePort(0, 8999, busy)
	if !avoided || got == busy {
		t.Errorf("记住的端口 %d 被占用时应避让，得到 (%d, %v)", busy, got, avoided)
	}
}

// TestResolvePortNegativePassthrough：非法负端口原样返回（让真实监听报错）。
func TestResolvePortNegativePassthrough(t *testing.T) {
	got, avoided := ResolvePort(-1, 8999, 0)
	if got != -1 || avoided {
		t.Errorf("ResolvePort(-1, ...) = (%d, %v), want (-1, false)", got, avoided)
	}
}

// TestRandomAvoidPortExcludesCommonPorts：随机避让绝不返回常见服务端口。
func TestRandomAvoidPortExcludesCommonPorts(t *testing.T) {
	for i := 0; i < 100; i++ {
		p := randomAvoidPort()
		if p < 1024 || p > 65535 {
			t.Fatalf("randomAvoidPort() = %d，超出 1024-65535 区间", p)
		}
		if commonServicePorts[p] {
			t.Fatalf("randomAvoidPort() 返回了常见服务端口 %d", p)
		}
		if !PortAvailable(p) {
			t.Fatalf("randomAvoidPort() 返回了被占用端口 %d", p)
		}
	}
}

// TestPortStateRoundTrip：状态文件写入后可读回，缺失时返回零值。
func TestPortStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := LoadPortState(dir)
	if err != nil || st != (PortState{}) {
		t.Fatalf("首次 LoadPortState = (%+v, %v), want (零值, nil)", st, err)
	}

	want := PortState{SyncplayPort: 23456, WebPort: 34567}
	if err := SavePortState(dir, want); err != nil {
		t.Fatalf("SavePortState: %v", err)
	}
	got, err := LoadPortState(dir)
	if err != nil {
		t.Fatalf("LoadPortState: %v", err)
	}
	if got != want {
		t.Errorf("LoadPortState = %+v, want %+v", got, want)
	}
}

// TestDataDirEnvOverride：SYNCMEDIA_DATA_DIR 覆盖默认数据目录。
func TestDataDirEnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "custom")
	t.Setenv("SYNCMEDIA_DATA_DIR", want)
	got, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() err = %v", err)
	}
	if got != want {
		t.Errorf("DataDir() = %q, want %q（环境变量覆盖）", got, want)
	}
}

// TestDataDirDefaultUsesExecutableDir：无环境变量时默认落在可执行文件所在目录的 data/ 下。
func TestDataDirDefaultUsesExecutableDir(t *testing.T) {
	t.Setenv("SYNCMEDIA_DATA_DIR", "")
	dir, err := DataDir()
	if err != nil {
		t.Fatalf("DataDir() err = %v", err)
	}
	if filepath.Base(dir) != "data" {
		t.Errorf("默认 DataDir() = %q, want 以 data 结尾（可执行文件目录下）", dir)
	}
}

// TestPortAvailableBounds：非法端口号（0/负数/超界）视为不可用。
func TestPortAvailableBounds(t *testing.T) {
	for _, p := range []int{0, -1, 65536, 70000} {
		if PortAvailable(p) {
			t.Errorf("PortAvailable(%d) = true, want false（非法端口）", p)
		}
	}
}
