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
		matchTarget := "/Applications/Antigravity.app"
		if target == "ide" {
			appName = "Antigravity IDE"
			matchTarget = "/Applications/Antigravity IDE.app"
		}
		if resolvedPath != "" {
			matchTarget = resolvedPath
			base := filepath.Base(resolvedPath)
			if strings.HasSuffix(strings.ToLower(base), ".app") {
				appName = strings.TrimSuffix(base, filepath.Ext(base))
			} else if base != "" {
				appName = base
			}
		}

		// 仅在目标客户端确实正在运行时才执行退出动作，避免未安装或未启动时触发系统弹窗。
		if len(findMacClientPIDs(matchTarget)) == 0 {
			return nil
		}

		if strings.HasSuffix(strings.ToLower(matchTarget), ".app") {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = exec.CommandContext(ctx, "osascript", "-e", fmt.Sprintf(`tell application %q to quit`, appName)).Run()
			cancel()
		} else {
			signalOwnedPIDs(findMacClientPIDs(matchTarget), syscall.SIGTERM)
		}

		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if len(findMacClientPIDs(matchTarget)) == 0 {
				return nil
			}
			time.Sleep(250 * time.Millisecond)
		}
		// 若仍未退出，仅向当前用户精确匹配出的客户端进程发送 SIGTERM（绝不调用未转义的 pkill -f）。
		if remaining := findMacClientPIDs(matchTarget); len(remaining) > 0 {
			signalOwnedPIDs(remaining, syscall.SIGTERM)
			time.Sleep(800 * time.Millisecond)
		}
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
		killCmd := exec.Command("taskkill", "/IM", exeName, "/T")
		configureHiddenSubprocess(killCmd)
		_ = killCmd.Run()
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

// findMacClientPIDs 枚举当前用户（ps -xo）进程并按可执行路径前缀精确匹配目标客户端，排除当前进程自身。
func findMacClientPIDs(appOrExePath string) []int {
	cleanTarget := filepath.Clean(strings.TrimSpace(appOrExePath))
	if cleanTarget == "" || cleanTarget == "/" || cleanTarget == "." {
		return nil
	}
	evalTarget := ""
	if r, err := filepath.EvalSymlinks(cleanTarget); err == nil {
		evalTarget = filepath.Clean(r)
	}

	out, err := exec.Command("ps", "-xo", "pid=,command=").Output()
	if err != nil {
		return nil
	}

	selfPID := os.Getpid()
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		pid, cmdLine, ok := parsePSLine(line)
		if !ok || pid == selfPID {
			continue
		}
		if matchMacProcessCommand(cmdLine, cleanTarget, evalTarget) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// matchMacProcessCommand 判断 ps 输出的完整命令行是否以目标 .app 包内可执行文件或指定二进制作为启动程序，
// 采用字面量前缀匹配，彻底免疫正则元字符注入与编辑器/脚本参数误匹配。
func matchMacProcessCommand(cmdLine, cleanTarget, evalTarget string) bool {
	trimmed := strings.TrimSpace(cmdLine)
	if trimmed == "" {
		return false
	}
	candidates := []string{cleanTarget}
	if evalTarget != "" && evalTarget != cleanTarget {
		candidates = append(candidates, evalTarget)
	}
	for _, target := range candidates {
		if target == "" || target == "/" || target == "." {
			continue
		}
		if strings.HasSuffix(strings.ToLower(target), ".app") {
			prefix := target + "/Contents/"
			if strings.HasPrefix(trimmed, prefix) {
				return true
			}
		} else {
			if trimmed == target || strings.HasPrefix(trimmed, target+" ") || strings.HasPrefix(trimmed, target+"\t") {
				return true
			}
		}
	}
	return false
}

func parsePSLine(line string) (int, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return 0, "", false
	}
	spIdx := strings.IndexAny(trimmed, " \t")
	if spIdx <= 0 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(trimmed[:spIdx]))
	if err != nil || pid <= 1 {
		return 0, "", false
	}
	return pid, strings.TrimSpace(trimmed[spIdx+1:]), true
}

func signalOwnedPIDs(pids []int, sig syscall.Signal) {
	for _, pid := range pids {
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Signal(sig)
		}
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

// snapshotAndStopMacHubs 在 macOS 上协同停止当前用户下共享凭据的 agy --hub --app_data_dir antigravity 进程，
// 避免其内存缓存覆盖刚刚写入的新账号凭据。仅使用 ps -xo（限定当前 UID）并在确认信号投递成功后才纳入重启列表。
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

	out, err := exec.Command("ps", "-xo", "pid=,command=").Output()
	if err != nil {
		return nil
	}

	selfPID := os.Getpid()
	var hubs []runningHub
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "--hub") || !strings.Contains(line, "antigravity") {
			continue
		}
		pid, rest, ok := parsePSLine(line)
		if !ok || pid == selfPID {
			continue
		}

		// 严格限定必须是当前用户 ~/.gemini/bin/agy（或其真实符号链接目标）启动的进程。
		var argsPart string
		switch {
		case strings.HasPrefix(rest, agyPath+" "):
			argsPart = strings.TrimSpace(rest[len(agyPath):])
		case strings.HasPrefix(rest, rawAgyPath+" "):
			argsPart = strings.TrimSpace(rest[len(rawAgyPath):])
		default:
			continue
		}

		args := splitCommandArgs(argsPart)
		if !isAntigravityHubArgs(args) {
			continue
		}
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			continue
		}
		if sigErr := proc.Signal(syscall.SIGTERM); sigErr != nil {
			// 无权限或已退出进程绝不记入待重启列表
			continue
		}
		hubs = append(hubs, runningHub{
			pid:  pid,
			exe:  agyPath,
			args: args,
		})
	}

	if len(hubs) > 0 {
		// 等待被停止的 hub 进程真正退出（最长 2.5 秒），减少竞态覆盖凭据的概率。
		deadline := time.Now().Add(2500 * time.Millisecond)
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

// splitCommandArgs 解析命令行参数，支持单双引号与反斜杠转义空格，同时还原未加引号的常见路径参数。
func splitCommandArgs(raw string) []string {
	var args []string
	var cur strings.Builder
	inQuote := rune(0)
	escaped := false
	tokenStarted := false

	for _, r := range raw {
		if escaped {
			cur.WriteRune(r)
			escaped = false
			tokenStarted = true
			continue
		}
		if r == '\\' && inQuote != '\'' {
			escaped = true
			tokenStarted = true
			continue
		}
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
			} else {
				cur.WriteRune(r)
			}
			tokenStarted = true
			continue
		}
		if r == '\'' || r == '"' {
			inQuote = r
			tokenStarted = true
			continue
		}
		if r == ' ' || r == '\t' {
			if tokenStarted {
				args = append(args, cur.String())
				cur.Reset()
				tokenStarted = false
			}
			continue
		}
		cur.WriteRune(r)
		tokenStarted = true
	}
	if tokenStarted {
		args = append(args, cur.String())
	}
	return args
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
		// 若原 hub 进程尚未退出，稍等片刻；若仍存活则跳过拉起，杜绝双实例冲突。
		if isPIDAlive(h.pid) {
			time.Sleep(400 * time.Millisecond)
			if isPIDAlive(h.pid) {
				continue
			}
		}
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
			return fmt.Errorf("未找到有效的 Antigravity IDE 安装路径，请在设置中手动指定")
		}
	} else {
		appPath = ResolveDesktopAppPath(customDesktop)
		if appPath == "" {
			return fmt.Errorf("未找到有效的 Antigravity 桌面版安装路径，请在设置中手动指定")
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
