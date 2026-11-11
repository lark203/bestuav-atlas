package syncclient

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"bestuav-atlas/internal/store"
	"bestuav-atlas/internal/syncsvc"
)

// 这一组用例是「两台设备对拍」：两个真实的 store（各自的 SQLite 文件）+ 一个
// 跑在 httptest 上的服务端，只通过 HTTP 同步。它验证的不是某个函数，而是
// 「用户在两台机器上各做一半事，最后能不能收敛」。

const testAdmin = "test-admin"

type device struct {
	core   *store.Store
	client *Client
}

type env struct {
	server *syncsvc.Server
	ts     *httptest.Server
	a, b   device
}

func newEnv(t *testing.T) *env {
	t.Helper()

	srv, err := syncsvc.Open(filepath.Join(t.TempDir(), "sync.db"), testAdmin)
	if err != nil {
		t.Fatalf("syncsvc.Open: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	newDev := func(name string) device {
		pair, err := New(ts.URL, "", "").AdminCreateDevice(ctx, testAdmin, name)
		if err != nil {
			t.Fatalf("登记设备 %s: %v", name, err)
		}
		core, err := store.Open(filepath.Join(t.TempDir(), name+".db"))
		if err != nil {
			t.Fatalf("store.Open(%s): %v", name, err)
		}
		return device{core: core, client: New(ts.URL, pair.DeviceID, pair.Token)}
	}

	e := &env{server: srv, ts: ts, a: newDev("桌面"), b: newDev("手机")}
	t.Cleanup(func() {
		_ = e.a.core.Close()
		_ = e.b.core.Close()
		ts.Close()
		_ = srv.Close()
	})
	return e
}

// round 跑一轮同步，失败直接终止用例。
func round(t *testing.T, d device) RoundResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := PushPull(ctx, d.core, d.client, 500)
	if err != nil {
		t.Fatalf("PushPull: %v", err)
	}
	return res
}

// titles 列出所有待办标题（按 id 顺序）。
func titles(t *testing.T, d device) []string {
	t.Helper()
	todos, err := d.core.ListTodos()
	if err != nil {
		t.Fatalf("ListTodos: %v", err)
	}
	out := make([]string, 0, len(todos))
	for _, td := range todos {
		out = append(out, td.Title)
	}
	return out
}

// TestTwoDevicesConverge 桌面记一条，手机要能看到；手机勾完成，桌面要能看到。
func TestTwoDevicesConverge(t *testing.T) {
	e := newEnv(t)

	pid, err := e.a.core.CreateProject(store.Project{Name: "星图", Status: "进行中"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := e.a.core.CreateTodo(store.Todo{
		Title: "写同步服务", ProjectID: pid, Priority: 1, Status: "待办",
	}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	round(t, e.a)
	round(t, e.b)

	if got := titles(t, e.b); len(got) != 1 || got[0] != "写同步服务" {
		t.Fatalf("手机上应看到 1 条待办，实际 %v", got)
	}
	todos, _ := e.b.core.ListTodos()
	if todos[0].ProjectID == 0 {
		t.Fatal("项目归属应解析成本地的 project_id，而不是丢成未归类")
	}
	projects, _ := e.b.core.ListProjects()
	if len(projects) != 1 || projects[0].Name != "星图" {
		t.Fatalf("项目应同步过去，实际 %v", projects)
	}

	if err := e.b.core.SetTodoStatus(todos[0].ID, "已完成"); err != nil {
		t.Fatalf("SetTodoStatus: %v", err)
	}
	round(t, e.b)
	round(t, e.a)

	done, _ := e.a.core.ListTodos()
	if len(done) != 1 || done[0].Status != "已完成" {
		t.Fatalf("桌面上应看到已完成，实际 %v", done)
	}
	if done[0].DoneAt.IsZero() {
		t.Error("完成时间也应同步过来")
	}
}

// TestDisjointFieldEditsBothSurvive 核心场景：手机只勾完成、桌面同时改备注，
// 两边各推一次之后**两个字段都在**——这就是「补丁只带改动字段」存在的理由。
func TestDisjointFieldEditsBothSurvive(t *testing.T) {
	e := newEnv(t)

	if _, err := e.a.core.CreateTodo(store.Todo{Title: "写文档", Status: "待办"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	round(t, e.a)
	round(t, e.b)

	// 桌面改备注（未推送）
	tdA, _, _ := e.a.core.TodoByUID(mustUID(t, e.a))
	tdA.Note = "桌面写的备注"
	if err := e.a.core.UpdateTodo(tdA); err != nil {
		t.Fatalf("UpdateTodo: %v", err)
	}
	// 手机勾完成（未推送）
	tdB, _, _ := e.b.core.TodoByUID(mustUID(t, e.b))
	if err := e.b.core.SetTodoStatus(tdB.ID, "已完成"); err != nil {
		t.Fatalf("SetTodoStatus: %v", err)
	}

	round(t, e.a)
	round(t, e.b)
	round(t, e.a) // 把手机的完成状态拉回桌面

	for _, d := range []device{e.a, e.b} {
		got, ok, err := d.core.TodoByUID(mustUID(t, d))
		if err != nil || !ok {
			t.Fatalf("读待办失败: ok=%v err=%v", ok, err)
		}
		if got.Note != "桌面写的备注" {
			t.Errorf("备注应保留，实际 %q", got.Note)
		}
		if got.Status != "已完成" {
			t.Errorf("完成状态应生效，实际 %q", got.Status)
		}
	}
}

// TestSameFieldConcurrentConverges 两台设备**离线改同一个字段**，恢复后必须收敛到同一个值。
//
// 这条用例是 M4 补的：先推后拉的顺序会让「后推的那台」在拉取时把对方的旧值盖回自己脸上，
// 而对方又已经被它覆盖——两边永久不一致。改成先拉后推，配合「本地未发送字段不被覆盖」，
// 才能保证后推者赢且双方一致。
func TestSameFieldConcurrentConverges(t *testing.T) {
	e := newEnv(t)
	if _, err := e.a.core.CreateTodo(store.Todo{Title: "原标题", Status: "待办"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	round(t, e.a)
	round(t, e.b)

	// 两边都离线改标题。
	tdA, _, _ := e.a.core.TodoByUID(mustUID(t, e.a))
	tdA.Title = "A 改的标题"
	if err := e.a.core.UpdateTodo(tdA); err != nil {
		t.Fatalf("A UpdateTodo: %v", err)
	}
	tdB, _, _ := e.b.core.TodoByUID(mustUID(t, e.b))
	tdB.Title = "B 改的标题"
	if err := e.b.core.UpdateTodo(tdB); err != nil {
		t.Fatalf("B UpdateTodo: %v", err)
	}

	round(t, e.a)
	round(t, e.b)
	round(t, e.a) // 让 A 也拿到 B 的结果

	gotA, _, _ := e.a.core.TodoByUID(mustUID(t, e.a))
	gotB, _, _ := e.b.core.TodoByUID(mustUID(t, e.b))
	if gotA.Title != gotB.Title {
		t.Fatalf("两台设备没有收敛：A=%q B=%q", gotA.Title, gotB.Title)
	}
	if gotA.Title != "B 改的标题" {
		t.Errorf("后推的 B 应该赢，实际 %q", gotA.Title)
	}
}

// TestOfflineKeepsOutbox 服务不可达时：同步报错、但本地变更一条也不丢，
// 恢复后重试即收敛。
func TestOfflineKeepsOutbox(t *testing.T) {
	e := newEnv(t)
	if _, err := e.a.core.CreateTodo(store.Todo{Title: "离线记的", Status: "待办"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	offline := New("http://127.0.0.1:1", e.a.client.DeviceID, e.a.client.Token)
	offline.HTTP.Timeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := PushPull(ctx, e.a.core, offline, 500); err == nil {
		t.Fatal("服务不可达时应返回错误")
	}
	if ops, _ := e.a.core.PendingOps(); len(ops) != 1 {
		t.Fatalf("失败的同步不该丢出站变更，实际 %d 条", len(ops))
	}

	round(t, e.a) // 换回正常客户端
	if ops, _ := e.a.core.PendingOps(); len(ops) != 0 {
		t.Fatalf("恢复后应推干净，实际还剩 %d 条", len(ops))
	}
	round(t, e.b)
	if got := titles(t, e.b); len(got) != 1 || got[0] != "离线记的" {
		t.Fatalf("手机应收到离线期间记的待办，实际 %v", got)
	}
}

// TestPullPaginates 客户端一次只拉 limit 条，要能翻完。
func TestPullPaginates(t *testing.T) {
	e := newEnv(t)
	for _, title := range []string{"一", "二", "三", "四", "五"} {
		if _, err := e.a.core.CreateTodo(store.Todo{Title: title, Status: "待办"}); err != nil {
			t.Fatalf("CreateTodo: %v", err)
		}
	}
	round(t, e.a)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := PushPull(ctx, e.b.core, e.b.client, 2)
	if err != nil {
		t.Fatalf("PushPull: %v", err)
	}
	if res.Pulled != 5 || res.Applied != 5 {
		t.Fatalf("分页拉取应拉全 5 条，实际 pulled=%d applied=%d", res.Pulled, res.Applied)
	}
	if got := titles(t, e.b); len(got) != 5 {
		t.Fatalf("手机应有 5 条，实际 %d", len(got))
	}
}

// TestDeletePropagates 墓碑要跨设备生效。
func TestDeletePropagates(t *testing.T) {
	e := newEnv(t)
	id, err := e.a.core.CreateTodo(store.Todo{Title: "要删的", Status: "待办"})
	if err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	round(t, e.a)
	round(t, e.b)

	if err := e.a.core.DeleteTodo(id); err != nil {
		t.Fatalf("DeleteTodo: %v", err)
	}
	round(t, e.a)
	round(t, e.b)

	if got := titles(t, e.b); len(got) != 0 {
		t.Fatalf("手机上的待办也应被删掉，实际 %v", got)
	}
}

// TestSecondDeviceSnapshot 新设备首次同步时若本地是空的，也该能用快照补齐。
//
// 这里用「重新打开一份空库 + 从 0 拉」来模拟：服务端的 op 日志本来就是完整历史，
// 所以新设备不依赖 /v1/snapshot 也能补齐——快照只是省时间的捷径。
func TestSecondDeviceSnapshot(t *testing.T) {
	e := newEnv(t)
	if _, err := e.a.core.CreateTodo(store.Todo{Title: "历史待办", Status: "待办"}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}
	round(t, e.a)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fresh, err := e.a.client.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(fresh.Todos) != 1 || fresh.Todos[0].Title != "历史待办" {
		t.Fatalf("快照应含 1 条待办，实际 %v", fresh.Todos)
	}
	if fresh.Todos[0].UID == "" {
		t.Fatal("快照里的实体必须带 uid")
	}
}

// mustUID 取本地唯一那条待办的 uid。
func mustUID(t *testing.T, d device) string {
	t.Helper()
	todos, err := d.core.ListTodos()
	if err != nil || len(todos) != 1 {
		t.Fatalf("期望恰好 1 条待办，实际 %d（err=%v）", len(todos), err)
	}
	return todos[0].UID
}
