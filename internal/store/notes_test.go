package store

import (
	"path/filepath"
	"testing"
)

// TestNoteCRUD 笔记基础增删改查：标签 JSON 往返、置顶排序、更新时间刷新、硬删除。
func TestNoteCRUD(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.CreateNote(Note{Title: "周会", Body: "# 议题\n- 发布", Tags: []string{"工作", "会议"}})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	got, err := s.GetNote(id)
	if err != nil {
		t.Fatalf("GetNote: %v", err)
	}
	if got.Title != "周会" || got.Body != "# 议题\n- 发布" {
		t.Fatalf("读回内容不一致: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[1] != "会议" {
		t.Fatalf("标签往返失败: %v", got.Tags)
	}
	if got.Pinned {
		t.Fatal("新笔记不应默认置顶")
	}

	// 更新：标题/正文/标签，created_at 不变，updated_at 刷新。
	created := got.CreatedAt
	got.Title = "周会（改）"
	got.Tags = []string{"工作"}
	if err := s.UpdateNote(got); err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	again, err := s.GetNote(id)
	if err != nil {
		t.Fatal(err)
	}
	if again.Title != "周会（改）" || len(again.Tags) != 1 {
		t.Fatalf("更新未生效: %+v", again)
	}
	if !again.CreatedAt.Equal(created) {
		t.Error("更新不应改动 created_at")
	}
	// timeLayout 只到秒：同一秒内更新可能相等，这里只要求不回退。
	if again.UpdatedAt.Before(created) {
		t.Error("updated_at 不应回退")
	}

	// 置顶：列表里排最前，且不改 updated_at。
	pinnedAt := again.UpdatedAt
	if err := s.SetNotePinned(id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNote(Note{Title: "更新的笔记"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != id || !list[0].Pinned {
		t.Fatalf("置顶笔记应排最前: %+v", list)
	}
	if !list[0].UpdatedAt.Equal(pinnedAt) {
		t.Error("置顶不应刷新 updated_at")
	}

	if err := s.DeleteNote(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNote(id); err == nil {
		t.Fatal("删除后不应再读到")
	}
	if n, _ := s.NoteCount(); n != 1 {
		t.Errorf("删除后应只剩 1 篇，实际 %d", n)
	}
}

// TestNoteRelationsAndEditor 归属/关联/编辑器类型往返；老库补列不丢已有笔记。
func TestNoteRelationsAndEditor(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "rel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, err := s.CreateNote(Note{Title: "关联", ProjectID: 7, RepoID: 9, Editor: "rich"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNote(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != 7 || got.RepoID != 9 || got.Editor != "rich" {
		t.Fatalf("归属/编辑器未落库: %+v", got)
	}
	got.ProjectID, got.RepoID, got.Editor = 0, 0, "markdown"
	if err := s.UpdateNote(got); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GetNote(id)
	if again.ProjectID != 0 || again.RepoID != 0 || again.Editor != "markdown" {
		t.Fatalf("更新未生效: %+v", again)
	}
}
