//go:build !windows

package gitx

import "os/exec"

// hideWindow 在非 Windows 平台上没有「控制台窗口」这回事，空实现即可。
func hideWindow(cmd *exec.Cmd) {}
