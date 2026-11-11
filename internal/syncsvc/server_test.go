package syncsvc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bestuav-atlas/internal/syncwire"
)

const testAdmin = "test-admin-token"

// newTestServer 起一个跑在 httptest 上的服务端（独立的临时库）。
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := Open(filepath.Join(t.TempDir(), "sync.db"), testAdmin)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		_ = srv.Close()
	})
	return srv, ts
}

// doJSON 发一个 JSON 请求并解出响应体；wanted 是期望的 HTTP 状态码。
func doJSON(t *testing.T, method, url string, body any, headers map[string]string, wanted int) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("编码请求失败: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != wanted {
		t.Fatalf("%s %s: 期望 %d，实际 %d，响应 %v", method, url, wanted, resp.StatusCode, out)
	}
	return out
}

func adminHeaders() map[string]string {
	return map[string]string{"X-Admin-Token": testAdmin}
}

func bearer(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

// registerDevice 走管理接口登记一台设备，返回 (deviceID, token)。
func registerDevice(t *testing.T, ts *httptest.Server, name string) (string, string) {
	t.Helper()
	out := doJSON(t, http.MethodPost, ts.URL+"/v1/admin/devices",
		map[string]string{"device_name": name}, adminHeaders(), http.StatusOK)
	return out["device_id"].(string), out["token"].(string)
}

// TestServerPersistsAcrossRestart 服务端重启（进程退出再拉起）后：设备、变更、水位都要还在。
//
// 同步服务是单进程 + SQLite，重启是常态（升级、机器重启）。op 日志如果丢了，
// 客户端的水位就指向了不存在的位置，会静默漏数据。
func TestServerPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.db")

	srv1, err := Open(path, testAdmin)
	if err != nil {
		t.Fatalf("Open #1: %v", err)
	}
	ts1 := httptest.NewServer(srv1.Handler())
	_, token := registerDevice(t, ts1, "桌面")
	doJSON(t, http.MethodPost, ts1.URL+"/v1/changes", syncwire.PushRequest{Ops: []syncwire.Op{
		{OpID: "01A", Entity: syncwire.EntityTodo, UID: "01A", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "重启前"}},
		{OpID: "01B", Entity: syncwire.EntityTodo, UID: "01B", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "重启前 2"}},
	}}, bearer(token), http.StatusOK)
	ts1.Close()
	if err := srv1.Close(); err != nil {
		t.Fatalf("Close #1: %v", err)
	}

	srv2, err := Open(path, testAdmin)
	if err != nil {
		t.Fatalf("Open #2: %v", err)
	}
	defer func() { _ = srv2.Close() }()
	ts2 := httptest.NewServer(srv2.Handler())
	defer ts2.Close()

	pulled := doJSON(t, http.MethodGet, ts2.URL+"/v1/changes?since=0&limit=100", nil, bearer(token), http.StatusOK)
	if n := len(pulled["ops"].([]any)); n != 2 {
		t.Fatalf("重启后仍有 2 条历史变更，实际 %d", n)
	}
	if pulled["next"] != float64(2) {
		t.Fatalf("水位应接着 2 往下走，实际 %v", pulled["next"])
	}

	// 新变更接着编号，不复用旧 seq。
	pushed := doJSON(t, http.MethodPost, ts2.URL+"/v1/changes", syncwire.PushRequest{Ops: []syncwire.Op{
		{OpID: "01C", Entity: syncwire.EntityTodo, UID: "01C", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "重启后"}},
	}}, bearer(token), http.StatusOK)
	if pushed["seq"] != float64(3) {
		t.Fatalf("重启后的新变更应拿到 seq=3，实际 %v", pushed["seq"])
	}
}

// TestArrivalOrderBeatsClientClocks 顺序由服务端 seq（到达顺序）决定，与客户端时钟无关。
//
// 手机时钟回拨/时区错乱是常见现象；如果按 created_at 排序，就会把新变更排到旧变更前面。
func TestArrivalOrderBeatsClientClocks(t *testing.T) {
	_, ts := newTestServer(t)
	_, token := registerDevice(t, ts, "桌面")

	// 故意让 created_at 与到达顺序相反。
	ops := []syncwire.Op{
		{OpID: "01LATE", Entity: syncwire.EntityTodo, UID: "u1", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "先到但时间戳最晚"}, CreatedAt: "2030-01-01T00:00:00Z"},
		{OpID: "01EARLY", Entity: syncwire.EntityTodo, UID: "u2", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "后到但时间戳最早"}, CreatedAt: "2000-01-01T00:00:00Z"},
	}
	doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
		syncwire.PushRequest{Ops: ops}, bearer(token), http.StatusOK)

	pulled := doJSON(t, http.MethodGet, ts.URL+"/v1/changes?since=0&limit=10", nil, bearer(token), http.StatusOK)
	list := pulled["ops"].([]any)
	if len(list) != 2 {
		t.Fatalf("应拉到 2 条，实际 %d", len(list))
	}
	if list[0].(map[string]any)["op_id"] != "01LATE" {
		t.Fatalf("应按到达顺序返回（01LATE 在前），实际 %v", list[0])
	}
	if list[0].(map[string]any)["seq"] != float64(1) || list[1].(map[string]any)["seq"] != float64(2) {
		t.Fatalf("seq 应按到达顺序递增，实际 %v / %v", list[0].(map[string]any)["seq"], list[1].(map[string]any)["seq"])
	}
}

// TestPairFlow 配对码换令牌：成功一次、不能复用、错码被拒。
func TestPairFlow(t *testing.T) {
	_, ts := newTestServer(t)

	out := doJSON(t, http.MethodPost, ts.URL+"/v1/admin/pair-codes", nil, adminHeaders(), http.StatusOK)
	code := out["code"].(string)
	if len(code) != 8 {
		t.Fatalf("配对码应为 8 位，实际 %q", code)
	}

	paired := doJSON(t, http.MethodPost, ts.URL+"/v1/pair",
		syncwire.PairRequest{Code: code, DeviceName: "手机"}, nil, http.StatusOK)
	token := paired["token"].(string)
	if token == "" || paired["device_id"] == "" {
		t.Fatalf("配对应返回设备 id 与令牌: %v", paired)
	}

	// 令牌能用
	doJSON(t, http.MethodGet, ts.URL+"/v1/health", nil, nil, http.StatusOK)
	doJSON(t, http.MethodGet, ts.URL+"/v1/changes", nil, bearer(token), http.StatusOK)

	// 同一个码不能再用
	doJSON(t, http.MethodPost, ts.URL+"/v1/pair",
		syncwire.PairRequest{Code: code, DeviceName: "再来一次"}, nil, http.StatusUnauthorized)
	// 错码
	doJSON(t, http.MethodPost, ts.URL+"/v1/pair",
		syncwire.PairRequest{Code: "ZZZZZZZZ", DeviceName: "x"}, nil, http.StatusUnauthorized)
}

// TestPairCodeExpires 过期的码不能用。
func TestPairCodeExpires(t *testing.T) {
	srv, ts := newTestServer(t)
	out := doJSON(t, http.MethodPost, ts.URL+"/v1/admin/pair-codes", nil, adminHeaders(), http.StatusOK)
	code := out["code"].(string)

	// 把系统时间推到 6 分钟后：直接用可注入的 now，避免真的 sleep。
	srv.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	doJSON(t, http.MethodPost, ts.URL+"/v1/pair",
		syncwire.PairRequest{Code: code, DeviceName: "手机"}, nil, http.StatusUnauthorized)
}

// TestPushIsIdempotent 同一条变更重发两次只入账一次——离线重发的正确性靠它。
func TestPushIsIdempotent(t *testing.T) {
	_, ts := newTestServer(t)
	_, token := registerDevice(t, ts, "桌面")

	op := syncwire.Op{
		OpID: "01OP", Entity: syncwire.EntityTodo, UID: "01UID", Kind: syncwire.KindCreate,
		Patch: map[string]any{"title": "写文档"},
	}
	first := doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
		syncwire.PushRequest{Ops: []syncwire.Op{op}}, bearer(token), http.StatusOK)
	if first["accepted"] != float64(1) {
		t.Fatalf("首次推送应入账 1 条，实际 %v", first["accepted"])
	}
	second := doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
		syncwire.PushRequest{Ops: []syncwire.Op{op}}, bearer(token), http.StatusOK)
	if second["accepted"] != float64(0) {
		t.Fatalf("重复推送应入账 0 条，实际 %v", second["accepted"])
	}
	if second["seq"] == nil || second["seq"] == float64(0) {
		t.Fatalf("重复推送也要回一个水位，实际 %v", second)
	}

	health := doJSON(t, http.MethodGet, ts.URL+"/v1/health", nil, nil, http.StatusOK)
	if health["ops"] != float64(1) {
		t.Fatalf("服务端应只有 1 条变更，实际 %v", health["ops"])
	}
}

// TestPullOrder 拉取按 seq 升序分页，next 指向本批最后一条。
func TestPullOrder(t *testing.T) {
	_, ts := newTestServer(t)
	_, token := registerDevice(t, ts, "桌面")

	var ops []syncwire.Op
	for i, title := range []string{"一", "二", "三"} {
		ops = append(ops, syncwire.Op{
			OpID:   "01OP" + title,
			Entity: syncwire.EntityTodo,
			UID:    "01UID" + title,
			Kind:   syncwire.KindCreate,
			Patch:  map[string]any{"title": title, "_order": i},
		})
	}
	doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
		syncwire.PushRequest{Ops: ops}, bearer(token), http.StatusOK)

	page := doJSON(t, http.MethodGet, ts.URL+"/v1/changes?since=0&limit=2", nil, bearer(token), http.StatusOK)
	list := page["ops"].([]any)
	if len(list) != 2 {
		t.Fatalf("第一页应 2 条，实际 %d", len(list))
	}
	if page["next"] != float64(2) {
		t.Fatalf("第一页 next 应为 2，实际 %v", page["next"])
	}
	first := list[0].(map[string]any)
	if first["uid"] != "01UID一" || first["seq"] != float64(1) {
		t.Fatalf("应按 seq 升序返回，实际 %v", first)
	}

	rest := doJSON(t, http.MethodGet, ts.URL+"/v1/changes?since=2&limit=10", nil, bearer(token), http.StatusOK)
	if n := len(rest["ops"].([]any)); n != 1 {
		t.Fatalf("第二页应 1 条，实际 %d", n)
	}
	if rest["next"] != float64(3) {
		t.Fatalf("第二页 next 应为 3，实际 %v", rest["next"])
	}

	empty := doJSON(t, http.MethodGet, ts.URL+"/v1/changes?since=3", nil, bearer(token), http.StatusOK)
	if n := len(empty["ops"].([]any)); n != 0 {
		t.Fatalf("没有新变更时应返回空数组，实际 %d", n)
	}
	if empty["next"] != float64(3) {
		t.Fatalf("空批次 next 不该回退，实际 %v", empty["next"])
	}
}

// TestSnapshotMergesPatches 快照是补丁合并后的结果，不是原始 op 列表。
func TestSnapshotMergesPatches(t *testing.T) {
	_, ts := newTestServer(t)
	_, token := registerDevice(t, ts, "桌面")

	ops := []syncwire.Op{
		{OpID: "01P", Entity: syncwire.EntityProject, UID: "01PROJ", Kind: syncwire.KindCreate,
			Patch: map[string]any{"name": "星图", "status": "进行中"}},
		{OpID: "01T", Entity: syncwire.EntityTodo, UID: "01TODO", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "写文档", "status": "待办", "priority": float64(1)}},
		{OpID: "01T2", Entity: syncwire.EntityTodo, UID: "01TODO", Kind: syncwire.KindPatch,
			Patch: map[string]any{"status": "已完成"}},
	}
	doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
		syncwire.PushRequest{Ops: ops}, bearer(token), http.StatusOK)

	snap := doJSON(t, http.MethodGet, ts.URL+"/v1/snapshot", nil, bearer(token), http.StatusOK)
	todos, _ := snap["todos"].([]any)
	projects, _ := snap["projects"].([]any)
	if len(todos) != 1 || len(projects) != 1 {
		t.Fatalf("快照应有 1 待办 1 项目，实际 %d/%d", len(todos), len(projects))
	}
	td := todos[0].(map[string]any)
	if td["title"] != "写文档" {
		t.Errorf("补丁应合并进快照: %v", td)
	}
	if td["status"] != "已完成" {
		t.Errorf("后到的补丁应覆盖先到的字段: %v", td)
	}
}

// TestSnapshotExcludesDeleted 删掉的实体不出现在快照里。
func TestSnapshotExcludesDeleted(t *testing.T) {
	_, ts := newTestServer(t)
	_, token := registerDevice(t, ts, "桌面")

	ops := []syncwire.Op{
		{OpID: "01A", Entity: syncwire.EntityTodo, UID: "01A", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "要删的"}},
		{OpID: "01B", Entity: syncwire.EntityTodo, UID: "01B", Kind: syncwire.KindCreate,
			Patch: map[string]any{"title": "留着的"}},
		{OpID: "01C", Entity: syncwire.EntityTodo, UID: "01A", Kind: syncwire.KindDelete},
	}
	doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
		syncwire.PushRequest{Ops: ops}, bearer(token), http.StatusOK)

	snap := doJSON(t, http.MethodGet, ts.URL+"/v1/snapshot", nil, bearer(token), http.StatusOK)
	todos, _ := snap["todos"].([]any)
	if len(todos) != 1 {
		t.Fatalf("墓碑不该出现在快照里，实际 %d 条", len(todos))
	}
	if todos[0].(map[string]any)["uid"] != "01B" {
		t.Fatalf("留下的应是 01B，实际 %v", todos[0])
	}
}

// TestAuthRequired 没有令牌、或令牌不对，一律 401。
func TestAuthRequired(t *testing.T) {
	_, ts := newTestServer(t)
	_, token := registerDevice(t, ts, "桌面")

	doJSON(t, http.MethodGet, ts.URL+"/v1/changes", nil, nil, http.StatusUnauthorized)
	doJSON(t, http.MethodGet, ts.URL+"/v1/changes", nil, bearer("wrong"), http.StatusUnauthorized)
	doJSON(t, http.MethodGet, ts.URL+"/v1/snapshot", nil, bearer("wrong"), http.StatusUnauthorized)
	doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
		syncwire.PushRequest{Ops: []syncwire.Op{}}, bearer("wrong"), http.StatusUnauthorized)

	// 管理接口没有管理员令牌时 401
	doJSON(t, http.MethodPost, ts.URL+"/v1/admin/pair-codes", nil, nil, http.StatusUnauthorized)
	doJSON(t, http.MethodPost, ts.URL+"/v1/admin/pair-codes", nil,
		map[string]string{"X-Admin-Token": "wrong"}, http.StatusUnauthorized)
	_ = token
}

// TestValidationRejectsBadOps 形状不对的变更直接 400，不能进库。
func TestValidationRejectsBadOps(t *testing.T) {
	_, ts := newTestServer(t)
	_, token := registerDevice(t, ts, "桌面")

	cases := []syncwire.Op{
		{OpID: "", Entity: syncwire.EntityTodo, UID: "x", Kind: syncwire.KindPatch},
		{OpID: "01", Entity: "note", UID: "x", Kind: syncwire.KindPatch},
		{OpID: "01", Entity: syncwire.EntityTodo, UID: "", Kind: syncwire.KindPatch},
		{OpID: "01", Entity: syncwire.EntityTodo, UID: "x", Kind: "upsert"},
	}
	for _, op := range cases {
		doJSON(t, http.MethodPost, ts.URL+"/v1/changes",
			syncwire.PushRequest{Ops: []syncwire.Op{op}}, bearer(token), http.StatusBadRequest)
	}

	health := doJSON(t, http.MethodGet, ts.URL+"/v1/health", nil, nil, http.StatusOK)
	if health["ops"] != float64(0) {
		t.Fatalf("非法变更不该入账，实际 %v 条", health["ops"])
	}
}

// TestPairRateLimited 同一 IP 连猜多次会被限流（而不是无限试）。
func TestPairRateLimited(t *testing.T) {
	_, ts := newTestServer(t)
	limited := false
	for i := 0; i < 12; i++ {
		resp, err := http.Post(ts.URL+"/v1/pair", "application/json",
			strings.NewReader(`{"code":"ZZZZZZZZ","device_name":"x"}`))
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("连续 12 次配对请求应触发限流")
	}
}

// TestAdminListDevices 管理接口能列出已登记设备（设置页要显示「有几台连着」）。
func TestAdminListDevices(t *testing.T) {
	_, ts := newTestServer(t)
	registerDevice(t, ts, "桌面")
	registerDevice(t, ts, "手机")

	out := doJSON(t, http.MethodGet, ts.URL+"/v1/admin/devices", nil, adminHeaders(), http.StatusOK)
	items, _ := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("应有 2 台设备，实际 %d", len(items))
	}
}
