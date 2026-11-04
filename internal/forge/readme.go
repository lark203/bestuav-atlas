// 本文件是 forge 包里「读仓库 README」的能力。
//
// 各平台差异集中在三处：认证方式、base64 响应格式，以及 AtomGit 会先在仓库
// 信息里给一个 readme_url。请求头/超时/状态码文案统一复用 forge.go 的 get。

package forge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// readmeMaxSize 返回给调用方的正文上限：512 KB。
	readmeMaxSize = 512 << 10
	// readmeTruncateNotice 截断后追加的提示，与 internal/readme 保持一致。
	readmeTruncateNotice = "\n\n（README 超过 512 KB，已截断，只显示开头部分。）"
)

// readmeJSON README 接口的响应（GitHub / Gitee / Gitea / AtomGit 通用）。
type readmeJSON struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
	Size     int    `json:"size"`
}

// Readme 取仓库的 README 正文。
//
// GitHub / Gitea 走 /repos/{o}/{r}/readme；Gitee 先走同一路径，失败再试
// contents/README.md；AtomGit 先用仓库信息里的 readme_url 取原文，没有才走
// base64 的 readme 接口。正文超过 512 KB 会按 UTF-8 边界截断并追加提示。
func (c *Client) Readme(ctx context.Context, owner, repo string) (string, error) {
	owner, repo, err := cleanOwnerRepo(owner, repo)
	if err != nil {
		return "", err
	}
	switch c.platform().Provider {
	case AtomGit:
		return c.readmeAtomGit(ctx, owner, repo)
	case Gitee:
		return c.readmeGitee(ctx, owner, repo)
	default:
		text, status, err := c.readmeBase64At(ctx, c.repoURL(owner, repo)+"/readme")
		if err != nil {
			if status == http.StatusNotFound {
				return "", readmeNotFoundError()
			}
			return "", err
		}
		return text, nil
	}
}

// readmeGitee Gitee 的 /readme 不可用时，退到 Contents API 的 README.md。
func (c *Client) readmeGitee(ctx context.Context, owner, repo string) (string, error) {
	repoURL := c.repoURL(owner, repo)
	text, firstStatus, firstErr := c.readmeBase64At(ctx, repoURL+"/readme")
	if firstErr == nil {
		return text, nil
	}

	// 按平台约定「失败再试」：/readme 可能未实现、路径不存在或返回了非预期格式。
	text, status, err := c.readmeBase64At(ctx, repoURL+"/contents/README.md")
	if err == nil {
		return text, nil
	}
	if status == http.StatusNotFound {
		// 第一接口已经是认证/限流错误时，不要用第二个接口的 404 把它盖掉。
		if firstStatus == http.StatusUnauthorized || firstStatus == http.StatusForbidden || firstStatus == http.StatusTooManyRequests {
			return "", firstErr
		}
		return "", readmeNotFoundError()
	}
	// 第二接口返回 200 但内容格式不符时，保留第一接口的错误。
	if status == http.StatusOK {
		return "", firstErr
	}
	return "", err
}

// readmeAtomGit 优先按仓库信息给的 readme_url 取纯文本原文。
func (c *Client) readmeAtomGit(ctx context.Context, owner, repo string) (string, error) {
	d, status, err := c.fetchRepo(ctx, owner, repo)
	if err != nil {
		if status == http.StatusNotFound {
			return "", readmeNotFoundError()
		}
		return "", err
	}
	if u := strings.TrimSpace(d.ReadmeURL); u != "" {
		raw, _, status, err := c.get(ctx, c.resolveReadmeURL(u), "text/plain")
		if err == nil {
			return formatReadme(raw)
		}
		// URL 失效时再给 base64 接口一次机会；其余错误（401/限流等）直接返回。
		if status != http.StatusNotFound {
			return "", err
		}
	}

	text, status, err := c.readmeBase64At(ctx, c.repoURL(owner, repo)+"/readme")
	if err != nil {
		if status == http.StatusNotFound {
			return "", readmeNotFoundError()
		}
		return "", err
	}
	return text, nil
}

// readmeBase64At 请求返回 base64 content 的 README 接口。
func (c *Client) readmeBase64At(ctx context.Context, u string) (string, int, error) {
	raw, _, status, err := c.get(ctx, u, "application/json")
	if err != nil {
		return "", status, err
	}
	text, err := decodeReadmeJSON(raw)
	if err != nil {
		return "", status, err
	}
	return text, status, nil
}

func decodeReadmeJSON(raw []byte) (string, error) {
	var d readmeJSON
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", fmt.Errorf("解析 README 失败: %w", err)
	}
	encoding := strings.TrimSpace(d.Encoding)
	if encoding != "" && !strings.EqualFold(encoding, "base64") {
		return "", fmt.Errorf("README %s 不是文本文件（encoding=%q）", strings.TrimSpace(d.Name), d.Encoding)
	}
	if strings.TrimSpace(d.Content) == "" {
		return "", fmt.Errorf("README 内容为空或不是文本")
	}
	// GitHub 的 base64 每 60 字符折一行；Gitee 等同理。解码前丢掉所有空白。
	clean := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, d.Content)
	decoded, err := base64.StdEncoding.DecodeString(clean)
	if err != nil {
		return "", fmt.Errorf("解码 README 失败: %w", err)
	}
	return formatReadme(decoded)
}

// formatReadme 校验空正文，并按 512 KB + UTF-8 边界处理。
func formatReadme(raw []byte) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("README 内容为空或不是文本")
	}
	if len(raw) <= readmeMaxSize {
		return string(raw), nil
	}
	cut := raw[:readmeMaxSize]
	cut = trimReadmeRune(cut)
	return string(cut) + readmeTruncateNotice, nil
}

// trimReadmeRune 把截断点回退到 UTF-8 字符边界，避免末尾出现半个汉字。
func trimReadmeRune(b []byte) []byte {
	for len(b) > 0 {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size > 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}

// resolveReadmeURL 容忍平台返回相对地址；绝对地址原样返回。
// 相对地址优先落在覆盖后的 BaseURL 上（测试/反代），否则落在平台网页 host 上。
func (c *Client) resolveReadmeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.IsAbs() {
		return raw
	}
	baseStr := "https://" + c.platform().host()
	if b := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); b != "" {
		if bu, err := url.Parse(b); err == nil && bu.Scheme != "" && bu.Host != "" {
			baseStr = bu.Scheme + "://" + bu.Host
		}
	}
	base, _ := url.Parse(baseStr)
	return base.ResolveReference(u).String()
}

// readmeNotFoundError 404 在 README 场景既可能是仓库不存在，也可能是仓库没有
// README；把两种可能都告诉用户，避免误以为令牌没配好。
func readmeNotFoundError() error {
	return fmt.Errorf("仓库不存在或无权限访问（或仓库没有 README）")
}
