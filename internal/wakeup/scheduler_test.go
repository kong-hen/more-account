package wakeup

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/storage"
)

type rewriteHostTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt *rewriteHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = rt.target.Scheme
	cloned.URL.Host = rt.target.Host
	return rt.base.RoundTrip(cloned)
}

func TestParseStreamSSEResponse(t *testing.T) {
	sse := "" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"thinking...\",\"thought\":true}]}}],\"responseId\":\"resp-1\"}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":12,\"candidatesTokenCount\":3,\"totalTokenCount\":15}},\"traceId\":\"tr-1\"}\n\n" +
		"data: [DONE]\n"

	parsed, err := ParseStreamResult(sse)
	if err != nil {
		t.Fatalf("解析 SSE 唤醒响应失败: %v", err)
	}
	if parsed.Reply != "Hello" {
		t.Fatalf("期望过滤思考段并得到 Hello，实际 %q", parsed.Reply)
	}
	if parsed.PromptTokens != 12 || parsed.CompletionTokens != 3 || parsed.TotalTokens != 15 {
		t.Fatalf("Token 统计不匹配: %+v", parsed)
	}
}

func TestShouldTriggerScheduleModes(t *testing.T) {
	// 2026-11-21 是周六 (time.Saturday == 6)
	now := time.Date(2026, 11, 21, 9, 30, 0, 0, time.Local)

	// 1. 每日定点命中
	dailyTask := model.WakeupTask{
		ID:      "t-daily",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode: "daily",
			DailyTimes: []string{"09:30", "18:00"},
		},
	}
	if !ShouldTriggerTask(dailyTask, now, 0) {
		t.Fatal("每日 09:30 应当触发")
	}
	// 同一分钟内已执行过则防重
	if ShouldTriggerTask(dailyTask, now, now.UnixMilli()) {
		t.Fatal("同分钟内不应重复触发")
	}

	// 2. 每周定点
	weeklyTask := model.WakeupTask{
		ID:      "t-weekly",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:  "weekly",
			WeeklyDays:  []int{int(time.Saturday)},
			WeeklyTimes: []string{"09:30"},
		},
	}
	if !ShouldTriggerTask(weeklyTask, now, 0) {
		t.Fatal("每周六 09:30 应当触发")
	}

	// 3. Cron 表达式
	cronTask := model.WakeupTask{
		ID:      "t-cron",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode: "crontab",
			Crontab:    "30 9 * * 6",
		},
	}
	if !ShouldTriggerTask(cronTask, now, 0) {
		t.Fatal("Cron '30 9 * * 6' 应当命中周六 09:30")
	}

	// 4. 固定间隔 + 时间窗
	intervalTask := model.WakeupTask{
		ID:      "t-interval",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:        "interval",
			IntervalHours:     4,
			IntervalStartTime: "08:00",
			IntervalEndTime:   "22:00",
		},
	}
	fiveHoursAgo := now.Add(-5 * time.Hour).UnixMilli()
	if !ShouldTriggerTask(intervalTask, now, fiveHoursAgo) {
		t.Fatal("距上次执行超过 4 小时且在时间窗内应当触发")
	}
	oneHourAgo := now.Add(-1 * time.Hour).UnixMilli()
	if ShouldTriggerTask(intervalTask, now, oneHourAgo) {
		t.Fatal("距上次执行不足 4 小时不应触发")
	}

	// 5. Cron 边界：周日用 7 表示、带偏移步长 a/step（如 5/15 命中 20 分不命中 15 分）
	sunday := time.Date(2026, 11, 22, 9, 20, 0, 0, time.Local) // 周日 09:20
	cronSundayStep := model.WakeupTask{
		ID:      "t-cron-edge",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode: "crontab",
			Crontab:    "5/15 9 * * 7",
		},
	}
	if !ShouldTriggerTask(cronSundayStep, sunday, 0) {
		t.Fatal("Cron '5/15 9 * * 7' 应当命中周日 09:20")
	}
	sundayMiss := time.Date(2026, 11, 22, 9, 15, 0, 0, time.Local)
	if ShouldTriggerTask(cronSundayStep, sundayMiss, 0) {
		t.Fatal("Cron '5/15 9 * * 7' 不应命中周日 09:15")
	}
}

func TestParseStreamSSEResponseCapturesFinalUsage(t *testing.T) {
	sse := "" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":1,\"totalTokenCount\":11}}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" there\"}]}}],\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":8,\"totalTokenCount\":18}}}\n\n" +
		"data: [DONE]\n"

	parsed, err := ParseStreamResult(sse)
	if err != nil {
		t.Fatalf("解析多段 SSE 失败: %v", err)
	}
	if parsed.Reply != "Hi there" {
		t.Fatalf("期望拼接回复 'Hi there'，实际 %q", parsed.Reply)
	}
	if parsed.PromptTokens != 10 || parsed.CompletionTokens != 8 || parsed.TotalTokens != 18 {
		t.Fatalf("应取最终段累计 Token (10/8/18)，实际 %+v", parsed)
	}
}

func TestCheckQuotaResetTriggerTracksLowQuotaAndRespectsSelectedModels(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email: "reset@example.com",
		Token: model.TokenData{RefreshToken: "rt-1"},
		Quota: &model.QuotaData{
			Models: []model.ModelQuota{
				{Name: "gemini-3.8-flash-high", Percentage: 40, ResetTime: "2026-11-21T12:00:00Z"},
				{Name: "claude-sonnet-4-5", Percentage: 100, ResetTime: "2026-11-21T12:00:00Z"},
			},
		},
	})
	if err != nil {
		t.Fatalf("写入账号失败: %v", err)
	}

	svc := NewService(store, nil, nil, nil)
	task := model.WakeupTask{
		ID:      "task-reset",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:       "quota_reset",
			WakeOnReset:      true,
			SelectedAccounts: []string{acc.ID},
			SelectedModels:   []string{"gemini-3.8-flash-high"},
		},
	}

	now := time.Now()
	// 首次采样：即使 gemini 当前配额只有 40%，也应记录其初始 resetTime 且不触发
	if svc.checkQuotaResetTrigger(task, now) {
		t.Fatal("首次基线采样不应触发唤醒")
	}

	// 仅未选中的模型（claude-sonnet-4-5）发生重置，不应触发任务
	_, _ = store.UpdateAccount(acc.ID, func(target *model.Account) error {
		target.Quota.Models[1].ResetTime = "2026-11-21T17:00:00Z"
		return nil
	})
	if svc.checkQuotaResetTrigger(task, now.Add(time.Minute)) {
		t.Fatal("未选中模型的配额重置不应误触发唤醒任务")
	}

	// 选中的 gemini 配额重置：resetTime 推进且配额恢复到 100%，应当触发唤醒
	_, _ = store.UpdateAccount(acc.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = "2026-11-21T17:00:00Z"
		return nil
	})
	if !svc.checkQuotaResetTrigger(task, now.Add(2*time.Minute)) {
		t.Fatal("选中模型从低配额重置至 100% 时应当触发唤醒")
	}
}

func TestTestNowConcurrentSameScopeDoesNotDeleteNewerCancelHandle(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email: "scope@example.com",
		Token: model.TokenData{
			AccessToken:     "valid-at",
			RefreshToken:    "rt-scope",
			ExpiryTimestamp: time.Now().Unix() + 3600,
			ProjectID:       "proj-scope",
		},
	})
	if err != nil {
		t.Fatalf("写入账号失败: %v", err)
	}

	req1Started := make(chan struct{})
	req2Started := make(chan struct{})
	releaseCh := make(chan struct{})
	var reqCount int
	var countMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		countMu.Lock()
		reqCount++
		n := reqCount
		countMu.Unlock()
		if n == 1 {
			close(req1Started)
		} else if n == 2 {
			close(req2Started)
		}
		// 阻塞等待请求上下文取消或测试结束释放
		select {
		case <-r.Context().Done():
		case <-releaseCh:
		}
	}))
	defer srv.Close()
	defer close(releaseCh)

	targetURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, oauth.NewClient(), nil, nil)
	svc.httpClient = &http.Client{
		Transport: &rewriteHostTransport{
			target: targetURL,
			base:   srv.Client().Transport,
		},
	}

	done1 := make(chan error, 1)
	go func() {
		_, runErr := svc.TestNow(context.Background(), model.WakeupTestRequest{
			AccountID: acc.ID,
			Model:     "gemini-3.8-flash-high",
			ScopeID:   "shared-scope",
		})
		done1 <- runErr
	}()
	<-req1Started

	// 同 scopeID 并发重入第二次测试：取消第一次测试，但第一次测试退出清理时不得删掉第二次测试的取消句柄
	done2 := make(chan error, 1)
	go func() {
		_, runErr := svc.TestNow(context.Background(), model.WakeupTestRequest{
			AccountID: acc.ID,
			Model:     "gemini-3.8-flash-high",
			ScopeID:   "shared-scope",
		})
		done2 <- runErr
	}()
	<-req2Started
	// 等待第一次测试彻底退出并执行完 defer 清理
	select {
	case <-done1:
	case <-time.After(2 * time.Second):
		t.Fatal("第一次测试未被同 scopeID 重入取消")
	}

	// 此时调用 CancelScope("shared-scope") 必须仍能成功取消第二次测试
	svc.CancelScope("shared-scope")
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("第二次测试的取消句柄被第一次测试误删，导致 CancelTest 失效")
	}
	if hist := store.ListWakeupHistory(); len(hist) != 0 {
		t.Fatalf("用户主动取消的唤醒测试不应写入失败历史记录，实际写入 %d 条: %+v", len(hist), hist)
	}
}

func TestThinkingOnlyStreamSucceedsAndCooldownPreservesReset(t *testing.T) {
	// 1. 思考模型仅返回 thought 分片与 thoughtsTokenCount 时应兼容判定唤醒成功
	thoughtOnlySSE := "" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"internal reasoning...\",\"thought\":true}]},\"finishReason\":\"MAX_TOKENS\"}],\"usageMetadata\":{\"promptTokenCount\":20,\"thoughtsTokenCount\":16,\"totalTokenCount\":36}}}\n\n" +
		"data: [DONE]\n"
	parsed, err := ParseStreamResult(thoughtOnlySSE)
	if err != nil {
		t.Fatalf("思考耗尽预算但 HTTP 200 有效流不应报错，实际: %v", err)
	}
	if parsed.Reply == "" || parsed.TotalTokens != 36 || parsed.CompletionTokens != 16 {
		t.Fatalf("思考流解析结果异常: %+v", parsed)
	}

	// 空响应仍应报错
	if _, err := ParseStreamResult(`data: {}`); err == nil {
		t.Fatal("无候选内容与无思考分片的空响应应当返回错误")
	}

	// 2. 验证 10 分钟冷却期内发生的配额重置不会被提前吞掉，冷却结束后仍能触发
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email: "cooldown@example.com",
		Token: model.TokenData{RefreshToken: "rt-cd"},
		Quota: &model.QuotaData{
			Models: []model.ModelQuota{
				{Name: "gemini-3.8-flash-high", Percentage: 10, ResetTime: "2026-11-21T10:00:00Z"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	baseNow := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	task := model.WakeupTask{
		ID:        "task-cd",
		Enabled:   true,
		LastRunAt: baseNow.UnixMilli(), // 刚刚执行过，处于 10 分钟冷却期内
		Schedule: model.WakeupSchedule{
			RepeatMode:       "quota_reset",
			WakeOnReset:      true,
			SelectedAccounts: []string{acc.ID},
		},
	}
	_ = store.SaveWakeupState(model.WakeupState{Enabled: true, Tasks: []model.WakeupTask{task}})

	svc := NewService(store, nil, nil, nil)
	svc.SyncResetBaseline() // 启动时立即建立 10:00:00Z 基线

	// 冷却期内（+2 分钟）配额重置至 100% 且 resetTime 推进
	_, _ = store.UpdateAccount(acc.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = "2026-11-21T15:00:00Z"
		return nil
	})
	if svc.checkQuotaResetTrigger(task, baseNow.Add(2*time.Minute)) {
		t.Fatal("冷却期内不应立即触发")
	}
	// 冷却期结束后（+11 分钟），此前未消费的重置事件必须正常触发
	if !svc.checkQuotaResetTrigger(task, baseNow.Add(11*time.Minute)) {
		t.Fatal("冷却期结束后应当触发此前未消费的配额重置事件")
	}
}

func TestWakeupPersistsValidationURLToAccountQuotaError(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email: "needverify@example.com",
		Token: model.TokenData{
			AccessToken:     "valid-at",
			RefreshToken:    "rt-v",
			ExpiryTimestamp: time.Now().Unix() + 3600,
			ProjectID:       "proj-v",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{
			"error": {
				"code": 403,
				"message": "Verify your account to continue.",
				"details": [{
					"reason": "VALIDATION_REQUIRED",
					"metadata": {"validation_url": "https://accounts.google.com/verify/wakeup-check"}
				}]
			}
		}`))
	}))
	defer srv.Close()

	targetURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, oauth.NewClient(), nil, nil)
	svc.httpClient = &http.Client{
		Transport: &rewriteHostTransport{
			target: targetURL,
			base:   srv.Client().Transport,
		},
	}

	item, runErr := svc.TestNow(context.Background(), model.WakeupTestRequest{
		AccountID: acc.ID,
		Model:     "gemini-3.8-flash-high",
	})
	if runErr == nil {
		t.Fatal("403 验证拦截应当返回错误")
	}
	if item == nil || item.ValidationURL != "https://accounts.google.com/verify/wakeup-check" {
		t.Fatalf("唤醒历史应记录 ValidationURL，实际 %+v", item)
	}

	updatedAcc, ok := store.GetAccount(acc.ID)
	if !ok || updatedAcc.QuotaError == nil || updatedAcc.QuotaError.ValidationURL != "https://accounts.google.com/verify/wakeup-check" {
		t.Fatalf("唤醒发现需网页验证时应同步回写账号 QuotaError.ValidationURL，实际 %+v", updatedAcc.QuotaError)
	}
}
