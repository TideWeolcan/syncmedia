package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// syncmediaEnvVars 是 applyEnv 会读取的全部环境变量。
var syncmediaEnvVars = []string{
	"SYNCMEDIA_SERVER_PORT",
	"SYNCMEDIA_TLS",
	"SYNCMEDIA_SERVER_PASSWORD",
	"SYNCMEDIA_TUNNEL_TYPE",
	"SYNCMEDIA_BORE_RELAY",
	"SYNCMEDIA_FRP_SERVER",
	"SYNCMEDIA_WEB_PORT",
	"SYNCMEDIA_WEB_BIND",
	"SYNCMEDIA_WEB_TOKEN",
	"SYNCMEDIA_PROXY",
	"SYNCMEDIA_NO_PROXY",
	"SYNCMEDIA_BIND_INTERFACE",
}

// clearSyncmediaEnv 把全部 SYNCMEDIA_* 变量设为空串（applyEnv 跳过空串），
// 避免外部环境污染测试；t.Setenv 会在测试结束后恢复原值。
func clearSyncmediaEnv(t *testing.T) {
	t.Helper()
	for _, k := range syncmediaEnvVars {
		t.Setenv(k, "")
	}
}

func writeTempYAML(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}

func TestDefaultWebBindLoopback(t *testing.T) {
	cfg := Default()
	if cfg.Web.Bind != "127.0.0.1" {
		t.Fatalf("Default().Web.Bind = %q, want %q", cfg.Web.Bind, "127.0.0.1")
	}
}

// TestDefaultPortsUnset：默认端口为 0（未设定），由启动时的自动避让逻辑决定。
func TestDefaultPortsUnset(t *testing.T) {
	cfg := Default()
	if cfg.Server.Port != 0 {
		t.Errorf("Default().Server.Port = %d, want 0（未设定，走自动避让）", cfg.Server.Port)
	}
	if cfg.Web.Port != 0 {
		t.Errorf("Default().Web.Port = %d, want 0（未设定，走自动避让）", cfg.Web.Port)
	}
}

func TestLoadUnknownFieldError(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"未知顶层字段", "server:\n  port: 8999\nwebz:\n  port: 1\n"},
		{"嵌套拼错字段", "server:\n  prot: 8999\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearSyncmediaEnv(t)
			path := writeTempYAML(t, tc.yaml)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load(%q) err = nil, want 未知字段错误", tc.yaml)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("错误信息应包含文件路径 %q，实际: %v", path, err)
			}
		})
	}
}

func TestLoadEmptyFileEqualsDefaults(t *testing.T) {
	clearSyncmediaEnv(t)
	path := writeTempYAML(t, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(空文件) err = %v, want nil", err)
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("Load(空文件) = %+v, want Default() = %+v", cfg, Default())
	}
}

func TestLoadValidAllKnownFields(t *testing.T) {
	clearSyncmediaEnv(t)
	// 与根目录 config.yaml 相同的字段集，另加 web.bind / web.token / server.bind。
	path := writeTempYAML(t, `
server:
  port: 9111
  tls: false
  password: "pw"
  bind: "192.168.1.5"
tunnel:
  type: frp
  bore:
    relay: "relay.example"
    secret: "bore-secret"
  frp:
    server: "frp.example"
    port: 7100
    token: "frptok"
    remote_port: 9200
web:
  port: 9080
  enabled: false
  bind: "127.0.0.1"
  token: "webtok"
network:
  proxy_url: "socks5://127.0.0.1:1080"
  no_proxy: true
  bind_interface: "wlan0"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load(全字段合法 yaml) err = %v, want nil", err)
	}
	if cfg.Server.Port != 9111 || cfg.Server.TLS || cfg.Server.Password != "pw" || cfg.Server.Bind != "192.168.1.5" {
		t.Errorf("Server = %+v, want port=9111 tls=false password=pw bind=192.168.1.5", cfg.Server)
	}
	if cfg.Tunnel.Type != "frp" || cfg.Tunnel.Bore.Relay != "relay.example" || cfg.Tunnel.Bore.Secret != "bore-secret" || cfg.Tunnel.FRP.RemotePort != 9200 {
		t.Errorf("Tunnel = %+v, want type=frp relay=relay.example secret=bore-secret remote_port=9200", cfg.Tunnel)
	}
	if cfg.Web.Port != 9080 || cfg.Web.Enabled || cfg.Web.Bind != "127.0.0.1" || cfg.Web.Token != "webtok" {
		t.Errorf("Web = %+v, want port=9080 enabled=false bind=127.0.0.1 token=webtok", cfg.Web)
	}
	if cfg.Network.ProxyURL != "socks5://127.0.0.1:1080" || !cfg.Network.NoProxy || cfg.Network.BindInterface != "wlan0" {
		t.Errorf("Network = %+v, want proxy/no_proxy/bind_interface 全生效", cfg.Network)
	}
}

func TestLoadEnvInvalidPortError(t *testing.T) {
	cases := []struct {
		envVar string
		value  string
	}{
		{"SYNCMEDIA_SERVER_PORT", "abc"},
		{"SYNCMEDIA_WEB_PORT", "xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.envVar, func(t *testing.T) {
			clearSyncmediaEnv(t)
			t.Setenv(tc.envVar, tc.value)
			_, err := Load("")
			if err == nil {
				t.Fatalf("Load() with %s=%q err = nil, want error", tc.envVar, tc.value)
			}
			if !strings.Contains(err.Error(), tc.envVar) {
				t.Errorf("错误信息应包含变量名 %s，实际: %v", tc.envVar, err)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Errorf("错误信息应包含非法值 %q，实际: %v", tc.value, err)
			}
		})
	}
}

func TestLoadEnvInvalidBoolError(t *testing.T) {
	cases := []struct {
		envVar string
		value  string
	}{
		{"SYNCMEDIA_TLS", "yes"},
		{"SYNCMEDIA_NO_PROXY", "maybe"},
	}
	for _, tc := range cases {
		t.Run(tc.envVar, func(t *testing.T) {
			clearSyncmediaEnv(t)
			t.Setenv(tc.envVar, tc.value)
			_, err := Load("")
			if err == nil {
				t.Fatalf("Load() with %s=%q err = nil, want error", tc.envVar, tc.value)
			}
			if !strings.Contains(err.Error(), tc.envVar) {
				t.Errorf("错误信息应包含变量名 %s，实际: %v", tc.envVar, err)
			}
		})
	}
}

func TestLoadEnvValidValues(t *testing.T) {
	t.Run("SYNCMEDIA_TLS=1", func(t *testing.T) {
		clearSyncmediaEnv(t)
		t.Setenv("SYNCMEDIA_TLS", "1")
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if !cfg.Server.TLS {
			t.Errorf("SYNCMEDIA_TLS=1 → TLS = false, want true")
		}
	})
	t.Run("SYNCMEDIA_TLS=false", func(t *testing.T) {
		clearSyncmediaEnv(t)
		t.Setenv("SYNCMEDIA_TLS", "false")
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.Server.TLS {
			t.Errorf("SYNCMEDIA_TLS=false → TLS = true, want false")
		}
	})
	t.Run("ports", func(t *testing.T) {
		clearSyncmediaEnv(t)
		t.Setenv("SYNCMEDIA_SERVER_PORT", "9001")
		t.Setenv("SYNCMEDIA_WEB_PORT", "9002")
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if cfg.Server.Port != 9001 {
			t.Errorf("Server.Port = %d, want 9001", cfg.Server.Port)
		}
		if cfg.Web.Port != 9002 {
			t.Errorf("Web.Port = %d, want 9002", cfg.Web.Port)
		}
	})
	t.Run("SYNCMEDIA_NO_PROXY=1", func(t *testing.T) {
		clearSyncmediaEnv(t)
		t.Setenv("SYNCMEDIA_NO_PROXY", "1")
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("Load() err = %v", err)
		}
		if !cfg.Network.NoProxy {
			t.Errorf("SYNCMEDIA_NO_PROXY=1 → NoProxy = false, want true")
		}
	})
}

func TestApplyEnvWebBindToken(t *testing.T) {
	clearSyncmediaEnv(t)
	t.Setenv("SYNCMEDIA_WEB_BIND", "0.0.0.0")
	t.Setenv("SYNCMEDIA_WEB_TOKEN", "envtok")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() err = %v", err)
	}
	if cfg.Web.Bind != "0.0.0.0" {
		t.Errorf("SYNCMEDIA_WEB_BIND → Web.Bind = %q, want %q", cfg.Web.Bind, "0.0.0.0")
	}
	if cfg.Web.Token != "envtok" {
		t.Errorf("SYNCMEDIA_WEB_TOKEN → Web.Token = %q, want %q", cfg.Web.Token, "envtok")
	}
}

// TestLoadInvalidTunnelTypeError：tunnel.type 非法值（YAML 或环境变量）
// 必须显式报错，而不是静默回落 bore。
func TestLoadInvalidTunnelTypeError(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		env  string
	}{
		{"yaml 非法值", "tunnel:\n  type: bogus\n", ""},
		{"环境变量非法值", "", "bogus"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearSyncmediaEnv(t)
			path := ""
			if tc.yaml != "" {
				path = writeTempYAML(t, tc.yaml)
			}
			if tc.env != "" {
				t.Setenv("SYNCMEDIA_TUNNEL_TYPE", tc.env)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load() err = nil, want 隧道类型校验错误")
			}
			if !strings.Contains(err.Error(), "tunnel.type") {
				t.Errorf("错误信息应包含 tunnel.type，实际: %v", err)
			}
		})
	}
}

func TestLoadValidTunnelTypes(t *testing.T) {
	for _, typ := range []string{"bore", "frp", "none"} {
		t.Run(typ, func(t *testing.T) {
			clearSyncmediaEnv(t)
			path := writeTempYAML(t, fmt.Sprintf("tunnel:\n  type: %s\n", typ))
			if _, err := Load(path); err != nil {
				t.Fatalf("Load(type=%s) err = %v, want nil", typ, err)
			}
		})
	}
}

// TestConfigExampleFieldsMatchStruct：解析 config.example.yaml 注释中的字段路径
// （如 "server.port"、"tunnel.bore.relay"），用反射验证每个路径都真实存在于
// Config 结构，防止示例文件与代码结构漂移。
func TestConfigExampleFieldsMatchStruct(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("读取 config.example.yaml 失败: %v", err)
	}
	re := regexp.MustCompile(`(?m)^#\s*([a-zA-Z_][a-zA-Z0-9_.]*):`)
	seen := map[string]bool{}
	matches := re.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		t.Fatal("config.example.yaml 中未找到任何字段路径注释")
	}
	for _, m := range matches {
		path := m[1]
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := resolveFieldPath(reflect.TypeOf(Config{}), path); err != nil {
			t.Errorf("config.example.yaml 字段 %q: %v", path, err)
		}
	}
}

// resolveFieldPath 沿 "." 分隔的路径在结构体上逐级解析（按 yaml tag 或字段名匹配）。
func resolveFieldPath(t reflect.Type, path string) error {
	parts := strings.Split(path, ".")
	cur := t
	for _, part := range parts {
		for cur.Kind() == reflect.Pointer {
			cur = cur.Elem()
		}
		if cur.Kind() != reflect.Struct {
			return fmt.Errorf("路径 %q 的 %q 不是结构体", path, part)
		}
		found := false
		for i := 0; i < cur.NumField(); i++ {
			f := cur.Field(i)
			tagName := strings.Split(f.Tag.Get("yaml"), ",")[0]
			if tagName == part || (tagName == "" && f.Name == part) {
				cur = f.Type
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("结构体 %s 没有字段 %q", cur.Name(), part)
		}
	}
	return nil
}
