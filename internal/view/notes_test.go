package view

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/store"
)

// notesApp 准备一个带临时数据库的 App，并进入笔记页。
func notesApp(t *testing.T) (*App, *ui.Tester) {
	t.Helper()
	a, _ := newTestApp(t)
	a.page = "notes"
	a.notesOnEnter()
	tt := ui.NewTester(a.View, 1200, 800)
	tt.Frame()
	return a, tt
}

// TestNotesPageCreateAndSave 空态 → 新建 → 改标题/正文 → 落库 → 左栏出现。
func TestNotesPageCreateAndSave(t *testing.T) {
	a, tt := notesApp(t)
	if !tt.HasText("从左侧选择一篇笔记") {
		t.Fatalf("空态提示缺失；文本：%v", tt.Texts())
	}

	a.newNote()
	tt.Frame()
	if a.notes.sel == 0 {
		t.Fatal("新建后应选中新笔记")
	}

	a.notes.title = "会议记录"
	a.notes.body = "# 要点\n- 第一条"
	n := a.noteCurrent()
	n.Title, n.Body = a.notes.title, a.notes.body
	if err := a.store.UpdateNote(*n); err != nil {
		t.Fatal(err)
	}
	a.notes.dirty = false
	a.loadNotes()
	tt.Frame()

	if !tt.HasText("会议记录") {
		t.Fatalf("左栏应出现笔记标题；文本：%v", tt.Texts())
	}
	list, err := a.store.ListNotes()
	if err != nil || len(list) != 1 || list[0].Title != "会议记录" {
		t.Fatalf("数据库应有这篇笔记: %+v err=%v", list, err)
	}
}

// TestNotesTagsPinAndSearch 标签落库、置顶排序、搜索过滤。
func TestNotesTagsPinAndSearch(t *testing.T) {
	a, tt := notesApp(t)
	a.newNote()
	a.notes.title = "周报"
	a.notes.body = "本周完成的事项"
	a.notes.tags = []string{"工作"}
	a.saveNote(true)
	id := a.notes.sel

	a.newNote()
	a.notes.title = "灵感"
	a.notes.body = "做一个更快的拓扑"
	a.saveNote(true)
	second := a.notes.sel

	got, err := a.store.GetNote(id)
	if err != nil || len(got.Tags) != 1 || got.Tags[0] != "工作" {
		t.Fatalf("标签应落库: %+v err=%v", got, err)
	}

	a.toggleNotePin(id)
	list, _ := a.store.ListNotes()
	if len(list) != 2 || list[0].ID != id || !list[0].Pinned {
		t.Fatalf("置顶应排最前: %+v", list)
	}

	a.notes.query = "拓扑"
	tt.Frame()
	if !tt.HasText("灵感") || tt.HasText("周报") {
		t.Fatalf("搜索应只剩「灵感」；文本：%v", tt.Texts())
	}
	if second == 0 {
		t.Fatal("第二篇 id 不应为 0")
	}
}

// TestNotesDeleteTwoStep 「删除」要两次点击，第二次才真删并回到空态。
func TestNotesDeleteTwoStep(t *testing.T) {
	a, tt := notesApp(t)
	a.newNote()
	a.notes.title = "要删的"
	a.saveNote(true)
	id := a.notes.sel

	a.deleteSelectedNote() // 第一次：只进入待确认
	if _, err := a.store.GetNote(id); err != nil {
		t.Fatal("第一次点击不应删除")
	}
	a.deleteSelectedNote() // 第二次：真删
	if _, err := a.store.GetNote(id); err == nil {
		t.Fatal("第二次点击应删除")
	}
	if a.notes.sel != 0 {
		t.Error("删除后应回到空态")
	}
	tt.Frame()
	if !tt.HasText("从左侧选择一篇笔记") {
		t.Fatalf("删除后应显示空态；文本：%v", tt.Texts())
	}
}

// TestNotesPasteImageSavesFile 图片按内容哈希存进 notes-media/，正文追加引用。
// TestNotesPreviewShowsImage 预览真的把本地图片画出来（红色像素），而不仅是替文字。
func TestNotesPreviewShowsImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a, _ := notesApp(t)
	a.newNote()

	img := image.NewRGBA(image.Rect(0, 0, 48, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 48; x++ {
			img.Set(x, y, color.RGBA{R: 220, G: 20, B: 20, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if !a.noteAddImageBytesAt(buf.Bytes(), "png", 0) {
		t.Fatal("写入图片失败")
	}
	a.saveNote(true)

	tt := ui.NewTester(a.View, 1100, 800)
	tt.Frame()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if hasRedPixel(tt.Image()) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("预览里没有出现图片（2 秒内未加载出红色像素）")
		}
		time.Sleep(20 * time.Millisecond)
		tt.Frame()
	}
}

// hasRedPixel 检查画面里是否有明显的红色像素（测试图片是纯红）。
func hasRedPixel(im *image.RGBA) bool {
	if im == nil {
		return false
	}
	for y := 0; y < im.Bounds().Dy(); y++ {
		for x := 0; x < im.Bounds().Dx(); x++ {
			r, g, b, _ := im.At(x, y).RGBA()
			if r > 0x9000 && g < 0x3000 && b < 0x3000 {
				return true
			}
		}
	}
	return false
}

func TestNotesPasteImageSavesFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a, _ := notesApp(t)
	a.newNote()

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if !a.noteAddImageBytes(buf.Bytes(), "png") {
		t.Fatal("写入图片应成功")
	}
	if !bytes.Contains([]byte(a.notes.body), []byte("notes-media/")) {
		t.Fatalf("正文应包含图片引用: %q", a.notes.body)
	}
	matches, _ := filepath.Glob(filepath.Join(noteMediaDir(), "*.png"))
	if len(matches) != 1 {
		t.Fatalf("notes-media 下应有 1 张图片，实际 %v", matches)
	}
	if _, err := os.Stat(matches[0]); err != nil {
		t.Fatalf("图片文件不存在: %v", err)
	}
}

// TestNotesImageInsertedAtCaret 图片按光标插入（不是永远追加到末尾）。
func TestNotesImageInsertedAtCaret(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a, _ := notesApp(t)
	a.newNote()
	a.notes.body = "abc"
	pngData := tinyPNG(t)
	if !a.noteAddImageBytesAt(pngData, "png", 1) {
		t.Fatal("插入图片失败")
	}
	if !strings.HasPrefix(a.notes.body, "a\n![图片](notes-media/") {
		t.Fatalf("图片应插在 a 之后: %q", a.notes.body)
	}
	if !strings.HasSuffix(a.notes.body, "bc") {
		t.Fatalf("光标后的文字应保留: %q", a.notes.body)
	}
	if !a.notes.hasPendingCaret || a.notes.pendingCaret == 0 {
		t.Error("插入后应把光标放到图片引用之后")
	}
}

// TestNotesCleanupUnreferencedMedia 只删没被正文引用的图片。
func TestNotesCleanupUnreferencedMedia(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a, _ := notesApp(t)
	a.newNote()
	if !a.noteAddImageBytes(tinyPNG(t), "png") {
		t.Fatal("写入图片失败")
	}
	a.notes.title = "带图"
	a.saveNote(true)

	dir := noteMediaDir()
	orphan := filepath.Join(dir, "orphan.png")
	if err := os.WriteFile(orphan, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, kept := a.removeUnreferencedMedia()
	if removed != 1 || kept != 1 {
		t.Fatalf("应删除 1 张、保留 1 张，实际 removed=%d kept=%d", removed, kept)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("无引用图片应被删除")
	}
}

// TestNotesNewDialogChooseRich 新建笔记弹窗可以选择富文本编辑器。
func TestNotesNewDialogChooseRich(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a, _ := notesApp(t)
	a.openNoteNewDialog()
	tt := renderPage(t, a, "notes")
	if !tt.HasText("富文本笔记（所见即所得）") {
		t.Fatalf("弹窗应提供富文本选项；文本：%v", tt.Texts())
	}
	if err := tt.Click("富文本笔记（所见即所得）"); err != nil {
		t.Fatal(err)
	}
	n := a.noteCurrent()
	if n == nil || n.Editor != "rich" {
		t.Fatalf("应创建富文本笔记: %+v", n)
	}
	tt.Frame()
	if a.notes.richDoc == nil {
		t.Error("富文本笔记应初始化 richtext 文档")
	}
}

// tinyPNG 生成一张 1×1 的 PNG，供图片相关用例使用。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 200, G: 30, B: 30, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestNoteTagAddRemove 标签逻辑：去重、清空输入、退格删除最后一个；chip 会渲染出来。
func TestNoteTagAddRemove(t *testing.T) {
	a, tt := notesApp(t)
	a.newNote()
	if !a.addNoteTag("想法") {
		t.Fatal("应添加标签")
	}
	if a.notes.draft != "" || len(a.notes.tags) != 1 {
		t.Fatalf("添加后应清空输入: draft=%q tags=%v", a.notes.draft, a.notes.tags)
	}
	if a.addNoteTag("想法") || len(a.notes.tags) != 1 {
		t.Fatal("重复标签不应再加一个")
	}
	tt.Frame()
	if !tt.HasText("想法") {
		t.Fatalf("chip 应渲染出来；文本：%v", tt.Texts())
	}
	if !a.removeLastNoteTag() || len(a.notes.tags) != 0 {
		t.Fatalf("退格应删除最后一个标签: %v", a.notes.tags)
	}
}

// TestNotesListOrderByCreated 列表按创建时间倒序，点击/保存不改变顺序。
func TestNotesListOrderByCreated(t *testing.T) {
	a, _ := notesApp(t)
	a.newNote()
	a.notes.title = "第一篇"
	a.saveNote(true)
	first := a.notes.sel
	a.newNote()
	a.notes.title = "第二篇"
	a.saveNote(true)
	second := a.notes.sel

	order := func() []int64 {
		out := make([]int64, 0, len(a.notes.list))
		for _, n := range a.notes.list {
			out = append(out, n.ID)
		}
		return out
	}
	if got := order(); len(got) != 2 || got[0] != second || got[1] != first {
		t.Fatalf("新笔记应排最前: %v（first=%d second=%d）", got, first, second)
	}
	// 点回第一篇：updated_at 会变，但排序仍按创建时间，不能换位。
	a.selectNote(first)
	a.saveNote(true)
	if got := order(); got[0] != second || got[1] != first {
		t.Fatalf("点击后顺序不应改变: %v", got)
	}
}

// TestNotesRichToolbarButtons 富文本工具条有全套格式按钮，且没有「复制 Markdown」。
func TestNotesRichToolbarButtons(t *testing.T) {
	a, tt := notesApp(t)
	a.newNoteKind("rich")
	tt.Frame()
	for _, want := range []string{"链接", "</>", "☐", "❝", "{}"} {
		if !tt.HasText(want) {
			t.Errorf("富文本工具条缺少 %q；文本：%v", want, tt.Texts())
		}
	}
	if tt.HasText("复制 Markdown") {
		t.Error("不应该有「复制 Markdown」按钮")
	}
}

// TestNoteTagKeepsFocus 回车加标签、退格删标签后，标签输入框仍保持焦点。
func TestNoteTagKeepsFocus(t *testing.T) {
	a, tt := notesApp(t)
	a.newNote()
	tt.Frame()
	a.notes.tagInput.Focus()
	tt.Frame()
	tt.Frame() // 焦点在提交后的下一帧才反映到 Focused()
	if !a.notes.tagFocused {
		t.Fatal("标签输入框应先获得焦点")
	}
	tt.Type("想法")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Frame()
	if len(a.notes.tags) != 1 {
		t.Fatalf("回车应加一个标签: %v", a.notes.tags)
	}
	if !a.notes.tagFocused {
		t.Error("回车加标签后焦点丢了")
	}
	tt.Key(0, ui.KeyBackspace)
	tt.Frame()
	tt.Frame()
	if len(a.notes.tags) != 0 {
		t.Fatalf("退格应删掉标签: %v", a.notes.tags)
	}
	if !a.notes.tagFocused {
		t.Error("退格删标签后焦点丢了")
	}
}

// TestNotesRichToolbarWraps 富文本工具条一行放得下时不换行，窄窗口自动换行。
func TestNotesRichToolbarWraps(t *testing.T) {
	a, _ := notesApp(t)
	a.newNoteKind("rich")
	tt := ui.NewTester(a.View, 1440, 900) // 宽窗口：一行放得下
	tt.Frame()
	b, ok1 := tt.Find("B")
	code, ok2 := tt.Find("{}")
	if !ok1 || !ok2 {
		t.Fatalf("找不到工具条按钮: B=%v {}=%v", ok1, ok2)
	}
	if d := b.Y - code.Y; d > 1 || d < -1 {
		t.Errorf("宽窗口应同一行: B.y=%.0f {}.y=%.0f", b.Y, code.Y)
	}

	narrow := ui.NewTester(a.View, 820, 700)
	narrow.Frame()
	nb, ok1 := narrow.Find("B")
	nc, ok2 := narrow.Find("{}")
	if !ok1 || !ok2 {
		t.Fatalf("窄窗口找不到工具条按钮: B=%v {}=%v", ok1, ok2)
	}
	if nc.Y <= nb.Y {
		t.Errorf("窄窗口应换行: B.y=%.0f {}.y=%.0f", nb.Y, nc.Y)
	}
}

// TestNotesSingleClickReadDoubleClickEdit 列表点击语义：单击阅读、双击编辑。
func TestNotesSingleClickReadDoubleClickEdit(t *testing.T) {
	a, tt := notesApp(t)
	a.newNote()
	a.notes.title = "阅读测试"
	a.notes.body = "# 正文\n\n一段内容。"
	a.saveNote(true)
	a.notes.reading = false
	id := a.notes.sel

	a.activateNoteRow(id, false) // 单击
	tt.Frame()
	if !a.notes.reading {
		t.Fatal("单击应进入阅读模式")
	}
	if !tt.HasText("阅读模式") {
		t.Fatalf("阅读模式应有标识；文本：%v", tt.Texts())
	}

	a.activateNoteRow(id, true) // 双击
	tt.Frame()
	if a.notes.reading {
		t.Fatal("双击应回到编辑模式")
	}
}

// TestNotesRichInsertImage 富文本插图：落盘 + 文档附件 + Markdown 引用。
func TestNotesRichInsertImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a, _ := notesApp(t)
	a.newNoteKind("rich")
	if !a.addRichImageBytes(tinyPNG(t), "png") {
		t.Fatal("富文本插图失败")
	}
	if !strings.Contains(a.notes.body, "notes-media/") {
		t.Fatalf("正文应含图片引用: %q", a.notes.body)
	}
	files, _ := filepath.Glob(filepath.Join(noteMediaDir(), "*.png"))
	if len(files) != 1 {
		t.Fatalf("应写入 1 张图片: %v", files)
	}
}

// TestGraphIncludesProjectNotes 项目拓扑包含挂到项目上的笔记，并直连项目。
func TestGraphIncludesProjectNotes(t *testing.T) {
	a, st := newTestApp(t)
	pid, _ := seedAtlas(t, a, st)
	if _, err := st.CreateNote(store.Note{Title: "设计笔记", ProjectID: pid}); err != nil {
		t.Fatal(err)
	}
	a.reloadAtlas()
	d := a.atlas()
	nodes, edges := projectTopologyGraph(nil, nil, pid, &ui.Theme{FontSize: 14}, "星图",
		d.projectRepos(pid), d.projectTodos(pid), d.notes)
	var noteIdx = -1
	for i, n := range nodes {
		if n.kind == gkNote && n.label == "设计笔记" {
			noteIdx = i
		}
	}
	if noteIdx < 0 {
		t.Fatalf("拓扑里应有笔记节点: %+v", nodes)
	}
	found := false
	for _, e := range edges {
		if e[0] == 0 && e[1] == noteIdx {
			found = true
		}
	}
	if !found {
		t.Errorf("笔记应直连项目：edges=%v noteIdx=%d", edges, noteIdx)
	}

	// 点节点 → 笔记页、选中并阅读。
	a.graphActivate(graphHit{kind: gkNote, id: nodes[noteIdx].id, label: "设计笔记"})
	if a.page != "notes" || a.notes.sel != nodes[noteIdx].id || !a.notes.reading {
		t.Errorf("点击笔记节点应进入阅读：page=%q sel=%d reading=%v", a.page, a.notes.sel, a.notes.reading)
	}
}

// TestNotesClickRowReads 真实点击左栏行：进入阅读模式。
func TestNotesClickRowReads(t *testing.T) {
	a, tt := notesApp(t)
	a.newNote()
	a.notes.title = "点我"
	a.saveNote(true)
	tt.Frame()
	tt.ClickAt(350, 150) // 左栏第一行
	tt.Frame()
	if !a.notes.reading {
		t.Fatalf("点击列表行应进入阅读模式（reading=false）")
	}
}

// TestNotesTextAreaSyncsExternalBody 外部改 body（插图/粘贴）后，编辑框里能看到新路径。
func TestNotesTextAreaSyncsExternalBody(t *testing.T) {
	a, tt := notesApp(t)
	a.newNote()
	a.notes.body = "abc"
	tt.Frame()
	a.notes.body = "abc\n![图片](notes-media/demo.png)\n"
	tt.Frame()
	tt.ClickAt(700, 300) // 右栏 Markdown 编辑区
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Key(ui.Cmd, ui.KeyC)
	if got := tt.Clipboard(); !strings.Contains(got, "notes-media/demo.png") {
		t.Fatalf("编辑框没有同步外部正文，剪贴板=%q", got)
	}
}

// TestNotesRichImageRoundTrip 富文本图片保存后切走再回来仍能显示（附件重新加载）。
func TestNotesRichImageRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", t.TempDir())
	} else {
		t.Setenv("XDG_DATA_HOME", t.TempDir())
	}
	a, _ := notesApp(t)
	a.newNoteKind("rich")
	a.notes.title = "图文"
	if !a.addRichImageBytes(tinyPNG(t), "png") {
		t.Fatal("插图失败")
	}
	a.saveNote(true)
	id := a.notes.sel
	a.loadNotes()
	a.selectNote(0) // 先切走
	a.loadNotes()
	a.selectNote(id) // 再切回来
	if a.notes.richDoc == nil {
		t.Fatal("富文本文档未重建")
	}
	found := false
	for _, p := range a.notes.richDoc.Text().Paragraphs() {
		for _, r := range p.Runs {
			if at := r.Attributes.Attachment; at != nil && at.Image != nil {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("切回后图片附件没有重新加载；body=%q", a.notes.body)
	}
}
