package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/store"
)

// notifyTaskDone 在窗口不可见时发一条系统通知。
//
// 场景：应用被「最小化到托盘」（用户设置的关闭行为）后跑完一个任务——
// 界面里撒的彩带、状态列的脉冲全都看不见，用户完全不知道任务已经结束。
// 窗口可见时不发：桌面通知会和界面里的庆祝效果重复，反而吵。
//
// 平台支持由 mygo 的 NotificationsSupported 判定（Linux 缺通知服务时会退化），
// 失败只记日志，不影响任务本身。
func (a *App) notifyTaskDone(label string) {
	if a.win == nil || a.win.IsVisible() {
		return
	}
	if !mygo.NotificationsSupported() {
		return
	}
	n := mygo.NewNotification(mygo.NotificationOptions{
		// 固定 ID：后一个任务完成会替换前一条，不会堆一屏通知。
		ID:    "task-done",
		Title: "任务完成",
		Body:  label,
	})
	if err := n.Show(); err != nil {
		logging.Warn("app", "发送任务完成通知失败", "err", err.Error())
	}
}

// todoReminderInterval 没有更近的截止时间时，多久检查一次待办。
//
// 30 秒足够让「18:00 到期」在 18:00 后不久弹出来，也不会让空闲界面
// 每一秒醒一次。
const todoReminderInterval = 30 * time.Second

// checkTodoDue 检查已到期的待办并弹系统通知，然后安排下一次唤醒。
//
// 只负责「到期」这一件事：已完成、没设置截止时间、已经提醒过的都跳过。
// NotifiedAt 存在数据库里，所以重启后不会把同一条待办再提醒一遍；用户改
// 截止时间时 store.UpdateTodo 会清空它，新时刻还能再提醒。
//
// 为什么一次到期多条时只发一条汇总：应用关了一晚再打开，可能同时有十几条
// 逾期待办；逐条弹会把通知中心刷屏。单条到期仍是单条通知，点击后回到待办页。
func (a *App) checkTodoDue(c *ui.Context) {
	// 离屏测试 / 单测没有窗口，也没有真实的系统通知；跳过副作用。
	if a.win == nil || a.store == nil {
		return
	}
	now := time.Now()
	d := a.atlas()

	due := dueTodos(d.todos, now)
	if len(due) > 0 {
		for _, td := range due {
			if err := a.store.MarkTodoNotified(td.ID, now); err != nil {
				logging.Errv("atlas", "标记待办已提醒失败", err, "todo", td.Title)
			}
		}
		a.reloadAtlas()
		a.notifyTodosDue(due)
	}

	// 下一次检查：普通 30 秒；如果有更近的未来截止时间，就精确在那个点醒来。
	next := now.Add(todoReminderInterval)
	for _, td := range d.todos {
		if td.Status == TodoDone || td.DueAt.IsZero() || !td.DueAt.After(now) {
			continue
		}
		if td.DueAt.Before(next) {
			next = td.DueAt
		}
	}
	c.After(time.Until(next))
}

// dueTodos 挑出「已到期、还没提醒、也没完成」的待办。
//
// 单独拆成纯函数，测试不需要窗口/数据库，也能把边界（刚好到点、已完成、
// 已提醒、无期限、未来）一次覆盖。
func dueTodos(todos []store.Todo, now time.Time) []store.Todo {
	var due []store.Todo
	for _, td := range todos {
		if td.Status == TodoDone || td.DueAt.IsZero() || !td.NotifiedAt.IsZero() {
			continue
		}
		// 包含「正好等于 now」：到期这一刻就该提醒。
		if !td.DueAt.After(now) {
			due = append(due, td)
		}
	}
	return due
}

// notifyTodosDue 把一批到期待办变成 1 条（单条时）或 1 条汇总通知。
func (a *App) notifyTodosDue(due []store.Todo) {
	if len(due) == 0 || !mygo.NotificationsSupported() {
		return
	}
	opts := mygo.NotificationOptions{
		Title: "待办到期",
		Group: "todo-due",
	}
	if len(due) == 1 {
		td := due[0]
		opts.ID = fmt.Sprintf("todo-due-%d", td.ID)
		opts.Body = td.Title + "（截止 " + td.DueAt.Format("01-02 15:04") + "）"
	} else {
		opts.ID = "todo-due-summary"
		names := make([]string, 0, 3)
		for i, td := range due {
			if i >= 3 {
				break
			}
			names = append(names, td.Title)
		}
		body := fmt.Sprintf("有 %d 条待办已到期：%s", len(due), strings.Join(names, "、"))
		if len(due) > len(names) {
			body += " 等"
		}
		opts.Body = body
	}
	n := mygo.NewNotification(opts)
	// macOS 首次通知会等用户授权；放到后台，避免把界面线程卡住。
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := n.ShowContext(ctx); err != nil {
			logging.Warn("atlas", "发送待办到期通知失败", "err", err.Error())
		}
	}()
}
