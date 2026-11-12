package view

import (
	"testing"
)

// TestTickSyncRespectsAutoOff 「定时同步」关掉后，tick 不能再排下一轮、也不能起一轮。
//
// 这条开关的意义是「别偷偷联网」：连接与令牌都还在，但后台不再自己跑。
// 手动「立即同步」走 syncNow，不受这里影响（另有用例覆盖）。
func TestTickSyncRespectsAutoOff(t *testing.T) {
	a, _ := newTestApp(t)
	a.settings.SyncEnabled = true
	a.settings.SyncURL = "http://127.0.0.1:1" // 不会被真的请求到
	a.settings.SyncToken = "tok"
	a.settings.SyncAutoOff = true

	if d := a.tickSync(); d != 0 {
		t.Fatalf("关掉定时同步后不应排下一轮，实际返回 %v", d)
	}
	if !a.syncNextAt.IsZero() {
		t.Fatalf("关掉后 syncNextAt 应清零，实际 %v", a.syncNextAt)
	}
	if a.syncBusy {
		t.Fatal("关掉定时同步后不该起同步轮次")
	}
}

// TestTickSyncRunsWhenEnabled 打开时应当排下一轮（30 秒）。
//
// 只检查返回值与 syncNextAt：真的发请求需要网络，留给集成测试。
func TestTickSyncRunsWhenEnabled(t *testing.T) {
	a, _ := newTestApp(t)
	a.settings.SyncEnabled = true
	a.settings.SyncURL = "http://127.0.0.1:1"
	a.settings.SyncToken = "tok"

	if d := a.tickSync(); d <= 0 {
		t.Fatalf("开启时应排下一轮，实际返回 %v", d)
	}
	if a.syncNextAt.IsZero() {
		t.Fatal("开启后 syncNextAt 不应为零")
	}
}
