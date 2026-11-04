package view

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 本文件实现「页面生命周期与缓存」，对应 javafx 版 ViewFactory 的那一套：
// 懒加载 / 缓存复用 / onShow(onEnter) / onHide(onLeave) / 闲置销毁(onDestroy)。
//
// 为什么不能照搬 javafx 的做法
// ----------------------------
// javafx 是保留模式 GUI：一个页面就是一个长期存在的 Node 实例，所以它可以
// 「缓存实例、切换时隐藏、闲置分钟后 clear 子节点 + 交给 GC」。
//
// mygo 是即时模式 UI：每帧都从页面函数重新构建元素树，**根本不存在页面实例**
// 可以缓存。真正会「跨帧留存」的只有两样东西：
//  1. 业务状态（表单值、已加载的数据、任务进度…）——由 App 字段或本文件的
//     pageState.data 持有；
//  2. 控件自身的交互状态（滚动位置、输入框光标）——由框架按元素 key 维护，
//     其中滚动位置可以用 ui.ScrollState 显式接管（见 ScrollFor）。
//
// 所以「页面缓存」在这里等于：**保留这两类状态、并在销毁时丢掉它们**；
// 「重新加载」等于：**进入前把状态清掉再重建**。这正是用户能感知的差别
// （切回来还能看到原来的滚动位置 / 表单内容，还是全新的一页）。
//
// 三种策略
// --------
//   - keepCached（默认）：离开后保留状态；闲置超过 TTL（默认 15 分钟，可在
//     设置页调整）后销毁。适合大多数业务页。
//   - keepPinned：永不销毁（首页、设置这类高频且轻量的页面）。
//   - keepFresh：离开即销毁，每次进入都是全新页面（如「生成 license」表单）。
//
// 生命周期回调
// ------------
//   - onEnter：每次进入都会调用（适合「进入即刷新数据」）。
//   - onLeave：离开时调用（适合暂停轮询/动画）。
//   - onDestroy：状态被丢弃前调用（适合释放 goroutine、解码后的图片等真实资源）。

// keepMode 页面缓存策略。
type keepMode int

const (
	// keepCached 离开后保留状态，闲置超时销毁。零值即此模式，无需显式声明。
	keepCached keepMode = iota
	// keepPinned 常驻内存，永不销毁。
	keepPinned
	// keepFresh 不缓存：离开即销毁，每次进入重建。
	keepFresh
)

// defaultPageIdleTTL 是未在设置里指定时的闲置销毁时长。
const defaultPageIdleTTL = 15 * time.Minute

// pagePolicy 一个页面的生命周期策略。
type pagePolicy struct {
	keep keepMode
	// idle 闲置多久销毁；0 表示用设置里的全局值（再缺省 15 分钟）。
	idle time.Duration

	onEnter   func(a *App)
	onLeave   func(a *App)
	onDestroy func(a *App)
}

// pageState 一个页面的运行时状态（不是控件树，而是「缓存下来的东西」）。
type pageState struct {
	born     time.Time
	lastSeen time.Time
	enters   int
	// scroll 该页内容区的滚动位置：切走再切回来会停在原处，销毁后归零。
	scroll ui.ScrollState
	// data 页面私有状态，destroyPage 时整体丢弃（用于演示「重新加载」）。
	data map[string]any
}

// syncPages 在每帧构建界面前同步生命周期：处理页面切换与闲置淘汰。
//
// 只在 page 变化时做切换动作（切换一帧内多次构建也只算一次进入）。
func (a *App) syncPages() {
	now := time.Now()
	if a.page != a.pagePrev {
		a.leavePage(a.pagePrev, now)
		cur, ok := pageByID(a.page)
		if ok {
			// keepFresh：进入前先销毁旧状态，保证「每次都是新页面」。
			if cur.policy.keep == keepFresh {
				a.destroyPage(cur.id)
			}
			a.enterPage(cur, now)
		}
		a.pagePrev = a.page
	}
	a.evictIdlePages(now)
}

// enterPage 记录进入并回调 onEnter。
func (a *App) enterPage(it navItem, now time.Time) {
	st := a.pageStat(it.id)
	st.enters++
	st.lastSeen = now
	if it.policy.onEnter != nil {
		it.policy.onEnter(a)
	}
}

// leavePage 回调 onLeave；keepFresh 的页面立即销毁。
func (a *App) leavePage(id string, now time.Time) {
	if id == "" {
		return
	}
	it, ok := pageByID(id)
	if !ok {
		return
	}
	if it.policy.onLeave != nil {
		it.policy.onLeave(a)
	}
	if st, ok := a.pages[id]; ok {
		st.lastSeen = now
	}
	if it.policy.keep == keepFresh {
		a.destroyPage(id)
	}
}

// destroyPage 销毁一个页面的缓存状态（回调 onDestroy 后丢弃数据与滚动位置）。
func (a *App) destroyPage(id string) {
	st, ok := a.pages[id]
	if !ok {
		return
	}
	if it, ok := pageByID(id); ok && it.policy.onDestroy != nil {
		it.policy.onDestroy(a)
	}
	// 取消该页仍在跑的异步加载：pageAsync 的状态实现了 cancelLoad（见 async.go）。
	// 页面都销毁了，再让后台任务跑完只是白烧 CPU / 占网络。
	for _, v := range st.data {
		if c, ok := v.(interface{ cancelLoad() }); ok {
			c.cancelLoad()
		}
	}
	// 丢弃滚动位置与页面数据：这就是「下次进来是全新页面」的含义。
	st.scroll = ui.ScrollState{}
	st.data = nil
	delete(a.pages, id)
}

// DestroyAllPages 销毁所有非常驻页面的缓存（退出登录、切换工作区等场景可用）。
func (a *App) DestroyAllPages() {
	for id := range a.pages {
		a.destroyPage(id)
	}
}

// idleEvictInterval 是闲置淘汰的检查间隔。
//
// 每帧检查一次纯浪费（遍历 pages 只是几个 map 查找，但完全没有必要）；
// 一秒一次最多让销毁晚一秒，用户感知不到。
const idleEvictInterval = time.Second

// evictIdlePages 淘汰闲置超时的缓存页面（keepPinned / keepFresh 不参与）。
//
// 正在看的页面不淘汰：用户在一个页面上停留超过 TTL 很常见（比如开着任务页看进度），
// 把它连同滚动位置一起销毁会显得像「界面自己重置了」。
func (a *App) evictIdlePages(now time.Time) {
	if !a.lastEvict.IsZero() && now.Sub(a.lastEvict) < idleEvictInterval {
		return
	}
	a.lastEvict = now
	for id, st := range a.pages {
		if id == a.page {
			continue
		}
		it, ok := pageByID(id)
		if !ok || it.policy.keep != keepCached {
			continue
		}
		if idle := a.pageIdleTTL(it.policy); idle > 0 && now.Sub(st.lastSeen) > idle {
			a.destroyPage(id)
		}
	}
}

// pageIdleTTL 单页策略 > 全局设置 > 内置默认。
func (a *App) pageIdleTTL(p pagePolicy) time.Duration {
	if p.idle > 0 {
		return p.idle
	}
	if a.settings != nil && a.settings.PageIdleMinutes > 0 {
		return time.Duration(a.settings.PageIdleMinutes) * time.Minute
	}
	if a.settings != nil && a.settings.PageIdleMinutes == 0 {
		// 设置里显式填 0 = 不按闲置销毁（仍可手动销毁或 keepFresh）。
		return 0
	}
	return defaultPageIdleTTL
}

// pageStat 取（必要时创建）页面运行时状态。
// pageStateOf 取「页面私有状态」：不存在就用 create 造一个并挂在该页的 data 上。
//
// 为什么把演示页的状态放在这里，而不是像以前那样挂在 App 结构体上：
//  1. 页面自包含——类型、状态、创建逻辑都在同一个页面文件里。删掉整个页面文件
//     就等于彻底移除该功能，App 不会留下「字段类型来自已删包」这种悬空依赖
//     （那正是「删页面必须连 App 结构体一起改」的根因）；
//  2. 释放自动化——destroyPage 会把 data 整体丢弃，GPU 纹理、缓冲这些不需要在
//     onDestroy 里逐个手工置 nil；
//  3. 语义正确——pageState 本来就是给「页面私有状态」准备的容器。
func pageStateOf[T any](a *App, page, key string, create func() T) T {
	st := a.pageStat(page)
	if v, ok := st.data[key].(T); ok {
		return v
	}
	v := create()
	st.data[key] = v
	return v
}

func (a *App) pageStat(id string) *pageState {
	if a.pages == nil {
		a.pages = make(map[string]*pageState)
	}
	st, ok := a.pages[id]
	if !ok {
		now := time.Now()
		st = &pageState{born: now, lastSeen: now, data: make(map[string]any)}
		a.pages[id] = st
	}
	return st
}

// ScrollFor 返回当前页面内容区的滚动状态，交给 ui.Scroll(...).TrackScroll 使用。
//
// 这样「切走再切回」能停在原来的位置（缓存命中），而页面被销毁后位置自动归零
// （等价于重新加载）。这也是用户最直观能感受到的缓存差别。
func (a *App) ScrollFor(id string) *ui.ScrollState {
	return &a.pageStat(id).scroll
}

// pageData 取页面私有状态的指针，首次访问用 init 初始化。
//
// 页面被销毁（keepFresh 离开、闲置超时、手动 DestroyPage）后，这里的数据会
// 一起消失，下次 init 重新执行——这就是「重新加载」的实现方式。
//
//	p := pageData(a, "task", "filter", func() *taskFilter { return &taskFilter{} })
func pageData[T any](a *App, pageID, key string, init func() T) *T {
	st := a.pageStat(pageID)
	if v, ok := st.data[key]; ok {
		if typed, ok := v.(*T); ok {
			return typed
		}
	}
	v := new(T)
	*v = init()
	st.data[key] = v
	return v
}

// pageCacheRows 供「基本信息」页展示，便于观察缓存与销毁是否按预期工作。
type pageCacheRow struct {
	id      string
	label   string
	policy  string
	enters  int
	age     string
	idle    string
	scrollY float32
}

// pageCacheRows 汇总当前仍在缓存里的页面。
func (a *App) pageCacheRows() []pageCacheRow {
	now := time.Now()
	var rows []pageCacheRow
	eachPage(func(it navItem) {
		st, ok := a.pages[it.id]
		if !ok {
			return
		}
		rows = append(rows, pageCacheRow{
			id:      it.id,
			label:   it.label,
			policy:  policyName(it.policy.keep),
			enters:  st.enters,
			age:     humanDuration(now.Sub(st.born)),
			idle:    humanDuration(now.Sub(st.lastSeen)),
			scrollY: st.scroll.Y,
		})
	})
	return rows
}

func policyName(k keepMode) string {
	switch k {
	case keepPinned:
		return "常驻"
	case keepFresh:
		return "不缓存"
	default:
		return "缓存"
	}
}

// humanDuration 把时长转成「x 秒 / x 分钟 / x 小时」这样的短文本。
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return itoa(int(d.Seconds())) + " 秒"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + " 分"
	default:
		return itoa(int(d.Hours())) + " 小时"
	}
}

// itoa 是 strconv.Itoa 的极小替代，避免为一个数字引入额外 import。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
