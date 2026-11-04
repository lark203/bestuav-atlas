package view

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

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
