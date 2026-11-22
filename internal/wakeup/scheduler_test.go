package wakeup

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/quota"
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
	svc.SetTransport(&rewriteHostTransport{
		target: targetURL,
		base:   srv.Client().Transport,
	})

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
	svc.SetTransport(&rewriteHostTransport{
		target: targetURL,
		base:   srv.Client().Transport,
	})

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

// ─── 新增全量修复回归测试 ─────────────────────────────────────────

func TestMisfireCatchupWindowAndStartupImmediateCheck(t *testing.T) {
	// 1. 休眠 6 分钟跳过 09:30，在 20 分钟补触发窗口内应命中
	dailyTask := model.WakeupTask{
		ID:      "t-misfire",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode: "daily",
			DailyTimes: []string{"09:30"},
		},
	}
	prevTick := time.Date(2026, 11, 22, 9, 29, 40, 0, time.Local)
	wakeNow := time.Date(2026, 11, 22, 9, 36, 10, 0, time.Local)
	if !ShouldTriggerTaskInWindow(dailyTask, prevTick, wakeNow, 0) {
		t.Fatal("休眠 6 分钟跨过 09:30 应在 20 分钟补触发窗口内命中")
	}

	// 若 09:30 当分钟在休眠前已运行过，醒来后绝不应重复触发
	ranAtTarget := time.Date(2026, 11, 22, 9, 30, 5, 0, time.Local).UnixMilli()
	if ShouldTriggerTaskInWindow(dailyTask, prevTick, wakeNow, ranAtTarget) {
		t.Fatal("休眠前已在 09:30 执行过的任务醒来后不应重复补触发")
	}

	// 超过 20 分钟最大补触发窗口（如关机 2 小时）不应触发过期任务，防开机风暴
	longSleepPrev := time.Date(2026, 11, 22, 7, 0, 0, 0, time.Local)
	longSleepWake := time.Date(2026, 11, 22, 10, 0, 0, 0, time.Local)
	if ShouldTriggerTaskInWindow(dailyTask, longSleepPrev, longSleepWake, 0) {
		t.Fatal("超过 20 分钟最大补触发窗口的过期计划点不应被补触发")
	}

	// 系统时钟回拨（now < prevTick）应安全回退至仅匹配当前时刻
	if ShouldTriggerTaskInWindow(dailyTask, wakeNow, prevTick, 0) {
		t.Fatal("时钟回拨且当前为 09:29 时不应误触发 09:30 任务")
	}

	// 2. 启动立即检查 + TriggerCheckNow + HasActiveScheduledTasks / SetActiveTasksChangedHook
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email: "startup@example.com",
		Token: model.TokenData{
			AccessToken:     "at-ok",
			RefreshToken:    "rt-ok",
			ExpiryTimestamp: time.Now().Unix() + 3600,
			ProjectID:       "proj-startup",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var hitCount atomic.Int32
	hitCh := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount.Add(1)
		select {
		case hitCh <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"pong\"}]}}]}}\n\ndata: [DONE]\n"))
	}))
	defer srv.Close()
	targetURL, _ := url.Parse(srv.URL)

	fixedNow := time.Date(2026, 11, 22, 9, 30, 45, 0, time.Local)
	var clockMu sync.Mutex
	curClock := fixedNow

	_ = store.SaveWakeupState(model.WakeupState{
		Enabled: true,
		Tasks: []model.WakeupTask{
			{
				ID:      "t-immediate",
				Name:    "启动立即命中",
				Enabled: true,
				Schedule: model.WakeupSchedule{
					RepeatMode:       "daily",
					DailyTimes:       []string{"09:30", "09:35"},
					SelectedAccounts: []string{acc.ID},
					SelectedModels:   []string{"gemini-3.8-flash-high"},
				},
			},
		},
	})

	svc := NewService(store, oauth.NewClient(), nil, nil)
	svc.nowFunc = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return curClock
	}
	svc.SetTransport(&rewriteHostTransport{target: targetURL, base: srv.Client().Transport})

	var hookEvents []bool
	var hookMu sync.Mutex
	svc.SetActiveTasksChangedHook(func(active bool) {
		hookMu.Lock()
		hookEvents = append(hookEvents, active)
		hookMu.Unlock()
	})
	if !svc.HasActiveScheduledTasks() {
		t.Fatal("总开关启用且存在启用任务时 HasActiveScheduledTasks 应为 true")
	}

	// 启动后应立即触发 09:30，无需等待 30s ticker
	svc.Start(context.Background())
	select {
	case <-hitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Start() 启动后未立即检查并触发当前分钟的任务")
	}

	// 模拟休眠唤醒：时钟推进到 09:40（跳过 09:35），调用 TriggerCheckNow() 应立即补触发 09:35
	clockMu.Lock()
	curClock = time.Date(2026, 11, 22, 9, 40, 10, 0, time.Local)
	clockMu.Unlock()
	// 等待首次任务完成释放 runningTasks
	deadline := time.Now().Add(2 * time.Second)
	for svc.isTaskRunning("t-immediate") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	svc.TriggerCheckNow()
	select {
	case <-hitCh:
	case <-time.After(2 * time.Second):
		t.Fatal("TriggerCheckNow() 未立即触发休眠跨过的 09:35 补触发任务")
	}

	svc.Stop()
	hookMu.Lock()
	gotHooks := append([]bool(nil), hookEvents...)
	hookMu.Unlock()
	if len(gotHooks) < 2 || !gotHooks[0] || gotHooks[len(gotHooks)-1] {
		t.Fatalf("活跃任务回调序列异常（期望首为 true、Stop 后为 false）: %v", gotHooks)
	}
}

func TestBucketFamilyMatchingCoversRealBucketsAndPlaceholders(t *testing.T) {
	// 1. Claude / GPT 应命中真实 3p-5h / 3p-weekly 桶
	if !modelMatchesBucketFamily(buildSelectedModelSet([]string{"claude-sonnet-4-5"}), "3p-5h") {
		t.Fatal("claude 模型应匹配 3p-5h 共享桶")
	}
	if !modelMatchesBucketFamily(buildSelectedModelSet([]string{"gpt-oss-120b-medium"}), "3p-weekly") {
		t.Fatal("gpt 模型应匹配 3p-weekly 共享桶")
	}
	if modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-3.8-flash-high"}), "3p-5h") {
		t.Fatal("gemini-flash 模型不应匹配 3p-5h 桶")
	}

	// 2. 默认模型 gemini-3.8-flash-high 与 gemini-4-hydrogen 应命中 gemini-5h / gemini-weekly
	if !modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-3.8-flash-high"}), "gemini-5h") {
		t.Fatal("默认模型 gemini-3.8-flash-high 必须匹配 gemini-5h 桶")
	}
	if !modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-3.8-flash-high"}), "gemini:weekly") {
		t.Fatal("默认模型 gemini-3.8-flash-high 必须匹配 gemini:weekly 桶")
	}
	if !modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-4-hydrogen"}), "gemini-5h") {
		t.Fatal("gemini-4-hydrogen 必须匹配 gemini-5h 桶")
	}

	// 3. Argon / Carbon 互相隔离且不串台至 gemini-5h
	if !modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-4-argon-high"}), "gemini-argon-wtu-weekly") {
		t.Fatal("argon 模型应匹配 gemini-argon-wtu-weekly")
	}
	if modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-4-argon-high"}), "gemini-5h") {
		t.Fatal("argon 模型不应错配到 gemini-5h (Flash) 桶")
	}
	if !modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-4-carbon-high"}), "gemini-carbon-wtu-weekly") {
		t.Fatal("carbon 模型应匹配 gemini-carbon-wtu-weekly")
	}
	if modelMatchesBucketFamily(buildSelectedModelSet([]string{"gemini-4-carbon-high"}), "gemini-argon-wtu-weekly") {
		t.Fatal("carbon 模型不应错配到 argon 桶")
	}

	// 4. MODEL_PLACEHOLDER_* 结合 DisplayName 与内置兜底映射精准命中 SummaryBuckets
	quotaData := &model.QuotaData{
		Models: []model.ModelQuota{
			{Name: "MODEL_PLACEHOLDER_M525", DisplayName: "Gemini 4 Hydrogen (Low)", Percentage: 50, ResetTime: "2026-11-22T12:00:00Z"},
			{Name: "MODEL_PLACEHOLDER_M402", DisplayName: "", Percentage: 60, ResetTime: "2026-11-22T12:00:00Z"},
		},
		SummaryBuckets: []model.QuotaBucket{
			{BucketID: "gemini-5h", Percentage: 50, ResetTime: "2026-11-22T12:00:00Z"},
			{BucketID: "3p-5h", Percentage: 60, ResetTime: "2026-11-22T12:00:00Z"},
			{BucketID: "gemini-argon-wtu-weekly", Percentage: 90, ResetTime: "2026-11-28T00:00:00Z"},
		},
	}

	flashResets := collectTrackableResets(quotaData, buildSelectedModelSet([]string{"MODEL_PLACEHOLDER_M525"}))
	hasGemini5h := false
	has3p5h := false
	for _, r := range flashResets {
		if r.id == "bucket:gemini-5h" {
			hasGemini5h = true
		}
		if r.id == "bucket:3p-5h" {
			has3p5h = true
		}
	}
	if !hasGemini5h || has3p5h {
		t.Fatalf("MODEL_PLACEHOLDER_M525 应仅命中 bucket:gemini-5h，实际: %+v", flashResets)
	}

	claudeResets := collectTrackableResets(quotaData, buildSelectedModelSet([]string{"MODEL_PLACEHOLDER_M402"}))
	has3pForM402 := false
	for _, r := range claudeResets {
		if r.id == "bucket:3p-5h" {
			has3pForM402 = true
		}
	}
	if !has3pForM402 {
		t.Fatalf("MODEL_PLACEHOLDER_M402（无 displayName 兜底）应命中 bucket:3p-5h，实际: %+v", claudeResets)
	}
}

func TestQuotaResetPerAccountIsolationNoCollateralAndEdgeCases(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accA, err := store.UpsertAccount(model.Account{
		Email: "a@example.com",
		Token: model.TokenData{AccessToken: "at-a", RefreshToken: "rt-a", ExpiryTimestamp: time.Now().Unix() + 3600, ProjectID: "proj-a"},
		Quota: &model.QuotaData{
			Models: []model.ModelQuota{{Name: "gemini-3.8-flash-high", Percentage: 30, ResetTime: "2026-11-22T10:00:00Z"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	accB, err := store.UpsertAccount(model.Account{
		Email: "b@example.com",
		Token: model.TokenData{AccessToken: "at-b", RefreshToken: "rt-b", ExpiryTimestamp: time.Now().Unix() + 3600, ProjectID: "proj-b"},
		Quota: &model.QuotaData{
			Models: []model.ModelQuota{{Name: "gemini-3.8-flash-high", Percentage: 20, ResetTime: "2026-11-22T10:02:00Z"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wokenMu sync.Mutex
	wokenTokens := make([]string, 0)
	hitCh := make(chan string, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		wokenMu.Lock()
		wokenTokens = append(wokenTokens, auth)
		wokenMu.Unlock()
		hitCh <- auth
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}}\n\ndata: [DONE]\n"))
	}))
	defer srv.Close()
	targetURL, _ := url.Parse(srv.URL)

	task := model.WakeupTask{
		ID:      "task-multi-reset",
		Name:    "多号重置",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:       "quota_reset",
			WakeOnReset:      true,
			SelectedAccounts: []string{accA.ID, accB.ID},
			SelectedModels:   []string{"gemini-3.8-flash-high"},
		},
	}
	_ = store.SaveWakeupState(model.WakeupState{Enabled: true, Tasks: []model.WakeupTask{task}})

	svc := NewService(store, oauth.NewClient(), nil, nil)
	svc.SetTransport(&rewriteHostTransport{target: targetURL, base: srv.Client().Transport})
	baseTime := time.Date(2026, 11, 22, 9, 55, 0, 0, time.UTC)
	svc.nowFunc = func() time.Time { return baseTime }
	svc.SyncResetBaseline()

	// 1. 仅账号 A 配额回满 100% 且服务端清空 resetTime（未开窗）-> 应只唤醒账号 A，绝不连坐账号 B
	_, _ = store.UpdateAccount(accA.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = ""
		return nil
	})
	t1 := time.Date(2026, 11, 22, 10, 0, 10, 0, time.UTC)
	svc.tick(context.Background(), t1)

	select {
	case got := <-hitCh:
		if got != "Bearer at-a" {
			t.Fatalf("应仅唤醒账号 A，实际请求 Authorization=%s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("账号 A 配额回满且 resetTime 为空时未触发唤醒")
	}
	svc.wg.Wait()
	if len(hitCh) != 0 {
		t.Fatal("账号 A 重置不应连坐唤醒未重置的账号 B")
	}

	// 2. 仅 2 分钟后（账号 A 仍在 10 分钟冷却期内），账号 B 发生重置 -> 账号 B 独立冷却未占用，应立即触发唤醒 B
	_, _ = store.UpdateAccount(accB.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = "2026-11-22T15:02:00Z"
		return nil
	})
	t2 := time.Date(2026, 11, 22, 10, 2, 10, 0, time.UTC)
	svc.tick(context.Background(), t2)
	select {
	case got := <-hitCh:
		if got != "Bearer at-b" {
			t.Fatalf("账号 B 重置时应仅唤醒 B，实际 %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("账号 B 不应受账号 A 的 10 分钟冷却期阻塞")
	}
	svc.wg.Wait()

	// 3. 唤醒开启新窗口 / 滚动 resetTime 防二次误触发：
	// 账号 B 当前为 100% (15:02Z)，10:20Z（远未到 15:02Z）服务端返回滚动的 100% (15:20Z) -> 不应误触发
	_, _ = store.UpdateAccount(accB.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = "2026-11-22T15:20:00Z"
		return nil
	})
	t3 := time.Date(2026, 11, 22, 10, 20, 0, 0, time.UTC)
	if svc.checkQuotaResetTrigger(task, t3) {
		t.Fatal("高配额旧窗口未到期前的 resetTime 滚动/微调不应二次误触发")
	}

	// 等到 15:21Z（旧窗口 15:20Z 已真实到期）推进到 20:20Z -> 应正常触发下一轮重置
	_, _ = store.UpdateAccount(accB.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = "2026-11-22T20:20:00Z"
		return nil
	})
	t4 := time.Date(2026, 11, 22, 15, 21, 0, 0, time.UTC)
	if !svc.checkQuotaResetTrigger(task, t4) {
		t.Fatal("满额旧窗口自然到期后推进 5 小时应正常触发唤醒")
	}

	// 4. 撞锁不丢事件：模拟任务正被占用时账号 A 再次重置 -> tick 跳过且不消费基线，解锁后下轮 tick 正常触发
	_, _ = store.UpdateAccount(accA.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 40
		target.Quota.Models[0].ResetTime = "2026-11-22T16:00:00Z"
		return nil
	})
	svc.tick(context.Background(), time.Date(2026, 11, 22, 15, 30, 0, 0, time.UTC))

	// 人工占住 runningTasks 锁
	svc.mu.Lock()
	svc.runningTasks[task.ID] = func() {}
	svc.mu.Unlock()

	_, _ = store.UpdateAccount(accA.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = "2026-11-22T21:00:00Z"
		return nil
	})
	svc.tick(context.Background(), time.Date(2026, 11, 22, 16, 1, 0, 0, time.UTC))
	if len(hitCh) != 0 {
		t.Fatal("任务运行期间不应并发重复启动")
	}
	// 释放锁后下轮 tick 必须补触发，不能丢失
	svc.mu.Lock()
	delete(svc.runningTasks, task.ID)
	svc.mu.Unlock()

	svc.tick(context.Background(), time.Date(2026, 11, 22, 16, 1, 30, 0, time.UTC))
	select {
	case got := <-hitCh:
		if got != "Bearer at-a" {
			t.Fatalf("释放锁后应补触发账号 A，实际 %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("撞锁期间发生的配额重置在释放锁后被永久丢弃")
	}
	svc.wg.Wait()

	// 5. 停用→启用任务重置基线，防止停用期间旧重置在重启后误触发
	_, _ = store.UpdateAccount(accA.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 30
		target.Quota.Models[0].ResetTime = "2026-11-22T21:00:00Z"
		return nil
	})
	svc.ResetTaskBaseline(task.ID)

	// 停用任务
	disabledTask := task
	disabledTask.Enabled = false
	_ = store.SaveWakeupState(model.WakeupState{Enabled: true, Tasks: []model.WakeupTask{disabledTask}})
	svc.SyncResetBaseline()

	// 停用期间发生配额重置
	_, _ = store.UpdateAccount(accA.ID, func(target *model.Account) error {
		target.Quota.Models[0].Percentage = 100
		target.Quota.Models[0].ResetTime = "2026-11-23T02:00:00Z"
		return nil
	})

	// 重新启用任务并同步基线，应以新配额建基线，绝不立刻误触发
	_ = store.SaveWakeupState(model.WakeupState{Enabled: true, Tasks: []model.WakeupTask{task}})
	svc.SyncResetBaseline()
	if svc.checkQuotaResetTrigger(task, time.Date(2026, 11, 22, 21, 5, 0, 0, time.UTC)) {
		t.Fatal("任务从停用重新启用后，不应误触发停用期间发生的陈旧重置")
	}
}

func TestWithinTimeWindowSingleSideAndCrossMidnight(t *testing.T) {
	// 两端皆空 -> 全天生效
	if !withinTimeWindow("03:00", "", "") {
		t.Fatal("起止时间均为空应全天生效")
	}
	// 仅填开始时间 -> 等价 08:00–23:59
	if withinTimeWindow("03:00", "08:00", "") {
		t.Fatal("start=08:00 且 end 为空时，03:00 不应在窗口内")
	}
	if !withinTimeWindow("08:00", "08:00", "") || !withinTimeWindow("23:59", "08:00", "") {
		t.Fatal("start=08:00 且 end 为空时，08:00 与 23:59 均应在窗口内")
	}
	// 仅填结束时间 -> 等价 00:00–22:00
	if !withinTimeWindow("00:00", "", "22:00") || !withinTimeWindow("22:00", "", "22:00") {
		t.Fatal("start 为空且 end=22:00 时，00:00 与 22:00 均应在窗口内")
	}
	if withinTimeWindow("22:30", "", "22:00") {
		t.Fatal("start 为空且 end=22:00 时，22:30 不应在窗口内")
	}
	// 跨午夜 22:00–06:00
	if !withinTimeWindow("23:30", "22:00", "06:00") || !withinTimeWindow("02:15", "22:00", "06:00") {
		t.Fatal("跨午夜窗口 22:00–06:00 应命中 23:30 与 02:15")
	}
	if withinTimeWindow("12:00", "22:00", "06:00") {
		t.Fatal("跨午夜窗口 22:00–06:00 不应命中 12:00")
	}
	// 非零填充兼容规范化
	if !withinTimeWindow("10:00", "8:00", "22:00") {
		t.Fatal("非零填充 8:00 应规范化为 08:00 后正确命中 10:00")
	}
}

func TestPlaceholderProjectFilteredHTTP401RetryAndPostWakeupRefresh(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 账号携带占位 projectId="aicode-consumers"、旧 QuotaError、旧 IsForbidden，但在 Quota 中存有真实 ProjectID
	acc, err := store.UpsertAccount(model.Account{
		Email: "retry401@example.com",
		Token: model.TokenData{
			AccessToken:     "stale-at",
			RefreshToken:    "rt-401",
			ExpiryTimestamp: time.Now().Unix() + 3600,
			ProjectID:       "aicode-consumers",
		},
		Quota: &model.QuotaData{
			IsForbidden: true,
			ProjectID:   "real-gcp-project-99",
			Models: []model.ModelQuota{
				{Name: "gemini-3.8-flash-high", Percentage: 50, ResetTime: "2026-11-22T10:00:00Z"},
			},
		},
		QuotaError: &model.QuotaErrorInfo{
			Code:          403,
			Reason:        "VALIDATION_REQUIRED",
			Message:       "old error",
			ValidationURL: "https://accounts.google.com/old",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wakeupCalls atomic.Int32
	var refreshTokenCalls atomic.Int32
	var quotaFetchCalls atomic.Int32
	var seenBodyProject string
	var seenMu sync.Mutex
	onUpdatedCalls := make(chan struct{}, 4)

	// 模拟 CloudCode + Quota + OAuth 统一后端
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/oauth/token":
			refreshTokenCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "fresh-at-after-401",
				"expires_in":   3600,
				"token_type":   "Bearer",
			})
		case strings.Contains(r.URL.Path, "streamGenerateContent"):
			n := wakeupCalls.Add(1)
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			seenMu.Lock()
			seenBodyProject, _ = body["project"].(string)
			seenMu.Unlock()

			if n == 1 && r.Header.Get("Authorization") == "Bearer stale-at" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":401,"message":"Invalid Credentials"}}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer fresh-at-after-401" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok-retried\"}]}}]}}\n\ndata: [DONE]\n"))
		case strings.Contains(r.URL.Path, "loadCodeAssist"):
			_ = json.NewEncoder(w).Encode(map[string]any{"cloudaicompanionProject": "real-gcp-project-99"})
		case strings.Contains(r.URL.Path, "fetchAvailableModels"):
			quotaFetchCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"models": map[string]any{
					"gemini-3.8-flash-high": map[string]any{
						"displayName": "Gemini 3.8 Flash (High)",
						"quotaInfo": map[string]any{
							"remainingFraction": 0.99,
							"resetTime":         "2026-11-22T15:00:00Z",
						},
					},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{})
		}
	}))
	defer srv.Close()
	targetURL, _ := url.Parse(srv.URL)

	oauthCli := oauth.NewClient()
	oauthCli.TokenURL = srv.URL + "/oauth/token"
	oauthCli.HTTPClient = srv.Client()

	quotaCli := quota.NewClient()
	quotaCli.BaseURLOverride = srv.URL
	quotaCli.HTTPClient = srv.Client()
	quotaSvc := quota.NewService(store, oauthCli, quotaCli)

	svc := NewService(store, oauthCli, quotaSvc, func() {
		select {
		case onUpdatedCalls <- struct{}{}:
		default:
		}
	})
	svc.SetTransport(&rewriteHostTransport{target: targetURL, base: srv.Client().Transport})

	item, err := svc.TestNow(context.Background(), model.WakeupTestRequest{
		AccountID: acc.ID,
		Model:     "gemini-3.8-flash-high",
	})
	if err != nil {
		t.Fatalf("HTTP 401 强制刷新重试应成功，实际错误: %v", err)
	}
	if item == nil || !item.Success || item.Reply != "ok-retried" {
		t.Fatalf("重试结果异常: %+v", item)
	}
	if refreshTokenCalls.Load() != 1 || wakeupCalls.Load() != 2 {
		t.Fatalf("期望 1 次强制刷新令牌 + 2 次唤醒请求，实际 refresh=%d wakeup=%d", refreshTokenCalls.Load(), wakeupCalls.Load())
	}

	seenMu.Lock()
	gotProj := seenBodyProject
	seenMu.Unlock()
	if gotProj != "real-gcp-project-99" {
		t.Fatalf("应过滤 aicode-consumers 并复用有效 Quota.ProjectID，实际发送 project=%q", gotProj)
	}

	// 等待唤醒主回调与异步配额刷新落盘后的第二次 onUpdated 回调全部完成
	for i := 0; i < 2; i++ {
		select {
		case <-onUpdatedCalls:
		case <-time.After(3 * time.Second):
			t.Fatalf("等待第 %d 次 onUpdated（含异步配额刷新完成）超时", i+1)
		}
	}
	svc.Stop()

	updatedAcc, ok := store.GetAccount(acc.ID)
	if !ok {
		t.Fatal("账号读取失败")
	}
	if updatedAcc.Token.AccessToken != "fresh-at-after-401" {
		t.Fatalf("刷新后的新 AccessToken 未持久化: %q", updatedAcc.Token.AccessToken)
	}
	if updatedAcc.QuotaError != nil {
		t.Fatalf("唤醒成功后应清除旧 QuotaError，实际仍残留: %+v", updatedAcc.QuotaError)
	}
	if updatedAcc.Quota == nil || updatedAcc.Quota.IsForbidden {
		t.Fatalf("唤醒成功后应清除旧 IsForbidden 标志，实际: %+v", updatedAcc.Quota)
	}
	if quotaFetchCalls.Load() < 1 {
		t.Fatal("应至少执行一次唤醒后配额刷新")
	}
}

func TestExecuteTaskConcurrencyCancelPenetrationAndLastRunPrecision(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	var accIDs []string
	for i := 0; i < 4; i++ {
		acc, uErr := store.UpsertAccount(model.Account{
			Email: "c" + string(rune('0'+i)) + "@example.com",
			Token: model.TokenData{
				AccessToken:     "at",
				RefreshToken:    "rt",
				ExpiryTimestamp: time.Now().Unix() + 3600,
				ProjectID:       "proj-c",
			},
		})
		if uErr != nil {
			t.Fatal(uErr)
		}
		accIDs = append(accIDs, acc.ID)
	}

	// 1. 验证并发执行（多账号同时在途，非串行）且手动执行不污染 LastRunAt
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32
	allArrived := make(chan struct{})
	var arrivedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		if cur >= 2 {
			arrivedOnce.Do(func() { close(allArrived) })
		}
		select {
		case <-allArrived:
		case <-time.After(500 * time.Millisecond):
		case <-r.Context().Done():
			inFlight.Add(-1)
			return
		}
		inFlight.Add(-1)
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}}\n\ndata: [DONE]\n"))
	}))
	defer srv.Close()
	targetURL, _ := url.Parse(srv.URL)

	task := model.WakeupTask{
		ID:      "task-conc",
		Name:    "并发任务",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:       "interval",
			IntervalHours:    4,
			SelectedAccounts: accIDs,
			SelectedModels:   []string{"gemini-3.8-flash-high"},
		},
	}
	_ = store.SaveWakeupState(model.WakeupState{Enabled: true, Tasks: []model.WakeupTask{task}})

	svc := NewService(store, oauth.NewClient(), nil, nil)
	svc.SetTransport(&rewriteHostTransport{target: targetURL, base: srv.Client().Transport})

	results, err := svc.RunTaskNow(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("手动执行并发任务失败: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("期望 4 条结果，实际 %d", len(results))
	}
	if maxInFlight.Load() < 2 {
		t.Fatalf("多账号任务应并发执行，实际最大并发仅 %d", maxInFlight.Load())
	}
	if got := store.GetWakeupState().Tasks[0].LastRunAt; got != 0 {
		t.Fatalf("手动执行（manual）不应污染定时周期 LastRunAt，实际 %d", got)
	}

	// 2. 验证 CancelTask 能即时取消在途任务且不覆写 LastRunAt（外层与 worker 循环均穿透退出）
	blockStarted := make(chan struct{})
	releaseBlock := make(chan struct{})
	var blockOnce sync.Once
	blockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		blockOnce.Do(func() { close(blockStarted) })
		select {
		case <-r.Context().Done():
		case <-releaseBlock:
		}
	}))
	defer blockSrv.Close()
	defer close(releaseBlock)
	blockURL, _ := url.Parse(blockSrv.URL)
	svc.SetTransport(&rewriteHostTransport{target: blockURL, base: blockSrv.Client().Transport})

	doneCh := make(chan struct{})
	go func() {
		_, _ = svc.executeTask(context.Background(), task, "scheduled")
		close(doneCh)
	}()
	<-blockStarted
	if !svc.CancelTask(task.ID) {
		t.Fatal("CancelTask 应命中正在运行的任务并返回 true")
	}
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("CancelTask 未能及时中止在途任务")
	}
	if got := store.GetWakeupState().Tasks[0].LastRunAt; got != 0 {
		t.Fatalf("被取消的任务不应覆写 LastRunAt，实际 %d", got)
	}

	// 3. 验证全禁用账号定时触发（0 次实际执行）不覆写 LastRunAt
	disabledAcc, _ := store.UpsertAccount(model.Account{
		Email:    "disabled@example.com",
		Disabled: true,
		Token:    model.TokenData{RefreshToken: "rt-dis"},
	})
	zeroTask := model.WakeupTask{
		ID:      "task-zero",
		Name:    "全禁用任务",
		Enabled: true,
		Schedule: model.WakeupSchedule{
			RepeatMode:       "daily",
			DailyTimes:       []string{"09:00"},
			SelectedAccounts: []string{disabledAcc.ID},
			SelectedModels:   []string{"gemini-3.8-flash-high"},
		},
	}
	_ = store.SaveWakeupState(model.WakeupState{Enabled: true, Tasks: []model.WakeupTask{zeroTask}})
	zeroRes, started := svc.executeTask(context.Background(), zeroTask, "scheduled")
	if !started || len(zeroRes) != 0 {
		t.Fatalf("全禁用账号定时任务应返回 0 条结果，实际 started=%v len=%d", started, len(zeroRes))
	}
	if got := store.GetWakeupState().Tasks[0].LastRunAt; got != 0 {
		t.Fatalf("0 次实际执行的定时任务不应覆写 LastRunAt，实际 %d", got)
	}
}

func TestStreamInlineErrorPlaceholderThinkingClaudePayloadAndSplitUsage(t *testing.T) {
	// 1. HTTP 200 SSE 流内嵌 JSON error 应提取结构化 StreamAPIError 与 ValidationURL
	inlineErrSSE := `data: {"error":{"code":403,"message":"Account verification required.","details":[{"reason":"VALIDATION_REQUIRED","metadata":{"validation_url":"https://accounts.google.com/verify/inline-sse"}}]}}` + "\n\ndata: [DONE]\n"
	_, err := ParseStreamResult(inlineErrSSE)
	if err == nil {
		t.Fatal("流内嵌 error 对象应当返回错误")
	}
	var streamErr *StreamAPIError
	if !errors.As(err, &streamErr) {
		t.Fatalf("期望返回 *StreamAPIError，实际 %T: %v", err, err)
	}
	if streamErr.Info.Code != 403 || streamErr.Info.Reason != "VALIDATION_REQUIRED" ||
		streamErr.Info.ValidationURL != "https://accounts.google.com/verify/inline-sse" {
		t.Fatalf("流内嵌错误解析不准确: %+v", streamErr.Info)
	}

	// 2. 分片拆分上报 thoughtsTokenCount 与 candidatesTokenCount 应分别取最大值后求和
	splitUsageSSE := "" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"think\",\"thought\":true}]}}],\"usageMetadata\":{\"promptTokenCount\":15,\"thoughtsTokenCount\":18,\"totalTokenCount\":33}}}\n\n" +
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Done\"}]}}],\"usageMetadata\":{\"promptTokenCount\":15,\"candidatesTokenCount\":9,\"totalTokenCount\":42}}}\n\n" +
		"data: [DONE]\n"
	parsed, err := ParseStreamResult(splitUsageSSE)
	if err != nil {
		t.Fatalf("解析拆分 usage 流失败: %v", err)
	}
	if parsed.Reply != "Done" || parsed.CompletionTokens != 27 || parsed.TotalTokens != 42 {
		t.Fatalf("期望 CompletionTokens=18+9=27，实际 %+v", parsed)
	}

	// 3. 占位思考模型自动抬升 maxOutputTokens 至 256，Claude 思考模型省略 temperature=0
	placeholderPayload := buildWakeupPayload("aicode-consumers", "MODEL_PLACEHOLDER_M525", "hi", 32, "Gemini 4 Hydrogen (Low)")
	if _, hasProj := placeholderPayload["project"]; hasProj {
		t.Fatalf("占位 projectId aicode-consumers 不应写入 payload: %+v", placeholderPayload)
	}
	reqObj := placeholderPayload["request"].(map[string]any)
	genCfg := reqObj["generationConfig"].(map[string]any)
	if genCfg["maxOutputTokens"] != minThinkingModelOutputToken {
		t.Fatalf("MODEL_PLACEHOLDER_* 应抬升 maxOutputTokens=%d，实际 %v", minThinkingModelOutputToken, genCfg["maxOutputTokens"])
	}
	if _, hasTemp := genCfg["temperature"]; !hasTemp {
		t.Fatal("非 Claude 模型应保留 temperature=0")
	}

	claudePayload := buildWakeupPayload("proj-1", "claude-opus-5-5-high", "hi", 64)
	claudeGenCfg := claudePayload["request"].(map[string]any)["generationConfig"].(map[string]any)
	if _, hasTemp := claudeGenCfg["temperature"]; hasTemp {
		t.Fatalf("Claude 思考模型不应发送 temperature=0: %+v", claudeGenCfg)
	}
	if claudeGenCfg["maxOutputTokens"] != minThinkingModelOutputToken {
		t.Fatalf("Claude 思考模型应抬升 maxOutputTokens=%d，实际 %v", minThinkingModelOutputToken, claudeGenCfg["maxOutputTokens"])
	}
}

func TestValidateCronExprAndWakeupState(t *testing.T) {
	validCrons := []string{
		"0 9 * * *",
		"0 9 * * 1-5",
		"*/15 * * * *",
		"5/15 9 * * 7",
		"0 0 1 1 0-7",
		"0,30 8-20/2 * 1-12 1,3,5",
	}
	for _, expr := range validCrons {
		if err := ValidateCronExpr(expr); err != nil {
			t.Fatalf("合法 Cron %q 被误判非法: %v", expr, err)
		}
	}

	invalidCrons := []string{
		"",
		"0 9 * *",
		"0 9 * * * *",
		"99 99 99 99 99",
		"*/0 * * * *",
		"*/60 * * * *",
		"0 22-6 * * *",
		"0 20-30 * * *",
		"1,,2 * * * *",
		"1-2-3 * * * *",
		"1/2/3 * * * *",
		"- - - - -",
		"0 9 0 * *",
		"0 9 * 13 *",
		"0 9 * * 8",
	}
	sampleTime := time.Date(2026, 11, 22, 22, 0, 0, 0, time.Local)
	for _, expr := range invalidCrons {
		if err := ValidateCronExpr(expr); err == nil {
			t.Fatalf("非法 Cron %q 应当校验失败", expr)
		}
		if matchFiveFieldCron(expr, sampleTime) {
			t.Fatalf("非法 Cron %q 不应发生越界钳位误匹配", expr)
		}
	}

	// ValidateWakeupState 校验测试
	if err := ValidateWakeupState(model.WakeupState{
		Enabled: true,
		Tasks: []model.WakeupTask{
			{
				Name: "合法间隔任务",
				Schedule: model.WakeupSchedule{
					RepeatMode:        "interval",
					IntervalHours:     4,
					IntervalStartTime: "08:00",
				},
			},
		},
	}); err != nil {
		t.Fatalf("合法单边窗口间隔任务校验应通过，实际: %v", err)
	}

	badStates := []model.WakeupState{
		{Tasks: []model.WakeupTask{{Name: "", Schedule: model.WakeupSchedule{RepeatMode: "quota_reset"}}}},
		{Tasks: []model.WakeupTask{{Name: "非法模式", Schedule: model.WakeupSchedule{RepeatMode: "yearly"}}}},
		{Tasks: []model.WakeupTask{{Name: "非法时间", Schedule: model.WakeupSchedule{RepeatMode: "daily", DailyTimes: []string{"25:00"}}}}},
		{Tasks: []model.WakeupTask{{Name: "非法间隔", Schedule: model.WakeupSchedule{RepeatMode: "interval", IntervalHours: 0}}}},
		{Tasks: []model.WakeupTask{{Name: "非法Cron", Schedule: model.WakeupSchedule{RepeatMode: "crontab", Crontab: "0 20-30 * * *"}}}},
	}
	for i, st := range badStates {
		if err := ValidateWakeupState(st); err == nil {
			t.Fatalf("第 %d 个非法 WakeupState 应当校验失败", i)
		}
	}
}

func TestSetTransportConcurrentSafeAndStopWaitsGracefully(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email: "race@example.com",
		Token: model.TokenData{
			AccessToken:     "at-race",
			RefreshToken:    "rt-race",
			ExpiryTimestamp: time.Now().Unix() + 3600,
			ProjectID:       "proj-race",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}}\n\ndata: [DONE]\n"))
	}))
	defer srv.Close()
	targetURL, _ := url.Parse(srv.URL)

	svc := NewService(store, oauth.NewClient(), nil, nil)
	svc.Start(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			svc.SetTransport(&rewriteHostTransport{target: targetURL, base: srv.Client().Transport})
		}()
		go func() {
			defer wg.Done()
			_, _ = svc.TestNow(context.Background(), model.WakeupTestRequest{
				AccountID: acc.ID,
				Model:     "gemini-3.8-flash-high",
			})
		}()
	}
	wg.Wait()
	svc.Stop()
}
