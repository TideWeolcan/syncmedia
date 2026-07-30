#!/bin/sh
# SyncMedia 停止脚本
kill $(pgrep -f "syncmedia start") 2>/dev/null && echo "已停止" || echo "未在运行"
