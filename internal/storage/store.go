package storage

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"more-account/internal/model"
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

func (s *Store) loadAll() error {
	var accFile persistedAccountsFile
	if exists, err := ReadJSONIfExists(filepath.Join(s.dataDir, accountsFileName), &accFile); err != nil {
		return err
	} else if exists {
		s.accounts = accFile.Accounts
		s.currentAccountID = accFile.CurrentAccountID
	}

	var groups []model.AccountGroup
	if exists, err := ReadJSONIfExists(filepath.Join(s.dataDir, groupsFileName), &groups); err != nil {
		return err
	} else if exists {
		s.groups = groups
	}

	settings := model.DefaultSettings()
	if exists, err := ReadJSONIfExists(filepath.Join(s.dataDir, settingsFileName), &settings); err != nil {
		return err
	} else if exists {
		settings = normalizeSettings(settings)
	}
	s.settings = settings

	var switchHistory []model.SwitchHistoryItem
	if exists, err := ReadJSONIfExists(filepath.Join(s.dataDir, switchHistoryFileName), &switchHistory); err != nil {
		return err
	} else if exists {
		s.switchHistory = switchHistory
	}

	var wakeupState model.WakeupState
	if exists, err := ReadJSONIfExists(filepath.Join(s.dataDir, wakeupStateFileName), &wakeupState); err != nil {
		return err
	} else if exists {
		s.wakeupState = wakeupState
	}

	var wakeupHistory []model.WakeupHistoryItem
	if exists, err := ReadJSONIfExists(filepath.Join(s.dataDir, wakeupHistoryFileName), &wakeupHistory); err != nil {
		return err
	} else if exists {
		s.wakeupHistory = wakeupHistory
	}

	return nil
}

func normalizeSettings(in model.AppSettings) model.AppSettings {
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
	return in
}

func (s *Store) saveAccountsLocked() error {
	payload := persistedAccountsFile{
		Version:          "1.0",
		CurrentAccountID: s.currentAccountID,
		Accounts:         s.accounts,
	}
	return WriteJSONAtomic(filepath.Join(s.dataDir, accountsFileName), payload, 0600)
}

func (s *Store) saveGroupsLocked() error {
	return WriteJSONAtomic(filepath.Join(s.dataDir, groupsFileName), s.groups, 0600)
}

func (s *Store) saveSettingsLocked() error {
	return WriteJSONAtomic(filepath.Join(s.dataDir, settingsFileName), s.settings, 0600)
}

func (s *Store) saveSwitchHistoryLocked() error {
	return WriteJSONAtomic(filepath.Join(s.dataDir, switchHistoryFileName), s.switchHistory, 0600)
}

func (s *Store) saveWakeupStateLocked() error {
	return WriteJSONAtomic(filepath.Join(s.dataDir, wakeupStateFileName), s.wakeupState, 0600)
}

func (s *Store) saveWakeupHistoryLocked() error {
	return WriteJSONAtomic(filepath.Join(s.dataDir, wakeupHistoryFileName), s.wakeupHistory, 0600)
}

// ListAccounts 返回按自定义排序顺序排列的账号深拷贝切片。
func (s *Store) ListAccounts() []model.Account {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]model.Account, len(s.accounts))
	copy(out, s.accounts)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SortOrder != out[j].SortOrder {
			return out[i].SortOrder < out[j].SortOrder
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

// GetAccount 根据 ID 查找账号。
func (s *Store) GetAccount(id string) (model.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, acc := range s.accounts {
		if acc.ID == id {
			return acc, true
		}
	}
	return model.Account{}, false
}

// GetAccountByEmail 按邮箱（忽略大小写）查找账号。
func (s *Store) GetAccountByEmail(email string) (model.Account, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	target := strings.ToLower(strings.TrimSpace(email))
	for _, acc := range s.accounts {
		if strings.ToLower(strings.TrimSpace(acc.Email)) == target {
			return acc, true
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

// SetCurrentAccountID 设置当前激活账号并更新其最近使用时间。
func (s *Store) SetCurrentAccountID(accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if accountID != "" {
		found := false
		now := time.Now().Unix()
		for i := range s.accounts {
			if s.accounts[i].ID == accountID {
				s.accounts[i].LastUsed = now
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("账号不存在: %s", accountID)
		}
	}
	s.currentAccountID = accountID
	return s.saveAccountsLocked()
}

// UpsertAccount 按邮箱去重插入或更新账号，保留既有分组、备注、密码和 2FA 密钥。
func (s *Store) UpsertAccount(input model.Account) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	email := strings.TrimSpace(input.Email)
	if email == "" {
		return model.Account{}, fmt.Errorf("账号邮箱不能为空")
	}
	now := time.Now().Unix()
	targetLower := strings.ToLower(email)

	for i := range s.accounts {
		if strings.ToLower(strings.TrimSpace(s.accounts[i].Email)) == targetLower {
			existing := &s.accounts[i]
			existing.Email = email
			if strings.TrimSpace(input.Name) != "" {
				existing.Name = strings.TrimSpace(input.Name)
			}
			if input.GroupID != "" {
				existing.GroupID = input.GroupID
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
				existing.Quota = input.Quota
			}
			existing.Disabled = input.Disabled
			existing.DisabledReason = input.DisabledReason
			existing.DisabledAt = input.DisabledAt
			existing.QuotaError = input.QuotaError
			existing.LastUsed = now
			if err := s.saveAccountsLocked(); err != nil {
				return model.Account{}, err
			}
			return *existing, nil
		}
	}

	if input.ID == "" {
		input.ID = uuid.NewString()
	}
	input.Email = email
	if input.CreatedAt == 0 {
		input.CreatedAt = now
	}
	if input.LastUsed == 0 {
		input.LastUsed = now
	}
	if input.Token.TokenType == "" {
		input.Token.TokenType = "Bearer"
	}
	input.SortOrder = len(s.accounts)

	s.accounts = append(s.accounts, input)
	if err := s.saveAccountsLocked(); err != nil {
		return model.Account{}, err
	}
	return input, nil
}

func mergeTokenData(oldTok, newTok model.TokenData) model.TokenData {
	merged := newTok
	if strings.TrimSpace(merged.RefreshToken) == "" {
		merged.RefreshToken = oldTok.RefreshToken
	}
	if strings.TrimSpace(merged.TokenType) == "" {
		merged.TokenType = "Bearer"
	}
	if strings.TrimSpace(merged.ProjectID) == "" {
		merged.ProjectID = oldTok.ProjectID
	}
	if merged.IsGcpTos == nil {
		merged.IsGcpTos = oldTok.IsGcpTos
	}
	if strings.TrimSpace(merged.IDToken) == "" {
		merged.IDToken = oldTok.IDToken
	}
	if strings.TrimSpace(merged.OAuthClientKey) == "" {
		merged.OAuthClientKey = oldTok.OAuthClientKey
	}
	return merged
}

// SaveAccount 原地替换已有账号（按 ID 匹配）。
func (s *Store) SaveAccount(acc model.Account) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.accounts {
		if s.accounts[i].ID == acc.ID {
			s.accounts[i] = acc
			if err := s.saveAccountsLocked(); err != nil {
				return model.Account{}, err
			}
			return acc, nil
		}
	}
	return model.Account{}, fmt.Errorf("账号不存在: %s", acc.ID)
}

// UpdateAccountDetails 更新账号的备注、登录账号、密码、2FA 密钥及所属分组。
func (s *Store) UpdateAccountDetails(accountID string, update model.AccountDetailsUpdate) (model.Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.accounts {
		if s.accounts[i].ID == accountID {
			s.accounts[i].Notes = strings.TrimSpace(update.Notes)
			s.accounts[i].LoginAccount = strings.TrimSpace(update.LoginAccount)
			s.accounts[i].AccountPassword = update.AccountPassword
			s.accounts[i].TwoFactorSecret = strings.TrimSpace(update.TwoFactorSecret)
			s.accounts[i].GroupID = strings.TrimSpace(update.GroupID)
			if err := s.saveAccountsLocked(); err != nil {
				return model.Account{}, err
			}
			return s.accounts[i], nil
		}
	}
	return model.Account{}, fmt.Errorf("账号不存在: %s", accountID)
}

// UpdateAccountGroup 批量或单个修改账号的分组归属。
func (s *Store) UpdateAccountGroup(accountIDs []string, groupID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	groupID = strings.TrimSpace(groupID)
	if groupID != "" {
		exists := false
		for _, g := range s.groups {
			if g.ID == groupID {
				exists = true
				break
			}
		}
		if !exists {
			return fmt.Errorf("目标分组不存在: %s", groupID)
		}
	}

	targets := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		targets[id] = struct{}{}
	}
	for i := range s.accounts {
		if _, ok := targets[s.accounts[i].ID]; ok {
			s.accounts[i].GroupID = groupID
		}
	}
	return s.saveAccountsLocked()
}

// DeleteAccounts 批量删除账号；如果删除了当前激活账号则同步清空 currentAccountID。
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
		remaining = append(remaining, acc)
	}
	for i := range remaining {
		remaining[i].SortOrder = i
	}
	s.accounts = remaining
	if _, dropped := delSet[s.currentAccountID]; dropped {
		s.currentAccountID = ""
	}
	return s.saveAccountsLocked()
}

// ReorderAccounts 按传入的账号 ID 序列更新自定义排序。
func (s *Store) ReorderAccounts(orderedIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rank := make(map[string]int, len(orderedIDs))
	for idx, id := range orderedIDs {
		rank[id] = idx
	}
	nextOrder := len(orderedIDs)
	for i := range s.accounts {
		if pos, ok := rank[s.accounts[i].ID]; ok {
			s.accounts[i].SortOrder = pos
		} else {
			s.accounts[i].SortOrder = nextOrder
			nextOrder++
		}
	}
	return s.saveAccountsLocked()
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

	if group.ID != "" {
		for i := range s.groups {
			if s.groups[i].ID == group.ID {
				s.groups[i].Name = name
				s.groups[i].Color = color
				if err := s.saveGroupsLocked(); err != nil {
					return model.AccountGroup{}, err
				}
				return s.groups[i], nil
			}
		}
	}

	created := model.AccountGroup{
		ID:        uuid.NewString(),
		Name:      name,
		Color:     color,
		SortOrder: len(s.groups),
		CreatedAt: time.Now().Unix(),
	}
	s.groups = append(s.groups, created)
	if err := s.saveGroupsLocked(); err != nil {
		return model.AccountGroup{}, err
	}
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
	s.groups = remaining

	accountsChanged := false
	for i := range s.accounts {
		if s.accounts[i].GroupID == groupID {
			s.accounts[i].GroupID = ""
			accountsChanged = true
		}
	}

	if err := s.saveGroupsLocked(); err != nil {
		return err
	}
	if accountsChanged {
		return s.saveAccountsLocked()
	}
	return nil
}

// GetSettings 返回当前全局设置。
func (s *Store) GetSettings() model.AppSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.settings
}

// SaveSettings 保存全局设置。
func (s *Store) SaveSettings(next model.AppSettings) (model.AppSettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.settings = normalizeSettings(next)
	if err := s.saveSettingsLocked(); err != nil {
		return model.AppSettings{}, err
	}
	return s.settings, nil
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
	s.switchHistory = append([]model.SwitchHistoryItem{item}, s.switchHistory...)
	if len(s.switchHistory) > maxHistoryItems {
		s.switchHistory = s.switchHistory[:maxHistoryItems]
	}
	return s.saveSwitchHistoryLocked()
}

// ClearSwitchHistory 清空全部切号历史。
func (s *Store) ClearSwitchHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.switchHistory = nil
	return s.saveSwitchHistoryLocked()
}

// GetWakeupState 返回唤醒调度器总开关和任务配置。
func (s *Store) GetWakeupState() model.WakeupState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := model.WakeupState{
		Enabled: s.wakeupState.Enabled,
		Tasks:   make([]model.WakeupTask, len(s.wakeupState.Tasks)),
	}
	copy(out.Tasks, s.wakeupState.Tasks)
	return out
}

// SaveWakeupState 保存唤醒任务配置。
func (s *Store) SaveWakeupState(state model.WakeupState) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()
	for i := range state.Tasks {
		if strings.TrimSpace(state.Tasks[i].ID) == "" {
			state.Tasks[i].ID = uuid.NewString()
		}
		if state.Tasks[i].CreatedAt == 0 {
			state.Tasks[i].CreatedAt = now
		}
	}
	s.wakeupState = state
	return s.saveWakeupStateLocked()
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
	s.wakeupHistory = append([]model.WakeupHistoryItem{item}, s.wakeupHistory...)
	if len(s.wakeupHistory) > maxHistoryItems {
		s.wakeupHistory = s.wakeupHistory[:maxHistoryItems]
	}
	return s.saveWakeupHistoryLocked()
}

// ClearWakeupHistory 清空唤醒执行历史。
func (s *Store) ClearWakeupHistory() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wakeupHistory = nil
	return s.saveWakeupHistoryLocked()
}
