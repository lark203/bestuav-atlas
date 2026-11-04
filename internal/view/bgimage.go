package view

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"github.com/egoist/mygo/ui"
)

// 背景图解码与缩放。
//
// 用户选的壁纸可能是手机拍的原图（6000×4000 解成 RGBA 就是 96MB），而窗口
// 通常只有一两千像素宽——原尺寸解码既慢又占内存，还会让「选完图界面卡住」。
// 这里在解码后立刻把宽度压到 maxBgWidth，并自己按面积平均缩放：
// x/image/draw 的 CatmullRom 对 JPEG 的 YCbCr 源会多分配两张 float32 中间图，
// 一张 4K 图临时就要上百 MB（在另一个项目里实测过）。

// maxBgWidth 是背景图解码后的最大宽度。
//
// 2560 的取舍：4K 显示器最大化窗口时是 1.5 倍放大（隔着一层 88% 不透明的
// 内容区几乎看不出来），而内存从 33MB 降到 17MB 左右。
const maxBgWidth = 2560

// decodeBackground 解码一张用户选的背景图，必要时缩到宽度 maxW 以内。
func decodeBackground(data []byte, maxW int) (*ui.Bitmap, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	img = fitWidth(img, maxW)
	return ui.NewBitmap(img), nil
}

// fitWidth 把图片等比缩到宽度不超过 maxW；已经够小就原样返回。
func fitWidth(src image.Image, maxW int) image.Image {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw <= maxW || maxW <= 0 || sw == 0 || sh == 0 {
		return src
	}
	h := sh * maxW / sw
	if h < 1 {
		h = 1
	}
	return downscale(src, maxW, h)
}

// downscale 按面积平均把 src 缩到 w×h，只分配目标图。
//
// 绝大多数照片是 4:2:0 的 JPEG 且恰好缩一半，走 fast2x420（4 个 Y + 1 个 Cb/Cr，
// 不经过接口调用）；其它比例走通用面积平均。
func downscale(src image.Image, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if y, ok := src.(*image.YCbCr); ok && sw == 2*w && sh == 2*h &&
		y.SubsampleRatio == image.YCbCrSubsampleRatio420 {
		fast2x420(y, dst)
		return dst
	}
	for dy := 0; dy < h; dy++ {
		y0 := b.Min.Y + dy*sh/h
		y1 := b.Min.Y + (dy+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < w; dx++ {
			x0 := b.Min.X + dx*sw/w
			x1 := b.Min.X + (dx+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, a, n uint32
			for yy := y0; yy < y1; yy++ {
				for xx := x0; xx < x1; xx++ {
					cr, cg, cb, ca := src.At(xx, yy).RGBA()
					r, g, bl, a = r+cr, g+cg, bl+cb, a+ca
					n++
				}
			}
			dst.SetRGBA(dx, dy, color.RGBA{
				R: uint8(r / n >> 8), G: uint8(g / n >> 8),
				B: uint8(bl / n >> 8), A: uint8(a / n >> 8),
			})
		}
	}
	return dst
}

// fast2x420 是 4:2:0 YCbCr 恰好缩小一半的专用路径：
// 每个目标像素取 2×2 的 Y 求平均、共享同一个 Cb/Cr。
func fast2x420(src *image.YCbCr, dst *image.RGBA) {
	b := src.Bounds()
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	for dy := 0; dy < h; dy++ {
		y0 := b.Min.Y + 2*dy
		row := dst.Pix[dy*dst.Stride:]
		for dx := 0; dx < w; dx++ {
			x := b.Min.X + 2*dx
			yy := (uint32(src.Y[src.YOffset(x, y0)]) +
				uint32(src.Y[src.YOffset(x+1, y0)]) +
				uint32(src.Y[src.YOffset(x, y0+1)]) +
				uint32(src.Y[src.YOffset(x+1, y0+1)]) + 2) / 4
			ci := src.COffset(x, y0)
			r, g, bl := color.YCbCrToRGB(uint8(yy), src.Cb[ci], src.Cr[ci])
			row[4*dx], row[4*dx+1], row[4*dx+2], row[4*dx+3] = r, g, bl, 255
		}
	}
}
