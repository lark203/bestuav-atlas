package shell

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/kit"
)

// SplitState 是三段式布局的拖拽状态，由页面持有（App 字段或页面私有状态），
// 跨帧保留「左栏多宽」与「是否正在拖」。
type SplitState struct {
	// Width 左栏宽度（DIP）。0 表示用默认值（320）。
	Width float32
	// dragging 仅在拖拽期间为 true；grabX 是按下时指针在分隔线内的局部横坐标。
	dragging bool
	grabX    float32
}

const (
	// splitDefaultWidth 左栏默认宽度。
	splitDefaultWidth = 320
	// splitDividerWidth 拖拽手柄宽度（视觉仍是 1px 分隔线，左右留出抓取区）。
	splitDividerWidth = 7
	// splitRightMin 右栏至少要留的宽度：窗口再小也不能把详情挤没。
	splitRightMin = 300
)

// clampf 把 v 夹在 [lo,hi]。
func clampf(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Split 绘制「左列表 + 右内容」两栏（微信式主从布局），整体占满内容区剩余高度，
// 中间的分隔线可以拖动调整左右宽度。
//
// 和 Content 的关系：调用方把 Fill 置为 true，Content 不再套统一滚动；本函数把
// 剩余高度切成左右两块，各自由页面套 ui.Scroll —— 左栏滚动不影响右栏。
//
// 宽度约束：minWidth（<=0 用 200）与 maxWidth（<=0 用容器 60%）；另外始终保证
// 右栏至少 splitRightMin，且左栏不超过容器宽度的一半以上，拖到头也不会失衡。
//
// 拖动时用「当前宽度 + 局部坐标增量」而不是「起始宽度 + 总位移」：分隔线本身会
// 随拖动移动，局部坐标的增量里已经扣掉了这点移动，逐帧累加反而精确（推导见下）：
// 设按下时局部 X = x0、起始宽度 W0；当前宽度 W、指针总位移 d，则局部 X = x0+d-(W-W0)，
// 所以 W += (x - x0) 后恰好等于 W0+d。
func Split(c *ui.Context, t *ui.Theme, state *SplitState, minWidth, maxWidth float32,
	left, right func()) {
	if state == nil {
		state = &SplitState{}
	}
	if state.Width <= 0 {
		state.Width = splitDefaultWidth
	}
	if minWidth <= 0 {
		minWidth = 200
	}
	w, _ := c.Size()
	if maxWidth <= 0 {
		maxWidth = w * 0.6
	}
	if lim := w - splitRightMin; maxWidth > lim {
		maxWidth = lim
	}
	if maxWidth < minWidth {
		maxWidth = minWidth
	}
	state.Width = clampf(state.Width, minWidth, maxWidth)

	ui.Row(c).Grow(1).Fill().Children(func() {
		ui.Column(c).Width(state.Width).Shrink(0).HeightPercent(100).Children(left)

		// 分隔线：宽度 splitDividerWidth 的抓取区，中间画 1px 线，指针变左右箭头。
		div := ui.Row(c).Width(splitDividerWidth).Shrink(0).HeightPercent(100).
			Cursor(ui.CursorResizeEW).AlignItems(ui.Center).Justify(ui.Center)
		div.Children(func() {
			ui.Row(c).Width(1).HeightPercent(100).Background(t.Border)
		})
		div.HandleInput(func(ev ui.InputEvent) bool {
			switch ev.Kind {
			case ui.InputPointerDown:
				state.dragging = true
				state.grabX = ev.X
				return true
			case ui.InputPointerMove:
				if !state.dragging {
					return false
				}
				state.Width = clampf(state.Width+(ev.X-state.grabX), minWidth, maxWidth)
				return true
			case ui.InputPointerUp:
				if !state.dragging {
					return false
				}
				state.dragging = false
				return true
			}
			return false
		})

		ui.Column(c).Grow(1).HeightPercent(100).Children(right)
	})
}

// SplitEmpty 右栏的「未选中」占位：三段式布局在没点左边时右边是空白页，
// 给一句操作提示比纯白更容易理解。
func SplitEmpty(c *ui.Context, t *ui.Theme, hint string) {
	ui.Column(c).Fill().AlignItems(ui.Center).Justify(ui.Center).Children(func() {
		ui.Text(c, hint).TextColor(t.TextMuted)
	})
}

// SplitListScroll 左栏滚动的统一留白（与页头左边缘对齐）。
func SplitListScroll(c *ui.Context, state *ui.ScrollState, body func()) {
	s := ui.Scroll(c).Grow(1).Padding(kit.PagePadY, kit.PagePadX)
	if state != nil {
		s.TrackScroll(state)
	}
	s.Children(body)
}

// SplitDetailScroll 右栏滚动的统一留白。
func SplitDetailScroll(c *ui.Context, state *ui.ScrollState, body func()) {
	s := ui.Scroll(c).Grow(1).Padding(kit.PagePadY, kit.PagePadX)
	if state != nil {
		s.TrackScroll(state)
	}
	s.Children(body)
}
