//go:build !darwin

package switcher

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func writeOSSystemCredential(payloadJSON string) error {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("secret-tool"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "secret-tool", "store", "--label=gemini", "service", "gemini", "username", "antigravity")
			cmd.Stdin = strings.NewReader(payloadJSON)
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				return fmt.Errorf("Linux secret-tool 写入失败: %v (%s)", runErr, strings.TrimSpace(string(out)))
			}
			return nil
		}
		// 若无 secret-tool，由上层同步写入 ~/.gemini/jetski-standalone-oauth-token 回退文件生效。
		return nil
	}
	// Windows 端如未集成原生 CredWriteW，由桌面回退文件承载（必要时可扩展为 Win32 syscall）。
	return nil
}

func readOSSystemCredential() (string, error) {
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("secret-tool"); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "secret-tool", "lookup", "service", "gemini", "username", "antigravity")
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				errMsg := strings.TrimSpace(stderr.String())
				// secret-tool 未查到条目时通常返回退出码 1 且 stderr 为空；若有 stderr 输出则属于真实故障（如 keyring 锁定/D-Bus 异常）。
				if errMsg == "" {
					if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
						return "", nil
					}
				}
				return "", fmt.Errorf("Linux secret-tool 读取失败: %v (%s)", err, errMsg)
			}
			return strings.TrimSpace(stdout.String()), nil
		}
	}
	return "", nil
}
