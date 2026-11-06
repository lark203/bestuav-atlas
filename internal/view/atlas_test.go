package view

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/forge"
	"bestuav-atlas/internal/gitx"
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
		// 评分与评价：详情页与表格的星级都读这几个字段。
		Importance: 4, Learning: 3, Difficulty: 2,
		Review: "值得长期跟的一个项目。",
	})
	if err != nil {
		t.Fatalf("建仓库失败: %v", err)
	}
	if _, err := st.CreateTodo(store.Todo{
		Title: "把仓库页接上批量推送", ProjectID: projectID, RepoID: repoID,
		Priority: 2, Important: true, Urgent: true, Status: TodoOpen,
		DueAt: time.Now().Add(24 * time.Hour),
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
		{"settings", []string{"常规", "工作区", "快捷键", "关于与诊断", "异步任务", "最大并发"}},
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

// TestWorkspaceSettingsTab 工作区单独一页：根目录与四个平台的令牌都在这里。
func TestWorkspaceSettingsTab(t *testing.T) {
	a, _ := newTestApp(t)
	a.pageStat(settingsPageID).data[settingsGroupKey] = settingsGroupWorkspace

	tt := renderPage(t, a, "settings")
	for _, want := range []string{"仓库根目录", "GitHub 令牌", "Gitee 令牌", "AtomGit 令牌", "Gitea 实例"} {
		if !tt.HasText(want) {
			t.Errorf("工作区页缺少 %q；实际文本：%v", want, tt.Texts())
		}
	}
	// 常规页的外观设置不应同时出现（一次只渲染一组）。
	if tt.HasText("字体大小") {
		t.Errorf("工作区页不应渲染常规页的外观设置")
	}
}

// TestGiteaSchemeFollowsConfig 自建 Gitea 的协议以配置为准：填 http:// 就全程 http。
func TestGiteaSchemeFollowsConfig(t *testing.T) {
	a, _ := newTestApp(t)
	a.settings.GiteaHost = "http://gitea.local:3000"

	// 另一种情况：仓库登记里只有 host（没有 URL），协议从设置取。
	p := a.platformForRepo(store.Repo{Name: "team/repo", Host: "gitea.local:3000"})
	if p.Provider != forge.Gitea || p.Scheme != "http" {
		t.Fatalf("平台应识别为 Gitea + http：%+v", p)
	}
	if got := p.WebURL("team", "repo"); got != "http://gitea.local:3000/team/repo" {
		t.Errorf("WebURL 应跟随配置的 http：%q", got)
	}

	// 导入时地址自带 http：存下来的 URL 必须还是 http。
	added, _, ids := a.importRepos([]string{"http://gitea.local:3000/team/repo.git"},
		0, filepath.Join(t.TempDir(), "repos"), forge.PlatformForHost("github.com"))
	if added != 1 || len(ids) != 1 {
		t.Fatalf("导入失败：added=%d ids=%v", added, ids)
	}
	r, ok := a.repoByID(ids[0])
	if !ok {
		t.Fatal("仓库没读到")
	}
	if r.Host != "gitea.local:3000" || r.URL != "http://gitea.local:3000/team/repo" {
		t.Errorf("导入结果应保留 http 协议：host=%q url=%q", r.Host, r.URL)
	}

	// 配置改成 https 后，同一个只存 host 的仓库应回到 https。
	a.settings.GiteaHost = "https://gitea.local:3000"
	if got := a.platformForRepo(store.Repo{Name: "team/repo", Host: "gitea.local:3000"}).Scheme; got == "http" {
		t.Errorf("配置 https 时不应再用 http：%q", got)
	}
}

// TestEraseRepos 删除仓库登记：只动本地登记，选中态一起清掉。
func TestEraseRepos(t *testing.T) {
	a, st := newTestApp(t)
	_, repoID := seedAtlas(t, a, st)
	second, err := st.CreateRepo(store.Repo{Name: "lark203/other", Status: RepoOK})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()
	a.atlasUI.sel = map[int64]bool{repoID: true, second: true}

	a.eraseRepos([]int64{repoID, second})

	repos, err := a.store.ListRepos()
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 0 {
		t.Errorf("登记应被删空，实际还剩 %d 个", len(repos))
	}
	if len(a.atlasUI.sel) != 0 {
		t.Errorf("删除后选中态应清空，实际 %v", a.atlasUI.sel)
	}
}

// TestEraseDialogRenders 删除确认框能画出来，并带上数量。
func TestEraseDialogRenders(t *testing.T) {
	a, st := newTestApp(t)
	_, repoID := seedAtlas(t, a, st)
	a.page = "repos"
	a.confirmErase([]int64{repoID})

	tt := renderPage(t, a, "repos")
	for _, want := range []string{"删除仓库登记", "1 个仓库"} {
		if !tt.HasText(want) {
			t.Errorf("删除确认缺少 %q；实际文本：%v", want, tt.Texts())
		}
	}
}

// TestRepoDetailHeaderBack 详情页的返回键在页头（标题左侧），页内不再有「← 返回」。
func TestRepoDetailHeaderBack(t *testing.T) {
	a, st := newTestApp(t)
	_, repoID := seedAtlas(t, a, st)
	if r, ok := a.repoByID(repoID); ok {
		a.openRepoDetail(r)
	}
	tt := renderPage(t, a, "repo-detail")
	if tt.HasText("← 返回") {
		t.Errorf("页内不应再有返回按钮；实际文本：%v", tt.Texts())
	}
	if !tt.HasText("仓库详情") {
		t.Errorf("页头标题缺失；实际文本：%v", tt.Texts())
	}
}

// TestHiddenPageConventions 隐藏页必须声明 back，且 back 目标必须存在——
// 否则用户进得去出不来（侧栏没有它，页头也没有返回键）。
func TestHiddenPageConventions(t *testing.T) {
	eachPage(func(it navItem) {
		if it.hidden && it.back == "" {
			t.Errorf("隐藏页 %q 没有声明 back，进去就出不来了", it.id)
		}
		if it.back != "" {
			if _, ok := pageByID(it.back); !ok {
				t.Errorf("页面 %q 的 back 指向不存在的页面 %q", it.id, it.back)
			}
		}
	})
}

// TestHeaderBackNavigates 页头返回键点了之后真的会跳页（与 mygo 同一套实现）。
func TestHeaderBackNavigates(t *testing.T) {
	a, _ := newTestApp(t)
	a.page = "repo-detail"

	tt := ui.NewTester(func(c *ui.Context) {
		a.headerBack(c, c.Theme(), "repos")
	}, 320, 80)
	tt.Frame()
	if err := tt.Click("返回"); err != nil {
		t.Fatalf("点击返回键失败: %v", err)
	}
	tt.Frame()
	if a.page != "repos" {
		t.Errorf("点击返回后应跳到 repos，实际 %q", a.page)
	}
}

// TestRefreshRemoteFetches 「刷新远端信息」必须先 fetch：远端有新提交时状态要变成
// 「需要更新」。这条测试钉住用户报过的 bug——只调 API、不 fetch 时状态永远不动。
func TestRefreshRemoteFetches(t *testing.T) {
	if !gitx.HasGit() {
		t.Skip("本机没有 git")
	}
	src := t.TempDir()
	runGit(t, src, "init", "-b", "main")
	runGit(t, src, "config", "user.name", "atlas-test")
	runGit(t, src, "config", "user.email", "atlas@example.com")
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-m", "init")

	bare := filepath.Join(t.TempDir(), "demo.git")
	runGit(t, t.TempDir(), "clone", "--bare", src, bare)

	local := filepath.Join(t.TempDir(), "local")
	runGit(t, t.TempDir(), "clone", bare, local)
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, t.TempDir(), "clone", bare, other)

	// 另一个人（另一个克隆）推了一个提交：此时远端领先本地。
	runGit(t, other, "config", "user.name", "other")
	runGit(t, other, "config", "user.email", "other@example.com")
	if err := os.WriteFile(filepath.Join(other, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "-A")
	runGit(t, other, "commit", "-m", "remote change")
	runGit(t, other, "push", "origin", "main")

	a, st := newTestApp(t)
	// 平台指向一个必然拒绝连接的端口：API 那一步不做，不影响状态刷新。
	id, err := st.CreateRepo(store.Repo{
		Name: "team/repo", Host: "127.0.0.1:9", URL: "http://127.0.0.1:9/team/repo",
		LocalPath: local, Branch: "main", Status: RepoOK,
	})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()
	r, ok := a.repoByID(id)
	if !ok {
		t.Fatal("仓库没读到")
	}

	if err := a.refreshRemoteOne(context.Background(), r, a.tokensSnapshot()); err != nil {
		t.Fatalf("刷新远端信息失败: %v", err)
	}
	after, ok, err := a.store.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("读库失败: %v", err)
	}
	if after.Status != RepoBehind {
		t.Errorf("远端有新提交时应为「%s」，实际 %q（behind=%d）", RepoBehind, after.Status, after.Behind)
	}
	if after.Behind < 1 {
		t.Errorf("behind 应至少为 1，实际 %d", after.Behind)
	}
}

// TestTaskRowRecordsDetailAndDuration 任务队列记录真实耗时与失败原因，并落到数据库。
func TestTaskRowRecordsDetailAndDuration(t *testing.T) {
	a, st := newTestApp(t)
	a.pushTaskRow("批量拉取更新", "手动", "失败", "xl/repo：认证失败", 1500*time.Millisecond)

	if len(a.tasks) != 1 {
		t.Fatalf("应有 1 条任务，实际 %d", len(a.tasks))
	}
	if got := a.tasks[0].detail; got != "xl/repo：认证失败" {
		t.Errorf("失败原因没记下来：%q", got)
	}
	if got := a.tasks[0].duration; got != 1500*time.Millisecond {
		t.Errorf("耗时应为 1.5s，实际 %v", got)
	}

	rows, err := st.RecentTasks(10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("读库失败: %v（%d 条）", err, len(rows))
	}
	if rows[0].Detail != "xl/repo：认证失败" || rows[0].DurationMS != 1500 {
		t.Errorf("落库字段不对：%+v", rows[0])
	}

	a.page = "task"
	tt := renderPage(t, a, "task")
	for _, want := range []string{"批量拉取更新", "手动", "失败", "1.5s", "xl/repo：认证失败"} {
		if !tt.HasText(want) {
			t.Errorf("任务表缺少 %q；实际文本：%v", want, tt.Texts())
		}
	}
}

// TestRepoAutoRefreshTicksWithoutFrames 自动刷新不依赖帧循环：没有窗口帧
// （托盘 / 最小化）时，只要到点，tick 也会启动一轮并排好下一轮。
func TestRepoAutoRefreshTicksWithoutFrames(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)
	// 后台协程在测试结束后还会写一次库：先睡一会再让 newTestApp 注册的
	// cleanup 关库，避免出现 "database is closed" 噪声日志。
	t.Cleanup(func() { time.Sleep(300 * time.Millisecond) })
	a.settings.RepoAutoRefresh = true

	// 刚开启：从下一个完整间隔开始，不立刻联网。
	if d := a.tickRepoAutoRefresh(); d <= 0 || a.taskRunning {
		t.Fatalf("刚开启应先排期、不执行：d=%v running=%v", d, a.taskRunning)
	}

	// 把时间拨到「已到点」：没有窗口、没有帧，也应该启动自动刷新。
	a.repoAutoOn = true
	a.repoAutoMinutes = a.repoAutoIntervalMinutes()
	a.repoAutoAt = time.Now().Add(-time.Second)
	if d := a.tickRepoAutoRefresh(); d <= 0 {
		t.Fatalf("到点后应返回下一轮间隔，实际 %v", d)
	}
	if !a.taskRunning {
		t.Fatal("到点应启动自动刷新（不需要窗口帧）")
	}
	if !a.repoAutoAt.After(time.Now()) {
		t.Error("到点后应把 repoAutoAt 推到下一轮")
	}

	// 收尾：后台协程的结果回调依赖真实主循环，测试里不会执行，手动复位即可。
	a.taskRunning = false
	a.taskProgress = 0
	a.taskLabel = ""
	a.atlasUI.busy = false
}

// TestTodoCountdownFreezes 已完成的逾期时长冻结在完成那一刻，不再随现在增长。
func TestTodoCountdownFreezes(t *testing.T) {
	now := time.Now()
	doneLate := store.Todo{
		Status: TodoDone,
		DueAt:  now.Add(-48 * time.Hour),
		DoneAt: now.Add(-24 * time.Hour),
	}
	first, overdue := todoCountdown(doneLate)
	if !overdue || !strings.HasPrefix(first, "逾期完成") {
		t.Fatalf("逾期完成的文案不对：%q（overdue=%v）", first, overdue)
	}
	// 文案只依赖 DoneAt，隔多久再看都一样。
	if second, _ := todoCountdown(doneLate); second != first {
		t.Errorf("完成后的逾期时长不应变化：%q vs %q", first, second)
	}

	doneOnTime := store.Todo{
		Status: TodoDone,
		DueAt:  now.Add(24 * time.Hour),
		DoneAt: now.Add(-time.Hour),
	}
	if label, od := todoCountdown(doneOnTime); label != "按时完成" || od {
		t.Errorf("按时完成的文案不对：%q（overdue=%v）", label, od)
	}

	open := store.Todo{DueAt: now.Add(time.Hour)}
	if label, od := todoCountdown(open); od || !strings.HasPrefix(label, "剩 ") {
		t.Errorf("未完成应是倒计时：%q（overdue=%v）", label, od)
	}
}

// TestTodoActiveToday 「今天要做」= 未完成 且 开始日 ≤ 今天 ≤ 截止日。
func TestTodoActiveToday(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		td   store.Todo
		want bool
	}{
		{"昨天开始今天截止", store.Todo{StartAt: now.AddDate(0, 0, -1), DueAt: now}, true},
		{"今天开始无截止", store.Todo{StartAt: now}, true},
		{"明天开始", store.Todo{StartAt: now.AddDate(0, 0, 1)}, false},
		{"昨天已截止（逾期）", store.Todo{StartAt: now.AddDate(0, 0, -3), DueAt: now.AddDate(0, 0, -1)}, false},
		{"已完成", store.Todo{StartAt: now.AddDate(0, 0, -1), DueAt: now, Status: TodoDone}, false},
	}
	for _, c := range cases {
		if got := todoActiveToday(c.td, now); got != c.want {
			t.Errorf("%s：got %v want %v", c.name, got, c.want)
		}
	}
}

// TestStickyBuckets 便签按截止时刻分早 / 中 / 晚 / 全天。
func TestStickyBuckets(t *testing.T) {
	day := func(h, m int) time.Time {
		n := time.Now()
		return time.Date(n.Year(), n.Month(), n.Day(), h, m, 0, 0, n.Location())
	}
	cases := map[string]struct {
		td   store.Todo
		want string
	}{
		"早":  {store.Todo{DueAt: day(9, 0)}, "早"},
		"中":  {store.Todo{DueAt: day(13, 30)}, "中"},
		"晚":  {store.Todo{DueAt: day(20, 0)}, "晚"},
		"全天": {store.Todo{}, "全天"},
	}
	for want, c := range cases {
		if got := todoBucket(c.td); got != want {
			t.Errorf("bucket 应为 %s，实际 %s", want, got)
		}
	}
}

// TestMonthStats 月度统计：新建 / 完成 / 逾期按各自时间归月。
func TestMonthStats(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	thisMonth := time.Date(2026, 11, 2, 9, 0, 0, 0, time.Local)
	lastMonth := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	todos := []store.Todo{
		// 本月新建、本月按时完成
		{CreatedAt: thisMonth, DueAt: thisMonth.Add(48 * time.Hour), DoneAt: thisMonth.Add(24 * time.Hour), Status: TodoDone},
		// 本月新建、逾期未完成
		{CreatedAt: thisMonth, DueAt: thisMonth.Add(-24 * time.Hour), Status: TodoOpen},
		// 上月新建并完成
		{CreatedAt: lastMonth, DueAt: lastMonth.Add(24 * time.Hour), DoneAt: lastMonth.Add(2 * time.Hour), Status: TodoDone},
	}
	stats := monthStats(todos, now, 2)
	if len(stats) != 2 {
		t.Fatalf("应有 2 个月，实际 %d", len(stats))
	}
	oct, nov := stats[0], stats[1]
	if oct.Created != 1 || oct.Done != 1 || oct.Overdue != 0 {
		t.Errorf("10 月统计不对：%+v", oct)
	}
	if nov.Created != 2 || nov.Done != 1 || nov.Overdue != 1 {
		t.Errorf("11 月统计不对：%+v", nov)
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
			[]string{"关联仓库", "优先级", "四象限", "截止时间", "保存"}},
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

// TestImportReposDedup 一次导入多行，重复与非法行被跳过；平台按域名识别。
func TestImportReposDedup(t *testing.T) {
	a, _ := newTestApp(t)

	added, skipped, ids := a.importRepos([]string{
		"lark203/bestuav-atlas",
		"https://github.com/lark203/bestuav-mygo.git",
		"https://gitee.com/mirrors/gin.git",
		"lark203/bestuav-atlas", // 重复
		"不是仓库",                  // 非法
		"",                      // 空行
	}, 0, filepath.Join(t.TempDir(), "repos"), forge.PlatformForHost("github.com"))

	if added != 3 || skipped != 2 {
		t.Fatalf("导入计数不对：added=%d skipped=%d，期望 3/2", added, skipped)
	}
	if len(ids) != added {
		t.Errorf("返回的 id 数应与成功数一致：ids=%d added=%d", len(ids), added)
	}
	repos, err := a.store.ListRepos()
	if err != nil {
		t.Fatalf("读取仓库失败: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("库里应有 3 个仓库，实际 %d：%+v", len(repos), repos)
	}
	hosts := map[string]string{}
	for _, r := range repos {
		if r.Status != RepoUnknown {
			t.Errorf("新导入的仓库状态应为「未知」，实际 %q", r.Status)
		}
		if r.LocalPath == "" {
			t.Errorf("指定了根目录时本地路径不该为空：%+v", r)
		}
		// 目录名只取仓库名，不拼 owner（例如 lark203/bestuav-atlas → bestuav-atlas）。
		if dir := filepath.Base(r.LocalPath); dir != "bestuav-atlas" && dir != "bestuav-mygo" && dir != "gin" {
			t.Errorf("本地目录应只含仓库名，实际 %q（仓库 %q）", r.LocalPath, r.Name)
		}
		hosts[r.Name] = r.Host
	}
	if hosts["mirrors/gin"] != "gitee.com" {
		t.Errorf("Gitee 地址应识别出平台：%+v", hosts)
	}
	if hosts["lark203/bestuav-atlas"] != "github.com" {
		t.Errorf("纯 owner/repo 行应落到默认平台（GitHub）：%+v", hosts)
	}
}

// TestRepoDetailPage 详情页能读到本地 README、显示评分，并回填评价。
func TestRepoDetailPage(t *testing.T) {
	a, st := newTestApp(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"),
		[]byte("# 演示仓库\n\n这是一段用来验证详情页渲染的说明文字。\n"), 0o644); err != nil {
		t.Fatalf("写 README 失败: %v", err)
	}
	id, err := st.CreateRepo(store.Repo{
		Name: "lark203/demo", LocalPath: dir, Status: RepoOK,
		Importance: 5, Learning: 4, Difficulty: 1, Review: "先读 README。",
	})
	if err != nil {
		t.Fatalf("建仓库失败: %v", err)
	}
	a.reloadAtlas()
	r, ok := a.repoByID(id)
	if !ok {
		t.Fatal("仓库没读到")
	}
	a.openRepoDetail(r)

	tt := renderPage(t, a, "repo-detail")
	for _, want := range []string{"我的评价", "重要程度", "可学习", "难度", "这是一段用来验证详情页渲染的说明文字。"} {
		if !tt.HasText(want) {
			t.Errorf("详情页缺少 %q；实际文本：%v", want, tt.Texts())
		}
	}
	// 评分表单应回填库里的值。
	if a.atlasUI.detailImport != 5 || a.atlasUI.detailLearn != 4 || a.atlasUI.detailDiff != 1 {
		t.Errorf("评分未回填：%+v", a.atlasUI)
	}
	if a.atlasUI.detailReview != "先读 README。" {
		t.Errorf("评价未回填：%q", a.atlasUI.detailReview)
	}
}

// TestCloneOne 导入后顺手克隆：把本地 bare 仓库克隆到登记的目录，并刷新状态。
func TestCloneOne(t *testing.T) {
	if !gitx.HasGit() {
		t.Skip("本机没有 git")
	}
	src := t.TempDir()
	runGit(t, src, "init", "-b", "main")
	runGit(t, src, "config", "user.name", "atlas-test")
	runGit(t, src, "config", "user.email", "atlas@example.com")
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "-A")
	runGit(t, src, "commit", "-m", "init")

	// 用本地 bare 仓库当「远端」：git clone 对普通路径同样有效，不需要联网。
	bare := filepath.Join(t.TempDir(), "demo.git")
	runGit(t, t.TempDir(), "clone", "--bare", src, bare)

	a, _ := newTestApp(t)
	dest := filepath.Join(t.TempDir(), "cloned")
	id, err := a.store.CreateRepo(store.Repo{Name: "demo/repo", URL: bare, LocalPath: dest})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()
	r, ok := a.repoByID(id)
	if !ok {
		t.Fatal("仓库没读到")
	}
	if err := a.cloneOne(context.Background(), r); err != nil {
		t.Fatalf("克隆失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "main.go")); err != nil {
		t.Fatalf("克隆后应有 main.go: %v", err)
	}
	after, ok, err := a.store.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("读库失败: %v", err)
	}
	if after.Status == RepoUnknown || after.Branch != "main" {
		t.Errorf("克隆后状态/分支应被刷新：status=%q branch=%q", after.Status, after.Branch)
	}
}

// runGit 在 dir 里跑一条 git 命令，失败即测试失败。
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestImportLocalRepos 从本地文件夹导入：远端地址决定名字，状态自动探测。
func TestImportLocalRepos(t *testing.T) {
	if !gitx.HasGit() {
		t.Skip("本机没有 git")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "atlas-test")
	git("config", "user.email", "atlas@example.com")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "init")
	git("remote", "add", "origin", "https://github.com/lark203/demo-repo.git")

	a, _ := newTestApp(t)
	added, skipped, firstSkip := a.importLocalRepos([]string{dir}, 0)
	if added != 1 || skipped != 0 {
		t.Fatalf("导入结果不对：added=%d skipped=%d (%s)", added, skipped, firstSkip)
	}
	repos, err := a.store.ListRepos()
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 {
		t.Fatalf("库里应有 1 个仓库，实际 %d", len(repos))
	}
	r := repos[0]
	if r.Name != "lark203/demo-repo" {
		t.Errorf("名字应从 origin 解析，实际 %q", r.Name)
	}
	if r.Host != "github.com" {
		t.Errorf("平台主机应从 origin 解析，实际 %q", r.Host)
	}
	if r.LocalPath != dir {
		t.Errorf("本地目录不对：%q", r.LocalPath)
	}
	if r.Branch != "main" || r.Status != RepoOK {
		t.Errorf("分支/状态不对：branch=%q status=%q", r.Branch, r.Status)
	}

	// 再导一次应该被「已在列表里」挡掉。
	added2, skipped2, _ := a.importLocalRepos([]string{dir}, 0)
	if added2 != 0 || skipped2 != 1 {
		t.Errorf("重复导入应被跳过：added=%d skipped=%d", added2, skipped2)
	}
}

// TestQuadrantAndCountdown 四象限归类与倒计时文案。
func TestQuadrantAndCountdown(t *testing.T) {
	cases := []struct {
		important, urgent bool
		want              int
	}{
		{true, true, 1}, {true, false, 2}, {false, true, 3}, {false, false, 4},
	}
	for _, tc := range cases {
		got := quadrantOf(store.Todo{Important: tc.important, Urgent: tc.urgent})
		if got != tc.want {
			t.Errorf("象限(%v,%v)=%d，期望 %d", tc.important, tc.urgent, got, tc.want)
		}
	}

	if s, overdue := countdownText(time.Now().Add(90 * time.Minute)); overdue || s == "" {
		t.Errorf("未到期应显示剩余时间，实际 %q overdue=%v", s, overdue)
	}
	if s, overdue := countdownText(time.Now().Add(-2 * time.Hour)); !overdue || s == "" {
		t.Errorf("已过期应显示逾期，实际 %q overdue=%v", s, overdue)
	}
	if s, _ := countdownText(time.Time{}); s != "" {
		t.Errorf("无期限应返回空串，实际 %q", s)
	}
}

// TestDefaultTodoDue 新待办默认的截止时刻是「未来的下一个 18:00」。
func TestDefaultTodoDue(t *testing.T) {
	now := time.Now()
	due := defaultTodoDue()
	if !due.After(now) {
		t.Errorf("默认截止时间应在未来：now=%v due=%v", now, due)
	}
	if due.Hour() != 18 || due.Minute() != 0 {
		t.Errorf("默认截止时间应为 18:00，实际 %v", due)
	}
	if due.Sub(now) > 25*time.Hour {
		t.Errorf("默认截止时间离现在太远：%v", due.Sub(now))
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
