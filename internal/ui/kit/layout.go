// Package kit 是本项目自研的 UI 构件层（设计系统）。
//
// 分两类内容：
//   - layout.go：全局统一的间距/圆角规格。页面只写内容，不写外边距；
//   - kit.go：通用构件（卡片、区块、指标卡、状态药丸、键值行…）。
//
// 为什么要单独成包：这些构件与具体业务无关，只依赖 ui.Context / ui.Theme。
// 放在独立包里，一改全站统一，且不会被业务代码顺手改坏（业务代码 import 它，
// 它不 import 业务）。
package kit

// 全局间距规格（DIP）。
//
// 唯一定义处：页面、卡片、区块都必须引用这里的常量，禁止在页面里硬编码数字。
// 这样新增页面时只需按区块顺序写内容，间距由容器统一负责。
const (
	// PagePadX 内容区左右留白：页头与页面内容共用，保证左边缘对齐。
	PagePadX = 28

	// PagePadY 内容区上下留白（滚动内容的 padding）。
	PagePadY = 24

	// PageGap 页面内「区块」之间的垂直间距（滚动容器统一施加）。
	PageGap = 16

	// RowGap 同一行/同一栅格里并排的卡片之间的间距。
	RowGap = 16

	// CardGap 卡片内部各行之间的垂直间距。
	CardGap = 12

	// CardPad 卡片内边距。
	CardPad = 18

	// CardRadius 卡片圆角。
	CardRadius = 10

	// TileRadius 小方块（快捷入口等嵌套元素）的圆角。
	TileRadius = 8

	// LabelWidth 表单/设置行左侧标签的固定宽度，保证多行标签对齐。
	LabelWidth = 96

	// SettingsLabelWidth 设置页「标签 + 控件」行的标签宽度。
	SettingsLabelWidth = 90
)
