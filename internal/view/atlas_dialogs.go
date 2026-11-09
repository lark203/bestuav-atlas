package view

import (
	"context"
	"fmt"
	"math"
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
	// graph 是拓扑画布的缩放 / 平移 / 悬停 / 脉冲状态（见 graph_canvas.go）。
	graph graphView

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

// openGraphDialog 进入「关系图谱」整页：全部项目 / 仓库 / 待办的总览。
func (a *App) openGraphDialog() {
	a.atlasUI.graphProject = 0
	a.atlasUI.graph.reset()
	a.page = "topology"
}

// openProjectGraph 进入某个项目的「拓扑图」整页：项目在正中、仓库/待办围绕它发散。
func (a *App) openProjectGraph(projectID int64) {
	a.atlasUI.graphProject = projectID
	a.atlasUI.graph.reset()
	a.page = "topology"
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

// topologyPage 关系拓扑图，整页大画布（仿「仓库详情」：从别处进入、页头带返回键）。
//
// 两种作用域：
//   - 从某个项目卡的「拓扑图」进入（graphProject>0）：项目在正中，仓库/待办围绕它发散；
//   - 从「关系图谱」进入（graphProject==0）：全部项目内圈、仓库中圈、待办外圈。
//
// 用整页而不是弹窗：拓扑节点多时需要足够画布，弹窗太小铺不开。
func (a *App) topologyPage(c *ui.Context, t *ui.Theme) {
	d := a.atlas()
	pid := a.atlasUI.graphProject

	ui.Column(c).Gap(kit.PageGap).Children(func() {
		g := &a.atlasUI.graph
		// ---- 标题行（返回键由页头 pageLeading 统一画）----
		ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
			if pid != 0 {
				ui.Text(c, d.projectName(pid)).FontSize(22).Bold().SingleLine()
				ui.Text(c, "中心是本项目，周围是它的仓库与待办").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			} else {
				ui.Text(c, "关系图谱").FontSize(22).Bold().SingleLine()
				ui.Text(c, "每个圆点是一个项目 / 仓库 / 待办；连线表示归属").
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			}
			ui.Text(c, "").Grow(1)
			// 缩放：滚轮以鼠标为中心；按钮以画布中心为锚点。
			if smallButton(c, "−").Clicked() {
				g.zoomCenter(1 / 1.25)
				a.requestFrame()
			}
			ui.Text(c, fmt.Sprintf("%d%%", int(g.zoom*100+0.5))).
				Width(46).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			if smallButton(c, "+").Clicked() {
				g.zoomCenter(1.25)
				a.requestFrame()
			}
			if ui.Button(c, "重置视图").Clicked() {
				g.reset()
				a.requestFrame()
			}
		})
		ui.Text(c, "滚轮以鼠标位置为中心缩放 · 拖动空白处平移 · 单击节点跳转 · 悬停节点发出脉冲（移开即停）").
			FontSize(t.FontSize - 2).TextColor(t.TextMuted)

		if pid == 0 {
			if len(d.projects) == 0 && len(d.repos) == 0 && len(d.todos) == 0 {
				ui.Text(c, "还没有任何项目 / 仓库 / 待办，先去录入一些数据再来看关系图。").
					TextColor(t.TextMuted)
				return
			}
		} else if len(d.projectRepos(pid)) == 0 && len(d.projectTodos(pid)) == 0 {
			ui.Text(c, "这个项目下面还没有仓库或待办。").
				TextColor(t.TextMuted)
			return
		}

		// 画布尽量占满可视区——整页就是要能把拓扑铺开。
		_, winH := c.Size()
		graphH := winH - 190
		if graphH < 360 {
			graphH = 360
		}
		ui.Box(c).Height(float32(graphH)).Children(func() {
			area := ui.Box(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).
				Cursor(ui.CursorGrab).HandleInput(a.graphCanvasInput)
			// 指针位置要在 Draw 之前取：它同时负责「悬停时请求新帧」。
			mx, my, over := area.PointerPosition()
			g.hasPointer = over
			g.pointerX, g.pointerY = mx, my
			area.Draw(func(p *ui.Painter, r ui.Rect) {
				if pid != 0 {
					a.drawProjectTopology(p, r, t, pid, d.projectName(pid), d.projectRepos(pid), d.projectTodos(pid))
				} else {
					a.drawAtlasGraph(p, r, t, d)
				}
			})
		})
		// 脉冲是时间动画：还在放的时候每 16ms 要一帧；放完就自然停。
		if g.pulse.active && !g.pulseDone(time.Now()) {
			c.After(16 * time.Millisecond)
		}
	})
}

// ---- Obsidian 风格的节点关系图 ----
//
// 视觉参考 Obsidian 的 graph view：每个实体是一个小圆点，标签居中在点下方，
// 关联用细线连接，位置由「力导向」布局自然散开（不是排队/分栏的方块）。
// 项目拓扑与全量关系图共用同一套「节点 + 连线」数据模型与渲染。

// graphNode 图里的一个实体（项目 / 仓库 / 待办 / 未归属）。
type graphNode struct {
	label string
	color ui.Color
	r     float32 // 圆点半径
	hub   bool    // 项目等中枢节点：点更大、标签更深色
	tier  int     // 0=项目 1=仓库 2=待办（环形布局按层分圈）
	// kind / id 是节点身份：单击时按它跳转，双击脉冲按它定位。
	kind graphKind
	id   int64
	x, y float32 // 由布局函数填充（画布内局部坐标）
}

// projectTopologyGraph 把「一个项目 + 它的仓库/待办」变成节点与连线。
//
// 连线的关键约定：**每条边都是 0 → 子节点**，也就是每个仓库/待办都从项目中心
// 直接连出去一条自己的线；不存在「先连到一个中间点再分叉」的共享线段。
// 渲染层再给每条边一点弯曲，所以中枢附近不会叠成一根粗线。
func projectTopologyGraph(projectID int64, t *ui.Theme, projectName string, repos []store.Repo, todos []store.Todo) ([]graphNode, [][2]int) {
	const capEach = 24 // 节点太多会糊成一团，超量截断（先在项目卡上看到总数）
	nodes := make([]graphNode, 0, 1+len(repos)+len(todos))
	nodes = append(nodes, graphNode{label: projectName, color: t.Accent, r: 9, hub: true, kind: gkProject, id: projectID})
	edges := make([][2]int, 0, len(repos)+len(todos))

	rs := repos
	if len(rs) > capEach {
		rs = rs[:capEach]
	}
	for _, rp := range rs {
		idx := len(nodes)
		nodes = append(nodes, graphNode{label: rp.Name, color: repoStatusColor(t, rp.Status), r: 5, kind: gkRepo, id: rp.ID})
		edges = append(edges, [2]int{0, idx})
	}
	ts := todos
	if len(ts) > capEach {
		ts = ts[:capEach]
	}
	for _, td := range ts {
		idx := len(nodes)
		col := t.TextMuted
		if td.Status == TodoDone {
			col = t.Success
		}
		nodes = append(nodes, graphNode{label: td.Title, color: col, r: 5, kind: gkTodo, id: td.ID})
		edges = append(edges, [2]int{0, idx})
	}
	return nodes, edges
}

// drawProjectTopology 项目拓扑：以该项目为中枢，仓库与待办作为周围节点。
func (a *App) drawProjectTopology(p *ui.Painter, r ui.Rect, t *ui.Theme, projectID int64, projectName string, repos []store.Repo, todos []store.Todo) {
	nodes, edges := projectTopologyGraph(projectID, t, projectName, repos, todos)
	a.drawNodeGraph(p, r, t, nodes, edges, false)
}

// measureText 估算单行文字宽度（CJK 全角≈字号，其余≈0.55 字号），用于标签水平居中。
func measureText(s string, font float32) float32 {
	var w float32
	for _, rr := range s {
		if rr > 0x2E80 {
			w += font
		} else {
			w += font * 0.55
		}
	}
	return w
}

// forceLayout 确定性力导向布局：初始点按序号均匀落在圆周上（无随机，每帧结果一致，
// 所以不会抖动）；迭代做「节点互斥 + 连线相吸 + 轻微向心」，得到类似 Obsidian 的
// 自然散开效果。坐标为画布内局部坐标（0..w, 0..h）。
func forceLayout(nodes []graphNode, edges [][2]int, w, h, pad float32) {
	n := len(nodes)
	if n == 0 {
		return
	}
	cx, cy := w/2, h/2
	short := w
	if h < short {
		short = h
	}
	initR := short/2 - pad
	if initR < 24 {
		initR = 24
	}
	for i := range nodes {
		ang := 2 * math.Pi * float64(i) / float64(n)
		nodes[i].x = cx + initR*float32(math.Cos(ang))
		nodes[i].y = cy + initR*float32(math.Sin(ang))
	}
	// 理想边长 k：按「每节点可用面积」推算，节点多则自动缩小；给上下限避免过稀/过挤。
	k := float32(math.Sqrt(float64(w*h) / float64(n+1)))
	k *= 0.9
	if k < 62 {
		k = 62
	}
	if k > 150 {
		k = 150
	}
	const iters = 280
	dxs := make([]float32, n)
	dys := make([]float32, n)
	for it := 0; it < iters; it++ {
		for i := range dxs {
			dxs[i], dys[i] = 0, 0
		}
		// 互斥（库仑力）
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				ddx := nodes[i].x - nodes[j].x
				ddy := nodes[i].y - nodes[j].y
				dist := float32(math.Hypot(float64(ddx), float64(ddy)))
				if dist < 0.5 {
					dist = 0.5
				}
				f := k * k / dist
				ux, uy := ddx/dist, ddy/dist
				dxs[i] += ux * f
				dys[i] += uy * f
				dxs[j] -= ux * f
				dys[j] -= uy * f
			}
		}
		// 相吸（沿连线的弹簧力）
		for _, e := range edges {
			a0, b0 := e[0], e[1]
			if a0 < 0 || b0 < 0 || a0 >= n || b0 >= n {
				continue
			}
			ddx := nodes[a0].x - nodes[b0].x
			ddy := nodes[a0].y - nodes[b0].y
			dist := float32(math.Hypot(float64(ddx), float64(ddy)))
			if dist < 0.5 {
				dist = 0.5
			}
			f := dist * dist / k
			ux, uy := ddx/dist, ddy/dist
			dxs[a0] -= ux * f
			dys[a0] -= uy * f
			dxs[b0] += ux * f
			dys[b0] += uy * f
		}
		// 轻微向心，避免离群点飘走
		for i := 0; i < n; i++ {
			dxs[i] += (cx - nodes[i].x) * 0.03
			dys[i] += (cy - nodes[i].y) * 0.03
		}
		// 应用位移（阻尼 + 限步，防止震荡）
		const damp = 0.12
		for i := 0; i < n; i++ {
			mx := dxs[i] * damp
			my := dys[i] * damp
			m := float32(math.Hypot(float64(mx), float64(my)))
			if maxStep := k * 0.4; m > maxStep {
				mx *= maxStep / m
				my *= maxStep / m
			}
			nodes[i].x += mx
			nodes[i].y += my
		}
	}
	// 夹在画布内：左右留出「半个标签宽」的余量，避免标签被画布边缘裁掉；
	// 下方留出标签高度（标签在点下方）。
	clampf := func(v, lo, hi float32) float32 {
		if hi < lo {
			lo, hi = hi, lo
		}
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	xm := w * 0.15
	if xm < 48 {
		xm = 48
	}
	if xm > w/2-8 {
		xm = w/2 - 8
	}
	if xm < 0 {
		xm = 0
	}
	for i := range nodes {
		nodes[i].x = clampf(nodes[i].x, xm, w-xm)
		nodes[i].y = clampf(nodes[i].y, 26, h-32)
	}
}

// radialCenterLayout 以 nodes[0] 为中心、其余节点在椭圆环上均分角度的放射布局：
// 项目在正中，仓库与待办围绕它「发散」。
//
// 半径跟叶子数量走：3 个节点时不该铺满整块画布（那样线拉得又长又稀），
// 20 个节点时又要铺得开。所以取「按数量估的环半径」和「画布可用半径」的较小值。
// 叶子很多时用内外两圈交替，避免一个圈挤太多、标签打架。
func radialCenterLayout(nodes []graphNode, w, h float32) {
	if len(nodes) == 0 {
		return
	}
	cx, cy := w/2, h/2
	nodes[0].x, nodes[0].y = cx, cy
	leaves := len(nodes) - 1
	if leaves <= 0 {
		return
	}
	// 环半径：每个叶子大约占 26px 弧长，太少时保底 170。
	ring := 170 + float32(leaves)*26
	rx := ring * 1.25
	ry := ring
	if maxRx := w/2 - 110; rx > maxRx {
		rx = maxRx
	}
	if maxRy := h/2 - 70; ry > maxRy {
		ry = maxRy
	}
	if rx < 140 {
		rx = 140
	}
	if ry < 120 {
		ry = 120
	}
	twoRings := leaves > 16
	for i := 1; i < len(nodes); i++ {
		j := i - 1
		ang := 2*math.Pi*float64(j)/float64(leaves) - math.Pi/2 // 从正上方起
		fx, fy := rx, ry
		if twoRings {
			if j%2 == 0 {
				fx *= 0.56
				fy *= 0.56
			} else {
				fx *= 0.98
				fy *= 0.98
			}
		}
		nodes[i].x = cx + fx*float32(math.Cos(ang))
		nodes[i].y = cy + fy*float32(math.Sin(ang))
	}
	clampNodesToCanvas(nodes, w, h)
}

// ringLayout 确定性同心环布局（全量关系图用）：按 tier 分圈（项目内圈 / 仓库中圈 /
// 待办外圈），每层内按下标等分角度、从正上方开始。用椭圆（横纵半径分开）铺满宽画布。
// 只有单个项目时把它放正中心。
func ringLayout(nodes []graphNode, w, h, pad float32) {
	cx, cy := w/2, h/2
	rx := w/2 - 120
	ry := h/2 - 80
	if rx < 140 {
		rx = 140
	}
	if ry < 110 {
		ry = 110
	}
	counts := map[int]int{}
	for _, nd := range nodes {
		counts[nd.tier]++
	}
	fracOf := func(tier int) float32 {
		switch tier {
		case 0:
			return 0.30
		case 1:
			return 0.66
		default:
			return 0.98
		}
	}
	seen := map[int]int{}
	for i := range nodes {
		t := nodes[i].tier
		k := counts[t]
		j := seen[t]
		seen[t]++
		if t == 0 && k == 1 {
			nodes[i].x, nodes[i].y = cx, cy
			continue
		}
		f := fracOf(t)
		ang := 2*math.Pi*float64(j)/float64(k) - math.Pi/2
		nodes[i].x = cx + rx*f*float32(math.Cos(ang))
		nodes[i].y = cy + ry*f*float32(math.Sin(ang))
	}
	clampNodesToCanvas(nodes, w, h)
}

// clampNodesToCanvas 把节点夹在画布内：左右留「半个标签宽」余量、下方留标签高度，
// 避免标签被画布边缘裁掉。
func clampNodesToCanvas(nodes []graphNode, w, h float32) {
	clampf := func(v, lo, hi float32) float32 {
		if hi < lo {
			lo, hi = hi, lo
		}
		if v < lo {
			return lo
		}
		if v > hi {
			return hi
		}
		return v
	}
	xm := w * 0.15
	if xm < 48 {
		xm = 48
	}
	if xm > w/2-8 {
		xm = w/2 - 8
	}
	if xm < 0 {
		xm = 0
	}
	for i := range nodes {
		nodes[i].x = clampf(nodes[i].x, xm, w-xm)
		nodes[i].y = clampf(nodes[i].y, 26, h-32)
	}
}

// drawNodeGraph 用 Obsidian 风格画节点图：弧线 + 小圆点 + 点下方居中标签，
// 支持缩放 / 平移 / 悬停高亮。
//
// ring=true 用同心环布局（项目内圈 / 仓库中圈 / 待办外圈，按下标等分角度），
// 适合节点多的「全量关系图」；ring=false 用放射布局（nodes[0] 居中、其余围绕它
// 发散），适合「项目在正中、仓库/待办围着它」的项目拓扑。
func (a *App) drawNodeGraph(p *ui.Painter, r ui.Rect, t *ui.Theme, nodes []graphNode, edges [][2]int, ring bool) {
	if len(nodes) == 0 {
		return
	}
	const pad float32 = 30
	g := &a.atlasUI.graph
	g.w, g.h = r.W, r.H
	g.ensure()

	if ring {
		// 全量关系图：项目内圈 / 仓库中圈 / 待办外圈。力导向在节点少时会拉成
		// 几团、标签互相压住，同心环在这份数据规模下更稳定、也更容易读层级。
		ringLayout(nodes, r.W, r.H, pad)
	} else {
		// 项目拓扑：nodes[0] 是项目，摆正中；其余节点在椭圆环上均分角度围绕它发散。
		radialCenterLayout(nodes, r.W, r.H)
	}

	// 世界坐标 → 屏幕坐标（缩放 + 平移在这里一次性算好）。
	pts := make([][2]float32, len(nodes))
	g.hits = g.hits[:0]
	for i := range nodes {
		x, y := g.screen(nodes[i].x, nodes[i].y)
		pts[i] = [2]float32{x, y}
		// 命中表与绘制共用同一份屏幕坐标，点击判定才不会偏。
		g.hits = append(g.hits, graphHit{
			x: x, y: y,
			r:    clampFloat(nodes[i].r*g.zoom, 2.5, 24),
			kind: nodes[i].kind, id: nodes[i].id, label: nodes[i].label,
		})
	}

	// 悬停：屏幕坐标里找最近的节点。判定半径跟着缩放走，放大后更好点中。
	g.hover = -1
	if g.hasPointer {
		best := float32(1e9)
		for i := range nodes {
			d := float32(math.Hypot(float64(pts[i][0]-g.pointerX), float64(pts[i][1]-g.pointerY)))
			if d > nodes[i].r*g.zoom+10 {
				continue
			}
			if d < best {
				best, g.hover = d, i
			}
		}
	}
	adjacent := make(map[int]bool, 8)
	if g.hover >= 0 {
		for _, e := range edges {
			switch g.hover {
			case e[0]:
				adjacent[e[1]] = true
			case e[1]:
				adjacent[e[0]] = true
			}
		}
	}
	// 悬停即脉冲：悬停到节点就开始 BFS 涟漪，移开（或移到别处）立刻结束 / 重开。
	g.hoverNode(nodes, edges, g.hover, time.Now())

	// 脉冲中心：正在发脉冲的节点（用身份定位，布局变化不受影响）。
	pulseCenter := -1
	if g.pulse.active {
		for i := range nodes {
			if (graphKey{kind: nodes[i].kind, id: nodes[i].id, label: nodes[i].label}) == g.pulse.key {
				pulseCenter = i
				break
			}
		}
	}

	a.drawGraphGrid(p, r, t, g)

	// 连线（先画，压在圆点下面）。每条边一条独立曲线：同中枢的多条线不会叠成一条。
	for k, e := range edges {
		if e[0] < 0 || e[1] < 0 || e[0] >= len(nodes) || e[1] >= len(nodes) {
			continue
		}
		col := kit.Alpha(t.Border, 1)
		width := clampFloat(1.25*g.zoom, 0.9, 2.6)
		if g.hover >= 0 {
			if e[0] == g.hover || e[1] == g.hover {
				col = kit.Alpha(t.Accent, 0.95)
				width = clampFloat(2*g.zoom, 1.3, 3.2)
			} else {
				col = kit.Alpha(t.Border, 0.35)
			}
		}
		if pulseCenter >= 0 {
			_, da := g.pulse.depths[graphKey{kind: nodes[e[0]].kind, id: nodes[e[0]].id, label: nodes[e[0]].label}]
			_, db := g.pulse.depths[graphKey{kind: nodes[e[1]].kind, id: nodes[e[1]].id, label: nodes[e[1]].label}]
			if da && db {
				col = kit.Alpha(t.Accent, 0.7)
				width = maxFloat(width, clampFloat(1.6*g.zoom, 1.1, 3))
			}
		}
		// 相邻边交替朝两弯：从同一个中枢出发的多条线因此散成扇形，而不是一根粗线。
		bend := float32(0.11)
		if k%2 == 1 {
			bend = -bend
		}
		graphCurve(p,
			r.X+pts[e[0]][0], r.Y+pts[e[0]][1], clampFloat(nodes[e[0]].r*g.zoom, 2.5, 24),
			r.X+pts[e[1]][0], r.Y+pts[e[1]][1], clampFloat(nodes[e[1]].r*g.zoom, 2.5, 24),
			bend, width, col)
	}

	// 节点：背景色光晕 + 实心圆点 + 居中标签；悬停的放大、显示完整名称。
	for i := range nodes {
		nd := &nodes[i]
		cx, cy := r.X+pts[i][0], r.Y+pts[i][1]
		rr := clampFloat(nd.r*g.zoom, 2.5, 24)
		dim := g.hover >= 0 && i != g.hover && !adjacent[i]
		col := nd.color
		if dim {
			col = kit.Alpha(col, 0.45)
		}
		halo := ui.Rect{X: cx - rr - 2, Y: cy - rr - 2, W: (rr + 2) * 2, H: (rr + 2) * 2}
		p.Fill(halo, kit.Alpha(t.Background, 0.92), rr+2) // 光晕：线条不贴在点上
		dot := ui.Rect{X: cx - rr, Y: cy - rr, W: rr * 2, H: rr * 2}
		p.Fill(dot, col, rr)
		if i == g.hover {
			ring := ui.Rect{X: cx - rr - 3, Y: cy - rr - 3, W: (rr + 3) * 2, H: (rr + 3) * 2}
			p.Stroke(ring, kit.Alpha(t.Accent, 0.9), rr+3, 1.5)
		}

		font := clampFloat(12*g.zoom, 8, 20)
		labelMax := 150 * g.zoom
		label := truncateToPx(nd.label, labelMax, font)
		if i == g.hover {
			label = nd.label // 悬停时给全名，不再省略
		}
		lw := measureText(label, font)
		lcol := t.TextMuted
		if nd.hub {
			lcol = t.Text
		}
		if dim {
			lcol = kit.Alpha(lcol, 0.5)
		}
		p.Text(cx-lw/2, cy+rr+3, label, font, lcol)
	}

	// 脉冲画在最上层：亮点要能压过圆点和标签，才看得清「从中心向外发送」。
	a.drawGraphPulse(p, r, t, g, nodes, edges, pts, pulseCenter)
}

// drawGraphPulse 画悬停节点的脉冲：中心一圈扩散波纹 + 沿边逐跳外送的亮点，
// 每触达一个邻居，那个节点就亮一下再淡出，然后脉冲继续跳向它的邻居（BFS 涟漪）。
//
// 节奏由 graphPulse.depths 决定：第 d 跳的边在 [ (d-1)*hop, (d-1)*hop+travel ]
// 内跑亮点；节点在边跑完后亮起、fade 时长内淡出。亮点用 quadPoint 求值，
// 所以严格贴着画出来的那条弧线跑。
func (a *App) drawGraphPulse(p *ui.Painter, r ui.Rect, t *ui.Theme, g *graphView, nodes []graphNode, edges [][2]int, pts [][2]float32, center int) {
	if !g.pulse.active || center < 0 {
		return
	}
	elapsed := time.Since(g.pulse.start)
	if elapsed < 0 {
		return
	}
	centerR := clampFloat(nodes[center].r*g.zoom, 2.5, 24)

	// 中心扩散波纹。
	if elapsed < pulseTravel*2 {
		prog := float32(elapsed) / float32(pulseTravel*2)
		wave := centerR + 80*prog
		alpha := 0.5 * (1 - prog)
		p.Stroke(ui.Rect{
			X: r.X + pts[center][0] - wave, Y: r.Y + pts[center][1] - wave,
			W: wave * 2, H: wave * 2,
		}, kit.Alpha(t.Accent, float64(alpha)), wave, clampFloat(1.8*(1-prog), 0.7, 2))
	}

	// 沿边跑的亮点：只画「父层 → 子层」方向的边，避免同一跳跑两遍。
	for k, e := range edges {
		pa, pb := nodes[e[0]], nodes[e[1]]
		da, oka := g.pulse.depths[graphKey{kind: pa.kind, id: pa.id, label: pa.label}]
		db, okb := g.pulse.depths[graphKey{kind: pb.kind, id: pb.id, label: pb.label}]
		if !oka || !okb {
			continue
		}
		from, to, depth := e[0], e[1], da
		if db < da {
			from, to, depth = e[1], e[0], db
		}
		if db == da { // 同层边不参与涟漪（BFS 树之外的横向连接）
			continue
		}
		prog := float32(elapsed-time.Duration(depth)*pulseHop) / float32(pulseTravel)
		if prog < 0 || prog > 1 {
			continue
		}
		bend := float32(0.11)
		if k%2 == 1 {
			bend = -bend
		}
		sx, sy, cx, cy, ex, ey := graphCurvePoints(
			r.X+pts[from][0], r.Y+pts[from][1], clampFloat(nodes[from].r*g.zoom, 2.5, 24),
			r.X+pts[to][0], r.Y+pts[to][1], clampFloat(nodes[to].r*g.zoom, 2.5, 24),
			bend)
		for _, at := range []float32{prog, prog - 0.16} {
			if at < 0 || at > 1 {
				continue
			}
			x, y := quadPoint(sx, sy, cx, cy, ex, ey, at)
			rad := clampFloat(3.4*(1-at*0.35), 1.6, 4)
			alpha := 0.95 * (1 - at*0.55)
			p.Fill(ui.Rect{X: x - rad, Y: y - rad, W: rad * 2, H: rad * 2}, kit.Alpha(t.Accent, float64(alpha)), rad)
		}
	}

	// 被触达的节点亮一下再淡出：这是「邻居亮起，再跳二跳」的可视反馈。
	for i := range nodes {
		key := graphKey{kind: nodes[i].kind, id: nodes[i].id, label: nodes[i].label}
		d, ok := g.pulse.depths[key]
		if !ok || d == 0 {
			continue
		}
		arrive := time.Duration(d-1)*pulseHop + pulseTravel
		since := elapsed - arrive
		if since < 0 || since > pulseFade {
			continue
		}
		fade := 1 - float32(since)/float32(pulseFade)
		rr := clampFloat(nodes[i].r*g.zoom, 2.5, 24)
		// 实心加亮 + 一圈外扩光环
		cx, cy := r.X+pts[i][0], r.Y+pts[i][1]
		p.Fill(ui.Rect{X: cx - rr, Y: cy - rr, W: rr * 2, H: rr * 2}, kit.Alpha(t.Accent, float64(0.35*fade)), rr)
		wave := rr + 16*(1-fade)
		p.Stroke(ui.Rect{X: cx - wave, Y: cy - wave, W: wave * 2, H: wave * 2},
			kit.Alpha(t.Accent, float64(0.9*fade)), wave, clampFloat(2*fade, 0.6, 2))
	}
}

// drawGraphGrid 画一层很淡的点阵：缩放 / 平移时有参照，不至于「什么都没动」。
// 点距跟着缩放走，太密或太稀时夹在合理区间。
func (a *App) drawGraphGrid(p *ui.Painter, r ui.Rect, t *ui.Theme, g *graphView) {
	step := clampFloat(28*g.zoom, 16, 64)
	if step <= 0 {
		return
	}
	// 点阵用「次要文字色 + 低透明度」：比边框色更耐看，缩放/平移时有参照物。
	col := kit.Alpha(t.TextMuted, 0.22)
	x0 := float32(math.Mod(float64(g.panX), float64(step)))
	y0 := float32(math.Mod(float64(g.panY), float64(step)))
	if x0 > 0 {
		x0 -= step
	}
	if y0 > 0 {
		y0 -= step
	}
	for y := y0; y < r.H; y += step {
		for x := x0; x < r.W; x += step {
			p.Fill(ui.Rect{X: r.X + x, Y: r.Y + y, W: 2, H: 2}, col, 1)
		}
	}
}

// drawAtlasGraph 全量关系图：所有项目 / 仓库 / 待办各自成一个圆点，仓库连到所属
// 项目、待办连到所属仓库（没有仓库时连到项目）。布局与项目拓扑共用 drawNodeGraph。
func (a *App) drawAtlasGraph(p *ui.Painter, r ui.Rect, t *ui.Theme, d *atlasCache) {
	nodes := make([]graphNode, 0, len(d.projects)+len(d.repos)+len(d.todos)+1)
	edges := make([][2]int, 0, len(d.repos)+len(d.todos))

	projIdx := make(map[int64]int, len(d.projects))
	for _, pr := range d.projects {
		projIdx[pr.ID] = len(nodes)
		nodes = append(nodes, graphNode{label: pr.Name, color: t.Accent, r: 9, hub: true, tier: 0, kind: gkProject, id: pr.ID})
	}
	// 「未归属」中枢：只有存在未归属仓库/待办时才创建。
	unassigned := -1
	getUnassigned := func() int {
		if unassigned < 0 {
			unassigned = len(nodes)
			nodes = append(nodes, graphNode{label: "未归属", color: t.TextMuted, r: 8, hub: true, tier: 0, kind: gkUnassigned})
		}
		return unassigned
	}

	repos := d.sortedRepos()
	if len(repos) > 40 {
		repos = repos[:40]
	}
	repoIdx := make(map[int64]int, len(repos))
	for _, rp := range repos {
		idx := len(nodes)
		repoIdx[rp.ID] = idx
		nodes = append(nodes, graphNode{label: rp.Name, color: repoStatusColor(t, rp.Status), r: 5, tier: 1, kind: gkRepo, id: rp.ID})
		if pi, ok := projIdx[rp.ProjectID]; ok {
			edges = append(edges, [2]int{pi, idx})
		} else {
			edges = append(edges, [2]int{getUnassigned(), idx})
		}
	}

	todos := d.todos
	if len(todos) > 60 {
		todos = todos[:60]
	}
	for _, td := range todos {
		idx := len(nodes)
		col := t.TextMuted
		if td.Status == TodoDone {
			col = t.Success
		}
		nodes = append(nodes, graphNode{label: td.Title, color: col, r: 5, tier: 2, kind: gkTodo, id: td.ID})
		if ri, ok := repoIdx[td.RepoID]; ok {
			edges = append(edges, [2]int{ri, idx})
		} else if pi, ok := projIdx[td.ProjectID]; ok {
			edges = append(edges, [2]int{pi, idx})
		} else {
			edges = append(edges, [2]int{getUnassigned(), idx})
		}
	}

	a.drawNodeGraph(p, r, t, nodes, edges, true)
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
