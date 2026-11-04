package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/locales/zh-Hans" // 注册 MyGo 控件的中文文案（日期/时间选择器、菜单等）
	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/apppath"
	"bestuav-atlas/internal/cli"
	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/profile"
	"bestuav-atlas/internal/settings"
	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/view"
)

// appIconPNG 应用图标（resources/icon.png，1024×1024 PNG），通过 go:embed 编入
// 二进制。在窗口就绪后通过 (*Window).SetIcon 设置，使窗口标题栏左上角与任务栏
// （Windows）/ Dock（macOS）/ 启动器（Linux）均显示该图标——开发模式（mygo dev）
// 与打包后运行时都生效。
//
// 说明：mygo 在构建安装包（go tool mygo build）时也会自动把 resources/icon.png
// 嵌入可执行文件（Windows .syso / macOS .icns / Linux .desktop），此处为运行时兜底，
// 保证不打包、直接 go run / mygo dev 时图标也可见。
//
//go:embed resources/icon.png
var appIconPNG []byte

func main() {
	// 0) 命令行参数：先解析，版本/重置这类操作不必初始化日志、数据库和窗口。
	opts, err := cli.Parse(os.Args[1:], os.Stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return
		}
		fmt.Fprintln(os.Stderr, "参数错误:", err)
		os.Exit(2)
	}
	if opts.Version {
		// Windows 的 GUI 子系统程序没有控制台，先接回启动它的那个，
		// 否则 `app.exe --version` 的输出直接被丢掉。
		cli.AttachConsole()
		fmt.Printf("%s %s (%s/%s)\n", appmeta.Title, appmeta.BuildVersion(), runtime.GOOS, runtime.GOARCH)
		return
	}

	// 1) 日志最先初始化：后续任何一步失败（日志目录不可写、数据库打不开…）都能留下
	//    可排查的记录。初始化失败不阻断启动（退化为 stderr）。
	closeLog := setupLogging(opts.LogLevel)
	defer closeLog()

	// 2) 单实例控制 —— 必须排在「迁移旧数据 / 打开数据库 / 重置偏好」之前。
	//
	//    早先取锁排在打开数据库之后，于是每拉起第二个进程（双击图标、双击关联文件、
	//    `app.exe --page=task`）都会先：跑一遍保留策略裁剪（真的删历史记录）、往同一个
	//    日志文件追加、往库里记一条启动事件，然后才发现自己不是主实例并退出。多进程
	//    同时写同一个 SQLite 还会偶发 "database is locked"。
	//
	//    这里只读一个字段决定是否取锁，不构造 App（构造会加载全部设置并预建演示页状态）。
	//    设置里显式开启「允许多开」时不取锁，允许并行运行独立实例。
	var (
		a       *view.App
		mainWin *mygo.Window
	)
	if !settings.MustLoad().AllowMultiInstance {
		if !mygo.App.RequestSingleInstanceLock() {
			// 第二个实例：命令行里的 --page 会由主实例的 OnSecondInstance 处理。
			// --reset-settings 没法转发——主实例已经把设置读进内存了，删文件只会被它
			// 下次写盘时覆盖回去，所以只能明确告诉用户先退出。
			if opts.ResetSettings {
				cli.AttachConsole() // GUI 子系统程序没控制台，不接回来这行提示看不见
				fmt.Fprintln(os.Stderr, "已有一个实例在运行：请先退出它，再执行 --reset-settings。")
			}
			return
		}
		mygo.App.OnSecondInstance(func(args []string, workingDir string) {
			// 第二个实例的命令行里可能带 --page=xxx：把主窗口唤出来并跳到那一页，
			// 这正是「app.exe --page=task」在已运行时该有的效果。
			page := cli.PageFromArgs(args)
			mygo.RunOnMain(func() {
				if a == nil {
					return // 主实例还没建好 App，此时窗口也不存在，忽略即可
				}
				if page != "" {
					a.SetPage(page)
				}
				if mainWin != nil {
					mainWin.Show()
				}
			})
		})
	}

	// 3) 旧版把 data.db / logs 放在配置目录里；新版改到本机数据目录。
	//    放在取锁之后：只有确认自己是唯一实例，才允许搬动另一个进程可能正在用的库文件。
	for _, name := range apppath.MigrateLegacy() {
		fmt.Fprintln(os.Stderr, "已迁移旧数据:", name)
		logging.Event(slog.LevelInfo, "app", "已迁移旧数据", "name", name)
	}

	// 4) 重置偏好：必须在 NewApp 读设置之前，也必须在取锁之后（见上）。
	//    Reset 会先把旧文件改名成 settings.json.bak-<时间>，删改坏了也能找回。
	if opts.ResetSettings {
		if backup, err := settings.Reset(); err != nil {
			logging.Errv("app", "重置用户偏好失败", err)
		} else {
			logging.Audit("app", "已重置用户偏好（--reset-settings）", "backup", backup)
		}
	}

	// 4.5) 命令行导入配置包：必须在打开数据库与 NewApp 之前（会替换 settings / data.db）。
	if opts.ImportProfile != "" {
		cli.AttachConsole()
		settingsPath, _ := settings.Path()
		dbPath, _ := apppath.DBPath()
		dataDir, _ := apppath.DataDir()
		res, err := profile.Import(profile.ImportOptions{
			In:            opts.ImportProfile,
			SettingsPath:  settingsPath,
			DBPath:        dbPath,
			DataDir:       dataDir,
			AppID:         appmeta.Identifier,
			SchemaVersion: store.SchemaVersion(),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "导入配置失败:", err)
			logging.Errv("profile", "命令行导入配置失败", err)
			os.Exit(1)
		}
		logging.Audit("profile", "命令行导入配置与数据", "path", opts.ImportProfile,
			"data", res.ImportedData, "settingsBackup", res.SettingsBackup, "dbBackup", res.DBBackup)
		fmt.Fprintln(os.Stderr, "已导入配置与数据:", opts.ImportProfile)
	}

	// 4.6) 命令行导出配置包：导出后直接退出（不需要窗口，也不动正在运行的实例）。
	if opts.ExportProfile != "" {
		cli.AttachConsole()
		settingsPath, _ := settings.Path()
		dbPath, _ := apppath.DBPath()
		res, err := profile.Export(profile.ExportOptions{
			Out:           opts.ExportProfile,
			SettingsPath:  settingsPath,
			DBPath:        dbPath,
			Background:    settings.MustLoad().BackgroundImage,
			IncludeData:   true,
			AppID:         appmeta.Identifier,
			AppVersion:    appmeta.BuildVersion(),
			SchemaVersion: store.SchemaVersion(),
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "导出配置失败:", err)
			logging.Errv("profile", "命令行导出配置失败", err)
			os.Exit(1)
		}
		logging.Audit("profile", "命令行导出配置与数据", "path", res.Path, "bytes", res.Bytes)
		fmt.Fprintf(os.Stderr, "已导出配置与数据: %s（%d 字节）\n", res.Path, res.Bytes)
		return
	}

	// 5) 本地数据库：失败同样不阻断——界面退化为纯内存，功能照常可用。
	//    安全模式跳过数据库：用来区分「是库坏了还是别的问题」。
	var db *store.Store
	if !opts.SafeMode {
		db = openStore()
		if db != nil {
			defer func() { _ = db.Close() }()
		}
	}

	a = view.NewApp()
	if db != nil {
		a.AttachStoreAsync(db)
	}
	if opts.Page != "" {
		a.SetPage(opts.Page)
	}
	if opts.SafeMode {
		a.SetSafeMode(true)
		logging.Warn("app", "安全模式启动：已跳过数据库与更新检查")
	}
	// 存储与日志都就绪后再写启动审计，保证这条事件能真正入库。
	announceStartup(db != nil)

	mygo.App.WhenReady(func() {
		// 星图的界面文案全是中文，MyGo 自带控件（尤其日期/时间选择器与日历）
		// 也固定跟随简体中文，避免英文系统区域下出现「11/5/2026 / 11:43 PM」。
		mygo.App.SetLocale("zh-Hans")

		// 主循环已就绪，应用已保存的主题（跟随系统 / 浅色 / 深色）。
		a.ApplyTheme()
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:     appmeta.Title,
			Width:     1100,
			Height:    720,
			MinWidth:  820,
			MinHeight: 520,
			StateKey:  appmeta.Name,
			Content:   ui.View(a.View),
		})
		mainWin = win
		// 注入重绘触发器：轻量任务的进度由后台 goroutine 更新，需要主动请求重绘。
		a.SetInvalidator(win.Invalidate)
		// 注入窗口句柄：浮出面板（Flyout）这类子窗口需要它作为 Parent。
		a.SetWindow(win)
		// 原生窗口创建完成后再设置图标，避免平台窗口尚未就绪导致设置失败。
		win.OnReadyToShow(func() {
			if err := win.SetIcon(appIconPNG); err != nil {
				// 图标缺失不应阻断启动：仅打印告警（部分平台窗口未就绪时会返回错误）。
				logging.Warn("app", "设置窗口图标失败", "err", err.Error())
			}
		})

		// 关闭按钮行为：交由 App 按已保存偏好（每次询问 / 最小化到托盘 /
		// 直接退出）处理。首次（CloseMode=="ask"）会弹出选择弹窗。
		// 点系统通知时把窗口唤回来（任务完成通知的 ID 固定为 task-done）。
		mygo.App.OnNotificationClick(func(id string) {
			mygo.RunOnMain(func() {
				// 待办到期通知（单条 todo-due-<id> / 汇总 todo-due-summary）点击后
				// 直接回到待办页，而不是停在当前页。
				if strings.HasPrefix(id, "todo-due") {
					a.SetPage("todos")
				}
				if mainWin != nil {
					mainWin.Show()
				}
			})
		})
		var quitting bool
		mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
			quitting = true
			// 偏好写盘是 300ms 防抖的：退出前把还没落盘的那一次补上，
			// 否则「改完设置立刻退出」会丢掉最后一次修改。
			a.FlushSettings()
		})
		win.OnClose(func(e *mygo.CloseEvent) {
			a.HandleClose(e, &quitting)
		})
		// 关闭执行器：隐藏窗口（最小化到托盘）或真正退出应用。
		// view 包不持有 *mygo.Window，故通过回调在此实现。
		a.SetCloseHandler(func(mode string) {
			if mode == "quit" {
				quitting = true
				mygo.App.Quit()
			} else {
				win.Hide()
			}
		})

		// 系统托盘：菜单栏(macOS) / 通知区域(Windows、Linux)常驻图标。
		// 默认不设 Menu，使左键点击走 OnClick 直接打开主面板；右键时再临时
		// 弹出菜单（见 OnRightClick），避免“有 Menu 时 OnClick 不触发”的限制。
		tray, err := mygo.NewTray(mygo.TrayOptions{
			Icon:           appIconPNG, // 复用应用图标（mygo 会缩放至托盘尺寸）
			IconIsTemplate: false,      // 彩色图标按原色显示（macOS 不强制黑白模板）
			ToolTip:        appmeta.Title,
			// Menu 留空：左键点击由 OnClick 接管（打开主面板）；
			// 右键菜单在 OnRightClick 中临时构建并弹出。
		})
		if err != nil {
			// 托盘缺失（如 Linux 缺 AppIndicator 库）不应阻断应用启动。
			logging.Warn("app", "创建系统托盘失败（应用仍可用）", "err", err.Error())
			return
		}

		// 左键点击打开主面板。
		// 平台差异（mygo 托盘模型决定）：
		//   - Linux（AppIndicator）：双击图标触发激活 → 打开主面板（即用户要求的“双击打开”）
		//   - Windows / macOS：单击图标触发 → 打开主面板（mygo 未暴露独立的“双击”事件，
		//     这是框架在该平台能给出的最接近行为；标准 Windows 托盘双击打开亦常见此语义）
		tray.OnClick(func() { win.Show() })

		// 右键：临时挂载菜单并弹出，调用返回后立即清空，保证左键仍可用于打开主面板。
		tray.OnRightClick(func() {
			tray.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
				{Label: "打开主界面", Click: func(*mygo.MenuItem, *mygo.Window) { win.Show() }},
				mygo.Separator(),
				{Role: mygo.RoleQuit}, // 触发退出流程：OnBeforeQuit→quitting=true→允许关闭→应用退出
			}))
			tray.PopUpMenu()
			tray.SetMenu(nil) // 清空，恢复左键打开主面板的能力
		})
	})
	if err := mygo.App.Run(); err != nil {
		panic(err)
	}
}

// ---- 启动期的两项基础设施 ----

// setupLogging 初始化日志系统，返回关闭函数。
//
// 日志目录不可写时不能让应用起不来：退回「不写文件」并打印到 stderr——用户至少
// 还能看到界面，且 Go 运行时仍会输出默认 panic 堆栈。
func setupLogging(level slog.Level) func() {
	dir, err := apppath.LogDir()
	if err != nil {
		logging.Warn("app", "无法定位日志目录，日志仅输出到 stderr", "err", err.Error())
		return func() {}
	}
	closeFn, err := logging.Setup(logging.Options{
		Dir:        dir,
		AppName:    appmeta.Name,
		AppVersion: appmeta.BuildVersion(),
		Level:      level,
	})
	if err != nil {
		logging.Warn("app", "初始化日志失败，日志仅输出到 stderr", "err", err.Error())
		return func() {}
	}
	// 启动即写环境快照（只进日志文件：此时事件入库管道尚未挂载，见下方 announceStartup）。
	logging.Event(slog.LevelInfo, "app", "应用启动", logging.EnvSnapshot()...)
	return func() { _ = closeFn() }
}

// announceStartup 在「日志 + 数据库」都就绪后写一条启动审计（入库，界面可查）。
//
// 为什么单独一步：入库靠 logging.SetRecorder 挂载，而它必须等数据库打开成功；
// 若在 Setup 阶段就 Audit，事件只会落到文件、进不了库（实测踩过）。
func announceStartup(dbReady bool) {
	storeState := "未启用"
	if dbReady {
		storeState = "已启用"
	}
	logging.Audit("app", "应用启动",
		"build", appmeta.BuildVersion(),
		"os", runtime.GOOS,
		"arch", runtime.GOARCH,
		"go", runtime.Version(),
		"cpus", runtime.NumCPU(),
		"store", storeState,
	)
}

// openStore 打开本地数据库并接上「错误事件入库」的管道；失败返回 nil（不阻断启动）。
func openStore() *store.Store {
	path, err := apppath.DBPath()
	if err != nil {
		logging.Errv("store", "无法定位数据库路径", err)
		return nil
	}
	s, err := store.Open(path)
	if err != nil {
		logging.Errv("store", "打开数据库失败", err, "path", path)
		return nil
	}
	// 启动时按保留策略裁剪历史记录（任务/事件/授权三类都只增不减，见 store/retention.go）。
	if n, perr := s.Prune(store.DefaultRetention()); perr != nil {
		logging.Warn("store", "裁剪历史记录失败", "err", perr.Error())
	} else if n > 0 {
		logging.Event(slog.LevelInfo, "store", "已裁剪历史记录", "count", n)
	}
	// ERROR/CRASH 级日志 + 显式 Audit 事件写入 app_event 表。
	// 注意：入库失败时只能写 stderr —— 再调 logging 会再次触发本回调，形成递归。
	logging.SetRecorder(func(level, kind, msg, detail string) {
		if err := s.RecordEvent(level, kind, msg, detail); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "warn: 事件入库失败:", err)
		}
	})
	logging.Event(slog.LevelInfo, "store", "数据库已就绪", "path", path)
	return s
}
