// Package pagegen 生成「新增一个菜单页面」所需的两处改动：页面文件 + 注册表条目。
//
// 为什么需要它：新增页面本来是「写一个 page_xxx.go + 在 navRegistry() 的字面量里加
// 一条」，两处都要改、页面 id 还要在注册表、状态、快捷键之间保持一致。本包把这件
// 事收敛成一条命令（scripts/newpage），并且**只做文本插入 + gofmt**——
// 注册表依然是「直接 return 字面量」，生成器（internal/scaffold/regcut.go）照常能
// 按 AST 裁剪它。
//
// 只依赖标准库：生成的页面用项目自己的模块路径（从 go.mod 读），所以本仓库和
// 「新建项目」生成的工程里都能直接用。
package pagegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Options 生成参数。
type Options struct {
	Root   string // 项目根目录（含 go.mod）；空 = 当前目录
	ID     string // 页面 id：小写字母开头，可含数字与连字符（如 orders、stock-alert）
	Label  string // 侧栏文字 + 页头标题
	Group  string // 菜单分组标题（如「工作台」）；空 = 置顶、不画分组标题
	Icon   string // internal/ui/icons 里的导出名（如 Table）；空 = Square
	Policy string // 生命周期：cached（默认）/ pinned / fresh
}

// Result 生成结果（路径均相对 Root，斜杠分隔）。
type Result struct {
	PageFile string
	Registry string
	IDConst  string
}

// 页面 id 的合法形态：小写字母开头，允许数字与单个连字符。
var idRe = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Add 按 opts 生成页面文件并把菜单项插进注册表。
//
// 任何一步失败都不会留下半成品：页面文件先写临时文件、注册表改完再整体落盘；
// 注册表写入前会用 go/format 校验，写坏直接报错而不是把工程变成编不过的状态。
func Add(opts Options) (Result, error) {
	var res Result
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = "."
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return res, err
	}
	if !idRe.MatchString(opts.ID) {
		return res, fmt.Errorf("页面 id %q 不合法：需小写字母开头，只能含小写字母、数字与连字符（如 orders、stock-alert）", opts.ID)
	}
	if strings.TrimSpace(opts.Label) == "" {
		return res, fmt.Errorf("必须给页面一个标题（侧栏文字与页头标题都用它）")
	}
	policy, err := policyField(opts.Policy)
	if err != nil {
		return res, err
	}
	module, err := readModule(root)
	if err != nil {
		return res, err
	}
	icon := opts.Icon
	if icon == "" {
		icon = "Square"
	}
	if err := checkIcon(root, icon); err != nil {
		return res, err
	}

	regPath := filepath.Join(root, "internal", "view", "registry.go")
	regSrc, err := os.ReadFile(regPath)
	if err != nil {
		return res, fmt.Errorf("读取注册表失败: %w", err)
	}
	idConst := "page" + camel(opts.ID) + "ID"
	pageFunc := lowerFirst(camel(opts.ID)) + "Page"
	fileID := strings.ReplaceAll(opts.ID, "-", "_")
	pageRel := filepath.ToSlash(filepath.Join("internal", "view", "page_"+fileID+".go"))
	pagePath := filepath.Join(root, filepath.FromSlash(pageRel))
	if _, statErr := os.Stat(pagePath); statErr == nil {
		return res, fmt.Errorf("页面文件已存在: %s（换一个 id，或先删掉它）", pageRel)
	}
	if exists, err := registryHasID(regSrc, opts.ID, idConst); err != nil {
		return res, err
	} else if exists {
		return res, fmt.Errorf("注册表里已经有 id=%q 的菜单项", opts.ID)
	}

	entry := fmt.Sprintf("{id: %s, label: %s, icon: icons.%s, build: (*App).%s%s},",
		idConst, strconv.Quote(opts.Label), icon, pageFunc, policy)
	newReg, err := insertEntry(regSrc, opts.Group, entry)
	if err != nil {
		return res, err
	}

	pageSrc := pageTemplate(module, idConst, opts.ID, fileID, pageFunc, opts.Label)
	if formatted, ferr := format.Source([]byte(pageSrc)); ferr == nil {
		pageSrc = string(formatted)
	}
	if err := os.WriteFile(pagePath, []byte(pageSrc), 0o644); err != nil {
		return res, fmt.Errorf("写页面文件失败: %w", err)
	}
	if err := os.WriteFile(regPath, newReg, 0o644); err != nil {
		// 回滚页面文件，避免留下「有页面但菜单里没有」的半成品。
		_ = os.Remove(pagePath)
		return res, fmt.Errorf("写注册表失败: %w", err)
	}
	return Result{PageFile: pageRel, Registry: "internal/view/registry.go", IDConst: idConst}, nil
}

// policyField 把 policy 参数转成注册表里的字段片段（空串 = 用默认的 keepCached）。
func policyField(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "cached", "keepcached":
		return "", nil
	case "pinned", "keeppinned":
		return ", policy: pagePolicy{keep: keepPinned}", nil
	case "fresh", "keepfresh":
		return ", policy: pagePolicy{keep: keepFresh}", nil
	default:
		return "", fmt.Errorf("未知的生命周期 %q：可选 cached / pinned / fresh", p)
	}
}

// readModule 从 go.mod 读出模块路径（生成的页面 import 自己的 kit 要用它）。
func readModule(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("读取 go.mod 失败（需要在项目根目录执行）: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("go.mod 里没有 module 声明")
}

// checkIcon 确认图标名在 internal/ui/icons/icons.go 里有导出（避免生成后编不过）。
func checkIcon(root, name string) error {
	path := filepath.Join(root, "internal", "ui", "icons", "icons.go")
	src, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取图标文件失败: %w", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return fmt.Errorf("解析图标文件失败: %w", err)
	}
	var names []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, n := range vs.Names {
				if ast.IsExported(n.Name) {
					names = append(names, n.Name)
				}
			}
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return fmt.Errorf("图标 %q 不存在（internal/ui/icons 里可用的导出名，如 %s）",
		name, strings.Join(firstN(names, 8), "、"))
}

// firstN 取前 n 个元素（提示信息用）。
func firstN(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

// registryHasID 检查注册表里是否已有该页面 id（字面量或常量名两种写法）。
func registryHasID(src []byte, id, idConst string) (bool, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "registry.go", src, 0)
	if err != nil {
		return false, fmt.Errorf("解析注册表失败: %w", err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "id" {
			return true
		}
		switch v := kv.Value.(type) {
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				if s, err := strconv.Unquote(v.Value); err == nil && s == id {
					found = true
				}
			}
		case *ast.Ident:
			if v.Name == idConst {
				found = true
			}
		}
		return true
	})
	return found, nil
}

// insertEntry 把菜单项文本插到指定分组的 items 字面量最前面，并整体 gofmt。
//
// 只做「按字节插入」而不是重建 AST：保留的分组、注释、空行一字不动，生成的 diff
// 最小；插入后统一 format.Source，缩进/对齐交给 gofmt。
func insertEntry(src []byte, group, entry string) ([]byte, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "registry.go", src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("解析注册表失败: %w", err)
	}
	fn := findFunc(f, "navRegistry")
	if fn == nil {
		return nil, fmt.Errorf("registry.go 里找不到 navRegistry 函数")
	}
	groups := returnCompositeLit(fn)
	if groups == nil || len(groups.Elts) == 0 {
		return nil, fmt.Errorf("navRegistry 的返回值不是分组字面量")
	}

	// 收集可选分组名（报错时列给用户）。
	var names []string
	var target *ast.CompositeLit
	for _, elt := range groups.Elts {
		grp, ok := elt.(*ast.CompositeLit)
		if !ok {
			continue
		}
		title := stringField(grp, "title")
		if title != "" {
			names = append(names, title)
		}
		if title == group {
			target = grp
		}
	}
	if target == nil {
		return nil, fmt.Errorf("找不到分组 %q；现有分组：%s（置顶组请传空 -group）",
			group, strings.Join(names, "、"))
	}
	items := compositeField(target, "items")
	if items == nil {
		return nil, fmt.Errorf("分组 %q 下没有 items 字面量", group)
	}

	offset := fset.Position(items.Lbrace).Offset + 1
	indent := itemIndent(src, fset, items, target)
	ins := "\n" + indent + entry
	out := make([]byte, 0, len(src)+len(ins))
	out = append(out, src[:offset]...)
	out = append(out, ins...)
	out = append(out, src[offset:]...)

	formatted, err := format.Source(out)
	if err != nil {
		return nil, fmt.Errorf("插入后的注册表语法不合法（模板结构可能变过）: %w", err)
	}
	// 双保险：gofmt 之后新增的那一条必须还在（防止插入位置落在注释/字符串里）。
	if !bytes.Contains(formatted, []byte(entry)) {
		return nil, fmt.Errorf("插入后校验失败（找不到新增的菜单项）")
	}
	return formatted, nil
}

// itemIndent 取分组内已有第一个菜单项的行首缩进；空分组退回「分组行 + 一个 Tab」。
func itemIndent(src []byte, fset *token.FileSet, items, group *ast.CompositeLit) string {
	if len(items.Elts) > 0 {
		lineStart := fset.Position(items.Elts[0].Pos()).Offset
		for lineStart > 0 && src[lineStart-1] != '\n' {
			lineStart--
		}
		prefix := string(src[lineStart:fset.Position(items.Elts[0].Pos()).Offset])
		if strings.TrimSpace(prefix) == "" {
			return prefix
		}
	}
	lineStart := fset.Position(group.Pos()).Offset
	for lineStart > 0 && src[lineStart-1] != '\n' {
		lineStart--
	}
	prefix := string(src[lineStart:fset.Position(group.Pos()).Offset])
	return prefix + "\t"
}

// findFunc 在文件里找顶层函数声明。
func findFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// returnCompositeLit 取函数体里第一条 return 的复合字面量（与 scaffold 同款约定）。
func returnCompositeLit(fn *ast.FuncDecl) *ast.CompositeLit {
	if fn.Body == nil {
		return nil
	}
	var found *ast.CompositeLit
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 {
			return true
		}
		if lit, ok := ret.Results[0].(*ast.CompositeLit); ok {
			found = lit
			return false
		}
		return true
	})
	return found
}

// stringField 读取复合字面量里 `key: "字符串"` 的值。
func stringField(lit *ast.CompositeLit, key string) string {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if ident, ok := kv.Key.(*ast.Ident); !ok || ident.Name != key {
			continue
		}
		if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
			if s, err := strconv.Unquote(bl.Value); err == nil {
				return s
			}
		}
	}
	return ""
}

// compositeField 读取复合字面量里某个键对应的子复合字面量（例如 items）。
func compositeField(lit *ast.CompositeLit, key string) *ast.CompositeLit {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if ident, ok := kv.Key.(*ast.Ident); !ok || ident.Name != key {
			continue
		}
		if sub, ok := kv.Value.(*ast.CompositeLit); ok {
			return sub
		}
	}
	return nil
}

// camel 把 kebab-case 的 id 转成 CamelCase（orders → Orders、stock-alert → StockAlert）。
func camel(id string) string {
	var b strings.Builder
	for _, part := range strings.Split(id, "-") {
		if part == "" {
			continue
		}
		b.WriteString(strings.ToUpper(part[:1]))
		b.WriteString(part[1:])
	}
	return b.String()
}

// lowerFirst 把首字母改成小写（OrdersPage → ordersPage：页面方法沿用包内未导出的命名）。
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// pageTemplate 页面文件模板：只依赖 ui 与 kit，编译通过、可直接跑。
//
// 刻意不生成业务状态与示例表格：模板越短，开发者越清楚「这里该写什么」；
// 需要页面私有状态时用 pageData / a.Prefs()（注释里给了一行提示）。
func pageTemplate(module, idConst, id, fileID, pageFunc, label string) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, `package view

import (
	"github.com/egoist/mygo/ui"

	"%s/internal/ui/kit"
)

// %s 页面 id：注册表、快捷键动作、页面状态都引用它，避免字符串散落。
const %s = %q

// %s %s。
//
// 页面私有状态用 pageData / a.Prefs()，不要挂到 App 字段上（见 README「开发约定」）。
func (a *App) %s(c *ui.Context, t *ui.Theme) {
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, %q, "在这里写一句话说明", nil)
		kit.EmptyHint(c, t, "还没有内容：在 page_%s.go 里补业务区块")
	})
}
`, module, idConst, idConst, id, pageFunc, label, pageFunc, label, fileID)
	return b.String()
}
