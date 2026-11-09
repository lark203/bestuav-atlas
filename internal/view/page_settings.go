package view

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/ui/kit"
)

// settingsPage 设置页：分区域组织用户偏好。
//   - 外观：主题（跟随系统/浅色/深色）、字体大小
//   - 背景：背景图片（选择/恢复默认）、菜单区与内容区的不透明度
//   - 窗口与关闭：关闭按钮行为（每次询问/最小化到托盘/直接退出）
//   - 启动：是否允许多开（单实例 / 多实例）
//   - 关于：应用信息与配置文件路径
//
// 注意：本函数只负责渲染；偏好变更的检测与持久化统一在 App.View 中完成
// （通过对比各绑定值与其“已应用”快照）。
func (a *App) settingsPage(c *ui.Context, t *ui.Theme) {
	ui.Column(c).Gap(kit.PageGap).Children(func() {
		// 分组：一次只渲染一组。
		//
		// 为什么不是「长列表 + 折叠」：实测这页的耗时由「可见内容的绘制」驱动——
		// 视口 720 → 1400（可见内容变多）时 2.78ms → 4.03ms，而把折叠线以下的
		// 两块折叠起来几乎不改变耗时。只有真正减少「同时画多少」，帧时间才会降。
		//
		// group 取的是局部副本，控件写回后再比对持久化：这样分组状态存在页面私有
		// 状态里，不必往 App 结构体加字段（见 app_structure_test.go 的约束）。
		group := a.settingsGroup()
		ui.Segmented(c, &group, settingsGroupLabels()...)
		if group != a.settingsGroup() {
			a.setSettingsGroup(group)
		}
		general := group == settingsGroupGeneral
		workspace := group == settingsGroupWorkspace
		shortcuts := group == settingsGroupShortcuts
		about := group == settingsGroupAbout
		pages := group == settingsGroupPages

		// ===== 工作区（仓库管理的本机参数）=====
		// 单独一页：根目录与四个平台的令牌属于「业务配置」，和主题/字体这类
		// 外观偏好混在一起时，用户要滚过整页才能找到它。
		if workspace {
			a.workspaceSection(c, t)
		}

		if general {
			// ===== 外观 =====
			kit.Section(c, t, "外观", func() {
				settingRow(c, t, "主题", func() {
					ui.Segmented(c, &a.themeIdx, "跟随系统", "浅色", "深色")
				})
				// 配色 = 强调色系：不影响明暗与背景，只换选中态/主按钮/图标等语义色。
				settingRow(c, t, "配色", func() {
					ui.Segmented(c, &a.paletteIdx, paletteLabels()...)
				})
				// 色调 = 背景与控件面：不影响强调色，可和配色自由组合。
				// 九个色调在窄窗口里塞不进一行 Segmented（它会直接裁掉后半段），
				// 这时换成可换行的按钮组：宽度不够就自动换行，没有隐藏项。
				settingRow(c, t, "色调", func() {
					if a.contentWidth(c) >= 760 {
						ui.Segmented(c, &a.toneIdx, toneLabels()...)
						return
					}
					ui.Row(c).Gap(6).Wrap().Children(func() {
						for i, label := range toneLabels() {
							var b ui.Element
							if i == a.toneIdx {
								b = ui.PrimaryButton(c, label).Padding(2, 8)
							} else {
								b = ui.Button(c, label).Padding(2, 8)
							}
							if b.Clicked() {
								a.toneIdx = i
							}
						}
					})
				})
				settingRow(c, t, "字体大小", func() {
					ui.Slider(c, &a.fontSize, 11, 20).Width(220)
					ui.Text(c, fmt.Sprintf("%.0f px", math.Round(a.fontSize))).
						TextColor(t.TextMuted).Padding(8, 0, 0, 0)
				})
			})

			// ===== 背景 =====
			a.backgroundSection(c, t)

			// ===== 窗口与关闭 =====
			// 默认展开：关闭行为是常用设置，不该让用户先点一次才看得到；
			// 折叠只作为「想少滚一点」的选项（状态由页面私有状态记住）。
			kit.SectionCollapsible(c, t, "窗口与关闭",
				a.settingsSectionOpen("window", true),
				func() { a.toggleSettingsSection("window", true) },
				func() {
					ui.Text(c, "关闭按钮行为").Bold().Padding(0, 0, 8)
					ui.RadioGroup(c, func() {
						ui.Radio(c, &a.closeModeIdx, 0, "每次询问（首次关闭弹出选择）")
						ui.Radio(c, &a.closeModeIdx, 1, "最小化到托盘")
						ui.Radio(c, &a.closeModeIdx, 2, "直接退出")
					})
					ui.Text(c, "选择后，点击窗口关闭按钮即按此行为执行；也可在此随时修改。").
						TextColor(t.TextMuted).FontSize(t.FontSize-2).Padding(8, 0, 0, 0)
				})

			// ===== 启动 =====
			kit.Section(c, t, "启动", func() {
				// 开机自启：交给 mygo 的登录项 API（Windows 写 HKCU Run，macOS 登录项 /
				// LaunchAgent，Linux 写 XDG autostart）。状态由系统持有，这里只发起变更。
				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Switch(c, &a.openAtLogin).Shrink(0)
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "开机自启").Bold()
						ui.Text(c, "登录系统时自动启动本应用（可在任务管理器的启动项里关闭）。").
							TextColor(t.TextMuted).FontSize(t.FontSize - 2)
					})
				})

				ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
					ui.Switch(c, &a.allowMultiInstance).Shrink(0)
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "允许多开").Bold()
						ui.Text(c, "关闭时为单实例：重复启动只会唤出已在运行的窗口并退出新进程；"+
							"开启后每次启动都是独立窗口。").
							TextColor(t.TextMuted).FontSize(t.FontSize - 2)
					})
				})
				ui.Text(c, "提示：该设置需重启应用后生效（单实例锁在进程启动时获取）。").
					TextColor(t.TextMuted).FontSize(t.FontSize-2).Padding(6, 0, 0, 0)

				// 页面缓存：闲置多久销毁（对应 javafx 版的 idleTime）
				settingRow(c, t, "页面缓存", func() {
					ui.Stepper(c, &a.pageIdle, 0, settings.MaxPageIdleMinutes, 5)
					hint := fmt.Sprintf("闲置 %d 分钟后销毁缓存页面", int(math.Round(a.pageIdle)))
					if int(math.Round(a.pageIdle)) == 0 {
						hint = "不按闲置销毁（常驻页面始终保留）"
					}
					ui.Text(c, hint).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				})
				ui.Text(c, "「不缓存」的页面（如生成 license）离开即销毁，不受此项影响。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			})

			// ===== 异步任务 =====
			// 一次最多同时跑几个后台任务。作用范围是「一批仓库操作」：
			// 导入后克隆、批量拉取/提交推送/刷新远端、全量状态探测。
			kit.Section(c, t, "异步任务", func() {
				settingRow(c, t, "最大并发", func() {
					// 绑定 App 上的 float64 镜像：Stepper 在构建之后才写值，
					// 用局部变量会在下一帧被 settings 的旧值覆盖回去。
					ui.Stepper(c, &a.asyncWorkers, 1, float64(settings.MaxAsyncConcurrency), 1)
					got := int(math.Round(a.asyncWorkers))
					if got < 1 {
						got = 1
					}
					if got > settings.MaxAsyncConcurrency {
						got = settings.MaxAsyncConcurrency
					}
					if got != a.settings.AsyncConcurrency {
						a.settings.AsyncConcurrency = got
						a.persist()
					}
					ui.Text(c, fmt.Sprintf("同时最多 %d 个", got)).
						TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				})
				ui.Text(c, "导入克隆、批量拉取 / 提交推送 / 刷新远端、状态探测共用这个上限："+
					"同时启动这么多个，完成一个再补一个，直到整批结束。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			})

			// ===== 桌宠 =====
			kit.Section(c, t, "桌宠", func() { a.petSection(c, t) })

			// ===== 今日便签 =====
			kit.Section(c, t, "今日便签", func() { a.stickySection(c, t) })

		}

		if shortcuts {
			// ===== 快捷键 =====
			a.shortcutSection(c, t)
		}

		if about {
			// ===== 关于 =====
			// 产品标识与版本留在页面上（一眼可见）；应用标识 / 运行环境 / 页面缓存 /
			// 数据库与日志路径这些「查问题时才看」的信息收进弹窗，点「查看」打开
			// （内容见 basicinfo.go）。
			kit.Section(c, t, "关于", func() {
				ui.Text(c, appmeta.Title+" · "+appmeta.Name).Bold()
				ui.Text(c, "版本 "+appmeta.BuildVersion()).
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				ui.Text(c, "基于 mygo 的纯 Go 原生桌面应用。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)

				ui.Row(c).Gap(kit.CardGap).AlignItems(ui.Center).Margin(6, 0, 0, 0).Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, "基本信息").Bold()
						ui.Text(c, "应用标识、运行环境、页面缓存、数据库与日志路径").
							TextColor(t.TextMuted).FontSize(t.FontSize - 2)
					})
					if ui.Button(c, "查看").Clicked() {
						a.showBasicInfo = true
					}
				})
			})

			// ===== 数据与诊断 =====
			// 用户报障时的最短路径：打开目录看文件，或一键打包发给支持人员
			// （见 diagnostics.go：settings + 近三天日志 + 库快照 + 环境信息）。
			// 这里不再折叠：它已经在一个很短的「关于与诊断」分组里，再折一层等于
			// 让用户点两次才能看到报障按钮。
			kit.Section(c, t, "数据与诊断", func() {
				ui.Text(c, "配置文件、日志与数据库都在本机的应用目录里，可以直接打开查看。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)

				ui.Row(c).Gap(8).Wrap().Margin(4, 0, 0, 0).Children(func() {
					if ui.Button(c, "打开配置目录").Clicked() {
						a.openConfigDir()
					}
					if ui.Button(c, "打开日志目录").Clicked() {
						a.openLogDir()
					}
					if ui.Button(c, "导出诊断包").Clicked() {
						a.exportDiagnostics()
					}
				})
				ui.Text(c, "诊断包含偏好、近三天日志、数据库快照与环境信息；不含任何账号密码。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)

				// 页面状态：各页面用 a.Prefs() 记住的筛选条件、草稿等。属于「可以随时丢」
				// 的数据，给一个明确的清理入口，免得用户以为它跟任务/授权一样重要。
				ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprintf("页面状态：%d 条偏好 + %d 个页面的运行时缓存（筛选条件、草稿、表格缓存等）",
						a.prefCount(), a.pageStateCount())).
						TextColor(t.TextMuted).FontSize(t.FontSize - 2)
					if ui.Button(c, "清理页面状态").Clicked() {
						a.clearPrefs()
					}
				})
				ui.Text(c, "清理只影响页面自己记住的状态与运行时缓存，不动任务、授权记录、偏好与日志；"+
					"当前页面保持不动，切走后自然重建。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)

				// 配置与数据：换机 / 备份 / 三台电脑之间搬运（见 internal/profile）。
				ui.Text(c, "配置与数据").Bold().Margin(8, 0, 0, 0)
				ui.Text(c, "换机时在旧机器导出、新机器导入即可复用设置、任务、授权与页面状态；"+
					"导入会先备份本机现有文件，完成后自动重启。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
					if ui.Button(c, "导出设置…").Clicked() {
						a.exportProfile(false)
					}
					if ui.Button(c, "导出设置与数据…").Clicked() {
						a.exportProfile(true)
					}
					if ui.Button(c, "导入…").Clicked() {
						a.importProfileDialog()
					}
				})
				ui.Text(c, "配置包是 zip，可能包含授权凭证；只导出设置时不包含任务与授权记录。").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)

				a.repoSection(c, t)
			})

			// ===== 更新 =====
			// 进入设置页（onEnter）即后台检查；这里只负责展示状态与让用户决定要不要更新。
			a.updateSection(c, t)
		}

		if pages {
			// ===== 页面 =====
			// 各页面在注册表里用 navItem.settings 声明自己的设置项，这里按注册表顺序
			// 渲染成区块。状态由页面自己用 a.PrefsFor(页面 id) 读写，设置页不需要知道
			// 任何具体字段。
			ui.Text(c, "以下设置由各页面自己声明，保存在页面私有状态里；"+
				"可在「关于与诊断 → 数据与诊断」里一键清理。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			for _, it := range pageSettingItems() {
				kit.Section(c, t, it.label, func() {
					it.settings(a, c, t)
				})
			}
		}
	})
}

// settingsPageID 定义在 registry.go（与 settingsPageItem 放在一起）。

// 设置页的分组。顺序即分段控件里的顺序。
const (
	settingsGroupGeneral   = iota // 常规：外观 / 背景 / 窗口与关闭 / 启动
	settingsGroupWorkspace        // 工作区：仓库根目录 + 各平台令牌
	settingsGroupShortcuts        // 快捷键
	settingsGroupAbout            // 关于与诊断
	settingsGroupPages            // 页面：各页面自己声明的设置（没有时不显示这一页）
)

// settingsGroupLabels 分段控件的文案。
//
// 「页面」只在真有页面声明了设置时才出现（见 navItem.settings）：一个空的设置页
// 对用户没有意义，还会让别人以为页面设置是必须的。页面是否注册在编译期就定了，
// 所以这个列表在一次运行里不会变。
func settingsGroupLabels() []string {
	labels := []string{"常规", "工作区", "快捷键", "关于与诊断"}
	if len(pageSettingItems()) > 0 {
		labels = append(labels, "页面")
	}
	return labels
}

// settingsGroup 返回当前分组（默认「常规」）。存页面私有状态：切走再回来还记着。
func (a *App) settingsGroup() int {
	if v, ok := a.pageStat(settingsPageID).data[settingsGroupKey].(int); ok {
		if v >= 0 && v < len(settingsGroupLabels()) {
			return v
		}
	}
	return settingsGroupGeneral
}

// setSettingsGroup 记录当前分组；越界值忽略。
func (a *App) setSettingsGroup(g int) {
	if g < 0 || g >= len(settingsGroupLabels()) {
		return
	}
	a.pageStat(settingsPageID).data[settingsGroupKey] = g
}

// settingsGroupKey 当前分组在页面私有状态里的键名。
//
// 带 -v2：分组顺序在 v0.3.0 变过（插入「工作区」），旧的下标会被解读成别的页，
// 换个键名等于「老用户这次回到默认页」，比停在一个奇怪的页面上好。
const settingsGroupKey = "group-v2"

// settingsOpenKey 分区折叠状态在页面私有状态里的键名。
const settingsOpenKey = "section-open"

// settingsSectionOpen 返回某分区是否展开；从没点过时按 def 决定默认值。
//
// 折叠状态存 pageState.data：设置页是 keepPinned，所以切走再回来还记着；又不必
// 往 App 结构体加字段（见 app_structure_test.go 的约束）。
func (a *App) settingsSectionOpen(id string, def bool) bool {
	m, _ := a.pageStat(settingsPageID).data[settingsOpenKey].(map[string]bool)
	if v, ok := m[id]; ok {
		return v
	}
	return def
}

// toggleSettingsSection 翻转某分区的展开状态。
func (a *App) toggleSettingsSection(id string, def bool) {
	st := a.pageStat(settingsPageID)
	m, _ := st.data[settingsOpenKey].(map[string]bool)
	if m == nil {
		m = make(map[string]bool, 4)
		st.data[settingsOpenKey] = m
	}
	m[id] = !a.settingsSectionOpen(id, def)
}

// updateSection 「更新」区块：只展示检查/安装状态，动作全部上提到设置页标题栏
// 的更新按钮（无更新=图标按钮，有更新=高亮「更新」胶囊），点击弹窗确认。
//
// 设计依据：Dropbox 等桌面应用把「更新」做成标题栏右侧的图标/胶囊按钮，正文区
// 只保留状态文案与安装进度，避免页面内再出现一套重复的更新按钮。
func (a *App) updateSection(c *ui.Context, t *ui.Theme) {
	kit.Section(c, t, "更新", func() {
		// 状态优先级：安全模式 > 安装中 > 检查中 > 已禁用 > 检查失败 > 可更新 > 已最新 > 尚未检查。
		switch {
		case a.safeMode:
			ui.Text(c, "安全模式启动：本次已跳过更新检查与数据库。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		case a.updateInstalling:
			ui.Column(c).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					kit.BreathingDot(c, t.Accent, 7, kit.BreathePeriod)
					ui.Text(c, "正在下载并更新…").TextColor(t.TextMuted).Grow(1)
					if a.updateProgress > 0 {
						ui.Text(c, fmt.Sprintf("%.0f%%", a.updateProgress*100)).
							TextColor(t.TextMuted)
					}
				})
				ui.Progress(c, a.updateProgress).Grow(1)
			})
		case a.updateChecking:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				kit.BreathingDot(c, t.Accent, 7, kit.BreathePeriod)
				ui.Text(c, "正在检查更新…").TextColor(t.TextMuted)
			})
		case a.updateDisabled:
			ui.Text(c, "当前构建未启用自动更新（开发构建 / 未通过 mygo build 产出）。经 go tool mygo build 生成的正式发布包支持自动下载更新。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		case a.updateErr != nil:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Bell).TextColor(t.Danger)
				ui.Text(c, "检查失败："+a.updateErr.Error()).
					TextColor(t.Danger).FontSize(t.FontSize - 2).Grow(1).SingleLine()
			})
		case a.availableUpdate != nil && !a.updateDismissed:
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Download).TextColor(t.Accent)
				ui.Text(c, "发现新版本 "+a.availableUpdate.Version+"：点击右上角「更新」查看详情").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2).Grow(1)
			})
		case a.availableUpdate != nil:
			// 有新版本、但本次点了「忽略」：绝不能显示成「已是最新」。
			// 忽略只是不再催促，新版本依旧可装（点标题栏更新图标会重查并重新提示）。
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Download).TextColor(t.TextMuted)
				ui.Text(c, "发现新版本 "+a.availableUpdate.Version+"（本次已忽略，可点右上角更新图标重开）").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2).Grow(1).SingleLine()
			})
		case a.updateChecked:
			// 只有「检查成功且确实没有新版本」才说已是最新。
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Sparkles).TextColor(t.Success)
				ui.Text(c, "已是最新版本").
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			})
		default:
			ui.Text(c, "尚未检查。进入本页即自动检查，也可点标题栏更新图标手动检查。").
				TextColor(t.TextMuted).FontSize(t.FontSize - 2)
		}
	})
}

// updateDialog 点击标题栏「更新」胶囊后弹出的确认弹窗：展示新版本号与发布说明，
// 由用户决定是否立即下载安装（mygo.Updater 负责签名校验与就地替换，完成后重启）。
//
// 绝不做「自动更新」：发现新版本只提示，是否更新完全由用户点「立即更新」决定；
// 「忽略」隐藏本次会话提示（下次进入设置重新出现）。
func (a *App) updateDialog(c *ui.Context, t *ui.Theme) {
	modalPanel(c, t, &a.showUpdateDialog, func() {
		up := a.availableUpdate
		if up == nil {
			return
		}
		ui.Text(c, "发现新版本").FontSize(18).Bold()
		ui.Text(c, "有新版本可用，是否立即更新？").
			TextColor(t.TextMuted).Padding(0, 6, 10)

		// 版本 + 发布说明卡
		ui.Column(c).Gap(8).Padding(12).Radius(kit.CardRadius).
			Background(kit.Alpha(t.Accent, 0.10)).Border(1, t.Accent).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, icons.Download).TextColor(t.Accent)
				ui.Text(c, up.Version).Bold().Grow(1)
			})
			if notes := strings.TrimSpace(up.Notes); notes != "" {
				// 发布说明可能很长（0.1.7 的说明就有整屏）。不能让它按内容
				// 无限撑高弹窗——否则底部的「忽略 / 立即更新」会被挤出可视区，
				// 用户反而没法更新。
				//
				// 短说明保持原来的自然高度；只有确实过长时才给滚动容器一个
				// 确定高度（Height，而不是 MaxHeight）。Scroll 的文档要求
				// “给尺寸或 Grow”，固定高度才能保证滚轮/滚动条有视口。
				if len([]rune(notes)) > 480 || strings.Count(notes, "\n") > 12 {
					_, winH := c.Size()
					notesH := winH * 0.38
					if notesH < 160 {
						notesH = 160
					}
					if notesH > 300 {
						notesH = 300
					}
					ui.Scroll(c).Height(notesH).Children(func() {
						ui.Text(c, notes).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
					})
				} else {
					ui.Text(c, notes).TextColor(t.TextMuted).FontSize(t.FontSize - 2)
				}
			}
			if !up.Date.IsZero() {
				// manifest 里的 date 是构建时的 **UTC** 时间（mygo 写的是
				// time.Now().UTC().Format(time.RFC3339)），直接 Format 会按 UTC 显示——
				// 北京时间凌晨构建时就会显示成"前一天"，看着像日期不更新。
				// 这里转成本地时区，并带上时分，避免"怎么一直是昨天"的困惑。
				ui.Text(c, "发布时间："+up.Date.Local().Format("2006-01-02 15:04")).
					TextColor(t.TextMuted).FontSize(t.FontSize - 2)
			}
		})

		ui.Text(c, "当前版本 "+appmeta.BuildVersion()).
			TextColor(t.TextMuted).FontSize(t.FontSize-2).Padding(4, 0, 0, 0)

		ui.Row(c).Gap(8).Justify(ui.End).Padding(0, 12, 0).Children(func() {
			if ui.Button(c, "忽略").Clicked() {
				a.updateDismissed = true
				// 记进设置：否则重启后又弹出同一个版本，用户会觉得「忽略」没生效。
				if a.availableUpdate != nil {
					a.settings.IgnoredVersion = a.availableUpdate.Version
					a.persist()
				}
				a.showUpdateDialog = false
				a.requestFrame()
			}
			install := ui.PrimaryButton(c, "立即更新")
			if a.updateInstalling {
				install.Disabled(true)
			}
			if install.Clicked() {
				a.showUpdateDialog = false
				a.installUpdate()
			}
		})
	})
}

// backgroundSection 「背景」区块：背景图片选择、菜单区/内容区不透明度、
// 恢复默认。
//
// 设计取舍：
//   - 透明度拆成「菜单区」「内容区」两个独立滑块，用户可以只让侧栏更透、内容区
//     更实（阅读区域通常需要更实）；
//   - 滑块调节的是「不透明度」而非「透明度」：100% = 完全遮蔽背景图（观感与未设
//     背景图时一致），数值越小背景图越透出；
//   - 未设置背景图时两个滑块置灰（无意义），并提供「恢复默认」一键清除背景图。
func (a *App) backgroundSection(c *ui.Context, t *ui.Theme) {
	hasBg := a.settings.BackgroundImage != ""
	kit.Section(c, t, "背景", func() {
		// 背景图片：展示当前路径 + 选择 / 恢复默认
		ui.Row(c).Gap(kit.CardGap).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "背景图片").Width(kit.SettingsLabelWidth).Shrink(0).SingleLine()

			label := "未设置（使用主题默认外观）"
			if hasBg {
				label = a.settings.BackgroundImage
			}
			ui.Text(c, label).TextColor(t.TextMuted).Grow(1).SingleLine()

			// 解码在后台 goroutine 里做（大图要几百毫秒），这里给一个明确的进行中状态，
			// 否则用户会以为「选了没反应」。
			if a.bgLoadingFor != "" {
				ui.Text(c, "加载中…").TextColor(t.TextMuted).FontSize(t.FontSize - 2).Shrink(0)
			}

			if ui.Button(c, "选择图片…").Shrink(0).Clicked() {
				a.chooseBackground()
			}
			reset := ui.Button(c, "恢复默认").Shrink(0)
			if !hasBg {
				// 未设置背景图时「恢复默认」无意义：禁用，避免误操作。
				reset.Disabled(true)
			} else if reset.Clicked() {
				a.resetBackground()
			}
		})

		if a.bgError != "" {
			ui.Text(c, a.bgError).TextColor(t.Danger).FontSize(t.FontSize - 2)
		}

		// 菜单区 / 内容区不透明度
		settingRow(c, t, "菜单区", func() {
			sl := ui.Slider(c, &a.menuOpacity, settings.MinOpacity, settings.MaxOpacity).Width(220)
			if !hasBg {
				sl.Disabled(true)
			}
			ui.Text(c, fmt.Sprintf("%.0f%%", a.menuOpacity*100)).
				TextColor(t.TextMuted).Padding(8, 0, 0, 0)
		})
		settingRow(c, t, "内容区", func() {
			sl := ui.Slider(c, &a.contentOpacity, settings.MinOpacity, settings.MaxOpacity).Width(220)
			if !hasBg {
				sl.Disabled(true)
			}
			ui.Text(c, fmt.Sprintf("%.0f%%", a.contentOpacity*100)).
				TextColor(t.TextMuted).Padding(8, 0, 0, 0)
		})

		hint := "「不透明度」越大遮罩越实、背景图越淡；100% 即完全遮蔽背景图。"
		if !hasBg {
			hint = "先设置背景图片，菜单区 / 内容区的不透明度才会生效。"
		}
		ui.Text(c, hint).TextColor(t.TextMuted).FontSize(t.FontSize-2).Padding(4, 0, 0, 0)
	})
}

// settingRow 一行「标签 + 控件」的布局（标签宽度取全局规格，保证多行对齐）。
func settingRow(c *ui.Context, t *ui.Theme, label string, fn func()) {
	ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).Width(kit.SettingsLabelWidth).Shrink(0).SingleLine()
		fn()
	})
}

// closeChoiceDialog 首次关闭（CloseMode=="ask"）时弹出的询问弹窗：
// 让用户选择本次关闭行为，并可勾选“记住我的选择”以便后续按该行为执行。
func (a *App) closeChoiceDialog(c *ui.Context, t *ui.Theme) {
	modalPanel(c, t, &a.showCloseChoice, func() {
		ui.Text(c, "关闭窗口").FontSize(18).Bold()
		ui.Text(c, "您希望点击关闭按钮时执行什么操作？").
			TextColor(t.TextMuted).Padding(0, 10, 10)

		ui.RadioGroup(c, func() {
			ui.Radio(c, &a.closeChoice, "minimize", "最小化到托盘")
			ui.Radio(c, &a.closeChoice, "quit", "直接退出")
		})

		ui.Checkbox(c, &a.rememberClose, "记住我的选择")

		ui.Row(c).Gap(8).Justify(ui.End).Padding(0, 12, 0).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				a.showCloseChoice = false
			}
			if ui.PrimaryButton(c, "确定").Clicked() {
				if a.rememberClose {
					a.settings.CloseMode = a.closeChoice
					a.persist()
				}
				a.showCloseChoice = false
				a.runClose(a.closeChoice)
			}
		})
	})
}
