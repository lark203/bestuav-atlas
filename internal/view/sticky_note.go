package view

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/icons"
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

	// stickyMiniSize 无操作超时后折叠成的小方块边长（DIP）。
	stickyMiniSize = 64
	// stickyMinW / stickyMinH 拖动 resize 的下限：太小连一行字都放不下。
	stickyMinW = 220
	stickyMinH = 160
	// stickyIdleTimeout 多少秒没有任何交互就折叠成小方块。
	stickyIdleTimeout = 60 * time.Second
)

// stickyBuckets 便签分段的固定顺序；与 todoBucket 的返回值一致。
var stickyBuckets = []string{"早", "中", "晚", "全天"}

// stickyTick 主窗口每帧调用：按开关创建/销毁窗口，应用置顶设置，并低频记录位置。
func (a *App) stickyTick() {
	a.stickySyncWindow()
	a.stickyApplyOnTop()
	a.stickyRememberPos()
}

// stickyApplyOnTop 把「始终置顶」设置同步到已经打开的窗口（设置里改完立刻生效）。
func (a *App) stickyApplyOnTop() {
	if a.stickyWin == nil {
		return
	}
	if a.stickyTopTop == a.settings.StickyAlwaysOnTop {
		return
	}
	a.stickyTopTop = a.settings.StickyAlwaysOnTop
	a.stickyWin.SetAlwaysOnTop(a.stickyTopTop)
}

// stickyApplySize 把当前尺寸（正常态用 stickySizeW/H，折叠态用 stickyMiniSize）
// 写到窗口；标题栏图钉即时切换置顶、resize 手柄拖动、折叠/恢复都走这里。
func (a *App) stickyApplySize() {
	if a.stickyWin == nil {
		return
	}
	if a.stickyMini {
		a.stickyWin.SetSize(stickyMiniSize, stickyMiniSize)
		return
	}
	a.stickyWin.SetSize(a.stickySizeW, a.stickySizeH)
}

// stickyToggleTop 标题栏图钉：即时切换「始终置顶」并落盘；图钉图标颜色由
// stickyView 读 settings.StickyAlwaysOnTop 决定，无需另存状态。
func (a *App) stickyToggleTop() {
	a.settings.StickyAlwaysOnTop = !a.settings.StickyAlwaysOnTop
	a.stickyTopTop = a.settings.StickyAlwaysOnTop
	if a.stickyWin != nil {
		a.stickyWin.SetAlwaysOnTop(a.stickyTopTop)
	}
	a.persist()
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
		Width:             a.stickySizeW,
		Height:            a.stickySizeH,
		Frameless:         true,
		AlwaysOnTop:       a.settings.StickyAlwaysOnTop,
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
	a.stickyTopTop = a.settings.StickyAlwaysOnTop
	// 从打开时刻起算 60 秒：一直没交互就折叠成小方块（见 stickyView）。
	a.stickyLastInteract = time.Now()
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

	groups := a.stickyToday(now)
	total := 0
	for _, items := range groups {
		total += len(items)
	}

	// 折叠态：60 秒没有任何交互（悬停 / 点击 / 拖动 / 缩放）就缩成小方块，
	// 只显示待办总数；点一下小方块恢复。展开态下靠 Hovered/Pressed 持续刷新
	// 最后交互时刻，指针停在便签上不会误折叠。
	if !a.stickyMini {
		if now.Sub(a.stickyLastInteract) > stickyIdleTimeout {
			a.stickyMini = true
			a.stickyApplySize()
		}
	}

	root := ui.Box(c).Fill().Background(t.Background)
	// 只有展开态才把「悬停 / 按住」当作交互：折叠态靠点击恢复，避免鼠标一靠近就弹开。
	if !a.stickyMini && (root.Hovered() || root.Pressed()) {
		a.stickyLastInteract = now
	}
	root.Children(func() {
		if a.stickyMini {
			// 折叠态只展示待办总数（一个数字），不画图标 / 文案，
			// 64×64 的小方块里才放得下、也不显得挤。悬浮提示带上数量。
			mini := ui.Box(c).Fill().Cursor(ui.CursorPointer).
				Tooltip("点击展开便签（" + itoa(total) + " 条待办）").
				Background(t.Surface).Children(func() {
				ui.Row(c).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
					ui.Text(c, itoa(total)).Bold().FontSize(24)
				})
			})
			if mini.Clicked() {
				a.stickyMini = false
				a.stickyLastInteract = now
				a.stickyApplySize()
			}
			return
		}

		// 静止时 30 秒一次心跳：刷新「剩 X 分钟」与跨天边界。
		c.After(30 * time.Second)

		ui.Column(c).Fill().Gap(0).Children(func() {
			// 标题栏兼拖拽把手：只有这一条 DragWindow，内容区的勾选框照常可点。
			ui.Row(c).Shrink(0).Padding(12, 10).Gap(8).AlignItems(ui.Center).
				Background(t.Surface).DragWindow().Label("拖动便签").
				Children(func() {
					ui.Text(c, "今日便签").Bold().FontSize(t.FontSize).SingleLine()
					ui.Text(c, now.Format("01-02")+" "+weekdayCN(now)).
						FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
					ui.Text(c, "").Grow(1)
					ui.Text(c, itoa(total)+" 条").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					// 图钉放到标题栏最后：快捷切换始终置顶，不必进设置页。
					// 钉住态用强调色高亮；靠行 AlignItems(Center) 与其它元素垂直居中对齐。
					pinCol := t.TextMuted
					if a.settings.StickyAlwaysOnTop {
						pinCol = t.Accent
					}
					pin := ui.Box(c).Size(22, 22).Cursor(ui.CursorPointer).Tooltip("切换始终置顶").
						Children(func() { ui.Icon(c, icons.Pin).FontSize(14).TextColor(pinCol) })
					if pin.Clicked() {
						a.stickyToggleTop()
						a.stickyLastInteract = now
					}
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
				ui.Text(c, "勾选即完成 · 拖动标题栏移动 · 右下角可缩放 · 设置里可关闭").
					FontSize(t.FontSize - 3).TextColor(t.TextMuted).SingleLine()
			})
		})

		// 右下角缩放手柄：frameless 窗口在 Windows 上没有原生缩放边，自己画一个。
		// Dragged() 每帧给出按下期间指针的位移增量，直接累加到窗口尺寸即可。
		handle := ui.Box(c).Absolute().Right(0).Bottom(0).Size(20, 20).
			Cursor(ui.CursorResizeNWSE)
		dx, dy, held := handle.Dragged()
		if held {
			nw := a.stickySizeW + int(dx)
			nh := a.stickySizeH + int(dy)
			if nw < stickyMinW {
				nw = stickyMinW
			}
			if nh < stickyMinH {
				nh = stickyMinH
			}
			a.stickySizeW, a.stickySizeH = nw, nh
			a.stickyWin.SetSize(nw, nh)
			// 落盘：缩放手柄每帧位移都重置保存计时器，松手后延迟写一次，
			// 下次启动保持调整过的大小（见 settings.StickyW/H 与 newApp 读取）。
			a.settings.StickyW, a.settings.StickyH = nw, nh
			a.persist()
			a.stickyLastInteract = now
		}
		handle.Draw(func(p *ui.Painter, r ui.Rect) {
			// 两条对角短线画在右下角，提示这里可以拖动缩放。
			p.Line(r.X+r.W-11, r.Y+r.H-2, r.X+r.W-2, r.Y+r.H-11, 1.5, t.TextMuted)
			p.Line(r.X+r.W-17, r.Y+r.H-2, r.X+r.W-2, r.Y+r.H-17, 1.5, t.TextMuted)
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
		// 剩余时长：全天（无期限）的任务不显示；逾期的用危险色高亮紧迫感。
		if cd, over := todoCountdown(td); cd != "" {
			cdCol := t.TextMuted
			if over {
				cdCol = t.Danger
			}
			ui.Text(c, cd).Shrink(0).FontSize(t.FontSize - 3).TextColor(cdCol).SingleLine()
		}
	})
}

// stickySection 设置页「今日便签」区块。
func (a *App) stickySection(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		sw := ui.Switch(c, &a.settings.StickyEnabled).Shrink(0)
		if sw.Changed() {
			a.persist()
		}
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "显示今日便签").Bold()
			ui.Text(c, "无边框小窗，按早 / 中 / 晚列出今天要做的待办；拖动标题栏移动，位置会记住。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		})
	})

	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		sw := ui.Switch(c, &a.settings.StickyAlwaysOnTop).Shrink(0)
		if sw.Changed() {
			a.persist()
		}
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "始终置顶").Bold()
			ui.Text(c, "打开时便签浮在所有窗口之上；关掉后它像普通窗口一样会被其它窗口盖住，不会一直挡住屏幕右上角。设置改动立刻生效。").
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
