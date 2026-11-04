#!/usr/bin/env bash
# ============================================================
# bestuav-atlas · Linux 打包脚本
# ------------------------------------------------------------
# 产出（文件名跟随 mygo.json 的 slug = quantum）：
#   build/quantum-<version>-linux-amd64            （裸可执行文件）
#   build/quantum_<version>_amd64.deb              （Debian 包）
#   build/quantum-<version>-linux-amd64.tar.gz     （含 install.sh，装到 ~/.local）
# ============================================================
#
# 【前置依赖】
#   1. Linux 本机（.deb 需要 dpkg/fakeroot，mygo 自行调用；tar.gz 不需要）。
#   2. Go 1.27+，且 `go` 在 PATH。
#   3. mygo 工具：`go tool mygo`（go.mod 已声明 tool 依赖）。
#   4. 打包 .deb 通常需要：sudo apt-get install -y dpkg-dev fakeroot
#      （mygo 生成 .deb 时会用到；若缺失会报错，按需安装）。
#
# 【关于架构】
#   默认 linux/amd64；其他架构改为 linux/arm64 等即可（纯 Go 交叉编译）。
#
# 【跨平台说明】
#   裸二进制可跨平台交叉编译，但 .deb 只能在 Linux 上生成。
#
# 用法（在仓库根目录）：
#   bash scripts/build-linux.sh
# ============================================================
set -euo pipefail

# 从 mygo.json 读取版本号（与构建产物命名保持一致）。
VERSION=$(grep -o '"version"[[:space:]]*:[[:space:]]*"[^"]*"' mygo.json | head -1 | sed -E 's/.*"([^"]*)".*/\1/')
echo "==> 开始构建 Linux 平台 (version: ${VERSION})"

# 构建 Linux/amd64：裸二进制 + .deb + 便携 tar.gz
go tool mygo build -platform linux/amd64

echo "==> 构建完成，产物位于 build/ ："
ls -1 build/*-linux-* 2>/dev/null || true
echo "==> 完成。.deb 用 'sudo dpkg -i *.deb' 安装；tar.gz 解压后运行 install.sh 装到 ~/.local。"
