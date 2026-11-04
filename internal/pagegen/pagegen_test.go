package pagegen

import (
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fixtureRegistry = `package view

import "demo/internal/ui/icons"

func navRegistry() []navGroup {
	return []navGroup{
		{title: "", items: []navItem{
			{id: "home", label: "首页", icon: icons.Home},
		}},
		{title: "工作台", items: []navItem{
			{id: "task", label: "异步任务", icon: icons.Square},
		}},
	}
}
`

const fixtureIcons = `package icons

var (
	Square = 1
	Table  = 2
	Home   = 3
)
`

// newFixture 造一个最小工程（go.mod + 注册表 + 图标集），返回根目录。
func newFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.27.3\n")
	write("internal/view/registry.go", fixtureRegistry)
	write("internal/ui/icons/icons.go", fixtureIcons)
	return root
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("读取 %s: %v", rel, err)
	}
	return string(data)
}

// groupItemIDs 解析注册表，返回「分组标题 → 该组菜单项的 id 表达式原文」。
func groupItemIDs(t *testing.T, src string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "registry.go", src, 0)
	if err != nil {
		t.Fatalf("生成的注册表无法解析: %v", err)
	}
	fn := findFunc(f, "navRegistry")
	if fn == nil {
		t.Fatal("找不到 navRegistry")
	}
	groups := returnCompositeLit(fn)
	out := map[string][]string{}
	for _, elt := range groups.Elts {
		grp, ok := elt.(*ast.CompositeLit)
		if !ok {
			continue
		}
		title := stringField(grp, "title")
		items := compositeField(grp, "items")
		if items == nil {
			continue
		}
		for _, it := range items.Elts {
			item, ok := it.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, elt := range item.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "id" {
					continue
				}
				if id, ok := kv.Value.(*ast.Ident); ok {
					out[title] = append(out[title], id.Name)
				} else if lit, ok := kv.Value.(*ast.BasicLit); ok {
					out[title] = append(out[title], strings.Trim(lit.Value, `"`))
				}
			}
		}
	}
	return out
}

// TestAddCreatesPageAndMenu 生成页面文件 + 插到指定分组，且两边都能被 gofmt 认可。
func TestAddCreatesPageAndMenu(t *testing.T) {
	root := newFixture(t)
	res, err := Add(Options{
		Root: root, ID: "orders", Label: "订单管理", Group: "工作台", Icon: "Table",
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if res.PageFile != "internal/view/page_orders.go" {
		t.Errorf("页面路径 = %q", res.PageFile)
	}
	if res.IDConst != "pageOrdersID" {
		t.Errorf("id 常量 = %q", res.IDConst)
	}

	page := read(t, root, res.PageFile)
	for _, want := range []string{
		`const pageOrdersID = "orders"`,
		`"demo/internal/ui/kit"`,
		`func (a *App) ordersPage(c *ui.Context, t *ui.Theme)`,
		`kit.Card(c, t, func() {`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("页面文件缺少 %q\n%s", want, page)
		}
	}
	if formatted, err := format.Source([]byte(page)); err != nil || string(formatted) != page {
		t.Errorf("页面文件不是 gofmt 结果（err=%v）", err)
	}

	reg := read(t, root, "internal/view/registry.go")
	if formatted, err := format.Source([]byte(reg)); err != nil || string(formatted) != reg {
		t.Errorf("注册表写入后不是 gofmt 结果（err=%v）\n%s", err, reg)
	}
	wantEntry := `{id: pageOrdersID, label: "订单管理", icon: icons.Table, build: (*App).ordersPage},`
	if !strings.Contains(reg, wantEntry) {
		t.Errorf("注册表缺少新增菜单项 %q\n%s", wantEntry, reg)
	}

	ids := groupItemIDs(t, reg)
	if got := ids["工作台"]; len(got) != 2 || got[0] != "pageOrdersID" || got[1] != "task" {
		t.Errorf("菜单项应插到「工作台」最前面，实际 %v", got)
	}
	if got := ids[""]; len(got) != 1 || got[0] != "home" {
		t.Errorf("置顶组不应被动过，实际 %v", got)
	}
}

// TestAddToUntitledGroup 不传分组时插到置顶组（首页那组），且标题/图标有默认值。
func TestAddToUntitledGroup(t *testing.T) {
	root := newFixture(t)
	res, err := Add(Options{Root: root, ID: "about", Label: "关于"})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	reg := read(t, root, "internal/view/registry.go")
	ids := groupItemIDs(t, reg)
	if got := ids[""]; len(got) != 2 || got[0] != "pageAboutID" {
		t.Errorf("应插到置顶组，实际 %v", got)
	}
	if !strings.Contains(reg, `{id: pageAboutID, label: "关于", icon: icons.Square, build: (*App).aboutPage},`) {
		t.Errorf("未指定图标时应回退 Square\n%s", reg)
	}
	if res.IDConst != "pageAboutID" {
		t.Errorf("id 常量 = %q", res.IDConst)
	}
}

// TestAddPolicy 生命周期参数进注册表。
func TestAddPolicy(t *testing.T) {
	root := newFixture(t)
	if _, err := Add(Options{
		Root: root, ID: "order-form", Label: "订单表单", Group: "工作台", Policy: "fresh",
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	reg := read(t, root, "internal/view/registry.go")
	want := `{id: pageOrderFormID, label: "订单表单", icon: icons.Square, build: (*App).orderFormPage, policy: pagePolicy{keep: keepFresh}},`
	if !strings.Contains(reg, want) {
		t.Errorf("注册表缺少 keepFresh 策略 %q\n%s", want, reg)
	}
}

// TestAddValidation 非法参数给出可操作的中文错误。
func TestAddValidation(t *testing.T) {
	root := newFixture(t)
	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"bad id", Options{ID: "Orders!", Label: "x"}, "不合法"},
		{"bad id underscore", Options{ID: "order_list", Label: "x"}, "不合法"},
		{"empty label", Options{ID: "orders"}, "标题"},
		{"bad policy", Options{ID: "orders", Label: "x", Policy: "forever"}, "未知的生命周期"},
		{"unknown group", Options{ID: "orders", Label: "x", Group: "不存在"}, "现有分组"},
		{"unknown icon", Options{ID: "orders", Label: "x", Icon: "NoSuchIcon"}, "不存在"},
	}
	for _, c := range cases {
		c.opts.Root = root
		if _, err := Add(c.opts); err == nil {
			t.Errorf("%s：应报错", c.name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误信息应包含 %q，实际 %v", c.name, c.want, err)
		}
	}
	// 失败的调用不能留下页面文件。
	if _, err := os.Stat(filepath.Join(root, "internal/view/page_orders.go")); err == nil {
		t.Error("校验失败时不应创建页面文件")
	}
}

// TestAddDuplicate 重复添加同一个 id 应被拦住（页面文件与注册表都查）。
func TestAddDuplicate(t *testing.T) {
	root := newFixture(t)
	opts := Options{Root: root, ID: "orders", Label: "订单管理", Group: "工作台"}
	if _, err := Add(opts); err != nil {
		t.Fatalf("首次 Add: %v", err)
	}
	if _, err := Add(opts); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Errorf("重复添加应报「已存在」，实际 %v", err)
	}

	// 注册表里已有同名 id（手工加的）也要拦住，即使页面文件被删了。
	regPath := filepath.Join(root, "internal/view/registry.go")
	reg, err := os.ReadFile(regPath)
	if err != nil {
		t.Fatal(err)
	}
	want := `{id: pageOrdersID, label: "订单管理", icon: icons.Square, build: (*App).ordersPage},`
	patch := strings.Replace(string(reg), want,
		`{id: "orders", label: "订单管理", icon: icons.Square},`, 1)
	if patch == string(reg) {
		t.Fatal("fixture 替换失败")
	}
	if err := os.WriteFile(regPath, []byte(patch), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "internal/view/page_orders.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := Add(Options{Root: root, ID: "orders", Label: "订单管理", Group: "工作台"}); err == nil ||
		!strings.Contains(err.Error(), "已经有") {
		t.Errorf("注册表已有该 id 时应报错，实际 %v", err)
	}
}

// TestAddRequiresGoMod 不在项目根目录执行时给出明确提示。
func TestAddRequiresGoMod(t *testing.T) {
	root := t.TempDir()
	if _, err := Add(Options{Root: root, ID: "orders", Label: "x"}); err == nil ||
		!strings.Contains(err.Error(), "go.mod") {
		t.Errorf("缺 go.mod 时应报错，实际 %v", err)
	}
}

// TestCamel kebab-case → CamelCase（页面方法名再转成小写开头）。
func TestCamel(t *testing.T) {
	for in, want := range map[string]string{
		"orders":      "Orders",
		"stock-alert": "StockAlert",
		"a1-b2":       "A1B2",
	} {
		if got := camel(in); got != want {
			t.Errorf("camel(%q) = %q，期望 %q", in, got, want)
		}
	}
	if got := lowerFirst("StockAlertPage"); got != "stockAlertPage" {
		t.Errorf("lowerFirst = %q", got)
	}
}
