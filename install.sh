#!/usr/bin/env bash
#
# QuickDock (快启坞) macOS 一键安装脚本
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/parieses/quickdock/main/install.sh | bash
#   bash install.sh [版本号，缺省取最新 release]
#
# 行为：下载对应架构的 .dmg → 静默挂载 → 拷贝 .app 到 ~/Applications → 卸载 → 清 quarantine。
# 注意：安装到 ~/Applications（无需 sudo），与就地替换更新前提一致。
set -euo pipefail

REPO="parieses/quickdock"

# 1) 选定版本（缺省 latest）
VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
    | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
fi
echo "==> 安装 QuickDock 版本：$VERSION"

# 2) 按架构选 dmg 资产名（uname -m: arm64 / x86_64）
ARCH="$(uname -m)"
case "$ARCH" in
  arm64|aarch64) DMG="quickdock-darwin-arm64.dmg" ;;
  x86_64|amd64)  DMG="quickdock-darwin-amd64.dmg" ;;
  *) echo "不支持的架构：$ARCH" >&2; exit 1 ;;
esac

# 3) 取下载 URL
URL="https://github.com/${REPO}/releases/download/${VERSION}/${DMG}"
TMP="$(mktemp -d)"
MOUNT=""
trap '[ -n "$MOUNT" ] && hdiutil detach "$MOUNT" >/dev/null 2>&1 || true; rm -rf "$TMP"' EXIT

echo "==> 下载 $URL"
curl -fL "$URL" -o "$TMP/$DMG"

# 4) 挂载并拷贝 .app 到 ~/Applications
#    挂载点显式指定到临时目录，避免卷名变化/同名卷冲突，也让 trap 能准确卸载。
echo "==> 挂载并安装到 ~/Applications"
MOUNT="$TMP/mnt"
mkdir -p "$MOUNT"
hdiutil attach "$TMP/$DMG" -nobrowse -quiet -mountpoint "$MOUNT"

# bundle 目录名 = Taskfile 的 APP_NAME（quickdock.app）；"快启坞" 只是 Info.plist 里的显示名，
# 不是磁盘上的文件名。这里先按预期名找，找不到再兜底取卷内第一个 .app。
SRC="$(find "$MOUNT" -maxdepth 1 -name 'quickdock.app' 2>/dev/null | head -1)"
[ -n "$SRC" ] || SRC="$(find "$MOUNT" -maxdepth 1 -name '*.app' 2>/dev/null | head -1)"
[ -n "$SRC" ] || { echo "未在 dmg 中找到 .app" >&2; exit 1; }

mkdir -p "$HOME/Applications"
DEST="$HOME/Applications/$(basename "$SRC")"
rm -rf "$DEST"
# 用 ditto 而非 cp -R：保留扩展属性、资源分叉与签名结构，避免拷完签名校验失败
ditto "$SRC" "$DEST"

# 5) 卸载 + 清 quarantine
hdiutil detach "$MOUNT" >/dev/null 2>&1 && MOUNT="" || true
echo "==> 清除隔离属性"
xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true

echo "==> 完成。启动：open \"$DEST\"  或访达中双击"
