// Package syncsvc 是 atlas-sync 服务端的实现：单用户、自建、只做「收变更 / 发变更」。
//
// 它刻意**不**做业务校验（不检查待办状态是否合法、不检查项目是否存在）：
// 合并规则在两端各自的 store 里已经实现了，服务端再实现一遍就会有两套语义，
// 一旦不一致就是最难查的那种 bug。服务端只保证三件事：
//
//  1. **持久与可重放**：op 表只追加，任何设备都能从 0 重放；
//  2. **幂等**：同一 (device_id, op_id) 只入账一次，手机断网重发不会翻倍；
//  3. **定序**：seq 自增，客户端按它排序应用——这是不做 HLC 的前提。
//
// 存储用 SQLite（modernc，纯 Go），与桌面同款：单文件、可 `VACUUM INTO` 备份、
// 交叉编译出 Linux 二进制不用带依赖。
package syncsvc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"bestuav-atlas/internal/syncwire"
)

// 服务端自己的库结构。与客户端库无关：客户端存 todo/project，服务端只存 op 与合并后的 head。
const schema = `
CREATE TABLE IF NOT EXISTS device (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL,
  last_seen  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS pair_code (
  code       TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at    TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS op (
  seq       INTEGER PRIMARY KEY AUTOINCREMENT,
  op_id     TEXT NOT NULL,
  device_id TEXT NOT NULL,
  entity    TEXT NOT NULL,
  uid       TEXT NOT NULL,
  kind      TEXT NOT NULL,
  payload   TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  UNIQUE(device_id, op_id)
);
CREATE INDEX IF NOT EXISTS idx_op_entity_uid ON op(entity, uid);
CREATE TABLE IF NOT EXISTS head (
  entity  TEXT NOT NULL,
  uid     TEXT NOT NULL,
  row     TEXT NOT NULL DEFAULT '{}',
  seq     INTEGER NOT NULL,
  deleted INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(entity, uid)
);
CREATE TABLE IF NOT EXISTS schema_meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
`

// SchemaVersion 服务端库结构版本（/v1/health 会报，客户端可据此拒绝不兼容）。
const SchemaVersion = "1"

// 限制：单次推送与单条补丁的上限。手机端一次同步最多几百条，给足余量但不允许无限大。
const (
	maxOpsPerPush  = 500
	maxPatchBytes  = 64 << 10 // 64KB
	maxBodyBytes   = 4 << 20  // 4MB
	maxPullLimit   = 1000
	defaultPairTTL = 5 * time.Minute
	codeAlphabet   = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // 去掉 0/O/1/I/L，避免手抄歧义
)

// Server 一个 atlas-sync 实例。
type Server struct {
	db         *sql.DB
	adminToken string
	now        func() time.Time
	// Version 由 main 注入（-ldflags），/v1/health 会报出去，便于两端对版本。
	Version string

	mu       sync.Mutex
	attempts map[string][]time.Time // 按 IP 的配对尝试时间戳（防暴力猜码）
}

// Open 打开（必要时创建）服务端数据库。
//
// adminToken 为空的实例没有管理接口权限：生成配对码、登记设备都会 401。
// 这是刻意的——公网上的实例宁可什么都干不了，也不要默认无密码。
func Open(path, adminToken string) (*Server, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("syncsvc: 未指定数据库路径")
	}
	db, err := sql.Open("sqlite", "file:"+path+
		"?_pragma=busy_timeout(5000)"+
		"&_pragma=journal_mode(WAL)"+
		"&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, fmt.Errorf("syncsvc: 打开数据库失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("syncsvc: 初始化表结构失败: %w", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_meta(key, value) VALUES('schema_version', ?)
		 ON CONFLICT(key) DO NOTHING`, SchemaVersion); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("syncsvc: 写入库版本失败: %w", err)
	}
	return &Server{
		db:         db,
		adminToken: adminToken,
		now:        time.Now,
		Version:    "dev",
		attempts:   map[string][]time.Time{},
	}, nil
}

// Close 关闭数据库。
func (s *Server) Close() error { return s.db.Close() }

// Handler 返回带路由的 http.Handler。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	mux.HandleFunc("POST /v1/pair", s.handlePair)
	mux.HandleFunc("POST /v1/changes", s.handlePush)
	mux.HandleFunc("GET /v1/changes", s.handlePull)
	mux.HandleFunc("GET /v1/snapshot", s.handleSnapshot)
	mux.HandleFunc("POST /v1/admin/devices", s.handleAdminDevice)
	mux.HandleFunc("GET /v1/admin/devices", s.handleAdminDevices)
	mux.HandleFunc("POST /v1/admin/pair-codes", s.handleAdminPairCode)
	return s.withCommonHeaders(mux)
}

// withCommonHeaders 统一补齐响应头：不缓存、严格 Content-Type、禁止被 iframe 内嵌。
func (s *Server) withCommonHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// ---- 健康检查 ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var devices, ops, seq int64
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM device`).Scan(&devices)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM op`).Scan(&ops)
	_ = s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM op`).Scan(&seq)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     s.Version,
		"schema":      SchemaVersion,
		"protocol":    syncwire.ProtocolVersion,
		"devices":     devices,
		"ops":         ops,
		"seq":         seq,
		"server_time": s.now().UTC().Format(time.RFC3339Nano),
	})
}

// ---- 配对 ----

// handlePair 用一次性配对码换设备令牌。
//
// 手机输入的是「服务器地址 + 8 位码」；码 5 分钟过期、只能用一次、按 IP 限流，
// 三者合起来把「猜码」的成本抬到不值得尝试。
func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if !s.allowPairAttempt(clientIP(r)) {
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	var req syncwire.PairRequest
	if !decodeBody(w, r, &req) {
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	name := strings.TrimSpace(req.DeviceName)
	if code == "" || name == "" {
		writeErr(w, http.StatusBadRequest, "配对码与设备名都不能为空")
		return
	}

	now := s.now()
	var expiresAt, usedAt string
	err := s.db.QueryRow(
		`SELECT expires_at, used_at FROM pair_code WHERE code = ?`, code).Scan(&expiresAt, &usedAt)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusUnauthorized, "配对码无效")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取配对码失败")
		return
	}
	if usedAt != "" {
		writeErr(w, http.StatusUnauthorized, "配对码已被使用")
		return
	}
	exp, perr := time.Parse(time.RFC3339Nano, expiresAt)
	if perr != nil || now.After(exp) {
		writeErr(w, http.StatusUnauthorized, "配对码已过期")
		return
	}

	device, err := s.createDevice(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.db.Exec(`UPDATE pair_code SET used_at = ? WHERE code = ?`,
		now.UTC().Format(time.RFC3339Nano), code); err != nil {
		writeErr(w, http.StatusInternalServerError, "作废配对码失败")
		return
	}
	writeJSON(w, http.StatusOK, syncwire.PairResponse{DeviceID: device.id, Token: device.token})
}

// allowPairAttempt 滑动窗口限流：同一 IP 每分钟最多 10 次配对请求。
func (s *Server) allowPairAttempt(ip string) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	kept := s.attempts[ip][:0]
	for _, t := range s.attempts[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= 10 {
		s.attempts[ip] = kept
		return false
	}
	s.attempts[ip] = append(kept, now)
	return true
}

// ---- 推送 / 拉取 ----

func (s *Server) handlePush(w http.ResponseWriter, r *http.Request) {
	deviceID, ok := s.authDevice(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "设备令牌无效")
		return
	}
	var req syncwire.PushRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Ops) > maxOpsPerPush {
		writeErr(w, http.StatusRequestEntityTooLarge, "单次推送的变更过多")
		return
	}

	tx, err := s.db.Begin()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "开始事务失败")
		return
	}
	defer func() { _ = tx.Rollback() }()

	accepted := 0
	var maxSeq int64
	for _, op := range req.Ops {
		if err := validateOp(op); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		payload, err := json.Marshal(op.Patch)
		if err != nil || len(payload) > maxPatchBytes {
			writeErr(w, http.StatusBadRequest, "变更内容过大或无法编码")
			return
		}
		createdAt := op.CreatedAt
		if createdAt == "" {
			createdAt = s.now().UTC().Format(time.RFC3339Nano)
		}
		res, err := tx.Exec(
			`INSERT OR IGNORE INTO op(op_id, device_id, entity, uid, kind, payload, created_at)
			 VALUES(?, ?, ?, ?, ?, ?, ?)`,
			op.OpID, deviceID, op.Entity, op.UID, op.Kind, string(payload), createdAt)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "写入变更失败")
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // 重复投递：已经入账过，幂等跳过
		}
		seq, err := res.LastInsertId()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取变更序号失败")
			return
		}
		if err := mergeHead(tx, op, seq); err != nil {
			writeErr(w, http.StatusInternalServerError, "合并变更失败")
			return
		}
		accepted++
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	if _, err := tx.Exec(`UPDATE device SET last_seen = ? WHERE id = ?`,
		s.now().UTC().Format(time.RFC3339Nano), deviceID); err != nil {
		writeErr(w, http.StatusInternalServerError, "更新设备状态失败")
		return
	}
	if err := tx.Commit(); err != nil {
		writeErr(w, http.StatusInternalServerError, "提交变更失败")
		return
	}

	// 全部是重复投递时也要回一个水位：否则客户端会一直以为自己没同步上去。
	seq, err := s.maxSeq()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取服务端水位失败")
		return
	}
	if maxSeq > seq {
		seq = maxSeq
	}
	writeJSON(w, http.StatusOK, syncwire.PushResponse{Accepted: accepted, Seq: seq})
}

func (s *Server) handlePull(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authDevice(r); !ok {
		writeErr(w, http.StatusUnauthorized, "设备令牌无效")
		return
	}
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > maxPullLimit {
		limit = 500
	}

	rows, err := s.db.Query(
		`SELECT seq, op_id, device_id, entity, uid, kind, payload, created_at
		   FROM op WHERE seq > ? ORDER BY seq LIMIT ?`, since, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询变更失败")
		return
	}
	defer rows.Close()

	// Ops 初始化成空切片：空批次要序列化成 []，不是 null——Dart 侧少一个判空分支。
	resp := syncwire.PullResponse{
		Ops:        []syncwire.Op{},
		Next:       since,
		ServerTime: s.now().UTC().Format(time.RFC3339Nano),
	}
	for rows.Next() {
		var (
			op      syncwire.Op
			payload string
		)
		if err := rows.Scan(&op.Seq, &op.OpID, &op.DeviceID, &op.Entity, &op.UID,
			&op.Kind, &payload, &op.CreatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "读取变更失败")
			return
		}
		if payload != "" && payload != "{}" {
			_ = json.Unmarshal([]byte(payload), &op.Patch)
		}
		resp.Ops = append(resp.Ops, op)
		resp.Next = op.Seq
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "读取变更失败")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleSnapshot 返回合并后的当前状态，供新设备首次配对时建库。
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.authDevice(r); !ok {
		writeErr(w, http.StatusUnauthorized, "设备令牌无效")
		return
	}
	rows, err := s.db.Query(`SELECT entity, row FROM head WHERE deleted = 0 ORDER BY entity, uid`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询快照失败")
		return
	}
	defer rows.Close()

	resp := syncwire.SnapshotResponse{
		Todos:    []syncwire.Todo{},
		Projects: []syncwire.Project{},
	}
	for rows.Next() {
		var entity, row string
		if err := rows.Scan(&entity, &row); err != nil {
			writeErr(w, http.StatusInternalServerError, "读取快照失败")
			return
		}
		switch entity {
		case syncwire.EntityTodo:
			var td syncwire.Todo
			if err := json.Unmarshal([]byte(row), &td); err == nil {
				resp.Todos = append(resp.Todos, td)
			}
		case syncwire.EntityProject:
			var p syncwire.Project
			if err := json.Unmarshal([]byte(row), &p); err == nil {
				resp.Projects = append(resp.Projects, p)
			}
		}
	}
	if err := rows.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "读取快照失败")
		return
	}
	if resp.Seq, err = s.maxSeq(); err != nil {
		writeErr(w, http.StatusInternalServerError, "读取水位失败")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// mergeHead 把一条变更合并进 head（快照用的当前状态）。
//
// 合并规则与客户端的一致：补丁里没有的键不碰；delete 只打墓碑，保留 row 以便恢复。
func mergeHead(tx *sql.Tx, op syncwire.Op, seq int64) error {
	if op.Kind == syncwire.KindDelete {
		_, err := tx.Exec(
			`INSERT INTO head(entity, uid, row, seq, deleted) VALUES(?, ?, '{}', ?, 1)
			 ON CONFLICT(entity, uid) DO UPDATE SET seq = excluded.seq, deleted = 1`,
			op.Entity, op.UID, seq)
		return err
	}

	merged := map[string]any{}
	var row string
	err := tx.QueryRow(`SELECT row FROM head WHERE entity = ? AND uid = ?`, op.Entity, op.UID).Scan(&row)
	switch {
	case err == nil:
		if err := json.Unmarshal([]byte(row), &merged); err != nil {
			merged = map[string]any{} // 坏行不该让整次推送失败：以补丁重建
		}
	case errors.Is(err, sql.ErrNoRows):
		// 首次出现：merged 保持空
	default:
		return err
	}
	for k, v := range op.Patch {
		merged[k] = v
	}
	// uid 不在补丁里（它是 Op 的字段），但快照必须带上：客户端就是按它认身份的。
	merged["uid"] = op.UID
	b, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO head(entity, uid, row, seq, deleted) VALUES(?, ?, ?, ?, 0)
		 ON CONFLICT(entity, uid) DO UPDATE SET row = excluded.row, seq = excluded.seq, deleted = 0`,
		op.Entity, op.UID, string(b), seq)
	return err
}

// ---- 管理接口 ----

type deviceRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen"`
}

func (s *Server) handleAdminDevices(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "管理员令牌无效")
		return
	}
	rows, err := s.db.Query(`SELECT id, name, created_at, last_seen FROM device ORDER BY created_at`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "查询设备失败")
		return
	}
	defer rows.Close()
	items := []deviceRow{}
	for rows.Next() {
		var d deviceRow
		if err := rows.Scan(&d.ID, &d.Name, &d.CreatedAt, &d.LastSeen); err != nil {
			writeErr(w, http.StatusInternalServerError, "读取设备失败")
			return
		}
		items = append(items, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleAdminDevice 直接登记一台设备并返回令牌（桌面端引导用）。
func (s *Server) handleAdminDevice(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "管理员令牌无效")
		return
	}
	var req syncwire.PairRequest
	if !decodeBody(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.DeviceName)
	if name == "" {
		name = "未命名设备"
	}
	device, err := s.createDevice(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, syncwire.PairResponse{DeviceID: device.id, Token: device.token})
}

// handleAdminPairCode 生成一次性配对码（桌面「添加手机」用）。
func (s *Server) handleAdminPairCode(w http.ResponseWriter, r *http.Request) {
	if !s.isAdmin(r) {
		writeErr(w, http.StatusUnauthorized, "管理员令牌无效")
		return
	}
	code, err := randomCode(8)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "生成配对码失败")
		return
	}
	now := s.now()
	expires := now.Add(defaultPairTTL)
	if _, err := s.db.Exec(
		`INSERT INTO pair_code(code, created_at, expires_at) VALUES(?, ?, ?)`,
		code, now.UTC().Format(time.RFC3339Nano), expires.UTC().Format(time.RFC3339Nano)); err != nil {
		writeErr(w, http.StatusInternalServerError, "保存配对码失败")
		return
	}
	// 顺手清掉过期未用的码，避免表无限增长。
	_, _ = s.db.Exec(`DELETE FROM pair_code WHERE expires_at < ?`,
		now.UTC().Format(time.RFC3339Nano))

	writeJSON(w, http.StatusOK, map[string]any{
		"code":       code,
		"expires_at": expires.UTC().Format(time.RFC3339Nano),
		"expires_in": int(defaultPairTTL.Seconds()),
	})
}

// ---- 身份与校验 ----

type newDevice struct {
	id    string
	token string
}

// createDevice 生成设备 id 与令牌；库里只存令牌的 SHA-256。
func (s *Server) createDevice(name string) (newDevice, error) {
	id, err := randomHex(8)
	if err != nil {
		return newDevice{}, fmt.Errorf("syncsvc: 生成设备 id 失败: %w", err)
	}
	token, err := randomToken()
	if err != nil {
		return newDevice{}, fmt.Errorf("syncsvc: 生成设备令牌失败: %w", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO device(id, name, token_hash, created_at) VALUES(?, ?, ?, ?)`,
		"dev-"+id, name, hashToken(token), s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return newDevice{}, fmt.Errorf("syncsvc: 登记设备失败: %w", err)
	}
	return newDevice{id: "dev-" + id, token: token}, nil
}

// authDevice 校验 Authorization: Bearer <token>，返回设备 id。
func (s *Server) authDevice(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return "", false
	}
	var id string
	if err := s.db.QueryRow(`SELECT id FROM device WHERE token_hash = ?`,
		hashToken(strings.TrimSpace(token))).Scan(&id); err != nil {
		return "", false
	}
	return id, true
}

// isAdmin 校验 X-Admin-Token（常量时间比较，避免按字节比较泄露前缀）。
func (s *Server) isAdmin(r *http.Request) bool {
	if s.adminToken == "" {
		return false // 没配管理员令牌 = 管理接口关闭
	}
	got := r.Header.Get("X-Admin-Token")
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.adminToken)) == 1
}

func (s *Server) maxSeq() (int64, error) {
	var seq int64
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM op`).Scan(&seq)
	return seq, err
}

// validateOp 只做形状校验：字段齐全、种类合法、uid 不像注入。
func validateOp(op syncwire.Op) error {
	if strings.TrimSpace(op.OpID) == "" {
		return errors.New("变更缺少 op_id")
	}
	if strings.TrimSpace(op.UID) == "" {
		return errors.New("变更缺少 uid")
	}
	if op.Entity != syncwire.EntityTodo && op.Entity != syncwire.EntityProject {
		return fmt.Errorf("未知实体 %q", op.Entity)
	}
	switch op.Kind {
	case syncwire.KindCreate, syncwire.KindPatch, syncwire.KindDelete:
	default:
		return fmt.Errorf("未知变更种类 %q", op.Kind)
	}
	return nil
}

// ---- 小工具 ----

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// randomCode 生成 n 位配对码（去掉易混字符的大写字母表）。
func randomCode(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, c := range b {
		out[i] = codeAlphabet[int(c)%len(codeAlphabet)]
	}
	return string(out), nil
}

// clientIP 取远端 IP（去掉端口）。不信任 X-Forwarded-For：反代后面才需要它，
// 而公网直连时信任这个头等于让人随便伪造限流维度。
func clientIP(r *http.Request) string {
	host, _, found := strings.Cut(r.RemoteAddr, ":")
	if !found {
		return r.RemoteAddr
	}
	return host
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}
