#!/usr/bin/env python3
"""把 Windows 免安装文件打成一个「英文名」的 zip。

背景：GitHub Releases 不接受中文资产名——上传 `量子科学.exe` 会静默变成
`default.exe`（只保留扩展名）。所以对外提供英文名的 zip，zip **内部**仍是
`量子科学.exe` + `icon.png`（归档内部文件名不受 GitHub 限制）。

为什么不用 PowerShell 的 Compress-Archive：
    Windows PowerShell 5.1 用系统代码页写 zip（中文 Windows 上是 GBK），
    pwsh 7 用 UTF-8，行为随版本漂移；Python 的 zipfile 对非 ASCII 名固定
    置 UTF-8 标志位（0x800），各平台解压都正确。

用法：
    python scripts/make-portable-zip.py [--dir build/windows-amd64] [--config mygo.json]

输出：
    <dir>/<slug>-<version>-windows-portable.zip
"""

import argparse
import glob
import json
import os
import sys
import zipfile

# Windows 上 Python 默认按系统代码页写 stdout（CI 的 Git Bash 里是 cp1252），
# 一旦打印中文文件名就抛 UnicodeEncodeError，整个 job 会以 1 退出——
# 即使 zip 已经打包成功。这里显式切到 UTF-8（并允许替换不可编码字符）。
for _stream in (sys.stdout, sys.stderr):
    try:
        _stream.reconfigure(encoding="utf-8", errors="replace")
    except Exception:  # 老版本 Python 或非标准流
        pass


def main() -> int:
    ap = argparse.ArgumentParser(description="Package the Windows portable exe into a zip.")
    ap.add_argument("--dir", default="build/windows-amd64", help="build directory of the target")
    ap.add_argument("--config", default="mygo.json", help="mygo config with slug/version")
    args = ap.parse_args()

    with open(args.config, encoding="utf-8") as f:
        cfg = json.load(f)
    version = cfg["version"]
    slug = cfg.get("slug") or "app"

    out_dir = args.dir
    if not os.path.isdir(out_dir):
        sys.exit(f"error: {out_dir} does not exist (build first)")

    # 免安装 exe：目录里的 *.exe，排除 NSIS 安装包（*-setup-*.exe）
    exes = [
        p
        for p in glob.glob(os.path.join(out_dir, "*.exe"))
        if "setup" not in os.path.basename(p).lower()
    ]
    if not exes:
        sys.exit(f"error: no portable exe found in {out_dir}")

    files = [exes[0]]
    icon = os.path.join(out_dir, "icon.png")
    if os.path.exists(icon):
        files.append(icon)
    else:
        print("warning: icon.png missing; the portable exe would lack its icon", file=sys.stderr)

    out = os.path.join(out_dir, f"{slug}-{version}-windows-portable.zip")
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
        for p in files:
            z.write(p, arcname=os.path.basename(p))

    # stdout 已切 UTF-8；万一失败（老 Python / 重定向到代码页流），
    # 再兜一层 ascii 替换，绝不因为打印中文而让整个 job 失败。
    names = ", ".join(os.path.basename(p) for p in files)
    msg = (
        f"created {out} ({os.path.getsize(out) / 1048576:.2f} MB, "
        f"{len(files)} file(s); names: {names})"
    )
    try:
        print(msg)
    except UnicodeEncodeError:
        print(msg.encode("ascii", "replace").decode("ascii"))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
