//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// AttachConsole 把标准输出接回启动它的控制台。
//
// Windows 的 GUI 子系统程序（mygo build 用 -H=windowsgui 链接）没有自己的控制台：
// 在 cmd/PowerShell 里执行 `app.exe --version` 时，fmt.Println 的输出会直接丢掉。
// 这里 AttachConsole(ATTACH_PARENT_PROCESS) 借用父进程的控制台，再把 stdout/stderr
// 重定向到 CONOUT$，命令行选项才有意义。
func AttachConsole() {
	const attachParentProcess = ^uint32(0) // (DWORD)-1
	kernel32 := windows.NewLazySystemDLL("kernel32.dll")
	proc := kernel32.NewProc("AttachConsole")
	if r, _, _ := proc.Call(uintptr(attachParentProcess)); r == 0 {
		return // 没有父控制台（双击启动）：保持原样，不报错
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout = f
		os.Stderr = f
	}
}
