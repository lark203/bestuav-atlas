package view

import (
	"context"
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

	// ---- 仓库页 ----
	sel         map[int64]bool
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
	todoProject        string // 全部项目 / 项目名
	todoSearch         string
	todoForm           todoForm
	confirmTodo        int64
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
	Due       string
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
	due := ""
	if !td.DueAt.IsZero() {
		due = td.DueAt.Local().Format("2006-01-02")
	}
	status := td.Status
	if status == "" {
		status = TodoOpen
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
		Due:       due,
	}
	a.atlasUI.dialog = "todo"
	a.atlasUI.dialogPage = a.page
	a.atlasUI.confirmTodo = 0
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
// 本项目不再自绘一层。唯一要补的是「背景图让主题半透明」这件事：overlay.Modal
// 的面板用 t.Background 画，背景图模式下它是半透明的，弹窗会和背后的内容叠在一起
// 看不清——所以这里在构建期临时把 t.Background 换成不透明值（构建完即还原）。
func (a *App) atlasModal(c *ui.Context, t *ui.Theme, title string, fn func()) {
	bg := t.Background
	if bg.A < 255 {
		t.Background = ui.RGBA(bg.R, bg.G, bg.B, 1)
	}
	defer func() { t.Background = bg }()

	open := true
	overlay.Modal(c, title, &open, overlay.ModalOptions{Close: "关闭"}, fn)
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

		// ---- 本地文件夹 ----
		ui.Text(c, "从本地文件夹导入").Bold().FontSize(t.FontSize - 2)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "选择文件夹…").Clicked() && !a.atlasUI.busy {
				a.pickLocalRepos(a.atlas().projectIDByName(a.atlasUI.importProject))
			}
			ui.Text(c, "可多选；会读 .git 的 origin 地址，没有远端时用文件夹名").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		})

		ui.Divider(c).Margin(4, 0)

		// ---- 远程地址 ----
		ui.Text(c, "从远程仓库导入").Bold().FontSize(t.FontSize - 2)
		ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, "平台").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Segmented(c, &a.atlasUI.importPlatform, "GitHub", "Gitee", "Gitea", "AtomGit")
			ui.Text(c, "只影响「纯 owner/repo」行的归属；带域名的地址按域名自动识别").
				FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		})
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "用户名").Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.TextInput(c, &a.atlasUI.importOwner).Width(220).Shrink(0).
				Placeholder("owner；留空 = 当前令牌账号")
		})
		ui.Text(c, "Gitea 令牌没有 read:user 权限时，在这里填用户名即可改用公开仓库列表。").
			FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		ui.TextArea(c, &a.atlasUI.importText).
			Placeholder("owner/repo\nhttps://github.com/owner/repo\ngit@atomgit.com:owner/repo.git\n…").
			Height(90)

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
		ui.Text(c, "勾选后把远程仓库克隆到「根目录/owner-repo」；大仓库会慢，进度显示在页头。").
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
		dialogRow(c, "截止时间", func() {
			ui.TextInput(c, &f.Due).Width(200).
				Placeholder("2026-11-30 18:00 或 2026-11-30，可空")
		})

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
				due, err := parseDueDate(f.Due)
				if err != nil {
					a.toast(err.Error())
					return
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

// parseDueDate 解析截止时间：支持「2026-11-30 18:00」（带时刻，用于倒计时）、
// 「2026-11-30」、「2026/11/30」与「11-30」（默认今年）。空串表示没有期限。
func parseDueDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04",
		"2006/01/02 15:04",
		"2006-01-02T15:04",
		"2006-01-02",
		"2006/01/02",
		"01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			if layout == "01-02" {
				now := time.Now()
				t = time.Date(now.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
			}
			return t, nil
		}
	}
	return time.Time{}, &dateError{s: s}
}

// dateError 日期格式错误的提示。
type dateError struct{ s string }

func (e *dateError) Error() string {
	return "日期格式不认识：" + e.s + "（用 2026-11-30 这种写法）"
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
