package view

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/gitx"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/store"
)

// 本文件是「星图」三块业务（待办 / 项目 / 仓库）的公共数据层：
//   - 一份带失效标记的数据库快照（atlasCache），避免每帧查三次库；
//   - 仓库状态的常量、排序与配色（状态语义是三个页面共用的语言）；
//   - 写操作的小包装（写库 → 失效快照 → 提示），让页面只关心界面；
//   - 系统交互（打开本地目录 / 打开网页）与时间文案。
//
// 页面文件（page_todos.go / page_projects.go / page_repos.go）只调用这里的函数，
// 不直接碰 store 的方法——否则「写完库忘了刷新快照」会在多个页面重复出现。

// ---- 仓库状态 ----
//
// 状态是「算出来的」而不是「用户填的」：gitx.Inspect 给出 dirty / ahead / behind，
// 本层按固定优先级折叠成一个状态词。优先级有讲究——
//   - 有未提交改动时先提示「待提交」，哪怕同时落后于远端（先处理自己的改动，
//     再去拉别人的，否则一拉就冲突）；
//   - 落后（需要更新）优先于领先（待推送）：先同步再推送；
//   - 都没有才算「正常」。
const (
	RepoOK      = "正常"
	RepoDirty   = "待提交"
	RepoAhead   = "待推送"
	RepoBehind  = "需要更新"
	RepoMissing = "未克隆"
	RepoError   = "错误"
	RepoUnknown = "未知"
)

// RepoStatuses 是筛选框里可选的仓库状态（顺序即界面顺序）。
var RepoStatuses = []string{RepoDirty, RepoBehind, RepoAhead, RepoMissing, RepoError, RepoOK, RepoUnknown}

// repoStatusRank 决定列表里的排序：需要动手的排前面，正常的沉底。
func repoStatusRank(s string) int {
	switch s {
	case RepoDirty:
		return 0
	case RepoBehind:
		return 1
	case RepoAhead:
		return 2
	case RepoError:
		return 3
	case RepoMissing:
		return 4
	case RepoOK:
		return 5
	default:
		return 6
	}
}

// repoStatusColor 状态药丸的颜色：红=要处理，橙=要同步，蓝=可推送，绿=干净。
func repoStatusColor(t *ui.Theme, s string) ui.Color {
	switch s {
	case RepoDirty:
		return t.Danger
	case RepoBehind:
		return t.Warning
	case RepoAhead:
		return t.Accent
	case RepoOK:
		return t.Success
	case RepoMissing:
		return t.TextMuted
	default:
		return t.Danger
	}
}

// ---- 数据库快照 ----

// atlasCache 是三个业务页共用的一份数据快照。
//
// 为什么缓存而不是每帧查：界面一帧里可能构建好几遍（悬停、动画、输入），
// 而 SQLite 查询虽快也是系统调用。写操作统一走下面的 save*/delete* 包装，
// 它们会把 loaded 置 false，下一帧自然重读——「忘了刷新」这件事被收敛到一个地方。
type atlasCache struct {
	projects []store.Project
	repos    []store.Repo
	todos    []store.Todo
	stats    store.AtlasStats
	loaded   bool
	err      error
}

// atlas 返回当前数据快照（必要时重读）。
func (a *App) atlas() *atlasCache {
	d := &a.atlasCache
	if d.loaded {
		return d
	}
	d.loaded = true
	d.err = nil
	d.projects, d.repos, d.todos = nil, nil, nil
	d.stats = store.AtlasStats{}
	if a.store == nil {
		// 安全模式 / 数据库打开失败：不把页面变成错误页，只让数据为空，
		// 页面顶部会有一条「本次运行不落盘」的提示（见 atlasStoreBanner）。
		return d
	}
	var err error
	if d.projects, err = a.store.ListProjects(); err != nil {
		d.err = fmt.Errorf("读取项目失败: %w", err)
		return d
	}
	if d.repos, err = a.store.ListRepos(); err != nil {
		d.err = fmt.Errorf("读取仓库失败: %w", err)
		return d
	}
	if d.todos, err = a.store.ListTodos(); err != nil {
		d.err = fmt.Errorf("读取待办失败: %w", err)
		return d
	}
	if d.stats, err = a.store.AtlasStats(); err != nil {
		d.err = fmt.Errorf("统计失败: %w", err)
	}
	return d
}

// reloadAtlas 让下一帧重读数据（写操作与批量任务结束都调它）。
func (a *App) reloadAtlas() {
	a.atlasCache.loaded = false
}

// ---- 名称与归属查询 ----

// projectName 按 id 取项目名；0 / 找不到返回空串。
func (d *atlasCache) projectName(id int64) string {
	if id == 0 {
		return ""
	}
	for _, p := range d.projects {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// repoName 按 id 取仓库名。
func (d *atlasCache) repoName(id int64) string {
	for _, r := range d.repos {
		if r.ID == id {
			return r.Name
		}
	}
	return ""
}

// repoCountOf 某项目下的仓库数。
func (d *atlasCache) repoCountOf(projectID int64) int {
	n := 0
	for _, r := range d.repos {
		if r.ProjectID == projectID {
			n++
		}
	}
	return n
}

// openTodoCountOf 某项目下未完成的待办数。
func (d *atlasCache) openTodoCountOf(projectID int64) int {
	n := 0
	for _, td := range d.todos {
		if td.ProjectID == projectID && td.Status != TodoDone {
			n++
		}
	}
	return n
}

// projectOptions 下拉框选项：第一项固定为「未归属」。
func (d *atlasCache) projectOptions() []string {
	out := make([]string, 0, len(d.projects)+1)
	out = append(out, "未归属")
	for _, p := range d.projects {
		out = append(out, p.Name)
	}
	return out
}

// projectIDByName 把下拉框文字还原成 id（「未归属」= 0）。
func (d *atlasCache) projectIDByName(name string) int64 {
	if name == "" || name == "未归属" {
		return 0
	}
	for _, p := range d.projects {
		if p.Name == name {
			return p.ID
		}
	}
	return 0
}

// projectNameOrDash 给列表用的展示名：没归属时显式写「未归属」。
func (d *atlasCache) projectNameOrDash(id int64) string {
	if n := d.projectName(id); n != "" {
		return n
	}
	return "未归属"
}

// todoStatusRank 待办的排序权重：待办 < 进行中 < 已完成。
func todoStatusRank(s string) int {
	switch s {
	case TodoDoing:
		return 1
	case TodoDone:
		return 2
	default:
		return 0
	}
}

// ---- 写操作包装 ----
//
// 统一模式：写库 → 失败弹提示并返回 false → 成功则失效快照。页面拿到 false 就
// 保持原样（表单不关、输入不丢），拿到 true 才清表单 / 关弹窗。

// saveProject 新建或更新项目（ID 为 0 表示新建）。
func (a *App) saveProject(p store.Project) bool {
	if a.store == nil {
		a.toast("本次运行不落盘，改动不会保存")
		return false
	}
	var err error
	if p.ID == 0 {
		_, err = a.store.CreateProject(p)
	} else {
		err = a.store.UpdateProject(p)
	}
	if err != nil {
		logging.Errv("atlas", "保存项目失败", err)
		a.toast("保存项目失败：" + err.Error())
		return false
	}
	a.reloadAtlas()
	return true
}

// deleteProject 删除项目（其下仓库与待办变为未归属，不级联删除）。
func (a *App) deleteProject(id int64) bool {
	if a.store == nil {
		return false
	}
	if err := a.store.DeleteProject(id); err != nil {
		logging.Errv("atlas", "删除项目失败", err)
		a.toast("删除项目失败：" + err.Error())
		return false
	}
	a.reloadAtlas()
	a.toast("已删除项目")
	return true
}

// saveRepo 新建或更新仓库登记信息。
func (a *App) saveRepo(r store.Repo) bool {
	if a.store == nil {
		a.toast("本次运行不落盘，改动不会保存")
		return false
	}
	var err error
	if r.ID == 0 {
		_, err = a.store.CreateRepo(r)
	} else {
		err = a.store.UpdateRepo(r)
	}
	if err != nil {
		logging.Errv("atlas", "保存仓库失败", err)
		a.toast("保存仓库失败：" + err.Error())
		return false
	}
	a.reloadAtlas()
	return true
}

// deleteRepo 删除仓库登记（不动本地磁盘上的代码）。
func (a *App) deleteRepo(id int64) bool {
	if a.store == nil {
		return false
	}
	if err := a.store.DeleteRepo(id); err != nil {
		logging.Errv("atlas", "删除仓库失败", err)
		a.toast("删除仓库失败：" + err.Error())
		return false
	}
	a.reloadAtlas()
	a.toast("已从列表移除（本地代码未删除）")
	return true
}

// saveTodo 新建或更新待办。
func (a *App) saveTodo(t store.Todo) bool {
	if a.store == nil {
		a.toast("本次运行不落盘，改动不会保存")
		return false
	}
	var err error
	if t.ID == 0 {
		_, err = a.store.CreateTodo(t)
	} else {
		err = a.store.UpdateTodo(t)
	}
	if err != nil {
		logging.Errv("atlas", "保存待办失败", err)
		a.toast("保存待办失败：" + err.Error())
		return false
	}
	a.reloadAtlas()
	return true
}

// setTodoStatus 改待办状态（勾选完成 / 退回待办）。
func (a *App) setTodoStatus(id int64, status string) bool {
	if a.store == nil {
		return false
	}
	if err := a.store.SetTodoStatus(id, status); err != nil {
		logging.Errv("atlas", "更新待办状态失败", err)
		a.toast("更新待办失败：" + err.Error())
		return false
	}
	a.reloadAtlas()
	return true
}

// deleteTodo 删除待办。
func (a *App) deleteTodo(id int64) bool {
	if a.store == nil {
		return false
	}
	if err := a.store.DeleteTodo(id); err != nil {
		logging.Errv("atlas", "删除待办失败", err)
		a.toast("删除待办失败：" + err.Error())
		return false
	}
	a.reloadAtlas()
	return true
}

// ---- 单仓库状态探测 ----

// inspectRepo 读一次本地 git 状态，折算成对外状态并写库。
//
// 纯本地操作（status / log / rev-list），不联网：远端领先/落后用的是上次
// fetch 留下的引用，所以界面上要同时显示「上次检查」时间，避免把陈旧数据当实时。
func (a *App) inspectRepo(r store.Repo) store.Repo {
	now := time.Now()
	r.CheckedAt = now
	r.StatusMsg = ""

	path := strings.TrimSpace(r.LocalPath)
	if path == "" {
		r.Status, r.StatusMsg = RepoMissing, "未设置本地目录"
		return r
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		r.Status, r.StatusMsg = RepoMissing, "本地目录不存在"
		return r
	}

	st := gitx.Inspect(path)
	if !st.IsRepo {
		r.Status, r.StatusMsg = RepoError, firstNonEmpty(st.Err, "不是 git 仓库")
		return r
	}

	r.Branch = st.Branch
	r.Dirty = st.Dirty
	r.Ahead = st.Ahead
	r.Behind = st.Behind
	r.LastCommit = st.LastCommit
	r.LastCommitAt = st.LastCommitAt

	switch {
	case st.Dirty > 0:
		r.Status = RepoDirty
		r.StatusMsg = fmt.Sprintf("%d 个文件有改动", st.Dirty)
	case st.Behind > 0:
		r.Status = RepoBehind
		r.StatusMsg = fmt.Sprintf("落后远端 %d 个提交", st.Behind)
	case st.Ahead > 0:
		r.Status = RepoAhead
		r.StatusMsg = fmt.Sprintf("领先远端 %d 个提交", st.Ahead)
	case st.Upstream == "":
		r.Status = RepoOK
		r.StatusMsg = "未设置上游分支，无法判断远端"
	default:
		r.Status = RepoOK
	}
	return r
}

// refreshRepoStatus 探测并落库一条仓库；返回探测后的记录。
func (a *App) refreshRepoStatus(r store.Repo) store.Repo {
	next := a.inspectRepo(r)
	if a.store != nil && r.ID != 0 {
		if err := a.store.SaveRepoStatus(r.ID, next); err != nil {
			logging.Errv("atlas", "写入仓库状态失败", err, "repo", r.Name)
		}
		// 分支是探测出来的观测值，但 SaveRepoStatus 刻意不写它（避免常规检查覆盖
		// 用户手填的字段）。只有「真的变了」（导入时为空、或本地切了分支）才整行
		// 更新一次；否则每轮检查都写一遍全字段是浪费。
		if next.Branch != r.Branch {
			if err := a.store.UpdateRepo(next); err != nil {
				logging.Errv("atlas", "写入仓库分支失败", err, "repo", r.Name)
			}
		}
	}
	return next
}

// ---- 时间文案 ----

// timeAgo 把时刻变成「刚刚 / 12 分钟前 / 3 小时前 / 2 天前 / 2026-10-01」。
func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "从未"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return t.Local().Format("2006-01-02")
	}
}

// dueText 截止日文案与是否已逾期（当天不算逾期）。
func dueText(t time.Time) (string, bool) {
	if t.IsZero() {
		return "", false
	}
	now := time.Now()
	day := t.Local()
	overdue := day.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()))
	switch {
	case sameDay(day, now):
		return "今天到期", false
	case sameDay(day, now.AddDate(0, 0, 1)):
		return "明天到期", false
	case overdue:
		return day.Format("01-02") + " 已逾期", true
	default:
		return day.Format("01-02") + " 到期", false
	}
}

// sameDay 判断两个时刻是否同一天（本地时区）。
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Local().Date()
	by, bm, bd := b.Local().Date()
	return ay == by && am == bm && ad == bd
}

// ---- 系统交互 ----

// openLocalPath 用系统文件管理器打开目录（仓库行上的「打开目录」）。
//
// 不用 mygo 的 Shell：它只有 OpenExternal（打开 URL）；打开本地目录在三个平台上
// 各有一条固定命令，直接调即可，失败也只是弹个提示。
func openLocalPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("未设置本地目录")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return fmt.Errorf("目录不存在：%s", path)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	// explorer.exe 打开成功也常返回非 0 退出码，所以只关心「启动失败」。
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("打开目录失败: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// workRoot 仓库默认所在的根目录：设置里为空时落到用户主目录下的 atlas-repos。
func (a *App) workRoot() string {
	if r := strings.TrimSpace(a.settings.WorkRoot); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "atlas-repos"
	}
	return filepath.Join(home, "atlas-repos")
}

// repoLocalPath 某仓库在本机的预期目录：显式设置优先，否则 根目录/仓库名。
func (a *App) repoLocalPath(name string) string {
	return filepath.Join(a.workRoot(), repoSafeDir(name))
}

// repoSafeDir 把 owner/repo 变成可以当目录名的形式。
func repoSafeDir(name string) string {
	s := strings.ReplaceAll(name, "/", "-")
	s = strings.ReplaceAll(s, "\\", "-")
	return strings.TrimSpace(s)
}

// ---- 小工具 ----

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// sortedRepos 复制一份按「状态优先级 + 名称」排好序的仓库列表。
func (d *atlasCache) sortedRepos() []store.Repo {
	out := make([]store.Repo, len(d.repos))
	copy(out, d.repos)
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := repoStatusRank(out[i].Status), repoStatusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// sortedTodos 复制一份按「状态 + 截止日 + 优先级」排好序的待办。
//
// 库里已经按这个顺序取过，这里再排一次是为了「刚改完还没重读」的那一帧也稳定。
func (d *atlasCache) sortedTodos() []store.Todo {
	out := make([]store.Todo, len(d.todos))
	copy(out, d.todos)
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := todoStatusRank(out[i].Status), todoStatusRank(out[j].Status)
		if si != sj {
			return si < sj
		}
		di, dj := out[i].DueAt.IsZero(), out[j].DueAt.IsZero()
		if di != dj {
			return !di // 有期限的排前面
		}
		if !di && !out[i].DueAt.Equal(out[j].DueAt) {
			return out[i].DueAt.Before(out[j].DueAt)
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].ID > out[j].ID
	})
	return out
}
