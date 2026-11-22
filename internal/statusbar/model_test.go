package statusbar

import (
	"strings"
	"testing"
	"time"

	"more-account/internal/model"
)

func sampleAccounts(now time.Time) []model.Account {
	return []model.Account{
		{
			ID:    "acc-1",
			Email: "alice@example.com",
			Quota: &model.QuotaData{
				SubscriptionTier: "PRO",
				SummaryBuckets: []model.QuotaBucket{
					{BucketID: "gemini-argon-wtu-weekly", Percentage: 100, ResetTime: now.Add(4*24*time.Hour + 12*time.Hour).Format(time.RFC3339)},
					{BucketID: "gemini-flash-5h", Percentage: 90, ResetTime: now.Add(2*time.Hour + 15*time.Minute).Format(time.RFC3339)},
					{BucketID: "gemini-flash-weekly", Percentage: 95, ResetTime: now.Add(5 * 24 * time.Hour).Format(time.RFC3339)},
					{BucketID: "3p-5h", Percentage: 72, ResetTime: now.Add(1*time.Hour + 40*time.Minute).Format(time.RFC3339)},
					{BucketID: "3p-weekly", Percentage: 88, ResetTime: now.Add(5 * 24 * time.Hour).Format(time.RFC3339)},
				},
			},
		},
		{
			ID:    "acc-2",
			Email: "bob@example.com",
			Quota: &model.QuotaData{
				SubscriptionTier: "ULTRA",
				SummaryBuckets: []model.QuotaBucket{
					{BucketID: "3p-5h", Percentage: 100, ResetTime: now.Add(5 * time.Hour).Format(time.RFC3339)},
				},
			},
		},
		{
			ID:       "acc-3",
			Email:    "broken@example.com",
			Disabled: true,
		},
	}
}

func TestBuildSnapshotModesAndQuotaSummary(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	accounts := sampleAccounts(now)

	// 1. off 模式隐藏状态栏
	offSnap := BuildSnapshot(BuildInput{
		Accounts:         accounts,
		CurrentAccountID: "acc-1",
		Settings: model.AppSettings{
			MacStatusBarMode: model.MacStatusBarOff,
		},
		Now: now,
	})
	if offSnap.Visible {
		t.Fatal("off 模式下状态栏应不可见")
	}

	// 2. icon_only 模式仅显示图标、不附带文字
	iconOnly := BuildSnapshot(BuildInput{
		Accounts:         accounts,
		CurrentAccountID: "acc-1",
		Settings: model.AppSettings{
			MacStatusBarMode: model.MacStatusBarIconOnly,
		},
		Now: now,
	})
	if !iconOnly.Visible || iconOnly.ButtonTitle != "" {
		t.Fatalf("icon_only 模式期望 visible=true 且 title 为空，实际 visible=%v title=%q", iconOnly.Visible, iconOnly.ButtonTitle)
	}

	// 3. icon_quota 默认模式显示最低额度百分比及完整配额明细
	snap := BuildSnapshot(BuildInput{
		Accounts:         accounts,
		CurrentAccountID: "acc-1",
		Settings: model.AppSettings{
			MacStatusBarMode: model.MacStatusBarIconQuota,
			PrivacyMode:      false,
		},
		Now: now,
	})
	if !snap.Visible {
		t.Fatal("icon_quota 模式状态栏应可见")
	}
	if snap.ButtonTitle != "72%" {
		t.Fatalf("期望菜单栏标题为 72%%，实际 %q", snap.ButtonTitle)
	}
	if !strings.Contains(snap.CurrentHeader, "alice@example.com") || !strings.Contains(snap.CurrentHeader, "[PRO]") {
		t.Fatalf("当前账号头部信息不符合预期: %q", snap.CurrentHeader)
	}
	if len(snap.Categories) != 3 {
		t.Fatalf("期望 3 条配额分类明细，实际 %d: %+v", len(snap.Categories), snap.Categories)
	}
	if !strings.Contains(snap.Categories[1].Text, "Gemini Flash") || !strings.Contains(snap.Categories[1].Text, "5h 90%") || !strings.Contains(snap.Categories[1].Text, "周 95%") {
		t.Fatalf("Gemini Flash 双窗口文案异常: %q", snap.Categories[1].Text)
	}
	if len(snap.Accounts) != 3 || !snap.Accounts[0].Active || snap.Accounts[1].Active {
		t.Fatalf("快速切换账号列表激活状态异常: %+v", snap.Accounts)
	}
	if !strings.Contains(snap.Accounts[2].Title, "异常") {
		t.Fatalf("禁用账号应在切换列表中标注「异常」，实际 %q", snap.Accounts[2].Title)
	}
	if !snap.CanRefreshCurrent || !snap.CanRefreshAll {
		t.Fatal("有激活账号且空闲时应允许刷新当前与全部账号")
	}
}

func TestBuildSnapshotPrivacyAbnormalAndBusyStates(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	accounts := sampleAccounts(now)

	// 异常账号 + 隐私脱敏 + 刷新中 + 报错提示
	snap := BuildSnapshot(BuildInput{
		Accounts:         accounts,
		CurrentAccountID: "acc-3",
		Settings: model.AppSettings{
			MacStatusBarMode: model.MacStatusBarIconQuota,
			PrivacyMode:      true,
		},
		Refreshing: true,
		LastError:  "网络连接超时",
		Now:        now,
	})
	if snap.ButtonTitle != "!" {
		t.Fatalf("当前账号异常时菜单栏标题应为 !，实际 %q", snap.ButtonTitle)
	}
	if strings.Contains(snap.CurrentHeader, "broken@example.com") || !strings.Contains(snap.CurrentHeader, "br***@example.com") {
		t.Fatalf("开启隐私模式后当前账号标题必须脱敏，实际 %q", snap.CurrentHeader)
	}
	if strings.Contains(snap.Tooltip, "broken@example.com") {
		t.Fatalf("开启隐私模式后 Tooltip 不应泄露明文邮箱，实际 %q", snap.Tooltip)
	}
	if snap.CanRefreshCurrent || snap.CanRefreshAll {
		t.Fatal("刷新进行中时应禁用刷新菜单项防重入")
	}
	if snap.StatusHint == "" || !strings.Contains(snap.ErrorBanner, "网络连接超时") {
		t.Fatalf("期望展示刷新忙碌态与错误提示，实际 hint=%q err=%q", snap.StatusHint, snap.ErrorBanner)
	}
	if !strings.Contains(snap.QuotaEmptyHint, "异常") {
		t.Fatalf("异常账号配额提示应明确提示异常，实际 %q", snap.QuotaEmptyHint)
	}

	// 无激活账号时按钮回退为纯图标且不可刷新当前账号
	emptyCurrent := BuildSnapshot(BuildInput{
		Accounts:         accounts,
		CurrentAccountID: "",
		Settings: model.AppSettings{
			MacStatusBarMode: model.MacStatusBarIconQuota,
		},
		Now: now,
	})
	if emptyCurrent.ButtonTitle != "" {
		t.Fatalf("无激活账号时菜单栏应回退纯图标，实际 title=%q", emptyCurrent.ButtonTitle)
	}
	if emptyCurrent.CanRefreshCurrent || !emptyCurrent.CanRefreshAll {
		t.Fatalf("无激活账号但有纳管账号时应仅允许刷新全部，实际 cur=%v all=%v", emptyCurrent.CanRefreshCurrent, emptyCurrent.CanRefreshAll)
	}
}
