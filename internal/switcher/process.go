package switcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type runningHub struct {
	pid  int
	exe  string
	args []string
}

// stopClientProcesses 优雅关闭正在运行的 Antigravity 或 Antigravity IDE 客户端进程。
func stopClientProcesses(target string) error {
	switch runtime.GOOS {
	case "darwin":
		appName := "Antigravity"
		matchPattern := "/Applications/Antigravity.app"
		if target == "ide" {
			appName = "Antigravity IDE"
			matchPattern = "/Applications/Antigravity IDE.app"
		}
		// 先尝试通过 AppleScript 优雅退出
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = exec.CommandContext(ctx, "osascript", "-e", fmt.Sprintf(`tell application "%s" to quit`, appName)).Run()
		cancel()

		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if !hasMatchingProcess(matchPattern) {
				return nil
			}
			time.Sleep(250 * time.Millisecond)
		}
		// 若仍未退出，发送 SIGTERM
		_ = exec.Command("pkill", "-TERM", "-f", matchPattern).Run()
		time.Sleep(800 * time.Millisecond)
		return nil

	case "windows":
		exeName := "Antigravity.exe"
		if target == "ide" {
			exeName = "Antigravity IDE.exe"
		}
		_ = exec.Command("taskkill", "/IM", exeName, "/T").Run()
		time.Sleep(800 * time.Millisecond)
		return nil

	default:
		pattern := "antigravity"
		if target == "ide" {
			pattern = "antigravity-ide"
		}
		_ = exec.Command("pkill", "-TERM", "-f", pattern).Run()
		time.Sleep(800 * time.Millisecond)
		return nil
	}
}

func hasMatchingProcess(pattern string) bool {
	cmd := exec.Command("pgrep", "-f", pattern)
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// snapshotAndStopMacHubs 在 macOS 上协同停止共享凭据的 agy --hub --app_data_dir antigravity 进程，
// 避免其内存缓存覆盖刚刚写入的新账号凭据。
func snapshotAndStopMacHubs() []runningHub {
	if runtime.GOOS != "darwin" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	agyPath, err := filepath.EvalSymlinks(filepath.Join(home, ".gemini", "bin", "agy"))
	if err != nil {
		return nil
	}

	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}

	var hubs []runningHub
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "--hub") || !strings.Contains(line, "antigravity") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		cmdExe, _ := filepath.EvalSymlinks(fields[1])
		if cmdExe != agyPath && filepath.Base(fields[1]) != "agy" {
			continue
		}
		if !isAntigravityHubArgs(fields[2:]) {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			hubs = append(hubs, runningHub{
				pid:  pid,
				exe:  agyPath,
				args: fields[2:],
			})
		}
	}

	if len(hubs) > 0 {
		time.Sleep(600 * time.Millisecond)
	}
	return hubs
}

func isAntigravityHubArgs(args []string) bool {
	hasHub := false
	matchedDir := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--hub" {
			hasHub = true
		} else if arg == "--app_data_dir" && i+1 < len(args) && args[i+1] == "antigravity" {
			matchedDir = true
		} else if arg == "--app_data_dir=antigravity" {
			matchedDir = true
		}
	}
	return hasHub && matchedDir
}

func restartMacHubs(hubs []runningHub) {
	if runtime.GOOS != "darwin" {
		return
	}
	for _, h := range hubs {
		cmd := exec.Command(h.exe, h.args...)
		cmd.Stdin = nil
		cmd.Stdout = nil
		cmd.Stderr = nil
		_ = cmd.Start()
	}
}

// launchClientApp 重新启动指定的反重力客户端。
func launchClientApp(target, customDesktop, customIDE string) error {
	var appPath string
	if target == "ide" {
		appPath = ResolveIDEAppPath(customIDE)
		if appPath == "" {
			return fmt.Errorf("未找到 Antigravity IDE 安装路径，请在设置中手动指定")
		}
	} else {
		appPath = ResolveDesktopAppPath(customDesktop)
		if appPath == "" {
			return fmt.Errorf("未找到 Antigravity 桌面版安装路径，请在设置中手动指定")
		}
	}

	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("open", "-a", appPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("启动应用失败: %v (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	case "windows":
		cmd := exec.Command(appPath)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("启动应用失败: %w", err)
		}
		return nil
	default:
		cmd := exec.Command(appPath)
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("启动应用失败: %w", err)
		}
		return nil
	}
}
