#!/usr/bin/env bash
# 跨平台安装程序打包脚本：
# 1. 生成 macOS Universal (.dmg) 拖拽安装镜像；
# 2. 生成 Windows amd64 NSIS 标准安装程序（防杀软误报配置：保留标准符号表、不加 UPX 壳、采用 zlib 压缩与用户级免提权安装）；
# 3. 不生成任何 .zip 压缩包。
set -euo pipefail
set +x

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

export PATH="$HOME/go/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"
# 确保 macOS / POSIX 非交互 shell 下 makensis 能正确解析 UTF-8 中文脚本
export LANG="en_US.UTF-8"
export LC_ALL="en_US.UTF-8"

# 读取本地 .env.local 注入内置 OAuth 凭据（绝不向终端打印密钥明文）
if [ -f ".env.local" ]; then
  set -a
  # shellcheck disable=SC1091
  source ".env.local"
  set +a
fi

# 从 wails.json 提取语义版本号，默认 1.0.0
APP_VERSION="$(python3 -c 'import json; print(json.load(open("wails.json")).get("info", {}).get("productVersion", "1.0.0"))')"

INJECT_LDFLAGS="-X more-account/internal/updater.AppVersion=${APP_VERSION}"
if [ -n "${MORE_ACCOUNT_OAUTH_CLIENT_ID:-}" ]; then
  INJECT_LDFLAGS="${INJECT_LDFLAGS} -X more-account/internal/oauth.DefaultClientID=${MORE_ACCOUNT_OAUTH_CLIENT_ID}"
fi
if [ -n "${MORE_ACCOUNT_OAUTH_CLIENT_SECRET:-}" ]; then
  INJECT_LDFLAGS="${INJECT_LDFLAGS} -X more-account/internal/oauth.DefaultClientSecret=${MORE_ACCOUNT_OAUTH_CLIENT_SECRET}"
fi
if [ -n "${MORE_ACCOUNT_GITHUB_REPO:-}" ]; then
  INJECT_LDFLAGS="${INJECT_LDFLAGS} -X more-account/internal/updater.DefaultGitHubRepo=${MORE_ACCOUNT_GITHUB_REPO}"
fi

MAC_LDFLAGS="-s -w ${INJECT_LDFLAGS}"
# Windows 端防爆毒关键：不剥离 Go 符号表（不加 -s -w）、不使用 UPX 加壳、不使用混淆，显著降低 Defender / 360 / 火绒启发式误报
WIN_LDFLAGS="${INJECT_LDFLAGS}"

echo "==> [1/4] 构建 macOS Universal (darwin/universal)..."
wails build -clean -trimpath -platform darwin/universal -ldflags "${MAC_LDFLAGS}" 2>&1 | grep -v "^LDFlags"

echo "==> 对 macOS 应用执行 ad-hoc 签名..."
codesign --force --deep --sign - "build/bin/MoreAccount.app"
codesign --verify --deep --strict "build/bin/MoreAccount.app"

echo "==> [2/4] 制作 macOS 安装镜像 (.dmg)..."
DMG_OUT="build/bin/MoreAccount-macOS-universal.dmg"
rm -f "${DMG_OUT}"

DMG_STAGE="$(mktemp -d)"
cleanup_stage() {
  rm -rf "${DMG_STAGE}"
}
trap cleanup_stage EXIT

cp -R "build/bin/MoreAccount.app" "${DMG_STAGE}/MoreAccount.app"

if command -v create-dmg >/dev/null 2>&1; then
  # 优先使用 create-dmg 生成美观的拖拽安装布局；若无头环境 AppleScript 超时则自动回退到 hdiutil
  if ! create-dmg \
    --volname "More Account" \
    --volicon "build/bin/MoreAccount.app/Contents/Resources/iconfile.icns" \
    --window-pos 200 120 \
    --window-size 600 400 \
    --icon-size 110 \
    --icon "MoreAccount.app" 160 190 \
    --hide-extension "MoreAccount.app" \
    --app-drop-link 440 190 \
    --no-internet-enable \
    "${DMG_OUT}" \
    "${DMG_STAGE}" >/dev/null 2>&1; then
    echo "    create-dmg 不可用或被无头环境拦截，自动切换为 hdiutil 标准镜像制作..."
    rm -f "${DMG_OUT}"
    ln -s /Applications "${DMG_STAGE}/Applications"
    hdiutil create -volname "More Account" -srcfolder "${DMG_STAGE}" -ov -format UDZO "${DMG_OUT}" >/dev/null
  fi
else
  ln -s /Applications "${DMG_STAGE}/Applications"
  hdiutil create -volname "More Account" -srcfolder "${DMG_STAGE}" -ov -format UDZO "${DMG_OUT}" >/dev/null
fi

echo "==> [3/4] 构建 Windows amd64 主程序（防误报参数：保留标准符号表 + -trimpath + -webview2 browser）..."
wails build -s -trimpath -webview2 browser -platform windows/amd64 -ldflags "${WIN_LDFLAGS}" 2>&1 | grep -v "^LDFlags"

echo "==> [4/4] 使用 NSIS 制作 Windows 安装程序..."
if ! command -v makensis >/dev/null 2>&1; then
  echo "错误：未找到 makensis，请先通过 brew install makensis 安装 NSIS" >&2
  exit 1
fi

WIN_INSTALLER_OUT="build/bin/MoreAccount-windows-amd64-installer.exe"
rm -f "${WIN_INSTALLER_OUT}"

makensis -V2 \
  -INPUTCHARSET UTF8 \
  -DAPP_VERSION="${APP_VERSION}" \
  -DAPP_EXE_PATH="${ROOT_DIR}/build/bin/MoreAccount.exe" \
  -DAPP_ICON_PATH="${ROOT_DIR}/build/windows/icon.ico" \
  -DOUT_FILE="${ROOT_DIR}/${WIN_INSTALLER_OUT}" \
  "${ROOT_DIR}/build/windows/installer/installer.nsi"

# 清理不需要的压缩包与 Windows 中间裸 exe，保持 build/bin 仅输出标准安装程序与 macOS 应用
find build/bin -maxdepth 1 -name '*.zip' -delete
rm -f build/bin/MoreAccount.exe

echo "==> 校验打包产物："
file "build/bin/MoreAccount.app/Contents/MacOS/MoreAccount" "${WIN_INSTALLER_OUT}"
hdiutil verify "${DMG_OUT}" >/dev/null && echo "DMG 镜像完整性校验通过: ${DMG_OUT}"
ls -lh build/bin/
