package readme

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// writeFile 在 dir 下造一个测试文件；内容一律按 UTF-8 写。
func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("写测试文件 %s 失败: %v", name, err)
	}
}

// TestReadLocalMarkdown 覆盖最常见的情况：README.md，含中文与 Markdown 表格。
func TestReadLocalMarkdown(t *testing.T) {
	dir := t.TempDir()
	const body = "# 无人机图集\n\n一个本地仓库的说明。\n\n| 字段 | 含义 |\n| --- | --- |\n| 机型 | 多旋翼 |\n| 用途 | 巡检 |\n"
	writeFile(t, dir, "README.md", body)

	name, text, err := ReadLocal(dir)
	if err != nil {
		t.Fatalf("ReadLocal 失败: %v", err)
	}
	if name != "README.md" {
		t.Errorf("文件名 = %q，期望 README.md", name)
	}
	if text != body {
		t.Errorf("正文 = %q，期望与写入内容一致（含中文与表格）", text)
	}
}

// TestReadLocalLowercase 覆盖只有小写 readme.md 的情况：匹配要大小写不敏感。
func TestReadLocalLowercase(t *testing.T) {
	dir := t.TempDir()
	const body = "小写文件名的说明\n"
	writeFile(t, dir, "readme.md", body)

	name, text, err := ReadLocal(dir)
	if err != nil {
		t.Fatalf("ReadLocal 失败: %v", err)
	}
	if name != "readme.md" {
		t.Errorf("文件名 = %q，期望 readme.md", name)
	}
	if text != body {
		t.Errorf("正文 = %q", text)
	}
}

// TestReadLocalNoExtension 覆盖无扩展名的 README（最后一个候选名）。
func TestReadLocalNoExtension(t *testing.T) {
	dir := t.TempDir()
	const body = "没有扩展名的 README\n"
	writeFile(t, dir, "README", body)

	name, text, err := ReadLocal(dir)
	if err != nil {
		t.Fatalf("ReadLocal 失败: %v", err)
	}
	if name != "README" {
		t.Errorf("文件名 = %q，期望 README", name)
	}
	if text != body {
		t.Errorf("正文 = %q", text)
	}
}

// TestReadLocalNotFound 覆盖空目录：必须能用 errors.Is 判断到 ErrNotFound。
func TestReadLocalNotFound(t *testing.T) {
	dir := t.TempDir() // 不写任何文件

	name, text, err := ReadLocal(dir)
	if err == nil {
		t.Fatal("空目录应返回错误")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("错误 = %v，期望包装 ErrNotFound", err)
	}
	if name != "" || text != "" {
		t.Errorf("失败时 name/text = %q/%q，期望都为空", name, text)
	}
}

// TestReadLocalTruncatesLargeFile 覆盖超过 512 KB 的截断：不报错、保留开头、
// 末尾有提示，且不会把汉字切成半个。
func TestReadLocalTruncatesLargeFile(t *testing.T) {
	dir := t.TempDir()
	// 每行 19 字节（6 个汉字 + 换行），200000 行约 3.8 MB，足够越过上限。
	body := strings.Repeat("无人大图集。\n", 200000)
	writeFile(t, dir, "README.md", body)

	name, text, err := ReadLocal(dir)
	if err != nil {
		t.Fatalf("大文件不应报错: %v", err)
	}
	if name != "README.md" {
		t.Errorf("文件名 = %q", name)
	}
	if !strings.Contains(text, "截断") {
		t.Errorf("截断后的正文应含「截断」提示，实际开头 = %q", text[:min(len(text), 60)])
	}
	if len(text) <= maxSize {
		t.Errorf("正文长度 = %d，期望大于 %d（正文被截断但保留 512 KB）", len(text), maxSize)
	}
	if len(text) > maxSize+len(truncateNotice) {
		t.Errorf("正文长度 = %d，超出「512 KB + 提示」的上限", len(text))
	}
	if !utf8.ValidString(text) {
		t.Error("截断后不是合法 UTF-8（可能切出了半个汉字）")
	}
	kept := strings.TrimSuffix(text, truncateNotice)
	if !strings.HasPrefix(body, kept) {
		t.Error("保留部分不是原文件的前缀")
	}
}

// TestReadLocalNotADirectory 覆盖「dir 不是目录 / 为空」：同样包 ErrNotFound。
func TestReadLocalNotADirectory(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README.md", "# 说明\n")

	for _, bad := range []string{"", filepath.Join(dir, "README.md")} {
		name, text, err := ReadLocal(bad)
		if err == nil {
			t.Fatalf("ReadLocal(%q) 应返回错误", bad)
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("ReadLocal(%q) 的错误 = %v，期望包装 ErrNotFound", bad, err)
		}
		if name != "" || text != "" {
			t.Errorf("失败时 name/text = %q/%q，期望都为空", name, text)
		}
	}
}

// TestReadLocalPriority 同一目录里有多个候选名时，按约定顺序取第一个：
// README.md 优先于 README（无扩展名）。
func TestReadLocalPriority(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "README", "无扩展名的正文")
	writeFile(t, dir, "README.md", "Markdown 的正文")

	name, text, err := ReadLocal(dir)
	if err != nil {
		t.Fatalf("ReadLocal 失败: %v", err)
	}
	if name != "README.md" || text != "Markdown 的正文" {
		t.Errorf("取到 %q（%q），期望 README.md", name, text)
	}
}
