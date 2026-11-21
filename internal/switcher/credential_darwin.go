//go:build darwin

package switcher

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func writeOSSystemCredential(payloadJSON string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 使用 -U 原地更新凭据并保持既有访问控制策略，不传 -A/-T 以避免每次切号触发 Keychain 弹窗。
	// 通过 /usr/bin/security -i 从标准输入传入子命令，避免 OAuth 令牌明文暴露在进程命令行参数中。
	keychainValue := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(payloadJSON))
	stdinCmd := fmt.Sprintf("add-generic-password -U -s gemini -a antigravity -w %s\n", keychainValue)

	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(stdinCmd)
	output, err := cmd.CombinedOutput()
	trimmedOut := strings.TrimSpace(string(output))
	if err != nil || strings.Contains(trimmedOut, "security:") {
		return fmt.Errorf("写入 macOS Keychain 失败: %v (%s)", err, trimmedOut)
	}
	return nil
}

func readOSSystemCredential() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/usr/bin/security",
		"find-generic-password",
		"-s", "gemini",
		"-a", "antigravity",
		"-w",
	)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 44 {
			// 44 表示条目不存在
			return "", nil
		}
		return "", fmt.Errorf("读取 macOS Keychain 失败: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
