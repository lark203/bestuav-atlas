package view

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/egoist/mygo/ui"

	"bestuav-atlas/internal/logging"
	"bestuav-atlas/internal/ui/kit"
)

// 页面异步数据：把「后台加载 + 加载中/失败重试 + 迟到结果丢弃」收成一个构件，
// 页面只写「怎么加载」与「怎么渲染」。
//
// 用法（页面函数里）：
//
//	op := pageAsync(a, "orders", func(ctx context.Context) ([]order, error) { return loadOrders(ctx) })
//	op.View(c, t, func(rows []order) {
//		kit.Card(c, t, func() { ... 用 rows 渲染 ... })
//	})
//
// 行为约定：
//   - 第一次绘制发起加载；**每次重新进入本页**（缓存页切回来）自动重载；
//   - 加载中显示一张「正在加载…」卡片，失败显示错误卡片 + 「重试」；
//   - 结果只采纳最新一次加载（切页后迟到的结果会被丢弃）；
//   - **页面销毁时取消仍在跑的加载**（context 被 cancel）；长任务应尊重 ctx.Done()；
//   - load 在后台协程里执行，**不要碰 ui / c / 控件**；写界面请交给 render。
//
// 线程模型：后台协程只把结果放进带锁的 pending 槽位，再请求一次重绘；
// 真正改状态与绘制都发生在主线程的 View 里。这样既不需要跨线程改 App 状态，
// 也让「加载完成 → 采纳」有明确的一帧边界（测试里可以稳定地逐帧推进）。
//
// 状态存在 pageData 里（按当前页 id + key），页面销毁即丢弃；要跨重启记住筛选
// 条件之类的东西用 a.Prefs()（见 prefs.go），两者不要混用。

// asyncResult 一次加载的原始结果（后台协程写入，主线程取走）。
type asyncResult[T any] struct {
	gen   uint64
	value T
	err   error
}

// asyncState 一次异步加载的跨帧状态。
//
// pending 是唯一会被后台协程写的字段，用 mu 保护；其余字段只在主线程读写。
type asyncState[T any] struct {
	mu      sync.Mutex
	pending *asyncResult[T]

	gen     uint64 // 代次：只有最新一次加载的结果会被采纳
	loading bool
	done    bool
	enters  int // 已处理到的页面进入次数（缓存页切回来时自增 → 触发重载）
	value   T
	err     error

	// cancel 取消当前这次加载；页面销毁（或重新发起加载）时调用。
	cancel context.CancelFunc
}

// cancelLoad 取消仍在跑的加载（页面销毁时由 destroyPage 统一调用）。
func (s *asyncState[T]) cancelLoad() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// Async 页面内一个异步数据源的句柄；每次绘制重新取一次即可。
type Async[T any] struct {
	a    *App
	page string
	key  string
	st   *asyncState[T]
	load func(context.Context) (T, error)
}

// pageAsync 取当前页面的异步数据源。同一页里多个数据源用不同 key 区分。
//
// load 会在后台协程里被调用（带 context，页面销毁时被取消）；它应当只读数据、
// 不做界面操作。
func pageAsync[T any](a *App, key string, load func(context.Context) (T, error)) *Async[T] {
	st := pageData(a, a.page, "async:"+key, func() asyncState[T] { return asyncState[T]{} })
	h := &Async[T]{a: a, page: a.page, key: key, st: st, load: load}
	// 「每次进入本页自动刷新」：缓存页切回来也算一次进入（首次创建时不额外触发）。
	if enters := a.pageStat(a.page).enters; st.enters != enters {
		if st.enters != 0 {
			st.done = false
		}
		st.enters = enters
	}
	return h
}

// Reload 标记重新加载：下一次 View 会重新发起（页面里做「刷新」按钮时用）。
func (h *Async[T]) Reload() { h.st.done = false }

// Value 最近一次成功加载的值（还没成功时是零值）。
func (h *Async[T]) Value() T { return h.st.value }

// Loading 报告是否正在加载。
func (h *Async[T]) Loading() bool { return h.st.loading }

// Err 最近一次加载的错误（nil = 没有错误）。
func (h *Async[T]) Err() error { return h.st.err }

// View 绘制异步数据：需要时发起加载，并按状态渲染 加载中 / 失败（可重试）/ 内容。
func (h *Async[T]) View(c *ui.Context, t *ui.Theme, render func(T)) {
	h.drain()
	h.start()
	switch {
	case h.st.loading:
		asyncLoadingCard(c, t)
	case h.st.err != nil:
		asyncErrorCard(c, t, h.st.err, func() {
			h.st.done = false
			h.a.requestFrame()
		})
	default:
		render(h.st.value)
	}
}

// drain 取走后台协程投递的结果（主线程调用）；过期结果直接丢弃。
func (h *Async[T]) drain() {
	h.st.mu.Lock()
	p := h.st.pending
	h.st.pending = nil
	h.st.mu.Unlock()
	if p == nil {
		return
	}
	if h.st.gen != p.gen {
		return // 已有更新的一次加载（切页/手动刷新），这次结果作废
	}
	h.st.loading = false
	h.st.done = true
	h.st.value, h.st.err = p.value, p.err
	// 主动取消（页面销毁）不是错误：不写 ERROR 日志，也不让页面弹错误卡片。
	// 迟到的取消结果通常也过不了代次校验，走到这里的多数是真正失败。
	if p.err != nil && !errors.Is(p.err, context.Canceled) {
		logging.Errv("async", "页面数据加载失败", p.err, "page", h.page, "key", h.key)
	}
}

// start 在需要时发起一次后台加载。
func (h *Async[T]) start() {
	if h.st.loading || h.st.done {
		return
	}
	// 重新发起前先取消上一次：避免同一个数据源同时跑两个加载。
	h.st.cancelLoad()

	ctx, cancel := context.WithCancel(context.Background())
	h.st.gen++
	gen := h.st.gen
	h.st.loading = true
	h.st.err = nil
	h.st.cancel = cancel
	load := h.load
	// 只把值带进后台协程：App 只用来请求重绘（Window.Invalidate 本身跨线程安全）。
	a := h.a
	page, key, st := h.page, h.key, h.st
	go func() {
		var (
			value T
			err   error
		)
		// 页面自己的 load 里 panic 不能带崩整个窗口：这里兜住并转成一次「加载失败」。
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("加载时发生异常：%v", r)
					logging.Errv("async", "页面数据加载 panic", fmt.Errorf("%v", r),
						"page", page, "key", key)
				}
			}()
			value, err = load(ctx)
		}()
		st.mu.Lock()
		st.pending = &asyncResult[T]{gen: gen, value: value, err: err}
		st.mu.Unlock()
		a.requestFrame()
	}()
}

// asyncLoadingCard 加载中的占位卡片。
func asyncLoadingCard(c *ui.Context, t *ui.Theme) {
	kit.Card(c, t, func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			kit.BreathingDot(c, t.Accent, 7, kit.BreathePeriod)
			ui.Text(c, "正在加载…").TextColor(t.TextMuted)
		})
	})
}

// asyncErrorCard 失败卡片：错误原因 + 重试。
func asyncErrorCard(c *ui.Context, t *ui.Theme, err error, retry func()) {
	kit.Card(c, t, func() {
		ui.Text(c, "加载失败").Bold().TextColor(t.Danger)
		ui.Text(c, err.Error()).FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "重试").Clicked() {
				retry()
			}
			ui.Text(c, "（数据没加载出来时，页面其余部分仍可正常使用）").
				FontSize(t.FontSize - 2).TextColor(t.TextMuted)
		})
	})
}
