// Package forge 是代码托管平台（GitHub / Gitee / Gitea / AtomGit）的只读客户端。
//
// 它统一了四个平台里差异最大的三件事：API 地址、认证方式和字段名。页面只关心
// 「仓库信息、README、某个人的仓库列表」，不必再按平台写 switch。
//
// 设计取舍：
//   - 只用标准库。这里只有几个 GET，引 SDK 会带来额外依赖与升级节奏。
//   - 没有定时器/轮询。何时拉取由界面决定，避免无谓消耗匿名配额。
//   - 令牌只由调用方传入；GitHub/Gitea 放在请求头，Gitee/AtomGit 放在
//     access_token 查询参数（平台约定），本包不写盘、不打日志。
//   - 公开仓库在无令牌时也能读仓库信息和 README。
package forge

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

// Provider 代码托管平台的 API 形态。
type Provider string

const (
	GitHub  Provider = "github"
	Gitee   Provider = "gitee"
	Gitea   Provider = "gitea"
	AtomGit Provider = "atomgit"
)

const (
	// userAgent 必填：GitHub 会拒绝没有 User-Agent 的请求。
	userAgent = "bestuav-atlas"
	// apiVersion 固定 GitHub API 版本，避免响应结构悄悄变化。
	apiVersion = "2022-11-28"
	// perPage 四个平台的单页上限都按 100 使用。
	perPage = 100
	// maxPages 列表最多取 3 页（300 个仓库），既够设置页挑选，也不会一次抽干配额。
	maxPages = 3
	// defaultTimeout 默认 HTTP 超时；20 秒不回来通常就是网络不通。
	defaultTimeout = 20 * time.Second
)

// defaultHTTP 共享默认客户端，复用连接池。
var defaultHTTP = &http.Client{Timeout: defaultTimeout}

// Platform 一个远程仓库平台：Provider 决定 API 形态，Host 决定网页/克隆地址。
type Platform struct {
	Provider Provider
	Host     string // github.com / gitee.com / atomgit.com / gitea.example.com
	// Scheme 网页、克隆与（Gitea）API 使用的协议："http" 或 "https"；空 = https。
	//
	// 自建 Gitea 常跑在内网 http 上，所以协议必须「以配置/地址为准」，不能硬编码
	// https。公开平台（GitHub/Gitee/AtomGit）始终 https。
	Scheme string
}

// Label 平台展示名。
func (p Platform) Label() string {
	switch p.Provider {
	case Gitee:
		return "Gitee"
	case Gitea:
		return "Gitea"
	case AtomGit:
		return "AtomGit"
	default:
		return "GitHub"
	}
}

// APILabel 用于错误与界面提示。自建 Gitea 带上实例域名，便于用户确认配的是哪台机器。
func (p Platform) APILabel() string {
	if p.Provider == Gitea {
		if host := normalizeHost(p.Host); host != "" {
			return "Gitea（" + host + "）"
		}
	}
	return p.Label()
}

// WebURL 仓库网页地址。
func (p Platform) WebURL(owner, repo string) string {
	return p.scheme() + "://" + p.host() + "/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

// CloneURL 仓库 HTTPS 克隆地址（协议跟随平台：自建 Gitea 可能是 http）。
func (p Platform) CloneURL(owner, repo string) string {
	return p.WebURL(owner, repo) + ".git"
}

// scheme 返回实际使用的协议：只认 http/https，其余一律 https。
func (p Platform) scheme() string {
	if strings.EqualFold(strings.TrimSpace(p.Scheme), "http") {
		return "http"
	}
	return "https"
}

// host 返回实际使用的 host；未填写时按平台给默认值。
func (p Platform) host() string {
	if h := normalizeHost(p.Host); h != "" {
		return h
	}
	switch p.Provider {
	case Gitee:
		return "gitee.com"
	case AtomGit:
		return "atomgit.com"
	case Gitea:
		return "gitea.com"
	default:
		return "github.com"
	}
}

// RepoInfo 一个仓库的摘要信息。
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

// Client 一个代码托管平台客户端。字段可零值构造，也可以先调 New。
type Client struct {
	Platform Platform
	Token    string
	BaseURL  string       // 空 = 按平台默认；测试与自建反代可覆盖
	HTTP     *http.Client // 空 = 20s 超时
}

// New 构造客户端。未知 Provider 回落到 GitHub；令牌允许为空。
func New(p Platform, token string) *Client {
	if !validProvider(p.Provider) {
		p.Provider = GitHub
	}
	return &Client{Platform: p, Token: strings.TrimSpace(token)}
}

// Repo 读取单个仓库信息。
func (c *Client) Repo(ctx context.Context, owner, repo string) (RepoInfo, error) {
	var info RepoInfo
	owner, repo, err := cleanOwnerRepo(owner, repo)
	if err != nil {
		return info, err
	}
	d, _, err := c.fetchRepo(ctx, owner, repo)
	if err != nil {
		return info, err
	}
	return d.info(c.platform(), owner, repo), nil
}

// ListRepos 列出某个 owner 的仓库，最多 3 页、每页 100 条。
// owner 为空时使用当前登录用户的仓库接口（通常需要令牌才能看到私有仓库）。
func (c *Client) ListRepos(ctx context.Context, owner string) ([]RepoInfo, error) {
	owner = strings.TrimSpace(owner)
	out := make([]RepoInfo, 0, perPage)
	for page := 1; page <= maxPages; page++ {
		raw, _, _, err := c.get(ctx, c.listURL(owner, page), "application/json")
		if err != nil {
			return nil, err
		}
		var items []repoJSON
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, fmt.Errorf("解析仓库列表失败: %w", err)
		}
		for _, it := range items {
			out = append(out, it.info(c.platform(), owner, ""))
		}
		// 不满一页就是最后一页；不解析 Link 头，四个平台的列表接口都遵循这个约定。
		if len(items) < perPage {
			break
		}
	}
	return out, nil
}

// Detect 从仓库地址识别平台与 owner/repo。支持：
//
//	https://host/o/r(.git)
//	git@host:o/r.git
//	ssh://git@host/o/r.git
//	host/o/r
//
// 已知的 github.com / gitee.com / atomgit.com 映射到对应平台，其它带点的
// host 视为自建 Gitea。无法解析时 ok=false。
func Detect(rawURL string) (p Platform, owner, repo string, ok bool) {
	s := strings.TrimSpace(rawURL)
	if s == "" {
		return Platform{}, "", "", false
	}

	// webScheme 记录地址里写的协议：http 只对自建 Gitea 有意义，
	// 公开平台即使被写成 http 也按 https 处理（它们只服务 https）。
	webScheme := "https"
	var host, path string
	if i := strings.Index(s, "://"); i >= 0 {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return Platform{}, "", "", false
		}
		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "http", "https", "ssh", "git", "git+ssh", "ssh+git":
		default:
			return Platform{}, "", "", false
		}
		if scheme == "http" {
			webScheme = "http"
		}
		// SSH 的端口是 22/2222，不是网页与 API 的端口；Host 用于拼 https 地址，
		// 因此只保留主机名。HTTP(S) 的非标准端口则可能是自建实例的网页端口，保留。
		switch scheme {
		case "ssh", "git", "git+ssh", "ssh+git":
			host = u.Hostname()
		default:
			host = u.Host
		}
		path = u.Path
	} else if at := strings.Index(s, "@"); at >= 0 && strings.Contains(s[at+1:], ":") {
		// scp 风格（git@host:o/r.git）不能被 url.Parse 正确处理，手工剥前缀。
		rest := s[at+1:]
		colon := strings.Index(rest, ":")
		host, path = rest[:colon], rest[colon+1:]
	}
	if host == "" {
		// host:o/r（省略 user@）也按 scp 风格处理；Windows 盘符（C:/...）前缀
		// 只有一个字母，必须排除。
		if colon := strings.Index(s, ":"); colon > 1 && !strings.Contains(s[:colon], "/") && !strings.Contains(s[:colon], "\\") && strings.Contains(s[:colon], ".") {
			host, path = s[:colon], s[colon+1:]
		}
	}
	if host == "" {
		segs := splitSegments(s)
		if len(segs) < 3 {
			return Platform{}, "", "", false
		}
		host = segs[0]
		path = strings.Join(segs[1:], "/")
	}

	host = normalizeHost(host)
	if !validDetectHost(host) {
		return Platform{}, "", "", false
	}
	path = strings.TrimRight(strings.TrimSpace(path), "/")
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		path = path[:len(path)-4]
	}
	segs := splitSegments(path)
	if len(segs) != 2 {
		return Platform{}, "", "", false
	}
	owner, repo = strings.TrimSpace(segs[0]), strings.TrimSpace(segs[1])
	if owner == "" || repo == "" {
		return Platform{}, "", "", false
	}
	platform := PlatformForHost(host)
	if platform.Provider == Gitea && webScheme == "http" {
		platform.Scheme = "http"
	}
	return platform, owner, repo, true
}

// PlatformForHost 由 host 造平台（同一套映射规则；用于只存了 host 的场景）。
// Scheme 留空 = https；自建 Gitea 需要 http 时由调用方补上。
func PlatformForHost(host string) Platform {
	host = normalizeHost(host)
	if host == "" {
		return Platform{Provider: GitHub, Host: "github.com"}
	}
	return Platform{Provider: providerForHost(host), Host: host}
}

// ---- 内部 ----

type repoJSON struct {
	FullName      string   `json:"full_name"`
	Owner         ownerRef `json:"owner"`
	Namespace     ownerRef `json:"namespace"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	HTMLURL       string   `json:"html_url"`
	DefaultBranch string   `json:"default_branch"`
	Branch        string   `json:"branch"`
	Language      string   `json:"language"`
	Private       flexBool `json:"private"`
	Archived      bool     `json:"archived"`
	Stars         int      `json:"stargazers_count"`
	StarsCount    int      `json:"stars_count"`
	Forks         int      `json:"forks_count"`
	OpenIssues    int      `json:"open_issues_count"`
	PushedAt      string   `json:"pushed_at"`
	UpdatedAt     string   `json:"updated_at"`
	ReadmeURL     string   `json:"readme_url"`
}

type ownerRef struct {
	Login    string `json:"login"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Path     string `json:"path"`
}

// flexBool 兼容 true/false 与 AtomGit 的 0/1。
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	s := strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`))
	switch s {
	case "1", "true":
		*b = true
	default:
		*b = false
	}
	return nil
}

func (r repoJSON) info(p Platform, reqOwner, reqRepo string) RepoInfo {
	fullOwner, fullName := splitFullName(r.FullName)
	owner := firstNonEmpty(r.Owner.Login, r.Owner.Username, r.Owner.Name, r.Namespace.Path, r.Namespace.Name, fullOwner, reqOwner)
	name := firstNonEmpty(r.Name, fullName, reqRepo)
	full := r.FullName
	if full == "" && owner != "" && name != "" {
		full = owner + "/" + name
	}

	updated := parseTime(r.UpdatedAt)
	pushed := parseTime(r.PushedAt)
	// Gitea / AtomGit 的仓库对象没有 pushed_at，用 updated_at 代替。
	if pushed.IsZero() && (p.Provider == Gitea || p.Provider == AtomGit) {
		pushed = updated
	}

	branch := r.DefaultBranch
	if branch == "" && p.Provider == Gitea {
		branch = r.Branch
	}
	stars := r.Stars
	if p.Provider == Gitea {
		stars = r.StarsCount
		// 个别 Gitea 版本/兼容层仍用 stargazers_count，避免显示成 0。
		if stars == 0 {
			stars = r.Stars
		}
	}
	if p.Provider == AtomGit && stars == 0 {
		stars = r.StarsCount
	}
	htmlURL := r.HTMLURL
	if htmlURL == "" && owner != "" && name != "" {
		htmlURL = p.WebURL(owner, name)
	}
	return RepoInfo{
		FullName:      full,
		Owner:         owner,
		Name:          name,
		Description:   r.Description,
		HTMLURL:       htmlURL,
		DefaultBranch: branch,
		Language:      r.Language,
		Private:       bool(r.Private),
		Archived:      r.Archived,
		Stars:         stars,
		Forks:         r.Forks,
		OpenIssues:    r.OpenIssues,
		PushedAt:      pushed,
		UpdatedAt:     updated,
	}
}

func (c *Client) fetchRepo(ctx context.Context, owner, repo string) (repoJSON, int, error) {
	var d repoJSON
	raw, _, status, err := c.get(ctx, c.repoURL(owner, repo), "application/json")
	if err != nil {
		return d, status, err
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, status, fmt.Errorf("解析仓库信息失败: %w", err)
	}
	return d, status, nil
}

func (c *Client) platform() Platform {
	p := c.Platform
	if !validProvider(p.Provider) {
		p.Provider = GitHub
	}
	return p
}

func (c *Client) base() string {
	if b := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); b != "" {
		return b
	}
	switch c.platform().Provider {
	case Gitee:
		return "https://gitee.com/api/v5"
	case Gitea:
		// 协议跟平台（自建实例可能是 http）；上文的 BaseURL 覆盖优先。
		p := c.platform()
		return p.scheme() + "://" + p.host() + "/api/v1"
	case AtomGit:
		return "https://api.atomgit.com/api/v5"
	default:
		return "https://api.github.com"
	}
}

func (c *Client) hc() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) repoURL(owner, repo string) string {
	return c.base() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func (c *Client) listURL(owner string, page int) string {
	base := c.base()
	q := url.Values{}
	q.Set("per_page", fmt.Sprintf("%d", perPage))
	q.Set("page", fmt.Sprintf("%d", page))
	switch c.platform().Provider {
	case GitHub:
		if owner == "" {
			q.Set("affiliation", "owner")
			return base + "/user/repos?" + q.Encode()
		}
		q.Set("sort", "pushed")
		return base + "/users/" + url.PathEscape(owner) + "/repos?" + q.Encode()
	case Gitea:
		q.Del("per_page")
		q.Set("limit", fmt.Sprintf("%d", perPage))
		if owner == "" {
			return base + "/user/repos?" + q.Encode()
		}
		return base + "/users/" + url.PathEscape(owner) + "/repos?" + q.Encode()
	case Gitee, AtomGit:
		if owner == "" {
			return base + "/user/repos?" + q.Encode()
		}
		return base + "/users/" + url.PathEscape(owner) + "/repos?" + q.Encode()
	default:
		return base + "/user/repos?" + q.Encode()
	}
}

// get 发一个 GET 并读出响应体。status 返回给调用方按场景覆盖文案。
func (c *Client) get(ctx context.Context, u, accept string) ([]byte, http.Header, int, error) {
	req, err := c.request(ctx, u, accept)
	if err != nil {
		return nil, nil, 0, err
	}
	resp, err := c.hc().Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("连接 %s 失败: %w", c.platform().APILabel(), err)
	}
	defer resp.Body.Close()
	// 列表页与 README 都可能较大，8 MB 足够，也避免异常响应吃满内存。
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, resp.Header, resp.StatusCode, nil
	}
	return raw, resp.Header, resp.StatusCode, c.statusError(resp.StatusCode, raw)
}

func (c *Client) request(ctx context.Context, u, accept string) (*http.Request, error) {
	p := c.platform()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("构造 %s 请求失败: %w", p.APILabel(), err)
	}
	req.Header.Set("User-Agent", userAgent)
	if strings.TrimSpace(accept) == "" {
		accept = "application/json"
	}
	req.Header.Set("Accept", accept)

	token := strings.TrimSpace(c.Token)
	switch p.Provider {
	case GitHub:
		req.Header.Set("X-GitHub-Api-Version", apiVersion)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	case Gitea:
		if token != "" {
			req.Header.Set("Authorization", "token "+token)
		}
	case Gitee, AtomGit:
		// 这两个平台用 access_token 查询参数；没有令牌就不拼空值。
		if token != "" {
			q := req.URL.Query()
			q.Set("access_token", token)
			req.URL.RawQuery = q.Encode()
		}
	}
	return req, nil
}

// ScopeError 令牌权限不足：403 且响应体点名了缺少的 scope。
//
// Gitea 的「列出我的仓库」（GET /user/repos）要求 read:user；只给了
// write:repository 的令牌会拿到 403。这个错误要能让人**照着改**，
// 所以把缺的 scope 单独抽出来，界面可以提示「补权限」或「填用户名走公开列表」。
type ScopeError struct {
	Platform string   // 平台展示名，如 Gitea（gitea.example.com）
	Scopes   []string // 需要的权限，如 read:user
	Detail   string   // 平台原始 message（便于排障）
}

func (e *ScopeError) Error() string {
	if len(e.Scopes) == 0 {
		return e.Platform + " 令牌权限不足：" + e.Detail
	}
	return fmt.Sprintf("%s 令牌权限不足：需要 %s。给令牌补上该权限，或在导入弹窗里填写用户名（改用公开仓库列表）",
		e.Platform, strings.Join(e.Scopes, "、"))
}

// statusError 把 HTTP 状态码翻译成用户能照做的中文提示。
func (c *Client) statusError(code int, raw []byte) error {
	label := c.platform().APILabel()
	switch code {
	case http.StatusUnauthorized:
		return fmt.Errorf("令牌无效或已过期")
	case http.StatusForbidden, http.StatusTooManyRequests:
		lower := strings.ToLower(string(raw))
		// 403 不一定是配额；只有响应体明确提到限流时才提示等待/配令牌。
		if strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate_limit") ||
			strings.Contains(lower, "ratelimit") || strings.Contains(string(raw), "限流") {
			return fmt.Errorf("%s API 限流，请稍后再试或配置令牌", label)
		}
		if scopes := requiredScopes(raw); len(scopes) > 0 {
			return &ScopeError{Platform: label, Scopes: scopes, Detail: apiMessage(raw)}
		}
		return fmt.Errorf("%s 返回 %d：%s", label, code, apiMessage(raw))
	case http.StatusNotFound:
		return fmt.Errorf("仓库不存在或无权限访问")
	default:
		return fmt.Errorf("%s 返回 %d：%s", label, code, apiMessage(raw))
	}
}

// requiredScopes 从 403 响应体里解析缺少的权限。
//
// Gitea 的文案形如：
//
//	token does not have at least one of required scope(s), required=[read:user], token scope=write:repository
//
// 只取 required=[...] 这一段；解析不出来就返回空（按普通 403 处理）。
func requiredScopes(raw []byte) []string {
	msg := apiMessage(raw)
	start := strings.Index(msg, "required=[")
	if start < 0 {
		return nil
	}
	rest := msg[start+len("required=["):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return nil
	}
	var out []string
	for _, s := range strings.Split(rest[:end], ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
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
	msg = strings.Join(strings.Fields(msg), " ")
	if msg == "" {
		return "（无响应内容）"
	}
	return truncateRunes(msg, 200)
}

// parseTime 解析 RFC3339；失败返回零值，不打断仓库信息读取。
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

func validProvider(p Provider) bool {
	switch p {
	case GitHub, Gitee, Gitea, AtomGit:
		return true
	default:
		return false
	}
}

func providerForHost(host string) Provider {
	switch hostName(host) {
	case "github.com":
		return GitHub
	case "gitee.com":
		return Gitee
	case "atomgit.com":
		return AtomGit
	default:
		return Gitea
	}
}

// normalizeHost 接受 "gitea.example.com" 或 "https://gitea.example.com/"，
// 统一成小写 host（保留端口）。
func normalizeHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			s = u.Host
		}
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(s), "/"))
}

// hostName 去掉端口，用于已知平台映射。
func hostName(host string) string {
	h := host
	if strings.HasPrefix(h, "[") {
		if i := strings.Index(h, "]"); i >= 0 {
			return strings.ToLower(h[:i+1])
		}
	}
	if i := strings.LastIndex(h, ":"); i >= 0 {
		return strings.ToLower(h[:i])
	}
	return strings.ToLower(h)
}

// validDetectHost 只接受「已知平台」或「带点的自建 host」。
func validDetectHost(host string) bool {
	if host == "" || strings.ContainsAny(host, " \t\r\n/\\") {
		return false
	}
	switch hostName(host) {
	case "github.com", "gitee.com", "atomgit.com":
		return true
	}
	return strings.Contains(hostName(host), ".")
}

func splitFullName(s string) (owner, repo string) {
	segs := strings.Split(strings.TrimSpace(s), "/")
	if len(segs) != 2 {
		return "", ""
	}
	return strings.TrimSpace(segs[0]), strings.TrimSpace(segs[1])
}

func splitSegments(s string) []string {
	parts := strings.Split(s, "/")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func cleanOwnerRepo(owner, repo string) (string, string, error) {
	owner, repo = strings.TrimSpace(owner), strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("请填写仓库的所有者与名称")
	}
	return owner, repo, nil
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}
