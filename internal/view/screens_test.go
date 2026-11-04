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

	// 两个弹窗：导入与批量提交。
	a.page = "repos"
	a.openImportDialog()
	save("atlas-dialog-import")
	a.closeAtlasDialog()
	a.openCommitDialog([]int64{1, 2})
	save("atlas-dialog-commit")
}
