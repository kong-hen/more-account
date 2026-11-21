package switcher

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ClientInstallInfo 描述本机探测到的反重力客户端安装状态。
type ClientInstallInfo struct {
	DesktopPath string `json:"desktopPath"`
	IdePath     string `json:"idePath"`
	DesktopOK   bool   `json:"desktopOk"`
	IdeOK       bool   `json:"ideOk"`
	CliDirOK    bool   `json:"cliDirOk"`
}

// DefaultDesktopStateDBPath 返回旧版 Antigravity 桌面端 state.vscdb 路径。
func DefaultDesktopStateDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Antigravity", "User", "globalStorage", "state.vscdb")
	case "windows":
		appData := os.Getenv("APPDATA")
		return filepath.Join(appData, "Antigravity", "User", "globalStorage", "state.vscdb")
	default:
		return filepath.Join(home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb")
	}
}

// DefaultIDEStateDBPath 返回 Antigravity IDE 默认 state.vscdb 路径。
func DefaultIDEStateDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Antigravity IDE", "User", "globalStorage", "state.vscdb")
	case "windows":
		appData := os.Getenv("APPDATA")
		return filepath.Join(appData, "Antigravity IDE", "User", "globalStorage", "state.vscdb")
	default:
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

// ResolveDesktopAppPath 查找 Antigravity 桌面版应用路径（优先用户自定义配置）。
func ResolveDesktopAppPath(customPath string) string {
	if p := strings.TrimSpace(customPath); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Antigravity.app",
			filepath.Join(home, "Applications", "Antigravity.app"),
		}
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("ProgramFiles")
		candidates = []string{
			filepath.Join(localAppData, "Programs", "Antigravity", "Antigravity.exe"),
			filepath.Join(programFiles, "Antigravity", "Antigravity.exe"),
		}
	default:
		candidates = []string{
			"/usr/bin/antigravity",
			"/usr/local/bin/antigravity",
			filepath.Join(home, ".local", "bin", "antigravity"),
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// ResolveIDEAppPath 查找 Antigravity IDE 应用路径（优先用户自定义配置）。
func ResolveIDEAppPath(customPath string) string {
	if p := strings.TrimSpace(customPath); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	home, _ := os.UserHomeDir()
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Antigravity IDE.app",
			filepath.Join(home, "Applications", "Antigravity IDE.app"),
		}
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		programFiles := os.Getenv("ProgramFiles")
		candidates = []string{
			filepath.Join(localAppData, "Programs", "Antigravity IDE", "Antigravity IDE.exe"),
			filepath.Join(programFiles, "Antigravity IDE", "Antigravity IDE.exe"),
		}
	default:
		candidates = []string{
			"/usr/bin/antigravity-ide",
			"/usr/local/bin/antigravity-ide",
			filepath.Join(home, ".local", "bin", "antigravity-ide"),
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
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
