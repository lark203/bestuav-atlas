package store

import (
	"strings"
	"testing"
	"time"

	"bestuav-atlas/internal/syncwire"
)

// 本文件验证「用户写一次 → 产生一条恰好的变更日志」以及「远端变更合回来时
// 不覆盖本地未发送的修改」。这两件事错了不会崩，只会静默丢数据，所以必须有测试。

// TestUIDShape 校验 ULID 的形状与可排序性。
func TestUIDShape(t *testing.T) {
	a := newUID()
	if len(a) != 26 {
		t.Fatalf("ULID 应为 26 字符，实际 %d: %q", len(a), a)
	}
	for _, r := range a {
		if !strings.ContainsRune(crockford, r) {
			t.Fatalf("ULID 含非法字符 %q: %q", r, a)
		}
	}
	if b := newUID(); a == b {
		t.Fatalf("两次生成的 ULID 不应相同: %q", a)
	}

	// 时间前进 → 字典序前进（同步靠这个保证「新建的排在后面」）。
	now := testInstant
	if !(newUIDAt(now) < newUIDAt(now.Add(time.Millisecond))) {
		t.Fatal("ULID 应按时间递增")
	}
}

// TestCreateTodoAppendsCreateOp 新建待办会带一条 create 变更，且补丁是整行。
func TestCreateTodoAppendsCreateOp(t *testing.T) {
	s := openTemp(t)
	pid, err := s.CreateProject(Project{Name: "星图", Status: "进行中"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// 清掉项目那条变更，只看待办。
	drain(t, s)

	id, err := s.CreateTodo(Todo{
		Title:     "写同步协议",
		ProjectID: pid,
		Priority:  2,
		Important: true,
		Status:    "待办",
		DueAt:     testInstant,
	})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	td, _, err := s.TodoByID(id)
	if err != nil {
		t.Fatalf("TodoByID: %v", err)
	}
	if td.UID == "" {
		t.Fatal("新建待办应带 uid")
	}

	ops := mustPending(t, s)
	if len(ops) != 1 {
		t.Fatalf("应恰好 1 条变更，实际 %d", len(ops))
	}
	op := ops[0]
	if op.Entity != syncwire.EntityTodo || op.Kind != syncwire.KindCreate || op.UID != td.UID {
		t.Fatalf("变更形状不对: %+v", op)
	}
	if op.Patch["title"] != "写同步协议" {
		t.Errorf("补丁应含标题，实际 %v", op.Patch["title"])
	}
	if op.Patch["important"] != true {
		t.Errorf("补丁应含重要标记，实际 %v", op.Patch["important"])
	}
	if _, ok := op.Patch["repo_id"]; ok {
		t.Error("repo_id 只在本机有意义，不该进补丁")
	}
}

// TestTodoPatchOnlyChangedFields 改什么就只记什么——这是「两台设备各改一个
// 字段互不覆盖」的前提。
func TestTodoPatchOnlyChangedFields(t *testing.T) {
	s := openTemp(t)
	id, err := s.CreateTodo(Todo{Title: "原标题", Note: "原备注", Priority: 1, Status: "待办"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	drain(t, s)

	td, _, _ := s.TodoByID(id)
	td.Title = "新标题"
	if err := s.UpdateTodo(td); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}

	ops := mustPending(t, s)
	if len(ops) != 1 {
		t.Fatalf("应恰好 1 条变更，实际 %d", len(ops))
	}
	if _, ok := ops[0].Patch["note"]; ok {
		t.Error("没改的 note 不该出现在补丁里")
	}
	if ops[0].Patch["title"] != "新标题" {
		t.Errorf("title 应为新值，实际 %v", ops[0].Patch["title"])
	}

	// 原样保存一遍：不该产生任何变更（否则同步队列会被无意义的写入淹没）。
	drain(t, s)
	again, _, _ := s.TodoByID(id)
	if err := s.UpdateTodo(again); err != nil {
		t.Fatalf("UpdateTodo(原样): %v", err)
	}
	if ops := mustPending(t, s); len(ops) != 0 {
		t.Fatalf("原样保存不应产生变更，实际 %d 条", len(ops))
	}
}

// TestSetTodoStatusEmitsOnlyStatus 勾完成只同步状态与完成时间，不碰标题。
func TestSetTodoStatusEmitsOnlyStatus(t *testing.T) {
	s := openTemp(t)
	id, err := s.CreateTodo(Todo{Title: "勾我", Status: "待办"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	drain(t, s)

	if err := s.SetTodoStatus(id, "已完成"); err != nil {
		t.Fatalf("SetTodoStatus: %v", err)
	}
	ops := mustPending(t, s)
	if len(ops) != 1 {
		t.Fatalf("应恰好 1 条变更，实际 %d", len(ops))
	}
	p := ops[0].Patch
	if p["status"] != "已完成" || p["done_at"] == "" {
		t.Errorf("补丁应含状态与完成时间，实际 %v", p)
	}
	if _, ok := p["title"]; ok {
		t.Error("勾完成不该带标题")
	}
}

// TestDeleteTodoAppendsTombstone 删除要留下墓碑，否则另一端会「复活」这条待办。
func TestDeleteTodoAppendsTombstone(t *testing.T) {
	s := openTemp(t)
	id, err := s.CreateTodo(Todo{Title: "要删的", Status: "待办"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	td, _, _ := s.TodoByID(id)
	drain(t, s)

	if err := s.DeleteTodo(id); err != nil {
		t.Fatalf("DeleteTodo: %v", err)
	}
	ops := mustPending(t, s)
	if len(ops) != 1 || ops[0].Kind != syncwire.KindDelete || ops[0].UID != td.UID {
		t.Fatalf("应留下删除墓碑: %+v", ops)
	}
}

// TestApplyOpsKeepsLocalPendingField 合并远端变更时，本地有未发送修改的字段
// 必须保留——否则用户刚在手机上打完字，一次后台同步就把它擦了。
func TestApplyOpsKeepsLocalPendingField(t *testing.T) {
	s := openTemp(t)
	id, err := s.CreateTodo(Todo{Title: "原标题", Status: "待办"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	td, _, _ := s.TodoByID(id)
	drain(t, s)

	// 本地改了标题，还没推送。
	td.Title = "本地标题"
	if err := s.UpdateTodo(td); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}

	// 远端（手机）改了这个字段，同时勾了完成。
	applied, skipped, err := s.ApplyOps([]syncwire.Op{{
		OpID:   newUID(),
		Entity: syncwire.EntityTodo,
		UID:    td.UID,
		Kind:   syncwire.KindPatch,
		Patch:  map[string]any{"title": "手机标题", "status": "已完成"},
	}})
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	if applied != 1 {
		t.Fatalf("应应用 1 条，实际 %d（skipped=%v）", applied, skipped)
	}

	got, _, _ := s.TodoByID(id)
	if got.Title != "本地标题" {
		t.Errorf("本地未发送的标题应保留，实际 %q", got.Title)
	}
	if got.Status != "已完成" {
		t.Errorf("远端的完成状态应生效，实际 %q", got.Status)
	}
}

// TestApplyOpsDoesNotEcho 合并远端变更不能自己再生成变更（否则两端会无限来回）。
func TestApplyOpsDoesNotEcho(t *testing.T) {
	s := openTemp(t)
	pid, _ := s.CreateProject(Project{Name: "P", Status: "进行中"})
	drain(t, s)
	projectUID, _ := projectUIDByID(s.db, pid)

	if _, _, err := s.ApplyOps([]syncwire.Op{{
		OpID:   newUID(),
		Entity: syncwire.EntityTodo,
		UID:    newUID(),
		Kind:   syncwire.KindCreate,
		Patch: map[string]any{
			"title":       "远端新建",
			"project_uid": projectUID,
			"status":      "待办",
			"priority":    float64(1), // JSON 解出来的数字是 float64
		},
	}}); err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	if ops := mustPending(t, s); len(ops) != 0 {
		t.Fatalf("合并远端变更不应产生本地变更，实际 %d 条", len(ops))
	}
}

// TestApplyOwnOpsSkipped 服务端把自己的变更回放回来时应跳过，不能把本地状态再改一遍。
func TestApplyOwnOpsSkipped(t *testing.T) {
	s := openTemp(t)
	if _, err := s.CreateTodo(Todo{Title: "本地新建", Status: "待办"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	ops := mustPending(t, s)
	if len(ops) != 1 {
		t.Fatalf("应有 1 条待推送变更，实际 %d", len(ops))
	}

	// 模拟服务端把同一条 op 原样发回（带上了 seq）。
	echo := ops[0]
	echo.Seq = 42
	applied, skipped, err := s.ApplyOps([]syncwire.Op{echo})
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	if applied != 0 || len(skipped) != 0 {
		t.Fatalf("自己的变更应静默跳过，实际 applied=%d skipped=%v", applied, skipped)
	}
	if ops := mustPending(t, s); len(ops) != 1 {
		t.Fatalf("待推送变更不该被改动，实际 %d 条", len(ops))
	}
}

// TestApplyOpsDeleteBlockedByPendingEdit 本地还有未发送的修改时，远端删除不生效。
func TestApplyOpsDeleteBlockedByPendingEdit(t *testing.T) {
	s := openTemp(t)
	id, err := s.CreateTodo(Todo{Title: "别删我", Status: "待办"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	td, _, _ := s.TodoByID(id)
	drain(t, s)

	td.Note = "刚写的备注"
	if err := s.UpdateTodo(td); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}

	applied, skipped, err := s.ApplyOps([]syncwire.Op{{
		OpID: newUID(), Entity: syncwire.EntityTodo, UID: td.UID, Kind: syncwire.KindDelete,
	}})
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	if applied != 0 || len(skipped) != 1 {
		t.Fatalf("应跳过删除，实际 applied=%d skipped=%v", applied, skipped)
	}
	if _, ok, _ := s.TodoByID(id); !ok {
		t.Fatal("本地有未发送修改时，待办不该被删掉")
	}

	// 本地变更推完（清空 outbox）后，墓碑仍然有效：再合一次就删掉。
	drain(t, s)
	if _, _, err := s.ApplyOps([]syncwire.Op{{
		OpID: newUID(), Entity: syncwire.EntityTodo, UID: td.UID, Kind: syncwire.KindDelete,
	}}); err != nil {
		t.Fatalf("ApplyOps(第二次): %v", err)
	}
	if _, ok, _ := s.TodoByID(id); ok {
		t.Fatal("没有本地修改后，远端删除应生效")
	}
}

// TestApplyDeleteProjectClearsChildren 删除项目的墓碑合回来后，子待办要变成未归类。
func TestApplyDeleteProjectClearsChildren(t *testing.T) {
	s := openTemp(t)
	pid, err := s.CreateProject(Project{Name: "要删的项目", Status: "进行中"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	p, _, _ := s.ProjectByID(pid)
	id, err := s.CreateTodo(Todo{Title: "挂在项目下", ProjectID: pid, Status: "待办"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	td, _, _ := s.TodoByID(id)
	drain(t, s)

	// 先按桌面删除的口径产生变更（项目删除 + 子项解绑），再把它们当远端变更合并回来。
	if err := s.DeleteProject(pid); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	own := mustPending(t, s)
	drain(t, s)
	if _, _, err := s.ApplyOps(own); err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	if _, ok, _ := s.ProjectByID(pid); ok {
		t.Fatal("项目应已被删除")
	}
	got, ok, err := s.TodoByUID(td.UID)
	if err != nil || !ok {
		t.Fatalf("待办应仍存在: ok=%v err=%v", ok, err)
	}
	if got.ProjectID != 0 {
		t.Fatalf("子待办应变成未归类，实际 project_id=%d（原项目 %s）", got.ProjectID, p.UID)
	}
}

// TestMarkOpsSentAdvancesSeqOnlyUp 水位只增不减，重放的旧响应不能把它拉回去。
func TestMarkOpsSentAdvancesSeqOnlyUp(t *testing.T) {
	s := openTemp(t)
	if _, err := s.CreateTodo(Todo{Title: "任务", Status: "待办"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	ops := mustPending(t, s)
	ids := []string{ops[0].OpID}

	if err := s.MarkOpsSent(ids, 100); err != nil {
		t.Fatalf("MarkOpsSent: %v", err)
	}
	if seq, _ := s.SyncState("last_seq"); seq != "100" {
		t.Fatalf("水位应为 100，实际 %q", seq)
	}
	if left := mustPending(t, s); len(left) != 0 {
		t.Fatalf("推送后不应有待推送变更，实际 %d 条", len(left))
	}

	if err := s.MarkOpsSent(nil, 30); err != nil {
		t.Fatalf("MarkOpsSent(旧水位): %v", err)
	}
	if seq, _ := s.SyncState("last_seq"); seq != "100" {
		t.Fatalf("旧水位不应覆盖新水位，实际 %q", seq)
	}
}

// TestBackfillUIDsForLegacyRows 手工写入的「无 uid」行在重开库后应被补上。
func TestBackfillUIDsForLegacyRows(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/data.db"
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// 模拟老数据：先建行，再把 uid 抹掉（等价于从 v10 升级上来）。
	if _, err := s.CreateTodo(Todo{Title: "老待办", Status: "待办"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE todo SET uid = ''`); err != nil {
		t.Fatalf("抹掉 uid: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	defer func() { _ = s2.Close() }()
	todos, err := s2.ListTodos()
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	if len(todos) != 1 {
		t.Fatalf("应有 1 条待办，实际 %d", len(todos))
	}
	if todos[0].UID == "" {
		t.Fatal("重开库后老数据应被补上 uid")
	}
}

// ---- 测试辅助 ----

// drain 把当前待推送变更标记为已发送，用于「先同步干净，再看这次操作产生什么」。
func drain(t *testing.T, s *Store) {
	t.Helper()
	ops := mustPending(t, s)
	if len(ops) == 0 {
		return
	}
	ids := make([]string, 0, len(ops))
	for _, op := range ops {
		ids = append(ids, op.OpID)
	}
	if err := s.MarkOpsSent(ids, 0); err != nil {
		t.Fatalf("MarkOpsSent: %v", err)
	}
}

func mustPending(t *testing.T, s *Store) []syncwire.Op {
	t.Helper()
	ops, err := s.PendingOps()
	if err != nil {
		t.Fatalf("PendingOps: %v", err)
	}
	return ops
}
