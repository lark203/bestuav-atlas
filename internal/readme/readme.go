// Package readme 读取仓库里的 README 文件。
//
// 为什么只认根目录的固定几个名字：README 是给人看仓库首页的约定文件，项目不会
// 把它藏在子目录里；只在根目录按固定顺序找，既快又不会把 docs/README.md 之类
// 的文档误当成仓库说明。文件名比较统一转小写，Windows 上大小写不敏感的写法
// （readme.md、ReadMe.MD）都能命中。
package readme

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ErrNotFound 表示目录里没有可识别的 README。
var ErrNotFound = errors.New("没有找到 README")

const (
	// maxSize 正文上限 512 KB。超过就截断而不是报错：README 往往只是开头
	// 那段介绍有用，为了几百 KB 的变更日志让整页读不出来不值得。
	maxSize = 512 << 10
	// truncateNotice 截断后追加的一行提示，让用户知道下面不是完整内容。
	truncateNotice = "\n\n（README 超过 512 KB，已截断，只显示开头部分。）"
)

// candidates 识别顺序（小写形式）。README.md / readme.md / Readme.md 三种写法
// 在「转小写后比较」的规则下是同一个名字，所以合并成一条；后面的 markdown、
// txt 依次退让，最后才是没有扩展名的 README。
var candidates = []string{"readme.md", "readme.markdown", "readme.txt", "readme"}

// ReadLocal 读取 dir 里的 README，返回识别到的文件名（如 "README.md"）与正文。
//
// 按 README.md、README.markdown、README.txt、README（无扩展名）的优先级查找，
// 文件名大小写不敏感；只读 dir 这一层，不遍历子目录。正文超过 512 KB 时截断，
// 并在末尾追加一行提示（大文件也要能看到开头）。dir 为空、不是目录，或目录里
// 没有可识别的 README 时，返回包装了 ErrNotFound 的错误（用 %w）。
func ReadLocal(dir string) (name string, text string, err error) {
	if strings.TrimSpace(dir) == "" {
		return "", "", fmt.Errorf("未设置 README 所在目录: %w", ErrNotFound)
	}
	// 路径不存在、指向普通文件、无权限，ReadDir 都会失败；这些都是「这里没有
	// 可读的 README」，统一包成 ErrNotFound，调用方只判断一个错误就够了。
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", fmt.Errorf("无法读取目录 %s（不是目录或不可访问）: %w（%v）", dir, ErrNotFound, err)
	}

	// 不为每个候选名再调一次 ReadFile：先把目录项收在手里，按优先级挑出真实
	// 文件名（保留磁盘上的大小写），最后只读一次文件。
	found := ""
	for _, cand := range candidates {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.ToLower(e.Name()) == cand {
				found = e.Name()
				break
			}
		}
		if found != "" {
			break
		}
	}
	if found == "" {
		return "", "", fmt.Errorf("目录 %s 里没有 README: %w", dir, ErrNotFound)
	}

	raw, err := os.ReadFile(filepath.Join(dir, found))
	if err != nil {
		return "", "", fmt.Errorf("读取 %s 失败: %w", found, err)
	}
	if len(raw) > maxSize {
		return found, string(trimPartialRune(raw[:maxSize])) + truncateNotice, nil
	}
	return found, string(raw), nil
}

// trimPartialRune 把截断点回退到 UTF-8 字符边界。
//
// 按 512 KB 直接切会切出半个汉字（一个汉字 3 字节），界面上就是一个乱码方块；
// DecodeLastRune 返回 RuneError 且长度为 1 说明末尾是不完整序列，退一个字节再试。
func trimPartialRune(b []byte) []byte {
	for len(b) > 0 {
		r, size := utf8.DecodeLastRune(b)
		if r != utf8.RuneError || size > 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return b
}
