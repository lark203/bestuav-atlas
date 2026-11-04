package view

import (
	"github.com/egoist/mygo/ui"
)

// 设置页的「工作区」区块：仓库页要用的三个本机参数。
//
// 单独成文件而不是塞进 page_settings.go：设置页已经很长，而且这块与「外观/窗口」
// 这类纯偏好无关，属于业务配置，改它的机会和看它的机会都更多。
//
// 这三个输入框都要显式 a.persist()：设置页没有「保存」按钮，全局的写盘检测只覆盖
// 主题/字号那类 App 字段；直接绑到 settings 上的文本框如果不在 Changed 时落盘，
// 用户改完重启就丢了。
func (a *App) workspaceSection(c *ui.Context, t *ui.Theme) {
	ui.Text(c, "仓库本地根目录与 GitHub 访问令牌。都是本机设置，不随配置包导出。").
		TextColor(t.TextMuted).FontSize(t.FontSize - 2)

	settingRow(c, t, "仓库根目录", func() {
		if ui.TextInput(c, &a.settings.WorkRoot).Grow(1).
			Placeholder(a.workRoot()).Changed() {
			a.persist()
		}
		if ui.Button(c, "打开").Clicked() {
			if err := openLocalPath(a.workRoot()); err != nil {
				a.toast(err.Error())
			}
		}
	})
	ui.Text(c, "导入仓库时默认放到「根目录/owner-repo」；留空则用上面的灰字路径。").
		TextColor(t.TextMuted).FontSize(t.FontSize - 2).Margin(0, 0, 0, 4)

	settingRow(c, t, "GitHub 令牌", func() {
		if ui.TextInput(c, &a.settings.RepoToken).Password().Grow(1).
			Placeholder("PAT：read 仓库元信息；公开仓库只读也可以").Changed() {
			a.persist()
		}
	})
	ui.Text(c, "令牌用于「从 GitHub 读取我的仓库」与「刷新远端信息」，只在点按钮时联网；"+
		"与「数据与诊断 → 仓库同步」共用同一个令牌。").
		TextColor(t.TextMuted).FontSize(t.FontSize - 2).Margin(0, 0, 0, 4)

	settingRow(c, t, "GitHub 用户名", func() {
		if ui.TextInput(c, &a.settings.RepoOwner).Width(220).
			Placeholder("owner（留空则列当前令牌的仓库）").Changed() {
			a.persist()
		}
	})

	ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(4, 0, 0, 0).Children(func() {
		if ui.Button(c, "测试令牌").Clicked() && !a.atlasUI.busy {
			a.testGitHubToken()
		}
		if a.atlasUI.busy {
			ui.Spinner(c)
			ui.Text(c, "正在请求 GitHub…").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		}
	})
}

// testGitHubToken 拉一次仓库列表，只报告连通性与数量（不改导入框）。
func (a *App) testGitHubToken() {
	a.fetchGitHubRepos(func(names []string, err error) {
		if err != nil {
			a.toast("令牌不可用：" + err.Error())
			return
		}
		a.toast("令牌可用，读到 " + itoa(len(names)) + " 个仓库")
	})
}
