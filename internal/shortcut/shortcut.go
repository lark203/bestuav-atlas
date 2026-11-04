// Package shortcut 定义应用的全局快捷键：动作清单、默认键位与「键位字符串」的解析/格式化。
//
// 为什么单独成包：设置页要把键位存进 JSON、启动时读回来、运行时逐帧匹配，
// 三处必须用同一套字符串格式；把它放在这里就能单测往返一致，也不会让 UI 包
// 依赖设置包。
//
// 存储格式（settings.json 的 shortcuts 字段）：
//
//	"cmd+1"        ⌘/Ctrl + 1（cmd = 平台主修饰键：macOS 是 ⌘，其它平台是 Ctrl）
//	"ctrl+shift+k" 显式组合
//	"f5"           单键
//	""             禁用该动作
//
// 修饰键顺序固定：cmd / shift / ctrl / alt / super。
package shortcut

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Action 是一个可绑定快捷键的动作。
type Action struct {
	ID    string // 配置里的键（稳定标识，不要翻译）
	Label string // 设置页展示的动作名
	// Page 非空时，动作 = 打开这个页面。
	Page string
	// Refresh 为 true 时，动作 = 刷新当前页。
	Refresh bool
	// ToggleSidebar 为 true 时，动作 = 收起/展开侧栏。
	//
	// 它是「窗口级动作」而不是导航动作：不指向任何页面，因此新项目裁掉菜单分组后
	// 依然有效（见 internal/scaffold/shortcut.go 的裁剪规则——只删 Page 指向已移除
	// 页面的动作）。
	ToggleSidebar bool
	Def           string // 默认键位（存储格式）；空 = 默认不绑定
}

// Actions 全部动作，顺序即设置页展示顺序。
var Actions = []Action{

	{ID: "nav.home", Label: "打开首页", Page: "home", Def: "cmd+1"},
	{ID: "nav.task", Label: "打开异步任务", Page: "task", Def: "cmd+2"},
	{ID: "nav.license", Label: "打开生成 license", Page: "gen-license", Def: "cmd+3"},
	{ID: "nav.settings", Label: "打开设置", Page: "settings", Def: "cmd+4"},
	{ID: "view.refresh", Label: "刷新当前页", Refresh: true, Def: "f5"},
	{ID: "view.toggle-sidebar", Label: "收起/展开侧栏", ToggleSidebar: true, Def: "cmd+b"},
}

// ByID 按动作 id 查找。
func ByID(id string) (Action, bool) {
	for _, a := range Actions {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// Binding 是一个解析后的键位。
type Binding struct {
	Mods ui.Modifiers
	Key  ui.Key
	// String 是存储格式（见包注释）。
	String string
}

// Zero 报告绑定是否为空（禁用）。
func (b Binding) Zero() bool { return b.Key == ui.KeyUnknown }

// Parse 解析存储格式的键位（空串 = 禁用，返回零值且无错误）。
func Parse(s string) (Binding, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return Binding{}, nil
	}
	parts := strings.Split(s, "+")
	keyName := parts[len(parts)-1]
	var mods ui.Modifiers
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "cmd":
			mods |= ui.Cmd
		case "ctrl", "control":
			mods |= ui.Ctrl
		case "shift":
			mods |= ui.Shift
		case "alt", "option":
			mods |= ui.Alt
		case "super", "win", "meta":
			mods |= ui.Super
		default:
			return Binding{}, fmt.Errorf("未知修饰键 %q", p)
		}
	}
	key, ok := keyByName(keyName)
	if !ok {
		return Binding{}, fmt.Errorf("未知按键 %q", keyName)
	}
	return Binding{Mods: mods, Key: key, String: Format(mods, key)}, nil
}

// FromInput 把一次按键事件转成绑定；不支持的键或仅修饰键返回 ok=false。
//
// cmd 用平台主修饰键表示：Windows 上按 Ctrl+2 与 macOS 上按 ⌘+2 存下来是同一个值，
// 用户换平台后快捷键仍然成立。
func FromInput(mods ui.Modifiers, key ui.Key) (Binding, bool) {
	if key == ui.KeyUnknown {
		return Binding{}, false
	}
	if _, ok := nameOfKey(key); !ok {
		return Binding{}, false
	}
	return Binding{Mods: mods, Key: key, String: Format(mods, key)}, true
}

// Format 把修饰键与按键转成存储格式（修饰键顺序固定）。
func Format(mods ui.Modifiers, key ui.Key) string {
	name, ok := nameOfKey(key)
	if !ok {
		return ""
	}
	var parts []string
	if mods&ui.Cmd != 0 {
		parts = append(parts, "cmd")
	}
	if mods&ui.Shift != 0 {
		parts = append(parts, "shift")
	}
	// ui.Cmd 本身就等于 Ctrl（Windows/Linux）或 Super（macOS），避免重复。
	if mods&ui.Ctrl != 0 && ui.Cmd != ui.Ctrl {
		parts = append(parts, "ctrl")
	}
	if mods&ui.Alt != 0 {
		parts = append(parts, "alt")
	}
	if mods&ui.Super != 0 && ui.Cmd != ui.Super {
		parts = append(parts, "super")
	}
	parts = append(parts, name)
	return strings.Join(parts, "+")
}

// Label 把存储格式转成给人看的文本（"ctrl+shift+k" → "Ctrl+Shift+K"）。
func Label(s string) string {
	b, err := Parse(s)
	if err != nil {
		return s // 脏数据原样展示，便于用户发现
	}
	if b.Zero() {
		return "未设置"
	}
	var parts []string
	if b.Mods&ui.Cmd != 0 {
		if runtime.GOOS == "darwin" {
			parts = append(parts, "⌘")
		} else {
			parts = append(parts, "Ctrl")
		}
	}
	if b.Mods&ui.Shift != 0 {
		parts = append(parts, "Shift")
	}
	if b.Mods&ui.Ctrl != 0 && ui.Cmd != ui.Ctrl {
		parts = append(parts, "Ctrl")
	}
	if b.Mods&ui.Alt != 0 {
		parts = append(parts, "Alt")
	}
	if b.Mods&ui.Super != 0 && ui.Cmd != ui.Super {
		parts = append(parts, "Win")
	}
	name, _ := nameOfKey(b.Key)
	parts = append(parts, strings.ToUpper(name))
	return strings.Join(parts, "+")
}

// Defaults 返回「动作 → 默认键位」的副本。
func Defaults() map[string]string {
	out := make(map[string]string, len(Actions))
	for _, a := range Actions {
		if a.Def != "" {
			out[a.ID] = a.Def
		}
	}
	return out
}

// Resolve 合并默认键位与用户覆盖：用户显式写了就覆盖（空串 = 禁用）。
func Resolve(overrides map[string]string) map[string]string {
	out := Defaults()
	for id, v := range overrides {
		if _, ok := ByID(id); ok {
			out[id] = strings.ToLower(strings.TrimSpace(v))
		}
	}
	return out
}

// keyNameToKey 是按键名 ↔ Key 的对照表（存储与展示都用小写名）。
var keyNameToKey = func() map[string]ui.Key {
	m := map[string]ui.Key{
		"enter": ui.KeyEnter, "escape": ui.KeyEscape, "tab": ui.KeyTab,
		"space": ui.KeySpace, "backspace": ui.KeyBackspace, "delete": ui.KeyDelete,
		"insert": ui.KeyInsert, "home": ui.KeyHome, "end": ui.KeyEnd,
		"pageup": ui.KeyPageUp, "pagedown": ui.KeyPageDown,
		"left": ui.KeyLeft, "right": ui.KeyRight, "up": ui.KeyUp, "down": ui.KeyDown,
		"minus": ui.KeyMinus, "equal": ui.KeyEqual, "comma": ui.KeyComma,
		"period": ui.KeyPeriod, "slash": ui.KeySlash, "semicolon": ui.KeySemicolon,
		"quote": ui.KeyQuote, "bracketleft": ui.KeyBracketLeft,
		"bracketright": ui.KeyBracketRight, "backslash": ui.KeyBackslash,
		"backquote": ui.KeyBackquote,
	}
	// 逐一声明，不用 Key0+i 之类的算术：键码由平台常量决定，不保证连续。
	digits := []ui.Key{ui.Key0, ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9}
	for i, k := range digits {
		m[fmt.Sprintf("%d", i)] = k
	}
	letters := []ui.Key{
		ui.KeyA, ui.KeyB, ui.KeyC, ui.KeyD, ui.KeyE, ui.KeyF, ui.KeyG, ui.KeyH, ui.KeyI,
		ui.KeyJ, ui.KeyK, ui.KeyL, ui.KeyM, ui.KeyN, ui.KeyO, ui.KeyP, ui.KeyQ, ui.KeyR,
		ui.KeyS, ui.KeyT, ui.KeyU, ui.KeyV, ui.KeyW, ui.KeyX, ui.KeyY, ui.KeyZ,
	}
	for i, k := range letters {
		m[string(rune('a'+i))] = k
	}
	fkeys := []ui.Key{
		ui.KeyF1, ui.KeyF2, ui.KeyF3, ui.KeyF4, ui.KeyF5, ui.KeyF6,
		ui.KeyF7, ui.KeyF8, ui.KeyF9, ui.KeyF10, ui.KeyF11, ui.KeyF12,
	}
	for i, k := range fkeys {
		m[fmt.Sprintf("f%d", i+1)] = k
	}
	return m
}()

// keyNames 是 keyNameToKey 的反查表。
var keyNames = func() map[ui.Key]string {
	m := make(map[ui.Key]string, len(keyNameToKey))
	for name, k := range keyNameToKey {
		m[k] = name
	}
	return m
}()

func keyByName(name string) (ui.Key, bool) {
	k, ok := keyNameToKey[name]
	return k, ok
}

func nameOfKey(k ui.Key) (string, bool) {
	name, ok := keyNames[k]
	return name, ok
}
