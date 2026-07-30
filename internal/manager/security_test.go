package manager

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/TideWeolcan/syncmedia/internal/config"
)

// buildWebServer 适配 NewWebServer 构造，测试断言均经由它。
// RED 阶段桥接旧签名（无校验、无 error）；GREEN 阶段改为直调新签名。
func buildWebServer(cfg config.WebConfig, mgr *Manager, logger *log.Logger) (*WebServer, error) {
	return NewWebServer(cfg, mgr, logger)
}

func discardLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// restartSafeCfg 返回让后台 Restart 快速失败且零监听泄漏的配置：
// Server.Port=-1 使 syncplay Listen 立即报错，tunnel=none、web 关闭、TLS 关闭。
func restartSafeCfg() *config.Config {
	cfg := config.Default()
	cfg.Server.Port = -1
	cfg.Server.TLS = false
	cfg.Tunnel.Type = "none"
	cfg.Web.Enabled = false
	return cfg
}

// syncBuf 是并发安全的日志缓冲：UpdateSettings 会 go Restart()，
// 后台 goroutine 与测试主线程可能同时读写日志。
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestNewWebServerDefaultAddrLoopback(t *testing.T) {
	ws, err := buildWebServer(config.WebConfig{Port: 8080}, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewWebServer(空 Bind) err = %v, want nil", err)
	}
	if got, want := ws.server.Addr, "127.0.0.1:8080"; got != want {
		t.Fatalf("默认 Addr = %q, want %q（必须只绑回环）", got, want)
	}
}

func TestNewWebServerNonLoopbackRequiresToken(t *testing.T) {
	for _, bind := range []string{"0.0.0.0", "::", "192.168.1.5", "example.com"} {
		t.Run(bind, func(t *testing.T) {
			_, err := buildWebServer(config.WebConfig{Bind: bind, Port: 8080}, nil, discardLogger())
			if err == nil {
				t.Fatalf("NewWebServer(Bind=%q, Token 空) err = nil, want 显式错误", bind)
			}
		})
	}
}

func TestNewWebServerLoopbackBindNoTokenOK(t *testing.T) {
	for _, bind := range []string{"", "127.0.0.1", "localhost", "::1"} {
		t.Run("bind="+bind, func(t *testing.T) {
			ws, err := buildWebServer(config.WebConfig{Bind: bind, Port: 8080}, nil, discardLogger())
			if err != nil {
				t.Fatalf("NewWebServer(Bind=%q, Token 空) err = %v, want nil", bind, err)
			}
			if ws == nil {
				t.Fatal("ws = nil, want 非 nil")
			}
		})
	}
}

func TestNewWebServerNonLoopbackWithTokenOK(t *testing.T) {
	ws, err := buildWebServer(config.WebConfig{Bind: "0.0.0.0", Port: 8080, Token: "tok"}, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewWebServer(Bind=0.0.0.0, Token 非空) err = %v, want nil", err)
	}
	if got, want := ws.server.Addr, "0.0.0.0:8080"; got != want {
		t.Fatalf("Addr = %q, want %q", got, want)
	}
}

func TestWebServerTimeouts(t *testing.T) {
	ws, err := buildWebServer(config.WebConfig{Port: 8080}, nil, discardLogger())
	if err != nil {
		t.Fatalf("NewWebServer err = %v", err)
	}
	srv := ws.server
	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout = %v, want > 0", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout <= 0 {
		t.Errorf("ReadTimeout = %v, want > 0", srv.ReadTimeout)
	}
	if srv.WriteTimeout <= 0 {
		t.Errorf("WriteTimeout = %v, want > 0", srv.WriteTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout = %v, want > 0", srv.IdleTimeout)
	}
}

func TestWebAuthWithToken(t *testing.T) {
	const token = "sesame"
	mgr := NewManager(restartSafeCfg(), discardLogger())
	ws, err := buildWebServer(config.WebConfig{Port: 8080, Token: token}, mgr, discardLogger())
	if err != nil {
		t.Fatalf("NewWebServer err = %v", err)
	}

	do := func(t *testing.T, setup func(*http.Request)) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		if setup != nil {
			setup(req)
		}
		rec := httptest.NewRecorder()
		ws.server.Handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("无凭据 401", func(t *testing.T) {
		rec := do(t, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("无凭据 GET /api/status = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
		if wa := rec.Header().Get("WWW-Authenticate"); !strings.Contains(wa, "Basic") {
			t.Errorf("WWW-Authenticate = %q, want 含 Basic", wa)
		}
	})
	t.Run("错误 Basic 密码 401", func(t *testing.T) {
		rec := do(t, func(r *http.Request) { r.SetBasicAuth("user", "wrong") })
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("错 token Basic = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
	t.Run("错误 Bearer 401", func(t *testing.T) {
		rec := do(t, func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") })
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("错 token Bearer = %d, want %d", rec.Code, http.StatusUnauthorized)
		}
	})
	t.Run("正确 Basic 密码 200", func(t *testing.T) {
		rec := do(t, func(r *http.Request) { r.SetBasicAuth("anyone", token) })
		if rec.Code != http.StatusOK {
			t.Fatalf("正确 Basic password = %d, want %d", rec.Code, http.StatusOK)
		}
	})
	t.Run("正确 Bearer 200", func(t *testing.T) {
		rec := do(t, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
		if rec.Code != http.StatusOK {
			t.Fatalf("正确 Bearer = %d, want %d", rec.Code, http.StatusOK)
		}
	})
}

func TestWebNoTokenNoAuth(t *testing.T) {
	mgr := NewManager(restartSafeCfg(), discardLogger())
	ws, err := buildWebServer(config.WebConfig{Port: 8080}, mgr, discardLogger())
	if err != nil {
		t.Fatalf("NewWebServer err = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	ws.server.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Token 为空时无凭据 GET /api/status = %d, want %d（本机默认行为不变）", rec.Code, http.StatusOK)
	}
}

func TestGetSettingsRedactsProxyPassword(t *testing.T) {
	cfg := config.Default()
	cfg.Network.ProxyURL = "socks5://user:sekret@127.0.0.1:1080"
	m := NewManager(cfg, discardLogger())
	resp := m.GetSettings()
	if strings.Contains(resp.ProxyURL, "sekret") {
		t.Errorf("GetSettings().ProxyURL = %q 泄露密码", resp.ProxyURL)
	}
	if !strings.Contains(resp.ProxyURL, "xxxxx") {
		t.Errorf("GetSettings().ProxyURL = %q, want 密码脱敏为 xxxxx", resp.ProxyURL)
	}
}

func TestUpdateSettingsLogRedacted(t *testing.T) {
	buf := &syncBuf{}
	logger := log.New(buf, "", 0)
	m := NewManager(restartSafeCfg(), logger)

	m.UpdateSettings(SettingsRequest{
		TunnelType: "none",
		ProxyURL:   "socks5://user:sekret@127.0.0.1:1080",
	})

	// UpdateSettings 的日志行在 go Restart() 之前同步写出，此时快照必然包含它；
	// 只断言这一行，后台 Restart 的失败日志不影响。
	snapshot := buf.String()
	var target string
	for _, line := range strings.Split(snapshot, "\n") {
		if strings.Contains(line, "设置已更新") {
			target = line
			break
		}
	}
	if target == "" {
		t.Fatalf("未找到「设置已更新」日志行，快照: %q", snapshot)
	}
	if strings.Contains(target, "sekret") {
		t.Errorf("日志泄露密码: %q", target)
	}
	if !strings.Contains(target, "xxxxx") {
		t.Errorf("日志应打印脱敏 proxy（xxxxx），实际: %q", target)
	}
}

func TestUpdateSettingsProxyRoundTrip(t *testing.T) {
	const real = "socks5://user:sekret@127.0.0.1:1080"
	cfg := restartSafeCfg()
	cfg.Network.ProxyURL = real
	m := NewManager(cfg, discardLogger())

	// 1) 提交脱敏形态 → 视为未修改，保留真实值
	m.UpdateSettings(SettingsRequest{TunnelType: "none", ProxyURL: "socks5://user:xxxxx@127.0.0.1:1080"})
	if cfg.Network.ProxyURL != real {
		t.Fatalf("提交脱敏串后 ProxyURL = %q, want 保留真实值 %q", cfg.Network.ProxyURL, real)
	}

	// 2) 提交新值 → 更新
	const newURL = "socks5://user:newpw@127.0.0.1:1080"
	m.UpdateSettings(SettingsRequest{TunnelType: "none", ProxyURL: newURL})
	if cfg.Network.ProxyURL != newURL {
		t.Fatalf("提交新值后 ProxyURL = %q, want %q", cfg.Network.ProxyURL, newURL)
	}

	// 3) 提交空 → 清空
	m.UpdateSettings(SettingsRequest{TunnelType: "none", ProxyURL: ""})
	if cfg.Network.ProxyURL != "" {
		t.Fatalf("提交空串后 ProxyURL = %q, want 空（清空）", cfg.Network.ProxyURL)
	}
}

// redactProxyURL 是新符号，RED 阶段无法编译引用（其缺陷行为已由
// GetSettings/UpdateSettings 的 RED 测试覆盖），本表格测试在 GREEN 阶段补充。
func TestRedactProxyURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"空串", "", ""},
		{"带密码", "socks5://user:sekret@127.0.0.1:1080", "socks5://user:xxxxx@127.0.0.1:1080"},
		{"有用户名无密码", "socks5://user@127.0.0.1:1080", "socks5://user@127.0.0.1:1080"},
		{"无 userinfo", "socks5://127.0.0.1:1080", "socks5://127.0.0.1:1080"},
		{"http 带密码", "http://u:p@proxy.example:8080", "http://u:xxxxx@proxy.example:8080"},
		{"解析失败含@", "socks5://us er:pw@ho st:1080", "<redacted>"},
		{"解析失败不含@", "://bad url", "://bad url"},
		// I1：无 scheme 时 url.Parse 把 "user:" 当 scheme、u.User 为 nil，
		// 密码不可被原样放行——含 @ 且未捕获 userinfo 必须全遮。
		{"无 scheme 含密码", "user:pw@host:port", "<redacted>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactProxyURL(tc.in); got != tc.want {
				t.Errorf("redactProxyURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
