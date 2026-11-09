package view

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// 拓扑画布的视口状态：缩放 / 平移 / 悬停。
//
// 坐标分两层：
//   - 世界坐标：布局函数算出来的节点位置（画布大小的局部坐标）；
//   - 屏幕坐标：加上缩放与平移之后、真正画到画布上的位置。
//
// 缩放以鼠标位置为锚点：该点下方的世界坐标保持不动（见 zoomAt）。
type graphView struct {
	zoom float32 // 1 = 适应画布；范围 graphZoomMin..graphZoomMax
	panX float32
	panY float32

	dragging     bool
	dragX, dragY float32 // 上一次指针位置（拖动平移）

	hover              int     // 悬停的节点下标；-1 = 无
	hasPointer         bool    // 本帧指针是否在画布上
	pointerX, pointerY float32 // 指针相对画布的位置（悬停判定用）

	w, h float32 // 最近一帧画布尺寸（缩放按钮用）
}

const (
	graphZoomMin     = 0.35
	graphZoomMax     = 4.0
	graphWheelFactor = 0.0022 // 滚轮每像素的缩放指数
)

// reset 回到初始视图：1 倍、不偏移。
func (g *graphView) reset() { *g = graphView{zoom: 1, hover: -1} }

// ensure 兜底：零值视图按 1 倍处理。
func (g *graphView) ensure() {
	if g.zoom <= 0 {
		g.zoom = 1
	}
	if !g.hasPointer {
		g.hover = -1
	}
}

// zoomAt 以画布内坐标 (mx,my) 为锚点缩放 factor 倍：
// 锚点下方的世界坐标在缩放前后保持不动，所以「滚轮往哪滚，就以哪里为中心」。
func (g *graphView) zoomAt(mx, my, factor float32) {
	g.ensure()
	if factor <= 0 {
		return
	}
	z := clampFloat(g.zoom*factor, graphZoomMin, graphZoomMax)
	if z == g.zoom {
		return
	}
	k := z / g.zoom
	g.panX = mx - (mx-g.panX)*k
	g.panY = my - (my-g.panY)*k
	g.zoom = z
	g.clampPan()
}

// zoomCenter 以画布中心为锚点缩放（页面上的 + / − 按钮用）。
func (g *graphView) zoomCenter(factor float32) {
	g.zoomAt(g.w/2, g.h/2, factor)
}

// screen 世界坐标 → 画布坐标。
func (g *graphView) screen(x, y float32) (float32, float32) {
	g.ensure()
	return g.panX + x*g.zoom, g.panY + y*g.zoom
}

// clampPan 别让画布被拖到找不回来：平移量限制在画布尺寸的两倍以内。
func (g *graphView) clampPan() {
	limit := func(v, span float32) float32 {
		if span <= 0 {
			return v
		}
		max := span * 2
		if v > max {
			return max
		}
		if v < -max {
			return -max
		}
		return v
	}
	g.panX = limit(g.panX, g.w)
	g.panY = limit(g.panY, g.h)
}

// graphCanvasInput 处理拓扑画布的输入：滚轮缩放（以鼠标为中心）、拖动平移。
//
// 返回 true 表示消费掉事件：滚轮不会再滚页面，拖动也不会触发其它手势。
func (a *App) graphCanvasInput(ev ui.InputEvent) bool {
	g := &a.atlasUI.graph
	switch ev.Kind {
	case ui.InputScroll:
		if ev.DY == 0 {
			return false
		}
		factor := float32(math.Exp(-float64(ev.DY) * graphWheelFactor))
		g.zoomAt(ev.X, ev.Y, factor)
		a.requestFrame()
		return true
	case ui.InputZoom:
		// 触控板双指捏合 / Windows 下 Ctrl+滚轮：直接按比例缩放。
		if ev.Scale <= 0 {
			return false
		}
		g.zoomAt(ev.X, ev.Y, ev.Scale)
		a.requestFrame()
		return true
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		g.dragging = true
		g.dragX, g.dragY = ev.X, ev.Y
		return true
	case ui.InputPointerMove:
		if !g.dragging {
			return false
		}
		g.panX += ev.X - g.dragX
		g.panY += ev.Y - g.dragY
		g.dragX, g.dragY = ev.X, ev.Y
		g.clampPan()
		a.requestFrame()
		return true
	case ui.InputPointerUp:
		if !g.dragging {
			return false
		}
		g.dragging = false
		return true
	}
	return false
}

// graphCurve 画一条「从 a 到 b」的二次贝塞尔弧线。
//
// 为什么不用直线：一个项目连十个仓库时，十条直线会在中枢附近叠成一条粗线，
// 看起来像「几个节点共用一条线」。每条边给一点固定方向的弯曲（相邻边交替左右），
// 线就散开了——这也是 Obsidian 图里连线的观感。
//
// 起点/终点各退到圆点边缘，线不会压在圆点上。
func graphCurve(p *ui.Painter, x1, y1, r1, x2, y2, r2, bend, width float32, col ui.Color) {
	dx, dy := x2-x1, y2-y1
	dist := float32(math.Hypot(float64(dx), float64(dy)))
	if dist < 1 {
		return
	}
	ux, uy := dx/dist, dy/dist
	sx, sy := x1+ux*r1, y1+uy*r1
	ex, ey := x2-ux*r2, y2-uy*r2
	mx, my := (sx+ex)/2, (sy+ey)/2
	// 法线方向 = 垂直于边；弯曲量按边长比例，短边不会弯成怪物。
	cx, cy := mx-uy*bend*dist, my+ux*bend*dist
	p.StrokePath((&ui.Path{}).MoveTo(sx, sy).QuadTo(cx, cy, ex, ey), width, col)
}

// clampFloat 把 v 夹在 [lo,hi]。
func clampFloat(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
