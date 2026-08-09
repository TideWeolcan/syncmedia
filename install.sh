#!/bin/sh
# SyncMedia 一键安装脚本（Linux）
# 用法：
#   install.sh                    自动获取最新版本并安装到 ~/syncmedia/
#   install.sh --version vX.Y.Z   安装指定版本
#   install.sh --systemd          配置 systemd 开机自启（需要 root）
#   install.sh --help             显示帮助
#
# 支持架构：x86_64 / arm64
# 依赖：curl 或 wget、tar（缺失时会尝试用系统包管理器自动安装）

set -u

# 定位脚本自身所在目录（脚本在仓库根目录或已安装的 ~/syncmedia/ 里都能运行）
DIR="$(cd "$(dirname "$0")" && pwd)"

# ---------- 默认参数 ----------
VERSION=""          # 空 = 自动获取最新版
USE_SYSTEMD=0       # 0 = 不配置 systemd
INSTALL_DIR="$HOME/syncmedia"
REPO="TideWeolcan/syncmedia"
# 下载基址，默认 GitHub。SYNCMEDIA_RELEASE_BASE 仅用于内部测试
# （可指向本地假服务器模拟发布页），不在文档中对外宣传
RELEASE_BASE="${SYNCMEDIA_RELEASE_BASE:-https://github.com}"
BASE_URL="${RELEASE_BASE}/${REPO}/releases/download"
PKG_MGR=""

# ---------- 帮助 ----------
usage() {
    cat <<'EOF'
SyncMedia 一键安装脚本（Linux）

用法：
  install.sh [选项]

选项：
  --version vX.Y.Z   安装指定版本（默认自动获取最新版）
  --systemd          配置 systemd 开机自启（需要 root 权限）
  --help             显示本帮助

安装位置：~/syncmedia/
支持架构：x86_64、arm64
EOF
}

# ---------- 参数解析 ----------
while [ $# -gt 0 ]; do
    case "$1" in
        --version)
            if [ $# -lt 2 ]; then
                echo "错误：--version 需要指定版本号，如 --version v2.0.0"
                exit 1
            fi
            VERSION="$2"
            shift 2
            ;;
        --systemd)
            USE_SYSTEMD=1
            shift
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            echo "错误：未知参数 '$1'（可用 --help 查看帮助）"
            exit 1
            ;;
    esac
done

# ---------- 架构判断 ----------
detect_arch() {
    MACHINE="$(uname -m)"
    case "$MACHINE" in
        x86_64|amd64)
            GOARCH="amd64"
            ;;
        aarch64|arm64)
            GOARCH="arm64"
            ;;
        *)
            echo "错误：不支持的架构 '$MACHINE'"
            echo "SyncMedia 目前仅支持 x86_64 与 arm64 架构"
            exit 1
            ;;
    esac
}

# ---------- 发行版判断（仅用于缺依赖时自动装包） ----------
# 注意：不能 source /etc/os-release（会覆盖本脚本的 VERSION 等全局变量），
# 只用 grep/sed 提取 ID 字段
detect_pkgmgr() {
    PKG_MGR=""
    [ -f /etc/os-release ] || return 0
    ID="$(grep '^ID=' /etc/os-release | head -n 1 | sed 's/^ID=//' | tr -d '"')"
    case "$ID" in
        debian|ubuntu)            PKG_MGR="apt-get" ;;
        arch|manjaro)             PKG_MGR="pacman" ;;
        fedora|rhel|centos)       PKG_MGR="dnf" ;;
        alpine)                   PKG_MGR="apk" ;;
        opensuse*|suse*)          PKG_MGR="zypper" ;;
        *)                        PKG_MGR="" ;;
    esac
}

# ---------- 用包管理器安装依赖 ----------
# 输出包管理器安装命令（字符串），供 root / sudo 通过 sh -c 执行
install_deps_cmd() {
    case "$PKG_MGR" in
        apt-get)
            echo "apt-get update -qq && apt-get install -y -qq curl tar"
            ;;
        pacman)
            echo "pacman -Sy --noconfirm curl tar"
            ;;
        dnf)
            if command -v dnf >/dev/null 2>&1; then
                echo "dnf install -y curl tar"
            else
                echo "yum install -y curl tar"
            fi
            ;;
        apk)
            echo "apk add --no-cache curl tar"
            ;;
        zypper)
            echo "zypper install -y curl tar"
            ;;
        *)
            echo ""
            ;;
    esac
}

run_install_deps() {
    INSTALL_CMD="$(install_deps_cmd)"
    [ -n "$INSTALL_CMD" ] || return 1
    if [ "$(id -u)" = "0" ]; then
        sh -c "$INSTALL_CMD"
    elif command -v sudo >/dev/null 2>&1; then
        sudo sh -c "$INSTALL_CMD"
    else
        return 1
    fi
}

# ---------- 依赖检查 ----------
check_deps() {
    MISSING=""
    if ! command -v curl >/dev/null 2>&1 && ! command -v wget >/dev/null 2>&1; then
        MISSING="curl/wget"
    fi
    if ! command -v tar >/dev/null 2>&1; then
        MISSING="$MISSING tar"
    fi
    [ -z "$MISSING" ] && return 0

    echo "缺少依赖：$MISSING"
    if [ -n "$PKG_MGR" ]; then
        echo "正在使用 $PKG_MGR 自动安装（可能需要 sudo 密码）..."
        if run_install_deps; then
            echo "依赖安装成功"
            return 0
        fi
        echo "自动安装失败。"
    fi
    echo ""
    echo "请手动安装缺失的依赖后重新运行本脚本："
    echo "  - curl：https://curl.se 或 GitHub（https://github.com/curl/curl/releases）"
    echo "  - wget：https://www.gnu.org/software/wget/"
    echo "  - tar ：https://www.gnu.org/software/tar/"
    echo "下载对应发行版的安装包安装后，重新执行本脚本即可。"
    exit 1
}

# ---------- 下载工具 ----------
download() {
    # $1: URL  $2: 输出文件路径
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "$2" "$1"
    elif command -v wget >/dev/null 2>&1; then
        wget -q -O "$2" "$1"
    else
        echo "错误：需要 curl 或 wget 才能下载"
        return 1
    fi
}

# ---------- 通用重试 ----------
# 用法：retry <最多尝试次数> <操作描述> <命令及其参数...>
# 失败后按递增间隔（1s、2s...）重试，全部失败返回 1
# 进度提示输出到 stderr，避免污染被重试命令的 stdout
retry() {
    MAX_ATTEMPTS="$1"
    LABEL="$2"
    shift 2
    N=1
    while [ "$N" -le "$MAX_ATTEMPTS" ]; do
        if "$@"; then
            return 0
        fi
        if [ "$N" -lt "$MAX_ATTEMPTS" ]; then
            echo "$LABEL 失败，正在重试（第 $((N+1))/$MAX_ATTEMPTS 次）..." >&2
            sleep "$N"
            N=$((N+1))
        else
            return 1
        fi
    done
    return 1
}

# ---------- 获取最新版本号（走 302 重定向解析，不用 GitHub API，避免限流） ----------
# 原理：https://github.com/<owner>/<repo>/releases/latest 会 302 跳转到
# .../releases/tag/vX.Y.Z，读 Location 头即可拿到版本号，全程不经过 API
fetch_latest_location() {
    # $1: URL，输出其 302 重定向目标（Location 头，一行）；
    #     网络错误或 HTTP 4xx/5xx 时返回非零，便于上层重试
    if command -v curl >/dev/null 2>&1; then
        curl -fs -o /dev/null -w '%{redirect_url}' "$1"
    elif command -v wget >/dev/null 2>&1; then
        # wget 会跟随重定向去请求目标页，目标页 4xx 也会让 wget 退出码非零；
        # 因此只要抓到了 Location 头就算成功（我们要的只是这个头）
        LOCATION_OUT="$(wget -S -O /dev/null "$1" 2>&1)"
        WGET_STATUS=$?
        LOCATION_LINE="$(printf '%s\n' "$LOCATION_OUT" | grep -i '^ *Location:' | sed 's/^[[:space:]]*[Ll]ocation:[[:space:]]*//' | tr -d '\r' | tail -n 1)"
        if [ -n "$LOCATION_LINE" ]; then
            printf '%s\n' "$LOCATION_LINE"
            return 0
        fi
        return "$WGET_STATUS"
    else
        echo "错误：需要 curl 或 wget 才能获取版本信息"
        return 1
    fi
}

extract_version_from_location() {
    # $1: Location 字符串（…/releases/tag/vX.Y.Z），输出其中的版本号
    printf '%s\n' "$1" | sed -n 's#.*/releases/tag/\([^/[:space:]]*\).*#\1#p' | head -n 1
}

get_latest_version() {
    echo "正在获取最新版本..."
    LATEST_URL="${RELEASE_BASE}/${REPO}/releases/latest"
    LOCATION="$(retry 3 "获取版本号" fetch_latest_location "$LATEST_URL")" || {
        echo ""
        echo "错误：没能获取到最新版本号。"
        echo "可能是网络不稳定，请检查网络后重新运行本脚本；"
        echo "或用 --version 手动指定版本：install.sh --version vX.Y.Z"
        exit 1
    }
    VERSION="$(extract_version_from_location "$LOCATION")"
    if [ -z "$VERSION" ]; then
        echo ""
        echo "错误：拿到了跳转地址，但没从中识别出版本号。"
        echo "请稍后重试，或用 --version 手动指定版本：install.sh --version vX.Y.Z"
        exit 1
    fi
    echo "最新版本：$VERSION"
}

# ---------- 校验下载文件 ----------
verify_checksum() {
    # $1: 临时目录  $2: tar 包文件名
    TMPD="$1"
    TARBALL="$2"
    CHKSUMS="syncmedia_${VERSION}_checksums.txt"

    echo "正在下载校验文件..."
    if ! retry 3 "下载校验文件" download "${BASE_URL}/${VERSION}/${CHKSUMS}" "$TMPD/$CHKSUMS"; then
        echo "警告：校验文件下载失败，跳过完整性校验"
        return 0
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        # 只校验本次下载的文件（checksums.txt 含多平台产物）
        if (cd "$TMPD" && grep -F "$TARBALL" "$CHKSUMS" | sha256sum -c -) >/dev/null 2>&1; then
            echo "SHA256 校验通过"
        else
            echo "错误：SHA256 校验失败，下载的文件可能不完整或已被篡改"
            exit 1
        fi
    elif command -v openssl >/dev/null 2>&1; then
        EXPECTED="$(grep -F "$TARBALL" "$TMPD/$CHKSUMS" | awk '{print $1}')"
        ACTUAL="$(openssl dgst -sha256 "$TMPD/$TARBALL" 2>/dev/null | awk '{print $NF}')"
        if [ -n "$EXPECTED" ] && [ "$EXPECTED" = "$ACTUAL" ]; then
            echo "SHA256 校验通过（openssl）"
        else
            echo "错误：SHA256 校验失败，下载的文件可能不完整或已被篡改"
            exit 1
        fi
    else
        echo "警告：系统没有 sha256sum 或 openssl，跳过完整性校验"
    fi
}

# ---------- 生成精简版配置 ----------
generate_config() {
    # $1: 安装目录
    INSTDIR="$1"
    if [ -f "$INSTDIR/config.yaml" ]; then
        echo "检测到已有配置文件 $INSTDIR/config.yaml，保持不变"
        return 0
    fi
    if [ -f "$INSTDIR/config.example.yaml" ]; then
        # 示例文件本身是"全部注释"的精简版，直接复制
        cp "$INSTDIR/config.example.yaml" "$INSTDIR/config.yaml"
    else
        # tar 内没有示例文件时，手写注释模板
        cat > "$INSTDIR/config.yaml" <<'EOF'
# SyncMedia 配置文件（全部为默认值，按需取消注释启用）
# 优先级：默认值 < config.yaml < 环境变量 (SYNCMEDIA_*) < 命令行参数

# server:
#   port: 8999              # 服务器监听端口
#   tls: true               # 启用 STARTTLS
#   password: ""            # 可选密码保护

# tunnel:
#   type: bore              # bore | frp | none

# web:
#   port: 8080              # WebUI 端口
#   enabled: true           # 是否启用 WebUI
#   bind: "127.0.0.1"       # 绑定地址

# network:
#   proxy_url: ""           # HTTP/SOCKS5 代理地址
EOF
    fi
    echo "已生成精简版配置 $INSTDIR/config.yaml（全部注释，按需启用）"
}

# ---------- 配置 systemd 开机自启 ----------
setup_systemd() {
    if [ "$(id -u)" != "0" ]; then
        echo "警告：配置 systemd 开机自启需要 root 权限，本次跳过。"
        echo "      如需开启，请用 sudo 重新运行：sudo bash $0 --systemd"
        return 0
    fi
    if ! command -v systemctl >/dev/null 2>&1; then
        echo "警告：系统没有 systemctl，无法配置开机自启"
        return 0
    fi
    UNIT="/etc/systemd/system/syncmedia.service"
    cat > "$UNIT" <<EOF
[Unit]
Description=SyncMedia 媒体同步服务器
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$INSTALL_DIR/syncmedia start
WorkingDirectory=$INSTALL_DIR
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable syncmedia.service
    echo "已配置 systemd 开机自启（syncmedia.service）"
}

# ---------- 完成提示 ----------
show_done() {
    cat <<EOF

==============================================
  SyncMedia ${VERSION} 安装完成！
==============================================

安装位置：$INSTALL_DIR/
目录结构：
  $INSTALL_DIR/syncmedia         服务端二进制
  $INSTALL_DIR/syncmedia_ctl     控制脚本（启动/停止/状态/日志）
  $INSTALL_DIR/config.yaml       配置文件（全注释，按需启用）
  $INSTALL_DIR/install.sh        安装脚本
  $INSTALL_DIR/uninstall.sh      卸载脚本
  $INSTALL_DIR/logs/             运行日志目录
  $INSTALL_DIR/data/             数据目录（PID 等）

常用命令：
  启动服务    ~/syncmedia/syncmedia_ctl start
  查看状态    ~/syncmedia/syncmedia_ctl status
  查看日志    ~/syncmedia/syncmedia_ctl logs
  停止服务    ~/syncmedia/syncmedia_ctl stop
  卸载        ~/syncmedia/uninstall.sh

配置文件：$INSTALL_DIR/config.yaml
EOF
    if [ "$USE_SYSTEMD" = "1" ]; then
        echo ""
        echo "开机自启：已启用（systemctl status syncmedia.service 可查看状态）"
    fi
    echo ""
}

# =============================================
# 主流程
# =============================================
detect_arch
detect_pkgmgr
check_deps

# 获取版本号
if [ -z "$VERSION" ]; then
    get_latest_version
else
    case "$VERSION" in
        v*) : ;;
        *) echo "提示：版本号已自动补上 v 前缀（$VERSION -> v$VERSION）"; VERSION="v$VERSION" ;;
    esac
fi

TARBALL="syncmedia_${VERSION}_linux_${GOARCH}.tar.gz"
URL="${BASE_URL}/${VERSION}/${TARBALL}"

# 创建临时目录并设置清理
TMPDIR="$(mktemp -d 2>/dev/null || echo "/tmp/syncmedia-install-$$")"
mkdir -p "$TMPDIR"
trap 'rm -rf "$TMPDIR"' EXIT

echo "=============================================="
echo "  安装 SyncMedia ${VERSION} (linux/${GOARCH})"
echo "=============================================="
echo ""

# 下载
echo "正在下载：$URL"
if ! retry 3 "下载安装包" download "$URL" "$TMPDIR/$TARBALL"; then
    echo ""
    echo "错误：安装包下载失败。"
    echo "可能是网络不稳定，请检查网络后重新运行本脚本。"
    echo "也可以手动下载后解压到 $INSTALL_DIR 使用：$URL"
    exit 1
fi
echo "下载完成：$TARBALL ($(ls -lh "$TMPDIR/$TARBALL" | awk '{print $5}'))"

# 校验
verify_checksum "$TMPDIR" "$TARBALL"

# 解压并安装
echo "正在解压..."
if ! tar -xzf "$TMPDIR/$TARBALL" -C "$TMPDIR"; then
    echo "错误：解压失败，下载的文件可能已损坏"
    exit 1
fi

mkdir -p "$INSTALL_DIR"

# 复制必需文件（二进制与控制脚本）
if [ -f "$TMPDIR/syncmedia" ]; then
    cp -f "$TMPDIR/syncmedia" "$INSTALL_DIR"/
else
    echo "错误：解压产物中未找到 syncmedia 二进制"
    exit 1
fi
if [ -f "$TMPDIR/syncmedia_ctl" ]; then
    cp -f "$TMPDIR/syncmedia_ctl" "$INSTALL_DIR"/
fi

# 复制可选文件（个别缺失不影响安装）
for f in config.example.yaml README.md install.sh uninstall.sh; do
    if [ -f "$TMPDIR/$f" ]; then
        cp -f "$TMPDIR/$f" "$INSTALL_DIR"/
    fi
done

# 设置可执行权限
chmod +x "$INSTALL_DIR"/syncmedia "$INSTALL_DIR"/syncmedia_ctl 2>/dev/null
chmod +x "$INSTALL_DIR"/install.sh "$INSTALL_DIR"/uninstall.sh 2>/dev/null
chmod +x "$INSTALL_DIR"/*.sh 2>/dev/null

# 生成配置
generate_config "$INSTALL_DIR"

# 配置 systemd
if [ "$USE_SYSTEMD" = "1" ]; then
    setup_systemd
fi

# 完成提示
show_done

exit 0
