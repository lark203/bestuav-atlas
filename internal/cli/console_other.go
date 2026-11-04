//go:build !windows

package cli

// AttachConsole 在非 Windows 平台是空操作：终端程序本来就带控制台。
func AttachConsole() {}
