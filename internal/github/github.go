// Package github 是 GitHub REST API 的最小客户端（只读两个接口）。
//
// 为什么不用 SDK（go-github）：本项目只用到「读单个仓库」与「列某个 owner 的仓库」
// 两个 GET 加一处分页；引一个 SDK 会带来一长串间接依赖和自己的升级节奏，而
// net/http + encoding/json 足以覆盖，也守住项目「只用标准库」的约束。
//
// 为什么没有轮询：什么时候刷新由界面决定（进仓库页 / 点刷新），后台定时拉取只会
// 白白烧掉配额——匿名请求每小时只有 60 次。
//
// 认证用个人访问令牌（PAT），令牌只从调用方传入、只放在请求头里：
// 本包不写盘、不打日志、不拼进 URL。
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultBaseURL 生产环境地址；测试把 BaseURL 指向 httptest 即可。
	defaultBaseURL = "https://api.github.com"
	// userAgent 必填：GitHub 会拒绝没有 User-Agent 的请求（403）。
	userAgent = "bestuav-atlas"
	// apiVersion 固定 API 版本，避免 GitHub 悄悄改变响应结构。
	apiVersion = "2022-11-28"
	// perPage GitHub 单页上限就是 100。
	perPage = 100
	// maxPages 最多取 3 页（300 个仓库）。设置页只是让人挑一个仓库，
	// 300 足够；硬上限则防止 owner 仓库特别多时一次把配额抽干。
	maxPages = 3
	// defaultTimeout 默认 HTTP 超时。GitHub 正常响应在数百毫秒级，
	// 20 秒还没回来基本是网络不通，早点失败比让界面转圈好。
	defaultTimeout = 20 * time.Second
)

// defaultHTTP 共享的默认客户端：复用连接池，避免每次调用新建 Transport。
var defaultHTTP = &http.Client{Timeout: defaultTimeout}

// Client 一个 GitHub API 客户端。字段可零值直接构造，也可以先调 New。
type Client struct {
	Token   string       // 可空：公开仓库/匿名限流
	BaseURL string       // 空= https://api.github.com；测试可指向 httptest
	HTTP    *http.Client // 空= 默认 20s 超时
}

// New 构造客户端。令牌允许为空（公开仓库可匿名访问）。
func New(token string) *Client {
	return &Client{Token: strings.TrimSpace(token)}
}

// RepoInfo 一个仓库的摘要信息。字段都做了零值兜底：GitHub 某些字段可能为 null。
type RepoInfo struct {
	FullName      string
	Owner         string
	Name          string
	Description   string
	HTMLURL       string
	DefaultBranch string
	Language      string
	Private       bool
	Archived      bool
	Stars         int
	Forks         int
	OpenIssues    int
	PushedAt      time.Time
	UpdatedAt     time.Time
}

// Repo 读取单个仓库信息。owner/repo 为空返回错误；404 → 「仓库不存在或无权限访问」。
func (c *Client) Repo(ctx context.Context, owner, repo string) (RepoInfo, error) {
	var info RepoInfo
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return info, fmt.Errorf("请填写仓库的所有者与名称")
	}
	// url.PathEscape：owner/repo 理论上不含需要转义的字符，但用户手填时什么都可能进来。
	u := c.base() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
	raw, _, _, err := c.get(ctx, u)
	if err != nil {
		return info, err
	}
	var d repoJSON
	if err := json.Unmarshal(raw, &d); err != nil {
		return info, fmt.Errorf("解析仓库信息失败: %w", err)
	}
	return d.info(), nil
}

// ListRepos 列出某个 owner 的仓库（最多 300 个，按 pushed_at 倒序由调用方排序）。
// owner 为空时用 /user/repos（需要令牌，含私有）；owner 非空用 /users/{owner}/repos。
func (c *Client) ListRepos(ctx context.Context, owner string) ([]RepoInfo, error) {
	owner = strings.TrimSpace(owner)
	out := make([]RepoInfo, 0, perPage)
	for page := 1; page <= maxPages; page++ {
		u := fmt.Sprintf("%s?per_page=%d&page=%d&sort=pushed", c.reposURL(owner), perPage, page)
		raw, _, status, err := c.get(ctx, u)
		if err != nil {
			// 这里是「列某个人的仓库」，404 的语义是用户不存在，不是仓库不存在。
			if status == http.StatusNotFound && owner != "" {
				return nil, fmt.Errorf("用户 %s 不存在或无权限访问", owner)
			}
			return nil, err
		}
		var items []repoJSON
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("解析仓库列表失败: %w", err)
		}
		for _, it := range items {
			out = append(out, it.info())
		}
		// 「一页不满 100 条」就是最后一页，这是 GitHub 列表接口的稳定行为。
		// 不解析 Link 头：它给的是相对 URL，还得处理 <> 与 rel="next"，收益为零。
		if len(items) < perPage {
			break
		}
	}
	return out, nil
}

// ParseRemote 从 git 远端地址里解析 owner/repo。
//
// 支持的形态（只解析路径，不判断平台，Gitee/Gitea 同样适用）：
//
//	https://github.com/o/r.git
//	git@github.com:o/r.git
//	ssh://git@github.com/o/r.git
//	https://gitee.com/o/r
//
// 无法解析时 ok=false。
func ParseRemote(raw string) (owner, repo string, ok bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	if i := strings.Index(s, "://"); i >= 0 {
		u, err := url.Parse(s)
		if err != nil {
			return "", "", false
		}
		// 只认真正指向远端的协议；file:// 指向本地目录，解析出的「owner/repo」
		// 只是磁盘层级，交给调用方会误导。
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return "", "", false
		}
		s = u.Path
	} else {
		// scp 风格（git@github.com:o/r.git）不能被 url.Parse 正确处理，
		// 手工剥掉 "user@host:" 前缀；没有冒号说明是普通本地路径，直接放弃。
		i := strings.Index(s, ":")
		if i < 0 {
			return "", "", false
		}
		prefix := s[:i]
		// 前缀里出现 "/" 说明冒号属于别的东西（如带端口的写法已被 :// 分支处理）。
		if strings.Contains(prefix, "/") {
			return "", "", false
		}
		// 单个字母前缀是 Windows 盘符（C:/repo、D:\repo），不是远端主机。
		// 只排长度为 1 的前缀，是为了不误伤 "gh:o/r.git" 这种短主机名写法。
		if len(prefix) == 1 {
			return "", "", false
		}
		s = s[i+1:]
	}
	// 先去掉尾部斜杠，再去掉 .git 后缀（"o/r.git/" 这种两种都带的写法也能处理）。
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")
	segs := splitSegments(s)
	if len(segs) < 2 {
		return "", "", false
	}
	owner, repo = segs[len(segs)-2], segs[len(segs)-1]
	if owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

// SplitFull 解析 "owner/repo"；也接受带 .git 后缀与首尾空格。
func SplitFull(s string) (owner, repo string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "/") // 顺手容忍 "o/r/"
	s = strings.TrimSuffix(s, ".git")
	segs := strings.Split(s, "/")
	// 只接受恰好两段：多出来的层级（a/b/c）语义不明，猜成哪两段都可能错。
	if len(segs) != 2 {
		return "", "", false
	}
	owner, repo = strings.TrimSpace(segs[0]), strings.TrimSpace(segs[1])
	if owner == "" || repo == "" {
		return "", "", false
	}
	return owner, repo, true
}

// ---- 内部 ----

// repoJSON GitHub 返回的仓库对象（只声明用得到的字段）。
type repoJSON struct {
	FullName      string  `json:"full_name"`
	Owner         userRef `json:"owner"`
	Name          string  `json:"name"`
	Description   string  `json:"description"` // 可能为 null
	HTMLURL       string  `json:"html_url"`
	DefaultBranch string  `json:"default_branch"`
	Language      string  `json:"language"` // 可能为 null
	Private       bool    `json:"private"`
	Archived      bool    `json:"archived"`
	Stars         int     `json:"stargazers_count"`
	Forks         int     `json:"forks_count"`
	OpenIssues    int     `json:"open_issues_count"`
	PushedAt      string  `json:"pushed_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type userRef struct {
	Login string `json:"login"`
}

func (r repoJSON) info() RepoInfo {
	return RepoInfo{
		FullName:      r.FullName,
		Owner:         r.Owner.Login,
		Name:          r.Name,
		Description:   r.Description,
		HTMLURL:       r.HTMLURL,
		DefaultBranch: r.DefaultBranch,
		Language:      r.Language,
		Private:       r.Private,
		Archived:      r.Archived,
		Stars:         r.Stars,
		Forks:         r.Forks,
		OpenIssues:    r.OpenIssues,
		PushedAt:      parseTime(r.PushedAt),
		UpdatedAt:     parseTime(r.UpdatedAt),
	}
}

func (c *Client) base() string {
	b := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if b == "" {
		return defaultBaseURL
	}
	return b
}

func (c *Client) hc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

// reposURL 列表接口路径：owner 为空走 /user/repos（含私有，需令牌），
// 否则走 /users/{owner}/repos（公开仓库；匿名也能读）。
func (c *Client) reposURL(owner string) string {
	if owner == "" {
		return c.base() + "/user/repos"
	}
	return c.base() + "/users/" + url.PathEscape(owner) + "/repos"
}

// get 发一个 GET 并读出响应体。返回值里的 status 供调用方按场景覆盖文案
// （同一个 404，在「读仓库」和「列某人的仓库」里含义不同）。
func (c *Client) get(ctx context.Context, u string) ([]byte, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("构造 GitHub 请求失败: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if tok := strings.TrimSpace(c.Token); tok != "" {
		// 没有令牌就干脆不发 Authorization：空值会被当成无效凭证直接 401。
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.hc().Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("连接 GitHub 失败: %w", err)
	}
	defer resp.Body.Close()
	// 列表页最大也就几 MB，8MB 上限足够，同时避免异常响应把内存吃满。
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, resp.Header, resp.StatusCode, nil
	}
	return raw, resp.Header, resp.StatusCode, statusError(resp.StatusCode, raw)
}

// statusError 把 HTTP 状态码翻译成用户能照做的中文提示。
//
// 为什么不直接把 GitHub 的英文 message 抛给用户：界面上出现
// "API rate limit exceeded for 1.2.3.4" 时，用户不知道该等一小时还是该配令牌；
// 这里按「用户下一步该做什么」来写文案。
func statusError(code int, raw []byte) error {
	msg := apiMessage(raw)
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("令牌无效或已过期")
	case http.StatusForbidden, http.StatusTooManyRequests:
		// 403 不一定是配额：也可能是无权限。GitHub 的限流响应体里一定有 rate limit。
		if strings.Contains(strings.ToLower(string(raw)), "rate limit") {
			return fmt.Errorf("GitHub API 限流，请稍后再试或配置令牌")
		}
		return fmt.Errorf("GitHub 返回 %d：%s", code, msg)
	case http.StatusNotFound:
		return fmt.Errorf("仓库不存在或无权限访问")
	default:
		return fmt.Errorf("GitHub 返回 %d：%s", code, msg)
	}
}

// apiMessage 从错误响应体里取 message；不是 JSON 就退回原文。
func apiMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	msg := ""
	if err := json.Unmarshal(raw, &e); err == nil {
		msg = strings.TrimSpace(e.Message)
	}
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	// 压成一行并截断：错误提示要能塞进界面的一行文案里。
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return "（无响应内容）"
	}
	return truncate(msg, 200)
}

// parseTime 解析 GitHub 的时间（形如 2026-11-04T07:23:17Z），转成本地时区。
//
// 用 RFC3339：GitHub 的时间字段就是这个格式（含 Z 或偏移），不需要自定义 layout。
// 字段可能为 null/空串，此时返回零值——界面通过 IsZero 判断「没有这个时间」。
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.Local()
}

// splitSegments 按 "/" 切分并丢掉空段（处理开头/结尾/连续斜杠）。
func splitSegments(s string) []string {
	parts := strings.Split(s, "/")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// truncate 按字符（而不是字节）截断，避免把一个汉字切成半个。
func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
