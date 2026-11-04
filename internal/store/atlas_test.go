package store

import (
	"testing"
	"time"
)

// testInstant 固定时间点（带纳秒），用来验证时间往返不丢精度。
// 不用 time.Now：测试要对同一个值比较，也不该依赖执行时刻。
var testInstant = time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)

// TestTimeTextRoundTrip 时间编码约定：零值 ↔ 空串，有值 ↔ UTC RFC3339Nano。
func TestTimeTextRoundTrip(t *testing.T) {
	if got := timeText(time.Time{}); got != "" {
		t.Errorf("零值时间应存空串，实际 %q", got)
	}
	if got := parseTime(""); !got.IsZero() {
		t.Errorf("空串应读出零值时间，实际 %v", got)
	}
	if got := parseTime(timeText(testInstant)); !got.Equal(testInstant) {
		t.Errorf("时间往返丢精度: want=%v got=%v", testInstant, got)
	}
}

// TestProjectCRUD 项目的基础增删改查：入参 ID/时间被忽略、列表按 id、
// 修改保留 created_at 并刷新 updated_at、空名称被拒绝。
func TestProjectCRUD(t *testing.T) {
	s := openTemp(t)

	// 故意传进来的 ID 与时间都应被忽略。
	id, err := s.CreateProject(Project{
		ID:        777,
		Name:      "无人机平台",
		Desc:      "整机研发项目管理",
		Status:    "进行中",
		CreatedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if id == 777 || id <= 0 {
		t.Fatalf("应使用库分配的自增 id，实际 %d", id)
	}

	got, ok, err := s.ProjectByID(id)
	if err != nil || !ok {
		t.Fatalf("ProjectByID: ok=%v err=%v", ok, err)
	}
	if got.Name != "无人机平台" || got.Desc != "整机研发项目管理" || got.Status != "进行中" {
		t.Errorf("字段丢失: %+v", got)
	}
	// created_at 应是服务端时间，而不是入参的 2000 年。
	if got.CreatedAt.IsZero() || time.Since(got.CreatedAt) > time.Minute {
		t.Errorf("created_at 未按服务端时间写入: %v", got.CreatedAt)
	}
	if !got.UpdatedAt.Equal(got.CreatedAt) {
		t.Errorf("新建时 updated_at 应与 created_at 一致: %v vs %v", got.UpdatedAt, got.CreatedAt)
	}

	if _, ok, err := s.ProjectByID(999999); err != nil || ok {
		t.Errorf("不存在的 id 应 ok=false 且不报错，实际 ok=%v err=%v", ok, err)
	}

	// 第二个项目用来验证 ORDER BY id。
	secondID, err := s.CreateProject(Project{Name: "地面站", Status: "暂停"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	list, err := s.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 || list[0].ID != id || list[1].ID != secondID {
		t.Fatalf("列表顺序应为 id 升序: %+v", list)
	}

	// 修改：created_at 不动，updated_at 刷新。
	before := got.UpdatedAt
	time.Sleep(10 * time.Millisecond)
	got.Desc = "整机 + 地面站"
	got.Status = "暂停"
	if err := s.UpdateProject(got); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	after, _, err := s.ProjectByID(id)
	if err != nil {
		t.Fatalf("ProjectByID: %v", err)
	}
	if after.Desc != "整机 + 地面站" || after.Status != "暂停" {
		t.Errorf("修改未生效: %+v", after)
	}
	if !after.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("更新不应改 created_at: before=%v after=%v", got.CreatedAt, after.CreatedAt)
	}
	if !after.UpdatedAt.After(before) {
		t.Errorf("更新应刷新 updated_at: before=%v after=%v", before, after.UpdatedAt)
	}

	// 校验：名称不能为空（纯空白也算空），更新时 id 必须有效。
	if _, err := s.CreateProject(Project{Name: "   "}); err == nil {
		t.Error("空名称应被拒绝")
	}
	if err := s.UpdateProject(Project{ID: 0, Name: "无 id"}); err == nil {
		t.Error("id<=0 的更新应被拒绝")
	}
	if err := s.UpdateProject(Project{ID: id, Name: ""}); err == nil {
		t.Error("空名称的更新应被拒绝")
	}
}

// TestDeleteProjectDetachesChildren 删项目只解除关联，不删除仓库/待办；
// 其他项目下的数据也不能被误伤。
func TestDeleteProjectDetachesChildren(t *testing.T) {
	s := openTemp(t)

	p1, err := s.CreateProject(Project{Name: "项目一"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := s.CreateProject(Project{Name: "项目二"})
	if err != nil {
		t.Fatal(err)
	}
	repo1, err := s.CreateRepo(Repo{ProjectID: p1, Name: "a/one"})
	if err != nil {
		t.Fatal(err)
	}
	todo1, err := s.CreateTodo(Todo{ProjectID: p1, Title: "项目一的待办", Status: "待办"})
	if err != nil {
		t.Fatal(err)
	}
	repo2, err := s.CreateRepo(Repo{ProjectID: p2, Name: "b/two"})
	if err != nil {
		t.Fatal(err)
	}
	todo2, err := s.CreateTodo(Todo{ProjectID: p2, Title: "项目二的待办", Status: "待办"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteProject(p1); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, ok, err := s.ProjectByID(p1); err != nil || ok {
		t.Fatalf("项目应已删除: ok=%v err=%v", ok, err)
	}

	r1, ok, err := s.RepoByID(repo1)
	if err != nil || !ok {
		t.Fatalf("仓库不应被级联删除: ok=%v err=%v", ok, err)
	}
	if r1.ProjectID != 0 {
		t.Errorf("仓库应变为未归类(project_id=0)，实际 %d", r1.ProjectID)
	}
	t1, ok, err := s.TodoByID(todo1)
	if err != nil || !ok {
		t.Fatalf("待办不应被级联删除: ok=%v err=%v", ok, err)
	}
	if t1.ProjectID != 0 {
		t.Errorf("待办应变为未归类(project_id=0)，实际 %d", t1.ProjectID)
	}

	// 项目二的数据原样保留。
	if r2, _, _ := s.RepoByID(repo2); r2.ProjectID != p2 {
		t.Errorf("其他项目的仓库被误改: project_id=%d", r2.ProjectID)
	}
	if t2, _, _ := s.TodoByID(todo2); t2.ProjectID != p2 {
		t.Errorf("其他项目的待办被误改: project_id=%d", t2.ProjectID)
	}

	// 重复删除不报错（幂等）。
	if err := s.DeleteProject(p1); err != nil {
		t.Errorf("重复删除已不存在的项目不应报错: %v", err)
	}
	if err := s.DeleteProject(0); err == nil {
		t.Error("id=0 的删除应被拒绝（否则会误清所有未归类数据）")
	}
}

// TestRepoCRUD 仓库的增删改查：时间往返、NOCASE 排序、按项目过滤。
func TestRepoCRUD(t *testing.T) {
	s := openTemp(t)

	pID, err := s.CreateProject(Project{Name: "无人机平台"})
	if err != nil {
		t.Fatal(err)
	}

	checked := testInstant.Add(30 * time.Minute)
	id, err := s.CreateRepo(Repo{
		ProjectID:    pID,
		Name:         "bestuav/atlas",
		URL:          "https://example.com/bestuav/atlas.git",
		LocalPath:    `D:\code\atlas`,
		Branch:       "main",
		Note:         "主仓库",
		Status:       "正常",
		StatusMsg:    "工作区干净",
		Dirty:        0,
		Ahead:        2,
		Behind:       1,
		LastCommit:   "abc1234 修复登录",
		LastCommitAt: testInstant,
		CheckedAt:    checked,
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	got, ok, err := s.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("RepoByID: ok=%v err=%v", ok, err)
	}
	if got.ProjectID != pID || got.Name != "bestuav/atlas" || got.URL != "https://example.com/bestuav/atlas.git" ||
		got.LocalPath != `D:\code\atlas` || got.Branch != "main" || got.Note != "主仓库" ||
		got.Status != "正常" || got.StatusMsg != "工作区干净" || got.Dirty != 0 ||
		got.Ahead != 2 || got.Behind != 1 || got.LastCommit != "abc1234 修复登录" {
		t.Errorf("字段丢失: %+v", got)
	}
	// 带纳秒的时间要原样读回。
	if !got.LastCommitAt.Equal(testInstant) {
		t.Errorf("last_commit_at 往返丢精度: want=%v got=%v", testInstant, got.LastCommitAt)
	}
	if !got.CheckedAt.Equal(checked) {
		t.Errorf("checked_at 往返丢精度: want=%v got=%v", checked, got.CheckedAt)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("created_at/updated_at 不应为零值: %+v", got)
	}

	// NOCASE 排序：Alpha 在 bestuav 之前、zeta 在最后。
	for _, name := range []string{"zeta/repo", "Alpha/repo"} {
		if _, err := s.CreateRepo(Repo{ProjectID: pID, Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateRepo(Repo{Name: "unfiled/repo"}); err != nil { // project_id=0
		t.Fatal(err)
	}
	list, err := s.ListRepos()
	if err != nil {
		t.Fatalf("ListRepos: %v", err)
	}
	wantOrder := []string{"Alpha/repo", "bestuav/atlas", "unfiled/repo", "zeta/repo"}
	if len(list) != len(wantOrder) {
		t.Fatalf("应有 %d 个仓库，实际 %d", len(wantOrder), len(list))
	}
	for i, want := range wantOrder {
		if list[i].Name != want {
			t.Fatalf("排序错误（期望 %v）: %+v", wantOrder, list)
		}
	}
	if byProject, err := s.ReposByProject(pID); err != nil || len(byProject) != 3 {
		t.Errorf("按项目过滤应得 3 个，实际 %d err=%v", len(byProject), err)
	}
	if unfiled, err := s.ReposByProject(0); err != nil || len(unfiled) != 1 || unfiled[0].Name != "unfiled/repo" {
		t.Errorf("未归类仓库过滤错误: %+v err=%v", unfiled, err)
	}

	// 更新：名称/关联/检查结果都能改，created_at 保持。
	got.Name = "bestuav/atlas2"
	got.ProjectID = 0
	got.Dirty = 1
	got.LastCommitAt = testInstant.Add(2 * time.Hour)
	if err := s.UpdateRepo(got); err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	after, _, err := s.RepoByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != "bestuav/atlas2" || after.ProjectID != 0 || after.Dirty != 1 {
		t.Errorf("更新未生效: %+v", after)
	}
	if !after.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("更新不应改 created_at: before=%v after=%v", got.CreatedAt, after.CreatedAt)
	}

	// 校验。
	if _, err := s.CreateRepo(Repo{Name: " "}); err == nil {
		t.Error("空名称应被拒绝")
	}
	if err := s.UpdateRepo(Repo{ID: 0, Name: "无 id"}); err == nil {
		t.Error("id<=0 的更新应被拒绝")
	}
	if err := s.UpdateRepo(Repo{ID: id, Name: ""}); err == nil {
		t.Error("空名称的更新应被拒绝")
	}

	// 删除；重复删除/不存在的 id 不报错。
	if err := s.DeleteRepo(id); err != nil {
		t.Fatalf("DeleteRepo: %v", err)
	}
	if _, ok, _ := s.RepoByID(id); ok {
		t.Error("删除后不应再读到")
	}
	if err := s.DeleteRepo(id); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}
	if _, ok, err := s.RepoByID(999999); err != nil || ok {
		t.Errorf("不存在的 id 应 ok=false: ok=%v err=%v", ok, err)
	}
}

// TestSaveRepoStatusOnlyTouchesStatus 定时检查只写状态列，人工维护的字段不能被冲掉。
func TestSaveRepoStatusOnlyTouchesStatus(t *testing.T) {
	s := openTemp(t)

	id, err := s.CreateRepo(Repo{
		ProjectID: 5,
		Name:      "bestuav/atlas",
		URL:       "https://example.com/bestuav/atlas.git",
		LocalPath: `D:\code\atlas`,
		Branch:    "main",
		Note:      "别动我",
		Status:    "未知",
	})
	if err != nil {
		t.Fatal(err)
	}

	before, ok, err := s.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("RepoByID: ok=%v err=%v", ok, err)
	}

	checked := testInstant.Add(90 * time.Minute)
	// 故意在状态快照里塞入错误的人工字段，验证它们不会被写库。
	if err := s.SaveRepoStatus(id, Repo{
		ID:           id,
		ProjectID:    99,
		Name:         "被覆盖的名称",
		URL:          "被覆盖的 URL",
		LocalPath:    "被覆盖的路径",
		Branch:       "被覆盖的分支",
		Note:         "被覆盖的备注",
		Status:       "待推送",
		StatusMsg:    "领先 3 个提交",
		Dirty:        1,
		Ahead:        3,
		Behind:       2,
		LastCommit:   "def5678 新提交",
		LastCommitAt: checked,
		CheckedAt:    checked,
	}); err != nil {
		t.Fatalf("SaveRepoStatus: %v", err)
	}

	got, ok, err := s.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("RepoByID: ok=%v err=%v", ok, err)
	}
	// 状态列已更新。
	if got.Status != "待推送" || got.StatusMsg != "领先 3 个提交" || got.Dirty != 1 ||
		got.Ahead != 3 || got.Behind != 2 || got.LastCommit != "def5678 新提交" {
		t.Errorf("状态列未更新: %+v", got)
	}
	if !got.LastCommitAt.Equal(checked) || !got.CheckedAt.Equal(checked) {
		t.Errorf("状态时间未更新: last_commit_at=%v checked_at=%v", got.LastCommitAt, got.CheckedAt)
	}
	if time.Since(got.UpdatedAt) > time.Minute {
		t.Errorf("updated_at 应刷新为当前时间: %v", got.UpdatedAt)
	}
	// 人工维护的字段原样保留。
	if got.ProjectID != 5 || got.Name != "bestuav/atlas" || got.URL != "https://example.com/bestuav/atlas.git" ||
		got.LocalPath != `D:\code\atlas` || got.Branch != "main" || got.Note != "别动我" {
		t.Errorf("人工维护的字段被覆盖: %+v", got)
	}
	if !got.CreatedAt.Equal(before.CreatedAt) || got.CreatedAt.IsZero() {
		t.Errorf("created_at 不应被改动: before=%v after=%v", before.CreatedAt, got.CreatedAt)
	}

	// 检查结果里的零值时间（没检查过）也要写成空串、读回零值。
	if err := s.SaveRepoStatus(id, Repo{Status: "未知"}); err != nil {
		t.Fatalf("SaveRepoStatus: %v", err)
	}
	got, _, _ = s.RepoByID(id)
	if !got.LastCommitAt.IsZero() || !got.CheckedAt.IsZero() {
		t.Errorf("零值时间应读回零值: last_commit_at=%v checked_at=%v", got.LastCommitAt, got.CheckedAt)
	}
	if err := s.SaveRepoStatus(0, Repo{}); err == nil {
		t.Error("id<=0 应被拒绝")
	}
}

// TestTodoCRUDAndOrdering 待办的增删改查与列表排序：
// 未完成在前 → 有期限在前 → 期限升序 → 优先级降序 → id 降序。
func TestTodoCRUDAndOrdering(t *testing.T) {
	s := openTemp(t)

	pID, err := s.CreateProject(Project{Name: "无人机平台"})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)

	firstID, err := s.CreateTodo(Todo{
		Title:     "无期限-中优先级",
		Note:      "没有 due 的待办",
		ProjectID: pID,
		Priority:  1,
		Status:    "待办",
	})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	// 为了构造出确定的排序，这里按已知顺序插入。
	mustCreate := func(td Todo) int64 {
		t.Helper()
		gotID, err := s.CreateTodo(td)
		if err != nil {
			t.Fatalf("CreateTodo(%q): %v", td.Title, err)
		}
		return gotID
	}
	_ = mustCreate(Todo{Title: "甲日-低优先级", DueAt: due, Priority: 0, Status: "待办"})
	_ = mustCreate(Todo{Title: "甲日-高优先级", DueAt: due, Priority: 2, Status: "待办"})
	_ = mustCreate(Todo{Title: "已完成-最早到期", DueAt: due.AddDate(0, -1, 0), Priority: 2, Status: "已完成"})
	_ = mustCreate(Todo{Title: "已完成-无期限", Priority: 2, Status: "已完成"})
	lastID := mustCreate(Todo{Title: "甲日-高优先级-后建", DueAt: due, Priority: 2, Status: "待办"})

	// 单条读回：零值时间 ↔ 空串。
	got, ok, err := s.TodoByID(firstID)
	if err != nil || !ok {
		t.Fatalf("TodoByID: ok=%v err=%v", ok, err)
	}
	if !got.DueAt.IsZero() || !got.DoneAt.IsZero() {
		t.Errorf("无期限/未完成的待办应读出零值时间: due=%v done=%v", got.DueAt, got.DoneAt)
	}
	if got.ProjectID != pID || got.Priority != 1 || got.Note != "没有 due 的待办" || got.CreatedAt.IsZero() {
		t.Errorf("字段丢失: %+v", got)
	}
	if _, ok, err := s.TodoByID(999999); err != nil || ok {
		t.Errorf("不存在的 id 应 ok=false: ok=%v err=%v", ok, err)
	}

	list, err := s.ListTodos()
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	wantOrder := []string{
		"甲日-高优先级-后建", // 未完成，同日、同优先级，id 更大者在前
		"甲日-高优先级",
		"甲日-低优先级",
		"无期限-中优先级", // due 为空，排在有期限的之后
		"已完成-最早到期", // 已完成的整体沉底
		"已完成-无期限",
	}
	if len(list) != len(wantOrder) {
		t.Fatalf("应有 %d 条待办，实际 %d", len(wantOrder), len(list))
	}
	for i, want := range wantOrder {
		if list[i].Title != want {
			t.Fatalf("排序错误（期望 %v）: 实际 %v", wantOrder, todoTitles(list))
		}
	}

	// 更新：全量字段可改；created_at 不动，updated_at 刷新。
	edit, _, _ := s.TodoByID(lastID)
	beforeUpdate := edit.UpdatedAt
	edit.Title = "甲日-高优先级-已改"
	edit.Note = "补一条说明"
	edit.Priority = 0
	edit.Status = "进行中"
	edit.DueAt = testInstant
	time.Sleep(10 * time.Millisecond)
	if err := s.UpdateTodo(edit); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	after, _, err := s.TodoByID(lastID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "甲日-高优先级-已改" || after.Note != "补一条说明" ||
		after.Priority != 0 || after.Status != "进行中" {
		t.Errorf("更新未生效: %+v", after)
	}
	if !after.DueAt.Equal(testInstant) {
		t.Errorf("due_at 往返丢精度: want=%v got=%v", testInstant, after.DueAt)
	}
	if !after.CreatedAt.Equal(edit.CreatedAt) {
		t.Errorf("更新不应改 created_at: before=%v after=%v", edit.CreatedAt, after.CreatedAt)
	}
	if !after.UpdatedAt.After(beforeUpdate) {
		t.Errorf("更新应刷新 updated_at: before=%v after=%v", beforeUpdate, after.UpdatedAt)
	}

	// 校验。
	if _, err := s.CreateTodo(Todo{Title: "  "}); err == nil {
		t.Error("空标题应被拒绝")
	}
	if err := s.UpdateTodo(Todo{ID: 0, Title: "无 id"}); err == nil {
		t.Error("id<=0 的更新应被拒绝")
	}
	if err := s.UpdateTodo(Todo{ID: firstID, Title: ""}); err == nil {
		t.Error("空标题的更新应被拒绝")
	}

	// 删除；重复删除不报错。
	if err := s.DeleteTodo(firstID); err != nil {
		t.Fatalf("DeleteTodo: %v", err)
	}
	if _, ok, _ := s.TodoByID(firstID); ok {
		t.Error("删除后不应再读到")
	}
	if err := s.DeleteTodo(firstID); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}
	if err := s.DeleteTodo(0); err == nil {
		t.Error("id=0 的删除应被拒绝")
	}
}

// TestSetTodoStatus 完成时写 done_at，退回未完成时清空；其他字段不动。
func TestSetTodoStatus(t *testing.T) {
	s := openTemp(t)

	id, err := s.CreateTodo(Todo{Title: "上线前检查", Status: "待办", Priority: 2})
	if err != nil {
		t.Fatal(err)
	}
	created, _, _ := s.TodoByID(id)

	// 待办 → 进行中：done_at 仍为空。
	if err := s.SetTodoStatus(id, "进行中"); err != nil {
		t.Fatalf("SetTodoStatus: %v", err)
	}
	got, _, _ := s.TodoByID(id)
	if got.Status != "进行中" {
		t.Errorf("状态未更新: %+v", got)
	}
	if !got.DoneAt.IsZero() {
		t.Errorf("未完成不应有 done_at: %v", got.DoneAt)
	}

	// 进行中 → 已完成：done_at 写入当前时间。
	if err := s.SetTodoStatus(id, "已完成"); err != nil {
		t.Fatalf("SetTodoStatus: %v", err)
	}
	got, _, _ = s.TodoByID(id)
	if got.Status != "已完成" {
		t.Errorf("状态未更新: %+v", got)
	}
	if got.DoneAt.IsZero() || time.Since(got.DoneAt) > time.Minute {
		t.Errorf("done_at 应为当前时间: %v", got.DoneAt)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("改状态不应改 created_at: before=%v after=%v", created.CreatedAt, got.CreatedAt)
	}
	if !got.UpdatedAt.After(created.UpdatedAt) && !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Errorf("updated_at 倒退: before=%v after=%v", created.UpdatedAt, got.UpdatedAt)
	}

	// 已完成 → 待办：done_at 清空。
	if err := s.SetTodoStatus(id, "待办"); err != nil {
		t.Fatalf("SetTodoStatus: %v", err)
	}
	got, _, _ = s.TodoByID(id)
	if got.Status != "待办" {
		t.Errorf("状态未回退: %+v", got)
	}
	if !got.DoneAt.IsZero() {
		t.Errorf("退回未完成应清空 done_at: %v", got.DoneAt)
	}
	// 内容字段一概不动。
	if got.Title != "上线前检查" || got.Priority != 2 {
		t.Errorf("改状态不应影响其他字段: %+v", got)
	}

	if err := s.SetTodoStatus(0, "已完成"); err == nil {
		t.Error("id<=0 应被拒绝")
	}
	if err := s.SetTodoStatus(id, "  "); err == nil {
		t.Error("空状态应被拒绝")
	}
}

// TestAtlasStats 概览计数：项目/仓库总数、三类异常仓库、未完成与已完成待办。
func TestAtlasStats(t *testing.T) {
	s := openTemp(t)

	empty, err := s.AtlasStats()
	if err != nil {
		t.Fatalf("AtlasStats: %v", err)
	}
	if empty != (AtlasStats{}) {
		t.Errorf("空库计数应全为 0，实际 %+v", empty)
	}

	if _, err := s.CreateProject(Project{Name: "项目一"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(Project{Name: "项目二"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"待提交", "待推送", "需要更新", "正常", "未知"} {
		if _, err := s.CreateRepo(Repo{Name: "repo-" + status, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	for _, td := range []struct {
		title  string
		status string
	}{
		{"待办一", "待办"},
		{"进行中一", "进行中"},
		{"已完成一", "已完成"},
		{"已完成二", "已完成"},
	} {
		if _, err := s.CreateTodo(Todo{Title: td.title, Status: td.status}); err != nil {
			t.Fatal(err)
		}
	}

	st, err := s.AtlasStats()
	if err != nil {
		t.Fatalf("AtlasStats: %v", err)
	}
	want := AtlasStats{
		Projects:    2,
		Repos:       5,
		ReposDirty:  1,
		ReposAhead:  1,
		ReposBehind: 1,
		TodosOpen:   2,
		TodosDone:   2,
	}
	if st != want {
		t.Errorf("统计错误:\n got=%+v\nwant=%+v", st, want)
	}
}

// todoTitles 把待办标题拼出来，排序失败时打印完整实际顺序。
func todoTitles(list []Todo) []string {
	out := make([]string, 0, len(list))
	for _, t := range list {
		out = append(out, t.Title)
	}
	return out
}
