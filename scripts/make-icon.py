#!/usr/bin/env python3
"""生成「星图」应用图标（resources/icon.png）。

设计：深空蓝的圆角方底（带轻微渐变与高光），一幅四颗星的星座连线，
主星是金色的四芒星——「星图」= 星空 + 图册/坐标。细节刻意做得少而大，
保证 16/32/48 px 的托盘与任务栏尺寸仍然可辨。

用法（需要 Pillow）：
    python scripts/make-icon.py                 # 写 resources/icon.png（1024）
    python scripts/make-icon.py --preview docs  # 额外输出尺寸预览图

为什么用 4 倍超采样：圆角、连线端点和星芒都是为了抗锯齿才画大的，
直接画 1024 再用 draw 的圆角会出毛边。
"""

from __future__ import annotations

import argparse
import math
import os
import sys

from PIL import Image, ImageDraw, ImageFilter

# 画布与超采样倍数。4096 → 1024 缩小时边缘足够干净。
BASE = 1024
SS = 4
S = BASE * SS

# 配色：与应用的深色主题一致（深蓝底 + 青色线条 + 金色主星）。
BG_TOP = (10, 22, 42)
BG_BOTTOM = (26, 58, 104)
HIGHLIGHT = (120, 176, 240, 46)
GRID = (110, 220, 205, 26)
LINE = (143, 231, 216, 210)
NODE = (234, 247, 255)
GOLD = (255, 196, 82)
GOLD_CORE = (255, 246, 214)


def lerp(a: int, b: int, t: float) -> int:
    return int(round(a + (b - a) * t))


def gradient(size: int) -> Image.Image:
    """自上而下的线性渐变（略带对角：每行偏移一点点色相）。"""
    img = Image.new("RGBA", (size, size))
    d = ImageDraw.Draw(img)
    for y in range(size):
        t = y / max(1, size - 1)
        # 用 1.15 次方让上半部分更深，视觉重心落在中间。
        tt = t ** 1.15
        c = tuple(lerp(BG_TOP[i], BG_BOTTOM[i], tt) for i in range(3))
        d.line([(0, y), (size, y)], fill=c + (255,))
    return img


def rounded_mask(size: int, margin: int, radius: int) -> Image.Image:
    mask = Image.new("L", (size, size), 0)
    d = ImageDraw.Draw(mask)
    d.rounded_rectangle(
        [margin, margin, size - margin - 1, size - margin - 1],
        radius=radius,
        fill=255,
    )
    return mask


def vignette(size: int, margin: int, radius: int) -> Image.Image:
    """四角压暗：让中间更聚焦，图标看起来是「完成品」而不是一块平色。"""
    import numpy as np

    yy, xx = np.mgrid[0:size, 0:size]
    cx = cy = (size - 1) / 2
    # 归一化到「角=1.0」的距离
    d = np.sqrt((xx - cx) ** 2 + (yy - cy) ** 2) / (size * 0.5)
    alpha = np.clip((d - 0.55) / 0.55, 0, 1) ** 1.3 * 90
    layer = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    layer.putalpha(Image.fromarray(alpha.astype("uint8"), mode="L"))
    keep = Image.composite(layer.split()[3], Image.new("L", (size, size), 0), rounded_mask(size, margin, radius))
    layer.putalpha(keep)
    return layer


def star_chart_layer(size: int) -> Image.Image:
    """背景星表：同心弧 + 放射刻度，透明度很低，只提供「图册/坐标」的暗示。"""
    layer = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    cx, cy = size * 0.5, size * 0.52
    for r, alpha in ((0.30, 30), (0.40, 22), (0.50, 14)):
        rr = size * r
        d.arc(
            [cx - rr, cy - rr, cx + rr, cy + rr],
            start=205,
            end=515,
            fill=GRID[:3] + (alpha,),
            width=max(1, int(size * 0.0022)),
        )
    # 8 条短刻度，只在右上方，避免整圈像齿轮。
    for deg in range(-70, 111, 45):
        rad = math.radians(deg)
        r0, r1 = size * 0.30, size * 0.335
        d.line(
            [
                (cx + math.cos(rad) * r0, cy + math.sin(rad) * r0),
                (cx + math.cos(rad) * r1, cy + math.sin(rad) * r1),
            ],
            fill=GRID,
            width=max(1, int(size * 0.0026)),
        )
    return layer


def sparkle(d: ImageDraw.ImageDraw, cx: float, cy: float, r: float, fill) -> None:
    """四芒星：四个长尖 + 四个短尖（8 点多边形）。"""
    inner = r * 0.30
    pts = []
    for i in range(8):
        ang = math.radians(-90 + i * 45)
        rad = r if i % 2 == 0 else inner
        pts.append((cx + math.cos(ang) * rad, cy + math.sin(ang) * rad))
    d.polygon(pts, fill=fill)


def constellation_layer(size: int) -> Image.Image:
    """星座：三颗白星 + 一颗金色主星，用细线连成「斗」的形状。"""
    layer = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)

    # 归一化坐标：主星在右上，三颗从星拉开，整体像一只风筝/北斗。
    # 整组略微右移下移，让视觉重心落在画布中心而不是左上。
    n_left = (0.330, 0.435)
    n_top = (0.480, 0.315)
    n_bottom = (0.530, 0.675)
    node_main = (0.670, 0.380)
    nodes = [n_left, n_top, n_bottom]
    edges = [(n_top, n_left), (n_top, node_main), (node_main, n_bottom)]

    lw = max(3, int(size * 0.0090))
    for (x1, y1), (x2, y2) in edges:
        d.line([(x1 * size, y1 * size), (x2 * size, y2 * size)], fill=LINE, width=lw)
        # 线两端补圆点，避免 45° 斜线的端点被削平。
        for x, y in ((x1, y1), (x2, y2)):
            r = lw * 0.5
            d.ellipse([x * size - r, y * size - r, x * size + r, y * size + r], fill=LINE)

    for i, (x, y) in enumerate(nodes):
        r = size * (0.031 if i == 1 else 0.026)
        d.ellipse([x * size - r, y * size - r, x * size + r, y * size + r], fill=NODE + (245,))

    mx, my = node_main[0] * size, node_main[1] * size
    sparkle(d, mx, my, size * 0.118, GOLD + (255,))
    # 主星中心留一点近白的高光，小尺寸下不会糊成一团金色。
    d.ellipse(
        [mx - size * 0.018, my - size * 0.018, mx + size * 0.018, my + size * 0.018],
        fill=GOLD_CORE + (255,),
    )
    return layer


def glow(layer: Image.Image, size: int, scale: float, alpha: float) -> Image.Image:
    """把图层模糊成辉光（只保留亮部，按比例缩小、降低不透明度）。"""
    g = layer.filter(ImageFilter.GaussianBlur(radius=size * scale))
    a = g.split()[3].point(lambda v: int(v * alpha))
    g.putalpha(a)
    return g


def render(size: int = BASE) -> Image.Image:
    w = size * SS
    canvas = Image.new("RGBA", (w, w), (0, 0, 0, 0))

    # 底：圆角方块 + 渐变
    margin = int(w * 0.055)
    radius = int(w * 0.225)
    bg = gradient(w)
    # 左上高光：一块柔和的大圆，模拟打光。
    hl = Image.new("RGBA", (w, w), (0, 0, 0, 0))
    hd = ImageDraw.Draw(hl)
    hr = int(w * 0.62)
    hd.ellipse([-hr * 0.55, -hr * 0.75, hr * 0.75, hr * 0.45], fill=HIGHLIGHT)
    hl = hl.filter(ImageFilter.GaussianBlur(radius=w * 0.09))
    bg = Image.alpha_composite(bg, hl)
    bg.putalpha(rounded_mask(w, margin, radius))
    canvas = Image.alpha_composite(canvas, bg)
    canvas = Image.alpha_composite(canvas, vignette(w, margin, radius))

    # 星表网格（裁剪进圆角内）
    grid = star_chart_layer(w)
    grid.putalpha(Image.composite(grid.split()[3], Image.new("L", (w, w), 0), rounded_mask(w, margin, radius)))
    canvas = Image.alpha_composite(canvas, grid)

    # 星座：先辉光再实体
    stars = constellation_layer(w)
    stars.putalpha(Image.composite(stars.split()[3], Image.new("L", (w, w), 0), rounded_mask(w, margin, radius)))
    canvas = Image.alpha_composite(canvas, glow(stars, w, 0.014, 0.75))
    canvas = Image.alpha_composite(canvas, stars)

    return canvas.resize((size, size), Image.LANCZOS)


def preview(icon: Image.Image, out_dir: str) -> None:
    """把图标按常见尺寸排在深浅两种背景上，便于肉眼检查小尺寸可读性。"""
    sizes = [16, 24, 32, 48, 64, 128, 256]
    pad, gap = 24, 20
    width = pad * 2 + sum(sizes) + gap * (len(sizes) - 1)
    height = pad * 2 + max(sizes) * 2 + 40
    sheet = Image.new("RGBA", (width, height), (255, 255, 255, 255))
    d = ImageDraw.Draw(sheet)
    d.rectangle([0, height // 2, width, height], fill=(24, 28, 36, 255))
    x = pad
    for s in sizes:
        small = icon.resize((s, s), Image.LANCZOS)
        sheet.alpha_composite(small, (x, pad + (max(sizes) - s)))
        sheet.alpha_composite(small, (x, height // 2 + pad + (max(sizes) - s)))
        x += s + gap
    os.makedirs(out_dir, exist_ok=True)
    path = os.path.join(out_dir, "icon-preview.png")
    sheet.save(path)
    print("preview ->", path)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default="resources/icon.png")
    ap.add_argument("--preview", default="", help="额外输出尺寸预览的目录")
    args = ap.parse_args()

    icon = render(BASE)
    os.makedirs(os.path.dirname(args.out) or ".", exist_ok=True)
    icon.save(args.out, optimize=True)
    print("icon ->", args.out, icon.size)
    if args.preview:
        preview(icon, args.preview)
    return 0


if __name__ == "__main__":
    sys.exit(main())
