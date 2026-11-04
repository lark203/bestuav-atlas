package view

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/store"
)

// 这组测试守的是「三块业务页面能画出来、能读到自己写进去的数据」。
//
// 用 ui.NewTester 离屏渲染（项目既有约定：不依赖显示器、不需要真窗口），所以
// 断言看的是「渲染树里有没有这句文字」，而不是像素。真正的视觉问题（间距、对齐）
// 仍然要靠截图看，但「面板一片空白」「数据没接上」这类问题这里就能拦住。

// newTestApp 构造一个带临时数据库的 App。
func newTestApp(t *testing.T) (*App, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "atlas.db"))
	if err != nil {
		t.Fatalf("打开临时数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	a := newApp(settings.Default())
	a.AttachStore(st)
	return a, st
}

// seedAtlas 灌入一套最小但真实的数据：项目 + 仓库 + 待办。
func seedAtlas(t *testing.T, a *App, st *store.Store) (projectID, repoID int64) {
	t.Helper()
	projectID, err := st.CreateProject(store.Project{Name: "星图", Desc: "项目与仓库总控台", Status: "进行中"})
	if err != nil {
		t.Fatalf("建项目失败: %v", err)
	}
	repoID, err = st.CreateRepo(store.Repo{
		ProjectID: projectID,
		Name:      "lark203/bestuav-atlas",
		URL:       "https://github.com/lark203/bestuav-atlas",
		LocalPath: filepath.Join(t.TempDir(), "bestuav-atlas"),
		Branch:    "main",
		Status:    RepoDirty,
		Dirty:     3,
	})
	if err != nil {
		t.Fatalf("建仓库失败: %v", err)
	}
	if _, err := st.CreateTodo(store.Todo{
		Title: "把仓库页接上批量推送", ProjectID: projectID, RepoID: repoID,
		Priority: 2, Status: TodoOpen, DueAt: time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("建待办失败: %v", err)
	}
	a.reloadAtlas()
	return projectID, repoID
}

// renderPage 渲染某个页面，并把入场动画推进到结束。
//
// 为什么必须 Advance：页面里的卡片、弹窗、状态药丸都用 kit.Arrive 之类的入场
// 动画，t=0 时透明度还是 0——像素级检查（截图）会看到「弹窗没画出来」的假象。
func renderPage(t *testing.T, a *App, page string) *ui.Tester {
	t.Helper()
	a.page = page
	tt := ui.NewTester(func(c *ui.Context) { a.View(c) }, 1440, 900)
	tt.Frame()
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	return tt
}

// TestPagesRenderWithData 四个页面都能画出关键文案，且数据确实被读到。
func TestPagesRenderWithData(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)

	cases := []struct {
		page string
		want []string
	}{
		{"home", []string{"星图", "要做的", "把仓库页接上批量推送"}},
		{"todos", []string{"待办", "把仓库页接上批量推送", "详细填写…"}},
		{"projects", []string{"项目", "星图", "1 仓库"}},
		{"repos", []string{"lark203/bestuav-atlas", RepoDirty, "刷新状态", "3 改"}},
		{"settings", []string{"工作区", "仓库根目录", "GitHub 令牌"}},
	}
	for _, tc := range cases {
		t.Run(tc.page, func(t *testing.T) {
			tt := renderPage(t, a, tc.page)
			got := tt.Texts()
			joined := ""
			for _, s := range got {
				joined += s + "\n"
			}
			for _, want := range tc.want {
				if !tt.HasText(want) {
					t.Errorf("页面 %s 没有渲染出 %q；实际文本：\n%s", tc.page, want, joined)
				}
			}
		})
	}
}

// TestEmptyPagesRender 空库时首页/仓库页给出引导，而不是白屏。
func TestEmptyPagesRender(t *testing.T) {
	a, _ := newTestApp(t)

	tt := renderPage(t, a, "home")
	if !tt.HasText("建一个项目") {
		t.Errorf("空库首页应显示上手引导，实际文本：%v", tt.Texts())
	}
	tt = renderPage(t, a, "repos")
	if !tt.HasText("还没有仓库") {
		t.Errorf("空仓库页应显示导入引导，实际文本：%v", tt.Texts())
	}
}

// TestImportDialogRenders 导入弹窗能画出来（它挂在窗口根节点，最容易被漏接）。
func TestImportDialogRenders(t *testing.T) {
	a, _ := newTestApp(t)
	a.page = "repos"
	a.openImportDialog()

	tt := renderPage(t, a, "repos")
	if !tt.HasText("导入仓库") {
		t.Errorf("导入弹窗没有渲染；实际文本：%v", tt.Texts())
	}
	if !tt.HasText("从 GitHub 读取我的仓库") {
		t.Errorf("导入弹窗缺少 GitHub 入口；实际文本：%v", tt.Texts())
	}
}

// TestDialogsRender 三个编辑弹窗都能渲染出各自的字段与说明。
func TestDialogsRender(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)

	cases := []struct {
		name  string
		page  string
		open  func()
		wants []string
	}{
		{"仓库编辑", "repos", func() { a.openRepoDialog(store.Repo{ID: 1, Name: "a/b"}) },
			[]string{"本地目录", "分支", "保存"}},
		{"项目编辑", "projects", func() { a.openProjectDialog(store.Project{}) },
			[]string{"描述", "状态", "保存"}},
		{"待办编辑", "todos", func() { a.openTodoDialog(store.Todo{}) },
			[]string{"关联仓库", "优先级", "截止日", "保存"}},
		{"批量提交", "repos", func() { a.openCommitDialog([]int64{1}) },
			[]string{"提交说明", "提交并推送"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a.page = tc.page
			tc.open()
			tt := renderPage(t, a, tc.page)
			for _, want := range tc.wants {
				if !tt.HasText(want) {
					t.Errorf("弹窗 %s 缺少 %q；实际文本：%v", tc.name, want, tt.Texts())
				}
			}
			a.closeAtlasDialog()
		})
	}
}

// TestDialogFromOverviewStaysOpen 总览上的「新建项目」弹窗要能停在总览上；
// 切到别的页面则自动关闭（弹窗记录了打开它的页面）。
func TestDialogFromOverviewStaysOpen(t *testing.T) {
	a, _ := newTestApp(t)

	a.page = "home"
	a.openProjectDialog(store.Project{})
	tt := renderPage(t, a, "home")
	if !tt.HasText("项目是仓库与待办的归属单位") {
		t.Errorf("总览上的建档弹窗没画出来；实际文本：%v", tt.Texts())
	}

	a.page = "todos"
	renderPage(t, a, "todos")
	if a.atlasUI.dialog != "" {
		t.Errorf("切页后弹窗应自动关闭，实际仍是 %q", a.atlasUI.dialog)
	}
}

// TestRepoBulkBarRenders 选中仓库后，CrychicUI 的批量条出现并列出批量动作。
func TestRepoBulkBarRenders(t *testing.T) {
	a, st := newTestApp(t)
	_, repoID := seedAtlas(t, a, st)
	a.atlasUI.sel = map[int64]bool{repoID: true}

	tt := renderPage(t, a, "repos")
	for _, want := range []string{"已选 1 个仓库", "批量拉取", "批量提交并推送", "刷新远端信息", "清除选择"} {
		if !tt.HasText(want) {
			t.Errorf("批量条缺少 %q；实际文本：%v", want, tt.Texts())
		}
	}
}

// TestQuickAddTodo 快速添加：写库、清空输入、切回「全部」筛选。
func TestQuickAddTodo(t *testing.T) {
	a, _ := newTestApp(t)
	a.atlasUI.todoQuick = "写一个 E2E 冒烟"
	a.atlasUI.todoQuickProject = "未归属"
	a.atlasUI.todoFilter = TodoDone

	a.quickAddTodo()

	if a.atlasUI.todoQuick != "" {
		t.Errorf("添加后输入框应清空，实际 %q", a.atlasUI.todoQuick)
	}
	if a.atlasUI.todoFilter != "全部" {
		t.Errorf("添加后筛选应切回「全部」，实际 %q", a.atlasUI.todoFilter)
	}
	todos, err := a.store.ListTodos()
	if err != nil {
		t.Fatalf("读取待办失败: %v", err)
	}
	if len(todos) != 1 || todos[0].Title != "写一个 E2E 冒烟" {
		t.Fatalf("待办没有写进库：%+v", todos)
	}
}

// TestImportReposDedup 一次导入多行，重复与非法行被跳过。
func TestImportReposDedup(t *testing.T) {
	a, _ := newTestApp(t)

	added, skipped := a.importRepos([]string{
		"lark203/bestuav-atlas",
		"https://github.com/lark203/bestuav-mygo.git",
		"lark203/bestuav-atlas", // 重复
		"不是仓库",                  // 非法
		"",                     // 空行
	}, 0, filepath.Join(t.TempDir(), "repos"))

	if added != 2 || skipped != 2 {
		t.Fatalf("导入计数不对：added=%d skipped=%d，期望 2/2", added, skipped)
	}
	repos, err := a.store.ListRepos()
	if err != nil {
		t.Fatalf("读取仓库失败: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("库里应有 2 个仓库，实际 %d：%+v", len(repos), repos)
	}
	for _, r := range repos {
		if r.Status != RepoUnknown {
			t.Errorf("新导入的仓库状态应为「未知」，实际 %q", r.Status)
		}
		if r.LocalPath == "" {
			t.Errorf("指定了根目录时本地路径不该为空：%+v", r)
		}
	}
}

// TestFilteredRepos 状态与搜索筛选。
func TestFilteredRepos(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)

	if _, err := st.CreateRepo(store.Repo{Name: "lark203/bestuav-mygo", Status: RepoOK}); err != nil {
		t.Fatalf("建仓库失败: %v", err)
	}
	a.reloadAtlas()

	a.atlasUI.repoStatus = RepoDirty
	if got := a.filteredRepos(); len(got) != 1 || got[0].Name != "lark203/bestuav-atlas" {
		t.Fatalf("按状态筛选失败：%+v", got)
	}

	a.atlasUI.repoStatus = "全部"
	a.atlasUI.repoSearch = "mygo"
	if got := a.filteredRepos(); len(got) != 1 || got[0].Name != "lark203/bestuav-mygo" {
		t.Fatalf("按关键字筛选失败：%+v", got)
	}
}

// TestTodoHeat 今天到期 / 逾期的计数。
func TestTodoHeat(t *testing.T) {
	a, st := newTestApp(t)
	now := time.Now()
	yesterday := time.Date(now.Year(), now.Month(), now.Day()-1, 9, 0, 0, 0, time.Local)
	mustTodo := func(title string, due time.Time, status string) {
		if _, err := st.CreateTodo(store.Todo{Title: title, DueAt: due, Status: status}); err != nil {
			t.Fatalf("建待办失败: %v", err)
		}
	}
	mustTodo("今天到期", now, TodoOpen)
	mustTodo("昨天逾期", yesterday, TodoOpen)
	mustTodo("已完成的逾期不算", yesterday, TodoDone)
	mustTodo("无期限", time.Time{}, TodoOpen)

	dueToday, overdue := a.todoHeat()
	if dueToday != 1 || overdue != 1 {
		t.Fatalf("到期统计不对：today=%d overdue=%d，期望 1/1", dueToday, overdue)
	}
}
