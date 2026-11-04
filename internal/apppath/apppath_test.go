package apppath

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// isolate 把配置目录与本机数据目录都指到临时目录（返回两者的根）。
func isolate(t *testing.T) (configRoot, dataRoot string) {
	t.Helper()
	configRoot = t.TempDir()
	dataRoot = t.TempDir()
	// 配置目录：Windows 读 AppData，Linux 读 XDG_CONFIG_HOME，macOS 读 HOME/Library。
	t.Setenv("AppData", configRoot)
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	// 本机数据目录：Windows 读 LOCALAPPDATA，Linux 读 XDG_DATA_HOME，macOS 读 HOME。
	t.Setenv("LOCALAPPDATA", dataRoot)
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("HOME", dataRoot)
	t.Setenv("USERPROFILE", dataRoot)
	return configRoot, dataRoot
}

// TestDataDirSeparateFromConfig 数据库/日志不该落在（可能漫游的）配置目录里。
func TestDataDirSeparateFromConfig(t *testing.T) {
	cfg, data := isolate(t)
	cd, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dd, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if cd == dd {
		t.Fatalf("配置目录与数据目录相同: %s", cd)
	}
	if !within(cd, cfg) {
		t.Errorf("配置目录 %q 不在 %q 下", cd, cfg)
	}
	if !within(dd, data) {
		t.Errorf("数据目录 %q 不在 %q 下", dd, data)
	}
	p, err := DBPath()
	if err != nil {
		t.Fatal(err)
	}
	if !within(p, data) {
		t.Errorf("数据库路径 %q 应在数据目录 %q 下", p, data)
	}
	l, err := LogDir()
	if err != nil {
		t.Fatal(err)
	}
	if !within(l, data) {
		t.Errorf("日志目录 %q 应在数据目录 %q 下", l, data)
	}
}

// TestMigrateLegacy 旧位置（配置目录）里的库与日志要搬到新位置。
func TestMigrateLegacy(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS 上配置目录与数据目录同源，没有迁移场景")
	}
	cfg, data := isolate(t)
	oldDir := filepath.Join(cfg, "bestuav-atlas")
	if err := os.MkdirAll(filepath.Join(oldDir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "data.db"), []byte("fake-db"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "logs", "app-2026-01-01.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved := MigrateLegacy()
	if len(moved) < 2 {
		t.Fatalf("应迁移数据库与日志，实际 %v", moved)
	}
	newDir := filepath.Join(data, "bestuav-atlas")
	if b, err := os.ReadFile(filepath.Join(newDir, "data.db")); err != nil || string(b) != "fake-db" {
		t.Fatalf("数据库未迁移: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "logs", "app-2026-01-01.log")); err != nil {
		t.Fatalf("日志未迁移: %v", err)
	}
	// 旧文件不应残留（rename 成功时）。
	if _, err := os.Stat(filepath.Join(oldDir, "data.db")); err == nil && runtime.GOOS != "windows" {
		t.Log("旧数据库仍在（跨盘复制失败时才允许）")
	}

	// 再跑一次不应覆盖新位置的文件。
	if err := os.WriteFile(filepath.Join(newDir, "data.db"), []byte("new-db"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = MigrateLegacy()
	if b, _ := os.ReadFile(filepath.Join(newDir, "data.db")); string(b) != "new-db" {
		t.Fatal("迁移不应覆盖已存在的数据库")
	}
}

func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) && rel != ""
}

// TestMigrateKeepsWALTrioTogether 库文件三件套必须一起搬。
//
// 只搬 data.db 而把 -wal 留在旧目录，等于丢掉 WAL 里已提交的事务：用户看到数据
// 凭空回退，且没有任何报错。这里断言三个文件都在新位置、旧位置一个不剩。
func TestMigrateKeepsWALTrioTogether(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS 上配置目录与数据目录同源，没有迁移场景")
	}
	cfg, data := isolate(t)
	oldDir := filepath.Join(cfg, "bestuav-atlas")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"data.db":     "main",
		"data.db-wal": "wal",
		"data.db-shm": "shm",
	}
	for name, body := range want {
		if err := os.WriteFile(filepath.Join(oldDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	moved := MigrateLegacy()
	if len(moved) < 3 {
		t.Fatalf("三件套都应被迁移，实际 %v", moved)
	}
	newDir := filepath.Join(data, "bestuav-atlas")
	for name, body := range want {
		got, err := os.ReadFile(filepath.Join(newDir, name))
		if err != nil {
			t.Fatalf("%s 未迁移: %v", name, err)
		}
		if string(got) != body {
			t.Errorf("%s 内容 = %q，期望 %q", name, got, body)
		}
		if _, err := os.Stat(filepath.Join(oldDir, name)); !os.IsNotExist(err) {
			t.Errorf("%s 应已离开旧目录", name)
		}
	}
}

// TestMigrateRollsBackOnFailure 中途失败必须整体回滚：宁可都不搬（下次还能再试），
// 也不能留下「主库在新位置、WAL 在旧位置」这种半套状态。
func TestMigrateRollsBackOnFailure(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS 上配置目录与数据目录同源，没有迁移场景")
	}
	cfg, data := isolate(t)
	oldDir := filepath.Join(cfg, "bestuav-atlas")
	newDir := filepath.Join(data, "bestuav-atlas")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"data.db", "data.db-wal"} {
		if err := os.WriteFile(filepath.Join(oldDir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 用「同名目录」占住 -wal 的目标路径：搬 data.db 会成功，搬 -wal 必然失败，
	// 从而触发回滚。
	if err := os.MkdirAll(filepath.Join(newDir, "data.db-wal"), 0o755); err != nil {
		t.Fatal(err)
	}

	_ = MigrateLegacy()

	if _, err := os.Stat(filepath.Join(oldDir, "data.db")); err != nil {
		t.Errorf("回滚后主库应留在旧目录: %v", err)
	}
	if _, err := os.Stat(filepath.Join(newDir, "data.db")); !os.IsNotExist(err) {
		t.Error("回滚后新目录不该留有主库（否则下次启动会读到不完整的库）")
	}
}
