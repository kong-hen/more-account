package quota

import (
	"testing"
	"time"

	"more-account/internal/model"
)

func TestSelectDueAccountIDs(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
	activeInterval := 5 * time.Minute
	inactiveInterval := 60 * time.Minute

	accounts := []model.Account{
		{
			// 已失效账号应跳过
			ID:       "disabled-acc",
			Disabled: true,
		},
		{
			// 当前使用账号，已超过 5 分钟，应刷新
			ID: "active-stale-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-6 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 80}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 80}},
			},
		},
		{
			// 未使用账号，仅过去 20 分钟（< 60 分钟），不应刷新
			ID: "inactive-fresh-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-20 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 90}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 90, ResetTime: now.Add(2 * time.Hour).Format(time.RFC3339)}},
			},
		},
		{
			// 未使用账号，已超过 60 分钟（65 分钟 > 60 分钟），应刷新
			ID: "inactive-stale-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-65 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 75}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 75}},
			},
		},
		{
			// 未使用账号虽仅过去 10 分钟，但 5h 重置窗口刚刚到期（超过 5 秒宽限期），应立即刷新
			ID: "reset-passed-acc",
			Quota: &model.QuotaData{
				LastUpdated: now.Add(-10 * time.Minute).Unix(),
				Models:      []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 0}},
				SummaryBuckets: []model.QuotaBucket{
					{BucketID: "gemini-5h", Percentage: 0, ResetTime: now.Add(-10 * time.Second).Format(time.RFC3339)},
				},
			},
		},
		{
			// 缺少 summaryBuckets 的旧缓存，首次应立即补拉
			ID: "missing-summary-acc",
			Quota: &model.QuotaData{
				LastUpdated: now.Add(-30 * time.Second).Unix(),
				Models:      []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 100}},
			},
		},
		{
			// 已尝试过一次补拉仍无 summaryBuckets 的账号，未到间隔不应重复补拉
			ID: "already-backfilled-acc",
			Quota: &model.QuotaData{
				LastUpdated: now.Add(-30 * time.Second).Unix(),
				Models:      []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 100}},
			},
		},
		{
			// 未使用账号最近 5 分钟刚失败（未过 10 分钟退避上限），不应高频重试
			ID: "recent-error-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-90 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 50}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 50}},
			},
			QuotaError: &model.QuotaErrorInfo{
				Message:   "network timeout",
				Timestamp: now.Add(-5 * time.Minute).Unix(),
			},
		},
		{
			// 未使用账号失败已超过 10 分钟退避上限（12 分钟 > 10 分钟），应重新尝试刷新
			ID: "expired-error-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-90 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 50}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 50}},
			},
			QuotaError: &model.QuotaErrorInfo{
				Message:   "network timeout",
				Timestamp: now.Add(-12 * time.Minute).Unix(),
			},
		},
	}

	tried := map[string]bool{
		"already-backfilled-acc": true,
	}
	due, backfill := selectDueAccountIDs(accounts, "active-stale-acc", now, activeInterval, inactiveInterval, tried)
	want := map[string]bool{
		"active-stale-acc":    true,
		"inactive-stale-acc":  true,
		"reset-passed-acc":    true,
		"missing-summary-acc": true,
		"expired-error-acc":   true,
	}
	if len(due) != len(want) {
		t.Fatalf("期望筛选出 %d 个到期账号，实际 %d: %v", len(want), len(due), due)
	}
	for _, id := range due {
		if !want[id] {
			t.Fatalf("意外选入账号: %s (all=%v)", id, due)
		}
	}
	if len(backfill) != 1 || backfill[0] != "missing-summary-acc" {
		t.Fatalf("期望仅记录 missing-summary-acc 为补拉账号，实际: %v", backfill)
	}
}

func TestSelectDueAccountIDsIndependentToggle(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
	accounts := []model.Account{
		{
			ID: "active-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-2 * time.Hour).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 80}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 80}},
			},
		},
		{
			ID: "inactive-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-2 * time.Hour).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 80}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 80}},
			},
		},
	}

	// 仅开启当前账号刷新，关闭未使用账号刷新
	due, _ := selectDueAccountIDs(accounts, "active-acc", now, 5*time.Minute, 0, nil)
	if len(due) != 1 || due[0] != "active-acc" {
		t.Fatalf("期望仅刷新 active-acc，实际 %v", due)
	}

	// 关闭当前账号刷新，仅开启未使用账号刷新
	due, _ = selectDueAccountIDs(accounts, "active-acc", now, 0, 60*time.Minute, nil)
	if len(due) != 1 || due[0] != "inactive-acc" {
		t.Fatalf("期望仅刷新 inactive-acc，实际 %v", due)
	}
}
