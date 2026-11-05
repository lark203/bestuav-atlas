//go:build windows

package gitx

import (
	"os/exec"
	"syscall"
)

// createNoWindow 是 CreateProcess 的 CREATE_NO_WINDOW 标志。
//
// 桌面应用里每跑一次 git 都会闪一个命令行黑框（哪怕只跑 status）：Windows 上
// exec.Command 默认会为控制台程序新建一个控制台窗口。隐藏窗口 + CREATE_NO_WINDOW
// 一起用，才能真正做到「后台跑、界面无感」。
const createNoWindow = 0x08000000

// hideWindow 让子进程不弹控制台窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
