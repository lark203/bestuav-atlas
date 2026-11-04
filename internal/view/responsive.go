package view

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/kit"
	"bestuav-atlas/internal/view/shell"
)

// 本文件是「页面跟着窗口伸缩」的统一入口。
//
// 为什么不由页面各自算：内容区宽度 = 窗口 - 侧栏 - 页面左右留白，这个公式
// 全站只有一份；散落在页面里迟早会出现某个页面忘了减侧栏、在窄窗口里溢出。
// 页面只声明「这一格里我希望至少多宽、最多几列」，其余交给这里。

// contentWidth 内容区可用的逻辑宽度（DIP）。
//
// 保守估计：再减 8 DIP 给滚动条/边框，宁可早一档换行，也不要卡在临界点上
// 出现半截按钮。
func (a *App) contentWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	side := float32(shell.ExpandedWidth)
	if a.sidebarCollapsed {
		side = shell.CollapsedWidth
	}
	return max(w-side-2*kit.PagePadX-8, 240)
}

// columnsFor 按「每格至少 min 宽」算出一行能放几列，夹在 1..maxCols。
//
// 例：项目卡每张至少 260，窗口宽到能放 4 张时也只取 3 列（maxCols=3）。
func (a *App) columnsFor(c *ui.Context, minWidth float32, maxCols int) int {
	if maxCols < 1 {
		maxCols = 1
	}
	if minWidth <= 0 {
		return maxCols
	}
	n := int(a.contentWidth(c) / minWidth)
	if n < 1 {
		n = 1
	}
	if n > maxCols {
		n = maxCols
	}
	return n
}

// stacked 判断是否该把并排的两栏改成上下堆叠。
//
// 阈值是「两栏各自还剩下多少」：例如总览的 2:1 两栏，内容区窄于 700 时右栏
// 只剩两百出头，仓库名和「拉取」按钮会挤在一起——这时堆叠比硬撑更好读。
func (a *App) stacked(c *ui.Context, min float32) bool {
	return a.contentWidth(c) < min
}
