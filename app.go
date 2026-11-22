package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"more-account/internal/importer"
	"more-account/internal/model"
	"more-account/internal/netutil"
	"more-account/internal/oauth"
	"more-account/internal/quota"
	"more-account/internal/statusbar"
	"more-account/internal/storage"
	"more-account/internal/switcher"
	"more-account/internal/tokenusage"
	"more-account/internal/totp"
	"more-account/internal/updater"
	"more-account/internal/wakeup"
)

// BootstrapState 包含前端首屏初始化所需的全部快照数据。
type BootstrapState struct {
	Accounts          []model.Account            `json:"accounts"`
	CurrentAccountID  string                     `json:"currentAccountId"`
	Groups            []model.AccountGroup       `json:"groups"`
	Settings          model.AppSettings          `json:"settings"`
	ClientInfo        switcher.ClientInstallInfo `json:"clientInfo"`
	DataDir           string                     `json:"dataDir"`
	AppVersion        string                     `json:"appVersion"`
	DefaultGitHubRepo string                     `json:"defaultGitHubRepo"`
	HasBuiltInOAuth   bool                       `json:"hasBuiltInOAuth"`
	Platform          string                     `json:"platform"`
}

// App 是 Wails 桌面应用的后端绑定总入口，仅负责参数协调与事件桥接。
type App struct {
	ctxMu              sync.RWMutex
	ctx                context.Context
	oauthGroupMu       sync.RWMutex
	oauthTargetGroupID string
	quitting           atomic.Bool
	statusRefreshing   atomic.Bool
	statusSwitching    atomic.Bool
	statusErrMu        sync.RWMutex
	statusLastError    string
	statusBar          statusbar.Controller
	store              *storage.Store
	netTransport       *netutil.DynamicTransport
	oauthClient        *oauth.Client
	oauthManager       *oauth.LoginManager
	quotaService       *quota.Service
	autoRefresher      *quota.AutoRefresher
	switcherService    *switcher.Service
	activeDetector     *switcher.ActiveDetector
	importerService    *importer.Service
	usageService       *tokenusage.Service
	wakeupService      *wakeup.Service
}

// NewApp 创建并装配全部后端领域服务。
func NewApp() *App {
	store, err := storage.NewStore("")
	if err != nil {
		panic(fmt.Sprintf("初始化本地数据存储失败: %v", err))
	}
	netTr, trErr := netutil.NewDynamicTransport(store.GetSettings().ProxyURL)
	if trErr != nil {
		netTr, _ = netutil.NewDynamicTransport("")
	}
	credsProvider := func() (string, string) {
		st := store.GetSettings()
		return st.OAuthClientID, st.OAuthClientSecret
	}
	oauthClient := oauth.NewClient()
	oauthClient.CredentialsProvider = credsProvider
	oauthMgr := oauth.NewLoginManager()
	oauthMgr.CredentialsProvider = credsProvider

	quotaClient := quota.NewClient()
	if netTr != nil {
		oauthClient.HTTPClient = &http.Client{Timeout: 15 * time.Second, Transport: netTr}
		quotaClient.HTTPClient = &http.Client{Timeout: 20 * time.Second, Transport: netTr}
	}
	quotaSvc := quota.NewService(store, oauthClient, quotaClient)
	switchSvc := switcher.NewService(store, oauthClient)
	importSvc := importer.NewService(store, oauthClient, quotaSvc)
	usageSvc := tokenusage.NewService(store.DataDir(), func() string {
		return store.GetSettings().TokenUsageDataDir
	})

	app := &App{
		store:           store,
		netTransport:    netTr,
		oauthClient:     oauthClient,
		oauthManager:    oauthMgr,
		quotaService:    quotaSvc,
		switcherService: switchSvc,
		importerService: importSvc,
		usageService:    usageSvc,
	}

	app.autoRefresher = quota.NewAutoRefresher(
		store,
		quotaSvc,
		func(running bool) {
			app.emitEvent("quota:auto_status", map[string]any{"running": running})
		},
		func(count int) {
			app.emitEvent("quota:updated", map[string]any{"count": count})
			app.syncStatusBar()
		},
	)

	app.activeDetector = switcher.NewActiveDetector(
		store,
		oauthClient,
		switchSvc.IsSwitching,
		func(res switcher.ActiveDetectionResult) {
			app.emitEvent("account:active_detected", res)
			// 探测到激活账号变化后，触发一次配额检查以按新身份应用刷新周期
			if res.Changed && res.Managed && app.autoRefresher != nil {
				app.autoRefresher.TriggerCheck()
			}
			app.syncStatusBar()
		},
	)

	// 在切号服务释放 switching 锁前同步内部切号基线，彻底消除误报外部切号的竞态窗口
	switchSvc.SetOnSwitched(func(acc model.Account) {
		if app.activeDetector != nil {
			app.activeDetector.SyncAfterInternalSwitch(acc)
		}
		app.syncStatusBar()
	})

	app.wakeupService = wakeup.NewService(store, oauthClient, quotaSvc, func() {
		app.emitEvent("wakeup:updated", nil)
		app.emitEvent("quota:updated", nil)
		app.syncStatusBar()
	})
	if netTr != nil {
		app.wakeupService.SetTransport(netTr)
	}

	app.statusBar = statusbar.NewController(statusbar.Callbacks{
		BuildSnapshot:    app.buildStatusBarSnapshot,
		OnShowWindow:     app.ShowMainWindow,
		OnSwitchAccount:  app.switchAccountFromStatusBar,
		OnRefreshCurrent: app.refreshCurrentFromStatusBar,
		OnRefreshAll:     app.refreshAllFromStatusBar,
		OnTogglePrivacy:  app.togglePrivacyFromStatusBar,
		OnQuit:           app.QuitApp,
	})
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctxMu.Lock()
	a.ctx = ctx
	a.ctxMu.Unlock()

	settings := a.store.GetSettings()
	a.syncWindowBackground(settings.Theme)
	if a.statusBar != nil {
		a.statusBar.Start(settings.CloseBehavior != model.CloseBehaviorQuit)
		a.syncStatusBar()
	}
	a.wakeupService.Start(ctx)
	a.activeDetector.Start(ctx)
	a.autoRefresher.Start(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.statusBar != nil {
		a.statusBar.Stop()
	}
	a.oauthManager.Cancel()
	a.activeDetector.Stop()
	a.autoRefresher.Stop()
	a.wakeupService.Stop()
}

// shouldPreventCloseOnPlatform 判定 Wails OnBeforeClose 回调是否应拦截退出并改为隐藏窗口。
// macOS 的窗口红叉与 Cmd+W 由原生层精准处理，进入 OnBeforeClose 的均为明确退出意图（如 Cmd+Q / Dock 退出 / 状态栏退出），始终放行。
func shouldPreventCloseOnPlatform(goos, closeBehavior string, quitting bool) bool {
	if goos == "darwin" {
		return false
	}
	if quitting {
		return false
	}
	return closeBehavior != model.CloseBehaviorQuit
}

func (a *App) beforeClose(ctx context.Context) bool {
	if shouldPreventCloseOnPlatform(runtime.GOOS, a.store.GetSettings().CloseBehavior, a.quitting.Load()) {
		wailsRuntime.WindowHide(ctx)
		return true
	}
	return false
}

// ShowMainWindow 恢复并前置显示主窗口，同时触发静默账号探测与到期配额刷新。
func (a *App) ShowMainWindow() {
	a.setStatusBarError("")
	if ctx := a.getCtx(); ctx != nil {
		wailsRuntime.WindowUnminimise(ctx)
		wailsRuntime.WindowShow(ctx)
		wailsRuntime.Show(ctx)
	}
	a.TriggerActiveDetect()
	a.TriggerAutoRefreshIfDue()
	a.syncStatusBar()
}

// HandleCloseWindowShortcut 响应 macOS Cmd+W 快捷键：按设置隐藏主窗口或直接退出程序。
func (a *App) HandleCloseWindowShortcut() {
	if a.store.GetSettings().CloseBehavior != model.CloseBehaviorQuit {
		if ctx := a.getCtx(); ctx != nil {
			wailsRuntime.WindowHide(ctx)
		}
		return
	}
	a.QuitApp()
}

// QuitApp 主动彻底退出应用程序（绕过后台常驻关闭拦截）。
func (a *App) QuitApp() {
	a.quitting.Store(true)
	if ctx := a.getCtx(); ctx != nil {
		wailsRuntime.Quit(ctx)
	}
}

func (a *App) setStatusBarError(msg string) {
	a.statusErrMu.Lock()
	a.statusLastError = strings.TrimSpace(msg)
	a.statusErrMu.Unlock()
}

func (a *App) getStatusBarError() string {
	a.statusErrMu.RLock()
	defer a.statusErrMu.RUnlock()
	return a.statusLastError
}

func (a *App) buildStatusBarSnapshot() statusbar.Snapshot {
	return statusbar.BuildSnapshot(statusbar.BuildInput{
		Accounts:         a.store.ListAccounts(),
		CurrentAccountID: a.store.GetCurrentAccountID(),
		Settings:         a.store.GetSettings(),
		Refreshing:       a.statusRefreshing.Load(),
		Switching:        a.statusSwitching.Load(),
		LastError:        a.getStatusBarError(),
		Now:              time.Now(),
	})
}

func (a *App) syncStatusBar() {
	if a.statusBar == nil {
		return
	}
	a.statusBar.Update(a.buildStatusBarSnapshot())
}

func (a *App) switchAccountFromStatusBar(accountID string) {
	if !a.statusSwitching.CompareAndSwap(false, true) {
		return
	}
	a.setStatusBarError("")
	a.syncStatusBar()
	defer func() {
		a.statusSwitching.Store(false)
		a.syncStatusBar()
	}()

	settings := a.store.GetSettings()
	outcome, err := a.SwitchAccount(switcher.SwitchRequest{
		AccountID:    accountID,
		TargetClient: settings.RuntimeTarget,
		Launch:       settings.LaunchOnSwitch,
	})
	if err != nil {
		a.setStatusBarError(err.Error())
		a.emitEvent("statusbar:error", map[string]any{"message": "状态栏切号失败：" + err.Error()})
		return
	}
	targetEmail := ""
	if outcome != nil {
		targetEmail = outcome.Account.Email
	}
	a.emitEvent("account:switched", map[string]any{
		"accountId": accountID,
		"email":     targetEmail,
	})
	a.emitEvent("quota:updated", nil)
}

func (a *App) refreshCurrentFromStatusBar() {
	curID := a.store.GetCurrentAccountID()
	if curID == "" {
		return
	}
	if !a.statusRefreshing.CompareAndSwap(false, true) {
		return
	}
	a.setStatusBarError("")
	a.syncStatusBar()
	defer func() {
		a.statusRefreshing.Store(false)
		a.syncStatusBar()
	}()

	if _, err := a.RefreshAccountQuota(curID); err != nil {
		a.setStatusBarError(err.Error())
		a.emitEvent("statusbar:error", map[string]any{"message": "刷新当前账号配额失败：" + err.Error()})
		return
	}
	a.emitEvent("quota:updated", nil)
}

func (a *App) refreshAllFromStatusBar() {
	if len(a.store.ListAccounts()) == 0 {
		return
	}
	if !a.statusRefreshing.CompareAndSwap(false, true) {
		return
	}
	a.setStatusBarError("")
	a.syncStatusBar()
	defer func() {
		a.statusRefreshing.Store(false)
		a.syncStatusBar()
	}()

	if _, err := a.RefreshAllQuotas(); err != nil {
		a.setStatusBarError(err.Error())
		a.emitEvent("statusbar:error", map[string]any{"message": "刷新全部账号配额失败：" + err.Error()})
		return
	}
	a.emitEvent("quota:updated", nil)
}

func (a *App) togglePrivacyFromStatusBar() {
	settings := a.store.GetSettings()
	settings.PrivacyMode = !settings.PrivacyMode
	saved, err := a.SaveSettings(settings)
	if err != nil {
		a.setStatusBarError(err.Error())
		a.syncStatusBar()
		return
	}
	a.emitEvent("settings:updated", saved)
}

func (a *App) getCtx() context.Context {
	a.ctxMu.RLock()
	defer a.ctxMu.RUnlock()
	return a.ctx
}

func (a *App) syncWindowBackground(theme string) {
	ctx := a.getCtx()
	if ctx == nil {
		return
	}
	if strings.EqualFold(strings.TrimSpace(theme), "dark") {
		wailsRuntime.WindowSetBackgroundColour(ctx, 15, 17, 21, 255)
	} else {
		wailsRuntime.WindowSetBackgroundColour(ctx, 247, 248, 250, 255)
	}
}

func (a *App) emitEvent(name string, data ...any) {
	ctx := a.getCtx()
	if ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(ctx, name, data...)
}

// ─── 启动与全局设置 ────────────────────────────────────────────

// GetBootstrapState 返回首屏渲染所需全部数据。
func (a *App) GetBootstrapState() *BootstrapState {
	settings := a.store.GetSettings()
	defaultRepo, _ := updater.ResolveEffectiveRepo("", "")
	return &BootstrapState{
		Accounts:          a.store.ListAccounts(),
		CurrentAccountID:  a.store.GetCurrentAccountID(),
		Groups:            a.store.ListGroups(),
		Settings:          settings,
		ClientInfo:        switcher.DetectInstalledClients(settings.DesktopAppPath, settings.IdeAppPath),
		DataDir:           a.store.DataDir(),
		AppVersion:        updater.AppVersion,
		DefaultGitHubRepo: defaultRepo,
		HasBuiltInOAuth:   oauth.HasBuiltInCredentials(),
		Platform:          runtime.GOOS,
	}
}

// GetAppVersion 返回当前客户端版本号。
func (a *App) GetAppVersion() string {
	return updater.AppVersion
}

// CheckForUpdates 检查 GitHub 最新发行版（自动复用用户配置的 HTTP / SOCKS5 代理）。
func (a *App) CheckForUpdates(repoOverride string) (*updater.UpdateCheckResult, error) {
	settings := a.store.GetSettings()
	targetRepo, err := updater.ResolveEffectiveRepo(repoOverride, settings.GitHubRepo)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(a.baseCtx(), 18*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 15 * time.Second}
	if a.netTransport != nil {
		client.Transport = a.netTransport
	}
	return updater.CheckLatestRelease(ctx, client, targetRepo, updater.AppVersion)
}

// SaveSettings 保存全局设置，热更新网络代理、同步原生窗口背景色并通知后台服务立即应用新配置。
func (a *App) SaveSettings(settings model.AppSettings) (model.AppSettings, error) {
	saved, err := a.store.SaveSettings(settings)
	if err == nil {
		if a.netTransport != nil {
			_ = a.netTransport.SetProxy(saved.ProxyURL)
		}
		a.syncWindowBackground(saved.Theme)
		if a.statusBar != nil {
			a.statusBar.SetCloseToBackground(saved.CloseBehavior != model.CloseBehaviorQuit)
		}
		a.syncStatusBar()
		if a.activeDetector != nil {
			a.activeDetector.TriggerCheck()
		}
		if a.autoRefresher != nil {
			a.autoRefresher.TriggerCheck()
		}
	}
	return saved, err
}

// TestProxy 测试给定代理（或留空时的直连/系统环境网络）与 Google OAuth 端点的连通延迟（毫秒）。
func (a *App) TestProxy(rawProxyURL string) (int64, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 10*time.Second)
	defer cancel()
	return netutil.TestConnectivity(ctx, rawProxyURL, "")
}

// TriggerAutoRefreshIfDue 立即触发一次后台到期账号配额检查（用于窗口重新回到前台时自动保鲜）。
func (a *App) TriggerAutoRefreshIfDue() {
	if a.autoRefresher != nil {
		a.autoRefresher.TriggerCheck()
	}
}

// DetectActiveAccount 立即同步探测本机当前正在使用的反重力账号，并返回最新探测结果（不重复推送后台事件）。
func (a *App) DetectActiveAccount() switcher.ActiveDetectionResult {
	if a.activeDetector == nil {
		return switcher.ActiveDetectionResult{DetectedAt: time.Now().Unix()}
	}
	ctx, cancel := context.WithTimeout(a.baseCtx(), 15*time.Second)
	defer cancel()
	res := a.activeDetector.DetectOnceSilent(ctx)
	if res.Changed {
		a.syncStatusBar()
	}
	if res.Changed && res.Managed && a.autoRefresher != nil {
		a.autoRefresher.TriggerCheck()
	}
	return res
}

// TriggerActiveDetect 异步唤醒后台探测器检查本机是否在其他程序中切换了账号。
func (a *App) TriggerActiveDetect() {
	if a.activeDetector != nil {
		a.activeDetector.TriggerCheck()
	}
}

// DetectInstalledClients 检测本机反重力桌面版、IDE 与 CLI 安装路径。
func (a *App) DetectInstalledClients() switcher.ClientInstallInfo {
	s := a.store.GetSettings()
	return switcher.DetectInstalledClients(s.DesktopAppPath, s.IdeAppPath)
}

// validateExternalHTTPURL 校验外部链接仅允许标准 http / https 协议且包含有效主机名，防止本地文件/协议执行或命令注入。
func validateExternalHTTPURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("链接不能为空")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("无效的外部链接: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("出于安全限制，仅允许打开 http/https 链接")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("外部链接缺少有效主机名")
	}
	return parsed.String(), nil
}

// OpenExternalURL 使用系统默认浏览器安全打开外部 http/https 链接。
func (a *App) OpenExternalURL(rawURL string) error {
	safeURL, err := validateExternalHTTPURL(rawURL)
	if err != nil {
		return err
	}
	if ctx := a.getCtx(); ctx != nil {
		wailsRuntime.BrowserOpenURL(ctx, safeURL)
		return nil
	}
	return openExternalBrowserWithOS(safeURL)
}

// OpenDataFolder 在系统文件管理器中打开 ~/.more-account 数据目录。
func (a *App) OpenDataFolder() error {
	return openDirectoryWithOS(a.store.DataDir())
}

// PickDirectory 弹出系统原生目录选择器（用于自定义 Token 用量扫描目录等）。
func (a *App) PickDirectory(title string) (string, error) {
	ctx := a.getCtx()
	if ctx == nil {
		return "", fmt.Errorf("应用上下文未就绪")
	}
	if strings.TrimSpace(title) == "" {
		title = "选择目录"
	}
	return wailsRuntime.OpenDirectoryDialog(ctx, wailsRuntime.OpenDialogOptions{
		Title: title,
	})
}

// ─── 账号管理 ─────────────────────────────────────────────────

// ListAccounts 返回全部反重力账号列表。
func (a *App) ListAccounts() []model.Account {
	return a.store.ListAccounts()
}

// GetCurrentAccountID 返回当前激活账号 ID。
func (a *App) GetCurrentAccountID() string {
	return a.store.GetCurrentAccountID()
}

// ImportRefreshTokens 批量导入多行 Refresh Token 或 JSON 文本。
func (a *App) ImportRefreshTokens(rawInput, groupID string) (*importer.BatchImportResult, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 3*time.Minute)
	defer cancel()
	res, err := a.importerService.ImportRefreshTokens(ctx, rawInput, groupID)
	if err == nil {
		a.syncStatusBar()
		if a.activeDetector != nil {
			a.activeDetector.TriggerCheck()
		}
		if a.autoRefresher != nil {
			a.autoRefresher.TriggerCheck()
		}
	}
	return res, err
}

// ImportFromLocal 一键导入本机当前已登录的反重力账号。
func (a *App) ImportFromLocal(groupID string) (model.Account, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 45*time.Second)
	defer cancel()
	acc, err := a.importerService.ImportFromLocal(ctx, groupID)
	if err == nil {
		a.syncStatusBar()
		if a.activeDetector != nil {
			a.activeDetector.SyncAfterInternalSwitch(acc)
		}
		if a.autoRefresher != nil {
			a.autoRefresher.TriggerCheck()
		}
	}
	return acc, err
}

// ExportAccounts 将选中账号（为空则全部）导出为 JSON 字符串。
func (a *App) ExportAccounts(accountIDs []string, includeSecrets bool) (string, error) {
	return a.importerService.ExportJSON(accountIDs, includeSecrets)
}

// SaveExportToFile 弹出保存对话框并将账号 JSON 以 0600 敏感权限原子写入本地文件（覆盖已有 0644 文件时同步收紧权限）。
func (a *App) SaveExportToFile(accountIDs []string, includeSecrets bool) (string, error) {
	ctx := a.getCtx()
	if ctx == nil {
		return "", fmt.Errorf("应用上下文未就绪")
	}
	content, err := a.importerService.ExportJSON(accountIDs, includeSecrets)
	if err != nil {
		return "", err
	}
	defaultName := fmt.Sprintf("more-account-export-%s.json", time.Now().Format("20060102-150405"))
	path, err := wailsRuntime.SaveFileDialog(ctx, wailsRuntime.SaveDialogOptions{
		Title:           "导出反重力账号 JSON",
		DefaultFilename: defaultName,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "JSON 文件 (*.json)", Pattern: "*.json"},
		},
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if err := storage.WriteBytesAtomic(path, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("写入导出文件失败: %w", err)
	}
	return path, nil
}

// UpdateAccountDetails 更新账号的备注、登录账号、登录密码、2FA 密钥和所属分组。
func (a *App) UpdateAccountDetails(accountID string, update model.AccountDetailsUpdate) (model.Account, error) {
	if strings.TrimSpace(update.TwoFactorSecret) != "" {
		normalized, err := totp.NormalizeSecret(update.TwoFactorSecret)
		if err != nil {
			return model.Account{}, err
		}
		update.TwoFactorSecret = normalized
	}
	acc, err := a.store.UpdateAccountDetails(accountID, update)
	if err == nil {
		a.syncStatusBar()
	}
	return acc, err
}

// UpdateAccountGroup 单个或批量修改账号所属分组。
func (a *App) UpdateAccountGroup(accountIDs []string, groupID string) error {
	err := a.store.UpdateAccountGroup(accountIDs, groupID)
	if err == nil {
		a.syncStatusBar()
	}
	return err
}

// DeleteAccounts 删除单个或多个账号（同时级联清理唤醒任务中的失效引用）。
func (a *App) DeleteAccounts(accountIDs []string) error {
	err := a.store.DeleteAccounts(accountIDs)
	if err == nil {
		a.syncStatusBar()
		if a.activeDetector != nil {
			a.activeDetector.TriggerCheck()
		}
		if a.wakeupService != nil {
			a.wakeupService.SyncResetBaseline()
		}
		a.emitEvent("wakeup:updated", nil)
	}
	return err
}

// ReorderAccounts 按指定顺序重排账号列表。
func (a *App) ReorderAccounts(orderedIDs []string) error {
	err := a.store.ReorderAccounts(orderedIDs)
	if err == nil {
		a.syncStatusBar()
	}
	return err
}

// GenerateTOTP 根据密钥实时计算 2FA 验证码与剩余倒计时秒数。
func (a *App) GenerateTOTP(secret string) (totp.CodeResult, error) {
	return totp.GenerateNow(secret)
}

// ─── Google OAuth 浏览器授权 ──────────────────────────────────

// SetOAuthTargetGroup 动态更新当前等待回调中的 OAuth 授权归属分组。
func (a *App) SetOAuthTargetGroup(groupID string) {
	a.oauthGroupMu.Lock()
	a.oauthTargetGroupID = strings.TrimSpace(groupID)
	a.oauthGroupMu.Unlock()
}

func (a *App) getOAuthTargetGroup() string {
	a.oauthGroupMu.RLock()
	defer a.oauthGroupMu.RUnlock()
	return a.oauthTargetGroupID
}

// GetActiveOAuthSession 返回当前仍在监听回调中的 OAuth 会话；若无活跃监听则返回 nil。
func (a *App) GetActiveOAuthSession() *oauth.LoginSession {
	sess, ok := a.oauthManager.CurrentSession()
	if !ok {
		return nil
	}
	return &sess
}

func (a *App) beginOAuthSession(groupID string, openBrowser bool) (oauth.LoginSession, error) {
	a.SetOAuthTargetGroup(groupID)

	// 若当前已有存活中的本地回调监听，直接复用同一端口与会话，避免重复点击或复制后误杀已发出的回调监听
	if existing, ok := a.oauthManager.CurrentSession(); ok {
		if openBrowser {
			_ = a.OpenExternalURL(existing.AuthURL)
		}
		return existing, nil
	}

	sess, err := a.oauthManager.StartSession()
	if err != nil {
		return oauth.LoginSession{}, err
	}

	if openBrowser {
		_ = a.OpenExternalURL(sess.AuthURL)
	}

	go func(sessionState, redirectURI, codeVerifier string) {
		ctx, cancel := context.WithTimeout(a.baseCtx(), 10*time.Minute)
		defer cancel()

		code, waitErr := a.oauthManager.WaitForState(ctx, sessionState)
		if waitErr != nil {
			// 用户主动取消或被新会话替代时静默结束，不再误报 oauth:failed 打断界面或弹出错误条
			if errors.Is(waitErr, oauth.ErrOAuthCanceled) {
				return
			}
			a.emitEvent("oauth:failed", map[string]any{"error": waitErr.Error()})
			return
		}

		tokenResp, exErr := a.oauthClient.ExchangeCode(ctx, code, redirectURI, codeVerifier)
		if exErr != nil {
			a.emitEvent("oauth:failed", map[string]any{"error": exErr.Error()})
			return
		}
		if strings.TrimSpace(tokenResp.RefreshToken) == "" {
			a.emitEvent("oauth:failed", map[string]any{
				"error": "Google 未返回 refresh_token，请在 Google 账号安全页撤销旧授权后重试",
			})
			return
		}

		targetGroupID := a.getOAuthTargetGroup()
		acc, addErr := a.importerService.AddByRefreshToken(ctx, tokenResp.RefreshToken, targetGroupID, nil)
		if addErr != nil {
			a.emitEvent("oauth:failed", map[string]any{"error": addErr.Error()})
			return
		}

		if a.activeDetector != nil {
			a.activeDetector.TriggerCheck()
		}
		if a.autoRefresher != nil {
			a.autoRefresher.TriggerCheck()
		}
		a.syncStatusBar()
		a.emitEvent("oauth:completed", map[string]any{"account": acc})
	}(sess.State, sess.RedirectURI, sess.CodeVerifier)

	return sess, nil
}

// StartOAuthLogin 启动本地回调监听并立即打开系统浏览器跳转到网页验证授权。
func (a *App) StartOAuthLogin(groupID string) (oauth.LoginSession, error) {
	return a.beginOAuthSession(groupID, true)
}

// PrepareOAuthLogin 仅启动本地回调监听并生成授权链接（不自动拉起默认浏览器，供用户直接复制授权链接到指定浏览器验证）。
func (a *App) PrepareOAuthLogin(groupID string) (oauth.LoginSession, error) {
	return a.beginOAuthSession(groupID, false)
}

// SubmitOAuthCallbackURL 允许用户手动粘贴回调 URL 完成 OAuth 授权。
func (a *App) SubmitOAuthCallbackURL(rawURL string) error {
	return a.oauthManager.SubmitCallbackURL(rawURL)
}

// CancelOAuthLogin 取消正在等待中的 OAuth 授权会话。
func (a *App) CancelOAuthLogin() {
	a.oauthManager.Cancel()
}

// ─── 配额刷新 ─────────────────────────────────────────────────

// RefreshAccountQuota 刷新单个账号的令牌和实时模型配额。
func (a *App) RefreshAccountQuota(accountID string) (model.Account, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 40*time.Second)
	defer cancel()
	acc, err := a.quotaService.RefreshAccount(ctx, accountID)
	a.syncStatusBar()
	return acc, err
}

// RefreshAllQuotas 并发刷新全部账号配额，并实时发送 quota:progress 进度事件。
func (a *App) RefreshAllQuotas() (*model.RefreshStats, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 3*time.Minute)
	defer cancel()
	stats, err := a.quotaService.RefreshAll(ctx, 4, func(done, total int) {
		a.emitEvent("quota:progress", map[string]any{
			"done":  done,
			"total": total,
		})
	})
	a.syncStatusBar()
	return stats, err
}

// ─── 账号切换与切号历史 ───────────────────────────────────────

// SwitchAccount 切换本地反重力客户端账号，可选是否自动重启客户端。
func (a *App) SwitchAccount(req switcher.SwitchRequest) (*switcher.SwitchOutcome, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 60*time.Second)
	defer cancel()
	res, err := a.switcherService.SwitchAccount(ctx, req)
	if err == nil && res != nil && a.autoRefresher != nil {
		a.autoRefresher.TriggerCheck()
	}
	a.syncStatusBar()
	return res, err
}

// ListSwitchHistory 返回切号历史记录。
func (a *App) ListSwitchHistory() []model.SwitchHistoryItem {
	return a.store.ListSwitchHistory()
}

// ClearSwitchHistory 清空全部切号历史记录。
func (a *App) ClearSwitchHistory() error {
	return a.store.ClearSwitchHistory()
}

// ─── 自建分组 ─────────────────────────────────────────────────

// ListGroups 返回全部自建分组。
func (a *App) ListGroups() []model.AccountGroup {
	return a.store.ListGroups()
}

// SaveGroup 新建或编辑分组。
func (a *App) SaveGroup(group model.AccountGroup) (model.AccountGroup, error) {
	return a.store.SaveGroup(group)
}

// DeleteGroup 删除分组（组内账号自动归入未分组）。
func (a *App) DeleteGroup(groupID string) error {
	return a.store.DeleteGroup(groupID)
}

// ─── Token 用量统计（只读）────────────────────────────────────

// QueryTokenUsage 按查询条件聚合返回 Token 用量总览与分页对话列表。
func (a *App) QueryTokenUsage(query model.TokenUsageQuery) (*model.TokenUsageBundle, error) {
	return a.usageService.Query(query)
}

// SyncTokenUsage 执行增量同步或全量重建缓存。
func (a *App) SyncTokenUsage(rebuild bool, query model.TokenUsageQuery) (*model.TokenUsageBundle, error) {
	return a.usageService.Sync(rebuild, query)
}

// GetConversationDetail 按需只读加载单条对话的步级 Token 消耗明细。
func (a *App) GetConversationDetail(conversationID string) (*model.ConversationDetail, error) {
	return a.usageService.GetConversationDetail(conversationID)
}

func sanitizeCSVCell(s string) string {
	trimmed := strings.TrimLeft(s, " \t")
	if trimmed != "" {
		switch trimmed[0] {
		case '=', '+', '-', '@':
			return "'" + s
		}
	}
	return s
}

func formatTokenUsageCSV(bundle *model.TokenUsageBundle) (string, error) {
	var buf bytes.Buffer
	// 写入 UTF-8 BOM，确保在 Windows / macOS Excel 中直接打开中文无乱码
	buf.WriteString("\xEF\xBB\xBF")
	w := csv.NewWriter(&buf)

	headers := []string{
		"对话ID",
		"对话标题",
		"类型",
		"工作区",
		"主模型",
		"交互轮次",
		"精确输入Token",
		"精确输出Token",
		"思考Token",
		"缓存读取Token",
		"估算Token",
		"综合总Token",
		"最后更新时间",
	}
	if err := w.Write(headers); err != nil {
		return "", err
	}

	if bundle != nil {
		for _, item := range bundle.Conversations.Items {
			combinedTotal := item.Exact.TotalTokens + item.Estimated.TotalTokens
			row := []string{
				sanitizeCSVCell(item.ConversationID),
				sanitizeCSVCell(item.DisplayTitle),
				sanitizeCSVCell(item.Kind),
				sanitizeCSVCell(item.WorkspaceName),
				sanitizeCSVCell(item.PrimaryModelDisplayName),
				strconv.Itoa(item.TurnCount),
				strconv.FormatUint(item.Exact.InputTokens, 10),
				strconv.FormatUint(item.Exact.OutputTokens, 10),
				strconv.FormatUint(item.Exact.ThinkingTokens, 10),
				strconv.FormatUint(item.Exact.CacheReadTokens, 10),
				strconv.FormatUint(item.Estimated.TotalTokens, 10),
				strconv.FormatUint(combinedTotal, 10),
				sanitizeCSVCell(item.LastModifiedTime),
			}
			if err := w.Write(row); err != nil {
				return "", err
			}
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// SaveTokenUsageCSV 将当前筛选条件下的完整对话 Token 消耗明细导出为 CSV 文件。
func (a *App) SaveTokenUsageCSV(query model.TokenUsageQuery) (string, error) {
	ctx := a.getCtx()
	if ctx == nil {
		return "", fmt.Errorf("应用上下文未就绪")
	}
	exportQuery := query
	exportQuery.Page = 1
	exportQuery.PageSize = 10000

	bundle, err := a.usageService.Query(exportQuery)
	if err != nil {
		return "", err
	}
	csvContent, err := formatTokenUsageCSV(bundle)
	if err != nil {
		return "", fmt.Errorf("生成 CSV 失败: %w", err)
	}

	defaultName := fmt.Sprintf("more-account-token-usage-%s.csv", time.Now().Format("20060102-150405"))
	path, err := wailsRuntime.SaveFileDialog(ctx, wailsRuntime.SaveDialogOptions{
		Title:           "导出 Token 用量明细 CSV",
		DefaultFilename: defaultName,
		Filters: []wailsRuntime.FileFilter{
			{DisplayName: "CSV 表格 (*.csv)", Pattern: "*.csv"},
		},
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if err := storage.WriteBytesAtomic(path, []byte(csvContent), 0600); err != nil {
		return "", fmt.Errorf("写入 CSV 文件失败: %w", err)
	}
	return path, nil
}

// ─── 定时唤醒任务 ─────────────────────────────────────────────

// GetWakeupState 获取唤醒调度总开关及所有唤醒任务。
func (a *App) GetWakeupState() model.WakeupState {
	return a.store.GetWakeupState()
}

// SaveWakeupState 保存唤醒调度总开关与任务列表，并同步配额重置监测基线。
func (a *App) SaveWakeupState(state model.WakeupState) error {
	err := a.store.SaveWakeupState(state)
	if err == nil && a.wakeupService != nil {
		a.wakeupService.SyncResetBaseline()
	}
	return err
}

// RunWakeupTest 手动立即执行单账号单模型唤醒测试。
func (a *App) RunWakeupTest(req model.WakeupTestRequest) (*model.WakeupHistoryItem, error) {
	return a.wakeupService.TestNow(a.baseCtx(), req)
}

// RunWakeupTaskNow 手动立即执行某条唤醒任务。
func (a *App) RunWakeupTaskNow(taskID string) ([]model.WakeupHistoryItem, error) {
	return a.wakeupService.RunTaskNow(a.baseCtx(), taskID)
}

// CancelWakeupTest 中断指定 scopeID 的唤醒测试请求。
func (a *App) CancelWakeupTest(scopeID string) {
	a.wakeupService.CancelScope(scopeID)
}

// ListWakeupHistory 获取唤醒执行历史记录。
func (a *App) ListWakeupHistory() []model.WakeupHistoryItem {
	return a.store.ListWakeupHistory()
}

// ClearWakeupHistory 清空唤醒执行历史记录。
func (a *App) ClearWakeupHistory() error {
	return a.store.ClearWakeupHistory()
}

func (a *App) baseCtx() context.Context {
	if ctx := a.getCtx(); ctx != nil {
		return ctx
	}
	return context.Background()
}
