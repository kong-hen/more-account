//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	modShell32        = syscall.NewLazyDLL("shell32.dll")
	procShellExecuteW = modShell32.NewProc("ShellExecuteW")
)

// shellExecuteOpen 使用 Windows 官方 ShellExecuteW API 打开网页链接或文件夹，
// 避免调用 rundll32 / cmd.exe 等命令行转发器引发杀软误报与控制台黑窗闪烁。
func shellExecuteOpen(target string) error {
	verbPtr, err := syscall.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	targetPtr, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	const swShowNormal = 1
	ret, _, callErr := procShellExecuteW.Call(
		0,
		uintptr(unsafe.Pointer(verbPtr)),
		uintptr(unsafe.Pointer(targetPtr)),
		0,
		0,
		swShowNormal,
	)
	// ShellExecuteW 返回值大于 32 表示调用成功
	if ret <= 32 {
		if callErr != nil && callErr != syscall.Errno(0) {
			return fmt.Errorf("打开目标失败 (code=%d): %w", ret, callErr)
		}
		return fmt.Errorf("打开目标失败 (code=%d)", ret)
	}
	return nil
}

func openExternalBrowserWithOS(safeURL string) error {
	return shellExecuteOpen(safeURL)
}

func openDirectoryWithOS(dir string) error {
	return shellExecuteOpen(dir)
}
