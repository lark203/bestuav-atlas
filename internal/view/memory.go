package view

import (
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/egoist/mygo/ui"

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
