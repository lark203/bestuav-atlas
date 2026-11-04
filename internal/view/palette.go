package view

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/settings"
)

// 配色方案：只替换「强调色系」的语义色（强调色 / 悬停 / 按下 / 选中 / 焦点 /
// 强调色上的文字）。背景、描边、正文颜色仍由 system/light/dark 决定——
//
//   - 这样每套配色在浅色与深色下都可读，不需要为 4 套配色 × 2 种明暗各写一份主题；
//   - 默认「跟随系统」保持原有行为：mygo 已经取用桌面强调色（Theme.follow），
//     我们什么都不覆盖；切回来时下一帧自然恢复。
//
// 实现方式与 applyBackdropTheme 相同：mygo 每帧给的是主题的副本（ui.Theme），
// 所以每帧绝对赋值即可，幂等、且不会把改动累积到下一帧。

// paletteDef 一套配色的定义。
type paletteDef struct {
	name  string   // 存储值（与 internal/settings 的 Palette* 常量一致）
	label string   // 设置页展示名
	light ui.Color // 浅色主题下的强调色
	dark  ui.Color // 深色主题下的强调色
}

// paletteDefs 全部配色，顺序即设置页顺序；第一项是「跟随系统」。
var paletteDefs = []paletteDef{
	{name: settings.PaletteSystem, label: "跟随系统"},
	{name: settings.PaletteAmber, label: "琥珀", light: ui.Hex("#b45309"), dark: ui.Hex("#f59e0b")},
	{name: settings.PaletteTeal, label: "青碧", light: ui.Hex("#0f766e"), dark: ui.Hex("#2dd4bf")},
	{name: settings.PaletteIndigo, label: "靛蓝", light: ui.Hex("#4f46e5"), dark: ui.Hex("#818cf8")},
	{name: settings.PaletteGraphite, label: "石墨", light: ui.Hex("#3f3f46"), dark: ui.Hex("#a1a1aa")},
}

// paletteName 把索引映射成存储值；越界回落到「跟随系统」。
func paletteName(i int) string {
	if i < 0 || i >= len(paletteDefs) {
		return settings.PaletteSystem
	}
	return paletteDefs[i].name
}

// paletteIndex 把存储值映射成索引；未知值回落到「跟随系统」。
func paletteIndex(name string) int {
	for i, p := range paletteDefs {
		if p.name == name {
			return i
		}
	}
	return 0
}

// paletteLabels 设置页分段控件的文案。
func paletteLabels() []string {
	out := make([]string, len(paletteDefs))
	for i, p := range paletteDefs {
		out[i] = p.label
	}
	return out
}

// applyPalette 按当前配色覆盖主题的强调色系；「跟随系统」不覆盖。
//
// 悬停/按下/选中/焦点/强调色文字全部由强调色推导，规则与 mygo 的
// Theme.follow 一致（深色提亮、浅色压暗；亮色背景用深色字）。
func applyPalette(t *ui.Theme, name string) {
	p := paletteDefs[paletteIndex(name)]
	if p.name == settings.PaletteSystem {
		return
	}
	base := p.light
	if t.Dark {
		base = p.dark
	}
	base.A = 255
	black := ui.RGB(0, 0, 0)
	white := ui.RGB(255, 255, 255)

	t.Accent = base
	if t.Dark {
		t.AccentHover = base.Mix(white, 0.2)
		t.AccentPressed = base.Mix(black, 0.15)
		t.Selection = base.Alpha(0.4)
		t.Focus = base.Mix(white, 0.2).Alpha(0.6)
	} else {
		t.AccentHover = base.Mix(black, 0.12)
		t.AccentPressed = base.Mix(black, 0.25)
		t.Selection = base.Alpha(0.25)
		t.Focus = base.Alpha(0.55)
	}
	// 强调色上的文字：亮色（如琥珀深色模式）用深色字，否则白字。
	t.AccentText = white
	if luma(base) > 165 {
		t.AccentText = ui.RGB(24, 24, 27)
	}
}

// luma 返回感知亮度（0–255），用于决定强调色上放白字还是深色字。
func luma(c ui.Color) int {
	return (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
}
