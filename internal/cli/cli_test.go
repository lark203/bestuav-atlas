package cli

import (
	"bytes"
	"log/slog"
	"testing"
)

// TestParseDefaults 默认值：不打印版本、不重置、不限页、Info 级别。
func TestParseDefaults(t *testing.T) {
	o, err := Parse(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if o.Version || o.ResetSettings || o.SafeMode || o.Page != "" {
		t.Errorf("默认值不对: %+v", o)
	}
	if o.LogLevel != slog.LevelInfo {
		t.Errorf("默认日志级别应为 Info，实际 %v", o.LogLevel)
	}
}

// TestParseAll 覆盖全部参数（含 --page=xxx 与空格写法）。
func TestParseAll(t *testing.T) {
	o, err := Parse([]string{"--version", "--page=task", "--reset-settings", "--safe-mode", "--log-level=debug"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !o.Version || !o.ResetSettings || !o.SafeMode {
		t.Fatalf("布尔参数解析失败: %+v", o)
	}
	if o.Page != "task" {
		t.Errorf("page 应为 task，实际 %q", o.Page)
	}
	if o.LogLevel != slog.LevelDebug {
		t.Errorf("日志级别应为 Debug，实际 %v", o.LogLevel)
	}
}

// TestParseProfileFlags 导出/导入配置包的参数（含等号与空格两种写法）。
func TestParseProfileFlags(t *testing.T) {
	o, err := Parse([]string{"--export-profile=C:\\tmp\\a.zip", "--import-profile", "C:\\tmp\\b.zip"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if o.ExportProfile != `C:\tmp\a.zip` {
		t.Errorf("export-profile = %q", o.ExportProfile)
	}
	if o.ImportProfile != `C:\tmp\b.zip` {
		t.Errorf("import-profile = %q", o.ImportProfile)
	}
}

// TestParseUnknownFlag 未知参数要报错（而不是静默忽略）。
func TestParseUnknownFlag(t *testing.T) {
	if _, err := Parse([]string{"--nope"}, &bytes.Buffer{}); err == nil {
		t.Fatal("未知参数应报错")
	}
}

// TestParseBadLogLevel 日志级别拼错要报错。
func TestParseBadLogLevel(t *testing.T) {
	if _, err := Parse([]string{"--log-level=verbose"}, &bytes.Buffer{}); err == nil {
		t.Fatal("非法日志级别应报错")
	}
}

// TestPageFromArgs 单实例转发：三种写法都要能取到页面 id。
func TestPageFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--page=settings"}, "settings"},
		{[]string{"-page", "task"}, "task"},
		{[]string{"--page", "settings"}, "settings"},
		{[]string{"-page=task"}, "task"},
		{[]string{}, ""},
		{[]string{"--page"}, ""},
		{[]string{"--other=x"}, ""},
	}
	for _, c := range cases {
		if got := PageFromArgs(c.args); got != c.want {
			t.Errorf("PageFromArgs(%v) = %q，期望 %q", c.args, got, c.want)
		}
	}
}
