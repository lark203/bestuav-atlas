// Package mobile 是手机端（Flutter）进入 Go 内核的唯一入口。
//
// 为 gomobile bind 而生，因此刻意遵守三条限制：
//  1. 只导出三个函数（Open / Close / Call），其余全是内部实现——绑定面越小越好查；
//  2. 参数与返回都是字符串（JSON），不导出结构体：gomobile 对 map/interface 支持有限，
//     而 JSON 在 Dart 侧本来就要解析；
//  3. 不 import 任何 ui / mygo 包：那些只对桌面成立，带进来会让 android/ios 编不过。
//
// 方法表（method 名 → 请求/返回）见 docs/20261111_星图移动端（bestuav-app）重构规划.md 4.1。
package mobile

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	// 手机系统的时区库不一定全（尤其 Android 老机型），内嵌一份免得「今天」算错。
	_ "time/tzdata"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/syncwire"
)

// 待办的状态字面量：与桌面端 view 层、数据库里存的值必须完全一致。
const (
	statusTodo = "待办"
	statusDone = "已完成"
)

var (
	mu sync.Mutex
	db *store.Store
)

// Open 打开（必要时创建）本地数据库。重复调用是空操作，返回 nil。
//
// path 由 Dart 侧传入（Android: getApplicationSupportDirectory()/atlas.db，
// iOS: Application Support）。不要传缓存目录：系统会清理。
func Open(path string) error {
	mu.Lock()
	defer mu.Unlock()
	if db != nil {
		return nil
	}
	s, err := store.Open(path)
	if err != nil {
		return err
	}
	db = s
	return nil
}

// Close 关闭数据库。
func Close() error {
	mu.Lock()
	defer mu.Unlock()
	if db == nil {
		return nil
	}
	err := db.Close()
	db = nil
	return err
}

// Call 执行一个方法。reqJSON 为空串按 "{}" 处理。
//
// 出错时返回的 error 会被 gomobile 转成平台异常（Android Exception / iOS NSError），
// Dart 侧统一按「调用失败」处理，不需要自己解析错误码。
func Call(method, reqJSON string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if db == nil {
		return "", fmt.Errorf("atlas_core: 数据库尚未打开（先调用 Open）")
	}
	req := strings.TrimSpace(reqJSON)
	if req == "" {
		req = "{}"
	}

	switch method {
	case "schema.version":
		return jsonOf(map[string]any{
			"version":  store.SchemaVersion(),
			"protocol": syncwire.ProtocolVersion,
		})
	case "projects.list":
		return projectsList()
	case "todos.list":
		return todosList(req)
	case "todos.today":
		return todosToday(req)
	case "todos.save":
		return todosSave(req)
	case "todos.delete":
		return todosDelete(req)
	case "todos.set_status":
		return todosSetStatus(req)
	case "sync.pending":
		return syncPending()
	case "sync.apply":
		return syncApply(req)
	case "sync.mark_sent":
		return syncMarkSent(req)
	case "reminders.window":
		return remindersWindow(req)
	}
	return "", fmt.Errorf("atlas_core: 未知方法 %q", method)
}

// ---- 查询 ----

func projectsList() (string, error) {
	projects, err := db.ListProjects()
	if err != nil {
		return "", err
	}
	todos, err := db.ListTodos()
	if err != nil {
		return "", err
	}
	open := map[int64]int{}
	for _, t := range todos {
		if t.Status != statusDone {
			open[t.ProjectID]++
		}
	}

	type item struct {
		syncwire.Project
		OpenCount int `json:"open_count"`
	}
	out := make([]item, 0, len(projects))
	for _, p := range projects {
		out = append(out, item{
			Project: syncwire.Project{
				UID:       p.UID,
				Name:      p.Name,
				Desc:      p.Desc,
				Status:    p.Status,
				CreatedAt: wireTime(p.CreatedAt),
				UpdatedAt: wireTime(p.UpdatedAt),
			},
			OpenCount: open[p.ID],
		})
	}
	return jsonOf(map[string]any{"items": out})
}

type listReq struct {
	Status     string `json:"status"`
	ProjectUID string `json:"project_uid"`
	Limit      int    `json:"limit"`
}

func todosList(reqJSON string) (string, error) {
	var req listReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	todos, err := db.ListTodos()
	if err != nil {
		return "", err
	}
	uidByID, err := projectUIDs()
	if err != nil {
		return "", err
	}

	out := make([]syncwire.Todo, 0, len(todos))
	for _, t := range todos {
		if req.Status != "" && t.Status != req.Status {
			continue
		}
		if req.ProjectUID != "" && uidByID[t.ProjectID] != req.ProjectUID {
			continue
		}
		out = append(out, toWireTodo(t, uidByID))
		if limitReached(req.Limit, len(out)) {
			break
		}
	}
	return jsonOf(map[string]any{"items": out})
}

type todayReq struct {
	Day   string `json:"day"`   // YYYY-MM-DD；空 = 设备时区的今天
	TZ    string `json:"tz"`    // IANA 时区名；空 = 系统时区
	Limit int    `json:"limit"` // 每组上限
}

// todosToday 复刻桌面端「今天要做」的口径：
//
//	未完成，且 开始日(零值回退创建日) ≤ 今天 ≤ 截止日(无截止日 = 一直有效)。
//
// 逾期单独一组返回，不混进「今天」——桌面上这两个列表也是分开的
// （见 internal/view/atlas_data.go 的 todoActiveToday）。
func todosToday(reqJSON string) (string, error) {
	var req todayReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	loc, day, err := resolveDay(req.Day, req.TZ)
	if err != nil {
		return "", err
	}
	todos, err := db.ListTodos()
	if err != nil {
		return "", err
	}
	uidByID, err := projectUIDs()
	if err != nil {
		return "", err
	}

	today := make([]syncwire.Todo, 0, 16)
	overdue := make([]syncwire.Todo, 0, 8)
	for _, t := range todos {
		if t.Status == statusDone {
			continue
		}
		start := t.StartAt
		if start.IsZero() {
			start = t.CreatedAt
		}
		if !start.IsZero() && dayOf(start, loc).After(day) {
			continue // 还没到开始日
		}
		if !t.DueAt.IsZero() && dayOf(t.DueAt, loc).Before(day) {
			overdue = append(overdue, toWireTodo(t, uidByID))
			continue
		}
		today = append(today, toWireTodo(t, uidByID))
	}

	sort.SliceStable(today, func(i, j int) bool { return sortKey(today[i]) < sortKey(today[j]) })
	sort.SliceStable(overdue, func(i, j int) bool { return overdue[i].DueAt < overdue[j].DueAt })
	if req.Limit > 0 {
		if len(today) > req.Limit {
			today = today[:req.Limit]
		}
		if len(overdue) > req.Limit {
			overdue = overdue[:req.Limit]
		}
	}

	return jsonOf(map[string]any{
		"day":     day.Format("2006-01-02"),
		"today":   today,
		"overdue": overdue,
		"counts":  map[string]int{"today": len(today), "overdue": len(overdue)},
	})
}

type windowReq struct {
	Hours int    `json:"hours"`
	Limit int    `json:"limit"`
	TZ    string `json:"tz"`
}

// remindersWindow 返回「接下来一段时间内会到期」的未完成待办，供手机重排本地通知。
//
// 只给窗口而不是全量：iOS 最多只允许 64 条待定通知，排多了会被系统丢掉，
// 与其让系统决定丢哪条，不如我们自己只排最近的。
func remindersWindow(reqJSON string) (string, error) {
	var req windowReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	if req.Hours <= 0 {
		req.Hours = 72
	}
	if req.Limit <= 0 || req.Limit > 60 {
		req.Limit = 60 // iOS 上限 64，留几条给「立刻提醒」
	}
	todos, err := db.ListTodos()
	if err != nil {
		return "", err
	}
	uidByID, err := projectUIDs()
	if err != nil {
		return "", err
	}

	now := time.Now()
	until := now.Add(time.Duration(req.Hours) * time.Hour)
	out := make([]syncwire.Todo, 0, req.Limit)
	for _, t := range todos {
		if t.Status == statusDone || t.DueAt.IsZero() {
			continue
		}
		if t.DueAt.Before(now) || t.DueAt.After(until) {
			continue
		}
		out = append(out, toWireTodo(t, uidByID))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].DueAt < out[j].DueAt })
	if len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return jsonOf(map[string]any{"items": out})
}

// ---- 写入 ----

type saveReq struct {
	UID        string `json:"uid"` // 空 = 新建
	Title      string `json:"title"`
	Note       string `json:"note"`
	ProjectUID string `json:"project_uid"`
	// Priority 用指针区分「没传」与「显式传 0（低）」。
	Priority  *int   `json:"priority"`
	Important bool   `json:"important"`
	Urgent    bool   `json:"urgent"`
	Status    string `json:"status"`
	StartAt   string `json:"start_at"`
	DueAt     string `json:"due_at"`
}

func todosSave(reqJSON string) (string, error) {
	var req saveReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.Title) == "" {
		return "", fmt.Errorf("atlas_core: 待办标题不能为空")
	}
	projectID, err := db.ProjectIDByUID(req.ProjectUID)
	if err != nil {
		return "", err
	}

	created := false
	var id int64
	if req.UID == "" {
		id, err = db.CreateTodo(store.Todo{
			Title:     req.Title,
			Note:      req.Note,
			ProjectID: projectID,
			Priority:  intOr(req.Priority, 1),
			Important: req.Important,
			Urgent:    req.Urgent,
			Status:    orDefault(req.Status, statusTodo),
			StartAt:   parseWireTime(req.StartAt),
			DueAt:     parseWireTime(req.DueAt),
		})
		if err != nil {
			return "", err
		}
		created = true
	} else {
		cur, ok, err := db.TodoByUID(req.UID)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("atlas_core: 待办不存在：%s", req.UID)
		}
		cur.Title = req.Title
		cur.Note = req.Note
		cur.ProjectID = projectID
		cur.Priority = intOr(req.Priority, cur.Priority)
		cur.Important = req.Important
		cur.Urgent = req.Urgent
		cur.Status = orDefault(req.Status, cur.Status)
		cur.StartAt = parseWireTime(req.StartAt)
		cur.DueAt = parseWireTime(req.DueAt)
		if err := db.UpdateTodo(cur); err != nil {
			return "", err
		}
		id = cur.ID
	}

	saved, ok, err := db.TodoByID(id)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("atlas_core: 保存后读不回待办（id=%d）", id)
	}
	uidByID, err := projectUIDs()
	if err != nil {
		return "", err
	}
	return jsonOf(map[string]any{"todo": toWireTodo(saved, uidByID), "created": created})
}

type uidReq struct {
	UID string `json:"uid"`
}

func todosDelete(reqJSON string) (string, error) {
	var req uidReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	t, ok, err := db.TodoByUID(req.UID)
	if err != nil {
		return "", err
	}
	if !ok {
		// 手机上重复点删除、或同步刚把它删掉：当成已经完成，不报错。
		return jsonOf(map[string]any{"deleted": false})
	}
	if err := db.DeleteTodo(t.ID); err != nil {
		return "", err
	}
	return jsonOf(map[string]any{"deleted": true})
}

type statusReq struct {
	UID    string `json:"uid"`
	Status string `json:"status"`
}

func todosSetStatus(reqJSON string) (string, error) {
	var req statusReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.Status) == "" {
		return "", fmt.Errorf("atlas_core: 状态不能为空")
	}
	t, ok, err := db.TodoByUID(req.UID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("atlas_core: 待办不存在：%s", req.UID)
	}
	if err := db.SetTodoStatus(t.ID, req.Status); err != nil {
		return "", err
	}
	return jsonOf(map[string]any{"ok": true})
}

// ---- 同步 ----

func syncPending() (string, error) {
	ops, err := db.PendingOps()
	if err != nil {
		return "", err
	}
	seq, err := db.SyncState("last_seq")
	if err != nil {
		return "", err
	}
	return jsonOf(map[string]any{"ops": ops, "last_seq": seq, "count": len(ops)})
}

type applyReq struct {
	Ops     []syncwire.Op `json:"ops"`
	NextSeq int64         `json:"next_seq"`
}

func syncApply(reqJSON string) (string, error) {
	var req applyReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	applied, skipped, err := db.ApplyOps(req.Ops)
	if err != nil {
		return "", err
	}
	// 合并成功才推进水位：中途出错时下次还会重拉同一段，op 幂等所以无害。
	if err := db.MarkOpsSent(nil, req.NextSeq); err != nil {
		return "", err
	}
	return jsonOf(map[string]any{"applied": applied, "skipped": skipped})
}

type markSentReq struct {
	OpIDs []string `json:"op_ids"`
}

// syncMarkSent 只标记「已推送」；**不推进水位**。
//
// 服务端在 Push 响应里回的 seq 是全局水位，可能已经包含别的设备在这中间推的变更；
// 拿它当 last_seq 会跳过那几条且永远拉不回来。水位只由 sync.apply 推进。
func syncMarkSent(reqJSON string) (string, error) {
	var req markSentReq
	if err := decode(reqJSON, &req); err != nil {
		return "", err
	}
	if err := db.MarkOpsSent(req.OpIDs, 0); err != nil {
		return "", err
	}
	return jsonOf(map[string]any{"ok": true})
}

// ---- 小工具 ----

func decode(reqJSON string, dst any) error {
	if err := json.Unmarshal([]byte(reqJSON), dst); err != nil {
		return fmt.Errorf("atlas_core: 参数不是合法 JSON: %w", err)
	}
	return nil
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("atlas_core: 序列化结果失败: %w", err)
	}
	return string(b), nil
}

// wireTime 与 store 的 timeText 同构：零值空串，其余 RFC3339Nano 的 UTC。
func wireTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseWireTime(s string) time.Time {
	if strings.TrimSpace(s) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func toWireTodo(t store.Todo, projectUID map[int64]string) syncwire.Todo {
	return syncwire.Todo{
		UID:        t.UID,
		Title:      t.Title,
		Note:       t.Note,
		ProjectUID: projectUID[t.ProjectID],
		Priority:   t.Priority,
		Important:  t.Important,
		Urgent:     t.Urgent,
		Status:     t.Status,
		StartAt:    wireTime(t.StartAt),
		DueAt:      wireTime(t.DueAt),
		DoneAt:     wireTime(t.DoneAt),
		NotifiedAt: wireTime(t.NotifiedAt),
		CreatedAt:  wireTime(t.CreatedAt),
		UpdatedAt:  wireTime(t.UpdatedAt),
	}
}

// projectUIDs 一次性取出 id → uid 的映射，避免每条待办都查一次库。
func projectUIDs() (map[int64]string, error) {
	projects, err := db.ListProjects()
	if err != nil {
		return nil, err
	}
	out := make(map[int64]string, len(projects))
	for _, p := range projects {
		out[p.ID] = p.UID
	}
	return out, nil
}

// resolveDay 把「哪一天」解析成设备时区下的零点；day 为空表示今天。
func resolveDay(day, tz string) (*time.Location, time.Time, error) {
	loc := time.Local
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("atlas_core: 未知时区 %q: %w", tz, err)
		}
		loc = l
	}
	if strings.TrimSpace(day) == "" {
		now := time.Now().In(loc)
		return loc, time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc), nil
	}
	d, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("atlas_core: 日期应为 YYYY-MM-DD，实际 %q", day)
	}
	return loc, d, nil
}

// dayOf 取某个时刻在 loc 下的日期零点。
func dayOf(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// sortKey 让「有截止时间的排在无截止时间之前，同截止时间按优先级」成立。
func sortKey(t syncwire.Todo) string {
	if t.DueAt == "" {
		return "9" // 任何真实时间戳都以 "2" 开头，用 9 把它们排到最后
	}
	return t.DueAt
}

func limitReached(limit, n int) bool { return limit > 0 && n >= limit }

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
