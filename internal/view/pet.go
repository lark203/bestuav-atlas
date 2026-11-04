package view

import (
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/pet"
	"bestuav-atlas/internal/settings"
)

// 伴生桌宠：一个无边框、透明、置顶、不占任务栏的小窗口，只画一只矢量小猫/小狗。
//
// 低占用约束（实现时不许破坏）：
//   - 静止时不请求帧：靠 c.After(20s) 做一次低频「心跳」；
//   - 只有矢量路径（无图片/无贴图），窗口两百像素见方；
//   - 为保证拖拽稳定，不使用点击互动；拖动走框架原生窗口拖动；
//   - 关闭窗口后不残留定时器（窗口销毁即停）。

// petBaseSize 幼崽阶段的窗口逻辑尺寸（按成长阶段缩放）。
const (
	petBaseW = 190
	petBaseH = 235
	// petBubbleSlot 头顶气泡的固定高度：气泡出现/消失都不挤动宠物。
	petBubbleSlot = 46
)

// petSize 当前阶段对应的窗口尺寸。
func petSize(stage int) (int, int) {
	s := pet.Scale(stage)
	return int(float64(petBaseW) * s), int(float64(petBaseH) * s)
}

// petTick 累积陪伴时长、驱动久坐提醒、同步窗口。
//
// 主窗口每帧与桌宠窗口每 20 秒都会调用；delta 以 petTickAt 为基准，重复调用不会多算。
func (a *App) petTick(now time.Time) {
	a.petSyncWindow()
	if !a.settings.PetEnabled {
		return
	}
	if a.petTickAt.IsZero() {
		a.petTickAt = now
		if a.petLastBreak.IsZero() {
			a.petLastBreak = now
		}
	}
	delta := now.Sub(a.petTickAt)
	a.petTickAt = now
	// 睡眠/长时间挂起后不把整段空闲算成陪伴时长（心跳是 20 秒，5 分钟已很宽松）。
	if delta > 0 && delta < 5*time.Minute {
		xp, stage := pet.AddXP(a.petXP, delta.Minutes())
		a.petXP = xp
		if stage != a.petStage {
			a.petStage = stage
			a.petResize()
			a.toast("桌宠长大了一点：" + pet.StageName(stage))
		}
		if a.petXP-a.petSavedXP >= 1 {
			a.petSavedXP = a.petXP
			a.settings.PetXP = a.petXP
			a.persist()
		}
	}
	// 位置：窗口移动没有事件，1 秒轮询一次（比每帧读平台窗口便宜）。
	if a.petWin != nil && now.Sub(a.petPollAt) >= time.Second {
		a.petPollAt = now
		x, y := a.petWin.Position()
		if x != a.settings.PetX || y != a.settings.PetY {
			a.settings.PetX, a.settings.PetY = x, y
			a.petLastPoke = now // 拖动过就保持坐姿，别刚放下就躺下
			a.persist()
		}
	}
	if pet.ReminderDue(a.petLastBreak, now, a.settings.PetReminderMinutes) {
		a.petLastBreak = now
		a.petRemind()
	}
}

// petSyncWindow 根据开关创建/销毁桌宠窗口。
func (a *App) petSyncWindow() {
	if a.settings.PetEnabled == a.petApplied {
		return
	}
	// 主窗口还没就绪（启动早期 / 测试）：下一帧再试，不要把「已应用」提前置位。
	if a.settings.PetEnabled && a.win == nil {
		return
	}
	a.petApplied = a.settings.PetEnabled
	if a.settings.PetEnabled {
		a.openPetWindow()
	} else {
		a.closePetWindow()
	}
	a.persist()
}

// openPetWindow 创建桌宠窗口（需要主窗口已就绪；托盘模式下也允许）。
func (a *App) openPetWindow() {
	if a.petWin != nil || a.win == nil {
		return
	}
	if a.petLastPoke.IsZero() {
		a.petLastPoke = time.Now() // 刚出现时坐着，不要立刻躺下
	}
	w, h := petSize(a.petStage)
	opts := mygo.WindowOptions{
		Title:             "桌宠",
		Width:             w,
		Height:            h,
		Frameless:         true,
		Transparent:       true,
		AlwaysOnTop:       true,
		SkipTaskbar:       true,
		DisableResize:     true,
		DisableMaximize:   true,
		DisableMinimize:   true,
		DisableFullScreen: true,
		DisableShadow:     true,
		// 桌宠没有标题栏也没有关闭按钮；键盘/系统关闭也禁掉，避免误关后
		// 设置里仍显示「已开启」。
		DisableClose: true,
		Content:      ui.View(a.petView),
	}
	if a.settings.PetX >= 0 && a.settings.PetY >= 0 {
		opts.X, opts.Y = a.settings.PetX, a.settings.PetY
	}
	win := mygo.NewWindow(opts)
	if win == nil {
		a.settings.PetEnabled = false
		a.petApplied = false
		a.toast("桌宠窗口创建失败")
		return
	}
	a.petWin = win
	win.OnClosed(func() {
		// 只有应用退出或设置里关掉桌宠时才会走到这里；不改设置（退出时改会丢开关）。
		a.petWin = nil
		a.petApplied = false
		a.requestFrame()
	})
	a.persist()
}

// closePetWindow 关闭桌宠窗口。
func (a *App) closePetWindow() {
	if a.petWin == nil {
		return
	}
	a.petWin.Close()
	a.petWin = nil
}

// petResize 阶段变化后调整窗口大小（保持当前位置）。
func (a *App) petResize() {
	if a.petWin == nil {
		return
	}
	w, h := petSize(a.petStage)
	a.petWin.SetSize(w, h)
}

// petPose 当前姿势：60 秒没有拖动就侧卧，否则坐着。
//
// 为了拖拽稳定，桌宠不做点击互动（点击与窗口拖动在框架里互斥，手工模拟拖动会抖动）。
func (a *App) petPose(now time.Time) (petPose, float32) {
	if !a.petLastPoke.IsZero() && now.Sub(a.petLastPoke) > time.Minute {
		return poseLie, 0
	}
	return poseSit, 0
}

// petView 桌宠窗口的内容：固定气泡槽 + 矢量宠物。
func (a *App) petView(c *ui.Context) {
	now := time.Now()
	a.petTick(now)
	t := c.Theme()
	accent := t.Accent
	// 透明窗口：把根节点的主题背景清掉，否则会看到一块白底。
	c.Root().Background(ui.Transparent)

	pose, _ := a.petPose(now)
	bubble := now.Before(a.petBubbleUntil)

	// 静止时 20 秒一次心跳（结算经验与久坐提醒）；气泡消失时补一帧。
	c.After(20 * time.Second)
	if bubble {
		c.After(time.Until(a.petBubbleUntil))
	}

	// 拖动交给框架的原生窗口拖动：元素只带 DragWindow、不带任何点击标记，
	// 这样既跟手又不会抖。代价是桌宠不响应点击（见 petPose 的说明）。
	root := ui.Box(c).Fill().DragWindow()
	root.Children(func() {
		ui.Column(c).Fill().AlignItems(ui.Center).PassThrough().Children(func() {
			// 气泡槽：固定高度，出现/消失不影响宠物位置。
			ui.Box(c).Width(180).Height(petBubbleSlot).AlignItems(ui.Center).Justify(ui.End).
				PassThrough().Children(func() {
				if bubble {
					ui.Text(c, a.petBubbleText).FontSize(11).TextColor(t.AccentText).
						Background(t.Accent).Radius(10).Padding(8, 5).SingleLine()
				}
			})
			ui.Box(c).Width(180).Grow(1).PassThrough().Draw(func(p *ui.Painter, r ui.Rect) {
				drawPetArt(p, r, a.settings.PetSkin, pose, 0, accent)
			})
		})
	})
}

// petRemind 久坐提醒：气泡 + （主窗口不可见时）系统通知。
func (a *App) petRemind() {
	a.petBubbleText = "起来活动一下～"
	a.petBubbleUntil = time.Now().Add(15 * time.Second)
	if a.petWin != nil {
		a.petWin.Invalidate()
	}
	if a.win != nil && !a.win.IsVisible() && mygo.NotificationsSupported() {
		n := mygo.NewNotification(mygo.NotificationOptions{
			ID:    "pet-break",
			Title: "该休息一下了",
			Body:  fmt.Sprintf("已经坐了 %d 分钟，起来走走、看看远处。", a.settings.PetReminderMinutes),
		})
		_ = n.Show()
	}
	a.requestFrame()
}

// petSection 设置页「桌宠」区块。
func (a *App) petSection(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Switch(c, &a.settings.PetEnabled).Shrink(0)
		ui.Column(c).Gap(2).Grow(1).Children(func() {
			ui.Text(c, "显示桌宠").Bold()
			ui.Text(c, "无边框、透明、置顶的小窗，不占任务栏；静止时不重绘，只有矢量图形。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		})
	})

	// 皮肤：两档。
	skinIdx := 0
	if pet.NormalizeSkin(a.settings.PetSkin) == settings.PetSkinDog {
		skinIdx = 1
	}
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "皮肤").Width(72).Shrink(0).TextColor(t.TextMuted)
		ui.Segmented(c, &skinIdx, "小猫", "小狗")
	})
	if got := []string{settings.PetSkinCat, settings.PetSkinDog}[skinIdx]; got != a.settings.PetSkin {
		a.settings.PetSkin = got
		a.persist()
		if a.petWin != nil {
			a.petWin.Invalidate()
		}
	}

	// 久坐提醒：0 = 关闭。
	remind := float64(a.settings.PetReminderMinutes)
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "久坐提醒").Width(72).Shrink(0).TextColor(t.TextMuted)
		ui.Stepper(c, &remind, 0, settings.MaxPetReminderMinutes, 5)
		hint := fmt.Sprintf("每 %d 分钟提醒一次", int(remind))
		if int(remind) == 0 {
			hint = "关闭提醒"
		}
		ui.Text(c, hint).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
	})
	if got := int(remind); got != a.settings.PetReminderMinutes {
		a.settings.PetReminderMinutes = got
		a.persist()
	}

	// 成长：只展示，不提供「一键喂大」——陪伴时长才有意义。
	stage := pet.Stage(a.petXP)
	next, ok := pet.NextThreshold(stage)
	progress := pet.Progress(a.petXP, stage)
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "成长").Width(72).Shrink(0).TextColor(t.TextMuted)
		ui.Text(c, pet.StageName(stage)).Bold()
		ui.Row(c).Gap(0).Width(160).Height(6).Children(func() {
			if progress > 0 {
				ui.Box(c).Width(160 * float32(progress)).Height(6).Radius(3).Background(t.Accent)
			}
			if progress < 1 {
				ui.Box(c).Width(160 * float32(1-progress)).Height(6).Radius(3).
					Background(t.SurfacePressed)
			}
		})
		if ok {
			ui.Text(c, fmt.Sprintf("%d / %d 分钟", int(a.petXP), int(next))).
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		} else {
			ui.Text(c, "已成年（不再长大）").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		}
	})
	ui.Text(c, "成长只看陪伴时长（前台每 20 秒结算一次），换机器后随之同步；"+
		"关掉桌宠不会丢进度。拖拽可移动，60 秒不动就躺下休息。").
		TextColor(t.TextMuted).FontSize(t.FontSize - 2)

	ui.Row(c).Children(func() {
		if ui.Button(c, "回到屏幕中央").Clicked() && a.petWin != nil {
			a.petWin.Center()
		}
	})
}
