package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"more-account/internal/model"
)

// FetchResult 表示一次配额拉取的完整结果（可能包含模型数据及部分/403 错误诊断）。
type FetchResult struct {
	Quota     model.QuotaData
	Error     *model.QuotaErrorInfo
	ProjectID string
}

// Client 负责向 Google CloudCode 查询订阅、项目与模型剩余配额。
type Client struct {
	HTTPClient      *http.Client
	BaseURLOverride string
}

// NewClient 创建默认 CloudCode 配额查询客户端。
func NewClient() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) baseURL(ctx CloudCodeContext) string {
	if strings.TrimSpace(c.BaseURLOverride) != "" {
		return strings.TrimRight(c.BaseURLOverride, "/")
	}
	return ResolveCloudCodeBaseURL(ctx)
}

func cloudCodeOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "darwin"
	case "windows":
		return "windows"
	case "linux":
		return "linux"
	default:
		return "windows"
	}
}

func cloudCodeArch() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64"
	default:
		return "amd64"
	}
}

func cloudCodePlatformName() string {
	switch cloudCodeOS() + "_" + cloudCodeArch() {
	case "darwin_amd64":
		return "DARWIN_AMD64"
	case "darwin_arm64":
		return "DARWIN_ARM64"
	case "linux_amd64":
		return "LINUX_AMD64"
	case "linux_arm64":
		return "LINUX_ARM64"
	case "windows_amd64":
		return "WINDOWS_AMD64"
	default:
		return "PLATFORM_UNSPECIFIED"
	}
}

func loadCodeAssistUserAgent() string {
	return fmt.Sprintf("antigravity/%s %s/%s google-api-nodejs-client/%s",
		DefaultCloudCodeIDEVersion, cloudCodeOS(), cloudCodeArch(), DefaultGoogleAPINodeClientVer)
}

func cloudCodeUserAgent() string {
	return fmt.Sprintf("antigravity/%s (aidev_client; os_type=%s; arch=%s; cl=%s)",
		DefaultCloudCodeIDEVersion, cloudCodeOS(), cloudCodeArch(), DefaultCloudCodeCL)
}

func buildCloudCodeMetadata(duetProject string) map[string]any {
	meta := map[string]any{
		"ideName":       "antigravity",
		"ideType":       "ANTIGRAVITY",
		"ideVersion":    DefaultCloudCodeIDEVersion,
		"pluginVersion": "1.0.0",
		"platform":      cloudCodePlatformName(),
		"updateChannel": "stable",
		"pluginType":    "GEMINI",
	}
	if p := strings.TrimSpace(duetProject); p != "" {
		meta["duetProject"] = p
	}
	return meta
}

type loadCodeAssistResp struct {
	Project      any `json:"cloudaicompanionProject"`
	CurrentTier  *rawTier `json:"currentTier"`
	PaidTier     *rawTier `json:"paidTier"`
	AllowedTiers []struct {
		ID        string `json:"id"`
		IsDefault bool   `json:"isDefault"`
	} `json:"allowedTiers"`
}

type rawTier struct {
	ID               string `json:"id"`
	AvailableCredits []struct {
		CreditType                  string `json:"creditType"`
		CreditAmount                string `json:"creditAmount"`
		MinimumCreditAmountForUsage string `json:"minimumCreditAmountForUsage"`
	} `json:"availableCredits"`
}

func extractProjectID(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if id, ok := v["id"].(string); ok {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func extractCredits(t *rawTier) []model.CreditInfo {
	if t == nil || len(t.AvailableCredits) == 0 {
		return nil
	}
	out := make([]model.CreditInfo, 0, len(t.AvailableCredits))
	for _, item := range t.AvailableCredits {
		if strings.TrimSpace(item.CreditType) == "" || strings.TrimSpace(item.CreditAmount) == "" {
			continue
		}
		out = append(out, model.CreditInfo{
			CreditType:                  strings.TrimSpace(item.CreditType),
			CreditAmount:                strings.TrimSpace(item.CreditAmount),
			MinimumCreditAmountForUsage: strings.TrimSpace(item.MinimumCreditAmountForUsage),
		})
	}
	return out
}

func (c *Client) loadProjectAndTier(ctx context.Context, baseURL, accessToken, preferredProjectID string) (string, string, []model.CreditInfo) {
	payload := map[string]any{
		"metadata": buildCloudCodeMetadata(preferredProjectID),
		"mode":     "FULL_ELIGIBILITY_CHECK",
	}
	if preferredProjectID != "" {
		payload["cloudaicompanionProject"] = preferredProjectID
	}

	rawBody, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/"+LoadCodeAssistPath, bytes.NewReader(rawBody))
	if err != nil {
		return "", "", nil
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", loadCodeAssistUserAgent())
	req.Header.Set("x-goog-api-client", "gl-node/"+DefaultGoogAPIClientNodeVer)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", "", nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", "", nil
	}

	var parsed loadCodeAssistResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", "", nil
	}

	var tierID string
	if parsed.PaidTier != nil && strings.TrimSpace(parsed.PaidTier.ID) != "" {
		tierID = strings.TrimSpace(parsed.PaidTier.ID)
	} else if parsed.CurrentTier != nil && strings.TrimSpace(parsed.CurrentTier.ID) != "" {
		tierID = strings.TrimSpace(parsed.CurrentTier.ID)
	}

	credits := extractCredits(parsed.PaidTier)
	projectID := extractProjectID(parsed.Project)
	if projectID != "" {
		return projectID, tierID, credits
	}

	// 若账号尚未分配云项目，尝试执行 onboardUser 获取默认项目 ID。
	var onboardTier string
	for _, t := range parsed.AllowedTiers {
		if t.IsDefault && strings.TrimSpace(t.ID) != "" {
			onboardTier = strings.TrimSpace(t.ID)
			break
		}
	}
	if onboardTier == "" && len(parsed.AllowedTiers) > 0 && strings.TrimSpace(parsed.AllowedTiers[0].ID) != "" {
		onboardTier = strings.TrimSpace(parsed.AllowedTiers[0].ID)
	}
	if onboardTier == "" {
		onboardTier = tierID
	}
	if onboardTier != "" {
		if proj, err := c.tryOnboardUser(ctx, baseURL, accessToken, onboardTier, preferredProjectID); err == nil && proj != "" {
			return proj, tierID, credits
		}
	}

	return "", tierID, credits
}

func (c *Client) tryOnboardUser(ctx context.Context, baseURL, accessToken, tierID, projectHint string) (string, error) {
	payload := map[string]any{
		"tierId":   tierID,
		"metadata": buildCloudCodeMetadata(projectHint),
	}
	if projectHint != "" {
		payload["cloudaicompanionProject"] = projectHint
	}
	rawBody, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/"+OnboardUserPath, bytes.NewReader(rawBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", loadCodeAssistUserAgent())

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("onboardUser 返回 HTTP %d", resp.StatusCode)
	}

	var op struct {
		Name     string `json:"name"`
		Done     bool   `json:"done"`
		Response *struct {
			Project any `json:"cloudaicompanionProject"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &op); err != nil {
		return "", err
	}

	for polls := 0; polls < 8; polls++ {
		if op.Done {
			if op.Response != nil {
				return extractProjectID(op.Response.Project), nil
			}
			return "", nil
		}
		if strings.TrimSpace(op.Name) == "" {
			return "", nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1internal/"+strings.TrimSpace(op.Name), nil)
		if err != nil {
			return "", err
		}
		pollReq.Header.Set("Authorization", "Bearer "+accessToken)
		pollReq.Header.Set("User-Agent", loadCodeAssistUserAgent())

		pollResp, err := c.httpClient().Do(pollReq)
		if err != nil {
			return "", err
		}
		pollBody, _ := io.ReadAll(io.LimitReader(pollResp.Body, 1<<20))
		_ = pollResp.Body.Close()
		if pollResp.StatusCode < 200 || pollResp.StatusCode >= 300 {
			return "", fmt.Errorf("onboardUser 轮询返回 HTTP %d", pollResp.StatusCode)
		}
		if err := json.Unmarshal(pollBody, &op); err != nil {
			return "", err
		}
	}
	return "", nil
}

func isExplicitQuotaDenial(info model.QuotaErrorInfo) bool {
	return info.Reason == "VALIDATION_REQUIRED" ||
		info.Reason == "SUBSCRIPTION_REQUIRED" ||
		strings.TrimSpace(info.ValidationURL) != ""
}

// fetchQuotaSummary 向 CloudCode 拉取 5 小时与周额度共享配额桶摘要。
func (c *Client) fetchQuotaSummary(ctx context.Context, baseURL, accessToken string, rawPayload []byte) (map[string]any, *model.QuotaErrorInfo) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/"+RetrieveUserQuotaSummaryPath, bytes.NewReader(rawPayload))
	if err != nil {
		return nil, nil
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", cloudCodeUserAgent())

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errInfo := ParseGoogleAPIError(resp.StatusCode, string(body))
		return nil, &errInfo
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil
	}
	return parsed, nil
}

// FetchQuota 拉取单个账号的订阅、AI 积分、5h/周共享配额桶及所有模型配额。
func (c *Client) FetchQuota(ctx context.Context, token model.TokenData, email string) (*FetchResult, error) {
	ccCtx := CloudCodeContextFromToken(token)
	baseURL := c.baseURL(ccCtx)

	resolvedProject, tierID, credits := c.loadProjectAndTier(ctx, baseURL, token.AccessToken, ccCtx.PreferredProjectID)
	effectiveProject := resolvedProject
	if effectiveProject == "" {
		effectiveProject = ccCtx.PreferredProjectID
	}

	reqPayload := map[string]any{}
	if effectiveProject != "" {
		reqPayload["project"] = effectiveProject
	}
	rawPayload, _ := json.Marshal(reqPayload)

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/"+FetchAvailableModelsPath, bytes.NewReader(rawPayload))
		if err != nil {
			return nil, fmt.Errorf("构建配额请求失败: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", cloudCodeUserAgent())

		resp, err := c.httpClient().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("请求 CloudCode 配额网络错误: %w", err)
			if attempt < 3 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt*500) * time.Millisecond):
				}
				continue
			}
			return nil, lastErr
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()

		var modelsResp rawModelsResponse
		var modelsErr *model.QuotaErrorInfo

		if resp.StatusCode == http.StatusForbidden {
			errInfo := ParseGoogleAPIError(resp.StatusCode, string(body))
			if isExplicitQuotaDenial(errInfo) {
				q := model.QuotaData{
					Models:           []model.ModelQuota{},
					LastUpdated:      time.Now().Unix(),
					IsForbidden:      true,
					SubscriptionTier: NormalizeSubscriptionTier(tierID),
					TierID:           strings.TrimSpace(tierID),
					Credits:          credits,
					ProjectID:        effectiveProject,
				}
				return &FetchResult{
					Quota:     q,
					Error:     &errInfo,
					ProjectID: effectiveProject,
				}, nil
			}
			// 非明确封禁/验证的 403 继续尝试拉取配额摘要桶
			modelsErr = &errInfo
		} else if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("配额接口暂时不可用 (HTTP %d)", resp.StatusCode)
			if attempt < 3 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt*600) * time.Millisecond):
				}
				continue
			}
			return nil, lastErr
		} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			errInfo := ParseGoogleAPIError(resp.StatusCode, string(body))
			return nil, fmt.Errorf("查询 %s 配额失败 (HTTP %d): %s", email, resp.StatusCode, errInfo.Message)
		} else {
			if err := json.Unmarshal(body, &modelsResp); err != nil {
				return nil, fmt.Errorf("解析配额响应失败: %w", err)
			}
		}

		// 同步拉取 retrieveUserQuotaSummary，获取 5 小时与周额度共享桶
		summaryMap, summaryErr := c.fetchQuotaSummary(ctx, baseURL, token.AccessToken, rawPayload)
		quotaData := buildQuotaData(modelsResp, summaryMap, tierID, credits, effectiveProject)

		var finalErr *model.QuotaErrorInfo
		if summaryErr != nil && isExplicitQuotaDenial(*summaryErr) {
			finalErr = summaryErr
			if modelsErr != nil {
				quotaData.IsForbidden = true
			}
		} else if modelsErr != nil && len(quotaData.SummaryBuckets) == 0 && len(quotaData.Models) == 0 {
			finalErr = modelsErr
			quotaData.IsForbidden = true
		}

		return &FetchResult{
			Quota:     quotaData,
			Error:     finalErr,
			ProjectID: effectiveProject,
		}, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("查询配额失败")
}
