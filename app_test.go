package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"more-account/internal/importer"
	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/statusbar"
	"more-account/internal/switcher"
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

	// 验证 GetActiveOAuthSession 返回当前活跃监听，且重复 PrepareOAuthLogin 复用同一端口与会话
	active := app.GetActiveOAuthSession()
	if active == nil || active.RedirectURI != sess.RedirectURI || active.State != sess.State {
		t.Fatalf("GetActiveOAuthSession 未返回当前活跃监听会话: %+v", active)
	}
	reused, err := app.PrepareOAuthLogin("grp-test-2")
	if err != nil {
		t.Fatalf("复用 OAuth 监听失败: %v", err)
	}
	if reused.RedirectURI != sess.RedirectURI || reused.State != sess.State {
		t.Fatalf("重复 PrepareOAuthLogin 应复用当前监听端口，期望 %s，实际 %s", sess.RedirectURI, reused.RedirectURI)
	}
	if got := app.getOAuthTargetGroup(); got != "grp-test-2" {
		t.Fatalf("复用会话时应同步更新目标分组为 grp-test-2，实际 %q", got)
	}

	// 验证显式取消后 GetActiveOAuthSession 返回 nil
	app.CancelOAuthLogin()
	if after := app.GetActiveOAuthSession(); after != nil {
		t.Fatalf("取消 OAuth 监听后应返回 nil，实际 %+v", after)
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
						InputTokens:      1000,
						OutputTokens:     400,
						CacheReadTokens:  120,
						CacheWriteTokens: 85,
						TotalTokens:      1605,
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
	if !strings.Contains(csvOut, "缓存读取Token,缓存写入Token,估算Token") {
		t.Fatalf("导出 CSV 表头缺少紧跟缓存读取Token之后的缓存写入Token列: %s", csvOut)
	}
	if !strings.Contains(csvOut, ",120,85,0,") {
		t.Fatalf("导出 CSV 行数据未写入 CacheWriteTokens=85: %s", csvOut)
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

func TestAppCloseBehaviorDecisionAndPlatformBootstrap(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	app := NewApp()

	boot := app.GetBootstrapState()
	if boot.Platform != runtime.GOOS {
		t.Fatalf("期望 BootstrapState.Platform=%q，实际 %q", runtime.GOOS, boot.Platform)
	}
	wantClose := model.DefaultCloseBehavior()
	if boot.Settings.CloseBehavior != wantClose {
		t.Fatalf("期望当前平台默认 CloseBehavior=%q，实际 %q", wantClose, boot.Settings.CloseBehavior)
	}
	// 跨平台默认关闭行为契约：仅 macOS 默认后台运行，Windows/Linux 默认直接退出
	if got := model.DefaultCloseBehaviorForOS("darwin"); got != model.CloseBehaviorBackground {
		t.Fatalf("期望 darwin 默认 CloseBehavior=%q，实际 %q", model.CloseBehaviorBackground, got)
	}
	if got := model.DefaultCloseBehaviorForOS("windows"); got != model.CloseBehaviorQuit {
		t.Fatalf("期望 windows 默认 CloseBehavior=%q，实际 %q", model.CloseBehaviorQuit, got)
	}
	if got := model.DefaultCloseBehaviorForOS("linux"); got != model.CloseBehaviorQuit {
		t.Fatalf("期望 linux 默认 CloseBehavior=%q，实际 %q", model.CloseBehaviorQuit, got)
	}
	if boot.Settings.MacStatusBarMode != model.MacStatusBarIconQuota {
		t.Fatalf("期望默认 MacStatusBarMode=%q，实际 %q", model.MacStatusBarIconQuota, boot.Settings.MacStatusBarMode)
	}

	cases := []struct {
		name          string
		goos          string
		closeBehavior string
		quitting      bool
		wantPrevent   bool
	}{
		{name: "Windows默认后台拦截", goos: "windows", closeBehavior: model.CloseBehaviorBackground, quitting: false, wantPrevent: true},
		{name: "Windows直接退出放行", goos: "windows", closeBehavior: model.CloseBehaviorQuit, quitting: false, wantPrevent: false},
		{name: "Windows主动退出放行", goos: "windows", closeBehavior: model.CloseBehaviorBackground, quitting: true, wantPrevent: false},
		{name: "macOS走OnBeforeClose始终放行真正退出", goos: "darwin", closeBehavior: model.CloseBehaviorBackground, quitting: false, wantPrevent: false},
		{name: "macOS直接退出放行", goos: "darwin", closeBehavior: model.CloseBehaviorQuit, quitting: false, wantPrevent: false},
	}
	for _, tc := range cases {
		if got := shouldPreventCloseOnPlatform(tc.goos, tc.closeBehavior, tc.quitting); got != tc.wantPrevent {
			t.Fatalf("%s: 期望 prevent=%v，实际 %v", tc.name, tc.wantPrevent, got)
		}
	}

	// 验证状态栏快照可从 App 状态即时生成并响应设置切换
	snapDefault := app.buildStatusBarSnapshot()
	if !snapDefault.Visible {
		t.Fatal("默认 icon_quota 模式下状态栏快照应可见")
	}

	_, err := app.SaveSettings(model.AppSettings{
		Theme:            "light",
		RuntimeTarget:    "desktop",
		CloseBehavior:    model.CloseBehaviorQuit,
		MacStatusBarMode: model.MacStatusBarOff,
	})
	if err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	snapOff := app.buildStatusBarSnapshot()
	if snapOff.Visible {
		t.Fatal("切换为 off 模式后状态栏快照应不可见")
	}
}

type fakeStatusBarController struct {
	mu            sync.Mutex
	started       bool
	closeToBg     bool
	preventAppNap []bool
	showMainCalls int
	snapshots     []statusbar.Snapshot
	wakeCB        func()
}

func (f *fakeStatusBarController) Start(closeToBackground bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = true
	f.closeToBg = closeToBackground
}

func (f *fakeStatusBarController) SetCloseToBackground(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeToBg = enabled
}

func (f *fakeStatusBarController) SetSystemWakeCallback(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakeCB = fn
}

func (f *fakeStatusBarController) SetPreventAppNap(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.preventAppNap = append(f.preventAppNap, enabled)
}

func (f *fakeStatusBarController) ShowMainWindow() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.showMainCalls++
}

func (f *fakeStatusBarController) Update(snap statusbar.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots = append(f.snapshots, snap)
}

func (f *fakeStatusBarController) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = false
}

func (f *fakeStatusBarController) lastPreventAppNap() (bool, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.preventAppNap) == 0 {
		return false, false
	}
	return f.preventAppNap[len(f.preventAppNap)-1], true
}

func (f *fakeStatusBarController) snapshotCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.snapshots)
}

func (f *fakeStatusBarController) showMainCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.showMainCalls
}

func TestTokenUsageCSVFullExportWithoutPaginationAndCacheWriteColumn(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	usageDir := t.TempDir()
	convDir := filepath.Join(usageDir, "conversations")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	nowRFC := now.Format(time.RFC3339)
	const totalConvs = 125

	// 1. 生成单份包含 cache_write (f4=250) 的模板 SQLite 会话文件并复制到 125 份（超过默认分页上限 100）
	tplPath := filepath.Join(usageDir, "template.db")
	cDB, err := sql.Open("sqlite", tplPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cDB.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	var tsBuf []byte
	tsBuf = append(tsBuf, switcher.EncodeVarintField(1, uint64(now.Unix()))...)
	var usageBuf []byte
	usageBuf = append(usageBuf, switcher.EncodeVarintField(1, 1427)...) // gemini-4-carbon-high
	usageBuf = append(usageBuf, switcher.EncodeVarintField(2, 1000)...) // input
	usageBuf = append(usageBuf, switcher.EncodeVarintField(4, 250)...)  // cache_write
	usageBuf = append(usageBuf, switcher.EncodeVarintField(5, 300)...)  // cache_read
	usageBuf = append(usageBuf, switcher.EncodeVarintField(9, 150)...)  // thinking
	usageBuf = append(usageBuf, switcher.EncodeVarintField(10, 400)...) // output
	var metaBuf []byte
	metaBuf = append(metaBuf, switcher.EncodeLenDelimField(1, tsBuf)...)
	metaBuf = append(metaBuf, switcher.EncodeLenDelimField(9, usageBuf)...)
	if _, err := cDB.Exec(`INSERT INTO steps (idx, step_type, status, metadata, step_payload) VALUES (1, 15, 1, ?, NULL)`, metaBuf); err != nil {
		t.Fatal(err)
	}
	_ = cDB.Close()

	tplBytes, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < totalConvs; i++ {
		dst := filepath.Join(convDir, fmt.Sprintf("conv-%03d.db", i))
		if err := os.WriteFile(dst, tplBytes, 0600); err != nil {
			t.Fatal(err)
		}
	}

	// 2. 写入 conversation_summaries.db
	sumDB, err := sql.Open("sqlite", filepath.Join(usageDir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sumDB.Exec(`CREATE TABLE conversation_summaries (
		conversation_id TEXT,
		title TEXT,
		preview TEXT,
		step_count INTEGER,
		last_modified_time TEXT,
		workspace_uris TEXT,
		status TEXT,
		project_id TEXT,
		agent_name TEXT,
		parent_conversation_id TEXT,
		nesting_depth INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	tx, err := sumDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO conversation_summaries VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < totalConvs; i++ {
		if _, err := stmt.Exec(
			fmt.Sprintf("conv-%03d", i),
			fmt.Sprintf("对话 %03d", i),
			"测试全量 CSV 导出",
			1,
			nowRFC,
			`["file:///Users/dev/more-account"]`,
			"DONE",
			"proj-1",
			"",
			"",
			0,
		); err != nil {
			t.Fatal(err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = sumDB.Close()

	app := NewApp()
	if _, err := app.SaveSettings(model.AppSettings{
		Theme:             "light",
		RuntimeTarget:     "desktop",
		TokenUsageDataDir: usageDir,
	}); err != nil {
		t.Fatalf("保存 TokenUsageDataDir 失败: %v", err)
	}

	// 普通查询应保持分页（默认 20 条）
	paged, err := app.QueryTokenUsage(model.TokenUsageQuery{Preset: "all", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("QueryTokenUsage 失败: %v", err)
	}
	if len(paged.Conversations.Items) != 20 || paged.Conversations.Total != totalConvs {
		t.Fatalf("期望普通查询返回 20/%d 条，实际 %d/%d", totalConvs, len(paged.Conversations.Items), paged.Conversations.Total)
	}

	// 导出路径通过 QueryForExport 跳过 100 条分页上限，导出全部 125 条且包含缓存写入Token列
	exportBundle, err := app.usageService.QueryForExport(model.TokenUsageQuery{Preset: "all", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("QueryForExport 失败: %v", err)
	}
	if len(exportBundle.Conversations.Items) != totalConvs {
		t.Fatalf("期望 QueryForExport 返回全部 %d 条，实际 %d", totalConvs, len(exportBundle.Conversations.Items))
	}

	csvText, err := formatTokenUsageCSV(exportBundle)
	if err != nil {
		t.Fatalf("formatTokenUsageCSV 失败: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(csvText, "\xEF\xBB\xBF"))).ReadAll()
	if err != nil {
		t.Fatalf("解析导出 CSV 失败: %v", err)
	}
	if len(records) != totalConvs+1 {
		t.Fatalf("期望 CSV 含表头共 %d 行，实际 %d 行", totalConvs+1, len(records))
	}
	if records[0][9] != "缓存读取Token" || records[0][10] != "缓存写入Token" {
		t.Fatalf("期望第 10/11 列为 缓存读取Token / 缓存写入Token，实际 %q / %q", records[0][9], records[0][10])
	}
	if records[1][9] != "300" || records[1][10] != "250" {
		t.Fatalf("期望缓存读取=300、缓存写入=250，实际 %q / %q", records[1][9], records[1][10])
	}
}

func TestOAuthEnsureSessionProcessingNoRedundantRefreshAndSettingsValidation(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "test-client-secret-placeholder")

	var authCodeHits atomic.Int32
	var refreshTokenHits atomic.Int32
	unblockExchange := make(chan struct{})
	exchangeEntered := make(chan struct{}, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			grant := r.FormValue("grant_type")
			if grant == "authorization_code" {
				authCodeHits.Add(1)
				select {
				case exchangeEntered <- struct{}{}:
				default:
				}
				<-unblockExchange
			} else if grant == "refresh_token" {
				refreshTokenHits.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "ya29.fresh-access-token",
				"refresh_token": "1//0g-fresh-refresh-token",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"id_token":      "mock-id-token",
			})
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"email": "oauth-user@example.com",
				"name":  "OAuth User",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	app := NewApp()
	defer app.CancelOAuthLogin()
	app.oauthClient.TokenURL = srv.URL + "/token"
	app.oauthClient.UserInfoURL = srv.URL + "/userinfo"
	// 测试期间隔离配额拉取外网请求，聚焦验证 OAuth 换取令牌与无冗余 RefreshAccessToken 入库链路
	app.quotaService = nil
	app.importerService = importer.NewService(app.store, app.oauthClient, nil)
	app.openBrowserFn = func(string) error { return nil }

	type emittedEvent struct {
		name    string
		payload map[string]any
	}
	var eventsMu sync.Mutex
	var events []emittedEvent
	completedCh := make(chan map[string]any, 1)
	processingCh := make(chan map[string]any, 1)

	app.eventEmitter = func(name string, data ...any) {
		var m map[string]any
		if len(data) > 0 {
			m, _ = data[0].(map[string]any)
		}
		eventsMu.Lock()
		events = append(events, emittedEvent{name: name, payload: m})
		eventsMu.Unlock()
		switch name {
		case "oauth:processing":
			select {
			case processingCh <- m:
			default:
			}
		case "oauth:completed":
			select {
			case completedCh <- m:
			default:
			}
		}
	}

	// 1. 静默预监听可通过 CancelSilentOAuthLogin 安全释放
	silentSess, err := app.PrepareOAuthLogin("grp-1")
	if err != nil {
		t.Fatalf("PrepareOAuthLogin 失败: %v", err)
	}
	if silentSess.Status != oauth.SessionStatusWaiting {
		t.Fatalf("期望新建会话状态为 waiting，实际 %q", silentSess.Status)
	}
	if !app.CancelSilentOAuthLogin() {
		t.Fatal("未主动发起授权的静默会话应允许 CancelSilentOAuthLogin 释放")
	}
	if active := app.GetActiveOAuthSession(); active != nil {
		t.Fatalf("静默取消后应无活跃会话，实际 %+v", active)
	}

	// 2. Prepare -> Start 原子复用同一会话并升级为主动发起（不可被静默取消误杀）
	preSess, err := app.PrepareOAuthLogin("grp-a")
	if err != nil {
		t.Fatalf("PrepareOAuthLogin 失败: %v", err)
	}
	startedSess, err := app.StartOAuthLogin("grp-b")
	if err != nil {
		t.Fatalf("StartOAuthLogin 复用失败: %v", err)
	}
	if startedSess.State != preSess.State || startedSess.RedirectURI != preSess.RedirectURI {
		t.Fatalf("StartOAuthLogin 应复用同一会话，期望 state=%s，实际 %s", preSess.State, startedSess.State)
	}
	if app.CancelSilentOAuthLogin() {
		t.Fatal("已通过 StartOAuthLogin 主动发起的会话绝不能被 CancelSilentOAuthLogin 误杀")
	}

	// 3. 提交手动回调 -> 触发 oauth:processing -> 二次提交返回 ErrOAuthProcessing -> 放行后入库且无冗余 RefreshAccessToken
	manualCallback := fmt.Sprintf("127.0.0.1:9999/oauth-callback?code=code-abc&state=%s", startedSess.State)
	if err := app.SubmitOAuthCallbackURL(manualCallback); err != nil {
		t.Fatalf("SubmitOAuthCallbackURL 失败: %v", err)
	}

	select {
	case procPayload := <-processingCh:
		if procPayload["state"] != startedSess.State {
			t.Fatalf("oauth:processing state 不匹配: %+v", procPayload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到 oauth:processing 事件")
	}

	select {
	case <-exchangeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("超时未进入 /token 换取令牌流程")
	}

	// 处理中状态下再次提交必须返回 ErrOAuthProcessing，且静默取消不打断处理中会话
	if err := app.SubmitOAuthCallbackURL(manualCallback); !errors.Is(err, oauth.ErrOAuthProcessing) {
		t.Fatalf("处理中重复提交应返回 ErrOAuthProcessing，实际: %v", err)
	}
	if active := app.GetActiveOAuthSession(); active == nil || active.Status != oauth.SessionStatusProcessing {
		t.Fatalf("处理中 GetActiveOAuthSession 应返回 status=processing，实际 %+v", active)
	}
	if app.CancelSilentOAuthLogin() {
		t.Fatal("processing 阶段绝不能被静默取消")
	}

	close(unblockExchange)

	select {
	case compPayload := <-completedCh:
		if compPayload["state"] != startedSess.State {
			t.Fatalf("oauth:completed 缺少或错误 state: %+v", compPayload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("超时未收到 oauth:completed 事件")
	}

	// 等待 defer FinishProcessing 清理活跃会话
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if app.GetActiveOAuthSession() == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if active := app.GetActiveOAuthSession(); active != nil {
		t.Fatalf("授权完成后应清理活跃会话，实际 %+v", active)
	}

	if gotAuth := authCodeHits.Load(); gotAuth != 1 {
		t.Fatalf("期望 /token authorization_code 恰好请求 1 次，实际 %d", gotAuth)
	}
	if gotRef := refreshTokenHits.Load(); gotRef != 0 {
		t.Fatalf("接入 AddByTokenResponse 后不应触发冗余 refresh_token 请求，实际 %d 次", gotRef)
	}

	accs := app.ListAccounts()
	if len(accs) != 1 || accs[0].Email != "oauth-user@example.com" || accs[0].Token.AccessToken != "ya29.fresh-access-token" {
		t.Fatalf("OAuth 入库账号与令牌不符合预期: %+v", accs)
	}

	// 4. 静默预监听超时后 IsSessionSilent 仍能正确返回 true，供 oauth:failed 附带 silent=true
	timeoutMgr := oauth.NewLoginManager()
	timeoutInfo, _, err := timeoutMgr.EnsureSession(oauth.SessionConfig{Silent: true})
	if err != nil {
		t.Fatalf("创建静默会话失败: %v", err)
	}
	shortCtx, shortCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer shortCancel()
	if _, waitErr := timeoutMgr.WaitForState(shortCtx, timeoutInfo.State); waitErr == nil {
		t.Fatal("期望短超时返回超时错误")
	}
	if !timeoutMgr.IsSessionSilent(timeoutInfo.State) {
		t.Fatal("静默预监听超时后 IsSessionSilent 应返回 true 以抑制前端误弹错误框")
	}
	timeoutMgr.FinishProcessing(timeoutInfo.State)
	if timeoutMgr.IsSessionSilent(timeoutInfo.State) {
		t.Fatal("FinishProcessing 清理后应释放 endedSilent 记录")
	}

	// 5. SaveSettings 成对凭据校验 & 合法凭据变更时自动取消进行中的旧 OAuth 会话
	if _, err := app.SaveSettings(model.AppSettings{
		Theme:         "light",
		RuntimeTarget: "desktop",
		OAuthClientID: "only-id-no-secret",
	}); !errors.Is(err, oauth.ErrIncompleteCredentials) {
		t.Fatalf("仅填写 OAuthClientID 应返回 ErrIncompleteCredentials，实际: %v", err)
	}
	if _, err := app.SaveSettings(model.AppSettings{
		Theme:             "light",
		RuntimeTarget:     "desktop",
		OAuthClientSecret: "only-secret-no-id",
	}); !errors.Is(err, oauth.ErrIncompleteCredentials) {
		t.Fatalf("仅填写 OAuthClientSecret 应返回 ErrIncompleteCredentials，实际: %v", err)
	}

	if _, err := app.PrepareOAuthLogin(""); err != nil {
		t.Fatalf("重新开启 OAuth 会话失败: %v", err)
	}
	if app.GetActiveOAuthSession() == nil {
		t.Fatal("期望存在活跃 OAuth 会话")
	}
	if _, err := app.SaveSettings(model.AppSettings{
		Theme:             "light",
		RuntimeTarget:     "desktop",
		OAuthClientID:     "custom-id-2.apps.googleusercontent.com",
		OAuthClientSecret: "custom-secret-2",
	}); err != nil {
		t.Fatalf("保存合法成对自定义 OAuth 凭据失败: %v", err)
	}
	if active := app.GetActiveOAuthSession(); active != nil {
		t.Fatalf("OAuth 凭据变更后应自动取消旧监听会话，实际仍存在: %+v", active)
	}
}

func TestWakeupValidationAppNapSystemWakeAndStatusBarBusySync(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	app := NewApp()
	fakeSB := &fakeStatusBarController{}
	app.statusBar = fakeSB

	// 1. SaveWakeupState 保存前严格校验：拒绝越界 Cron、倒置范围、空任务名与非法时间格式
	invalidStates := []model.WakeupState{
		{
			Enabled: true,
			Tasks: []model.WakeupTask{
				{
					ID:      "bad-cron-overflow",
					Name:    "非法分钟越界",
					Enabled: true,
					Schedule: model.WakeupSchedule{
						RepeatMode:       "crontab",
						Crontab:          "65 * * * *",
						SelectedModels:   []string{"gemini-3.8-flash-high"},
						SelectedAccounts: []string{"acc-1"},
					},
				},
			},
		},
		{
			Enabled: true,
			Tasks: []model.WakeupTask{
				{
					ID:      "bad-cron-inverted",
					Name:    "倒置范围 Cron",
					Enabled: true,
					Schedule: model.WakeupSchedule{
						RepeatMode:       "crontab",
						Crontab:          "10-5 * * * *",
						SelectedModels:   []string{"gemini-3.8-flash-high"},
						SelectedAccounts: []string{"acc-1"},
					},
				},
			},
		},
		{
			Enabled: true,
			Tasks: []model.WakeupTask{
				{
					ID:      "bad-daily-time",
					Name:    "非法时间点",
					Enabled: true,
					Schedule: model.WakeupSchedule{
						RepeatMode:       "daily",
						DailyTimes:       []string{"25:99"},
						SelectedModels:   []string{"gemini-3.8-flash-high"},
						SelectedAccounts: []string{"acc-1"},
					},
				},
			},
		},
	}
	for _, st := range invalidStates {
		if err := app.SaveWakeupState(st); err == nil {
			t.Fatalf("非法唤醒配置应当被 SaveWakeupState 拦截: %+v", st)
		}
	}

	// 2. 合法启用任务保存后联动开启防 App Nap；关闭总开关后联动释放防 App Nap
	validState := model.WakeupState{
		Enabled: true,
		Tasks: []model.WakeupTask{
			{
				ID:      "task-ok",
				Name:    "每日早高峰唤醒",
				Enabled: true,
				Schedule: model.WakeupSchedule{
					RepeatMode:       "daily",
					DailyTimes:       []string{"09:00"},
					SelectedModels:   []string{"gemini-3.8-flash-high"},
					SelectedAccounts: []string{"acc-1"},
				},
			},
		},
	}
	if err := app.SaveWakeupState(validState); err != nil {
		t.Fatalf("保存合法唤醒配置失败: %v", err)
	}
	if active, ok := fakeSB.lastPreventAppNap(); !ok || !active {
		t.Fatalf("存在启用中的定时任务时应开启防 App Nap，实际 ok=%v active=%v", ok, active)
	}

	validState.Enabled = false
	if err := app.SaveWakeupState(validState); err != nil {
		t.Fatalf("暂停唤醒总开关失败: %v", err)
	}
	if active, ok := fakeSB.lastPreventAppNap(); !ok || active {
		t.Fatalf("暂停唤醒总开关后应释放防 App Nap，实际 ok=%v active=%v", ok, active)
	}

	// 3. 验证系统休眠唤醒回调与 ShowMainWindow 均会安全触发检查并刷新状态栏，且调用原生层主窗口前置恢复
	beforeSnaps := fakeSB.snapshotCount()
	beforeShowCalls := fakeSB.showMainCallCount()
	app.handleSystemWake()
	app.ShowMainWindow()
	if fakeSB.snapshotCount() < beforeSnaps+2 {
		t.Fatalf("期望休眠唤醒与显示主窗口同步刷新状态栏，前后计数: %d -> %d", beforeSnaps, fakeSB.snapshotCount())
	}
	if fakeSB.showMainCallCount() != beforeShowCalls+1 {
		t.Fatalf("期望 ShowMainWindow 调用原生状态栏前置接口 1 次，实际新增 %d 次", fakeSB.showMainCallCount()-beforeShowCalls)
	}

	// 4. 验证主窗口刷新与切号操作同步状态栏忙碌态
	app.activeRefreshOps.Add(1)
	snapRefreshing := app.buildStatusBarSnapshot()
	if !snapRefreshing.Refreshing || snapRefreshing.StatusHint != "正在刷新配额..." {
		t.Fatalf("主窗口刷新期间状态栏快照应显示忙碌态，实际: %+v", snapRefreshing)
	}
	app.activeRefreshOps.Add(-1)

	app.activeSwitchOps.Add(1)
	snapSwitching := app.buildStatusBarSnapshot()
	if !snapSwitching.Switching || snapSwitching.StatusHint != "正在切换账号..." {
		t.Fatalf("主窗口切号期间状态栏快照应显示忙碌态，实际: %+v", snapSwitching)
	}
	app.activeSwitchOps.Add(-1)

	snapIdle := app.buildStatusBarSnapshot()
	if snapIdle.Refreshing || snapIdle.Switching {
		t.Fatalf("操作结束后状态栏快照应恢复空闲，实际: %+v", snapIdle)
	}
}

func TestCorruptJSONStartupRecoveryAndWarnings(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("MORE_ACCOUNT_DATA_DIR", dataDir)

	if err := os.WriteFile(filepath.Join(dataDir, "accounts.json"), []byte("{corrupt-accounts"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "settings.json"), []byte("[bad-settings"), 0600); err != nil {
		t.Fatal(err)
	}

	// 损坏 JSON 不应导致 NewApp() panic，而应自动备份隔离并暴露启动告警
	app := NewApp()
	boot := app.GetBootstrapState()
	if len(boot.StartupWarnings) != 2 {
		t.Fatalf("期望收到 2 条启动恢复告警，实际 %d 条: %+v", len(boot.StartupWarnings), boot.StartupWarnings)
	}

	joined := strings.Join(boot.StartupWarnings, "\n")
	if !strings.Contains(joined, "accounts.json") || !strings.Contains(joined, "settings.json") || !strings.Contains(joined, ".corrupt-") {
		t.Fatalf("启动恢复告警缺少文件名或备份说明: %s", joined)
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	var accBackup, settingsBackup bool
	for _, ent := range entries {
		name := ent.Name()
		if strings.HasPrefix(name, "accounts.json.corrupt-") {
			accBackup = true
		}
		if strings.HasPrefix(name, "settings.json.corrupt-") {
			settingsBackup = true
		}
	}
	if !accBackup || !settingsBackup {
		t.Fatalf("期望在数据目录生成损坏文件备份，实际文件: %+v", entries)
	}
}

func TestOAuthSilentFailureSemanticsEndToEnd(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "test-client-secret-placeholder")

	app := NewApp()
	defer app.CancelOAuthLogin()
	app.openBrowserFn = func(string) error { return nil }

	failedCh := make(chan map[string]any, 4)
	app.eventEmitter = func(name string, data ...any) {
		if name != "oauth:failed" || len(data) == 0 {
			return
		}
		if payload, ok := data[0].(map[string]any); ok {
			failedCh <- payload
		}
	}

	// 1. 静默预监听会话收到浏览器 error=access_denied 回调：必须非静默上报（不含 silent: true）
	sessDenied, err := app.PrepareOAuthLogin("")
	if err != nil {
		t.Fatalf("PrepareOAuthLogin 失败: %v", err)
	}
	resp, err := http.Get(fmt.Sprintf("%s?state=%s&error=access_denied", sessDenied.RedirectURI, sessDenied.State)) //nolint:gosec // 本地测试回环地址
	if err != nil {
		t.Fatalf("请求本地回调失败: %v", err)
	}
	_ = resp.Body.Close()

	select {
	case payload := <-failedCh:
		if payload["state"] != sessDenied.State {
			t.Fatalf("access_denied 事件 state 不匹配: %+v", payload)
		}
		if silent, _ := payload["silent"].(bool); silent {
			t.Fatalf("浏览器真实 access_denied 回调绝不能携带 silent=true: %+v", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到 access_denied 的 oauth:failed 事件")
	}

	// 等待前一个会话清理完成
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && app.GetActiveOAuthSession() != nil {
		time.Sleep(15 * time.Millisecond)
	}

	// 2. 用户主动操作（如复制链接/点网页验证调用 MarkOAuthUserInitiated）后等待超时：必须非静默上报
	app.oauthWaitTimeout = 40 * time.Millisecond
	sessInitiated, err := app.PrepareOAuthLogin("")
	if err != nil {
		t.Fatalf("PrepareOAuthLogin 失败: %v", err)
	}
	app.MarkOAuthUserInitiated(sessInitiated.State)

	select {
	case payload := <-failedCh:
		if payload["state"] != sessInitiated.State {
			t.Fatalf("主动发起超时事件 state 不匹配: %+v", payload)
		}
		if silent, _ := payload["silent"].(bool); silent {
			t.Fatalf("用户主动发起后超时绝不能携带 silent=true: %+v", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到主动发起超时的 oauth:failed 事件")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && app.GetActiveOAuthSession() != nil {
		time.Sleep(15 * time.Millisecond)
	}

	// 3. 纯静默预监听（用户未任何主动操作）等待超时：必须携带 silent=true，避免误弹红框
	sessSilent, err := app.PrepareOAuthLogin("")
	if err != nil {
		t.Fatalf("PrepareOAuthLogin 失败: %v", err)
	}

	select {
	case payload := <-failedCh:
		if payload["state"] != sessSilent.State {
			t.Fatalf("静默超时事件 state 不匹配: %+v", payload)
		}
		if silent, _ := payload["silent"].(bool); !silent {
			t.Fatalf("纯静默预监听超时应携带 silent=true，实际: %+v", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时未收到静默超时的 oauth:failed 事件")
	}
}

func TestAppOAuthHostSwitchTLSDetectionAndRealHTTPCallbackEndToEnd(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_DATA_DIR", t.TempDir())
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "test-client-secret-placeholder")

	var gotTokenRedirectURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			gotTokenRedirectURI = r.FormValue("redirect_uri")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "ya29.e2e-access",
				"refresh_token": "1//0g-e2e-refresh",
				"expires_in":    3600,
				"token_type":    "Bearer",
			})
		case "/userinfo":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"email": "e2e-callback@example.com",
				"name":  "E2E Callback",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	app := NewApp()
	defer app.CancelOAuthLogin()
	app.oauthClient.TokenURL = srv.URL + "/token"
	app.oauthClient.UserInfoURL = srv.URL + "/userinfo"
	app.quotaService = nil
	app.importerService = importer.NewService(app.store, app.oauthClient, nil)
	app.openBrowserFn = func(string) error { return nil }

	tlsCh := make(chan map[string]any, 2)
	completedCh := make(chan map[string]any, 2)
	failedCh := make(chan map[string]any, 4)

	app.eventEmitter = func(name string, data ...any) {
		var m map[string]any
		if len(data) > 0 {
			m, _ = data[0].(map[string]any)
		}
		switch name {
		case "oauth:tls_detected":
			tlsCh <- m
		case "oauth:completed":
			completedCh <- m
		case "oauth:failed":
			failedCh <- m
		}
	}

	// 1. 打开弹窗预监听，再显式切换为 127.0.0.1 回调地址，旧会话静默取消不得误发 oauth:failed
	grp, err := app.SaveGroup(model.AccountGroup{Name: "E2E分组", Color: "#2563EB"})
	if err != nil {
		t.Fatalf("SaveGroup 失败: %v", err)
	}
	preSess, err := app.PrepareOAuthLogin("")
	if err != nil {
		t.Fatalf("PrepareOAuthLogin 失败: %v", err)
	}
	switchedSess, err := app.SwitchOAuthCallbackHost(grp.ID, oauth.CallbackHostLoopback)
	if err != nil {
		t.Fatalf("SwitchOAuthCallbackHost 失败: %v", err)
	}
	if switchedSess.State == preSess.State || switchedSess.CallbackHost != oauth.CallbackHostLoopback || !strings.HasPrefix(switchedSess.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("切换回调主机结果异常: pre=%+v switched=%+v", preSess, switchedSess)
	}
	app.MarkOAuthUserInitiated(switchedSess.State)

	// 2. 模拟浏览器对本地端口发起意外 HTTPS TLS 握手，验证推送 oauth:tls_detected 且不阻塞原会话
	parsed, err := url.Parse(switchedSess.RedirectURI)
	if err != nil {
		t.Fatalf("解析 RedirectURI 失败: %v", err)
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+parsed.Port(), time.Second)
	if err != nil {
		t.Fatalf("连接本地监听失败: %v", err)
	}
	_, _ = conn.Write([]byte{0x16, 0x03, 0x01, 0x00, 0x04, 0x01, 0x00, 0x00, 0x00})
	_ = conn.Close()

	select {
	case tlsPayload := <-tlsCh:
		if tlsPayload["state"] != switchedSess.State || tlsPayload["callbackHost"] != oauth.CallbackHostLoopback {
			t.Fatalf("oauth:tls_detected 载荷不匹配: %+v", tlsPayload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("超时未收到 oauth:tls_detected 事件")
	}

	// 3. 模拟浏览器完成登录后重定向到真实 HTTP 回调地址，验证端到端换取令牌与入库
	cbURL := fmt.Sprintf("%s?state=%s&code=e2e-real-http-code", switchedSess.RedirectURI, url.QueryEscape(switchedSess.State))
	cbResp, err := http.Get(cbURL) //nolint:gosec // 本地测试回环地址
	if err != nil {
		t.Fatalf("发起真实本地回调请求失败: %v", err)
	}
	_ = cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK {
		t.Fatalf("期望回调页返回 200，实际 %d", cbResp.StatusCode)
	}

	select {
	case comp := <-completedCh:
		if comp["state"] != switchedSess.State {
			t.Fatalf("oauth:completed state 不匹配: %+v", comp)
		}
	case errPayload := <-failedCh:
		t.Fatalf("不应收到 oauth:failed: %+v", errPayload)
	case <-time.After(5 * time.Second):
		t.Fatal("超时未收到 oauth:completed 事件")
	}

	if gotTokenRedirectURI != switchedSess.RedirectURI {
		t.Fatalf("ExchangeCode 传入的 redirect_uri (%q) 与回调监听地址 (%q) 不一致", gotTokenRedirectURI, switchedSess.RedirectURI)
	}
	accs := app.ListAccounts()
	if len(accs) != 1 || accs[0].Email != "e2e-callback@example.com" || accs[0].GroupID != grp.ID {
		t.Fatalf("端到端入库账号异常: %+v", accs)
	}
	select {
	case extraErr := <-failedCh:
		t.Fatalf("切换主机取消旧会话不应产生 oauth:failed: %+v", extraErr)
	default:
	}
}
