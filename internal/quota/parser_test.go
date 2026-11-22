package quota

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/storage"
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

func TestRefreshAccountPreservesDetailsAndPriorTierSummaryOnPartialFailure(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email:           "keep@example.com",
		Notes:           "重要备注",
		AccountPassword: "secret-pass",
		Token: model.TokenData{
			AccessToken:     "valid-at",
			RefreshToken:    "valid-rt",
			ExpiryTimestamp: time.Now().Unix() + 3600,
			ProjectID:       "proj-1",
		},
		Quota: &model.QuotaData{
			SubscriptionTier: "PRO",
			TierID:           "g1-pro-tier",
			Credits:          []model.CreditInfo{{CreditType: "GOOGLE_ONE_AI", CreditAmount: "300"}},
			SummaryBuckets:   []model.QuotaBucket{{BucketID: "gemini-weekly", Percentage: 88, ResetTime: "2026-11-28T00:00:00Z"}},
		},
	})
	if err != nil {
		t.Fatalf("写入测试账号失败: %v", err)
	}

	// 模拟 loadCodeAssist 与 retrieveUserQuotaSummary 瞬时 500，仅 fetchAvailableModels 正常返回
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + LoadCodeAssistPath, "/" + RetrieveUserQuotaSummaryPath:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
		case "/" + FetchAvailableModelsPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"gemini-3-pro-high": map[string]any{
						"displayName": "Gemini 3 Pro (High)",
						"quotaInfo": map[string]any{
							"remainingFraction": 0.72,
							"resetTime":         "2026-11-21T16:00:00Z",
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := NewService(store, oauth.NewClient(), &Client{
		HTTPClient:      srv.Client(),
		BaseURLOverride: srv.URL,
	})
	updated, err := svc.RefreshAccount(context.Background(), acc.ID)
	if err != nil {
		t.Fatalf("部分子接口瞬时失败时 RefreshAccount 应容错成功，实际 err=%v", err)
	}
	if updated.Notes != "重要备注" || updated.AccountPassword != "secret-pass" {
		t.Fatalf("配额刷新不应覆盖账号备注或密码: %+v", updated)
	}
	if updated.Quota == nil || len(updated.Quota.Models) != 1 || updated.Quota.Models[0].Percentage != 72 {
		t.Fatalf("期望更新模型配额为 72%%，实际 %+v", updated.Quota)
	}
	if updated.Quota.SubscriptionTier != "PRO" || updated.Quota.TierID != "g1-pro-tier" || len(updated.Quota.Credits) != 1 {
		t.Fatalf("loadCodeAssist 瞬时失败时应保留既有订阅与积分: %+v", updated.Quota)
	}
	if len(updated.Quota.SummaryBuckets) != 1 || updated.Quota.SummaryBuckets[0].BucketID != "gemini-weekly" {
		t.Fatalf("retrieveUserQuotaSummary 瞬时失败时应保留既有共享额度桶: %+v", updated.Quota.SummaryBuckets)
	}
}

func TestSanitizeOverrideURLOperationPathAndPrivacyError(t *testing.T) {
	if got := sanitizeCloudCodeBaseURLOverride("http://evil.example.com"); got != "" {
		t.Fatalf("非本机回环的明文 http 覆盖地址必须被拦截，实际 %q", got)
	}
	if got := sanitizeCloudCodeBaseURLOverride("http://127.0.0.1:8080/"); got != "http://127.0.0.1:8080" {
		t.Fatalf("本机回环测试地址应当允许，实际 %q", got)
	}
	if got := sanitizeCloudCodeBaseURLOverride("https://custom-cloudcode.googleapis.com/"); got != "https://custom-cloudcode.googleapis.com" {
		t.Fatalf("合法 https 覆盖地址应当允许，实际 %q", got)
	}

	if _, ok := sanitizeOperationPath("../admin/secret"); ok {
		t.Fatal("包含 .. 的 operation 路径必须被拦截")
	}
	if clean, ok := sanitizeOperationPath("v1internal/operations/op-123"); !ok || clean != "operations/op-123" {
		t.Fatalf("应自动剥离重复的 v1internal/ 前缀，实际 ok=%v clean=%q", ok, clean)
	}

	// 验证非 2xx 错误消息不拼接明文邮箱，防止绕过前端隐私模式
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()

	client := &Client{HTTPClient: srv.Client(), BaseURLOverride: srv.URL}
	_, err := client.FetchQuota(context.Background(), model.TokenData{AccessToken: "at"}, "secret-user@example.com")
	if err == nil {
		t.Fatal("HTTP 400 应返回错误")
	}
	if strings.Contains(err.Error(), "secret-user@example.com") {
		t.Fatalf("配额错误消息不应泄露明文邮箱: %v", err)
	}
}

func TestParseGoogleAPIErrorExtendedFormatsAndSafety(t *testing.T) {
	// 1. 兼容 google.rpc.Help 的 links[].url 结构
	helpBody := `{
		"error": {
			"code": 403,
			"message": "Account verification required before using Cloud Code.",
			"details": [
				{
					"@type": "type.googleapis.com/google.rpc.Help",
					"links": [
						{
							"description": "Complete web verification",
							"url": "https://accounts.google.com/signin/continue?sarp=1"
						}
					]
				}
			]
		}
	}`
	parsedHelp := ParseGoogleAPIError(403, helpBody)
	if parsedHelp.ValidationURL != "https://accounts.google.com/signin/continue?sarp=1" {
		t.Fatalf("期望从 Help.links 提取验证 URL，实际 %q", parsedHelp.ValidationURL)
	}
	if parsedHelp.Reason != "VALIDATION_REQUIRED" {
		t.Fatalf("期望自动标记 VALIDATION_REQUIRED，实际 %q", parsedHelp.Reason)
	}

	// 2. 兼容错误文本内嵌验证链接并自动剥离末尾标点
	inlineBody := `{"error":{"code":403,"message":"Please verify at https://accounts.google.com/verify/challenge?id=42)."}}`
	parsedInline := ParseGoogleAPIError(403, inlineBody)
	if parsedInline.ValidationURL != "https://accounts.google.com/verify/challenge?id=42" {
		t.Fatalf("期望提取文本内嵌验证 URL 并剥离末尾标点，实际 %q", parsedInline.ValidationURL)
	}

	// 3. 拦截非 http/https 的危险验证链接
	unsafeBody := `{"error":{"code":403,"details":[{"reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"javascript:alert(1)"}}]}}`
	parsedUnsafe := ParseGoogleAPIError(403, unsafeBody)
	if parsedUnsafe.ValidationURL != "" {
		t.Fatalf("危险协议验证链接必须被过滤为空，实际 %q", parsedUnsafe.ValidationURL)
	}
}

func TestFetchQuotaCapturesValidationURLFromLoadCodeAssist(t *testing.T) {
	// 模拟 loadCodeAssist 返回带 validation_url 的 403，而 fetchAvailableModels 仅返回普通无链接 403
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + LoadCodeAssistPath:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{
				"error": {
					"code": 403,
					"message": "Verify your account to continue.",
					"details": [{
						"reason": "VALIDATION_REQUIRED",
						"metadata": {"validation_url": "https://accounts.google.com/verify/from-assist"}
					}]
				}
			}`))
		case "/" + FetchAvailableModelsPath, "/" + RetrieveUserQuotaSummaryPath:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"code":403,"message":"Permission denied"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := &Client{HTTPClient: srv.Client(), BaseURLOverride: srv.URL}
	res, err := client.FetchQuota(context.Background(), model.TokenData{AccessToken: "tok-v"}, "verify@example.com")
	if err != nil {
		t.Fatalf("明确验证拦截应返回结构化 FetchResult 而非普通 err: %v", err)
	}
	if res.Error == nil || res.Error.ValidationURL != "https://accounts.google.com/verify/from-assist" {
		t.Fatalf("期望保留 loadCodeAssist 返回的 validation_url，实际 %+v", res.Error)
	}
	if res.Error.Reason != "VALIDATION_REQUIRED" || !res.Quota.IsForbidden {
		t.Fatalf("期望标记 VALIDATION_REQUIRED 与 IsForbidden=true，实际 err=%+v quota=%+v", res.Error, res.Quota)
	}
}

func TestRefreshAllCountsUnprocessedAndReturnsErrorOnCanceledContext(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化 store 失败: %v", err)
	}
	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		if _, err := store.UpsertAccount(model.Account{
			Email: email,
			Token: model.TokenData{
				AccessToken:     "at",
				RefreshToken:    "rt-" + email,
				ExpiryTimestamp: time.Now().Unix() + 3600,
			},
		}); err != nil {
			t.Fatalf("写入测试账号失败: %v", err)
		}
	}

	svc := NewService(store, oauth.NewClient(), NewClient())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	stats, err := svc.RefreshAll(ctx, 2, nil)
	if err == nil {
		t.Fatal("已取消上下文执行 RefreshAll 必须返回错误，避免前端误报全部成功")
	}
	if stats == nil || stats.Total != 3 || stats.Failed != 3 || stats.Success != 0 {
		t.Fatalf("期望统计全部 3 个未执行账号为失败，实际 stats=%+v", stats)
	}
	if len(stats.Errors) == 0 {
		t.Fatalf("期望 stats.Errors 包含取消/超时诊断信息: %+v", stats)
	}
}
