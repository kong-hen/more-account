package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"more-account/internal/model"
)

const (
	CloudCodeDailyBaseURL          = "https://daily-cloudcode-pa.googleapis.com"
	CloudCodeProdBaseURL           = "https://cloudcode-pa.googleapis.com"
	CloudCodeAutopushSandboxURL    = "https://autopush-cloudcode-pa.sandbox.googleapis.com"
	LoadCodeAssistPath             = "v1internal:loadCodeAssist"
	OnboardUserPath                = "v1internal:onboardUser"
	FetchAvailableModelsPath       = "v1internal:fetchAvailableModels"
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

type rawModelsResponse struct {
	Models       map[string]rawModelInfo `json:"models"`
	QuotaSummary map[string]any          `json:"quota_summary"`
}

// isExcludedNonAntigravityModel 排除非反重力客户端的模型（如 Gemini 3 Flash、Flash Image、Flash Lite 等）。
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

func buildQuotaData(resp rawModelsResponse, tierID string, credits []model.CreditInfo, projectID string) model.QuotaData {
	models := make([]model.ModelQuota, 0, len(resp.Models))
	for name, info := range resp.Models {
		if info.QuotaInfo == nil || !isTrackedModel(name, info.DisplayName) {
			continue
		}
		pct := 0
		if info.QuotaInfo.RemainingFraction != nil {
			pct = int(*info.QuotaInfo.RemainingFraction * 100.0)
			if pct < 0 {
				pct = 0
			}
			if pct > 100 {
				pct = 100
			}
		}
		models = append(models, model.ModelQuota{
			Name:        name,
			DisplayName: strings.TrimSpace(info.DisplayName),
			Percentage:  pct,
			ResetTime:   strings.TrimSpace(info.QuotaInfo.ResetTime),
		})
	}

	// 兼容 quota_summary.groups.buckets 结构。
	if groups, ok := resp.QuotaSummary["groups"].([]any); ok {
		for _, g := range groups {
			gm, ok := g.(map[string]any)
			if !ok {
				continue
			}
			buckets, ok := gm["buckets"].([]any)
			if !ok {
				continue
			}
			for _, b := range buckets {
				bm, ok := b.(map[string]any)
				if !ok {
					continue
				}
				bid, _ := bm["bucketId"].(string)
				frac, hasFrac := bm["remainingFraction"].(float64)
				if strings.TrimSpace(bid) == "" || !hasFrac {
					continue
				}
				disp, _ := bm["displayName"].(string)
				reset, _ := bm["resetTime"].(string)
				models = append(models, model.ModelQuota{
					Name:        strings.TrimSpace(bid),
					DisplayName: strings.TrimSpace(disp),
					Percentage:  int(frac * 100.0),
					ResetTime:   strings.TrimSpace(reset),
				})
			}
		}
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].Name < models[j].Name
	})

	return model.QuotaData{
		Models:           models,
		LastUpdated:      time.Now().Unix(),
		IsForbidden:      false,
		SubscriptionTier: NormalizeSubscriptionTier(tierID),
		TierID:           strings.TrimSpace(tierID),
		Credits:          credits,
		ProjectID:        strings.TrimSpace(projectID),
	}
}
