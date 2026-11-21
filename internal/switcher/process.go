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

// startDetached 启动外部进程并异步 Wait 回收子进程退出状态，避免产生僵尸进程。
func startDetached(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func(c *exec.Cmd) {
		_ = c.Wait()
	}(cmd)
	return nil
}

// stopClientProcesses 优雅关闭正在运行的 Antigravity 或 Antigravity IDE 客户端进程（支持自定义安装路径）。
func stopClientProcesses(target, customDesktop, customIDE string) error {
	var resolvedPath string
	if target == "ide" {
		resolvedPath = ResolveIDEAppPath(customIDE)
	} else {
		resolvedPath = ResolveDesktopAppPath(customDesktop)
	}

	switch runtime.GOOS {
	case "darwin":
		appName := "Antigravity"
		matchPattern := "/Applications/Antigravity.app"
		if target == "ide" {
			appName = "Antigravity IDE"
			matchPattern = "/Applications/Antigravity IDE.app"
		}
		if resolvedPath != "" {
			matchPattern = resolvedPath
			base := filepath.Base(resolvedPath)
			if strings.HasSuffix(strings.ToLower(base), ".app") {
				appName = strings.TrimSuffix(base, filepath.Ext(base))
			}
		}

		// 仅在目标客户端确实正在运行时才执行 AppleScript 退出，避免未安装或未启动时触发系统弹窗或误拉起应用。
		if !hasMatchingProcess(matchPattern) {
			return nil
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = exec.CommandContext(ctx, "osascript", "-e", fmt.Sprintf(`tell application %q to quit`, appName)).Run()
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
		if resolvedPath != "" {
			if base := filepath.Base(resolvedPath); base != "" {
				exeName = base
			}
		}
		_ = exec.Command("taskkill", "/IM", exeName, "/T").Run()
		time.Sleep(800 * time.Millisecond)
		return nil

	default:
		// Linux 通过 /proc/<pid>/exe 精确匹配目标可执行文件并排除自身 PID，杜绝 pkill -f 误杀路径或参数含同名子串的无关进程。
		expectedBase := "antigravity"
		if target == "ide" {
			expectedBase = "antigravity-ide"
		}
		resolvedReal := ""
		if resolvedPath != "" {
			if r, err := filepath.EvalSymlinks(resolvedPath); err == nil {
				resolvedReal = r
			} else {
				resolvedReal = resolvedPath
			}
			if b := filepath.Base(resolvedReal); b != "" {
				expectedBase = b
			}
		}
		stopLinuxProcessesByExe(resolvedReal, expectedBase)
		return nil
	}
}

func stopLinuxProcessesByExe(expectedRealPath, expectedBase string) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	selfPID := os.Getpid()
	killedAny := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 1 || pid == selfPID {
			continue
		}
		exePath, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil {
			continue
		}
		exeClean := strings.TrimSuffix(exePath, " (deleted)")
		matched := false
		if expectedRealPath != "" && exeClean == expectedRealPath {
			matched = true
		} else if filepath.Base(exeClean) == expectedBase {
			matched = true
		}
		if !matched {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			if sigErr := proc.Signal(syscall.SIGTERM); sigErr == nil {
				killedAny = true
			}
		}
	}
	if killedAny {
		time.Sleep(800 * time.Millisecond)
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
	rawAgyPath := filepath.Join(home, ".gemini", "bin", "agy")
	agyPath, err := filepath.EvalSymlinks(rawAgyPath)
	if err != nil {
		return nil
	}

	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return nil
	}

	selfPID := os.Getpid()
	var hubs []runningHub
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "--hub") || !strings.Contains(line, "antigravity") {
			continue
		}
		spIdx := strings.IndexAny(line, " \t")
		if spIdx <= 0 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(line[:spIdx]))
		if err != nil || pid <= 0 || pid == selfPID {
			continue
		}
		rest := strings.TrimSpace(line[spIdx+1:])

		// 优先按已知完整路径前缀切分命令行，避免用户主目录含空格时 strings.Fields 拆坏可执行路径。
		var argsPart string
		switch {
		case strings.HasPrefix(rest, agyPath+" "):
			argsPart = strings.TrimSpace(rest[len(agyPath):])
		case strings.HasPrefix(rest, rawAgyPath+" "):
			argsPart = strings.TrimSpace(rest[len(rawAgyPath):])
		default:
			fields := strings.Fields(rest)
			if len(fields) < 2 {
				continue
			}
			cmdExe, _ := filepath.EvalSymlinks(fields[0])
			if cmdExe != agyPath && filepath.Base(fields[0]) != "agy" {
				continue
			}
			argsPart = strings.Join(fields[1:], " ")
		}

		args := strings.Fields(argsPart)
		if !isAntigravityHubArgs(args) {
			continue
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			hubs = append(hubs, runningHub{
				pid:  pid,
				exe:  agyPath,
				args: args,
			})
		}
	}

	if len(hubs) > 0 {
		// 等待被停止的 hub 进程真正退出（最长 2 秒），减少竞态覆盖凭据的概率。
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			anyAlive := false
			for _, h := range hubs {
				if isPIDAlive(h.pid) {
					anyAlive = true
					break
				}
			}
			if !anyAlive {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	return hubs
}

func isPIDAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
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
		_ = startDetached(cmd)
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
	default:
		cmd := exec.Command(appPath)
		if err := startDetached(cmd); err != nil {
			return fmt.Errorf("启动应用失败: %w", err)
		}
		return nil
	}
}
