package view

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/ui/kit"
	"bestuav-atlas/internal/view/shell"
)

// 本文件是页头右侧的「自定义图标按钮」工具条。
//
// 它虽然属于骨架的一部分（页头由 shell.Content 布局），但按钮数据来自菜单
// 注册表（registry.go 的 navItem.tools），所以渲染逻辑留在 view，由 shell 通过
// ContentOptions.Tools 回调调用。

// toolAction 页头图标按钮。
//
//	icon：图标（internal/ui/icons 里的 *ui.SVG，16px 线性，跟随主题文本色）
//	label：动作名（默认行为下作为轻量任务名显示在页头标题右侧）
//	tip：悬浮提示
//	run：点击行为；为 nil 时走「演示一条轻量任务」，新增页面可直接只填前三个字段
type toolAction struct {
	icon  *ui.SVG
	label string
	tip   string
	run   func(a *App)
}

// pageLeading 页头标题左侧的控件。
//
// 注册表里声明了 back 的页面（通常是 hidden 的详情页）自动获得返回键：
// 「从哪来、回哪去」是页面在导航里的位置，写进注册表后由骨架统一画，
// 页面函数不必自己维护一份「当前页 == 详情页」的判断。
func (a *App) pageLeading(c *ui.Context, t *ui.Theme) func() {
	it, ok := pageByID(a.page)
	if !ok || it.back == "" {
		return nil
	}
	return func() { a.headerBack(c, t, it.back) }
}

// headerBack 页头的返回键：图标 + 悬浮提示 + 可访问标签。
//
// 图标集里只有「右箭头」没有「左箭头」：转 180° 就是标准的返回箭头。
func (a *App) headerBack(c *ui.Context, t *ui.Theme, target string) {
	b := ui.Row(c).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).
		Width(28).Height(28).Radius(t.Radius).Label("返回").Tooltip("返回")
	if b.Hovered() {
		b.Background(t.SurfaceHover)
	}
	b.Children(func() {
		ui.Icon(c, icons.ChevronRight).FontSize(16).Rotate(180).TextColor(t.TextMuted)
	})
	if b.Clicked() {
		a.page = target
	}
}

// headerTools 页头右侧的图标按钮区。
//
// 布局要点：外层横向滚动容器用 Shrink(1) 参与伸缩——按钮少时按内容宽度靠右
// 排列；空间不足时被压缩到可用宽度，超出部分进横向滚动，而不是把标题挤出屏幕
// 或溢出窗口。横向滚动条同样隐藏（页头是固定高度的一条，出现滚动条会破坏对齐）。
func (a *App) headerTools(c *ui.Context, t *ui.Theme) {
	tools := pageToolsFor(a.page)
	// 设置页的更新按钮是动态渲染的（与注册表 tools 无关）：即便本页没有静态工具，
	// 也要保留标题栏的更新控件（无更新=图标按钮，有更新=高亮「更新」胶囊）。
	if len(tools) == 0 && a.page != "settings" {
		return
	}
	ui.ScrollHorizontal(c).Shrink(1).
		// 大下内缩 → 横向滚动条被推到页头上边界之外（不可见）。
		ScrollbarInsets(0, 0, shell.DividerSlot+400, 0).
		Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(t.Space(1)).Children(func() {
				for _, tool := range tools {
					a.toolButton(c, t, tool)
				}
				// 设置页：更新控件常驻标题栏右侧（参考 Dropbox——无更新时是普通图标
				// 按钮，有更新时变为高亮「更新」胶囊按钮），与页面正文解耦。
				// 安全模式下整块不画：README 承诺「标题栏不会出现更新图标/胶囊」，
				// 画一个点了没反应的图标只会让人以为坏了。
				if a.page == "settings" && !a.safeMode {
					a.updateHeaderButton(c, t)
				}
			})
		})
}

// updateHeaderButton 设置页标题栏右侧的更新控件，随检查状态变化：
//   - 无更新 / 检查中 / 已禁用 / 出错 / 已忽略：仅一个下载图标按钮，点击=重新检查；
//   - 有更新（未忽略）：变为高亮「更新」胶囊按钮（带下载图标+文字），点击=弹确认窗；
//   - 安装中：胶囊显示「更新中」并禁用，进度由页面正文区进度条呈现。
func (a *App) updateHeaderButton(c *ui.Context, t *ui.Theme) {
	switch {
	case a.updateInstalling:
		a.updatePill(c, t, "更新中", false, "")

	case a.availableUpdate != nil && !a.updateDismissed:
		// 高亮「更新」胶囊：点击弹窗确认，而非直接开装。
		a.updatePill(c, t, "更新", true, "发现新版本 "+a.availableUpdate.Version+"，点击查看")

	default:
		// 普通图标按钮：点击重新检查；检查中显示呼吸点，其余显示下载图标。
		btn := ui.Row(c).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).
			Width(28).Height(28).Radius(t.Radius)
		if btn.Hovered() {
			btn.Background(t.SurfaceHover)
		}
		btn.Tooltip(a.updateTip())
		btn.Children(func() {
			if a.updateChecking {
				kit.BreathingDot(c, t.Accent, 6, kit.BreathePeriod)
			} else {
				ui.Icon(c, icons.Download).FontSize(16).TextColor(t.TextMuted)
			}
		})
		if btn.Clicked() {
			a.checkForUpdates(true)
		}
	}
}

// updatePill 标题栏的胶囊按钮（accent 高亮或安装中态），点击由 openDialog 决定。
func (a *App) updatePill(c *ui.Context, t *ui.Theme, label string, openDialog bool, tip string) {
	btn := ui.Row(c).Shrink(0).AlignItems(ui.Center).Gap(6).
		Padding(0, 12).Height(28).Radius(t.Radius).
		Background(t.Accent)
	btn.Tooltip(tip)
	btn.Children(func() {
		ui.Icon(c, icons.Download).FontSize(15).TextColor(t.Background)
		ui.Text(c, label).Bold().TextColor(t.Background)
	})
	if !a.updateInstalling && btn.Clicked() {
		if openDialog {
			a.showUpdateDialog = true
			a.requestFrame()
		} else {
			a.checkForUpdates(true)
		}
	}
}

// updateTip 普通图标按钮的悬浮提示，按当前状态给出说明。
func (a *App) updateTip() string {
	switch {
	case a.updateChecking:
		return "正在检查更新…"
	case a.updateDisabled:
		return "当前为开发构建，正式发布包才支持自动更新（点击重新检查）"
	case a.updateErr != nil:
		return "检查失败，点击重试"
	case a.availableUpdate != nil:
		return "已是最新版本（" + a.availableUpdate.Version + "）"
	case a.updateDismissed:
		return "已忽略本次更新（点击重新检查）"
	default:
		return "检查更新"
	}
}

// toolButton 单个页头图标按钮（28×28 点击区，悬停有圆角底色，悬浮提示说明用途）。
func (a *App) toolButton(c *ui.Context, t *ui.Theme, tool toolAction) {
	btn := ui.Row(c).Shrink(0).AlignItems(ui.Center).Justify(ui.Center).
		Width(28).Height(28).Radius(t.Radius)
	if btn.Hovered() {
		btn.Background(t.SurfaceHover)
	}
	btn.Tooltip(tool.tip)
	btn.Children(func() {
		ui.Icon(c, tool.icon).FontSize(16).TextColor(t.TextMuted)
	})
	if btn.Clicked() {
		// 有具体动作就执行（如「演示彩带」「复制结果」）；没有动作的按钮什么也不做，
		// 不再退化成「演示一条任务」——任务队列现在只记录真实动作。
		if tool.run != nil {
			tool.run(a)
		}
	}
}
