package quota

import (
	"testing"
	"time"

	"more-account/internal/model"
)

func TestSelectDueAccountIDs(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
	interval := 5 * time.Minute

	accounts := []model.Account{
		{
			// 已失效账号应跳过
			ID:       "disabled-acc",
			Disabled: true,
		},
		{
			// 刚刷新过且有 summaryBuckets，尚未到期，不刷新
			ID: "fresh-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-2 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 90}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 90, ResetTime: now.Add(2 * time.Hour).Format(time.RFC3339)}},
			},
		},
		{
			// 超过自动刷新间隔（6 分钟 > 5 分钟），应刷新
			ID: "stale-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-6 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 80}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 80}},
			},
		},
		{
			// 虽仅过去 1 分钟，但 5h 重置窗口刚刚到期（超过 5 秒宽限期），应立即刷新
			ID: "reset-passed-acc",
			Quota: &model.QuotaData{
				LastUpdated: now.Add(-1 * time.Minute).Unix(),
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
			// 最近 1 分钟刚拉取失败的账号处于退避期，不应高频重试
			ID: "recent-error-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-10 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 50}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 50}},
			},
			QuotaError: &model.QuotaErrorInfo{
				Message:   "network timeout",
				Timestamp: now.Add(-1 * time.Minute).Unix(),
			},
		},
		{
			// 失败已超过退避间隔（6 分钟 > 5 分钟），应重新尝试刷新
			ID: "expired-error-acc",
			Quota: &model.QuotaData{
				LastUpdated:    now.Add(-15 * time.Minute).Unix(),
				Models:         []model.ModelQuota{{Name: "gemini-4-neon-high", Percentage: 50}},
				SummaryBuckets: []model.QuotaBucket{{BucketID: "gemini-5h", Percentage: 50}},
			},
			QuotaError: &model.QuotaErrorInfo{
				Message:   "network timeout",
				Timestamp: now.Add(-6 * time.Minute).Unix(),
			},
		},
	}

	tried := map[string]bool{
		"already-backfilled-acc": true,
	}
	due, backfill := selectDueAccountIDs(accounts, now, interval, tried)
	want := map[string]bool{
		"stale-acc":           true,
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
