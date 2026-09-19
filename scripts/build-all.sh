#!/usr/bin/env bash
# opsbox 一键出包（macOS）：
#   1. 构建 CLIs（sshctl / sqlctl / redisctl，arm64+amd64 lipo 合并为 universal）到 internal/climgr/clis
#   2. wails build -platform darwin/universal 打出双架构 .app（CLI 一并内嵌，应用内可一键安装）
#   3. hdiutil 打包 DMG 安装版（staging 目录含 /Applications 软链，拖拽安装惯例）
#   4. ditto 打包 .app 免安装版 zip（解压即用）
#
# 用法：
#   scripts/build-all.sh                 # 版本号取 git describe，缺省 dev
#   scripts/build-all.sh v0.2.0          # 显式指定版本号
# 产物：build/bin/opsbox.app、build/dmg/opsbox-<version>-macos.dmg（安装版）、
#       build/dmg/opsbox-<version>-macos-portable.zip（免安装版）
# Windows 对应脚本：scripts/build-all.ps1（exe + NSIS 安装版 + portable 免安装版）。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
clisDir="$root/internal/climgr/clis"

version="${1:-}"
if [ -z "$version" ]; then
  version="dev"
  desc="$(git -C "$root" describe --tags --always --dirty 2>/dev/null || true)"
  [ -n "$desc" ] && version="$desc"
fi

# 1) CLIs -> embed 目录。CLIs 是纯 Go（modernc sqlite，无 cgo），可直接交叉编译双架构；
#    lipo 合并成 universal，保证 Intel / Apple Silicon 用户的 mac 都能跑应用内安装出的 CLI。
#    bin/ 下保留宿主架构副本供开发调试。
mkdir -p "$clisDir" "$root/bin"
for tool in sshctl sqlctl redisctl; do
  echo "build $tool ($version, darwin universal)"
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go -C "$root" build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$clisDir/.tmp-$tool-arm64" "./cmd/$tool"
  GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go -C "$root" build -trimpath \
    -ldflags "-s -w -X main.version=$version" -o "$clisDir/.tmp-$tool-amd64" "./cmd/$tool"
  lipo -create -output "$clisDir/$tool" "$clisDir/.tmp-$tool-arm64" "$clisDir/.tmp-$tool-amd64"
  rm -f "$clisDir/.tmp-$tool-arm64" "$clisDir/.tmp-$tool-amd64"
  cp -f "$clisDir/$tool" "$root/bin/$tool"
done
printf '%s\n' "$version" > "$clisDir/VERSION"

# 2) 桌面应用（darwin/universal = Apple Silicon + Intel 双架构单包；
#    需本机 Xcode 命令行工具提供两个架构的 macOS SDK）
if ! command -v wails >/dev/null 2>&1; then
  echo "wails not found (install with: go install github.com/wailsapp/wails/v2/cmd/wails@latest)" >&2
  exit 1
fi
echo "wails build (version $version)"
(cd "$root" && wails build -platform darwin/universal -ldflags "-X main.appVersion=$version")

# 3) DMG 安装版：staging 目录放 .app + /Applications 软链（拖拽安装惯例）
staging="$(mktemp -d)"
trap 'rm -rf "$staging"' EXIT
cp -R "$root/build/bin/opsbox.app" "$staging/"
ln -s /Applications "$staging/Applications"
mkdir -p "$root/build/dmg"
dmg="$root/build/dmg/opsbox-$version-macos.dmg"
hdiutil create -volname opsbox -srcfolder "$staging" -ov -format UDZO "$dmg"

# 4) 免安装版：.app 打成 zip（ditto -k 保留包结构与资源，--keepParent 使解压得到 opsbox.app）
portable="$root/build/dmg/opsbox-$version-macos-portable.zip"
ditto -c -k --sequesterRsrc --keepParent "$root/build/bin/opsbox.app" "$portable"

echo "done: build/bin/opsbox.app"
echo "done: $dmg (CLIs embedded: $version)"
echo "done: $portable (CLIs embedded: $version)"
