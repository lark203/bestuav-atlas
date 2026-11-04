package store

import (
	"path/filepath"
	"testing"
	"time"
)

// openTemp 在临时目录里打开一个全新的数据库，测试结束自动关闭。
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestTaskRoundTrip 验证任务写入后能按时间倒序读出，字段不丢。
func TestTaskRoundTrip(t *testing.T) {
	s := openTemp(t)

	if _, err := s.InsertTask("同步授权数据", "轻量级", "已完成", "0.8s"); err != nil {
		t.Fatalf("InsertTask: %v", err)
	}
	if _, err := s.InsertTask("重建检索索引", "重量级", "排队", "—"); err != nil {
		t.Fatalf("InsertTask: %v", err)
	}

	rows, err := s.RecentTasks(10)
	if err != nil {
		t.Fatalf("RecentTasks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有 2 条记录，实际 %d", len(rows))
	}
	// 最近写入的排在最前（列表就是按这个顺序展示的）。
	if rows[0].Name != "重建检索索引" || rows[1].Name != "同步授权数据" {
		t.Fatalf("顺序错误: %+v", rows)
	}
	if rows[0].Kind != "重量级" || rows[0].Status != "排队" || rows[0].Cost != "—" {
		t.Errorf("字段丢失: %+v", rows[0])
	}
	if rows[0].CreatedAt.IsZero() {
		t.Error("created_at 未正确解析")
	}
}

// TestPersistAcrossReopen 验证关库重开后数据仍在（真正的「持久化」）。
func TestPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := s1.InsertTask("导出月度报表", "轻量级", "已完成", "2.1s"); err != nil {
		t.Fatalf("InsertTask: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("重新 Open: %v", err)
	}
	defer s2.Close()

	rows, err := s2.RecentTasks(10)
	if err != nil {
		t.Fatalf("RecentTasks: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "导出月度报表" {
		t.Fatalf("重开后数据丢失: %+v", rows)
	}
}

// TestClearFinishedTasks 验证只删「已完成」，排队/运行中的保留。
func TestClearFinishedTasks(t *testing.T) {
	s := openTemp(t)
	for _, r := range []struct{ name, status string }{
		{"已完成的任务", "已完成"},
		{"排队中的任务", "排队"},
		{"运行中的任务", "运行中"},
	} {
		if _, err := s.InsertTask(r.name, "轻量级", r.status, "1s"); err != nil {
			t.Fatalf("InsertTask: %v", err)
		}
	}

	n, err := s.DeleteFinishedTasks()
	if err != nil {
		t.Fatalf("DeleteFinishedTasks: %v", err)
	}
	if n != 1 {
		t.Errorf("应删除 1 条，实际 %d", n)
	}

	rows, err := s.RecentTasks(10)
	if err != nil {
		t.Fatalf("RecentTasks: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("应剩 2 条，实际 %d", len(rows))
	}
	for _, r := range rows {
		if r.Status == "已完成" {
			t.Errorf("已完成的记录不应残留: %+v", r)
		}
	}
}

// TestLicenseAndEvent 验证授权记录、事件入库与计数统计。
func TestLicenseAndEvent(t *testing.T) {
	s := openTemp(t)

	if _, err := s.InsertLicense("北京必优航空科技", "企业版", "1 年", "payload.sig"); err != nil {
		t.Fatalf("InsertLicense: %v", err)
	}
	lics, err := s.RecentLicenses(10)
	if err != nil {
		t.Fatalf("RecentLicenses: %v", err)
	}
	if len(lics) != 1 || lics[0].Subject != "北京必优航空科技" || lics[0].Token != "payload.sig" {
		t.Fatalf("授权记录错误: %+v", lics)
	}

	if err := s.RecordEvent("INFO", "app", "应用启动", "os=windows"); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}
	if err := s.RecordEvent("ERROR", "store", "写入失败", "err=disk full"); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	events, err := s.RecentEvents(10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("应有 2 条事件，实际 %d", len(events))
	}
	if events[0].Level != "ERROR" || events[0].Kind != "store" {
		t.Errorf("事件顺序/内容错误: %+v", events[0])
	}
	if events[0].Detail != "err=disk full" {
		t.Errorf("detail 丢失: %q", events[0].Detail)
	}

	st, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if st.Licenses != 1 || st.Events != 2 || st.Errors != 1 || st.Tasks != 0 {
		t.Errorf("统计错误: %+v", st)
	}
}

// TestFlag 验证元数据读写（首次运行标记就靠它）。
func TestFlag(t *testing.T) {
	s := openTemp(t)

	if _, ok, err := s.Flag("seeded"); err != nil || ok {
		t.Fatalf("未写入时应 ok=false: ok=%v err=%v", ok, err)
	}
	if err := s.SetFlag("seeded", "1"); err != nil {
		t.Fatalf("SetFlag: %v", err)
	}
	if v, ok, err := s.Flag("seeded"); err != nil || !ok || v != "1" {
		t.Fatalf("读取失败: v=%q ok=%v err=%v", v, ok, err)
	}
	// 覆盖写不应报错。
	if err := s.SetFlag("seeded", "2"); err != nil {
		t.Fatalf("SetFlag 覆盖: %v", err)
	}
	if v, _, _ := s.Flag("seeded"); v != "2" {
		t.Errorf("覆盖失败: %q", v)
	}
}

// TestOpenIsIdempotent 验证同一路径重复打开（建表语句幂等）不会报错或清空数据。
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次 Open: %v", i+1, err)
		}
		if i == 0 {
			if _, err := s.InsertTask("首次运行写入", "轻量级", "已完成", "1s"); err != nil {
				t.Fatalf("InsertTask: %v", err)
			}
		} else {
			rows, err := s.RecentTasks(10)
			if err != nil {
				t.Fatalf("RecentTasks: %v", err)
			}
			if len(rows) != 1 {
				t.Errorf("重复建表不应丢数据，实际 %d 条", len(rows))
			}
		}
		_ = s.Close()
	}
}

// TestRecentEventsLimit 验证 limit 为 0/负数时被兜底处理（不会返回全表或报错）。
func TestRecentEventsLimit(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 3; i++ {
		if err := s.RecordEvent("INFO", "unit", "e", ""); err != nil {
			t.Fatalf("RecordEvent: %v", err)
		}
	}
	if _, err := s.RecentEvents(0); err != nil {
		t.Errorf("limit=0 不应报错: %v", err)
	}
	if rows, err := s.RecentEvents(-1); err != nil || len(rows) != 3 {
		t.Errorf("limit=-1 应回落到默认值: n=%d err=%v", len(rows), err)
	}
}

// TestBackupTo 验证诊断包用的库快照：能导出一份可独立打开的完整库，
// 且不破坏原库、目标已存在时明确报错。
func TestBackupTo(t *testing.T) {
	s := openTemp(t)
	if _, err := s.InsertTask("快照前的任务", "轻量级", "已完成", "0.1s"); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.BackupTo(dst); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	// 快照本身是一个可打开的库，内容与源库一致。
	cp, err := Open(dst)
	if err != nil {
		t.Fatalf("打开快照失败: %v", err)
	}
	defer cp.Close()
	rows, err := cp.RecentTasks(10)
	if err != nil || len(rows) != 1 || rows[0].Name != "快照前的任务" {
		t.Fatalf("快照内容不对: rows=%v err=%v", rows, err)
	}
	// 目标已存在时必须报错（VACUUM INTO 不允许覆盖）。
	if err := s.BackupTo(dst); err == nil {
		t.Fatal("目标已存在时应报错")
	}
}

// TestPrune 验证按条数与时间裁剪：多出来的旧记录被删，新记录保留。
func TestPrune(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 5; i++ {
		if _, err := s.InsertTask("任务", "轻量级", "已完成", "0.1s"); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.Prune(Retention{Tasks: 2})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 3 {
		t.Errorf("应删除 3 条旧任务，实际 %d", n)
	}
	rows, err := s.RecentTasks(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应保留 2 条，实际 %d", len(rows))
	}

	// 全部关闭时不删任何东西。
	if n, err := s.Prune(Retention{}); err != nil || n != 0 {
		t.Errorf("零值保留策略应为空操作，实际 n=%d err=%v", n, err)
	}
}

// TestPruneEventAge 验证事件的时间上限。
func TestPruneEventAge(t *testing.T) {
	s := openTemp(t)
	if err := s.RecordEvent("ERROR", "app", "旧错误", ""); err != nil {
		t.Fatal(err)
	}
	// 把这条事件的 ts 改成 100 天前。
	old := time.Now().Add(-100 * 24 * time.Hour).UTC().Format(timeLayout)
	if _, err := s.db.Exec(`UPDATE app_event SET ts = ?`, old); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(Retention{Events: 100, EventAge: 90 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应删除 1 条过期事件，实际 %d", n)
	}
}

// TestBackupAfterPrune 裁剪后 VACUUM 仍能正常导出快照。
func TestBackupAfterPrune(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < 8; i++ {
		_, _ = s.InsertTask("任务", "轻量级", "已完成", "0.1s")
	}
	if _, err := s.Prune(Retention{Tasks: 1}); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "after.db")
	if err := s.BackupTo(dst); err != nil {
		t.Fatalf("裁剪后备份失败: %v", err)
	}
}

// TestPagePrefRoundTrip 页面私有键：写、读、覆盖、按页面隔离、删除。
func TestPagePrefRoundTrip(t *testing.T) {
	s := openTemp(t)

	if _, ok, err := s.PagePref("orders", "filter"); err != nil || ok {
		t.Fatalf("没写过的键应返回 ok=false，实际 ok=%v err=%v", ok, err)
	}
	if err := s.SetPagePref("orders", "filter", `{"kind":"批发"}`); err != nil {
		t.Fatalf("SetPagePref: %v", err)
	}
	got, ok, err := s.PagePref("orders", "filter")
	if err != nil || !ok {
		t.Fatalf("应读到刚写的键，实际 ok=%v err=%v", ok, err)
	}
	if got != `{"kind":"批发"}` {
		t.Errorf("值被改写: %q", got)
	}

	// 同名 key 在不同页面之间互相隔离。
	if _, ok, _ := s.PagePref("task", "filter"); ok {
		t.Error("页面之间不应串键")
	}

	// 覆盖写。
	if err := s.SetPagePref("orders", "filter", `{"kind":"零售"}`); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.PagePref("orders", "filter"); got != `{"kind":"零售"}` {
		t.Errorf("覆盖失败: %q", got)
	}

	// 删除后回到「没存过」。
	if err := s.DeletePagePref("orders", "filter"); err != nil {
		t.Fatalf("DeletePagePref: %v", err)
	}
	if _, ok, _ := s.PagePref("orders", "filter"); ok {
		t.Error("删除后不应再读到")
	}
	if err := s.DeletePagePref("orders", "filter"); err != nil {
		t.Errorf("重复删除不应报错: %v", err)
	}
}

// TestPagePrefPersistsAcrossReopen 关库重开后页面私有键仍在。
func TestPagePrefPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.SetPagePref("orders", "draft", `{"qty":3}`); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, ok, err := s2.PagePref("orders", "draft")
	if err != nil || !ok {
		t.Fatalf("重开后应读到，实际 ok=%v err=%v", ok, err)
	}
	if got != `{"qty":3}` {
		t.Errorf("重开后值不一致: %q", got)
	}
}

// TestSchemaVersionUpgradesOnce 旧版本号的库被新版打开后应升级到当前版本，
// 但降级（用旧版打开新库）不能把版本写回去。
func TestSchemaVersionUpgradesOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟旧库：把版本改回 1。
	if err := s1.SetFlag("schema_version", "1"); err != nil {
		t.Fatal(err)
	}
	_ = s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s2.Flag("schema_version")
	if err != nil {
		t.Fatal(err)
	}
	if got != schemaVersion {
		t.Errorf("旧库应升级到 %s，实际 %q", schemaVersion, got)
	}

	// 模拟降级：把版本写成 99（比当前高），重开不应被覆盖。
	if err := s2.SetFlag("schema_version", "99"); err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
	s3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	got, _, _ = s3.Flag("schema_version")
	if got != "99" {
		t.Errorf("更高的版本号不应被覆盖，实际 %q", got)
	}
}

// TestPagePrefRejectsEmptyKeys 空 page/key 容易写出「全局」脏数据，直接拒绝。
func TestPagePrefRejectsEmptyKeys(t *testing.T) {
	s := openTemp(t)
	if _, _, err := s.PagePref("", "k"); err == nil {
		t.Error("空 page 应报错")
	}
	if err := s.SetPagePref("orders", "", "x"); err == nil {
		t.Error("空 key 应报错")
	}
}

// TestPagePrefCountAndClear 设置页「清理页面状态」依赖的计数与清空。
func TestPagePrefCountAndClear(t *testing.T) {
	s := openTemp(t)
	if n, err := s.PagePrefCount(); err != nil || n != 0 {
		t.Fatalf("空库计数应为 0，实际 n=%d err=%v", n, err)
	}
	if _, err := s.InsertTask("任务", "轻量级", "已完成", "0.1s"); err != nil {
		t.Fatal(err)
	}
	_ = s.SetPagePref("orders", "filter", `{"a":1}`)
	_ = s.SetPagePref("orders", "draft", `{"b":2}`)
	_ = s.SetPagePref("task", "sort", `"desc"`)
	if n, err := s.PagePrefCount(); err != nil || n != 3 {
		t.Fatalf("计数应为 3，实际 n=%d err=%v", n, err)
	}

	n, err := s.ClearPagePrefs()
	if err != nil {
		t.Fatalf("ClearPagePrefs: %v", err)
	}
	if n != 3 {
		t.Errorf("应删除 3 条，实际 %d", n)
	}
	if n, _ := s.PagePrefCount(); n != 0 {
		t.Errorf("清空后计数应为 0，实际 %d", n)
	}
	// 只清页面状态：任务记录不受影响。
	if rows, err := s.RecentTasks(10); err != nil || len(rows) != 1 {
		t.Errorf("清空页面状态不应影响任务表: rows=%d err=%v", len(rows), err)
	}
}
