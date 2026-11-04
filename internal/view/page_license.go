package view

import (
	"time"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/license"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/ui/kit"
)

// ---- 生成 license 页 ----
//
// 一个「真实业务形态」的页面：表单 → 校验 → 生成 → 结果可复制 → 历史记录。
// 签名逻辑在 internal/license（纯 Go、可单测），页面只负责交互。
//
// 该页面在注册表里是 keepFresh：每次进入都是空表单，避免上一次的授权对象
// 被误当成这一次的（这也是「不缓存页面」最典型的使用场景）。

// licSecret 本地演示用签名密钥。
//
// 真实产品应改为「服务端签发 + 客户端验签（公钥）」，见 internal/license 的包注释。
const licSecret = "bestuav-local-demo-secret"

// licForm 表单状态（随页面缓存存亡：keepFresh 页面离开即丢弃）。
type licForm struct {
	subject  string
	edition  string
	term     string
	features map[string]bool

	result  string // 最近一次生成的凭证
	history []licRecord
}

// licRecord 历史记录的一条。
type licRecord struct {
	subject string
	edition string
	term    string
	token   string
	at      time.Time
}

// defaultLicForm 表单初始值。
func defaultLicForm() licForm {
	return licForm{
		subject:  "",
		edition:  "专业版",
		term:     "1 年",
		features: map[string]bool{"数据导出": true, "开放 API": true},
	}
}

// resetLicForm 清空表单（保留版本/有效期的默认值）。
func (a *App) resetLicForm() {
	keep := a.lic.history
	a.lic = defaultLicForm()
	a.lic.history = keep
	a.toast("表单已重置")
}

// copyLicense 复制最近一次生成的结果。
func (a *App) copyLicense() {
	if a.lic.result == "" {
		a.toast("还没有生成结果")
		return
	}
	a.copyText(a.lic.result, "license 已复制")
}

// generateLicense 依据表单生成凭证（本地 HMAC 签名）。
func (a *App) generateLicense() {
	subject := a.lic.subject
	if subject == "" {
		a.toast("请先填写授权对象")
		return
	}
	p := license.Payload{
		Subject:  subject,
		Edition:  a.lic.edition,
		Features: selectedFeatures(a.lic.features),
		Issuer:   appmeta.Name,
	}
	switch a.lic.term {
	case "30 天":
		p.ExpiresAt = time.Now().AddDate(0, 0, 30)
	case "1 年":
		p.ExpiresAt = time.Now().AddDate(1, 0, 0)
	case "3 年":
		p.ExpiresAt = time.Now().AddDate(3, 0, 0)
	default: // 永久：ExpiresAt 零值
	}
	token, err := license.Generate(p, licSecret)
	if err != nil {
		a.toast("生成失败：" + err.Error())
		return
	}
	a.lic.result = token
	a.lic.history = append([]licRecord{{
		subject: p.Subject, edition: p.Edition, term: a.lic.term, token: token, at: time.Now(),
	}}, a.lic.history...)
	if len(a.lic.history) > maxLicenseRows {
		a.lic.history = a.lic.history[:maxLicenseRows]
	}
	// 落地：库是持久化真源，界面里这份只是本帧展示用。
	a.persistLicense(p.Subject, p.Edition, a.lic.term, token)
	logging.Audit("license", "生成授权凭证",
		"subject", p.Subject, "edition", p.Edition, "term", a.lic.term)
	a.celebrate(2) // 生成成功的小庆祝
	a.toast("license 生成成功")
}

// selectedFeatures 把勾选的功能位整理成有序切片。
func selectedFeatures(m map[string]bool) []string {
	order := []string{"数据导出", "开放 API", "离线部署", "优先支持"}
	out := make([]string, 0, len(order))
	for _, k := range order {
		if m[k] {
			out = append(out, k)
		}
	}
	return out
}

// licensePage 生成 license 页。
func (a *App) licensePage(c *ui.Context, t *ui.Theme) {
	// ---- 左：表单 ----
	form := func() {
		kit.Card(c, t, func() {
			kit.CardHeader(c, t, "授权信息", "填写后点击生成，凭证可离线校验", nil)

			ui.Field(c, "授权对象", func() {
				ui.TextInput(c, &a.lic.subject).Placeholder("公司或个人名称")
			})

			ui.Row(c).Gap(12).Children(func() {
				ui.Column(c).Grow(1).Children(func() {
					ui.Field(c, "版本", func() {
						ui.Select(c, &a.lic.edition, []string{"标准版", "专业版", "企业版"}).Grow(1)
					})
				})
				ui.Column(c).Grow(1).Children(func() {
					ui.Field(c, "有效期", func() {
						ui.Select(c, &a.lic.term, []string{"30 天", "1 年", "3 年", "永久"}).Grow(1)
					})
				})
			})

			ui.Fieldset(c, "功能位", func() {
				ui.Column(c).Gap(6).Children(func() {
					for _, f := range []string{"数据导出", "开放 API", "离线部署", "优先支持"} {
						k := f
						if a.lic.features == nil {
							a.lic.features = map[string]bool{}
						}
						// Go 里 map 元素不可取址：用局部变量承接，回写。
						on := a.lic.features[k]
						ui.Checkbox(c, &on, k)
						a.lic.features[k] = on
					}
				})
			})

			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "重置").Clicked() {
					a.resetLicForm()
				}
				if ui.PrimaryButton(c, "生成凭证").Clicked() {
					a.generateLicense()
				}
			})
		})
	}

	// ---- 右：结果 + 历史 ----
	results := func() {
		kit.Card(c, t, func() {
			kit.CardHeader(c, t, "生成结果", "形如「载荷.签名」，载荷可读、签名防篡改", func() {
				if ui.Button(c, "复制").Clicked() {
					a.copyLicense()
				}
			})
			if a.lic.result == "" {
				kit.EmptyHint(c, t, "尚未生成。左侧填好授权对象后点击「生成凭证」。")
				return
			}
			// 凭证很长（载荷+签名），列表里只展示头尾，完整值靠「复制」。
			ui.Text(c, license.Shorten(a.lic.result, 12)).Selectable().
				FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
			if p, err := license.Verify(a.lic.result, licSecret); err == nil {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					kit.Pill(c, t, "自校验通过", t.Success)
					ui.Text(c, p.Edition+" · 至 "+license.ExpiryText(p)).
						FontSize(t.FontSize - 2).TextColor(t.TextMuted).Grow(1).SingleLine()
				})
			}
		})

		kit.Card(c, t, func() {
			kit.CardHeader(c, t, "生成历史", "最近 6 条", nil)
			if len(a.lic.history) == 0 {
				kit.EmptyHint(c, t, "暂无记录")
				return
			}
			ui.Column(c).Gap(2).Children(func() {
				for _, h := range a.lic.history {
					ui.Row(c).Gap(10).AlignItems(ui.Center).Padding(6, 6).Radius(t.Radius).
						Children(func() {
							ui.Column(c).Gap(1).Grow(1).Children(func() {
								ui.Text(c, h.subject).SingleLine()
								ui.Text(c, h.edition+" · "+h.term+" · "+h.at.Format("01-02 15:04")).
									FontSize(t.FontSize - 2).TextColor(t.TextMuted).SingleLine()
							})
							if ui.Button(c, "复制").Clicked() {
								a.copyText(h.token, "已复制该条 license")
							}
						})
				}
			})
		})
	}

	// 窄窗口上下堆叠：左栏固定 460 + 右栏最小可读宽度，塞不进一行时
	// 硬并排只会把右栏压成一条，历史记录完全不可读。
	if a.stacked(c, 780) {
		ui.Column(c).Gap(kit.PageGap).Children(func() {
			form()
			ui.Column(c).Gap(kit.PageGap).Children(results)
		})
		return
	}
	ui.Row(c).Gap(kit.RowGap).AlignItems(ui.Start).Children(func() {
		// 左栏平时 460，窗口收窄时允许缩（Shrink(1)），不会把右栏挤到窗外。
		ui.Column(c).Width(460).Shrink(1).Children(form)

		// Shrink(1)：长凭证文本不允许把右列撑到窗口外（否则整张卡片溢出）。
		ui.Column(c).Grow(1).Shrink(1).Gap(16).Children(results)
	})
}
