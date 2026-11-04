// Package store 是应用的本地持久化层（SQLite）。
//
// 选型：modernc.org/sqlite（**纯 Go 实现**，无需 cgo/gcc）。
//
//	好处：保持 CGO_ENABLED=0，三平台交叉编译与「单文件二进制」的打包方式不变；
//	代价：二进制约增大 10MB，性能比 cgo 版低 2–3 倍——对「本地单机、几千行数据」的
//	桌面场景完全够用（实测本包插入+查询在毫秒级）。
//
// 存什么（三张业务表 + 两张基础表）：
//   - task            异步任务记录（重启后仍在）
//   - license_record  授权凭证生成历史
//   - app_event       应用事件/错误/崩溃（由 internal/logging 的 Recorder 写入）
//   - page_pref       页面私有的键值状态（筛选条件、草稿、上次选中的一行…）
//   - schema_meta     键值元数据（schema 版本、首次演示数据是否已灌入等）
//
// 不存什么：**用户偏好仍留在 settings.json**。单机单用户的键值配置用 JSON 文件是
// 业界惯例（electron-store / .NET user.config 都是这么做的），搬进数据库只会增加
// 迁移成本与故障面。页面私有的零散状态则放 page_pref——它天然是多页多键，用 JSON
// 文件得自己管命名空间与并发，反而不如一张表。
//
// 并发策略：桌面进程只有一个写入者、写入量极小，故 SetMaxOpenConns(1) 串行化所有
// 访问——彻底避免 SQLITE_BUSY，也不需要在调用方到处加锁。
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // 注册 "sqlite" 驱动（纯 Go）
)

// schemaVersion 当前数据库结构版本，写入 schema_meta 便于将来做增量迁移。
//
// 2：新增 page_pref（页面私有键值），见 internal/view/prefs.go。
const schemaVersion = "2"

// SchemaVersion 返回当前数据库结构版本（配置包 manifest 与导入校验用）。
func SchemaVersion() string { return schemaVersion }

// Store 数据库句柄。方法并发安全（底层由 database/sql 串行化）。
type Store struct {
	db   *sql.DB
	path string
}

// Task 一条异步任务记录。
type Task struct {
	ID        int64
	Name      string
	Kind      string // 轻量级 / 重量级
	Status    string // 已完成 / 运行中 / 排队 / 失败
	Cost      string // 耗时展示文本（如 "0.8s"、"刚刚"）
	CreatedAt time.Time
}

// License 一条授权凭证生成记录。
type License struct {
	ID        int64
	Subject   string
	Edition   string
	Term      string
	Token     string
	CreatedAt time.Time
}

// Event 一条应用事件（信息 / 警告 / 错误 / 崩溃）。
type Event struct {
	ID      int64
	TS      time.Time
	Level   string
	Kind    string
	Message string
	Detail  string
}

// Stats 供界面展示的概览计数。
type Stats struct {
	Tasks    int64
	Licenses int64
	Events   int64
	Errors   int64 // level >= ERROR 的事件数
}

// Open 打开（必要时创建）数据库并完成建表。
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: 未指定数据库路径")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: 创建数据目录失败: %w", err)
	}
	// 连接级 PRAGMA 通过 DSN 参数下发（modernc 驱动支持 _pragma=name(value)）：
	//   WAL    读写不互相阻塞，崩溃后恢复更快；
	//   NORMAL 桌面场景下 durability 与性能的平衡点（断电最多丢最后几条事务）；
	//   busy_timeout 万一将来多连接，也不要立刻报 SQLITE_BUSY。
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: 打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1) // 单写入者：串行化，免锁
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Path 返回数据库文件路径（界面展示 / 排障用）。
func (s *Store) Path() string { return s.path }

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// Ping 探测数据库可用性（启动自检用）。
func (s *Store) Ping() error { return s.db.Ping() }

// BackupTo 把数据库完整复制到 dst（不能是已存在的文件）。
//
// 用 SQLite 的 VACUUM INTO：它在一个事务里导出一份一致快照，不受 WAL 里
// 尚未回写的数据影响。直接拷 data.db 文件在 WAL 模式下可能丢最近的写入。
func (s *Store) BackupTo(dst string) error {
	if dst == "" {
		return fmt.Errorf("store: 未指定备份路径")
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("store: 备份目标已存在: %s", dst)
	}
	if _, err := s.db.Exec(`VACUUM INTO ?`, dst); err != nil {
		return fmt.Errorf("store: 备份失败: %w", err)
	}
	return nil
}

// migrate 建表（幂等）。后续版本变更时按 schema_meta 里的版本号分支即可。
func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS task (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL,
			kind       TEXT NOT NULL,
			status     TEXT NOT NULL,
			cost       TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_task_created_at ON task(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS license_record (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			subject    TEXT NOT NULL,
			edition    TEXT NOT NULL,
			term       TEXT NOT NULL,
			token      TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_license_created_at ON license_record(created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS app_event (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			ts      TEXT NOT NULL,
			level   TEXT NOT NULL,
			kind    TEXT NOT NULL,
			message TEXT NOT NULL,
			detail  TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_app_event_ts ON app_event(ts DESC)`,
		// 页面私有键值：页面 id + key 唯一；value 由调用方序列化（视图层用 JSON）。
		`CREATE TABLE IF NOT EXISTS page_pref (
			page       TEXT NOT NULL,
			key        TEXT NOT NULL,
			value      TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (page, key)
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("store: 初始化表结构失败: %w", err)
		}
	}
	// 记录版本：首次写入；已有时只在**新版本更高**时更新。
	//
	// 为什么不是 DO NOTHING：装了新版的用户库里会一直停在旧版本号，之后再按版本分支
	// 迁移就会被误导。为什么不是无条件覆盖：用旧版打开新库（降级）时不该把版本写回去。
	if _, err := s.db.Exec(
		`INSERT INTO schema_meta(key, value) VALUES('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value
		 WHERE CAST(excluded.value AS INTEGER) > CAST(schema_meta.value AS INTEGER)`,
		schemaVersion); err != nil {
		return fmt.Errorf("store: 写入 schema 版本失败: %w", err)
	}
	return nil
}

// ---- 元数据 ----

// Flag 读取元数据键；ok=false 表示不存在。
func (s *Store) Flag(key string) (value string, ok bool, err error) {
	err = s.db.QueryRow(`SELECT value FROM schema_meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetFlag 写入元数据键（覆盖）。
func (s *Store) SetFlag(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO schema_meta(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ---- 页面私有键值 ----

// PagePref 读取某个页面私有的键；ok=false 表示没存过。
//
// page 是菜单注册表里的页面 id，key 由页面自己约定（视图层通常塞一段 JSON）。
// 值原样返回字符串——序列化格式属于调用方，存储层不解释。
func (s *Store) PagePref(page, key string) (value string, ok bool, err error) {
	if page == "" || key == "" {
		return "", false, fmt.Errorf("store: page_pref 的 page/key 不能为空")
	}
	err = s.db.QueryRow(
		`SELECT value FROM page_pref WHERE page = ? AND key = ?`, page, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	return value, err == nil, err
}

// SetPagePref 写入/覆盖一个页面私有键。
func (s *Store) SetPagePref(page, key, value string) error {
	if page == "" || key == "" {
		return fmt.Errorf("store: page_pref 的 page/key 不能为空")
	}
	_, err := s.db.Exec(
		`INSERT INTO page_pref(page, key, value, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(page, key) DO UPDATE SET
		   value = excluded.value, updated_at = excluded.updated_at`,
		page, key, value, nowText())
	return err
}

// DeletePagePref 删除一个页面私有键；不存在不算错。
func (s *Store) DeletePagePref(page, key string) error {
	if page == "" || key == "" {
		return fmt.Errorf("store: page_pref 的 page/key 不能为空")
	}
	_, err := s.db.Exec(`DELETE FROM page_pref WHERE page = ? AND key = ?`, page, key)
	return err
}

// PagePrefCount 返回页面私有键的总数（设置页显示「共 N 条」用）。
func (s *Store) PagePrefCount() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM page_pref`).Scan(&n)
	return n, err
}

// ClearPagePrefs 清空所有页面的私有键，返回删除条数。
//
// 供设置页「清理页面状态」用：页面记住的筛选条件/草稿可以随时丢弃，不影响任务、
// 授权记录、应用偏好与日志。
func (s *Store) ClearPagePrefs() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM page_pref`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

// ---- 任务 ----

// InsertTask 追加一条任务记录。
func (s *Store) InsertTask(name, kind, status, cost string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO task(name, kind, status, cost, created_at) VALUES(?, ?, ?, ?, ?)`,
		name, kind, status, cost, nowText())
	if err != nil {
		return 0, fmt.Errorf("store: 写入任务失败: %w", err)
	}
	return res.LastInsertId()
}

// RecentTasks 取最近 limit 条任务（按时间倒序）。
func (s *Store) RecentTasks(limit int) ([]Task, error) {
	rows, err := s.db.Query(
		`SELECT id, name, kind, status, cost, created_at
		   FROM task ORDER BY id DESC LIMIT ?`, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("store: 查询任务失败: %w", err)
	}
	defer rows.Close()

	var out []Task
	for rows.Next() {
		var t Task
		var created string
		if err := rows.Scan(&t.ID, &t.Name, &t.Kind, &t.Status, &t.Cost, &created); err != nil {
			return nil, err
		}
		t.CreatedAt = parseTime(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteFinishedTasks 删除所有「已完成」的任务记录，返回删除条数。
func (s *Store) DeleteFinishedTasks() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM task WHERE status = ?`, "已完成")
	if err != nil {
		return 0, fmt.Errorf("store: 清理任务失败: %w", err)
	}
	return res.RowsAffected()
}

// ---- 授权记录 ----

// InsertLicense 追加一条授权凭证记录。
func (s *Store) InsertLicense(subject, edition, term, token string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO license_record(subject, edition, term, token, created_at) VALUES(?, ?, ?, ?, ?)`,
		subject, edition, term, token, nowText())
	if err != nil {
		return 0, fmt.Errorf("store: 写入授权记录失败: %w", err)
	}
	return res.LastInsertId()
}

// RecentLicenses 取最近 limit 条授权记录。
func (s *Store) RecentLicenses(limit int) ([]License, error) {
	rows, err := s.db.Query(
		`SELECT id, subject, edition, term, token, created_at
		   FROM license_record ORDER BY id DESC LIMIT ?`, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("store: 查询授权记录失败: %w", err)
	}
	defer rows.Close()

	var out []License
	for rows.Next() {
		var l License
		var created string
		if err := rows.Scan(&l.ID, &l.Subject, &l.Edition, &l.Term, &l.Token, &created); err != nil {
			return nil, err
		}
		l.CreatedAt = parseTime(created)
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- 应用事件 ----

// RecordEvent 写入一条应用事件（由 logging 的 Recorder 调用）。
//
// 刻意返回 error 而不是内部吞掉：调用方需要知道「写库失败」，但**不要**在失败时
// 再调 logging.Error——那会再次触发 Recorder，形成递归。
func (s *Store) RecordEvent(level, kind, message, detail string) error {
	_, err := s.db.Exec(
		`INSERT INTO app_event(ts, level, kind, message, detail) VALUES(?, ?, ?, ?, ?)`,
		nowText(), level, kind, message, detail)
	if err != nil {
		return fmt.Errorf("store: 写入事件失败: %w", err)
	}
	return nil
}

// RecentEvents 取最近 limit 条事件。
func (s *Store) RecentEvents(limit int) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT id, ts, level, kind, message, detail
		   FROM app_event ORDER BY id DESC LIMIT ?`, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("store: 查询事件失败: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var e Event
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.Level, &e.Kind, &e.Message, &e.Detail); err != nil {
			return nil, err
		}
		e.TS = parseTime(ts)
		out = append(out, e)
	}
	return out, rows.Err()
}

// Stats 返回各表计数（界面展示「已持久化多少数据」）。
//
// 一次查询取四个计数：以前是四条独立的 QueryRow，虽然走的是同一个连接、
// 每条都是毫秒级，但界面每 5 秒就要刷一次，合成一条更省也更一致（同一快照）。
func (s *Store) Stats() (Stats, error) {
	var st Stats
	err := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM task),
			(SELECT COUNT(*) FROM license_record),
			(SELECT COUNT(*) FROM app_event),
			(SELECT COUNT(*) FROM app_event WHERE level IN ('ERROR','CRASH'))`,
	).Scan(&st.Tasks, &st.Licenses, &st.Events, &st.Errors)
	if err != nil {
		return st, fmt.Errorf("store: 统计失败: %w", err)
	}
	return st, nil
}

// ---- 内部工具 ----

// timeLayout 统一的时间存储格式：UTC + 纳秒，字符串排序即时间排序。
const timeLayout = time.RFC3339Nano

func nowText() string { return time.Now().UTC().Format(timeLayout) }

func parseTime(s string) time.Time {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t.Local()
}

// clampLimit 限制单次查询条数，避免调用方传 0 / 负数时返回空或全表。
func clampLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 1000 {
		return 1000
	}
	return n
}
