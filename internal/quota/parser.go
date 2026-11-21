package quota

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"more-account/internal/model"
)

const (
	CloudCodeDailyBaseURL          = "https://daily-cloudcode-pa.googleapis.com"
	CloudCodeProdBaseURL           = "https://cloudcode-pa.googleapis.com"
	LoadCodeAssistPath             = "v1internal:loadCodeAssist"
	OnboardUserPath                = "v1internal:onboardUser"
	FetchAvailableModelsPath       = "v1internal:fetchAvailableModels"
	RetrieveUserQuotaSummaryPath   = "v1internal:retrieveUserQuotaSummary"
	DefaultCloudCodeIDEVersion     = "2.25.0"
	DefaultCloudCodeCL             = "1003475615"
	DefaultGoogleAPINodeClientVer  = "10.3.0"
	DefaultGoogAPIClientNodeVer    = "22.21.1"
)

// CloudCodeContext 决定 CloudCode 请求的目标域名与项目上下文。
type CloudCodeContext struct {
	PreferredProjectID string
	IsGcpTos           bool
}

// CloudCodeContextFromToken 从 TokenData 提取路由上下文。
func CloudCodeContextFromToken(tok model.TokenData) CloudCodeContext {
	isGcp := false
	if tok.IsGcpTos != nil {
		isGcp = *tok.IsGcpTos
	}
	return CloudCodeContext{
		PreferredProjectID: validCloudCodeProjectID(tok.ProjectID),
		IsGcpTos:           isGcp,
	}
}

func validCloudCodeProjectID(projectID string) string {
	trimmed := strings.TrimSpace(projectID)
	if trimmed == "" || trimmed == "aicode-consumers" {
		return ""
	}
	return trimmed
}

// ResolveCloudCodeBaseURL 按官方优先级选择 CloudCode API 域名：
// 环境变量覆盖 > GCP ToS 且含有效项目走 prod > 默认 daily。
func ResolveCloudCodeBaseURL(ctx CloudCodeContext) string {
	if override := strings.TrimSpace(os.Getenv("ANTIGRAVITY_CLOUD_CODE_URL_OVERRIDE")); override != "" {
		return strings.TrimRight(override, "/")
	}
	if ctx.IsGcpTos && validCloudCodeProjectID(ctx.PreferredProjectID) != "" {
		return CloudCodeProdBaseURL
	}
	return CloudCodeDailyBaseURL
}

// NormalizeSubscriptionTier 将原始 Tier ID（如 g1-pro-tier / g1-ultra-tier / free-tier）规范为 FREE / PRO / ULTRA。
func NormalizeSubscriptionTier(tierID string) string {
	lower := strings.ToLower(strings.TrimSpace(tierID))
	if lower == "" {
		return ""
	}
	switch {
	case strings.Contains(lower, "ultra"):
		return "ULTRA"
	case strings.Contains(lower, "pro") || strings.Contains(lower, "standard") || strings.Contains(lower, "enterprise"):
		return "PRO"
	case strings.Contains(lower, "free") || strings.Contains(lower, "legacy"):
		return "FREE"
	default:
		return strings.ToUpper(strings.TrimSpace(tierID))
	}
}

// ParseGoogleAPIError 解析 Google API 返回的 JSON 错误体，提取错误原因与验证链接。
func ParseGoogleAPIError(status int, text string) model.QuotaErrorInfo {
	msg := strings.TrimSpace(text)
	if msg == "" {
		msg = fmt.Sprintf("API 返回状态码 %d", status)
	}
	var reason string
	var validationURL string

	var root map[string]any
	if err := json.Unmarshal([]byte(text), &root); err == nil {
		if errObj, ok := root["error"].(map[string]any); ok {
			if m, ok := errObj["message"].(string); ok && strings.TrimSpace(m) != "" {
				msg = strings.TrimSpace(m)
			}
			if details, ok := errObj["details"].([]any); ok {
				for _, item := range details {
					dm, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if reason == "" {
						if r, ok := dm["reason"].(string); ok && strings.TrimSpace(r) != "" {
							reason = strings.TrimSpace(r)
						}
					}
					if validationURL == "" {
						if meta, ok := dm["metadata"].(map[string]any); ok {
							if u, ok := meta["validation_url"].(string); ok && strings.TrimSpace(u) != "" {
								validationURL = strings.TrimSpace(u)
							}
						}
					}
				}
			}
		}
	}

	if reason == "" {
		switch {
		case strings.Contains(msg, "Verify your account"):
			reason = "VALIDATION_REQUIRED"
		case strings.Contains(msg, "Subscription required"):
			reason = "SUBSCRIPTION_REQUIRED"
		}
	}

	return model.QuotaErrorInfo{
		Code:          status,
		Message:       msg,
		Reason:        reason,
		ValidationURL: validationURL,
		Timestamp:     time.Now().Unix(),
	}
}

type rawModelInfo struct {
	DisplayName string        `json:"displayName"`
	QuotaInfo   *rawQuotaInfo `json:"quotaInfo"`
}

type rawQuotaInfo struct {
	RemainingFraction *float64 `json:"remainingFraction"`
	ResetTime         string   `json:"resetTime"`
}

type rawAgentModelGroup struct {
	ModelIDs []string `json:"modelIds"`
}

type rawAgentModelSort struct {
	DisplayName string               `json:"displayName"`
	Groups      []rawAgentModelGroup `json:"groups"`
}

type rawModelsPayload struct {
	AgentModelSorts []rawAgentModelSort     `json:"agentModelSorts"`
	Models          map[string]rawModelInfo `json:"models"`
}

type rawModelsResponse struct {
	Payload         *rawModelsPayload       `json:"payload"`
	AgentModelSorts []rawAgentModelSort     `json:"agentModelSorts"`
	Models          map[string]rawModelInfo `json:"models"`
	QuotaSummary    map[string]any          `json:"quota_summary"`
}

// isExcludedNonAntigravityModel 在服务端未返回 agentModelSorts 时的兜底过滤：
// 排除已下线或非反重力客户端的模型（如 Gemini 2.5 Pro、Gemini 3 Flash、Flash Image、Flash Lite 等）。
func isExcludedNonAntigravityModel(name, displayName string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.ReplaceAll(s, ".", "-")
		s = strings.ReplaceAll(s, " ", "-")
		return s
	}
	n := norm(name)
	d := norm(displayName)
	for _, target := range []string{
		"gemini-2-5-pro",
		"gemini-3-1-flash-image",
		"gemini-3-1-flash-lite",
		"gemini-3-5-flash-lite",
	} {
		if strings.Contains(n, target) || strings.Contains(d, target) {
			return true
		}
	}
	// 精确排除 gemini-3-flash（保留 gemini-3.8-flash 等其他版本）
	for _, candidate := range []string{n, d} {
		if candidate == "gemini-3-flash" || strings.HasPrefix(candidate, "gemini-3-flash-") || strings.HasPrefix(candidate, "gemini-3-flash(") {
			return true
		}
	}
	return false
}

// isTrackedModel 过滤保留 Antigravity 核心模型系列（同时匹配原始 ID 与 displayName，兼容 MODEL_PLACEHOLDER_* 占位模型）。
func isTrackedModel(name, displayName string) bool {
	if isExcludedNonAntigravityModel(name, displayName) {
		return false
	}
	combined := strings.ToLower(name + " " + displayName)
	return strings.Contains(combined, "gemini") ||
		strings.Contains(combined, "claude") ||
		strings.Contains(combined, "argon") ||
		strings.Contains(combined, "carbon") ||
		strings.Contains(combined, "hydrogen") ||
		strings.Contains(combined, "neon") ||
		strings.Contains(combined, "gpt")
}

func extractModelsMap(resp rawModelsResponse) map[string]rawModelInfo {
	if resp.Payload != nil && len(resp.Payload.Models) > 0 {
		return resp.Payload.Models
	}
	return resp.Models
}

// extractSelectableModelIDs 提取服务端 agentModelSorts 下发的官方反重力客户端可选模型顺序。
// CloudCode 的 models 映射表会混入已下线和内部模型，官方客户端选择器严格以 agentModelSorts 为准。
func extractSelectableModelIDs(resp rawModelsResponse, entries map[string]rawModelInfo) []string {
	sorts := resp.AgentModelSorts
	if resp.Payload != nil && len(resp.Payload.AgentModelSorts) > 0 {
		sorts = resp.Payload.AgentModelSorts
	}
	if len(sorts) == 0 {
		return nil
	}
	ordered := make([]string, 0)
	seen := make(map[string]struct{})
	for _, s := range sorts {
		for _, g := range s.Groups {
			for _, rawID := range g.ModelIDs {
				id := strings.TrimSpace(rawID)
				if id == "" {
					continue
				}
				if _, ok := entries[id]; !ok {
					continue
				}
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				ordered = append(ordered, id)
			}
		}
	}
	return ordered
}

func fractionToPercent(frac float64) int {
	pct := int(math.Round(frac * 100.0))
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

func toModelQuota(name string, info rawModelInfo) model.ModelQuota {
	pct := 0
	if info.QuotaInfo != nil && info.QuotaInfo.RemainingFraction != nil {
		pct = fractionToPercent(*info.QuotaInfo.RemainingFraction)
	}
	resetTime := ""
	if info.QuotaInfo != nil {
		resetTime = strings.TrimSpace(info.QuotaInfo.ResetTime)
	}
	return model.ModelQuota{
		Name:        name,
		DisplayName: strings.TrimSpace(info.DisplayName),
		Percentage:  pct,
		ResetTime:   resetTime,
	}
}

// parseQuotaSummaryBuckets 解析 retrieveUserQuotaSummary 返回的 groups[].buckets[]（兼容 payload 包装与顶层 buckets），
// 独立存放为 SummaryBuckets（包含 5h 与周额度窗口），不混入模型明细列表。
func parseQuotaSummaryBuckets(summary map[string]any) []model.QuotaBucket {
	if len(summary) == 0 {
		return nil
	}
	root := summary
	if payload, ok := summary["payload"].(map[string]any); ok && len(payload) > 0 {
		root = payload
	}

	rawBucketLists := make([][]any, 0)
	if groups, ok := root["groups"].([]any); ok {
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				continue
			}
			if rawBuckets, ok := gm["buckets"].([]any); ok {
				rawBucketLists = append(rawBucketLists, rawBuckets)
			}
		}
	}
	if topBuckets, ok := root["buckets"].([]any); ok {
		rawBucketLists = append(rawBucketLists, topBuckets)
	}
	if len(rawBucketLists) == 0 {
		return nil
	}

	buckets := make([]model.QuotaBucket, 0)
	seen := make(map[string]struct{})
	for _, rawBuckets := range rawBucketLists {
		for _, b := range rawBuckets {
			bm, ok := b.(map[string]any)
			if !ok {
				continue
			}
			bid, _ := bm["bucketId"].(string)
			bid = strings.TrimSpace(bid)
			if bid == "" {
				continue
			}
			frac, hasFrac := bm["remainingFraction"].(float64)
			reset, _ := bm["resetTime"].(string)
			reset = strings.TrimSpace(reset)
			// Proto3 JSON 序列化在 remainingFraction 为 0 时可能省略该字段；只要带 resetTime 即视为 0% 已耗尽
			if !hasFrac && reset == "" {
				continue
			}
			if !hasFrac {
				frac = 0
			}
			key := strings.ToLower(bid)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			disp, _ := bm["displayName"].(string)
			buckets = append(buckets, model.QuotaBucket{
				BucketID:    bid,
				DisplayName: strings.TrimSpace(disp),
				Percentage:  fractionToPercent(frac),
				ResetTime:   reset,
				Exhausted:   frac <= 0,
			})
		}
	}
	return buckets
}

func buildQuotaData(resp rawModelsResponse, summary map[string]any, tierID string, credits []model.CreditInfo, projectID string) model.QuotaData {
	entries := extractModelsMap(resp)
	selectableIDs := extractSelectableModelIDs(resp, entries)

	models := make([]model.ModelQuota, 0, len(entries))
	if len(selectableIDs) > 0 {
		// 严格按云端 agentModelSorts 过滤与排序，自动剔除已下线或非反重力客户端模型
		for _, name := range selectableIDs {
			info := entries[name]
			if info.QuotaInfo == nil {
				continue
			}
			models = append(models, toModelQuota(name, info))
		}
	} else {
		for name, info := range entries {
			if info.QuotaInfo == nil || !isTrackedModel(name, info.DisplayName) {
				continue
			}
			models = append(models, toModelQuota(name, info))
		}
		sort.Slice(models, func(i, j int) bool {
			return models[i].Name < models[j].Name
		})
	}

	effectiveSummary := summary
	if len(effectiveSummary) == 0 {
		effectiveSummary = resp.QuotaSummary
	}
	summaryBuckets := parseQuotaSummaryBuckets(effectiveSummary)

	return model.QuotaData{
		Models:           models,
		SummaryBuckets:   summaryBuckets,
		LastUpdated:      time.Now().Unix(),
		IsForbidden:      false,
		SubscriptionTier: NormalizeSubscriptionTier(tierID),
		TierID:           strings.TrimSpace(tierID),
		Credits:          credits,
		ProjectID:        strings.TrimSpace(projectID),
	}
}
