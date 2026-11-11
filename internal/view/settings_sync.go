package view

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/syncclient"
	"bestuav-atlas/internal/syncwire"
	"bestuav-atlas/internal/ui/kit"
)

// 设置页「同步」分组：连服务、看状态、配手机、断开。
//
// 三件事刻意分开（三种凭据，权限不同）：
//   - 管理员令牌：只在「连接服务」和「生成配对码」时用，不参与日常同步；
//   - 设备令牌：连接时换到的，日常同步就用它；
//   - 配对码：8 位、5 分钟、一次性，给手机换设备令牌。
//
// 为什么不在设置里放「手动填令牌」：填令牌等于让用户去翻服务端日志/数据库；
// 扫码或配对码只有一步，出错也只会错在服务端那一侧。

// syncSection 「同步」分组的全部内容。
func (a *App) syncSection(c *ui.Context, t *ui.Theme) {
	kit.Section(c, t, "同步服务", func() {
		ui.Text(c, "把待办与项目同步到你自己服务器上的 atlas-sync。"+
			"只同步待办与项目；仓库、笔记、便签仍只在本机。").
			TextColor(t.TextMuted).FontSize(t.FontSize - 2)

		settingRow(c, t, "服务地址", func() {
			if ui.TextInput(c, &a.settings.SyncURL).Grow(1).
				Placeholder("http://127.0.0.1:7420 或 https://sync.example.com").
				Changed() {
				a.persist()
			}
		})
		settingRow(c, t, "管理员令牌", func() {
			if ui.TextInput(c, &a.settings.SyncAdminToken).Password().Grow(1).
				Placeholder("服务端启动时的 ATLAS_SYNC_ADMIN_TOKEN").
				Changed() {
				a.persist()
			}
		})
		ui.Text(c, "管理员令牌只用来连接服务与生成配对码；日常同步用连接时换到的设备令牌。"+
			"两组令牌都只存本机，不随配置包导出。").
			TextColor(t.TextMuted).FontSize(t.FontSize - 2)

		connected := a.settings.SyncEnabled && a.settings.SyncToken != ""
		ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
			a.syncStatusLine(c, t)
			if !connected {
				if ui.Button(c, "连接").Clicked() && !a.syncBusy {
					a.syncConnect()
				}
				return
			}
			if ui.Button(c, "立即同步").Clicked() && !a.syncBusy {
				a.syncNow()
			}
			if ui.Button(c, "生成配对码").Clicked() && !a.syncBusy {
				a.syncGenerateCode()
			}
			if ui.Button(c, "断开").Clicked() {
				a.syncDisconnect()
			}
		})

		if connected {
			ui.Text(c, "跨设备只认 uid：同一件事在第两台机器上是同一条待办，不会变成两条。"+
				"手机端在「今日」页勾完成，这里 30 秒内能看到。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		}
	})

	kit.Section(c, t, "手机配对", func() {
		if a.syncCode != "" && time.Now().Before(a.syncCodeExpire) {
			ui.Text(c, "在手机上输入下面这串（服务地址与配对码）：").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			ui.Text(c, a.syncCode).FontSize(t.FontSize + 8).Bold().TextColor(t.Accent)
			ui.Text(c, a.settings.SyncURL).
				FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if ui.Button(c, "复制配对串").Clicked() {
					mygo.Clipboard.WriteText(a.syncPairText)
					a.toast("已复制：" + a.syncPairText)
				}
				ui.Text(c, fmt.Sprintf("有效期到 %s（一次性）",
					a.syncCodeExpire.Format("15:04:05"))).
					FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			})
			return
		}
		ui.Text(c, "点上面的「生成配对码」，手机用它换一台设备的令牌。"+
			"码 5 分钟内有效、只能用一次。").
			TextColor(t.TextMuted).FontSize(t.FontSize - 2)
	})
}

// syncStatusLine 状态文本：未连接 / 同步中 / 成功 / 失败。
func (a *App) syncStatusLine(c *ui.Context, t *ui.Theme) {
	switch {
	case a.syncBusy:
		kit.BreathingDot(c, t.Accent, 7, kit.BreathePeriod)
		ui.Text(c, "同步中…").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
	case strings.HasPrefix(a.syncStatus, "同步失败"):
		ui.Text(c, "● "+a.syncStatus).TextColor(t.Danger).FontSize(t.FontSize - 2)
	case a.settings.SyncEnabled && a.settings.SyncToken != "":
		ui.Text(c, "● "+a.syncStatusOrLast()).TextColor(t.Success).FontSize(t.FontSize - 2)
	default:
		ui.Text(c, "未连接").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
	}
}

// syncStatusOrLast 没有本轮状态时退回「上次同步时间」。
func (a *App) syncStatusOrLast() string {
	if a.syncStatus != "" {
		return a.syncStatus
	}
	if a.settings.SyncLastAt != "" {
		return "上次同步 " + a.settings.SyncLastAt
	}
	return "已连接"
}

// syncConnect 用管理员令牌登记本机为设备，成功后立刻跑一轮。
func (a *App) syncConnect() {
	url := strings.TrimSpace(a.settings.SyncURL)
	admin := strings.TrimSpace(a.settings.SyncAdminToken)
	if url == "" || admin == "" {
		a.toast("先填服务地址与管理员令牌")
		return
	}
	client := syncclient.New(url, "", "")
	name := desktopDeviceName()

	a.syncBusy = true
	a.syncStatus = "正在连接…"
	go func() {
		defer logging.Recover("atlas-sync-connect")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		health, err := client.Health(ctx)
		if err == nil && health.Protocol != syncwire.ProtocolVersion {
			err = fmt.Errorf("服务端协议 v%d 与本机 v%d 不一致，请升级一端",
				health.Protocol, syncwire.ProtocolVersion)
		}
		var pair syncwire.PairResponse
		if err == nil {
			pair, err = client.AdminCreateDevice(ctx, admin, name)
		}
		mygo.RunOnMain(func() {
			a.syncBusy = false
			if err != nil {
				a.syncStatus = "同步失败：" + err.Error()
				a.syncLastErr = err.Error()
				a.toast("连接失败：" + err.Error())
				a.requestFrame()
				return
			}
			a.settings.SyncURL = url
			a.settings.SyncDeviceID = pair.DeviceID
			a.settings.SyncToken = pair.Token
			a.settings.SyncEnabled = true
			a.syncLastErr = ""
			a.syncStatus = "已连接：" + pair.DeviceID
			a.syncNextAt = time.Time{} // 立刻跑一轮
			a.persist()
			a.toast("已连接同步服务")
			a.startSyncRound()
		})
	}()
}

// syncGenerateCode 换一个 8 位配对码给手机用。
func (a *App) syncGenerateCode() {
	if !a.syncConfigured() {
		a.toast("先连接同步服务")
		return
	}
	client := syncclient.New(a.settings.SyncURL, a.settings.SyncDeviceID, a.settings.SyncToken)
	admin := a.settings.SyncAdminToken
	if admin == "" {
		a.toast("生成配对码需要管理员令牌")
		return
	}

	a.syncBusy = true
	go func() {
		defer logging.Recover("atlas-sync-pair")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		code, expires, err := client.AdminPairCode(ctx, admin)
		mygo.RunOnMain(func() {
			a.syncBusy = false
			if err != nil {
				a.toast("生成配对码失败：" + err.Error())
				a.requestFrame()
				return
			}
			a.syncCode = code
			a.syncCodeExpire = expires
			a.syncPairText = fmt.Sprintf("atlas://pair?server=%s&code=%s",
				a.settings.SyncURL, code)
			a.toast("配对码：" + code)
			a.requestFrame()
		})
	}()
}

// syncDisconnect 断开：清设备令牌与设备 id，保留地址与管理员令牌（便于重连）。
func (a *App) syncDisconnect() {
	a.settings.SyncEnabled = false
	a.settings.SyncToken = ""
	a.settings.SyncDeviceID = ""
	a.syncNextAt = time.Time{}
	a.syncCode = ""
	a.syncPairText = ""
	a.syncStatus = "已断开"
	a.persist()
	a.toast("已断开同步服务")
}

// desktopDeviceName 给服务端看的设备名：主机名 · 桌面。
func desktopDeviceName() string {
	if h, err := os.Hostname(); err == nil && strings.TrimSpace(h) != "" {
		return h + " · 桌面"
	}
	return "桌面"
}
