package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestClient 起一个假 GitHub，并把 Client 指向它。
// HTTP 用 srv.Client()：沿用 httptest 的连接设置，也避免测试里遗留真实连接。
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{Token: "test-token", BaseURL: srv.URL, HTTP: srv.Client()}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("写测试响应失败: %v", err)
	}
}

func TestRepoOK(t *testing.T) {
	const body = `{
		"full_name": "bestuav/atlas",
		"owner": {"login": "bestuav"},
		"name": "atlas",
		"description": "无人机图集",
		"html_url": "https://github.com/bestuav/atlas",
		"default_branch": "main",
		"language": "Go",
		"private": true,
		"archived": false,
		"stargazers_count": 12,
		"forks_count": 3,
		"open_issues_count": 4,
		"pushed_at": "2026-11-04T07:23:17Z",
		"updated_at": "2026-11-03T01:02:03Z"
	}`

	var gotPath, gotUA, gotAccept, gotVersion, gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})

	info, err := c.Repo(context.Background(), "bestuav", "atlas")
	if err != nil {
		t.Fatalf("Repo 失败: %v", err)
	}
	if gotPath != "/repos/bestuav/atlas" {
		t.Errorf("请求路径 = %q，期望 /repos/bestuav/atlas", gotPath)
	}
	if gotUA != "bestuav-atlas" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotAccept != "application/vnd.github+json" {
		t.Errorf("Accept = %q", gotAccept)
	}
	if gotVersion != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", gotVersion)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q，期望 Bearer 令牌", gotAuth)
	}

	if info.FullName != "bestuav/atlas" {
		t.Errorf("FullName = %q", info.FullName)
	}
	if info.Owner != "bestuav" || info.Name != "atlas" {
		t.Errorf("Owner/Name = %q/%q", info.Owner, info.Name)
	}
	if info.Description != "无人机图集" || info.Language != "Go" {
		t.Errorf("Description/Language = %q/%q", info.Description, info.Language)
	}
	if info.HTMLURL != "https://github.com/bestuav/atlas" || info.DefaultBranch != "main" {
		t.Errorf("HTMLURL/DefaultBranch = %q/%q", info.HTMLURL, info.DefaultBranch)
	}
	if !info.Private || info.Archived {
		t.Errorf("Private/Archived = %v/%v", info.Private, info.Archived)
	}
	if info.Stars != 12 || info.Forks != 3 || info.OpenIssues != 4 {
		t.Errorf("Stars/Forks/OpenIssues = %d/%d/%d", info.Stars, info.Forks, info.OpenIssues)
	}

	// 时间：GitHub 给 UTC，这里应转成本地时区但表示同一个瞬间。
	want := time.Date(2026, 11, 4, 7, 23, 17, 0, time.UTC)
	if !info.PushedAt.Equal(want) {
		t.Errorf("PushedAt = %v，期望 %v", info.PushedAt, want)
	}
	if info.UpdatedAt.IsZero() {
		t.Error("UpdatedAt 为零值，期望解析成功")
	}
	if info.PushedAt.IsZero() {
		t.Error("PushedAt 为零值，期望解析成功")
	}
}

func TestRepoWithoutToken(t *testing.T) {
	var gotAuth string
	var got bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 用 Values 而不是 Get：要区分「没发这个头」与「发了空值」。
		if vals := r.Header.Values("Authorization"); len(vals) > 0 {
			gotAuth, got = vals[0], true
		}
		_, _ = w.Write([]byte(`{"full_name":"o/r"}`))
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}

	if _, err := c.Repo(context.Background(), "o", "r"); err != nil {
		t.Fatalf("匿名读公开仓库失败: %v", err)
	}
	if got {
		t.Errorf("没有令牌时不该发 Authorization，实际 = %q", gotAuth)
	}
}

func TestRepoValidation(t *testing.T) {
	c := New("")
	if _, err := c.Repo(context.Background(), "", "r"); err == nil {
		t.Error("owner 为空应报错")
	}
	if _, err := c.Repo(context.Background(), "o", "  "); err == nil {
		t.Error("repo 为空应报错")
	}
}

func TestRepoNotFound(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	_, err := c.Repo(context.Background(), "o", "missing")
	if err == nil {
		t.Fatal("404 应返回错误")
	}
	if !strings.Contains(err.Error(), "仓库不存在或无权限访问") {
		t.Errorf("404 文案 = %q，期望含「仓库不存在或无权限访问」", err)
	}
}

func TestRepoRateLimited(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 203.0.113.7."}`))
	})
	_, err := c.Repo(context.Background(), "o", "r")
	if err == nil {
		t.Fatal("403 限流应返回错误")
	}
	if !strings.Contains(err.Error(), "限流") || !strings.Contains(err.Error(), "配置令牌") {
		t.Errorf("403 限流文案 = %q", err)
	}
}

func TestRepoUnauthorized(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	})
	_, err := c.Repo(context.Background(), "o", "r")
	if err == nil || !strings.Contains(err.Error(), "令牌无效或已过期") {
		t.Fatalf("401 文案 = %v，期望含「令牌无效或已过期」", err)
	}
}

// TestListReposPaging 验证两页拼接：第一页满 100 条会继续翻页，第二页 2 条即停。
func TestListReposPaging(t *testing.T) {
	var mu sync.Mutex
	var pages []string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/users/o/repos" {
			t.Errorf("请求路径 = %q，期望 /users/o/repos", got)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "100" {
			t.Errorf("per_page = %q，期望 100", q.Get("per_page"))
		}
		page := q.Get("page")
		mu.Lock()
		pages = append(pages, page)
		mu.Unlock()

		var items []map[string]any
		switch page {
		case "1":
			for i := 0; i < perPage; i++ {
				items = append(items, map[string]any{
					"full_name": fmt.Sprintf("o/r%03d", i),
					"name":      fmt.Sprintf("r%03d", i),
					"owner":     map[string]any{"login": "o"},
				})
			}
		case "2":
			items = append(items, map[string]any{"full_name": "o/last1"}, map[string]any{"full_name": "o/last2"})
		default:
			t.Errorf("不该请求第 %s 页（第二页已不满 100 条）", page)
		}
		writeJSON(t, w, items)
	})

	repos, err := c.ListRepos(context.Background(), "o")
	if err != nil {
		t.Fatalf("ListRepos 失败: %v", err)
	}
	if len(repos) != perPage+2 {
		t.Fatalf("仓库数 = %d，期望 %d（两页拼接）", len(repos), perPage+2)
	}
	if repos[0].FullName != "o/r000" || repos[perPage-1].FullName != "o/r099" {
		t.Errorf("第一页顺序不对：%q ... %q", repos[0].FullName, repos[perPage-1].FullName)
	}
	if repos[perPage].FullName != "o/last1" || repos[perPage+1].FullName != "o/last2" {
		t.Errorf("第二页顺序不对：%q %q", repos[perPage].FullName, repos[perPage+1].FullName)
	}
	mu.Lock()
	gotPages := strings.Join(pages, ",")
	mu.Unlock()
	if gotPages != "1,2" {
		t.Errorf("请求页码序列 = %q，期望 1,2", gotPages)
	}
}

// TestListReposUser 验证 owner 为空时走 /user/repos（含私有，需要令牌）。
func TestListReposUser(t *testing.T) {
	var gotPath, gotAuth string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		writeJSON(t, w, []map[string]any{{"full_name": "me/private", "private": true}})
	})
	repos, err := c.ListRepos(context.Background(), "")
	if err != nil {
		t.Fatalf("ListRepos(/user/repos) 失败: %v", err)
	}
	if gotPath != "/user/repos" {
		t.Errorf("请求路径 = %q，期望 /user/repos", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(repos) != 1 || !repos[0].Private {
		t.Fatalf("返回 = %+v，期望一条私有仓库", repos)
	}
}

func TestParseRemote(t *testing.T) {
	cases := []struct {
		name, url   string
		owner, repo string
		ok          bool
	}{
		{name: "https 带 .git", url: "https://github.com/o/r.git", owner: "o", repo: "r", ok: true},
		{name: "scp 风格", url: "git@github.com:o/r.git", owner: "o", repo: "r", ok: true},
		{name: "ssh 协议", url: "ssh://git@github.com/o/r.git", owner: "o", repo: "r", ok: true},
		{name: "gitee 无后缀", url: "https://gitee.com/o/r", owner: "o", repo: "r", ok: true},
		{name: "尾部斜杠", url: "https://github.com/o/r/", owner: "o", repo: "r", ok: true},
		{name: "带端口", url: "ssh://git@gitea.example.com:2222/o/r.git", owner: "o", repo: "r", ok: true},
		{name: "空串", url: "", ok: false},
		{name: "不是地址", url: "not-a-url", ok: false},
		{name: "本地路径", url: `C:/work/r`, ok: false},
		{name: "只有 owner", url: "https://github.com/o", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, ok := ParseRemote(tc.url)
			if ok != tc.ok {
				t.Fatalf("ParseRemote(%q) ok = %v，期望 %v（得到 %q/%q）", tc.url, ok, tc.ok, owner, repo)
			}
			if ok && (owner != tc.owner || repo != tc.repo) {
				t.Fatalf("ParseRemote(%q) = %q/%q，期望 %q/%q", tc.url, owner, repo, tc.owner, tc.repo)
			}
		})
	}
}

func TestSplitFull(t *testing.T) {
	cases := []struct {
		in, owner, repo string
		ok              bool
	}{
		{in: "o/r", owner: "o", repo: "r", ok: true},
		{in: " o/r.git ", owner: "o", repo: "r", ok: true},
		{in: "o/r/", owner: "o", repo: "r", ok: true},
		{in: "", ok: false},
		{in: "o", ok: false},
		{in: "a/b/c", ok: false},
		{in: "o/", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			owner, repo, ok := SplitFull(tc.in)
			if ok != tc.ok {
				t.Fatalf("SplitFull(%q) ok = %v，期望 %v（得到 %q/%q）", tc.in, ok, tc.ok, owner, repo)
			}
			if ok && (owner != tc.owner || repo != tc.repo) {
				t.Fatalf("SplitFull(%q) = %q/%q，期望 %q/%q", tc.in, owner, repo, tc.owner, tc.repo)
			}
		})
	}
}
