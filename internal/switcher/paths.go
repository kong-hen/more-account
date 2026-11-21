package switcher

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"more-account/internal/storage"
)

// ClientInstallInfo 描述本机探测到的反重力客户端安装状态。
type ClientInstallInfo struct {
	DesktopPath string `json:"desktopPath"`
	IdePath     string `json:"idePath"`
	DesktopOK   bool   `json:"desktopOk"`
	IdeOK       bool   `json:"ideOk"`
	CliDirOK    bool   `json:"cliDirOk"`
}

func windowsAppDataDir(home string) string {
	if appData := strings.TrimSpace(os.Getenv("APPDATA")); appData != "" {
		return appData
	}
	if home != "" {
		return filepath.Join(home, "AppData", "Roaming")
	}
	return ""
}

func windowsLocalAppDataDir(home string) string {
	if localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); localAppData != "" {
		return localAppData
	}
	if home != "" {
		return filepath.Join(home, "AppData", "Local")
	}
	return ""
}

// isValidClientAppPath 校验客户端路径确实为可执行应用包（macOS .app）或常规可执行文件，拒绝普通目录（如 "/" 或 "~"）防止进程匹配越界。
func isValidClientAppPath(raw string) bool {
	return isValidClientAppPathForOS(raw, runtime.GOOS)
}

func isValidClientAppPathForOS(raw, goos string) bool {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return false
	}
	st, err := os.Stat(cleaned)
	if err != nil {
		return false
	}
	if st.IsDir() {
		if goos == "darwin" && strings.HasSuffix(strings.ToLower(cleaned), ".app") {
			macosDir, mErr := os.Stat(filepath.Join(cleaned, "Contents", "MacOS"))
			return mErr == nil && macosDir.IsDir()
		}
		return false
	}
	if goos == "darwin" || goos == "linux" {
		return st.Mode()&0111 != 0
	}
	return true
}

// DefaultDesktopStateDBPath 返回旧版 Antigravity 桌面端 state.vscdb 路径。
func DefaultDesktopStateDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	switch runtime.GOOS {
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage", "state.vscdb")
	case "windows":
		appData := windowsAppDataDir(home)
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Antigravity", "User", "globalStorage", "state.vscdb")
	default:
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb")
	}
}

// DefaultIDEStateDBPath 返回 Antigravity IDE 默认 state.vscdb 路径。
func DefaultIDEStateDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	switch runtime.GOOS {
	case "darwin":
		if home == "" {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Antigravity IDE", "User", "globalStorage", "state.vscdb")
	case "windows":
		appData := windowsAppDataDir(home)
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Antigravity IDE", "User", "globalStorage", "state.vscdb")
	default:
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config", "Antigravity IDE", "User", "globalStorage", "state.vscdb")
	}
}

// AllExistingStateDBPaths 返回本机存在的全部 Antigravity state.vscdb 路径（兼容桌面旧版与 IDE）。
func AllExistingStateDBPaths() []string {
	candidates := []string{DefaultIDEStateDBPath(), DefaultDesktopStateDBPath()}
	var existing []string
	seen := make(map[string]struct{})
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			existing = append(existing, p)
		}
	}
	return existing
}

// ResolveDesktopAppPath 查找 Antigravity 桌面版应用路径（优先用户自定义配置，支持 ~/ 展开与合法性校验）。
func ResolveDesktopAppPath(customPath string) string {
	if p := storage.ExpandHomePath(customPath); p != "" {
		if isValidClientAppPath(p) {
			return filepath.Clean(p)
		}
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, "/Applications/Antigravity.app")
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "Applications", "Antigravity.app"))
		}
	case "windows":
		if localAppData := windowsLocalAppDataDir(home); localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "Programs", "Antigravity", "Antigravity.exe"))
		}
		if programFiles := strings.TrimSpace(os.Getenv("ProgramFiles")); programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Antigravity", "Antigravity.exe"))
		}
	default:
		candidates = append(candidates, "/usr/bin/antigravity", "/usr/local/bin/antigravity")
		if home != "" {
			candidates = append(candidates, filepath.Join(home, ".local", "bin", "antigravity"))
		}
	}
	for _, c := range candidates {
		if isValidClientAppPath(c) {
			return filepath.Clean(c)
		}
	}
	return ""
}

// ResolveIDEAppPath 查找 Antigravity IDE 应用路径（优先用户自定义配置，支持 ~/ 展开与合法性校验）。
func ResolveIDEAppPath(customPath string) string {
	if p := storage.ExpandHomePath(customPath); p != "" {
		if isValidClientAppPath(p) {
			return filepath.Clean(p)
		}
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, "/Applications/Antigravity IDE.app")
		if home != "" {
			candidates = append(candidates, filepath.Join(home, "Applications", "Antigravity IDE.app"))
		}
	case "windows":
		if localAppData := windowsLocalAppDataDir(home); localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "Programs", "Antigravity IDE", "Antigravity IDE.exe"))
		}
		if programFiles := strings.TrimSpace(os.Getenv("ProgramFiles")); programFiles != "" {
			candidates = append(candidates, filepath.Join(programFiles, "Antigravity IDE", "Antigravity IDE.exe"))
		}
	default:
		candidates = append(candidates, "/usr/bin/antigravity-ide", "/usr/local/bin/antigravity-ide")
		if home != "" {
			candidates = append(candidates, filepath.Join(home, ".local", "bin", "antigravity-ide"))
		}
	}
	for _, c := range candidates {
		if isValidClientAppPath(c) {
			return filepath.Clean(c)
		}
	}
	return ""
}

// DetectInstalledClients 返回本机反重力桌面版、IDE 和 CLI 目录探测结果。
func DetectInstalledClients(customDesktop, customIDE string) ClientInstallInfo {
	desktop := ResolveDesktopAppPath(customDesktop)
	ide := ResolveIDEAppPath(customIDE)
	home, _ := os.UserHomeDir()
	cliOK := false
	if home != "" {
		if st, err := os.Stat(filepath.Join(home, ".gemini", "antigravity-cli")); err == nil && st.IsDir() {
			cliOK = true
		}
	}
	return ClientInstallInfo{
		DesktopPath: desktop,
		IdePath:     ide,
		DesktopOK:   desktop != "",
		IdeOK:       ide != "",
		CliDirOK:    cliOK,
	}
}
