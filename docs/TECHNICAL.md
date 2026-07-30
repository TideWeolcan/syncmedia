# SyncMedia 技术文档

## 架构概览

```
cmd/syncmedia/          CLI 入口
internal/
├── config/             配置加载与校验
├── manager/            服务编排（生命周期、WebUI、API）
├── syncplay/           Syncplay 协议实现（Server、Room、State）
└── tunnel/             隧道抽象层（bore/frp 原生实现）
pkg/version/            版本信息（ldflags 注入）
android/                Android APK 源码
kernelsu/               KernelSU/Magisk 模块描述
scripts/                构建与验证脚本
```

## 核心模块

### Manager（`internal/manager/`）

服务编排中心，负责按序启动/停止所有子系统。

```go
type Manager struct {
    cfg        *config.Config
    srv        *syncplay.Server
    tunnelMgr  *tunnel.Manager
    webSrv     *WebServer
    mu         sync.RWMutex     // 保护 Status 读
    restartMu  sync.Mutex       // 串行化 Restart
}
```

**生命周期：**

1. `NewManager(cfg, logger)` — 构造，不启动任何服务
2. `Start(ctx)` — 顺序启动：WebServer 校验 → TLS 证书生成 → Syncplay Server → Tunnel → WebUI → 阻塞等待 ctx 取消
3. `Stop()` — 反序关闭：Tunnel → Syncplay → WebUI，有界等待（Syncplay Close 主动关闭活跃连接后 wg.Wait）
4. `Restart()` — 持 restartMu 互斥锁，Stop → Start，保证并发安全

**API 层（WebServer）：**

| 端点 | 方法 | 功能 | 认证 |
|------|------|------|------|
| `/` | GET | HTML 仪表盘 | 条件 |
| `/api/status` | GET | 实时状态 JSON | 条件 |
| `/api/settings` | GET | 当前配置（秘密脱敏） | 条件 |
| `/api/settings` | POST | 更新配置并异步重启 | 条件 |
| `/api/restart` | POST | 触发重启 | 条件 |

认证条件：绑定地址为非回环时强制 Token 认证（Basic/Bearer，恒定时间比较）。

### Syncplay Server（`internal/syncplay/`）

Syncplay 1.7.5 协议的 Go 实现。

**协议栈：**

```
TCP 连接
  ├── 可选 STARTTLS（客户端发 {"TLS":{"startTLS":"send"}}）
  │     └── 国密 SM2 + ECDSA 双证书自动协商
  └── JSON 单行消息（换行分隔）
        ├── Hello — 身份认证与房间加入
        ├── State — 播放状态同步（位置、暂停、播放速率）
        ├── Set — 设置更新（就绪状态、播放列表）
        ├── List — 房间列表
        ├── Chat — 聊天
        └── Error — 错误通知
```

**关键设计决策：**

- **位置类型**：`*float64`（指针），区分"未提供"与"seek 到 0.0"
- **锁序**：RoomManager.mu → Room.mu，用户名通过 atomic 注册防止并发冲突
- **Close 语义**：关闭 listener → 主动关闭所有已注册连接 → wg.Wait()，保证 goroutine 全部退出
- **重复 Hello 防护**：同一连接重复发送 Hello 不产生幽灵用户，旧 watcher 先移除再注册

**常量：**

| 常量 | 值 | 说明 |
|------|---|------|
| DefaultPort | 8999 | 默认监听端口 |
| ProtocolTimeout | 12.5s | 连接超时 |
| StateInterval | 1s | 状态广播间隔 |
| MaxUsernameLen | 16 | 用户名上限 |
| MaxRoomNameLen | 35 | 房间名上限 |
| MaxFilenameLen | 250 | 文件名上限 |
| MaxChatLen | 150 | 聊天消息上限 |

### Tunnel（`internal/tunnel/`）

隧道抽象层，提供统一的生命周期与状态观测接口。

**接口：**

```go
type State string
const (
    StateStarting     State = "starting"
    StateReady        State = "ready"
    StateReconnecting State = "reconnecting"
    StateDegraded     State = "degraded"
    StateStopped      State = "stopped"
)

type Tunnel interface {
    Start(localPort int) error
    WaitReady(ctx context.Context) (publicAddr string, err error)
    Stop() error
    Name() string
    State() State
    PublicAddr() string  // 实时地址，断线后返回空
}
```

**Bore 原生实现（`bore_native.go`）：**

- Go 原生实现 bore 控制协议（端口 7835）
- 帧格式：4 字节大端长度 + JSON payload
- 消息类型：Challenge/Authenticate/Accept/Refused/Incoming
- 数据连接：收到 Incoming 后反向连接并发送 Accept + secret
- 断线检测：控制连接 EOF → 状态切 reconnecting → 指数退避重连（500ms 起步，8s 上限）
- 连续失败 5 次进入 degraded 状态
- Stop 后进入 stopped 终态，忽略后续重连
- 支持 SOCKS5 代理和网卡绑定（通过自定义 Dialer）

**FRP 原生实现（`frp_native.go`）：**

- 基于 `github.com/fatedier/frp/client` Go 库
- 轮询代理状态（200ms 间隔），Phase=="running" 时切 ready
- 从 RemoteAddr 推导公网地址
- 非 running 持续 15s 进入 degraded
- 支持配置 token、remote_port、自定义 server

**Manager：**

```go
type Manager struct {
    tunnels []Tunnel   // 按注册顺序
    active  Tunnel     // 当前活跃隧道
}
```

按注册顺序尝试启动，30s 超时内第一个 WaitReady 成功的隧道成为 active。`PublicAddr()` 和 `ActiveTunnelName()` 实时委托给 active 隧道。

### Config（`internal/config/`）

```go
func Load(path string) (*Config, error)
```

加载流程：
1. 填充默认值
2. 若 path 非空，读取并用 `yaml.Decoder` + `KnownFields(true)` 解析（未知字段报错）
3. 应用环境变量覆盖（`applyEnv`），解析错误显式返回 error
4. 返回最终 Config

## 构建产物

| 产物 | 构建脚本 | 目标平台 | 说明 |
|------|---------|---------|------|
| `syncmedia` | `go build` | linux/{amd64,arm64} | 静态链接二进制 |
| `syncmedia.exe` | GoReleaser | windows/{amd64,arm64} | Windows 版本 |
| `syncmedia.apk` | `build-apk.sh` | android/arm64-v8a | APK（min SDK 29） |
| `syncmedia-ksu.zip` | `build-ksu.sh` | android/arm64 | KernelSU/Magisk 模块 |
| `dist/linux/*.tar.gz` | `build-linux.sh` | linux/{amd64,arm64} | 发行压缩包 |

### APK 构建流程

```
Go 编译 (CGO_ENABLED=0, GOOS=linux, GOARCH=arm64, 静态链接)
    ↓
lib/arm64-v8a/libsyncmedia.so (嵌为 native library)
    ↓
Java 编译 → DEX (d8 --min-api 29)
    ↓
aapt2 compile → aapt2 link → 组装 ZIP
    ↓
zipalign → apksigner (debug.keystore)
```

关键保证：
- 每次从空 staging 构建，不复用旧产物
- 任一步骤失败立即退出（`set -euo pipefail`）
- Go 二进制无 PT_INTERP（静态链接，不依赖 linker）

### APK 静态检查（`scripts/verify-apk.sh`）

4 项检查：
1. AndroidManifest.xml 存在
2. ABI 目录为 arm64-v8a
3. libsyncmedia.so 无 `/lib/ld-linux` PT_INTERP
4. classes.dex 存在

## CI 流水线

`.github/workflows/ci.yml` 定义：

| Job | 触发条件 | 内容 |
|-----|---------|------|
| test | push/PR (main,develop) | Go 1.25, `go test -race`(CGO_ENABLED=1), coverage |
| lint | 同上 | golangci-lint |
| vulncheck | 同上 | govulncheck |
| build | 同上 | 多平台构建 + binary smoke (`syncmedia version`) |
| release | tags `v*` | GoReleaser 全自动发布 |

Windows 步骤使用 PowerShell 语法设置环境变量。

## 安全模型

| 层 | 措施 |
|----|------|
| 传输 | STARTTLS（SM2 + ECDSA 双证书，内存自签名） |
| WebUI | 默认 127.0.0.1；非本机需 token；恒定时间认证 |
| API | 秘密字段脱敏（proxy_url 显示 `***`）；日志同样脱敏 |
| 配置 | 严格 YAML 解析（未知字段报错）；环境变量解析错误显式上报 |
| HTTP | ReadTimeout 15s / WriteTimeout 30s / IdleTimeout 60s / ReadHeaderTimeout 5s |
| 并发 | Restart 互斥锁；Room 锁序强制；连接注册/注销原子化 |
| Android | 单实例守卫（ProcessGuard）；stop 终止全部子进程 |

## 测试矩阵

| 包 | 测试数 | 覆盖要点 |
|----|--------|---------|
| cmd/syncmedia | 1 | usage 输出包含所有 flag |
| internal/config | 4+ | 严格解码、环境变量、默认值 |
| internal/manager | 6+ | WebUI JS 解析、安全策略、Status 实时、并发重启、有界停机 |
| internal/syncplay | 13+ | 协议全语义（join/play/pause/seek/seek0/后加入/断线/重复Hello/并发hammer） |
| internal/tunnel | 12+ | 状态机转换、断线清地址、退避重连、fake bore/frp |

所有测试仅监听 `127.0.0.1:0`，无公网依赖。

## 已知限制

- `-race` 检测器在 39-bit VMA 内核（部分 Android/arm64）上不可用（TSan 需 48-bit）
- FRP 真实数据面测试需联网（当前仅状态机 + fake 测试）
- Android 实机行为（LMK、START_STICKY 时序）无法在 chroot 验证
- `Process.pid()` 需 API 33+，API 29-32 会抛 NoSuchMethodError（pre-existing）
