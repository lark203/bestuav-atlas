package view

import (
	"context"
	"math"
	"os"
	"runtime/debug"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/ELY-Open-Source/CrychicUI/code"
	"github.com/ELY-Open-Source/CrychicUI/sound"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/pet"
	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/shortcut"
	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/ui/icons"
	"bestuav-atlas/internal/ui/kit"
	"bestuav-atlas/internal/view/fx"
	"bestuav-atlas/internal/view/shell"
)

// App 持有窗口状态；View 在主线程按帧从它构建界面。
type App struct {
	page string // 当前选中页 id（由侧栏写入）

	// 侧栏分组的折叠状态（key 为分组标题；缺省视为展开）。
	// 用 map 而不是四个 bool 字段：新增分组时不必再改 App 结构体。
	groupFolded map[string]bool

	// 侧栏整体展开/收缩（false=展开 220px，true=收缩 64px 图标栏）。持久化。
	sidebarCollapsed bool

	// ---- 页面生命周期（见 pagelife.go）----
	pages     map[string]*pageState // 仍在缓存中的页面运行时状态
	pagePrev  string                // 上一帧的页面 id，用于检测切换
	lastEvict time.Time             // 上次闲置淘汰检查的时刻（见 evictIdlePages）

	showDlg bool
	// kpiStart 首页四个 KPI 数字滚动的**帧时刻**起点。零值 = 下一帧开始滚；
	// 每次进入首页由 onEnter 清零，于是切回来会重新滚一遍（见 page_home.go）。
	kpiStart time.Time

	// chartHover 首页趋势折线图的 hover 索引（CrychicUI chart.Line 的跨帧状态）。
	// -1 表示未悬停；悬停时写入序列上的 X 下标，供下一帧高亮同一位置。
	chartHover int

	// 基本信息弹窗开关：由设置页「关于」区的按钮或首页快捷入口打开。
	// 内容见 basicinfo.go（应用标识 / 运行环境 / 页面缓存 / 数据库与日志路径）。
	showBasicInfo bool
	basicTab      int // 弹窗内当前页签（0 应用 / 1 环境 / 2 缓存 / 3 数据与日志 / 4 配置）

	// 用户偏好（持久化到 per-user 配置目录的 JSON 文件）
	settings *settings.Settings

	// UI 绑定值与其“已应用”快照（用于检测变化并触发应用/持久化）。
	// 主题：0 跟随系统 / 1 浅色 / 2 深色
	themeIdx        int
	appliedThemeIdx int
	// 配色方案（强调色系）：索引对应 paletteDefs，存储值见 internal/settings。
	paletteIdx        int
	appliedPaletteIdx int
	// 背景色调：索引对应 toneDefs。
	toneIdx        int
	appliedToneIdx int
	// 关闭行为：0 每次询问 / 1 最小化到托盘 / 2 直接退出
	closeModeIdx        int
	appliedCloseModeIdx int
	// 字体大小（Slider 绑定值，px）
	fontSize      float64
	appliedFontPx float64

	// 首次关闭询问弹窗状态
	showCloseChoice bool
	closeChoice     string // 弹窗内临时选择："minimize" / "quit"
	rememberClose   bool   // 弹窗内“记住我的选择”

	// ---- 背景图与浮层透明度 ----
	// 背景图只在路径变化时读盘并解码一次（bgLoaded 记录已加载的路径），
	// 之后每帧直接复用 *ui.Bitmap，避免逐帧解码导致卡顿。
	bgBitmap *ui.Bitmap
	bgLoaded string // 已加载到 bgBitmap 的路径（"" = 未加载）
	bgError  string // 最近一次加载失败的原因（设置页展示）
	// bgLoadingFor 是正在后台解码的路径；非空时设置页显示「加载中…」。
	bgLoadingFor string
	// bgGen 是解码代次：换图/清除时递增，用来丢弃迟到的旧解码结果。
	bgGen uint64

	// 浮层不透明度（0.2–1.0）：设置背景图后侧栏/内容区半透明，背景图透出。
	// 未设置背景图时恒为不透明，保持主题默认外观。
	menuOpacity      float64
	contentOpacity   float64
	appliedMenuOp    float64
	appliedContentOp float64

	// 每帧算出的浮层不透明度（0..1）。没有背景图时都是 1（完全不透明），
	// 由 applyBackdropTheme 统一赋值，供 menuSurface / windowSurface 使用。
	menuPanelAlpha    float64
	contentPanelAlpha float64

	// 页面缓存闲置销毁时长（分钟，0=不销毁）。Stepper 绑定 float64，故单列一份。
	pageIdle        float64
	appliedPageIdle float64

	// 多实例开关（保存后需重启生效：单实例锁在进程启动时获取）。
	allowMultiInstance bool
	appliedMulti       bool

	// 开机自启开关：变化时调用 mygo 的登录项 API，并持久化。
	openAtLogin        bool
	appliedOpenAtLogin bool

	// shortcuts 是运行时绑定表（动作 id → 解析后的键位），由 rebuildShortcuts 重建。
	shortcuts map[string]shortcut.Binding
	// sidebarToggleHint 侧栏开关悬浮提示里的键位文本（如「Ctrl+B」），由
	// rebuildShortcuts 刷新：每帧现算要多做一次字符串解析与分配。
	sidebarToggleHint string
	// shortcutCapture 非空时正在设置页录制该动作的新键位（此时不登记全局快捷键）。
	shortcutCapture string

	// ---- 轻量级任务进度 ----
	// taskRunning 为 true 时，页头下方那条分割线的槽位改画进度条。
	taskRunning  bool
	taskProgress float64 // 0..1；负值表示“不确定进度”（无限循环动画）
	taskLabel    string  // 当前任务名（显示在页头标题右侧）

	// ---- 重量级任务（全局遮罩）----
	// heavyRunning 为 true 时，整个窗口罩上一层不可关闭的遮罩并拦截所有操作。
	heavyRunning  bool
	heavyProgress float64
	heavyLabel    string

	// ---- 任务列表（异步任务页）----
	tasks    []taskRow
	taskSort ui.SortOrder // 任务队列表头排序态（CrychicUI data.DataTable 翻转）

	// ---- 彩带庆祝（任务完成 / 手动触发）----
	bursts      []*fx.Burst
	nextBurstID uint64

	// ---- 轻提示（toast）----
	toastMsg   string
	toastUntil time.Time

	// ---- 设置写盘（防抖）----
	// settingsDirty 表示内存里的偏好还没落盘；saveTimer 是攒批计时器。
	// 二者只在主线程读写（见 persist / flushSettings）。
	settingsDirty bool
	saveTimer     *time.Timer

	// ---- 检查更新（mygo.Updater：进入设置页后台检查，由用户决定是否安装）----
	// 数据源、下载、就地替换与重启全部由 mygo 内置 Updater 负责
	// （GitHub Release + ed25519 签名校验）。开发构建（go run）下 Updater 被禁用，
	// UI 优雅降级为提示「正式发布包才支持自动更新」。
	updateChecking  bool         // 正在后台检查
	updateDisabled  bool         // 当前构建未启用自动更新（开发构建或未配置更新源）
	updateChecked   bool         // 已完成过一次成功的检查（区分「已是最新」与「尚未检查」）
	availableUpdate *mygo.Update // 检查到的新版本（nil = 无更新 / 未检查 / 出错）
	updateErr       error        // 检查或安装错误
	updateDismissed bool         // 本次会话是否已「忽略」新版本横幅
	// safeMode 是 --safe-mode 启动：不接数据库、不查更新，设置页会写明原因。
	safeMode         bool
	lastUpdateCheck  time.Time // 上一次检查结束的时刻（自动检查节流用）
	updateInstalling bool      // 正在下载并安装
	updateProgress   float64   // 安装进度 0..1
	showUpdateDialog bool      // 标题栏「更新」按钮弹出的确认弹窗

	// pendingImport 待导入的配置包路径：设置页按钮只登记，下一帧开头执行
	// （导入要关数据库、替换文件，必须在主线程且不打断正在构建的界面）。
	pendingImport string
	// pendingImportTemp 表示 pendingImport 是下载到临时目录的文件，导入后要删掉。
	pendingImportTemp bool
	// repoBusy 仓库同步请求进行中（避免重复点击）。
	repoBusy bool

	// ---- 星图业务（待办 / 项目 / 仓库，见 atlas_data.go / atlas_dialogs.go）----
	// atlasCache 是三个业务页共用的数据库快照（写操作后失效、下一帧重读）；
	// atlasUI 是弹窗、筛选与多选状态。
	atlasCache atlasCache
	atlasUI    atlasUI

	// ---- 伴生桌宠（见 pet.go） ----
	petWin         *mygo.Window // 桌宠小窗（nil = 未创建）
	petApplied     bool         // 已应用的开关状态（用于检测变化）
	petStage       int          // 成长阶段（由 petXP 推导）
	petXP          float64      // 当前陪伴分钟数
	petSavedXP     float64      // 已写盘的陪伴分钟数（每满 1 分钟才写）
	petTickAt      time.Time    // 上次结算时间
	petLastBreak   time.Time    // 上次「休息」时间（久坐提醒基准）
	petBubbleUntil time.Time    // 气泡显示到何时
	petBubbleText  string       // 气泡文字
	petPollAt      time.Time    // 上次轮询窗口位置
	petLastPoke    time.Time    // 上次拖动/互动（60 秒不动就躺下）

	// ---- 基本信息页的运行时快照 ----
	env envSnapshot

	// ---- 生成 license 页的表单与结果 ----
	lic licForm

	// ---- 组件演示页的控件状态 ----
	demoToggle, demoToggle2 bool
	demoRating              int
	demoText, demoNote      string
	demoDateT               time.Time
	demoSelect              string
	demoCheck, demoSwitch   bool
	demoSlider, demoStep    float64
	demoTable               ui.ListState
	demoSel                 int
	demoTab, demoSeg        int
	demoCollapse            bool

	// ---- CrychicUI 对照页状态（datetime / code 包；不改动任何原有页面）----
	cryPick, cryMini, cryNav, cryHeatFrom time.Time
	cryJSON                               string
	cryCodeTab, cryConsoleFilter          int
	cryCmdHistory                         []code.Command
	cryCmdDraft                           string
	crySplit                              bool

	// ---- 功能演示状态 ----
	todoInput    string       // 待办输入框（页面状态在 pageState.data 里，输入框绑在 App 上）
	chartSeries  [3][]float32 // 滚动图表的三个序列
	chartTick    int
	chartPaused  bool
	vlistState   ui.ListState
	vlistSel     int
	timelinePlay bool

	// 由 main 注入的窗口重绘触发器：后台任务更新进度后需主动请求重绘，
	// 否则界面要等下一次交互才刷新。
	invalidate func()

	// 由 main 注入的关闭执行器：隐藏/退出这类需要「应用级」语义的行为通过回调
	// 交给 main 执行（那里才知道 quitting 标志与托盘状态）。
	closeHandler func(mode string)

	// 由 main 注入的主窗口句柄（WhenReady 里创建窗口后注入）。
	// 只有「开子窗口」这类必须拿父窗口的能力才用它（目前仅浮出面板示例）：
	// 离屏渲染与单测环境下为 nil，相关入口自动降级（不弹出子窗口）。
	win *mygo.Window

	// ---- 本地持久化（见 persist.go）----
	// store 可为 nil：数据库打开失败或单测环境时，全部读写退化为纯内存。
	store     *store.Store
	dbStats   store.Stats // 库计数快照（带 TTL，避免每帧 COUNT）
	dbStatsAt time.Time
	// prefMem 页面私有键的内存缓存（见 prefs.go）：库不可用时退化为纯内存，
	// 可用时读穿透并回填，省掉页面每帧读同一个键的查询。
	prefMem map[string]string

	// ---- 官方示例页的运行时状态 ----
	// （页面实现见 page_loading.go / page_markdown_chat.go / page_notes.go）
	loadingSize   int  // 指示器尺寸档位（对应 loadingSizes）
	loadingColor  int  // 颜色档位：正文 / 强调 / 次要
	loadingEasing int  // 缓动档位
	loadingCap    int  // 端点档位
	loadingPaused bool // 暂停动画

	chatReplies int    // 已用回复数（轮换回复文案）
	chatReveal  int    // 流式单位档位
	chatInput   string // 输入框内容
	chatMD      markdown.Options

	piMuted bool // 静音开关（Switch 绑在 App 上，再同步给 Piano）

}

// NewApp 构造窗口初始状态：加载已保存偏好，并按其应用主题。
// NewApp 读取用户偏好并构造 App（读盘失败时用默认值，并写一条警告）。
func NewApp() *App {
	s, err := settings.Load()
	if err != nil {
		// 以前这里走 MustLoad 静默吞掉错误：用户只会看到「我的设置怎么全没了」，
		// 日志里却没有任何线索。现在至少留下是哪一步失败（读文件 / 解析 JSON）。
		logging.Warn("settings", "读取设置失败，本次使用默认值", "err", err.Error())
	}
	return newApp(s)
}

// newApp 用给定偏好构造 App，不碰磁盘。
//
// 单独拆出来是为了让测试能用 settings.Default() 构造确定的初始状态：直接
// NewApp() 会读取开发机上的真实 settings.json，测试结果会随「我本机是否收起
// 过侧栏 / 是否设过背景图」而变化，属于典型的测试不确定源。
func newApp(s *settings.Settings) *App {
	a := &App{
		page:             "home",
		groupFolded:      map[string]bool{},
		pages:            map[string]*pageState{},
		sidebarCollapsed: s.SidebarCollapsed,
		settings:         s,
		chartHover:       -1,
		fontSize:         s.FontSize,
		// 背景与浮层透明度
		menuOpacity:    s.MenuOpacity,
		contentOpacity: s.ContentOpacity,
		// 多实例开关
		allowMultiInstance: s.AllowMultiInstance,
		openAtLogin:        s.OpenAtLogin,
		appliedOpenAtLogin: s.OpenAtLogin,
		// 页面缓存闲置销毁
		pageIdle:        float64(s.PageIdleMinutes),
		appliedPageIdle: float64(s.PageIdleMinutes),
		// 任务列表初始演示数据
		tasks: demoTaskRows(),
		// 表单初始值
		lic: defaultLicForm(),
		// 组件演示页的默认值（选中项用 -1 表示「未选中」）
		demoSelect: "专业版",
		demoRating: 3,
		demoSlider: 42,
		demoStep:   4,
		demoSel:    -1,
		vlistSel:   -1,
		// CrychicUI 对照页初始数据
		cryHeatFrom: time.Date(2026, 3, 1, 0, 0, 0, 0, time.Local),
		cryPick:     time.Now(),
		cryMini:     time.Now(),
		cryNav:      time.Now(),
		cryJSON:     `{"name":"bestuav","version":"1.0.0","features":["task","drone","license"],"released":true}`,
		cryCmdHistory: []code.Command{
			{Text: "go run .", Output: "bestuav-atlas listening on :8080"},
			{Text: "ls tasks", Output: "task-001  task-002  task-003"},
		},
		// 加载指示器默认 24px（loadingSizes 第 2 档）
		loadingSize: 1,
	}
	a.themeIdx = themeIdxOf(s.Theme)
	a.appliedThemeIdx = a.themeIdx
	a.paletteIdx = paletteIndex(s.Palette)
	a.appliedPaletteIdx = a.paletteIdx
	a.toneIdx = toneIndex(s.Tone)
	a.appliedToneIdx = a.toneIdx
	a.petXP = s.PetXP
	a.petSavedXP = s.PetXP
	a.petStage = pet.Stage(s.PetXP)
	a.petLastBreak = time.Now()
	a.closeModeIdx = closeModeIdxOf(s.CloseMode)
	a.appliedCloseModeIdx = a.closeModeIdx
	a.appliedFontPx = math.Round(s.FontSize)
	a.appliedMenuOp = s.MenuOpacity
	a.appliedContentOp = s.ContentOpacity
	a.appliedMulti = s.AllowMultiInstance

	// 注意：此处只初始化索引，不调用 SetSource。主题 source 的切换必须在
	// 应用主循环就绪后（mygo.App.WhenReady 内）进行——SetSource 内部走
	// onMain，未启动 App 时（如单测环境）会永久阻塞。
	a.rebuildShortcuts()
	return a
}

// ApplyTheme 在应用主循环就绪后（mygo.App.WhenReady 内）应用已保存的主题
// source。请勿在 NewApp 或 App.Run 之前调用。
func (a *App) ApplyTheme() {
	// 关闭 CrychicUI 组件默认的点击/悬停合成音效：本应用是工控/科学工具，
	// 不需要每个可交互控件都响一声（见 docs/20261103_MyGo与CrychicUI选型结论.md §七）。
	sound.SetEnabled(false)
	a.applyThemeSource()
}

// ---- 偏好 <-> UI 索引 转换 ----

func themeIdxOf(s string) int {
	switch s {
	case "light":
		return 1
	case "dark":
		return 2
	default:
		return 0
	}
}

func themeName(i int) string {
	switch i {
	case 1:
		return "light"
	case 2:
		return "dark"
	default:
		return "system"
	}
}

func closeModeIdxOf(s string) int {
	switch s {
	case "minimize":
		return 1
	case "quit":
		return 2
	default:
		return 0
	}
}

func closeModeName(i int) string {
	switch i {
	case 1:
		return "minimize"
	case 2:
		return "quit"
	default:
		return "ask"
	}
}

// applyThemeSource 把当前 themeIdx 映射到 mygo 主题源并应用。
func (a *App) applyThemeSource() {
	switch a.themeIdx {
	case 1:
		mygo.Theme.SetSource(mygo.ThemeLight)
	case 2:
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

// settingsSaveDelay 是偏好写盘的防抖窗口：停手 300ms 后落盘。
const settingsSaveDelay = 300 * time.Millisecond

// persist 标记偏好已变化，并安排一次防抖写盘。
//
// 为什么不在这一帧直接写：拖动字号/不透明度滑块时，数值每个像素变一次，
// 直接 Save 会在一秒内写几十次临时文件 + rename——机械盘和杀毒软件上足以
// 看出卡顿。这里攒 300ms，用户停手后再落盘；退出前由 FlushSettings 兜底。
func (a *App) persist() {
	a.settingsDirty = true
	if a.saveTimer != nil {
		a.saveTimer.Stop()
	}
	a.saveTimer = time.AfterFunc(settingsSaveDelay, func() {
		mygo.RunOnMain(a.flushSettings)
	})
}

// FlushSettings 立即把待写的偏好落盘（退出前由 main 调用）。
func (a *App) FlushSettings() {
	if a.saveTimer != nil {
		a.saveTimer.Stop()
		a.saveTimer = nil
	}
	a.flushSettings()
}

// flushSettings 真正写盘；没有待写内容时什么也不做。
func (a *App) flushSettings() {
	if !a.settingsDirty {
		return
	}
	a.settingsDirty = false
	if err := a.settings.Save(); err != nil {
		logging.Errv("settings", "保存设置失败", err)
	}
}

// SetCloseHandler 由 main 注入关闭执行器（隐藏或退出窗口）。
func (a *App) SetCloseHandler(fn func(mode string)) { a.closeHandler = fn }

// SetInvalidator 由 main 注入窗口重绘触发器（win.Invalidate）。后台任务
// 更新进度后调用它请求重绘。
func (a *App) SetInvalidator(fn func()) { a.invalidate = fn }

// SetWindow 由 main 注入主窗口句柄。浮出面板等子窗口需要它作为 Parent；
// 未注入（离屏渲染 / 单测）时为 nil，相关入口自动降级。
func (a *App) SetWindow(w *mygo.Window) { a.win = w }

// SetPage 设置启动页（命令行 --page / 单实例转发用）。不存在的 id 忽略。
func (a *App) SetPage(id string) {
	if id == "" {
		return
	}
	if _, ok := pageByID(id); ok {
		a.page = id
	}
}

// saveSidebarCollapsed 写回侧栏折叠偏好。
//
// 侧栏底部开关与快捷键都走这里：前者由 shell 翻转 *a.sidebarCollapsed 后回调，
// 后者自己翻转再调用，持久化行为因此只有一处。
func (a *App) saveSidebarCollapsed(collapsed bool) {
	a.settings.SidebarCollapsed = collapsed
	a.persist()
}

// toggleSidebar 收起/展开侧栏（快捷键 Ctrl+B）。
func (a *App) toggleSidebar() {
	a.sidebarCollapsed = !a.sidebarCollapsed
	a.saveSidebarCollapsed(a.sidebarCollapsed)
}

// SetSafeMode 标记安全模式（--safe-mode）：跳过更新检查，设置页给出说明。
func (a *App) SetSafeMode(on bool) {
	a.safeMode = on
	if on {
		a.updateDisabled = true
	}
}

// requestFrame 请求一次重绘；未注入（如单测环境）时静默忽略。
func (a *App) requestFrame() {
	if a.invalidate != nil {
		a.invalidate()
	}
}

// ---- 背景图 ----

// ensureBackground 保证 bgBitmap 与设置中的背景图路径一致：路径变化才读盘解码。
//
// 解码放在后台 goroutine（大图要几百毫秒，在主线程做会让窗口卡住），完成后
// 回主线程换图并请求重绘；期间设置页显示「加载中…」。
// bgGen 是代次号：换图/清除时递增，迟到的旧结果直接丢弃，避免「选了 A 又选 B，
// A 解码完把 B 盖掉」。
func (a *App) ensureBackground() {
	want := a.settings.BackgroundImage
	if want == a.bgLoaded || want == a.bgLoadingFor {
		return // 未变化：复用已解码位图；已在解码中：等它回来
	}
	a.bgError = ""
	if want == "" {
		// 同步清除，同时作废在途解码。
		a.bgGen++
		a.bgLoadingFor = ""
		a.bgBitmap = nil
		a.bgLoaded = ""
		return
	}
	a.bgGen++
	gen := a.bgGen
	a.bgLoadingFor = want
	go func() {
		// 后台协程里的 panic 不会经过 View 的 recover，单独兜一层。
		defer logging.Recover("bg-decode")
		data, err := os.ReadFile(want)
		if err != nil {
			mygo.RunOnMain(func() {
				if a.bgGen != gen {
					return // 期间又换了图
				}
				a.bgLoadingFor = ""
				a.bgLoaded = want
				a.bgBitmap = nil
				a.bgError = "读取失败：" + err.Error()
				a.requestFrame()
			})
			return
		}
		bmp, err := decodeBackground(data, maxBgWidth)
		data = nil // 让压缩数据尽早可回收（ui.NewBitmap 不持有它）
		// 解码一张 2400 万像素的照片会临时分配几十 MB（YCbCr + 目标位图），
		// 而 Go 的 scavenger 要过几分钟才把空闲页还给系统——表现为「选完图
		// 任务管理器里的内存一直不回落」。换图是一次性动作，这里直接回收。
		debug.FreeOSMemory()
		mygo.RunOnMain(func() {
			if a.bgGen != gen {
				return
			}
			a.bgLoadingFor = ""
			a.bgLoaded = want
			if err != nil {
				a.bgBitmap = nil
				a.bgError = "解码失败：" + err.Error()
			} else {
				a.bgBitmap = bmp
				a.bgError = ""
			}
			a.requestFrame()
		})
	}()
}

// chooseBackground 弹出系统原生的文件选择框挑选背景图。
//
// 对话框会阻塞调用方直到用户关闭，故放到独立 goroutine：不阻塞 UI 主循环；
// 选中后回到主线程更新状态并请求重绘。
func (a *App) chooseBackground() {
	go func() {
		// 协程里的 panic 不经过 View 的 recover：没有这一层就是「进程直接消失、
		// 日志里什么都没有」。同 check-updates / bg-decode 等协程保持一致。
		defer logging.Recover("choose-background")
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Title:       "选择背景图片",
			ButtonLabel: "设为背景",
			Filters: []mygo.FileFilter{{
				Name:       "图片",
				Extensions: []string{"png", "jpg", "jpeg", "webp", "bmp", "gif"},
			}},
		})
		if err != nil || len(paths) == 0 {
			return
		}
		chosen := paths[0]
		mygo.RunOnMain(func() {
			a.settings.BackgroundImage = chosen
			a.ensureBackground() // 立即加载，失败原因可在设置页看到
			a.persist()
			a.requestFrame()
		})
	}()
}

// resetBackground 移除背景图并恢复默认浮层不透明度（设置页的「恢复默认」）。
func (a *App) resetBackground() {
	a.settings.BackgroundImage = ""
	a.settings.MenuOpacity = settings.DefaultMenuOpacity
	a.settings.ContentOpacity = settings.DefaultContentOpacity
	a.menuOpacity = a.settings.MenuOpacity
	a.contentOpacity = a.settings.ContentOpacity
	a.appliedMenuOp = a.menuOpacity
	a.appliedContentOp = a.contentOpacity
	a.ensureBackground() // 释放位图（路径为空）
	a.persist()
}

// windowSurface 内容区浮层的填充色：有背景图时按「内容区不透明度」半透明，
// 让背景图透出；没有背景图时用主题默认背景（不透明）。
func (a *App) windowSurface(t *ui.Theme) ui.Color {
	return kit.Alpha(t.Background, a.contentPanelAlpha)
}

// menuSurface 侧栏浮层的填充色，语义同 windowSurface（用菜单区不透明度）。
func (a *App) menuSurface(t *ui.Theme) ui.Color {
	return kit.Alpha(t.Surface, a.menuPanelAlpha)
}

// applyBackdropTheme 设置背景图时，把主题里的「控件面」相关颜色整体降透明度，
// 让卡片、按钮、输入框、表格与各种悬停底色不再像白板一样挡住背景图。
//
// 为什么这样做：mygo 的主题是整帧共享的（c.Theme() 返回同一份 *Theme），
// 因此无法只对某个子树生效；反过来，这也正好让我们「一处调整、全局一致」。
// 各组件（ui.Button / ui.Table / ui.Segmented / ui.Slider / 我们自己的卡片）
// 都从这些字段取色，所以无需逐个改组件。
//
// 不参与调整的：Text / Border / Accent 等语义色——文字、描边、强调色与选中态
// 需要保持对比度，半透明化会直接伤害可读性。
func (a *App) applyBackdropTheme(t *ui.Theme) {
	// 默认（无背景图）：全部不透明。
	a.menuPanelAlpha, a.contentPanelAlpha = 1, 1
	surface, hover, pressed := 1.0, 1.0, 1.0

	if a.bgBitmap != nil {
		a.menuPanelAlpha = kit.Clamp01(a.menuOpacity)
		a.contentPanelAlpha = kit.Clamp01(a.contentOpacity)

		// 控件面（卡片/按钮/输入框/分段控件底）：比它所在的浮层「再实一点」。
		// 若与浮层同透明度，卡片会与背景浮层融为一体，层次只剩 1px 描边。
		// 取「向不透明方向补一半剩余差值」：内容区 54% → 卡片 77%。
		surface = kit.Clamp01(a.contentPanelAlpha + (1-a.contentPanelAlpha)*0.5)
		// 悬停底比控件面「虚一点」但不弱于浮层：既能在照片上看出悬停，
		// 又让「选中的分段/页签」（用 Surface 画）比它更实，选中态才读得出来。
		hover = kit.Clamp01(a.contentPanelAlpha + (surface-a.contentPanelAlpha)*0.35)
		// 按下比控件面更实，给足点击反馈。
		pressed = kit.Clamp01(surface + (1-surface)*0.5)
	}

	t.Surface = kit.Alpha(t.Surface, surface)
	t.SurfaceHover = kit.Alpha(t.SurfaceHover, hover)
	t.SurfacePressed = kit.Alpha(t.SurfacePressed, pressed)
	t.Background = kit.Alpha(t.Background, a.contentPanelAlpha)
}

// ---- 轻量级任务进度 ----

// startLightTask 演示一条「轻量级任务」：进度条画在页头下方分割线的槽位里。
// 真实业务只需把 goroutine 里的模拟循环换成实际工作，并在完成时调用 endTask。
func (a *App) startLightTask(label string) {
	if a.taskRunning {
		return // 同一时刻只显示一条轻量任务进度
	}
	a.taskRunning = true
	a.taskProgress = 0
	a.taskLabel = label

	go func() {
		// 后台协程里的 panic 不会经过 View 的 recover，单独兜一层。
		defer logging.Recover("light-task")
		const steps = 24
		for i := 1; i <= steps; i++ {
			time.Sleep(90 * time.Millisecond)
			v := float64(i) / steps
			mygo.RunOnMain(func() {
				a.taskProgress = v
				a.requestFrame()
			})
		}
		mygo.RunOnMain(a.endTask)
	}()
}

// endTask 结束轻量任务：分割线恢复常态，并撒彩带庆祝（用户要求的「完成时烟花/彩带秀」）。
func (a *App) endTask() {
	// 完成的轻量任务也进队列（与重量级对称）：不记的话「跑过什么」无从查起，
	// 状态列也就没有脉冲可看。标签要在清空之前读。
	if label := a.taskLabel; label != "" {
		a.pushTaskRow(label, "轻量级", "已完成")
		// 窗口在托盘里时界面效果看不见，补一条系统通知。
		a.notifyTaskDone(label + "（轻量级）")
	}
	a.taskRunning = false
	a.taskProgress = 0
	a.taskLabel = ""
	a.celebrate(3)
	a.requestFrame()
}

// runClose 通过注入的回调执行关闭行为。
func (a *App) runClose(mode string) {
	if a.closeHandler != nil {
		a.closeHandler(mode)
	}
}

// updateCheckTimeout 是单次更新检查的等待上限。
//
// 没有超时时，断网/网络黑洞会让 Check 卡在 TCP 超时上（可达一两分钟），
// 期间「正在检查」的呼吸点一直转，用户以为程序卡死。
const updateCheckTimeout = 15 * time.Second

// updateCheckThrottle 是自动检查的最小间隔：反复进出设置页不必每次都打网络。
const updateCheckThrottle = 10 * time.Minute

// checkForUpdates 在后台协程里检查更新：进入设置页时自动触发（force=false），
// 也可由标题栏图标 / 设置页按钮手动触发（force=true，跳过节流）。
//
// 设计要点：
//   - 数据源、下载、就地替换与重启全部交给 mygo 内置 Updater（GitHub Release +
//     ed25519 签名校验），本函数只负责「发起检查 / 收结果 / 切回主线程更新 UI」。
//   - 开发构建（go run / 普通 go build）下 Updater 被禁用：先判 Enabled()，
//     禁用时直接标记 updateDisabled，UI 提示「正式发布包才支持自动更新」。
//   - 仅「提示」不「自动更新」：发现新版本只把信息交回 UI，由用户点击「立即更新」
//     决定是否下载安装并重启（见 installUpdate / updateSection）。
func (a *App) checkForUpdates(force bool) {
	// 安全模式（--safe-mode）不查更新：这是给「怀疑是更新/数据库出问题」时的
	// 排查通道，一旦在这里发网络请求，安全模式就证明不了任何事。
	// 注意必须挡在下面那行 `updateDisabled = false` 之前——否则手动点一下标题栏
	// 图标就会把安全模式的标记抹掉，紧接着照常检查（见 TestSafeModeSkipsUpdateCheck）。
	if a.safeMode {
		return
	}
	if a.updateChecking || a.updateInstalling {
		return // 同一时刻只跑一次
	}
	// 自动检查节流：10 分钟内只查一次（手动点按钮不受限）。
	if !force && !a.lastUpdateCheck.IsZero() && time.Since(a.lastUpdateCheck) < updateCheckThrottle {
		return
	}
	a.updateChecking = true
	a.updateDisabled = false
	a.updateChecked = false
	a.availableUpdate = nil
	a.updateErr = nil
	// 重新检查就恢复提示：否则「忽略」之后即使点标题栏图标重查，
	// 胶囊也永远不出现（只能离开设置页再进来），用户会以为没法更新了。
	a.updateDismissed = false
	a.requestFrame()

	go func() {
		// 后台协程里的 panic 不会经过 View 的 recover，单独兜一层。
		defer logging.Recover("check-updates")
		if !mygo.Updater.Enabled() {
			mygo.RunOnMain(func() {
				a.updateChecking = false
				a.updateDisabled = true
				a.lastUpdateCheck = time.Now()
				a.requestFrame()
			})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
		defer cancel()
		up, err := mygo.Updater.Check(ctx)
		mygo.RunOnMain(func() {
			a.updateChecking = false
			a.lastUpdateCheck = time.Now()
			a.updateErr = err
			// err == nil 才算「真的检查过了」：此时 up 为 nil 表示已是最新。
			// 出错时保持 false，UI 走「检查失败」分支而不是「已是最新」。
			a.updateChecked = err == nil
			a.availableUpdate = up
			// 上一轮「忽略」过的版本，重启后依然不该再弹（见 settings.IgnoredVersion）。
			if up != nil && up.Version == a.settings.IgnoredVersion {
				a.updateDismissed = true
			}
			a.requestFrame()
		})
	}()
}

// installUpdate 下载并安装检查到的新版本，完成后由 mygo 重启应用。
//
// mygo.Updater 会：下载签名归档 → 校验 ed25519 → 就地替换应用；本函数随后调用
// App.Relaunch() 重启。安装期间用 updateProgress 驱动进度条；失败则回主线程记录
// 错误并保留界面，由用户重试。
func (a *App) installUpdate() {
	// 安全模式连安装也不做：装完会重启进程并换掉可执行文件，那是排查状态下
	// 最不该发生的事。availableUpdate 在安全模式下本来就不会被填上，这里再挡一道。
	if a.safeMode {
		return
	}
	if a.availableUpdate == nil || a.updateInstalling {
		return
	}
	a.updateInstalling = true
	a.updateProgress = 0
	a.requestFrame()

	up := a.availableUpdate
	go func() {
		defer logging.Recover("update-install")
		err := up.Install(context.Background(), func(downloaded, total int64) {
			if total <= 0 {
				return
			}
			p := float64(downloaded) / float64(total)
			mygo.RunOnMain(func() {
				a.updateProgress = p
				a.requestFrame()
			})
		})
		mygo.RunOnMain(func() {
			a.updateInstalling = false
			if err != nil {
				a.updateErr = err
				a.requestFrame()
				return
			}
			// 安装成功：新版本已就位，重启应用以加载它（进程在此退出）。
			mygo.App.Relaunch()
		})
	}()
}

// HandleClose 是窗口关闭按钮的回调（由 main 的 win.OnClose 转发）。
//   - CloseMode=="minimize"：最小化到托盘
//   - CloseMode=="quit"：直接退出
//   - CloseMode=="ask"（默认）：弹出首次关闭询问，交由用户本次决定
func (a *App) HandleClose(e *mygo.CloseEvent, quitting *bool) {
	if *quitting {
		return // 系统退出流程进行中，放行
	}
	switch a.settings.CloseMode {
	case "minimize":
		e.PreventDefault()
		a.runClose("minimize")
	case "quit":
		e.PreventDefault()
		a.runClose("quit")
	default: // "ask"
		e.PreventDefault()
		a.closeChoice = "minimize"
		a.rememberClose = false
		a.showCloseChoice = true
	}
}

// View 由 mygo 每帧调用，按当前状态构建整个界面。
func (a *App) View(c *ui.Context) {
	// 渲染期 panic 会直接崩掉整个进程：先落一条 CRASH 日志（含堆栈）并入库，
	// 再按原样抛出，保持既有崩溃行为不变（见 logging.Recover 的说明）。
	defer logging.Recover("view")

	t := c.Theme()

	// 全局快捷键：放在最前，切页/关弹窗在本帧就生效（见 shortcuts.go）。
	a.handleShortcuts(c)

	// 待处理的配置导入：会替换设置与数据库，必须在构建任何界面之前、在主线程完成。
	a.applyPendingImport()

	// 桌宠：累积陪伴时长、久坐提醒、按开关创建/关闭小窗（见 pet.go）。
	a.petTick(time.Now())

	// 0) 页面生命周期：处理「进入 / 离开 / 闲置销毁」，必须在构建界面前完成，
	//    这样 keepFresh 页面在本帧就是全新的状态（见 pagelife.go）。
	a.syncPages()

	// 1) 主题源：仅在变化时切换（SetSource 会触发整树重建，不可每帧调用）。
	if a.themeIdx != a.appliedThemeIdx {
		a.applyThemeSource()
		a.appliedThemeIdx = a.themeIdx
		a.settings.Theme = themeName(a.themeIdx)
		a.persist()
	}
	// 1.5) 配色方案：只存偏好，颜色在下面每帧应用（applyPalette 绝对赋值，幂等）。
	if a.paletteIdx != a.appliedPaletteIdx {
		a.settings.Palette = paletteName(a.paletteIdx)
		a.appliedPaletteIdx = a.paletteIdx
		a.persist()
	}
	// 1.6) 背景色调：同上，只存偏好；颜色在 backdrop 之前应用。
	if a.toneIdx != a.appliedToneIdx {
		a.settings.Tone = toneName(a.toneIdx)
		a.appliedToneIdx = a.toneIdx
		a.persist()
	}
	// 2) 关闭行为：变化时持久化（关闭时实时读取 settings.CloseMode）。
	if a.closeModeIdx != a.appliedCloseModeIdx {
		a.settings.CloseMode = closeModeName(a.closeModeIdx)
		a.appliedCloseModeIdx = a.closeModeIdx
		a.persist()
	}
	// 3) 字体大小：整数 px 变化时更新并持久化（避免拖动时频繁写盘）。
	rounded := math.Round(a.fontSize)
	if rounded != a.appliedFontPx {
		a.settings.FontSize = rounded
		a.appliedFontPx = rounded
		a.persist()
	}
	// 4) 每帧应用字号（绝对赋值，幂等；确保各页面一致缩放）。
	if a.settings.FontSize > 0 {
		t.FontSize = float32(a.settings.FontSize)
	}
	// 5) 滚动条：保留主题默认的可见性（内容区需要滚动条）；侧栏的滚动条在
	//    shell.Sidebar 内部单独隐藏（见 internal/view/shell/layout.go）。
	// 6) 浮层不透明度：保留两位小数比较，避免拖动滑块时每个像素都写盘。
	mo := math.Round(a.menuOpacity*100) / 100
	co := math.Round(a.contentOpacity*100) / 100
	if mo != a.appliedMenuOp || co != a.appliedContentOp {
		a.settings.MenuOpacity = mo
		a.settings.ContentOpacity = co
		a.appliedMenuOp, a.appliedContentOp = mo, co
		a.persist()
	}
	// 6.5) 页面缓存闲置销毁时长（分钟，0=不销毁）：整数变化才写盘。
	pi := math.Round(a.pageIdle)
	if pi != a.appliedPageIdle {
		a.settings.PageIdleMinutes = int(pi)
		a.appliedPageIdle = pi
		a.persist()
	}
	// 7) 多实例开关：仅持久化，重启后由 main 在启动时读取生效。
	if a.allowMultiInstance != a.appliedMulti {
		a.settings.AllowMultiInstance = a.allowMultiInstance
		a.appliedMulti = a.allowMultiInstance
		a.persist()
	}
	// 7.5) 开机自启：写系统登录项（Windows Run / macOS 登录项 / Linux autostart）。
	if a.openAtLogin != a.appliedOpenAtLogin {
		a.settings.OpenAtLogin = a.openAtLogin
		a.appliedOpenAtLogin = a.openAtLogin
		a.persist()
		a.applyOpenAtLogin()
	}
	// 8) 背景图：路径变化时重新读取并解码（只做一次，非每帧）。
	a.ensureBackground()
	// 9) 有背景图时把「控件面」整体降透明度（卡片/按钮/输入框不再挡住背景），
	//    同时算出侧栏与内容区两层浮层的透明度；无背景图时全部为不透明。
	//    色调必须在 backdrop 之前：先定基色，再按浮层透明度降 alpha。
	applyTone(t, toneName(a.toneIdx))
	a.applyBackdropTheme(t)
	// 9.5) 配色方案：覆盖强调色系。放在 backdrop 之后——强调色不参与半透明化，
	//      但顺序固定下来可以避免以后有人把颜色调整塞进 backdrop 时互相覆盖。
	applyPalette(t, paletteName(a.paletteIdx))

	if a.bgBitmap != nil {
		// 有背景图时：背景图作为常规流的第一个子元素（因此绘制在最底层），
		// 界面主体改为绝对定位铺满并叠在其上。
		//
		// 为什么不用「背景图 Absolute + 主体常规流」：mygo 的绝对定位元素
		// 是在所有常规流兄弟之后绘制的（见 ui/paint.go 的绘制顺序），会盖住
		// 界面，故反过来做。
		ui.Column(c).Fill().Children(func() {
			ui.Image(c, a.bgBitmap).Fit(ui.Cover).Fill()
			ui.Row(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Children(func() {
				a.shell(c, t)
			})
		})
	} else {
		ui.Row(c).Fill().Children(func() {
			a.shell(c, t)
		})
	}
	a.dialog(c, t)
	a.closeChoiceDialog(c, t)
	a.basicInfoDialog(c, t)
	a.updateDialog(c, t)
	a.atlasDialogs(c, t)
	// 重量级任务遮罩：盖在最上层并拦截一切操作（用户要求「完成前禁止操作」）。
	a.heavyTaskMask(c, t)
	// 彩带庆祝：任务完成或手动触发时撒一把，飘完自动清空。
	a.drawCelebration(c)
	// 轻提示（已复制 / 已重置等）。
	a.drawToast(c, t)
}

// shell 应用主体骨架：左侧导航栏 + 右侧内容区。
//
// 两块都由 internal/view/shell 绘制，这里只做「装配」——把当前页、折叠态、
// 页面内容与工具条以指针/回调的形式交给骨架：
//   - 改骨架（间距、页头、分割线、滚动行为）不牵扯任何业务页面；
//   - 新增业务页面也不需要碰骨架，只在 registry.go 登记即可。
func (a *App) shell(c *ui.Context, t *ui.Theme) {
	shell.Sidebar(c, t,
		&shell.NavState{
			Page:      &a.page,
			Collapsed: &a.sidebarCollapsed,
			Folded:    a.groupFolded,
		},
		shell.SidebarOptions{
			Title:     appmeta.Title,
			BrandIcon: icons.Atom,
			// 品牌区只做一件事：点它回首页。侧栏的收/展是底部的独立开关
			// （见 shell.footerToggle），两者互不抢点击区域。
			HomeID:     "home",
			Groups:     navGroups(),
			ToggleHint: a.sidebarToggleHint,
			Footer: shell.Item{
				ID:    settingsPageID,
				Label: settingsPageLabel,
				Icon:  settingsNavIcon,
			},
			Background: a.menuSurface(t),
			// 折叠状态变化后持久化，使下次启动保持现状。
			OnCollapseChange: func(collapsed bool) {
				a.saveSidebarCollapsed(collapsed)
			},
		})

	// 轻量任务执行时，标题右侧显示任务名（与分割线槽位里的进度条呼应）。
	subtitle := ""
	if a.taskRunning && a.taskLabel != "" {
		subtitle = a.taskLabel
	}
	shell.Content(c, t, shell.ContentOptions{
		Title:      pageTitle(a.page),
		Subtitle:   subtitle,
		Tools:      func() { a.headerTools(c, t) },
		Body:       func() { a.pageBody(c, t) },
		Scroll:     a.ScrollFor(a.page),
		Running:    a.taskRunning,
		Progress:   a.taskProgress,
		Background: a.windowSurface(t),
	})
}

// applyOpenAtLogin 把开机自启开关写进系统；失败时回滚 UI 并提示，
// 避免「界面上开着、系统里其实没生效」。
func (a *App) applyOpenAtLogin() {
	if a.win == nil {
		return // 离屏渲染 / 单测：没有系统登录项可写，也没有主循环可调度
	}
	if err := mygo.App.SetOpenAtLogin(a.openAtLogin); err != nil {
		logging.Errv("app", "设置开机自启失败", err)
		a.toast("设置开机自启失败：" + err.Error())
		a.openAtLogin = !a.openAtLogin
		a.appliedOpenAtLogin = a.openAtLogin
		a.settings.OpenAtLogin = a.openAtLogin
	}
}
