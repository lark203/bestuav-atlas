// Package repo 把配置包上传 / 下载到代码托管平台（GitHub / Gitea / Gitee）。
//
// 只用各家都有的「仓库文件（Contents）API」：
//
//	GET  /repos/{owner}/{repo}/contents/{path}   读文件（base64）
//	PUT  /repos/{owner}/{repo}/contents/{path}   新建/更新文件（带 sha 才是更新）
//
// 都是手动操作：没有定时器、没有后台轮询；由调用方决定何时上传/下载。
// 认证用个人访问令牌（PAT），令牌由调用方保管，本包不写盘、不打日志。
package repo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Kind 代码托管平台。
type Kind string

const (
	GitHub Kind = "github"
	Gitea  Kind = "gitea"
	Gitee  Kind = "gitee"
)

// Kinds 全部平台（顺序与设置页一致）。
var Kinds = []Kind{GitHub, Gitea, Gitee}

// KindLabel 平台展示名。
func KindLabel(k Kind) string {
	switch k {
	case Gitea:
		return "Gitea"
	case Gitee:
		return "Gitee"
	default:
		return "GitHub"
	}
}

// Config 一个仓库连接的参数。
type Config struct {
	Kind Kind
	// BaseURL 平台地址：Gitea 必填（如 https://gitea.example.com）；
	// GitHub / Gitee 可留空用默认 API，也可填自建反代（测试用）。
	BaseURL string
	Token   string
	Owner   string
	Repo    string
	Branch  string
	Path    string
}

// Client 一个仓库客户端。
type Client struct {
	cfg  Config
	hc   *http.Client
	base string
}

// Result 一次上传的结果。
type Result struct {
	Commit string // commit sha（平台没返回时为空）
	URL    string // 提交/文件页面（平台没返回时为空）
}

// New 校验参数并构造客户端。
func New(cfg Config) (*Client, error) {
	cfg.Owner = strings.TrimSpace(cfg.Owner)
	cfg.Repo = strings.TrimSpace(cfg.Repo)
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.Path = strings.Trim(strings.TrimSpace(cfg.Path), "/")
	cfg.Branch = strings.TrimSpace(cfg.Branch)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	if cfg.Owner == "" || cfg.Repo == "" {
		return nil, fmt.Errorf("请填写仓库的所有者与名称")
	}
	if cfg.Token == "" {
		return nil, fmt.Errorf("请填写访问令牌")
	}
	if cfg.Path == "" {
		return nil, fmt.Errorf("请填写仓库里的文件路径")
	}
	if cfg.Kind == "" {
		cfg.Kind = GitHub
	}
	var base string
	switch cfg.Kind {
	case GitHub:
		base = "https://api.github.com"
		if cfg.BaseURL != "" {
			base = cfg.BaseURL
		}
	case Gitee:
		base = "https://gitee.com/api/v5"
		if cfg.BaseURL != "" {
			base = cfg.BaseURL
		}
	case Gitea:
		if cfg.BaseURL == "" {
			return nil, fmt.Errorf("Gitea 需要填写实例地址（如 https://gitea.example.com）")
		}
		base = cfg.BaseURL + "/api/v1"
	default:
		return nil, fmt.Errorf("不支持的平台：%s", cfg.Kind)
	}
	return &Client{
		cfg:  cfg,
		hc:   &http.Client{Timeout: 30 * time.Second},
		base: strings.TrimRight(base, "/"),
	}, nil
}

// Path 实际使用的文件路径。
func (c *Client) Path() string { return c.cfg.Path }

// Test 检查仓库与令牌是否可用（读仓库信息）。
func (c *Client) Test(ctx context.Context) error {
	req, err := c.request(ctx, http.MethodGet, c.repoURL(), nil)
	if err != nil {
		return err
	}
	_, _, err = c.do(req)
	return err
}

// Upload 上传（新建或更新）配置包，返回提交信息。
func (c *Client) Upload(ctx context.Context, data []byte, message string) (Result, error) {
	var res Result
	sha, err := c.fileSHA(ctx)
	if err != nil {
		return res, err
	}
	body := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString(data),
		"branch":  c.cfg.Branch,
	}
	if sha != "" {
		body["sha"] = sha
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return res, err
	}
	req, err := c.request(ctx, http.MethodPut, c.contentsURL(), bytes.NewReader(payload))
	if err != nil {
		return res, err
	}
	raw, _, err := c.do(req)
	if err != nil {
		return res, err
	}
	var out struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
		Content struct {
			HTMLURL string `json:"html_url"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &out)
	res.Commit = out.Commit.SHA
	res.URL = out.Content.HTMLURL
	return res, nil
}

// Download 下载配置包。
func (c *Client) Download(ctx context.Context) ([]byte, error) {
	req, err := c.request(ctx, http.MethodGet, c.contentsURL(), nil)
	if err != nil {
		return nil, err
	}
	raw, status, err := c.do(req)
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("仓库里还没有这个文件（先上传一次）")
	}
	if err != nil {
		return nil, err
	}
	var out contentsFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析仓库响应失败: %w", err)
	}
	if out.Content != "" && out.Encoding == "base64" {
		clean := strings.NewReplacer("\n", "", "\r", "", " ", "").Replace(out.Content)
		data, derr := base64.StdEncoding.DecodeString(clean)
		if derr != nil {
			return nil, fmt.Errorf("解码仓库文件失败: %w", derr)
		}
		return data, nil
	}
	// 大文件平台会把 content 留空，只给下载地址；再按 raw 取一次。
	if out.DownloadURL != "" {
		req, err := c.request(ctx, http.MethodGet, out.DownloadURL, nil)
		if err != nil {
			return nil, err
		}
		raw, _, err := c.do(req)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}
	return nil, fmt.Errorf("仓库响应里没有文件内容")
}

// ---- 内部 ----

type contentsFile struct {
	Content     string `json:"content"`
	Encoding    string `json:"encoding"`
	SHA         string `json:"sha"`
	DownloadURL string `json:"download_url"`
}

func (c *Client) repoURL() string {
	return c.base + "/repos/" + url.PathEscape(c.cfg.Owner) + "/" + url.PathEscape(c.cfg.Repo)
}

func (c *Client) contentsURL() string {
	segs := strings.Split(c.cfg.Path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return c.repoURL() + "/contents/" + strings.Join(segs, "/")
}

// fileSHA 返回远端文件的 sha；文件不存在返回空。
func (c *Client) fileSHA(ctx context.Context) (string, error) {
	req, err := c.request(ctx, http.MethodGet, c.contentsURL(), nil)
	if err != nil {
		return "", err
	}
	raw, status, err := c.do(req)
	if status == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var out contentsFile
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析仓库响应失败: %w", err)
	}
	return out.SHA, nil
}

func (c *Client) request(ctx context.Context, method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "bestuav-atlas")
	req.Header.Set("Accept", "application/json")
	switch c.cfg.Kind {
	case GitHub:
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	case Gitea:
		req.Header.Set("Authorization", "token "+c.cfg.Token)
	case Gitee:
		// Gitee v5 用 access_token 查询参数认证。
		q := req.URL.Query()
		q.Set("access_token", c.cfg.Token)
		req.URL.RawQuery = q.Encode()
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (c *Client) do(req *http.Request) ([]byte, int, error) {
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("连接 %s 失败: %w", c.cfg.Kind, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, resp.StatusCode, nil
	}
	var e struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &e)
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return raw, resp.StatusCode, fmt.Errorf("%s 返回 %d：%s", c.cfg.Kind, resp.StatusCode, msg)
}
