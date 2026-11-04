package shell

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/icons"
)

// 侧栏几何：让所有行（含高亮背景）只留 4px 边距、接近满宽，对应 javafx 版
// .sidebar{padding:4} + 行背景铺满整行。

// sideMargin 侧栏根容器的水平内边距：唯一一处水平内缩。
func sideMargin(t *ui.Theme) float32 { return t.Space(1) }

// contentInset 菜单项「内容」（图标 + 文字）相对近满宽高亮的左缩进：
// 背景铺满整行，内容再左缩 12px（对应 javafx 的 -fx-padding: 10px 0 10px 13px）。
func contentInset(t *ui.Theme) float32 { return t.Space(3) } // = 12px

// brandInset 品牌头图标的左缩进：在 contentInset 基础上回退
// (BrandIconSize-NavIconSize)/2，使品牌图标与菜单图标的中心点对齐。
func brandInset(t *ui.Theme) float32 {
	return contentInset(t) - (BrandIconSize-NavIconSize)/2
}

// brandPillPad 品牌按钮的左右内边距（悬停药丸让内容有呼吸空间）。
//
// brandHeader 的行内边距会扣掉同样的值，所以这个内边距不会把品牌图标挤偏。
func brandPillPad(t *ui.Theme) float32 { return t.Space(1.5) } // = 6px

// Item 一个导航项（仅描述，不含行为）。
type Item struct {
	ID    string  // 页面 id，点击后写回 NavState.Page
	Label string  // 展开态文字 + 收缩态悬浮提示
	Icon  *ui.SVG // 16px 线性图标
}

// Group 一个可折叠分组；Title 为空的分组表示「置顶项，不画分组标题」。
type Group struct {
	Title string
	Items []Item
}

// NavState 侧栏的可变状态，由调用方（App）持有并以指针方式注入：
// 侧栏直接读写这些字段，因此不需要认识 App 类型。
type NavState struct {
	// Page 当前选中的页面 id；点击菜单项时由侧栏写回。
	Page *string
	// Collapsed 侧栏是否收缩为图标栏；点击侧栏开关（品牌行右端 / 收缩态底部）时由侧栏翻转。
	Collapsed *bool
	// Folded 分组折叠状态（key 为分组标题；缺省视为展开）。
	Folded map[string]bool
}

// SidebarOptions 侧栏的静态配置。
type SidebarOptions struct {
	Title     string  // 品牌文字（收缩态不显示）
	BrandIcon *ui.SVG // 品牌标志

	// HomeID 品牌区点击后前往的页面 id；空 = 品牌区不可点（纯展示）。
	// 一般传首页的 id：logo 回首页是通用约定，也让「品牌区可点」有了明确含义。
	HomeID string

	Groups []Group // 中部导航分组
	Footer Item    // 底部固定项（设置），不随中部菜单滚动

	// ToggleHint 侧栏开关的附加提示（通常是最新键位，如 "Ctrl+B"）；空则不提示。
	ToggleHint string

	// Background 侧栏浮层填充色（有背景图时由调用方给半透明色）。
	Background ui.Color

	// OnCollapseChange 折叠状态变化回调：调用方据此持久化偏好。
	OnCollapseChange func(collapsed bool)
}

// Sidebar 绘制左侧导航栏。
//
// 结构：顶部品牌头固定（展开态右端带收/展开关）→ 分割线 → 中部菜单（可滚动，
// 滚动条隐藏）→ 分割线 → 底部固定区（收缩态才有侧栏开关 + 调用方给的固定项）。
// 中部菜单用 Scroll 包裹，窗口再矮也能滚到每一项。
func Sidebar(c *ui.Context, t *ui.Theme, st *NavState, o SidebarOptions) {
	width := float32(ExpandedWidth)
	if st.collapsed() {
		width = CollapsedWidth
	}
	ui.Column(c).Width(width).HeightPercent(100).Background(o.Background).
		Padding(t.Space(1), sideMargin(t), t.Space(1), sideMargin(t)).Children(func() {
		brandHeader(c, t, st, o)
		// 品牌头下分割线：上边距为 0，使浅线正好落在 y = Space(1)+HeaderHeight
		// 处，与内容区页头下的分割线保持同一水平线（见 content.go）。
		ui.Row(c).Height(1).Shrink(0).Margin(0, 0, t.Space(1), 0).Background(t.Border)

		scroll := ui.Scroll(c).Grow(1).
			// 纵向 6px 上下留白、项间 8px 行距（对齐 javafx spacing(8)+行 padding 10）。
			Padding(t.Space(1.5), 0, t.Space(1.5), 0).Gap(t.Space(2)).
			// 侧栏滚动条隐藏（内容区不受影响）。
			ScrollbarInsets(0, scrollbarHideInset, 0, 0)
		scroll.Children(func() {
			navMenu(c, t, st, o.Groups)
		})

		ui.Row(c).Height(1).Shrink(0).Margin(t.Space(1), 0, t.Space(1), 0).Background(t.Border)
		navFooter(c, t, st, o)
	})
}

// collapsed 读折叠态（未注入时按展开处理）。
func (st *NavState) collapsed() bool { return st.Collapsed != nil && *st.Collapsed }

// setPage 写回选中的页面 id（未注入时忽略）。
func (st *NavState) setPage(id string) {
	if st.Page != nil {
		*st.Page = id
	}
}

// toggle 翻转折叠态并通知调用方持久化。
func (st *NavState) toggle(o SidebarOptions) {
	if st.Collapsed == nil {
		return
	}
	*st.Collapsed = !*st.Collapsed
	if o.OnCollapseChange != nil {
		o.OnCollapseChange(*st.Collapsed)
	}
}

// ---- 品牌头（只负责品牌与回首页） ----

// brandHeader 品牌头：左侧品牌按钮（点击回首页），右侧「收侧栏」图标按钮。
//
// 三件事各归各位：
//   - 品牌按钮 → 回首页（HomeID 为空时是纯展示，不可点、无悬停）；
//   - 品牌行右端的图标按钮 → 收起侧栏（收缩态在底部，见 sidebarToggle）；
//   - 整行不再兼任开关：同一块命中区不能既回首页又收侧栏。
func brandHeader(c *ui.Context, t *ui.Theme, st *NavState, o SidebarOptions) {
	collapsed := st.collapsed()
	row := ui.Row(c).Height(HeaderHeight).Shrink(0).AlignItems(ui.Center)
	if collapsed {
		row.Justify(ui.Center)
	} else {
		// 品牌按钮自带左右内边距（brandPillPad），行内边距回退同样的距离，
		// 品牌图标中心才会与菜单图标中心落在同一条竖线上（见 brandInset）。
		row.Padding(0, brandInset(t)-brandPillPad(t), 0, brandInset(t)-brandPillPad(t))
	}
	row.Children(func() {
		brandButton(c, t, st, o)
		if !collapsed {
			ui.Row(c).Grow(1) // 弹簧：把侧栏开关顶到最右端
			sidebarToggle(c, t, st, o)
		}
	})
}

// brandButton 品牌按钮：图标 +（展开态）产品名。
//
// 悬停高亮只包住「图标 + 文字」，不是整行——否则它与右侧的开关按钮会共用一整块
// 大高亮，看起来像同一个控件，点哪儿会发生什么又说不清了。
func brandButton(c *ui.Context, t *ui.Theme, st *NavState, o SidebarOptions) {
	collapsed := st.collapsed()
	clickable := o.HomeID != ""
	btn := ui.Row(c).Shrink(0).AlignItems(ui.Center).Height(brandButtonHeight).
		Gap(t.Space(2)).Padding(0, brandPillPad(t)).Radius(t.Radius)
	if clickable {
		if btn.Hovered() {
			btn.Background(t.SurfaceHover)
		}
		btn.Focusable().Cursor(ui.CursorPointer).Role(ui.RoleButton).
			Label(o.Title).Tooltip("返回首页")
	}
	btn.Children(func() {
		ui.Icon(c, o.BrandIcon).FontSize(BrandIconSize).TextColor(t.Accent)
		if !collapsed {
			// 品牌名过长时省略：它是固定宽度的按钮，不能把右侧开关挤出侧栏。
			ui.Text(c, o.Title).FontSize(15).Bold().SingleLine().
				MaxWidth(brandTitleMax).Ellipsis("…")
		}
	})
	if clickable && btn.Clicked() {
		st.setPage(o.HomeID)
	}
}

// ---- 中部导航 ----

// navMenu 中部导航：完全由传入的分组数据驱动，shell 不认识具体业务菜单。
func navMenu(c *ui.Context, t *ui.Theme, st *NavState, groups []Group) {
	collapsed := st.collapsed()
	for _, g := range groups {
		// 无标题分组 = 首页这类置顶项，不画分组标题/分割线。
		if g.Title == "" {
			for _, it := range g.Items {
				navRow(c, t, st, it)
			}
			continue
		}

		if collapsed {
			// 收缩态：用组间细分割线提示分组，不占文字宽度。
			ui.Row(c).Height(1).Shrink(0).
				Margin(t.Space(2), t.Space(1.5), t.Space(2), t.Space(1.5)).
				Background(t.Border)
		} else {
			sectionHeader(c, t, st, g.Title)
		}

		if collapsed || !st.Folded[g.Title] {
			for _, it := range g.Items {
				navRow(c, t, st, it)
			}
		}
	}
}

// sectionHeader 展开态的分组标题（纯文字）：点击折叠/展开，右侧箭头提示状态。
// 悬停高亮、圆角、铺满程度与菜单项完全一致；文字左缩与菜单图标对齐。
func sectionHeader(c *ui.Context, t *ui.Theme, st *NavState, title string) {
	head := ui.Row(c).Shrink(0).AlignItems(ui.Center).
		Padding(t.Space(2.5), 0, t.Space(2.5), contentInset(t)).Radius(t.Radius)
	if head.Hovered() {
		head.Background(t.SurfaceHover)
	}
	if head.Clicked() {
		if st.Folded == nil {
			st.Folded = map[string]bool{}
		}
		st.Folded[title] = !st.Folded[title]
	}
	folded := st.Folded[title]
	head.Children(func() {
		ui.Text(c, title).FontSize(t.FontSize - 1).FontWeight(600).
			TextColor(t.TextMuted).Grow(1).SingleLine()
		ui.Icon(c, icons.Chevron(!folded)).FontSize(ChevronSize).TextColor(t.TextMuted)
	})
}

// navRow 单个导航项：点击后写回页面 id。
func navRow(c *ui.Context, t *ui.Theme, st *NavState, it Item) {
	actionRow(c, t, st, it, func() { st.setPage(it.ID) })
}

// actionRow 侧栏里的一行（菜单项 / 底部固定项）。
//   - 展开态：背景铺满整行（仅由根容器的 4px sideMargin 收边），内容左缩 contentInset；
//     悬停/选中时背景只在该行范围内着色。
//   - 收缩态：整行作为点击/悬停命中区，内部图标药丸居中，悬浮提示菜单名。
//
// 两态的高亮与内缩规则都收在这里：菜单项与底部固定项不必各抄一遍（曾经抄漏过一次，
// 导致底部图标比菜单项左移一截）。
func actionRow(c *ui.Context, t *ui.Theme, st *NavState, it Item, onPick func()) {
	chosen := it.ID != "" && st.Page != nil && *st.Page == it.ID

	if st.collapsed() {
		outer := ui.Row(c).Shrink(0).AlignItems(ui.Center).Justify(ui.Center)
		outer.Children(func() {
			pill := ui.Row(c).AlignItems(ui.Center).
				Padding(t.Space(1.5), t.Space(2)).Radius(t.Radius)
			if chosen {
				pill.Background(t.SurfacePressed)
			} else if outer.Hovered() {
				pill.Background(t.SurfaceHover)
			}
			pill.Children(func() {
				ui.Icon(c, it.Icon).FontSize(NavIconSize).TextColor(t.Accent)
			})
		})
		outer.Tooltip(it.Label)
		if outer.Clicked() && onPick != nil {
			onPick()
		}
		return
	}

	row := ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(t.Space(2)).
		Padding(t.Space(2.5), 0, t.Space(2.5), contentInset(t)).Radius(t.Radius)
	if chosen {
		row.Background(t.SurfacePressed)
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Icon(c, it.Icon).FontSize(NavIconSize).TextColor(t.Accent)
		ui.Text(c, it.Label).Grow(1).SingleLine().FontSize(t.FontSize)
	})
	if row.Clicked() && onPick != nil {
		onPick()
	}
}

// ---- 底部固定区 ----

// navFooter 底部固定区：收缩态的侧栏开关 + 调用方给的固定项（设置）。
//
// 展开态没有这一行——开关在品牌行右端（见 sidebarToggle），底部只剩设置，
// 与改造前一致，不额外占一行、也不新增一条像菜单的条目。
func navFooter(c *ui.Context, t *ui.Theme, st *NavState, o SidebarOptions) {
	// 收缩成 64px 图标栏后，品牌行里放不下第二个图标（品牌图标必须与菜单图标同轴），
	// 于是开关落到这里：同样只有一个图标，与「设置」同一列。
	if st.collapsed() {
		ui.Row(c).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			sidebarToggle(c, t, st, o)
		})
	}
	if o.Footer.ID == "" {
		return
	}
	navRow(c, t, st, o.Footer)
}

// sidebarToggle 侧栏的收起/展开开关：只有一个图标，**不占一行菜单**。
//
// 展开态挂在品牌行右端（与页头的图标按钮同规格：28 高、悬停才出现底色）；
// 收缩态挂在底部，与「设置」图标药丸同一列。为什么不再让品牌区自己兼任：
//   - 品牌区的语义是「我是什么产品」，点击它回首页（或干脆不可点）符合 logo 的
//     通用约定；把「收侧栏」也压在这里，同一块区域就有了两种互斥含义；
//   - 收缩后没有文字，品牌图标如果同时兼任「展开」，用户点 logo 时无法预期会回
//     首页还是展开。
//
// 键盘用户不必找这个图标：Ctrl/Cmd+B（可在设置页改绑）走同一条状态与持久化路径。
func sidebarToggle(c *ui.Context, t *ui.Theme, st *NavState, o SidebarOptions) ui.Element {
	label, icon := "收起侧栏", icons.ChevronsLeft
	if st.collapsed() {
		label, icon = "展开侧栏", icons.ChevronsRight
	}
	tip := label
	if o.ToggleHint != "" {
		tip += "（" + o.ToggleHint + "）"
	}
	btn := ui.Row(c).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).
		Width(toggleWidth).Height(toggleHeight).Radius(t.Radius)
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	// 图形是「窗口级动作」，用次要文字色而不是菜单强调色，避免被读成又一个菜单项。
	// Label 让读屏与测试都能找到这个纯图标按钮；Focusable 让它能被 Tab 到并用
	// Enter/Space 触发。
	btn.Tooltip(tip).Focusable().Cursor(ui.CursorPointer).Role(ui.RoleButton).Label(label)
	btn.Children(func() {
		ui.Icon(c, icon).FontSize(NavIconSize).TextColor(t.TextMuted)
	})
	if btn.Clicked() {
		st.toggle(o)
	}
	return btn
}
