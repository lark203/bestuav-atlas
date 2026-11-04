package view

import (
	"strings"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/data"
	"github.com/ELY-Open-Source/CrychicUI/display"
	"github.com/ELY-Open-Source/CrychicUI/feedback"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/ui/kit"
)

// 仓库页：本产品的核心界面。
//
// 设计约束来自需求本身——「十来个仓库，一次性导入，表格展示，状态一眼看得出，
// 能批量更新、批量提交推送」。于是：
//   - 表格是主体，一屏一行一个仓库，列固定（仓库/项目/分支/状态/差异/最近提交/操作）；
//   - 状态是算出来的（见 atlas_data.go 的优先级），颜色即语义；
//   - 批量动作只在「有选中」时出现，避免误触；
//   - 所有联网/写操作都要用户点一下（没有定时器、没有自动 fetch）。

// repoCols 仓库表的列。
//
// 固定的「状态/评分/差异/操作」给足宽度，其余一律让位给仓库名——名字被截成
// 「lark203/bestu…」时两张行看起来一模一样，那才是真的难用。
var repoCols = []data.Column{
	{Title: "仓库", Width: 210},
	{Title: "平台", Width: 76},
	{Title: "项目", Width: 84},
	{Title: "分支", Width: 70},
	{Title: "状态", Width: 92},
	{Title: "评分", Width: 84},
	{Title: "差异", Width: 76},
	{Title: "最近提交"},
	{Title: "操作", Width: 200, End: true},
}

// ratingStars 把 0–5 的评分画成五颗星（0 分显示「—」，表示还没评）。
func ratingStars(n int) string {
	n = clampRating(n)
	if n == 0 {
		return "—"
	}
	return strings.Repeat("★", n) + strings.Repeat("☆", 5-n)
}

// repoPage 仓库页。
func (a *App) repoPage(c *ui.Context, t *ui.Theme) {
	d := a.atlas()

	ui.Column(c).Gap(kit.PageGap).Children(func() {
		a.atlasBanner(c, t, d)

		// ---- 顶部：统计 + 主操作 ----
		ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Gap(4).Grow(1).Children(func() {
				ui.Text(c, "把本地仓库与远端状态摊在一张表里").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			})
			ui.Row(c).Gap(8).Shrink(0).Children(func() {
				if ui.Button(c, "刷新状态").Clicked() {
					a.runRepoBatch(batchInspect, a.allRepoIDs(), "")
				}
				if ui.Button(c, "刷新远端信息").Clicked() {
					a.runRepoBatch(batchRemote, a.selectionOrAll(), "")
				}
				if ui.PrimaryButton(c, "导入仓库").Clicked() {
					a.openImportDialog()
				}
			})
		})

		// ---- 四个状态计数（display.Stat：标签 + 滚动数字）----
		kit.Card(c, t, func() {
			ui.Row(c).Gap(28).AlignItems(ui.Center).Wrap().Children(func() {
				display.Stat(c, "仓库总数", float64(d.stats.Repos)).Grow(1)
				display.Stat(c, "待提交", float64(d.stats.ReposDirty)).Grow(1)
				display.Stat(c, "需要更新", float64(d.stats.ReposBehind)).Grow(1)
				display.Stat(c, "待推送", float64(d.stats.ReposAhead)).Grow(1)
			})
		})

		// ---- 筛选 + 批量条 ----
		rows := a.filteredRepos()
		a.repoFilterBar(c, t, rows)
		a.repoBulkBar(c, t)

		// ---- 表格 ----
		if len(rows) == 0 {
			a.repoEmptyState(c, t)
		} else {
			kit.Card(c, t, func() { a.repoTable(c, t, rows) })
		}
	})
}

// repoFilterBar 搜索与状态/项目筛选。
func (a *App) repoFilterBar(c *ui.Context, t *ui.Theme, rows []store.Repo) {
	u := &a.atlasUI
	if u.repoStatus == "" {
		u.repoStatus = "全部"
	}
	if u.repoProject == "" {
		u.repoProject = "全部项目"
	}
	statusOpts := append([]string{"全部"}, RepoStatuses...)
	projectOpts := append([]string{"全部项目"}, a.atlas().projectOptions()[1:]...)

	ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
		ui.TextInput(c, &u.repoSearch).Width(220).Placeholder("搜索仓库 / 路径 / 备注")
		choice.Dropdown(c, "状态筛选", statusOpts, &u.repoStatus)
		choice.Dropdown(c, "项目筛选", projectOpts, &u.repoProject)
		ui.Text(c, itoa(len(rows))+" 个仓库").TextColor(t.TextMuted).
			FontSize(t.FontSize - 2).Margin(6, 0, 0, 0)
	})
}

// repoBulkBar 批量操作条：用 CrychicUI 的 data.BulkBar（有选中时从下方滑出）。
//
// chosen 用局部变量：BulkBar 在点击的那一帧把被点的按钮下标写进来，本函数立刻
// 分发并结束——状态不需要跨帧，也就不会出现「上一次点的按钮」残留。
func (a *App) repoBulkBar(c *ui.Context, t *ui.Theme) {
	selected := a.selectedRepoIDs()
	chosen := -1
	_, clear := data.BulkBar(c, len(selected), []data.Action{
		{Label: "批量拉取", Icon: "download"},
		{Label: "批量提交并推送", Icon: "upload"},
		{Label: "刷新状态", Icon: "refresh-cw"},
		{Label: "刷新远端信息", Icon: "git-commit"},
	}, &chosen, data.BulkBarOptions{Selected: "已选 %d 个仓库", Clear: "清除选择"})

	if clear.Clicked() {
		a.clearRepoSelection()
	}
	switch chosen {
	case 0:
		a.runRepoBatch(batchPull, selected, "")
	case 1:
		a.openCommitDialog(selected)
	case 2:
		a.runRepoBatch(batchInspect, selected, "")
	case 3:
		a.runRepoBatch(batchRemote, selected, "")
	}
}

// repoTable 仓库表本体（SelectTable：表头全选 + 每行勾选）。
func (a *App) repoTable(c *ui.Context, t *ui.Theme, rows []store.Repo) {
	u := &a.atlasUI
	if u.sel == nil {
		u.sel = make(map[int64]bool, len(rows))
	}

	// 把「按 id 的选择」投影成 SelectTable 需要的「按行号的选择」，绘制完再写回。
	// 这样筛选、排序变化不会让选中项错位到别的仓库上。
	sel := ui.Selection[int]{}
	for i, r := range rows {
		if u.sel[r.ID] {
			sel.Add(i)
		}
	}

	data.SelectTable(c, "仓库列表", repoCols, len(rows), &sel,
		func(row, col int) {
			r := rows[row]
			switch col {
			case 0:
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, r.Name).SingleLine().Bold()
					sub := firstNonEmpty(r.LocalPath, r.URL)
					if sub != "" {
						ui.Text(c, sub).FontSize(t.FontSize-2).TextColor(t.TextMuted).SingleLine()
					}
				})
			case 1:
				p := platformForRepo(r)
				lbl := ui.Text(c, p.Label()).SingleLine().FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				if p.Host != "" {
					lbl.Tooltip(p.Host)
				}
			case 2:
				ui.Text(c, a.atlas().projectNameOrDash(r.ProjectID)).SingleLine()
			case 3:
				ui.Text(c, firstNonEmpty(r.Branch, "—")).SingleLine().TextColor(t.TextMuted)
			case 4:
				pill := kit.Pill(c, t, r.Status, repoStatusColor(t, r.Status))
				if r.StatusMsg != "" {
					pill.Tooltip(r.StatusMsg)
				}
			case 5:
				stars := ui.Text(c, ratingStars(r.Importance)).
					FontSize(t.FontSize - 2).TextColor(t.Warning).SingleLine()
				stars.Tooltip("重要 " + itoa(r.Importance) + " · 可学习 " + itoa(r.Learning) + " · 难度 " + itoa(r.Difficulty))
			case 6:
				ui.Text(c, repoDiffText(r)).FontSize(t.FontSize - 2).SingleLine().TextColor(t.TextMuted)
			case 7:
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, firstNonEmpty(r.LastCommit, "—")).SingleLine().FontSize(t.FontSize - 2)
					if !r.LastCommitAt.IsZero() {
						ui.Text(c, r.LastCommitAt.Local().Format("2006-01-02 15:04")).
							FontSize(t.FontSize-2).TextColor(t.TextMuted).SingleLine()
					}
				})
			case 8:
				ui.Row(c).Gap(4).AlignItems(ui.Center).Justify(ui.End).Children(func() {
					if smallButton(c, "详情").Clicked() {
						a.openRepoDetail(r)
					}
					if smallButton(c, "拉取").Clicked() {
						a.runRepoBatch(batchPull, []int64{r.ID}, "")
					}
					if smallButton(c, "提交").Clicked() {
						a.openCommitDialog([]int64{r.ID})
					}
					if smallButton(c, "编辑").Clicked() {
						a.openRepoDialog(r)
					}
				})
			}
		},
		data.SelectTableOptions{All: "全选", Row: "选择第 %d 行"})

	for i, r := range rows {
		if sel.Has(i) {
			u.sel[r.ID] = true
		} else {
			delete(u.sel, r.ID)
		}
	}
}

// repoDiffText 一览式差异文案：改动数 + 领先 + 落后。
func repoDiffText(r store.Repo) string {
	var parts []string
	if r.Dirty > 0 {
		parts = append(parts, itoa(r.Dirty)+" 改")
	}
	if r.Ahead > 0 {
		parts = append(parts, "↑"+itoa(r.Ahead))
	}
	if r.Behind > 0 {
		parts = append(parts, "↓"+itoa(r.Behind))
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " · ")
}

// repoEmptyState 一个仓库都没有时的引导（feedback.EmptyState 自带图标、标题、
// 文案与动作按钮，不用自己拼）。
func (a *App) repoEmptyState(c *ui.Context, t *ui.Theme) {
	_, act := feedback.EmptyState(c, "package", "还没有仓库",
		"一次可以导入多个：从本地文件夹导入（自动解析 git 远端），或粘贴 owner/repo / 地址；"+
			"也可以填好平台令牌后直接读取你的仓库列表。",
		"导入仓库")
	if act.Clicked() {
		a.openImportDialog()
	}
}

// ---- 页面共用的小构件 ----

// atlasBanner 数据库不可用 / 读取失败时的提示条。
//
// 刻意不用错误页盖住整个界面：星图的价值是「看得见全局」，某个读操作失败时
// 仍然应该让人看到能用的部分，并在顶部说明哪里出了问题。
func (a *App) atlasBanner(c *ui.Context, t *ui.Theme, d *atlasCache) {
	switch {
	case a.store == nil:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 12).Radius(8).
			Background(kit.Alpha(t.Warning, 0.12)).
			Children(func() {
				ui.Icon(c, icons.Bell).FontSize(14).TextColor(t.Warning)
				ui.Text(c, "本次运行不落库（安全模式或数据库不可用），改动只在内存里，退出即丢。").
					FontSize(t.FontSize-2).TextColor(t.Warning)
			})
	case d.err != nil:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 12).Radius(8).
			Background(kit.Alpha(t.Danger, 0.12)).
			Children(func() {
				ui.Text(c, d.err.Error()).FontSize(t.FontSize-2).TextColor(t.Danger).Grow(1)
				if ui.Button(c, "重试").Clicked() {
					a.reloadAtlas()
				}
			})
	}
}

// smallButton 行内小按钮：低对比、紧凑，不与主按钮抢注意力。
func smallButton(c *ui.Context, label string) ui.Element {
	return ui.Button(c, label).Padding(2, 8)
}

// allRepoIDs 全部仓库的 id（刷新状态用）。
func (a *App) allRepoIDs() []int64 {
	d := a.atlas()
	out := make([]int64, 0, len(d.repos))
	for _, r := range d.repos {
		out = append(out, r.ID)
	}
	return out
}

// selectionOrAll 有选中就用选中的，否则用全部（「刷新远端信息」的默认行为）。
func (a *App) selectionOrAll() []int64 {
	if ids := a.selectedRepoIDs(); len(ids) > 0 {
		return ids
	}
	return a.allRepoIDs()
}

// clearRepoSelection 清空仓库选择。
func (a *App) clearRepoSelection() {
	a.atlasUI.sel = nil
}
