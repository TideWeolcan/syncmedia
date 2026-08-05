package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// DialContext 在 context 超时范围内拨号，支持代理和网卡绑定。
func DialContext(ctx context.Context, network, addr, proxyURL, bindInterface string) (net.Conn, error) {
	type dialResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan dialResult, 1)
	go func() {
		c, e := dialOnce(network, addr, proxyURL, bindInterface)
		ch <- dialResult{c, e}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-ctx.Done():
		// ctx 已取消，但 goroutine 可能已建立连接；等待结果并关闭泄漏的 conn
		go func() {
			r := <-ch
			if r.conn != nil {
				r.conn.Close()
			}
		}()
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
			// bindInterface 必须在底层 socket connect 前绑定，通过 Control 函数注入
			baseDialer := &net.Dialer{Timeout: 10 * time.Second}
			if bindInterface != "" {
				baseDialer.Control = bindInterfaceControl(bindInterface)
			}
			dialer, err := proxy.SOCKS5("tcp", u.Host, nil, baseDialer)
			if err != nil {
				return nil, fmt.Errorf("创建 SOCKS5 拨号器失败: %w", err)
			}
			conn, err := dialer.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			return conn, nil

		case "http", "https":
			return httpConnect(u.Host, addr, bindInterface)

		default:
			return nil, fmt.Errorf("不支持的代理类型: %s", u.Scheme)
		}
	}

	// 直连：使用 Dialer.Control 在 connect 前绑定网卡
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if bindInterface != "" {
		dialer.Control = bindInterfaceControl(bindInterface)
	}
	conn, err := dialer.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// httpConnect 通过 HTTP CONNECT 代理拨号。
func httpConnect(proxyHost, targetAddr, bindInterface string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if bindInterface != "" {
		dialer.Control = bindInterfaceControl(bindInterface)
	}
	conn, err := dialer.Dial("tcp", proxyHost)
	if err != nil {
		return nil, fmt.Errorf("连接代理失败: %w", err)
	}

	// 发送 CONNECT 请求
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", targetAddr, targetAddr)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送 CONNECT 失败: %w", err)
	}

	// 使用 http.ReadResponse 正确解析可能分多次到达的 HTTP 响应
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取代理响应失败: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != 200 {
		conn.Close()
		return nil, fmt.Errorf("代理拒绝连接: %s", resp.Status)
	}

	return conn, nil
}
