package view

import (
	"github.com/ELY-Open-Source/CrychicUI/feedback"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/ui/kit"
)

// 项目页：把「项目」当成仓库与待办的归属单位来管理。
//
// 这一页刻意不做表格：项目数量少（几个到十几个），卡片能一次显示更多上下文
// （描述、仓库数、待办数、仓库名），比一行四列的表格更有信息量。

// projectPage 项目页。
func (a *App) projectPage(c *ui.Context, t *ui.Theme) {
	d := a.atlas()

	ui.Column(c).Gap(kit.PageGap).Children(func() {
		a.atlasBanner(c, t, d)

		ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Column(c).Gap(4).Grow(1).Children(func() {
				ui.Text(c, "项目是仓库与待办的归属单位").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			})
			if ui.PrimaryButton(c, "新建项目").Clicked() {
				a.openProjectDialog(store.Project{})
			}
		})

		if len(d.projects) == 0 {
			a.projectEmptyState(c, t)
			return
		}

		// 3 / 2 / 1 列随窗口宽度自适应：项目卡里有名称、描述、仓库名与三个按钮，
		// 硬塞 3 列时窄窗口会把按钮挤出去。
		kit.RowGrid(c, a.columnsFor(c, 260, 3), func() {
			for _, p := range d.projects {
				a.projectCard(c, t, p)
			}
		})

		a.unassignedCard(c, t, d)
	})
}

// projectCard 单个项目卡。
func (a *App) projectCard(c *ui.Context, t *ui.Theme, p store.Project) {
	d := a.atlas()
	repos := d.repoCountOf(p.ID)
	todos := d.openTodoCountOf(p.ID)

	kit.Card(c, t, func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, p.Name).FontSize(16).Bold().SingleLine().Grow(1)
			kit.Pill(c, t, firstNonEmpty(p.Status, "进行中"), projectStatusColor(t, p.Status))
		})
		if p.Desc != "" {
			ui.Text(c, p.Desc).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		}

		ui.Row(c).Gap(14).AlignItems(ui.Center).Children(func() {
			stat := func(icon *ui.SVG, n int, label string) {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, icon).FontSize(13).TextColor(t.TextMuted)
					ui.Text(c, itoa(n)+" "+label).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				})
			}
			stat(icons.Code, repos, "仓库")
			stat(icons.Checklist, todos, "待办")
		})

		// 仓库名：只列前三个，多出来的用「+N」带过——卡片不是清单，点进仓库页看全量。
		if names := a.projectRepoNames(p.ID); len(names) > 0 {
			ui.Text(c, stringsJoinLimit(names, 3)).FontSize(t.FontSize - 2).
				TextColor(t.TextMuted).SingleLine()
		}

		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "编辑").Clicked() {
				a.openProjectDialog(p)
			}
			if ui.Button(c, "加仓库").Clicked() {
				a.openImportDialogWithProject(p)
			}
			if ui.Button(c, "看待办").Clicked() {
				a.atlasUI.todoProject = p.Name
				a.page = "todos"
			}
		})
	})
}

// projectStatusColor 项目状态配色：进行中=强调色，暂停=警告，归档=灰。
func projectStatusColor(t *ui.Theme, status string) ui.Color {
	switch status {
	case "暂停":
		return t.Warning
	case "归档":
		return t.TextMuted
	default:
		return t.Accent
	}
}

// projectRepoNames 某个项目下的仓库名（按名称排序）。
func (a *App) projectRepoNames(projectID int64) []string {
	d := a.atlas()
	out := make([]string, 0, 4)
	for _, r := range d.sortedRepos() {
		if r.ProjectID == projectID {
			out = append(out, r.Name)
		}
	}
	return out
}

// stringsJoinLimit 取前 n 项拼接，多出的用「+N」表示。
func stringsJoinLimit(items []string, n int) string {
	if len(items) <= n {
		return join(items, " · ")
	}
	return join(items[:n], " · ") + " +" + itoa(len(items)-n)
}

// join 用 sep 拼接（避免为一处调用引入 strings）。
func join(items []string, sep string) string {
	out := ""
	for i, it := range items {
		if i > 0 {
			out += sep
		}
		out += it
	}
	return out
}

// projectEmptyState 没有项目时的引导（feedback.EmptyState 自带图标、文案与动作）。
func (a *App) projectEmptyState(c *ui.Context, t *ui.Theme) {
	_, act := feedback.EmptyState(c, "folder-kanban", "还没有项目",
		"建议先按「一件事」建几个项目，例如「星图」「量子科学桌面端」「内部工具链」，再把仓库和待办挂上去。",
		"新建第一个项目")
	if act.Clicked() {
		a.openProjectDialog(store.Project{})
	}
}

// unassignedCard 未归属的仓库与待办：数据不丢，但要显式提醒「还没归类」。
func (a *App) unassignedCard(c *ui.Context, t *ui.Theme, d *atlasCache) {
	repos, todos := 0, 0
	for _, r := range d.repos {
		if r.ProjectID == 0 {
			repos++
		}
	}
	for _, td := range d.todos {
		if td.ProjectID == 0 && td.Status != TodoDone {
			todos++
		}
	}
	if repos == 0 && todos == 0 {
		return
	}
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "未归属", "这些仓库/待办还没有挂到项目上，可以在编辑里指定归属。", nil)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			ui.Text(c, itoa(repos)+" 个仓库 · "+itoa(todos)+" 个未完成的待办").
				FontSize(t.FontSize - 2).TextColor(t.TextMuted).Grow(1)
			// 只给「确实有东西可看」的入口：没有未归属仓库时不该出现
			// 「去仓库页」；两类都有就两个按钮都给。
			if repos > 0 {
				if ui.Button(c, "去仓库页").Clicked() {
					a.page = "repos"
				}
			}
			if todos > 0 {
				if ui.Button(c, "去待办页").Clicked() {
					a.page = "todos"
				}
			}
		})
	})
}

// openImportDialogWithProject 打开导入弹窗并预选项目（项目卡上的「加仓库」）。
func (a *App) openImportDialogWithProject(p store.Project) {
	a.openImportDialog()
	a.atlasUI.importProject = p.Name
}
