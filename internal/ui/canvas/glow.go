package canvas

import (
	"image"
	"math"
	"sync"

	"github.com/egoist/mygo/ui"
)

// 画家能把矩形、路径糊成渐变，但没有「把一团形状糊开」的滤镜。CSS 里一行
// filter: blur() 或 box-shadow 就能出的柔光，这里只能先算出一张位图再贴上去。
//
// 位图按「颜色 + 量到 1/32 的不透明度」缓存：每帧重算 96×96 个像素太贵，而
// 不透明度在一帧内是连续变化的，量化到 32 级肉眼无损，还能把缓存条目数压住。

// glowSteps 不透明度的量化级数。
const glowSteps = 32

// glowCacheMax 是柔光位图缓存的上限（每张 96×96 RGBA ≈ 36KB）。
// 键里带颜色，主题/状态色多起来时条目会缓慢增长，超过就整体清空重来。
const glowCacheMax = 256

// glowSize 柔光位图的边长（像素）。它会被缩放到调用方要的直径，所以只要够大
// 到放大后不糊即可。
const glowSize = 96

var (
	glowMu  sync.Mutex
	glowBmp = map[uint64]*ui.Bitmap{}
)

// Glow 在 (cx, cy) 画一团半径 r DIP 的柔光，颜色 c、不透明度 a（0→1）。
//
// a 量化到 1/32：小于半级的直接不画（省一次贴图）。柔光是叠加的，画之前不必
// 自己铺底。
func Glow(p *ui.Painter, cx, cy, r float32, c ui.Color, a float32) {
	step := int(a*glowSteps + 0.5)
	if step <= 0 {
		return
	}
	p.Image(GlowBitmap(c, float64(step)/glowSteps),
		ui.Rect{X: cx - r, Y: cy - r, W: 2 * r, H: 2 * r}, ui.FillBox)
}

// GlowBitmap 返回那团柔光的位图本身（中心亮、边缘透明），给要自己贴、或要
// 反复贴同一团的场合。同样的颜色与不透明度返回同一个位图。
func GlowBitmap(c ui.Color, a float64) *ui.Bitmap {
	step := int(a*glowSteps + 0.5)
	key := uint64(step)<<24 | uint64(c.R)<<16 | uint64(c.G)<<8 | uint64(c.B)
	glowMu.Lock()
	defer glowMu.Unlock()
	if bmp := glowBmp[key]; bmp != nil {
		return bmp
	}
	img := image.NewRGBA(image.Rect(0, 0, glowSize, glowSize))
	alpha := float64(step) / glowSteps
	for y := range glowSize {
		for x := range glowSize {
			dx, dy := (float64(x)+0.5)/glowSize*2-1, (float64(y)+0.5)/glowSize*2-1
			d2 := dx*dx + dy*dy
			// 高斯核管中心那团，再乘一个边缘归零的项，免得位图四边被切出硬边。
			v := alpha * math.Exp(-d2*4.5) * math.Max(0, 1-d2)
			i := img.PixOffset(x, y)
			img.Pix[i] = uint8(float64(c.R)*v + 0.5)
			img.Pix[i+1] = uint8(float64(c.G)*v + 0.5)
			img.Pix[i+2] = uint8(float64(c.B)*v + 0.5)
			img.Pix[i+3] = uint8(255*v + 0.5)
		}
	}
	bmp := ui.NewBitmap(img)
	if len(glowBmp) >= glowCacheMax {
		clear(glowBmp)
	}
	glowBmp[key] = bmp
	return bmp
}
