//go:build !linux

package tunnel

import "syscall"

// bindInterfaceControl 在非 Linux 平台无 SO_BINDTODEVICE，绑定网卡不可用；
// 返回 no-op Control，保持调用点一致（仅 Linux 生效）。
func bindInterfaceControl(_ string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, _ syscall.RawConn) error {
		return nil
	}
}
