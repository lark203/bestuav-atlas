package logging

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"bestuav-atlas/internal/appmeta"
	"bestuav-atlas/internal/apppath"
)

// EnvSnapshot 采集「应用 + 运行时 + 机器 + 系统 + 路径」信息，作为启动日志的环境快照。
//
// 为什么启动时就写全：用户报障时通常只有一句「打不开 / 卡住了」，日志里如果只有
// 错误本身，排查者还得反复追问「什么系统、多少核、装在哪」。把这份快照一次写全，
// 配合崩溃堆栈基本能定位到环境类问题（内存不足、路径含中文、旧版本、分辨率缩放…）。
//
// 返回 slog 风格的键值对，直接 Event(..., EnvSnapshot()...) 即可。
// 只采集本机诊断所需信息，不涉及任何业务数据或凭据。
func EnvSnapshot() []any {
	kv := []any{
		// 注意：不要在这里再写 "app" —— slog 的默认 handler 已经带了 app/version
		// 两个字段，重复的键会让 JSON 日志出现两个 "app"，解析时后者覆盖前者。
		"title", appmeta.Title,
		"identifier", appmeta.Identifier,
		"build", appmeta.BuildVersion(),

		"go", runtime.Version(),
		"goos", runtime.GOOS,
		"goarch", runtime.GOARCH,
		"cpus", runtime.NumCPU(),
		"gomaxprocs", runtime.GOMAXPROCS(0),
		"goroutines", runtime.NumGoroutine(),
		"pid", os.Getpid(),

		"tz", time.Local.String(),
		"now", time.Now().Format(time.RFC3339),
	}

	// 进程内存基线（启动时很小，用于对比后续是否泄漏）。
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	kv = append(kv, "heapMB", fmt.Sprintf("%.1f", float64(m.HeapAlloc)/(1<<20)),
		"sysMB", fmt.Sprintf("%.1f", float64(m.Sys)/(1<<20)))

	// 机器与用户（本机日志，属于排障必需信息；不落任何凭据）。
	if host, err := os.Hostname(); err == nil {
		kv = append(kv, "host", host)
	}
	if user, err := os.UserHomeDir(); err == nil {
		kv = append(kv, "home", user)
	}
	if user := os.Getenv("USERNAME"); user != "" {
		kv = append(kv, "user", user)
	} else if user := os.Getenv("USER"); user != "" {
		kv = append(kv, "user", user)
	}
	if exe, err := os.Executable(); err == nil {
		kv = append(kv, "exe", exe)
	}

	// 平台特有的 CPU 信息（Windows 上这两个环境变量普遍存在，零依赖零开销）。
	if v := os.Getenv("PROCESSOR_IDENTIFIER"); v != "" {
		kv = append(kv, "cpu", v)
	}
	if v := os.Getenv("PROCESSOR_ARCHITECTURE"); v != "" {
		kv = append(kv, "cpuArch", v)
	}
	if v := os.Getenv("LANG"); v != "" {
		kv = append(kv, "lang", v)
	}

	// 应用路径（配置 / 日志 / 数据库），排查「权限不足、路径含中文」时最关键。
	if dir, err := apppath.ConfigDir(); err == nil {
		kv = append(kv, "configDir", dir)
	}
	if dir, err := apppath.LogDir(); err == nil {
		kv = append(kv, "logDir", dir)
	}
	if p, err := apppath.DBPath(); err == nil {
		kv = append(kv, "dbPath", p)
	}
	return kv
}
