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

// repoCols 仓库表的列（mygo 原生 ui.Table）。
//
// 为什么从 CrychicUI 的 SelectTable 换成 ui.Table：
//   - 窗口变窄时列总宽超过表格，ui.Table 会自己出现横向滚动条，表头跟着一起
//     滚；SelectTable 只是把超出的列裁掉，用户会以为「后面没有数据」；
//   - 行是虚拟化的，仓库多时只为可见行建元素；
//   - 表头固定，行在下面滚动，不再随整页滚走。
//
// 固定列给足宽度，只有「最近提交」吃剩余空间；名字被截成「lark203/bestu…」
// 时两行看起来一模一样，那才是真的难用。
//
// repoTableWideEnough 是「所有列都能摆下」的内容区宽度：固定列合计 950，
// 「最近提交」至少 150，再加卡片内边距。窄于它时筛选条会提示可以左右滚动。
const repoTableWideEnough = 1136

var repoCols = []ui.TableColumn{
	{Title: "", Width: 42, Fixed: true},
	{Title: "仓库", Width: 210, Fixed: true},
	{Title: "平台", Width: 76, Fixed: true},
	{Title: "项目", Width: 84, Fixed: true},
	{Title: "分支", Width: 70, Fixed: true},
	{Title: "状态", Width: 92, Fixed: true},
	{Title: "评分", Width: 84, Fixed: true},
	{Title: "差异", Width: 76, Fixed: true},
	{Title: "最近提交", MinWidth: 150},
	// 216 而不是 200：四个行内按钮（详情/拉取/提交/删除）加上单元格左右
	// 内边距已经接近 200，按 200 分栏时最右侧「删除」会被列边框裁掉一点。
	// 再给右侧留出滚动条覆盖的余量。
	{Title: "操作", Width: 216, Fixed: true, Align: ui.End},
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
		ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Center).Wrap().Children(func() {
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

		// ---- 自动刷新：开关 + 频率 ----
		// 放在标题行下方而不是塞进右侧按钮组：频率有四档，窄窗口里和三个
		// 主操作挤一行会被压扁。
		a.repoAutoRefreshBar(c, t)

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
		// 表头里的「全选」随 SelectTable 一起换掉了，放到筛选条这里：
		// 只作用于当前筛选出的行，清空时也只清这些行，和用户眼前的列表一致。
		if len(rows) > 0 {
			if u.sel == nil {
				u.sel = make(map[int64]bool, len(rows))
			}
			selected := 0
			for _, r := range rows {
				if u.sel[r.ID] {
					selected++
				}
			}
			all := selected == len(rows)
			if ui.Checkbox(c, &all, "全选").Changed() {
				for _, r := range rows {
					if all {
						u.sel[r.ID] = true
					} else {
						delete(u.sel, r.ID)
					}
				}
			}
		}
		ui.Text(c, itoa(len(rows))+" 个仓库").TextColor(t.TextMuted).
			FontSize(t.FontSize-2).Margin(6, 0, 0, 0)
		// mygo 的滚动条是「悬停才出现」的覆盖式指示条：窄窗口里如果不提示，
		// 用户看到表格在「状态」列被裁掉，很容易以为后面没有数据。
		if a.contentWidth(c) < repoTableWideEnough {
			ui.Text(c, "列较多，可左右滚动表格查看全部").
				FontSize(t.FontSize-2).TextColor(t.TextMuted).Margin(6, 0, 0, 0)
		}
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
		{Label: "批量删除", Icon: "trash-2"},
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
	case 4:
		a.confirmErase(selected)
	}
}

// repoTable 仓库表本体（mygo ui.Table：固定表头 + 横向/纵向滚动 + 行虚拟化）。
//
// 选择仍按仓库 id 存在 u.sel 里，而不是按行号：筛选、排序变化时选中项跟着
// 仓库走，不会错位到别的行上。
func (a *App) repoTable(c *ui.Context, t *ui.Theme, rows []store.Repo) {
	u := &a.atlasUI
	if u.sel == nil {
		u.sel = make(map[int64]bool, len(rows))
	}
	list := &u.repoList
	list.Key = func(i int) any { return rows[i].ID }
	list.Label = func(i int) string { return "仓库：" + rows[i].Name }

	ui.Table(c, list, repoCols, len(rows), func(row, col int) {
		r := rows[row]
		switch col {
		case 0:
			on := u.sel[r.ID]
			if ui.Checkbox(c, &on, "").Changed() {
				if on {
					u.sel[r.ID] = true
				} else {
					delete(u.sel, r.ID)
				}
			}
		case 1:
			ui.Column(c).Gap(1).Children(func() {
				// 悬停仓库名看评价（详情页里有完整评价卡；这里只做快速预览）。
				tooltipIfSet(ui.Text(c, r.Name).SingleLine().Bold(), r.Review)
				sub := firstNonEmpty(r.LocalPath, r.URL)
				if sub != "" {
					ui.Text(c, sub).FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
				}
			})
		case 2:
			p := a.platformForRepo(r)
			lbl := ui.Text(c, p.Label()).SingleLine().FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			if p.Host != "" {
				lbl.Tooltip(p.Host)
			}
		case 3:
			a.repoProjectCell(c, t, r)
		case 4:
			ui.Text(c, firstNonEmpty(r.Branch, "—")).SingleLine().TextColor(t.TextMuted)
		case 5:
			pill := kit.Pill(c, t, r.Status, repoStatusColor(t, r.Status))
			if r.StatusMsg != "" {
				pill.Tooltip(r.StatusMsg)
			}
		case 6:
			stars := ui.Text(c, ratingStars(r.Importance)).
				FontSize(t.FontSize - 2).TextColor(t.Warning).SingleLine()
			stars.Tooltip("重要 " + itoa(r.Importance) + " · 可学习 " + itoa(r.Learning) + " · 难度 " + itoa(r.Difficulty))
		case 7:
			ui.Text(c, repoDiffText(r)).FontSize(t.FontSize - 2).SingleLine().TextColor(t.TextMuted)
		case 8:
			ui.Column(c).Gap(1).Children(func() {
				ui.Text(c, firstNonEmpty(r.LastCommit, "—")).SingleLine().FontSize(t.FontSize - 2)
				if !r.LastCommitAt.IsZero() {
					ui.Text(c, r.LastCommitAt.Local().Format("2006-01-02 15:04")).
						FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
				}
			})
		case 9:
			ui.Row(c).Gap(2).AlignItems(ui.Center).Justify(ui.End).Children(func() {
				if smallButton(c, "详情").Clicked() {
					a.openRepoDetail(r)
				}
				if smallButton(c, "拉取").Clicked() {
					a.runRepoBatch(batchPull, []int64{r.ID}, "")
				}
				if smallButton(c, "提交").Clicked() {
					a.openCommitDialog([]int64{r.ID})
				}
				// 编辑移到详情页（「编辑登记」），这一列留给高频动作 + 删除。
				if smallButton(c, "删除").Clicked() {
					a.confirmErase([]int64{r.ID})
				}
			})
		}
	}).Height(a.repoTableHeight(c, len(rows))).Label("仓库列表")
}

// repoProjectCell 仓库表的「项目」单元格：点一下就弹出项目菜单，选中即写库。
//
// 为什么不在表格里放常驻下拉框：项目列只有 84 DIP，常驻下拉框会把这一列
// 变得比「仓库」还抢眼；用「文字 + 箭头」的菜单按钮，平时仍是表格里的一格，
// 悬停/点击时才表现成可操作控件。菜单用 mygo 在窗口内绘制的 PopoverMenu，
// 和主题一致，也不依赖系统原生菜单。
func (a *App) repoProjectCell(c *ui.Context, t *ui.Theme, r store.Repo) {
	d := a.atlas()
	current := d.projectNameOrDash(r.ProjectID)

	btn := ui.ButtonBase(c).Label("归属项目："+current).
		FillWidth().Padding(2, 4).Radius(t.Radius).
		Cursor(ui.CursorPointer).
		Tooltip("归属项目：" + current + "；点击选择")
	if btn.Hovered() {
		btn.Background(kit.Alpha(t.Accent, 0.10))
	}
	btn.Children(func() {
		ui.Text(c, current).SingleLine().Grow(1)
		ui.Icon(c, icons.ChevronDown).FontSize(12).TextColor(t.TextMuted).Shrink(0)
	})

	btn.PopoverMenuButton(func(m *ui.Menu) {
		if m.Item("未归属").Radio(r.ProjectID == 0).Chosen() {
			a.setRepoProject(r, 0)
		}
		if len(d.projects) > 0 {
			m.Separator()
		}
		for _, p := range d.projects {
			p := p
			if m.Item(p.Name).Radio(r.ProjectID == p.ID).Chosen() {
				a.setRepoProject(r, p.ID)
			}
		}
	})
}

// setRepoProject 把一个仓库的归属项目改掉并立刻刷新列表。
func (a *App) setRepoProject(r store.Repo, projectID int64) {
	if r.ProjectID == projectID {
		return
	}
	next := r
	next.ProjectID = projectID
	if !a.saveRepo(next) {
		return
	}
	if projectID == 0 {
		a.toast("已改为「未归属」")
		return
	}
	a.toast("已归入「" + a.atlas().projectNameOrDash(projectID) + "」")
}

// repoTableHeight 表格本体给多高：行少时按内容自然高度，行多时在表格内部
// 滚动（而不是把整页越撑越长）。
//
// 为什么按窗口高度封顶：表格在一个可滚动的页面里，如果只按行数给高度，
// 二十个仓库会把按钮和筛选条推到一屏之外；给一个「视口内最多这么高」的
// 上限，表头固定、行内滚动，浏览体验才像表格。
func (a *App) repoTableHeight(c *ui.Context, rowCount int) float32 {
	_, winH := c.Size()
	maxH := winH - 400 // 页头、统计卡、筛选条与卡片内边距的粗略预留
	if maxH < 220 {
		maxH = 220
	}
	// 行高随字号缩放：字号 20 时仍按 14 的固定行高估，最后一行会被切掉。
	rowH := float32(c.Theme().FontSize)*2.8 + 20
	natural := 44 + float32(rowCount)*rowH
	if natural < maxH {
		return natural
	}
	return maxH
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
					FontSize(t.FontSize - 2).TextColor(t.Warning)
			})
	case d.err != nil:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(8, 12).Radius(8).
			Background(kit.Alpha(t.Danger, 0.12)).
			Children(func() {
				ui.Text(c, d.err.Error()).FontSize(t.FontSize - 2).TextColor(t.Danger).Grow(1)
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
