// 本文件是 github 包里「读仓库 README」的能力，单独成文件便于和仓库信息接口区分。
//
// 复用 github.go 的私有 get：请求头（User-Agent/Accept/API 版本/Authorization）、
// 超时、限流与状态码文案都在那一处维护，这里只负责拼路径和解析 base64。

package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	// readmeMaxSize 返回给调用方的正文上限：512 KB。GitHub 的 README 接口本身
	// 最大 1 MB，但界面展示用不到全部，截断能避免把整页拖慢。
	readmeMaxSize = 512 << 10
	// readmeTruncateNotice 截断后追加的一行提示，与 internal/readme 保持一致。
	readmeTruncateNotice = "\n\n（README 超过 512 KB，已截断，只显示开头部分。）"
)

// readmeJSON GitHub 的 README 响应里用得到的字段。
type readmeJSON struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Encoding string `json:"encoding"` // 文本是 "base64"；二进制是 "none"
	Content  string `json:"content"`  // base64，且按 60 个字符折行
	Size     int    `json:"size"`
}

// Readme 取仓库的 README 正文（GitHub 的 /repos/{owner}/{repo}/readme）。
//
// 仓库没有 README 时返回可读错误（含「没有 README」字样）；403 限流沿用
// statusError 的文案。只接受文本类文件：encoding 不是 base64（GitHub 判定为
// 二进制时给 "none"）或解码后正文为空，都返回 ("", error)——这两种情况都没有
// 可展示的文本，返回错误能让调用方明确回退到别的来源，而不是渲染一个空页面。
// 返回内容按 512 KB 截断。
func (c *Client) Readme(ctx context.Context, owner, repo string) (string, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("请填写仓库的所有者与名称")
	}
	// 与 Repo 一致地做 PathEscape：owner/repo 正常不含特殊字符，但用户手填时不可信。
	u := c.base() + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/readme"
	raw, _, status, err := c.get(ctx, u)
	if err != nil {
		// 只有 404 需要换文案：get 的「仓库不存在或无权限访问」在 README 场景里
		// 更可能是「仓库能访问、只是没有 README」。403/401 等保持原有提示。
		if status == http.StatusNotFound {
			return "", fmt.Errorf("仓库没有 README 或不可访问")
		}
		return "", err
	}

	var d readmeJSON
	if err := json.Unmarshal(raw, &d); err != nil {
		return "", fmt.Errorf("解析 README 失败: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(d.Encoding), "base64") {
		return "", fmt.Errorf("README %s 不是文本文件（encoding=%q）", strings.TrimSpace(d.Name), d.Encoding)
	}
	// GitHub 的 content 是折过行的 base64（每 60 字符一个 \n），解码前先去掉
	// 所有换行；StdEncoding 本身也会忽略 \r\n，这里显式清掉是为了顺手处理空格。
	content := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(d.Content)
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return "", fmt.Errorf("解码 README 失败: %w", err)
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("README 内容为空或不是文本")
	}
	if len(decoded) > readmeMaxSize {
		return string(trimReadmeRune(decoded[:readmeMaxSize])) + readmeTruncateNotice, nil
	}
	return string(decoded), nil
}

// trimReadmeRune 把截断点回退到 UTF-8 字符边界，避免末尾出现半个汉字。
// 逻辑与 internal/readme 的同名工具一致；两个包各自保留一份，避免 github
// 为了截断一个字符串去依赖本地文件读取包。
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
