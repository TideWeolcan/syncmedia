#!/bin/bash
set -e

# SyncMedia KernelSU/Magisk 模块构建脚本
# 产出：syncmedia-ksu.zip（刷入 KernelSU/Magisk 即可开机自启）

export PATH="/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-arm64/bin:$PATH"
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_DIR="$PROJECT_ROOT/dist/ksu"

echo "=== 构建 KernelSU 模块 ==="

# 1. 编译 arm64 二进制
echo "[1/3] 编译 Go 二进制 (linux/arm64)..."
mkdir -p "$BUILD_DIR/system/bin"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
    -ldflags "-s -w -X github.com/TideWeolcan/syncmedia/pkg/version.Version=v2.0.0-ksu" \
    -o "$BUILD_DIR/system/bin/syncmedia" \
    "$PROJECT_ROOT/cmd/syncmedia/"
echo "  ✓ syncmedia ($(ls -lh "$BUILD_DIR/system/bin/syncmedia" | awk '{print $5}'))"

# 2. 复制模块文件
echo "[2/3] 复制模块文件..."
cp "$PROJECT_ROOT/kernelsu/module.prop" "$BUILD_DIR/"
cp "$PROJECT_ROOT/kernelsu/service.sh" "$BUILD_DIR/"
chmod +x "$BUILD_DIR/service.sh"
cp "$PROJECT_ROOT/start.sh" "$BUILD_DIR/start.sh"
cp "$PROJECT_ROOT/stop.sh" "$BUILD_DIR/stop.sh"
echo "  ✓ 模块文件就绪"

# 3. 打包 zip
echo "[3/3] 打包 zip..."
cd "$BUILD_DIR"
zip -q -r "$PROJECT_ROOT/syncmedia-ksu.zip" \
    module.prop service.sh start.sh stop.sh system/
echo "  ✓ syncmedia-ksu.zip"

echo ""
echo "=== 完成 ==="
echo "  模块: $PROJECT_ROOT/syncmedia-ksu.zip"
echo "  安装: 在 KernelSU/Magisk 管理器中刷入此 zip"
echo "  开机后自动启动，访问 http://127.0.0.1:8080"
