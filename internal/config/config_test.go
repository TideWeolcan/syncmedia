package config

import (
	"os"
	"path/filepath"
	"reflect"
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
	// 与根目录 config.yaml 相同的字段集，另加 web.bind / web.token。
	path := writeTempYAML(t, `
server:
  port: 9111
  tls: false
  password: "pw"
tunnel:
  type: frp
  bore:
    relay: "relay.example"
    binary: "/opt/bore"
  frp:
    server: "frp.example"
    port: 7100
    token: "frptok"
    remote_port: 9200
    binary: "/opt/frpc"
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
	if cfg.Server.Port != 9111 || cfg.Server.TLS || cfg.Server.Password != "pw" {
		t.Errorf("Server = %+v, want port=9111 tls=false password=pw", cfg.Server)
	}
	if cfg.Tunnel.Type != "frp" || cfg.Tunnel.Bore.Relay != "relay.example" || cfg.Tunnel.FRP.RemotePort != 9200 {
		t.Errorf("Tunnel = %+v, want type=frp relay=relay.example remote_port=9200", cfg.Tunnel)
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
