#!/bin/sh
# SyncMedia 停止脚本
PID=$(pidof syncmedia 2>/dev/null) || PID=$(pgrep -x syncmedia 2>/dev/null)
if [ -n "$PID" ]; then
    kill "$PID" && echo "已停止 (PID $PID)"
else
    echo "未在运行"
fi
