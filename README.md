# SyncMedia

[![CI](https://github.com/TideWeolcan/syncmedia/actions/workflows/ci.yml/badge.svg)](https://github.com/TideWeolcan/syncmedia/actions/workflows/ci.yml)

Syncplay 兼容的媒体同步服务器，内置隧道穿透（bore / frp）与嵌入式 WebUI，纯 Go 实现。

配合 [Kazumi](https://github.com/Kazumi-Team/Kazumi) 等支持 Syncplay 协议的客户端使用，可实现多人远程同步观影。

## 功能

- Syncplay 1.7.5 协议（TLS / 明文可选）
- 内置 bore / frp 隧道，零配置公网穿透
- 嵌入式 WebUI 仪表盘（状态查看、设置修改、一键重启）
- 国密 SM2 + ECDSA 双证书自动协商
- 隧道健康状态机（starting → ready → degraded → reconnecting → stopped）
- Android APK / KernelSU 模块 / Linux 多平台支持

## 快速开始

```bash
# 默认启动：端口 8999 + bore 隧道 + WebUI :8080
./syncmedia start

# 指定配置
./syncmedia start --config /path/to/config.yaml

# 禁用隧道，仅本地
./syncmedia start --tunnel none

# 查看版本
./syncmedia version
```

启动后访问 `http://127.0.0.1:8080` 查看公网地址和服务状态。

## 安装

### Linux

从 [Releases](https://github.com/TideWeolcan/syncmedia/releases) 下载对应架构的压缩包，解压后运行：

```bash
tar xzf syncmedia-linux-arm64.tar.gz
cd syncmedia
./start.sh
```

### Android APK

下载 `syncmedia.apk` 安装，打开即自动启动服务。

### KernelSU / Magisk 模块

下载 `syncmedia-ksu.zip`，通过 KernelSU 或 Magisk 刷入，开机自动启动。

## 配置

配置优先级：默认值 < `config.yaml` < 环境变量 (`SYNCMEDIA_*`) < 命令行参数。

### config.yaml

```yaml
server:
  port: 8999              # Syncplay 监听端口
  tls: true               # 启用 STARTTLS（Kazumi 需要）
  password: ""            # 房间密码（留空=无密码）

tunnel:
  type: bore              # bore | frp | none
  bore:
    relay: "bore.pub"     # bore 中继服务器
    binary: ""            # bore 二进制路径（留空=原生实现）
  frp:
    server: ""            # frps 服务器地址
    port: 7000            # frps 端口
    token: ""             # frp 认证 token
    remote_port: 0        # 远程端口（0=自动分配）
    binary: ""            # frpc 路径（留空=原生实现）

web:
  port: 8080              # WebUI 端口
  enabled: true           # 启用 WebUI
  bind: "127.0.0.1"      # 绑定地址（默认仅本机）
  token: ""              # 访问令牌（非本机绑定时必填）

network:
  proxy_url: ""           # HTTP/SOCKS5 代理
  no_proxy: false         # 绕过系统代理
  bind_interface: ""      # 绑定网卡（绕过 VPN）
```

### 环境变量

所有配置均可通过 `SYNCMEDIA_` 前缀的环境变量覆盖：

| 环境变量 | 对应配置 |
|---------|---------|
| `SYNCMEDIA_SERVER_PORT` | server.port |
| `SYNCMEDIA_TLS` | server.tls |
| `SYNCMEDIA_SERVER_PASSWORD` | server.password |
| `SYNCMEDIA_TUNNEL_TYPE` | tunnel.type |
| `SYNCMEDIA_BORE_RELAY` | tunnel.bore.relay |
| `SYNCMEDIA_FRP_SERVER` | tunnel.frp.server |
| `SYNCMEDIA_WEB_PORT` | web.port |
| `SYNCMEDIA_WEB_BIND` | web.bind |
| `SYNCMEDIA_WEB_TOKEN` | web.token |
| `SYNCMEDIA_PROXY` | network.proxy_url |
| `SYNCMEDIA_NO_PROXY` | network.no_proxy |
| `SYNCMEDIA_BIND_INTERFACE` | network.bind_interface |

### 命令行参数

```
syncmedia start [flags]

Flags:
  --config <path>          配置文件路径
  --port <int>             Syncplay 端口
  --web-port <int>         WebUI 端口
  --no-tls                 禁用 TLS
  --tunnel <type>          隧道类型 (bore/frp/none)
  --bore-binary <path>     bore 二进制路径
  --frp-server <addr>      frp 服务器地址
  --proxy <url>            网络代理
  --bind-interface <name>  绑定网卡
  --no-proxy               绕过系统代理
```

## WebUI

嵌入式管理面板，提供：

- 服务状态（运行时长、公网地址、隧道状态）
- 在线设置修改（保存自动重启生效）
- 一键重启

安全策略：
- 默认绑定 `127.0.0.1`（仅本机访问）
- 非本机绑定时必须配置 `web.token`
- 认证方式：Basic Auth 密码或 Bearer Token
- 秘密字段（代理、密码等）在 API 响应中脱敏

## 构建

需要 Go 1.25+。

```bash
# Linux
CGO_ENABLED=0 go build -o syncmedia ./cmd/syncmedia

# 多架构打包
./scripts/build-linux.sh

# Android APK
./scripts/build-apk.sh

# KernelSU 模块
./scripts/build-ksu.sh

# APK 静态检查
./scripts/verify-apk.sh <apk-path>
```

## 测试

```bash
go test ./... -count=1
go vet ./...
```

测试覆盖：Syncplay 协议黑盒测试（13 项）、隧道状态机测试（12 项）、WebUI 安全测试、配置解析测试、并发重启测试、有界停机测试。

## 许可证

MIT

## 分支策略

- **main**：稳定发布分支，仅接受经过 CI 验证的合并
- **develop**：活跃开发分支，日常开发在此进行

## 贡献

1. Fork 本仓库
2. 基于 `develop` 分支创建功能分支
3. 提交 PR 到 `develop`（commit 消息使用英文，格式：`type: description`）
4. 等待 CI 通过后合并

## CI / 故障排查

CI 流水线定义于 `.github/workflows/ci.yml`，包含 test、lint、vulncheck、build、build-apk、build-ksu、release 七个 job。

遇到 CI 失败请参阅 [故障排除指南](docs/CI-TROUBLESHOOTING.md)。
