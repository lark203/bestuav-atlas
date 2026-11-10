package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Note 一篇笔记。Body 是 Markdown 原文（图片以 notes-media/<文件名> 相对路径引用）。
type Note struct {
	ID        int64
	Title     string
	Body      string
	Tags      []string
	Pinned    bool
	ProjectID int64
	RepoID    int64
	Editor    string // "markdown"（默认）或 "rich"
	CreatedAt time.Time
	UpdatedAt time.Time
}

// noteColumns 所有查询共用的列顺序（scanNote 与之对应）。
const noteColumns = `id, title, body, tags, pinned, project_id, repo_id, editor, created_at, updated_at`

// scanNote 读一行；tags 存 JSON 数组，坏数据当作空标签，不让一条脏记录炸掉列表。
func scanNote(sc interface{ Scan(...any) error }) (Note, error) {
	var n Note
	var tags, created, updated string
	var pinned int
	if err := sc.Scan(&n.ID, &n.Title, &n.Body, &tags, &pinned,
		&n.ProjectID, &n.RepoID, &n.Editor, &created, &updated); err != nil {
		return n, err
	}
	if n.Editor == "" {
		n.Editor = "markdown"
	}
	if tags != "" {
		_ = json.Unmarshal([]byte(tags), &n.Tags)
	}
	n.Pinned = pinned != 0
	n.CreatedAt = parseTime(created)
	n.UpdatedAt = parseTime(updated)
	return n, nil
}

// ListNotes 全部笔记：置顶优先，其余按更新时间倒序。
func (s *Store) ListNotes() ([]Note, error) {
	rows, err := s.db.Query(`SELECT ` + noteColumns + ` FROM note
		ORDER BY pinned DESC, created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: 读取笔记失败: %w", err)
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, fmt.Errorf("store: 读取笔记失败: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNote 按 id 取一篇（不存在时返回 sql.ErrNoRows）。
func (s *Store) GetNote(id int64) (Note, error) {
	return scanNote(s.db.QueryRow(`SELECT `+noteColumns+` FROM note WHERE id = ?`, id))
}

// CreateNote 新建笔记；标题可以为空（如落地后在编辑器里再写）。
func (s *Store) CreateNote(n Note) (int64, error) {
	now := nowText()
	tags, err := json.Marshal(nonNilTags(n.Tags))
	if err != nil {
		return 0, fmt.Errorf("store: 序列化笔记标签失败: %w", err)
	}
	editor := noteEditorKind(n.Editor)
	res, err := s.db.Exec(`INSERT INTO note(title, body, tags, pinned, project_id, repo_id, editor, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.Title, n.Body, string(tags), boolInt(n.Pinned), n.ProjectID, n.RepoID, editor, now, now)
	if err != nil {
		return 0, fmt.Errorf("store: 写入笔记失败: %w", err)
	}
	return res.LastInsertId()
}

// UpdateNote 更新标题/正文/标签，并刷新 updated_at；created_at 保持不变。
func (s *Store) UpdateNote(n Note) error {
	if n.ID <= 0 {
		return fmt.Errorf("store: 更新笔记缺少有效 id")
	}
	tags, err := json.Marshal(nonNilTags(n.Tags))
	if err != nil {
		return fmt.Errorf("store: 序列化笔记标签失败: %w", err)
	}
	_, err = s.db.Exec(`UPDATE note SET title = ?, body = ?, tags = ?, project_id = ?, repo_id = ?,
		editor = ?, updated_at = ? WHERE id = ?`,
		n.Title, n.Body, string(tags), n.ProjectID, n.RepoID, noteEditorKind(n.Editor), nowText(), n.ID)
	if err != nil {
		return fmt.Errorf("store: 更新笔记失败: %w", err)
	}
	return nil
}

// SetNotePinned 只改置顶位（列表里的星标），不动 updated_at：
// 置顶是排序动作，不该把一篇旧笔记的「更新时间」也刷新成现在。
func (s *Store) SetNotePinned(id int64, pinned bool) error {
	if id <= 0 {
		return fmt.Errorf("store: 置顶笔记缺少有效 id")
	}
	_, err := s.db.Exec(`UPDATE note SET pinned = ? WHERE id = ?`, boolInt(pinned), id)
	if err != nil {
		return fmt.Errorf("store: 更新笔记置顶失败: %w", err)
	}
	return nil
}

// DeleteNote 永久删除一篇笔记（调用方负责二次确认）。
func (s *Store) DeleteNote(id int64) error {
	if id <= 0 {
		return fmt.Errorf("store: 删除笔记缺少有效 id")
	}
	if _, err := s.db.Exec(`DELETE FROM note WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: 删除笔记失败: %w", err)
	}
	return nil
}

// NoteCount 笔记总数（页头副标题/空态用）。
func (s *Store) NoteCount() (int64, error) {
	var n int64
	err := s.db.QueryRow(`SELECT COUNT(*) FROM note`).Scan(&n)
	return n, err
}

func nonNilTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// noteEditorKind 编辑器类型只认已知值，其余按 Markdown 处理。
func noteEditorKind(s string) string {
	if s == "rich" {
		return "rich"
	}
	return "markdown"
}
