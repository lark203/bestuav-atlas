package shortcut

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestParseFormatRoundTrip 存储格式必须能往返：写进去什么，读出来还是什么。
func TestParseFormatRoundTrip(t *testing.T) {
	cases := []string{
		"cmd+1", "cmd+shift+k", "f5", "alt+left", "shift+space",
		"cmd+minus", "super+period", "a", "f12", "pagedown",
	}
	for _, want := range cases {
		b, err := Parse(want)
		if err != nil {
			t.Fatalf("Parse(%q): %v", want, err)
		}
		if b.String != want {
			t.Errorf("往返不一致: %q → %q", want, b.String)
		}
	}
}

// TestParseEmptyIsDisabled 空串是「禁用」，不是错误。
func TestParseEmptyIsDisabled(t *testing.T) {
	b, err := Parse("")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Zero() {
		t.Fatal("空串应解析为零值（禁用）")
	}
}

// TestParseRejectsGarbage 脏数据要报错（界面会原样展示）。
func TestParseRejectsGarbage(t *testing.T) {
	for _, s := range []string{"nope+2", "cmd+nosuchkey", "cmd+"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) 应报错", s)
		}
	}
}

// TestFormatUsesCmdForPlatformModifier 平台主修饰键统一写成 cmd。
func TestFormatUsesCmdForPlatformModifier(t *testing.T) {
	if got := Format(ui.Cmd, ui.Key2); got != "cmd+2" {
		t.Errorf("Format(Cmd, 2) = %q，期望 cmd+2", got)
	}
	// Cmd+Shift 组合不能丢 Shift。
	if got := Format(ui.Cmd|ui.Shift, ui.KeyK); got != "cmd+shift+k" {
		t.Errorf("Format(Cmd|Shift, K) = %q", got)
	}
}

// TestFromInputRejectsUnknown 不支持的键（如单纯的 KeyUnknown）不产生绑定。
func TestFromInputRejectsUnknown(t *testing.T) {
	if _, ok := FromInput(0, ui.KeyUnknown); ok {
		t.Error("KeyUnknown 不应产生绑定")
	}
}

// TestLabel 展示文本要可读。
func TestLabel(t *testing.T) {
	if got := Label("cmd+shift+k"); got == "" || got == "cmd+shift+k" {
		t.Errorf("Label 未转换成可读文本: %q", got)
	}
	if got := Label(""); got != "未设置" {
		t.Errorf("空绑定的标签应为「未设置」，实际 %q", got)
	}
	if got := Label("bogus+x"); got != "bogus+x" {
		t.Errorf("脏数据应原样展示，实际 %q", got)
	}
}

// TestResolve 用户覆盖优先，未知动作忽略。
func TestResolve(t *testing.T) {
	got := Resolve(map[string]string{
		"nav.task":     "cmd+9",
		"view.refresh": "",
		"no.such":      "cmd+8",
	})
	if got["nav.task"] != "cmd+9" {
		t.Errorf("覆盖未生效: %q", got["nav.task"])
	}
	if _, ok := got["no.such"]; ok {
		t.Error("未知动作不应出现在结果里")
	}
	if _, ok := got["view.refresh"]; !ok {
		t.Error("显式禁用（空串）也应保留在结果里")
	}
	// 未覆盖的用默认。
	if got["nav.home"] != "cmd+1" {
		t.Errorf("默认值丢了: %q", got["nav.home"])
	}
}

// TestDefaultsCoverAllActions 每个动作都要有默认键（否则设置页展示空白）。
func TestDefaultsCoverAllActions(t *testing.T) {
	d := Defaults()
	for _, a := range Actions {
		if a.Def == "" {
			continue
		}
		if _, err := Parse(d[a.ID]); err != nil {
			t.Errorf("动作 %s 的默认键位非法: %v", a.ID, err)
		}
	}
}

// TestActionsHaveExactlyOneEffect 每个动作必须有且只有一种效果：
// 打开页面 / 刷新当前页 / 收展侧栏。
//
// 少写一种效果的表现是「设置页能改键、按下去什么都不发生」，而多写一种会让同一个
// 组合键同时做两件事——两种都很难在 UI 上发现，所以在这里卡死。
func TestActionsHaveExactlyOneEffect(t *testing.T) {
	for _, a := range Actions {
		n := 0
		if a.Page != "" {
			n++
		}
		if a.Refresh {
			n++
		}
		if a.ToggleSidebar {
			n++
		}
		if n != 1 {
			t.Errorf("动作 %s 的效果数量 = %d，期望恰好 1", a.ID, n)
		}
	}
}

// TestDefaultBindingsUnique 默认键位不能互相冲突。
//
// 设置页的冲突检查只覆盖用户自己按下的组合；模板默认值写重了，就会有两个动作同时
// 响应同一个组合键，而界面上看不出来。
func TestDefaultBindingsUnique(t *testing.T) {
	seen := map[string]string{}
	for _, a := range Actions {
		if a.Def == "" {
			continue
		}
		if prev, ok := seen[a.Def]; ok {
			t.Errorf("默认键位 %q 被 %s 与 %s 同时占用", a.Def, prev, a.ID)
		}
		seen[a.Def] = a.ID
	}
}

// TestToggleSidebarAction 侧栏开关是一个独立的窗口级动作：有默认键位、不指向页面
// （因此新项目裁菜单后依然有效）。
func TestToggleSidebarAction(t *testing.T) {
	act, ok := ByID("view.toggle-sidebar")
	if !ok {
		t.Fatal("缺少侧栏开关动作 view.toggle-sidebar")
	}
	if !act.ToggleSidebar || act.Page != "" || act.Refresh {
		t.Errorf("侧栏开关应是纯窗口动作，实际 %+v", act)
	}
	if act.Def != "cmd+b" {
		t.Errorf("侧栏开关默认键位应为 cmd+b，实际 %q", act.Def)
	}
}
