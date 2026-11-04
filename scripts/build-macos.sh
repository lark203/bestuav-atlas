#!/usr/bin/env bash
# ============================================================
# bestuav-atlas · macOS 打包脚本
# ------------------------------------------------------------
# 产出（文件名跟随 mygo.json 的 slug = quantum）：
#   build/量子科学.app                    （应用包）
#   build/quantum-<version>.dmg           （磁盘镜像）
# ============================================================
#
# 【前置依赖】
#   1. macOS 本机（.app/.dmg 只能在 macOS 上生成）。
#   2. Go 1.27+，且 `go` 在 PATH。
#   3. mygo 工具：`go tool mygo`（go.mod 已声明 tool 依赖）。
#   4. Xcode 命令行工具：`xcode-select --install`（打包/签名需要）。
#
# 【签名与公证（发布到 Mac App Store 之外时必须）】
#   默认 ad-hoc 签名（仅本机可运行，其他 Mac 会被 Gatekeeper 拦截）。
#   正式发布请在 mygo.json 配置：
#     "macos": { "signingIdentity": "Developer ID Application: XXX (TEAMID)" }
#   或以 -sign 传入；公证需额外配置 "macos": { "notarize": {...} }。
#
# 【关于架构】
#   -platform darwin/universal 同时打 arm64 + amd64 的通用二进制；
#   若只需本机架构，可改为 darwin/$(uname -m)（如 darwin/arm64）。
#
# 【跨平台说明】
#   裸二进制可跨平台交叉编译，但 .dmg 只能在 macOS 上生成。
#
# 用法（在仓库根目录）：
#   bash scripts/build-macos.sh
# ============================================================
set -euo pipefail

# 从 mygo.json 读取版本号（与构建产物命名保持一致）。
VERSION=$(grep -o '"version"[[:space:]]*:[[:space:]]*"[^"]*"' mygo.json | head -1 | sed -E 's/.*"([^"]*)".*/\1/')
echo "==> 开始构建 macOS 平台 (version: ${VERSION})"

# 构建通用二进制 + .app + .dmg
go tool mygo build -platform darwin/universal

echo "==> 构建完成，产物位于 build/ ："
ls -1 build/*-darwin-* 2>/dev/null || true
echo "==> 完成。将 .dmg 拖入 Applications 即可安装；未公证的版本需在‘系统设置-隐私与安全性’中允许。"
