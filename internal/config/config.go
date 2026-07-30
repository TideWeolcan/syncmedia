package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for syncmedia.
type NetworkConfig struct {
	ProxyURL      string `yaml:"proxy_url"`       // HTTP/SOCKS5 代理地址 (如 socks5://127.0.0.1:1080)
	NoProxy       bool   `yaml:"no_proxy"`        // 绕过系统代理
	BindInterface string `yaml:"bind_interface"`  // 绑定网卡绕过 VPN (如 wlan0)
}

type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Tunnel  TunnelConfig  `yaml:"tunnel"`
	Web     WebConfig     `yaml:"web"`
	Network NetworkConfig `yaml:"network"`
}

type ServerConfig struct {
	Port     int    `yaml:"port"`
	TLS      bool   `yaml:"tls"`
	Password string `yaml:"password"`
}

type TunnelConfig struct {
	Type string    `yaml:"type"` // bore | frp | none
	Bore BoreCfg   `yaml:"bore"`
	FRP  FRPCfg    `yaml:"frp"`
}

type BoreCfg struct {
	Relay  string `yaml:"relay"`
	Binary string `yaml:"binary"`
}

type FRPCfg struct {
	Server     string `yaml:"server"`
	Port       int    `yaml:"port"`
	Token      string `yaml:"token"`
	RemotePort int    `yaml:"remote_port"`
	Binary     string `yaml:"binary"`
}

type WebConfig struct {
	Port    int    `yaml:"port"`
	Enabled bool   `yaml:"enabled"`
	Bind    string `yaml:"bind"`  // Web UI 绑定地址（默认 127.0.0.1，仅本机）
	Token   string `yaml:"token"` // Web UI 访问令牌（非本机绑定时必填）
}

// Default returns the default configuration.
func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Port:    8999,
			TLS:     true,
		},
		Tunnel: TunnelConfig{
			Type: "bore",
			Bore: BoreCfg{
				Relay:  "bore.pub",
				Binary: "",
			},
			FRP: FRPCfg{
				Port: 7000,
			},
		},
		Web: WebConfig{
			Port:    8080,
			Enabled: true,
			Bind:    "127.0.0.1",
		},
	}
}

// Load reads config from a YAML file, then applies CLI overrides.
// If path is empty, returns defaults.
func Load(path string) (*Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取配置 %s: %w", path, err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true) // 未知/拼错字段显式报错
		if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
			// 空文件（io.EOF）等同于纯默认值
			return nil, fmt.Errorf("解析配置 %s: %w", path, err)
		}
	}

	// Apply environment variable overrides
	if err := applyEnv(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// applyEnv overrides config values from SYNCMEDIA_* environment variables.
// 非法值（端口非数字、布尔无法解析）返回点名变量与值的显式错误。
func applyEnv(cfg *Config) error {
	if v := os.Getenv("SYNCMEDIA_SERVER_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("环境变量 SYNCMEDIA_SERVER_PORT 无效: %q 不是数字", v)
		}
		cfg.Server.Port = port
	}
	if v := os.Getenv("SYNCMEDIA_TLS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("环境变量 SYNCMEDIA_TLS 无效: %q 不是布尔值（可用 true/false/1/0）", v)
		}
		cfg.Server.TLS = b
	}
	if v := os.Getenv("SYNCMEDIA_SERVER_PASSWORD"); v != "" {
		cfg.Server.Password = v
	}
	if v := os.Getenv("SYNCMEDIA_TUNNEL_TYPE"); v != "" {
		cfg.Tunnel.Type = v
	}
	if v := os.Getenv("SYNCMEDIA_BORE_RELAY"); v != "" {
		cfg.Tunnel.Bore.Relay = v
	}
	if v := os.Getenv("SYNCMEDIA_FRP_SERVER"); v != "" {
		cfg.Tunnel.FRP.Server = v
	}
	if v := os.Getenv("SYNCMEDIA_WEB_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("环境变量 SYNCMEDIA_WEB_PORT 无效: %q 不是数字", v)
		}
		cfg.Web.Port = port
	}
	if v := os.Getenv("SYNCMEDIA_WEB_BIND"); v != "" {
		cfg.Web.Bind = v
	}
	if v := os.Getenv("SYNCMEDIA_WEB_TOKEN"); v != "" {
		cfg.Web.Token = v
	}
	if v := os.Getenv("SYNCMEDIA_PROXY"); v != "" {
		cfg.Network.ProxyURL = v
	}
	if v := os.Getenv("SYNCMEDIA_NO_PROXY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("环境变量 SYNCMEDIA_NO_PROXY 无效: %q 不是布尔值（可用 true/false/1/0）", v)
		}
		cfg.Network.NoProxy = b
	}
	if v := os.Getenv("SYNCMEDIA_BIND_INTERFACE"); v != "" {
		cfg.Network.BindInterface = v
	}
	return nil
}

// ApplyCLIOverrides applies command-line flag overrides onto the config.
func (cfg *Config) ApplyCLIOverrides(serverPort, webPort int, tlsEnabled *bool, tunnelType, boreBinary, frpServer, proxyURL, bindInterface string, noProxy bool) {
	if serverPort > 0 {
		cfg.Server.Port = serverPort
	}
	if webPort > 0 {
		cfg.Web.Port = webPort
	}
	if tlsEnabled != nil {
		cfg.Server.TLS = *tlsEnabled
	}
	if tunnelType != "" {
		cfg.Tunnel.Type = tunnelType
	}
	if boreBinary != "" {
		cfg.Tunnel.Bore.Binary = boreBinary
	}
	if frpServer != "" {
		cfg.Tunnel.FRP.Server = frpServer
	}
	if proxyURL != "" {
		cfg.Network.ProxyURL = proxyURL
	}
	if bindInterface != "" {
		cfg.Network.BindInterface = bindInterface
	}
	if noProxy {
		cfg.Network.NoProxy = true
	}
}
