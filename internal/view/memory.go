package view

import (
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/egoist/mygo"

	"bestuav-atlas/internal/logging"
)

// ---- 整体缩放 / 改窗后的内存回收 ----
//
// mygo 的窗口整体缩放（Ctrl+滚轮 / 触控板捏合，c.Zoom()）会按新的像素比例重新排版
// 并重新栅格化文字；缩放过程中每帧都在重建整棵元素树，短命分配非常多。Go 堆本身
// 有界（实测来回缩放 20 轮，GC 后 HeapAlloc 不涨），但 HeapSys / 工作集会涨，
// 而 GC 默认不急着把空闲页还给系统——任务管理器看起来就是「缩放一次涨一截，
// 长时间不回落」。
//
// 这里在「缩放/改窗停下来」之后做一次 FreeOSMemory：只做一次，不干扰手感；
// 同时手动「释放内存」按钮可以随时触发，并把前后系统内存写进日志与提示。

const (
	// viewSettleDelay 缩放/改窗停止多久后回收。太短会在手势中途触发（卡顿），
	// 太长又等不到（用户可能不再产生新帧）。
	viewSettleDelay = 1200 * time.Millisecond
)

// watchViewport 监听整体缩放与窗口尺寸变化，静止后安排一次内存回收。
//
// 用代次号合并连续手势：只有最后一次变化对应的定时器会真正执行，中间那些
// 定时器醒来发现代次已变就直接退出。
func (a *App) watchViewport(c *ui.Context) {
	z := c.Zoom()
	if z <= 0 {
		z = 1
	}
	w, h := c.Size()
	font := c.Theme().FontSize
	if a.zoomSeen == 0 {
		a.zoomSeen, a.viewW, a.viewH, a.fontSeen = z, w, h, font
		return // 首帧只记基线
	}
	changed := math.Abs(float64(z-a.zoomSeen)) > 1e-4 ||
		math.Abs(float64(w-a.viewW)) > 0.5 || math.Abs(float64(h-a.viewH)) > 0.5 ||
		math.Abs(float64(font-a.fontSeen)) > 0.01
	if !changed {
		return
	}
	logging.Debug("mem", "视口变化", "zoom", fmt.Sprintf("%.2f", z), "font", fmt.Sprintf("%.0f", font),
		"from", fmt.Sprintf("%.0fx%.0f", a.viewW, a.viewH),
		"to", fmt.Sprintf("%.0fx%.0f", w, h))
	a.zoomSeen, a.viewW, a.viewH, a.fontSeen = z, w, h, font
	gen := a.viewportGen.Add(1)
	time.AfterFunc(viewSettleDelay, func() {
		if a.viewportGen.Load() == gen {
			a.releaseMemory("viewport-settle")
			a.viewportReleases.Add(1)
		}
	})
}

// releaseMemory 触发一次 GC 并把空闲页还给系统；返回「归还给系统的 MB」。
//
// 判据用 HeapReleased（Go 记账「已还给操作系统的页」）而不是 Sys：Sys 是保留的
// 虚拟地址空间，FreeOSMemory 之后通常不变，拿它算会永远得到 0；HeapReleased
// 才会在 scavenge 后增长。
func (a *App) releaseMemory(trigger string) float64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	debug.FreeOSMemory()
	runtime.ReadMemStats(&after)
	mb := func(v uint64) string { return fmt.Sprintf("%.1f", float64(v)/(1<<20)) }
	released := float64(after.HeapReleased)/(1<<20) - float64(before.HeapReleased)/(1<<20)
	logging.Info("mem", "已释放内存", "trigger", trigger,
		"heapBeforeMB", mb(before.HeapAlloc),
		"heapAfterMB", mb(after.HeapAlloc),
		"releasedBeforeMB", mb(before.HeapReleased),
		"releasedAfterMB", mb(after.HeapReleased),
		"freedMB", fmt.Sprintf("%.1f", released))
	return released
}

// releaseMemoryNow 手动「释放内存」：同步执行并提示归还量（设置页按钮用）。
func (a *App) releaseMemoryNow() {
	released := a.releaseMemory("manual")
	if released < 0 {
		released = 0
	}
	if released < 0.1 {
		a.toast("当前没有可归还的空闲页（引擎缓存不在此列）")
		return
	}
	a.toast(fmt.Sprintf("已归还 %.1f MB 给系统", released))
}

// ---- 重建渲染缓存 ----

// relaunchApp 是「重启应用」的可替换入口：生产代码走 mygo 的 Relaunch，
// 测试里换成计数闭包，避免测试进程真的退出。
var relaunchApp = func() { mygo.App.Relaunch() }

// rebuildConfirmWindow 二次确认的有效期：超时未确认就当没点过。
const rebuildConfirmWindow = 5 * time.Second

// onRebuildClicked 处理「重建渲染缓存」按钮：两次点击才真正执行。
//
// 第一次点击只记下时间并提示后果；5 秒内再点一次才清缓存并重启。
// 这样既不弹模态框打断设置页，也不会误触后直接丢未保存内容。
func (a *App) onRebuildClicked() {
	now := time.Now()
	if !a.confirmRebuildAt.IsZero() && now.Sub(a.confirmRebuildAt) <= rebuildConfirmWindow {
		a.confirmRebuildAt = time.Time{}
		a.rebuildRenderCache()
		return
	}
	a.confirmRebuildAt = now
	a.toast("再点一次「重建渲染缓存」确认重启；未保存的输入会丢失")
}

// rebuildRenderCache 丢弃页面运行时缓存并重启应用。
//
// 为什么必须重启：文字/蒙版图集与场景缓存都挂在 mygo 的进程级渲染器上，
// 没有公开 API 能就地缩回；重启 = 换一个全新的渲染器，图集与纹理缓存随之重建。
// 页面偏好（page_pref）是用户数据，不清；这里只清「可以随时丢」的运行时缓存。
func (a *App) rebuildRenderCache() {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	pages, prefs := len(a.pages), a.prefCount()
	a.pages = make(map[string]*pageState)
	a.prefMem = nil
	a.releaseGraphBuffers()
	logging.Audit("app", "重建渲染缓存（重启应用）",
		"cachedPages", pages,
		"pagePrefs", prefs,
		"heapMB", fmt.Sprintf("%.1f", float64(m.HeapAlloc)/(1<<20)),
		"sysMB", fmt.Sprintf("%.1f", float64(m.Sys)/(1<<20)))
	a.persist()
	relaunchApp()
}
