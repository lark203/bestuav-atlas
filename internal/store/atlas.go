package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// 本文件是「项目 / 仓库 / 待办」三张业务表的持久化层。
//
// 关系：项目只是归类容器，仓库（代码库）与待办通过 project_id 挂靠它，
// project_id = 0 表示未归类。删项目不级联删业务数据——仓库和待办是真实的
// 工作项，项目没了它们仍需要被看到（见 DeleteProject）。
//
// 时间列沿用 store.go 的约定：RFC3339Nano 的 UTC 字符串。零值（无期限、
// 从未检查过）写空串，读出来是零值 Time；空串在 ORDER BY 里也天然排在
// 有值之后，「无期限」因此不需要额外的排序标记。

// Project 一个项目（业务上的归类容器）。
type Project struct {
	ID        int64
	Name      string
	Desc      string
	Status    string // 进行中 / 暂停 / 归档
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repo 一个被纳管的代码仓库及其最近一次检查结果。
type Repo struct {
	ID           int64
	ProjectID    int64
	Name         string // 展示名，通常 owner/repo
	URL          string
	LocalPath    string
	Branch       string // 期望分支，空=跟随当前
	Note         string
	Status       string // 正常/待提交/待推送/需要更新/未克隆/错误/未知
	StatusMsg    string
	Dirty        int
	Ahead        int
	Behind       int
	LastCommit   string
	LastCommitAt time.Time
	CheckedAt    time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Todo 一条待办。
type Todo struct {
	ID        int64
	Title     string
	Note      string
	ProjectID int64
	RepoID    int64
	Priority  int       // 0 低 / 1 中 / 2 高
	Status    string    // 待办 / 进行中 / 已完成
	DueAt     time.Time // 零值 = 无期限
	DoneAt    time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// 三张表的列清单。查询与 scan 共用同一份顺序，改表时只改这里，避免
// "SELECT 里加了列、Scan 忘了加" 这类错位（错位通常不会报错，只会读出脏值）。
//
// project 的 "desc" 带双引号：DESC 是 SQLite 关键字。
const (
	projectColumns = `id, name, "desc", status, created_at, updated_at`
	repoColumns    = `id, project_id, name, url, local_path, branch, note, status,
		status_msg, dirty, ahead, behind, last_commit, last_commit_at,
		checked_at, created_at, updated_at`
	todoColumns = `id, title, note, project_id, repo_id, priority, status,
		due_at, done_at, created_at, updated_at`
)

// rowScanner 让 QueryRow 与 Rows 共用同一套扫描逻辑。
type rowScanner interface {
	Scan(dest ...any) error
}

// timeText 把时间编码成列值：零值写空串，其余写成 UTC 的 RFC3339Nano。
//
// 为什么零值要特殊处理：time.Time 的零值格式化出来是个非法但非空的字符串，
// 读回来只能靠猜；空串既能明确表达「没有」，也让 due_at 的空值排序自然成立。
// 它与 store.go 的 parseTime 对称（parseTime("") 返回零值）。
func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func scanProject(r rowScanner) (Project, error) {
	var p Project
	var created, updated string
	if err := r.Scan(&p.ID, &p.Name, &p.Desc, &p.Status, &created, &updated); err != nil {
		return Project{}, err
	}
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

func scanRepo(r rowScanner) (Repo, error) {
	var x Repo
	var lastCommitAt, checkedAt, created, updated string
	if err := r.Scan(&x.ID, &x.ProjectID, &x.Name, &x.URL, &x.LocalPath, &x.Branch,
		&x.Note, &x.Status, &x.StatusMsg, &x.Dirty, &x.Ahead, &x.Behind,
		&x.LastCommit, &lastCommitAt, &checkedAt, &created, &updated); err != nil {
		return Repo{}, err
	}
	x.LastCommitAt = parseTime(lastCommitAt)
	x.CheckedAt = parseTime(checkedAt)
	x.CreatedAt = parseTime(created)
	x.UpdatedAt = parseTime(updated)
	return x, nil
}

func scanTodo(r rowScanner) (Todo, error) {
	var t Todo
	var due, done, created, updated string
	if err := r.Scan(&t.ID, &t.Title, &t.Note, &t.ProjectID, &t.RepoID,
		&t.Priority, &t.Status, &due, &done, &created, &updated); err != nil {
		return Todo{}, err
	}
	t.DueAt = parseTime(due)
	t.DoneAt = parseTime(done)
	t.CreatedAt = parseTime(created)
	t.UpdatedAt = parseTime(updated)
	return t, nil
}

// ---- 项目 ----

// ListProjects 按创建顺序返回全部项目（界面顶部的项目切换器用）。
func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectColumns + ` FROM project ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询项目失败: %w", err)
	}
	defer rows.Close()

	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 读取项目失败: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProjectByID 按 id 取项目；ok=false 表示不存在。
func (s *Store) ProjectByID(id int64) (Project, bool, error) {
	p, err := scanProject(s.db.QueryRow(
		`SELECT `+projectColumns+` FROM project WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, fmt.Errorf("store: 查询项目失败: %w", err)
	}
	return p, true, nil
}

// CreateProject 新建项目，返回自增 id。
//
// 入参的 ID 与时间一律忽略：id 由库分配，created_at/updated_at 取服务端时间，
// 避免界面传进来的零值或客户端时钟把时间列写坏。
func (s *Store) CreateProject(p Project) (int64, error) {
	if strings.TrimSpace(p.Name) == "" {
		return 0, fmt.Errorf("store: 项目名称不能为空")
	}
	now := nowText()
	res, err := s.db.Exec(
		`INSERT INTO project(name, "desc", status, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?)`,
		p.Name, p.Desc, p.Status, now, now)
	if err != nil {
		return 0, fmt.Errorf("store: 写入项目失败: %w", err)
	}
	return res.LastInsertId()
}

// UpdateProject 覆盖项目的可编辑字段；created_at 保持原值，updated_at 刷新为当前时间。
func (s *Store) UpdateProject(p Project) error {
	if p.ID <= 0 {
		return fmt.Errorf("store: 更新项目缺少有效 id")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("store: 项目名称不能为空")
	}
	_, err := s.db.Exec(
		`UPDATE project SET name = ?, "desc" = ?, status = ?, updated_at = ?
		  WHERE id = ?`,
		p.Name, p.Desc, p.Status, nowText(), p.ID)
	if err != nil {
		return fmt.Errorf("store: 更新项目失败: %w", err)
	}
	return nil
}

// DeleteProject 删除项目，并把挂在它下面的仓库/待办改为「未归类」。
//
// 用事务保证三件事要么全成、要么全不成：否则中途失败会留下 project_id 指向
// 已删除项目的孤儿记录（界面按项目筛选时会莫名少数据）。
func (s *Store) DeleteProject(id int64) error {
	if id <= 0 {
		return fmt.Errorf("store: 删除项目缺少有效 id")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: 开始事务失败: %w", err)
	}
	// Commit 之后 Rollback 是空操作，这样写省掉每个错误分支的重复回滚。
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`UPDATE repo SET project_id = 0 WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("store: 解除仓库与项目的关联失败: %w", err)
	}
	if _, err := tx.Exec(`UPDATE todo SET project_id = 0 WHERE project_id = ?`, id); err != nil {
		return fmt.Errorf("store: 解除待办与项目的关联失败: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM project WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: 删除项目失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: 提交删除项目事务失败: %w", err)
	}
	return nil
}

// ---- 仓库 ----

// ListRepos 返回全部仓库，按展示名（忽略大小写）排序，同名再按 id。
func (s *Store) ListRepos() ([]Repo, error) {
	return s.queryRepos(`SELECT ` + repoColumns + `
		FROM repo ORDER BY name COLLATE NOCASE, id`)
}

// ReposByProject 返回挂在某个项目下的仓库；projectID=0 即「未归类」。
func (s *Store) ReposByProject(projectID int64) ([]Repo, error) {
	return s.queryRepos(`SELECT `+repoColumns+`
		FROM repo WHERE project_id = ? ORDER BY name COLLATE NOCASE, id`, projectID)
}

func (s *Store) queryRepos(query string, args ...any) ([]Repo, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: 查询仓库失败: %w", err)
	}
	defer rows.Close()

	var out []Repo
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 读取仓库失败: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepoByID 按 id 取仓库；ok=false 表示不存在。
func (s *Store) RepoByID(id int64) (Repo, bool, error) {
	r, err := scanRepo(s.db.QueryRow(
		`SELECT `+repoColumns+` FROM repo WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Repo{}, false, nil
	}
	if err != nil {
		return Repo{}, false, fmt.Errorf("store: 查询仓库失败: %w", err)
	}
	return r, true, nil
}

// CreateRepo 新建仓库，返回自增 id；created_at/updated_at 用服务端时间。
//
// LastCommitAt / CheckedAt 是业务数据（上次检查结果），按入参落库，可以是零值
// （表示还没检查过）。
func (s *Store) CreateRepo(r Repo) (int64, error) {
	if strings.TrimSpace(r.Name) == "" {
		return 0, fmt.Errorf("store: 仓库名称不能为空")
	}
	now := nowText()
	res, err := s.db.Exec(
		`INSERT INTO repo(project_id, name, url, local_path, branch, note, status,
		                  status_msg, dirty, ahead, behind, last_commit, last_commit_at,
		                  checked_at, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Name, r.URL, r.LocalPath, r.Branch, r.Note, r.Status, r.StatusMsg,
		r.Dirty, r.Ahead, r.Behind, r.LastCommit, timeText(r.LastCommitAt),
		timeText(r.CheckedAt), now, now)
	if err != nil {
		return 0, fmt.Errorf("store: 写入仓库失败: %w", err)
	}
	return res.LastInsertId()
}

// UpdateRepo 覆盖仓库的可编辑字段（含最近检查结果）；created_at 不动，
// updated_at 刷新为当前时间。
//
// 只改检查结果的场景应走 SaveRepoStatus：它会保留 name/url 等人工维护的字段。
func (s *Store) UpdateRepo(r Repo) error {
	if r.ID <= 0 {
		return fmt.Errorf("store: 更新仓库缺少有效 id")
	}
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("store: 仓库名称不能为空")
	}
	_, err := s.db.Exec(
		`UPDATE repo SET project_id = ?, name = ?, url = ?, local_path = ?, branch = ?,
		        note = ?, status = ?, status_msg = ?, dirty = ?, ahead = ?, behind = ?,
		        last_commit = ?, last_commit_at = ?, checked_at = ?, updated_at = ?
		  WHERE id = ?`,
		r.ProjectID, r.Name, r.URL, r.LocalPath, r.Branch, r.Note, r.Status, r.StatusMsg,
		r.Dirty, r.Ahead, r.Behind, r.LastCommit, timeText(r.LastCommitAt),
		timeText(r.CheckedAt), nowText(), r.ID)
	if err != nil {
		return fmt.Errorf("store: 更新仓库失败: %w", err)
	}
	return nil
}

// DeleteRepo 删除仓库；不存在不算错。
func (s *Store) DeleteRepo(id int64) error {
	if id <= 0 {
		return fmt.Errorf("store: 删除仓库缺少有效 id")
	}
	if _, err := s.db.Exec(`DELETE FROM repo WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: 删除仓库失败: %w", err)
	}
	return nil
}

// SaveRepoStatus 只写检查结果列，不碰 name/url/local_path 等人工维护的字段。
//
// 为什么单独开一个方法：git 检查是定时/按需批量跑的，它拿到的只是状态快照；
// 若走 UpdateRepo 全量覆盖，一个过期的快照会把用户刚改过的展示名或备注冲掉。
func (s *Store) SaveRepoStatus(id int64, st Repo) error {
	if id <= 0 {
		return fmt.Errorf("store: 保存仓库状态缺少有效 id")
	}
	_, err := s.db.Exec(
		`UPDATE repo SET status = ?, status_msg = ?, dirty = ?, ahead = ?, behind = ?,
		        last_commit = ?, last_commit_at = ?, checked_at = ?, updated_at = ?
		  WHERE id = ?`,
		st.Status, st.StatusMsg, st.Dirty, st.Ahead, st.Behind,
		st.LastCommit, timeText(st.LastCommitAt), timeText(st.CheckedAt), nowText(), id)
	if err != nil {
		return fmt.Errorf("store: 保存仓库状态失败: %w", err)
	}
	return nil
}

// ---- 待办 ----

// ListTodos 返回全部待办，按「先看该做的」排序：
//
//  1. 未完成的在前（已完成沉底，但不清掉，方便回看）；
//  2. 有期限的在前，无期限的排最后（空串在文本排序里最小，这里显式标记避免被误排）；
//  3. 期限早的在前；
//  4. 同一天里优先级高的在前；
//  5. 同优先级里新建的在前（id DESC，符合「刚记下来的先看到」）。
func (s *Store) ListTodos() ([]Todo, error) {
	rows, err := s.db.Query(`SELECT ` + todoColumns + `
		  FROM todo
		 ORDER BY CASE WHEN status = '已完成' THEN 1 ELSE 0 END,
		          CASE WHEN due_at = '' THEN 1 ELSE 0 END,
		          due_at,
		          priority DESC,
		          id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: 查询待办失败: %w", err)
	}
	defer rows.Close()

	var out []Todo
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 读取待办失败: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TodoByID 按 id 取待办；ok=false 表示不存在。
func (s *Store) TodoByID(id int64) (Todo, bool, error) {
	t, err := scanTodo(s.db.QueryRow(
		`SELECT `+todoColumns+` FROM todo WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Todo{}, false, nil
	}
	if err != nil {
		return Todo{}, false, fmt.Errorf("store: 查询待办失败: %w", err)
	}
	return t, true, nil
}

// CreateTodo 新建待办，返回自增 id；时间戳用服务端时间。
//
// DueAt / DoneAt 的零值写空串，分别表示「无期限」「未完成」。
func (s *Store) CreateTodo(t Todo) (int64, error) {
	if strings.TrimSpace(t.Title) == "" {
		return 0, fmt.Errorf("store: 待办标题不能为空")
	}
	now := nowText()
	res, err := s.db.Exec(
		`INSERT INTO todo(title, note, project_id, repo_id, priority, status,
		                  due_at, done_at, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.Title, t.Note, t.ProjectID, t.RepoID, t.Priority, t.Status,
		timeText(t.DueAt), timeText(t.DoneAt), now, now)
	if err != nil {
		return 0, fmt.Errorf("store: 写入待办失败: %w", err)
	}
	return res.LastInsertId()
}

// UpdateTodo 全量覆盖待办字段；created_at 保持原值，updated_at 刷新。
//
// 状态切换请走 SetTodoStatus：它会同步 done_at，而这里只按入参落库
// （编辑正文时调用方通常原样传回读到的 DoneAt）。
func (s *Store) UpdateTodo(t Todo) error {
	if t.ID <= 0 {
		return fmt.Errorf("store: 更新待办缺少有效 id")
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("store: 待办标题不能为空")
	}
	_, err := s.db.Exec(
		`UPDATE todo SET title = ?, note = ?, project_id = ?, repo_id = ?, priority = ?,
		        status = ?, due_at = ?, done_at = ?, updated_at = ?
		  WHERE id = ?`,
		t.Title, t.Note, t.ProjectID, t.RepoID, t.Priority, t.Status,
		timeText(t.DueAt), timeText(t.DoneAt), nowText(), t.ID)
	if err != nil {
		return fmt.Errorf("store: 更新待办失败: %w", err)
	}
	return nil
}

// SetTodoStatus 只改状态，并让 done_at 与状态保持一致。
//
// 完成时记下完成时间，退回未完成时清空：否则列表里会出现「未完成但有完成时间」
// 的矛盾数据，统计和排序也会各自得出不同结论。
func (s *Store) SetTodoStatus(id int64, status string) error {
	if id <= 0 {
		return fmt.Errorf("store: 更新待办状态缺少有效 id")
	}
	if strings.TrimSpace(status) == "" {
		return fmt.Errorf("store: 待办状态不能为空")
	}
	now := nowText()
	doneAt := ""
	if status == "已完成" {
		doneAt = now
	}
	if _, err := s.db.Exec(
		`UPDATE todo SET status = ?, done_at = ?, updated_at = ? WHERE id = ?`,
		status, doneAt, now, id); err != nil {
		return fmt.Errorf("store: 更新待办状态失败: %w", err)
	}
	return nil
}

// DeleteTodo 删除待办；不存在不算错。
func (s *Store) DeleteTodo(id int64) error {
	if id <= 0 {
		return fmt.Errorf("store: 删除待办缺少有效 id")
	}
	if _, err := s.db.Exec(`DELETE FROM todo WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: 删除待办失败: %w", err)
	}
	return nil
}

// ---- 概览 ----

// AtlasStats 项目/仓库/待办的总览计数。
type AtlasStats struct {
	Projects    int64
	Repos       int64
	ReposDirty  int64 // status='待提交'
	ReposAhead  int64 // status='待推送'
	ReposBehind int64 // status='需要更新'
	TodosOpen   int64 // status != '已完成'
	TodosDone   int64
}

// AtlasStats 一次查询返回全部计数。
//
// 和 Stats 一样合成一条 SELECT：子查询各自走索引，且七个数字来自同一快照，
// 不会出现「项目 3 个、仓库数比项目数还新」的错位。
func (s *Store) AtlasStats() (AtlasStats, error) {
	var st AtlasStats
	err := s.db.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM project),
			(SELECT COUNT(*) FROM repo),
			(SELECT COUNT(*) FROM repo WHERE status = '待提交'),
			(SELECT COUNT(*) FROM repo WHERE status = '待推送'),
			(SELECT COUNT(*) FROM repo WHERE status = '需要更新'),
			(SELECT COUNT(*) FROM todo WHERE status <> '已完成'),
			(SELECT COUNT(*) FROM todo WHERE status = '已完成')`,
	).Scan(&st.Projects, &st.Repos, &st.ReposDirty, &st.ReposAhead,
		&st.ReposBehind, &st.TodosOpen, &st.TodosDone)
	if err != nil {
		return st, fmt.Errorf("store: 统计项目/仓库/待办失败: %w", err)
	}
	return st, nil
}
