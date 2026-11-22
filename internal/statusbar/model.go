package statusbar

import (
	"fmt"
	"strings"
	"time"

	"more-account/internal/model"
	"more-account/internal/quota"
)

// CategoryLine 表示下拉菜单中单条模型分类配额信息行。
type CategoryLine struct {
	Key  string `json:"key"`
	Text string `json:"text"`
}

// AccountOption 表示「快速切换账号」子菜单中的单个账号项。
type AccountOption struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Active   bool   `json:"active"`
	Disabled bool   `json:"disabled"`
}

// Snapshot 是跨平台状态栏渲染的纯数据快照，JSON 序列化后直接交付原生菜单层渲染。
type Snapshot struct {
	Visible           bool            `json:"visible"`
	ButtonTitle       string          `json:"buttonTitle"`
	Tooltip           string          `json:"tooltip"`
	CurrentHeader     string          `json:"currentHeader"`
	StatusHint        string          `json:"statusHint,omitempty"`
	ErrorBanner       string          `json:"errorBanner,omitempty"`
	Categories        []CategoryLine  `json:"categories"`
	QuotaEmptyHint    string          `json:"quotaEmptyHint,omitempty"`
	Accounts          []AccountOption `json:"accounts"`
	CanRefreshCurrent bool            `json:"canRefreshCurrent"`
	CanRefreshAll     bool            `json:"canRefreshAll"`
	Refreshing        bool            `json:"refreshing"`
	Switching         bool            `json:"switching"`
	PrivacyMode       bool            `json:"privacyMode"`
}

// Callbacks 定义状态栏用户交互及原生窗口重开回调集合。
type Callbacks struct {
	BuildSnapshot    func() Snapshot
	OnShowWindow     func()
	OnSwitchAccount  func(accountID string)
	OnRefreshCurrent func()
	OnRefreshAll     func()
	OnTogglePrivacy  func()
	OnQuit           func()
	OnSystemWake     func()
}

// Controller 抽象跨平台状态栏、窗口后台钩子、休眠唤醒监听与防 App Nap 生命周期控制。
type Controller interface {
	Start(closeToBackground bool)
	SetCloseToBackground(enabled bool)
	SetSystemWakeCallback(fn func())
	SetPreventAppNap(enabled bool)
	Update(snap Snapshot)
	Stop()
}

// BuildInput 是构建状态栏渲染快照的纯输入参数。
type BuildInput struct {
	Accounts         []model.Account
	CurrentAccountID string
	Settings         model.AppSettings
	Refreshing       bool
	Switching        bool
	LastError        string
	Now              time.Time
}

func findAccountByID(accounts []model.Account, id string) (*model.Account, bool) {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return nil, false
	}
	for i := range accounts {
		if accounts[i].ID == trimmed {
			return &accounts[i], true
		}
	}
	return nil, false
}

func formatCategoryLine(c quota.CategorySummary, now time.Time) string {
	parts := make([]string, 0, 2)
	if c.FiveHour != nil {
		parts = append(parts, fmt.Sprintf("5h %d%%", c.FiveHour.Percentage))
	}
	if c.Weekly != nil {
		parts = append(parts, fmt.Sprintf("周 %d%%", c.Weekly.Percentage))
	}
	quotaText := strings.Join(parts, " / ")
	if quotaText == "" {
		quotaText = fmt.Sprintf("%d%%", c.Percentage)
	}
	countdown := quota.FormatResetCountdown(c.ResetTime, now)
	if countdown != "" && countdown != "—" {
		return fmt.Sprintf("  %s · %s (%s)", c.Label, quotaText, countdown)
	}
	return fmt.Sprintf("  %s · %s", c.Label, quotaText)
}

func formatAccountOptionTitle(acc model.Account, privacy bool, now time.Time) string {
	email := quota.MaskEmail(acc.Email, privacy)
	if email == "" {
		email = "未命名账号"
	}
	parts := []string{email}
	if tier := quota.TierLabel(acc); tier != "" {
		parts = append(parts, "["+tier+"]")
	}
	if quota.IsAccountAbnormal(acc) {
		parts = append(parts, "异常")
	} else if minPct := quota.MinQuotaPercentage(acc, now); minPct >= 0 {
		parts = append(parts, fmt.Sprintf("%d%%", minPct))
	} else {
		parts = append(parts, "--")
	}
	return strings.Join(parts, "  ")
}

func trimSingleLineError(raw string) string {
	clean := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(raw, "\r", " "), "\n", " "))
	runes := []rune(clean)
	if len(runes) > 48 {
		return string(runes[:48]) + "..."
	}
	return clean
}

// BuildSnapshot 根据当前账号列表、激活账号与设置生成确定性的状态栏渲染快照。
func BuildSnapshot(in BuildInput) Snapshot {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	mode := strings.TrimSpace(in.Settings.MacStatusBarMode)
	if mode == "" {
		mode = model.MacStatusBarIconQuota
	}

	snap := Snapshot{
		Visible:     mode != model.MacStatusBarOff,
		Tooltip:     "More Account",
		Categories:  make([]CategoryLine, 0, 4),
		Accounts:    make([]AccountOption, 0, len(in.Accounts)),
		Refreshing:  in.Refreshing,
		Switching:   in.Switching,
		PrivacyMode: in.Settings.PrivacyMode,
	}

	cur, hasCurrent := findAccountByID(in.Accounts, in.CurrentAccountID)
	if hasCurrent {
		maskedEmail := quota.MaskEmail(cur.Email, in.Settings.PrivacyMode)
		tier := quota.TierLabel(*cur)
		if tier != "" {
			snap.CurrentHeader = fmt.Sprintf("当前账号：%s [%s]", maskedEmail, tier)
		} else {
			snap.CurrentHeader = fmt.Sprintf("当前账号：%s", maskedEmail)
		}

		abnormal := quota.IsAccountAbnormal(*cur)
		minPct := quota.MinQuotaPercentage(*cur, now)

		if abnormal {
			snap.Tooltip = fmt.Sprintf("More Account · %s · 状态异常", maskedEmail)
			snap.QuotaEmptyHint = "  状态异常：请打开主窗口查看或处理"
			if mode == model.MacStatusBarIconQuota {
				snap.ButtonTitle = "!"
			}
		} else {
			if minPct >= 0 {
				snap.Tooltip = fmt.Sprintf("More Account · %s · 最低额度 %d%%", maskedEmail, minPct)
				if mode == model.MacStatusBarIconQuota {
					snap.ButtonTitle = fmt.Sprintf("%d%%", minPct)
				}
			} else {
				snap.Tooltip = fmt.Sprintf("More Account · %s", maskedEmail)
			}

			cats := quota.SummarizeAccountCategories(*cur, now)
			for _, c := range cats {
				snap.Categories = append(snap.Categories, CategoryLine{
					Key:  c.Key,
					Text: formatCategoryLine(c, now),
				})
			}
			if len(snap.Categories) == 0 {
				snap.QuotaEmptyHint = "  尚未拉取配额，可点击下方立即刷新"
			}
		}
	} else {
		snap.CurrentHeader = "当前未检测到使用中账号"
		if len(in.Accounts) == 0 {
			snap.QuotaEmptyHint = "  暂未导入任何反重力账号"
		} else {
			snap.QuotaEmptyHint = "  可从下方「快速切换账号」直接切号"
		}
	}

	switch {
	case in.Switching:
		snap.StatusHint = "正在切换账号..."
	case in.Refreshing:
		snap.StatusHint = "正在刷新配额..."
	}

	if errMsg := trimSingleLineError(in.LastError); errMsg != "" {
		snap.ErrorBanner = "上次操作失败：" + errMsg
	}

	for _, acc := range in.Accounts {
		isActive := hasCurrent && acc.ID == cur.ID
		snap.Accounts = append(snap.Accounts, AccountOption{
			ID:       acc.ID,
			Title:    formatAccountOptionTitle(acc, in.Settings.PrivacyMode, now),
			Active:   isActive,
			Disabled: in.Switching,
		})
	}

	snap.CanRefreshCurrent = hasCurrent && !in.Refreshing && !in.Switching
	snap.CanRefreshAll = len(in.Accounts) > 0 && !in.Refreshing && !in.Switching
	return snap
}
