# SyncMedia — AI Agent 指南

## 项目概述

SyncMedia 是一个 Syncplay 1.7.5 协议兼容的媒体同步服务器，纯 Go 实现。
主要功能：多人远程同步观影，内置隧道穿透（bore / frp 原生 Go 实现），嵌入式 WebUI 仪表盘。
配合 [Kazumi](https://github.com/Kazumi-Team/Kazumi) 等 Syncplay 客户端使用。

## 技术栈

- **语言**：Go 1.25+（CGO_ENABLED=0 静态链接）
- **协议**：Syncplay 1.7.5（JSON 单行消息，TLS 可选）
- **TLS**：国密 SM2 + ECDSA 双证书，内存自签名（`github.com/tjfoc/gmsm`）
- **隧道**：bore 原生 Go 实现 / frp（`github.com/fatedier/frp` Go 库）
- **配置**：`gopkg.in/yaml.v3`（strict mode）
- **CI**：GitHub Actions（test/lint/vulncheck/build/build-apk/build-ksu/release）
- **发布**：GoReleaser

## 目录结构

```
cmd/syncmedia/          CLI 入口（start / version 子命令）
internal/
├── config/             配置加载与校验（YAML + 环境变量 + CLI 覆盖）
├── manager/            服务编排（生命周期、WebUI、REST API、Restart 互斥）
├── syncplay/           Syncplay 协议实现（Server、Room、RoomManager、State、TLS）
└── tunnel/             隧道抽象层（Tunnel 接口、Manager、bore_native、frp_native、Dialer）
pkg/version/            版本信息（ldflags 注入：Version/Commit/Date）
android/                Android APK 源码（Java: MainActivity + SyncService）
kernelsu/               KernelSU/Magisk 模块（module.prop + service.sh）
scripts/                构建与验证脚本
docs/                   技术文档（TECHNICAL.md、CI-TROUBLESHOOTING.md）
```

## 构建命令

```bash
# 前置：Go 1.25+

# Linux 本地构建（当前架构）
CGO_ENABLED=0 go build -o syncmedia ./cmd/syncmedia

# 多架构 Linux 打包（amd64 + arm64 → dist/linux/*.tar.gz）
./scripts/build-linux.sh

# Android APK（需 Android SDK + JDK）
./scripts/build-apk.sh

# KernelSU/Magisk 模块
./scripts/build-ksu.sh

# GoReleaser 全平台发布
goreleaser release --clean
```

版本号通过 ldflags 注入：
```
-X github.com/TideWeolcan/syncmedia/pkg/version.Version=...
-X github.com/TideWeolcan/syncmedia/pkg/version.Commit=...
-X github.com/TideWeolcan/syncmedia/pkg/version.Date=...
```

## 测试

```bash
# 运行全部测试（推荐加 -race，需 CGO_ENABLED=1）
CGO_ENABLED=1 go test ./... -race -count=1

# 不加 race（CGO_ENABLED=0 也能跑）
go test ./... -count=1

# 静态分析
go vet ./...
```

测试全部监听 `127.0.0.1:0`，无公网依赖。覆盖范围：

| 包 | 关键测试 |
|----|---------|
| `cmd/syncmedia` | usage 输出包含所有 flag |
| `internal/config` | strict YAML 解码、环境变量覆盖、默认值 |
| `internal/manager` | WebUI JS 解析、安全策略、Status 实时、并发重启、有界停机 |
| `internal/syncplay` | 协议全语义（join/play/pause/seek/后加入/断线/重复Hello/并发hammer） |
| `internal/tunnel` | 状态机转换、断线清地址、退避重连、fake bore/frp |

## CI 流水线

定义于 `.github/workflows/ci.yml`，触发条件：push/PR 到 main/develop，tag `v*` 触发 release。

| Job | 内容 |
|-----|------|
| test | Go 1.25，`go test -race`，ubuntu/windows/macos 三平台 |
| lint | golangci-lint |
| vulncheck | govulncheck |
| build | 多平台构建 + `syncmedia version` smoke test |
| build-apk | APK 构建验证（非 tag 时触发） |
| build-ksu | KSU 模块构建验证（非 tag 时触发） |
| release | GoReleaser + APK + KSU 模块（仅 tag 触发） |

## 配置系统

优先级（低 → 高）：默认值 → `config.yaml` → 环境变量 (`SYNCMEDIA_*`) → 命令行参数。

YAML 解析使用 `KnownFields(true)`：未知字段会报错，防止拼写错误被静默忽略。

## 代码风格与约定

- 注释和用户可见文本使用中文
- 代码标识符、commit 消息使用英文
- 错误信息使用中文（面向终端用户）
- 导出类型有英文 doc comment
- 锁序严格：`RoomManager.mu → Room.mu`，`Manager.restartMu` 串行化 Restart
- 位置/状态使用指针类型区分"未提供"与"零值"
- 隧道状态机：`starting → ready → reconnecting → degraded → stopped`

## 安全模型

| 层 | 措施 |
|----|------|
| 传输 | STARTTLS（SM2 + ECDSA 双证书，内存自签名，不落盘） |
| WebUI | 默认绑定 127.0.0.1；非回环绑定强制 token 认证 |
| API 认证 | Basic Auth / Bearer Token，恒定时间比较 |
| 敏感数据 | API 响应中 proxy_url/password 等字段脱敏为 `***` |
| HTTP | ReadTimeout 15s / WriteTimeout 30s / IdleTimeout 60s / ReadHeaderTimeout 5s |
| 并发 | Restart 互斥锁；Room 锁序强制；连接注册/注销原子化 |
| YAML | strict 模式，环境变量解析错误显式报错 |

## 运行方式

```bash
# 默认启动（bore 隧道 + TLS + WebUI :8080）
./syncmedia start

# 指定配置
./syncmedia start --config /path/to/config.yaml

# 禁用隧道
./syncmedia start --tunnel none

# 查看版本
./syncmedia version
```

## 部署目标

| 产物 | 平台 | 说明 |
|------|------|------|
| `syncmedia` 二进制 | linux/amd64, linux/arm64 | 静态链接，直接运行 |
| `syncmedia.apk` | Android arm64-v8a (min SDK 29) | Go 二进制作为 native lib 嵌入 |
| `syncmedia-ksu.zip` | Android arm64 | KernelSU/Magisk 模块，开机自启 |
| GoReleaser 产物 | linux+windows, amd64+arm64 | 自动发布到 GitHub Releases |

## 已知限制

- `-race` 在 39-bit VMA 内核（部分 Android/arm64）不可用（TSan 需 48-bit）
- FRP 数据面测试需联网（当前仅 fake 测试）
- Android 实机行为（LMK、START_STICKY）无法在 CI 验证
