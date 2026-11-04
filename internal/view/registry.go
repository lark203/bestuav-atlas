package view

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/view/shell"
)

// 本文件是「菜单 + 页面 + 页头按钮」的**唯一注册处**。
//
// 旧做法要在三处同步改：sidebar.go 的 navItemDef、app.go 的 pageTitles、
// 以及页面分发 switch——漏改一处就会出现「点了菜单没反应」「页头标题空白」这类
// 假 bug。现在统一成一张表：加一条 = 多一个菜单项 + 多一个可用页面。
//
// 新增一个菜单/页面：
//
//	1) 写页面函数（新建文件或加到已有文件）：
//	     func (a *App) orderPage(c *ui.Context, t *ui.Theme) { ... }
//	2) 在下面 navRegistry() 的对应分组里加一条：
//	     {id: "order", label: "订单管理", icon: icons.Table, build: (*App).orderPage,
//	      tools: []toolAction{{icon: icons.Search, label: "搜索", tip: "搜索订单"}}}
//
// build 为 nil 时自动落到占位页（hint 作为说明文案），可以先占位再实现；页面需要
// 「页面级设置」时再补 settings: (*App).orderSettings（会出现在「设置 → 页面」里）。
// 更省事的做法是 `go run ./scripts/newpage ...`（见 internal/pagegen）。

// pageFunc 页面内容构建函数：签名与 App 方法一致，可直接写 (*App).xxx。
type pageFunc func(a *App, c *ui.Context, t *ui.Theme)

// navItem 一个菜单项 = 一个页面。
type navItem struct {
	id     string       // 页面 id，同时是选中态判断依据
	label  string       // 侧栏文字 + 页头标题
	icon   *ui.SVG      // 侧栏图标（16px 线性 SVG）
	tools  []toolAction // 页头右侧图标按钮（可空）
	build  pageFunc     // 页面内容（nil → 占位页）
	hint   string       // 占位页的一句话说明
	policy pagePolicy   // 生命周期与缓存策略（零值 = 缓存 + 默认闲置销毁）
	// hidden 为 true 时不在侧栏出现，但照样能按 id 分发与显示页头标题。
	// 用于「只能从别处进入的页面」（如仓库详情），避免为了一个详情页去改导航模型。
	hidden bool
	// back 非空时，页头标题左侧自动出现返回键，点击跳到该页面 id。
	//
	// 与 bestuav-mygo 保持同一套约定：「从哪来、回哪去」是页面在导航里的位置，
	// 声明在注册表，骨架（shell.Content 的 Leading 插槽）统一画，页面函数不用管。
	back string
	// settings 页面级设置项（可空）：在「设置 → 页面」里渲染成一个区块。
	//
	// 设置项自己用 a.PrefsFor(id) 读写页面私有状态（见 prefs.go），设置页不需要
	// 认识任何具体字段；没有任何页面声明 settings 时，「页面」这一组不会出现。
	settings pageFunc
}

// navGroup 侧栏里的一个可折叠分组。
type navGroup struct {
	title string
	items []navItem
}

// navRegistry 是全部菜单与页面的定义。侧栏、页头、页面分发都由它驱动。
//
// 注意：这里必须保持「直接 return 一个 []navGroup 字面量」的写法——生成器
// （internal/scaffold/regcut.go）靠 AST 在这个字面量上做增删，改成先算再 return
// 会让「新建项目」的菜单裁剪失效（scaffold 的 TestCutRegistry 会拦住）。
// 每帧的查询走 navAll() 里一次性建好的缓存，不要直接调本函数。
func navRegistry() []navGroup {
	return []navGroup{
		// 首页不放进分组（侧栏顶部第一项，无分组标题）：它是「现在该干什么」的总览，
		// 不是某个业务分组里的页面。
		{title: "", items: []navItem{
			{id: "home", label: "总览", icon: icons.Home, build: (*App).overviewPage,
				policy: pagePolicy{keep: keepPinned}}, // 首页常驻，不必重建
		}},

		// 研发：星图的三块核心业务。顺序即日常使用顺序——先看要做什么（待办），
		// 再看这些事属于谁（项目），最后处理代码（仓库）。
		{title: "研发", items: []navItem{
			// 仓库详情：不出现在侧栏，只能从仓库行点「详情」进入；back 让页头
			// 自动出现返回键，回到仓库列表。
			{id: "repo-detail", label: "仓库详情", icon: icons.Code, build: (*App).repoDetailPage,
				hidden: true, back: "repos", policy: pagePolicy{keep: keepCached}},
			{id: "todos", label: "待办", icon: icons.Checklist, build: (*App).todoPage,
				policy: pagePolicy{keep: keepCached},
				tools: []toolAction{
					{icon: icons.Checklist, label: "新建待办", tip: "打开待办编辑弹窗",
						run: func(a *App) { a.openTodoDialog(store.Todo{}) }},
				}},
			{id: "projects", label: "项目", icon: icons.IdCard, build: (*App).projectPage,
				policy: pagePolicy{keep: keepCached},
				tools: []toolAction{
					{icon: icons.Package, label: "新建项目", tip: "新建一个项目",
						run: func(a *App) { a.openProjectDialog(store.Project{}) }},
				}},
			{id: "repos", label: "仓库", icon: icons.Code, build: (*App).repoPage,
				// 选中项与筛选条件要「切回来还在」，用缓存；闲置超时后由全局规则带走。
				// 页头不再放工具按钮：导入/刷新在页面顶部已经各有一个入口，
				// 同一屏两个「导入仓库」只会让人多想一秒。
				policy: pagePolicy{keep: keepCached}},
		}},

		// 工具：工程自带的能力，与业务无关。放在最后，不干扰日常路径。
		{title: "工具", items: []navItem{
			{id: "task", label: "异步任务", icon: icons.Bolt, build: (*App).taskPage,
				// 任务列表要"切回来还在"，用缓存；闲置 30 分钟再由全局规则带走。
				policy: pagePolicy{keep: keepCached},
				tools: []toolAction{
					{icon: icons.Bolt, label: "轻量任务", tip: "运行一个轻量级任务（进度显示在页头浅线处）"},
					{icon: icons.Sparkles, label: "演示彩带", tip: "撒一把彩带看看效果"},
				}},
			{id: "gen-license", label: "生成 license", icon: icons.FileKey, build: (*App).licensePage,
				// 表单类页面：每次进入都要一份干净的表单，故不缓存。
				policy: pagePolicy{keep: keepFresh},
				tools: []toolAction{
					{icon: icons.Refresh, label: "重置表单", tip: "清空当前表单"},
					{icon: icons.Copy, label: "复制结果", tip: "复制最近一次生成的 license"},
				}},
		}},
	}
}

// settingsPageID 底部固定「设置」页的 id（页面文件、单实例转发共用）。
const settingsPageID = "settings"

// settingsPageLabel 「设置」项的侧栏文字与页头标题。
const settingsPageLabel = "设置"

// settingsNavIcon 「设置」项的图标（侧栏底部与注册表共用）。
//
// 单独提出来是为了让 app.go 的侧栏底部不必调用 settingsPageItem()（那会每帧构造
// 一个 navItem 与它的策略闭包）。
var settingsNavIcon = icons.Gear

// settingsOnEnter 进入设置页时后台检查更新（见 settingsPageItem 的说明）。
func settingsOnEnter(a *App) { a.checkForUpdates(false) }

// settingsPageItem 底部固定的「设置」项：不放在任何分组里（侧栏单独渲染在底部），
// 但它同样需要参与「页头标题 / 页面分发 / 生命周期」的查找，故并进 eachPage。
//
// 为什么用函数而不是包级 var：设置页会经 pageSettingItems → navAll 间接引用本项，
// 包级 var 的初始化器会形成「初始化环」（Go 会静态检查这种间接引用）。
func settingsPageItem() navItem {
	return navItem{
		id: settingsPageID, label: settingsPageLabel, icon: settingsNavIcon,
		build: (*App).settingsPage,
		// 进入设置即后台检查更新（onEnter 只在切到本页那一帧触发，启动默认页是 home
		// 故不会误触发）。检查由 goroutine 异步完成，UI 显示「正在检查」再到结果。
		//
		// 非阻塞 goroutine，10 分钟内不重复查；用户「忽略」过的版本记在
		// settings.IgnoredVersion 里，检查结果回来时会被重新标记为已忽略；
		// 手动点标题栏图标（force）才重新显示提示。
		policy: pagePolicy{keep: keepPinned, onEnter: settingsOnEnter},
	}
}

// navAll 返回一次性构建的注册表切片与 id → 页面 的索引。
//
// 侧栏、页头标题、页头工具条和闲置淘汰每帧都要查这张表；每次重建五个分组
// 三十来个 item 属于纯浪费（实测单次 9.9µs / 6KB，已访问十个页面后每帧十几次）。
func navAll() ([]navGroup, map[string]navItem) {
	navOnce.Do(func() {
		navGroupsCache = navRegistry()
		navIndex = make(map[string]navItem, 24)
		for _, g := range navGroupsCache {
			for _, it := range g.items {
				navIndex[it.id] = it
			}
		}
		// 设置项不放进任何分组（侧栏单独画在底部），但同样要能被按 id 找到。
		it := settingsPageItem()
		navIndex[it.id] = it
	})
	return navGroupsCache, navIndex
}

var (
	navOnce        sync.Once
	navGroupsCache []navGroup
	navIndex       map[string]navItem
)

// eachPage 遍历注册表里的所有页面（含无分组标题的首页与底部设置项）。
func eachPage(fn func(navItem)) {
	groups, _ := navAll()
	for _, g := range groups {
		for _, it := range g.items {
			fn(it)
		}
	}
	fn(settingsPageItem())
}

// pageByID 按 id 找页面定义（O(1)，走一次性建好的索引）。
func pageByID(id string) (navItem, bool) {
	_, idx := navAll()
	it, ok := idx[id]
	return it, ok
}

// pageSettingItems 返回声明了「页面级设置」的菜单项（顺序即注册表顺序）。
//
// 「设置 → 页面」与 settingsGroupLabels() 都靠它决定是否显示那一组。数量很少
// （页面数级别），每次重建切片没有性能问题；只有设置页会调用。
func pageSettingItems() []navItem {
	groups, _ := navAll()
	var out []navItem
	for _, g := range groups {
		for _, it := range g.items {
			if it.settings != nil {
				out = append(out, it)
			}
		}
	}
	if it := settingsPageItem(); it.settings != nil {
		out = append(out, it)
	}
	return out
}

// pageTitle 页头标题：注册表里的 label，找不到时退回 id。
func pageTitle(id string) string {
	if it, ok := pageByID(id); ok {
		return it.label
	}
	return id
}

// pageToolsFor 页头右侧图标按钮：来自注册表，避免与菜单定义分家。
func pageToolsFor(id string) []toolAction {
	it, ok := pageByID(id)
	if !ok {
		return nil
	}
	return it.tools
}

// ---- 与架构骨架（internal/view/shell）的桥接 ----

// navGroups 把菜单注册表投影成侧栏骨架需要的分组数据。
//
// 注册表里的 navItem 还带着页面构建函数、工具条、生命周期策略等业务信息，
// 而侧栏只需要「id + 文字 + 图标」。做这次投影后，shell 包不必 import 业务类型，
// 也就不会出现包循环依赖。
//
// 结果是只读的常量数据，构建一次即可：侧栏每帧都要用它，没必要每帧重建一遍
// 分组与 item 切片（那又是十几个分配）。
func navGroups() []shell.Group {
	if navShell != nil {
		return navShell
	}
	reg := navRegistry()
	out := make([]shell.Group, 0, len(reg))
	for _, g := range reg {
		items := make([]shell.Item, 0, len(g.items))
		for _, it := range g.items {
			if it.hidden {
				continue // 隐藏页（如仓库详情）不进行导航投影
			}
			items = append(items, shell.Item{ID: it.id, Label: it.label, Icon: it.icon})
		}
		// 分组里全是隐藏页时，整个分组也不必出现在侧栏（避免空分组标题）。
		if len(items) == 0 && g.title != "" {
			continue
		}
		out = append(out, shell.Group{Title: g.title, Items: items})
	}
	navShell = out
	return out
}

// navShell 是 navGroups 的缓存（只由主线程在首帧构建）。
var navShell []shell.Group

// pageBody 渲染当前页面：注册表里有 build 就用它，否则落到占位页。
//
// 这是「菜单 → 页面」唯一的映射点：新增页面只需改本文件一处。
func (a *App) pageBody(c *ui.Context, t *ui.Theme) {
	if it, ok := pageByID(a.page); ok && it.build != nil {
		it.build(a, c, t)
		return
	}
	placeholderPage(c, t, pageTitle(a.page), a.page)
}
