// Command newpage 新增一个菜单页面：生成 internal/view/page_<id>.go，并把菜单项
// 插进 internal/view/registry.go 的对应分组。
//
// 用法（在项目根目录执行）：
//
//	go run ./scripts/newpage -id orders -label 订单管理 -group 工作台 -icon Table
//
// 参数：
//
//	-id      页面 id（小写字母/数字/连字符），同时是菜单 id 与页面状态命名空间
//	-label   侧栏文字 = 页头标题
//	-group   分组标题（如「工作台」）；空 = 置顶、不画分组标题
//	-icon    internal/ui/icons 里的导出名；空 = Square
//	-policy  生命周期 cached / pinned / fresh；空 = cached
//	-dir     项目根目录；空 = 当前目录
//
// 生成后直接 `go tool mygo dev` 就能看到菜单；业务只写在 page_<id>.go 里。
package main

import (
	"flag"
	"fmt"
	"os"

	"bestuav-atlas/internal/pagegen"
)

func main() {
	var opts pagegen.Options
	flag.StringVar(&opts.ID, "id", "", "页面 id（小写字母/数字/连字符）")
	flag.StringVar(&opts.Label, "label", "", "侧栏文字与页头标题")
	flag.StringVar(&opts.Group, "group", "", "菜单分组标题；空 = 置顶")
	flag.StringVar(&opts.Icon, "icon", "", "internal/ui/icons 的导出名；空 = Square")
	flag.StringVar(&opts.Policy, "policy", "", "生命周期 cached / pinned / fresh")
	flag.StringVar(&opts.Root, "dir", "", "项目根目录；空 = 当前目录")
	flag.Parse()

	res, err := pagegen.Add(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "生成失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已生成 %s\n", res.PageFile)
	fmt.Printf("已在 %s 插入菜单项（id 常量 %s）\n", res.Registry, res.IDConst)
	fmt.Println("下一步：go tool mygo dev 查看菜单；业务写在页面文件里，状态用 pageData / a.Prefs()")
}
