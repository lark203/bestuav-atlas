package view

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/richtext"

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

	// 异步任务页：几条真实动作记录（含失败原因与真实耗时），让截图能看出
	// 任务队列现在记录什么。pushTaskRow 会同时写内存与库。
	a.pushTaskRow("从本地导入", "导入", "已完成", "导入 5 个，跳过 1 个；首个跳过：bestuav-docs（已在列表中）", 12*time.Second+300*time.Millisecond)
	a.pushTaskRow("自动刷新", "自动", "失败", "1 个成功 / 1 个失败；首个失败：lark203/bestuav-mygo：认证失败", 820*time.Millisecond)
	a.pushTaskRow("批量拉取更新", "手动", "已完成", "全部成功（2 个仓库）", 2400*time.Millisecond)
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

	// 今天早 / 中 / 晚各一条待办：给总览的月度节奏与便签截图提供素材。
	today := startOfDay(time.Now())
	for _, it := range []struct {
		title string
		hour  int
	}{{"写晨会纪要", 9}, {"过一遍仓库状态", 13}, {"整理今日复盘", 20}} {
		if _, err := st.CreateTodo(store.Todo{
			Title: it.title, Status: TodoOpen, StartAt: today,
			DueAt: today.Add(time.Duration(it.hour) * time.Hour),
		}); err != nil {
			t.Fatalf("建今日待办失败: %v", err)
		}
	}
	a.reloadAtlas()

	for _, page := range []string{"home", "todos", "projects", "repos", "task", "settings", "sync-deploy"} {
		a.page = page
		save("atlas-" + page)
	}

	// 今日便签：独立小窗，按同样的尺寸离屏渲染。
	if tt := ui.NewTester(a.stickyView, stickyW, stickyH); tt != nil {
		tt.Frame()
		if img := tt.Image(); img != nil {
			if f, err := os.Create(filepath.Join(dir, "atlas-sticky.png")); err == nil {
				_ = png.Encode(f, img)
				_ = f.Close()
			}
		}
	}

	// 笔记页：左列表 + 右 Markdown 编辑/预览。
	a.page = "notes"
	a.notesOnEnter()
	// 用临时数据目录放截图里的图片，避免污染真实 notes-media。
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a.newNote()
	first := a.notes.sel
	a.notes.title = "发布检查清单"
	a.notes.body = "# 发布前\n\n- [ ] 跑全量测试\n- [ ] 更新截图\n- [x] 写 CHANGELOG\n\n| 平台 | 状态 |\n|---|---|\n| Windows | 待验证 |\n| Linux | 待验证 |\n"
	a.notes.tags = []string{"发布", "清单"}
	// 一张 120×48 的红色示意图，验证预览能真正加载本地图片。
	shot := image.NewRGBA(image.Rect(0, 0, 120, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 120; x++ {
			shot.Set(x, y, color.RGBA{R: 220, G: 60, B: 60, A: 255})
		}
	}
	var shotBuf bytes.Buffer
	_ = png.Encode(&shotBuf, shot)
	a.noteAddImageBytesAt(shotBuf.Bytes(), "png", 0)
	// 归属到第一个项目/仓库，展示关联信息。
	if d := a.atlas(); len(d.projects) > 0 {
		a.noteCurrent().ProjectID = d.projects[0].ID
	}
	if d := a.atlas(); len(d.repos) > 0 {
		a.noteCurrent().RepoID = d.repos[0].ID
	}
	a.refreshNoteRelationSel()
	a.saveNote(true)

	a.newNote()
	a.notes.title = "灵感：更快的关系图"
	a.notes.body = "预取邻居的布局坐标，滚轮时只做插值。\n\n> 先量一下帧时间再决定。\n"
	a.notes.tags = []string{"想法"}
	a.saveNote(true)

	// 富文本笔记：所见即所得编辑。
	a.newNoteKind("rich")
	a.notes.title = "富文本示例"
	a.notes.body = "# 周会\n\n**结论**：先做 Markdown，再评估富文本。\n\n- 目标 1\n- 目标 2\n"
	a.notes.richDoc = richtext.NewDocument(richtext.ParseMarkdown(a.notes.body))
	a.saveNote(true)
	save("atlas-notes-rich")

	a.selectNote(first)
	save("atlas-notes")
	a.notes.reading = true
	save("atlas-notes-read")
	a.notes.reading = false

	// 项目拓扑包含挂到项目上的笔记。
	a.page = "topology"
	if d := a.atlas(); len(d.projects) > 0 {
		a.atlasUI.graphProject = d.projects[0].ID
	}
	a.atlasUI.graph.reset()
	save("atlas-topology-notes")
	// 全量图谱 / 项目卡 / 总览也重截：这时已有笔记节点与计数。
	a.atlasUI.graphProject = 0
	a.atlasUI.graph.reset()
	save("atlas-graph-all")
	a.page = "projects"
	save("atlas-projects")
	a.page = "home"
	save("atlas-home")
	a.page = "notes"

	// 新建笔记的编辑器选择弹窗。
	a.openNoteNewDialog()
	save("atlas-notes-new")
	a.notes.newDialog = false
	a.closeAtlasDialog()

	// 布局示例：页头 tab / 左右两栏（空态与选中态）。
	a.page = "layout-tabs"
	a.demoTab = 0
	save("atlas-layout-tabs")
	a.demoTab = 1
	save("atlas-layout-tabs-detail")
	a.page = "layout-split"
	a.demoSel = 0
	save("atlas-layout-split-empty")
	a.demoSel = 1
	save("atlas-layout-split")

	// 仓库详情页。
	if r, ok := a.repoByID(detail.ID); ok {
		a.openRepoDetail(r)
		save("atlas-repo-detail")
	}

	// 拓扑图：项目在正中、仓库/待办发散；同一张图放大 1.6 倍再截一张。
	a.openProjectGraph(1)
	save("atlas-topology")
	a.atlasUI.graph.zoom = 1.6
	a.atlasUI.graph.panX, a.atlasUI.graph.panY = -110, -70
	save("atlas-topology-zoom")
	a.atlasUI.graph.reset()

	// 全量关系图谱：所有项目 / 仓库 / 待办一起看。
	a.openGraphDialog()
	save("atlas-graph-all")
	a.atlasUI.graph.reset()

	// 悬停脉冲：同一个 Tester 里把指针移到项目节点上，200ms 时截「亮点跑在半路」。
	a.openProjectGraph(1)
	{
		tt := ui.NewTester(a.View, 1440, 900)
		tt.Frame()
		g := &a.atlasUI.graph
		var hit graphHit
		for _, h := range g.hits {
			if h.kind == gkProject {
				hit = h
				break
			}
		}
		if hit.kind == gkProject {
			// 画布是页面里的一个元素：先把指针挪到窗口 (0,0) 反推出画布原点，
			// 再把指针移到节点的窗口坐标上（PointerPosition 给的是元素局部坐标）。
			tt.Move(0, 0)
			tt.Frame()
			ox, oy := -g.pointerX, -g.pointerY
			tt.Move(hit.x+ox, hit.y+oy)
			tt.Frame() // 悬停成立 → 开始脉冲
			shot := func(name string) {
				tt.Frame()
				if img := tt.Image(); img != nil {
					if f, err := os.Create(filepath.Join(dir, name+".png")); err == nil {
						_ = png.Encode(f, img)
						_ = f.Close()
					}
				}
			}
			time.Sleep(200 * time.Millisecond)
			shot("atlas-topology-pulse") // 亮点跑到半路
			time.Sleep(240 * time.Millisecond)
			shot("atlas-topology-pulse-lit") // 邻居被触达、正在亮起
		}
	}
	a.atlasUI.graph.pulse.active = false
	a.atlasUI.graph.reset()

	// 仓库页：选中两个仓库时的批量条（含批量删除）。
	if repos2, err := st.ListRepos(); err == nil && len(repos2) > 0 {
		a.atlasUI.sel = map[int64]bool{repos2[0].ID: true}
		if len(repos2) > 1 {
			a.atlasUI.sel[repos2[1].ID] = true
		}
		a.page = "repos"
		save("atlas-repos-bulk")
		a.clearRepoSelection()
	}

	// 两个弹窗：导入与批量提交。
	a.page = "repos"
	a.openImportDialog()
	save("atlas-dialog-import")
	a.closeAtlasDialog()
	a.openCommitDialog([]int64{1, 2})
	save("atlas-dialog-commit")

	// 删除确认（单个与批量共用）。
	a.closeAtlasDialog()
	a.confirmErase([]int64{1, 2})
	save("atlas-dialog-erase")

	// 重量级任务：本地导入时的全窗口遮罩 + 进度。
	a.closeAtlasDialog()
	a.heavyRunning = true
	a.heavyProgress = 0.45
	a.heavyLabel = "从本地导入（6 个文件夹）"
	save("atlas-heavy-import")
}
