#!/system/bin/sh
# SyncMedia KernelSU/Magisk 开机自启脚本
# 开机后自动启动 syncmedia 服务（bore 隧道 + TLS）

MODDIR=${0%/*}

# 等待网络就绪
until [ "$(getprop sys.boot_completed)" = "1" ]; do
    sleep 2
done
sleep 5

# 启动 syncmedia（后台运行，日志输出到 logcat）
nohup "$MODDIR/system/bin/syncmedia" start --tunnel bore > /dev/null 2>&1 &

echo "SyncMedia 已启动，访问 http://127.0.0.1:8080 查看公共地址"
