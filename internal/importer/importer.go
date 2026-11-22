package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/quota"
	"more-account/internal/storage"
	"more-account/internal/switcher"
	"more-account/internal/totp"
)

const batchImportConcurrency = 4

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

// AddByTokenResponse 直接复用 OAuth 授权码交换得到的 TokenResponse 拉取用户信息、入库并刷新配额，
// 避免丢弃全新的 AccessToken 后再次发起多余的 RefreshAccessToken 网络请求。
func (s *Service) AddByTokenResponse(ctx context.Context, tokenResp *oauth.TokenResponse, groupID string, meta *model.AccountDetailsUpdate) (model.Account, error) {
	if tokenResp == nil {
		return model.Account{}, fmt.Errorf("OAuth 令牌响应不能为空")
	}
	accessToken := strings.TrimSpace(tokenResp.AccessToken)
	if accessToken == "" {
		return model.Account{}, fmt.Errorf("access_token 不能为空")
	}
	refreshToken := strings.TrimSpace(tokenResp.RefreshToken)
	if refreshToken == "" {
		return model.Account{}, fmt.Errorf("refresh_token 不能为空")
	}

	var normalizedSecret string
	if meta != nil && strings.TrimSpace(meta.TwoFactorSecret) != "" {
		norm, err := totp.NormalizeSecret(meta.TwoFactorSecret)
		if err != nil {
			return model.Account{}, fmt.Errorf("2FA 密钥格式无效: %w", err)
		}
		normalizedSecret = norm
	}

	userInfo, err := s.oauthClient.FetchUserInfo(ctx, accessToken)
	if err != nil {
		return model.Account{}, fmt.Errorf("获取账号信息失败: %w", err)
	}

	now := time.Now().Unix()
	tokenType := strings.TrimSpace(tokenResp.TokenType)
	if tokenType == "" {
		tokenType = "Bearer"
	}
	clientKey := strings.TrimSpace(tokenResp.OAuthClientKey)
	if clientKey == "" {
		clientKey = oauth.DefaultOAuthClientKey
	}
	var expiryTS int64
	if tokenResp.ExpiresIn > 0 {
		expiryTS = now + tokenResp.ExpiresIn
	}

	candidate := model.Account{
		Email:   userInfo.Email,
		Name:    userInfo.DisplayName(),
		GroupID: strings.TrimSpace(groupID),
		Token: model.TokenData{
			AccessToken:     accessToken,
			RefreshToken:    refreshToken,
			ExpiresIn:       tokenResp.ExpiresIn,
			ExpiryTimestamp: expiryTS,
			TokenType:       tokenType,
			Email:           userInfo.Email,
			OAuthClientKey:  clientKey,
			IDToken:         strings.TrimSpace(tokenResp.IDToken),
		},
	}
	if meta != nil {
		candidate.Notes = strings.TrimSpace(meta.Notes)
		candidate.LoginAccount = strings.TrimSpace(meta.LoginAccount)
		// 登录密码保留原始首尾字符，不执行 TrimSpace
		candidate.AccountPassword = meta.AccountPassword
		candidate.TwoFactorSecret = normalizedSecret
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
		} else if latest, ok := s.store.GetAccount(acc.ID); ok {
			acc = latest
		}
	}
	return acc, nil
}

// AddByRefreshToken 通过单个 Refresh Token 换取用户信息并入库，随后尝试刷新配额。
func (s *Service) AddByRefreshToken(ctx context.Context, refreshToken string, groupID string, meta *model.AccountDetailsUpdate) (model.Account, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return model.Account{}, fmt.Errorf("refresh_token 不能为空")
	}

	// 在发网络请求前提前校验 2FA 密钥格式，避免无效条目白耗远程请求
	if meta != nil && strings.TrimSpace(meta.TwoFactorSecret) != "" {
		if _, err := totp.NormalizeSecret(meta.TwoFactorSecret); err != nil {
			return model.Account{}, fmt.Errorf("2FA 密钥格式无效: %w", err)
		}
	}

	tokenResp, err := s.oauthClient.RefreshAccessToken(ctx, rt)
	if err != nil {
		return model.Account{}, fmt.Errorf("验证 Refresh Token 失败: %w", err)
	}

	merged := *tokenResp
	if strings.TrimSpace(merged.RefreshToken) == "" {
		merged.RefreshToken = rt
	}
	return s.AddByTokenResponse(ctx, &merged, groupID, meta)
}

// ImportFromLocal 从本机已登录的反重力客户端自动提取当前账号（先刷新系统凭据缓存，再按目标客户端顺序与全量回退探测）。
func (s *Service) ImportFromLocal(ctx context.Context, groupID string) (model.Account, error) {
	// 主动失效系统凭据库内存缓存，防止用户刚刚登录客户端后点击导入命中旧负缓存
	switcher.InvalidateSystemCredentialCache()

	var refreshToken string

	target := s.store.GetSettings().RuntimeTarget
	if cred, _, err := switcher.ReadActiveCredentialForTarget(target); err == nil && cred != nil && strings.TrimSpace(cred.RefreshToken) != "" {
		refreshToken = cred.RefreshToken
	}

	// 若首选客户端（如 desktop/ide）未检测到有效凭据，补充回退读取 CLI 凭据文件
	if refreshToken == "" && !strings.EqualFold(strings.TrimSpace(target), "cli") {
		if cliCred, err := switcher.ReadCLICredential(); err == nil && cliCred != nil && strings.TrimSpace(cliCred.RefreshToken) != "" {
			refreshToken = cliCred.RefreshToken
		}
	}

	// 最后回退扫描本机其余存在的 state.vscdb（兼容切换前曾在另一客户端登录）
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
	// 本机导入的账号即当前正在使用的账号，同步将其设为当前激活账号并重读最新时间戳
	_ = s.store.SetCurrentAccountID(acc.ID)
	if latest, ok := s.store.GetAccount(acc.ID); ok {
		acc = latest
	}
	return acc, nil
}

// ImportRefreshTokens 批量导入多行 Refresh Token（支持每行一个 Token，或 JSON 数组/对象），采用可控并发提升大批量导入速度并保持原始顺序。
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
	tokens := make([]string, 0, len(lines))
	for _, line := range lines {
		token := strings.TrimSpace(line)
		if token == "" || strings.HasPrefix(token, "#") {
			continue
		}
		tokens = append(tokens, token)
	}

	res := &BatchImportResult{
		Imported: []model.Account{},
		Failed:   []BatchImportFailed{},
	}
	if len(tokens) == 0 {
		return res, nil
	}

	type tokenOutcome struct {
		acc model.Account
		err error
	}
	outcomes := make([]tokenOutcome, len(tokens))

	workers := batchImportConcurrency
	if len(tokens) < workers {
		workers = len(tokens)
	}
	jobs := make(chan int, len(tokens))
	for idx := range tokens {
		jobs <- idx
	}
	close(jobs)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if err := ctx.Err(); err != nil {
					outcomes[idx] = tokenOutcome{err: err}
					continue
				}
				acc, err := s.AddByRefreshToken(ctx, tokens[idx], groupID, nil)
				outcomes[idx] = tokenOutcome{acc: acc, err: err}
			}
		}()
	}
	wg.Wait()

	for idx, out := range outcomes {
		if out.err != nil {
			res.Failed = append(res.Failed, BatchImportFailed{
				Source: truncateLabelRunes(tokens[idx], 12),
				Error:  out.err.Error(),
			})
			continue
		}
		res.Imported = append(res.Imported, out.acc)
	}
	return res, nil
}

// ImportJSON 解析标准账号 JSON（兼容数组与单对象、camelCase 与 snake_case 字段），采用可控并发验证，并仅在账号导入成功后按需创建缺失分组。
func (s *Service) ImportJSON(ctx context.Context, jsonContent string, defaultGroupID string) (*BatchImportResult, error) {
	trimmed := strings.TrimSpace(jsonContent)
	if trimmed == "" {
		return nil, fmt.Errorf("JSON 内容不能为空")
	}

	var rawElements []any
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &rawElements); err != nil {
			return nil, fmt.Errorf("解析 JSON 数组失败: %w", err)
		}
	} else {
		var single map[string]any
		if err := json.Unmarshal([]byte(trimmed), &single); err != nil {
			return nil, fmt.Errorf("解析 JSON 对象失败: %w", err)
		}
		// 兼容 { "accounts": [...] } 包装格式；若 accounts 字段存在但非数组则明确报错
		if nestedRaw, hasAccounts := single["accounts"]; hasAccounts {
			nested, ok := nestedRaw.([]any)
			if !ok {
				return nil, fmt.Errorf("解析 JSON 对象失败: accounts 字段必须为数组")
			}
			rawElements = nested
		} else {
			rawElements = []any{single}
		}
	}

	groupByName := make(map[string]string)
	for _, g := range s.store.ListGroups() {
		groupByName[strings.ToLower(strings.TrimSpace(g.Name))] = g.ID
	}

	type parsedJob struct {
		source           string
		staticErr        string
		refreshToken     string
		targetGroupID    string
		pendingGroupName string
		meta             *model.AccountDetailsUpdate
	}

	jobs := make([]parsedJob, len(rawElements))
	runnableIndices := make([]int, 0, len(rawElements))

	for idx, elem := range rawElements {
		fallbackSource := fmt.Sprintf("第 %d 项", idx+1)
		item, ok := elem.(map[string]any)
		if !ok || item == nil {
			jobs[idx] = parsedJob{
				source:    fallbackSource,
				staticErr: "条目格式无效，必须为 JSON 对象",
			}
			continue
		}

		rt := pickString(item, "refresh_token", "refreshToken")
		if rt == "" {
			if tokObj, ok := item["token"].(map[string]any); ok {
				rt = pickString(tokObj, "refresh_token", "refreshToken")
			}
		}
		emailHint := pickString(item, "email")
		if emailHint == "" {
			emailHint = fallbackSource
		}
		if rt == "" {
			jobs[idx] = parsedJob{
				source:    emailHint,
				staticErr: "缺少 refresh_token",
			}
			continue
		}

		targetGroupID := strings.TrimSpace(defaultGroupID)
		pendingGroupName := ""
		if groupName := pickString(item, "group_name", "groupName"); groupName != "" {
			key := strings.ToLower(groupName)
			if gid, exists := groupByName[key]; exists {
				targetGroupID = gid
			} else {
				pendingGroupName = groupName
			}
		}

		meta := &model.AccountDetailsUpdate{
			Notes:           pickString(item, "notes", "note"),
			LoginAccount:    pickString(item, "login_account", "loginAccount", "account_username", "username"),
			AccountPassword: pickStringRaw(item, "account_password", "accountPassword"),
			TwoFactorSecret: pickString(item, "two_factor_secret", "twoFactorSecret"),
			GroupID:         targetGroupID,
		}

		jobs[idx] = parsedJob{
			source:           emailHint,
			refreshToken:     rt,
			targetGroupID:    targetGroupID,
			pendingGroupName: pendingGroupName,
			meta:             meta,
		}
		runnableIndices = append(runnableIndices, idx)
	}

	type jsonOutcome struct {
		acc model.Account
		err error
	}
	outcomes := make([]jsonOutcome, len(rawElements))

	if len(runnableIndices) > 0 {
		workers := batchImportConcurrency
		if len(runnableIndices) < workers {
			workers = len(runnableIndices)
		}
		workCh := make(chan int, len(runnableIndices))
		for _, idx := range runnableIndices {
			workCh <- idx
		}
		close(workCh)

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range workCh {
					if err := ctx.Err(); err != nil {
						outcomes[idx] = jsonOutcome{err: err}
						continue
					}
					job := jobs[idx]
					acc, err := s.AddByRefreshToken(ctx, job.refreshToken, job.targetGroupID, job.meta)
					outcomes[idx] = jsonOutcome{acc: acc, err: err}
				}
			}()
		}
		wg.Wait()
	}

	res := &BatchImportResult{
		Imported: []model.Account{},
		Failed:   []BatchImportFailed{},
	}

	// 按输入顺序统一归集结果，并在主协程中按需串行创建缺失分组，防止并发重复建组。
	for idx, job := range jobs {
		if job.staticErr != "" {
			res.Failed = append(res.Failed, BatchImportFailed{
				Source: job.source,
				Error:  job.staticErr,
			})
			continue
		}
		out := outcomes[idx]
		if out.err != nil {
			res.Failed = append(res.Failed, BatchImportFailed{
				Source: job.source,
				Error:  out.err.Error(),
			})
			continue
		}

		acc := out.acc
		if job.pendingGroupName != "" {
			key := strings.ToLower(job.pendingGroupName)
			gid, exists := groupByName[key]
			if !exists {
				if created, gErr := s.store.SaveGroup(model.AccountGroup{Name: job.pendingGroupName}); gErr == nil {
					gid = created.ID
					groupByName[key] = gid
				}
			}
			if gid != "" {
				if uErr := s.store.UpdateAccountGroup([]string{acc.ID}, gid); uErr == nil {
					acc.GroupID = gid
				}
			}
		}

		res.Imported = append(res.Imported, acc)
	}

	return res, nil
}

func truncateLabelRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes+4 {
		return s
	}
	return string(runes[:maxRunes]) + "..."
}

func pickString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// pickStringRaw 用于密码等敏感字符串，保留原始首尾空白字符。
func pickStringRaw(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
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
