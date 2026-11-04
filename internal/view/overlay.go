package view

import (
	"math/rand/v2"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/ui/kit"
	"bestuav-atlas/internal/view/fx"
)

// 本文件放两类「跨页面」的视觉反馈：
//   - 彩带庆祝（celebrate / drawCelebration）：任务完成时撒彩带；
//   - 重量级任务遮罩（heavyTaskMask）：任务完成前禁止一切操作。
//
// 彩带核心算法移植自 mygo 官方 examples/confetti（见 internal/view/fx）。

// celebrate 撒 n 团彩带。每团从窗口下方不同位置以不同角度炸开，视觉上像
// 多处同时放礼花；不放完不会重复叠加，避免连续点击时满屏都是。
func (a *App) celebrate(n int) {
	if n <= 0 {
		n = 3
	}
	for i := 0; i < n; i++ {
		o := fx.Defaults()
		o.Count = 60
		// 每团角度与起点都不同：中间偏上两团向上，两侧各一团向内斜上。
		o.Angle = 60 + rand.Float64()*60
		o.Spread = 50
		o.StartVelocity = 48
		o.Ticks = 210
		o.X = 0.2 + rand.Float64()*0.6
		o.Y = 0.78
		o.Scalar = 0.9 + rand.Float64()*0.5
		o.Shapes = []fx.Shape{fx.Square, fx.Circle, fx.Star, fx.Heart}
		a.nextBurstID++
		a.bursts = append(a.bursts, fx.New(o, o.X, o.Y, a.nextBurstID))
	}
	a.requestFrame()
}

// drawCelebration 在窗口最上层绘制彩带，并把已放完的团清掉。
//
// 用 ui.Overlay 而不是普通子元素：彩带必须盖在侧栏之上且不参与布局。
// 每片彩带都 PassThrough，不拦截鼠标。
func (a *App) drawCelebration(c *ui.Context) {
	if len(a.bursts) == 0 {
		return
	}
	now := time.Now()
	live := a.bursts[:0]
	for _, b := range a.bursts {
		if !b.Expired(now) {
			live = append(live, b)
		}
	}
	a.bursts = live
	if len(a.bursts) == 0 {
		return
	}
	ui.Overlay(c, func() {
		for _, b := range a.bursts {
			b.Build(c, now)
		}
	})
}

// ---- 重量级任务：全局遮罩 ----

// startHeavyTask 运行一个「重量级任务」：期间整个窗口被遮罩盖住，用户无法
// 操作任何控件（也点不到关闭按钮，因为遮罩在最上层）。
//
// 真实业务把 goroutine 里的模拟循环换成实际工作即可；结束时 endHeavyTask。
func (a *App) startHeavyTask(label string) {
	if a.heavyRunning {
		return // 重量级任务串行执行
	}
	a.heavyRunning = true
	a.heavyProgress = 0
	a.heavyLabel = label

	go func() {
		// 后台协程里的 panic 不会经过 View 的 recover，单独兜一层。
		defer logging.Recover("heavy-task")
		const steps = 40
		for i := 1; i <= steps; i++ {
			time.Sleep(110 * time.Millisecond)
			v := float64(i) / steps
			mygo.RunOnMain(func() {
				a.heavyProgress = v
				a.requestFrame()
			})
		}
		mygo.RunOnMain(func() {
			a.heavyRunning = false
			a.heavyProgress = 0
			label := a.heavyLabel
			a.heavyLabel = ""
			a.celebrate(5) // 重量级任务完成后放更多彩带
			a.pushTaskRow(label+"（重量级）", "重量级", "已完成")
			a.notifyTaskDone(label + "（重量级）")
			a.requestFrame()
		})
	}()
}

// heavyTaskMask 绘制重量级任务遮罩。
//
// 用 ui.Modal 实现而不是自绘覆盖层：Modal 自带全窗口背景（拦截指针）、焦点
// 收束与 Escape 处理，是框架里唯一「保证点不到底层控件」的组件。
// 传入的是**局部** open 变量，且每帧都为 true——于是用户按 Esc 或点击遮罩外侧
// 都无法关闭它，只能等任务自己结束，符合「完成前禁止操作」的语义。
func (a *App) heavyTaskMask(c *ui.Context, t *ui.Theme) {
	if !a.heavyRunning {
		return
	}
	open := true
	modalPanel(c, t, &open, func() {
		ui.Column(c).Gap(14).Width(360).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, "正在执行重量级任务").FontSize(16).Bold().Grow(1)
			})
			label := a.heavyLabel
			if label == "" {
				label = "处理中…"
			}
			ui.Text(c, label).TextColor(t.TextMuted).SingleLine()

			ui.Progress(c, a.heavyProgress).Height(6)
			ui.Text(c, percentText(a.heavyProgress)).
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)

			ui.Text(c, "任务完成前界面不可操作，请稍候。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		})
	})
}

// modalPanel 是不透明的 ui.Modal：所有弹窗都走这里。
//
// 为什么需要：设置背景图后，applyBackdropTheme 会把 t.Background 的 alpha 调低
// （让壁纸从内容区透出），而 ui.Modal 的面板正是用 t.Background 画的 → 弹窗跟着半透明，
// 弹窗正文与背后的页面叠在一起根本没法读（实测截图确认）。
//
// 实现依据：主题颜色是在「构建元素时复制进元素」的（不是绘制时现读），所以只在这段
// 构建期内把 t.Background 换成不透明值，就只影响弹窗自身——已经构建好的页面、以及
// 之后构建的其他浮层都不受影响。
func modalPanel(c *ui.Context, t *ui.Theme, open *bool, fn func()) {
	bg := t.Background
	defer func() { t.Background = bg }()
	if bg.A < 255 {
		t.Background = ui.RGBA(bg.R, bg.G, bg.B, 1)
	}
	ui.Modal(c, open, fn)
}

// percentText 把 0..1 的进度格式化成百分比文本。
func percentText(v float64) string {
	if v < 0 {
		return "进行中…"
	}
	p := int(v*100 + 0.5)
	return itoa(p) + "%"
}

// ---- 轻提示（toast）----

// toast 在窗口底部弹出一条 2.2 秒的轻提示，用于「已复制」「已重置」这类反馈。
// 用 Overlay 绘制，不参与布局、不拦截鼠标。
func (a *App) toast(msg string) {
	a.toastMsg = msg
	a.toastUntil = time.Now().Add(2200 * time.Millisecond)
	a.requestFrame()
}

// drawToast 绘制尚未过期的轻提示。
func (a *App) drawToast(c *ui.Context, t *ui.Theme) {
	if a.toastMsg == "" || time.Now().After(a.toastUntil) {
		return
	}
	ui.Overlay(c, func() {
		ui.Row(c).Absolute().LeftPercent(50).Bottom(28).PassThrough().
			Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).
					Padding(t.Space(2), t.Space(4)).Radius(t.Space(2)).
					Background(kit.Alpha(t.Text, 0.92)).
					Children(func() {
						ui.Text(c, a.toastMsg).FontSize(t.FontSize - 2).
							TextColor(t.Background).SingleLine()
					})
			})
	})
}

// copyText 写入系统剪贴板并给出反馈。桌面应用里「复制结果」是最常见的收尾动作，
// 统一走这里，避免每个页面各写一遍反馈文案。
func (a *App) copyText(text, okMsg string) {
	if text == "" {
		a.toast("没有可复制的内容")
		return
	}
	mygo.Clipboard.WriteText(text)
	a.toast(okMsg)
}
