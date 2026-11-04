package view

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/apppath"
	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/ui/kit"
)

// ---- 基本信息弹窗 ----
//
// 这里放的是「系统与运营信息」：应用标识、运行环境、页面缓存、数据库/日志路径、
// 配置文件位置。它们不是日常业务操作，所以**不进侧栏菜单**（原来的「个人中心」
// 分组已移除），而是收进设置页「关于」区的一个按钮，点开弹窗查看。
//
// 为什么是弹窗而不是设置页里的一块区域：信息量大（5 组、二十来行），铺在设置页会把
// 「改偏好」这条主线冲淡；弹窗只在需要时出现，还能带自己的动作按钮——用户报障时
// 一键把环境信息复制出来。

const (
	// basicInfoWidth 弹窗内容宽度。窗口最小 820 宽，弹窗可用宽度约 780，够放。
	basicInfoWidth = 680

	// cacheRowsPreferred 缓存表体希望露出的整行数（字号小时能放更多，这里是上限）。
	cacheRowsPreferred = 5

	// cacheTabChrome 缓存页签里与行数无关的固定高度：说明行 + 间距 + 表头。
	cacheTabChrome = 104

	// cacheTabMaxHeight 缓存页签的高度上限：最小窗口（520 高）时弹窗内容区约 480，
	// 扣掉标题/页签条/底部按钮后留给内容区的余量。
	cacheTabMaxHeight = 292
)

// cacheRowHeight 缓存表一行的高度：上下内边距各 6 + 一行文字的近似行高。
func cacheRowHeight(t *ui.Theme) float32 { return t.FontSize*1.3 + 12 }

// cacheRowsFit 当前字号下能**完整**显示的行数。
//
// 字号越大行越高、能放的越少，但始终取整行数（3..cacheRowsPreferred）——
// 这样表体底部永远不会出现被切一半的行（那比截断更难看）。
func cacheRowsFit(t *ui.Theme) int {
	avail := float32(cacheTabMaxHeight - cacheTabChrome)
	rowH := cacheRowHeight(t)
	// body = n*rowH + (n-1)*2 ≤ avail  ⇒  n ≤ (avail+2)/(rowH+2)
	// （行距要算进去，否则浮点取整会让总高刚好越界——实测字号 19 时越界 3.5px）
	n := int((avail + 2) / (rowH + 2))
	if n > cacheRowsPreferred {
		n = cacheRowsPreferred
	}
	if n < 3 {
		n = 3
	}
	return n
}

// cacheBodyHeight 缓存表体的高度：整行数 × 行高 + 行距（2）。
func cacheBodyHeight(t *ui.Theme) float32 {
	n := float32(cacheRowsFit(t))
	rowH := cacheRowHeight(t)
	return n*rowH + (n-1)*2
}

// basicTabHeights 各页签内容区高度（DIP），下标与 App.basicTab 对应：
// 应用 / 环境 / 缓存 / 数据与日志 / 配置。
//
// 按各自内容量给，而不是统一高度：统一高度会让「应用」这种只有 4 行的页签留出大片
// 空白。各页签高度都远小于最小窗口（520 高）的可用空间，不会被挤出屏幕。
// 缓存页签（下标 2）是动态计算的，见 basicTabHeight。
var basicTabHeights = [5]float32{
	132, // 应用：4 行键值
	186, // 环境：6 行键值
	0,   // 缓存：说明行 + 固定表头 + 可滚动表体（随字号自适应）
	236, // 数据与日志：计数药丸 + 2 行路径 + 5 条事件
	96,  // 配置：路径 + 说明
}

// basicTabHeight 返回当前页签的内容区高度（越界时按第一个页签处理）。
//
// 「缓存」页签随字号自适应：表体要露出整行，字号一变行高就变（见 cacheBodyHeight）。
func (a *App) basicTabHeight(t *ui.Theme) float32 {
	if a.basicTab == 2 {
		return cacheTabChrome + cacheBodyHeight(t)
	}
	if a.basicTab < 0 || a.basicTab >= len(basicTabHeights) {
		return basicTabHeights[0]
	}
	return basicTabHeights[a.basicTab]
}

// basicInfoDialog 基本信息弹窗：标题 + 页签 + 底部动作。
//
// 由设置页「关于」区的「查看」按钮打开（见 page_settings.go），首页「快捷入口 →
// 基本信息」也会跳到设置页并直接打开它。
func (a *App) basicInfoDialog(c *ui.Context, t *ui.Theme) {
	if !a.showBasicInfo {
		return
	}
	// 打开期间顺手刷新环境快照（函数内部有 5 秒缓存，不会每帧读 MemStats）。
	a.collectEnv(false)

	modalPanel(c, t, &a.showBasicInfo, func() {
		ui.Row(c).Width(basicInfoWidth).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "基本信息").FontSize(18).Bold().Grow(1)
			ui.Text(c, appmeta.Title+" · "+appmeta.BuildVersion()).
				FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
		})

		// 分页签而不是长滚动：信息有二十来行，单页滚动会在中途把文字截断（看起来像
		// 渲染坏了）；分页签每屏只显示一组，高度可控，也更好定位——查日志路径直接点
		// 「数据与日志」，不用在长列表里找。
		ui.Tabs(c, &a.basicTab, "应用", "环境", "缓存", "数据与日志", "配置")

		// 高度随页签变化：内容少的页签不留大片空白。
		// Clip：任何页签内容万一超出给定高度，也只会在自己的框内被裁掉，
		// 绝不会画到下面的「刷新环境 / 复制全部信息 / 关闭」按钮上（踩过：缓存页签
		// 行数多时，提示文字直接盖住了按钮）。
		ui.Column(c).Width(basicInfoWidth).Height(a.basicTabHeight(t)).Clip().Children(func() {
			switch a.basicTab {
			case 1:
				a.basicEnvContent(c, t)
			case 2:
				a.basicCacheContent(c, t)
			case 3:
				a.basicRuntimeContent(c, t)
			case 4:
				a.basicConfigContent(c, t)
			default:
				a.basicAppContent(c, t)
			}
		})

		ui.Row(c).Width(basicInfoWidth).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "刷新环境").Clicked() {
				a.collectEnv(true)
			}
			if ui.Button(c, "复制全部信息").Clicked() {
				a.copyText(a.basicInfoText(), "基本信息已复制")
			}
			if ui.PrimaryButton(c, "关闭").Clicked() {
				a.showBasicInfo = false
			}
		})
	})
}

// ---- 页签内容（标题由页签本身承担，这里只画内容）----

// basicAppContent 应用标识。
func (a *App) basicAppContent(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(6).Children(func() {
		kit.KV(c, t, "产品名称", appmeta.Title)
		kit.KV(c, t, "内部标识", appmeta.Name)
		kit.KV(c, t, "应用标识", appmeta.Identifier)
		kit.KV(c, t, "构建版本", a.buildVersionText())
	})
}

// basicEnvContent 运行环境（数据来自 Go 运行时，5 秒缓存）。
func (a *App) basicEnvContent(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(6).Children(func() {
		kit.KV(c, t, "操作系统", a.env.platform+" / "+a.env.arch)
		kit.KV(c, t, "Go 版本", a.env.goVersion)
		kit.KV(c, t, "逻辑核心", itoa(a.env.cpus)+" 核")
		kit.KV(c, t, "协程数", itoa(a.env.goroutines))
		kit.KV(c, t, "堆内存", formatMB(a.env.heapMB))
		kit.KV(c, t, "系统内存", formatMB(a.env.sysMB))
	})
}

// basicCacheContent 页面缓存与生命周期（实时反映 pagelife.go 的结果）。
func (a *App) basicCacheContent(c *ui.Context, t *ui.Theme) {
	rows := a.pageCacheRows()
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			// 说明合并成一行：原来底部还有一句「滚动位置…归零」的提示，与这句重复，
			// 而且多占一行会把表体挤小（曾导致提示被裁成半行）。
			ui.Text(c, "缓存页切回保留状态（含滚动位置）；闲置或「不缓存」页离开即销毁。").
				FontSize(t.FontSize - 2).TextColor(t.TextMuted).Grow(1)
			// 计数：表体只显示 5 行，用它告诉用户「还有更多，表内可滚」。
			kit.Pill(c, t, "共 "+itoa(len(rows))+" 个", t.TextMuted)
			if ui.Button(c, "销毁非常驻缓存").Clicked() {
				a.destroyCachedPages()
			}
		})

		if len(rows) == 0 {
			kit.EmptyHint(c, t, "当前没有缓存的页面")
			return
		}
		// 表头固定、表体独立滚动：缓存的页面**全部列出**（不再截断），
		// 数量多了就在表体里滚，不会把下面的说明挤出去。
		a.pageCacheHeader(c, t)
		ui.Scroll(c).Height(cacheBodyHeight(t)).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				a.pageCacheBody(c, t, rows)
			})
		})
	})
}

// basicRuntimeContent 运行数据与日志：库计数、路径、最近事件。
//
// 这块不是装饰：用户报障时能直接看到「数据存在哪、最近有没有报错」，
// 不用去翻 %APPDATA%（很多时候用户根本找不到）。
func (a *App) basicRuntimeContent(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(8).Children(func() {
		st := a.DBStats()
		ui.Row(c).Gap(8).Children(func() {
			kit.Pill(c, t, itoa(int(st.Tasks))+" 条任务", t.Accent)
			authColor := t.TextMuted
			if st.Licenses > 0 {
				authColor = t.Success
			}
			kit.Pill(c, t, itoa(int(st.Licenses))+" 条授权", authColor)
			kit.Pill(c, t, itoa(int(st.Events))+" 条事件", t.TextMuted)
			if st.Errors > 0 {
				kit.Pill(c, t, itoa(int(st.Errors))+" 条错误", t.Danger)
			} else {
				kit.Pill(c, t, "无错误", t.Success)
			}
		})

		if p := a.StorePath(); p != "" {
			kit.KV(c, t, "数据库", p)
		} else {
			ui.Text(c, "数据库未启用：本次运行的数据只保存在内存中（原因见日志）。").
				FontSize(t.FontSize - 2).TextColor(t.Danger)
		}
		if d, err := apppath.LogDir(); err == nil {
			kit.KV(c, t, "日志目录", d)
		}

		events := a.RecentEvents(5)
		if len(events) == 0 {
			kit.EmptyHint(c, t, "暂无事件记录")
			return
		}
		ui.Divider(c)
		for _, e := range events {
			ui.Row(c).Gap(kit.CardGap).AlignItems(ui.Center).Children(func() {
				ui.Text(c, e.TS.Format("01-02 15:04:05")).Width(96).Shrink(0).
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				ui.Text(c, e.Level).Width(52).Shrink(0).
					FontSize(t.FontSize - 2).TextColor(eventColor(t, e.Level))
				ui.Text(c, e.Kind).Width(64).Shrink(0).
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				ui.Text(c, e.Message).Grow(1).FontSize(t.FontSize - 2).SingleLine()
			})
		}
	})
}

// basicConfigContent 配置文件位置。
func (a *App) basicConfigContent(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(8).Children(func() {
		p, err := settings.Path()
		if err != nil {
			ui.Text(c, "无法定位配置目录："+err.Error()).TextColor(t.Danger)
			return
		}
		kit.KV(c, t, "配置文件", p)
		ui.Text(c, "用户偏好以 JSON 原子写入（临时文件 + 重命名），与数据库分开存放。").
			FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	})
}

// basicInfoText 把弹窗里的信息拼成一段纯文本，供「复制全部信息」使用。
//
// 目的很实际：用户报障时把这段贴出来，支持人员立刻知道版本、系统、路径与错误计数，
// 不必再逐项追问。
func (a *App) basicInfoText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s（%s）\n", appmeta.Title, appmeta.Name)
	fmt.Fprintf(&b, "应用标识：%s\n", appmeta.Identifier)
	fmt.Fprintf(&b, "构建版本：%s\n", a.buildVersionText())
	fmt.Fprintf(&b, "运行环境：%s / %s · Go %s · %d 核 · 协程 %d\n",
		a.env.platform, a.env.arch, a.env.goVersion, a.env.cpus, a.env.goroutines)
	fmt.Fprintf(&b, "内存：堆 %s / 系统 %s\n", formatMB(a.env.heapMB), formatMB(a.env.sysMB))

	if p := a.StorePath(); p != "" {
		fmt.Fprintf(&b, "数据库：%s\n", p)
	} else {
		b.WriteString("数据库：未启用\n")
	}
	if d, err := apppath.LogDir(); err == nil {
		fmt.Fprintf(&b, "日志目录：%s\n", d)
	}
	if p, err := settings.Path(); err == nil {
		fmt.Fprintf(&b, "配置文件：%s\n", p)
	}
	st := a.DBStats()
	fmt.Fprintf(&b, "数据量：任务 %d / 授权 %d / 事件 %d（错误 %d）\n",
		st.Tasks, st.Licenses, st.Events, st.Errors)
	return b.String()
}

// eventColor 事件级别的颜色（错误醒目、正常灰）。
func eventColor(t *ui.Theme, level string) ui.Color {
	switch level {
	case "CRASH", "ERROR":
		return t.Danger
	case "WARN":
		return t.Warning
	default:
		return t.TextMuted
	}
}

// ---- 运行环境采集 ----

// envSnapshot 一次运行环境采集的结果。
type envSnapshot struct {
	goVersion  string
	platform   string
	arch       string
	cpus       int
	goroutines int
	heapMB     float64
	sysMB      float64
	buildVer   string
	buildRev   string
	collected  time.Time
}

// collectEnv 采集运行环境信息。force=false 时 5 秒内复用上次结果，避免每帧读
// MemStats（它会短暂停顿 GC）。
func (a *App) collectEnv(force bool) {
	if !force && !a.env.collected.IsZero() && time.Since(a.env.collected) < 5*time.Second {
		return
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	snap := envSnapshot{
		goVersion:  runtime.Version(),
		platform:   runtime.GOOS,
		arch:       runtime.GOARCH,
		cpus:       runtime.NumCPU(),
		goroutines: runtime.NumGoroutine(),
		heapMB:     float64(m.HeapAlloc) / (1 << 20),
		sysMB:      float64(m.Sys) / (1 << 20),
		collected:  time.Now(),
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		snap.buildVer = bi.Main.Version
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				snap.buildRev = s.Value
			}
		}
	}
	a.env = snap
}

// buildVersionText 拼装版本信息：模块版本 + 提交号（本地 go run 时是 dev）。
func (a *App) buildVersionText() string {
	v := a.env.buildVer
	if v == "" || v == "(devel)" {
		v = "dev"
	}
	if a.env.buildRev != "" {
		rev := a.env.buildRev
		if len(rev) > 7 {
			rev = rev[:7]
		}
		return v + " · " + rev
	}
	return v
}

// pageCacheHeader 缓存表的表头（固定，不随表体滚动）。
func (a *App) pageCacheHeader(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(2).Children(func() {
		ui.Row(c).Gap(12).Padding(6, 8).Children(func() {
			ui.Text(c, "页面").Width(120).Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, "策略").Width(64).Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, "进入").Width(56).Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, "闲置").Width(72).Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, "滚动位置").Grow(1).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		})
		ui.Divider(c)
	})
}

// pageCacheBody 缓存表的全部行（外层 Scroll 负责滚动，这里不再截断行数）。
func (a *App) pageCacheBody(c *ui.Context, t *ui.Theme, rows []pageCacheRow) {
	for _, r := range rows {
		ui.Row(c).Gap(12).Padding(6, 8).Radius(t.Radius).Children(func() {
			ui.Text(c, r.label).Width(120).Shrink(0).SingleLine()
			ui.Text(c, r.policy).Width(64).Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, itoa(r.enters)+" 次").Width(56).Shrink(0).FontSize(t.FontSize - 2)
			ui.Text(c, r.idle).Width(72).Shrink(0).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Text(c, formatPX(r.scrollY)).Grow(1).FontSize(t.FontSize - 2)
		})
	}
}

// destroyCachedPages 销毁所有「可缓存」页面（常驻页面保留）。
func (a *App) destroyCachedPages() {
	n := 0
	for id := range a.pages {
		if it, ok := pageByID(id); ok && it.policy.keep == keepCached {
			a.destroyPage(id)
			n++
		}
	}
	if n == 0 {
		a.toast("没有可销毁的缓存页面")
		return
	}
	a.toast("已销毁 " + itoa(n) + " 个缓存页面")
}

// formatMB 把 MB 数值格式化成一位小数。
func formatMB(v float64) string {
	return itoa(int(v)) + "." + itoa(int(v*10)%10) + " MB"
}

// formatPX 滚动位置（DIP）显示。
func formatPX(v float32) string {
	if v <= 0 {
		return "顶部"
	}
	return itoa(int(v)) + " px"
}
