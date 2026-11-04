package view

import (
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/ui/kit"

	"github.com/ELY-Open-Source/CrychicUI/chart"
)

// 首页：产品级概览。四块内容——欢迎与快捷操作、四个 KPI、近期任务、服务与入口。
//
// 数据都是本地演示数据（没有后端），但结构按真实看板组织：KPI 有环比、任务有
// 状态与耗时、服务有健康度。接后端时只需把这些字面量换成接口返回即可。

// kpi 首页指标项。value 存数值而不是字符串，是为了让大数字能滚（见 CountingKPICard）；
// format 决定它长什么样（千分位、小数位、单位）。
type kpi struct {
	icon   *ui.SVG
	label  string
	value  float64
	format func(float64) string
	delta  string
	up     bool
}

// homeKPIList 四个核心指标。包级变量：首页每帧都要用，没必要每帧重建切片。
var homeKPIList = []kpi{
	{icons.Bolt, "今日任务", 37, intText, "12.4%（较昨日）", true},
	{icons.Checklist, "任务成功率", 99.2, pctText, "0.3%", true},
	{icons.Shield, "有效授权", 1286, groupText, "24 份", true},
	{icons.Clock, "平均耗时", 1.8, secText, "0.4s（较昨日）", false},
}

// homeKPIs 四个核心指标（返回包级切片，调用方只读）。
func (a *App) homeKPIs() []kpi { return homeKPIList }

// homeTrend 近 7 日任务量趋势（演示数据），供首页 CrychicUI 折线图使用。
// 接后端时把这两个字面序列换成接口返回的「每日创建/完成」即可，组件签名不变。
//
// 同样放包级：chart.Line 只读这两个列表，每帧重建纯属浪费。
var (
	homeTrendLabels = []string{"周一", "周二", "周三", "周四", "周五", "周六", "周日"}
	homeTrendSeries = []chart.Series{
		{Name: "创建", Values: []float64{12, 18, 9, 22, 30, 8, 6}},
		{Name: "完成", Values: []float64{10, 16, 8, 20, 27, 7, 5}},
	}
)

func (a *App) homeTrend() (labels []string, series []chart.Series) {
	return homeTrendLabels, homeTrendSeries
}

// 四个格式化函数：滚动时每帧都会调用，所以一律写死小数位——
// 否则数字宽度会一路跳，整行跟着抖。
func intText(v float64) string   { return fmt.Sprintf("%.0f", v) }
func pctText(v float64) string   { return fmt.Sprintf("%.1f%%", v) }
func secText(v float64) string   { return fmt.Sprintf("%.1fs", v) }
func groupText(v float64) string { return groupDigits(int(math.Round(v))) }

// groupDigits 给整数加千分位（1286 → "1,286"）。Go 的标准库没有这功能，
// 又不必为一个分隔符引入 x/text。
func groupDigits(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	var b []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			b = append(b, ',')
		}
		b = append(b, c)
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

func (a *App) homePage(c *ui.Context, t *ui.Theme) {
	// 说明：页面只写「区块」，区块之间的垂直间距与四周留白由 shell.Content
	// 统一施加（kit.PageGap / PagePadX / PagePadY），这里不要再写外边距。

	// KPI 数字滚动的起点：在**帧内**取时刻，不能用 time.Now()（离屏测试的帧
	// 时钟由 Tester 持有，两个时钟混用会让进度算错）。
	// a.kpiStart 会在每次进入首页时被 onEnter 清零（见 registry.go），
	// 于是每次切回首页都会重新滚一遍。
	if a.kpiStart.IsZero() {
		a.kpiStart = c.Now()
	}

	// ---- 欢迎区 ----
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Gap(4).Grow(1).Children(func() {
			ui.Text(c, greeting()+"，欢迎使用"+appmeta.Title).
				FontSize(20).Bold().SingleLine()
			ui.Text(c, "授权服务与任务队列运行正常，以下是最近 24 小时的运行概览。").
				FontSize(t.FontSize - 1).TextColor(t.TextMuted)
		})
		if ui.PrimaryButton(c, "发起任务").Clicked() {
			a.page = "task"
		}
		if ui.Button(c, "生成 license").Clicked() {
			a.page = "gen-license"
		}
	})

	// ---- KPI 四卡 ----
	// 用栅格而不是 Row+Grow：四张卡严格等宽（内容长度不影响列宽），同一行等高。
	kit.RowGrid(c, 4, func() {
		for _, k := range a.homeKPIs() {
			kit.CountingKPICard(c, t, k.icon, k.label, k.value, k.format, a.kpiStart, k.delta, k.up)
		}
	})

	// ---- 趋势图（CrychicUI chart.Line 演示：读取本页主题，自动继承量子科学观感）----
	// 仅新增一张卡片，不动既有 KPI / 任务列表布局；旨在验证「组件库 + 自建 kit 共存」。
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "近 7 日任务趋势", "演示数据 · CrychicUI chart.Line", nil)
		labels, series := a.homeTrend()
		chart.Line(c, "趋势", labels, series, &a.chartHover, chart.LineOptions{Height: 200, Format: "%d"})
	})

	// ---- 主体两栏（左主右辅，宽度按 2:1 分配，间距与上面的区块一致）----
	ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Start).Children(func() {
		// 左：近期任务
		ui.Column(c).Grow(2).Children(func() {
			kit.Card(c, t, func() {
				kit.CardHeader(c, t, "近期任务", "点击任意一行查看任务详情（演示）", func() {
					if ui.Button(c, "查看全部").Clicked() {
						a.page = "task"
					}
				})
				a.recentTaskRows(c, t)
			})
		})

		// 右：服务状态 + 快捷入口
		ui.Column(c).Grow(1).Gap(kit.PageGap).Children(func() {
			kit.Card(c, t, func() {
				kit.CardHeader(c, t, "服务状态", "实时（演示）", nil)
				ui.Column(c).Gap(12).Children(func() {
					kit.StatRow(c, t, "授权服务", "api.bestuav.local · 12ms", true)
					kit.StatRow(c, t, "license 签发", "签名服务 · 正常", true)
					kit.StatRow(c, t, "对象存储", "report 桶 · 78% 已用", true)
					kit.StatRow(c, t, "任务队列", "0 个失败任务", true)
				})
			})
			kit.Card(c, t, func() {
				kit.CardHeader(c, t, "快捷入口", "", nil)
				ui.Grid(c).Columns(2).Gap(10).Children(func() {
					a.quickTile(c, t, "异步任务", icons.Bolt, func() { a.page = "task" })
					a.quickTile(c, t, "生成 license", icons.FileKey, func() { a.page = "gen-license" })
					a.quickTile(c, t, "基本信息", icons.User, func() {
						// 基本信息已不在菜单里：跳到设置页并直接开弹窗（见 basicinfo.go）。
						a.page = "settings"
						a.showBasicInfo = true
					})
					a.quickTile(c, t, "设置", icons.Gear, func() { a.page = "settings" })
				})
			})
		})
	})
}

// recentTaskRows 首页的近期任务列表（最多 5 行，手工排版以便控制紧凑度）。
func (a *App) recentTaskRows(c *ui.Context, t *ui.Theme) {
	n := len(a.tasks)
	if n > 5 {
		n = 5
	}
	if n == 0 {
		kit.EmptyHint(c, t, "暂无任务记录")
		return
	}
	ui.Column(c).Gap(2).Children(func() {
		for i := 0; i < n; i++ {
			r := a.tasks[i]
			row := ui.Row(c).Gap(12).AlignItems(ui.Center).
				Padding(9, 8).Radius(t.Radius)
			if row.Hovered() {
				row.Background(t.SurfaceHover)
			}
			row.Children(func() {
				ui.Column(c).Gap(1).Grow(1).Children(func() {
					ui.Text(c, r.name).SingleLine()
					ui.Text(c, r.kind+" · "+r.cost).FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
				})
				// 刚完成的那条会脉冲一下（doneAt 为零值时与 kit.Pill 等价）。
				kit.PillPulse(c, t, r.status, kit.StatusColor(t, r.status), r.doneAt)
			})
			if row.Clicked() {
				a.page = "task"
			}
		}
	})
}

// quickTile 快捷入口小方块：图标 + 文字，点击执行动作。
//
// 用回调而不是「页面 id」：入口不只有跳页（如「基本信息」是跳设置页 + 开弹窗）。
func (a *App) quickTile(c *ui.Context, t *ui.Theme, label string, icon *ui.SVG, action func()) {
	tile := ui.Column(c).Gap(6).Padding(12).Radius(kit.TileRadius).
		Background(kit.Alpha(t.SurfaceHover, 0.8)).Border(1, t.Border)
	if tile.Hovered() {
		tile.Background(t.SurfacePressed)
	}
	tile.Children(func() {
		ui.Icon(c, icon).FontSize(16).TextColor(t.Accent)
		ui.Text(c, label).FontSize(t.FontSize - 2).SingleLine()
	})
	if tile.Clicked() && action != nil {
		action()
	}
}

// greeting 按当前时间给出问候语。
func greeting() string {
	h := time.Now().Hour()
	switch {
	case h < 6:
		return "凌晨好"
	case h < 12:
		return "上午好"
	case h < 14:
		return "中午好"
	case h < 18:
		return "下午好"
	default:
		return "晚上好"
	}
}

// homeSummary 供其它页面复用的简短摘要文案（如任务页顶部）。
func (a *App) homeSummary() string {
	return fmt.Sprintf("累计任务 %d 条，失败 0 条", len(a.tasks))
}
