package model

// SwitchHistoryItem 记录一次账号切换的执行明细。
type SwitchHistoryItem struct {
	ID           string `json:"id"`
	Timestamp    int64  `json:"timestamp"`
	AccountID    string `json:"accountId"`
	TargetEmail  string `json:"targetEmail"`
	TargetClient string `json:"targetClient"` // desktop | ide | cli
	Launched     bool   `json:"launched"`
	Success      bool   `json:"success"`
	DurationMs   int64  `json:"durationMs"`
	ErrorStage   string `json:"errorStage,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}
