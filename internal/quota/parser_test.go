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
					"MODEL_PLACEHOLDER_M525": map[string]any{
						"displayName": "Gemini 4 Hydrogen (Low)",
						"quotaInfo": map[string]any{
							"remainingFraction": 0.9,
							"resetTime":         "2026-11-21T16:30:00Z",
						},
					},
					"gemini-2.5-pro": map[string]any{
						"displayName": "Gemini 2.5 Pro",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
						},
					},
					"gemini-3-flash": map[string]any{
						"displayName": "Gemini 3 Flash",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
						},
					},
					"gemini-3.1-flash-image": map[string]any{
						"displayName": "Gemini 3.1 Flash Image",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
						},
					},
					"gemini-3.1-flash-lite": map[string]any{
						"displayName": "Gemini 3.1 Flash Lite",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
						},
					},
					"gemini-3.5-flash-lite": map[string]any{
						"displayName": "Gemini 3.5 Flash Lite",
						"quotaInfo": map[string]any{
							"remainingFraction": 1.0,
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
		case "/" + RetrieveUserQuotaSummaryPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"groups": []map[string]any{
					{
						"buckets": []map[string]any{
							{"bucketId": "gemini-argon-wtu-weekly", "remainingFraction": 0.95, "resetTime": "2026-11-28T00:00:00Z"},
							{"bucketId": "gemini-5h", "remainingFraction": 1.0, "resetTime": "2026-11-21T18:00:00Z"},
							{"bucketId": "gemini-weekly", "remainingFraction": 0.88, "resetTime": "2026-11-28T00:00:00Z"},
							{"bucketId": "3p-5h", "remainingFraction": 0.6, "resetTime": "2026-11-21T17:00:00Z"},
							{"bucketId": "3p-weekly", "remainingFraction": 0.45, "resetTime": "2026-11-28T00:00:00Z"},
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
	if len(res.Quota.Models) != 3 {
		t.Fatalf("期望保留 3 个核心模型（含占位 ID 展示名匹配），实际 %d: %+v", len(res.Quota.Models), res.Quota.Models)
	}
	if len(res.Quota.SummaryBuckets) != 5 {
		t.Fatalf("期望独立解析出 5 个 5h/周共享额度桶，实际 %d: %+v", len(res.Quota.SummaryBuckets), res.Quota.SummaryBuckets)
	}
}

func TestBuildQuotaDataFollowsAgentModelSorts(t *testing.T) {
	f1 := 0.9
	f2 := 0.7
	f3 := 1.0
	resp := rawModelsResponse{
		AgentModelSorts: []rawAgentModelSort{
			{
				DisplayName: "Recommended",
				Groups: []rawAgentModelGroup{
					{
						ModelIDs: []string{
							"gemini-4-neon-high",
							"claude-sonnet-5-5-high",
						},
					},
				},
			},
		},
		Models: map[string]rawModelInfo{
			"gemini-2.5-pro": {
				DisplayName: "Gemini 2.5 Pro",
				QuotaInfo:   &rawQuotaInfo{RemainingFraction: &f3},
			},
			"gemini-3-flash": {
				DisplayName: "Gemini 3 Flash",
				QuotaInfo:   &rawQuotaInfo{RemainingFraction: &f3},
			},
			"claude-sonnet-5-5-high": {
				DisplayName: "Claude Sonnet 5.5 (High)",
				QuotaInfo:   &rawQuotaInfo{RemainingFraction: &f2},
			},
			"gemini-4-neon-high": {
				DisplayName: "Gemini 4 Neon (High)",
				QuotaInfo:   &rawQuotaInfo{RemainingFraction: &f1},
			},
		},
	}

	got := buildQuotaData(resp, nil, "g1-pro-tier", nil, "proj-1")
	if len(got.Models) != 2 {
		t.Fatalf("有 agentModelSorts 时应仅保留官方可选模型，实际 %d: %+v", len(got.Models), got.Models)
	}
	if got.Models[0].Name != "gemini-4-neon-high" || got.Models[1].Name != "claude-sonnet-5-5-high" {
		t.Fatalf("应保持 agentModelSorts 顺序，实际: %+v", got.Models)
	}
}

func TestParseQuotaSummaryBucketsHandlesZeroOmittedFraction(t *testing.T) {
	summary := map[string]any{
		"groups": []any{
			map[string]any{
				"buckets": []any{
					map[string]any{
						"bucketId":  "3p-weekly",
						"resetTime": "2026-11-28T00:00:00Z",
					},
					map[string]any{
						"bucketId":          "3p-5h",
						"remainingFraction": 0.65,
						"resetTime":         "2026-11-21T18:00:00Z",
					},
				},
			},
		},
	}
	buckets := parseQuotaSummaryBuckets(summary)
	if len(buckets) != 2 {
		t.Fatalf("期望解析出 2 个桶（含省略 0 值的周额度桶），实际 %d: %+v", len(buckets), buckets)
	}
	if buckets[0].BucketID != "3p-weekly" || buckets[0].Percentage != 0 || !buckets[0].Exhausted {
		t.Fatalf("省略 remainingFraction 且带 resetTime 的桶应视为 0%% 已耗尽: %+v", buckets[0])
	}
}
