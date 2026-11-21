package model

// TokenData 保存 Antigravity 账号的 OAuth 认证凭据。
type TokenData struct {
	AccessToken     string `json:"accessToken"`
	RefreshToken    string `json:"refreshToken"`
	ExpiresIn       int64  `json:"expiresIn"`
	ExpiryTimestamp int64  `json:"expiryTimestamp"`
	TokenType       string `json:"tokenType"`
	Email           string `json:"email,omitempty"`
	ProjectID       string `json:"projectId,omitempty"`
	IsGcpTos        *bool  `json:"isGcpTos,omitempty"`
	OAuthClientKey  string `json:"oauthClientKey,omitempty"`
	IDToken         string `json:"idToken,omitempty"`
	SessionID       string `json:"sessionId,omitempty"`
}

// ModelQuota 表示单个模型或配额桶的剩余额度与重置时间。
type ModelQuota struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Percentage  int    `json:"percentage"`
	ResetTime   string `json:"resetTime,omitempty"`
}

// QuotaBucket 表示云端 retrieveUserQuotaSummary 返回的共享配额窗口桶（如 5h 与周额度）。
type QuotaBucket struct {
	BucketID    string `json:"bucketId"`
	DisplayName string `json:"displayName,omitempty"`
	Percentage  int    `json:"percentage"`
	ResetTime   string `json:"resetTime,omitempty"`
	Exhausted   bool   `json:"exhausted,omitempty"`
}

// CreditInfo 表示付费订阅账号的 AI 积分明细。
type CreditInfo struct {
	CreditType                  string `json:"creditType"`
	CreditAmount                string `json:"creditAmount,omitempty"`
	MinimumCreditAmountForUsage string `json:"minimumCreditAmountForUsage,omitempty"`
}

// QuotaData 表示某次配额查询得到的完整快照。
type QuotaData struct {
	Models           []ModelQuota  `json:"models"`
	SummaryBuckets   []QuotaBucket `json:"summaryBuckets,omitempty"`
	LastUpdated      int64         `json:"lastUpdated"`
	IsForbidden      bool          `json:"isForbidden"`
	SubscriptionTier string        `json:"subscriptionTier,omitempty"`
	Credits          []CreditInfo  `json:"credits,omitempty"`
	TierID           string        `json:"tierId,omitempty"`
	IsGcpTos         *bool         `json:"isGcpTos,omitempty"`
	ProjectID        string        `json:"projectId,omitempty"`
}

// QuotaErrorInfo 记录最近一次刷新配额或令牌失败时的诊断信息。
type QuotaErrorInfo struct {
	Code          int    `json:"code,omitempty"`
	Message       string `json:"message"`
	Reason        string `json:"reason,omitempty"`
	ValidationURL string `json:"validationUrl,omitempty"`
	Timestamp     int64  `json:"timestamp"`
}

// Account 表示单个反重力账号实体。
// 已移除原有标签与辅助邮箱等冗余字段，仅保留自建分组归属与核心凭据备注。
type Account struct {
	ID              string          `json:"id"`
	Email           string          `json:"email"`
	Name            string          `json:"name,omitempty"`
	GroupID         string          `json:"groupId,omitempty"`
	Notes           string          `json:"notes,omitempty"`
	LoginAccount    string          `json:"loginAccount,omitempty"`
	AccountPassword string          `json:"accountPassword,omitempty"`
	TwoFactorSecret string          `json:"twoFactorSecret,omitempty"`
	Token           TokenData       `json:"token"`
	Quota           *QuotaData      `json:"quota,omitempty"`
	Disabled        bool            `json:"disabled"`
	DisabledReason  string          `json:"disabledReason,omitempty"`
	DisabledAt      int64           `json:"disabledAt,omitempty"`
	QuotaError      *QuotaErrorInfo `json:"quotaError,omitempty"`
	SortOrder       int             `json:"sortOrder"`
	CreatedAt       int64           `json:"createdAt"`
	LastUsed        int64           `json:"lastUsed"`
}

// AccountDetailsUpdate 表示编辑账号备注、登录账号、登录密码、2FA 密钥与所属分组的入参。
type AccountDetailsUpdate struct {
	Notes           string `json:"notes"`
	LoginAccount    string `json:"loginAccount"`
	AccountPassword string `json:"accountPassword"`
	TwoFactorSecret string `json:"twoFactorSecret"`
	GroupID         string `json:"groupId"`
}

// RefreshStats 表示批量刷新全部账号配额的统计结果。
type RefreshStats struct {
	Total   int      `json:"total"`
	Success int      `json:"success"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}
