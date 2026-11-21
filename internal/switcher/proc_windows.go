//go:build windows

package switcher

import (
	"os/exec"
	"syscall"
)

// configureHiddenSubprocess 在 Windows GUI 模式下隐藏命令行子进程（如 taskkill）的黑色控制台闪窗。
func configureHiddenSubprocess(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
}
