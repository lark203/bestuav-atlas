package view

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/deploy"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/syncclient"
	"bestuav-atlas/internal/ui/kit"
)

// ---- 工具 → 同步服务 ----
//
// 为什么要有这一页：部署一次同步服务原本要手抄四条 SSH 参数、手动 go build、
// scp、再连上去 docker compose，换台机器就得重来。把这几步收进界面后，
// 「改完内核 → 点一下 → 健康检查通过」是一条可重复的路。
//
// 边界：
//   - 只做「部署/重启/看日志/查健康」，不做远端 shell（那会变成半个运维终端）；
//   - 私钥只读不写，口令保护的私钥不支持（Tunnel 用的部署密钥应无口令）；
//   - 远端 .env 只在缺失时生成，避免每次部署把管理员令牌冲掉。

// deployMaxLines 部署输出保留的行数：够看一次完整流程，又不会拖慢每帧渲染。
const deployMaxLines = 200

// deployLogf 追加一行部署输出（必须在主线程调用）。
func (a *App) deployLogf(format string, args ...any) {
	a.deployLines = append(a.deployLines, fmt.Sprintf(format, args...))
	if n := len(a.deployLines); n > deployMaxLines {
		a.deployLines = a.deployLines[n-deployMaxLines:]
	}
	a.requestFrame()
}

// deployLogFrom 从后台 goroutine 安全地写一行。
func (a *App) deployLogFrom(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	mygo.RunOnMain(func() { a.deployLogf("%s", line) })
}

// deployTarget 把设置里的字段组装成部署目标。
func (a *App) deployTarget() deploy.Target {
	port := a.settings.DeployPort
	if port <= 0 {
		port = 22
	}
	return deploy.Target{
		Host: strings.TrimSpace(a.settings.DeployHost),
		Port: port,
		User: strings.TrimSpace(a.settings.DeployUser),
		Key:  strings.TrimSpace(a.settings.DeployKey),
		Dir:  strings.TrimSpace(a.settings.DeployDir),
		Src:  strings.TrimSpace(a.settings.DeploySrc),
	}
}

// deployCheckService 查一次 /v1/health（走设置里的服务地址，支持 https）。
func (a *App) deployCheckService() {
	url := strings.TrimSpace(a.settings.SyncURL)
	if url == "" {
		a.toast("先填服务地址")
		return
	}
	client := syncclient.New(url, "", "")
	a.deployLogf("检查 %s/v1/health …", strings.TrimRight(url, "/"))
	go func() {
		defer logging.Recover("atlas-deploy-health")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		h, err := client.Health(ctx)
		if err != nil {
			a.deployLogFrom("✗ 健康检查失败：%v", err)
			return
		}
		a.deployLogFrom("✓ 服务在线：version=%s schema=%s protocol=%d devices=%d ops=%d",
			h.Version, h.Schema, h.Protocol, h.Devices, h.Ops)
	}()
}

// deployTestSSH 建连并打一条服务器概况，用来确认参数填对。
func (a *App) deployTestSSH() {
	t := a.deployTarget()
	if err := t.Validate(); err != nil {
		a.toast(err.Error())
		return
	}
	a.deployLogf("连接 %s@%s:%d …", t.User, t.Host, t.Port)
	go func() {
		defer logging.Recover("atlas-deploy-ssh")
		c, err := deploy.Dial(t)
		if err != nil {
			a.deployLogFrom("✗ %v", err)
			return
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := c.Run(ctx, "uname -srm; id -u; id -g; docker --version")
		if err != nil {
			a.deployLogFrom("✗ %v\n%s", err, out)
			return
		}
		a.deployLogFrom("✓ SSH 可用\n%s", strings.TrimSpace(out))
	}()
}

// deployPush 构建 → 上传 → 重建容器 → 健康检查。
func (a *App) deployPush() {
	if a.deployBusy {
		return
	}
	t := a.deployTarget()
	if err := t.Validate(); err != nil {
		a.toast(err.Error())
		return
	}
	a.deployBusy = true
	a.deployLines = nil
	a.deployLogf("目标：%s@%s:%d  →  %s", t.User, t.Host, t.Port, t.Dir)

	go func() {
		defer logging.Recover("atlas-deploy-push")
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()

		err := deploy.Push(ctx, t, func(format string, args ...any) {
			a.deployLogFrom(format, args...)
		})
		mygo.RunOnMain(func() {
			a.deployBusy = false
			if err != nil {
				a.deployLogf("✗ 部署失败：%v", err)
				a.toast("部署失败，看下方输出")
			} else {
				a.deployLogf("✓ 部署完成，服务已在服务器上运行")
				a.toast("部署完成")
			}
			a.requestFrame()
		})
	}()
}

// deployRestart 只重启容器（改了 compose/环境变量但二进制没变时用）。
func (a *App) deployRestart() {
	t := a.deployTarget()
	if err := t.Validate(); err != nil {
		a.toast(err.Error())
		return
	}
	a.deployLogf("重启 %s …", t.Dir)
	go func() {
		defer logging.Recover("atlas-deploy-restart")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		out, err := deploy.Restart(ctx, t)
		if err != nil {
			a.deployLogFrom("✗ %v\n%s", err, out)
			return
		}
		a.deployLogFrom("✓ %s", strings.TrimSpace(out))
	}()
}

// deployTailLogs 拉最近 40 行容器日志。
func (a *App) deployTailLogs() {
	t := a.deployTarget()
	if err := t.Validate(); err != nil {
		a.toast(err.Error())
		return
	}
	a.deployLogf("拉取日志 %s …", t.Dir)
	go func() {
		defer logging.Recover("atlas-deploy-logs")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := deploy.Logs(ctx, t, 40)
		if err != nil {
			a.deployLogFrom("✗ %v\n%s", err, out)
			return
		}
		if strings.TrimSpace(out) == "" {
			out = "（没有输出）"
		}
		a.deployLogFrom("---- 容器日志 ----\n%s", strings.TrimSpace(out))
	}()
}

// syncDeployPage 工具 → 同步服务。
func (a *App) syncDeployPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(16).Children(func() {
		kit.Section(c, t, "服务状态", func() {
			ui.Text(c, "这里查的是设置里那个服务地址，与下面要部署的目标是同一台机器。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)

			settingRow(c, t, "服务地址", func() {
				if ui.TextInput(c, &a.settings.SyncURL).Grow(1).
					Placeholder("https://sync.example.com").Changed() {
					a.persist()
				}
			})
			settingRow(c, t, "管理员令牌", func() {
				if ui.TextInput(c, &a.settings.SyncAdminToken).Password().Grow(1).
					Placeholder("服务器 .env 里的 ATLAS_SYNC_ADMIN_TOKEN").Changed() {
					a.persist()
				}
			})

			ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
				if ui.Button(c, "检查服务").Clicked() {
					a.deployCheckService()
				}
				if a.settings.SyncEnabled && a.settings.SyncToken != "" {
					a.syncStatusLine(c, t)
				} else {
					ui.Text(c, "尚未连接").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				}
			})
		})

		kit.Section(c, t, "部署目标（SSH）", func() {
			ui.Text(c, "一次点击完成：交叉编译 → 上传 → 写 compose → 重建容器 → 健康检查。"+
				"只支持私钥登录；远端 .env 不会被覆盖。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)

			settingRow(c, t, "主机", func() {
				if ui.TextInput(c, &a.settings.DeployHost).Grow(1).
					Placeholder("140.143.250.175").Changed() {
					a.persist()
				}
			})
			settingRow(c, t, "端口", func() {
				port := fmt.Sprintf("%d", a.settings.DeployPort)
				if a.settings.DeployPort <= 0 {
					port = ""
				}
				ui.TextInput(c, &port).Width(120).Placeholder("22").Changed()
				if n, err := strconv.Atoi(strings.TrimSpace(port)); err == nil {
					if a.settings.DeployPort != n {
						a.settings.DeployPort = n
						a.persist()
					}
				}
			})
			settingRow(c, t, "用户", func() {
				if ui.TextInput(c, &a.settings.DeployUser).Grow(1).
					Placeholder("wangss").Changed() {
					a.persist()
				}
			})
			settingRow(c, t, "私钥", func() {
				if ui.TextInput(c, &a.settings.DeployKey).Grow(1).
					Placeholder(`C:\Users\你\.ssh\id_rsa 或 ~/.ssh/id_rsa`).Changed() {
					a.persist()
				}
			})
			settingRow(c, t, "远端目录", func() {
				if ui.TextInput(c, &a.settings.DeployDir).Grow(1).
					Placeholder("~/atlas-sync").Changed() {
					a.persist()
				}
			})
			settingRow(c, t, "源码目录", func() {
				if ui.TextInput(c, &a.settings.DeploySrc).Grow(1).
					Placeholder(`D:\workbuddy\bestuav-atlas`).Changed() {
					a.persist()
				}
			})

			ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
				if ui.Button(c, "测试 SSH").Clicked() && !a.deployBusy {
					a.deployTestSSH()
				}
				if ui.Button(c, "构建并部署").Clicked() && !a.deployBusy {
					a.deployPush()
				}
				if ui.Button(c, "重启服务").Clicked() && !a.deployBusy {
					a.deployRestart()
				}
				if ui.Button(c, "看日志").Clicked() && !a.deployBusy {
					a.deployTailLogs()
				}
				if a.deployBusy {
					kit.BreathingDot(c, t.Accent, 7, kit.BreathePeriod)
					ui.Text(c, "处理中…").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				}
			})
		})

		kit.Section(c, t, "输出", func() {
			if len(a.deployLines) == 0 {
				ui.Text(c, "还没有操作记录。先点「测试 SSH」确认参数，再「构建并部署」。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				return
			}
			for _, line := range a.deployLines {
				ui.Text(c, line).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			}
		})
	})
}
