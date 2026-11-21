package model

// AppSettings 保存用户全局偏好设置。
// Theme 默认固定为 "light"，不跟随系统暗色模式，仅在用户手动切换时变更为 "dark"。
type AppSettings struct {
	Theme                      string `json:"theme"`                        // light | dark
	PrivacyMode                bool   `json:"privacyMode"`                  // 隐私脱敏模式
	RuntimeTarget              string `json:"runtimeTarget"`                // desktop | ide | cli
	LaunchOnSwitch             bool   `json:"launchOnSwitch"`               // 切号后是否自动重启客户端
	LowQuotaThreshold          int    `json:"lowQuotaThreshold"`            // 低配额筛选阈值百分比（默认 20）
	AutoRefreshMinutes         int    `json:"autoRefreshMinutes"`           // 当前使用账号后台自动刷新间隔（分钟，默认 5；-1 表示关闭）
	InactiveAutoRefreshMinutes int    `json:"inactiveAutoRefreshMinutes"`   // 未使用账号后台自动刷新间隔（分钟，默认 60；-1 表示关闭）
	DesktopAppPath             string `json:"desktopAppPath,omitempty"`     // 自定义桌面版路径
	IdeAppPath                 string `json:"ideAppPath,omitempty"`         // 自定义 IDE 路径
	TokenUsageDataDir          string `json:"tokenUsageDataDir,omitempty"`  // 自定义 Token 用量扫描目录
	ProxyURL                   string `json:"proxyUrl,omitempty"`           // 可选 HTTP / HTTPS / SOCKS5 代理地址
	GitHubRepo                 string `json:"githubRepo,omitempty"`         // 自定义版本检查 GitHub 仓库（格式 owner/repo，留空使用默认仓库）
	AutoCheckUpdate            bool   `json:"autoCheckUpdate"`              // 启动时是否自动静默检查新版本
	OAuthClientID              string `json:"oauthClientId,omitempty"`      // 可选自定义 Google OAuth Client ID（仅存于本机 0600 配置文件）
	OAuthClientSecret          string `json:"oauthClientSecret,omitempty"`  // 可选自定义 Google OAuth Client Secret（仅存于本机 0600 配置文件）
}

// DefaultSettings 返回应用默认配置。
func DefaultSettings() AppSettings {
	return AppSettings{
		Theme:                      "light",
		PrivacyMode:                false,
		RuntimeTarget:              "desktop",
		LaunchOnSwitch:             true,
		LowQuotaThreshold:          20,
		AutoRefreshMinutes:         5,
		InactiveAutoRefreshMinutes: 60,
		AutoCheckUpdate:            true,
	}
}
