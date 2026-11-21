package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"more-account/internal/model"
	"more-account/internal/updater"
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
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "test-client-secret-placeholder")
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

	// 校验首帧 256x256 32-bit BGRA DIB，确认 Windows 主体占比 >= 90%（避免携带 macOS 大留白导致 Windows 任务栏图标偏小）
	sizeBytes := binary.LittleEndian.Uint32(icoRaw[6+8 : 6+12])
	offsetBytes := binary.LittleEndian.Uint32(icoRaw[6+12 : 6+16])
	firstFrame := icoRaw[offsetBytes : offsetBytes+sizeBytes]
	if len(firstFrame) < 40+256*256*4 {
		t.Fatalf("icon.ico 首帧 256x256 DIB 长度不足: %d", len(firstFrame))
	}
	biWidth := int(int32(binary.LittleEndian.Uint32(firstFrame[4:8])))
	biHeight := int(int32(binary.LittleEndian.Uint32(firstFrame[8:12])))
	if biWidth != 256 || biHeight != 512 {
		t.Fatalf("icon.ico 首帧 DIB 头应为 256x512(含掩码)，实际 %dx%d", biWidth, biHeight)
	}
	// 检查中部第 128 行像素（BGRA 顺序，每像素 4 字节）的非透明水平跨度
	midRowStart := 40 + 128*256*4
	wMinX, wMaxX := 256, 0
	for x := 0; x < 256; x++ {
		alpha := firstFrame[midRowStart+x*4+3]
		if alpha > 200 {
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

func TestProxySettingsValidationAndCSVExportFormat(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	app := NewApp()

	// 非法代理应当被拦截
	if _, err := app.SaveSettings(model.AppSettings{
		Theme:             "light",
		RuntimeTarget:     "desktop",
		LowQuotaThreshold: 20,
		ProxyURL:          "ftp://127.0.0.1:21",
	}); err == nil {
		t.Fatal("保存非法协议代理应当返回错误")
	}

	// 合法 SOCKS5 代理应当保存成功并同步热更新到传输层
	saved, err := app.SaveSettings(model.AppSettings{
		Theme:             "light",
		RuntimeTarget:     "desktop",
		LowQuotaThreshold: 20,
		ProxyURL:          "socks5://127.0.0.1:1080",
	})
	if err != nil || saved.ProxyURL != "socks5://127.0.0.1:1080" {
		t.Fatalf("保存合法 SOCKS5 代理失败: saved=%+v err=%v", saved, err)
	}
	if app.netTransport.ProxyURL() != "socks5://127.0.0.1:1080" {
		t.Fatalf("期望 DynamicTransport 热更新代理生效，实际 %q", app.netTransport.ProxyURL())
	}

	// 验证 CSV 导出包含 UTF-8 BOM 并拦截公式注入
	bundle := &model.TokenUsageBundle{
		Conversations: model.PaginatedConversations{
			Items: []model.ConversationSummaryItem{
				{
					ConversationID:          "conv-1",
					DisplayTitle:            "=CMD|' /C calc'!A0",
					Kind:                    "main",
					WorkspaceName:           "more-account",
					PrimaryModelDisplayName: "Gemini 4 Carbon",
					TurnCount:               5,
					Exact: model.ExactTokenTotals{
						InputTokens:  1000,
						OutputTokens: 400,
						TotalTokens:  1400,
					},
					LastModifiedTime: "2026-11-21T22:00:00Z",
				},
			},
		},
	}
	csvOut, err := formatTokenUsageCSV(bundle)
	if err != nil {
		t.Fatalf("生成 CSV 失败: %v", err)
	}
	if !strings.HasPrefix(csvOut, "\xEF\xBB\xBF") {
		t.Fatal("导出 CSV 缺少 UTF-8 BOM 头")
	}
	if strings.Contains(csvOut, ",=CMD") {
		t.Fatalf("导出 CSV 未对公式起始字符做转义防护: %s", csvOut)
	}
	if !strings.Contains(csvOut, "'=CMD") {
		t.Fatalf("期望对公式起始字符添加单引号前缀，实际: %s", csvOut)
	}
}

func TestCheckForUpdatesAndRepoValidation(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	app := NewApp()

	boot := app.GetBootstrapState()
	if boot.AppVersion == "" || app.GetAppVersion() != boot.AppVersion {
		t.Fatalf("期望返回有效客户端版本号，实际 boot=%q get=%q", boot.AppVersion, app.GetAppVersion())
	}
	if !boot.Settings.AutoCheckUpdate {
		t.Fatal("默认设置应当开启启动自动检查更新 (AutoCheckUpdate=true)")
	}

	// 非法 GitHub 仓库输入应当在保存设置时被拦截
	if _, err := app.SaveSettings(model.AppSettings{
		Theme:             "light",
		RuntimeTarget:     "desktop",
		LowQuotaThreshold: 20,
		GitHubRepo:        "https://gitlab.com/someone/repo",
	}); err == nil {
		t.Fatal("保存非 GitHub 域名仓库应当报错")
	}

	// 完整 GitHub URL 应当自动规范化为 owner/repo
	saved, err := app.SaveSettings(model.AppSettings{
		Theme:             "light",
		RuntimeTarget:     "desktop",
		LowQuotaThreshold: 20,
		GitHubRepo:        "https://github.com/kong-hen/more-account.git",
		AutoCheckUpdate:   true,
	})
	if err != nil || saved.GitHubRepo != "kong-hen/more-account" {
		t.Fatalf("保存合法 GitHub 仓库链接应规范化为 kong-hen/more-account，实际 saved=%+v err=%v", saved, err)
	}

	// 模拟 GitHub Release API 验证 CheckForUpdates 端到端链路
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/kong-hen/more-account/releases/latest" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name":     "v2.0.0",
				"name":         "v2.0.0 重大更新",
				"body":         "1. 支持 GitHub Release 自动检测更新",
				"html_url":     "https://github.com/kong-hen/more-account/releases/tag/v2.0.0",
				"published_at": "2026-11-22T00:00:00Z",
				"assets": []map[string]any{
					{
						"name":                 "MoreAccount-macOS-universal.zip",
						"browser_download_url": "https://github.com/kong-hen/more-account/releases/download/v2.0.0/MoreAccount-macOS-universal.zip",
						"size":                 14000000,
					},
					{
						"name":                 "MoreAccount-windows-amd64.zip",
						"browser_download_url": "https://github.com/kong-hen/more-account/releases/download/v2.0.0/MoreAccount-windows-amd64.zip",
						"size":                 7500000,
					},
				},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	origAPI, origWeb := updater.GitHubAPIBaseURL, updater.GitHubWebBaseURL
	updater.GitHubAPIBaseURL = srv.URL
	updater.GitHubWebBaseURL = srv.URL
	defer func() {
		updater.GitHubAPIBaseURL = origAPI
		updater.GitHubWebBaseURL = origWeb
	}()

	checkRes, err := app.CheckForUpdates("")
	if err != nil {
		t.Fatalf("CheckForUpdates 调用失败: %v", err)
	}
	if !checkRes.HasUpdate || checkRes.LatestVersion != "2.0.0" || checkRes.Repo != "kong-hen/more-account" {
		t.Fatalf("CheckForUpdates 结果不符合预期: %+v", checkRes)
	}
}
