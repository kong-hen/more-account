package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"more-account/internal/model"
	"more-account/internal/netutil"
	"more-account/internal/updater"
)

const (
	accountsFileName      = "accounts.json"
	groupsFileName        = "groups.json"
	settingsFileName      = "settings.json"
	switchHistoryFileName = "switch_history.json"
	wakeupStateFileName   = "wakeup_state.json"
	wakeupHistoryFileName = "wakeup_history.json"
	maxHistoryItems       = 200
)

type persistedAccountsFile struct {
	Version          string          `json:"version"`
	CurrentAccountID string          `json:"currentAccountId,omitempty"`
	Accounts         []model.Account `json:"accounts"`
}

// Store 负责管理应用本地独立数据目录（~/.more-account/）中的所有持久化数据。
type Store struct {
	mu               sync.RWMutex
	dataDir          string
	accounts         []model.Account
	currentAccountID string
	groups           []model.AccountGroup
	settings         model.AppSettings
	switchHistory    []model.SwitchHistoryItem
	wakeupState      model.WakeupState
	wakeupHistory    []model.WakeupHistoryItem
	startupWarnings  []string
}

// DefaultDataDir 返回默认独立数据目录，支持环境变量 MORE_ACCOUNT_DATA_DIR 覆盖。
func DefaultDataDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv("MORE_ACCOUNT_DATA_DIR")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法获取用户主目录: %w", err)
	}
	return filepath.Join(home, ".more-account"), nil
}

// NewStore 初始化并加载本地数据目录。
func NewStore(dataDir string) (*Store, error) {
	if strings.TrimSpace(dataDir) == "" {
		var err error
		dataDir, err = DefaultDataDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("初始化数据目录失败: %w", err)
	}

	s := &Store{
		dataDir:  dataDir,
		settings: model.DefaultSettings(),
	}
	if err := s.loadAll(); err != nil {
		return nil, err
	}
	return s, nil
}

// DataDir 返回当前数据目录路径。
func (s *Store) DataDir() string {
	return s.dataDir
}

// StartupWarnings 返回启动加载本地数据期间记录的损坏文件容错恢复告警列表（深拷贝）。
func (s *Store) StartupWarnings() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.startupWarnings) == 0 {
		return nil
	}
	out := make([]string, len(s.startupWarnings))
	copy(out, s.startupWarnings)
	return out
}

// backupCorruptFile 将损坏的本地 JSON 文件隔离备份为 <file>.corrupt-<timestamp>，避免反复崩溃并保留现场供人工恢复。
func (s *Store) backupCorruptFile(path string) string {
	ts := time.Now().Format("20060102-150405")
	backupPath := fmt.Sprintf("%s.corrupt-%s", path, ts)
	if _, err := os.Stat(backupPath); err == nil {
		backupPath = fmt.Sprintf("%s.corrupt-%s-%d", path, ts, time.Now().UnixNano())
	}
	if err := os.Rename(path, backupPath); err == nil {
		return filepath.Base(backupPath)
	}
	// 若跨卷或文件句柄占用导致重命名失败，尝试复制一份只读备份保留现场
	if raw, err := os.ReadFile(path); err == nil {
		if wErr := WriteBytesAtomic(backupPath, raw, 0600); wErr == nil {
			return filepath.Base(backupPath)
		}
	}
	return ""
}

// loadJSONWithRecovery 以容错模式加载本地 JSON：解析损坏时自动备份隔离并记录启动告警，防止单个文件损坏导致整个应用启动 panic。
func (s *Store) loadJSONWithRecovery(filename string, out any, isCritical bool) bool {
	path := filepath.Join(s.dataDir, filename)
	exists, err := ReadJSONIfExists(path, out)
	if err == nil {
		return exists
	}

	backupName := ""
	if errors.Is(err, ErrCorruptJSON) {
		backupName = s.backupCorruptFile(path)
	}

	if isCritical {
		if backupName != "" {
			s.startupWarnings = append(s.startupWarnings,
				fmt.Sprintf("【高危】核心账号数据文件 %s 已损坏，已自动备份至 %s 并以空账号列表启动，请从备份手动修复或重新导入账号。", filename, backupName))
		} else {
			s.startupWarnings = append(s.startupWarnings,
				fmt.Sprintf("【高危】核心账号数据文件 %s 读取失败（%v），已回退空账号列表启动，请检查数据目录文件权限或完整性。", filename, err))
		}
	} else {
		if backupName != "" {
			s.startupWarnings = append(s.startupWarnings,
				fmt.Sprintf("本地配置文件 %s 已损坏，已备份至 %s 并恢复默认配置继续启动。", filename, backupName))
		} else {
			s.startupWarnings = append(s.startupWarnings,
				fmt.Sprintf("本地配置文件 %s 读取失败（%v），已恢复默认配置继续启动。", filename, err))
		}
	}
	return false
}

func (s *Store) loadAll() error {
	var accFile persistedAccountsFile
	if s.loadJSONWithRecovery(accountsFileName, &accFile, true) {
		s.accounts = accFile.Accounts
		s.currentAccountID = accFile.CurrentAccountID
	}

	var groups []model.AccountGroup
	if s.loadJSONWithRecovery(groupsFileName, &groups, false) {
		s.groups = groups
	}

	settings := model.DefaultSettings()
	if s.loadJSONWithRecovery(settingsFileName, &settings, false) {
		settings = normalizeSettings(settings)
		// 启动自检：若磁盘手工修改了非法代理地址或非法仓库路径，自动回退为空以免阻塞启动
		if _, pErr := netutil.ValidateProxyURL(settings.ProxyURL); pErr != nil {
			settings.ProxyURL = ""
		}
		if normRepo, rErr := updater.NormalizeGitHubRepo(settings.GitHubRepo); rErr != nil {
			settings.GitHubRepo = ""
		} else {
			settings.GitHubRepo = normRepo
		}
	} else {
		// 反序列化中途失败可能污染结构体部分字段，确保完整回退默认值
		settings = model.DefaultSettings()
	}
	s.settings = settings

	var switchHistory []model.SwitchHistoryItem
	if s.loadJSONWithRecovery(switchHistoryFileName, &switchHistory, false) {
		s.switchHistory = switchHistory
	}

	var wakeupState model.WakeupState
	if s.loadJSONWithRecovery(wakeupStateFileName, &wakeupState, false) {
		s.wakeupState = wakeupState
	}

	var wakeupHistory []model.WakeupHistoryItem
	if s.loadJSONWithRecovery(wakeupHistoryFileName, &wakeupHistory, false) {
		s.wakeupHistory = wakeupHistory
	}

	// 启动自检：清理指向已不存在分组或已不存在账号的内存悬空引用
	validGroups := make(map[string]struct{}, len(s.groups))
	for _, g := range s.groups {
		validGroups[g.ID] = struct{}{}
	}
	validAccounts := make(map[string]struct{}, len(s.accounts))
	for i := range s.accounts {
		validAccounts[s.accounts[i].ID] = struct{}{}
		if s.accounts[i].GroupID != "" {
			if _, ok := validGroups[s.accounts[i].GroupID]; !ok {
				s.accounts[i].GroupID = ""
			}
		}
	}
	if s.currentAccountID != "" {
		if _, ok := validAccounts[s.currentAccountID]; !ok {
			s.currentAccountID = ""
		}
	}
	for i := range s.wakeupState.Tasks {
		filtered := make([]string, 0, len(s.wakeupState.Tasks[i].Schedule.SelectedAccounts))
		for _, id := range s.wakeupState.Tasks[i].Schedule.SelectedAccounts {
			if _, ok := validAccounts[id]; ok {
				filtered = append(filtered, id)
			}
		}
		s.wakeupState.Tasks[i].Schedule.SelectedAccounts = filtered
	}

	return nil
}

func normalizeSettings(in model.AppSettings) model.AppSettings {
	return normalizeSettingsForOS(in, runtime.GOOS)
}

func normalizeSettingsForOS(in model.AppSettings, goos string) model.AppSettings {
	// 严格遵守设计约束：仅允许 light 或 dark，默认始终为 light（不跟随系统暗色）。
	if in.Theme != "dark" {
		in.Theme = "light"
	}
	switch in.RuntimeTarget {
	case "desktop", "ide", "cli":
	default:
		in.RuntimeTarget = "desktop"
	}
	if in.LowQuotaThreshold <= 0 || in.LowQuotaThreshold > 100 {
		in.LowQuotaThreshold = 20
	}
	// AutoRefreshMinutes（当前使用账号）: 0 视为旧配置缺省，升级为默认 5 分钟；< 0 统一归为 -1（关闭）；上限 120 分钟。
	if in.AutoRefreshMinutes == 0 {
		in.AutoRefreshMinutes = 5
	} else if in.AutoRefreshMinutes < 0 {
		in.AutoRefreshMinutes = -1
	} else if in.AutoRefreshMinutes > 120 {
		in.AutoRefreshMinutes = 120
	}
	// InactiveAutoRefreshMinutes（未使用账号）: 0 视为旧配置缺省，升级为默认 60 分钟（1 小时）；< 0 归为 -1（关闭）；上限 1440 分钟（24 小时）。
	if in.InactiveAutoRefreshMinutes == 0 {
		in.InactiveAutoRefreshMinutes = 60
	} else if in.InactiveAutoRefreshMinutes < 0 {
		in.InactiveAutoRefreshMinutes = -1
	} else if in.InactiveAutoRefreshMinutes > 1440 {
		in.InactiveAutoRefreshMinutes = 1440
	}
	// CloseBehavior: 尊重老配置或用户显式写入的 background / quit，缺省或非法值按平台默认值归一化（macOS 默认后台，Windows/Linux 默认直接退出）。
	switch strings.TrimSpace(in.CloseBehavior) {
	case model.CloseBehaviorBackground:
		in.CloseBehavior = model.CloseBehaviorBackground
	case model.CloseBehaviorQuit:
		in.CloseBehavior = model.CloseBehaviorQuit
	default:
		in.CloseBehavior = model.DefaultCloseBehaviorForOS(goos)
	}
	// MacStatusBarMode: 缺省或非法值默认归为图标+额度百分比。
	switch strings.TrimSpace(in.MacStatusBarMode) {
	case model.MacStatusBarIconOnly:
		in.MacStatusBarMode = model.MacStatusBarIconOnly
	case model.MacStatusBarOff:
		in.MacStatusBarMode = model.MacStatusBarOff
	default:
		in.MacStatusBarMode = model.MacStatusBarIconQuota
	}
	in.DesktopAppPath = strings.TrimSpace(in.DesktopAppPath)
	in.IdeAppPath = strings.TrimSpace(in.IdeAppPath)
	in.TokenUsageDataDir = strings.TrimSpace(in.TokenUsageDataDir)
	in.ProxyURL = strings.TrimSpace(in.ProxyURL)
	in.GitHubRepo = strings.TrimSpace(in.GitHubRepo)
	in.OAuthClientID = strings.TrimSpace(in.OAuthClientID)
	in.OAuthClientSecret = strings.TrimSpace(in.OAuthClientSecret)
	return in
}

func cloneBoolPtr(p *bool) *bool {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func cloneQuotaData(q *model.QuotaData) *model.QuotaData {
	if q == nil {
		return nil
	}
	cp := *q
	if q.Models != nil {
		cp.Models = make([]model.ModelQuota, len(q.Models))
		copy(cp.Models, q.Models)
	}
	if q.SummaryBuckets != nil {
		cp.SummaryBuckets = make([]model.QuotaBucket, len(q.SummaryBuckets))
		copy(cp.SummaryBuckets, q.SummaryBuckets)
	}
	if q.Credits != nil {
		cp.Credits = make([]model.CreditInfo, len(q.Credits))
		copy(cp.Credits, q.Credits)
	}
	cp.IsGcpTos = cloneBoolPtr(q.IsGcpTos)
	return &cp
}

func cloneQuotaError(e *model.QuotaErrorInfo) *model.QuotaErrorInfo {
	if e == nil {
		return nil
	}
	cp := *e
	return &cp
}

func cloneAccount(acc model.Account) model.Account {
	cp := acc
	cp.Token.IsGcpTos = cloneBoolPtr(acc.Token.IsGcpTos)
	cp.Quota = cloneQuotaData(acc.Quota)
	cp.QuotaError = cloneQuotaError(acc.QuotaError)
	return cp
}

func cloneAccounts(src []model.Account) []model.Account {
	out := make([]model.Account, len(src))
	for i := range src {
		out[i] = cloneAccount(src[i])
	}
	return out
}

func cloneStrings(src []string) []string {
	if src == nil {
		return nil
	}
	out := make([]string, len(src))
	copy(out, src)
	return out
}

func cloneInts(src []int) []int {
	if src == nil {
		return nil
	}
	out := make([]int, len(src))
	copy(out, src)
	return out
}

func cloneWakeupTask(t model.WakeupTask) model.WakeupTask {
	cp := t
	cp.Schedule.DailyTimes = cloneStrings(t.Schedule.DailyTimes)
	cp.Schedule.WeeklyDays = cloneInts(t.Schedule.WeeklyDays)
	cp.Schedule.WeeklyTimes = cloneStrings(t.Schedule.WeeklyTimes)
	cp.Schedule.SelectedModels = cloneStrings(t.Schedule.SelectedModels)
	cp.Schedule.SelectedAccounts = cloneStrings(t.Schedule.SelectedAccounts)
	return cp
}

func cloneWakeupState(st model.WakeupState) model.WakeupState {
	out := model.WakeupState{
		Enabled: st.Enabled,
		Tasks:   make([]model.WakeupTask, len(st.Tasks)),
	}
	for i := range st.Tasks {
		out.Tasks[i] = cloneWakeupTask(st.Tasks[i])
	}
	return out
}

func maxAccountSortOrder(accounts []model.Account) int {
	if len(accounts) == 0 {
		return -1
	}
	maxVal := accounts[0].SortOrder
	for i := 1; i < len(accounts); i++ {
		if accounts[i].SortOrder > maxVal {
			maxVal = accounts[i].SortOrder
		}
	}
	return maxVal
}

func maxGroupSortOrder(groups []model.AccountGroup) int {
	if len(groups) == 0 {
		return -1
	}
	maxVal := groups[0].SortOrder
	for i := 1; i < len(groups); i++ {
		if groups[i].SortOrder > maxVal {
			maxVal = groups[i].SortOrder
		}
	}
	return maxVal
}

func (s *Store) hasGroupLocked(groupID string) bool {
	for _, g := range s.groups {
		if g.ID == groupID {
			return true
		}
	}
	return false
}

func (s *Store) writeAccountsFile(currentID string, accounts []model.Account) error {
	if accounts == nil {
		accounts = []model.Account{}
	}
	payload := persistedAccountsFile{
		Version:          "1.0",
		CurrentAccountID: currentID,
		Accounts:         accounts,
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, accountsFileName), payload, 0600)
}

func (s *Store) writeGroupsFile(groups []model.AccountGroup) error {
	if groups == nil {
		groups = []model.AccountGroup{}
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, groupsFileName), groups, 0600)
}

func (s *Store) writeSettingsFile(settings model.AppSettings) error {
	return WriteJSONAtomic(filepath.Join(s.dataDir, settingsFileName), settings, 0600)
}

func (s *Store) writeSwitchHistoryFile(items []model.SwitchHistoryItem) error {
	if items == nil {
		items = []model.SwitchHistoryItem{}
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, switchHistoryFileName), items, 0600)
}

func (s *Store) writeWakeupStateFile(state model.WakeupState) error {
	if state.Tasks == nil {
		state.Tasks = []model.WakeupTask{}
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, wakeupStateFileName), state, 0600)
}

func (s *Store) writeWakeupHistoryFile(items []model.WakeupHistoryItem) error {
	if items == nil {
		items = []model.WakeupHistoryItem{}
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, wakeupHistoryFileName), items, 0600)
}

// ListAccounts 返回按自定义排序顺序排列的账号深拷贝切片。
func (s *Store) ListAccounts() []model.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := cloneAccounts(s.accounts)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// GetAccount 根据 ID 查找账号并返回深拷贝，避免并发共享内部指针。
func (s *Store) GetAccount(id string) (model.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, acc := range s.accounts {
		if acc.ID == id {
			return cloneAccount(acc), true
		}
	}
	return model.Account{}, false
}

// GetCurrentAccountID 返回当前激活账号 ID。
func (s *Store) GetCurrentAccountID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentAccountID
}

// SetCurrentAccountID 设置当前激活账号并更新其最近使用时间（落盘成功后才提交内存）。
func (s *Store) SetCurrentAccountID(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	nextAccounts := cloneAccounts(s.accounts)
	if accountID != "" {
		found := false
		now := time.Now().Unix()
		for i := range nextAccounts {
			if nextAccounts[i].ID == accountID {
				nextAccounts[i].LastUsed = now
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("账号不存在: %s", accountID)
		}
	}
	if err := s.writeAccountsFile(accountID, nextAccounts); err != nil {
		return err
	}
	s.accounts = nextAccounts
	s.currentAccountID = accountID
	return nil
}

// UpsertAccount 按邮箱去重插入或更新账号，保留既有分组、备注、密码和 2FA 密钥（写盘失败不污染内存）。
func (s *Store) UpsertAccount(input model.Account) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	email := strings.TrimSpace(input.Email)
	if email == "" {
		return model.Account{}, fmt.Errorf("账号邮箱不能为空")
	}

	groupID := strings.TrimSpace(input.GroupID)
	if groupID != "" && !s.hasGroupLocked(groupID) {
		groupID = ""
	}

	now := time.Now().Unix()
	targetLower := strings.ToLower(email)
	nextAccounts := cloneAccounts(s.accounts)

	for i := range nextAccounts {
		if strings.ToLower(strings.TrimSpace(nextAccounts[i].Email)) == targetLower {
			existing := &nextAccounts[i]
			existing.Email = email
			if strings.TrimSpace(input.Name) != "" {
				existing.Name = strings.TrimSpace(input.Name)
			}
			if groupID != "" {
				existing.GroupID = groupID
			}
			if input.Notes != "" {
				existing.Notes = input.Notes
			}
			if strings.TrimSpace(input.LoginAccount) != "" {
				existing.LoginAccount = strings.TrimSpace(input.LoginAccount)
			}
			if input.AccountPassword != "" {
				existing.AccountPassword = input.AccountPassword
			}
			if input.TwoFactorSecret != "" {
				existing.TwoFactorSecret = input.TwoFactorSecret
			}
			if strings.TrimSpace(input.Token.AccessToken) != "" || strings.TrimSpace(input.Token.RefreshToken) != "" {
				existing.Token = mergeTokenData(existing.Token, input.Token)
			}
			if input.Quota != nil {
				existing.Quota = cloneQuotaData(input.Quota)
			}
			existing.Disabled = input.Disabled
			existing.DisabledReason = input.DisabledReason
			existing.DisabledAt = input.DisabledAt
			existing.QuotaError = cloneQuotaError(input.QuotaError)
			existing.LastUsed = now
			if err := s.writeAccountsFile(s.currentAccountID, nextAccounts); err != nil {
				return model.Account{}, err
			}
			s.accounts = nextAccounts
			return cloneAccount(*existing), nil
		}
	}

	created := cloneAccount(input)
	if created.ID == "" {
		created.ID = uuid.NewString()
	}
	created.Email = email
	created.GroupID = groupID
	if created.CreatedAt == 0 {
		created.CreatedAt = now
	}
	if created.LastUsed == 0 {
		created.LastUsed = now
	}
	if created.Token.TokenType == "" {
		created.Token.TokenType = "Bearer"
	}
	created.SortOrder = maxAccountSortOrder(nextAccounts) + 1

	nextAccounts = append(nextAccounts, created)
	if err := s.writeAccountsFile(s.currentAccountID, nextAccounts); err != nil {
		return model.Account{}, err
	}
	s.accounts = nextAccounts
	return cloneAccount(created), nil
}

func mergeTokenData(oldTok, newTok model.TokenData) model.TokenData {
	merged := newTok
	merged.IsGcpTos = cloneBoolPtr(newTok.IsGcpTos)
	if strings.TrimSpace(merged.AccessToken) == "" {
		merged.AccessToken = oldTok.AccessToken
		if merged.ExpiresIn == 0 {
			merged.ExpiresIn = oldTok.ExpiresIn
		}
		if merged.ExpiryTimestamp == 0 {
			merged.ExpiryTimestamp = oldTok.ExpiryTimestamp
		}
	}
	if strings.TrimSpace(merged.RefreshToken) == "" {
		merged.RefreshToken = oldTok.RefreshToken
	}
	if strings.TrimSpace(merged.TokenType) == "" {
		merged.TokenType = "Bearer"
	}
	if strings.TrimSpace(merged.Email) == "" {
		merged.Email = oldTok.Email
	}
	if strings.TrimSpace(merged.ProjectID) == "" {
		merged.ProjectID = oldTok.ProjectID
	}
	if merged.IsGcpTos == nil {
		merged.IsGcpTos = cloneBoolPtr(oldTok.IsGcpTos)
	}
	if strings.TrimSpace(merged.IDToken) == "" {
		merged.IDToken = oldTok.IDToken
	}
	if strings.TrimSpace(merged.OAuthClientKey) == "" {
		merged.OAuthClientKey = oldTok.OAuthClientKey
	}
	return merged
}

// UpdateAccount 在持锁状态下对目标账号执行原子局部修改并落盘，避免后台刷新覆盖并发编辑的备注、密码与分组。
func (s *Store) UpdateAccount(accountID string, mutate func(acc *model.Account) error) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	nextAccounts := cloneAccounts(s.accounts)
	for i := range nextAccounts {
		if nextAccounts[i].ID == accountID {
			if mutate != nil {
				if err := mutate(&nextAccounts[i]); err != nil {
					return model.Account{}, err
				}
			}
			if err := s.writeAccountsFile(s.currentAccountID, nextAccounts); err != nil {
				return model.Account{}, err
			}
			s.accounts = nextAccounts
			return cloneAccount(nextAccounts[i]), nil
		}
	}
	return model.Account{}, fmt.Errorf("账号不存在: %s", accountID)
}

// RecordTokenRefreshSuccess 原子保存刷新后的新令牌，并自动解除此前因 invalid_grant 产生的禁用与令牌报错状态。
func (s *Store) RecordTokenRefreshSuccess(accountID string, freshTok model.TokenData) (model.Account, error) {
	return s.UpdateAccount(accountID, func(target *model.Account) error {
		target.Token = freshTok
		target.Token.IsGcpTos = cloneBoolPtr(freshTok.IsGcpTos)
		if target.Disabled && strings.HasPrefix(target.DisabledReason, "invalid_grant") {
			target.Disabled = false
			target.DisabledReason = ""
			target.DisabledAt = 0
		}
		if target.QuotaError != nil && (target.QuotaError.Reason == "INVALID_GRANT" || target.QuotaError.Reason == "TOKEN_REFRESH_FAILED") {
			target.QuotaError = nil
		}
		return nil
	})
}

// RecordTokenRefreshFailure 原子记录令牌刷新失败诊断；若确认是 invalid_grant 则将账号标记为禁用异常态。
func (s *Store) RecordTokenRefreshFailure(accountID string, err error, isInvalidGrant bool) (model.Account, error) {
	now := time.Now().Unix()
	reason := "TOKEN_REFRESH_FAILED"
	if isInvalidGrant {
		reason = "INVALID_GRANT"
	}
	errInfo := &model.QuotaErrorInfo{
		Message:   err.Error(),
		Reason:    reason,
		Timestamp: now,
	}
	return s.UpdateAccount(accountID, func(target *model.Account) error {
		if isInvalidGrant {
			target.Disabled = true
			target.DisabledReason = "invalid_grant: " + err.Error()
			target.DisabledAt = now
		}
		target.QuotaError = errInfo
		return nil
	})
}

// UpdateAccountDetails 更新账号的备注、登录账号、密码、2FA 密钥及所属分组（校验分组存在性）。
func (s *Store) UpdateAccountDetails(accountID string, update model.AccountDetailsUpdate) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	groupID := strings.TrimSpace(update.GroupID)
	if groupID != "" && !s.hasGroupLocked(groupID) {
		return model.Account{}, fmt.Errorf("目标分组不存在: %s", groupID)
	}

	nextAccounts := cloneAccounts(s.accounts)
	for i := range nextAccounts {
		if nextAccounts[i].ID == accountID {
			nextAccounts[i].Notes = strings.TrimSpace(update.Notes)
			nextAccounts[i].LoginAccount = strings.TrimSpace(update.LoginAccount)
			nextAccounts[i].AccountPassword = update.AccountPassword
			nextAccounts[i].TwoFactorSecret = strings.TrimSpace(update.TwoFactorSecret)
			nextAccounts[i].GroupID = groupID
			if err := s.writeAccountsFile(s.currentAccountID, nextAccounts); err != nil {
				return model.Account{}, err
			}
			s.accounts = nextAccounts
			return cloneAccount(nextAccounts[i]), nil
		}
	}
	return model.Account{}, fmt.Errorf("账号不存在: %s", accountID)
}

// UpdateAccountGroup 批量或单个修改账号的分组归属。
func (s *Store) UpdateAccountGroup(accountIDs []string, groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	groupID = strings.TrimSpace(groupID)
	if groupID != "" && !s.hasGroupLocked(groupID) {
		return fmt.Errorf("目标分组不存在: %s", groupID)
	}

	targets := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		targets[id] = struct{}{}
	}
	nextAccounts := cloneAccounts(s.accounts)
	for i := range nextAccounts {
		if _, ok := targets[nextAccounts[i].ID]; ok {
			nextAccounts[i].GroupID = groupID
		}
	}
	if err := s.writeAccountsFile(s.currentAccountID, nextAccounts); err != nil {
		return err
	}
	s.accounts = nextAccounts
	return nil
}

// DeleteAccounts 批量删除账号；如果删除了当前激活账号则同步清空 currentAccountID，并级联移除唤醒任务中的失效账号引用。
func (s *Store) DeleteAccounts(accountIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delSet := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		delSet[id] = struct{}{}
	}

	remaining := make([]model.Account, 0, len(s.accounts))
	for _, acc := range s.accounts {
		if _, drop := delSet[acc.ID]; drop {
			continue
		}
		remaining = append(remaining, cloneAccount(acc))
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		if remaining[i].SortOrder != remaining[j].SortOrder {
			return remaining[i].SortOrder < remaining[j].SortOrder
		}
		return remaining[i].CreatedAt < remaining[j].CreatedAt
	})
	for i := range remaining {
		remaining[i].SortOrder = i
	}

	nextCurrentID := s.currentAccountID
	if _, dropped := delSet[nextCurrentID]; dropped {
		nextCurrentID = ""
	}

	nextWakeup := cloneWakeupState(s.wakeupState)
	wakeupChanged := false
	for i := range nextWakeup.Tasks {
		orig := nextWakeup.Tasks[i].Schedule.SelectedAccounts
		kept := make([]string, 0, len(orig))
		for _, id := range orig {
			if _, drop := delSet[id]; drop {
				wakeupChanged = true
				continue
			}
			kept = append(kept, id)
		}
		nextWakeup.Tasks[i].Schedule.SelectedAccounts = kept
	}

	if err := s.writeAccountsFile(nextCurrentID, remaining); err != nil {
		return err
	}
	if wakeupChanged {
		if err := s.writeWakeupStateFile(nextWakeup); err != nil {
			_ = s.writeAccountsFile(s.currentAccountID, s.accounts)
			return err
		}
		s.wakeupState = nextWakeup
	}
	s.accounts = remaining
	s.currentAccountID = nextCurrentID
	return nil
}

// ReorderAccounts 按传入的账号 ID 序列更新自定义排序（未在序列中的账号按原有排序依次顺延）。
func (s *Store) ReorderAccounts(orderedIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rank := make(map[string]int, len(orderedIDs))
	for _, id := range orderedIDs {
		if _, exists := rank[id]; !exists {
			rank[id] = len(rank)
		}
	}

	nextAccounts := cloneAccounts(s.accounts)
	sort.SliceStable(nextAccounts, func(i, j int) bool {
		pi, oki := rank[nextAccounts[i].ID]
		pj, okj := rank[nextAccounts[j].ID]
		if oki && okj {
			return pi < pj
		}
		if oki != okj {
			return oki
		}
		if nextAccounts[i].SortOrder != nextAccounts[j].SortOrder {
			return nextAccounts[i].SortOrder < nextAccounts[j].SortOrder
		}
		return nextAccounts[i].CreatedAt < nextAccounts[j].CreatedAt
	})
	for i := range nextAccounts {
		nextAccounts[i].SortOrder = i
	}
	if err := s.writeAccountsFile(s.currentAccountID, nextAccounts); err != nil {
		return err
	}
	s.accounts = nextAccounts
	return nil
}

// ListGroups 返回按顺序排列的全部自建分组。
func (s *Store) ListGroups() []model.AccountGroup {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.AccountGroup, len(s.groups))
	copy(out, s.groups)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// SaveGroup 新建或更新自建分组。
func (s *Store) SaveGroup(group model.AccountGroup) (model.AccountGroup, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	name := strings.TrimSpace(group.Name)
	if name == "" {
		return model.AccountGroup{}, fmt.Errorf("分组名称不能为空")
	}
	color := strings.TrimSpace(group.Color)
	if color == "" {
		color = "#2563EB"
	}

	nextGroups := make([]model.AccountGroup, len(s.groups))
	copy(nextGroups, s.groups)

	if group.ID != "" {
		for i := range nextGroups {
			if nextGroups[i].ID == group.ID {
				nextGroups[i].Name = name
				nextGroups[i].Color = color
				if err := s.writeGroupsFile(nextGroups); err != nil {
					return model.AccountGroup{}, err
				}
				s.groups = nextGroups
				return nextGroups[i], nil
			}
		}
	}

	created := model.AccountGroup{
		ID:        uuid.NewString(),
		Name:      name,
		Color:     color,
		SortOrder: maxGroupSortOrder(nextGroups) + 1,
		CreatedAt: time.Now().Unix(),
	}
	nextGroups = append(nextGroups, created)
	if err := s.writeGroupsFile(nextGroups); err != nil {
		return model.AccountGroup{}, err
	}
	s.groups = nextGroups
	return created, nil
}

// DeleteGroup 删除自建分组，并将原组内账号自动归入「未分组」。
func (s *Store) DeleteGroup(groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	remaining := make([]model.AccountGroup, 0, len(s.groups))
	for _, g := range s.groups {
		if g.ID == groupID {
			continue
		}
		remaining = append(remaining, g)
	}

	nextAccounts := cloneAccounts(s.accounts)
	accountsChanged := false
	for i := range nextAccounts {
		if nextAccounts[i].GroupID == groupID {
			nextAccounts[i].GroupID = ""
			accountsChanged = true
		}
	}

	if err := s.writeGroupsFile(remaining); err != nil {
		return err
	}
	if accountsChanged {
		if err := s.writeAccountsFile(s.currentAccountID, nextAccounts); err != nil {
			// 尽量恢复分组文件，保持磁盘一致性
			_ = s.writeGroupsFile(s.groups)
			return err
		}
		s.accounts = nextAccounts
	}
	s.groups = remaining
	return nil
}

// GetSettings 返回当前全局设置。
func (s *Store) GetSettings() model.AppSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SaveSettings 保存全局设置（并在落盘前校验代理地址与 GitHub 仓库路径合法性）。
func (s *Store) SaveSettings(next model.AppSettings) (model.AppSettings, error) {
	normalized := normalizeSettings(next)
	if _, err := netutil.ValidateProxyURL(normalized.ProxyURL); err != nil {
		return model.AppSettings{}, err
	}
	normRepo, err := updater.NormalizeGitHubRepo(normalized.GitHubRepo)
	if err != nil {
		return model.AppSettings{}, err
	}
	normalized.GitHubRepo = normRepo

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.writeSettingsFile(normalized); err != nil {
		return model.AppSettings{}, err
	}
	s.settings = normalized
	return normalized, nil
}

// ListSwitchHistory 返回按时间倒序排列的切号历史。
func (s *Store) ListSwitchHistory() []model.SwitchHistoryItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.SwitchHistoryItem, len(s.switchHistory))
	copy(out, s.switchHistory)
	return out
}

// AppendSwitchHistory 追加一条切号历史记录（新记录在前，最多保留 maxHistoryItems 条）。
func (s *Store) AppendSwitchHistory(item model.SwitchHistoryItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Timestamp == 0 {
		item.Timestamp = time.Now().UnixMilli()
	}
	next := make([]model.SwitchHistoryItem, 0, min(len(s.switchHistory)+1, maxHistoryItems))
	next = append(next, item)
	limit := maxHistoryItems - 1
	if len(s.switchHistory) < limit {
		limit = len(s.switchHistory)
	}
	next = append(next, s.switchHistory[:limit]...)
	if err := s.writeSwitchHistoryFile(next); err != nil {
		return err
	}
	s.switchHistory = next
	return nil
}

// ClearSwitchHistory 清空全部切号历史（落盘写空数组 [] 而非 null）。
func (s *Store) ClearSwitchHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := []model.SwitchHistoryItem{}
	if err := s.writeSwitchHistoryFile(empty); err != nil {
		return err
	}
	s.switchHistory = empty
	return nil
}

// GetWakeupState 返回唤醒调度器总开关和任务配置的深拷贝。
func (s *Store) GetWakeupState() model.WakeupState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneWakeupState(s.wakeupState)
}

// SaveWakeupState 保存唤醒任务配置，并自动保留后端更新的最近运行时间戳，避免前端旧快照覆盖后台并发触发记录。
func (s *Store) SaveWakeupState(state model.WakeupState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	prevLastRun := make(map[string]int64, len(s.wakeupState.Tasks))
	prevCreated := make(map[string]int64, len(s.wakeupState.Tasks))
	for _, t := range s.wakeupState.Tasks {
		if t.ID != "" {
			prevLastRun[t.ID] = t.LastRunAt
			prevCreated[t.ID] = t.CreatedAt
		}
	}

	next := cloneWakeupState(state)
	now := time.Now().UnixMilli()
	for i := range next.Tasks {
		id := strings.TrimSpace(next.Tasks[i].ID)
		if id == "" {
			id = uuid.NewString()
			next.Tasks[i].ID = id
		}
		if next.Tasks[i].CreatedAt == 0 {
			if c := prevCreated[id]; c > 0 {
				next.Tasks[i].CreatedAt = c
			} else {
				next.Tasks[i].CreatedAt = now
			}
		}
		if prev := prevLastRun[id]; prev > next.Tasks[i].LastRunAt {
			next.Tasks[i].LastRunAt = prev
		}
	}
	if err := s.writeWakeupStateFile(next); err != nil {
		return err
	}
	s.wakeupState = next
	return nil
}

// UpdateWakeupTaskLastRun 原子更新单条唤醒任务的最近运行时间戳，避免覆盖并发编辑的其他任务。
func (s *Store) UpdateWakeupTaskLastRun(taskID string, lastRunAtMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := cloneWakeupState(s.wakeupState)
	found := false
	for i := range next.Tasks {
		if next.Tasks[i].ID == taskID {
			next.Tasks[i].LastRunAt = lastRunAtMs
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	if err := s.writeWakeupStateFile(next); err != nil {
		return err
	}
	s.wakeupState = next
	return nil
}

// ListWakeupHistory 返回按时间倒序排列的唤醒执行历史。
func (s *Store) ListWakeupHistory() []model.WakeupHistoryItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.WakeupHistoryItem, len(s.wakeupHistory))
	copy(out, s.wakeupHistory)
	return out
}

// AppendWakeupHistory 追加一条唤醒执行历史（新记录在前，最多保留 maxHistoryItems 条）。
func (s *Store) AppendWakeupHistory(item model.WakeupHistoryItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Timestamp == 0 {
		item.Timestamp = time.Now().UnixMilli()
	}
	next := make([]model.WakeupHistoryItem, 0, min(len(s.wakeupHistory)+1, maxHistoryItems))
	next = append(next, item)
	limit := maxHistoryItems - 1
	if len(s.wakeupHistory) < limit {
		limit = len(s.wakeupHistory)
	}
	next = append(next, s.wakeupHistory[:limit]...)
	if err := s.writeWakeupHistoryFile(next); err != nil {
		return err
	}
	s.wakeupHistory = next
	return nil
}

// ClearWakeupHistory 清空唤醒执行历史（落盘写空数组 [] 而非 null）。
func (s *Store) ClearWakeupHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	empty := []model.WakeupHistoryItem{}
	if err := s.writeWakeupHistoryFile(empty); err != nil {
		return err
	}
	s.wakeupHistory = empty
	return nil
}
