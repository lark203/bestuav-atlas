package view

import (
	"testing"
	"time"
)

// 设置页「同步」分组的渲染用例：这一页是用户唯一能看见同步状态的地方，
// 它渲染不出来（空指针、缺文案）就等于功能不存在。

// TestSyncSettingsTab 未连接时该显示引导与两个输入框。
func TestSyncSettingsTab(t *testing.T) {
	a, _ := newTestApp(t)
	a.pageStat(settingsPageID).data[settingsGroupKey] = settingsGroupSync

	tt := renderPage(t, a, "settings")
	for _, want := range []string{"同步服务", "服务地址", "管理员令牌", "未连接", "手机配对", "生成配对码"} {
		if !tt.HasText(want) {
			t.Errorf("同步设置页没有渲染出 %q", want)
		}
	}
}

// TestSyncSettingsShowsPairingCode 生成配对码后，码、地址、复制按钮都要出现。
func TestSyncSettingsShowsPairingCode(t *testing.T) {
	a, _ := newTestApp(t)
	a.pageStat(settingsPageID).data[settingsGroupKey] = settingsGroupSync

	a.settings.SyncURL = "http://127.0.0.1:7420"
	a.settings.SyncDeviceID = "dev-1234567890abcdef"
	a.settings.SyncToken = "test-token"
	a.settings.SyncEnabled = true
	a.syncStatus = "已同步：没有新变更"
	a.syncCode = "ABCD2345"
	a.syncCodeExpire = time.Now().Add(5 * time.Minute)
	a.syncPairText = "atlas://pair?server=http://127.0.0.1:7420&code=ABCD2345"
	// 把下一轮排在很远的未来：渲染用例不该真的去连 127.0.0.1:7420。
	a.syncNextAt = time.Now().Add(time.Hour)

	tt := renderPage(t, a, "settings")
	for _, want := range []string{"ABCD2345", "复制配对串", "立即同步", "生成配对码", "断开", "已同步：没有新变更"} {
		if !tt.HasText(want) {
			t.Errorf("配对码界面没有渲染出 %q", want)
		}
	}
}

// TestSyncStatusLinePrefersLastError 失败状态优先显示，且用危险色（不能被「已连接」盖掉）。
func TestSyncStatusLinePrefersLastError(t *testing.T) {
	a, _ := newTestApp(t)
	a.pageStat(settingsPageID).data[settingsGroupKey] = settingsGroupSync
	a.settings.SyncURL = "http://127.0.0.1:7420"
	a.settings.SyncToken = "test-token"
	a.settings.SyncEnabled = true
	a.syncStatus = "同步失败：请求失败"
	a.syncNextAt = time.Now().Add(time.Hour)

	tt := renderPage(t, a, "settings")
	if !tt.HasText("同步失败：请求失败") {
		t.Error("失败原因应显示在设置页上，而不是只弹一次 toast 就消失")
	}
}
