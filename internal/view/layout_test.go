package view

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/gitx"
	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/store"
)

// 这一组测试守的是「窗口伸缩时不出错」：总览两张卡片等高、小窗口里弹窗
// 底部按钮仍可见、仓库表在窄窗口里能横向滚动、目录名只取仓库名。
//
// 断言看的是渲染树里的几何（ui.Tester.Find 返回 DIP 矩形），不是像素；
// 这样不依赖显示器，也能拦住「按钮被裁到窗外」「两栏一高一矮」这类回归。

// renderPageSized 以指定窗口尺寸渲染页面（renderPage 固定 1440×900）。
func renderPageSized(t *testing.T, a *App, page string, w, h int) *ui.Tester {
	t.Helper()
	a.page = page
	tt := ui.NewTester(func(c *ui.Context) { a.View(c) }, w, h)
	tt.Frame()
	tt.Advance(600 * time.Millisecond)
	tt.Frame()
	tt.Frame()
	return tt
}

// TestOverviewCardsEqualHeight 总览「要做的」与「仓库」两张卡必须同高同顶。
func TestOverviewCardsEqualHeight(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)

	for _, size := range [][2]int{{1440, 900}, {1100, 800}} {
		tt := renderPageSized(t, a, "home", size[0], size[1])
		todo, ok := tt.Find("总览·要做的")
		if !ok {
			t.Fatalf("%dx%d：找不到「要做的」卡片", size[0], size[1])
		}
		repo, ok := tt.Find("总览·仓库")
		if !ok {
			t.Fatalf("%dx%d：找不到「仓库」卡片", size[0], size[1])
		}
		if math.Abs(float64(todo.H-repo.H)) > 1 {
			t.Errorf("%dx%d：两张卡高度不一致：要做的 %.1f，仓库 %.1f",
				size[0], size[1], todo.H, repo.H)
		}
		if math.Abs(float64(todo.Y-repo.Y)) > 1 {
			t.Errorf("%dx%d：两张卡顶部未对齐：要做的 %.1f，仓库 %.1f",
				size[0], size[1], todo.Y, repo.Y)
		}
	}
}

// TestImportDialogFooterVisibleInSmallWindow 窗口变矮时，正文可以滚，但
// 「取消 / 导入远程仓库」必须始终在窗口内。
func TestImportDialogFooterVisibleInSmallWindow(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)
	a.page = "repos"
	a.openImportDialog()

	tt := renderPageSized(t, a, "repos", 900, 600)
	for _, label := range []string{"取消", "导入远程仓库"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("小窗口里找不到底部按钮 %q", label)
		}
		if r.Y < 0 || r.Y+r.H > 600 {
			t.Errorf("小窗口里 %q 被裁到窗外：y=%.1f h=%.1f", label, r.Y, r.H)
		}
	}

	// 在正文区域滚一下：底部按钮的位置不应该跟着动。
	body, ok := tt.Find("从远程仓库导入")
	if !ok {
		t.Fatal("找不到正文标题")
	}
	before, _ := tt.Find("导入远程仓库")
	tt.Wheel(body.X+body.W/2, body.Y+body.H/2, 0, 260)
	tt.Frame()
	after, ok := tt.Find("导入远程仓库")
	if !ok {
		t.Fatal("滚动后底部按钮消失了")
	}
	if math.Abs(float64(before.Y-after.Y)) > 1 {
		t.Errorf("正文滚动时底部按钮跟着移动了：%.1f → %.1f", before.Y, after.Y)
	}
}

// TestReposTableScrollsSideways 窄窗口里仓库表必须能横向滚动，最后一列
// 「操作」不是被裁掉、更不是「没有数据」。
func TestReposTableScrollsSideways(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)
	a.page = "repos"

	tt := renderPageSized(t, a, "repos", 900, 700)
	table, ok := tt.Find("仓库列表")
	if !ok {
		t.Fatal("找不到仓库表")
	}
	before, ok := tt.Find("删除")
	if !ok {
		t.Fatal("找不到行内「删除」")
	}

	// 表格内容比视口宽时，第一屏的「删除」在表格右侧之外。
	if before.X+before.W <= table.X+table.W {
		t.Logf("表格没有溢出（table=%.0f..%.0f delete=%.0f..%.0f），跳过横向滚动断言",
			table.X, table.X+table.W, before.X, before.X+before.W)
		return
	}
	tt.Wheel(table.X+table.W/2, table.Y+table.H/2, 500, 0)
	tt.Advance(400 * time.Millisecond) // 鼠标滚轮是滑行动画，要推进时钟
	tt.Frame()
	after, ok := tt.Find("删除")
	if !ok {
		t.Fatal("横向滚动后找不到「删除」")
	}
	if after.X >= before.X {
		t.Errorf("横向滚动没有生效：删除列 x %.1f → %.1f", before.X, after.X)
	}
	if after.X+after.W > 900 {
		t.Errorf("横向滚动后「删除」仍在窗口外：x=%.1f w=%.1f", after.X, after.W)
	}
}

// TestRepoProjectCellPicksProject 点表格里的项目单元格，能从菜单改归属并写库。
func TestRepoProjectCellPicksProject(t *testing.T) {
	a, st := newTestApp(t)
	_, repoID := seedAtlas(t, a, st)
	otherID, err := st.CreateProject(store.Project{Name: "量子科学", Status: "进行中"})
	if err != nil {
		t.Fatalf("建第二个项目失败: %v", err)
	}
	a.reloadAtlas()
	a.page = "repos"

	tt := renderPageSized(t, a, "repos", 1440, 900)
	if err := tt.Click("归属项目：星图"); err != nil {
		t.Fatalf("点项目单元格失败: %v；文本：%v", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("量子科学") {
		t.Fatalf("项目菜单里没有另一个项目；文本：%v", tt.Texts())
	}
	if err := tt.Click("量子科学"); err != nil {
		t.Fatalf("点菜单项失败: %v", err)
	}
	tt.Frame()

	got, ok, err := st.RepoByID(repoID)
	if err != nil || !ok {
		t.Fatalf("读仓库失败: ok=%v err=%v", ok, err)
	}
	if got.ProjectID != otherID {
		t.Fatalf("归属项目未写库：got=%d want=%d", got.ProjectID, otherID)
	}
}

// TestDueTodosSelection 只挑「已到期、未完成、未提醒」的待办。
func TestDueTodosSelection(t *testing.T) {
	now := time.Now()
	todos := []store.Todo{
		{ID: 1, Title: "刚到期", DueAt: now},
		{ID: 2, Title: "已过期", DueAt: now.Add(-time.Minute)},
		{ID: 3, Title: "未来", DueAt: now.Add(time.Minute)},
		{ID: 4, Title: "已完成", DueAt: now.Add(-time.Minute), Status: TodoDone},
		{ID: 5, Title: "已提醒", DueAt: now.Add(-time.Minute), NotifiedAt: now.Add(-time.Minute)},
		{ID: 6, Title: "无期限"},
	}
	got := dueTodos(todos, now)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("到期筛选不对：%+v", got)
	}
}

// TestTodoDialogUsesPickers 编辑有期限的待办时，弹出的是日期/时间选择器，
// 不再出现手填日期的文本框。
func TestTodoDialogUsesPickers(t *testing.T) {
	a, st := newTestApp(t)
	seedAtlas(t, a, st)
	a.page = "todos"
	a.openTodoDialog(a.atlas().todos[0])

	tt := renderPageSized(t, a, "todos", 1440, 900)
	for _, want := range []string{"截止日期", "截止时间", "设置截止时间"} {
		if !tt.HasText(want) {
			t.Errorf("待办弹窗缺少截止时间选择器 %q；实际文本：%v", want, tt.Texts())
		}
	}
	if tt.HasText("2026-11-30 18:00 或 2026-11-30，可空") {
		t.Error("不应再出现手填截止时间的占位提示")
	}
}

// TestUnassignedCardButtons 未归属区按实际内容给入口：有仓库去仓库页，
// 有待办去待办页，两者都有就两个都给。
func TestUnassignedCardButtons(t *testing.T) {
	cases := []struct {
		name               string
		repo, todo         bool
		wantRepo, wantTodo bool
	}{
		{"只有仓库", true, false, true, false},
		{"只有待办", false, true, false, true},
		{"两者都有", true, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, st := newTestApp(t)
			if _, err := st.CreateProject(store.Project{Name: "有项目", Status: "进行中"}); err != nil {
				t.Fatal(err)
			}
			if tc.repo {
				if _, err := st.CreateRepo(store.Repo{Name: "a/b", Status: RepoUnknown}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.todo {
				if _, err := st.CreateTodo(store.Todo{Title: "未归属待办", Status: TodoOpen}); err != nil {
					t.Fatal(err)
				}
			}
			a.reloadAtlas()

			tt := renderPageSized(t, a, "projects", 1440, 900)
			if got := tt.HasText("去仓库页"); got != tc.wantRepo {
				t.Errorf("去仓库页按钮 = %v，期望 %v", got, tc.wantRepo)
			}
			if got := tt.HasText("去待办页"); got != tc.wantTodo {
				t.Errorf("去待办页按钮 = %v，期望 %v", got, tc.wantTodo)
			}
		})
	}
}

// TestTitleHoverShowsNote 鼠标停在待办标题上时显示备注。
func TestTitleHoverShowsNote(t *testing.T) {
	a, st := newTestApp(t)
	if _, err := st.CreateTodo(store.Todo{
		Title: "带备注的待办", Note: "这是备注内容", Status: TodoOpen,
	}); err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()

	tt := renderPageSized(t, a, "todos", 1440, 900)
	r, ok := tt.Find("带备注的待办")
	if !ok {
		t.Fatal("找不到待办标题")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("这是备注内容") {
		t.Fatalf("悬停标题没有显示备注；文本：%v", tt.Texts())
	}
}

// TestRepoNameHoverShowsReview 鼠标停在仓库名上时显示评价。
func TestRepoNameHoverShowsReview(t *testing.T) {
	a, st := newTestApp(t)
	if _, err := st.CreateRepo(store.Repo{
		Name: "demo/repo", Review: "这是仓库评价", Status: RepoOK,
	}); err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()

	tt := renderPageSized(t, a, "repos", 1440, 900)
	r, ok := tt.Find("demo/repo")
	if !ok {
		t.Fatal("找不到仓库名")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(time.Second)
	tt.Frame()
	if !tt.HasText("这是仓库评价") {
		t.Fatalf("悬停仓库名没有显示评价；文本：%v", tt.Texts())
	}
}

// TestGitHubTokensAreSeparate 工作区 GitHub 令牌与配置包同步令牌互不覆盖。
func TestGitHubTokensAreSeparate(t *testing.T) {
	a, _ := newTestApp(t)
	a.settings.RepoToken = "sync-token"
	a.settings.GitHubToken = "workspace-token"

	if got := a.tokenFor("github.com"); got != "workspace-token" {
		t.Fatalf("星图仓库页应使用工作区 GitHubToken，实际 %q", got)
	}
	if got := a.tokensSnapshot().github; got != "workspace-token" {
		t.Fatalf("批量快照应使用工作区 GitHubToken，实际 %q", got)
	}
	if got := a.repoConfig().Token; got != "sync-token" {
		t.Fatalf("配置包同步应使用 RepoToken，实际 %q", got)
	}
}

// TestRepoAutoRefreshControls 仓库页的自动刷新开关与频率选择能写回设置。
func TestRepoAutoRefreshControls(t *testing.T) {
	a, _ := newTestApp(t)
	a.reloadAtlas()

	tt := renderPageSized(t, a, "repos", 1440, 900)
	if !tt.HasText("自动刷新") {
		t.Fatalf("仓库页没有自动刷新开关；文本：%v", tt.Texts())
	}
	if err := tt.Click("自动刷新"); err != nil {
		t.Fatalf("点自动刷新开关失败: %v", err)
	}
	tt.Frame()
	if !a.settings.RepoAutoRefresh {
		t.Fatal("自动刷新开关没有写回设置")
	}
	for _, label := range []string{"10 分钟", "30 分钟", "1 小时", "3 小时"} {
		if !tt.HasText(label) {
			t.Errorf("开启后缺少频率选项 %q", label)
		}
	}
	if err := tt.Click("1 小时"); err != nil {
		t.Fatalf("点频率失败: %v", err)
	}
	tt.Frame()
	if a.settings.RepoAutoRefreshMinutes != 60 {
		t.Fatalf("刷新频率未写回设置：%d", a.settings.RepoAutoRefreshMinutes)
	}
}

// TestBatchAutoRefreshesLocal 自动刷新至少会刷新本地状态；没有远端令牌时
// 远端部分静默跳过，不应因此报错。
func TestBatchAutoRefreshesLocal(t *testing.T) {
	if !gitx.HasGit() {
		t.Skip("本机没有 git")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.name", "atlas-test")
	runGit(t, dir, "config", "user.email", "atlas@example.com")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	a, st := newTestApp(t)
	id, err := st.CreateRepo(store.Repo{Name: "demo/repo", LocalPath: dir, Status: RepoUnknown})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()
	r, ok := a.repoByID(id)
	if !ok {
		t.Fatal("仓库没读到")
	}
	if err := a.batchOne(context.Background(), batchAuto, r, "", tokenSet{}); err != nil {
		t.Fatalf("batchAuto 无令牌时不应失败: %v", err)
	}
	a.reloadAtlas()
	got, ok := a.repoByID(id)
	if !ok {
		t.Fatal("刷新后仓库没读到")
	}
	if got.Status != RepoOK || got.LastCommit == "" {
		t.Fatalf("自动刷新没有写入本地状态：%+v", got)
	}
}

// TestProjectStatusMenu 项目卡上的状态药丸点一下就能改状态。
func TestProjectStatusMenu(t *testing.T) {
	a, st := newTestApp(t)
	pID, err := st.CreateProject(store.Project{Name: "星图", Status: "进行中"})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()

	tt := renderPageSized(t, a, "projects", 1440, 900)
	if err := tt.Click("项目状态：进行中"); err != nil {
		t.Fatalf("点项目状态失败: %v；文本：%v", err, tt.Texts())
	}
	tt.Frame()
	if !tt.HasText("归档") {
		t.Fatalf("状态菜单里没有「归档」；文本：%v", tt.Texts())
	}
	if err := tt.Click("暂停"); err != nil {
		t.Fatalf("点「暂停」失败: %v", err)
	}
	tt.Frame()

	p, ok, err := st.ProjectByID(pID)
	if err != nil || !ok {
		t.Fatalf("读项目失败: ok=%v err=%v", ok, err)
	}
	if p.Status != "暂停" {
		t.Fatalf("项目状态未写库：%q", p.Status)
	}
}

// TestQuadrantDragMovesTodo 从四象限的一格拖到另一格，重要/紧急会跟着改。
func TestQuadrantDragMovesTodo(t *testing.T) {
	a, st := newTestApp(t)
	pID, err := st.CreateProject(store.Project{Name: "星图", Status: "进行中"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateTodo(store.Todo{
		Title: "拖拽我", ProjectID: pID, Status: TodoOpen,
		Important: true, Urgent: true, // 先放在「重要且紧急」
	})
	if err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()

	tt := renderPageSized(t, a, "home", 1440, 1000)
	src, ok := tt.Find("四象限待办：拖拽我")
	if !ok {
		t.Fatalf("找不到拖拽源；文本：%v", tt.Texts())
	}
	dst, ok := tt.Find("象限：重要不紧急")
	if !ok {
		t.Fatal("找不到目标象限")
	}
	tt.Press(src.X+src.W/2, src.Y+src.H/2)
	tt.Move(dst.X+dst.W/2, dst.Y+dst.H/2)
	tt.Frame()
	tt.Release(dst.X+dst.W/2, dst.Y+dst.H/2)
	tt.Frame()

	got, ok, err := st.TodoByID(id)
	if err != nil || !ok {
		t.Fatalf("读待办失败: ok=%v err=%v", ok, err)
	}
	if !got.Important || got.Urgent {
		t.Fatalf("拖到「重要不紧急」后应为 important=true urgent=false，实际 %+v", got)
	}
}

// TestSettingsToneWraps 九个色调在窄窗口里必须换行显示，而不是被 Segmented 裁掉。
func TestSettingsToneWraps(t *testing.T) {
	a, _ := newTestApp(t)
	tt := renderPageSized(t, a, "settings", 900, 700)
	for _, label := range []string{"青瓷", "薰衣草", "摩卡"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("窄窗口里找不到色调 %q", label)
		}
		if r.X < 0 || r.X+r.W > 900 {
			t.Errorf("色调 %q 被裁到窗外：x=%.1f w=%.1f", label, r.X, r.W)
		}
	}
}

// TestAsyncLimitClampsSettings 并发设置越界时回落到默认，绝不出现 0 或超大值。
func TestAsyncLimitClampsSettings(t *testing.T) {
	a, _ := newTestApp(t)
	cases := []struct {
		set  int
		want int
	}{
		{0, settings.DefaultAsyncConcurrency},
		{-3, settings.DefaultAsyncConcurrency},
		{99, settings.DefaultAsyncConcurrency},
		{1, 1},
		{8, 8},
		{5, 5},
	}
	for _, tc := range cases {
		a.settings.AsyncConcurrency = tc.set
		if got := a.asyncLimit(); got != tc.want {
			t.Errorf("AsyncConcurrency=%d 时 asyncLimit()=%d，期望 %d", tc.set, got, tc.want)
		}
	}
}

// TestRepoDirUsesRepoNameOnly 本地目录只取仓库名，不拼 owner。
func TestRepoDirUsesRepoNameOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repos")
	cases := map[string]string{
		"lark203/bestuav-atlas": "bestuav-atlas",
		"mirrors/gin.git":       "gin",
		"repo":                  "repo",
	}
	for in, want := range cases {
		if got := repoSafeDir(in); got != want {
			t.Errorf("repoSafeDir(%q) = %q，期望 %q", in, got, want)
		}
	}
	if got := repoPathUnder(root, "lark203/bestuav-atlas"); got != filepath.Join(root, "bestuav-atlas") {
		t.Errorf("repoPathUnder 应为根目录/仓库名，实际 %q", got)
	}
}
