package view

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/kit"
	"bestuav-atlas/internal/view/shell"
)

// 布局示例页：验证三种内容区样式（页头扩展 / tab 切换 / 左右两栏）。
//
// 这两页是「骨架能力的活文档」：菜单里点开就能看到 shell.Content 的三种用法，
// 新页面照着抄即可。数据是静态的，不碰数据库。

// layoutTabsPage 页头 tab 示例：tab 由 shell 渲染（见 header_layout.go），
// 正文按 a.demoTab 切换；tab 切换不改变页头高度，也不显示副标题。
func (a *App) layoutTabsPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(kit.PageGap).Children(func() {
		switch a.demoTab {
		case 1:
			kit.Card(c, t, func() {
				kit.CardHeader(c, t, "明细", "同一个页面、不同 tab 展示不同的正文区块。", nil)
				kit.KV(c, t, "当前 tab", "明细（索引 1）")
				kit.KV(c, t, "数据来源", "静态示例数据")
				kit.KV(c, t, "布局方式", "shell.Content + Tabs/OnTab")
				ui.Text(c, "正文可以自由换成表格、列表或任意组件；页头保持「标题 + tab + 工具条」三段不变。").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			})
		case 2:
			kit.Card(c, t, func() {
				kit.CardHeader(c, t, "设置", "第三个 tab：演示正文可以完全不同。", nil)
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "示例开关").Grow(1)
					flag := true
					ui.Switch(c, &flag)
				})
				ui.Text(c, "这里的开关只是示例，不写任何设置；真正需要持久化的值走 a.Prefs() 或 settings。").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			})
		default:
			ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Stretch).Children(func() {
				ui.Column(c).Gap(kit.PageGap).Grow(1).Children(func() {
					kit.Card(c, t, func() {
						kit.CardHeader(c, t, "概览", "页头 tab 的第一栏。", nil)
						ui.Text(c, "标题栏固定 52px：标题在左，tab 紧跟其后，右侧留给工具按钮。").
							TextColor(t.TextMuted)
						ui.Text(c, "tab 数量变化、正文高度变化，都不会让标题栏跳动。").
							FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					})
					kit.Card(c, t, func() {
						kit.CardHeader(c, t, "适用场景", "", nil)
						ui.Text(c, "同一实体有多种视图时（概览 / 明细 / 设置、按状态分栏），"+
							"用 tab 比在正文里塞 Segmented 更省垂直空间，切换也不会把内容顶下去。").
							FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					})
				})
				ui.Column(c).Gap(kit.PageGap).Grow(1).Children(func() {
					kit.Card(c, t, func() {
						kit.CardHeader(c, t, "布局要点", "", nil)
						ui.Text(c, "• 有 tab 时不显示副标题（位置让给 tab）").FontSize(t.FontSize - 2)
						ui.Text(c, "• 页头 Extras（下拉框/文本）仍然可用").FontSize(t.FontSize - 2)
						ui.Text(c, "• tab 状态由页面自己保存，切换页面再回来保持原 tab").FontSize(t.FontSize - 2)
					})
				})
			})
		}
	})
}

// layoutSplitPage 三段式布局示例：左栏卡片列表 + 右栏详情，未选中时右栏空白提示。
//
// 用 shell.Content{Fill:true} + shell.Split：内容区不套统一滚动，左右各自滚动，
// 左栏滚动不影响右栏。
func (a *App) layoutSplitPage(c *ui.Context, t *ui.Theme) {
	shell.Split(c, t, &a.split, 240, 520, func() {
		shell.SplitListScroll(c, nil, func() {
			ui.Column(c).Gap(8).Children(func() {
				ui.Text(c, "示例列表").Bold().FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				for i, p := range demoProjects {
					id := i + 1
					sel := a.demoSel == id
					row := ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(10, 10).Radius(8)
					if sel {
						row.Background(kit.Alpha(t.Accent, 0.14))
					} else if row.Hovered() {
						row.Background(t.SurfaceHover)
					}
					row.Children(func() {
						ui.Column(c).Gap(2).Grow(1).Children(func() {
							ui.Text(c, p.name).Bold().SingleLine()
							ui.Text(c, p.desc).FontSize(t.FontSize - 2).
								TextColor(t.TextMuted).SingleLine()
						})
					})
					if row.Clicked() {
						a.demoSel = id
						a.requestFrame()
					}
				}
			})
		})
	}, func() {
		if a.demoSel <= 0 || a.demoSel > len(demoProjects) {
			shell.SplitEmpty(c, t, "从左侧选择一个项目，右侧展示详情")
			return
		}
		p := demoProjects[a.demoSel-1]
		shell.SplitDetailScroll(c, nil, func() {
			ui.Column(c).Gap(kit.PageGap).Children(func() {
				kit.Card(c, t, func() {
					kit.CardHeader(c, t, p.name, p.desc, nil)
					kit.KV(c, t, "状态", p.meta)
					kit.KV(c, t, "数据来源", "左侧示例列表（静态）")
					ui.Row(c).Gap(8).Children(func() {
						if ui.Button(c, "打开仓库页").Clicked() {
							a.page = "repos"
						}
					})
				})
				ui.Text(c, "右栏独立滚动：左栏滚到第 20 项，右栏仍停在详情顶部。").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			})
		})
	})
}
