package view

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/form"
	"github.com/ELY-Open-Source/CrychicUI/overlay"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/forge"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/kit"
)

// 本文件是三个业务页的弹窗与表单状态。
//
// 为什么弹窗状态挂在 App 而不是页面私有状态：ui.Modal 必须画在窗口根节点上
// （否则会被内容区裁剪、也盖不住侧栏），所以 View 统一调用 a.atlasDialogs。
// 页面只负责「打开哪个弹窗」与「保存后干什么」。
//
// 一个约定：弹窗里的表单字段是**草稿**——点「保存」才写库，点「取消」丢弃。
// 因此表单值放在 App 上跨帧保存，而不是每帧从数据库重算（那样输入会丢）。

// atlasUI 三个业务页共享的界面状态。
type atlasUI struct {
	dialog string // "" / "import" / "repo" / "project" / "todo" / "commit"
	// dialogPage 打开弹窗时所在的页面：切到别的页面就自动关掉，避免「人在待办页，
	// 屏幕上却挂着仓库弹窗」。允许从总览打开（总览上的「加待办」「导入仓库」），
	// 所以不能用「弹窗属于哪个页面」的死映射来判断。
	dialogPage string
	busy       bool // 导入时拉取 GitHub 列表等次要请求进行中

	// graphProject 关系图谱弹窗的作用域：0 = 全量总览；>0 = 只看该项目的拓扑图。
	graphProject int64

	// ---- 仓库页 ----
	sel         map[int64]bool
	repoList    ui.ListState // 仓库表（mygo ui.Table）的滚动位置与行高缓存
	repoSearch  string
	repoStatus  string // "" 或 "全部" = 不筛
	repoProject string // "" 或 "全部项目" = 不筛
	repoForm    repoForm
	confirmRepo int64

	// 导入弹窗
	importText    string
	importProject string
	importRoot    string
	// importPlatform 纯 owner/repo 行归属的平台（索引对应 forgePlatforms）。
	importPlatform int
	// importClone 导入后是否顺手克隆到本地根目录。
	importClone bool
	// importOwner 从平台读「我的仓库」时用的 owner；空 = 当前令牌的仓库。
	importOwner string

	// 批量提交弹窗
	commitMsg string
	commitIDs []int64
	// 删除确认弹窗要删的仓库 id（单个/批量共用一条路径）。
	eraseIDs []int64

	// ---- 仓库详情页 ----
	detailID     int64
	detailImport int // 重要程度 0–5
	detailLearn  int // 可学习 0–5
	detailDiff   int // 难度 0–5
	detailReview string
	detailLoaded bool   // 本页是否已读过 README（进入时读一次）
	readmeText   string // README 正文（已截断）
	readmeName   string // 本地文件名
	readmeFrom   string // "local" / "remote" / ""
	readmeErr    string // 读不到时的解释

	// ---- 项目页 ----
	projFormOpen bool
	projForm     projectForm
	confirmProj  int64

	// ---- 待办页 ----
	todoQuick         string // 快速添加输入框
	todoQuickProject  string
	todoQuickPriority int
	// 快速添加的两个象限开关（重要 / 紧急）。
	todoQuickImportant bool
	todoQuickUrgent    bool
	todoFilter         string // 全部 / 待办 / 进行中 / 已完成
	// todoTodayOnly 只看「今天要做」：未完成，且今天在 [开始日, 截止日] 内。
	todoTodayOnly bool
	todoProject   string // 全部项目 / 项目名
	todoSearch    string
	todoForm      todoForm
	confirmTodo   int64
}

// repoForm 仓库编辑表单。
type repoForm struct {
	ID        int64
	Name      string
	URL       string // 远端地址（可空：纯本地仓库）
	Project   string // 项目名（"未归属" = 0）
	LocalPath string
	Branch    string
	Note      string
}

// projectForm 项目编辑表单。
type projectForm struct {
	ID     int64
	Name   string
	Desc   string
	Status string
}

// todoForm 待办编辑表单。
type todoForm struct {
	ID       int64
	Title    string
	Note     string
	Project  string
	Repo     string
	Priority int
	// Important / Urgent 决定四象限归属（重要 × 紧急）。
	Important bool
	Urgent    bool
	Status    string
	// StartAt 计划开始日（只取日期）；新建时默认今天。
	StartAt time.Time
	// DueAt 是草稿里的截止时刻；DueSet=false 表示「无期限」（DueAt 可能仍
	// 保留一个默认值，供用户重新勾选时使用，但保存时不写库）。
	DueAt  time.Time
	DueSet bool
}

// 待办的三个状态。
const (
	TodoOpen  = "待办"
	TodoDoing = "进行中"
	TodoDone  = "已完成"
)

// TodoStatuses 状态切换的顺序（也是分段控件的顺序）。
var TodoStatuses = []string{TodoOpen, TodoDoing, TodoDone}

// projectStatuses 项目状态。
var projectStatuses = []string{"进行中", "暂停", "归档"}

// ---- 打开弹窗 ----

// openImportDialog 打开「导入仓库」。
func (a *App) openImportDialog() {
	u := &a.atlasUI
	u.dialog = "import"
	u.dialogPage = a.page
	if strings.TrimSpace(u.importRoot) == "" {
		u.importRoot = a.workRoot()
	}
	if u.importProject == "" {
		u.importProject = "未归属"
	}
	// 用户名默认沿用设置里的「默认用户名」：多数平台上它就是自己的账号。
	if strings.TrimSpace(u.importOwner) == "" {
		u.importOwner = strings.TrimSpace(a.settings.RepoOwner)
	}
}

// openGraphDialog 打开「关系图谱」全量总览：仓库居中、左连项目、右连待办。
func (a *App) openGraphDialog() {
	a.atlasUI.graphProject = 0
	a.atlasUI.dialog = "graph"
	a.atlasUI.dialogPage = a.page
}

// openProjectGraph 打开某个项目的「拓扑图」：项目居中，左连它的待办、右连它的仓库。
func (a *App) openProjectGraph(projectID int64) {
	a.atlasUI.graphProject = projectID
	a.atlasUI.dialog = "graph"
	a.atlasUI.dialogPage = a.page
}

// openRepoDialog 打开仓库编辑；r.ID 为 0 时是新建。
func (a *App) openRepoDialog(r store.Repo) {
	d := a.atlas()
	a.atlasUI.repoForm = repoForm{
		ID:        r.ID,
		Name:      r.Name,
		URL:       r.URL,
		Project:   d.projectNameOrDash(r.ProjectID),
		LocalPath: r.LocalPath,
		Branch:    r.Branch,
		Note:      r.Note,
	}
	a.atlasUI.dialog = "repo"
	a.atlasUI.dialogPage = a.page
	a.atlasUI.confirmRepo = 0
}

// openProjectDialog 打开项目编辑；p.ID 为 0 时是新建。
func (a *App) openProjectDialog(p store.Project) {
	status := p.Status
	if status == "" {
		status = projectStatuses[0]
	}
	a.atlasUI.projForm = projectForm{ID: p.ID, Name: p.Name, Desc: p.Desc, Status: status}
	a.atlasUI.dialog = "project"
	a.atlasUI.dialogPage = a.page
	a.atlasUI.confirmProj = 0
}

// openTodoDialog 打开待办编辑；td.ID 为 0 时是新建。
func (a *App) openTodoDialog(td store.Todo) {
	d := a.atlas()
	status := td.Status
	if status == "" {
		status = TodoOpen
	}
	// 编辑有期限的待办时沿用原值；新建（或无期限）时给一个「下一个 18:00」
	// 作为勾选「设置截止时间」后的起点。DueSet 决定保存时是否写库。
	due := td.DueAt
	dueSet := !due.IsZero()
	if !dueSet {
		due = defaultTodoDue()
	}
	// 开始时间默认今天；老数据没有 start_at，用创建日回填（只回填显示，
	// 保存时才写入——这样不会因为打开看一眼就改动历史记录）。
	start := todoStart(td)
	if start.IsZero() {
		start = time.Now()
	}
	a.atlasUI.todoForm = todoForm{
		ID:        td.ID,
		Title:     td.Title,
		Note:      td.Note,
		Project:   d.projectNameOrDash(td.ProjectID),
		Repo:      firstNonEmpty(d.repoName(td.RepoID), "不关联"),
		Priority:  td.Priority,
		Important: td.Important,
		Urgent:    td.Urgent,
		Status:    status,
		StartAt:   startOfDay(start),
		DueAt:     due,
		DueSet:    dueSet,
	}
	a.atlasUI.dialog = "todo"
	a.atlasUI.dialogPage = a.page
	a.atlasUI.confirmTodo = 0
}

// defaultTodoDue 新待办默认的截止时刻：下一个 18:00。
//
// 已经是 18:00 之后（或正好到点）就顺延到明天，避免一打开就是「已逾期」。
func defaultTodoDue() time.Time {
	now := time.Now()
	due := time.Date(now.Year(), now.Month(), now.Day(), 18, 0, 0, 0, now.Location())
	if !due.After(now) {
		due = due.AddDate(0, 0, 1)
	}
	return due
}

// openCommitDialog 打开「批量提交并推送」确认框。
func (a *App) openCommitDialog(ids []int64) {
	a.atlasUI.commitIDs = append([]int64(nil), ids...)
	if strings.TrimSpace(a.atlasUI.commitMsg) == "" {
		a.atlasUI.commitMsg = "chore: 同步本地改动"
	}
	a.atlasUI.dialog = "commit"
	a.atlasUI.dialogPage = a.page
}

// closeAtlasDialog 关闭当前弹窗。
func (a *App) closeAtlasDialog() {
	a.atlasUI.dialog = ""
	a.atlasUI.dialogPage = ""
}

// atlasDialogs 在窗口根节点绘制当前弹窗（由 View 调用）。
func (a *App) atlasDialogs(c *ui.Context, t *ui.Theme) {
	dialog := a.atlasUI.dialog
	if dialog == "" {
		return
	}
	if p := a.atlasUI.dialogPage; p != "" && p != a.page {
		a.closeAtlasDialog()
		return
	}
	switch dialog {
	case "import":
		a.importDialog(c, t)
	case "repo":
		a.repoDialog(c, t)
	case "project":
		a.projectDialog(c, t)
	case "todo":
		a.todoDialog(c, t)
	case "commit":
		a.commitDialog(c, t)
	case "erase":
		a.eraseDialog(c, t)
	case "graph":
		a.graphDialog(c, t)
	}
}

// confirmErase 打开「删除仓库登记」确认框（单个与批量共用）。
//
// 只删本机登记：仓库对应的本地目录、远端仓库都不动——把「删登记」和「删代码」
// 混在一个按钮里，迟早会有人点错。
func (a *App) confirmErase(ids []int64) {
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		a.toast("先选中要删除的仓库")
		return
	}
	a.atlasUI.eraseIDs = ids
	a.atlasUI.dialog = "erase"
	a.atlasUI.dialogPage = a.page
}

// eraseDialog 删除确认。
//
// 用本项目统一的 atlasModal 外壳（标题 + 关闭键 + 遮罩），而不是 CrychicUI 的
// ConfirmDialog：后者没有标题栏样式，和导入/编辑这些弹窗放在一起会显得不像一家人。
func (a *App) eraseDialog(c *ui.Context, t *ui.Theme) {
	ids := append([]int64(nil), a.atlasUI.eraseIDs...)
	a.atlasModal(c, t, "删除仓库登记", func() {
		dialogDesc(c, t, "确定删除这 "+itoa(len(ids))+" 个仓库的登记吗？")
		ui.Text(c, "只删本机登记：磁盘上的代码目录、GitHub/Gitee 等远端仓库都不会动，之后可以重新导入。").
			FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	}, func() {
		dialogButtons(c, func() {
			if ui.Button(c, "取消").Clicked() {
				a.closeAtlasDialog()
			}
			if ui.PrimaryButton(c, "删除").Clicked() {
				a.closeAtlasDialog()
				a.eraseRepos(ids)
			}
		})
	})
}

// eraseRepos 批量删除仓库登记；返回成功删除的条数。
func (a *App) eraseRepos(ids []int64) {
	if a.store == nil {
		a.toast("本次运行不落库，无法删除")
		return
	}
	deleted, failed := 0, 0
	for _, id := range ids {
		if err := a.store.DeleteRepo(id); err != nil {
			failed++
			logging.Errv("atlas", "删除仓库登记失败", err, "id", id)
			continue
		}
		deleted++
		delete(a.atlasUI.sel, id)
	}
	a.reloadAtlas()
	switch {
	case failed > 0:
		a.toast("删除 " + itoa(deleted) + " 个，失败 " + itoa(failed) + " 个（详见日志）")
	case deleted == 1:
		a.toast("已删除 1 个仓库登记（本地代码未动）")
	default:
		a.toast("已删除 " + itoa(deleted) + " 个仓库登记（本地代码未动）")
	}
	// 详情页删掉的正是不在列表里了：回列表，避免停在「这个仓库已经不在列表里」。
	if a.page == "repo-detail" && len(ids) == 1 && a.atlasUI.detailID == ids[0] {
		a.page = "repos"
	}
}

// uniqueIDs 去重并丢掉非法 id（<=0）。
func uniqueIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// ---- 通用版式 ----

// atlasModal 弹窗外壳：直接用 CrychicUI 的 overlay.Modal。
//
// 它已经带好了标题栏、关闭按钮、遮罩、入场动画与 Escape/点击外部的关闭语义，
// 本项目不再自绘一层。这里补两件事：
//
//  1. 背景图让主题半透明：overlay.Modal 的面板用 t.Background 画，背景图模式下
//     它是半透明的，弹窗会和背后的内容叠在一起看不清——构建期临时换成不透明值。
//  2. 长表单在小窗口里要能滚，底部按钮必须一直看得见：正文放进一个按窗口高度
//     限高的 Scroll，底部按钮作为独立的一行固定在正文之后。否则窗口一矮，
//     「取消 / 导入远程仓库」就会被挤出屏幕，用户只能关窗。
func (a *App) atlasModal(c *ui.Context, t *ui.Theme, title string, body, footer func()) {
	bg := t.Background
	if bg.A < 255 {
		t.Background = ui.RGBA(bg.R, bg.G, bg.B, 1)
	}
	defer func() { t.Background = bg }()

	open := true
	overlay.Modal(c, title, &open, overlay.ModalOptions{Close: "关闭"}, func() {
		// 标题栏 / 面板内边距 / 底部按钮大约占 190 DIP；余下的都给正文。
		// 窗口再小也留 120 DIP 的可视区，避免滚动容器被压成 0。
		_, winH := c.Size()
		bodyH := winH - 190
		if bodyH < 120 {
			bodyH = 120
		}
		ui.Scroll(c).MaxHeight(bodyH).Children(body)
		if footer != nil {
			footer()
		}
	})
	if !open {
		a.closeAtlasDialog()
	}
}

// dialogDesc 弹窗里标题下方的一句话说明（标题由 overlay.Modal 负责）。
func dialogDesc(c *ui.Context, t *ui.Theme, text string) {
	ui.Text(c, text).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
}

// dialogRow 一个表单项：标签在上、控件在下，用 mygo 的 ui.Field。
func dialogRow(c *ui.Context, label string, body func()) {
	ui.Field(c, label, body).FillWidth()
}

// dialogButtons 底部按钮行（右对齐）。
func dialogButtons(c *ui.Context, fn func()) {
	ui.Row(c).Gap(8).Justify(ui.End).Margin(4, 0, 0, 0).Children(fn)
}

// projectSelect 项目下拉框：选项永远包含当前值，否则 choice.Dropdown 会 panic。
func (a *App) projectSelect(c *ui.Context, label string, value *string) {
	opts := a.atlas().projectOptions()
	if !containsString(opts, *value) {
		*value = opts[0]
	}
	choice.Dropdown(c, label, opts, value)
}

// repoSelect 仓库下拉框（待办里关联仓库用）；第一项是「不关联」。
func (a *App) repoSelect(c *ui.Context, label string, value *string) {
	d := a.atlas()
	opts := make([]string, 0, len(d.repos)+1)
	opts = append(opts, "不关联")
	for _, r := range d.repos {
		opts = append(opts, r.Name)
	}
	if !containsString(opts, *value) {
		*value = opts[0]
	}
	choice.Dropdown(c, label, opts, value)
}

// containsString 小工具：判断切片里有没有这个字符串。
func containsString(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

// ---- 导入仓库 ----

func (a *App) importDialog(c *ui.Context, t *ui.Theme) {
	a.atlasModal(c, t, "导入仓库", func() {
		dialogDesc(c, t, "本地文件夹会自动解析 git 远端、分支与状态；远程仓库每行一个 owner/repo 或地址，已存在的会跳过。")

		ui.Column(c).Gap(12).Children(func() {
			// ---- 本地文件夹 ----
			kit.Card(c, t, func() {
				kit.CardHeader(c, t, "从本地文件夹导入", "可多选；自动读 .git 的 origin 地址，没有远端时用文件夹名当作仓库名。", nil)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if ui.PrimaryButton(c, "选择文件夹…").Clicked() && !a.atlasUI.busy {
						a.pickLocalRepos(a.atlas().projectIDByName(a.atlasUI.importProject))
					}
					ui.Text(c, "选中的目录会被扫描并登记为仓库；不读取远程列表，也不会改动磁盘上的代码。").
						TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				})
			})

			// ---- 远程仓库 ----
			kit.Card(c, t, func() {
				kit.CardHeader(c, t, "从远程仓库导入", "每行一个 owner/repo 或完整地址；带域名的地址按域名自动识别平台。", nil)
				ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
					ui.Text(c, "平台").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					ui.Segmented(c, &a.atlasUI.importPlatform, "GitHub", "Gitee", "Gitea", "AtomGit")
					ui.Text(c, "只影响「纯 owner/repo」行的归属").
						FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				})
				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "用户名").Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					// Grow 而不是固定 220：弹窗在窄窗口里变窄时输入框跟着缩，
					// 不能把右侧挤出面板。
					ui.TextInput(c, &a.atlasUI.importOwner).Grow(1).
						Placeholder("owner；留空 = 当前令牌账号")
				})
				ui.Text(c, "Gitea 令牌没有 read:user 权限时，在这里填用户名即可改用公开仓库列表。").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				ui.TextArea(c, &a.atlasUI.importText).
					Placeholder("owner/repo\nhttps://github.com/owner/repo\ngit@atomgit.com:owner/repo.git\n…").
					Height(96)

				// 两个短字段并排（CrychicUI 的 FormRow 会在窄窗口里自动竖排）。
				form.FormRow(c, []func(){
					func() {
						dialogRow(c, "归属项目", func() { a.projectSelect(c, "归属项目", &a.atlasUI.importProject) })
					},
					func() {
						dialogRow(c, "本地根目录", func() { ui.TextInput(c, &a.atlasUI.importRoot).Placeholder(a.workRoot()) })
					},
				})

				ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
					choice.Toggle(c, "导入后克隆", &a.atlasUI.importClone)
				})
				ui.Text(c, "勾选后把远程仓库克隆到「根目录/仓库名」；大仓库会慢，进度显示在页头。").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)

				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if ui.Button(c, "从 "+forgePlatforms[a.atlasUI.importPlatform].Label()+" 读取我的仓库").Clicked() && !a.atlasUI.busy {
						a.fetchPlatformRepos(a.importPlatform(), a.atlasUI.importOwner)
					}
					if a.atlasUI.busy {
						ui.Spinner(c)
						ui.Text(c, "正在读取…").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
					}
				})
				ui.Text(c, "读取列表需要该平台的令牌：设置 → 工作区；Gitea 还要填实例地址。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			})
		})
	}, func() {
		dialogButtons(c, func() {
			if ui.Button(c, "取消").Clicked() {
				a.closeAtlasDialog()
			}
			if ui.PrimaryButton(c, "导入远程仓库").Clicked() {
				lines := strings.Split(a.atlasUI.importText, "\n")
				added, skipped, ids := a.importRepos(lines,
					a.atlas().projectIDByName(a.atlasUI.importProject), a.atlasUI.importRoot, a.importPlatform())
				if added == 0 && skipped == 0 {
					a.toast("没有可导入的仓库，检查一下格式")
					return
				}
				clone := a.atlasUI.importClone
				a.closeAtlasDialog()
				a.atlasUI.importText = ""
				msg := "已导入 " + itoa(added) + " 个"
				if skipped > 0 {
					msg += "，跳过 " + itoa(skipped) + " 个（重复或格式不对）"
				}
				a.toast(msg)
				// 列表这时已经出现（导入只写元数据），随后的克隆/探测走轻量任务：
				// 页头进度条 + 界面照常可用。
				switch {
				case clone && len(ids) > 0:
					a.cloneRepos(ids)
				case len(ids) > 0:
					a.runRepoBatch(batchInspect, ids, "")
				default:
					a.inspectAllAsync()
				}
			}
		})
	})
}

// graphDialog 关系图谱弹窗：用 Painter 在一块画布上画「项目 → 仓库 → 待办」的关系。
//
// 仓库居中，向左的线归到所属项目（同一项目下多个仓库共用一条汇聚线），
// 向右的线连到关联待办（按仓库聚拢，避免压成一团）。画布高度随数据量增长，
// 弹窗外壳已包了滚动，内容多时中间区域可滚动查看。
func (a *App) graphDialog(c *ui.Context, t *ui.Theme) {
	d := a.atlas()
	// 项目作用域的拓扑图：从项目卡上的「拓扑图」按钮进入，只看这一个项目。
	if pid := a.atlasUI.graphProject; pid != 0 {
		a.projectTopologyDialog(c, t, d, pid)
		return
	}
	a.atlasModal(c, t, "关系图谱", func() {
		dialogDesc(c, t, "仓库居中，向左连线到归属项目，向右连线到关联待办；同一项目下的多个仓库共用一条汇聚线。")

		if len(d.repos) == 0 && len(d.projects) == 0 && len(d.todos) == 0 {
			ui.Text(c, "还没有任何项目 / 仓库 / 待办，先去录入一些数据再来看关系图。").
				TextColor(t.TextMuted)
			return
		}

		// 画布高度：行数取「项目数 / 仓库数」较大者，每行给足高度；上限避免过高。
		rowCount := len(d.repos)
		if len(d.projects)+1 > rowCount { // +1 = 可能有的「未归属」
			rowCount = len(d.projects) + 1
		}
		if rowCount < 1 {
			rowCount = 1
		}
		graphH := 48 + rowCount*46
		if graphH > 920 {
			graphH = 920
		}

		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).Height(float32(graphH)).Children(func() {
				ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Draw(func(p *ui.Painter, r ui.Rect) {
					a.drawAtlasGraph(p, r, t, d)
				})
			})
		})
	}, func() {
		dialogButtons(c, func() {
			if ui.Button(c, "关闭").Clicked() {
				a.closeAtlasDialog()
			}
		})
	})
}

// projectTopologyDialog 单个项目的「拓扑图」：项目居中，左连待办、右连仓库，
// 一眼看清「这个项目有哪些待办、哪些仓库」。从项目卡上的按钮进入。
func (a *App) projectTopologyDialog(c *ui.Context, t *ui.Theme, d *atlasCache, pid int64) {
	name := d.projectName(pid)
	repos := d.projectRepos(pid)
	todos := d.projectTodos(pid)
	a.atlasModal(c, t, "拓扑图 · "+name, func() {
		dialogDesc(c, t, "中心是本项目；左侧连出它的待办，右侧连出它的仓库。")
		if len(repos) == 0 && len(todos) == 0 {
			ui.Text(c, "这个项目下面还没有仓库或待办。").
				TextColor(t.TextMuted)
			return
		}
		n := len(repos)
		if len(todos) > n {
			n = len(todos)
		}
		if n < 1 {
			n = 1
		}
		if n > 14 {
			n = 14
		}
		// 树形高度：项目块 + 分支 + n 行子节点（行高 34）。
		graphH := 130 + (n-1)*34
		if graphH < 240 {
			graphH = 240
		}
		if graphH > 920 {
			graphH = 920
		}
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Grow(1).Height(float32(graphH)).Children(func() {
				ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Draw(func(p *ui.Painter, r ui.Rect) {
					a.drawProjectTopology(p, r, t, name, repos, todos)
				})
			})
		})
	}, func() {
		dialogButtons(c, func() {
			if ui.Button(c, "关闭").Clicked() {
				a.closeAtlasDialog()
			}
		})
	})
}

// drawProjectTopology 项目拓扑图（树形/组织图式）：项目在顶部居中，向下分叉出
// 左列（仓库）与右列（待办）。宽度自适应画布——两列各占一半，窄弹窗里也不会重叠。
//
// 为什么不用「左中右三列」（那是全量关系图的做法）：弹窗面板在窄窗口里只有 ~440 宽，
// 三列 150/120/150 会挤在一起。树形布局只依赖两列，任何宽度都摆得下，也更像「一个
// 项目往下展开它的待办与仓库」这种拓扑关系。
//
// 待办沿用「未完成=次要色 / 已完成=成功色」，仓库沿用状态色，与仓库页配色一致。
func (a *App) drawProjectTopology(p *ui.Painter, r ui.Rect, t *ui.Theme, projectName string, repos []store.Repo, todos []store.Todo) {
	const pad float32 = 16
	const nodeH float32 = 24
	const step float32 = 34 // 行高 = nodeH + 10 行距
	const size float32 = 12
	const colGap float32 = 20
	const maxPerCol = 14

	// 把「数据」整理成「展示节点」：超量时末位换成「+N」汇总节点。
	type node struct {
		label   string
		border  ui.Color
		textCol ui.Color
	}
	build := func(n int, mk func(i int) node, more string) []node {
		show := n
		if show > maxPerCol-1 {
			show = maxPerCol - 1
		}
		out := make([]node, 0, show+1)
		for i := 0; i < show; i++ {
			out = append(out, mk(i))
		}
		if n > show {
			out = append(out, node{fmt.Sprintf(more, n-show), t.TextMuted, t.TextMuted})
		}
		return out
	}
	rnodes := build(len(repos), func(i int) node {
		rp := repos[i]
		return node{rp.Name, repoStatusColor(t, rp.Status), t.Text}
	}, "+%d 个仓库")
	tnodes := build(len(todos), func(i int) node {
		td := todos[i]
		bc, tc := t.TextMuted, t.Text
		if td.Status == TodoDone {
			bc, tc = t.Success, t.TextMuted
		}
		return node{td.Title, bc, tc}
	}, "+%d 条待办")

	rows := len(rnodes)
	if len(tnodes) > rows {
		rows = len(tnodes)
	}
	if rows < 1 {
		rows = 1
	}

	// 两列等分（仓库左、待办右）。宽度全由 r.W 推出来，避免写死导致窄弹窗重叠。
	colW := (r.W - 2*pad - colGap) / 2
	leftCx := r.X + pad + colW/2
	rightCx := r.X + pad + colW + colGap + colW/2

	// 竖直方向尽量居中：内容总高固定，多余空间上下均分。
	projW := r.W * 0.6
	if projW > 240 {
		projW = 240
	}
	contentH := nodeH + 16 + 16 + nodeH + float32(rows-1)*step + nodeH/2 + pad
	offY := (r.H - contentH) / 2
	if offY < 0 {
		offY = 0
	}
	top := r.Y + offY + pad

	projCx := r.X + r.W/2
	projCy := top + nodeH/2
	branchY := top + nodeH + 16              // 横向分支线
	firstY := branchY + 16 + nodeH/2         // 第一行子节点中心

	// ---- 连线（先画，节点盖住线头）----
	edge := t.Border
	p.Line(projCx, top+nodeH, projCx, branchY, 1.2, edge) // 项目下方竖线
	p.Line(leftCx, branchY, rightCx, branchY, 1.2, edge)  // 横向分支
	for i := range rnodes {
		cy := firstY + float32(i)*step
		p.Line(leftCx, branchY, leftCx, cy-nodeH/2, 1.2, edge)
	}
	for i := range tnodes {
		cy := firstY + float32(i)*step
		p.Line(rightCx, branchY, rightCx, cy-nodeH/2, 1.2, edge)
	}

	// ---- 节点 ----
	drawNode := func(cx, cy, w float32, label string, border, textCol ui.Color) {
		rr := ui.Rect{X: cx - w/2, Y: cy - nodeH/2, W: w, H: nodeH}
		p.Fill(rr, t.Surface, 6)
		p.Stroke(rr, border, 6, 1.5)
		// Text 的 y 是文字顶部（不是基线）：cy-size/2 让文字在节点里垂直居中。
		p.Text(rr.X+8, cy-size*0.5, truncateToPx(label, w, size), size, textCol)
	}
	// 项目（强调色，顶部居中）
	drawNode(projCx, projCy, projW, "项目 · "+projectName, t.Accent, t.Accent)
	// 子节点
	for i, nd := range rnodes {
		drawNode(leftCx, firstY+float32(i)*step, colW, nd.label, nd.border, nd.textCol)
	}
	for i, nd := range tnodes {
		drawNode(rightCx, firstY+float32(i)*step, colW, nd.label, nd.border, nd.textCol)
	}
	// 两列小标题（放在分支线与第一行之间的空隙里）
	p.Text(leftCx-colW/2, branchY+3, "仓库", size-2, t.TextMuted)
	p.Text(rightCx-colW/2, branchY+3, "待办", size-2, t.TextMuted)
}

// gnode 图谱里的一个节点（项目 / 仓库 / 待办）。
type gnode struct {
	x, y, w, h float32
	label      string
	border     ui.Color
	textCol    ui.Color
}

// drawAtlasGraph 把关系图画在 r 里：项目在左、仓库居中、待办在右，连线表达归属。
func (a *App) drawAtlasGraph(p *ui.Painter, r ui.Rect, t *ui.Theme, d *atlasCache) {
	const pad float32 = 16
	const nodeH float32 = 22
	const size float32 = 12

	// 三列宽度按画布宽度自适应（总和恰好占满 pad 之间的区域），避免窄弹窗里重叠。
	inner := r.W - 2*pad
	gap := inner * 0.05
	projW := inner * 0.30
	repoW := inner * 0.30
	todoW := inner * 0.30
	leftX := r.X + pad
	repoX := leftX + projW + gap
	rightX := repoX + repoW + gap

	// ---- 收集项目（含「未归属」）----
	type proj struct{ id int64; name string; y float32 }
	projs := make([]proj, 0)
	projSeen := map[int64]bool{}
	needUnassigned := false
	for _, rp := range d.repos {
		if rp.ProjectID == 0 {
			needUnassigned = true
		} else if !projSeen[rp.ProjectID] {
			projSeen[rp.ProjectID] = true
			projs = append(projs, proj{id: rp.ProjectID, name: d.projectName(rp.ProjectID)})
		}
	}
	for _, td := range d.todos {
		if td.ProjectID == 0 {
			needUnassigned = true
		} else if !projSeen[td.ProjectID] {
			projSeen[td.ProjectID] = true
			projs = append(projs, proj{id: td.ProjectID, name: d.projectName(td.ProjectID)})
		}
	}
	for _, pr := range d.projects {
		if !projSeen[pr.ID] {
			projSeen[pr.ID] = true
			projs = append(projs, proj{id: pr.ID, name: pr.Name})
		}
	}
	if needUnassigned {
		projs = append(projs, proj{id: 0, name: "未归属"})
	}
	// 项目纵向均匀分布（layVertical 给的是相对顶部的 y，加上 r.Y 才是绝对坐标）
	projY := make(map[int64]float32, len(projs))
	layVertical(len(projs), pad, r.H, func(i int, y float32) { projY[projs[i].id] = r.Y + y })

	// ---- 仓库纵向均匀分布 ----
	type repo struct{ id int64; name string; col ui.Color; y float32 }
	repos := make([]repo, 0, len(d.repos))
	repoY := make(map[int64]float32, len(d.repos))
	layVerticalIdx(len(d.repos), pad, r.H, func(i int, y float32) {
		rp := d.sortedRepos()[i]
		repos = append(repos, repo{id: rp.ID, name: rp.Name, col: repoStatusColor(t, rp.Status)})
		repoY[rp.ID] = r.Y + y
	})

	// ---- 待办：连到其仓库（优先）或项目，按锚点聚拢 ----
	type todo struct{ anchor int64; isProj bool; title string; done bool; y float32 }
	todos := make([]todo, 0)
	// 先按锚点分组，便于堆叠
	byAnchor := map[string][]todo{}
	for _, td := range d.todos {
		var anchor int64
		isProj := false
		if td.RepoID != 0 {
			if _, ok := repoY[td.RepoID]; !ok {
				continue // 仓库不在列表里（理论上不会）
			}
			anchor = td.RepoID
		} else if td.ProjectID != 0 {
			if _, ok := projY[td.ProjectID]; !ok {
				continue
			}
			anchor = td.ProjectID
			isProj = true
		} else {
			continue // 既无仓库也无项目：不显示
		}
		rec := todo{anchor: anchor, isProj: isProj, title: td.Title, done: td.Status == TodoDone}
		byAnchor[anchorKey(anchor, isProj)] = append(byAnchor[anchorKey(anchor, isProj)], rec)
	}
	const maxPerAnchor = 6
	const step float32 = 20
	for key, group := range byAnchor {
		// 稳定排序：未完成的在上、按标题
		sort.SliceStable(group, func(i, j int) bool {
			if group[i].done != group[j].done {
				return !group[i].done
			}
			return group[i].title < group[j].title
		})
		// 锚点 y
		var ay float32
		if isProj, aid := parseAnchorKey(key); isProj {
			ay = projY[aid]
		} else {
			ay = repoY[aid]
		}
		m := len(group)
		if m > maxPerAnchor {
			m = maxPerAnchor
		}
		for k := 0; k < m; k++ {
			yy := ay + (float32(k) - float32(m-1)/2)*step
			if yy < r.Y+pad+nodeH/2 {
				yy = r.Y + pad + nodeH/2
			}
			if yy > r.Y+r.H-pad-nodeH/2 {
				yy = r.Y + r.H - pad - nodeH/2
			}
			todos = append(todos, todo{anchor: anchorOfKey(key), isProj: isProjOfKey(key), title: group[k].title, done: group[k].done, y: yy})
		}
		if len(group) > maxPerAnchor {
			yy := ay + (float32(m-1)-float32(m-1)/2)*step + step
			if yy > r.Y+r.H-pad-nodeH/2 {
				yy = r.Y + r.H - pad - nodeH/2
			}
			todos = append(todos, todo{anchor: anchorOfKey(key), isProj: isProjOfKey(key), title: fmt.Sprintf("+%d 条", len(group)-maxPerAnchor), done: false, y: yy})
		}
	}

	edge := t.Border
	// ---- 先画线（被节点盖在下面）----
	// 项目 → 仓库
	for _, rp := range repos {
		pid := projectIDOfRepo(d, rp.id)
		py, ok := projY[pid]
		if !ok {
			continue
		}
		// rp.y 字段未赋值（恒 0），真实 y 在 repoY 映射里——用错会画出从窗口顶部落下的斜线。
		p.Line(repoX, repoY[rp.id], leftX+projW, py, 1.2, edge)
	}
	// 仓库/项目 → 待办
	for _, td := range todos {
		var sx, sy float32
		if td.isProj {
			sx, sy = leftX+projW, projY[td.anchor]
		} else {
			sx, sy = repoX+repoW, repoY[td.anchor]
		}
		p.Line(sx, sy, rightX, td.y, 1.2, edge)
	}

	// ---- 节点 ----
	drawNode := func(x, y, w, h float32, label string, border, textCol ui.Color) {
		rr := ui.Rect{X: x, Y: y - h/2, W: w, H: h}
		p.Fill(rr, t.Surface, 6)
		p.Stroke(rr, border, 6, 1.5)
		// Text 的 y 是文字顶部：y-size/2 让文字在节点里垂直居中。
		p.Text(x+8, y-size*0.5, truncateToPx(label, w, size), size, textCol)
	}
	// 项目（左侧，强调色描边 + 强调色文字）。y 取自 projY 映射——
	// proj 结构体的 y 字段并未赋值，早期误用它导致节点被画在 y=0（跑到画布外）。
	for _, pr := range projs {
		drawNode(leftX, projY[pr.id], projW, nodeH, pr.name, t.Accent, t.Accent)
	}
	// 仓库（居中，按状态色描边）。同理取 repoY 映射。
	for _, rp := range repos {
		drawNode(repoX, repoY[rp.id], repoW, nodeH, rp.name, rp.col, t.Text)
	}
	// 待办（右侧，未完成用次要色、已完成用成功色）
	for _, td := range todos {
		bc := t.TextMuted
		if td.done {
			bc = t.Success
		}
		tc := t.Text
		if td.done {
			tc = t.TextMuted
		}
		drawNode(rightX, td.y, todoW, nodeH, td.title, bc, tc)
	}
}

// layVertical 把 n 个节点在 [pad, h-pad] 上均匀分布，回调拿到第 i 个的中心 y。
func layVertical(n int, pad, h float32, fn func(i int, y float32)) {
	if n <= 0 {
		return
	}
	usable := h - 2*pad
	step := usable / float32(n)
	for i := 0; i < n; i++ {
		fn(i, pad+step*(float32(i)+0.5))
	}
}

// layVerticalIdx 同上，但调用方在回调里按 i 取真实数据，避免闭包捕获切片问题。
func layVerticalIdx(n int, pad, h float32, fn func(i int, y float32)) {
	layVertical(n, pad, h, fn)
}

// anchorKey / parseAnchorKey 把（锚点 id, 是否项目）编码成 map key 再解出来，
// 避免引入结构化 key 类型。
func anchorKey(id int64, isProj bool) string {
	if isProj {
		return "P" + fmt.Sprintf("%d", id)
	}
	return "R" + fmt.Sprintf("%d", id)
}

func parseAnchorKey(k string) (isProj bool, id int64) {
	if len(k) == 0 {
		return false, 0
	}
	isProj = k[0] == 'P'
	v, err := strconv.ParseInt(k[1:], 10, 64)
	if err != nil {
		return false, 0
	}
	id = v
	return
}

func anchorOfKey(k string) int64 { _, id := parseAnchorKey(k); return id }
func isProjOfKey(k string) bool  { p, _ := parseAnchorKey(k); return p }

// projectIDOfRepo 从快照取某仓库的归属项目 id（找不到返回 0 = 未归属）。
func projectIDOfRepo(d *atlasCache, repoID int64) int64 {
	for _, r := range d.repos {
		if r.ID == repoID {
			return r.ProjectID
		}
	}
	return 0
}

// truncateLabel 超长标签裁掉并加省略号，避免节点文字溢出。
func truncateLabel(s string, n int) string {
	r := []rune(s)
	if n <= 1 {
		if len(r) == 0 {
			return ""
		}
		return string(r[0:1])
	}
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// truncateToPx 按估算像素宽度截断：CJK 全角约一个字号宽，其余约 0.55 个字号，
// 超出可用宽度时加省略号。节点变窄时（拓扑图两列只有半宽），中文标题也不会溢出到
// 相邻节点上。font 传字号（DIP）。
func truncateToPx(s string, maxPx, font float32) string {
	limit := maxPx - 8 // 右侧留 8px 内边距
	if limit <= 0 {
		return ""
	}
	var w float32
	rs := []rune(s)
	for i, r := range rs {
		rw := font * 0.55
		if r > 0x2E80 { // CJK 全角（含中日韩标点）
			rw = font
		}
		if w+rw > limit {
			if i == 0 {
				return string(rs[:1])
			}
			return string(rs[:i]) + "…"
		}
		w += rw
	}
	return s
}

// forgePlatforms 导入弹窗里可选的平台。
//
// Gitea 的 Host 是空的：自建实例地址来自设置，选中时由 App.importPlatform 填入。
var forgePlatforms = []forge.Platform{
	{Provider: forge.GitHub, Host: "github.com"},
	{Provider: forge.Gitee, Host: "gitee.com"},
	{Provider: forge.Gitea},
	{Provider: forge.AtomGit, Host: "atomgit.com"},
}

// importPlatform 当前选中的平台（Gitea 用设置里的实例地址）。
func (a *App) importPlatform() forge.Platform {
	idx := a.atlasUI.importPlatform
	if idx < 0 || idx >= len(forgePlatforms) {
		idx = 0
	}
	p := forgePlatforms[idx]
	if p.Provider == forge.Gitea {
		if h := hostOf(a.settings.GiteaHost); h != "" {
			p.Host = h
		}
		p.Scheme = a.giteaScheme()
	}
	return p
}

// fetchPlatformRepos 用令牌调一次平台 API，把「我的仓库」填进导入框。
//
// 这是本产品唯一「批量联网」的入口，且只在用户点按钮时发生。结果直接追加成
// 文本行而不是另开一个选择表格：用户看得见、改得动，也能顺手删掉不想导的。
func (a *App) fetchPlatformRepos(p forge.Platform, owner string) {
	if p.Provider == forge.Gitea && p.Host == "" {
		a.toast("先在设置里填 Gitea 实例地址")
		return
	}
	a.listPlatformRepos(p, owner, func(names []string, err error) {
		switch {
		case err != nil:
			// ScopeError 的文案里已经写了「补 scope 或填用户名」，这里原样提示即可。
			a.toast("读取失败：" + err.Error())
		case len(names) == 0:
			a.toast("这个账号下没有读到仓库")
		default:
			if strings.TrimSpace(a.atlasUI.importText) != "" {
				a.atlasUI.importText += "\n"
			}
			a.atlasUI.importText += strings.Join(names, "\n")
			a.toast("已读取 " + itoa(len(names)) + " 个仓库")
		}
	})
}

// listPlatformRepos 后台拉一次「我的仓库」列表，结果回到主线程交给 done。
//
// 导入弹窗与设置页的「测试」共用这一段：区别只在拿到列表后干什么
// （填进导入框 / 只报告数量），联网与错误处理只写一次。
func (a *App) listPlatformRepos(p forge.Platform, owner string, done func([]string, error)) {
	token := a.tokenFor(p.Host)
	if token == "" {
		a.toast("请先在设置里填写 " + p.Label() + " 令牌")
		return
	}
	client := forge.New(p, token) // 主线程构造，协程只读
	a.atlasUI.busy = true
	go func() {
		defer logging.Recover("atlas-list-repos")
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		list, err := client.ListRepos(ctx, owner)
		names := make([]string, 0, len(list))
		for _, r := range list {
			names = append(names, r.FullName)
		}
		if err != nil {
			logging.Errv("atlas", "读取仓库列表失败", err, "platform", p.Label())
		}
		mygo.RunOnMain(func() {
			a.atlasUI.busy = false
			done(names, err)
			a.requestFrame()
		})
	}()
}

// ---- 仓库编辑 ----

func (a *App) repoDialog(c *ui.Context, t *ui.Theme) {
	f := &a.atlasUI.repoForm
	title := "添加仓库"
	if f.ID != 0 {
		title = "编辑仓库"
	}
	a.atlasModal(c, t, title, func() {
		dialogDesc(c, t, "登记的本地目录决定「状态」怎么算；留空则视为未克隆。")

		dialogRow(c, "名称", func() {
			ui.TextInput(c, &f.Name).Grow(1).Placeholder("owner/repo")
		})
		dialogRow(c, "远端地址", func() {
			ui.TextInput(c, &f.URL).Grow(1).
				Placeholder("https://github.com/owner/repo（纯本地仓库可留空）")
		})
		dialogRow(c, "归属项目", func() { a.projectSelect(c, "归属项目", &f.Project) })
		dialogRow(c, "本地目录", func() {
			ui.TextInput(c, &f.LocalPath).Grow(1).Placeholder(a.repoLocalPath(f.Name))
			if ui.Button(c, "探测").Clicked() {
				r := store.Repo{ID: f.ID, Name: f.Name, LocalPath: f.LocalPath}
				next := a.inspectRepo(r)
				f.Branch = next.Branch
				a.toast("状态：" + next.Status + "，" + firstNonEmpty(next.StatusMsg, next.LastCommit))
			}
		})
		dialogRow(c, "分支", func() {
			ui.TextInput(c, &f.Branch).Width(220).Placeholder("留空 = 当前分支")
		})
		dialogRow(c, "备注", func() {
			ui.TextInput(c, &f.Note).Grow(1)
		})
	}, func() {
		dialogButtons(c, func() {
			if f.ID != 0 {
				if ui.Button(c, "从列表移除").Clicked() {
					if a.atlasUI.confirmRepo == f.ID {
						if a.deleteRepo(f.ID) {
							a.closeAtlasDialog()
						}
						return
					}
					a.atlasUI.confirmRepo = f.ID
					a.toast("再点一次「从列表移除」确认（本地代码不会被删除）")
				}
			}
			if ui.Button(c, "取消").Clicked() {
				a.closeAtlasDialog()
			}
			if ui.PrimaryButton(c, "保存").Clicked() {
				if strings.TrimSpace(f.Name) == "" {
					a.toast("名称不能为空")
					return
				}
				// 平台与地址：填了地址就按地址识别平台；只填名字按 GitHub 理解。
				rawURL := strings.TrimSpace(f.URL)
				platform := forge.PlatformForHost("github.com")
				if p, _, _, ok := forge.Detect(rawURL); ok {
					platform = p
				} else if owner, repo, ok := splitOwnerRepo(f.Name); ok {
					rawURL = platform.WebURL(owner, repo)
				}
				r := store.Repo{
					ID:        f.ID,
					ProjectID: a.atlas().projectIDByName(f.Project),
					Name:      strings.TrimSpace(f.Name),
					Host:      platform.Host,
					URL:       rawURL,
					LocalPath: strings.TrimSpace(f.LocalPath),
					Branch:    strings.TrimSpace(f.Branch),
					Note:      strings.TrimSpace(f.Note),
					Status:    RepoUnknown,
				}
				if f.ID != 0 {
					// 保留状态列：编辑名称/目录不该把上次探测结果清成「未知」。
					if old, ok := a.repoByID(f.ID); ok {
						r = mergeRepo(old, r)
					}
				}
				if a.saveRepo(r) {
					a.closeAtlasDialog()
					a.inspectAllAsync()
				}
			}
		})
	})
}

// mergeRepo 保留编辑表单没有覆盖的字段（状态、时间戳、Host/URL）。
func mergeRepo(old, next store.Repo) store.Repo {
	next.Status = old.Status
	next.StatusMsg = old.StatusMsg
	next.Dirty = old.Dirty
	next.Ahead = old.Ahead
	next.Behind = old.Behind
	next.LastCommit = old.LastCommit
	next.LastCommitAt = old.LastCommitAt
	next.CheckedAt = old.CheckedAt
	next.CreatedAt = old.CreatedAt
	if next.URL == "" {
		next.URL = old.URL
	}
	if next.Host == "" {
		next.Host = old.Host
	}
	return next
}

// repoByID 从快照里取一条仓库。
func (a *App) repoByID(id int64) (store.Repo, bool) {
	for _, r := range a.atlas().repos {
		if r.ID == id {
			return r, true
		}
	}
	return store.Repo{}, false
}

// todoByID 从快照里取一条待办。
func (a *App) todoByID(id int64) (store.Todo, bool) {
	for _, td := range a.atlas().todos {
		if td.ID == id {
			return td, true
		}
	}
	return store.Todo{}, false
}

// ---- 项目编辑 ----

func (a *App) projectDialog(c *ui.Context, t *ui.Theme) {
	f := &a.atlasUI.projForm
	title := "新建项目"
	if f.ID != 0 {
		title = "编辑项目"
	}
	a.atlasModal(c, t, title, func() {
		dialogDesc(c, t, "项目是仓库与待办的归属单位；删除项目不会删除仓库登记。")

		dialogRow(c, "名称", func() {
			ui.TextInput(c, &f.Name).Grow(1).Placeholder("例如 星图 / 量子科学 / 内部工具")
		})
		dialogRow(c, "描述", func() {
			ui.TextArea(c, &f.Desc).Height(72).Placeholder("一句话说明这个项目要做什么")
		})
		dialogRow(c, "状态", func() {
			idx := 0
			for i, s := range projectStatuses {
				if s == f.Status {
					idx = i
				}
			}
			ui.Segmented(c, &idx, projectStatuses...)
			if got := projectStatuses[idx]; got != f.Status {
				f.Status = got
			}
		})
	}, func() {
		dialogButtons(c, func() {
			if f.ID != 0 {
				if ui.Button(c, "删除项目").Clicked() {
					if a.atlasUI.confirmProj == f.ID {
						if a.deleteProject(f.ID) {
							a.closeAtlasDialog()
						}
						return
					}
					a.atlasUI.confirmProj = f.ID
					a.toast("再点一次「删除项目」确认")
				}
			}
			if ui.Button(c, "取消").Clicked() {
				a.closeAtlasDialog()
			}
			if ui.PrimaryButton(c, "保存").Clicked() {
				if strings.TrimSpace(f.Name) == "" {
					a.toast("项目名不能为空")
					return
				}
				p := store.Project{ID: f.ID, Name: strings.TrimSpace(f.Name), Desc: strings.TrimSpace(f.Desc), Status: f.Status}
				if a.saveProject(p) {
					a.closeAtlasDialog()
					a.toast("项目已保存")
				}
			}
		})
	})
}

// ---- 待办编辑 ----

func (a *App) todoDialog(c *ui.Context, t *ui.Theme) {
	f := &a.atlasUI.todoForm
	title := "新建待办"
	if f.ID != 0 {
		title = "编辑待办"
	}
	a.atlasModal(c, t, title, func() {
		dialogDesc(c, t, "待办是最小执行单元：挂到项目上，必要时再关联一个仓库。")

		dialogRow(c, "标题", func() {
			ui.TextInput(c, &f.Title).Grow(1).Placeholder("要做的事")
		})
		dialogRow(c, "备注", func() {
			ui.TextArea(c, &f.Note).Height(72)
		})
		dialogRow(c, "归属项目", func() { a.projectSelect(c, "归属项目", &f.Project) })
		dialogRow(c, "关联仓库", func() { a.repoSelect(c, "关联仓库", &f.Repo) })
		dialogRow(c, "优先级", func() {
			ui.Segmented(c, &f.Priority, "低", "中", "高")
		})
		dialogRow(c, "四象限", func() {
			choice.Toggle(c, "重要", &f.Important)
			choice.Toggle(c, "紧急", &f.Urgent)
			ui.Text(c, "重要 × 紧急 决定它在总览四象限里的位置").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		})
		dialogRow(c, "状态", func() {
			idx := 0
			for i, s := range TodoStatuses {
				if s == f.Status {
					idx = i
				}
			}
			ui.Segmented(c, &idx, TodoStatuses...)
			if got := TodoStatuses[idx]; got != f.Status {
				f.Status = got
			}
		})
		dialogRow(c, "开始日期", func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				ui.DateInput(c, &f.StartAt).Label("开始日期")
				if ui.Button(c, "今天").Clicked() {
					f.StartAt = startOfDay(time.Now())
				}
				ui.Text(c, "默认今天；「今天要做」按 开始日 ≤ 今天 ≤ 截止日 筛选").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			})
		})
		dialogRow(c, "截止时间", func() {
			choice.Toggle(c, "设置截止时间", &f.DueSet)
			if !f.DueSet {
				ui.Text(c, "不设截止时间（待办列表里显示为无期限）").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				return
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				// mygo 原生日期/时间选择器：DateInput 点开日历选日期，
				// TimeInput 的时/分/上下午可以直接步进或键盘输入。
				ui.DateInput(c, &f.DueAt).Label("截止日期")
				ui.TimeInput(c, &f.DueAt).Label("截止时间")
				if ui.Button(c, "清除").Clicked() {
					f.DueSet = false
				}
			})
		})
	}, func() {
		dialogButtons(c, func() {
			if f.ID != 0 {
				if ui.Button(c, "删除").Clicked() {
					if a.atlasUI.confirmTodo == f.ID {
						if a.deleteTodo(f.ID) {
							a.closeAtlasDialog()
							a.toast("已删除待办")
						}
						return
					}
					a.atlasUI.confirmTodo = f.ID
					a.toast("再点一次「删除」确认")
				}
			}
			if ui.Button(c, "取消").Clicked() {
				a.closeAtlasDialog()
			}
			if ui.PrimaryButton(c, "保存").Clicked() {
				if strings.TrimSpace(f.Title) == "" {
					a.toast("标题不能为空")
					return
				}
				var due time.Time
				if f.DueSet {
					due = f.DueAt
					if due.IsZero() {
						due = defaultTodoDue()
					}
				}
				start := startOfDay(f.StartAt)
				if start.IsZero() {
					start = startOfDay(time.Now())
				}
				td := store.Todo{
					ID:        f.ID,
					Title:     strings.TrimSpace(f.Title),
					Note:      strings.TrimSpace(f.Note),
					ProjectID: a.atlas().projectIDByName(f.Project),
					RepoID:    a.atlas().repoIDByName(f.Repo),
					Priority:  f.Priority,
					Important: f.Important,
					Urgent:    f.Urgent,
					Status:    f.Status,
					StartAt:   start,
					DueAt:     due,
				}
				if a.saveTodo(td) {
					a.closeAtlasDialog()
					a.toast("待办已保存")
				}
			}
		})
	})
}

// ---- 批量提交 ----

func (a *App) commitDialog(c *ui.Context, t *ui.Theme) {
	n := len(a.atlasUI.commitIDs)
	a.atlasModal(c, t, "批量提交并推送", func() {
		dialogDesc(c, t, "对选中的 "+itoa(n)+" 个仓库执行 git add -A && git commit && git push。没有改动的仓库会跳过提交、只补推。")

		dialogRow(c, "提交说明", func() {
			ui.TextInput(c, &a.atlasUI.commitMsg).Grow(1).Placeholder("chore: 同步本地改动")
		})
		ui.Text(c, "注意：这会把这批仓库里所有未提交的改动一并提交，且直接推到当前分支。").
			TextColor(t.Warning).FontSize(t.FontSize - 2)
	}, func() {
		dialogButtons(c, func() {
			if ui.Button(c, "取消").Clicked() {
				a.closeAtlasDialog()
			}
			if ui.PrimaryButton(c, "提交并推送").Clicked() {
				ids := a.atlasUI.commitIDs
				a.closeAtlasDialog()
				a.runRepoBatch(batchCommitPush, ids, a.atlasUI.commitMsg)
			}
		})
	})
}

// ---- 小工具 ----

// repoIDByName 把仓库下拉框的文字还原成 id（「不关联」= 0）。
func (d *atlasCache) repoIDByName(name string) int64 {
	if name == "" || name == "不关联" {
		return 0
	}
	for _, r := range d.repos {
		if r.Name == name {
			return r.ID
		}
	}
	return 0
}
