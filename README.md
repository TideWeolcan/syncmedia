# SyncMedia

SyncMedia —— 多人同步观影服务器：兼容 Syncplay 协议，纯 Go 实现，单二进制即可运行。

配合 [Kazumi](https://github.com/Kazumi-Team/Kazumi) 等支持 Syncplay 协议的客户端，和朋友一起远程同步观影。

## 功能特性

- **Syncplay 1.7.5 协议兼容**：支持 Kazumi 等 Syncplay 客户端
- **自动端口避让**：端口被占用时自动改用可用端口，无需手动配置
- **内置隧道**：bore（免配置，临时公网地址）/ frp（自建服务器，固定地址）
- **国密 SM2 加密**：STARTTLS，传输全程加密
- **嵌入式 WebUI 仪表盘**：浏览器查看状态、管理配置、一键重启
- **IPv6 直连**：有公网 IPv6 时无需隧道即可直连

## 快速开始

### 方式一：一键安装（推荐）

```bash
bash install.sh
```

脚本自动完成：检测系统架构（x86_64 / arm64）与发行版 → 下载对应版本 → SHA256 校验 → 安装到 `~/syncmedia/` → 生成配置文件。安装后启动服务：

```bash
~/syncmedia/syncmedia_ctl start
```

### 方式二：手动安装

1. 从 [GitHub Releases](https://github.com/TideWeolcan/syncmedia/releases) 下载 `syncmedia_vX.Y.Z_linux_amd64.tar.gz`（ARM 设备选 `_arm64`）
2. 解压：`tar xzf syncmedia_vX.Y.Z_linux_amd64.tar.gz`
3. 启动：`./syncmedia_ctl start`

启动后访问 `http://127.0.0.1:8080` 查看 WebUI（服务状态、公网地址、配置）。

## 控制脚本

`syncmedia_ctl` 管理服务生命周期：

| 命令 | 作用 |
|------|------|
| `start` | 后台启动服务 |
| `stop` | 停止服务 |
| `restart` | 重启服务 |
| `status` | 查看运行状态与实际端口 |
| `logs` | 实时查看日志（Ctrl+C 退出） |
| `help` | 显示帮助 |

## 卸载

```bash
bash uninstall.sh
```

脚本会：停止运行中的服务 → 删除 `~/syncmedia/` 目录 → 移除 systemd 开机自启（如有）。

## 配置文件

配置示例见 `config.example.yaml`：所有设置都有默认值，想改哪个取消注释填上即可。

配置优先级（低 → 高）：**默认值 < `config.yaml` < 环境变量（`SYNCMEDIA_*`）< 命令行参数**。

## 端口说明

- **Syncplay 端口**（朋友连接用）：你设定的端口优先；未设定时自动选择（默认尝试 8999，被占用会自动避让并记住结果，重启不漂移）
- **WebUI 端口**（自己访问管理界面用）：默认 8080，被占用自动避让
- **隧道公网端口**：bore 由中继服务器随机分配（不可控，看 WebUI 显示的公网地址）；frp 由你指定（`tunnel.frp.remote_port`）；有公网 IPv6 时可直接连本机，免隧道
- 实际端口怎么看：`./syncmedia_ctl status`（或看启动日志 / WebUI）

## IPv6

设备有公网 IPv6 时，朋友可直接连 `[IPv6地址]:端口`，无需隧道。
`server.bind` 可指定监听地址（默认全接口，同时监听 IPv4 和 IPv6）。

## 构建（开发者）

需要 Go 1.25+：

```bash
CGO_ENABLED=0 go build -o syncmedia ./cmd/syncmedia
go test ./... -count=1
```

## 平台说明

当前仅支持 **Linux（x86_64 / arm64）**。Android / Magisk / Windows 版本见独立仓库：Sync Media For Android、Sync Media For Magisk、Sync Media For Windows。

## 许可证

MIT
