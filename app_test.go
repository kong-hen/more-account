package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
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

func TestCrossPlatformAppIconsCompliance(t *testing.T) {
	// 1. 校验 macOS build/appicon.png 严格符合 Apple HIG 1024x1024 画布与 824x824 主体安全边距规范
	macRaw, err := os.ReadFile("build/appicon.png")
	if err != nil {
		t.Fatalf("读取 build/appicon.png 失败: %v", err)
	}
	macImg, err := png.Decode(bytes.NewReader(macRaw))
	if err != nil {
		t.Fatalf("解码 build/appicon.png 失败: %v", err)
	}
	if macImg.Bounds().Dx() != 1024 || macImg.Bounds().Dy() != 1024 {
		t.Fatalf("macOS 图标画布应为 1024x1024，实际 %v", macImg.Bounds())
	}

	minX, minY, maxX, maxY := 1024, 1024, 0, 0
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			_, _, _, a := macImg.At(x, y).RGBA()
			if a>>8 > 200 {
				if x < minX {
					minX = x
				}
				if y < minY {
					minY = y
				}
				if x+1 > maxX {
					maxX = x + 1
				}
				if y+1 > maxY {
					maxY = y + 1
				}
			}
		}
	}
	abs := func(n int) int {
		if n < 0 {
			return -n
		}
		return n
	}
	if abs(minX-100) > 2 || abs(minY-100) > 2 || abs(maxX-924) > 2 || abs(maxY-924) > 2 {
		t.Fatalf("macOS 图标主体边界应贴合 (100,100)-(924,924) (824x824)，实际 (%d,%d)-(%d,%d)", minX, minY, maxX, maxY)
	}

	// 2. 校验 Windows build/windows/icon.ico 包含完整多尺寸帧且首帧 256x256 为饱满构图
	icoRaw, err := os.ReadFile("build/windows/icon.ico")
	if err != nil {
		t.Fatalf("读取 build/windows/icon.ico 失败: %v", err)
	}
	if len(icoRaw) < 6 {
		t.Fatal("icon.ico 文件过短")
	}
	reserved := binary.LittleEndian.Uint16(icoRaw[0:2])
	icoType := binary.LittleEndian.Uint16(icoRaw[2:4])
	frameCount := int(binary.LittleEndian.Uint16(icoRaw[4:6]))
	if reserved != 0 || icoType != 1 || frameCount < 6 {
		t.Fatalf("icon.ico 头部异常: reserved=%d type=%d count=%d", reserved, icoType, frameCount)
	}

	// 解码首帧 256x256 PNG，确认 Windows 主体占比 >= 90%（避免携带 macOS 大留白导致 Windows 任务栏图标偏小）
	sizeBytes := binary.LittleEndian.Uint32(icoRaw[6+8 : 6+12])
	offsetBytes := binary.LittleEndian.Uint32(icoRaw[6+12 : 6+16])
	firstFrame := icoRaw[offsetBytes : offsetBytes+sizeBytes]
	winImg, err := png.Decode(bytes.NewReader(firstFrame))
	if err != nil {
		t.Fatalf("解码 icon.ico 首帧 256x256 PNG 失败: %v", err)
	}
	if winImg.Bounds().Dx() != 256 || winImg.Bounds().Dy() != 256 {
		t.Fatalf("icon.ico 首帧应为 256x256，实际 %v", winImg.Bounds())
	}
	wMinX, wMaxX := 256, 0
	for x := 0; x < 256; x++ {
		_, _, _, a := winImg.At(x, 128).RGBA()
		if a>>8 > 200 {
			if x < wMinX {
				wMinX = x
			}
			if x+1 > wMaxX {
				wMaxX = x + 1
			}
		}
	}
	if wMaxX-wMinX < 232 {
		t.Fatalf("Windows 256x256 图标主体过小（宽 %dpx，期望 >=232px 饱满构图）", wMaxX-wMinX)
	}

	// 3. 校验 Windows 清单与版本资源配置已就绪
	for _, p := range []string{"build/windows/info.json", "build/windows/wails.exe.manifest"} {
		if st, err := os.Stat(p); err != nil || st.Size() == 0 {
			t.Fatalf("缺少有效的 Windows 打包配置文件 %s: %v", p, err)
		}
	}
}
