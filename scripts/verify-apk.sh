#!/bin/bash
set -euo pipefail

# SyncMedia APK 静态核验
# 检查 4 项：AndroidManifest.xml、classes.dex、arm64-v8a native lib、so 无 PT_INTERP

if [ $# -lt 1 ] || [ ! -f "${1:-}" ]; then
    echo "用法: $0 <apk-path>"
    echo "  对 APK 做 4 项静态检查（zip 条目 + native lib ELF 头），任一 FAIL 退出码 1"
    exit 2
fi

APK="$1"
SO_PATH="lib/arm64-v8a/libsyncmedia.so"
FAIL=0

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

ZIP_LIST="$(unzip -l "$APK")"

# 1. AndroidManifest.xml
if grep -q "AndroidManifest.xml" <<<"$ZIP_LIST"; then
    echo "PASS [1/4] AndroidManifest.xml 存在"
else
    echo "FAIL [1/4] AndroidManifest.xml 缺失"
    FAIL=1
fi

# 2. classes.dex
if grep -q "classes.dex" <<<"$ZIP_LIST"; then
    echo "PASS [2/4] classes.dex 存在"
else
    echo "FAIL [2/4] classes.dex 缺失"
    FAIL=1
fi

# 3. native lib（ABI=arm64-v8a）
if grep -q "$SO_PATH" <<<"$ZIP_LIST"; then
    echo "PASS [3/4] $SO_PATH 存在"
else
    echo "FAIL [3/4] $SO_PATH 缺失"
    FAIL=1
fi

# 4. so 无 PT_INTERP（存在 /lib/ld-linux 解释器则无法在 Android 上执行）
if grep -q "$SO_PATH" <<<"$ZIP_LIST"; then
    unzip -q -o "$APK" "$SO_PATH" -d "$TMP_DIR"
    ELF_PHDRS="$(readelf -l "$TMP_DIR/$SO_PATH")"
    if grep -q "/lib/ld-linux" <<<"$ELF_PHDRS"; then
        echo "FAIL [4/4] $SO_PATH 含 PT_INTERP 动态解释器:"
        grep "/lib/ld-linux" <<<"$ELF_PHDRS"
        FAIL=1
    else
        echo "PASS [4/4] $SO_PATH 无 PT_INTERP（静态链接，Android 可直接执行）"
    fi
else
    echo "FAIL [4/4] $SO_PATH 缺失，无法检查 PT_INTERP"
    FAIL=1
fi

if [ "$FAIL" -ne 0 ]; then
    echo "结果: FAIL"
    exit 1
fi
echo "结果: PASS (4/4)"
