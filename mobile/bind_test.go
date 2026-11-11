package mobile

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这些测试跑在桌面（GOOS=windows）上，但走的是手机端唯一那条路径：
// Call(method, json) → store → JSON。gomobile 产物只是把这些函数再包一层，
// 所以这里绿了，手机上的读写逻辑就绿了。

func openTempCore(t *testing.T) {
	t.Helper()
	if err := Close(); err != nil { // 上一个用例可能留着句柄
		t.Fatalf("Close: %v", err)
	}
	if err := Open(filepath.Join(t.TempDir(), "atlas.db")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = Close() })
}

// call 调用一个方法并把结果解成 map；任何错误都让用例失败。
func call(t *testing.T, method, req string) map[string]any {
	t.Helper()
	raw, err := Call(method, req)
	if err != nil {
		t.Fatalf("%s(%s): %v", method, req, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("%s 返回的不是合法 JSON: %v\n%s", method, err, raw)
	}
	return out
}

func items(t *testing.T, resp map[string]any) []any {
	t.Helper()
	got, ok := resp["items"].([]any)
	if !ok {
		t.Fatalf("返回里没有 items 数组: %#v", resp)
	}
	return got
}

// TestCallBeforeOpen 没打开库就调用，应报错而不是崩。
func TestCallBeforeOpen(t *testing.T) {
	if err := Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Call("schema.version", ""); err == nil {
		t.Fatal("未打开数据库时应返回错误")
	}
}

// TestSchemaVersion 版本号要与 store 同步，手机端据此判断能不能处理数据。
func TestSchemaVersion(t *testing.T) {
	openTempCore(t)
	resp := call(t, "schema.version", "")
	if resp["version"] != "11" {
		t.Fatalf("schema 版本应为 11，实际 %v", resp["version"])
	}
	if resp["protocol"] != float64(1) {
		t.Fatalf("协议版本应为 1，实际 %v", resp["protocol"])
	}
}

// TestSaveAndListTodo 新建 → 列表 → 改状态，是手机端最高频的一条链路。
func TestSaveAndListTodo(t *testing.T) {
	openTempCore(t)

	saved := call(t, "todos.save", `{"title":"买电池","due_at":"2027-01-20T04:00:00Z"}`)
	if saved["created"] != true {
		t.Fatalf("应标记为新建: %#v", saved)
	}
	todo, ok := saved["todo"].(map[string]any)
	if !ok {
		t.Fatalf("返回里没有 todo: %#v", saved)
	}
	uid, _ := todo["uid"].(string)
	if uid == "" {
		t.Fatal("新建待办应有 uid")
	}
	if todo["status"] != "待办" {
		t.Errorf("默认状态应为待办，实际 %v", todo["status"])
	}
	if todo["priority"] != float64(1) {
		t.Errorf("默认优先级应为中(1)，实际 %v", todo["priority"])
	}

	if n := len(items(t, call(t, "todos.list", `{}`))); n != 1 {
		t.Fatalf("列表应有 1 条，实际 %d", n)
	}

	call(t, "todos.set_status", `{"uid":"`+uid+`","status":"已完成"}`)
	done := items(t, call(t, "todos.list", `{"status":"已完成"}`))
	if len(done) != 1 {
		t.Fatalf("已完成列表应有 1 条，实际 %d", len(done))
	}
	open := items(t, call(t, "todos.list", `{"status":"待办"}`))
	if len(open) != 0 {
		t.Fatalf("待办列表应为空，实际 %d", len(open))
	}
}

// TestTodayClassification 复刻桌面口径：开始日 ≤ 今天 ≤ 截止日 才算「今天」，
// 截止日已过进「逾期」组。用固定日期，避免用例随执行时刻变脸。
func TestTodayClassification(t *testing.T) {
	openTempCore(t)

	call(t, "todos.save", `{"title":"今天要做","start_at":"2027-01-10T00:00:00Z","due_at":"2027-01-20T04:00:00Z"}`)
	call(t, "todos.save", `{"title":"已经逾期","due_at":"2027-01-10T04:00:00Z"}`)
	call(t, "todos.save", `{"title":"还没开始","start_at":"2027-02-01T00:00:00Z","due_at":"2027-02-10T04:00:00Z"}`)
	call(t, "todos.save", `{"title":"没有期限"}`)

	resp := call(t, "todos.today", `{"day":"2027-01-15","tz":"Asia/Shanghai"}`)
	today, _ := resp["today"].([]any)
	overdue, _ := resp["overdue"].([]any)

	titles := func(list []any) []string {
		var out []string
		for _, it := range list {
			m, _ := it.(map[string]any)
			s, _ := m["title"].(string)
			out = append(out, s)
		}
		return out
	}
	gotToday, gotOverdue := strings.Join(titles(today), ","), strings.Join(titles(overdue), ",")

	if !strings.Contains(gotToday, "今天要做") {
		t.Errorf("「今天要做」应在今天组，实际今天组=%q", gotToday)
	}
	if !strings.Contains(gotToday, "没有期限") {
		t.Errorf("无期限待办应算今天，实际今天组=%q", gotToday)
	}
	if strings.Contains(gotToday, "还没开始") {
		t.Errorf("还没到开始日的不该在今天组，实际今天组=%q", gotToday)
	}
	if !strings.Contains(gotOverdue, "已经逾期") {
		t.Errorf("逾期应进逾期组，实际逾期组=%q", gotOverdue)
	}
	if strings.Contains(gotToday, "已经逾期") {
		t.Errorf("逾期不该混进今天组，实际今天组=%q", gotToday)
	}

	counts, _ := resp["counts"].(map[string]any)
	if counts["today"] != float64(len(today)) {
		t.Errorf("counts.today 与数组长度不一致: %v", counts)
	}
}

// TestReminderWindow 只返回「未来一段时间内到期」的未完成待办，供手机排通知。
func TestReminderWindow(t *testing.T) {
	openTempCore(t)

	soon := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	far := time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339Nano)
	call(t, "todos.save", `{"title":"两小时后","due_at":"`+soon+`"}`)
	call(t, "todos.save", `{"title":"一个月后","due_at":"`+far+`"}`)
	call(t, "todos.save", `{"title":"没期限"}`)

	resp := call(t, "reminders.window", `{"hours":24,"limit":60}`)
	got := items(t, resp)
	if len(got) != 1 {
		t.Fatalf("24 小时窗口应只有 1 条，实际 %d", len(got))
	}
	first, _ := got[0].(map[string]any)
	if first["title"] != "两小时后" {
		t.Errorf("窗口里应是两小时后那条，实际 %v", first["title"])
	}
}

// TestSyncRoundTrip 本地变更 → 取待推送 → 标记已发送 → 合入远端变更。
func TestSyncRoundTrip(t *testing.T) {
	openTempCore(t)

	call(t, "todos.save", `{"title":"本地记的"}`)
	pending := call(t, "sync.pending", "")
	ops, _ := pending["ops"].([]any)
	if len(ops) != 1 {
		t.Fatalf("应有 1 条待推送变更，实际 %d", len(ops))
	}
	op, _ := ops[0].(map[string]any)
	opID, _ := op["op_id"].(string)

	call(t, "sync.mark_sent", `{"op_ids":["`+opID+`"]}`)
	if pending := call(t, "sync.pending", ""); pending["count"] != float64(0) {
		t.Fatalf("推送后应清空待推送队列，实际 %v 条", pending["count"])
	}
	// 标记已发送**不推进水位**：水位只能由「拉取并应用」推进，
	// 否则会把别的设备在这中间推的变更整段跳过。
	if seq := call(t, "sync.pending", "")["last_seq"]; seq != "" {
		t.Errorf("mark_sent 不该推进水位，实际 %v", seq)
	}

	// 远端新建一条 → 本地应出现，且不再产生新的待推送变更（不回环）。
	applied := call(t, "sync.apply", `{"ops":[{"op_id":"01REMOTE","entity":"todo","uid":"01REMOTEUID",
		"kind":"create","patch":{"title":"手机上记的","status":"待办","priority":1}}],"next_seq":8}`)
	if applied["applied"] != float64(1) {
		t.Fatalf("应合入 1 条，实际 %#v", applied)
	}
	if titles := items(t, call(t, "todos.list", `{}`)); len(titles) != 2 {
		t.Fatalf("合入后应有 2 条，实际 %d", len(titles))
	}
	if pending := call(t, "sync.pending", ""); pending["count"] != float64(0) {
		t.Fatalf("合入远端变更不该产生本地变更，实际 %v 条", pending["count"])
	}
	if seq := call(t, "sync.pending", "")["last_seq"]; seq != "8" {
		t.Errorf("应用远端变更后水位应推进到 8，实际 %v", seq)
	}
}

// TestUnknownMethod 拼错方法名要报错，不能静默返回空串。
func TestUnknownMethod(t *testing.T) {
	openTempCore(t)
	if _, err := Call("todos.nope", "{}"); err == nil {
		t.Fatal("未知方法应返回错误")
	}
}
