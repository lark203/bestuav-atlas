package repo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeHost 一个最小的「仓库文件 API」：够验证三种平台的请求与响应处理。
type fakeHost struct {
	mu        sync.Mutex
	srv       *httptest.Server
	stored    []byte
	sha       string
	lastAuth  string
	lastPath  string
	lastBody  map[string]any
	lastQuery string
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	f := &fakeHost{sha: ""}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.lastAuth = r.Header.Get("Authorization")
		f.mu.Unlock()
		_, _ = w.Write([]byte(`{"full_name":"o/r"}`))
	})
	mux.HandleFunc("/repos/o/r/contents/profiles/x.zip", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastAuth = r.Header.Get("Authorization")
		f.lastQuery = r.URL.RawQuery
		switch r.Method {
		case http.MethodGet:
			if f.stored == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content":  base64.StdEncoding.EncodeToString(f.stored),
				"encoding": "base64",
				"sha":      f.sha,
			})
		case http.MethodPut:
			f.lastPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
			if f.stored != nil && f.lastBody["sha"] != f.sha {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"sha does not match"}`))
				return
			}
			content, _ := f.lastBody["content"].(string)
			f.stored, _ = base64.StdEncoding.DecodeString(content)
			f.sha = "sha-" + f.lastBody["message"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"commit":  map[string]any{"sha": "c1"},
				"content": map[string]any{"html_url": "https://example.test/o/r/blob/main/profiles/x.zip"},
			})
		}
	})
	mux.HandleFunc("/raw/profiles/x.zip", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = w.Write(f.stored)
	})
	// Gitea 的客户端会在 BaseURL 后加 /api/v1；这里按前缀剥掉，兼容三种平台。
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v1")
			r.URL.RawPath = ""
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func TestUploadDownloadPerKind(t *testing.T) {
	for _, kind := range Kinds {
		t.Run(string(kind), func(t *testing.T) {
			h := newFakeHost(t)
			c, err := New(Config{
				Kind: kind, BaseURL: h.srv.URL, Token: "tok",
				Owner: "o", Repo: "r", Branch: "main", Path: "/profiles/x.zip",
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			ctx := context.Background()
			if err := c.Test(ctx); err != nil {
				t.Fatalf("Test: %v", err)
			}
			// 第一次上传：文件不存在，不带 sha。
			res, err := c.Upload(ctx, []byte("hello"), "sync 1")
			if err != nil {
				t.Fatalf("Upload: %v", err)
			}
			if res.Commit != "c1" || res.URL == "" {
				t.Errorf("上传结果不完整: %+v", res)
			}
			h.mu.Lock()
			if h.lastBody["sha"] != nil {
				t.Errorf("首次上传不应带 sha: %v", h.lastBody["sha"])
			}
			h.mu.Unlock()

			// 第二次上传：带上第一次的 sha。
			if _, err := c.Upload(ctx, []byte("hello2"), "sync 2"); err != nil {
				t.Fatalf("Upload 2: %v", err)
			}
			h.mu.Lock()
			if h.lastBody["sha"] == nil {
				t.Error("更新已有文件应带 sha")
			}
			h.mu.Unlock()

			data, err := c.Download(ctx)
			if err != nil {
				t.Fatalf("Download: %v", err)
			}
			if string(data) != "hello2" {
				t.Errorf("下载内容 = %q", data)
			}

			// 平台认证方式。
			h.mu.Lock()
			auth, q := h.lastAuth, h.lastQuery
			h.mu.Unlock()
			switch kind {
			case GitHub:
				if auth != "Bearer tok" {
					t.Errorf("GitHub 认证头 = %q", auth)
				}
			case Gitea:
				if auth != "token tok" {
					t.Errorf("Gitea 认证头 = %q", auth)
				}
			case Gitee:
				if !strings.Contains(q, "access_token=tok") {
					t.Errorf("Gitee 应使用 access_token 查询参数，实际 %q", q)
				}
			}
		})
	}
}

func TestDownloadMissing(t *testing.T) {
	h := newFakeHost(t)
	c, err := New(Config{Kind: GitHub, BaseURL: h.srv.URL, Token: "t", Owner: "o", Repo: "r", Path: "profiles/x.zip"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background()); err == nil || !strings.Contains(err.Error(), "还没有") {
		t.Errorf("缺少文件应给出提示，实际 %v", err)
	}
}

func TestErrorSurfacesMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()
	c, err := New(Config{Kind: GitHub, BaseURL: srv.URL, Token: "t", Owner: "o", Repo: "r", Path: "x"})
	if err != nil {
		t.Fatal(err)
	}
	err = c.Test(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("应透出平台错误信息，实际 %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	cases := []Config{
		{Kind: GitHub, Owner: "", Repo: "r", Token: "t", Path: "x"},
		{Kind: GitHub, Owner: "o", Repo: "r", Token: "", Path: "x"},
		{Kind: GitHub, Owner: "o", Repo: "r", Token: "t", Path: ""},
		{Kind: Gitea, Owner: "o", Repo: "r", Token: "t", Path: "x"}, // 缺实例地址
		{Kind: "svn", Owner: "o", Repo: "r", Token: "t", Path: "x"},
	}
	for i, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("第 %d 个配置应报错: %+v", i, cfg)
		}
	}
}
