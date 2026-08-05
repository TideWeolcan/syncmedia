//go:build linux

package tunnel

import "syscall"

// bindInterfaceControl 返回 Control 函数，用于在 connect 前通过
// SO_BINDTODEVICE 绑定指定网卡（绕过 VPN）。仅 Linux 支持。
func bindInterfaceControl(ifName string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifName)
		})
	}
}
