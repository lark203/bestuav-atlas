package view

import (
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/forge"
)

// 设置页的「工作区」区块：仓库页要用的本机参数与各平台令牌。
//
// 单独成文件而不是塞进 page_settings.go：设置页已经很长，而且这块与「外观/窗口」
// 这类纯偏好无关，属于业务配置，改它的机会和看它的机会都更多。
//
// 令牌必须每个平台一份：Gitee 的私人令牌访问不了 GitHub，AtomGit 更是独立账号体系。
// GitHub 复用「数据与诊断 → 仓库同步」那个 PAT（同一个令牌能做的事一样）。
//
// 这些输入框都要显式 a.persist()：设置页没有「保存」按钮，全局的写盘检测只覆盖
// 主题/字号那类 App 字段；直接绑到 settings 上的文本框如果不在 Changed 时落盘，
// 用户改完重启就丢了。
func (a *App) workspaceSection(c *ui.Context, t *ui.Theme) {
	ui.Text(c, "仓库本地根目录与各平台访问令牌。都是本机设置，不随配置包导出。").
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

	a.tokenRow(c, t, "GitHub 令牌", &a.settings.RepoToken, forgePlatforms[0],
		"PAT：read 仓库信息；与「仓库同步」共用")
	a.tokenRow(c, t, "Gitee 令牌", &a.settings.GiteeToken, forgePlatforms[1],
		"私人令牌：projects / pull_requests 只读即可")
	a.tokenRow(c, t, "AtomGit 令牌", &a.settings.AtomGitToken, forgePlatforms[3],
		"访问令牌：读仓库信息与 README")

	settingRow(c, t, "Gitea 实例", func() {
		if ui.TextInput(c, &a.settings.GiteaHost).Grow(1).
			Placeholder("http:// 或 https://gitea.example.com（以填写为准）").Changed() {
			a.persist()
		}
	})
	ui.Text(c, "自建 Gitea 常在内网 http 上：这里写 http:// 就全程用 http（网页、克隆、API），"+
		"写 https:// 就用 https。").
		TextColor(t.TextMuted).FontSize(t.FontSize - 2)
	a.tokenRow(c, t, "Gitea 令牌", &a.settings.GiteaToken, forgePlatforms[2],
		"访问令牌：read:repository")

	ui.Text(c, "令牌只在点按钮时使用（读取仓库列表 / 刷新远端信息 / 读 README）；"+
		"克隆与推送走本机 git 凭据，星图不保存 git 密码。").
		TextColor(t.TextMuted).FontSize(t.FontSize - 2).Margin(0, 0, 0, 4)

	settingRow(c, t, "GitHub 用户名", func() {
		if ui.TextInput(c, &a.settings.RepoOwner).Width(220).
			Placeholder("owner（留空则列当前令牌的仓库）").Changed() {
			a.persist()
		}
	})
}

// tokenRow 一行「平台令牌 + 测试」。
func (a *App) tokenRow(c *ui.Context, t *ui.Theme, label string, dst *string, base forge.Platform, hint string) {
	settingRow(c, t, label, func() {
		if ui.TextInput(c, dst).Password().Grow(1).Placeholder(hint).Changed() {
			a.persist()
		}
		if ui.Button(c, "测试").Clicked() && !a.atlasUI.busy {
			a.testPlatformToken(base)
		}
	})
}

// testPlatformToken 拉一次「我的仓库」列表，只报告连通性与数量（不改导入框）。
func (a *App) testPlatformToken(p forge.Platform) {
	if p.Provider == forge.Gitea {
		if h := hostOf(a.settings.GiteaHost); h != "" {
			p.Host = h
		} else {
			a.toast("先填 Gitea 实例地址")
			return
		}
		p.Scheme = a.giteaScheme()
	}
	a.listPlatformRepos(p, "", func(names []string, err error) {
		if err != nil {
			a.toast(p.Label()+" 不可用："+err.Error())
			return
		}
		a.toast(p.Label()+" 可用，读到 " + itoa(len(names)) + " 个仓库")
	})
}
