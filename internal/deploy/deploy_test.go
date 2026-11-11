package deploy

import (
	"strings"
	"testing"
)

// TestValidateReportsMissingFields 表单漏填时要给出能直接显示的中文提示，而不是连不上再猜。
func TestValidateReportsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		t    Target
		want string
	}{
		{"缺主机", Target{User: "u", Key: "k", Dir: "d", Src: "s"}, "服务器地址"},
		{"缺用户", Target{Host: "h", Key: "k", Dir: "d", Src: "s"}, "SSH 用户"},
		{"缺私钥", Target{Host: "h", User: "u", Dir: "d", Src: "s"}, "私钥路径"},
		{"缺远端目录", Target{Host: "h", User: "u", Key: "k", Src: "s"}, "远端目录"},
		{"缺源码目录", Target{Host: "h", User: "u", Key: "k", Dir: "d"}, "源码目录"},
	}
	for _, c := range cases {
		err := c.t.Validate()
		if err == nil {
			t.Fatalf("%s：应当报错", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s：错误里应提到 %q，实际 %q", c.name, c.want, err)
		}
	}
}

// TestComposeFileUsesRemoteIDs 远端 compose 的 uid 不能写死：
// bind mount 的 data 目录属于部署用户，uid 不一致容器会一直重启。
func TestComposeFileUsesRemoteIDs(t *testing.T) {
	got := ComposeFile(1002, 1003)
	for _, want := range []string{`user: "1002:1003"`, `"17420:7420"`, "atlas-sync.db", "restart: unless-stopped"} {
		if !strings.Contains(got, want) {
			t.Errorf("compose 里缺少 %q", want)
		}
	}
	if strings.Contains(got, "127.0.0.1:17420") {
		t.Error("部署用的是另一台机器做入口，端口必须监听所有网卡（127.0.0.1 会 502）")
	}
}

// TestTailShortens 长输出只留末尾，避免错误里塞进整段编译日志。
func TestTailShortens(t *testing.T) {
	long := strings.Repeat("x", 5000)
	got := tail(long, 100)
	if len(got) > 101+3 {
		t.Errorf("tail 没有截断：%d 字节", len(got))
	}
	if !strings.HasPrefix(got, "…") {
		t.Error("截断后应有省略号前缀")
	}
}
