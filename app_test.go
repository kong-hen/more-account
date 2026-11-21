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

	// 验证手动切换暗色主题持久化
	savedSettings, err := app.SaveSettings(model.AppSettings{
		Theme:             "dark",
		RuntimeTarget:     "desktop",
		LaunchOnSwitch:    true,
		LowQuotaThreshold: 25,
	})
	if err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	if savedSettings.Theme != "dark" || savedSettings.LowQuotaThreshold != 25 {
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
