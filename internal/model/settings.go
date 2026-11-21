package model

// AppSettings 保存用户全局偏好设置。
// Theme 默认固定为 "light"，不跟随系统暗色模式，仅在用户手动切换时变更为 "dark"。
type AppSettings struct {
	Theme                      string `json:"theme"`                      // light | dark
	PrivacyMode                bool   `json:"privacyMode"`                // 隐私脱敏模式
	RuntimeTarget              string `json:"runtimeTarget"`              // desktop | ide | cli
	LaunchOnSwitch             bool   `json:"launchOnSwitch"`             // 切号后是否自动重启客户端
	LowQuotaThreshold          int    `json:"lowQuotaThreshold"`          // 低配额筛选阈值百分比（默认 20）
	AutoRefreshMinutes         int    `json:"autoRefreshMinutes"`         // 当前使用账号后台自动刷新间隔（分钟，默认 5；-1 表示关闭）
	InactiveAutoRefreshMinutes int    `json:"inactiveAutoRefreshMinutes"` // 未使用账号后台自动刷新间隔（分钟，默认 60；-1 表示关闭）
	DesktopAppPath             string `json:"desktopAppPath,omitempty"`
	IdeAppPath                 string `json:"ideAppPath,omitempty"`
	TokenUsageDataDir          string `json:"tokenUsageDataDir,omitempty"`
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
	}
}
