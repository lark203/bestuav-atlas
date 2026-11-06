package view

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/kit"
)

// 今日便签：一个无边框、置顶、不进任务栏的小窗，贴在屏幕右上角（可拖动），
// 按「早 / 中 / 晚 / 全天」列出今天要做的待办，勾选即完成。
//
// 与桌宠（pet.go）同一套做法：静态时低频心跳、拖动走框架原生 DragWindow、
// 开关由设置页控制。区别是便签要显示业务数据，所以数据变化时由 reloadAtlas
// 主动 Invalidate。
const (
	stickyW    = 300
	stickyH    = 440
	stickyEdge = 24 // 与屏幕（或主窗口）边缘的间距
)

// stickyBuckets 便签分段的固定顺序；与 todoBucket 的返回值一致。
var stickyBuckets = []string{"早", "中", "晚", "全天"}

// stickyTick 主窗口每帧调用：按开关创建/销毁窗口，并低频记录位置。
func (a *App) stickyTick() {
	a.stickySyncWindow()
	a.stickyRememberPos()
}

// stickySyncWindow 根据开关创建/销毁便签窗口。
func (a *App) stickySyncWindow() {
	if a.settings.StickyEnabled == a.stickyApplied {
		return
	}
	// 主窗口还没就绪（启动早期 / 离屏测试）：下一帧再试。
	if a.settings.StickyEnabled && a.win == nil {
		return
	}
	a.stickyApplied = a.settings.StickyEnabled
	if a.settings.StickyEnabled {
		a.openStickyWindow()
	} else {
		a.closeStickyWindow()
	}
}

// openStickyWindow 创建便签窗口（需要主窗口已就绪）。
func (a *App) openStickyWindow() {
	if a.stickyWin != nil || a.win == nil {
		return
	}
	opts := mygo.WindowOptions{
		Title:             "今日便签",
		Width:             stickyW,
		Height:            stickyH,
		Frameless:         true,
		AlwaysOnTop:       true,
		SkipTaskbar:       true,
		DisableResize:     true,
		DisableMaximize:   true,
		DisableMinimize:   true,
		DisableFullScreen: true,
		// 没有标题栏也没有关闭按钮：只能通过设置开关关闭，避免误关后
		// 设置里还显示「已开启」。
		DisableClose: true,
		Content:      ui.View(a.stickyView),
	}
	if x, y, ok := a.stickyPos(); ok {
		opts.X, opts.Y = x, y
	}
	win := mygo.NewWindow(opts)
	if win == nil {
		a.settings.StickyEnabled = false
		a.stickyApplied = false
		a.toast("便签窗口创建失败")
		return
	}
	a.stickyWin = win
	win.OnClosed(func() {
		a.stickyWin = nil
		a.stickyApplied = false
		a.requestFrame()
	})
}

// closeStickyWindow 关闭便签窗口。
func (a *App) closeStickyWindow() {
	if a.stickyWin == nil {
		return
	}
	a.stickyWin.Close()
	a.stickyWin = nil
}

// stickyPos 便签窗口位置：用户拖过就用记住的位置，否则贴主窗口右上角
// （主窗口最大化时就是屏幕右上角；不引入平台 API，避免 DPI 坐标换算误差）。
func (a *App) stickyPos() (int, int, bool) {
	if a.settings.StickyX >= 0 && a.settings.StickyY >= 0 {
		return a.settings.StickyX, a.settings.StickyY, true
	}
	return a.stickyDefaultPos()
}

// stickyDefaultPos 默认位置：主窗口右上角内侧。
func (a *App) stickyDefaultPos() (int, int, bool) {
	if a.win == nil {
		return 0, 0, false
	}
	b := a.win.Bounds()
	x := b.X + b.Width - stickyW - stickyEdge
	y := b.Y + stickyEdge
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y, true
}

// stickyRememberPos 低频记录窗口位置（拖动结束后落盘）。
func (a *App) stickyRememberPos() {
	if a.stickyWin == nil {
		return
	}
	now := time.Now()
	if now.Sub(a.stickyPollAt) < time.Second {
		return
	}
	a.stickyPollAt = now
	x, y := a.stickyWin.Position()
	if x != a.settings.StickyX || y != a.settings.StickyY {
		a.settings.StickyX, a.settings.StickyY = x, y
		a.persist()
	}
}

// stickyInvalidate 数据变化后重画便签（reloadAtlas 调用）。
func (a *App) stickyInvalidate() {
	if a.stickyWin != nil {
		a.stickyWin.Invalidate()
	}
}

// stickyToday 今天要做的待办，按早 / 中 / 晚 / 全天分组。
func (a *App) stickyToday(now time.Time) map[string][]store.Todo {
	out := make(map[string][]store.Todo, len(stickyBuckets))
	for _, td := range a.atlas().sortedTodos() {
		if !todoActiveToday(td, now) {
			continue
		}
		b := todoBucket(td)
		out[b] = append(out[b], td)
	}
	return out
}

// stickyView 便签窗口的内容。
func (a *App) stickyView(c *ui.Context) {
	now := time.Now()
	t := c.Theme()
	a.stickyRememberPos()

	// 静止时 30 秒一次心跳：刷新「剩 X 分钟」与跨天边界。
	c.After(30 * time.Second)

	groups := a.stickyToday(now)
	total := 0
	for _, items := range groups {
		total += len(items)
	}

	ui.Column(c).Fill().Background(t.Background).Gap(0).Children(func() {
		// 标题栏兼拖拽把手：只有这一条 DragWindow，内容区的勾选框照常可点。
		ui.Row(c).Shrink(0).Padding(12, 10).Gap(8).AlignItems(ui.Center).
			Background(t.Surface).DragWindow().Label("拖动便签").
			Children(func() {
				ui.Text(c, "今日便签").Bold().FontSize(t.FontSize).SingleLine()
				ui.Text(c, now.Format("01-02")+" "+weekdayCN(now)).
					FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
				ui.Text(c, "").Grow(1)
				ui.Text(c, itoa(total)+" 条").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			})

		ui.Column(c).Gap(10).Padding(12, 10).Grow(1).Children(func() {
			if total == 0 {
				ui.Column(c).Gap(6).Grow(1).Children(func() {
					ui.Text(c, "今天没有要做的待办").TextColor(t.TextMuted)
					ui.Text(c, "去待办页加一条，或把某条的开始日改到今天。").
						FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					ui.Row(c).Children(func() {
						if ui.Button(c, "打开星图").Clicked() {
							a.showMainWindow()
						}
					})
				})
				return
			}
			for _, bucket := range stickyBuckets {
				items := groups[bucket]
				if len(items) == 0 {
					continue
				}
				a.stickyBucket(c, t, bucket, items, now)
			}
		})

		ui.Row(c).Shrink(0).Padding(12, 8).Gap(6).AlignItems(ui.Center).
			Background(t.Surface).Children(func() {
			ui.Text(c, "勾选即完成 · 拖动标题栏移动 · 设置里可关闭").
				FontSize(t.FontSize - 3).TextColor(t.TextMuted).SingleLine()
		})
	})
}

// stickyBucket 一个时段（早 / 中 / 晚 / 全天）。
func (a *App) stickyBucket(c *ui.Context, t *ui.Theme, name string, items []store.Todo, now time.Time) {
	col := map[string]ui.Color{
		"早": t.Warning, "中": t.Accent, "晚": t.Danger, "全天": t.TextMuted,
	}[name]
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			kit.Pill(c, t, name, col)
			ui.Text(c, itoa(len(items))).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, "").Grow(1)
		})
		for _, td := range items {
			a.stickyTodoRow(c, t, td, now)
		}
	})
}

// stickyTodoRow 便签里的一行：勾选 + 时间 + 标题。
func (a *App) stickyTodoRow(c *ui.Context, t *ui.Theme, td store.Todo, now time.Time) {
	done := false
	ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(2, 2).Children(func() {
		check := ui.Checkbox(c, &done, "")
		check.Tooltip("标记完成")
		if check.Changed() && done {
			if a.setTodoStatus(td.ID, TodoDone) {
				a.stickyInvalidate()
			}
		}
		when := "全天"
		if !td.DueAt.IsZero() {
			when = td.DueAt.Local().Format("15:04")
		}
		ui.Text(c, when).Width(44).Shrink(0).
			FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
		title := ui.Text(c, td.Title).Grow(1).SingleLine().FontSize(t.FontSize - 1)
		if !td.DueAt.IsZero() && td.DueAt.Before(now) {
			title.TextColor(t.Danger)
		}
		tooltipIfSet(title, td.Note)
	})
}

// stickySection 设置页「今日便签」区块。
func (a *App) stickySection(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Switch(c, &a.settings.StickyEnabled).Shrink(0)
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "显示今日便签").Bold()
			ui.Text(c, "无边框置顶小窗，按早 / 中 / 晚列出今天要做的待办；拖动标题栏移动，位置会记住。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		})
	})
	if !a.settings.StickyEnabled {
		return
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "回到屏幕右上角").Clicked() {
			a.settings.StickyX, a.settings.StickyY = -1, -1
			a.persist()
			if x, y, ok := a.stickyDefaultPos(); ok && a.stickyWin != nil {
				a.stickyWin.SetPosition(x, y)
			}
		}
		ui.Text(c, fmt.Sprintf("当前尺寸 %d × %d", stickyW, stickyH)).
			FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	})
}
