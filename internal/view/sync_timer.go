package view

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/syncclient"
)

// 本文件是「跨设备同步」的调度：什么时候跑、跑的时候状态怎么显示。
//
// 与仓库自动刷新同构（见 repo_autorefresh.go）：帧循环用 c.After 兜底，另有一个
// 与窗口生命周期无关的 ticker，保证收进托盘后也会同步。两者共用 syncNextAt，
// 谁先到谁把时刻推后，不会叠两轮。
//
// 同步本身跑在后台协程，只把结果交回主线程——界面状态一律在主线程改。

const (
	// syncEvery 自动同步的间隔。比仓库刷新（10 分钟起）密：待办是随手记的，
	// 用户期望「手机上勾完，回到电脑就变了」，30 秒一轮、每轮几百字节，代价可以忽略。
	syncEvery = 30 * time.Second

	// syncRoundTimeout 一轮同步的超时：推到一半卡住不能把下几轮也堵死。
	syncRoundTimeout = 30 * time.Second
)

// syncConfigured 是否具备同步条件（开关 + 地址 + 令牌都齐全）。
func (a *App) syncConfigured() bool {
	return a.store != nil &&
		a.settings.SyncEnabled &&
		a.settings.SyncURL != "" &&
		a.settings.SyncToken != ""
}

// checkSync 每帧兜底调度：界面可见时把下一次唤醒排进帧循环。
func (a *App) checkSync(c *ui.Context) {
	if d := a.tickSync(); d > 0 {
		c.After(d)
	}
}

// tickSync 检查一次：到了就跑一轮，返回建议的下次检查间隔。
func (a *App) tickSync() time.Duration {
	if !a.syncConfigured() {
		a.syncNextAt = time.Time{}
		return 0
	}
	// 关掉定时同步：不排下一轮，也不跑（手动「立即同步」走 syncNow，不受这里影响）。
	if a.settings.SyncAutoOff {
		a.syncNextAt = time.Time{}
		return 0
	}
	now := time.Now()
	if a.syncNextAt.IsZero() {
		// 刚启动 / 刚连上：先跑一轮，不让用户等满 30 秒。
		a.syncNextAt = now.Add(syncEvery)
		a.startSyncRound()
		return syncEvery
	}
	if now.Before(a.syncNextAt) {
		return time.Until(a.syncNextAt)
	}
	a.syncNextAt = now.Add(syncEvery)
	a.startSyncRound()
	return syncEvery
}

// startSyncLoop 起一个与帧循环无关的计时器（窗口最小化 / 收托盘时仍然同步）。
func (a *App) startSyncLoop() {
	a.syncLoop.Do(func() {
		go func() {
			defer logging.Recover("atlas-sync-loop")
			t := time.NewTicker(syncEvery)
			defer t.Stop()
			for range t.C {
				mygo.RunOnMain(func() { a.tickSync() })
			}
		}()
	})
}

// syncNow 用户手动点「立即同步」。
func (a *App) syncNow() {
	if !a.syncConfigured() {
		a.toast("先连接同步服务")
		return
	}
	if a.syncBusy {
		return
	}
	a.syncStatus = "正在同步…"
	a.startSyncRound()
}

// startSyncRound 把一轮同步丢到后台协程。
//
// 配置在启动前**拷一份**：后台线程不读 a.settings，用户此刻正在设置页改地址
// 也不会让这一轮读到半新半旧的组合。
func (a *App) startSyncRound() {
	if a.syncBusy || !a.syncConfigured() {
		return
	}
	client := syncclient.New(a.settings.SyncURL, a.settings.SyncDeviceID, a.settings.SyncToken)
	core := a.store
	a.syncBusy = true
	a.requestFrame()

	go func() {
		defer logging.Recover("atlas-sync-round")
		ctx, cancel := context.WithTimeout(context.Background(), syncRoundTimeout)
		defer cancel()
		res, err := syncclient.PushPull(ctx, core, client, 500)
		mygo.RunOnMain(func() {
			a.syncBusy = false
			a.finishSyncRound(res, err)
		})
	}()
}

// finishSyncRound 在主线程收尾：更新状态文本、必要时刷新页面。
func (a *App) finishSyncRound(res syncclient.RoundResult, err error) {
	if err != nil {
		a.syncStatus = "同步失败：" + err.Error()
		// 同一句错误只提示一次：断网时每 30 秒弹一次会把人烦死，但状态文本要一直留着。
		if err.Error() != a.syncLastErr {
			a.syncLastErr = err.Error()
			a.toast("同步失败：" + err.Error())
			logging.Errv("sync", "同步失败", err)
		}
		a.requestFrame()
		return
	}

	a.syncLastErr = ""
	a.settings.SyncLastAt = time.Now().Format("01-02 15:04")
	if res.Applied > 0 {
		a.syncStatus = fmt.Sprintf("已同步：上传 %d 条，应用 %d 条", res.Pushed, res.Applied)
	} else {
		a.syncStatus = "已同步：没有新变更"
	}
	a.persist()
	if res.Applied > 0 {
		// 远端变更已经落库，让页面下一帧重读（缓存见 atlasCache）。
		a.reloadAtlas()
		a.refreshDBStats()
	}
	logging.Info("sync", "同步完成",
		"pushed", res.Pushed, "applied", res.Applied, "skipped", res.Skipped,
		"seq", res.Seq, "ms", res.Elapsed.Milliseconds())
	a.requestFrame()
}
