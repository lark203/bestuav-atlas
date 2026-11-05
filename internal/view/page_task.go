package view

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/data"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/ui/kit"
)

// 异步任务页：只展示**真实发生过**的后台动作，不再有演示任务。
//
// 每次批量拉取 / 提交推送 / 自动刷新 / 导入 / 克隆都记一条：类型（手动 / 自动 / 导入）、
// 状态、实际耗时、结果说明（失败原因是重点）与完成时间。演示用的「轻量级 / 重量级
// 任务」卡片已移除——它们教不会用户任何东西，还会把真实的执行记录淹掉。

// taskRow 任务列表的一行。
type taskRow struct {
	name     string
	kind     string        // 手动 / 自动 / 导入
	status   string        // 已完成 / 失败 / 运行中 / 排队
	detail   string        // 结果摘要或失败原因
	duration time.Duration // 实际执行耗时；0 = 未知（v7 之前的历史记录）
	costText string        // 历史记录的耗时展示文本，duration 为 0 时兜底
	at       time.Time     // 完成（或失败）时刻：状态脉冲与「完成时间」列都用它
}

// pushTaskRow 在队列表头部插入一条记录（内存 + 数据库，最多保留 maxTaskRows 条）。
//
// 耗时由调用方用 time.Since(start) 算好传进来——任务真实的执行时长只有发起方知道，
// 界面层不该去猜。
func (a *App) pushTaskRow(name, kind, status, detail string, dur time.Duration) {
	r := taskRow{
		name:     name,
		kind:     kind,
		status:   status,
		detail:   detail,
		duration: dur,
		at:       time.Now(),
	}
	a.tasks = append([]taskRow{r}, a.tasks...)
	if len(a.tasks) > maxTaskRows {
		a.tasks = a.tasks[:maxTaskRows]
	}
	*a.taskRowsVersion()++ // 行数可能没变（已经满 20 条），用版本号标记排序缓存脏
	a.persistTask(name, kind, status, detail, dur)
}

// taskPage 异步任务页：一句摘要 + 任务队列。
func (a *App) taskPage(c *ui.Context, t *ui.Theme) {
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "任务队列", a.taskSummary(), func() {
			if ui.Button(c, "清空已完成").Clicked() {
				a.clearFinishedTasks()
			}
		})
		a.taskTable(c, t)
	})
}

// taskSummary 队列摘要：条数 / 失败数 / 平均耗时 / 最近一次。
func (a *App) taskSummary() string {
	if len(a.tasks) == 0 {
		return "还没有任务记录：批量拉取、提交推送、自动刷新、导入都会记在这里。"
	}
	failed, timed := 0, 0
	var total time.Duration
	var last time.Time
	for _, r := range a.tasks {
		if r.status == "失败" {
			failed++
		}
		if r.duration > 0 {
			total += r.duration
			timed++
		}
		if r.at.After(last) {
			last = r.at
		}
	}
	s := fmt.Sprintf("共 %d 条 · 失败 %d", len(a.tasks), failed)
	if timed > 0 {
		s += " · 平均耗时 " + formatDuration(total/time.Duration(timed))
	}
	if !last.IsZero() {
		s += " · 最近 " + timeAgo(last)
	}
	return s
}

// clearFinishedTasks 清掉已完成的记录（内存 + 数据库），保留失败/排队/运行中的。
//
// 只清「已完成」：失败记录是排查线索，不该跟着一起被清掉。
func (a *App) clearFinishedTasks() {
	kept := a.tasks[:0]
	for _, r := range a.tasks {
		if r.status != "已完成" {
			kept = append(kept, r)
		}
	}
	a.tasks = kept
	*a.taskRowsVersion()++
	a.persistClearFinished()
	a.toast("已清空完成记录（失败的保留）")
}

// taskCols 任务队列的列定义（CrychicUI data.DataTable）。
var taskCols = []data.Column{
	{Title: "任务"},
	{Title: "类型", Width: 80},
	{Title: "状态", Width: 96},
	{Title: "耗时", Width: 96, End: true},
	{Title: "结果 / 失败原因", Width: 340},
	{Title: "完成时间", Width: 120, End: true},
}

// taskCellText 取某行某列的展示文案，供排序时比较。
func taskCellText(r taskRow, col int) string {
	switch col {
	case 0:
		return r.name
	case 1:
		return r.kind
	case 2:
		return r.status
	case 3:
		return formatDuration(r.duration)
	case 4:
		return r.detail
	default:
		return formatTaskTime(r.at)
	}
}

// taskTableCache 是任务表排序结果的缓存。放在页面状态里而不是 App 字段上：
// 这个页面文件被生成器整页删掉时，App 不该残留一个引用 taskRow 的字段，
// 而且销毁页面时缓存自然也释放。
type taskTableCache struct {
	rows []taskRow
	sort ui.SortOrder
	len  int
	ver  int // 行内容版本：push/clear 时 +1，覆盖「长度不变但内容变了」
}

func (a *App) taskTableCache() *taskTableCache {
	return pageData(a, "task", "tableCache", func() taskTableCache { return taskTableCache{} })
}

// taskRowsVersion 是任务列表的内容版本：push/clear 时 +1，让排序缓存知道要重排。
func (a *App) taskRowsVersion() *int {
	return pageData(a, "task", "rowsVer", func() int { return 0 })
}

// taskTable 任务队列表：用 CrychicUI 的 data.DataTable 渲染，主题自动继承自本页。
// 排序结果按「排序键 + 行数 + 行版本」缓存，避免每帧 clone+排序。
func (a *App) taskTable(c *ui.Context, t *ui.Theme) {
	cache := a.taskTableCache()
	if cache.rows == nil || cache.sort != a.taskSort ||
		cache.len != len(a.tasks) || cache.ver != *a.taskRowsVersion() {
		rows := slices.Clone(a.tasks)
		if a.taskSort.Column != "" {
			if col := slices.IndexFunc(taskCols, func(k data.Column) bool {
				return k.Title == a.taskSort.Column
			}); col >= 0 {
				slices.SortStableFunc(rows, func(x, y taskRow) int {
					if a.taskSort.Descending {
						x, y = y, x
					}
					return strings.Compare(taskCellText(x, col), taskCellText(y, col))
				})
			}
		}
		cache.rows = rows
		cache.sort = a.taskSort
		cache.len = len(a.tasks)
		cache.ver = *a.taskRowsVersion()
	}
	rows := cache.rows
	data.DataTable(c, "任务队列", taskCols, len(rows), &a.taskSort, func(row, col int) {
		r := rows[row]
		switch col {
		case 0:
			ui.Text(c, r.name).SingleLine()
		case 1:
			ui.Text(c, r.kind).SingleLine().TextColor(t.TextMuted)
		case 2:
			// 刚完成的那一行脉冲一下，1.1 秒后自然停止（见 kit.PillPulse）。
			kit.PillPulse(c, t, r.status, kit.StatusColor(t, r.status), r.at)
		case 3:
			ui.Text(c, taskDurationText(r)).SingleLine()
		case 4:
			detail := firstNonEmpty(r.detail, "—")
			txt := ui.Text(c, detail).SingleLine().FontSize(t.FontSize - 2)
			if r.status == "失败" {
				txt.TextColor(t.Danger)
			} else {
				txt.TextColor(t.TextMuted)
			}
			if r.detail != "" {
				txt.Tooltip(r.detail)
			}
		default:
			ui.Text(c, formatTaskTime(r.at)).SingleLine().FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		}
	}, data.DataTableOptions{Empty: "还没有任务记录"})
}

// taskDurationText 耗时展示：新记录用真实时长，旧记录退回历史文本。
func taskDurationText(r taskRow) string {
	if r.duration > 0 {
		return formatDuration(r.duration)
	}
	if strings.TrimSpace(r.costText) != "" {
		return r.costText
	}
	return "—"
}

// formatDuration 把实际耗时写成「820ms / 1.8s / 2m05s / 1h03m」。
func formatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "—"
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// formatTaskTime 完成时间列：月-日 时:分。
func formatTaskTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("01-02 15:04")
}
