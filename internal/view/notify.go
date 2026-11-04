package view

import (
	"github.com/egoist/mygo"

	"bestuav-atlas/internal/logging"
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
