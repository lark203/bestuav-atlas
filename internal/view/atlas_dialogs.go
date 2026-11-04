package view

import (
	"context"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/overlay"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/github"
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

	// 批量提交弹窗
	commitMsg string
	commitIDs []int64

	// ---- 项目页 ----
	projFormOpen bool
	projForm     projectForm
	confirmProj  int64

	// ---- 待办页 ----
	todoQuick         string // 快速添加输入框
	todoQuickProject  string
	todoQuickPriority int
	todoFilter        string // 全部 / 待办 / 进行中 / 已完成
	todoProject       string // 全部项目 / 项目名
	todoSearch        string
	todoForm          todoForm
	confirmTodo       int64
}

// repoForm 仓库编辑表单。
type repoForm struct {
	ID        int64
	Name      string
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
	Status   string
	Due      string
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
}

// openRepoDialog 打开仓库编辑；r.ID 为 0 时是新建。
func (a *App) openRepoDialog(r store.Repo) {
	d := a.atlas()
	a.atlasUI.repoForm = repoForm{
		ID:        r.ID,
		Name:      r.Name,
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
		ID:       td.ID,
		Title:    td.Title,
		Note:     td.Note,
		Project:  d.projectNameOrDash(td.ProjectID),
		Repo:     firstNonEmpty(d.repoName(td.RepoID), "不关联"),
		Priority: td.Priority,
		Status:   status,
		Due:      due,
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
	}
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
		dialogDesc(c, t, "每行一个 owner/repo 或仓库地址；已存在的会跳过。导入只登记，状态随后自动探测。")

		ui.TextArea(c, &a.atlasUI.importText).
			Placeholder("owner/repo\nhttps://github.com/owner/repo\n…").
			Height(150)

		dialogRow(c, "归属项目", func() {
			a.projectSelect(c, "归属项目", &a.atlasUI.importProject)
		})
		dialogRow(c, "本地根目录", func() {
			ui.TextInput(c, &a.atlasUI.importRoot).Grow(1).
				Placeholder(a.workRoot())
			ui.Text(c, "仓库会放在「根目录/owner-repo」").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2).Margin(8, 0, 0, 0)
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "从 GitHub 读取我的仓库").Clicked() && !a.atlasUI.busy {
				a.fetchMyGitHubRepos()
			}
			if a.atlasUI.busy {
				ui.Spinner(c)
				ui.Text(c, "正在读取…").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			}
		})
		ui.Text(c, "读取仓库列表需要令牌：设置 → 工作区 → GitHub 令牌。").
			TextColor(t.TextMuted).FontSize(t.FontSize - 2)

		dialogButtons(c, func() {
			if ui.Button(c, "取消").Clicked() {
				a.closeAtlasDialog()
			}
			if ui.PrimaryButton(c, "导入").Clicked() {
				lines := strings.Split(a.atlasUI.importText, "\n")
				added, skipped := a.importRepos(lines, a.atlas().projectIDByName(a.atlasUI.importProject), a.atlasUI.importRoot)
				if added == 0 && skipped == 0 {
					a.toast("没有可导入的仓库，检查一下格式")
					return
				}
				a.closeAtlasDialog()
				a.atlasUI.importText = ""
				msg := "已导入 " + itoa(added) + " 个"
				if skipped > 0 {
					msg += "，跳过 " + itoa(skipped) + " 个（重复或格式不对）"
				}
				a.toast(msg)
				a.inspectAllAsync()
			}
		})
	})
}

// fetchMyGitHubRepos 用令牌调一次 GitHub API，把「我的仓库」填进导入框。
//
// 这是本产品唯一「批量联网」的入口，且只在用户点按钮时发生。结果直接追加成
// 文本行而不是另开一个选择表格：用户看得见、改得动，也能顺手删掉不想导的。
func (a *App) fetchMyGitHubRepos() {
	a.fetchGitHubRepos(func(names []string, err error) {
		switch {
		case err != nil:
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

// fetchGitHubRepos 后台拉一次「我的仓库」列表，结果回到主线程交给 done。
//
// 导入弹窗与设置页的「测试令牌」共用这一段：区别只在拿到列表后干什么
// （填进导入框 / 只报告数量），联网与错误处理只写一次。
func (a *App) fetchGitHubRepos(done func([]string, error)) {
	token := strings.TrimSpace(a.settings.RepoToken)
	if token == "" {
		a.toast("请先在设置里填写 GitHub 令牌")
		return
	}
	owner := strings.TrimSpace(a.settings.RepoOwner)
	a.atlasUI.busy = true
	go func() {
		defer logging.Recover("atlas-github-list")
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		list, err := github.New(token).ListRepos(ctx, owner)
		names := make([]string, 0, len(list))
		for _, r := range list {
			names = append(names, r.FullName)
		}
		if err != nil {
			logging.Errv("atlas", "读取 GitHub 仓库列表失败", err)
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
				r := store.Repo{
					ID:        f.ID,
					ProjectID: a.atlas().projectIDByName(f.Project),
					Name:      strings.TrimSpace(f.Name),
					LocalPath: strings.TrimSpace(f.LocalPath),
					Branch:    strings.TrimSpace(f.Branch),
					Note:      strings.TrimSpace(f.Note),
					URL:       githubURLOf(f.Name),
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

// mergeRepo 保留编辑表单没有覆盖的字段（状态、时间戳、URL）。
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

// githubURLOf 由 owner/repo 拼出网页地址；不是这种形式时返回空。
func githubURLOf(name string) string {
	owner, repo, ok := github.SplitFull(name)
	if !ok {
		return ""
	}
	return "https://github.com/" + owner + "/" + repo
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
		dialogRow(c, "截止日", func() {
			ui.TextInput(c, &f.Due).Width(160).Placeholder("2026-11-30，可空")
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

// parseDueDate 解析「2026-11-30」这类日期；空串表示没有期限。
func parseDueDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", "2006/01/02", "01-02"} {
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
