package view

import (
	"time"

	"github.com/ELY-Open-Source/CrychicUI/display"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/kit"
)

// 首页（总览）：回答「现在该干什么」。
//
// 三块信息，按「要不要动手」排序：
//  1. 四个指标：未完成待办 / 项目 / 仓库 / 需要处理的仓库；
//  2. 今天要做的待办（截止日最近的前几条）；
//  3. 状态不正常的仓库（待提交、需要更新、错误…）。
//
// 不用折线图与趋势：本地单机数据只有「当前状态」有决策价值，历史趋势没有采集，
// 硬画一张只会是装饰。

// overviewPage 首页。
func (a *App) overviewPage(c *ui.Context, t *ui.Theme) {
	d := a.atlas()
	today := time.Now()
	dueToday, overdue := a.todoHeat()

	ui.Column(c).Gap(kit.PageGap).Children(func() {
		a.atlasBanner(c, t, d)

		// ---- 问候与全局入口 ----
		ui.Row(c).Gap(kit.RowGap).AlignItems(ui.End).Wrap().Children(func() {
			ui.Column(c).Gap(6).Grow(1).Children(func() {
				ui.Text(c, greeting()+"，"+appmeta.Title).FontSize(24).Bold()
				ui.Text(c, overviewSubtitle(today, dueToday, overdue)).
					TextColor(t.TextMuted)
			})
			ui.Row(c).Gap(8).Shrink(0).Children(func() {
				if ui.Button(c, "加待办").Clicked() {
					a.page = "todos"
				}
				if ui.PrimaryButton(c, "导入仓库").Clicked() {
					a.openImportDialog()
				}
			})
		})

		// ---- 指标（display.Stat：标签 + 滚动数字）----
		needWork := d.stats.ReposDirty + d.stats.ReposBehind + d.stats.ReposAhead
		kit.Card(c, t, func() {
			ui.Row(c).Gap(28).AlignItems(ui.Center).Wrap().Children(func() {
				display.Stat(c, "未完成待办", float64(d.stats.TodosOpen)).Grow(1)
				display.Stat(c, "项目", float64(d.stats.Projects)).Grow(1)
				display.Stat(c, "仓库", float64(d.stats.Repos)).Grow(1)
				display.Stat(c, "需要处理", float64(needWork)).Grow(1)
			})
		})

		if len(d.projects) == 0 && len(d.repos) == 0 && len(d.todos) == 0 {
			a.overviewOnboarding(c, t)
			return
		}

		// ---- 待办 + 仓库两栏 ----
		// 用 Grid（2:1 轨道）而不是 Row + Grow：
		//   - 同一行的卡片由 Grid 的默认 Stretch 拉到同高，高度永远一致；
		//   - 两列宽度严格 2:1，不会因为哪边文字长就变宽。
		// 内容区窄于 700 时改成上下堆叠：再按 2:1 分，右栏只剩两百出头，
		// 仓库名与按钮会挤成一团。
		if a.stacked(c, 700) {
			ui.Column(c).Gap(kit.PageGap).Children(func() {
				a.overviewTodos(c, t)
				a.overviewRepos(c, t)
			})
		} else {
			ui.Grid(c).ColumnTracks(ui.Fr(2), ui.Fr(1)).Gap(kit.RowGap).Children(func() {
				a.overviewTodos(c, t)
				a.overviewRepos(c, t)
			})
		}

		// ---- 四象限 ----
		a.quadrantsCard(c, t)
	})
}

// quadrantDefs 四象限的标题、做事原则与配色。
var quadrantDefs = [4]struct {
	Title string
	Hint  string
}{
	{"重要且紧急", "马上做"},
	{"重要不紧急", "排进计划"},
	{"紧急不重要", "尽快清掉"},
	{"不紧急不重要", "有空再说"},
}

// quadrantsCard 艾森豪威尔四象限：用「重要 × 紧急」把未完成待办分到四格。
//
// 为什么放在首页最下方：它是「今天怎么排」的总览，看完今日待办与仓库之后，
// 再把没排上的事分个优先级——顺序上刚好是从「具体」到「取舍」。
func (a *App) quadrantsCard(c *ui.Context, t *ui.Theme) {
	var groups [4][]store.Todo
	for _, td := range a.atlas().sortedTodos() {
		if td.Status == TodoDone {
			continue
		}
		q := quadrantOf(td) - 1
		groups[q] = append(groups[q], td)
	}

	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "四象限",
			"重要 × 紧急。先做左上；右上（重要不紧急）是长期价值，别让它一直拖成左上。拖动待办可换象限。", nil)
		kit.RowGrid(c, a.columnsFor(c, 300, 2), func() {
			for i := range quadrantDefs {
				a.quadrantCell(c, t, i, groups[i])
			}
		})
	})
}

// quadrantDrag 是四象限拖拽的值：只带待办 id，落在哪一格由目标格决定。
type quadrantDrag struct{ id int64 }

// quadrantCell 一个象限格子：标题 + 数量 + 至多四条待办。
//
// 整格是拖放目标：从别的格子把待办拖进来，按落点格子的「重要 × 紧急」
// 重新写 important / urgent。格子上高亮只在拖动经过时出现，不改变平时外观。
func (a *App) quadrantCell(c *ui.Context, t *ui.Theme, i int, todos []store.Todo) {
	def := quadrantDefs[i]
	col := []ui.Color{t.Danger, t.Accent, t.Warning, t.TextMuted}[i]

	zone := ui.Column(c).Gap(6).Padding(12).Radius(8).Label("象限：" + def.Title)
	_, over := ui.DragOver[quadrantDrag](zone)
	bg, edge := 0.07, 0.22
	if over {
		bg, edge = 0.16, 0.60
	}
	zone.Background(kit.Alpha(col, bg)).Border(1, kit.Alpha(col, edge))
	zone.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, def.Title).Bold().TextColor(col).SingleLine()
			ui.Text(c, itoa(len(todos))).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, "").Grow(1)
			ui.Text(c, def.Hint).FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
		})
		if len(todos) == 0 {
			kit.EmptyHint(c, t, "这一格是空的")
			return
		}
		// 每格最多四条：象限是「一眼看清轻重」，不是第二张待办列表。
		limit := len(todos)
		if limit > 4 {
			limit = 4
		}
		for _, td := range todos[:limit] {
			a.quadrantTodoRow(c, t, td)
		}
		if len(todos) > 4 {
			ui.Text(c, "还有 "+itoa(len(todos)-4)+" 条，去待办页看").
				FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		}
	})
	if d, ok := ui.Drop[quadrantDrag](zone); ok {
		a.moveTodoToQuadrant(d.id, i)
	}
}

// quadrantTodoRow 四象限里的一行待办：勾选 + 标题 + 倒计时。
func (a *App) quadrantTodoRow(c *ui.Context, t *ui.Theme, td store.Todo) {
	done := td.Status == TodoDone
	row := ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(3, 2).
		Label("四象限待办：" + td.Title).
		Tooltip("拖动到其它象限可改变「重要 / 紧急」").
		Cursor(ui.CursorGrab).Drag(quadrantDrag{id: td.ID})
	if row.Dragging() {
		row.Opacity(0.4)
	}
	row.Children(func() {
		check := ui.Checkbox(c, &done, "")
		if check.Changed() && done {
			a.setTodoStatus(td.ID, TodoDone)
		}
		tooltipIfSet(ui.Text(c, td.Title).Grow(1).SingleLine().FontSize(t.FontSize-2), td.Note)
		if label, overdue := countdownText(td.DueAt); label != "" {
			col := t.TextMuted
			if overdue {
				col = t.Danger
			}
			ui.Text(c, label).FontSize(t.FontSize - 2).TextColor(col).SingleLine()
		}
	})
}

// moveTodoToQuadrant 把待办移到第 i 个象限（0 基），并写回重要/紧急。
func (a *App) moveTodoToQuadrant(id int64, i int) {
	if i < 0 || i >= len(quadrantDefs) {
		return
	}
	td, ok := a.todoByID(id)
	if !ok {
		return
	}
	important := i == 0 || i == 1
	urgent := i == 0 || i == 2
	if td.Important == important && td.Urgent == urgent {
		return
	}
	td.Important, td.Urgent = important, urgent
	if a.saveTodo(td) {
		a.toast("已移到「" + quadrantDefs[i].Title + "」")
	}
}

// overviewSubtitle 一句「今天什么情况」。
func overviewSubtitle(now time.Time, dueToday, overdue int) string {
	base := now.Format("2006 年 01 月 02 日") + " · " + weekdayCN(now)
	switch {
	case overdue > 0:
		return base + " · 有 " + itoa(overdue) + " 件已逾期，先处理它们"
	case dueToday > 0:
		return base + " · 今天有 " + itoa(dueToday) + " 件到期"
	default:
		return base + " · 今天没有到期的待办"
	}
}

// weekdayCN 星期几的中文名。
func weekdayCN(t time.Time) string {
	names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return names[int(t.Weekday())]
}

// overviewTodos 首页的待办列表（最多 6 条）。
func (a *App) overviewTodos(c *ui.Context, t *ui.Theme) {
	open := a.openTodos(6)
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "要做的", "按截止日与优先级排序，勾选即完成。", func() {
			if ui.Button(c, "全部待办").Clicked() {
				a.page = "todos"
			}
		})
		if len(open) == 0 {
			kit.EmptyHint(c, t, "没有未完成的待办。去「待办」页加一条？")
			return
		}
		ui.Column(c).Gap(2).Children(func() {
			for _, td := range open {
				a.overviewTodoRow(c, t, td)
			}
		})
	}).Label("总览·要做的")
}

// overviewTodoRow 首页的待办行：只保留勾选、标题、项目、截止日。
func (a *App) overviewTodoRow(c *ui.Context, t *ui.Theme, td store.Todo) {
	done := false
	ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(6, 6).Radius(8).Children(func() {
		check := ui.Checkbox(c, &done, "")
		if check.Changed() && done {
			a.setTodoStatus(td.ID, TodoDone)
		}
		tooltipIfSet(ui.Text(c, td.Title).Grow(1).SingleLine(), td.Note)
		if name := a.atlas().projectName(td.ProjectID); name != "" {
			kit.Pill(c, t, name, t.Accent)
		}
		if label, overdue := countdownText(td.DueAt); label != "" {
			col := t.TextMuted
			if overdue {
				col = t.Danger
			}
			ui.Text(c, label).FontSize(t.FontSize - 2).TextColor(col).SingleLine()
		}
		if td.Important {
			kit.Pill(c, t, "重要", t.Danger)
		}
	})
}

// overviewRepos 首页的「需要处理的仓库」（状态不正常的，最多 6 条）。
func (a *App) overviewRepos(c *ui.Context, t *ui.Theme) {
	problem := a.problemRepos(6)
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "仓库", "状态不正常的排前面。", func() {
			if ui.Button(c, "全部仓库").Clicked() {
				a.page = "repos"
			}
		})
		if len(problem) == 0 {
			kit.EmptyHint(c, t, "所有仓库状态正常。")
			return
		}
		ui.Column(c).Gap(2).Children(func() {
			for _, r := range problem {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(6, 6).Radius(8).Children(func() {
					tooltipIfSet(ui.Text(c, r.Name).Grow(1).SingleLine().FontSize(t.FontSize-2), r.Review)
					kit.Pill(c, t, r.Status, repoStatusColor(t, r.Status))
					if r.Status == RepoBehind {
						if smallButton(c, "拉取").Clicked() {
							a.runRepoBatch(batchPull, []int64{r.ID}, "")
						}
					}
				})
			}
		})
		if n := len(a.problemRepos(999)); n > 6 {
			ui.Text(c, "另有 "+itoa(n-6)+" 个，去仓库页处理。").
				FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		}
	}).Label("总览·仓库")
}

// overviewOnboarding 第一次打开时的三步引导（没有任何数据才出现）。
func (a *App) overviewOnboarding(c *ui.Context, t *ui.Theme) {
	kit.RowGrid(c, a.columnsFor(c, 240, 3), func() {
		a.onboardStep(c, t, "01", "建一个项目",
			"项目是仓库与待办的归属单位，先按「一件事」分组。",
			"新建项目", func() { a.openProjectDialog(store.Project{}) })
		a.onboardStep(c, t, "02", "导入仓库",
			"一次贴多行 owner/repo，或读取 GitHub 上的仓库列表。",
			"导入仓库", func() { a.openImportDialog() })
		a.onboardStep(c, t, "03", "记下第一件待办",
			"待办是执行单元；关联到项目/仓库后，首页会替你排队。",
			"去待办页", func() { a.page = "todos" })
	})
}

// onboardStep 引导卡片。
func (a *App) onboardStep(c *ui.Context, t *ui.Theme, no, title, desc, action string, fn func()) {
	kit.Card(c, t, func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, no).FontSize(20).Bold().TextColor(kit.Alpha(t.Accent, 0.85))
			ui.Text(c, title).Bold()
			ui.Text(c, desc).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			ui.Row(c).Children(func() {
				if ui.Button(c, action).Clicked() {
					fn()
				}
			})
		})
	})
}

// ---- 首页用到的查询 ----

// openTodos 未完成的待办，最多 n 条（排序沿用库里的顺序）。
func (a *App) openTodos(n int) []store.Todo {
	out := make([]store.Todo, 0, n)
	for _, td := range a.atlas().sortedTodos() {
		if td.Status == TodoDone {
			continue
		}
		out = append(out, td)
		if len(out) >= n {
			break
		}
	}
	return out
}

// problemRepos 状态不正常的仓库，最多 n 条。
func (a *App) problemRepos(n int) []store.Repo {
	out := make([]store.Repo, 0, n)
	for _, r := range a.atlas().sortedRepos() {
		if r.Status == RepoOK || r.Status == "" {
			continue
		}
		out = append(out, r)
		if len(out) >= n {
			break
		}
	}
	return out
}

// todoHeat 今天到期与已逾期的待办数。
func (a *App) todoHeat() (dueToday, overdue int) {
	now := time.Now()
	for _, td := range a.atlas().todos {
		if td.Status == TodoDone || td.DueAt.IsZero() {
			continue
		}
		if sameDay(td.DueAt, now) {
			dueToday++
			continue
		}
		if td.DueAt.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) {
			overdue++
		}
	}
	return dueToday, overdue
}

// greeting 按当前时段问候。
func greeting() string {
	switch h := time.Now().Hour(); {
	case h < 6:
		return "夜深了"
	case h < 11:
		return "早上好"
	case h < 14:
		return "中午好"
	case h < 18:
		return "下午好"
	default:
		return "晚上好"
	}
}
