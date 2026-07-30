#!/bin/bash
set -e

# SyncMedia Linux 构建脚本
# 产出：dist/linux/ 下两个 tar.gz（amd64 + arm64），各含二进制 + start.sh

export PATH="/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-arm64/bin:$PATH"
PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

echo "=== 构建 Linux 产物 ==="

build_arch() {
    ARCH=$1
    OUTDIR="$PROJECT_ROOT/dist/linux/$ARCH"
    mkdir -p "$OUTDIR"

    echo "[$ARCH] 编译中..."
    CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build \
        -ldflags "-s -w -X github.com/TideWeolcan/syncmedia/pkg/version.Version=v2.0.0-linux-$ARCH" \
        -o "$OUTDIR/syncmedia" \
        "$PROJECT_ROOT/cmd/syncmedia/"

    cp "$PROJECT_ROOT/start.sh" "$OUTDIR/"
    cp "$PROJECT_ROOT/stop.sh" "$OUTDIR/"
    chmod +x "$OUTDIR/syncmedia" "$OUTDIR/start.sh" "$OUTDIR/stop.sh"

    cd "$PROJECT_ROOT/dist/linux"
    tar -czf "syncmedia-linux-$ARCH.tar.gz" "$ARCH/"
    echo "  ✓ syncmedia-linux-$ARCH.tar.gz ($(ls -lh "syncmedia-linux-$ARCH.tar.gz" | awk '{print $5}'))"
}

build_arch amd64
build_arch arm64

echo ""
echo "=== 完成 ==="
echo "  产物在 dist/linux/"
echo "  解压后直接运行: ./syncmedia start  或  ./start.sh"
