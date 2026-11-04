package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func newForgeTestClient(t *testing.T, p Platform, token string, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(p, token)
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	return c
}

func writeForgeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("写测试响应失败: %v", err)
	}
}

func wrapForgeBase64(s string) string {
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

func TestDetect(t *testing.T) {
	platforms := []struct {
		name  string
		host  string
		p     Provider
		forms []string
	}{
		{"github", "github.com", GitHub, []string{
			"https://github.com/o/r.git",
			"git@github.com:o/r.git",
			"ssh://git@github.com/o/r.git",
			"github.com/o/r",
		}},
		{"gitee", "gitee.com", Gitee, []string{
			"https://gitee.com/o/r.git",
			"git@gitee.com:o/r.git",
			"ssh://git@gitee.com/o/r.git",
			"gitee.com/o/r",
		}},
		{"atomgit", "atomgit.com", AtomGit, []string{
			"https://atomgit.com/o/r.git",
			"git@atomgit.com:o/r.git",
			"ssh://git@atomgit.com/o/r.git",
			"atomgit.com/o/r",
		}},
		{"gitea", "gitea.example.com", Gitea, []string{
			"https://gitea.example.com/o/r.git",
			"git@gitea.example.com:o/r.git",
			"ssh://git@gitea.example.com/o/r.git",
			"gitea.example.com/o/r",
		}},
	}
	for _, tc := range platforms {
		for _, raw := range tc.forms {
			t.Run(tc.name+" "+raw, func(t *testing.T) {
				p, owner, repo, ok := Detect(raw)
				if !ok {
					t.Fatalf("Detect(%q) ok = false", raw)
				}
				if p.Provider != tc.p || p.Host != tc.host {
					t.Errorf("Detect(%q) 平台 = %+v，期望 %s/%s", raw, p, tc.p, tc.host)
				}
				if owner != "o" || repo != "r" {
					t.Errorf("Detect(%q) = %q/%q", raw, owner, repo)
				}
			})
		}
	}

	for _, raw := range []string{"", "not-a-url", "https://github.com/o", "https://github.com", "C:/work/r"} {
		if _, _, _, ok := Detect(raw); ok {
			t.Errorf("Detect(%q) 应失败", raw)
		}
	}

	// 其它带点的 host 一律当自建 Gitea。
	p, owner, repo, ok := Detect("https://git.example.org/team/project")
	if !ok || p.Provider != Gitea || p.Host != "git.example.org" || owner != "team" || repo != "project" {
		t.Errorf("未知带点 host = %+v %q/%q %v", p, owner, repo, ok)
	}

	// SSH 端口只用于 SSH；Host 要用于 https 的网页/API，不能把 2222 带进去。
	p, owner, repo, ok = Detect("ssh://git@gitea.example.com:2222/o/r.git")
	if !ok || p.Host != "gitea.example.com" || owner != "o" || repo != "r" {
		t.Errorf("SSH 带端口解析 = %+v %q/%q %v", p, owner, repo, ok)
	}
}

func TestPlatformForHost(t *testing.T) {
	cases := []struct {
		host string
		p    Provider
	}{
		{"github.com", GitHub},
		{"gitee.com", Gitee},
		{"atomgit.com", AtomGit},
		{"gitea.example.com", Gitea},
		{"https://Gitea.Example.com/", Gitea},
	}
	for _, tc := range cases {
		got := PlatformForHost(tc.host)
		if got.Provider != tc.p {
			t.Errorf("PlatformForHost(%q).Provider = %q，期望 %q", tc.host, got.Provider, tc.p)
		}
	}
	if got := PlatformForHost("gitea.example.com"); got.Host != "gitea.example.com" {
		t.Errorf("PlatformForHost 丢失 host：%+v", got)
	}
}

func TestPlatformURLs(t *testing.T) {
	cases := []struct {
		p     Platform
		label string
		api   string
		web   string
		clone string
	}{
		{Platform{GitHub, "github.com"}, "GitHub", "GitHub", "https://github.com/o/r", "https://github.com/o/r.git"},
		{Platform{Gitee, "gitee.com"}, "Gitee", "Gitee", "https://gitee.com/o/r", "https://gitee.com/o/r.git"},
		{Platform{AtomGit, "atomgit.com"}, "AtomGit", "AtomGit", "https://atomgit.com/o/r", "https://atomgit.com/o/r.git"},
		{Platform{Gitea, "gitea.example.com"}, "Gitea", "Gitea（gitea.example.com）", "https://gitea.example.com/o/r", "https://gitea.example.com/o/r.git"},
	}
	for _, tc := range cases {
		if got := tc.p.Label(); got != tc.label {
			t.Errorf("%+v.Label() = %q，期望 %q", tc.p, got, tc.label)
		}
		if got := tc.p.APILabel(); got != tc.api {
			t.Errorf("%+v.APILabel() = %q，期望 %q", tc.p, got, tc.api)
		}
		if got := tc.p.WebURL("o", "r"); got != tc.web {
			t.Errorf("%+v.WebURL = %q，期望 %q", tc.p, got, tc.web)
		}
		if got := tc.p.CloneURL("o", "r"); got != tc.clone {
			t.Errorf("%+v.CloneURL = %q，期望 %q", tc.p, got, tc.clone)
		}
	}
}

func TestRepoGitHub(t *testing.T) {
	var gotPath, gotUA, gotAccept, gotVersion, gotAuth string
	c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "gh-token", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUA = r.Header.Get("User-Agent")
		gotAccept = r.Header.Get("Accept")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		gotAuth = r.Header.Get("Authorization")
		writeForgeJSON(t, w, map[string]any{
			"full_name":         "bestuav/atlas",
			"owner":             map[string]any{"login": "bestuav"},
			"name":              "atlas",
			"description":       "无人机图集",
			"html_url":          "https://github.com/bestuav/atlas",
			"default_branch":    "main",
			"language":          "Go",
			"private":           false,
			"archived":          true,
			"stargazers_count":  12,
			"forks_count":       3,
			"open_issues_count": 4,
			"pushed_at":         "2026-11-04T07:23:17Z",
			"updated_at":        "2026-11-03T01:02:03Z",
		})
	})

	info, err := c.Repo(context.Background(), "bestuav", "atlas")
	if err != nil {
		t.Fatalf("Repo 失败: %v", err)
	}
	if gotPath != "/repos/bestuav/atlas" {
		t.Errorf("路径 = %q", gotPath)
	}
	if gotUA != "bestuav-atlas" || gotAccept != "application/json" {
		t.Errorf("请求头 UA/Accept = %q/%q", gotUA, gotAccept)
	}
	if gotVersion != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version = %q", gotVersion)
	}
	if gotAuth != "Bearer gh-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if info.FullName != "bestuav/atlas" || info.Owner != "bestuav" || info.Name != "atlas" {
		t.Errorf("仓库名 = %+v", info)
	}
	if info.Description != "无人机图集" || info.HTMLURL != "https://github.com/bestuav/atlas" ||
		info.DefaultBranch != "main" || info.Language != "Go" {
		t.Errorf("描述/URL/分支/语言 = %+v", info)
	}
	if info.Private || !info.Archived || info.Stars != 12 || info.Forks != 3 || info.OpenIssues != 4 {
		t.Errorf("布尔/计数 = %+v", info)
	}
	want := time.Date(2026, 11, 4, 7, 23, 17, 0, time.UTC)
	if !info.PushedAt.Equal(want) || info.UpdatedAt.IsZero() {
		t.Errorf("时间 = %v / %v", info.PushedAt, info.UpdatedAt)
	}
}

func TestRepoGitee(t *testing.T) {
	var gotPath, gotToken, gotAuth string
	c := newForgeTestClient(t, Platform{Gitee, "gitee.com"}, "gitee-token", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.URL.Query().Get("access_token")
		gotAuth = r.Header.Get("Authorization")
		writeForgeJSON(t, w, map[string]any{
			"full_name":         "open/gitee",
			"owner":             map[string]any{"login": "open"},
			"name":              "gitee",
			"description":       "码云仓库",
			"html_url":          "https://gitee.com/open/gitee",
			"default_branch":    "master",
			"language":          "Go",
			"private":           true,
			"stargazers_count":  21,
			"forks_count":       5,
			"open_issues_count": 6,
			"pushed_at":         "2026-10-01T12:00:00Z",
			"updated_at":        "2026-10-02T13:00:00Z",
		})
	})
	info, err := c.Repo(context.Background(), "open", "gitee")
	if err != nil {
		t.Fatalf("Repo 失败: %v", err)
	}
	if gotPath != "/repos/open/gitee" {
		t.Errorf("路径 = %q", gotPath)
	}
	if gotToken != "gitee-token" {
		t.Errorf("access_token = %q", gotToken)
	}
	if gotAuth != "" {
		t.Errorf("Gitee 不应发 Authorization，实际 %q", gotAuth)
	}
	if info.FullName != "open/gitee" || info.DefaultBranch != "master" || !info.Private {
		t.Errorf("字段 = %+v", info)
	}
	if info.Stars != 21 || info.Forks != 5 || info.OpenIssues != 6 {
		t.Errorf("计数 = %+v", info)
	}
	if !info.PushedAt.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("PushedAt = %v", info.PushedAt)
	}
}

func TestRepoGitea(t *testing.T) {
	c0 := New(Platform{Gitea, "gitea.example.com"}, "")
	if got := c0.base(); got != "https://gitea.example.com/api/v1" {
		t.Errorf("Gitea 默认 base = %q", got)
	}

	var gotPath, gotAuth string
	c := newForgeTestClient(t, Platform{Gitea, "gitea.example.com"}, "gitea-pat", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		writeForgeJSON(t, w, map[string]any{
			"full_name":         "team/repo",
			"owner":             map[string]any{"login": "team"},
			"name":              "repo",
			"description":       "自建实例",
			"html_url":          "https://gitea.example.com/team/repo",
			"branch":            "develop",
			"language":          "Go",
			"private":           false,
			"archived":          true,
			"stars_count":       7,
			"forks_count":       2,
			"open_issues_count": 1,
			"updated_at":        "2026-09-09T09:09:09Z",
		})
	})
	info, err := c.Repo(context.Background(), "team", "repo")
	if err != nil {
		t.Fatalf("Repo 失败: %v", err)
	}
	if gotPath != "/repos/team/repo" {
		t.Errorf("路径 = %q", gotPath)
	}
	if gotAuth != "token gitea-pat" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if info.DefaultBranch != "develop" {
		t.Errorf("default_branch 缺省时应使用 branch，实际 %q", info.DefaultBranch)
	}
	if info.Stars != 7 || !info.Archived {
		t.Errorf("stars/archived = %d/%v", info.Stars, info.Archived)
	}
	want := time.Date(2026, 9, 9, 9, 9, 9, 0, time.UTC)
	if !info.UpdatedAt.Equal(want) || !info.PushedAt.Equal(want) {
		t.Errorf("updated_at 应作为 pushed_at 回落，实际 %v / %v", info.UpdatedAt, info.PushedAt)
	}
}

func TestRepoAtomGit(t *testing.T) {
	var gotPath, gotToken, gotAuth string
	c := newForgeTestClient(t, Platform{AtomGit, "atomgit.com"}, "atom-token", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.URL.Query().Get("access_token")
		gotAuth = r.Header.Get("Authorization")
		writeForgeJSON(t, w, map[string]any{
			"full_name":         "atlas/drone",
			"namespace":         map[string]any{"path": "atlas"},
			"name":              "drone",
			"description":       "AtomGit 仓库",
			"html_url":          "https://atomgit.com/atlas/drone",
			"default_branch":    "main",
			"private":           1,
			"stargazers_count":  8,
			"forks_count":       1,
			"open_issues_count": 2,
			"updated_at":        "2026-08-08T08:08:08Z",
		})
	})
	info, err := c.Repo(context.Background(), "atlas", "drone")
	if err != nil {
		t.Fatalf("Repo 失败: %v", err)
	}
	if gotPath != "/repos/atlas/drone" {
		t.Errorf("路径 = %q", gotPath)
	}
	if gotToken != "atom-token" || gotAuth != "" {
		t.Errorf("access_token/Authorization = %q/%q", gotToken, gotAuth)
	}
	if !info.Private || info.Owner != "atlas" || info.Name != "drone" {
		t.Errorf("字段 = %+v", info)
	}
	want := time.Date(2026, 8, 8, 8, 8, 8, 0, time.UTC)
	if !info.UpdatedAt.Equal(want) || !info.PushedAt.Equal(want) {
		t.Errorf("无 pushed_at 时应回落 updated_at，实际 %v / %v", info.UpdatedAt, info.PushedAt)
	}
}

func TestReadmeGitHub(t *testing.T) {
	const body = "# 无人机图集\n\n| 字段 | 含义 |\n| --- | --- |\n"
	var gotPath, gotAuth string
	c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "gh-token", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		writeForgeJSON(t, w, map[string]any{
			"name":     "README.md",
			"encoding": "base64",
			"content":  wrapForgeBase64(body),
		})
	})
	text, err := c.Readme(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Readme 失败: %v", err)
	}
	if gotPath != "/repos/o/r/readme" || gotAuth != "Bearer gh-token" {
		t.Errorf("路径/认证 = %q/%q", gotPath, gotAuth)
	}
	if text != body {
		t.Errorf("正文 = %q", text)
	}
}

func TestReadmeAtomGitURL(t *testing.T) {
	var srv *httptest.Server
	var gotRepoPath, gotRawPath, gotAccept, gotToken string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r":
			gotRepoPath = r.URL.Path
			writeForgeJSON(t, w, map[string]any{
				"full_name":  "o/r",
				"readme_url": srv.URL + "/raw/README.md",
			})
		case "/raw/README.md":
			gotRawPath = r.URL.Path
			gotAccept = r.Header.Get("Accept")
			gotToken = r.URL.Query().Get("access_token")
			_, _ = w.Write([]byte("纯文本 README\n"))
		default:
			t.Errorf("意外请求 %s", r.URL.Path)
		}
	})
	srv = httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(Platform{AtomGit, "atomgit.com"}, "atom-token")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()

	text, err := c.Readme(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Readme 失败: %v", err)
	}
	if text != "纯文本 README\n" {
		t.Errorf("正文 = %q", text)
	}
	if gotRepoPath != "/repos/o/r" || gotRawPath != "/raw/README.md" {
		t.Errorf("路径 = %q / %q", gotRepoPath, gotRawPath)
	}
	if gotAccept != "text/plain" {
		t.Errorf("原文 Accept = %q", gotAccept)
	}
	if gotToken != "atom-token" {
		t.Errorf("原文请求应带 access_token，实际 %q", gotToken)
	}
}

func TestReadmeNotFound(t *testing.T) {
	c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"Not Found"}`))
	})
	text, err := c.Readme(context.Background(), "o", "empty")
	if err == nil || text != "" {
		t.Fatalf("404 应返回空正文和错误，实际 %q / %v", text, err)
	}
	if !strings.Contains(err.Error(), "仓库不存在或无权限访问") || !strings.Contains(err.Error(), "README") {
		t.Errorf("404 文案 = %q", err)
	}
}

func TestReadmeGiteeFallback(t *testing.T) {
	var paths []string
	c := newForgeTestClient(t, Platform{Gitee, "gitee.com"}, "gitee-token", func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/repos/o/r/readme":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		case "/repos/o/r/contents/README.md":
			writeForgeJSON(t, w, map[string]any{
				"name":     "README.md",
				"encoding": "base64",
				"content":  wrapForgeBase64("gitee fallback"),
			})
		default:
			t.Errorf("意外路径 %s", r.URL.Path)
		}
	})
	text, err := c.Readme(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("Readme 失败: %v", err)
	}
	if text != "gitee fallback" {
		t.Errorf("正文 = %q", text)
	}
	if strings.Join(paths, ",") != "/repos/o/r/readme,/repos/o/r/contents/README.md" {
		t.Errorf("请求路径 = %v", paths)
	}
}

func TestReadmeTruncated(t *testing.T) {
	body := strings.Repeat("航拍影像。\n", 100000) // 约 1.5 MB
	c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "", func(w http.ResponseWriter, r *http.Request) {
		writeForgeJSON(t, w, map[string]any{
			"name":     "README.md",
			"encoding": "base64",
			"content":  wrapForgeBase64(body),
		})
	})
	text, err := c.Readme(context.Background(), "o", "r")
	if err != nil {
		t.Fatalf("大 README 不应报错: %v", err)
	}
	if !strings.Contains(text, "截断") {
		t.Error("缺少截断提示")
	}
	if !strings.HasSuffix(text, readmeTruncateNotice) {
		t.Errorf("结尾提示 = %q", text[len(text)-80:])
	}
	if !utf8.ValidString(text) {
		t.Error("截断后不是合法 UTF-8")
	}
	kept := strings.TrimSuffix(text, readmeTruncateNotice)
	if !strings.HasPrefix(body, kept) {
		t.Error("保留部分不是原文前缀")
	}
}

func TestListReposPaging(t *testing.T) {
	var pages []string
	c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "gh-token", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/o/repos" {
			t.Errorf("路径 = %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("per_page") != "100" || q.Get("sort") != "pushed" {
			t.Errorf("查询 = %q", r.URL.RawQuery)
		}
		page := q.Get("page")
		pages = append(pages, page)
		var items []map[string]any
		switch page {
		case "1":
			for i := 0; i < 100; i++ {
				items = append(items, map[string]any{
					"full_name": fmt.Sprintf("o/r%03d", i),
					"owner":     map[string]any{"login": "o"},
					"name":      fmt.Sprintf("r%03d", i),
				})
			}
		case "2":
			items = append(items,
				map[string]any{"full_name": "o/last1"},
				map[string]any{"full_name": "o/last2"},
			)
		default:
			t.Errorf("不该请求第 %s 页", page)
		}
		writeForgeJSON(t, w, items)
	})
	repos, err := c.ListRepos(context.Background(), "o")
	if err != nil {
		t.Fatalf("ListRepos 失败: %v", err)
	}
	if len(repos) != 102 {
		t.Fatalf("仓库数 = %d，期望 102", len(repos))
	}
	if repos[0].FullName != "o/r000" || repos[100].FullName != "o/last1" || repos[101].FullName != "o/last2" {
		t.Errorf("顺序 = %q / %q / %q", repos[0].FullName, repos[100].FullName, repos[101].FullName)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Errorf("页码 = %v", pages)
	}
}

func TestListReposCurrentUser(t *testing.T) {
	var gotPath, gotAffiliation, gotAuth string
	c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "gh-token", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAffiliation = r.URL.Query().Get("affiliation")
		gotAuth = r.Header.Get("Authorization")
		writeForgeJSON(t, w, []map[string]any{{"full_name": "me/private", "private": true}})
	})
	repos, err := c.ListRepos(context.Background(), "")
	if err != nil {
		t.Fatalf("ListRepos 失败: %v", err)
	}
	if gotPath != "/user/repos" || gotAffiliation != "owner" || gotAuth != "Bearer gh-token" {
		t.Errorf("路径/affiliation/认证 = %q/%q/%q", gotPath, gotAffiliation, gotAuth)
	}
	if len(repos) != 1 || !repos[0].Private {
		t.Errorf("返回 = %+v", repos)
	}
}

func TestListReposGiteaPagingParams(t *testing.T) {
	var gotPath, gotLimit, gotPage string
	c := newForgeTestClient(t, Platform{Gitea, "gitea.example.com"}, "", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotLimit = r.URL.Query().Get("limit")
		gotPage = r.URL.Query().Get("page")
		writeForgeJSON(t, w, []map[string]any{})
	})
	if _, err := c.ListRepos(context.Background(), "team"); err != nil {
		t.Fatalf("ListRepos 失败: %v", err)
	}
	if gotPath != "/users/team/repos" || gotLimit != "100" || gotPage != "1" {
		t.Errorf("路径/查询 = %q limit=%q page=%q", gotPath, gotLimit, gotPage)
	}
}

func TestStatusErrors(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for 203.0.113.7."}`))
		})
		_, err := c.Repo(context.Background(), "o", "r")
		if err == nil || !strings.Contains(err.Error(), "GitHub API 限流") ||
			!strings.Contains(err.Error(), "配置令牌") {
			t.Errorf("限流文案 = %v", err)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		c := newForgeTestClient(t, Platform{Gitee, "gitee.com"}, "bad", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
		})
		_, err := c.Repo(context.Background(), "o", "r")
		if err == nil || err.Error() != "令牌无效或已过期" {
			t.Errorf("401 文案 = %v", err)
		}
	})
	t.Run("not found", func(t *testing.T) {
		c := newForgeTestClient(t, Platform{GitHub, "github.com"}, "", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		_, err := c.Repo(context.Background(), "o", "r")
		if err == nil || err.Error() != "仓库不存在或无权限访问" {
			t.Errorf("404 文案 = %v", err)
		}
	})
	t.Run("other error body", func(t *testing.T) {
		c := newForgeTestClient(t, Platform{Gitee, "gitee.com"}, "", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("服务暂时不可用"))
		})
		_, err := c.Repo(context.Background(), "o", "r")
		if err == nil || !strings.Contains(err.Error(), "Gitee 返回 500：服务暂时不可用") {
			t.Errorf("其它错误文案 = %v", err)
		}
	})
}

func TestValidation(t *testing.T) {
	c := New(Platform{}, "")
	if _, err := c.Repo(context.Background(), "", "r"); err == nil {
		t.Error("owner 为空应报错")
	}
	if _, err := c.Repo(context.Background(), "o", " "); err == nil {
		t.Error("repo 为空应报错")
	}
	if _, err := c.Readme(context.Background(), "", "r"); err == nil {
		t.Error("owner 为空应报错")
	}
}

func TestNewFallback(t *testing.T) {
	c := New(Platform{Provider: "unknown", Host: "gitea.example.com"}, " tok ")
	if c.Platform.Provider != GitHub {
		t.Errorf("未知 Provider 应回落 GitHub，实际 %q", c.Platform.Provider)
	}
	if c.Token != "tok" {
		t.Errorf("Token 未 trim：%q", c.Token)
	}
}
