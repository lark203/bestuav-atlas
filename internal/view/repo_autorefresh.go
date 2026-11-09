package view

import (
	"fmt"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/settings"
)

// 本文件是仓库页的「自动刷新」：一个开关 + 频率选择，以及背后的定时器。
//
// 自动刷新走和手动批量完全相同的 worker pool（runRepoBatch + batchAuto）：
// 并发上限沿用「设置 → 常规 → 异步任务 → 最大并发」，单个仓库失败只记一条
// 失败并继续下一个，不会中断整批。

// repoRefreshLabels 频率分段控件的文案，顺序与 settings.RepoRefreshMinutes 一致。
func repoRefreshLabels() []string {
	labels := make([]string, len(settings.RepoRefreshMinutes))
	for i, m := range settings.RepoRefreshMinutes {
		labels[i] = repoRefreshLabel(m)
	}
	return labels
}

// repoRefreshLabel 把分钟数变成「10 分钟 / 1 小时 / 3 小时」。
func repoRefreshLabel(minutes int) string {
	switch minutes {
	case 60:
		return "1 小时"
	case 180:
		return "3 小时"
	default:
		return fmt.Sprintf("%d 分钟", minutes)
	}
}

// repoRefreshIndex 把存储的分钟数映射成分段控件索引；未知值用默认档。
func repoRefreshIndex(minutes int) int {
	for i, m := range settings.RepoRefreshMinutes {
		if m == minutes {
			return i
		}
	}
	for i, m := range settings.RepoRefreshMinutes {
		if m == settings.DefaultRepoAutoRefreshMinutes {
			return i
		}
	}
	return 0
}

// repoAutoIntervalMinutes 当前选中的间隔（分钟），索引越界时兜底到默认。
func (a *App) repoAutoIntervalMinutes() int {
	if a.repoRefreshIdx < 0 || a.repoRefreshIdx >= len(settings.RepoRefreshMinutes) {
		return settings.DefaultRepoAutoRefreshMinutes
	}
	return settings.RepoRefreshMinutes[a.repoRefreshIdx]
}

// repoAutoRefreshBar 仓库页标题下方的自动刷新控制条。
func (a *App) repoAutoRefreshBar(c *ui.Context, t *ui.Theme) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
		toggle := choice.Toggle(c, "自动刷新", &a.settings.RepoAutoRefresh)
		if toggle.Changed() {
			a.persist()
			// 让 checkRepoAutoRefresh 重新计算下一次：开启从下一个间隔开始，
			// 关闭则清掉计时。
			a.repoAutoOn = false
			a.repoAutoAt = time.Time{}
			if a.settings.RepoAutoRefresh {
				a.toast("自动刷新已开启：" + repoRefreshLabel(a.repoAutoIntervalMinutes()))
			} else {
				a.toast("自动刷新已关闭")
			}
		}

		if !a.settings.RepoAutoRefresh {
			ui.Text(c, "关闭时只在你点「刷新状态 / 刷新远端信息」时联网").
				FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			return
		}

		seg := ui.Segmented(c, &a.repoRefreshIdx, repoRefreshLabels()...)
		if seg.Changed() {
			a.settings.RepoAutoRefreshMinutes = a.repoAutoIntervalMinutes()
			a.repoAutoOn = false
			a.repoAutoAt = time.Time{}
			a.persist()
		}
		ui.Text(c, "每 "+repoRefreshLabel(a.repoAutoIntervalMinutes())+"刷新本地状态与远端信息").
			FontSize(t.FontSize - 2).TextColor(t.TextMuted)
	})
}

// checkRepoAutoRefresh 每帧兜底调度：界面可见时把下一次唤醒排进帧循环。
//
// 执行逻辑在 tickRepoAutoRefresh 里，与托盘计时器共用。
func (a *App) checkRepoAutoRefresh(c *ui.Context) {
	if d := a.tickRepoAutoRefresh(); d > 0 {
		c.After(d)
	}
}

// tickRepoAutoRefresh 在主线程检查一次：到点就跑一轮，返回建议的下次检查间隔。
//
// 有两个驱动源，共用 repoAutoAt：界面可见时由帧循环（c.After）驱动；窗口收进
// 托盘 / 最小化、View 不再被调用时，由 startRepoAutoLoop 的计时器驱动。先到者
// 把 repoAutoAt 推后，另一个看到时间未到就什么都不做——不会重复跑。
func (a *App) tickRepoAutoRefresh() time.Duration {
	if a.store == nil {
		return 0
	}
	if !a.settings.RepoAutoRefresh {
		a.repoAutoOn = false
		a.repoAutoAt = time.Time{}
		return 0
	}

	minutes := a.repoAutoIntervalMinutes()
	interval := time.Duration(minutes) * time.Minute
	now := time.Now()

	// 刚开启 / 刚改频率 / 第一次运行：从下一个完整间隔开始，不立刻联网。
	if !a.repoAutoOn || a.repoAutoMinutes != minutes || a.repoAutoAt.IsZero() {
		a.repoAutoOn = true
		a.repoAutoMinutes = minutes
		a.repoAutoAt = now.Add(interval)
		return interval
	}
	if now.Before(a.repoAutoAt) {
		return time.Until(a.repoAutoAt)
	}

	// 到点：先排下一轮，再决定这一轮跑不跑（跑起来后 taskRunning 会拦住
	// 下一轮，不会叠两个自动刷新）。
	a.repoAutoAt = now.Add(interval)
	if !a.taskRunning && !a.heavyRunning && !a.atlasUI.busy {
		if ids := a.allRepoIDs(); len(ids) > 0 {
			logging.Info("auto", "自动刷新触发", "repos", len(ids), "intervalMin", minutes)
			a.runRepoBatch(batchAuto, ids, "")
		} else {
			logging.Debug("auto", "自动刷新跳过：没有仓库")
		}
	} else {
		logging.Debug("auto", "自动刷新跳过：已有任务在跑")
	}
	return interval
}

// repoAutoTick 后台计时器的节拍：最短档位是 10 分钟，20 秒的检查粒度足够，
// 也不会让后台线程频繁唤醒主线程。
const repoAutoTick = 20 * time.Second

// startRepoAutoLoop 起一个与帧循环无关的计时器。
//
// 为什么需要它：窗口收进托盘或最小化后，View 不再被调用，c.After 排的唤醒
// 永远不会到来——自动刷新看起来「只有打开主页面才执行」。计时器只负责定时把
// 检查丢回主线程，其余判断仍由 tickRepoAutoRefresh 完成。
func (a *App) startRepoAutoLoop() {
	a.repoAutoLoop.Do(func() {
		go func() {
			defer logging.Recover("repo-auto-loop")
			t := time.NewTicker(repoAutoTick)
			defer t.Stop()
			for range t.C {
				mygo.RunOnMain(func() { a.tickRepoAutoRefresh() })
			}
		}()
	})
}
