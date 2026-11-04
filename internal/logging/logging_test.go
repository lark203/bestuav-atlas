package logging

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readLines 读取日志文件并按行解析成 JSON 对象。
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取日志文件: %v", err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("日志行不是合法 JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

// TestSetupWritesJSONL 验证：按天命名的文件、每行一条合法 JSON、级别与错误文本完整。
func TestSetupWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	closeFn, err := Setup(Options{Dir: dir, AppName: "unit", AppVersion: "dev"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = closeFn() }()

	Event(slog.LevelInfo, "unit", "hello", "k", "v")
	Errv("unit", "boom", errors.New("disk full"))

	path := filepath.Join(dir, filePrefix+time.Now().Format(dateLayout)+fileSuffix)
	lines := readLines(t, path)
	if len(lines) != 2 {
		t.Fatalf("应有 2 行日志，实际 %d", len(lines))
	}
	if lines[0]["msg"] != "hello" || lines[0]["kind"] != "unit" {
		t.Errorf("第一条日志内容错误: %+v", lines[0])
	}
	if lines[0]["app"] != "unit" || lines[0]["version"] != "dev" {
		t.Errorf("应带 app/version 字段: %+v", lines[0])
	}
	if lines[1]["level"] != "ERROR" {
		t.Errorf("错误日志级别应为 ERROR，实际 %v", lines[1]["level"])
	}
	if !strings.Contains(lines[1]["msg"].(string), "boom") {
		t.Errorf("缺少错误文本: %+v", lines[1])
	}
}

// TestPruneKeepsRecentLogs 验证超过保留天数的日志被清理，未过期的保留。
func TestPruneKeepsRecentLogs(t *testing.T) {
	dir := t.TempDir()

	oldDay := time.Now().AddDate(0, 0, -10).Format(dateLayout)
	keepDay := time.Now().AddDate(0, 0, -2).Format(dateLayout)
	oldPath := filepath.Join(dir, filePrefix+oldDay+fileSuffix)
	keepPath := filepath.Join(dir, filePrefix+keepDay+fileSuffix)
	for _, p := range []string{oldPath, keepPath} {
		if err := os.WriteFile(p, []byte(`{"msg":"old"}`+"\n"), 0o644); err != nil {
			t.Fatalf("准备日志文件: %v", err)
		}
	}
	// 非本包命名的文件不应被误删。
	otherPath := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(otherPath, []byte("keep me"), 0o644); err != nil {
		t.Fatalf("准备无关文件: %v", err)
	}

	closeFn, err := Setup(Options{Dir: dir, AppName: "unit"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	_ = closeFn()

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Errorf("10 天前的日志应被清理: err=%v", err)
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Errorf("2 天前的日志应保留: err=%v", err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Errorf("非日志文件不应被删除: err=%v", err)
	}
}

// TestRecorderOnlyForErrorsAndAudit 验证入库规则：ERROR 及以上 + Audit 才投递，
// 普通 INFO 不入库（否则日志文件与库两份冗余数据）。
func TestRecorderOnlyForErrorsAndAudit(t *testing.T) {
	closeFn, err := Setup(Options{Dir: t.TempDir(), AppName: "unit"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = closeFn() }()

	var got []string
	SetRecorder(func(level, kind, msg, detail string) {
		got = append(got, level+"|"+kind+"|"+msg+"|"+detail)
	})
	defer SetRecorder(nil)

	Event(slog.LevelInfo, "unit", "普通信息")           // 不入库
	Event(slog.LevelWarn, "unit", "警告")             // 不入库
	Errv("unit", "失败", errors.New("x"), "retry", 1) // 入库
	Audit("unit", "关键操作", "count", 2)               // 入库

	if len(got) != 2 {
		t.Fatalf("应投递 2 条，实际 %d: %v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "ERROR|unit|失败|") || !strings.Contains(got[0], "err=x") {
		t.Errorf("错误事件格式错误: %q", got[0])
	}
	if !strings.HasPrefix(got[1], "INFO|unit|关键操作|") || !strings.Contains(got[1], "count=2") {
		t.Errorf("审计事件格式错误: %q", got[1])
	}
}

// TestRecoverWritesCrashAndRepanics 验证 panic 会被记成 CRASH（含堆栈）后重新抛出。
func TestRecoverWritesCrashAndRepanics(t *testing.T) {
	dir := t.TempDir()
	closeFn, err := Setup(Options{Dir: dir, AppName: "unit"})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer func() { _ = closeFn() }()

	var levels []string
	SetRecorder(func(level, _, _, _ string) { levels = append(levels, level) })
	defer SetRecorder(nil)

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Recover 必须重新抛出 panic（不改变原崩溃行为）")
			}
		}()
		defer Recover("unit-test")
		panic("数据错乱")
	}()

	if len(levels) != 1 || levels[0] != "CRASH" {
		t.Fatalf("应入库 1 条 CRASH，实际 %v", levels)
	}

	path := filepath.Join(dir, filePrefix+time.Now().Format(dateLayout)+fileSuffix)
	lines := readLines(t, path)
	if len(lines) != 1 {
		t.Fatalf("应有 1 行日志，实际 %d", len(lines))
	}
	if lines[0]["level"] != "CRASH" {
		t.Errorf("级别应为 CRASH，实际 %v", lines[0]["level"])
	}
	if stack, _ := lines[0]["stack"].(string); !strings.Contains(stack, "goroutine") {
		t.Errorf("应记录堆栈，实际 %q", stack)
	}
}

// TestSetupRejectsEmptyDir 验证未给目录时明确报错，避免悄悄把日志写到当前目录。
func TestSetupRejectsEmptyDir(t *testing.T) {
	if _, err := Setup(Options{}); err == nil {
		t.Fatal("未指定目录时应返回错误")
	}
}

// TestEnvSnapshot 验证环境快照包含排障必需的几项。
//
// 注意：快照里**故意**没有 "app"——slog 的默认 handler 已经带了 app/version，
// 再写一遍会让 JSON 日志出现重复键。
func TestEnvSnapshot(t *testing.T) {
	kv := EnvSnapshot()
	joined := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			joined[k] = kv[i+1]
		}
	}
	for _, key := range []string{"build", "go", "goos", "goarch", "cpus", "tz"} {
		if _, ok := joined[key]; !ok {
			t.Errorf("环境快照缺少字段 %q", key)
		}
	}
	if _, dup := joined["app"]; dup {
		t.Error("快照不应重复提供 app 字段（handler 已带）")
	}
	if joined["title"] == "" {
		t.Error("title 字段不应为空")
	}
}

// TestDailyWriterRotatesBySize 单文件写满要切分片，否则 --log-level=debug 常开
// 一天能写出几百 MB（既吃磁盘，也让诊断包变成巨型下载）。
func TestDailyWriterRotatesBySize(t *testing.T) {
	dir := t.TempDir()
	// 每片 32 字节：一行 11 字节，两行 22 字节装得下，第三行就必须换片。
	w := &dailyWriter{dir: dir, retention: 7, maxBytes: 32}
	defer func() { _ = w.Close() }()

	line := []byte("0123456789\n")
	const n = 6
	for i := 0; i < n; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatalf("第 %d 次写入: %v", i, err)
		}
	}

	day := time.Now().Format(dateLayout)
	first := filepath.Join(dir, fileName(day, 0))
	second := filepath.Join(dir, fileName(day, 1))
	if fileName(day, 1) != filePrefix+day+".2"+fileSuffix {
		t.Fatalf("分片命名不符合约定: %s", fileName(day, 1))
	}
	for _, p := range []string{first, second} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("应有分片文件 %s: %v", filepath.Base(p), err)
		}
	}
	// 内容总量不能丢：切分只是分文件，不是丢弃。
	var total int
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			t.Fatalf("读 %s: %v", e.Name(), rerr)
		}
		total += len(data)
	}
	if want := n * len(line); total != want {
		t.Errorf("写入总量 = %d 字节，期望 %d", total, want)
	}
}

// TestPruneRemovesOldShards 分片文件也要按天清理：早先 prune 只认
// app-YYYY-MM-DD.log，分片名解析失败会被当成「别人的文件」永久留下。
func TestPruneRemovesOldShards(t *testing.T) {
	dir := t.TempDir()
	oldDay := time.Now().AddDate(0, 0, -10).Format(dateLayout)
	keepDay := time.Now().AddDate(0, 0, -2).Format(dateLayout)

	oldBase := filepath.Join(dir, fileName(oldDay, 0))
	oldShard := filepath.Join(dir, fileName(oldDay, 1))
	keepShard := filepath.Join(dir, fileName(keepDay, 1))
	for _, p := range []string{oldBase, oldShard, keepShard} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatalf("准备文件: %v", err)
		}
	}

	w := &dailyWriter{dir: dir, retention: 7, maxBytes: 64}
	if err := w.prune(time.Now()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	for _, p := range []string{oldBase, oldShard} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("10 天前的日志（含分片）应被清理: %s err=%v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(keepShard); err != nil {
		t.Errorf("2 天前的分片应保留: %v", err)
	}
}
