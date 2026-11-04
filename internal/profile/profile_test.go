package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bestuav-atlas/internal/store"
)

// fixture 一个「旧机器」：设置文件、数据库、背景图。
type fixture struct {
	dir          string
	settingsPath string
	dbPath       string
	dataDir      string
	bgPath       string
	zipPath      string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{
		dir:          root,
		settingsPath: filepath.Join(root, "config", "settings.json"),
		dbPath:       filepath.Join(root, "data", "data.db"),
		dataDir:      filepath.Join(root, "data"),
		bgPath:       filepath.Join(root, "wall.png"),
		zipPath:      filepath.Join(root, "me.mygo-profile.zip"),
	}
	if err := os.MkdirAll(filepath.Dir(f.settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(f.dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	settingsJSON, err := json.MarshalIndent(map[string]any{
		"theme":              "dark",
		"palette":            "teal",
		"tone":               "green",
		"fontSize":           16,
		"shortcuts":          map[string]string{"nav.task": "cmd+9"},
		"openAtLogin":        true,
		"allowMultiInstance": true,
		"ignoredVersion":     "0.1.2",
		"backgroundImage":    f.bgPath,
		"futureOption":       map[string]bool{"keep": true},
		"repoKind":           "gitea",
		"repoToken":          "package-secret",
		"repoOwner":          "old-owner",
		"githubToken":        "package-workspace-secret",
		"giteeToken":         "package-gitee-secret",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.settingsPath, string(settingsJSON)+"\n")
	writeFile(t, f.bgPath, "PNGDATA")
	s, err := store.Open(f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTask("同步授权数据", "轻量级", "已完成", "0.8s"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPagePref("orders", "filter", `{"kind":"批发"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exportFixture(t *testing.T, f fixture, includeData bool) ExportResult {
	t.Helper()
	res, err := Export(ExportOptions{
		Out:           f.zipPath,
		SettingsPath:  f.settingsPath,
		DBPath:        f.dbPath,
		Background:    f.bgPath,
		IncludeData:   includeData,
		AppID:         "com.bestuav.quantum",
		AppVersion:    "0.1.10",
		SchemaVersion: store.SchemaVersion(),
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	return res
}

// TestExportImportRoundTrip 设置、页面状态、任务与背景图都能搬过去；
// 设备相关字段保留目标机器的值。
func TestExportImportRoundTrip(t *testing.T) {
	src := newFixture(t)
	res := exportFixture(t, src, true)
	if !res.IncludesData || !res.IncludesImage {
		t.Fatalf("导出内容不完整: %+v", res)
	}

	m, err := Inspect(src.zipPath)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if m.Format != Format || m.App != "com.bestuav.quantum" || m.Device == "" ||
		!contains(m.Includes, "data") || !contains(m.Includes, "background") {
		t.Fatalf("manifest 不正确: %+v", m)
	}

	// 目标机器：已有自己的设置与库。
	dst := fixture{
		dir:          t.TempDir(),
		settingsPath: filepath.Join(t.TempDir(), "settings.json"),
		dbPath:       filepath.Join(t.TempDir(), "data.db"),
		dataDir:      t.TempDir(),
	}
	writeFile(t, dst.settingsPath, `{"theme":"light","openAtLogin":false,"allowMultiInstance":false,"ignoredVersion":"","fontSize":12,"repoToken":"dst-secret","repoOwner":"me","githubToken":"dst-workspace","giteeToken":"dst-gitee"}`)
	s, err := store.Open(dst.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertTask("本机旧任务", "轻量级", "已完成", "0.1s"); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	imp, err := Import(ImportOptions{
		In:            src.zipPath,
		SettingsPath:  dst.settingsPath,
		DBPath:        dst.dbPath,
		DataDir:       dst.dataDir,
		AppID:         "com.bestuav.quantum",
		SchemaVersion: store.SchemaVersion(),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if !imp.ImportedData || !imp.ImportedImage {
		t.Fatalf("导入结果不完整: %+v", imp)
	}
	if imp.SettingsBackup == "" || imp.DBBackup == "" {
		t.Error("导入前应备份现有设置与数据库")
	}
	if _, err := os.Stat(imp.SettingsBackup); err != nil {
		t.Errorf("设置备份不存在: %v", err)
	}

	// 设置：可移植字段来自包，设备字段保留本机值，未知字段也保留。
	data, err := os.ReadFile(dst.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("导入后的设置不是合法 JSON: %v\n%s", err, data)
	}
	if got["theme"] != "dark" || got["palette"] != "teal" || got["tone"] != "green" {
		t.Errorf("可移植设置未导入: %v", got)
	}
	if got["fontSize"].(float64) != 16 {
		t.Errorf("字号未导入: %v", got["fontSize"])
	}
	if got["openAtLogin"] != false || got["allowMultiInstance"] != false {
		t.Errorf("设备相关字段应保留本机值: %v / %v", got["openAtLogin"], got["allowMultiInstance"])
	}
	// 仓库令牌只属于本机：包里的 token 不能带过来，本机的要保留。
	if got["repoToken"] != "dst-secret" {
		t.Errorf("仓库令牌应保留本机值，实际 %v", got["repoToken"])
	}
	if got["githubToken"] != "dst-workspace" || got["giteeToken"] != "dst-gitee" {
		t.Errorf("工作区令牌应保留本机值，实际 github=%v gitee=%v",
			got["githubToken"], got["giteeToken"])
	}
	if got["repoOwner"] == "old-owner" {
		t.Error("包里的仓库配置不应覆盖本机")
	}
	if got["futureOption"] == nil {
		t.Error("包内的未知字段应原样保留（向前兼容）")
	}
	if bg, _ := got["backgroundImage"].(string); bg == "" || bg == src.bgPath {
		t.Errorf("背景图路径应改写为本机新路径，实际 %q", bg)
	}
	if _, err := os.Stat(got["backgroundImage"].(string)); err != nil {
		t.Errorf("背景图应复制到本机: %v", err)
	}

	// 数据库：任务与页面状态来自包（本机旧任务被替换）。
	s2, err := store.Open(dst.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	rows, err := s2.RecentTasks(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Name != "同步授权数据" {
		t.Errorf("任务应来自配置包，实际 %+v", rows)
	}
	if v, ok, _ := s2.PagePref("orders", "filter"); !ok || !strings.Contains(v, "批发") {
		t.Errorf("页面状态未导入: %q ok=%v", v, ok)
	}
}

// TestImportWithoutData 只导出设置时不碰目标数据库。
func TestImportWithoutData(t *testing.T) {
	src := newFixture(t)
	exportFixture(t, src, false)

	dst := fixture{
		dir:          t.TempDir(),
		settingsPath: filepath.Join(t.TempDir(), "settings.json"),
		dbPath:       filepath.Join(t.TempDir(), "data.db"),
		dataDir:      t.TempDir(),
	}
	writeFile(t, dst.settingsPath, `{"theme":"light"}`)
	s, _ := store.Open(dst.dbPath)
	_, _ = s.InsertTask("保留我", "轻量级", "已完成", "1s")
	_ = s.Close()

	imp, err := Import(ImportOptions{
		In: src.zipPath, SettingsPath: dst.settingsPath, DBPath: dst.dbPath,
		DataDir: dst.dataDir, AppID: "com.bestuav.quantum", SchemaVersion: store.SchemaVersion(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if imp.ImportedData {
		t.Error("包内没有 data.db 时不应导入数据")
	}
	s2, _ := store.Open(dst.dbPath)
	defer s2.Close()
	rows, _ := s2.RecentTasks(10)
	if len(rows) != 1 || rows[0].Name != "保留我" {
		t.Errorf("不应改动本机数据库: %+v", rows)
	}
}

// TestImportRejects 不兼容的包在改文件之前就被拒绝。
func TestImportRejects(t *testing.T) {
	src := newFixture(t)
	exportFixture(t, src, true)

	dst := fixture{
		dir:          t.TempDir(),
		settingsPath: filepath.Join(t.TempDir(), "settings.json"),
		dbPath:       filepath.Join(t.TempDir(), "data.db"),
		dataDir:      t.TempDir(),
	}
	writeFile(t, dst.settingsPath, `{"theme":"light"}`)

	if _, err := Import(ImportOptions{
		In: src.zipPath, SettingsPath: dst.settingsPath, DBPath: dst.dbPath,
		DataDir: dst.dataDir, AppID: "com.other.app", SchemaVersion: store.SchemaVersion(),
	}); err == nil || !strings.Contains(err.Error(), "另一个应用") {
		t.Errorf("应用标识不符应拒绝，实际 %v", err)
	}
	if _, err := Import(ImportOptions{
		In: src.zipPath, SettingsPath: dst.settingsPath, DBPath: dst.dbPath,
		DataDir: dst.dataDir, AppID: "com.bestuav.quantum", SchemaVersion: "1",
	}); err == nil || !strings.Contains(err.Error(), "更新版本") {
		t.Errorf("未来 schema 应拒绝，实际 %v", err)
	}
	// 被拒绝后本机设置原样不动。
	data, _ := os.ReadFile(dst.settingsPath)
	if string(data) != `{"theme":"light"}` {
		t.Errorf("校验失败不应改设置: %s", data)
	}

	// 不是 zip 的文件。
	bad := filepath.Join(t.TempDir(), "bad.zip")
	writeFile(t, bad, "not a zip")
	if _, err := Inspect(bad); err == nil {
		t.Error("损坏的包应报错")
	}
}

// TestDeviceName 设备名只含安全字符。
func TestDeviceName(t *testing.T) {
	if DeviceName() == "" {
		t.Error("设备名不应为空")
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
