package view

import (
	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/egoist/mygo/ui"
)

// 本文件把「页头要展示什么」从各页面里收拢到一处：
//
//   - 副标题：任务名 / 拓扑页的项目名 / 仓库详情名；
//   - Extras：拓扑页的项目切换下拉框（页头固定高度，组件不会把标题栏撑高）；
//   - Tabs：设置页的四个分组、布局示例页的三个 tab（有 tab 时副标题让位）；
//   - Fill：三段式布局（左右两栏）不套统一滚动，由页面自己管理栏内滚动。

// pageSubtitle 页头副标题。
func (a *App) pageSubtitle() string {
	// 轻量任务执行时优先显示任务名（与页头下方的进度条呼应）。
	if a.taskRunning && a.taskLabel != "" {
		return a.taskLabel
	}
	switch a.page {
	case "topology":
		if pid := a.atlasUI.graphProject; pid != 0 {
			return a.atlas().projectName(pid)
		}
	case "repo-detail":
		if r, ok := a.repoByID(a.atlasUI.detailID); ok {
			return r.Name
		}
	case "layout-split":
		if a.demoSel > 0 && a.demoSel <= len(demoProjects) {
			return demoProjects[a.demoSel-1].name
		}
		return "未选择"
	}
	return ""
}

// pageExtras 页头自定义组件：目前用于拓扑页的项目范围下拉框。
//
// 返回 nil 表示这一页没有额外组件；容器高度由 shell 锁定为页头高度，
// 下拉框/文本/小控件都只占一行，不会改变标题栏高度。
func (a *App) pageExtras(c *ui.Context, t *ui.Theme) func() {
	if a.page != "topology" {
		return nil
	}
	return func() {
		d := a.atlas()
		opts := make([]string, 0, len(d.projects)+1)
		opts = append(opts, "全部项目")
		for _, p := range d.projects {
			opts = append(opts, p.Name)
		}
		cur := "全部项目"
		if pid := a.atlasUI.graphProject; pid != 0 {
			cur = d.projectName(pid)
		}
		choice.Dropdown(c, "拓扑范围", opts, &cur)
		if cur == "" || cur == "全部项目" {
			if a.atlasUI.graphProject != 0 {
				a.atlasUI.graphProject = 0
				a.atlasUI.graph.reset()
				a.requestFrame()
			}
			return
		}
		if id := d.projectIDByName(cur); id != a.atlasUI.graphProject {
			a.atlasUI.graphProject = id
			a.atlasUI.graph.reset()
			a.requestFrame()
		}
	}
}

// pageTabs 页头 tab：返回 (标签, 当前下标, 切换回调)；nil 表示这一页不用 tab。
func (a *App) pageTabs() ([]string, int, func(int)) {
	switch a.page {
	case settingsPageID:
		return settingsGroupLabels(), a.settingsGroup(), a.setSettingsGroup
	case "layout-tabs":
		return []string{"概览", "明细", "设置"}, a.demoTab, func(i int) {
			a.demoTab = i
			a.requestFrame()
		}
	}
	return nil, 0, nil
}

// pageFill 是否需要「内容区不套统一滚动」：三段式布局自己管左右两栏的滚动。
func (a *App) pageFill() bool {
	return a.page == "layout-split"
}

// demoProjects 布局示例用的静态数据（不碰数据库，纯展示）。
var demoProjects = []struct {
	name string
	desc string
	meta string
}{
	{"星图", "把待办、项目与多个仓库摊在一张图上的桌面工作台。", "研发 · 3 个仓库 · 活跃"},
	{"量子科学桌面端", "内部工具链的桌面入口，负责配置与诊断。", "工具 · 1 个仓库 · 维护中"},
	{"数据同步", "跨机器同步配置与数据的辅助项目。", "基础设施 · 2 个仓库 · 暂停"},
	{"文档站", "使用说明与发布手册的静态站点。", "文档 · 0 个仓库 · 活跃"},
	{"实验沙盒", "临时验证新想法的空间。", "实验 · 1 个仓库 · 归档"},
}
