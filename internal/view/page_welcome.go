package view

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/ui/kit"
)

// welcomePage 首页（欢迎页）。
//
// 为什么首页不是「数据概览」：本工程由 bestuav-atlas 脚手架生成，**新项目一开始
// 没有任何业务数据**，摆一屏假的 KPI 只会让人分不清哪些是真数据。首页改成
// 三张「下一步做什么」的卡片，指向真正要改的三个位置（菜单 / 品牌 / 发版）。
//
// 原来的运行概览页没有删，降级成「概览示例」分组里的一页（registry.go），
// 需要时勾选保留即可当 KPI 布局的参考。
func (a *App) welcomePage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(kit.PageGap).Children(func() {
		// ---- 头部 ----
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, greeting()+"，这里是 "+appmeta.Title).
				FontSize(24).Bold()
			ui.Text(c, "这是一个可以直接开工的桌面端工程：菜单、设置、主题、更新链路都已经接好，"+
				"你要做的是把下面三件事换成自己的。").
				TextColor(t.TextMuted)
			// 文案要与侧栏菜单「运行概览」区分开：同名会让「点哪个」变得含糊，
			// 自动化测试按下标取到的也可能是侧栏里滚出视口的那一项。
			ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(8, 0, 0, 0).Children(func() {
				// 「概览示例」属于可裁剪的分组：新项目里它可能已经不存在了。
				// 这里按注册表判断，而不是假设它一定在——否则按钮点了没反应。
				if _, ok := pageByID(overviewPageID); ok {
					if ui.Button(c, "打开概览示例").Clicked() {
						a.page = overviewPageID
					}
				}
				if ui.Button(c, "打开设置").Clicked() {
					// 用常量而不是 settingsNav 变量：设置项自身会经设置页间接引用本函数，
					// 引用变量会形成包级初始化环（见 registry.go 的 pageSettingItems 注释）。
					a.page = settingsPageID
				}
			})
		})

		// ---- 三步上手指引 ----
		kit.RowGrid(c, 3, func() {
			welcomeStep(c, t, "01", "加业务页面",
				"菜单与页面只在一张表里注册：internal/view/registry.go。",
				"写好页面函数，再登记一条 navItem，侧栏、页头标题、页面分发会一起生效。")

			welcomeStep(c, t, "02", "换品牌与标识",
				"品牌名称可以在这里直接改，改完侧栏与窗口标题立即生效。",
				"要改安装包名与内部标识，动 internal/appmeta/appmeta.go 与 mygo.json 后重新打包。")

			welcomeStep(c, t, "03", "发版与自动更新",
				"改 mygo.json 的 version，打一个同号 tag 推上去，CI 会产出三平台安装包。",
				"draft release 转正式后，已安装的客户端就能在设置页检查到更新。")
		})

		// ---- 常用入口 ----
		kit.Section(c, t, "常用入口", func() {
			ui.Text(c, "配置、数据库与日志的实际路径，见「设置 → 关于 → 基本信息」，"+
				"那一页同时提供「打开配置目录」按钮，不用去猜系统目录。").
				TextColor(t.TextMuted)
			ui.Text(c, appmeta.Name+" · v"+appmeta.BuildVersion()).
				FontSize(t.FontSize-2).TextColor(t.TextMuted).Margin(6, 0, 0, 0)
		})
	})
}

// welcomeStep 单张上手卡片：编号 + 标题 + 一句话说明 + 补充细节。
//
// 编号用强调色大字而不是图标：三步是有先后关系的动作，数字比图标更能传达顺序。
func welcomeStep(c *ui.Context, t *ui.Theme, no, title, desc, detail string) {
	kit.Card(c, t, func() {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, no).FontSize(20).Bold().TextColor(kit.Alpha(t.Accent, 0.85))
			ui.Text(c, title).Bold()
			ui.Text(c, desc).TextColor(t.TextMuted)
			ui.Text(c, detail).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		})
	})
}
