package store

import (
	"database/sql"
	"fmt"

	"bestuav-atlas/internal/syncwire"
)

// 本文件是「业务写入口」与「变更日志」之间的胶水：
// 插入/读回/算差集。单独成文件是为了让 atlas.go 保持「一个方法一件事」的样子，
// 也避免改动量最大的同步逻辑挤进那个已经 600 行的文件。
//
// 关键约束：所有 *Patch / *Diff 函数都只做纯计算，唯一会写库的是 appendTodoDiff
// （而且必须传事务）。这样「什么算一次变更」这件事只有一个地方定义。

// insertProject 只负责落库，不产生变更日志；由调用方在事务里决定记什么。
func insertProject(e execer, p Project) (int64, error) {
	res, err := e.Exec(
		`INSERT INTO project(uid, name, "desc", status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)`,
		p.UID, p.Name, p.Desc, p.Status, timeText(p.CreatedAt), timeText(p.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("store: 写入项目失败: %w", err)
	}
	return res.LastInsertId()
}

// selectProject 在事务/连接上按 id 读项目；ok=false 表示不存在。
func selectProject(e queryRower, id int64) (Project, bool, error) {
	p, err := scanProject(e.QueryRow(`SELECT `+projectColumns+` FROM project WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, fmt.Errorf("store: 查询项目失败: %w", err)
	}
	return p, true, nil
}

// insertTodo 只负责落库；uid 必须已由调用方填好。
func insertTodo(e execer, t Todo) (int64, error) {
	res, err := e.Exec(
		`INSERT INTO todo(uid, title, note, project_id, repo_id, priority, important, urgent, status,
		                  start_at, due_at, done_at, notified_at, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.UID, t.Title, t.Note, t.ProjectID, t.RepoID, t.Priority, t.Important, t.Urgent, t.Status,
		timeText(t.StartAt), timeText(t.DueAt), timeText(t.DoneAt), timeText(t.NotifiedAt),
		timeText(t.CreatedAt), timeText(t.UpdatedAt))
	if err != nil {
		return 0, fmt.Errorf("store: 写入待办失败: %w", err)
	}
	return res.LastInsertId()
}

// selectTodo 在事务/连接上按 id 读待办；ok=false 表示不存在。
func selectTodo(e queryRower, id int64) (Todo, bool, error) {
	t, err := scanTodo(e.QueryRow(`SELECT `+todoColumns+` FROM todo WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Todo{}, false, nil
	}
	if err != nil {
		return Todo{}, false, fmt.Errorf("store: 查询待办失败: %w", err)
	}
	return t, true, nil
}

// projectCreatePatch 新建项目的整行补丁。
func projectCreatePatch(p Project) map[string]any {
	return map[string]any{
		"name":       p.Name,
		"desc":       p.Desc,
		"status":     p.Status,
		"created_at": timeText(p.CreatedAt),
		"updated_at": timeText(p.UpdatedAt),
	}
}

// todoFullPatch 新建待办的整行补丁。
//
// 刻意不含 repo_id：仓库是每台机器各自的本地登记（路径、git 状态都不同），
// 同步过去的 id 在另一台机器上指向别的仓库，比不同步更糟。
func todoFullPatch(q queryRower, t Todo) (map[string]any, error) {
	puid, err := projectUIDByID(q, t.ProjectID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"title":       t.Title,
		"note":        t.Note,
		"project_uid": puid,
		"priority":    t.Priority,
		"important":   t.Important,
		"urgent":      t.Urgent,
		"status":      t.Status,
		"start_at":    timeText(t.StartAt),
		"due_at":      timeText(t.DueAt),
		"done_at":     timeText(t.DoneAt),
		"notified_at": timeText(t.NotifiedAt),
		"created_at":  timeText(t.CreatedAt),
		"updated_at":  timeText(t.UpdatedAt),
	}, nil
}

// todoDiff 算出前后两行之间真正变了的字段，只返回这些。
//
// 这是「手机只勾完成、桌面同时改标题，两边都不丢」的全部实现：
// 补丁里没有的字段，合并方一律不碰。
func todoDiff(q queryRower, old, cur Todo) (map[string]any, error) {
	patch := map[string]any{}
	if old.Title != cur.Title {
		patch["title"] = cur.Title
	}
	if old.Note != cur.Note {
		patch["note"] = cur.Note
	}
	if old.Priority != cur.Priority {
		patch["priority"] = cur.Priority
	}
	if old.Important != cur.Important {
		patch["important"] = cur.Important
	}
	if old.Urgent != cur.Urgent {
		patch["urgent"] = cur.Urgent
	}
	if old.Status != cur.Status {
		patch["status"] = cur.Status
	}
	if timeText(old.StartAt) != timeText(cur.StartAt) {
		patch["start_at"] = timeText(cur.StartAt)
	}
	if timeText(old.DueAt) != timeText(cur.DueAt) {
		patch["due_at"] = timeText(cur.DueAt)
	}
	if timeText(old.DoneAt) != timeText(cur.DoneAt) {
		patch["done_at"] = timeText(cur.DoneAt)
	}
	if timeText(old.NotifiedAt) != timeText(cur.NotifiedAt) {
		patch["notified_at"] = timeText(cur.NotifiedAt)
	}
	// 刻意不比较 updated_at：它每次保存都会刷新，带上它就变成「原样保存也产生
	// 一条变更」，把同步队列灌满无意义的补丁，还会在另一端把时间戳改来改去。
	if old.ProjectID != cur.ProjectID {
		uid, err := projectUIDByID(q, cur.ProjectID)
		if err != nil {
			return nil, err
		}
		patch["project_uid"] = uid
	}
	return patch, nil
}

// appendTodoDiff 读回更新后的行、算差集、写变更日志；空差集不写。
//
// 为什么读完再算、而不是在调用方手拼 patch：UPDATE 里带了
// `notified_at = CASE WHEN due_at <> ? ...` 这类由 SQL 决定的副作用，
// 只有读回来才是「真正落库的值」。多一次 SELECT 换掉一类难查的偏差。
func (s *Store) appendTodoDiff(tx *sql.Tx, old Todo, id int64) error {
	cur, ok, err := selectTodo(tx, id)
	if err != nil {
		return err
	}
	if !ok || old.UID == "" {
		return nil // 行已被删掉，或这行没有跨设备身份（只可能出现在测试里）
	}
	patch, err := todoDiff(tx, old, cur)
	if err != nil {
		return err
	}
	if len(patch) == 0 {
		return nil
	}
	return appendOp(tx, syncwire.EntityTodo, old.UID, syncwire.KindPatch, patch)
}
