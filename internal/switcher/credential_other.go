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
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			if err := cmd.Run(); err != nil {
				return "", nil
			}
			return strings.TrimSpace(stdout.String()), nil
		}
	}
	return "", nil
}
