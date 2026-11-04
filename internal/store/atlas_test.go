package store

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // 老库升级用例需要手工建库，直接用同一个驱动
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

	// 到期提醒状态：MarkTodoNotified 只写 notified_at，不动 updated_at；
	// 改截止时间会清空它，改别的字段则保留。
	if err := s.MarkTodoNotified(lastID, testInstant); err != nil {
		t.Fatalf("MarkTodoNotified: %v", err)
	}
	notified, _, _ := s.TodoByID(lastID)
	if !notified.NotifiedAt.Equal(testInstant) {
		t.Errorf("notified_at 未写入: %v", notified.NotifiedAt)
	}
	if !notified.UpdatedAt.Equal(after.UpdatedAt) {
		t.Errorf("提醒状态不应改 updated_at: before=%v after=%v", after.UpdatedAt, notified.UpdatedAt)
	}
	notified.Title = "改标题但不动期限"
	if err := s.UpdateTodo(notified); err != nil {
		t.Fatalf("UpdateTodo(保留提醒): %v", err)
	}
	kept, _, _ := s.TodoByID(lastID)
	if !kept.NotifiedAt.Equal(testInstant) {
		t.Errorf("不改期限时提醒状态应保留: %v", kept.NotifiedAt)
	}
	kept.DueAt = kept.DueAt.Add(time.Hour)
	if err := s.UpdateTodo(kept); err != nil {
		t.Fatalf("UpdateTodo(改期限): %v", err)
	}
	changed, _, _ := s.TodoByID(lastID)
	if !changed.NotifiedAt.IsZero() {
		t.Errorf("改截止时间应清空提醒状态: %v", changed.NotifiedAt)
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

// TestRepoReviewRoundTrip 评分/评价四个新列的往返：新建、单条读回、列表读回、
// 按项目过滤与整条更新都要带上；review 里的换行与中文必须原样存取。
func TestRepoReviewRoundTrip(t *testing.T) {
	s := openTemp(t)

	const review = "整体不错：\n- 结构清晰\n- 注释到位\n\n下一步补测试。"

	pID, err := s.CreateProject(Project{Name: "无人机平台"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateRepo(Repo{
		ProjectID:  pID,
		Name:       "bestuav/atlas",
		LocalPath:  `D:\code\atlas`,
		Branch:     "main",
		Note:       "主仓库",
		Importance: 5,
		Learning:   4,
		Difficulty: 3,
		Review:     review,
		Status:     "正常",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	assertReview := func(t *testing.T, r Repo) {
		t.Helper()
		if r.Importance != 5 || r.Learning != 4 || r.Difficulty != 3 || r.Review != review {
			t.Errorf("评分/评价丢失: %+v", r)
		}
	}

	got, ok, err := s.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("RepoByID: ok=%v err=%v", ok, err)
	}
	assertReview(t, got)

	list, err := s.ListRepos()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListRepos: n=%d err=%v", len(list), err)
	}
	assertReview(t, list[0])

	byProject, err := s.ReposByProject(pID)
	if err != nil || len(byProject) != 1 {
		t.Fatalf("ReposByProject: n=%d err=%v", len(byProject), err)
	}
	assertReview(t, byProject[0])

	// 整条更新同样要写到这四个列。
	got.Importance, got.Learning, got.Difficulty = 1, 2, 0
	got.Review = "改过的评价：\n第二行"
	if err := s.UpdateRepo(got); err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	after, _, err := s.RepoByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Importance != 1 || after.Learning != 2 || after.Difficulty != 0 ||
		after.Review != "改过的评价：\n第二行" {
		t.Errorf("更新未写到评分/评价列: %+v", after)
	}
}

// TestSaveRepoReviewOnlyTouchesReview 详情页保存只动评分与评价这 4 列：
// name/path/branch/note/project/status 等人工与状态字段一概不能被冲掉，
// created_at 保持，updated_at 刷新。
func TestSaveRepoReviewOnlyTouchesReview(t *testing.T) {
	s := openTemp(t)

	id, err := s.CreateRepo(Repo{
		ProjectID:  5,
		Name:       "bestuav/atlas",
		URL:        "https://example.com/bestuav/atlas.git",
		LocalPath:  `D:\code\atlas`,
		Branch:     "main",
		Note:       "别动我",
		Status:     "待推送",
		StatusMsg:  "领先 3 个提交",
		Dirty:      1,
		Ahead:      3,
		Behind:     2,
		LastCommit: "abc1234 修复登录",
		Importance: 1,
		Learning:   1,
		Difficulty: 1,
		Review:     "旧评价",
	})
	if err != nil {
		t.Fatal(err)
	}

	before, ok, err := s.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("RepoByID: ok=%v err=%v", ok, err)
	}

	time.Sleep(10 * time.Millisecond)
	review := "新评价：\n- 第一条\n- 第二条"
	if err := s.SaveRepoReview(id, 5, 4, 3, review); err != nil {
		t.Fatalf("SaveRepoReview: %v", err)
	}

	got, ok, err := s.RepoByID(id)
	if err != nil || !ok {
		t.Fatalf("RepoByID: ok=%v err=%v", ok, err)
	}
	if got.Importance != 5 || got.Learning != 4 || got.Difficulty != 3 || got.Review != review {
		t.Errorf("评分/评价未更新: %+v", got)
	}
	// 其余列一概不动。
	if got.ProjectID != before.ProjectID || got.Name != before.Name || got.URL != before.URL ||
		got.LocalPath != before.LocalPath || got.Branch != before.Branch || got.Note != before.Note ||
		got.Status != before.Status || got.StatusMsg != before.StatusMsg ||
		got.Dirty != before.Dirty || got.Ahead != before.Ahead || got.Behind != before.Behind ||
		got.LastCommit != before.LastCommit || !got.LastCommitAt.Equal(before.LastCommitAt) ||
		!got.CheckedAt.Equal(before.CheckedAt) {
		t.Errorf("SaveRepoReview 不应改动其他列:\nbefore=%+v\n after=%+v", before, got)
	}
	if !got.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("不应改 created_at: before=%v after=%v", before.CreatedAt, got.CreatedAt)
	}
	if !got.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("应刷新 updated_at: before=%v after=%v", before.UpdatedAt, got.UpdatedAt)
	}
}

// TestSaveRepoReviewValidation 评分越界直接拒绝且不写库；0 与 5 是合法边界。
func TestSaveRepoReviewValidation(t *testing.T) {
	s := openTemp(t)

	id, err := s.CreateRepo(Repo{
		Name:       "bestuav/atlas",
		Importance: 2,
		Learning:   3,
		Difficulty: 4,
		Review:     "原评价",
	})
	if err != nil {
		t.Fatal(err)
	}

	bad := []struct {
		reason                           string
		importance, learning, difficulty int
	}{
		{"重要程度为 -1", -1, 3, 4},
		{"重要程度为 6", 6, 3, 4},
		{"可学习性为 -1", 2, -1, 4},
		{"可学习性为 6", 2, 6, 4},
		{"难度为 -1", 2, 3, -1},
		{"难度为 6", 2, 3, 6},
	}
	for _, b := range bad {
		if err := s.SaveRepoReview(id, b.importance, b.learning, b.difficulty, "不该写进去"); err == nil {
			t.Errorf("%s：应返回错误", b.reason)
		}
	}
	got, _, _ := s.RepoByID(id)
	if got.Importance != 2 || got.Learning != 3 || got.Difficulty != 4 || got.Review != "原评价" {
		t.Errorf("越界被拒后不应写库: %+v", got)
	}
	if err := s.SaveRepoReview(0, 0, 0, 0, ""); err == nil {
		t.Error("id<=0 应被拒绝")
	}
	if err := s.SaveRepoReview(id, 0, 5, 0, ""); err != nil {
		t.Errorf("0 与 5 应视为合法评分: %v", err)
	}
}

// TestTodoQuadrant 重要/紧急的往返与艾森豪威尔四象限的四种组合：
// 落库是 0/1 整数，读回是 bool；新建、列表、更新三条路径都要带上。
func TestTodoQuadrant(t *testing.T) {
	s := openTemp(t)

	combos := []struct {
		title     string
		important bool
		urgent    bool
	}{
		{"重要且紧急", true, true},
		{"重要不紧急", true, false},
		{"紧急不重要", false, true},
		{"都不", false, false},
	}
	ids := make([]int64, 0, len(combos))
	for _, c := range combos {
		id, err := s.CreateTodo(Todo{
			Title:     c.title,
			Status:    "待办",
			Priority:  1,
			Important: c.important,
			Urgent:    c.urgent,
		})
		if err != nil {
			t.Fatalf("CreateTodo(%s): %v", c.title, err)
		}
		ids = append(ids, id)
	}

	// 单条读回。
	for i, c := range combos {
		got, ok, err := s.TodoByID(ids[i])
		if err != nil || !ok {
			t.Fatalf("TodoByID(%s): ok=%v err=%v", c.title, ok, err)
		}
		if got.Important != c.important || got.Urgent != c.urgent {
			t.Errorf("%s 往返错误: important=%v urgent=%v", c.title, got.Important, got.Urgent)
		}
	}

	// 列表读回：ListTodos 也必须带这两列。
	list, err := s.ListTodos()
	if err != nil || len(list) != len(combos) {
		t.Fatalf("ListTodos: n=%d err=%v", len(list), err)
	}
	byTitle := make(map[string]Todo, len(list))
	for _, td := range list {
		byTitle[td.Title] = td
	}
	for _, c := range combos {
		td, ok := byTitle[c.title]
		if !ok {
			t.Fatalf("列表里缺少 %q", c.title)
		}
		if td.Important != c.important || td.Urgent != c.urgent {
			t.Errorf("列表里 %s 的两列错误: %+v", c.title, td)
		}
	}

	// 落库用 0/1 整数表示（bool ↔ 0/1）。
	var important, urgent int
	if err := s.db.QueryRow(`SELECT important, urgent FROM todo WHERE id = ?`, ids[0]).
		Scan(&important, &urgent); err != nil {
		t.Fatalf("直接读列: %v", err)
	}
	if important != 1 || urgent != 1 {
		t.Errorf("bool 应以 1/0 落库，实际 important=%d urgent=%d", important, urgent)
	}

	// 更新也要带上两列：把「都不」改成「重要且紧急」。
	last, _, _ := s.TodoByID(ids[3])
	last.Important, last.Urgent = true, true
	if err := s.UpdateTodo(last); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	after, _, _ := s.TodoByID(ids[3])
	if !after.Important || !after.Urgent {
		t.Errorf("更新未写到 important/urgent: %+v", after)
	}
}

// TestMigrateV3Database 老库升级：手工用 database/sql 建一个 v3 结构（无新列）
// 的库并写入数据，用新版 Open 打开后应自动补列——旧数据还在、新列取默认值、
// 之后能写新值，再打开一次也不报错（补列幂等）。
func TestMigrateV3Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")

	// 这里刻意绕开 Open：模拟旧版本写出来的文件（schema_meta version=3，
	// repo/todo 还是旧表结构）。
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开 v3 库失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	v3 := []string{
		`CREATE TABLE schema_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE project (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL,
			"desc"     TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT '进行中',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE repo (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id     INTEGER NOT NULL DEFAULT 0,
			name           TEXT NOT NULL,
			url            TEXT NOT NULL DEFAULT '',
			local_path     TEXT NOT NULL DEFAULT '',
			branch         TEXT NOT NULL DEFAULT '',
			note           TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT '未知',
			status_msg     TEXT NOT NULL DEFAULT '',
			dirty          INTEGER NOT NULL DEFAULT 0,
			ahead          INTEGER NOT NULL DEFAULT 0,
			behind         INTEGER NOT NULL DEFAULT 0,
			last_commit    TEXT NOT NULL DEFAULT '',
			last_commit_at TEXT NOT NULL DEFAULT '',
			checked_at     TEXT NOT NULL DEFAULT '',
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL
		)`,
		`CREATE TABLE todo (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			title      TEXT NOT NULL,
			note       TEXT NOT NULL DEFAULT '',
			project_id INTEGER NOT NULL DEFAULT 0,
			repo_id    INTEGER NOT NULL DEFAULT 0,
			priority   INTEGER NOT NULL DEFAULT 1,
			status     TEXT NOT NULL DEFAULT '待办',
			due_at     TEXT NOT NULL DEFAULT '',
			done_at    TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, q := range v3 {
		if _, err := db.Exec(q); err != nil {
			_ = db.Close()
			t.Fatalf("建 v3 表失败: %v", err)
		}
	}

	ts := nowText()
	if _, err := db.Exec(
		`INSERT INTO repo(project_id, name, url, local_path, branch, note, status,
		                  status_msg, dirty, ahead, behind, last_commit, last_commit_at,
		                  checked_at, created_at, updated_at)
		 VALUES(0, 'old/repo', '', 'D:\code\old', 'main', '老备注', '正常',
		        '工作区干净', 0, 0, 0, '', '', '', ?, ?)`, ts, ts); err != nil {
		_ = db.Close()
		t.Fatalf("写入 v3 仓库失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO todo(title, note, project_id, repo_id, priority, status,
		                  due_at, done_at, created_at, updated_at)
		 VALUES('老待办', '升级前写入', 0, 0, 2, '待办', '', '', ?, ?)`, ts, ts); err != nil {
		_ = db.Close()
		t.Fatalf("写入 v3 待办失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_meta(key, value) VALUES('schema_version', '3')`); err != nil {
		_ = db.Close()
		t.Fatalf("写入 v3 版本号失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 v3 库失败: %v", err)
	}

	// 用当前版本打开，升级应自动发生。
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v3 库应自动迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if v, _, err := s.Flag("schema_version"); err != nil || v != schemaVersion {
		t.Fatalf("升级后版本应为 %s，实际 %q err=%v", schemaVersion, v, err)
	}

	repos, err := s.ListRepos()
	if err != nil || len(repos) != 1 || repos[0].Name != "old/repo" {
		t.Fatalf("升级后老仓库应能读出: rows=%v err=%v", repos, err)
	}
	r := repos[0]
	if r.Importance != 0 || r.Learning != 0 || r.Difficulty != 0 || r.Review != "" {
		t.Errorf("升级后新列默认值应为 0/空串: %+v", r)
	}

	todos, err := s.ListTodos()
	if err != nil || len(todos) != 1 || todos[0].Title != "老待办" {
		t.Fatalf("升级后老待办应能读出: rows=%v err=%v", todos, err)
	}
	if todos[0].Important || todos[0].Urgent {
		t.Errorf("升级后待办默认不应重要/紧急: %+v", todos[0])
	}

	// 升级后两条写入路径都可用。
	if err := s.SaveRepoReview(r.ID, 4, 3, 2, "升级后补的评价\n第二行"); err != nil {
		t.Fatalf("SaveRepoReview: %v", err)
	}
	reviewed, _, _ := s.RepoByID(r.ID)
	if reviewed.Importance != 4 || reviewed.Learning != 3 || reviewed.Difficulty != 2 ||
		reviewed.Review != "升级后补的评价\n第二行" {
		t.Errorf("升级后写评分/评价失败: %+v", reviewed)
	}

	td := todos[0]
	td.Important, td.Urgent = true, false
	if err := s.UpdateTodo(td); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	tdAfter, _, _ := s.TodoByID(td.ID)
	if !tdAfter.Important || tdAfter.Urgent {
		t.Errorf("升级后写 important/urgent 失败: %+v", tdAfter)
	}

	// 再打开一次：补列是幂等的，不应报 duplicate column name。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重复打开升级后的库: %v", err)
	}
	defer s2.Close()
}

// TestRepoHostRoundTrip Host 列的往返：新建、单条读回、列表、按项目过滤与整条
// 更新都要带上；空串（只有名字、没有地址的本地仓库）必须原样存取；SaveRepoStatus
// 只写状态列，快照里的 Host 不能覆盖库里的值。
func TestRepoHostRoundTrip(t *testing.T) {
	s := openTemp(t)

	pID, err := s.CreateProject(Project{Name: "无人机平台"})
	if err != nil {
		t.Fatal(err)
	}
	withHost, err := s.CreateRepo(Repo{
		ProjectID: pID,
		Name:      "bestuav/atlas",
		URL:       "https://github.com/bestuav/atlas.git",
		Host:      "github.com",
		LocalPath: `D:\code\atlas`,
		Status:    "未知",
	})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}
	localOnly, err := s.CreateRepo(Repo{Name: "local/only", Status: "未知"})
	if err != nil {
		t.Fatalf("CreateRepo: %v", err)
	}

	got, ok, err := s.RepoByID(withHost)
	if err != nil || !ok {
		t.Fatalf("RepoByID: ok=%v err=%v", ok, err)
	}
	if got.Host != "github.com" {
		t.Errorf("Host = %q，期望 github.com", got.Host)
	}
	// 空 Host 读回来必须是空串（不是 NULL、也不是别的脏值）。
	if only, _, err := s.RepoByID(localOnly); err != nil || only.Host != "" {
		t.Errorf("空 Host 应原样读回空串: host=%q err=%v", only.Host, err)
	}

	// 列表与按项目过滤两条路径都必须带上 Host。
	wantHost := map[int64]string{withHost: "github.com", localOnly: ""}
	list, err := s.ListRepos()
	if err != nil || len(list) != 2 {
		t.Fatalf("ListRepos: n=%d err=%v", len(list), err)
	}
	for _, r := range list {
		if r.Host != wantHost[r.ID] {
			t.Errorf("列表里 id=%d 的 Host = %q，期望 %q", r.ID, r.Host, wantHost[r.ID])
		}
	}
	byProject, err := s.ReposByProject(pID)
	if err != nil || len(byProject) != 1 || byProject[0].Host != "github.com" {
		t.Errorf("ReposByProject 未带上 Host: %+v err=%v", byProject, err)
	}

	// 整条更新：换平台，再清空（回到「未知」）。
	got.Host = "gitee.com"
	if err := s.UpdateRepo(got); err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	if after, _, _ := s.RepoByID(withHost); after.Host != "gitee.com" {
		t.Errorf("更新 Host 未生效: %q", after.Host)
	}
	got.Host = ""
	if err := s.UpdateRepo(got); err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	if after, _, _ := s.RepoByID(withHost); after.Host != "" {
		t.Errorf("清空 Host 未生效: %q", after.Host)
	}

	// SaveRepoStatus 不写 Host：快照里塞进来的主机名不该覆盖库里的值。
	got.Host = "atomgit.com"
	if err := s.UpdateRepo(got); err != nil {
		t.Fatalf("UpdateRepo: %v", err)
	}
	if err := s.SaveRepoStatus(withHost, Repo{Host: "被覆盖的主机", Status: "正常"}); err != nil {
		t.Fatalf("SaveRepoStatus: %v", err)
	}
	after, _, _ := s.RepoByID(withHost)
	if after.Host != "atomgit.com" {
		t.Errorf("SaveRepoStatus 不应写 Host，实际 %q", after.Host)
	}
	if after.Status != "正常" {
		t.Errorf("状态列应被更新，实际 %q", after.Status)
	}
}

// TestMigrateV4Database 老库升级：手工建一个 v4 结构的库（已有评分/评价与
// 待办重要/紧急，但 repo 还没有 host，schema_meta 是 4），写入一行；用新版
// Open 打开后应自动补 host 列——旧行 Host 为空串、v4 的旧数据原样保留、
// 之后能写新值，再打开一次也不报错（补列幂等）。
func TestMigrateV4Database(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v4.db")

	// 刻意绕开 Open：模拟 v4 版本写出来的文件。
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("打开 v4 库失败: %v", err)
	}
	db.SetMaxOpenConns(1)
	v4 := []string{
		`CREATE TABLE schema_meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE project (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT NOT NULL,
			"desc"     TEXT NOT NULL DEFAULT '',
			status     TEXT NOT NULL DEFAULT '进行中',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE repo (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id     INTEGER NOT NULL DEFAULT 0,
			name           TEXT NOT NULL,
			url            TEXT NOT NULL DEFAULT '',
			local_path     TEXT NOT NULL DEFAULT '',
			branch         TEXT NOT NULL DEFAULT '',
			note           TEXT NOT NULL DEFAULT '',
			importance     INTEGER NOT NULL DEFAULT 0,
			learning       INTEGER NOT NULL DEFAULT 0,
			difficulty     INTEGER NOT NULL DEFAULT 0,
			review         TEXT NOT NULL DEFAULT '',
			status         TEXT NOT NULL DEFAULT '未知',
			status_msg     TEXT NOT NULL DEFAULT '',
			dirty          INTEGER NOT NULL DEFAULT 0,
			ahead          INTEGER NOT NULL DEFAULT 0,
			behind         INTEGER NOT NULL DEFAULT 0,
			last_commit    TEXT NOT NULL DEFAULT '',
			last_commit_at TEXT NOT NULL DEFAULT '',
			checked_at     TEXT NOT NULL DEFAULT '',
			created_at     TEXT NOT NULL,
			updated_at     TEXT NOT NULL
		)`,
		`CREATE TABLE todo (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			title      TEXT NOT NULL,
			note       TEXT NOT NULL DEFAULT '',
			project_id INTEGER NOT NULL DEFAULT 0,
			repo_id    INTEGER NOT NULL DEFAULT 0,
			priority   INTEGER NOT NULL DEFAULT 1,
			important  INTEGER NOT NULL DEFAULT 0,
			urgent     INTEGER NOT NULL DEFAULT 0,
			status     TEXT NOT NULL DEFAULT '待办',
			due_at     TEXT NOT NULL DEFAULT '',
			done_at    TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, q := range v4 {
		if _, err := db.Exec(q); err != nil {
			_ = db.Close()
			t.Fatalf("建 v4 表失败: %v", err)
		}
	}

	const oldReview = "升级前的评价\n第二行"
	ts := nowText()
	if _, err := db.Exec(
		`INSERT INTO repo(project_id, name, url, local_path, branch, note,
		                  importance, learning, difficulty, review,
		                  status, status_msg, dirty, ahead, behind, last_commit, last_commit_at,
		                  checked_at, created_at, updated_at)
		 VALUES(0, 'old/repo', 'https://github.com/old/repo.git', 'D:\code\old', 'main', '老备注',
		        3, 4, 2, ?, '正常', '工作区干净', 0, 0, 0, '', '', '', ?, ?)`,
		oldReview, ts, ts); err != nil {
		_ = db.Close()
		t.Fatalf("写入 v4 仓库失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO todo(title, note, project_id, repo_id, priority, important, urgent, status,
		                  due_at, done_at, created_at, updated_at)
		 VALUES('老待办', '升级前写入', 0, 0, 2, 1, 0, '待办', '', '', ?, ?)`, ts, ts); err != nil {
		_ = db.Close()
		t.Fatalf("写入 v4 待办失败: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_meta(key, value) VALUES('schema_version', '4')`); err != nil {
		_ = db.Close()
		t.Fatalf("写入 v4 版本号失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("关闭 v4 库失败: %v", err)
	}

	// 用当前版本打开，升级应自动发生。
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v4 库应自动迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if v, _, err := s.Flag("schema_version"); err != nil || v != schemaVersion {
		t.Fatalf("升级后版本应为 %s，实际 %q err=%v", schemaVersion, v, err)
	}

	// 补列走的是 PRAGMA table_info 缺列检测：host 必须真的长出来了。
	cols, err := s.tableColumns("repo")
	if err != nil {
		t.Fatalf("读取 repo 表结构: %v", err)
	}
	if !cols["host"] {
		t.Fatalf("升级后 repo 应补上 host 列，实际列 = %v", cols)
	}

	repos, err := s.ListRepos()
	if err != nil || len(repos) != 1 || repos[0].Name != "old/repo" {
		t.Fatalf("升级后老仓库应能读出: rows=%v err=%v", repos, err)
	}
	r := repos[0]
	if r.Host != "" {
		t.Errorf("升级后旧行 Host 应为空串（未知），实际 %q", r.Host)
	}
	// v4 已有的数据不能被补列过程弄丢。
	if r.URL != "https://github.com/old/repo.git" || r.LocalPath != `D:\code\old` ||
		r.Importance != 3 || r.Learning != 4 || r.Difficulty != 2 || r.Review != oldReview {
		t.Errorf("升级后 v4 旧数据丢失: %+v", r)
	}

	todos, err := s.ListTodos()
	if err != nil || len(todos) != 1 || todos[0].Title != "老待办" {
		t.Fatalf("升级后老待办应能读出: rows=%v err=%v", todos, err)
	}
	if !todos[0].Important || todos[0].Urgent {
		t.Errorf("升级后待办的重要/紧急应保留: %+v", todos[0])
	}

	// 升级后 Host 可写：旧行补写、再清空、新建行直接带上。
	r.Host = "github.com"
	if err := s.UpdateRepo(r); err != nil {
		t.Fatalf("升级后写 Host: %v", err)
	}
	if after, _, _ := s.RepoByID(r.ID); after.Host != "github.com" {
		t.Errorf("升级后写 Host 未生效: %q", after.Host)
	}
	r.Host = ""
	if err := s.UpdateRepo(r); err != nil {
		t.Fatalf("升级后清空 Host: %v", err)
	}
	if after, _, _ := s.RepoByID(r.ID); after.Host != "" {
		t.Errorf("升级后清空 Host 未生效: %q", after.Host)
	}

	newID, err := s.CreateRepo(Repo{
		Name: "new/repo",
		URL:  "https://gitee.com/new/repo.git",
		Host: "gitee.com",
	})
	if err != nil {
		t.Fatalf("升级后 CreateRepo: %v", err)
	}
	if created, _, _ := s.RepoByID(newID); created.Host != "gitee.com" {
		t.Errorf("升级后新建仓库的 Host 丢失: %q", created.Host)
	}

	// 再打开一次：补列是幂等的，不应报 duplicate column name。
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重复打开升级后的库: %v", err)
	}
	defer s2.Close()
	if v, _, err := s2.Flag("schema_version"); err != nil || v != schemaVersion {
		t.Errorf("重开后版本应保持 %s，实际 %q err=%v", schemaVersion, v, err)
	}
	if again, _, _ := s2.RepoByID(newID); again.Host != "gitee.com" {
		t.Errorf("重开后 Host 应持久化: %q", again.Host)
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
