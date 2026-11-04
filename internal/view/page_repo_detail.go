package view

import (
	"context"
	"strings"
	"time"

	"github.com/ELY-Open-Source/CrychicUI/choice"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/readme"
	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/kit"
)

// 仓库详情页：把「这个仓库是什么、我给它打几分、它的 README 写了什么」放在一页。
//
// 单独成页（而不是弹窗）的理由：README 可能很长，需要真正的滚动与阅读空间；
// 评分/评价也是需要慢慢想的事，不该挤在一个 440 宽的对话框里。
//
// 页面 id 是 "repo-detail"，在注册表里标了 hidden——它不出现在侧栏，只能从
// 仓库行点「详情」进入，返回时回到仓库页。

// openRepoDetail 打开某个仓库的详情页，并把评分/评价表单填成当前值。
func (a *App) openRepoDetail(r store.Repo) {
	u := &a.atlasUI
	u.detailID = r.ID
	u.detailImport = clampRating(r.Importance)
	u.detailLearn = clampRating(r.Learning)
	u.detailDiff = clampRating(r.Difficulty)
	u.detailReview = r.Review
	u.readmeName = ""
	u.readmeText = ""
	u.readmeErr = ""
	u.readmeFrom = ""
	u.detailLoaded = false
	a.page = "repo-detail"
}

// clampRating 把库里的评分夹到 0–5：历史数据/手改库都可能越界，而 StarRating
// 对越界值会直接 panic，不能把这个风险带到界面上。
func clampRating(v int) int {
	if v < 0 {
		return 0
	}
	if v > 5 {
		return 5
	}
	return v
}

// repoDetailPage 仓库详情。
func (a *App) repoDetailPage(c *ui.Context, t *ui.Theme) {
	u := &a.atlasUI
	r, ok := a.repoByID(u.detailID)
	if !ok {
		kit.Card(c, t, func() {
			ui.Text(c, "这个仓库已经不在列表里了").Bold()
			if ui.Button(c, "返回仓库列表").Clicked() {
				a.page = "repos"
			}
		})
		return
	}
	if !u.detailLoaded {
		u.detailLoaded = true
		a.loadLocalReadme(r)
	}

	ui.Column(c).Gap(kit.PageGap).Children(func() {
		// ---- 标题行 ----
		ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
			if ui.Button(c, "← 返回").Clicked() {
				a.page = "repos"
			}
			ui.Text(c, r.Name).FontSize(22).Bold().SingleLine()
			kit.Pill(c, t, r.Status, repoStatusColor(t, r.Status))
			if name := a.atlas().projectName(r.ProjectID); name != "" {
				kit.Pill(c, t, name, t.Accent)
			}
			ui.Text(c, "").Grow(1)
			if ui.Button(c, "打开目录").Clicked() {
				if err := openLocalPath(r.LocalPath); err != nil {
					a.toast(err.Error())
				}
			}
			if r.URL != "" && ui.Button(c, "在 GitHub 打开").Clicked() {
				openExternalURL(r.URL)
			}
			if ui.Button(c, "刷新状态").Clicked() {
				a.refreshRepoStatus(r)
				a.reloadAtlas()
				a.toast("状态已刷新")
			}
		})

		// AlignItems(Start)：Row 默认把子项垂直居中，README 卡片会被推到页面中间
		// （视觉上像上面空了一大块）。两栏都应该从顶部开始。
		ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Start).Children(func() {
			ui.Column(c).Grow(2).Children(func() { a.readmeCard(c, t, r) })
			ui.Column(c).Grow(1).Gap(kit.PageGap).Children(func() {
				a.reviewCard(c, t, r)
				a.repoMetaCard(c, t, r)
			})
		})
	})
}

// readmeCard README 阅读区（只读）。
func (a *App) readmeCard(c *ui.Context, t *ui.Theme, r store.Repo) {
	u := &a.atlasUI
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "README",
			firstNonEmpty(u.readmeSource(), "本地仓库根目录里的 README，只读"),
			func() {
				if u.readmeFrom == "remote" || a.tokenFor(a.platformForRepo(r).Host) != "" {
					if ui.Button(c, "从远程读取").Clicked() && !u.busy {
						a.fetchRemoteReadme(r)
					}
				}
			})

		switch {
		case u.busy && u.readmeText == "":
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, "正在读取 README…").TextColor(t.TextMuted)
			})
		case u.readmeErr != "" && u.readmeText == "":
			ui.Text(c, u.readmeErr).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			ui.Row(c).Children(func() {
				if ui.Button(c, "从远程读取").Clicked() && !u.busy {
					a.fetchRemoteReadme(r)
				}
			})
		case u.readmeText == "":
			kit.EmptyHint(c, t, "这个仓库没有可读的 README。")
		default:
			markdown.View(c, u.readmeText, &markdown.Options{})
		}
	})
}

// reviewCard 评分与评价（可编辑）。
func (a *App) reviewCard(c *ui.Context, t *ui.Theme, r store.Repo) {
	u := &a.atlasUI
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "我的评价", "三个维度各自打分；评价原文存本机。", nil)

		star := func(label string, dst *int) {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, label).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
				choice.StarRating(c, label, dst, choice.StarRatingOptions{Max: 5})
			})
		}
		star("重要程度", &u.detailImport)
		star("可学习", &u.detailLearn)
		star("难度", &u.detailDiff)

		ui.Column(c).Gap(2).Children(func() {
			ui.Text(c, "评价").FontSize(t.FontSize - 2).TextColor(t.TextMuted)
			ui.TextArea(c, &u.detailReview).Height(120).
				Placeholder("记下这个仓库值得学什么、坑在哪、什么情况下会再用它…")
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			// 未保存提示：星星与评价都是「改完要记得存」的表单，不提示的话
			// 用户很容易关掉页面才发现没保存。
			if a.reviewDirty(r) {
				ui.Text(c, "有未保存的修改").FontSize(t.FontSize - 2).TextColor(t.Warning).Grow(1)
			} else {
				ui.Text(c, "评价会随「保存评价」写进本地库").FontSize(t.FontSize - 2).
					TextColor(t.TextMuted).Grow(1)
			}
			if ui.Button(c, "重置").Clicked() {
				u.detailImport = clampRating(r.Importance)
				u.detailLearn = clampRating(r.Learning)
				u.detailDiff = clampRating(r.Difficulty)
				u.detailReview = r.Review
			}
			if ui.PrimaryButton(c, "保存评价").Clicked() {
				if a.store == nil {
					a.toast("本次运行不落库，无法保存")
					return
				}
				if err := a.store.SaveRepoReview(r.ID, u.detailImport, u.detailLearn, u.detailDiff, u.detailReview); err != nil {
					a.toast("保存失败：" + err.Error())
					return
				}
				a.reloadAtlas()
				a.toast("评价已保存")
			}
		})
	})
}

// repoMetaCard 仓库的只读信息（远端地址、分支、最近提交、检查时间、说明）。
func (a *App) repoMetaCard(c *ui.Context, t *ui.Theme, r store.Repo) {
	kit.Card(c, t, func() {
		kit.CardHeader(c, t, "仓库信息", "", nil)
		p := a.platformForRepo(r)
		kit.KV(c, t, "平台", p.Label()+"（"+firstNonEmpty(p.Host, "未知")+"）")
		kit.KV(c, t, "远端", firstNonEmpty(r.URL, "—"))
		kit.KV(c, t, "本地目录", firstNonEmpty(r.LocalPath, "未设置"))
		kit.KV(c, t, "分支", firstNonEmpty(r.Branch, "—"))
		kit.KV(c, t, "最近提交", firstNonEmpty(r.LastCommit, "—"))
		kit.KV(c, t, "检查时间", timeAgo(r.CheckedAt))
		kit.KV(c, t, "差异", repoDiffText(r))
		if r.Note != "" {
			kit.KV(c, t, "备注", r.Note)
		}
		ui.Row(c).Gap(6).Children(func() {
			if ui.Button(c, "编辑登记").Clicked() {
				a.openRepoDialog(r)
			}
		})
	})
}

// reviewDirty 表单与库里的值是否不同。
func (a *App) reviewDirty(r store.Repo) bool {
	u := &a.atlasUI
	return u.detailImport != clampRating(r.Importance) ||
		u.detailLearn != clampRating(r.Learning) ||
		u.detailDiff != clampRating(r.Difficulty) ||
		u.detailReview != r.Review
}

// loadLocalReadme 读本地仓库根目录的 README（打开详情时调用一次）。
func (a *App) loadLocalReadme(r store.Repo) {
	u := &a.atlasUI
	u.readmeText, u.readmeName, u.readmeFrom = "", "", ""
	u.readmeErr = ""
	if strings.TrimSpace(r.LocalPath) == "" {
		u.readmeErr = "没有本地目录，读不到 README；可以从 GitHub 读取。"
		return
	}
	name, text, err := readme.ReadLocal(r.LocalPath)
	if err != nil {
		u.readmeErr = "本地没有找到 README；可以从 GitHub 读取。"
		return
	}
	u.readmeName = name
	u.readmeText = capReadme(text)
	u.readmeFrom = "local"
}

// fetchRemoteReadme 用平台 API 拉 README（手动触发）。
func (a *App) fetchRemoteReadme(r store.Repo) {
	p := a.platformForRepo(r)
	owner, repo, ok := forgeOwnerRepo(r, p)
	if !ok {
		a.toast("无法从地址解析出 owner/repo")
		return
	}
	// 客户端在主线程构造好再进协程：forge.Client 只读，协程里用它是安全的。
	client := a.forgeClient(p)
	u := &a.atlasUI
	u.busy = true
	go func() {
		defer logging.Recover("atlas-readme")
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		text, err := client.Readme(ctx, owner, repo)
		mygo.RunOnMain(func() {
			u.busy = false
			if err != nil {
				logging.Errv("atlas", "读取远端 README 失败", err, "repo", r.Name)
				u.readmeErr = "读取失败：" + err.Error()
			} else if strings.TrimSpace(text) == "" {
				u.readmeErr = "远端仓库没有 README"
			} else {
				u.readmeText = capReadme(text)
				u.readmeName = "README（GitHub）"
				u.readmeFrom = "remote"
				u.readmeErr = ""
			}
			a.requestFrame()
		})
	}()
}

// readmeSource README 的来源说明。
func (u *atlasUI) readmeSource() string {
	switch u.readmeFrom {
	case "local":
		return "本地 " + u.readmeName
	case "remote":
		return "来自远程仓库"
	default:
		return ""
	}
}

// capReadme 限制渲染长度：README 动辄上百 KB，全量渲染会让页面变成幻灯片。
func capReadme(text string) string {
	const limit = 40000
	rs := []rune(text)
	if len(rs) <= limit {
		return text
	}
	return string(rs[:limit]) + "\n\n---\n\n（README 过长，已截断显示前 4 万字符）"
}
