package view

import (
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 拓扑画布的视口状态：缩放 / 平移 / 悬停 / 聚焦 / 脉冲。
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

	downX, downY float32 // 按下时的位置（区分「点击」与「拖动」）
	pressed      *graphHit
	moved        bool

	hover              int     // 悬停的节点下标；-1 = 无
	hasPointer         bool    // 本帧指针是否在画布上
	pointerX, pointerY float32 // 指针相对画布的位置（悬停判定用）

	// hits 是最近一帧的节点命中表（画布屏幕坐标），输入命中测试用它。
	hits []graphHit
	// visible 最近一帧经过筛选后可见的节点数（页面上给「没有匹配」提示用）。
	visible int

	w, h float32 // 最近一帧画布尺寸（缩放按钮 / 聚焦换算用）

	focus graphFocus // 双击节点后的镜头推进
	pulse graphPulse // 双击节点后沿连线扩散的脉冲
}

// graphKind 节点代表的实体类型：点击时据此跳转。
type graphKind int

const (
	gkNone graphKind = iota
	gkProject
	gkRepo
	gkTodo
	gkUnassigned // 「未归属」虚拟中枢
)

// graphHit 一个可点击节点的命中信息（画布屏幕坐标）。
type graphHit struct {
	x, y, r float32
	kind    graphKind
	id      int64
	label   string
}

const (
	graphZoomMin     = 0.35
	graphZoomMax     = 4.0
	graphWheelFactor = 0.0022 // 滚轮每像素的缩放指数

	graphClickSlop = 4                       // 按下后移动超过这个距离就算拖动
	graphFocusZoom = 1.9                     // 双击聚焦后的缩放
	graphFocusDur  = 320 * time.Millisecond  // 镜头推进时长
	graphPulseDur  = 1100 * time.Millisecond // 一轮脉冲时长
)

// graphFocus 双击节点后的镜头推进：从当前 zoom/pan 平滑过渡到「节点居中」。
type graphFocus struct {
	active                       bool
	start                        time.Time
	fromZoom, fromPanX, fromPanY float32
	toZoom, toPanX, toPanY       float32
}

// graphPulse 双击节点后的脉冲：沿该节点的每条连线，从中心向外跑一个亮点。
type graphPulse struct {
	active bool
	start  time.Time
	kind   graphKind
	id     int64
	label  string
}

// graphKey 节点身份（筛选/重排后下标会变，脉冲与聚焦用身份定位）。
type graphKey struct {
	kind  graphKind
	id    int64
	label string
}

// reset 回到初始视图：1 倍、不偏移；筛选条件在 atlasUI 里，不随视图重置。
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

// world 画布坐标 → 世界坐标。
func (g *graphView) world(mx, my float32) (float32, float32) {
	g.ensure()
	return (mx - g.panX) / g.zoom, (my - g.panY) / g.zoom
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

// hitAt 返回画布坐标 (x,y) 处的节点；没有返回 nil。
func (g *graphView) hitAt(x, y float32) *graphHit {
	for i := range g.hits {
		h := &g.hits[i]
		d := float32(math.Hypot(float64(x-h.x), float64(y-h.y)))
		if d <= h.r+8 {
			return h
		}
	}
	return nil
}

// focus 双击某个节点：把镜头推到该节点居中，并从这里发出一轮脉冲。
// 节点世界坐标由调用方用当前 zoom/pan 反算后传入。
func (g *graphView) focusOn(h graphHit, wx, wy float32, now time.Time) {
	targetZoom := maxFloat(g.zoom, graphFocusZoom)
	g.focus = graphFocus{
		active:   true,
		start:    now,
		fromZoom: g.zoom, fromPanX: g.panX, fromPanY: g.panY,
		toZoom: targetZoom,
		toPanX: g.w/2 - wx*targetZoom,
		toPanY: g.h/2 - wy*targetZoom,
	}
	g.pulse = graphPulse{active: true, start: now, kind: h.kind, id: h.id, label: h.label}
}

// advance 推进镜头动画与脉冲；返回是否还需要下一帧。
func (g *graphView) advance(now time.Time) bool {
	if g.focus.active {
		t := float32(now.Sub(g.focus.start)) / float32(graphFocusDur)
		if t >= 1 {
			g.zoom, g.panX, g.panY = g.focus.toZoom, g.focus.toPanX, g.focus.toPanY
			g.focus.active = false
		} else {
			if t < 0 {
				t = 0
			}
			e := 1 - float32(math.Pow(1-float64(t), 3)) // ease-out cubic
			g.zoom = g.focus.fromZoom + (g.focus.toZoom-g.focus.fromZoom)*e
			g.panX = g.focus.fromPanX + (g.focus.toPanX-g.focus.fromPanX)*e
			g.panY = g.focus.fromPanY + (g.focus.toPanY-g.focus.fromPanY)*e
		}
	}
	if g.pulse.active && now.Sub(g.pulse.start) > graphPulseDur+300*time.Millisecond {
		g.pulse.active = false
	}
	return g.focus.active || g.pulse.active
}

// graphCanvasInput 处理拓扑画布的输入：滚轮缩放、拖动平移、单击跳转、双击聚焦。
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
		g.downX, g.downY = ev.X, ev.Y
		g.moved = false
		g.pressed = g.hitAt(ev.X, ev.Y)
		// 双击（ev.Clicks>=2）：聚焦该节点并发一轮脉冲，不进入拖动。
		// 同时清掉 pressed：双击的抬起不应再被当成「单击跳转」，否则页面会
		// 立刻跳走，聚焦和脉冲根本看不见。
		if ev.Clicks >= 2 && g.pressed != nil {
			wx, wy := g.world(ev.X, ev.Y)
			g.focusOn(*g.pressed, wx, wy, time.Now())
			g.pressed, g.dragging, g.moved = nil, false, false
			a.requestFrame()
			return true
		}
		g.dragging = false
		return true
	case ui.InputPointerMove:
		if g.pressed == nil && !g.dragging {
			return false
		}
		if !g.moved {
			dx, dy := ev.X-g.downX, ev.Y-g.downY
			if dx*dx+dy*dy > graphClickSlop*graphClickSlop {
				g.moved = true
				g.dragging = true
				g.dragX, g.dragY = ev.X, ev.Y
			}
			return true
		}
		g.panX += ev.X - g.dragX
		g.panY += ev.Y - g.dragY
		g.dragX, g.dragY = ev.X, ev.Y
		g.clampPan()
		a.requestFrame()
		return true
	case ui.InputPointerUp:
		if g.pressed == nil && !g.dragging {
			return false
		}
		pressed := g.pressed
		wasDrag := g.dragging
		g.pressed, g.dragging, g.moved = nil, false, false
		// 没拖动 = 单击：命中节点就跳到对应页面。
		if !wasDrag && pressed != nil {
			a.graphActivate(*pressed)
		}
		return true
	}
	return false
}

// graphActivate 单击节点：仓库进详情、项目回项目页、待办回待办页。
func (a *App) graphActivate(h graphHit) {
	switch h.kind {
	case gkRepo:
		if r, ok := a.repoByID(h.id); ok {
			a.openRepoDetail(r)
		}
	case gkProject, gkUnassigned:
		a.page = "projects"
	case gkTodo:
		a.page = "todos"
	}
}

// ---- 筛选 ----

// filterGraph 按标签筛选：命中节点 + 它们的一跳邻居（保留上下文），并重排下标。
//
// Obsidian 的搜索也是这样：只留命中项会让图失去意义，所以把直接相连的邻居一起留着。
func filterGraph(nodes []graphNode, edges [][2]int, query string) ([]graphNode, [][2]int) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nodes, edges
	}
	matched := make([]bool, len(nodes))
	keep := make([]bool, len(nodes))
	for i := range nodes {
		if strings.Contains(strings.ToLower(nodes[i].label), q) {
			matched[i], keep[i] = true, true
		}
	}
	for _, e := range edges {
		a, b := e[0], e[1]
		if a < 0 || b < 0 || a >= len(nodes) || b >= len(nodes) {
			continue
		}
		if matched[a] {
			keep[b] = true
		}
		if matched[b] {
			keep[a] = true
		}
	}
	idx := make(map[int]int, len(nodes))
	out := make([]graphNode, 0, len(nodes))
	for i := range nodes {
		if !keep[i] {
			continue
		}
		idx[i] = len(out)
		out = append(out, nodes[i])
	}
	kept := make([][2]int, 0, len(edges))
	for _, e := range edges {
		a, okA := idx[e[0]]
		b, okB := idx[e[1]]
		if okA && okB {
			kept = append(kept, [2]int{a, b})
		}
	}
	return out, kept
}

// ---- 几何 ----

// graphCurve 画一条「从 a 到 b」的二次贝塞尔弧线。
//
// 为什么不用直线：一个项目连十个仓库时，十条直线会在中枢附近叠成一条粗线，
// 看起来像「几个节点共用一条线」。每条边给一点固定方向的弯曲（相邻边交替左右），
// 线就散开了——这也是 Obsidian 图里连线的观感。
//
// 起点/终点各退到圆点边缘，线不会压在圆点上。
func graphCurve(p *ui.Painter, x1, y1, r1, x2, y2, r2, bend, width float32, col ui.Color) {
	sx, sy, cx, cy, ex, ey := graphCurvePoints(x1, y1, r1, x2, y2, r2, bend)
	if sx == ex && sy == ey {
		return
	}
	p.StrokePath((&ui.Path{}).MoveTo(sx, sy).QuadTo(cx, cy, ex, ey), width, col)
}

// graphCurvePoints 计算一条边的起点、控制点、终点（都退到圆点边缘）。
func graphCurvePoints(x1, y1, r1, x2, y2, r2, bend float32) (sx, sy, cx, cy, ex, ey float32) {
	dx, dy := x2-x1, y2-y1
	dist := float32(math.Hypot(float64(dx), float64(dy)))
	if dist < 1 {
		return x1, y1, x1, y1, x1, y1
	}
	ux, uy := dx/dist, dy/dist
	sx, sy = x1+ux*r1, y1+uy*r1
	ex, ey = x2-ux*r2, y2-uy*r2
	mx, my := (sx+ex)/2, (sy+ey)/2
	// 法线方向 = 垂直于边；弯曲量按边长比例，短边不会弯成怪物。
	cx, cy = mx-uy*bend*dist, my+ux*bend*dist
	return
}

// quadPoint 二次贝塞尔在 t 处的点。
func quadPoint(sx, sy, cx, cy, ex, ey, t float32) (float32, float32) {
	u := 1 - t
	return u*u*sx + 2*u*t*cx + t*t*ex, u*u*sy + 2*u*t*cy + t*t*ey
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

// maxFloat 两个浮点数的较大值。
func maxFloat(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}
