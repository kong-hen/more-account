package main

import (
	"testing"

	"more-account/internal/model"
)

func TestAppBootstrapAndCoreBindings(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())

	app := NewApp()
	boot := app.GetBootstrapState()
	if boot == nil {
		t.Fatal("GetBootstrapState 不应返回 nil")
	}
	if boot.Settings.Theme != "light" {
		t.Fatalf("期望默认主题为 light，实际 %q", boot.Settings.Theme)
	}
	if boot.Settings.AutoRefreshMinutes != 5 {
		t.Fatalf("期望默认当前账号自动刷新间隔为 5 分钟，实际 %d", boot.Settings.AutoRefreshMinutes)
	}
	if boot.Settings.InactiveAutoRefreshMinutes != 60 {
		t.Fatalf("期望默认未使用账号自动刷新间隔为 60 分钟，实际 %d", boot.Settings.InactiveAutoRefreshMinutes)
	}

	// 验证手动切换暗色主题与两档自动刷新间隔持久化
	savedSettings, err := app.SaveSettings(model.AppSettings{
		Theme:                      "dark",
		RuntimeTarget:              "desktop",
		LaunchOnSwitch:             true,
		LowQuotaThreshold:          25,
		AutoRefreshMinutes:         10,
		InactiveAutoRefreshMinutes: 120,
	})
	if err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	if savedSettings.Theme != "dark" ||
		savedSettings.LowQuotaThreshold != 25 ||
		savedSettings.AutoRefreshMinutes != 10 ||
		savedSettings.InactiveAutoRefreshMinutes != 120 {
		t.Fatalf("设置保存异常: %+v", savedSettings)
	}

	// 验证分组增删与 TOTP 计算接口
	grp, err := app.SaveGroup(model.AccountGroup{Name: "测试分组", Color: "#2563EB"})
	if err != nil {
		t.Fatalf("新建分组失败: %v", err)
	}
	if len(app.ListGroups()) != 1 {
		t.Fatalf("期望 1 个分组，实际 %d", len(app.ListGroups()))
	}
	if err := app.DeleteGroup(grp.ID); err != nil {
		t.Fatalf("删除分组失败: %v", err)
	}

	otp, err := app.GenerateTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ")
	if err != nil || len(otp.Code) != 6 {
		t.Fatalf("TOTP 计算异常: code=%+v err=%v", otp, err)
	}
}

func TestValidateExternalHTTPURLAndSecurityGuard(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	app := NewApp()

	rejected := []string{
		"",
		"   ",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"calc.exe",
		"ftp://example.com/a",
		"https:///missing-host",
		"http://",
	}
	for _, raw := range rejected {
		if _, err := validateExternalHTTPURL(raw); err == nil {
			t.Fatalf("危险或非法 URL %q 应当被拦截", raw)
		}
		if err := app.OpenExternalURL(raw); err == nil {
			t.Fatalf("OpenExternalURL(%q) 应当返回错误", raw)
		}
	}

	accepted := []string{
		"https://accounts.google.com/o/oauth2/v2/auth?state=1",
		"http://127.0.0.1:8080/oauth-callback",
	}
	for _, raw := range accepted {
		got, err := validateExternalHTTPURL(raw)
		if err != nil || got != raw {
			t.Fatalf("合法 URL %q 应当通过校验，实际 got=%q err=%v", raw, got, err)
		}
	}
}

func TestReorderAccountsAndCancelWakeupTestBindings(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	app := NewApp()

	acc1, err := app.store.UpsertAccount(model.Account{
		Email: "first@example.com",
		Token: model.TokenData{RefreshToken: "rt-1"},
	})
	if err != nil {
		t.Fatalf("创建账号 1 失败: %v", err)
	}
	acc2, err := app.store.UpsertAccount(model.Account{
		Email: "second@example.com",
		Token: model.TokenData{RefreshToken: "rt-2"},
	})
	if err != nil {
		t.Fatalf("创建账号 2 失败: %v", err)
	}

	if err := app.ReorderAccounts([]string{acc2.ID, acc1.ID}); err != nil {
		t.Fatalf("ReorderAccounts 调用失败: %v", err)
	}
	list := app.ListAccounts()
	if len(list) != 2 || list[0].ID != acc2.ID || list[1].ID != acc1.ID {
		t.Fatalf("账号拖拽排序结果不符合预期: %+v", list)
	}

	// 验证取消不存在的唤醒测试不会报错或 panic
	app.CancelWakeupTest("nonexistent-scope")
}

func TestPrepareOAuthLoginAndDynamicGroupUpdate(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	app := NewApp()
	defer app.CancelOAuthLogin()

	sess, err := app.PrepareOAuthLogin("")
	if err != nil {
		t.Fatalf("PrepareOAuthLogin 调用失败: %v", err)
	}
	if sess.AuthURL == "" || sess.RedirectURI == "" || sess.State == "" {
		t.Fatalf("PrepareOAuthLogin 返回会话字段不完整: %+v", sess)
	}
	if _, err := validateExternalHTTPURL(sess.AuthURL); err != nil {
		t.Fatalf("生成的 AuthURL 非法: %v", err)
	}

	// 验证等待回调期间支持动态更新目标归属分组
	app.SetOAuthTargetGroup("grp-test-1")
	if got := app.getOAuthTargetGroup(); got != "grp-test-1" {
		t.Fatalf("期望更新 OAuth 目标分组为 grp-test-1，实际 %q", got)
	}
}
