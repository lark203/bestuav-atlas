package view

import (
	"encoding/json"
	"fmt"

	"bestuav-atlas/internal/logging"
)

// 页面私有持久化：页面里要「记住」的状态（筛选条件、草稿、上次选中的一行…）放这里，
// 不必改 settings 的结构体、也不必给数据库加表。
//
// 存储是 SQLite 的 page_pref 表，按页面 id 命名空间隔离；值是 JSON。页面 id 取当前
// 页面（a.page），所以页面函数里直接 a.Prefs() 即可，不用重复写页面 id：
//
//	p := a.Prefs()
//	var f orderFilter
//	if p.Get("filter", &f) { ... }   // 读到上次的条件；没存过则 f 保持零值
//	p.Set("filter", f)               // 记住（失败只记日志，不打断业务）
//
// 边界：应用级偏好（主题、字号、快捷键…）仍归 settings.json，不进这张表；这里只放
// 「只属于某个页面」的状态。数据不会因页面销毁而清除——「记住」本来就该跨重启。

// pagePrefs 绑定到某个页面的私有键值存储。
type pagePrefs struct {
	a    *App
	page string
}

// Prefs 返回当前页面的私有键值存储。
//
// 只能在页面函数或该页的 onEnter/onLeave 里调用（那时 a.page 就是这一页）。
func (a *App) Prefs() *pagePrefs { return a.PrefsFor(a.page) }

// PrefsFor 返回**指定页面**的私有键值存储。
//
// 跨页面的场景用这个：例如设置页里渲染「页面级设置」时，要读写的是那个页面的状态，
// 而不是设置页自己的。页面内仍然用 a.Prefs()。
func (a *App) PrefsFor(page string) *pagePrefs { return &pagePrefs{a: a, page: page} }

// Get 读取 key 并反序列化到 dst；ok=false 表示没存过（dst 保持不变）。
//
// 历史 JSON 解析失败时记一条 WARN 并按「没存过」处理：页面代码演进后（字段改名、
// 类型变化）不该因为旧值卡死，用户最多丢一次上次的状态。
func (p *pagePrefs) Get(key string, dst any) bool {
	raw, ok := p.a.prefRaw(p.page, key)
	if !ok {
		return false
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		logging.Warn("pref", "页面状态解析失败，已忽略",
			"page", p.page, "key", key, "err", err.Error())
		return false
	}
	return true
}

// Set 序列化并写入 key。返回的 error 供页面决定是否提示（多数页面忽略即可）；
// 序列化失败的常见原因是传了 channel / func 这类不能转 JSON 的值。
func (p *pagePrefs) Set(key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("页面状态无法序列化为 JSON: %w", err)
	}
	return p.a.setPrefRaw(p.page, key, string(b))
}

// Delete 删除 key（下一次 Get 返回 false）。
func (p *pagePrefs) Delete(key string) {
	if err := p.a.delPrefRaw(p.page, key); err != nil {
		logging.Errv("pref", "删除页面状态失败", err, "page", p.page, "key", key)
	}
}

// prefRaw 读页面私有键：内存缓存优先，其次数据库；数据库读到后回填缓存。
//
// 缓存的意义：① 数据库不可用（a.store == nil）时页面状态仍能在本次运行内保持；
// ② 同一帧多次读同一个键不必反复查库（页面函数每帧都会读上次的筛选条件）。
func (a *App) prefRaw(page, key string) (string, bool) {
	k := prefMemKey(page, key)
	if v, ok := a.prefMem[k]; ok {
		return v, true
	}
	if a.store == nil {
		return "", false
	}
	v, ok, err := a.store.PagePref(page, key)
	if err != nil {
		logging.Errv("pref", "读取页面状态失败", err, "page", page, "key", key)
		return "", false
	}
	if ok {
		a.rememberPref(k, v)
	}
	return v, ok
}

// setPrefRaw 写页面私有键：先更新内存缓存，再尽力写库。
func (a *App) setPrefRaw(page, key, value string) error {
	if page == "" || key == "" {
		return fmt.Errorf("页面状态需要页面 id 与 key")
	}
	a.rememberPref(prefMemKey(page, key), value)
	if a.store == nil {
		return nil // 纯内存：本次运行内有效，与「库打不开时一切照常可用」的约定一致
	}
	if err := a.store.SetPagePref(page, key, value); err != nil {
		logging.Errv("pref", "写入页面状态失败", err, "page", page, "key", key)
		return err
	}
	return nil
}

// delPrefRaw 删页面私有键（内存缓存与库都要删，否则下次读到旧缓存）。
func (a *App) delPrefRaw(page, key string) error {
	delete(a.prefMem, prefMemKey(page, key))
	if a.store == nil {
		return nil
	}
	if err := a.store.DeletePagePref(page, key); err != nil {
		return err
	}
	return nil
}

// rememberPref 写入内存缓存（懒初始化）。
func (a *App) rememberPref(k, v string) {
	if a.prefMem == nil {
		a.prefMem = make(map[string]string, 8)
	}
	a.prefMem[k] = v
}

// prefMemKey 内存缓存的键：页面与 key 用不会出现在 id 里的分隔符拼起来。
func prefMemKey(page, key string) string { return page + "\x00" + key }

// prefCount 页面状态总条数（设置页展示用；无库时为内存缓存条数）。
func (a *App) prefCount() int64 {
	if a.store != nil {
		n, err := a.store.PagePrefCount()
		if err != nil {
			logging.Errv("pref", "统计页面状态失败", err)
			return 0
		}
		return n
	}
	return int64(len(a.prefMem))
}

// clearPrefs 清空所有页面状态（设置页「清理页面状态」）。
//
// 只动 page_pref：任务、授权记录、应用偏好与日志都不受影响。内存缓存必须一起清，
// 否则清完还会读到旧值。
func (a *App) clearPrefs() {
	var n int64
	if a.store != nil {
		deleted, err := a.store.ClearPagePrefs()
		if err != nil {
			logging.Errv("pref", "清空页面状态失败", err)
			a.toast("清理页面状态失败：" + err.Error())
			return
		}
		n = deleted
	} else {
		n = int64(len(a.prefMem))
	}
	a.prefMem = nil
	logging.Audit("pref", "清空页面状态", "count", n)
	a.toast(fmt.Sprintf("已清理 %d 条页面状态", n))
}
