package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/quota"
	"more-account/internal/storage"
	"more-account/internal/switcher"
)

// ExportAccountItem 描述 JSON 导入导出用的标准轻量账号结构（不包含标签和辅助邮箱）。
type ExportAccountItem struct {
	Email           string `json:"email"`
	RefreshToken    string `json:"refresh_token"`
	GroupName       string `json:"group_name,omitempty"`
	Notes           string `json:"notes,omitempty"`
	LoginAccount    string `json:"login_account,omitempty"`
	AccountPassword string `json:"account_password,omitempty"`
	TwoFactorSecret string `json:"two_factor_secret,omitempty"`
}

// BatchImportResult 汇报批量导入账号的成功列表与失败明细。
type BatchImportResult struct {
	Imported []model.Account     `json:"imported"`
	Failed   []BatchImportFailed `json:"failed"`
}

// BatchImportFailed 表示批量导入中单条失败的错误详情。
type BatchImportFailed struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

// Service 负责本机读取、Refresh Token 导入以及 JSON 导入导出。
type Service struct {
	store        *storage.Store
	oauthClient  *oauth.Client
	quotaService *quota.Service
}

// NewService 创建导入导出服务。
func NewService(store *storage.Store, oauthClient *oauth.Client, quotaService *quota.Service) *Service {
	if oauthClient == nil {
		oauthClient = oauth.NewClient()
	}
	return &Service{
		store:        store,
		oauthClient:  oauthClient,
		quotaService: quotaService,
	}
}

// AddByRefreshToken 通过单个 Refresh Token 换取用户信息并入库，随后尝试刷新配额。
func (s *Service) AddByRefreshToken(ctx context.Context, refreshToken string, groupID string, meta *model.AccountDetailsUpdate) (model.Account, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return model.Account{}, fmt.Errorf("refresh_token 不能为空")
	}

	tokenResp, err := s.oauthClient.RefreshAccessToken(ctx, rt)
	if err != nil {
		return model.Account{}, fmt.Errorf("验证 Refresh Token 失败: %w", err)
	}
	userInfo, err := s.oauthClient.FetchUserInfo(ctx, tokenResp.AccessToken)
	if err != nil {
		return model.Account{}, fmt.Errorf("获取账号信息失败: %w", err)
	}

	finalRT := strings.TrimSpace(tokenResp.RefreshToken)
	if finalRT == "" {
		finalRT = rt
	}
	now := time.Now().Unix()

	candidate := model.Account{
		Email:   userInfo.Email,
		Name:    userInfo.DisplayName(),
		GroupID: strings.TrimSpace(groupID),
		Token: model.TokenData{
			AccessToken:     tokenResp.AccessToken,
			RefreshToken:    finalRT,
			ExpiresIn:       tokenResp.ExpiresIn,
			ExpiryTimestamp: now + tokenResp.ExpiresIn,
			TokenType:       "Bearer",
			Email:           userInfo.Email,
			OAuthClientKey:  oauth.DefaultOAuthClientKey,
			IDToken:         tokenResp.IDToken,
		},
	}
	if meta != nil {
		candidate.Notes = strings.TrimSpace(meta.Notes)
		candidate.LoginAccount = strings.TrimSpace(meta.LoginAccount)
		candidate.AccountPassword = meta.AccountPassword
		candidate.TwoFactorSecret = strings.TrimSpace(meta.TwoFactorSecret)
		if strings.TrimSpace(meta.GroupID) != "" {
			candidate.GroupID = strings.TrimSpace(meta.GroupID)
		}
	}

	acc, err := s.store.UpsertAccount(candidate)
	if err != nil {
		return model.Account{}, err
	}

	if s.quotaService != nil {
		if refreshedAcc, qErr := s.quotaService.RefreshAccount(ctx, acc.ID); qErr == nil {
			acc = refreshedAcc
		}
	}
	return acc, nil
}

// ImportFromLocal 从本机已登录的反重力客户端自动提取当前账号（按当前目标客户端优先顺序探测凭据）。
func (s *Service) ImportFromLocal(ctx context.Context, groupID string) (model.Account, error) {
	var refreshToken string

	target := s.store.GetSettings().RuntimeTarget
	if cred, _, err := switcher.ReadActiveCredentialForTarget(target); err == nil && cred != nil && strings.TrimSpace(cred.RefreshToken) != "" {
		refreshToken = cred.RefreshToken
	}

	if refreshToken == "" {
		if cred, err := switcher.ReadDesktopCredential(); err == nil && cred != nil && strings.TrimSpace(cred.RefreshToken) != "" {
			refreshToken = cred.RefreshToken
		}
	}

	if refreshToken == "" {
		for _, dbPath := range switcher.AllExistingStateDBPaths() {
			if rt, err := switcher.ReadRefreshTokenFromStateDB(dbPath); err == nil && strings.TrimSpace(rt) != "" {
				refreshToken = rt
				break
			}
		}
	}

	if strings.TrimSpace(refreshToken) == "" {
		return model.Account{}, fmt.Errorf("未在本机检测到已登录的 Antigravity 账号凭据（Keychain、桌面回退文件、CLI 及 state.vscdb 均为空）")
	}

	acc, err := s.AddByRefreshToken(ctx, refreshToken, groupID, nil)
	if err != nil {
		return model.Account{}, err
	}
	// 本机导入的账号即当前正在使用的账号，同步将其设为当前激活账号
	_ = s.store.SetCurrentAccountID(acc.ID)
	return acc, nil
}

// ImportRefreshTokens 批量导入多行 Refresh Token（支持每行一个 Token，或 JSON 数组/对象）。
func (s *Service) ImportRefreshTokens(ctx context.Context, rawInput string, groupID string) (*BatchImportResult, error) {
	trimmed := strings.TrimSpace(rawInput)
	if trimmed == "" {
		return nil, fmt.Errorf("导入内容不能为空")
	}

	// 若输入以 [ 或 { 开头，按 JSON 账号导入处理
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		return s.ImportJSON(ctx, trimmed, groupID)
	}

	lines := strings.Split(trimmed, "\n")
	res := &BatchImportResult{
		Imported: []model.Account{},
		Failed:   []BatchImportFailed{},
	}

	for _, line := range lines {
		token := strings.TrimSpace(line)
		if token == "" || strings.HasPrefix(token, "#") {
			continue
		}
		acc, err := s.AddByRefreshToken(ctx, token, groupID, nil)
		if err != nil {
			label := token
			if len(label) > 16 {
				label = label[:12] + "..."
			}
			res.Failed = append(res.Failed, BatchImportFailed{
				Source: label,
				Error:  err.Error(),
			})
			continue
		}
		res.Imported = append(res.Imported, acc)
	}
	return res, nil
}

// ImportJSON 解析标准账号 JSON（兼容数组与单对象、camelCase 与 snake_case 字段），并同步创建缺失的分组。
func (s *Service) ImportJSON(ctx context.Context, jsonContent string, defaultGroupID string) (*BatchImportResult, error) {
	trimmed := strings.TrimSpace(jsonContent)
	if trimmed == "" {
		return nil, fmt.Errorf("JSON 内容不能为空")
	}

	var rawItems []map[string]any
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &rawItems); err != nil {
			return nil, fmt.Errorf("解析 JSON 数组失败: %w", err)
		}
	} else {
		var single map[string]any
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("解析 JSON 对象失败: %w", err)
		}
		// 兼容 { "accounts": [...] } 包装格式
		if nested, ok := single["accounts"].([]any); ok {
			for _, elem := range nested {
				if m, ok := elem.(map[string]any); ok {
					rawItems = append(rawItems, m)
				}
			}
		} else {
			rawItems = []map[string]any{single}
		}
	}

	groupByName := make(map[string]string)
	for _, g := range s.store.ListGroups() {
		groupByName[strings.ToLower(strings.TrimSpace(g.Name))] = g.ID
	}

	res := &BatchImportResult{
		Imported: []model.Account{},
		Failed:   []BatchImportFailed{},
	}

	for idx, item := range rawItems {
		rt := pickString(item, "refresh_token", "refreshToken")
		if rt == "" {
			if tokObj, ok := item["token"].(map[string]any); ok {
				rt = pickString(tokObj, "refresh_token", "refreshToken")
			}
		}
		emailHint := pickString(item, "email")
		if emailHint == "" {
			emailHint = fmt.Sprintf("第 %d 项", idx+1)
		}
		if rt == "" {
			res.Failed = append(res.Failed, BatchImportFailed{
				Source: emailHint,
				Error:  "缺少 refresh_token",
			})
			continue
		}

		targetGroupID := strings.TrimSpace(defaultGroupID)
		if groupName := pickString(item, "group_name", "groupName"); groupName != "" {
			key := strings.ToLower(groupName)
			if gid, exists := groupByName[key]; exists {
				targetGroupID = gid
			} else if created, err := s.store.SaveGroup(model.AccountGroup{Name: groupName}); err == nil {
				groupByName[key] = created.ID
				targetGroupID = created.ID
			}
		}

		meta := &model.AccountDetailsUpdate{
			Notes:           pickString(item, "notes", "note"),
			LoginAccount:    pickString(item, "login_account", "loginAccount", "account_username", "username"),
			AccountPassword: pickString(item, "account_password", "accountPassword"),
			TwoFactorSecret: pickString(item, "two_factor_secret", "twoFactorSecret"),
			GroupID:         targetGroupID,
		}

		acc, err := s.AddByRefreshToken(ctx, rt, targetGroupID, meta)
		if err != nil {
			res.Failed = append(res.Failed, BatchImportFailed{
				Source: emailHint,
				Error:  err.Error(),
			})
			continue
		}
		res.Imported = append(res.Imported, acc)
	}

	return res, nil
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ExportJSON 将指定账号（accountIDs 为空时导出全部）序列化为标准 JSON，可选是否附带密码与 2FA 密钥。
func (s *Service) ExportJSON(accountIDs []string, includeSecrets bool) (string, error) {
	all := s.store.ListAccounts()
	groupNameMap := make(map[string]string)
	for _, g := range s.store.ListGroups() {
		groupNameMap[g.ID] = g.Name
	}

	filter := make(map[string]struct{}, len(accountIDs))
	for _, id := range accountIDs {
		filter[id] = struct{}{}
	}

	out := make([]ExportAccountItem, 0, len(all))
	for _, acc := range all {
		if len(filter) > 0 {
			if _, ok := filter[acc.ID]; !ok {
				continue
			}
		}
		item := ExportAccountItem{
			Email:        acc.Email,
			RefreshToken: acc.Token.RefreshToken,
			GroupName:    groupNameMap[acc.GroupID],
			Notes:        acc.Notes,
		}
		if includeSecrets {
			item.LoginAccount = acc.LoginAccount
			item.AccountPassword = acc.AccountPassword
			item.TwoFactorSecret = acc.TwoFactorSecret
		}
		out = append(out, item)
	}

	raw, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", fmt.Errorf("导出序列化失败: %w", err)
	}
	return string(raw), nil
}
