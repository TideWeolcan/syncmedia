# CI 故障排除指南

本文档记录 `.github/workflows/ci.yml` 中每个 job 的常见失败原因及修复步骤。

---

## test

### Race detector 失败（`-race`）

**现象**：`DATA RACE` 报错，仅在 CI 复现。

**原因**：并发代码中存在未保护的共享变量访问，CI 多核环境比本地更容易触发。

**修复**：
1. 阅读 race 输出中的两个 goroutine 堆栈，定位共享变量。
2. 加 mutex / 改用 atomic / channel 保护。
3. 本地验证：`CGO_ENABLED=1 go test ./... -race -count=100`。

### Windows 路径问题

**现象**：`open .\config.yaml: The system cannot find the path specified`。

**原因**：硬编码 `/` 路径分隔符，Windows 需要 `filepath.Join`。

**修复**：用 `filepath.Join` 或 `filepath.FromSlash` 替换手写路径拼接。

### 依赖下载超时

**现象**：`go mod download` 超时或 `i/o timeout`。

**原因**：GitHub Actions runner 到 Go module proxy 网络波动。

**修复**：
1. 重试 workflow（大多数情况自动恢复）。
2. 如果持续失败，检查 `go.sum` 是否被误修改；运行 `go mod tidy` 确认一致。
3. 可在 workflow 中设置 `GOPROXY=https://proxy.golang.org,direct`。

---

## lint

### golangci-lint 版本不兼容

**现象**：`unknown linters`、`unsupported option` 等错误。

**原因**：`golangci-lint` 新版本移除/重命名了 linter 或配置选项。

**修复**：
1. 查看 [golangci-lint changelog](https://github.com/golangci/golangci-lint/releases)。
2. 更新 `.golangci.yml`（如有）中被废弃的 linter 名称。
3. 或在 workflow 中固定版本：`version: v1.xx.x`。

### 超时

**现象**：`deadline exceeded`（默认 1m）。

**原因**：项目代码量增长或 linter 做了更多分析。

**修复**：workflow 中已设置 `--timeout=10m`。如仍超时，检查是否有 linter 陷入死循环（如 `goanalysis_metalinter`），尝试排除特定 linter。

---

## vulncheck

### 新漏洞报告

**现象**：`govulncheck` 报告某依赖存在已知漏洞。

**处理流程**：
1. 阅读输出中的 CVE/GHSA 编号和影响的函数。
2. 检查项目是否实际调用了受影响的代码路径（`govulncheck` 只报告可达路径）。
3. 升级依赖：`go get <module>@latest && go mod tidy`。
4. 如果上游未修复，评估风险后可暂时在 CI 中记录 known issue（不建议忽略）。

---

## build

### CGO 相关

**现象**：`cgo: C compiler not found` 或链接错误。

**原因**：workflow 全局设置 `CGO_ENABLED=0`，但某些代码路径意外依赖 cgo。

**修复**：
1. 确认 `CGO_ENABLED=0` 下所有包可编译（项目设计就是纯 Go）。
2. 如果引入了需要 cgo 的依赖，要么替换为纯 Go 实现，要么在 build step 中安装 `gcc`。

### 交叉编译问题

**现象**：`GOOS=windows GOARCH=arm64` 编译失败。

**原因**：某些 `//go:build` 约束或 `_linux.go` 后缀文件包含平台特定代码。

**修复**：
1. 检查失败的文件是否正确使用了 build tags。
2. 确保平台特定代码有对应的 stub 文件（如 `signal_windows.go`）。

---

## build-apk

### Android SDK 安装失败

**现象**：`sdkmanager: command not found` 或 license 未接受。

**原因**：`setup-android` action 版本变更或 cmdline-tools 版本号过期。

**修复**：
1. 检查 [android-actions/setup-android](https://github.com/android-actions/setup-android) 最新版。
2. 更新 `cmdline-tools-version`（在 [Google 官方](https://developer.android.com/studio#command-tools) 查看最新版本号）。
3. License：action 默认接受，如失败加 `yes | sdkmanager --licenses`。

### aapt2 版本 / 编译失败

**现象**：`aapt2: error: resource not found` 或 `Failed to compile`。

**原因**：`build-tools` 版本与资源 XML 不兼容，或资源文件格式错误。

**修复**：
1. 确认 `build-tools;34.0.0` 已安装（`sdkmanager --list | grep build-tools`）。
2. 检查 `android/AndroidManifest.xml` 和资源 XML 语法。
3. 如需升级 build-tools 版本，同步更新 `scripts/build-apk.sh` 中的 `BUILD_TOOLS` 路径。

### readelf 不可用（verify-apk.sh 失败）

**现象**：`readelf: command not found`。

**修复**：ubuntu-latest 默认有 `binutils`，如缺失加 `sudo apt-get install -y binutils`。

---

## build-ksu

### zip 不可用

**现象**：`zip: command not found`。

**原因**：极少数 runner 镜像缺失 `zip`（正常 ubuntu-latest 自带）。

**修复**：在 step 前加 `sudo apt-get install -y zip`，或改用 `tar` + 在脚本中用 Python zipfile。

---

## release

### GoReleaser 配置错误

**现象**：`yaml: unmarshal errors`、`invalid configuration`。

**原因**：`.goreleaser.yaml` 使用了新/旧版本不支持的字段。

**修复**：
1. 本地验证：`goreleaser check`。
2. 查看 [GoReleaser 文档](https://goreleaser.com/errors/) 获取迁移指南。
3. CI 中使用 `version: latest` 确保与最新语法兼容。

### Token 权限不足

**现象**：`403 Forbidden`、`Resource not accessible by integration`。

**原因**：`GITHUB_TOKEN` 默认权限不够，或 repo 设置了严格的 token 范围。

**修复**：
1. 确认 workflow 中有 `permissions: contents: write`。
2. 如果发布到其他 repo 或需要额外权限（packages 等），使用 PAT 或 GitHub App token。
3. 检查 repo Settings → Actions → General → Workflow permissions 是否为 "Read and write"。

### cosign 签名失败

**现象**：`cosign: command not found` 或签名验证失败。

**原因**：`sigstore/cosign-installer` action 版本变更或 Fulcio/Rekor 服务临时不可用。

**修复**：
1. 确认 `sigstore/cosign-installer@v3` 是最新 major 版本。
2. Fulcio/Rekor 故障是暂时的，重试即可。
3. 如不需要 keyless signing，可移除 cosign step（GoReleaser 配置中也要移除 `signs` 段）。

### APK/KSU asset 上传失败

**现象**：`softprops/action-gh-release` 报错 `release not found`。

**原因**：GoReleaser 创建的 release 与 `action-gh-release` 预期的 tag 不一致。

**修复**：
1. 确认 GoReleaser 成功创建了 release（检查前一步日志）。
2. `softprops/action-gh-release@v2` 默认用 `github.ref` 找 release，确保 tag 格式匹配。
3. 如 GoReleaser 失败但后续 step 仍执行，需要在 GoReleaser step 确认 `exit-code` 检查生效。
