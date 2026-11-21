package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"more-account/internal/model"
)

func TestResolveCloudCodeBaseURL(t *testing.T) {
	if got := ResolveCloudCodeBaseURL(CloudCodeContext{}); got != CloudCodeDailyBaseURL {
		t.Fatalf("默认应走 daily 域名，实际 %s", got)
	}
	if got := ResolveCloudCodeBaseURL(CloudCodeContext{
		IsGcpTos:           true,
		PreferredProjectID: "my-gcp-project",
	}); got != CloudCodeProdBaseURL {
		t.Fatalf("GCP ToS 且有有效项目应走 prod 域名，实际 %s", got)
	}
	if got := ResolveCloudCodeBaseURL(CloudCodeContext{
		IsGcpTos:           true,
		PreferredProjectID: "aicode-consumers",
	}); got != CloudCodeDailyBaseURL {
		t.Fatalf("占位项目 aicode-consumers 不应触发 prod 路由，实际 %s", got)
	}
}

func TestParseGoogleAPIErrorValidationURL(t *testing.T) {
	body := `{
		"error": {
			"code": 403,
			"message": "Verify your account to continue.",
			"details": [
				{
					"reason": "VALIDATION_REQUIRED",
					"metadata": {
						"validation_url": "https://accounts.google.com/verify/example"
					}
				}
			]
		}
	}`
	parsed := ParseGoogleAPIError(403, body)
	if parsed.Code != 403 {
		t.Fatalf("期望状态码 403，实际 %d", parsed.Code)
	}
	if parsed.Reason != "VALIDATION_REQUIRED" {
		t.Fatalf("期望 reason=VALIDATION_REQUIRED，实际 %q", parsed.Reason)
	}
	if parsed.ValidationURL != "https://accounts.google.com/verify/example" {
		t.Fatalf("期望提取 validation_url，实际 %q", parsed.ValidationURL)
	}
}

func TestClientFetchQuotaEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + LoadCodeAssistPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"cloudaicompanionProject": "proj-demo-1",
				"paidTier": map[string]any{
					"id": "g1-pro-tier",
					"availableCredits": []map[string]any{
						{
							"creditType":   "GOOGLE_ONE_AI",
							"creditAmount": "500",
						},
					},
				},
			})
		case "/" + FetchAvailableModelsPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"gemini-3-pro-high": map[string]any{
						"displayName": "Gemini 3 Pro (High)",
						"quotaInfo": map[string]any{
							"remainingFraction": 0.85,
							"resetTime":         "2026-11-21T16:00:00Z",
						},
					},
					"claude-sonnet-4-5": map[string]any{
						"displayName": "Claude Sonnet 4.5",
						"quotaInfo": map[string]any{
							"remainingFraction": 0.4,
							"resetTime":         "2026-11-21T15:00:00Z",
						},
					},
					"irrelevant-internal-model": map[string]any{
						"displayName": "Other",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := &Client{
		HTTPClient:      srv.Client(),
		BaseURLOverride: srv.URL,
	}

	res, err := client.FetchQuota(context.Background(), model.TokenData{
		AccessToken: "tok-1",
	}, "alice@example.com")
	if err != nil {
		t.Fatalf("拉取配额失败: %v", err)
	}
	if res.Quota.SubscriptionTier != "PRO" {
		t.Fatalf("期望识别订阅为 PRO，实际 %q", res.Quota.SubscriptionTier)
	}
	if res.Quota.ProjectID != "proj-demo-1" {
		t.Fatalf("期望 ProjectID=proj-demo-1，实际 %q", res.Quota.ProjectID)
	}
	if len(res.Quota.Credits) != 1 || res.Quota.Credits[0].CreditAmount != "500" {
		t.Fatalf("期望提取 1 条 AI 积分，实际 %+v", res.Quota.Credits)
	}
	if len(res.Quota.Models) != 2 {
		t.Fatalf("期望保留 2 个核心模型，实际 %d: %+v", len(res.Quota.Models), res.Quota.Models)
	}
}
