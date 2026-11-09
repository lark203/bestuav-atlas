package view

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
)

// 拓扑画布的视口状态：缩放 / 平移 / 悬停 / 悬停脉冲。
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

	// 绘制用的复用缓冲：每帧重建但容量保留，避免缩放/脉冲期间反复分配。
	ptsBuf   [][2]float32 // 节点屏幕坐标
	curveBuf [][2]float32 // 一条边的采样折线
	adjacent []bool       // 悬停节点的相邻标记（按节点下标）
	nodesBuf []graphNode  // 图模型：节点（每帧重填，不重新分配）
	edgesBuf [][2]int     // 图模型：连线

	w, h float32 // 最近一帧画布尺寸（缩放按钮用）

	pulse graphPulse // 悬停节点后沿连线逐跳扩散的脉冲
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

// graphKey 节点身份：筛选/重排/重新布局后下标会变，脉冲用身份定位节点。
type graphKey struct {
	kind  graphKind
	id    int64
	label string
}

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

	graphClickSlop = 4 // 按下后移动超过这个距离就算拖动

	// 脉冲节奏：每一跳的间隔、沿一条边跑完的时长、节点亮起后的淡出时长。
	pulseHop    = 260 * time.Millisecond
	pulseTravel = 320 * time.Millisecond
	pulseFade   = 460 * time.Millisecond
)

// graphPulse 从悬停节点出发、按 BFS 层级向外扩散的脉冲。
//
// 为什么用 BFS：需求是「脉冲触达邻居时邻居亮一下，再继续跳二跳」。所以每个节点
// 有一个到中心的最短跳数；第 d 跳的边在 [ (d-1)*hop, (d-1)*hop+travel ] 内跑亮点，
// 节点在边跑完后亮起并淡出。
type graphPulse struct {
	active   bool
	start    time.Time
	key      graphKey
	depths   map[graphKey]int
	maxDepth int
}

// releaseGraphBuffers 离开拓扑页时释放画布缓存：命中表、采样缓冲、BFS 层级，
// 并让 Go 把归还的堆页尽快交还操作系统。
//
// 页面级的数据在这里丢；真正的「把空闲页还给系统」复用 memory.go 的
// releaseMemory（统一记录 heap/sys 前后值），延迟 1.5s 执行，避免阻塞离场动画。
func (a *App) releaseGraphBuffers() {
	g := &a.atlasUI.graph
	hits, pts, curves := len(g.hits), len(g.ptsBuf), len(g.curveBuf)
	nodes, edges, pulseNodes := len(g.nodesBuf), len(g.edgesBuf), len(g.pulse.depths)
	g.hits = nil
	g.ptsBuf = nil
	g.curveBuf = nil
	g.adjacent = nil
	g.nodesBuf = nil
	g.edgesBuf = nil
	g.pulse = graphPulse{}
	logging.Info("graph", "释放拓扑画布缓存",
		"hits", hits, "pts", pts, "curve", curves,
		"nodes", nodes, "edges", edges, "pulseDepths", pulseNodes)
	time.AfterFunc(1500*time.Millisecond, func() { a.releaseMemory("graph-release") })
}

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

// ---- 悬停脉冲 ----

// hoverNode 跟随悬停状态开始 / 结束脉冲。
//
// 悬停到节点：以它为中心算一次 BFS 层级并发脉冲；换一个节点：从新节点重新开始；
// 离开节点 / 离开画布：立刻结束（用户要的「移开就结束」）。
func (g *graphView) hoverNode(nodes []graphNode, edges [][2]int, idx int, now time.Time) {
	if idx < 0 || idx >= len(nodes) {
		if g.pulse.active {
			logging.Debug("graph", "脉冲结束", "center", g.pulse.key.label, "depth", g.pulse.maxDepth)
		}
		g.pulse.active = false
		return
	}
	key := graphKey{kind: nodes[idx].kind, id: nodes[idx].id, label: nodes[idx].label}
	if g.pulse.active && g.pulse.key == key {
		return // 同一个节点：让已经在跑的脉冲继续，不要每帧重置
	}
	depths, maxDepth := graphBFS(nodes, edges, idx)
	g.pulse = graphPulse{active: true, start: now, key: key, depths: depths, maxDepth: maxDepth}
	logging.Debug("graph", "脉冲开始", "center", key.label, "nodes", len(depths), "depth", maxDepth)
}

// pulseDone 脉冲是否已经放完（所有跳数 + 淡出）。
func (g *graphView) pulseDone(now time.Time) bool {
	if !g.pulse.active {
		return true
	}
	span := time.Duration(g.pulse.maxDepth)*pulseHop + pulseTravel + pulseFade
	return now.Sub(g.pulse.start) > span
}

// graphBFS 从 from 出发做广度优先，返回每个节点的最短跳数与最大跳数。
//
// 边按无向处理（关系图里「归属」反过来也是关系）；未连通的节点不进 depths。
func graphBFS(nodes []graphNode, edges [][2]int, from int) (map[graphKey]int, int) {
	adj := make([][]int, len(nodes))
	for _, e := range edges {
		a, b := e[0], e[1]
		if a < 0 || b < 0 || a >= len(nodes) || b >= len(nodes) {
			continue
		}
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}
	depths := map[graphKey]int{{kind: nodes[from].kind, id: nodes[from].id, label: nodes[from].label}: 0}
	seen := make([]bool, len(nodes))
	seen[from] = true
	queue := []int{from}
	maxDepth := 0
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if seen[next] {
				continue
			}
			seen[next] = true
			d := depths[graphKey{kind: nodes[cur].kind, id: nodes[cur].id, label: nodes[cur].label}] + 1
			depths[graphKey{kind: nodes[next].kind, id: nodes[next].id, label: nodes[next].label}] = d
			if d > maxDepth {
				maxDepth = d
			}
			queue = append(queue, next)
		}
	}
	return depths, maxDepth
}

// ---- 输入 ----

// graphCanvasInput 处理拓扑画布的输入：滚轮缩放、拖动平移、单击跳转。
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
		logging.Debug("graph", "滚轮缩放", "zoom", fmt.Sprintf("%.2f", g.zoom), "dy", ev.DY)
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
	logging.Info("graph", "点击拓扑节点", "kind", h.kind.String(), "label", h.label, "id", h.id)
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

// String 让日志里能直接看出节点类型。
func (k graphKind) String() string {
	switch k {
	case gkProject:
		return "project"
	case gkRepo:
		return "repo"
	case gkTodo:
		return "todo"
	case gkUnassigned:
		return "unassigned"
	default:
		return "none"
	}
}

// ---- 几何 ----

// graphCurve 画一条「从 a 到 b」的二次贝塞尔弧线。
//
// 用**折线采样**而不是 Path + StrokePath：Path 笔画在 mygo 里会走「按路径哈希缓存
// 栅格化蒙版」的通道，而拓扑图每帧的曲线坐标都随缩放/平移变化，等于每帧给蒙版
// 缓存塞新条目——内存涨上去、页面销毁也不回落。StrokePolyline 在 GPU 后端是
// 逐段圆角矩形绘制，不碰蒙版缓存；折线用 12 段近似，肉眼与真曲线一致。
func graphCurve(p *ui.Painter, buf [][2]float32, x1, y1, r1, x2, y2, r2, bend, width float32, col ui.Color) [][2]float32 {
	pts := graphCurvePolyline(buf[:0], x1, y1, r1, x2, y2, r2, bend)
	if len(pts) < 2 {
		return pts
	}
	p.StrokePolyline(pts, width, col)
	return pts
}

// graphCurvePolyline 把一条边的二次贝塞尔采样成折线，写进 dst（复用切片）。
// 起点/终点各退到圆点边缘，线不会压在圆点上。
func graphCurvePolyline(dst [][2]float32, x1, y1, r1, x2, y2, r2, bend float32) [][2]float32 {
	sx, sy, cx, cy, ex, ey := graphCurvePoints(x1, y1, r1, x2, y2, r2, bend)
	if sx == ex && sy == ey {
		return dst
	}
	const segments = 12
	for i := 0; i <= segments; i++ {
		t := float32(i) / segments
		x, y := quadPoint(sx, sy, cx, cy, ex, ey, t)
		dst = append(dst, [2]float32{x, y})
	}
	return dst
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
