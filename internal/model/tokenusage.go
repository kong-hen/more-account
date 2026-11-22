package model

// ExactTokenTotals 表示来自 API 精确遥测的 Token 计数。
// 严格遵循不变量：TotalTokens = InputTokens + OutputTokens + ThinkingTokens + CacheReadTokens + CacheWriteTokens，
// 其中 OutputTokens 仅代表纯回复输出，不重复计入 ThinkingTokens。
type ExactTokenTotals struct {
	InputTokens      uint64 `json:"inputTokens"`
	OutputTokens     uint64 `json:"outputTokens"`
	ThinkingTokens   uint64 `json:"thinkingTokens"`
	CacheReadTokens  uint64 `json:"cacheReadTokens"`
	CacheWriteTokens uint64 `json:"cacheWriteTokens"`
	TotalTokens      uint64 `json:"totalTokens"`
}

// Add 将另一组精确统计累加到当前值。
func (e *ExactTokenTotals) Add(other ExactTokenTotals) {
	e.InputTokens += other.InputTokens
	e.OutputTokens += other.OutputTokens
	e.ThinkingTokens += other.ThinkingTokens
	e.CacheReadTokens += other.CacheReadTokens
	e.CacheWriteTokens += other.CacheWriteTokens
	e.TotalTokens += other.TotalTokens
}

// EstimatedTokenTotals 表示无精确遥测步骤的启发式估算 Token。
type EstimatedTokenTotals struct {
	InputTokens        uint64 `json:"inputTokens"`
	OutputTokens       uint64 `json:"outputTokens"`
	TotalTokens        uint64 `json:"totalTokens"`
	EstimatedTurnCount int    `json:"estimatedTurnCount"`
}

// TurnUsageRecord 表示单对话中某一步骤的 Token 消耗记录。
type TurnUsageRecord struct {
	StepIndex        int                  `json:"stepIndex"`
	Timestamp        string               `json:"timestamp,omitempty"`
	ModelID          string               `json:"modelId"`
	ModelDisplayName string               `json:"modelDisplayName"`
	APIProvider      string               `json:"apiProvider"`
	IsExact          bool                 `json:"isExact"`
	Source           string               `json:"source"`
	Status           int                  `json:"status"`
	Exact            ExactTokenTotals     `json:"exact"`
	Estimated        EstimatedTokenTotals `json:"estimated"`
	MessageID        string               `json:"messageId,omitempty"`
}

// ConversationModelSlice 表示单对话内按模型聚合的消耗切片。
type ConversationModelSlice struct {
	ModelID              string           `json:"modelId"`
	ModelDisplayName     string           `json:"modelDisplayName"`
	APIProvider          string           `json:"apiProvider"`
	TurnCount            int              `json:"turnCount"`
	Exact                ExactTokenTotals `json:"exact"`
	EstimatedTotalTokens uint64           `json:"estimatedTotalTokens"`
}

// ConversationSummaryItem 表示一条对话的汇总消耗数据。
type ConversationSummaryItem struct {
	ConversationID          string               `json:"conversationId"`
	Title                   string               `json:"title"`
	DisplayTitle            string               `json:"displayTitle"`
	Preview                 string               `json:"preview"`
	Kind                    string               `json:"kind"` // main | subagent | swarm
	AgentName               string               `json:"agentName,omitempty"`
	ParentConversationID    string               `json:"parentConversationId,omitempty"`
	WorkspacePath           string               `json:"workspacePath,omitempty"`
	WorkspaceName           string               `json:"workspaceName"`
	CreatedAt               string               `json:"createdAt,omitempty"`
	LastModifiedTime        string               `json:"lastModifiedTime"`
	StepCount               int                  `json:"stepCount"`
	TurnCount               int                  `json:"turnCount"`
	ExactTurnCount          int                  `json:"exactTurnCount"`
	EstimatedTurnCount      int                  `json:"estimatedTurnCount"`
	PrimaryModelID          string               `json:"primaryModelId"`
	PrimaryModelDisplayName string               `json:"primaryModelDisplayName"`
	Models                  []string             `json:"models"`
	DataSource              string               `json:"dataSource"`
	Exact                   ExactTokenTotals     `json:"exact"`
	Estimated               EstimatedTokenTotals `json:"estimated"`
}

// ConversationDetail 包含单条对话的摘要、模型分解及逐步消耗明细。
type ConversationDetail struct {
	Summary        ConversationSummaryItem  `json:"summary"`
	ModelBreakdown []ConversationModelSlice `json:"modelBreakdown"`
	Turns          []TurnUsageRecord        `json:"turns"`
}

// UsageTrendBucket 表示按天/周/月分桶的时间趋势点。
type UsageTrendBucket struct {
	Key                  string           `json:"key"`
	Label                string           `json:"label"`
	TurnCount            int              `json:"turnCount"`
	ConversationCount    int              `json:"conversationCount"`
	Exact                ExactTokenTotals `json:"exact"`
	EstimatedTotalTokens uint64           `json:"estimatedTotalTokens"`
}

// ModelUsageSummary 表示按模型聚合的全局消耗分布。
type ModelUsageSummary struct {
	ModelID              string           `json:"modelId"`
	ModelDisplayName     string           `json:"modelDisplayName"`
	APIProvider          string           `json:"apiProvider"`
	TurnCount            int              `json:"turnCount"`
	ConversationCount    int              `json:"conversationCount"`
	Exact                ExactTokenTotals `json:"exact"`
	EstimatedTotalTokens uint64           `json:"estimatedTotalTokens"`
}

// WorkspaceUsageSummary 表示按工作区聚合的消耗分布。
type WorkspaceUsageSummary struct {
	WorkspaceName        string           `json:"workspaceName"`
	WorkspacePath        string           `json:"workspacePath,omitempty"`
	ConversationCount    int              `json:"conversationCount"`
	TurnCount            int              `json:"turnCount"`
	Exact                ExactTokenTotals `json:"exact"`
	EstimatedTotalTokens uint64           `json:"estimatedTotalTokens"`
}

// TokenUsageQuery 表示前端查询 Token 用量面板的过滤参数。
type TokenUsageQuery struct {
	Preset      string `json:"preset"`      // today | 7d | 30d | month | all
	Granularity string `json:"granularity"` // day | week | month
	Kind        string `json:"kind"`        // "" | main | subagent | swarm
	Model       string `json:"model"`
	Workspace   string `json:"workspace"`
	Search      string `json:"search"`
	Sort        string `json:"sort"`  // lastModifiedTime | totalTokens | turnCount
	Order       string `json:"order"` // desc | asc
	Page        int    `json:"page"`
	PageSize    int    `json:"pageSize"`
}

// WorkspaceFilterOption 表示工作区筛选下拉选项，支持同名不同路径目录按路径区分。
type WorkspaceFilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
}

// TokenUsageOverview 表示顶部总览统计与多维图表数据。
type TokenUsageOverview struct {
	Exact                     ExactTokenTotals        `json:"exact"`
	Estimated                 EstimatedTokenTotals    `json:"estimated"`
	ConversationCount         int                     `json:"conversationCount"`
	TurnCount                 int                     `json:"turnCount"`
	ExactTurnCount            int                     `json:"exactTurnCount"`
	Trend                     []UsageTrendBucket      `json:"trend"`
	Models                    []ModelUsageSummary     `json:"models"`
	Workspaces                []WorkspaceUsageSummary `json:"workspaces"`
	AvailableModels           []string                `json:"availableModels"`
	AvailableModelNames       map[string]string       `json:"availableModelNames,omitempty"`
	AvailableWorkspace        []string                `json:"availableWorkspaces"`
	AvailableWorkspaceOptions []WorkspaceFilterOption `json:"availableWorkspaceOptions,omitempty"`
	ScanDurationMs            int64                   `json:"scanDurationMs"`
	ScannedFiles              int                     `json:"scannedFiles"`
	CachedFiles               int                     `json:"cachedFiles"`
	SkippedErrors             int                     `json:"skippedErrors"`
	LastSyncedAt              string                  `json:"lastSyncedAt"`
}

// PaginatedConversations 表示分页后的对话消耗列表。
type PaginatedConversations struct {
	Items      []ConversationSummaryItem `json:"items"`
	Total      int                       `json:"total"`
	Page       int                       `json:"page"`
	PageSize   int                       `json:"pageSize"`
	TotalPages int                       `json:"totalPages"`
}

// TokenUsageSettings 描述 Token 用量扫描的数据目录状态。
type TokenUsageSettings struct {
	CustomDataDir    string `json:"customDataDir"`
	EffectiveDataDir string `json:"effectiveDataDir"`
	DefaultDataDir   string `json:"defaultDataDir"`
	DataDirExists    bool   `json:"dataDirExists"`
}

// TokenUsageBundle 是前端 Token 用量页面一次查询获得的完整响应包。
type TokenUsageBundle struct {
	Overview      TokenUsageOverview     `json:"overview"`
	Conversations PaginatedConversations `json:"conversations"`
	Settings      TokenUsageSettings     `json:"settings"`
}
