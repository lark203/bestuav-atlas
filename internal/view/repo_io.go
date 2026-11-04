package view

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/profile"
	"bestuav-atlas/internal/repo"
	"bestuav-atlas/internal/store"
)

// 仓库同步（手动上传 / 下载）：GitHub / Gitea / Gitee 共用 Contents API。
//
// 约束：
//   - 只有用户点按钮才联网，没有定时器、没有自动上传；
//   - 令牌只存本机 settings.json，导出配置包时被剔除；
//   - 下载先落到临时文件、校验 manifest，再走已有的「导入并重启」流程。

// repoKindIndex 平台在 repo.Kinds 里的下标。
func repoKindIndex(kind string) int {
	for i, k := range repo.Kinds {
		if string(k) == kind {
			return i
		}
	}
	return 0
}

// repoDefaultPath 默认文件路径：每台设备一个文件，避免互相覆盖。
func repoDefaultPath() string {
	return "profiles/" + profile.DeviceName() + profile.FileExt
}

// repoConfig 按当前设置拼出客户端配置。
func (a *App) repoConfig() repo.Config {
	path := a.settings.RepoPath
	if path == "" {
		path = repoDefaultPath()
	}
	return repo.Config{
		Kind:    repo.Kind(a.settings.RepoKind),
		BaseURL: a.settings.RepoBaseURL,
		Token:   a.settings.RepoToken,
		Owner:   a.settings.RepoOwner,
		Repo:    a.settings.RepoRepo,
		Branch:  a.settings.RepoBranch,
		Path:    path,
	}
}

// repoClient 校验并构造客户端（顺手把表单写入设置）。
func (a *App) repoClient() (*repo.Client, error) {
	c := a.repoConfig()
	client, err := repo.New(c)
	if err != nil {
		return nil, err
	}
	a.settings.RepoKind = string(c.Kind)
	a.settings.RepoPath = c.Path
	a.settings.RepoBranch = c.Branch
	a.persist()
	return client, nil
}

// repoTest 测试连接（后台）。
func (a *App) repoTest() {
	if a.repoBusy {
		return
	}
	client, err := a.repoClient()
	if err != nil {
		a.toast(err.Error())
		return
	}
	a.repoBusy = true
	go func() {
		defer logging.Recover("repo-test")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.Test(ctx)
		mygo.RunOnMain(func() {
			a.repoBusy = false
			if err != nil {
				logging.Errv("repo", "仓库连接测试失败", err)
				a.toast("连接失败：" + err.Error())
			} else {
				a.toast("仓库连接正常")
			}
			a.requestFrame()
		})
	}()
}

// repoUpload 导出当前配置与数据，手动上传到仓库。
func (a *App) repoUpload() {
	if a.repoBusy {
		return
	}
	client, err := a.repoClient()
	if err != nil {
		a.toast(err.Error())
		return
	}
	settingsPath, dbPath, _ := a.profilePaths()
	bg := a.settings.BackgroundImage
	a.repoBusy = true
	go func() {
		defer logging.Recover("repo-upload")
		tmp, err := os.CreateTemp("", "mygo-repo-*.zip")
		if err != nil {
			mygo.RunOnMain(func() { a.repoBusy = false; a.toast("导出失败：" + err.Error()); a.requestFrame() })
			return
		}
		tmpPath := tmp.Name()
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		defer os.Remove(tmpPath)
		res, err := profile.Export(profile.ExportOptions{
			Out:           tmpPath,
			SettingsPath:  settingsPath,
			DBPath:        dbPath,
			Background:    bg,
			IncludeData:   true,
			AppID:         appmeta.Identifier,
			AppVersion:    appmeta.BuildVersion(),
			SchemaVersion: store.SchemaVersion(),
		})
		if err != nil {
			mygo.RunOnMain(func() { a.repoBusy = false; a.toast("导出失败：" + err.Error()); a.requestFrame() })
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		data, rerr := os.ReadFile(tmpPath)
		if rerr != nil {
			mygo.RunOnMain(func() { a.repoBusy = false; a.toast("读取临时包失败：" + rerr.Error()); a.requestFrame() })
			return
		}
		up, err := client.Upload(ctx, data, "sync from "+profile.DeviceName())
		mygo.RunOnMain(func() {
			a.repoBusy = false
			if err != nil {
				logging.Errv("repo", "上传配置失败", err)
				a.toast("上传失败：" + err.Error())
			} else {
				suffix := ""
				if up.Commit != "" {
					sha := up.Commit
					if len(sha) > 7 {
						sha = sha[:7]
					}
					suffix = " · " + sha
				}
				a.settings.RepoLast = fmt.Sprintf("%s 上传成功%s（%s）",
					time.Now().Format("01-02 15:04"), suffix, formatBytes(res.Bytes))
				a.persist()
				logging.Audit("repo", "上传配置包", "path", client.Path(), "commit", up.Commit)
				a.toast("已上传到仓库：" + client.Path())
			}
			a.requestFrame()
		})
	}()
}

// repoDownload 从仓库下载，落到临时文件并转交导入流程。
func (a *App) repoDownload() {
	if a.repoBusy {
		return
	}
	client, err := a.repoClient()
	if err != nil {
		a.toast(err.Error())
		return
	}
	a.repoBusy = true
	go func() {
		defer logging.Recover("repo-download")
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		data, err := client.Download(ctx)
		if err != nil {
			mygo.RunOnMain(func() { a.repoBusy = false; a.toast("下载失败：" + err.Error()); a.requestFrame() })
			return
		}
		f, err := os.CreateTemp("", "mygo-repo-download-*.zip")
		if err != nil {
			mygo.RunOnMain(func() { a.repoBusy = false; a.toast("写入临时文件失败：" + err.Error()); a.requestFrame() })
			return
		}
		path := f.Name()
		_, werr := f.Write(data)
		_ = f.Close()
		if werr != nil {
			_ = os.Remove(path)
			mygo.RunOnMain(func() { a.repoBusy = false; a.toast("写入临时文件失败：" + werr.Error()); a.requestFrame() })
			return
		}
		m, ierr := profile.Inspect(path)
		if ierr != nil {
			_ = os.Remove(path)
			mygo.RunOnMain(func() {
				a.repoBusy = false
				a.toast("下载的文件不是配置包：" + ierr.Error())
				a.requestFrame()
			})
			return
		}
		mygo.RunOnMain(func() {
			a.repoBusy = false
			a.settings.RepoLast = fmt.Sprintf("%s 下载 %s（%s）",
				time.Now().Format("01-02 15:04"), m.Device, formatBytes(int64(len(data))))
			a.persist()
			a.pendingImport = path
			a.pendingImportTemp = true
			a.toast("已下载配置包，正在导入…")
			a.requestFrame()
		})
	}()
}

// repoSection 设置页「仓库同步」区块。
func (a *App) repoSection(c *ui.Context, t *ui.Theme) {
	ui.Text(c, "仓库同步").Bold().Margin(8, 0, 0, 0)
	ui.Text(c, "手动上传 / 下载配置包到自己的私有仓库（GitHub / Gitea / Gitee）。"+
		"令牌只存本机，不随配置包导出；不会自动上传。").
		TextColor(t.TextMuted).FontSize(t.FontSize - 2)

	kindIdx := repoKindIndex(a.settings.RepoKind)
	repoField(c, t, "平台", func() {
		labels := make([]string, len(repo.Kinds))
		for i, k := range repo.Kinds {
			labels[i] = repo.KindLabel(k)
		}
		ui.Segmented(c, &kindIdx, labels...)
	})
	if got := string(repo.Kinds[kindIdx]); got != a.settings.RepoKind {
		a.settings.RepoKind = got
		a.persist()
	}

	repoField(c, t, "实例地址", func() {
		ui.TextInput(c, &a.settings.RepoBaseURL).
			Placeholder("Gitea 必填，如 https://gitea.example.com（GitHub/Gitee 可留空）").
			Grow(1)
	})
	repoField(c, t, "所有者 / 仓库", func() {
		ui.TextInput(c, &a.settings.RepoOwner).Placeholder("owner").Width(150)
		ui.TextInput(c, &a.settings.RepoRepo).Placeholder("repo").Width(150)
	})
	repoField(c, t, "分支 / 路径", func() {
		ui.TextInput(c, &a.settings.RepoBranch).Placeholder("main").Width(110)
		ui.TextInput(c, &a.settings.RepoPath).
			Placeholder(repoDefaultPath()).Grow(1)
	})
	repoField(c, t, "访问令牌", func() {
		ui.TextInput(c, &a.settings.RepoToken).Password().
			Placeholder("PAT：只授权该仓库的 contents 读写").Grow(1)
	})

	ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
		if ui.Button(c, "测试连接").Clicked() && !a.repoBusy {
			a.repoTest()
		}
		if ui.Button(c, "上传当前配置").Clicked() && !a.repoBusy {
			a.repoUpload()
		}
		if ui.Button(c, "从仓库下载并导入").Clicked() && !a.repoBusy {
			a.repoDownload()
		}
		if a.repoBusy {
			ui.Text(c, "处理中…").TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		}
	})
	if a.settings.RepoLast != "" {
		ui.Text(c, "最近："+a.settings.RepoLast).
			TextColor(t.TextMuted).FontSize(t.FontSize - 2)
	}
}

// repoField 一行「标签 + 控件」，标签宽度与设置页其它行一致。
func repoField(c *ui.Context, t *ui.Theme, label string, body func()) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Width(90).Shrink(0).TextColor(t.TextMuted)
		body()
	})
}
