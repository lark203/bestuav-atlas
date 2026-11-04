package view

import (
	"fmt"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/egoist/mygo/ui"

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

// checkRepoAutoRefresh 每帧检查一次是否到点；没到点就按剩余时间安排下一次唤醒。
//
// 和待办提醒一样，c.After 只在最后真正空闲时生效；有动画/悬停帧时每帧检查，
// 不会漏掉到点时刻。已有任务或读取列表进行中时跳过这一轮，避免叠任务。
func (a *App) checkRepoAutoRefresh(c *ui.Context) {
	// 离屏测试没有窗口，也没有真实的定时器需求。
	if a.win == nil || a.store == nil {
		return
	}
	if !a.settings.RepoAutoRefresh {
		a.repoAutoOn = false
		a.repoAutoAt = time.Time{}
		return
	}

	minutes := a.repoAutoIntervalMinutes()
	interval := time.Duration(minutes) * time.Minute
	now := time.Now()

	// 刚开启 / 刚改频率 / 第一次运行：从下一个完整间隔开始，不立刻联网。
	if !a.repoAutoOn || a.repoAutoMinutes != minutes || a.repoAutoAt.IsZero() {
		a.repoAutoOn = true
		a.repoAutoMinutes = minutes
		a.repoAutoAt = now.Add(interval)
		c.After(time.Until(a.repoAutoAt))
		return
	}
	if now.Before(a.repoAutoAt) {
		c.After(time.Until(a.repoAutoAt))
		return
	}

	// 到点：先排下一轮，再决定这一轮跑不跑（跑起来后 taskRunning 会拦住
	// 下一轮，不会叠两个自动刷新）。
	a.repoAutoAt = now.Add(interval)
	if !a.taskRunning && !a.heavyRunning && !a.atlasUI.busy {
		if ids := a.allRepoIDs(); len(ids) > 0 {
			a.runRepoBatch(batchAuto, ids, "")
		}
	}
	c.After(time.Until(a.repoAutoAt))
}
