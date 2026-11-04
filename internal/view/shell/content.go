package shell

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/kit"
)

// ContentOptions 内容框的渲染参数。业务侧通过回调把「标题 / 工具条 / 页面内容」
// 交给骨架，骨架只负责框与间距，不认识任何业务类型。
type ContentOptions struct {
	Title    string // 页头标题（来自菜单注册表）
	Subtitle string // 标题右侧的附加文字（如正在执行的任务名），可为空
	Leading  func() // 标题左侧的附加控件（如「返回」图标）；nil 表示没有
	Tools    func() // 页头右侧工具条；nil 表示没有
	Body     func() // 页面内容

	Scroll *ui.ScrollState // 该页的滚动位置（切换页面时保持），可为 nil

	// Running 为 true 时，分割线槽位改画进度条（轻量任务）。
	Running  bool
	Progress float64

	// Background 内容区浮层的填充色（有背景图时由调用方给半透明色）。
	Background ui.Color
}

// Content 绘制右侧内容区，自上而下分三段：
//
//  1. 固定高度的页头：页面标题 + 右侧工具条（工具条放在横向滚动容器里，
//     按钮多到放不下时可横向滚动查看）。
//  2. 分割线槽位：一条 1px 浅线；执行轻量级任务时同槽位改画进度条。
//  3. 可滚动的页面内容，**统一施加**页面留白与区块间距（PagePadX/PagePadY/
//     PageGap）——页面函数只写内容，不再各自设置外边距。
func Content(c *ui.Context, t *ui.Theme, o ContentOptions) {
	col := ui.Column(c).Grow(1).HeightPercent(100).Padding(t.Space(1), 0, 0, 0).
		Background(o.Background)
	col.Children(func() {
		contentHeader(c, t, o)
		contentDivider(c, t, o)
		scroll := ui.Scroll(c).Grow(1).
			// 统一留白：上下 PagePadY、左右 PagePadX（与页头共用左右值，左边缘对齐）。
			Padding(kit.PagePadY, kit.PagePadX)
		if o.Scroll != nil {
			// TrackScroll 让每页记住自己的滚动位置：切走再切回停在原处（缓存命中），
			// 页面被销毁后自动归零（等价于重新加载）。
			scroll.TrackScroll(o.Scroll)
		}
		scroll.Children(func() {
			// 用一个显式 Column 承载页面内容，保证「区块之间」的间距由框架统一
			// 给出（PageGap）：页面新增一个区块时不需要关心上下留白。
			ui.Column(c).Gap(kit.PageGap).Children(o.Body)
		})
	})
}

// contentHeader 页头：左标题（固定，不参与滚动），右侧工具条。
//
// Leading 放在标题**左侧**（不是右侧工具条里）：返回键是「离开这一页」的动作，
// 紧贴标题更符合「标题栏就是这一页」的心智模型，也不会和页内操作混在一起。
func contentHeader(c *ui.Context, t *ui.Theme, o ContentOptions) {
	ui.Row(c).Height(HeaderHeight).Shrink(0).AlignItems(ui.Center).
		Padding(0, kit.PagePadX).Gap(t.Space(2)).Children(func() {
		if o.Leading != nil {
			o.Leading()
		}
		ui.Text(c, o.Title).FontSize(16).Bold().Shrink(0).SingleLine()

		// 正在执行轻量任务时，标题右侧显示任务名（与进度条呼应）。
		if o.Subtitle != "" {
			ui.Text(c, o.Subtitle).FontSize(t.FontSize - 2).
				TextColor(t.TextMuted).Shrink(0).SingleLine()
		}

		ui.Row(c).Grow(1) // 弹性空白，把工具条推到右端
		if o.Tools != nil {
			o.Tools()
		}
	})
}

// contentDivider 分割线槽位：常态 1px 浅线，任务执行时同槽位画进度条。
func contentDivider(c *ui.Context, t *ui.Theme, o ContentOptions) {
	ui.Column(c).Height(DividerSlot).Shrink(0).Children(func() {
		if o.Running {
			// 进度条与浅线同槽位、同起点：任务跑起来时「线变成进度条」，
			// 结束后再变回线，视觉上不产生位移。
			ui.Progress(c, o.Progress).Height(DividerSlot).Radius(0)
			return
		}
		ui.Row(c).Height(1).Shrink(0).Background(t.Border)
	})
}
