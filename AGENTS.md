# SyncMedia — AI Agent 指南

## 项目概述

SyncMedia 是一个 Syncplay 1.7.5 协议兼容的媒体同步服务器，纯 Go 实现，单二进制运行。
主要功能：多人远程同步观影，内置隧道穿透（bore / frp），嵌入式 WebUI 仪表盘，自动端口避让。
配合 [Kazumi](https://github.com/Kazumi-Team/Kazumi) 等 Syncplay 客户端使用。

**平台**：仅 Linux（x86_64 / arm64）。Android / Magisk / Windows 版本在独立分仓仓库维护，本仓库不含相关代码。

## 技术栈

- **语言**：Go 1.25+（CGO_ENABLED=0 静态链接）
- **协议**：Syncplay 1.7.5（JSON 单行消息，STARTTLS 可选）
- **TLS**：国密 SM2 + ECDSA 双证书，内存自签名（`github.com/tjfoc/gmsm`）
- **隧道**：bore 原生 Go 实现 / frp（`github.com/fatedier/frp` Go 库）
- **配置**：`gopkg.in/yaml.v3`（strict mode）
- **CI**：GitHub Actions（test/lint/vulncheck/build/release）
- **发布**：GoReleaser（仅 linux）

## 目录结构

```
cmd/syncmedia/          CLI 入口（start / version 子命令）
internal/
├── config/             配置加载与校验（YAML + 环境变量 + CLI 覆盖）+ 端口避让（ports.go）
├── manager/            服务编排（生命周期、WebUI、REST API、Restart 互斥）
├── syncplay/           Syncplay 协议实现（Server、Room、RoomManager、State、TLS）
└── tunnel/             隧道抽象层（Tunnel 接口、Manager、bore_native、frp_native、Dialer）
pkg/version/            版本信息（ldflags 注入：Version/Commit/Date）
scripts/                构建脚本（build-linux.sh：amd64 + arm64 打包）
install.sh / uninstall.sh / syncmedia_ctl    根目录安装 / 卸载 / 服务控制脚本
```

## 构建与测试

```bash
# 前置：Go 1.25+
CGO_ENABLED=0 go build -o syncmedia ./cmd/syncmedia   # 本地构建
go test ./... -count=1                                 # 全部测试
go vet ./...                                           # 静态分析
./scripts/build-linux.sh                               # 多架构打包（dist/linux/*.tar.gz）
```

- 测试全部监听 `127.0.0.1:0`，无公网依赖
- `-race` 在 39-bit VMA 内核（部分 Android/arm64）不可用，如需 race 用 `CGO_ENABLED=1 go test ./... -race`

## 配置系统

优先级（低 → 高）：默认值 → `config.yaml` → 环境变量（`SYNCMEDIA_*`）→ 命令行参数。

- YAML 解析使用 `KnownFields(true)`：未知字段显式报错，防止拼写错误被静默忽略
- `config.example.yaml` 是"全注释"精简版：所有设置都有默认值，取消注释即启用
- **端口默认 `0` = 未设定**（走自动避让），不是固定用 8999/8080
- 环境变量解析错误（端口非数字、布尔无法解析）显式报错并点名变量

## 端口机制（自动避让）

### 内置首选端口

`internal/config/ports.go`：

```go
PreferredSyncplayPort = 8999  // Syncplay 监听端口首选
PreferredWebPort      = 8080  // WebUI 端口首选
```

### ResolvePort 策略

| configured | 行为 |
|-----------|------|
| `> 0`（用户显式设定） | 优先使用设定值；被占 → 随机避让 |
| `< 0`（非法配置） | 原样返回，交给真实监听报错（测试用 -1） |
| `== 0`（未设定） | 尝试上次记住的端口（无则内置首选）；被占 → 随机避让 |

- 避让区间 `1024-65535`，随机选空闲端口，且排除常见服务端口表 `commonServicePorts`（22/53/80/443/3306/5432/6379/27017/8080/9090 等，见 `internal/config/ports.go`）
- 避让结果写入 `data/ports.json`（`PortState{SyncplayPort, WebPort}`），重启沿用，端口不漂移
- `DataDir`：`SYNCMEDIA_DATA_DIR` 环境变量优先，否则为可执行文件所在目录下的 `data/`
- 实际端口通过 status / 启动日志 / WebUI 展示，带"自动避让"标注（manager 的 `portNote`）

### 与隧道的区别

端口避让只管**本地监听端口**。隧道公网端口：bore 由中继随机分配（不可控）；frp 由 `tunnel.frp.remote_port` 指定。

## 安全模型

| 层 | 措施 |
|----|------|
| 传输 | STARTTLS（SM2 + ECDSA 双证书，内存自签名，不落盘） |
| WebUI 绑定 | 默认绑定 127.0.0.1；非回环绑定强制 token 认证 |
| WebUI Host 校验 | 回环绑定时校验 Host 头，拒绝 DNS rebinding（攻击者域名解析到 127.0.0.1 也无法读写 API） |
| XSS | WebUI 对用户可控字段（proxyUrl 等）转义后再嵌入 HTML |
| API 认证 | Basic Auth / Bearer Token，恒定时间比较 |
| 敏感数据 | API 响应中 proxy_url/password 等字段脱敏为 `***` |
| HTTP | ReadTimeout 15s / WriteTimeout 30s / IdleTimeout 60s / ReadHeaderTimeout 5s |
| 隧道 | 拨号握手设 deadline（10s，ctx 更早则取 ctx）；bore 控制连接读超时 30s 触发重连 |

## 锁序约定

- 严格锁序：`RoomManager.mu → Room.mu → Watcher.mu`
- `Manager.restartMu` 串行化 Restart；epoch 代际保护：每轮 Start 递增，过期代静默丢弃自身写入、不再调用 `m.Stop()`（见 `internal/manager/manager.go`）

## 仓库文档规范（重要）

- GitHub 仓库内不得出现除 `AGENTS.md` 和主动加入的文档 md（如 `README.md`）以外的任何文档 md
- 开发过程中 agent 产生的 md（superpowers 计划、spec、brainstorm 记录等）绝不可上传
- 设计文档等过程产物放 `/tmp` 或仓库外
- 推送前检查：`git status` 干净、无敏感信息（密钥/密码）、项目结构完整
- `.codegraph/`、`.cloud-code/`、`.syncmedia/` 等本地工具/运行时目录不得入库（已列入 .gitignore）

## 代码风格与约定

- 注释和用户可见文本使用中文
- 代码标识符、commit 消息使用英文
- 错误信息使用中文（面向终端用户）
- 导出类型有英文 doc comment
- 位置/状态使用指针类型区分"未提供"与"零值"

## 已知限制

- `-race` 在 39-bit VMA 内核（部分 Android/arm64）不可用（TSan 需 48-bit）
- FRP 数据面测试需联网（当前仅 fake 测试）
- `gmsm`/`gmtls` 库约 2020 年后停更，标准库 TLS 安全修复不覆盖，为已知风险，暂不替换

## 构建产物与 CI

- 产物：`syncmedia` 二进制（linux/amd64 + linux/arm64，静态链接）；tar.gz 内含二进制、`syncmedia_ctl`、`install.sh`、`uninstall.sh`、`config.example.yaml`、`README.md`
- GoReleaser 仅发布 linux；版本号经 ldflags 注入（Version/Commit/Date）
- CI（`.github/workflows/ci.yml`）：push/PR 到 main/develop；tag `v*` 触发 release
  - test：Go 1.25，`go test -race`（ubuntu/windows/macos 三平台）
  - lint：golangci-lint
  - vulncheck：govulncheck
  - build：多平台构建 + `syncmedia version` smoke test
  - release：GoReleaser（仅 tag 触发）
