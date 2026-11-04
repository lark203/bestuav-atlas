// Package cli 解析命令行参数。
//
// 桌面应用也需要一点命令行能力：
//   - --version 给打包脚本 / 用户确认装的是哪一版；
//   - --reset-settings 在配置写坏、界面打不开时救命（不必手动去翻 %APPDATA%）；
//   - --safe-mode 跳过数据库与更新检查，用于排查「是不是库坏了」；
//   - --page 给单实例转发用：第二次启动 `app.exe --page=task` 直接打开任务页；
//   - --export-profile / --import-profile 在机器之间搬配置与数据（见 internal/profile）。
//
// 参数解析单独成包（不依赖 UI），便于单测与复用。
package cli

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// Options 是解析后的启动选项。
type Options struct {
	Version       bool       // 打印版本后退出
	Page          string     // 启动后打开哪个页面（页面 id；空 = 默认首页）
	ResetSettings bool       // 启动前删除用户偏好
	SafeMode      bool       // 安全模式：不启用数据库与更新检查
	LogLevel      slog.Level // 文件日志级别
	// ExportProfile 非空时导出配置与数据到该路径后退出。
	ExportProfile string
	// ImportProfile 非空时先导入该配置包，再正常启动。
	ImportProfile string
	Args          []string // 原始参数（单实例转发时用）
}

// Parse 解析参数。args 不含程序名（传 os.Args[1:]）。
func Parse(args []string, out io.Writer) (Options, error) {
	fs := flag.NewFlagSet("bestuav-atlas", flag.ContinueOnError)
	fs.SetOutput(out)

	var (
		version  = fs.Bool("version", false, "打印版本并退出")
		page     = fs.String("page", "", "启动后打开的页面 id（如 task、settings）")
		reset    = fs.Bool("reset-settings", false, "启动前把用户偏好恢复为默认值")
		safe     = fs.Bool("safe-mode", false, "安全模式：不启用数据库与自动更新")
		logLevel = fs.String("log-level", "info", "日志级别：debug / info / warn / error")
		exportP  = fs.String("export-profile", "", "导出配置与数据到指定 zip 后退出")
		importP  = fs.String("import-profile", "", "启动前先导入指定配置包")
	)
	fs.Usage = func() {
		fmt.Fprintln(out, "用法: bestuav-atlas [选项]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return Options{}, err
	}
	lv, err := parseLevel(*logLevel)
	if err != nil {
		return Options{}, err
	}
	return Options{
		Version:       *version,
		Page:          *page,
		ResetSettings: *reset,
		SafeMode:      *safe,
		LogLevel:      lv,
		ExportProfile: *exportP,
		ImportProfile: *importP,
		Args:          args,
	}, nil
}

// PageFromArgs 从任意参数列表里取出 --page 的值（单实例转发用）。
//
// 第二个实例的参数来自操作系统，可能带引号/等号/空格三种写法，这里统一处理；
// 解析失败就当没传（只弹主窗口，不影响使用）。
func PageFromArgs(args []string) string {
	for i, a := range args {
		for _, prefix := range []string{"--page=", "-page="} {
			if strings.HasPrefix(a, prefix) {
				return strings.TrimPrefix(a, prefix)
			}
		}
		if (a == "--page" || a == "-page") && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// parseLevel 把字符串映射成 slog 级别；未知值报错（避免拼错后静默用错级别）。
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("未知日志级别 %q（可选 debug/info/warn/error）", s)
	}
}
