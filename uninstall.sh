#!/bin/sh
# SyncMedia 一键卸载脚本（Linux）
# 用法：
#   uninstall.sh         交互式确认后卸载 ~/syncmedia/
#   uninstall.sh -y      跳过确认直接卸载
#   uninstall.sh --help  显示帮助
#
# 卸载内容：
#   1. 停止正在运行的 SyncMedia 服务
#   2. 删除整个 ~/syncmedia/ 目录
#   3. 移除 systemd 开机自启（syncmedia.service，需要 root）

set -u

# 定位脚本自身所在目录
DIR="$(cd "$(dirname "$0")" && pwd)"
INSTALL_DIR="$HOME/syncmedia"

# ---------- 帮助 ----------
usage() {
    cat <<'EOF'
SyncMedia 一键卸载脚本（Linux）

用法：
  uninstall.sh [选项]

选项：
  -y, --yes   跳过确认，直接卸载
  -h, --help  显示本帮助

卸载内容：
  1. 停止 SyncMedia 服务
  2. 删除 ~/syncmedia/ 目录
  3. 移除 systemd 开机自启（需要 root）
EOF
}

# ---------- 参数解析 ----------
FORCE=0
while [ $# -gt 0 ]; do
    case "$1" in
        -y|--yes)
            FORCE=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "错误：未知参数 '$1'（可用 --help 查看帮助）"
            exit 1
            ;;
    esac
done

# ---------- 停止服务 ----------
stop_service() {
    # 优先停安装目录里的服务（脚本可能从仓库根目录运行）
    if [ -x "$INSTALL_DIR/syncmedia_ctl" ]; then
        echo "正在停止 SyncMedia 服务..."
        "$INSTALL_DIR/syncmedia_ctl" stop 2>/dev/null || true
    elif [ -x "$DIR/syncmedia_ctl" ]; then
        echo "正在停止 SyncMedia 服务..."
        "$DIR/syncmedia_ctl" stop 2>/dev/null || true
    fi
}

# ---------- 确认 ----------
confirm() {
    if [ "$FORCE" = "1" ]; then
        return 0
    fi
    echo "即将删除整个目录：$INSTALL_DIR/"
    echo "此操作不可恢复！"
    printf "确认继续卸载？[y/N] "
    read CONFIRM
    case "$CONFIRM" in
        y|Y|yes|YES|Yes) return 0 ;;
        *) echo "已取消卸载。"; exit 0 ;;
    esac
}

# ---------- 清理 systemd ----------
cleanup_systemd() {
    UNIT="/etc/systemd/system/syncmedia.service"
    if [ ! -f "$UNIT" ]; then
        return 0
    fi
    if [ "$(id -u)" = "0" ]; then
        echo "正在移除 systemd 服务..."
        systemctl disable syncmedia.service 2>/dev/null || true
        rm -f "$UNIT"
        systemctl daemon-reload 2>/dev/null || true
        echo "已移除 systemd 服务 syncmedia.service"
    else
        echo "提示：检测到 systemd 服务 syncmedia.service，但移除它需要 root 权限。"
        echo "      请手动执行以下命令清理："
        echo "        sudo systemctl disable syncmedia.service"
        echo "        sudo rm /etc/systemd/system/syncmedia.service"
    fi
}

# =============================================
# 主流程
# =============================================

if [ ! -d "$INSTALL_DIR" ]; then
    echo "未检测到安装目录 $INSTALL_DIR/，无需卸载。"
    exit 0
fi

stop_service
confirm

# 先离开目标目录再删除（脚本可能正运行在 ~/syncmedia/ 里）
cd / || exit 1
rm -rf "$INSTALL_DIR"

cleanup_systemd

echo ""
echo "=============================================="
echo "  SyncMedia 已卸载"
echo "=============================================="
echo "已删除目录：$INSTALL_DIR/"
echo "已停止服务、移除相关配置。"
echo ""
echo "如需重新安装，请执行："
echo "  bash install.sh"
echo ""

exit 0
