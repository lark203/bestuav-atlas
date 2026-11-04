package view

import (
	"math"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/pet"
	"bestuav-atlas/internal/settings"
)

// 桌宠的矢量画法：全部用 ui.Painter 画路径（圆/椭圆/三角/曲线），没有图片。
//
// 坐标系统一用 100×100 的「虚拟画布」：按可用区域等比缩放并居中，这样窗口大小、
// 成长阶段（缩放）变化都不用改任何坐标。
//
// 姿势：Sit 坐、Lie 侧卧（长时间无互动）、Walk 走（点击后）、Lick 舔爪（点击后）。

// petPose 桌宠姿势。
type petPose int

const (
	poseSit petPose = iota
	poseLie
	poseWalk
	poseLick
)

// petPalette 一只宠物的配色。
type petPalette struct {
	fur, furDark, belly, line, pink, ear ui.Color
}

var (
	catPalette = petPalette{
		fur: ui.Hex("#f5b969"), furDark: ui.Hex("#e3a04a"), belly: ui.Hex("#ffe9c6"),
		line: ui.Hex("#4a3324"), pink: ui.Hex("#f2a3a3"), ear: ui.Hex("#f7c98d"),
	}
	dogPalette = petPalette{
		fur: ui.Hex("#c08a5a"), furDark: ui.Hex("#a06f42"), belly: ui.Hex("#f2ddc2"),
		line: ui.Hex("#3b2a1b"), pink: ui.Hex("#e8958f"), ear: ui.Hex("#9a6a3f"),
	}
)

// drawPetArt 把当前姿势画在 r 里（等比缩放、居中）。accent 用于项圈。
func drawPetArt(p *ui.Painter, r ui.Rect, skin string, pose petPose, phase float32, accent ui.Color) {
	pal := catPalette
	if pet.NormalizeSkin(skin) == settings.PetSkinDog {
		pal = dogPalette
	}
	s := float32(math.Min(float64(r.W), float64(r.H))) / 100
	if s <= 0 {
		return
	}
	ox := r.X + (r.W-100*s)/2
	oy := r.Y + (r.H-100*s)/2
	x := func(v float32) float32 { return ox + v*s }
	y := func(v float32) float32 { return oy + v*s }
	w := func(v float32) float32 { return v * s }

	ellipse := func(cx, cy, rx, ry float32) *ui.Path {
		const k = 0.5523
		var q ui.Path
		q.MoveTo(x(cx-rx), y(cy))
		q.CubeTo(x(cx-rx), y(cy-ry*k), x(cx-rx*k), y(cy-ry), x(cx), y(cy-ry))
		q.CubeTo(x(cx+rx*k), y(cy-ry), x(cx+rx), y(cy-ry*k), x(cx+rx), y(cy))
		q.CubeTo(x(cx+rx), y(cy+ry*k), x(cx+rx*k), y(cy+ry), x(cx), y(cy+ry))
		q.CubeTo(x(cx-rx*k), y(cy+ry), x(cx-rx), y(cy+ry*k), x(cx-rx), y(cy))
		q.Close()
		return &q
	}
	fill := func(path *ui.Path, c ui.Color) { p.FillPath(path, c) }

	// 尾巴：先画（在身体后面），三段贝塞尔，末端略细。
	tail := func(x0, y0, c1x, c1y, c2x, c2y, x1, y1 float32) {
		var q ui.Path
		q.MoveTo(x(x0), y(y0))
		q.CubeTo(x(c1x), y(c1y), x(c2x), y(c2y), x(x1), y(y1))
		q.Cap(ui.RoundCap)
		p.StrokePath(&q, w(5), pal.furDark)
	}

	// 眼睛：睁（圆 + 高光）或闭（两道弯）。
	openEye := func(cx, cy, r float32) {
		fill(ellipse(cx, cy, r, r*1.15), pal.line)
		fill(ellipse(cx+r*0.35, cy-r*0.4, r*0.34, r*0.34), ui.RGB(255, 255, 255))
	}
	closedEye := func(cx, cy, r float32) {
		var q ui.Path
		q.MoveTo(x(cx-r), y(cy))
		q.QuadTo(x(cx), y(cy+r*0.9), x(cx+r), y(cy))
		q.Cap(ui.RoundCap)
		p.StrokePath(&q, w(1.6), pal.line)
	}
	mouth := func(cx, cy float32, open bool) {
		var q ui.Path
		q.MoveTo(x(cx), y(cy))
		q.QuadTo(x(cx-3), y(cy+2.4), x(cx-5), y(cy+0.6))
		q.MoveTo(x(cx), y(cy))
		q.QuadTo(x(cx+3), y(cy+2.4), x(cx+5), y(cy+0.6))
		q.Cap(ui.RoundCap)
		p.StrokePath(&q, w(1.2), pal.line)
		if open {
			fill(ellipse(cx, cy+3.2, 2.4, 3.0), pal.pink)
		}
	}
	whiskers := func(cx, cy float32) {
		p.Line(x(cx-16), y(cy), x(cx-26), y(cy-2), w(1), pal.furDark)
		p.Line(x(cx-16), y(cy+2.5), x(cx-26), y(cy+3.5), w(1), pal.furDark)
		p.Line(x(cx+16), y(cy), x(cx+26), y(cy-2), w(1), pal.furDark)
		p.Line(x(cx+16), y(cy+2.5), x(cx+26), y(cy+3.5), w(1), pal.furDark)
	}

	// 耳朵：猫是尖三角（内粉），狗是垂耳（椭圆）。
	drawEars := func(cx, cy, r float32, back bool) {
		if pet.NormalizeSkin(skin) == settings.PetSkinDog {
			fill(ellipse(cx-r*0.95, cy+r*0.35, r*0.36, r*0.85), pal.ear)
			fill(ellipse(cx+r*0.95, cy+r*0.35, r*0.36, r*0.85), pal.ear)
			return
		}
		if back {
			return
		}
		for _, sgn := range []float32{-1, 1} {
			var ear ui.Path
			ear.MoveTo(x(cx+sgn*r*0.88), y(cy-r*0.22))
			ear.LineTo(x(cx+sgn*r*0.62), y(cy-r*1.05))
			ear.QuadTo(x(cx+sgn*r*0.9), y(cy-r*1.1), x(cx+sgn*r*1.12), y(cy-r*0.5))
			ear.Close()
			fill(&ear, pal.fur)
			var inner ui.Path
			inner.MoveTo(x(cx+sgn*r*0.82), y(cy-r*0.4))
			inner.LineTo(x(cx+sgn*r*0.7), y(cy-r*0.88))
			inner.LineTo(x(cx+sgn*r*0.98), y(cy-r*0.62))
			inner.Close()
			fill(&inner, pal.pink)
		}
	}

	head := func(cx, cy, r float32, eyesClosed, happy bool) {
		drawEars(cx, cy, r, false)
		fill(ellipse(cx, cy, r, r*0.95), pal.fur)
		// 额头三道斑纹（猫）
		if pet.NormalizeSkin(skin) == settings.PetSkinCat {
			for i := -1; i <= 1; i++ {
				p.Line(x(cx+float32(i)*r*0.28), y(cy-r*0.72),
					x(cx+float32(i)*r*0.34), y(cy-r*0.45), w(1.4), pal.furDark)
			}
		}
		if eyesClosed {
			closedEye(cx-r*0.36, cy-r*0.06, r*0.2)
			closedEye(cx+r*0.36, cy-r*0.06, r*0.2)
		} else {
			openEye(cx-r*0.36, cy-r*0.08, r*0.19)
			openEye(cx+r*0.36, cy-r*0.08, r*0.19)
		}
		// 腮红
		fill(ellipse(cx-r*0.62, cy+r*0.28, r*0.14, r*0.09), ui.RGBA(pal.pink.R, pal.pink.G, pal.pink.B, 0.55))
		fill(ellipse(cx+r*0.62, cy+r*0.28, r*0.14, r*0.09), ui.RGBA(pal.pink.R, pal.pink.G, pal.pink.B, 0.55))

		// 鼻子（小三角）与嘴
		var nose ui.Path
		nose.MoveTo(x(cx-r*0.12), y(cy+r*0.2))
		nose.LineTo(x(cx+r*0.12), y(cy+r*0.2))
		nose.LineTo(x(cx), y(cy+r*0.34))
		nose.Close()
		fill(&nose, pal.pink)
		mouth(cx, cy+r*0.36, happy)
		whiskers(cx, cy+r*0.2)
	}

	body := func(cx, cy, rx, ry float32) {
		fill(ellipse(cx, cy, rx, ry), pal.fur)
		fill(ellipse(cx, cy+ry*0.28, rx*0.62, ry*0.55), pal.belly)
	}

	collar := func(cx, cy, hw float32) {
		fill(ellipse(cx, cy, hw, 3.4), accent)
		fill(ellipse(cx, cy+4.2, 2.6, 2.6), ui.Hex("#f2c14e"))
	}

	switch pose {
	case poseLie:
		tail(70, 86, 86, 82, 92, 92, 80, 94)
		body(52, 82, 29, 13)
		// 前爪垫在下巴下
		fill(ellipse(30, 88, 8, 4.5), pal.furDark)
		fill(ellipse(40, 90, 8, 4.5), pal.furDark)
		head(30, 66, 17, true, false)
		// 尾巴尖的深色
		fill(ellipse(80, 94, 3, 3), pal.furDark)
	case poseWalk:
		bob := float32(math.Sin(float64(phase)*math.Pi*4)) * 1.6
		tail(72, 66+bob, 86, 56+bob, 90, 46+bob, 88, 42+bob)
		// 四条腿：对角交替
		legs := []struct {
			x     float32
			phase float32
		}{{38, 0}, {45, math.Pi}, {56, math.Pi}, {63, 0}}
		for _, l := range legs {
			dy := float32(math.Sin(float64(phase)*math.Pi*4+float64(l.phase))) * 3
			p.Fill(ui.Rect{X: x(l.x - 3.2), Y: y(78 + bob + dy), W: w(6.4), H: w(13)},
				pal.furDark, w(3.2))
		}
		body(50, 68+bob, 24, 15)
		head(50, 41+bob, 17, false, true)
		collar(50, 55+bob, 9)
	case poseLick:
		tail(66, 84, 84, 78, 84, 62, 74, 58)
		body(50, 72, 21, 22)
		// 抬起的一只前爪，靠近嘴
		fill(ellipse(59, 50, 6.5, 9), pal.fur)
		fill(ellipse(59, 54, 3.4, 4.6), pal.pink)
		head(49, 34, 19, true, true)
		collar(49, 54, 10)
	default: // poseSit
		tail(66, 82, 86, 74, 90, 60, 80, 58)
		body(50, 71, 21, 22)
		fill(ellipse(39, 92, 7, 4.6), pal.furDark)
		fill(ellipse(61, 92, 7, 4.6), pal.furDark)
		head(50, 34, 19, false, false)
		collar(50, 53, 10)
	}
}
