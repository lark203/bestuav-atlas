package view

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/shortcut"
)

// 本文件是全局快捷键。mygo 的 c.Shortcut 是「登记 + 本帧匹配」的模型：
// 每帧都要调用一次，按键到达的那一帧返回 true——所以入口只有一个（View 开头）。
//
// 键位可在设置页改：settings.Shortcuts 存「动作 id → 键位字符串」，
// 启动时与默认值合并成 a.shortcuts（动作 id → 解析后的绑定）。

// rebuildShortcuts 根据设置重建运行时绑定表。
//
// 配置里出现未知动作/非法键位时只记日志、跳过该条：一份写坏的配置不该让
// 全部快捷键失效（默认值仍然生效）。
func (a *App) rebuildShortcuts() {
	resolved := shortcut.Resolve(a.settings.Shortcuts)
	m := make(map[string]shortcut.Binding, len(resolved))
	for id, s := range resolved {
		b, err := shortcut.Parse(s)
		if err != nil {
			logging.Warn("shortcut", "快捷键配置无效，已忽略", "action", id, "value", s, "err", err.Error())
			continue
		}
		if b.Zero() {
			continue // 显式禁用
		}
		m[id] = b
	}
	a.shortcuts = m
	// 侧栏开关的提示文本顺带刷新：悬浮提示要跟着用户改绑的键位走。
	a.sidebarToggleHint = ""
	if b, ok := m["view.toggle-sidebar"]; ok {
		a.sidebarToggleHint = shortcut.Label(b.String)
	}
}

// shortcutLabel 返回某个动作当前生效键位的展示文本（禁用时为「未设置」）。
func (a *App) shortcutLabel(id string) string {
	if b, ok := a.shortcuts[id]; ok {
		return shortcut.Label(b.String)
	}
	return "未设置"
}

// setShortcut 把某个动作改绑到「按键 + 修饰」，冲突或非法时返回错误。
func (a *App) setShortcut(actionID string, mods ui.Modifiers, key ui.Key) error {
	if key == ui.KeyEscape {
		return fmt.Errorf("Esc 保留给「关闭弹窗」，不能绑定")
	}
	b, ok := shortcut.FromInput(mods, key)
	if !ok {
		return fmt.Errorf("这个按键不能用作快捷键")
	}
	for id, other := range a.shortcuts {
		if id == actionID {
			continue
		}
		if other.Mods == b.Mods && other.Key == b.Key {
			name := "其它动作"
			if act, ok := shortcut.ByID(id); ok {
				name = act.Label
			}
			return fmt.Errorf("该组合已被「%s」占用", name)
		}
	}
	if a.settings.Shortcuts == nil {
		a.settings.Shortcuts = map[string]string{}
	}
	a.settings.Shortcuts[actionID] = b.String
	a.rebuildShortcuts()
	a.persist()
	return nil
}

// resetShortcuts 清掉用户覆盖，恢复默认键位。
func (a *App) resetShortcuts() {
	a.settings.Shortcuts = nil
	a.rebuildShortcuts()
	a.shortcutCapture = ""
	a.persist()
}

// handleShortcuts 处理全局快捷键。
func (a *App) handleShortcuts(c *ui.Context) {
	// 正在录制新键位：这一帧不登记任何快捷键，把按键让给设置页的捕获控件。
	if a.shortcutCapture != "" {
		return
	}
	// Esc 保留给关闭弹窗（不参与自定义）。
	if c.Shortcut(0, ui.KeyEscape) {
		if a.closeTopOverlay() {
			c.Invalidate()
		}
		return
	}
	for _, act := range shortcut.Actions {
		b, ok := a.shortcuts[act.ID]
		if !ok {
			continue
		}
		if c.Shortcut(b.Mods, b.Key) {
			a.runShortcut(act)
			c.Invalidate()
			return
		}
	}
}

// runShortcut 执行一个动作。
func (a *App) runShortcut(act shortcut.Action) {
	switch {
	case act.ToggleSidebar:
		a.toggleSidebar()
	case act.Refresh:
		a.refreshPage()
	case act.Page != "":
		a.SetPage(act.Page)
	}
}

// refreshPage 重新进入当前页：keepFresh 的页面先销毁，保证拿到干净状态；
// 其余页面保留缓存，只重跑 onEnter（「刷新数据，但别丢我填了一半的表单」）。
func (a *App) refreshPage() {
	it, ok := pageByID(a.page)
	if !ok {
		return
	}
	if it.policy.keep == keepFresh {
		a.destroyPage(it.id)
	}
	a.enterPage(it, time.Now())
	a.toast("已刷新当前页")
}

// closeTopOverlay 关闭最上层的弹窗，返回是否真的关掉了一个。
//
// 顺序与绘制顺序相反（后画的先关）：更新弹窗 > 关闭询问 > 基本信息 > 通用弹窗。
func (a *App) closeTopOverlay() bool {
	switch {
	case a.showUpdateDialog:
		a.showUpdateDialog = false
	case a.showCloseChoice:
		a.showCloseChoice = false
	case a.showBasicInfo:
		a.showBasicInfo = false
	case a.showDlg:
		a.showDlg = false
	default:
		return false
	}
	a.requestFrame()
	return true
}
