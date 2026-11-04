package view

import "github.com/egoist/mygo/ui"

// ---- 占位页（注册表里 build == nil 的页面） ----
//
// 新增菜单时可以先只登记一条、不写页面函数，自动落到这里，避免「点了没反应」。
func placeholderPage(c *ui.Context, t *ui.Theme, title, id string) {
	if title == "" {
		title = id
	}
	// 页面标题由内容区固定页头展示，这里只留页面自身内容；区块间距由内容区统一给出。
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Width(8).Height(8).Radius(999).Background(t.Accent)
		ui.Text(c, "（演示占位页面）").TextColor(t.TextMuted)
	})
	ui.Text(c, "该页面为整体框架占位，后续可接入对应功能模块。").TextColor(t.TextMuted)
	ui.Text(c, "实现方式：在 internal/view/registry.go 里给该菜单项加上 build 函数即可。").
		FontSize(t.FontSize-2).TextColor(t.TextMuted).Padding(8, 0, 0)
}

// ---- 弹窗（对应 JavaFX 的 FXCustomDialog） ----

func (a *App) dialog(c *ui.Context, t *ui.Theme) {
	modalPanel(c, t, &a.showDlg, func() {
		ui.Text(c, "系统通知").FontSize(18).Bold()
		ui.Text(c, "您保存的配置已成功应用到本地 config.json 文件。").
			TextColor(t.TextMuted).Padding(0, 12, 12)
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.PrimaryButton(c, "我知道了").Clicked() {
				a.showDlg = false
			}
		})
	})
}
