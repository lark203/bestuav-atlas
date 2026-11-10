package view

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/ELY-Open-Source/CrychicUI/overlay"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/egoist/mygo/ui/richtext"

	"bestuav-atlas/internal/apppath"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/ui/kit"
	"bestuav-atlas/internal/view/shell"
)

// 笔记页：左列表 + 右编辑器（Markdown 原文 + 实时预览）。
//
// 数据是业务表 note（不是 page_pref）：配置包、备份、换机都会跟着数据库走。
// 图片存 <数据目录>/notes-media/，正文里用相对路径引用，换机时随配置包一起搬。

// notesUI 是笔记页的跨帧状态（列表缓存、当前编辑中的字段、自动保存计时器）。
type notesUI struct {
	list   []store.Note
	loaded bool

	sel   int64
	title string
	body  string
	tags  []string
	draft string // 标签输入框里尚未确认的文字
	query string // 左栏搜索词
	// reading 为 true 时右栏是只读阅读模式；单击列表进入，双击回到编辑。
	reading bool
	// pendingPaste：粘贴回调发生在剪贴板被引擎占用的时刻，自己的剪贴板读取会失败；
	// 先记下意图，下一帧（锁已释放）再读剪贴板并插图。
	pendingPaste bool

	// saved* 是「上次落库的值」，用于判断有没有改动、以及标题/正文是否已同步。
	savedTitle string
	savedBody  string
	savedTags  []string
	dirty      bool
	savedAt    time.Time
	saveErr    string
	timer      *time.Timer

	// 删除二次确认（5 秒内再点一次才真删）。
	confirmID int64
	confirmAt time.Time

	// 图片插入后希望光标落到的位置（只应用一次，避免和用户输入抢光标）。
	pendingCaret    int
	hasPendingCaret bool
	// caretPos 是上一帧（构建期）读到的光标位置。粘贴/拖放的回调在事件期执行，
	// 那时手里的 Element 已经过期（mygo 会 panic），只能用这个缓存值。
	caretPos int
	// tagInput 用于「标题回车 → 聚焦标签输入框」，以及增删标签后保持焦点。
	tagInput ui.Handle
	// tagFocused 记录标签输入框上一帧是否聚焦（测试用）。
	tagFocused bool
	// 富文本链接弹层的草稿与开关。
	linkURL  string
	linkOpen bool

	// md 必须跨帧复用：markdown.Options 里缓存着已加载的图片；
	// 每帧新建会让异步加载结果写进旧对象，预览永远等不到图。
	md markdown.Options

	// 归属下拉框的当前文字（存的是 id，控件绑文字）。
	projSel, repoSel string
	// richDoc 富文本笔记的文档对象（只在 Editor=="rich" 时存在）。
	richDoc *richtext.Document
	// newDialog 打开「新建笔记：选编辑器」弹窗。
	newDialog bool
	// 清理无引用图片的二次确认。
	cleanAt time.Time

	split shell.SplitState
}

const (
	noteSaveDelay    = 700 * time.Millisecond
	noteConfirmDelay = 5 * time.Second
	noteMediaFolder  = "notes-media"
)

// ---- 数据加载与保存 ----

// notesOnEnter 进入页面时读一次列表（改完立即写库，不需要每帧查库）。
func (a *App) notesOnEnter() {
	a.loadNotes()
}

// loadNotes 重新读全量笔记并保持当前选中项（若仍存在）。
func (a *App) loadNotes() {
	a.notes.loaded = true
	if a.store == nil {
		a.notes.list = nil
		return
	}
	list, err := a.store.ListNotes()
	if err != nil {
		logging.Errv("notes", "读取笔记失败", err)
		a.notes.saveErr = "读取失败：" + err.Error()
		return
	}
	a.notes.list = list
	if a.notes.sel != 0 && a.findNote(a.notes.sel) == nil {
		a.notes.sel = 0
	}
}

func (a *App) findNote(id int64) *store.Note {
	for i := range a.notes.list {
		if a.notes.list[i].ID == id {
			return &a.notes.list[i]
		}
	}
	return nil
}

// noteCurrent 当前选中的笔记（nil = 空态）。
func (a *App) noteCurrent() *store.Note {
	if a.notes.sel == 0 {
		return nil
	}
	return a.findNote(a.notes.sel)
}

// filteredNotes 按搜索词过滤（标题 / 正文 / 标签，大小写不敏感）。
func (a *App) filteredNotes() []store.Note {
	q := strings.ToLower(strings.TrimSpace(a.notes.query))
	if q == "" {
		return a.notes.list
	}
	out := make([]store.Note, 0, len(a.notes.list))
	for _, n := range a.notes.list {
		if strings.Contains(strings.ToLower(n.Title), q) ||
			strings.Contains(strings.ToLower(n.Body), q) ||
			strings.Contains(strings.ToLower(strings.Join(n.Tags, " ")), q) {
			out = append(out, n)
		}
	}
	return out
}

// selectNote 切换当前笔记：先把未保存的改动落库，再装载新的一篇。
func (a *App) selectNote(id int64) {
	if id == a.notes.sel {
		return
	}
	a.saveNote(true)
	n := a.findNote(id)
	if n == nil {
		return
	}
	a.notes.sel = id
	a.notes.title = n.Title
	a.notes.body = n.Body
	a.notes.tags = append([]string(nil), n.Tags...)
	a.notes.savedTitle, a.notes.savedBody = n.Title, n.Body
	a.notes.savedTags = append([]string(nil), n.Tags...)
	a.notes.dirty = false
	a.notes.saveErr = ""
	// 富文本笔记：从 Markdown 还原文档（Markdown 是唯一存储格式）。
	if n.Editor == "rich" {
		txt := richtext.ParseMarkdown(n.Body)
		a.rehydrateRichImages(txt)
		a.notes.richDoc = richtext.NewDocument(txt)
	} else {
		a.notes.richDoc = nil
	}
	a.refreshNoteRelationSel()
	a.requestFrame()
}

// newNote 新建一篇 Markdown 笔记并选中（保留给测试与快捷入口）。
func (a *App) newNote() { a.newNoteKind("markdown") }

// newNoteKind 新建指定编辑器类型的笔记（"markdown" / "rich"）并选中。
func (a *App) newNoteKind(kind string) {
	if a.store == nil {
		a.toast("数据库未就绪，暂不能新建笔记")
		return
	}
	a.saveNote(true)
	id, err := a.store.CreateNote(store.Note{Editor: kind})
	if err != nil {
		logging.Errv("notes", "新建笔记失败", err)
		a.toast("新建笔记失败：" + err.Error())
		return
	}
	a.loadNotes()
	a.selectNote(id)
	a.notes.reading = false
	logging.Info("notes", "新建笔记", "id", id, "editor", kind)
	a.requestFrame()
}

// noteMarkDirty 标记有改动，并安排一次延迟保存。
func (a *App) noteMarkDirty() {
	a.notes.dirty = true
	// 离屏渲染/单测没有主循环，定时器里的 RunOnMain 无处投递；只标脏即可。
	if a.win == nil {
		return
	}
	if a.notes.timer != nil {
		a.notes.timer.Stop()
	}
	a.notes.timer = time.AfterFunc(noteSaveDelay, func() {
		mygo.RunOnMain(func() {
			a.saveNote(false)
			a.requestFrame()
		})
	})
}

// saveNote 把编辑中的字段写回数据库；force 为 true 时即使没有改动也执行
// （切换笔记 / 离开页面时用，保证标题 Trim 与 updated_at 一致）。
func (a *App) saveNote(force bool) {
	n := a.noteCurrent()
	if n == nil || a.store == nil {
		return
	}
	if !force && !a.notes.dirty {
		return
	}
	title := strings.TrimSpace(a.notes.title)
	n.Title = title
	n.Body = a.notes.body
	n.Tags = append([]string(nil), a.notes.tags...)
	if err := a.store.UpdateNote(*n); err != nil {
		logging.Errv("notes", "保存笔记失败", err, "id", n.ID)
		a.notes.saveErr = "保存失败：" + err.Error()
		return
	}
	n.UpdatedAt = time.Now()
	a.notes.savedTitle, a.notes.savedBody = title, a.notes.body
	a.notes.savedTags = append([]string(nil), a.notes.tags...)
	a.notes.dirty = false
	a.notes.savedAt = n.UpdatedAt
	a.notes.saveErr = ""
	a.sortNotes()
}

// sortNotes 与 SQL 的排序保持一致：置顶优先，其余按**创建时间**倒序。
//
// 不用 updated_at：切换/保存笔记会刷新它，列表会跟着「点一下换一次位置」。
// 创建时间不会因为点击而变，列表稳定。
func (a *App) sortNotes() {
	list := a.notes.list
	for i := 1; i < len(list); i++ {
		for j := i; j > 0; j-- {
			x, y := list[j-1], list[j]
			if y.Pinned && !x.Pinned ||
				y.Pinned == x.Pinned && y.CreatedAt.After(x.CreatedAt) ||
				y.Pinned == x.Pinned && y.CreatedAt.Equal(x.CreatedAt) && y.ID > x.ID {
				list[j-1], list[j] = y, x
				continue
			}
			break
		}
	}
}

// notesOnLeave 离开页面：把没来得及自动保存的内容立刻落库。
func (a *App) notesOnLeave() {
	if a.notes.timer != nil {
		a.notes.timer.Stop()
	}
	a.saveNote(true)
	// 笔记的归属/标题可能变了：让拓扑等依赖 atlas 缓存的页面下一帧重读。
	a.atlasCache.loaded = false
}

// deleteSelectedNote 二次确认后硬删除，并把右侧恢复成空态。
func (a *App) deleteSelectedNote() {
	now := time.Now()
	if a.notes.sel == 0 {
		return
	}
	if a.notes.confirmID != a.notes.sel || now.Sub(a.notes.confirmAt) > noteConfirmDelay {
		a.notes.confirmID = a.notes.sel
		a.notes.confirmAt = now
		a.requestFrame()
		return
	}
	id := a.notes.sel
	if a.store != nil {
		if err := a.store.DeleteNote(id); err != nil {
			logging.Errv("notes", "删除笔记失败", err)
			a.toast("删除失败：" + err.Error())
			return
		}
	}
	a.notes.confirmID = 0
	a.notes.sel = 0
	a.notes.title, a.notes.body, a.notes.tags = "", "", nil
	a.loadNotes()
	logging.Info("notes", "删除笔记", "id", id)
	a.toast("笔记已删除")
	a.requestFrame()
}

// toggleNotePin 置顶/取消置顶（不改 updated_at，避免把旧笔记排到最前）。
func (a *App) toggleNotePin(id int64) {
	n := a.findNote(id)
	if n == nil || a.store == nil {
		return
	}
	if err := a.store.SetNotePinned(id, !n.Pinned); err != nil {
		logging.Errv("notes", "更新置顶失败", err)
		return
	}
	n.Pinned = !n.Pinned
	a.sortNotes()
	a.requestFrame()
}

// ---- 图片：粘贴 / 拖入 → 存到应用数据目录 ----

// noteMediaDir 图片目录：<数据目录>/notes-media。
func noteMediaDir() string {
	dir, err := apppath.DataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, noteMediaFolder)
}

// noteAddImageBytes 把图片字节按内容哈希落盘（相同图片只存一份），
// 并把 Markdown 引用追加到正文末尾（测试/没有光标时用）。
func (a *App) noteAddImageBytes(data []byte, ext string) bool {
	return a.noteAddImageBytesAt(data, ext, -1)
}

// noteAddImageBytesAt 同上，但把引用插到 at（rune 下标；负数 = 末尾）。
func (a *App) noteAddImageBytesAt(data []byte, ext string, at int) bool {
	rel, ok := a.saveNoteImageBytes(data, ext)
	if !ok {
		return false
	}
	a.insertNoteImage(rel, at)
	return true
}

// saveNoteImageBytes 把图片按内容哈希写进 notes-media，返回正文引用路径。
// Markdown 图片与富文本附件共用这一份落盘逻辑。
func (a *App) saveNoteImageBytes(data []byte, ext string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	if ext == "" {
		ext = "png"
	}
	dir := noteMediaDir()
	if dir == "" {
		return "", false
	}
	if err := apppath.EnsureDir(dir); err != nil {
		logging.Errv("notes", "创建笔记图片目录失败", err)
		return "", false
	}
	sum := sha1.Sum(data)
	name := hex.EncodeToString(sum[:])[:16] + "." + strings.TrimPrefix(ext, ".")
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			logging.Errv("notes", "写入笔记图片失败", err)
			a.toast("保存图片失败：" + err.Error())
			return "", false
		}
	}
	return filepath.ToSlash(filepath.Join(noteMediaFolder, name)), true
}

// addRichImageBytes 富文本笔记插图：落盘后作为 Attachment 插到当前光标处。
func (a *App) addRichImageBytes(data []byte, ext string) bool {
	if a.notes.richDoc == nil {
		return false
	}
	rel, ok := a.saveNoteImageBytes(data, ext)
	if !ok {
		return false
	}
	src, err := ui.DecodeImage(data)
	if err != nil {
		logging.Errv("notes", "解码富文本图片失败", err)
		a.toast("图片格式不支持：" + err.Error())
		return false
	}
	a.notes.richDoc.InsertAttachment(&richtext.Attachment{
		Image: src,
		Name:  "图片",
		URL:   rel,
	})
	a.notes.body = a.notes.richDoc.Text().Markdown()
	a.noteMarkDirty()
	a.requestFrame()
	return true
}

// chooseRichImages 富文本工具条的「图片」：选本地图片后插入正文。
func (a *App) chooseRichImages() {
	go func() {
		defer logging.Recover("notes-rich-image")
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:       "插入图片",
			ButtonLabel: "插入",
			Filters: []mygo.FileFilter{{
				Name:       "图片",
				Extensions: []string{"png", "jpg", "jpeg", "gif", "webp", "bmp"},
			}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		mygo.RunOnMain(func() {
			for _, p := range paths {
				ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
				data, rerr := os.ReadFile(p)
				if rerr != nil {
					logging.Errv("notes", "读取图片失败", rerr, "path", p)
					continue
				}
				if ext == "jpeg" {
					ext = "jpg"
				}
				a.addRichImageBytes(data, ext)
			}
			a.requestFrame()
		})
	}()
}

// noteAddImageFiles 拖入的本地图片：读文件后走同一套哈希落盘（末尾插入）。
func (a *App) noteAddImageFiles(paths []string) bool {
	return a.noteAddImageFilesAt(paths, -1)
}

// noteAddImageFilesAt 同 noteAddImageFiles，但插到 at；多张按顺序接在上一张之后。
func (a *App) noteAddImageFilesAt(paths []string, at int) bool {
	added := false
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		switch ext {
		case "png", "jpg", "jpeg", "gif", "webp", "bmp":
		default:
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			logging.Errv("notes", "读取拖入图片失败", err, "path", p)
			continue
		}
		if ext == "jpeg" {
			ext = "jpg"
		}
		if a.noteAddImageBytesAt(data, ext, at) {
			added = true
			if at >= 0 {
				at = a.notes.pendingCaret
			}
		}
	}
	return added
}

// notePasteImage 从剪贴板取图片（PNG）或复制来的图片文件。没有图片返回 false，
// 让文本粘贴照常进行。
func (a *App) notePasteImage() bool {
	return a.notePasteImageAt(-1)
}

// notePasteImageAt 同 notePasteImage，图片插到 at（-1 = 正文末尾）。
func (a *App) notePasteImageAt(at int) bool {
	formats := mygo.Clipboard.Formats()
	for _, f := range formats {
		if f == transfer.PNG {
			data, err := mygo.Clipboard.ReadFormat(transfer.PNG)
			if err == nil {
				return a.noteAddImageBytesAt(data, "png", at)
			}
			// 有些复制源只提供 DIB/文件列表：继续试 ReadFiles，不直接失败。
			logging.Warn("notes", "读取剪贴板 PNG 失败，尝试文件列表", "err", err.Error())
		}
	}
	if files, err := mygo.Clipboard.ReadFiles(); err == nil && len(files) > 0 {
		return a.noteAddImageFilesAt(files, at)
	}
	return false
}

// insertNoteImage 在 at（rune 下标）插入图片引用，并把光标放到引用之后；
// 引用前后补换行，保证它单独成段（Markdown 图片不能粘在文字中间显示）。
func (a *App) insertNoteImage(rel string, at int) {
	body := []rune(a.notes.body)
	if at < 0 || at > len(body) {
		at = len(body)
	}
	ins := "![图片](" + rel + ")"
	if at > 0 && body[at-1] != '\n' {
		ins = "\n" + ins
	}
	if at < len(body) && body[at] != '\n' {
		ins += "\n"
	}
	a.notes.body = string(body[:at]) + ins + string(body[at:])
	a.notes.pendingCaret = at + len([]rune(ins))
	a.notes.hasPendingCaret = true
	a.noteMarkDirty()
	a.requestFrame()
}

// noteImageLoader 给 markdown.View 用：本地 notes-media 相对路径 + 远程 http(s)。
func (a *App) noteImageLoader(url string) (ui.ImageSource, error) {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		client := &http.Client{Timeout: 8 * time.Second}
		resp, err := client.Get(url)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
		if err != nil {
			return nil, err
		}
		return ui.DecodeImage(data)
	}
	dir, err := apppath.DataDir()
	if err != nil {
		return nil, err
	}
	return markdown.FileImages(dir)(url)
}

// ---- 页面 ----

// notesPage 笔记页：左列表 + 右编辑器（ContentOptions.Fill + 可拖拽 Split）。
func (a *App) notesPage(c *ui.Context, t *ui.Theme) {
	if !a.notes.loaded {
		a.loadNotes()
	}
	shell.Split(c, t, &a.notes.split, 260, 460, func() {
		a.notesList(c, t)
	}, func() {
		a.notesEditor(c, t)
	})
}

func (a *App) notesList(c *ui.Context, t *ui.Theme) {
	shell.SplitListScroll(c, nil, func() {
		ui.Column(c).Gap(8).Children(func() {
			ui.TextInput(c, &a.notes.query).Placeholder("搜索标题、正文、标签")
			list := a.filteredNotes()
			if len(list) == 0 {
				kit.EmptyHint(c, t, "还没有笔记。点右上角「新建笔记」开始写。")
				return
			}
			for _, n := range list {
				a.noteRow(c, t, n, n.ID == a.notes.sel)
			}
		})
	})
}

// noteRow 左栏一行：标题、更新时间、标签、置顶星标。整行可点选中。
func (a *App) noteRow(c *ui.Context, t *ui.Theme, n store.Note, sel bool) {
	row := ui.Row(c).Gap(8).AlignItems(ui.Start).Padding(10, 10).Radius(8)
	if sel {
		row.Background(kit.Alpha(t.Accent, 0.14))
	} else if row.Hovered() {
		row.Background(t.SurfaceHover)
	}
	row.Children(func() {
		ui.Column(c).Gap(3).Grow(1).Children(func() {
			ui.Text(c, noteDisplayTitle(n)).Bold().SingleLine()
			if line := noteSnippet(n.Body); line != "" {
				ui.Text(c, line).FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
			}
			meta := timeAgo(n.UpdatedAt)
			if rel := a.noteRelationLabel(n); rel != "" {
				meta += " · " + rel
			}
			if len(n.Tags) > 0 {
				meta += " · " + strings.Join(n.Tags, " · ")
			}
			ui.Text(c, meta).FontSize(t.FontSize - 3).TextColor(t.TextMuted).SingleLine()
		})
		star := ui.ButtonBase(c).Label("置顶").Padding(2, 4).Radius(6).
			Cursor(ui.CursorPointer).Tooltip(pick(n.Pinned, "取消置顶", "置顶"))
		col := t.TextMuted
		if n.Pinned {
			col = t.Accent
		}
		star.Children(func() {
			ui.Icon(c, icons.Star).FontSize(14).TextColor(col)
		})
		if star.Clicked() {
			a.toggleNotePin(n.ID)
		}
	})
	// 单击 = 选中并进入阅读模式。进入编辑统一走右栏「编辑」按钮
	// （双击在列表行上不可靠，已去掉）。
	if row.Clicked() {
		a.activateNoteRow(n.ID, false)
	}
}

// activateNoteRow 列表行的点击语义：edit=false 进阅读，true 回编辑。
// 抽成方法便于测试，也避免单击/双击两套重复逻辑。
func (a *App) activateNoteRow(id int64, edit bool) {
	if a.notes.sel != id {
		a.selectNote(id)
	}
	if edit {
		a.notes.reading = false
	} else {
		a.notes.reading = true
		a.saveNote(true)
	}
	a.requestFrame()
}

// notesEditor 右侧编辑器：标题、标签、Markdown 源码 + 实时预览、保存状态。
func (a *App) notesEditor(c *ui.Context, t *ui.Theme) {
	n := a.noteCurrent()
	if n == nil {
		shell.SplitEmpty(c, t, "从左侧选择一篇笔记，或点右上角「新建笔记」")
		return
	}
	if a.notes.reading {
		a.noteReader(c, t, n)
		return
	}
	a.processPendingPaste()
	ui.Column(c).Fill().Padding(kit.PagePadY, kit.PagePadX).Gap(10).Children(func() {
		// 归属与关联（只存 id；名字从项目/仓库缓存解析）
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			a.noteRelationRow(c, t)
		})

		// 标题 + 删除 + 保存状态
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			title := ui.TextInput(c, &a.notes.title).Grow(1).
				FontSize(t.FontSize + 4).Placeholder("无标题")
			title.OnTextKey(func(k ui.TextKey) bool {
				if k.Key != ui.KeyEnter || k.Mods != 0 {
					return false
				}
				a.notes.tagInput.Focus(c)
				return true
			})
			if a.notes.dirty {
				ui.Text(c, "未保存").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			} else if !a.notes.savedAt.IsZero() {
				ui.Text(c, "已保存 "+a.notes.savedAt.Format("15:04")).
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			}
			delLabel := "删除"
			if a.notes.confirmID == a.notes.sel && time.Since(a.notes.confirmAt) <= noteConfirmDelay {
				delLabel = "确认删除？"
			}
			if ui.Button(c, delLabel).Clicked() {
				a.deleteSelectedNote()
			}
		})

		// 标签：chips 与输入框同一行（在同一个框里），回车/逗号确认，空输入退格删末尾
		a.noteTagEditor(c, t)

		// 编辑器：Markdown 两栏（源码 / 预览）或富文本（所见即所得）
		if n.Editor == "rich" {
			// 富文本编辑器只处理文本粘贴；Ctrl/Cmd+V 时先记一笔，下一帧
			// 再把剪贴板图片作为附件插进去（文本粘贴不受影响）。
			if c.Shortcut(ui.Cmd, ui.KeyV) || c.Shortcut(ui.Ctrl, ui.KeyV) {
				a.notes.pendingPaste = true
				a.requestFrame()
			}
			if a.notes.richDoc == nil {
				txt := richtext.ParseMarkdown(a.notes.body)
				a.rehydrateRichImages(txt)
				a.notes.richDoc = richtext.NewDocument(txt)
			}
			a.noteRichToolbar(c, t)
			// 富文本编辑器自己会滚动，但它的布局高度可能大于可见区；再套一层
			// Fill + Clip 的容器，超出内容区的内容一定被裁掉，不会画到边框外。
			var ed ui.Element
			ui.Box(c).Fill().Clip().Children(func() {
				ed = richtext.Editor(c, a.notes.richDoc, richtext.EditorOptions{
					Label:       "笔记正文",
					Placeholder: "开始写…",
				}).Fill()
			})
			if ed.Changed() {
				a.notes.body = a.notes.richDoc.Text().Markdown()
				a.noteMarkDirty()
			}
		} else {
			ui.Row(c).Gap(10).AlignItems(ui.Stretch).Grow(1).Children(func() {
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					ui.Text(c, "Markdown").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					ta := ui.TextArea(c, &a.notes.body).Fill().MinHeight(240).
						Placeholder("写点什么… 支持 Markdown；可拖入图片或粘贴剪贴板图片")
					if ta.Changed() {
						a.noteMarkDirty()
					}
					if a.notes.hasPendingCaret {
						ta.SetTextSelection(a.notes.pendingCaret, a.notes.pendingCaret)
						a.notes.hasPendingCaret = false
					}
					if s, _ := ta.TextSelection(); s >= 0 {
						a.notes.caretPos = s
					}
					if files := ta.DroppedFiles(); len(files) > 0 {
						a.noteAddImageFilesAt(files, a.notes.caretPos)
					}
					ta.OnPaste(func(text string) bool {
						// 粘贴事件期间剪贴板被引擎占用，这里只记意图、下一帧再读图片；
						// 返回 false 让文字照常粘贴（浏览器复制图片常同时带文字）。
						a.notes.pendingPaste = true
						a.requestFrame()
						return false
					})
				})
				ui.Column(c).Grow(1).Basis(0).Children(func() {
					ui.Text(c, "预览").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
					box := ui.Box(c).Fill().MinHeight(240).Padding(12).
						Radius(t.Radius).Background(t.Surface)
					box.Children(func() {
						if a.notes.md.LoadImage == nil {
							a.notes.md.LoadImage = a.noteImageLoader
						}
						ui.Scroll(c).Fill().Children(func() {
							markdown.View(c, a.notes.body, &a.notes.md)
						})
					})
				})
			})
		}

		// 底部状态：错误 / 字数 / 图片提示
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if a.notes.saveErr != "" {
				ui.Text(c, a.notes.saveErr).FontSize(t.FontSize - 2).TextColor(t.Danger)
			} else {
				ui.Text(c, fmt.Sprintf("%d 字 · 图片存于 notes-media/", len([]rune(a.notes.body)))).
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			}
			ui.Text(c, "").Grow(1)
			ui.Text(c, "自动保存：停止输入 0.7 秒后落库，切换/离开页面也会保存").
				FontSize(t.FontSize - 3).TextColor(t.TextMuted)
		})
	})
}

// noteTagEditor 标签编辑：chips 与输入框在同一行/同一个框里。
//
// 交互：回车或逗号把输入框内容变成 chip；点 chip 删除；输入框为空时按退格删最后一个
// chip（不用把光标移出输入框）。
func (a *App) noteTagEditor(c *ui.Context, t *ui.Theme) {
	changed := false
	box := ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Padding(4, 8).
		Radius(t.Radius).Background(t.Surface).Border(1, t.Border)
	box.Children(func() {
		// 固定顺序：先「chips 容器」，再输入框。容器始终存在，标签增删不会改变
		// 输入框在兄弟节点里的位置，焦点不会因为重排而丢。
		chips := ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Shrink(0)
		chips.Children(func() {
			for i := range a.notes.tags {
				i := i
				tag := a.notes.tags[i]
				chip := ui.Row(c).Gap(4).AlignItems(ui.Center).Padding(2, 8).
					Radius(999).Background(kit.Alpha(t.Accent, 0.12)).
					Cursor(ui.CursorPointer).Label("标签 " + tag).Tooltip("点击移除")
				chip.Children(func() {
					ui.Text(c, tag).FontSize(t.FontSize - 2)
					ui.Text(c, "×").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				})
				if chip.Clicked() {
					a.notes.tags = append(a.notes.tags[:i], a.notes.tags[i+1:]...)
					changed = true
					a.notes.tagInput.Focus(c)
				}
			}
		})

		// TextInputBase 没有自己的边框/底色：chips 与输入框看起来是**同一个输入框**。
		in := ui.TextInputBase(c, &a.notes.draft).MinWidth(120).Grow(1).Basis(0).
			Placeholder("添加标签，回车/逗号确认；空输入时退格删除上一个").
			Bind(&a.notes.tagInput)
		a.notes.tagFocused = in.Focused()
		in.OnTextKey(func(k ui.TextKey) bool {
			switch {
			case k.Key == ui.KeyEnter && k.Mods == 0:
				if a.addNoteTag(k.Text()) {
					changed = true
				}
				k.SetText("")
				// 标签增删会重排；显式把焦点要回来，连续输入不用重新点输入框。
				a.notes.tagInput.Focus(c)
				return true
			case k.Key == ui.KeyBackspace && k.Mods == 0 && k.Text() == "":
				if a.removeLastNoteTag() {
					changed = true
				}
				a.notes.tagInput.Focus(c)
				return true
			}
			return false
		})
		if strings.HasSuffix(a.notes.draft, ",") || strings.HasSuffix(a.notes.draft, "，") {
			if a.addNoteTag(strings.TrimRight(a.notes.draft, ",，")) {
				changed = true
			}
		}
	})
	if changed {
		a.noteMarkDirty()
	}
}

// removeLastNoteTag 删除最后一个标签；没有标签时返回 false。
func (a *App) removeLastNoteTag() bool {
	if len(a.notes.tags) == 0 {
		return false
	}
	a.notes.tags = a.notes.tags[:len(a.notes.tags)-1]
	return true
}

// addNoteTag 追加一个标签（去重、去空白）。
func (a *App) addNoteTag(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, t := range a.notes.tags {
		if t == s {
			a.notes.draft = ""
			return false
		}
	}
	a.notes.tags = append(a.notes.tags, s)
	a.notes.draft = ""
	return true
}

// rehydrateRichImages 富文本从 Markdown 还原后，把附件 URL 对应的本地图片重新加载。
// Markdown 里只存相对路径，ParseMarkdown 出来的 Attachment.Image 是空的；
// 不补这一步，切走再切回来图片就「消失」了。
func (a *App) rehydrateRichImages(txt richtext.Text) {
	for _, p := range txt.Paragraphs() {
		for _, r := range p.Runs {
			at := r.Attributes.Attachment
			if at == nil || at.Image != nil || at.URL == "" {
				continue
			}
			if src, err := a.noteImageLoader(at.URL); err == nil {
				at.Image = src
			}
		}
	}
}

// processPendingPaste 在粘贴事件后的下一帧处理剪贴板图片（此时剪贴板锁已释放）。
func (a *App) processPendingPaste() {
	if !a.notes.pendingPaste || a.notes.reading {
		return
	}
	a.notes.pendingPaste = false
	n := a.noteCurrent()
	if n == nil {
		return
	}
	if n.Editor == "rich" {
		a.pasteImageIntoRich()
		return
	}
	a.notePasteImageAt(a.notes.caretPos)
}

// pasteImageIntoRich 读剪贴板图片并作为附件插入富文本。
func (a *App) pasteImageIntoRich() bool {
	formats := mygo.Clipboard.Formats()
	for _, f := range formats {
		if f != transfer.PNG {
			continue
		}
		data, err := mygo.Clipboard.ReadFormat(transfer.PNG)
		if err == nil {
			return a.addRichImageBytes(data, "png")
		}
		logging.Warn("notes", "读取剪贴板 PNG 失败（富文本）", "err", err.Error())
	}
	if files, err := mygo.Clipboard.ReadFiles(); err == nil {
		added := false
		for _, p := range files {
			ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
			switch ext {
			case "png", "jpg", "jpeg", "gif", "webp", "bmp":
			default:
				continue
			}
			data, rerr := os.ReadFile(p)
			if rerr != nil {
				continue
			}
			if ext == "jpeg" {
				ext = "jpg"
			}
			if a.addRichImageBytes(data, ext) {
				added = true
			}
		}
		return added
	}
	return false
}

// noteReader 阅读模式：只读展示标题、元信息与正文；点「编辑」或双击列表回到编辑。
func (a *App) noteReader(c *ui.Context, t *ui.Theme, n *store.Note) {
	ui.Column(c).Fill().Padding(kit.PagePadY, kit.PagePadX).Gap(10).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, noteDisplayTitle(*n)).FontSize(22).Bold().Grow(1)
			ui.Text(c, "阅读模式").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			if ui.Button(c, "编辑").Clicked() {
				a.notes.reading = false
				a.requestFrame()
			}
			delLabel := "删除"
			if a.notes.confirmID == a.notes.sel && time.Since(a.notes.confirmAt) <= noteConfirmDelay {
				delLabel = "确认删除？"
			}
			if ui.Button(c, delLabel).Clicked() {
				a.deleteSelectedNote()
			}
		})
		meta := timeAgo(n.UpdatedAt)
		if rel := a.noteRelationLabel(*n); rel != "" {
			meta += " · " + rel
		}
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(c, meta).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			for _, tag := range n.Tags {
				kit.Pill(c, t, tag, t.Accent)
			}
		})
		if n.Editor == "rich" {
			if a.notes.richDoc == nil {
				txt := richtext.ParseMarkdown(n.Body)
				a.rehydrateRichImages(txt)
				a.notes.richDoc = richtext.NewDocument(txt)
			}
			ui.Box(c).Fill().Clip().Children(func() {
				richtext.Editor(c, a.notes.richDoc, richtext.EditorOptions{
					Label:    "笔记正文",
					ReadOnly: true,
				}).Fill()
			})
			return
		}
		if a.notes.md.LoadImage == nil {
			a.notes.md.LoadImage = a.noteImageLoader
		}
		ui.Scroll(c).Fill().Children(func() {
			markdown.View(c, n.Body, &a.notes.md)
		})
	})
}

// noteRichToolbar 富文本格式工具条（对齐 bestuav-mygo 的笔记富文本，除了「复制 Markdown」）。
//
// 一行工具条：按钮按窗口宽度自动换行（Wrap），宽窗口一行放得下，
// 窄窗口自动折到第二行；不再固定分两行。按钮 KeepFocus，点了不会丢选区。
func (a *App) noteRichToolbar(c *ui.Context, t *ui.Theme) {
	doc := a.notes.richDoc
	if doc == nil {
		return
	}
	f := doc.Formatting()
	button := func(label, tip string, on bool, run func()) ui.Element {
		b := ui.Button(c, label).KeepFocus().Padding(5, 10).MinWidth(32).Tooltip(tip)
		if on {
			b.Background(t.Accent.Alpha(0.18))
		}
		if b.Clicked() {
			run()
		}
		return b
	}
	divider := func() { ui.Box(c).Width(1).Height(22).Background(t.Border) }

	ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		style := map[int]string{0: "正文", 1: "标题", 2: "一级标题", 3: "二级标题"}[f.Paragraph.Heading]
		if f.ParagraphsMixed {
			style = ""
		}
		if ui.Select(c, &style, []string{"标题", "一级标题", "二级标题", "正文"}).
			KeepFocus().Label("段落样式").Changed() {
			doc.SetHeading(map[string]int{"标题": 1, "一级标题": 2, "二级标题": 3}[style])
		}
		divider()
		button("B", "粗体", f.Bold == richtext.FormatOn, doc.ToggleBold).Bold()
		button("I", "斜体", f.Italic == richtext.FormatOn, doc.ToggleItalic).Italic()
		button("U", "下划线", f.Underline == richtext.FormatOn, doc.ToggleUnderline).Underline()
		button("S", "删除线", f.Strikethrough == richtext.FormatOn, doc.ToggleStrikethrough).Strikethrough()
		button("</>", "代码", f.Code == richtext.FormatOn, doc.ToggleCode)
		button("图片", "插入图片", false, a.chooseRichImages)

		link := button("链接", "插入链接", f.Link != "", func() {
			a.notes.linkURL = doc.Formatting().Link
			a.notes.linkOpen = true
		})
		ui.Popover(c, link, &a.notes.linkOpen, func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				in := ui.TextInput(c, &a.notes.linkURL).Placeholder("https://").
					Width(260).AutoFocus().Label("URL")
				if in.Submitted() || ui.PrimaryButton(c, "确定").Clicked() {
					doc.SetLink(strings.TrimSpace(a.notes.linkURL))
					a.notes.linkOpen = false
				}
				if f.Link != "" && ui.Button(c, "移除").Clicked() {
					doc.SetLink("")
					a.notes.linkOpen = false
				}
			})
		})
		button("•", "无序列表", f.Paragraph.List == richtext.Bullets, func() {
			doc.ToggleList(richtext.Bullets)
		})
		button("1.", "有序列表", f.Paragraph.List == richtext.Numbers, func() {
			doc.ToggleList(richtext.Numbers)
		})
		button("☐", "任务清单", f.Paragraph.List == richtext.Tasks, func() {
			doc.ToggleList(richtext.Tasks)
		})
		divider()
		button("❝", "引用", f.Paragraph.Quote, doc.ToggleQuote)
		button("{}", "代码块", f.Paragraph.CodeBlock, doc.ToggleCodeBlock)
	})
}

// ---- 归属 / 富文本 / 图片清理 ----

// refreshNoteRelationSel 把当前笔记的 project/repo id 还原成下拉框文字。
func (a *App) refreshNoteRelationSel() {
	d := a.atlas()
	n := a.noteCurrent()
	a.notes.projSel, a.notes.repoSel = "未归属", "未关联"
	if n == nil {
		return
	}
	if n.ProjectID != 0 {
		a.notes.projSel = d.projectName(n.ProjectID)
	}
	for _, r := range d.repos {
		if r.ID == n.RepoID {
			a.notes.repoSel = r.Name
			break
		}
	}
}

// noteRelationRow 编辑器的「归属项目 / 关联仓库」两个下拉框。
func (a *App) noteRelationRow(c *ui.Context, t *ui.Theme) {
	n := a.noteCurrent()
	if n == nil {
		return
	}
	d := a.atlas()
	projOpts := d.projectOptions()
	repoOpts := make([]string, 0, len(d.repos)+1)
	repoOpts = append(repoOpts, "未关联")
	for _, r := range d.repos {
		repoOpts = append(repoOpts, r.Name)
	}
	if a.notes.projSel == "" {
		a.notes.projSel = "未归属"
	}
	if a.notes.repoSel == "" {
		a.notes.repoSel = "未关联"
	}
	ui.Text(c, "项目").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	choice.Dropdown(c, "归属项目", projOpts, &a.notes.projSel)
	ui.Text(c, "仓库").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	choice.Dropdown(c, "关联仓库", repoOpts, &a.notes.repoSel)

	pid := d.projectIDByName(a.notes.projSel)
	var rid int64
	if a.notes.repoSel != "未关联" {
		rid = a.repoIDByName(a.notes.repoSel)
	}
	if pid != n.ProjectID || rid != n.RepoID {
		n.ProjectID, n.RepoID = pid, rid
		a.noteMarkDirty()
	}
}

// repoIDByName 把仓库下拉框文字还原成 id（找不到 = 0）。
func (a *App) repoIDByName(name string) int64 {
	for _, r := range a.atlas().repos {
		if r.Name == name {
			return r.ID
		}
	}
	return 0
}

// noteRelationLabel 列表里的归属摘要（项目 · 仓库），没有就返回空。
func (a *App) noteRelationLabel(n store.Note) string {
	d := a.atlas()
	var parts []string
	if n.ProjectID != 0 {
		if name := d.projectName(n.ProjectID); name != "" {
			parts = append(parts, name)
		}
	}
	if n.RepoID != 0 {
		for _, r := range d.repos {
			if r.ID == n.RepoID {
				parts = append(parts, r.Name)
				break
			}
		}
	}
	return strings.Join(parts, " · ")
}

// openNoteNewDialog 打开「新建笔记：选编辑器」弹窗。
func (a *App) openNoteNewDialog() {
	a.notes.newDialog = true
	a.atlasUI.dialog = "note-new"
	a.atlasUI.dialogPage = a.page
	a.requestFrame()
}

// noteNewDialog 新建笔记时选择编辑器：Markdown（源码+预览）或富文本（WYSIWYG）。
func (a *App) noteNewDialog(c *ui.Context, t *ui.Theme) {
	open := &a.notes.newDialog
	overlay.Modal(c, "新建笔记", open, overlay.ModalOptions{Close: "取消"}, func() {
		dialogDesc(c, t, "选择编辑器后立即创建；两种笔记都存为 Markdown，之后不会互相转换。")
		ui.Row(c).Gap(10).Wrap().Children(func() {
			if ui.Button(c, "Markdown 笔记（源码 + 预览）").Clicked() {
				*open = false
				a.closeAtlasDialog()
				a.newNoteKind("markdown")
			}
			if ui.Button(c, "富文本笔记（所见即所得）").Clicked() {
				*open = false
				a.closeAtlasDialog()
				a.newNoteKind("rich")
			}
		})
		ui.Text(c, "Markdown 适合代码块、表格与纯文本；富文本适合加粗、列表等排版。").
			FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	})
	if !*open {
		a.closeAtlasDialog()
	}
}

// cleanNoteMedia 清理 notes-media 里没有被任何笔记引用的图片（二次确认）。
func (a *App) cleanNoteMedia() {
	if time.Since(a.notes.cleanAt) > noteConfirmDelay {
		a.notes.cleanAt = time.Now()
		a.toast("再点一次「清理图片」确认：删除没有被任何笔记引用的图片")
		a.requestFrame()
		return
	}
	a.notes.cleanAt = time.Time{}
	a.saveNote(true) // 正在编辑的正文也要参与引用统计
	removed, kept := a.removeUnreferencedMedia()
	logging.Audit("notes", "清理无引用图片", "removed", removed, "kept", kept)
	a.toast(fmt.Sprintf("已清理 %d 张无引用图片（保留 %d 张）", removed, kept))
	a.requestFrame()
}

// noteMediaRe 匹配正文里的 notes-media/<文件名> 引用。
var noteMediaRe = regexp.MustCompile(`notes-media/([A-Za-z0-9._-]+)`)

// removeUnreferencedMedia 扫描全部笔记正文，删除目录里没被引用的文件。
func (a *App) removeUnreferencedMedia() (removed, kept int) {
	dir := noteMediaDir()
	if dir == "" {
		return 0, 0
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	refs := make(map[string]bool)
	for _, n := range a.notes.list {
		for _, m := range noteMediaRe.FindAllStringSubmatch(n.Body, -1) {
			refs[m[1]] = true
		}
	}
	for _, f := range files {
		if f.IsDir() {
			continue
		}
		name := f.Name()
		if refs[name] {
			kept++
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err == nil {
			removed++
		}
	}
	return removed, kept
}

// ---- 小工具 ----

// noteDisplayTitle 列表里的标题：空标题显示「无标题」。
func noteDisplayTitle(n store.Note) string {
	if strings.TrimSpace(n.Title) == "" {
		return "无标题"
	}
	return strings.TrimSpace(n.Title)
}

// noteSnippet 正文摘要：取第一行非空文字，去掉常见 Markdown 标记。
func noteSnippet(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "![") {
			continue
		}
		line = strings.TrimLeft(line, "#>*-+ \t")
		if line != "" {
			return line
		}
	}
	return ""
}

func pick(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
