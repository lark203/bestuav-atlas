package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bestuav-atlas/internal/appmeta"
)

// isolate 把「用户配置目录」重定向到临时目录，避免测试写坏开发机上的真实偏好。
//
// os.UserConfigDir 在 Windows 读 %AppData%，在 Linux 读 $XDG_CONFIG_HOME，在
// macOS 读 ~/Library/Application Support；同时设置三者即可跨平台生效。
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AppData", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	return dir
}

// TestSaveLoadRoundTrip 验证偏好（含新增的背景图与多实例字段）能原样存取。
func TestSaveLoadRoundTrip(t *testing.T) {
	isolate(t)

	s := Default()
	s.Theme = "dark"
	s.Palette = PaletteTeal
	s.FontSize = 16
	s.CloseMode = "minimize"
	s.SidebarCollapsed = true
	s.AllowMultiInstance = true
	s.BackgroundImage = filepath.Join("tmp", "wall.png")
	s.MenuOpacity = 0.5
	s.ContentOpacity = 0.7
	s.AsyncConcurrency = 6
	s.RepoAutoRefresh = true
	s.RepoAutoRefreshMinutes = 60
	if err := s.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Theme != "dark" || got.Palette != PaletteTeal ||
		got.FontSize != 16 || got.CloseMode != "minimize" ||
		!got.SidebarCollapsed || !got.AllowMultiInstance ||
		got.BackgroundImage != s.BackgroundImage ||
		got.MenuOpacity != 0.5 || got.ContentOpacity != 0.7 ||
		got.AsyncConcurrency != 6 || !got.RepoAutoRefresh ||
		got.RepoAutoRefreshMinutes != 60 {
		t.Fatalf("读取结果与写入不一致: %+v", got)
	}
}

// TestPathUnderAppDir 验证配置文件位于以应用名命名的子目录下
// （曾经直接写在用户配置目录根下，与其他应用混在一起）。
func TestPathUnderAppDir(t *testing.T) {
	dir := isolate(t)
	p, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if !strings.Contains(p, appmeta.Name) {
		t.Errorf("配置路径应包含应用名 %q，实际 %q", appmeta.Name, p)
	}
	if !strings.HasPrefix(filepath.Clean(p), filepath.Clean(dir)) {
		t.Errorf("配置路径应位于隔离目录内，实际 %q", p)
	}
}

// TestNormalizeBadValues 验证脏数据/越界值被纠正为合法值（含老文件缺字段时
// 反序列化得到的 0 值）。
func TestNormalizeBadValues(t *testing.T) {
	isolate(t)
	p, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	bad, _ := json.Marshal(map[string]any{
		"theme":                  "purple",
		"palette":                "neon",
		"tone":                   "vaporwave",
		"fontSize":               99,
		"closeMode":              "explode",
		"menuOpacity":            0,   // 老文件没有该字段 → 0，应回落默认
		"contentOpacity":         9.9, // 越界
		"asyncConcurrency":       99,  // 越界
		"repoAutoRefreshMinutes": 17,  // 不是预设档位
		"petSkin":                "dragon",
		"petReminderMinutes":     999,
		"petXP":                  -3,
		"petX":                   -9,
		"petY":                   -9,
		"repoKind":               "svn",
		"repoToken":              "  tok  ",
		"repoBranch":             "",
		"repoPath":               "/profiles/x.zip/",
	})
	if err := os.WriteFile(p, bad, 0o644); err != nil {
		t.Fatalf("写脏数据: %v", err)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Theme != "system" || s.FontSize != 14 || s.CloseMode != "ask" {
		t.Errorf("枚举/数值未纠正: %+v", s)
	}
	if s.Palette != PaletteSystem {
		t.Errorf("未知配色应回落到跟随系统，实际 %q", s.Palette)
	}
	if s.Tone != ToneDefault {
		t.Errorf("未知色调应回落到默认，实际 %q", s.Tone)
	}
	if s.PetSkin != PetSkinCat || s.PetReminderMinutes != DefaultPetReminderMinutes ||
		s.PetXP != 0 || s.PetX != -1 || s.PetY != -1 {
		t.Errorf("桌宠字段未纠正: %+v", s)
	}
	if s.RepoKind != "github" || s.RepoBranch != "main" || s.RepoToken != "tok" ||
		s.RepoPath != "profiles/x.zip" {
		t.Errorf("仓库字段未纠正: %+v", s)
	}
	if s.MenuOpacity != DefaultMenuOpacity || s.ContentOpacity != DefaultContentOpacity {
		t.Errorf("不透明度未回落到默认: %v / %v", s.MenuOpacity, s.ContentOpacity)
	}
	if s.AsyncConcurrency != DefaultAsyncConcurrency {
		t.Errorf("异步并发未回落到默认: %d", s.AsyncConcurrency)
	}
	if s.RepoAutoRefreshMinutes != DefaultRepoAutoRefreshMinutes {
		t.Errorf("自动刷新间隔未回落到默认: %d", s.RepoAutoRefreshMinutes)
	}
}

// TestLegacyMigration 验证老版本配置（直接放在用户配置目录根下的 settings.json）
// 会被读取并迁移到新的按应用名分目录的位置，用户不丢偏好。
func TestLegacyMigration(t *testing.T) {
	dir := isolate(t)
	legacy, err := legacyPath()
	if err != nil {
		t.Fatalf("legacyPath: %v", err)
	}
	old, _ := json.Marshal(map[string]any{"theme": "light", "fontSize": 18})
	if err := os.WriteFile(legacy, old, 0o644); err != nil {
		t.Fatalf("写旧配置: %v", err)
	}

	s, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Theme != "light" || s.FontSize != 18 {
		t.Fatalf("未从旧路径迁移: %+v", s)
	}

	// 迁移后新位置应已有文件
	newPath, _ := Path()
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Fatalf("迁移后新配置文件不存在: %v (dir=%s)", statErr, dir)
	}
}

// TestResetBacksUpSettings --reset-settings 是「设置写坏、界面打不开」时的救命通道，
// 用户是被迫用它：主题、快捷键、背景图路径全在文件里。Reset 必须留备份，
// 而不是像以前那样直接删。
func TestResetBacksUpSettings(t *testing.T) {
	isolate(t)

	s := Default()
	s.Theme = "dark"
	s.FontSize = 18
	if err := s.Save(); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	p, err := Path()
	if err != nil {
		t.Fatalf("取路径失败: %v", err)
	}

	backup, err := Reset()
	if err != nil {
		t.Fatalf("Reset 失败: %v", err)
	}
	if backup == "" {
		t.Fatal("Reset 应返回备份路径（文件存在时）")
	}
	if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
		t.Error("重置后原设置文件应已不在（已改名为备份）")
	}
	if filepath.Dir(backup) != filepath.Dir(p) {
		t.Errorf("备份应与原文件同目录，实际 %s", backup)
	}
	// 备份内容必须还在（用户能照着改回来）
	data, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("读备份失败: %v", err)
	}
	var got Settings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("备份不是合法 JSON: %v", err)
	}
	if got.Theme != "dark" || got.FontSize != 18 {
		t.Errorf("备份内容不对：theme=%q fontSize=%g", got.Theme, got.FontSize)
	}
	// 重置之后读到的是默认值
	if after := MustLoad(); after.Theme != Default().Theme {
		t.Errorf("重置后应读回默认主题，实际 %q", after.Theme)
	}
}

// TestResetWithoutFileIsNoop 文件本来就不存在时，Reset 不算失败也不产生备份。
func TestResetWithoutFileIsNoop(t *testing.T) {
	isolate(t)
	backup, err := Reset()
	if err != nil {
		t.Fatalf("文件不存在时 Reset 不应报错: %v", err)
	}
	if backup != "" {
		t.Errorf("文件不存在时不该有备份，实际 %q", backup)
	}
}
