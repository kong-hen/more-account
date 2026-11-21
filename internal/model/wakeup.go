package model

// WakeupSchedule 描述定时唤醒任务的调度规则与请求配置。
type WakeupSchedule struct {
	RepeatMode        string   `json:"repeatMode"` // daily | weekly | interval | crontab | quota_reset
	DailyTimes        []string `json:"dailyTimes,omitempty"`
	WeeklyDays        []int    `json:"weeklyDays,omitempty"` // 0=周日 .. 6=周六
	WeeklyTimes       []string `json:"weeklyTimes,omitempty"`
	IntervalHours     int      `json:"intervalHours,omitempty"`
	IntervalStartTime string   `json:"intervalStartTime,omitempty"`
	IntervalEndTime   string   `json:"intervalEndTime,omitempty"`
	Crontab           string   `json:"crontab,omitempty"`
	WakeOnReset       bool     `json:"wakeOnReset"`
	SelectedModels    []string `json:"selectedModels"`
	SelectedAccounts  []string `json:"selectedAccounts"`
	CustomPrompt      string   `json:"customPrompt,omitempty"`
	MaxOutputTokens   int      `json:"maxOutputTokens,omitempty"`
}

// WakeupTask 表示一条定时唤醒任务配置。
type WakeupTask struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	LastRunAt int64          `json:"lastRunAt,omitempty"`
	CreatedAt int64          `json:"createdAt"`
	Schedule  WakeupSchedule `json:"schedule"`
}

// WakeupState 持久化唤醒模块的总开关和全部任务列表。
type WakeupState struct {
	Enabled bool         `json:"enabled"`
	Tasks   []WakeupTask `json:"tasks"`
}

// WakeupHistoryItem 记录单次唤醒请求（手动测试或定时触发）的执行结果。
type WakeupHistoryItem struct {
	ID               string `json:"id"`
	Timestamp        int64  `json:"timestamp"`
	TaskID           string `json:"taskId,omitempty"`
	TaskName         string `json:"taskName,omitempty"`
	TriggerType      string `json:"triggerType"` // manual | scheduled
	AccountID        string `json:"accountId"`
	AccountEmail     string `json:"accountEmail"`
	Model            string `json:"model"`
	Prompt           string `json:"prompt"`
	Success          bool   `json:"success"`
	Reply            string `json:"reply,omitempty"`
	PromptTokens     int    `json:"promptTokens,omitempty"`
	CompletionTokens int    `json:"completionTokens,omitempty"`
	TotalTokens      int    `json:"totalTokens,omitempty"`
	DurationMs       int64  `json:"durationMs"`
	ErrorMessage     string `json:"errorMessage,omitempty"`
	ValidationURL    string `json:"validationUrl,omitempty"`
}

// WakeupTestRequest 表示前端手动发起一次立即唤醒测试的入参。
type WakeupTestRequest struct {
	ScopeID         string `json:"scopeId,omitempty"`
	AccountID       string `json:"accountId"`
	Model           string `json:"model"`
	Prompt          string `json:"prompt,omitempty"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
}
