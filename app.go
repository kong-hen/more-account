package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"more-account/internal/importer"
	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/quota"
	"more-account/internal/storage"
	"more-account/internal/switcher"
	"more-account/internal/tokenusage"
	"more-account/internal/totp"
	"more-account/internal/wakeup"
)

// BootstrapState 包含前端首屏初始化所需的全部快照数据。
type BootstrapState struct {
	Accounts         []model.Account            `json:"accounts"`
	CurrentAccountID string                     `json:"currentAccountId"`
	Groups           []model.AccountGroup       `json:"groups"`
	Settings         model.AppSettings          `json:"settings"`
	ClientInfo       switcher.ClientInstallInfo `json:"clientInfo"`
	DataDir          string                     `json:"dataDir"`
}

// App 是 Wails 桌面应用的后端绑定总入口，仅负责参数协调与事件桥接。
type App struct {
	ctx             context.Context
	store           *storage.Store
	oauthClient     *oauth.Client
	oauthManager    *oauth.LoginManager
	quotaService    *quota.Service
	switcherService *switcher.Service
	importerService *importer.Service
	usageService    *tokenusage.Service
	wakeupService   *wakeup.Service
}

// NewApp 创建并装配全部后端领域服务。
func NewApp() *App {
	store, err := storage.NewStore("")
	if err != nil {
		panic(fmt.Sprintf("初始化本地数据存储失败: %v", err))
	}
	oauthClient := oauth.NewClient()
	quotaClient := quota.NewClient()
	quotaSvc := quota.NewService(store, oauthClient, quotaClient)
	switchSvc := switcher.NewService(store, oauthClient)
	importSvc := importer.NewService(store, oauthClient, quotaSvc)
	usageSvc := tokenusage.NewService(store.DataDir(), func() string {
		return store.GetSettings().TokenUsageDataDir
	})

	app := &App{
		store:           store,
		oauthClient:     oauthClient,
		oauthManager:    oauth.NewLoginManager(),
		quotaService:    quotaSvc,
		switcherService: switchSvc,
		importerService: importSvc,
		usageService:    usageSvc,
	}

	app.wakeupService = wakeup.NewService(store, oauthClient, quotaSvc, func() {
		app.emitEvent("wakeup:updated", nil)
	})
	return app
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.wakeupService.Start(ctx)
}

func (a *App) shutdown(ctx context.Context) {
	a.oauthManager.Cancel()
	a.wakeupService.Stop()
}

func (a *App) emitEvent(name string, data ...any) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, name, data...)
}

// ─── 启动与全局设置 ────────────────────────────────────────────

// GetBootstrapState 返回首屏渲染所需全部数据。
func (a *App) GetBootstrapState() *BootstrapState {
	settings := a.store.GetSettings()
	return &BootstrapState{
		Accounts:         a.store.ListAccounts(),
		CurrentAccountID: a.store.GetCurrentAccountID(),
		Groups:           a.store.ListGroups(),
		Settings:         settings,
		ClientInfo:       switcher.DetectInstalledClients(settings.DesktopAppPath, settings.IdeAppPath),
		DataDir:          a.store.DataDir(),
	}
}

// GetSettings 获取当前全局设置。
func (a *App) GetSettings() model.AppSettings {
	return a.store.GetSettings()
}

// SaveSettings 保存全局设置。
func (a *App) SaveSettings(settings model.AppSettings) (model.AppSettings, error) {
	return a.store.SaveSettings(settings)
}

// DetectInstalledClients 检测本机反重力桌面版、IDE 与 CLI 安装路径。
func (a *App) DetectInstalledClients() switcher.ClientInstallInfo {
	s := a.store.GetSettings()
	return switcher.DetectInstalledClients(s.DesktopAppPath, s.IdeAppPath)
}

// OpenExternalURL 使用系统默认浏览器打开外部链接。
func (a *App) OpenExternalURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("链接不能为空")
	}
	if a.ctx != nil {
		wailsRuntime.BrowserOpenURL(a.ctx, trimmed)
		return nil
	}
	return openWithOS(trimmed)
}

// OpenDataFolder 在系统文件管理器中打开 ~/.more-account 数据目录。
func (a *App) OpenDataFolder() error {
	return openWithOS(a.store.DataDir())
}

// PickDirectory 弹出系统原生目录选择器（用于自定义 Token 用量扫描目录等）。
func (a *App) PickDirectory(title string) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("应用上下文未就绪")
	}
	if strings.TrimSpace(title) == "" {
		title = "选择目录"
	}
	return wailsRuntime.OpenDirectoryDialog(a.ctx, wailsRuntime.OpenDialogOptions{
		Title: title,
	})
}

func openWithOS(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", target).Start()
	default:
		return exec.Command("xdg-open", target).Start()
	}
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

// AddAccountByRefreshToken 使用单个 Refresh Token 添加账号。
func (a *App) AddAccountByRefreshToken(refreshToken, groupID string) (model.Account, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 45*time.Second)
	defer cancel()
	return a.importerService.AddByRefreshToken(ctx, refreshToken, groupID, nil)
}

// ImportRefreshTokens 批量导入多行 Refresh Token 或 JSON 文本。
func (a *App) ImportRefreshTokens(rawInput, groupID string) (*importer.BatchImportResult, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 3*time.Minute)
	defer cancel()
	return a.importerService.ImportRefreshTokens(ctx, rawInput, groupID)
}

// ImportFromLocal 一键导入本机当前已登录的反重力账号。
func (a *App) ImportFromLocal(groupID string) (model.Account, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 45*time.Second)
	defer cancel()
	return a.importerService.ImportFromLocal(ctx, groupID)
}

// ImportFromJSON 通过 JSON 文本批量导入账号。
func (a *App) ImportFromJSON(jsonContent, groupID string) (*importer.BatchImportResult, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 3*time.Minute)
	defer cancel()
	return a.importerService.ImportJSON(ctx, jsonContent, groupID)
}

// ExportAccounts 将选中账号（为空则全部）导出为 JSON 字符串。
func (a *App) ExportAccounts(accountIDs []string, includeSecrets bool) (string, error) {
	return a.importerService.ExportJSON(accountIDs, includeSecrets)
}

// SaveExportToFile 弹出保存对话框并将账号 JSON 写入本地文件。
func (a *App) SaveExportToFile(accountIDs []string, includeSecrets bool) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("应用上下文未就绪")
	}
	content, err := a.importerService.ExportJSON(accountIDs, includeSecrets)
	if err != nil {
		return "", err
	}
	defaultName := fmt.Sprintf("more-account-export-%s.json", time.Now().Format("20060102-150405"))
	path, err := wailsRuntime.SaveFileDialog(a.ctx, wailsRuntime.SaveDialogOptions{
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
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("写入导出文件失败: %w", err)
	}
	return path, nil
}

// UpdateAccountDetails 更新账号的备注、密码、2FA 密钥和所属分组。
func (a *App) UpdateAccountDetails(accountID string, update model.AccountDetailsUpdate) (model.Account, error) {
	if strings.TrimSpace(update.TwoFactorSecret) != "" {
		normalized, err := totp.NormalizeSecret(update.TwoFactorSecret)
		if err != nil {
			return model.Account{}, err
		}
		update.TwoFactorSecret = normalized
	}
	return a.store.UpdateAccountDetails(accountID, update)
}

// UpdateAccountGroup 单个或批量修改账号所属分组。
func (a *App) UpdateAccountGroup(accountIDs []string, groupID string) error {
	return a.store.UpdateAccountGroup(accountIDs, groupID)
}

// DeleteAccounts 删除单个或多个账号。
func (a *App) DeleteAccounts(accountIDs []string) error {
	return a.store.DeleteAccounts(accountIDs)
}

// ReorderAccounts 按指定顺序重排账号列表。
func (a *App) ReorderAccounts(orderedIDs []string) error {
	return a.store.ReorderAccounts(orderedIDs)
}

// GenerateTOTP 根据密钥实时计算 6 位 2FA 验证码与剩余倒计时秒数。
func (a *App) GenerateTOTP(secret string) (totp.CodeResult, error) {
	return totp.GenerateNow(secret)
}

// ─── Google OAuth 浏览器授权 ──────────────────────────────────

// StartOAuthLogin 启动本地回调监听并打开系统浏览器授权，授权完成后通过事件通知前端。
func (a *App) StartOAuthLogin(groupID string) (oauth.LoginSession, error) {
	sess, err := a.oauthManager.StartSession()
	if err != nil {
		return oauth.LoginSession{}, err
	}

	_ = a.OpenExternalURL(sess.AuthURL)

	go func(redirectURI, targetGroupID string) {
		ctx, cancel := context.WithTimeout(a.baseCtx(), 5*time.Minute)
		defer cancel()

		code, waitErr := a.oauthManager.WaitForCode(ctx)
		if waitErr != nil {
			a.emitEvent("oauth:failed", map[string]any{"error": waitErr.Error()})
			return
		}

		tokenResp, exErr := a.oauthClient.ExchangeCode(ctx, code, redirectURI)
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

		acc, addErr := a.importerService.AddByRefreshToken(ctx, tokenResp.RefreshToken, targetGroupID, nil)
		if addErr != nil {
			a.emitEvent("oauth:failed", map[string]any{"error": addErr.Error()})
			return
		}

		a.emitEvent("oauth:completed", map[string]any{"account": acc})
	}(sess.RedirectURI, groupID)

	return sess, nil
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
	return a.quotaService.RefreshAccount(ctx, accountID)
}

// RefreshAllQuotas 并发刷新全部账号配额，并实时发送 quota:progress 进度事件。
func (a *App) RefreshAllQuotas() (*model.RefreshStats, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 3*time.Minute)
	defer cancel()
	return a.quotaService.RefreshAll(ctx, 4, func(done, total int) {
		a.emitEvent("quota:progress", map[string]any{
			"done":  done,
			"total": total,
		})
	})
}

// ─── 账号切换与切号历史 ───────────────────────────────────────

// SwitchAccount 切换本地反重力客户端账号，可选是否自动重启客户端。
func (a *App) SwitchAccount(req switcher.SwitchRequest) (*switcher.SwitchOutcome, error) {
	ctx, cancel := context.WithTimeout(a.baseCtx(), 60*time.Second)
	defer cancel()
	return a.switcherService.SwitchAccount(ctx, req)
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

// ─── 定时唤醒任务 ─────────────────────────────────────────────

// GetWakeupState 获取唤醒调度总开关及所有唤醒任务。
func (a *App) GetWakeupState() model.WakeupState {
	return a.store.GetWakeupState()
}

// SaveWakeupState 保存唤醒调度总开关与任务列表。
func (a *App) SaveWakeupState(state model.WakeupState) error {
	return a.store.SaveWakeupState(state)
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
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}
