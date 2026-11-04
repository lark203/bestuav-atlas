package view

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"bestuav-atlas/internal/store"
)

// 截图工具：设置 ATLAS_SHOTS=<目录> 后运行
//
//	ATLAS_SHOTS=docs/screenshots go test ./internal/view/ -run TestScreenshots -count=1
//
// 就把四个页面与两个弹窗渲染成 PNG。默认跳过，避免普通测试留下文件。
// 之所以用离屏渲染而不是真窗口：CI / 无显示器环境也能跑，且不依赖用户桌面状态。
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("ATLAS_SHOTS")
	if dir == "" {
		t.Skip("设置 ATLAS_SHOTS=<目录> 才会输出截图")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建截图目录失败: %v", err)
	}

	a, st := newTestApp(t)
	seedAtlas(t, a, st)
	// 再补一个「需要更新」的仓库，让表格里出现两种状态。
	if _, err := st.CreateRepo(store.Repo{
		ProjectID: 1, Name: "lark203/bestuav-mygo",
		URL:       "https://github.com/lark203/bestuav-mygo",
		LocalPath: filepath.Join(t.TempDir(), "bestuav-mygo"),
		Branch:    "main", Status: RepoBehind, Behind: 4,
	}); err != nil {
		t.Fatalf("建仓库失败: %v", err)
	}
	if _, err := st.CreateTodo(store.Todo{
		Title: "把首页的仓库卡片改成状态分组", Priority: 1, Status: TodoDoing,
	}); err != nil {
		t.Fatalf("建待办失败: %v", err)
	}
	a.reloadAtlas()

	// 详情页：把第一个仓库指到有 README 的本地目录，并补上评分与评价。
	readmeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(readmeDir, "README.md"), []byte(
		"# bestuav-atlas\n\n"+
			"把「待办 / 项目 / 多个 GitHub 仓库」摊在一张图上的本地桌面工作台。\n\n"+
			"## 功能\n\n"+
			"- 待办：倒计时、重要 / 紧急、四象限\n"+
			"- 项目：仓库与待办的归属单位\n"+
			"- 仓库：本地与远程导入、五星评分、README 只读浏览\n"), 0o644); err != nil {
		t.Fatalf("写 README 失败: %v", err)
	}
	repos, err := st.ListRepos()
	if err != nil || len(repos) == 0 {
		t.Fatalf("读仓库失败: %v", err)
	}
	detail := repos[0]
	detail.LocalPath = readmeDir
	detail.Importance, detail.Learning, detail.Difficulty = 5, 4, 2
	detail.Review = "主工程：桌面端骨架 + 业务页都在这里，值得反复读它的 shell 分层写法。"
	if err := st.UpdateRepo(detail); err != nil {
		t.Fatalf("更新仓库失败: %v", err)
	}
	a.reloadAtlas()

	save := func(name string) {
		tt := renderPage(t, a, a.page)
		img := tt.Image()
		if img == nil {
			t.Fatalf("页面 %s 没有渲染出图像", name)
		}
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatalf("创建截图失败: %v", err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatalf("写截图失败: %v", err)
		}
	}

	for _, page := range []string{"home", "todos", "projects", "repos"} {
		a.page = page
		save("atlas-" + page)
	}

	// 仓库详情页。
	if r, ok := a.repoByID(detail.ID); ok {
		a.openRepoDetail(r)
		save("atlas-repo-detail")
	}

	// 两个弹窗：导入与批量提交。
	a.page = "repos"
	a.openImportDialog()
	save("atlas-dialog-import")
	a.closeAtlasDialog()
	a.openCommitDialog([]int64{1, 2})
	save("atlas-dialog-commit")
}
