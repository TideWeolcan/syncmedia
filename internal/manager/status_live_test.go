package manager

import (
	"testing"

	"github.com/TideWeolcan/syncmedia/internal/tunnel"
)

// Status 必须反映 tunnel 层实时状态，而不是 Start 时拷贝的一次性缓存。

// TestStatusAfterRestartResetNoStaleAddr 模拟 Restart 重置后的形态：
// tunnelMgr 已置 nil，但缓存字段残留旧隧道地址。Status 不得吐出陈旧值。
func TestStatusAfterRestartResetNoStaleAddr(t *testing.T) {
	m := NewManager(restartSafeCfg(), discardLogger())
	m.publicAddr = "bore.example.com:12345"
	m.tunnelName = "bore"
	m.tunnelMgr = nil

	st := m.Status()
	if st.PublicAddr != "" {
		t.Errorf("tunnelMgr=nil 时 Status().PublicAddr = %q, want 空（陈旧缓存泄漏）", st.PublicAddr)
	}
	if st.TunnelType != "" {
		t.Errorf("tunnelMgr=nil 时 Status().TunnelType = %q, want 空（陈旧缓存泄漏）", st.TunnelType)
	}
}

// TestStatusDelegatesToLiveTunnelManager：缓存值与实时值不同时以实时值为准。
// 真实 tunnel.Manager 无 active 隧道 → PublicAddr()/ActiveTunnelName() 均为空。
func TestStatusDelegatesToLiveTunnelManager(t *testing.T) {
	m := NewManager(restartSafeCfg(), discardLogger())
	m.tunnelMgr = tunnel.NewManager(discardLogger())
	m.publicAddr = "stale.example.com:1"
	m.tunnelName = "stale"

	st := m.Status()
	if st.PublicAddr != "" {
		t.Errorf("无 active 隧道时 Status().PublicAddr = %q, want 空（应实时委托 tunnelMgr）", st.PublicAddr)
	}
	if st.TunnelType != "" {
		t.Errorf("无 active 隧道时 Status().TunnelType = %q, want 空（应实时委托 tunnelMgr）", st.TunnelType)
	}
}
