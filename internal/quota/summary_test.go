package quota

import (
	"testing"
	"time"

	"more-account/internal/model"
)

func TestSummarizeAccountCategoriesProDualWindow(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	acc := model.Account{
		ID:    "acc-pro",
		Email: "alice@example.com",
		Quota: &model.QuotaData{
			SubscriptionTier: "PRO",
			SummaryBuckets: []model.QuotaBucket{
				{BucketID: "3p-5h", Percentage: 72, ResetTime: now.Add(95 * time.Minute).Format(time.RFC3339)},
				{BucketID: "3p-weekly", Percentage: 88, ResetTime: now.Add(4 * 24 * time.Hour).Format(time.RFC3339)},
				{BucketID: "gemini-flash-5h", Percentage: 90, ResetTime: now.Add(2 * time.Hour).Format(time.RFC3339)},
				{BucketID: "gemini-flash-weekly", Percentage: 95, ResetTime: now.Add(5 * 24 * time.Hour).Format(time.RFC3339)},
				{BucketID: "gemini-argon-wtu-weekly", Percentage: 100, ResetTime: now.Add(6 * 24 * time.Hour).Format(time.RFC3339)},
				{BucketID: "gemini-carbon-wtu-weekly", Percentage: 85, ResetTime: now.Add(3 * 24 * time.Hour).Format(time.RFC3339)},
			},
			Models: []model.ModelQuota{
				{Name: "claude-sonnet-4-6-thinking", DisplayName: "Claude Sonnet 4.6", Percentage: 72},
				{Name: "gemini-3-flash-high", DisplayName: "Gemini 3 Flash", Percentage: 5}, // 非反重力客户端模型，应过滤
			},
		},
	}

	cats := SummarizeAccountCategories(acc, now)
	if len(cats) != 4 {
		t.Fatalf("期望汇总出 4 个核心分类，实际 %d: %+v", len(cats), cats)
	}
	wantOrder := []string{"gemini-argon", "gemini-carbon", "gemini-flash", "claude-gpt"}
	for i, key := range wantOrder {
		if cats[i].Key != key {
			t.Fatalf("第 %d 个分类期望 %q，实际 %q", i, key, cats[i].Key)
		}
	}
	if cats[2].FiveHour == nil || cats[2].FiveHour.Percentage != 90 || cats[2].Weekly == nil || cats[2].Weekly.Percentage != 95 {
		t.Fatalf("Gemini Flash 应同时保留 5h(90%%) 与周额度(95%%)，实际 %+v", cats[2])
	}
	if cats[3].FiveHour == nil || cats[3].FiveHour.Percentage != 72 || cats[3].Weekly == nil || cats[3].Weekly.Percentage != 88 {
		t.Fatalf("Claude/GPT 应保留双窗口，实际 %+v", cats[3])
	}
	if gotMin := MinQuotaPercentage(acc, now); gotMin != 72 {
		t.Fatalf("排除非客户端模型后最低配额应为 72，实际 %d", gotMin)
	}
}

func TestSummarizeAccountCategoriesFreeHidesFiveHourAndFallback(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	freeAcc := model.Account{
		Email: "free@example.com",
		Quota: &model.QuotaData{
			SubscriptionTier: "FREE",
			SummaryBuckets: []model.QuotaBucket{
				{BucketID: "3p-5h", Percentage: 50, ResetTime: now.Add(2 * time.Hour).Format(time.RFC3339)},
				{BucketID: "3p-weekly", Percentage: 40, ResetTime: now.Add(3 * 24 * time.Hour).Format(time.RFC3339)},
			},
		},
	}
	cats := SummarizeAccountCategories(freeAcc, now)
	if len(cats) != 1 {
		t.Fatalf("期望 1 个分类，实际 %d", len(cats))
	}
	if cats[0].FiveHour != nil {
		t.Fatalf("FREE 账号已有周额度时不应展示 5h 额度，实际 %+v", cats[0].FiveHour)
	}
	if cats[0].Weekly == nil || cats[0].Weekly.Percentage != 40 {
		t.Fatalf("期望保留周额度 40%%，实际 %+v", cats[0].Weekly)
	}

	// 验证无 summaryBuckets 时从 models 兜底推导，且忽略被排除的旧模型
	fallbackAcc := model.Account{
		Email: "fallback@example.com",
		Quota: &model.QuotaData{
			SubscriptionTier: "ULTRA",
			Models: []model.ModelQuota{
				{Name: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", Percentage: 0},
				{Name: "gemini-3.1-flash-lite", DisplayName: "Gemini 3.1 Flash Lite", Percentage: 0},
				{Name: "gemini-4-neon-high", DisplayName: "Gemini 4 Neon (High)", Percentage: 82, ResetTime: now.Add(3 * time.Hour).Format(time.RFC3339)},
				{Name: "gemini-4-argon-high", DisplayName: "Gemini 4 Argon (High)", Percentage: 64, ResetTime: now.Add(48 * time.Hour).Format(time.RFC3339)},
			},
		},
	}
	fbCats := SummarizeAccountCategories(fallbackAcc, now)
	if len(fbCats) != 2 {
		t.Fatalf("兜底推导期望 2 个分类，实际 %d: %+v", len(fbCats), fbCats)
	}
	if fbCats[0].Key != "gemini-argon" || fbCats[0].Weekly == nil || fbCats[0].Weekly.Percentage != 64 {
		t.Fatalf("Argon 兜底应归入周额度 64%%，实际 %+v", fbCats[0])
	}
	if fbCats[1].Key != "gemini-flash" || fbCats[1].FiveHour == nil || fbCats[1].FiveHour.Percentage != 82 {
		t.Fatalf("Neon 兜底应归入 5h 额度 82%%，实际 %+v", fbCats[1])
	}
	if MinQuotaPercentage(model.Account{}, now) != -1 {
		t.Fatal("无配额数据时 MinQuotaPercentage 应返回 -1")
	}
}

func TestIsAccountAbnormalCountdownAndMaskEmail(t *testing.T) {
	if IsAccountAbnormal(model.Account{Email: "ok@example.com"}) {
		t.Fatal("正常账号不应判为异常")
	}
	if !IsAccountAbnormal(model.Account{Disabled: true}) {
		t.Fatal("禁用账号应判为异常")
	}
	if !IsAccountAbnormal(model.Account{Quota: &model.QuotaData{IsForbidden: true}}) {
		t.Fatal("403 封禁账号应判为异常")
	}
	if !IsAccountAbnormal(model.Account{QuotaError: &model.QuotaErrorInfo{Reason: "VALIDATION_REQUIRED"}}) {
		t.Fatal("需要验证账号应判为异常")
	}
	if !IsAccountAbnormal(model.Account{QuotaError: &model.QuotaErrorInfo{Message: "quota error"}}) {
		t.Fatal("带配额报错账号应判为异常")
	}

	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	if got := FormatResetCountdown("", now); got != "—" {
		t.Fatalf("空时间期望 —，实际 %q", got)
	}
	if got := FormatResetCountdown(now.Add(-time.Minute).Format(time.RFC3339), now); got != "即将重置" {
		t.Fatalf("已过期期望 即将重置，实际 %q", got)
	}
	if got := FormatResetCountdown(now.Add(25*time.Minute).Format(time.RFC3339), now); got != "25m" {
		t.Fatalf("期望 25m，实际 %q", got)
	}
	if got := FormatResetCountdown(now.Add(2*time.Hour+14*time.Minute).Format(time.RFC3339), now); got != "2h 14m" {
		t.Fatalf("期望 2h 14m，实际 %q", got)
	}
	if got := FormatResetCountdown(now.Add(4*24*time.Hour+12*time.Hour).Format(time.RFC3339), now); got != "4d 12h" {
		t.Fatalf("期望 4d 12h，实际 %q", got)
	}

	if got := MaskEmail("alice@example.com", false); got != "alice@example.com" {
		t.Fatalf("关闭脱敏应原样返回，实际 %q", got)
	}
	if got := MaskEmail("alice@example.com", true); got != "al***@example.com" {
		t.Fatalf("期望 al***@example.com，实际 %q", got)
	}
	if got := MaskEmail("ab@example.com", true); got != "a*@example.com" {
		t.Fatalf("期望 a*@example.com，实际 %q", got)
	}
	if got := MaskEmail("no-at", true); got != "no***" {
		t.Fatalf("期望 no***，实际 %q", got)
	}
}

func TestExportedClassifiersAndFreeMinQuotaAlignment(t *testing.T) {
	// 1. 验证导出的跨模块配额桶与模型家族归类函数
	bucketCases := map[string]string{
		"3p-5h":                    "claude-gpt",
		"3p-weekly":                "claude-gpt",
		"gemini-5h":                "gemini-flash",
		"gemini-weekly":            "gemini-flash",
		"gemini-argon-wtu-weekly":  "gemini-argon",
		"gemini-carbon-wtu-weekly": "gemini-carbon",
		"unknown-bucket":           "",
	}
	for bid, want := range bucketCases {
		if got := ClassifySummaryBucket(bid); got != want {
			t.Fatalf("ClassifySummaryBucket(%q) = %q，期望 %q", bid, got, want)
		}
	}
	if got := ClassifyModelCategory("MODEL_PLACEHOLDER_M525", "Gemini 4 Hydrogen (Low)"); got != "gemini-flash" {
		t.Fatalf("占位模型 Hydrogen 应归入 gemini-flash，实际 %q", got)
	}
	if got := ClassifyModelCategory("claude-sonnet-4-6-thinking", "Claude Sonnet 4.6"); got != "claude-gpt" {
		t.Fatalf("Claude 模型应归入 claude-gpt，实际 %q", got)
	}

	// 2. 验证 FREE 账号隐藏 5h 仅展示周额度（90%）时，MinQuotaPercentage 与展示分类保持一致（不被 models 中的 10% 拉低）
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	freeAcc := model.Account{
		Email: "free-align@example.com",
		Quota: &model.QuotaData{
			SubscriptionTier: "FREE",
			SummaryBuckets: []model.QuotaBucket{
				{BucketID: "3p-5h", Percentage: 10, ResetTime: now.Add(2 * time.Hour).Format(time.RFC3339)},
				{BucketID: "3p-weekly", Percentage: 90, ResetTime: now.Add(5 * 24 * time.Hour).Format(time.RFC3339)},
			},
			Models: []model.ModelQuota{
				{Name: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6", Percentage: 10},
			},
		},
	}
	if got := MinQuotaPercentage(freeAcc, now); got != 90 {
		t.Fatalf("FREE 账号已由周额度权威展示时，MinQuotaPercentage 应为 90，实际 %d", got)
	}

	// 3. 若存在未被任何展示分类覆盖的孤立未知模型，仍应参与最低值计算
	freeAcc.Quota.Models = append(freeAcc.Quota.Models, model.ModelQuota{
		Name:        "custom-orphan-model",
		DisplayName: "Custom Orphan",
		Percentage:  15,
	})
	if got := MinQuotaPercentage(freeAcc, now); got != 15 {
		t.Fatalf("未归类的孤立模型（15%%）仍应参与最低值计算，实际 %d", got)
	}
}
