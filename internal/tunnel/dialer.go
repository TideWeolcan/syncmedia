package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

// DialContext 在 context 超时范围内拨号，支持代理和网卡绑定。
func DialContext(ctx context.Context, network, addr, proxyURL, bindInterface string) (net.Conn, error) {
	done := make(chan struct{})
	var conn net.Conn
	var err error
	go func() {
		conn, err = dialOnce(network, addr, proxyURL, bindInterface)
		close(done)
	}()
	select {
	case <-done:
		return conn, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// dialOnce 执行一次实际拨号（代理或直连），不处理 context 超时。
func dialOnce(network, addr, proxyURL, bindInterface string) (net.Conn, error) {
	// 解析代理
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("解析代理地址失败: %w", err)
		}

		switch u.Scheme {
		case "socks5", "socks5h":
			dialer, err := proxy.SOCKS5("tcp", u.Host, nil, &net.Dialer{Timeout: 10 * time.Second})
			if err != nil {
				return nil, fmt.Errorf("创建 SOCKS5 拨号器失败: %w", err)
			}
			conn, err := dialer.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			if bindInterface != "" {
				setBindInterface(conn, bindInterface)
			}
			return conn, nil

		case "http", "https":
			return httpConnect(u.Host, addr, bindInterface)

		default:
			return nil, fmt.Errorf("不支持的代理类型: %s", u.Scheme)
		}
	}

	// 直连
	conn, err := net.DialTimeout(network, addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if bindInterface != "" {
		setBindInterface(conn, bindInterface)
	}
	return conn, nil
}

// httpConnect 通过 HTTP CONNECT 代理拨号。
func httpConnect(proxyHost, targetAddr, bindInterface string) (net.Conn, error) {
	conn, err := net.DialTimeout("tcp", proxyHost, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("连接代理失败: %w", err)
	}
	if bindInterface != "" {
		setBindInterface(conn, bindInterface)
	}

	// 发送 CONNECT 请求
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", targetAddr, targetAddr)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送 CONNECT 失败: %w", err)
	}

	// 读取响应
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取代理响应失败: %w", err)
	}

	resp := string(buf[:n])
	if len(resp) < 12 || resp[9:12] != "200" {
		conn.Close()
		return nil, fmt.Errorf("代理拒绝连接: %s", resp)
	}

	return conn, nil
}

// setBindInterface 使用 SO_BINDTODEVICE 绑定指定网卡（绕过 VPN）。
func setBindInterface(conn net.Conn, ifName string) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	rawConn, err := tcpConn.SyscallConn()
	if err != nil {
		return
	}
	rawConn.Control(func(fd uintptr) {
		syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, syscall.SO_BINDTODEVICE, ifName)
	})
}
