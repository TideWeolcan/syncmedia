#!/bin/bash
set -euo pipefail

# SyncMedia APK 构建（使用官方 aapt2 + zipalign）
export ANDROID_HOME=${ANDROID_HOME:-/opt/android-sdk}
BUILD_TOOLS="$ANDROID_HOME/build-tools/34.0.0"
PLATFORM_JAR="$ANDROID_HOME/platforms/android-34/android.jar"
export PATH="/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-arm64/bin:$PATH"

PROJECT_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ANDROID_DIR="$PROJECT_ROOT/android"
BUILD_DIR="${BUILD_DIR:-$ANDROID_DIR/build}"
QEMU="qemu-x86_64"

echo "=== SyncMedia APK 构建（官方工具链）==="

# 0. 清空 staging 子目录/中间产物（保证每次从空 staging 构建；不删 BUILD_DIR 本身）
rm -rf "$BUILD_DIR/classes" "$BUILD_DIR/dex" "$BUILD_DIR/jniLibs" "$BUILD_DIR/res" \
    "$BUILD_DIR/compiled_res" "$BUILD_DIR/base.apk" "$BUILD_DIR/full-unsigned.apk"

# 1. Go 二进制（静态链接、无 PT_INTERP，Android 可直接 exec）
echo "[1/8] 编译 Go 二进制 (linux/arm64)..."
mkdir -p "$BUILD_DIR/jniLibs/lib/arm64-v8a"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build \
    -ldflags "-s -w -X github.com/TideWeolcan/syncmedia/pkg/version.Version=v2.0.0-android" \
    -o "$BUILD_DIR/jniLibs/lib/arm64-v8a/libsyncmedia.so" \
    "$PROJECT_ROOT/cmd/syncmedia/"
echo "  ✓ libsyncmedia.so"

# 2. Java 编译
echo "[2/8] 编译 Java..."
mkdir -p "$BUILD_DIR/classes"
javac -source 11 -target 11 -encoding UTF-8 \
    -classpath "$PLATFORM_JAR" \
    -d "$BUILD_DIR/classes" \
    "$ANDROID_DIR/src/MainActivity.java" \
    "$ANDROID_DIR/src/SyncService.java"
echo "  ✓ Java classes"

# 3. Dex 编译
echo "[3/8] Dex 编译..."
mkdir -p "$BUILD_DIR/dex"
"$BUILD_TOOLS/d8" --min-api 29 --lib "$PLATFORM_JAR" --output "$BUILD_DIR/dex" \
    $(find "$BUILD_DIR/classes" -name "*.class") 2>&1
echo "  ✓ classes.dex"

# 4. 准备资源目录
echo "[4/8] 准备资源..."
RES_DIR="$BUILD_DIR/res"
mkdir -p "$RES_DIR/values" "$RES_DIR/mipmap"
# 生成图标
python3 "$PROJECT_ROOT/scripts/gen-icon-only.py" "$RES_DIR/mipmap/ic_launcher.png"
# strings.xml
cat > "$RES_DIR/values/strings.xml" << 'XMLEOF'
<?xml version="1.0" encoding="utf-8"?>
<resources>
    <string name="app_name">SyncMedia</string>
</resources>
XMLEOF
echo "  ✓ 资源就绪"

# 5. 用 aapt2 编译资源
echo "[5/8] aapt2 编译资源..."
rm -rf "$BUILD_DIR/compiled_res"
mkdir -p "$BUILD_DIR/compiled_res"
$QEMU "$BUILD_TOOLS/aapt2" compile --dir "$RES_DIR" -o "$BUILD_DIR/compiled_res/resources.zip" 2>&1
echo "  ✓ 资源编译完成"

# 6. aapt2 链接（生成带正确二进制 manifest 的 base.apk）
echo "[6/8] aapt2 链接..."
$QEMU "$BUILD_TOOLS/aapt2" link \
    -o "$BUILD_DIR/base.apk" \
    -I "$PLATFORM_JAR" \
    --manifest "$ANDROID_DIR/AndroidManifest.xml" \
    --min-sdk-version 29 \
    --target-sdk-version 34 \
    "$BUILD_DIR/compiled_res/resources.zip" 2>&1
echo "  ✓ base.apk"

# 7. 组装完整 APK
echo "[7/8] 组装 APK..."
cp "$BUILD_DIR/base.apk" "$BUILD_DIR/full-unsigned.apk"
cd "$BUILD_DIR"
# 添加 dex
zip -q -j full-unsigned.apk dex/classes.dex
# 添加 native lib（保持目录结构）
cd "$BUILD_DIR/jniLibs"
zip -q -r "$BUILD_DIR/full-unsigned.apk" lib/arm64-v8a/libsyncmedia.so
cd "$PROJECT_ROOT"

# 8. 对齐 + 签名
echo "[8/8] 对齐 + 签名..."
KEYSTORE="$ANDROID_DIR/debug.keystore"
if [ ! -f "$KEYSTORE" ]; then
    keytool -genkey -keystore "$KEYSTORE" -alias androiddebugkey \
        -storepass android -keypass android -keyalg RSA -keysize 2048 \
        -validity 10000 -dname "CN=SyncMedia, OU=Debug, O=SyncMedia, L=Unknown, ST=Unknown, C=Unknown" 2>&1
fi

APK_OUTPUT="${APK_OUTPUT:-$PROJECT_ROOT/syncmedia.apk}"
$QEMU "$BUILD_TOOLS/zipalign" -f 4 "$BUILD_DIR/full-unsigned.apk" "$APK_OUTPUT"
"$BUILD_TOOLS/apksigner" sign \
    --ks "$KEYSTORE" \
    --ks-pass pass:android \
    --key-pass pass:android \
    --ks-key-alias androiddebugkey \
    "$APK_OUTPUT" 2>&1

echo ""
echo "=== 构建完成 ==="
echo "  APK: $APK_OUTPUT"
echo "  大小: $(ls -lh "$APK_OUTPUT" | awk '{print $5}')"

# 验证（输出先落文件再 head，避免 pipefail 下 head 提前退出引发 SIGPIPE 假失败）
"$BUILD_TOOLS/apksigner" verify --verbose "$APK_OUTPUT" > "$BUILD_DIR/apksigner-verify.log" 2>&1
head -5 "$BUILD_DIR/apksigner-verify.log"
