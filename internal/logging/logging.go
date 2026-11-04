// Package logging 负责应用的运行日志与崩溃记录。
//
// 设计目标（面向「用户报障后远程排查」）：
//   - **零第三方依赖**：用标准库 log/slog，JSON 每行一条，字段可 grep / 可入库；
//   - **按天轮转、保留 7 天**：文件名 app-YYYY-MM-DD.log。桌面应用长期运行的日志
//     不该无限增长，也不该频繁滚动（按天比按大小更容易对上"用户说昨天出问题"）；
//   - **错误与崩溃额外入库**：ERROR 及以上级别通过 Recorder 回调投递给
//     internal/store，写进 app_event 表，便于以后做「错误历史 / 一键导出」；
//   - **启动即写环境快照**：机器、系统、运行时、路径，一次写全（见 EnvSnapshot）。
//
// 本包不 import 任何业务包，因此可被 main / store / view 任意层引用。
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// LevelCrash 崩溃级别：高于 ERROR，用于 panic 记录（渲染成 "CRASH"）。
const LevelCrash = slog.LevelError + 4

const (
	defaultRetention = 7 // 日志保留天数
	filePrefix       = "app-"
	fileSuffix       = ".log"
	dateLayout       = "2006-01-02"

	// fileMaxBytes 单个日志文件的上限，超了就切下一个分片（app-<日期>.2.log…）。
	// 16MB 足够放下几万条 JSON 日志，同时挡住「--log-level=debug 常开写满磁盘」。
	fileMaxBytes = 16 << 20
	// maxLogShardsPerDay 同一天最多切多少片，防止极端情况下无限建文件。
	maxLogShardsPerDay = 64
)

// Options 初始化参数。
type Options struct {
	Dir        string     // 日志目录（一般由 apppath.LogDir 给出）
	AppName    string     // 写入每条日志的 app 字段
	AppVersion string     // 写入每条日志的 version 字段
	Level      slog.Level // 文件日志的最低级别；零值 = Info
	Retention  int        // 保留天数，<=0 用默认 7
	Mirror     bool       // 是否同时输出到 stderr（开发模式调试用）
}

// Recorder 事件入库回调。ERROR 及以上级别的日志会同时投递给它。
// 参数刻意用基础类型，避免 logging 与 store 互相引用。
type Recorder func(level, kind, message, detail string)

var (
	recMu sync.RWMutex
	recFn Recorder
)

// SetRecorder 注册事件入库回调；传 nil 取消。
//
// 注意：回调内部不要再调用本包的 Event（失败的入库会再次触发回调 → 递归）。
func SetRecorder(r Recorder) {
	recMu.Lock()
	recFn = r
	recMu.Unlock()
}

// Setup 初始化全局 slog：JSON 输出到 <dir>/app-YYYY-MM-DD.log。
//
// 返回的 close 用于退出前关闭文件句柄（不调用也不会丢数据：每条都直接 Write 到
// 文件，没有用户态缓冲）。
func Setup(o Options) (closeFn func() error, err error) {
	if o.Dir == "" {
		return func() error { return nil }, fmt.Errorf("logging: 未指定日志目录")
	}
	if o.Retention <= 0 {
		o.Retention = defaultRetention
	}
	w := &dailyWriter{dir: o.Dir, retention: o.Retention, maxBytes: fileMaxBytes}
	// 启动时就清理一次过期日志：避免长期运行的进程只在「跨天写日志」时才清理，
	// 也保证用户在设置页看到的就是清理后的结果。
	if perr := w.prune(time.Now()); perr != nil {
		return func() error { return nil }, perr
	}

	var out io.Writer = w
	if o.Mirror {
		out = io.MultiWriter(w, os.Stderr)
	}
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level: o.Level,
		// 把自定义的崩溃级别渲染成可读字符串，而不是数字 12。
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				if lv, ok := a.Value.Any().(slog.Level); ok && lv >= LevelCrash {
					a.Value = slog.StringValue("CRASH")
				}
			}
			return a
		},
	})
	slog.SetDefault(slog.New(h).With("app", o.AppName, "version", o.AppVersion))
	return w.Close, nil
}

// ---- 事件 API ----

// Event 写一条应用事件：总是写日志文件；ERROR 及以上同时入库。
//
//	// kind 用于分类（app / store / task / license / panic …），便于按类排查
//	// attrs 是 slog 风格的键值对：logging.Event(slog.LevelInfo, "task", "任务完成", "name", n)
func Event(level slog.Level, kind, msg string, attrs ...any) {
	slog.Log(context.Background(), level, msg, append([]any{"kind", kind}, attrs...)...)
	if level < slog.LevelError {
		return
	}
	recMu.RLock()
	rec := recFn
	recMu.RUnlock()
	if rec != nil {
		rec(levelName(level), kind, msg, detailOf(attrs))
	}
}

// Errv 记录一条带 error 的错误（自动附加 err 字段，并入事件库）。
func Errv(kind, msg string, err error, attrs ...any) {
	Event(slog.LevelError, kind, msg, append([]any{"err", errText(err)}, attrs...)...)
}

// Warn 记录一条警告（只进日志文件，不入事件库）。
//
// 用于「不影响使用、但事后排查时需要知道」的降级路径：托盘创建失败、图标设置
// 失败、设置文件解析失败等。这些以前用 println 打到 stderr，打包成 GUI 程序后
// 根本没有控制台，用户报障时什么都看不到。
func Warn(kind, msg string, attrs ...any) {
	Event(slog.LevelWarn, kind, msg, attrs...)
}

// Audit 写一条**审计**事件：INFO 日志 + **无条件入库**。
//
// 与 Event 的区别：Event 只把 ERROR 及以上入库（日志文件才是全量的）；而「启动」
// 「生成授权」「清空数据」这类关键动作本身不是错误，却必须留在库里备查，所以单独
// 给一个入口。用法与 Event 相同（kind 用来分类）。
func Audit(kind, msg string, attrs ...any) {
	slog.Log(context.Background(), slog.LevelInfo, msg, append([]any{"kind", kind}, attrs...)...)
	recMu.RLock()
	rec := recFn
	recMu.RUnlock()
	if rec != nil {
		rec("INFO", kind, msg, detailOf(attrs))
	}
}

// Recover 供 defer 使用：捕获 panic → 记 CRASH 日志（含堆栈）→ 入库 → **重新抛出**。
//
// 为什么重新抛出：本包只负责「留下可排查的记录」，不改变原有崩溃行为。若吞掉
// panic，界面可能停在一个不一致的状态上，反而制造更难查的问题。
//
//	func (a *App) View(c *ui.Context) {
//	    defer logging.Recover("view")
//	    ...
//	}
func Recover(scope string) {
	r := recover()
	if r == nil {
		return
	}
	Event(LevelCrash, "panic", fmt.Sprintf("%s 崩溃: %v", scope, r), "stack", string(debug.Stack()))
	panic(r)
}

// ---- 内部 ----

func levelName(l slog.Level) string {
	switch {
	case l >= LevelCrash:
		return "CRASH"
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	default:
		return "INFO"
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// detailOf 把附加字段压成一行文本入库（避免在 app_event 里再建一张子表）。
func detailOf(attrs []any) string {
	if len(attrs) == 0 {
		return ""
	}
	var b strings.Builder
	for i := 0; i < len(attrs); i += 2 {
		if i > 0 {
			b.WriteString("; ")
		}
		key := fmt.Sprint(attrs[i])
		var val any = "(缺失)"
		if i+1 < len(attrs) {
			val = attrs[i+1]
		}
		fmt.Fprintf(&b, "%s=%v", key, val)
	}
	return b.String()
}

// dailyWriter 按天切分的日志写入器：写之前比对当前日期，变了就换文件；
// 单个文件超过 fileMaxBytes 再按序号分片（app-2026-11-04.log → app-2026-11-04.2.log）。
//
// 为什么要分片：只按天切分时，`--log-level=debug` 常开一天能写出几百 MB，
// 磁盘会被慢慢吃满，诊断包也会跟着变成几百 MB 的下载。分片把单文件钉在上限内，
// 代价只是同一天可能有多个文件。
type dailyWriter struct {
	mu        sync.Mutex
	dir       string
	retention int
	file      *os.File
	day       string
	index     int   // 0 = 基础文件名；n>0 = app-<day>.<n+1>.log
	size      int64 // 当前文件已写字节数（重启后由 Stat 恢复）
	maxBytes  int64 // 单片上限；0 表示用 fileMaxBytes（测试里注入小值）
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	day := time.Now().Format(dateLayout)
	if w.file == nil || w.day != day {
		if err := w.open(day); err != nil {
			return 0, err
		}
	}
	if w.size+int64(len(p)) > w.limit() {
		// 分片失败不致命：继续往当前文件追加，宁可超一点也别把这条日志丢了。
		_ = w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

// limit 返回单片上限：测试可以注入小值，生产一律 fileMaxBytes。
func (w *dailyWriter) limit() int64 {
	if w.maxBytes > 0 {
		return w.maxBytes
	}
	return fileMaxBytes
}

// fileName 返回某天第 index 片的文件名。
func fileName(day string, index int) string {
	if index == 0 {
		return filePrefix + day + fileSuffix
	}
	return fmt.Sprintf("%s%s.%d%s", filePrefix, day, index+1, fileSuffix)
}

// rotate 切到同一天的下一个分片。
func (w *dailyWriter) rotate() error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	for i := w.index + 1; i < w.index+maxLogShardsPerDay; i++ {
		p := filepath.Join(w.dir, fileName(w.day, i))
		if _, err := os.Stat(p); err == nil {
			continue // 该序号已被占用（比如上次运行写满过），找下一个
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		w.file, w.index, w.size = f, i, 0
		return nil
	}
	return fmt.Errorf("logging: 当天日志分片已达上限 %d 个", maxLogShardsPerDay)
}

// open 切换到指定日期的文件，并顺带清理过期日志。
func (w *dailyWriter) open(day string) error {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	p := filepath.Join(w.dir, fileName(day, 0))
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	w.file, w.day, w.index = f, day, 0
	if st, statErr := f.Stat(); statErr == nil {
		w.size = st.Size()
	} else {
		w.size = 0
	}
	// 当天的基础文件已经写满（上次运行写超的）：直接开下一个分片，
	// 免得每次启动都往一个满文件里再加几行。
	if w.size >= w.limit() {
		_ = w.rotate()
	}
	return w.prune(time.Now())
}

// prune 删除超过保留天数的日志文件（认得 app-YYYY-MM-DD.log 与分片 app-YYYY-MM-DD.N.log）。
func (w *dailyWriter) prune(now time.Time) error {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 目录还没建，等于没有过期文件
		}
		return err
	}
	cutoff := now.AddDate(0, 0, -w.retention)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, filePrefix) || !strings.HasSuffix(name, fileSuffix) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, filePrefix), fileSuffix)
		// 分片的文件名是 <日期>.N：只取点号前的日期部分再解析，
		// 否则分片文件会被当成「不是本包产出」而永远不清理。
		if i := strings.IndexByte(day, '.'); i >= 0 {
			day = day[:i]
		}
		t, perr := time.ParseInLocation(dateLayout, day, time.Local)
		if perr != nil {
			continue // 不是本包产出的文件，不动
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
	return nil
}

// Close 关闭当前日志文件。
func (w *dailyWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
