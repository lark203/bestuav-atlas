package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"bestuav-atlas/internal/syncwire"
)

// 本文件是同步的「本地半边」：身份（uid）、变更日志（sync_op）、水位（sync_state）、
// 以及把远端变更合并回本地（ApplyOps）。
//
// 设计要点（与 docs/20261111_星图移动端（bestuav-app）重构规划.md 对应）：
//
//  1. 本地主键仍是自增 INTEGER：界面、外键、排序全部依赖它，不动。
//     跨设备只认 uid（ULID），两者在本地一一对应。
//  2. 每次用户写入都在**同一个事务**里追加一条 sync_op——先写数据、后写日志，
//     两步同生共死，避免出现「数据变了、日志没记」这种静默漏同步。
//  3. sync_op 记的是**字段补丁**而不是整行快照：手机只勾完成、桌面同时改标题时，
//     两条 patch 各自只碰自己那个字段，谁也不覆盖谁。这是不做 CRDT 也能接受的全部原因。
//  4. 合并顺序由服务端 seq 决定（不是设备时钟），所以不需要 HLC：
//     冲突就是「后到者胜」，但只对**同一个字段**成立。
//  5. 有未发送本地修改的字段，远端变更**不覆盖**——否则用户刚在手机上打完字，
//     一次后台同步就把它擦了。代价是这台设备会赢下这个字段，等它推送后全网收敛。

// execer 让同一段 SQL 既能跑在 *sql.DB 上，也能跑在事务里。
// 「改数据 + 记日志」必须共用事务，所以 insert 类函数收这个接口而不是收 Store。
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// queryRower 同理，用于在事务里读回刚写完的行。
type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

// ---- 同步身份 ----

// crockford 是 ULID 的字母表：去掉 I / L / O / U，避免人工抄写混淆。
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newUID 生成一个新的 ULID（26 字符，字典序即时间序）。
func newUID() string { return newUIDAt(time.Now()) }

// newUIDAt 按给定时刻生成 ULID，便于测试断言前缀。
//
// 结构：48 位毫秒时间戳 + 80 位随机，编码成 130 位（前面补 2 个零位）的
// Crockford Base32，正好 26 个字符。
func newUIDAt(t time.Time) string {
	var raw [16]byte
	ms := uint64(t.UnixMilli())
	raw[0] = byte(ms >> 40)
	raw[1] = byte(ms >> 32)
	raw[2] = byte(ms >> 24)
	raw[3] = byte(ms >> 16)
	raw[4] = byte(ms >> 8)
	raw[5] = byte(ms)
	if _, err := rand.Read(raw[6:]); err != nil {
		// 随机源不可用时退回纳秒时间：同毫秒内仍可能重复，但远好过 panic。
		ns := uint64(t.UnixNano())
		for i := 0; i < 10; i++ {
			raw[6+i] = byte(ns >> (uint(i) * 5))
		}
	}

	var out [26]byte
	idx, acc, bits := 0, uint32(0), uint(2) // 前 2 位恒为 0
	for _, b := range raw {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out[idx] = crockford[(acc>>bits)&31]
			idx++
		}
	}
	if bits > 0 {
		out[idx] = crockford[(acc<<(5-bits))&31]
		idx++
	}
	return string(out[:idx])
}

// backfillUIDs 给没有 uid 的老数据补上（幂等）。
//
// 必须独立成一步：CREATE TABLE IF NOT EXISTS 不会给已存在的表加列，
// 而 ADD COLUMN 又不能带默认表达式，所以只能「先补列 → 再逐行回填」。
func (s *Store) backfillUIDs() error {
	for _, table := range []string{"project", "todo"} {
		rows, err := s.db.Query(`SELECT id FROM ` + table + ` WHERE uid = '' ORDER BY id`)
		if err != nil {
			return fmt.Errorf("store: 查询待回填 uid 的 %s 失败: %w", table, err)
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("store: 读取 %s.id 失败: %w", table, err)
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			// 表名来自上面的白名单常量，id 是参数化传入。
			if _, err := s.db.Exec(`UPDATE `+table+` SET uid = ? WHERE id = ?`, newUID(), id); err != nil {
				return fmt.Errorf("store: 回填 %s.uid 失败: %w", table, err)
			}
		}
	}
	return nil
}

// ---- 变更日志 ----

// appendOp 在调用方的事务里追加一条变更日志。
//
// 接受 *sql.Tx 而不是 Store，就是为了让「改数据」和「记日志」绑在同一个事务上；
// 调用方若是直接写 s.db，说明这次写入本来就不该同步（如远端合并），别用这个函数。
func appendOp(tx *sql.Tx, entity, uid, kind string, patch map[string]any) error {
	payload := "{}"
	if len(patch) > 0 {
		b, err := json.Marshal(patch)
		if err != nil {
			return fmt.Errorf("store: 编码变更补丁失败: %w", err)
		}
		payload = string(b)
	}
	if _, err := tx.Exec(
		`INSERT INTO sync_op(op_id, entity, uid, kind, payload, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		newUID(), entity, uid, kind, payload, nowText()); err != nil {
		return fmt.Errorf("store: 写入变更日志失败: %w", err)
	}
	return nil
}

// PendingOps 返回尚未推送的变更，按写入顺序。
//
// 用 rowid 而不是 op_id 排序：同一毫秒内多条变更的 ULID 随机后缀大小不定，
// 而 rowid 严格反映插入顺序——本地变更的先后只由用户操作顺序决定。
func (s *Store) PendingOps() ([]syncwire.Op, error) {
	rows, err := s.db.Query(
		`SELECT op_id, entity, uid, kind, payload, created_at
		   FROM sync_op WHERE sent_at = '' ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询待推送变更失败: %w", err)
	}
	defer rows.Close()

	var out []syncwire.Op
	for rows.Next() {
		var (
			op                 syncwire.Op
			payload, createdAt string
		)
		if err := rows.Scan(&op.OpID, &op.Entity, &op.UID, &op.Kind, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("store: 读取待推送变更失败: %w", err)
		}
		op.CreatedAt = createdAt
		if payload != "" && payload != "{}" {
			if err := json.Unmarshal([]byte(payload), &op.Patch); err != nil {
				return nil, fmt.Errorf("store: 解析变更补丁失败(%s): %w", op.OpID, err)
			}
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// MarkOpsSent 标记已推送的变更，并把同步水位推进到 seq（seq<=0 表示只标记不推进）。
//
// 水位只增不减：乱序/重放的响应不能让客户端以为「已经同步到更早的位置」。
//
// 注意：**推送成功后不要传服务端返回的 seq**。那是最新一轮的全局水位，可能已经包含
// 别的设备在中间推的变更；拿它当 last_seq 会跳过那几条且永远拉不回来。
// 水位只由「拉取并应用」推进（见 syncclient.PushPull）。
func (s *Store) MarkOpsSent(opIDs []string, seq int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: 开始事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := nowText()
	for _, id := range opIDs {
		if _, err := tx.Exec(`UPDATE sync_op SET sent_at = ? WHERE op_id = ? AND sent_at = ''`, now, id); err != nil {
			return fmt.Errorf("store: 标记变更已推送失败: %w", err)
		}
	}
	if seq > 0 {
		if _, err := tx.Exec(
			`INSERT INTO sync_state(key, value) VALUES('last_seq', ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value
			 WHERE CAST(excluded.value AS INTEGER) > CAST(sync_state.value AS INTEGER)`,
			fmt.Sprint(seq)); err != nil {
			return fmt.Errorf("store: 推进同步水位失败: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交同步状态失败: %w", err)
	}
	return nil
}

// SyncState 读同步状态；不存在时返回空串。
func (s *Store) SyncState(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM sync_state WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: 读取同步状态失败: %w", err)
	}
	return v, nil
}

// SetSyncState 写同步状态（覆盖）。
func (s *Store) SetSyncState(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO sync_state(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: 写入同步状态失败: %w", err)
	}
	return nil
}

// lastSeq 把 last_seq 解析成整数（空/坏值都当 0，从最早开始拉）。
func (s *Store) lastSeq() (int64, error) {
	v, err := s.SyncState("last_seq")
	if err != nil || v == "" {
		return 0, err
	}
	var n int64
	if _, err := fmt.Sscan(v, &n); err != nil {
		return 0, nil
	}
	return n, nil
}

// pendingFields 返回某个实体上「本地已改但还没推送」的字段集合。
//
// 返回通配 "*" 表示本地有未发送的删除：整行都不该被远端覆盖。
func (s *Store) pendingFields(uid string) (map[string]bool, error) {
	rows, err := s.db.Query(
		`SELECT kind, payload FROM sync_op WHERE uid = ? AND sent_at = '' ORDER BY rowid`, uid)
	if err != nil {
		return nil, fmt.Errorf("store: 查询本地未发送变更失败: %w", err)
	}
	defer rows.Close()

	fields := map[string]bool{}
	for rows.Next() {
		var kind, payload string
		if err := rows.Scan(&kind, &payload); err != nil {
			return nil, fmt.Errorf("store: 读取本地未发送变更失败: %w", err)
		}
		if kind == syncwire.KindDelete {
			fields["*"] = true
			continue
		}
		var patch map[string]any
		if err := json.Unmarshal([]byte(payload), &patch); err != nil {
			continue // 坏补丁不该拖垮整次合并，按「没有保护」处理
		}
		for k := range patch {
			fields[k] = true
		}
	}
	return fields, rows.Err()
}

// ---- 合并远端变更 ----

// ApplyOps 把拉取到的变更合并进本地，返回应用条数与跳过原因。
//
// 调用方（手机 / 桌面同步层）负责在成功后推进水位；本函数不碰 last_seq，
// 这样上层可以把「合并成功」与「水位落库」放进同一次 UI 事务里。
func (s *Store) ApplyOps(ops []syncwire.Op) (applied int, skipped []string, err error) {
	for _, op := range ops {
		// 自己发出去的变更被服务端回放：本地已经是这个状态，跳过。
		if op.OpID != "" {
			var one int
			switch e := s.db.QueryRow(`SELECT 1 FROM sync_op WHERE op_id = ?`, op.OpID).Scan(&one); {
			case e == nil:
				continue
			case e != sql.ErrNoRows:
				return applied, skipped, fmt.Errorf("store: 检查变更回环失败: %w", e)
			}
		}

		var ok bool
		var reason string
		switch op.Entity {
		case syncwire.EntityTodo:
			ok, reason, err = s.applyTodo(op)
		case syncwire.EntityProject:
			ok, reason, err = s.applyProject(op)
		default:
			skipped = append(skipped, "未知实体 "+op.Entity)
			continue
		}
		if err != nil {
			return applied, skipped, err
		}
		if ok {
			applied++
		} else if reason != "" {
			skipped = append(skipped, reason)
		}
	}
	return applied, skipped, nil
}

func (s *Store) applyTodo(op syncwire.Op) (bool, string, error) {
	switch op.Kind {
	case syncwire.KindDelete:
		pending, err := s.pendingFields(op.UID)
		if err != nil {
			return false, "", err
		}
		if len(pending) > 0 {
			return false, "本地有未发送的修改，保留待办", nil
		}
		res, err := s.db.Exec(`DELETE FROM todo WHERE uid = ?`, op.UID)
		if err != nil {
			return false, "", fmt.Errorf("store: 合并删除待办失败: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return false, "", nil // 本地本来就没有：幂等
		}
		return true, "", nil

	case syncwire.KindCreate, syncwire.KindPatch:
		var id int64
		err := s.db.QueryRow(`SELECT id FROM todo WHERE uid = ?`, op.UID).Scan(&id)
		switch {
		case err == nil:
			// 已经存在：create 当 patch 处理，保证重放幂等。
			return s.patchTodo(id, op.UID, op.Patch)
		case err == sql.ErrNoRows && op.Kind == syncwire.KindCreate:
			return s.createTodoFromPatch(op)
		case err == sql.ErrNoRows:
			return false, "未知待办 " + op.UID, nil
		default:
			return false, "", fmt.Errorf("store: 查询待合并待办失败: %w", err)
		}
	}
	return false, "未知变更种类 " + op.Kind, nil
}

func (s *Store) applyProject(op syncwire.Op) (bool, string, error) {
	switch op.Kind {
	case syncwire.KindDelete:
		pending, err := s.pendingFields(op.UID)
		if err != nil {
			return false, "", err
		}
		if len(pending) > 0 {
			return false, "本地有未发送的修改，保留项目", nil
		}
		res, err := s.db.Exec(`DELETE FROM project WHERE uid = ?`, op.UID)
		if err != nil {
			return false, "", fmt.Errorf("store: 合并删除项目失败: %w", err)
		}
		n, _ := res.RowsAffected()
		return n > 0, "", nil

	case syncwire.KindCreate, syncwire.KindPatch:
		var id int64
		err := s.db.QueryRow(`SELECT id FROM project WHERE uid = ?`, op.UID).Scan(&id)
		switch {
		case err == nil:
			return s.patchProject(id, op.UID, op.Patch)
		case err == sql.ErrNoRows && op.Kind == syncwire.KindCreate:
			return s.createProjectFromPatch(op)
		case err == sql.ErrNoRows:
			return false, "未知项目 " + op.UID, nil
		default:
			return false, "", fmt.Errorf("store: 查询待合并项目失败: %w", err)
		}
	}
	return false, "未知变更种类 " + op.Kind, nil
}

// todoPatchKeys 是允许通过同步写入的 todo 字段（列名）。
//
// 白名单而不是黑名单：将来加列时默认不同步，需要显式加到这里——
// 免得 id、repo_id 这类「只在本机有意义」的字段被误同步出去。
var todoPatchKeys = map[string]bool{
	"title": true, "note": true, "project_uid": true, "priority": true,
	"important": true, "urgent": true, "status": true,
	"start_at": true, "due_at": true, "done_at": true, "notified_at": true,
	"created_at": true, "updated_at": true,
}

var projectPatchKeys = map[string]bool{
	"name": true, "desc": true, "status": true, "created_at": true, "updated_at": true,
}

func (s *Store) patchTodo(id int64, uid string, patch map[string]any) (bool, string, error) {
	pending, err := s.pendingFields(uid)
	if err != nil {
		return false, "", err
	}
	if pending["*"] {
		return false, "本地有未发送的删除，保留本地", nil
	}

	sets, args, err := s.todoSetClause(patch, pending)
	if err != nil {
		return false, "", err
	}
	if len(sets) == 0 {
		return false, "字段都有本地未发送的修改", nil
	}
	args = append(args, id)
	if _, err := s.db.Exec(`UPDATE todo SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return false, "", fmt.Errorf("store: 合并待办字段失败: %w", err)
	}
	return true, "", nil
}

func (s *Store) patchProject(id int64, uid string, patch map[string]any) (bool, string, error) {
	pending, err := s.pendingFields(uid)
	if err != nil {
		return false, "", err
	}
	if pending["*"] {
		return false, "本地有未发送的删除，保留本地", nil
	}

	keys := make([]string, 0, len(patch))
	for k := range patch {
		if projectPatchKeys[k] && !pending[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return false, "字段都有本地未发送的修改", nil
	}
	sort.Strings(keys)

	sets := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)+1)
	for _, k := range keys {
		str, ok := patch[k].(string)
		if !ok {
			return false, "", fmt.Errorf("store: 项目字段 %s 类型错误", k)
		}
		sets = append(sets, k+" = ?")
		args = append(args, str)
	}
	args = append(args, id)
	if _, err := s.db.Exec(`UPDATE project SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return false, "", fmt.Errorf("store: 合并项目字段失败: %w", err)
	}
	return true, "", nil
}

// todoSetClause 把补丁翻译成 SQL 的 SET 片段，跳过受本地保护与白名单外的字段。
func (s *Store) todoSetClause(patch map[string]any, pending map[string]bool) ([]string, []any, error) {
	keys := make([]string, 0, len(patch))
	for k := range patch {
		if todoPatchKeys[k] && !pending[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys) // 固定顺序：便于测试断言，也避免 SQL 文本随机变化

	sets := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)+1)
	for _, k := range keys {
		switch k {
		case "project_uid":
			uid, ok := patch[k].(string)
			if !ok {
				return nil, nil, fmt.Errorf("store: project_uid 类型错误")
			}
			id, err := s.projectIDByUID(uid)
			if err != nil {
				return nil, nil, err
			}
			sets = append(sets, "project_id = ?")
			args = append(args, id)
		case "priority":
			n, err := asInt(patch[k])
			if err != nil {
				return nil, nil, err
			}
			sets = append(sets, "priority = ?")
			args = append(args, n)
		case "important", "urgent":
			b, ok := patch[k].(bool)
			if !ok {
				return nil, nil, fmt.Errorf("store: %s 类型错误", k)
			}
			sets = append(sets, k+" = ?")
			args = append(args, boolInt(b))
		default:
			str, ok := patch[k].(string)
			if !ok {
				return nil, nil, fmt.Errorf("store: %s 类型错误", k)
			}
			sets = append(sets, k+" = ?")
			args = append(args, str)
		}
	}
	return sets, args, nil
}

func (s *Store) createTodoFromPatch(op syncwire.Op) (bool, string, error) {
	t, err := todoFromPatch(op.UID, op.Patch)
	if err != nil {
		return false, "", err
	}
	if strings.TrimSpace(t.Title) == "" {
		return false, "远端待办没有标题", nil
	}
	// project_uid 是跨设备身份，得换成本地的自增 id 才能挂上项目。
	if puid, ok := op.Patch["project_uid"].(string); ok {
		id, err := s.projectIDByUID(puid)
		if err != nil {
			return false, "", err
		}
		t.ProjectID = id
	}
	if _, err := insertTodo(s.db, t); err != nil {
		return false, "", err
	}
	return true, "", nil
}

func (s *Store) createProjectFromPatch(op syncwire.Op) (bool, string, error) {
	p := Project{UID: op.UID}
	applyStringPatch := func(k string, dst *string) {
		if v, ok := op.Patch[k].(string); ok {
			*dst = v
		}
	}
	applyStringPatch("name", &p.Name)
	applyStringPatch("desc", &p.Desc)
	applyStringPatch("status", &p.Status)
	if v, ok := op.Patch["created_at"].(string); ok {
		p.CreatedAt = parseTime(v)
	}
	if v, ok := op.Patch["updated_at"].(string); ok {
		p.UpdatedAt = parseTime(v)
	}
	if strings.TrimSpace(p.Name) == "" {
		return false, "远端项目没有名称", nil
	}
	if _, err := insertProject(s.db, p); err != nil {
		return false, "", err
	}
	return true, "", nil
}

// todoFromPatch 用一个 create 补丁构造 Todo（供合入远端新建的待办）。
func todoFromPatch(uid string, patch map[string]any) (Todo, error) {
	t := Todo{UID: uid, Priority: 1}
	str := func(k string) string {
		v, _ := patch[k].(string)
		return v
	}
	t.Title = str("title")
	t.Note = str("note")
	t.Status = str("status")
	if t.Status == "" {
		t.Status = "待办"
	}
	t.StartAt = parseTime(str("start_at"))
	t.DueAt = parseTime(str("due_at"))
	t.DoneAt = parseTime(str("done_at"))
	t.NotifiedAt = parseTime(str("notified_at"))
	t.CreatedAt = parseTime(str("created_at"))
	t.UpdatedAt = parseTime(str("updated_at"))
	if v, ok := patch["priority"]; ok {
		n, err := asInt(v)
		if err != nil {
			return Todo{}, err
		}
		t.Priority = n
	}
	if v, ok := patch["important"].(bool); ok {
		t.Important = v
	}
	if v, ok := patch["urgent"].(bool); ok {
		t.Urgent = v
	}
	return t, nil
}

// asInt 接受 JSON 解出来的 float64 与内存里的 int（两边类型不同）。
func asInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		return int(n), nil
	default:
		return 0, fmt.Errorf("store: 期望整数，实际 %T", v)
	}
}

// TodoByUID 按跨设备身份取待办；ok=false 表示本地还没有这条。
//
// 手机端只认 uid（用户在手机上新建时还没有本地自增 id），所以对外要留这个入口。
func (s *Store) TodoByUID(uid string) (Todo, bool, error) {
	if uid == "" {
		return Todo{}, false, nil
	}
	t, err := scanTodo(s.db.QueryRow(`SELECT `+todoColumns+` FROM todo WHERE uid = ?`, uid))
	if err == sql.ErrNoRows {
		return Todo{}, false, nil
	}
	if err != nil {
		return Todo{}, false, fmt.Errorf("store: 按 uid 查询待办失败: %w", err)
	}
	return t, true, nil
}

// ProjectIDByUID 把项目 uid 解析成本地 id（手机端写待办时把项目挂上去用）。
func (s *Store) ProjectIDByUID(uid string) (int64, error) { return s.projectIDByUID(uid) }

// projectIDByUID 把项目 uid 解析成本地 id；空 uid 或未同步到的项目返回 0（未归类）。
func (s *Store) projectIDByUID(uid string) (int64, error) {
	if uid == "" {
		return 0, nil
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM project WHERE uid = ?`, uid).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil // 项目还没同步过来：先当未归类，等它到了再指向
	}
	if err != nil {
		return 0, fmt.Errorf("store: 解析项目 uid 失败: %w", err)
	}
	return id, nil
}

// projectUIDByID 把本地项目 id 解析成 uid；0 或查不到返回空串（未归类）。
func projectUIDByID(q queryRower, id int64) (string, error) {
	if id <= 0 {
		return "", nil
	}
	var uid string
	err := q.QueryRow(`SELECT uid FROM project WHERE id = ?`, id).Scan(&uid)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: 解析项目 id 失败: %w", err)
	}
	return uid, nil
}
