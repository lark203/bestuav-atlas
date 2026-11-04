package view

import (
	"strings"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/kit"
)

// 待办页：最小执行单元。快速输入是主路径（回车即存），编辑/关联项目走弹窗。
//
// 与 bestuav 主工程的关系：那边的 `sys_todo` 是工作流待办（引擎派发、推进即消失），
// 这里的待办是人手写的轻量清单（无审批、无流程），两者语义不同，不共用数据。

// todoPage 待办页。
func (a *App) todoPage(c *ui.Context, t *ui.Theme) {
	d := a.atlas()

	ui.Column(c).Gap(kit.PageGap).Children(func() {
		a.atlasBanner(c, t, d)

		// ---- 快速添加 ----
		kit.Card(c, t, func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				input := ui.TextInput(c, &a.atlasUI.todoQuick).
					Grow(1).Placeholder("加一件事，回车即存")
				if input.Submitted() {
					a.quickAddTodo()
				}
			})
			ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, "归属").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				a.projectSelect(c, "快速添加的项目", &a.atlasUI.todoQuickProject)
				ui.Text(c, "优先级").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				ui.Segmented(c, &a.atlasUI.todoQuickPriority, "低", "中", "高")
				ui.Text(c, "四象限").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				choice.Toggle(c, "重要", &a.atlasUI.todoQuickImportant)
				choice.Toggle(c, "紧急", &a.atlasUI.todoQuickUrgent)
				ui.Text(c, "").Grow(1)
				if ui.Button(c, "详细填写…").Clicked() {
					a.openTodoDialog(store.Todo{
						Priority:  a.atlasUI.todoQuickPriority,
						Important: a.atlasUI.todoQuickImportant,
						Urgent:    a.atlasUI.todoQuickUrgent,
					})
				}
				if ui.PrimaryButton(c, "添加").Clicked() {
					a.quickAddTodo()
				}
			})
		})

		// ---- 筛选 ----
		a.todoFilterBar(c, t)

		// ---- 列表 ----
		rows := a.filteredTodos()
		kit.Card(c, t, func() {
			kit.CardHeader(c, t, "待办", "勾选即完成；截止时间过了会标红并显示逾期时长。", nil)
			if len(rows) == 0 {
				kit.EmptyHint(c, t, "这里还没有待办。在上面输入框里写一件小事，回车即可。")
				return
			}
			ui.Column(c).Gap(2).Children(func() {
				for _, td := range rows {
					a.todoRow(c, t, td)
				}
			})
		})
	})
}

// todoFilterBar 状态 / 项目 / 搜索三个筛选。
func (a *App) todoFilterBar(c *ui.Context, t *ui.Theme) {
	u := &a.atlasUI
	if u.todoFilter == "" {
		u.todoFilter = "全部"
	}
	if u.todoProject == "" {
		u.todoProject = "全部项目"
	}
	filterIdx := 0
	for i, s := range []string{"全部", TodoOpen, TodoDoing, TodoDone} {
		if s == u.todoFilter {
			filterIdx = i
		}
	}
	projectOpts := append([]string{"全部项目"}, a.atlas().projectOptions()[1:]...)

	ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Column(c).Shrink(0).Children(func() {
			ui.Segmented(c, &filterIdx, "全部", TodoOpen, TodoDoing, TodoDone)
		})
		if got := []string{"全部", TodoOpen, TodoDoing, TodoDone}[filterIdx]; got != u.todoFilter {
			u.todoFilter = got
		}
		choice.Dropdown(c, "项目筛选", projectOpts, &u.todoProject)
		ui.TextInput(c, &u.todoSearch).Width(200).Placeholder("搜索标题 / 备注")
	})
}

// filteredTodos 按筛选条件过滤后的待办（顺序由 atlasCache.sortedTodos 保证）。
func (a *App) filteredTodos() []store.Todo {
	d := a.atlas()
	u := &a.atlasUI
	q := strings.ToLower(strings.TrimSpace(u.todoSearch))
	out := make([]store.Todo, 0, len(d.todos))
	for _, td := range d.sortedTodos() {
		if u.todoFilter != "" && u.todoFilter != "全部" && td.Status != u.todoFilter {
			continue
		}
		if u.todoProject != "" && u.todoProject != "全部项目" &&
			d.projectNameOrDash(td.ProjectID) != u.todoProject {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(td.Title+" "+td.Note), q) {
			continue
		}
		out = append(out, td)
	}
	return out
}

// todoRow 单条待办：勾选框 + 标题 + 归属/仓库/优先级/截止日 + 操作。
func (a *App) todoRow(c *ui.Context, t *ui.Theme, td store.Todo) {
	done := td.Status == TodoDone

	ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(7, 8).Radius(8).Children(func() {
		check := ui.Checkbox(c, &done, "")
		check.Tooltip("标记为" + map[bool]string{true: "未完成", false: "已完成"}[done])
		if check.Changed() {
			next := TodoDone
			if !done {
				next = TodoOpen
			}
			a.setTodoStatus(td.ID, next)
		}

		title := ui.Text(c, td.Title).Grow(1).SingleLine()
		if done {
			title.Strikethrough().TextColor(t.TextMuted)
		}

		// 元信息按「信息密度从高到低」排：项目 > 仓库 > 优先级 > 截止日。
		if name := a.atlas().projectName(td.ProjectID); name != "" {
			kit.Pill(c, t, name, t.Accent)
		}
		if name := a.atlas().repoName(td.RepoID); name != "" {
			ui.Text(c, name).FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
		}
		if td.Priority > 0 {
			label, col := "中", t.Warning
			if td.Priority >= 2 {
				label, col = "高", t.Danger
			}
			kit.Pill(c, t, label, col)
		}
		if td.Important {
			kit.Pill(c, t, "重要", t.Danger)
		}
		if td.Urgent {
			kit.Pill(c, t, "紧急", t.Warning)
		}
		if label, overdue := countdownText(td.DueAt); label != "" {
			col := t.TextMuted
			if overdue {
				col = t.Danger
			}
			ui.Text(c, label).FontSize(t.FontSize - 2).TextColor(col).SingleLine()
		}

		if smallButton(c, "编辑").Clicked() {
			a.openTodoDialog(td)
		}
		if smallButton(c, "删除").Clicked() {
			if a.atlasUI.confirmTodo == td.ID {
				if a.deleteTodo(td.ID) {
					a.toast("已删除待办")
				}
				return
			}
			a.atlasUI.confirmTodo = td.ID
			a.toast("再点一次「删除」确认")
		}
	})
}

// quickAddTodo 把快速输入框里的文字存成一条待办。
func (a *App) quickAddTodo() {
	u := &a.atlasUI
	title := strings.TrimSpace(u.todoQuick)
	if title == "" {
		return
	}
	td := store.Todo{
		Title:     title,
		ProjectID: a.atlas().projectIDByName(u.todoQuickProject),
		Priority:  u.todoQuickPriority,
		Important: u.todoQuickImportant,
		Urgent:    u.todoQuickUrgent,
		Status:    TodoOpen,
	}
	if !a.saveTodo(td) {
		return
	}
	u.todoQuick = ""
	// 刚加的那条要立刻看得见：如果当前筛选是「已完成」，切回「全部」。
	if u.todoFilter == TodoDone {
		u.todoFilter = "全部"
	}
	a.requestFrame()
}

// todoEmpty 空列表提示（首页也复用这句文案的语义）。
func todoEmptyHint(t *ui.Theme) string {
	return "还没有待办。在上面输入一件小事，回车就存下来。"
}
