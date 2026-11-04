package view

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/shortcut"
	"bestuav-atlas/internal/ui/kit"
)

// 设置页的「快捷键」区块：列出全部动作与当前键位，点「修改」后按新组合键即改绑。
//
// 录制期间 handleShortcuts 不登记任何全局快捷键（见 shortcuts.go），按键才会落到
// 这里的捕获控件上；Esc 取消，冲突的组合作废并提示。

// shortcutSection 绘制快捷键设置区块。
func (a *App) shortcutSection(c *ui.Context, t *ui.Theme) {
	kit.Section(c, t, "快捷键", func() {
		ui.Text(c, "全局快捷键在任何页面都生效（文本框获得焦点时除外）。点「修改」后按下新的组合键，Esc 取消。").
			TextColor(t.TextMuted).FontSize(t.FontSize - 2)

		for _, act := range shortcut.Actions {
			a.shortcutRow(c, t, act)
		}

		ui.Row(c).Gap(8).Margin(4, 0, 0, 0).Children(func() {
			if ui.Button(c, "恢复默认").Clicked() {
				a.resetShortcuts()
				a.toast("快捷键已恢复默认")
			}
			if a.shortcutCapture != "" {
				if ui.Button(c, "取消录制").Clicked() {
					a.shortcutCapture = ""
					a.requestFrame()
				}
			}
		})
		ui.Text(c, "Esc 与「关闭弹窗」固定绑定，不参与自定义。").
			TextColor(t.TextMuted).FontSize(t.FontSize - 2)
	})
}

// shortcutRow 是单个动作的一行：名称 + 当前键位 + 修改/录制。
func (a *App) shortcutRow(c *ui.Context, t *ui.Theme, act shortcut.Action) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Padding(4, 0).Children(func() {
		ui.Text(c, act.Label).Grow(1)
		ui.Text(c, a.shortcutLabel(act.ID)).
			TextColor(t.TextMuted).FontSize(t.FontSize - 2).
			Width(130).Shrink(0)

		if a.shortcutCapture == act.ID {
			a.shortcutCaptureRow(c, t)
			return
		}
		btn := ui.Button(c, "修改").Shrink(0)
		if btn.Clicked() {
			a.shortcutCapture = act.ID
			a.requestFrame()
		}
	})
}

// shortcutCaptureRow 是录制态控件：获得焦点后吃掉按键事件。
//
// 用按钮而不是普通容器：它天然可获得键盘焦点，AutoFocus 能保证录制一开始就接上
// 键盘；点击它本身不会撤销录制（用户可能只是点了界面其它地方）。
func (a *App) shortcutCaptureRow(c *ui.Context, t *ui.Theme) {
	ui.PrimaryButton(c, "请按下新的组合键…（Esc 取消）").
		Shrink(0).
		AutoFocus().
		HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputKeyDown || ev.Repeat {
				return false
			}
			if ev.Key == ui.KeyEscape {
				a.shortcutCapture = ""
				a.requestFrame()
				return true // 消费掉，避免同时触发「关闭弹窗」
			}
			if err := a.setShortcut(a.shortcutCapture, ev.Mods, ev.Key); err != nil {
				a.toast(err.Error())
				return true // 留在录制态，让用户直接重按
			}
			a.shortcutCapture = ""
			a.toast("快捷键已更新")
			a.requestFrame()
			return true
		})
}
