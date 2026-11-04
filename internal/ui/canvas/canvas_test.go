package canvas

import (
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/imgtest"
)

// 本包画的是像素，测试也就只能落到像素上：离屏渲一帧，然后数/比像素。
//
// ⚠️ ui.Render 自己要跑一帧，所以只能在测试函数里直接调（不能在 Tester 的某一帧
// 里调）。下面每个用例都是独立的一帧，互不干扰。

var testFont = ui.Font{Family: "system-ui", Size: 12, Antialiased: true}

// render 渲一帧 w×h，背景透明，画什么由 fn 定。
func render(t *testing.T, w, h int, fn func(p *ui.Painter)) *image.RGBA {
	t.Helper()
	return ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Box(c).Fill().Draw(func(p *ui.Painter, _ ui.Rect) { fn(p) })
	}, w, h, 1)
}

// TestRoundRect 圆角矩形：中心是实的，四个角外面是空的（圆角真的倒了）。
func TestRoundRect(t *testing.T) {
	const n = 40
	img := render(t, n, n, func(p *ui.Painter) {
		var path ui.Path
		RoundRect(&path, 0, 0, n, n, 10)
		p.FillPath(&path, ui.Hex("#ff0000"))
	})
	if got := imgtest.At(img, n/2, n/2); !imgtest.Near(got, ui.Hex("#ff0000"), 8) || got.A < 250 {
		t.Errorf("中心应是实心红，实际 %v", got)
	}
	// 角上的点：在半径 10 的圆外（距角点 0,0 的距离大于 10）。
	for _, c := range [][2]int{{0, 0}, {n - 1, 0}, {0, n - 1}, {n - 1, n - 1}} {
		if got := imgtest.At(img, c[0], c[1]); got.A > 8 {
			t.Errorf("角点 (%d,%d) 应在圆角外（透明），实际 alpha %d", c[0], c[1], got.A)
		}
	}
	// 边中点：圆角之外仍是实的。
	if got := imgtest.At(img, 0, n/2); got.A < 250 {
		t.Errorf("左边中点应是实的，实际 alpha %d", got.A)
	}
}

// TestRoundRectCapsule r 取短边一半得到胶囊形：两端是半圆，中间是矩形。
func TestRoundRectCapsule(t *testing.T) {
	img := render(t, 40, 20, func(p *ui.Painter) {
		var path ui.Path
		RoundRect(&path, 0, 0, 40, 20, 10)
		p.FillPath(&path, ui.Hex("#00ff00"))
	})
	if got := imgtest.At(img, 20, 10); !imgtest.Near(got, ui.Hex("#00ff00"), 8) {
		t.Errorf("胶囊中心应是绿，实际 %v", got)
	}
	if got := imgtest.At(img, 1, 1); got.A > 8 {
		t.Errorf("胶囊左上角应在圆外，实际 alpha %d", got.A)
	}
}

// TestGlow 柔光：中心最亮、往外衰减、四个角干净；同样的参数命中缓存。
func TestGlow(t *testing.T) {
	const n = 60
	img := render(t, n, n, func(p *ui.Painter) {
		Glow(p, n/2, n/2, n/2, ui.Hex("#ffffff"), 0.8)
	})
	center := imgtest.At(img, n/2, n/2).A
	mid := imgtest.At(img, n/2+15, n/2).A
	edge := imgtest.At(img, n-1, n-1).A
	if center <= mid {
		t.Errorf("柔光中心（alpha %d）应亮过中段（%d）", center, mid)
	}
	if mid <= 0 {
		t.Error("柔光中段应该还有一点亮，实际全灭了 —— 衰减太陡")
	}
	if edge > 8 {
		t.Errorf("柔光不该铺到画布角上，实际 alpha %d", edge)
	}
	// 弱到量化为 0 的柔光干脆不画。
	blank := render(t, n, n, func(p *ui.Painter) {
		Glow(p, n/2, n/2, n/2, ui.Hex("#ffffff"), 0.001)
	})
	if n := opaquePixels(blank); n != 0 {
		t.Errorf("不透明度 0.001 的柔光不该画任何东西，实际 %d 个像素", n)
	}
}

// TestGlowBitmapCached 同色同不透明度返回同一张位图（每帧重算 96×96 太贵）。
func TestGlowBitmapCached(t *testing.T) {
	a := GlowBitmap(ui.Hex("#7c74ff"), 0.4)
	b := GlowBitmap(ui.Hex("#7c74ff"), 0.4)
	if a != b {
		t.Error("同样的颜色与不透明度应命中缓存，返回同一张位图")
	}
	if a == GlowBitmap(ui.Hex("#7c74ff"), 0.9) {
		t.Error("不透明度不同应是不同的位图")
	}
	if a == GlowBitmap(ui.Hex("#000000"), 0.4) {
		t.Error("颜色不同应是不同的位图")
	}
}

// TestWordLetters 逐字母排版：段数等于字母数，各段偏移累加等于整串宽度。
func TestWordLetters(t *testing.T) {
	w := Letters("Low", testFont)
	if w.Len() != 3 {
		t.Fatalf("Low 应是 3 段，实际 %d", w.Len())
	}
	var sum float32
	for i := range w.Len() {
		if i > 0 && w.Advance(i) <= w.Advance(i-1) {
			t.Errorf("第 %d 段的偏移（%.2f）应比前一段靠右", i, w.Advance(i))
		}
		for _, g := range w.Glyphs(i) {
			sum += g.Advance
		}
	}
	if math.Abs(float64(sum-w.Width())) > 0.01 {
		t.Errorf("各段宽度之和 %.3f 与整串宽度 %.3f 对不上", sum, w.Width())
	}
	if w.Width() <= 0 {
		t.Errorf("整串宽度应是正数，实际 %v", w.Width())
	}
}

// TestWordKerned 整串成形：只有一段（没法逐字母动画，但字距是准的）。
func TestWordKerned(t *testing.T) {
	k := Kerned("Low", testFont)
	if k.Len() != 1 {
		t.Errorf("整串成形应只有 1 段，实际 %d", k.Len())
	}
	if k.Width() <= 0 {
		t.Errorf("整串宽度应是正数，实际 %v", k.Width())
	}
	if k.Advance(0) != 0 {
		t.Errorf("整串成形第一段的偏移应是 0，实际 %v", k.Advance(0))
	}
}

// TestWordCached 同样 (文本, 字体) 命中缓存：成形不便宜，标签就那么几个词。
func TestWordCached(t *testing.T) {
	a := Kerned("Medium", testFont)
	if b := Kerned("Medium", testFont); a.Width() != b.Width() {
		t.Error("同样的文本与字体应命中缓存（宽度都该一样）")
	}
	other := ui.Font{Family: "system-ui", Size: 20, Antialiased: true}
	if Kerned("Medium", other).Width() == a.Width() {
		t.Error("字号不同，宽度不该一样 —— 缓存 key 大概漏了字体")
	}
}

// TestTextSheet 模糊字：糊开之后覆盖的像素比清晰字形多（真的糊出去了），
// 且字形中心比清晰时淡（墨被摊开了）。
func TestTextSheet(t *testing.T) {
	const w, h = 60, 30
	ts := NewTextSheet(testFont, "Low")
	if !ts.Has("Low") || !ts.Has("w") {
		t.Fatal("字表应含整词，也含它的每个字母（逐字母动画要用）")
	}
	if ts.Has("zz") {
		t.Error("没给字表的词不该有")
	}
	sharp := render(t, w, h, func(p *ui.Painter) {
		ts.Draw(p, "Low", 4, 20, 0, ui.Hex("#ffffff"))
	})
	blurred := render(t, w, h, func(p *ui.Painter) {
		ts.Draw(p, "Low", 4, 20, 2, ui.Hex("#ffffff"))
	})
	if n, m := opaquePixels(sharp), opaquePixels(blurred); m <= n {
		t.Errorf("糊开后覆盖的像素（%d）应多于清晰字形（%d）", m, n)
	}
	// 清晰字形最浓的那一点，糊开之后要变淡（墨被摊开了）。
	if a, b := darkest(sharp), darkest(blurred); b >= a {
		t.Errorf("糊开后最浓的一点（alpha %d）应淡于清晰时（%d）", b, a)
	}
}

// TestTextSheetUnknownText 字表里没有的词不该画崩，退化成清晰字形。
func TestTextSheetUnknownText(t *testing.T) {
	ts := NewTextSheet(testFont, "Low")
	img := render(t, 60, 30, func(p *ui.Painter) {
		ts.Draw(p, "Unknown", 4, 20, 2, ui.Hex("#ffffff"))
	})
	if n := opaquePixels(img); n == 0 {
		t.Error("字表里没有的词应退化成清晰字形，而不是什么都不画")
	}
}

// opaquePixels 图里有多少像素不是全透明。
func opaquePixels(img *image.RGBA) int {
	n := 0
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			if imgtest.At(img, x, y).A > 4 {
				n++
			}
		}
	}
	return n
}

// darkest 图里不透明度的最大值（字形是白字透明底，alpha 就是墨的浓淡）。
func darkest(img *image.RGBA) uint8 {
	var max uint8
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			max = max2(max, imgtest.At(img, x, y).A)
		}
	}
	return max
}

func max2(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}
