#!/bin/sh
# SyncMedia 一键启动脚本（Linux/Android 通用）
# 用法：
#   ./start.sh              # 默认启动（bore 隧道 + TLS）
#   ./start.sh --tunnel frp --frp-server xxx
#   ./start.sh --no-tls

DIR="$(cd "$(dirname "$0")" && pwd)"
exec "$DIR/syncmedia" start "$@"
