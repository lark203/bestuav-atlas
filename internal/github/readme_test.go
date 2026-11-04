package github

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// wrapBase64 按 GitHub 的做法每 60 个字符插一个换行，验证解码前会去掉换行。
func wrapBase64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 60 {
		b.WriteString(enc[:60])
		b.WriteByte('\n')
		enc = enc[60:]
	}
	b.WriteString(enc)
	return b.String()
}

// TestReadmeOK 正常路径：路径正确、带认证头、去掉折行后 base64 解码成功。
func TestReadmeOK(t *testing.T) {
	const body = "# 无人机图集\n\n| 字段 | 含义 |\n| --- | --- |\n| 机型 | 多旋翼 |\n"
	var gotPath, gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		writeJSON(t, w, map[string]any{
			"name":     "README.md",
			"path":     "README.md",
			"encoding": "base64",
			"content":  wrapBase64(body),
			"size":     len(body),
		})
	})

	text, err := c.Readme(context.Background(), "bestuav", "atlas")
	if err != nil {
		t.Fatalf("Readme 失败: %v", err)
	}
	if gotPath != "/repos/bestuav/atlas/readme" {
		t.Errorf("请求路径 = %q，期望 /repos/bestuav/atlas/readme", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q，期望复用 Client 的令牌", gotAuth)
	}
	if text != body {
		t.Errorf("正文 = %q，期望与原文一致（含中文与表格）", text)
	}
}

// TestReadmeNotFound 404：文案必须点明「没有 README」，不能沿用「仓库不存在」。
func TestReadmeNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	text, err := c.Readme(context.Background(), "o", "empty")
	if err == nil {
		t.Fatal("404 应返回错误")
	}
	if text != "" {
		t.Errorf("失败时正文 = %q，期望空串", text)
	}
	if !strings.Contains(err.Error(), "没有 README") {
		t.Errorf("404 文案 = %q，期望含「没有 README」", err)
	}
}

// TestReadmeNonBase64 降级路径之一：GitHub 把文件判为二进制时 encoding=none、
// content 为空。约定返回 ("", error)，让调用方明确知道没有可展示的文本。
func TestReadmeNonBase64(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name":     "README.md",
			"encoding": "none",
			"content":  "",
		})
	})
	text, err := c.Readme(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("encoding=none 应返回错误")
	}
	if text != "" {
		t.Errorf("降级时正文 = %q，期望空串", text)
	}
	if !strings.Contains(err.Error(), "不是文本") {
		t.Errorf("降级错误 = %q，期望说明不是文本", err)
	}
}

// TestReadmeEmpty base64 但正文为空：与上面的降级约定一致，返回空串 + 错误。
func TestReadmeEmpty(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name":     "README.md",
			"encoding": "base64",
			"content":  "",
		})
	})
	text, err := c.Readme(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("空正文应返回错误")
	}
	if text != "" {
		t.Errorf("降级时正文 = %q，期望空串", text)
	}
}

// TestReadmeRateLimited 403 限流：沿用 statusError 的文案，不在这里另写一套。
func TestReadmeRateLimited(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 203.0.113.7."}`))
	})
	_, err := c.Readme(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("403 限流应返回错误")
	}
	if !strings.Contains(err.Error(), "限流") || !strings.Contains(err.Error(), "配置令牌") {
		t.Errorf("403 文案 = %q，期望沿用现有限流提示", err)
	}
}

// TestReadmeTruncated 超过 512 KB 时截断并追加提示，且保留部分是合法 UTF-8。
func TestReadmeTruncated(t *testing.T) {
	body := strings.Repeat("航拍影像。\n", 100000) // 每个汉字 3 字节，约 1.5 MB
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"name":     "README.md",
			"encoding": "base64",
			"content":  wrapBase64(body),
		})
	})
	text, err := c.Readme(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("大 README 不应报错: %v", err)
	}
	if !strings.Contains(text, "截断") {
		t.Errorf("截断后应含「截断」提示，实际开头 = %q", text[:min(len(text), 60)])
	}
	if len(text) <= readmeMaxSize {
		t.Errorf("正文长度 = %d，期望大于 %d", len(text), readmeMaxSize)
	}
	if len(text) > readmeMaxSize+len(readmeTruncateNotice) {
		t.Errorf("正文长度 = %d，超出「512 KB + 提示」的上限", len(text))
	}
	if !utf8.ValidString(text) {
		t.Error("截断后不是合法 UTF-8（可能切出了半个汉字）")
	}
	kept := strings.TrimSuffix(text, readmeTruncateNotice)
	if !strings.HasPrefix(body, kept) {
		t.Error("保留部分不是原正文的前缀")
	}
}

// TestReadmeValidation owner/repo 为空时在本地就失败，不发请求。
func TestReadmeValidation(t *testing.T) {
	c := New("")
	if _, err := c.Readme(context.Background(), "", "r"); err == nil {
		t.Error("owner 为空应报错")
	}
	if _, err := c.Readme(context.Background(), "o", "  "); err == nil {
		t.Error("repo 为空应报错")
	}
}
