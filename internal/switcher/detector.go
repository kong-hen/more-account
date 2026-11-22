package switcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/storage"
)

const (
	activeDetectPollInterval = 10 * time.Second
	activeDetectStartupDelay = 500 * time.Millisecond
	emailResolveNegativeTTL  = 30 * time.Second
	maxEmailCacheSize        = 256
)

// ActiveDetectionResult 描述一次本机正在使用账号的探测结果。
type ActiveDetectionResult struct {
	HasCredential    bool   `json:"hasCredential"`              // 本机是否探测到有效登录凭据
	Managed          bool   `json:"managed"`                    // 探测到的账号是否已在本程序纳管
	MatchedAccountID string `json:"matchedAccountId,omitempty"` // 命中的纳管账号 ID
	DetectedEmail    string `json:"detectedEmail,omitempty"`    // 探测到的账号邮箱
	Source           string `json:"source,omitempty"`           // 凭据来源：keychain | desktop_fallback | desktop_statedb | ide_statedb | cli_file
	TargetClient     string `json:"targetClient"`               // 当前探测的目标客户端类型：desktop | ide | cli
	Changed          bool   `json:"changed"`                    // 本次探测是否引起当前激活账号状态变更
	DetectedAt       int64  `json:"detectedAt"`                 // 探测时间戳（秒）
	Error            string `json:"error,omitempty"`            // 读取凭据或联网校验异常信息
}

type cachedResolvedEmail struct {
	email     string
	checkedAt time.Time
}

// ActiveDetector 定期及按需探测本机反重力客户端当前登录账号，自动同步其他程序引起的切号变化。
type ActiveDetector struct {
	store       *storage.Store
	oauthClient *oauth.Client
	isSwitching func() bool
	readLocal   func(target string) (*SystemCredential, string, error)
	onChanged   func(result ActiveDetectionResult)
	nowFunc     func() time.Time

	loopMu   sync.Mutex
	cancel   context.CancelFunc
	wakeCh   chan struct{}
	detectMu sync.Mutex

	stateMu      sync.Mutex
	syncEpoch    uint64
	lastStateKey string
	lastResult   ActiveDetectionResult
	emailCache   map[string]cachedResolvedEmail
}

// NewActiveDetector 创建本机正在使用账号探测器。
func NewActiveDetector(
	store *storage.Store,
	oauthClient *oauth.Client,
	isSwitching func() bool,
	onChanged func(result ActiveDetectionResult),
) *ActiveDetector {
	return &ActiveDetector{
		store:       store,
		oauthClient: oauthClient,
		isSwitching: isSwitching,
		readLocal:   ReadActiveCredentialForTarget,
		onChanged:   onChanged,
		nowFunc:     time.Now,
		wakeCh:      make(chan struct{}, 1),
		emailCache:  make(map[string]cachedResolvedEmail),
	}
}

// Start 启动后台定期探测循环，并在短延迟后执行首次本机账号同步。
func (d *ActiveDetector) Start(parent context.Context) {
	d.loopMu.Lock()
	if d.cancel != nil {
		d.loopMu.Unlock()
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	d.cancel = cancel
	d.loopMu.Unlock()

	go d.loop(ctx)
}

// Stop 优雅停止后台探测协程。
func (d *ActiveDetector) Stop() {
	d.loopMu.Lock()
	cancel := d.cancel
	d.cancel = nil
	d.loopMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// TriggerCheck 非阻塞唤醒后台探测器立即执行一次探测（如窗口切回前台或切换目标客户端设置时）。
func (d *ActiveDetector) TriggerCheck() {
	InvalidateSystemCredentialCache()
	select {
	case d.wakeCh <- struct{}{}:
	default:
	}
}

// LastResult 返回最近一次探测结果快照。
func (d *ActiveDetector) LastResult() ActiveDetectionResult {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.lastResult
}

// SyncAfterInternalSwitch 在本程序内部完成切号或本机导入后同步基线状态并递增 syncEpoch，
// 使此前已发起但滞后返回的后台探测结果自动失效，彻底消除两段式 TOCTOU 竞态窗口。
func (d *ActiveDetector) SyncAfterInternalSwitch(acc model.Account) {
	if d.store == nil {
		return
	}
	target := normalizeTarget(d.store.GetSettings().RuntimeTarget)
	res := ActiveDetectionResult{
		HasCredential:    true,
		Managed:          true,
		MatchedAccountID: acc.ID,
		DetectedEmail:    acc.Email,
		TargetClient:     target,
		Changed:          false,
		DetectedAt:       d.nowFunc().Unix(),
	}
	d.stateMu.Lock()
	d.syncEpoch++
	d.lastStateKey = buildDetectionStateKey(res)
	d.lastResult = res
	d.stateMu.Unlock()
}

func (d *ActiveDetector) getSyncEpoch() uint64 {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.syncEpoch
}

func (d *ActiveDetector) isStaleDetection(startEpoch uint64) bool {
	if d.isSwitching != nil && d.isSwitching() {
		return true
	}
	return d.getSyncEpoch() != startEpoch
}

func (d *ActiveDetector) loop(ctx context.Context) {
	startupTimer := time.NewTimer(activeDetectStartupDelay)
	ticker := time.NewTicker(activeDetectPollInterval)
	defer startupTimer.Stop()
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-startupTimer.C:
			d.DetectOnce(ctx)
		case <-ticker.C:
			d.DetectOnce(ctx)
		case <-d.wakeCh:
			d.DetectOnce(ctx)
		}
	}
}

// DetectOnce 立即执行一次本机正在使用账号探测，并在发现变化时同步更新 store 与通知回调。
func (d *ActiveDetector) DetectOnce(ctx context.Context) ActiveDetectionResult {
	return d.detect(ctx, true)
}

// DetectOnceSilent 执行一次同步探测并更新基线，但不触发后台变更回调（供手动探测 RPC 调用，避免与事件推送重复弹通知）。
func (d *ActiveDetector) DetectOnceSilent(ctx context.Context) ActiveDetectionResult {
	InvalidateSystemCredentialCache()
	return d.detect(ctx, false)
}

func (d *ActiveDetector) detect(ctx context.Context, emitCallback bool) ActiveDetectionResult {
	d.detectMu.Lock()
	defer d.detectMu.Unlock()

	now := d.nowFunc()
	if d.store == nil {
		return ActiveDetectionResult{DetectedAt: now.Unix()}
	}
	target := normalizeTarget(d.store.GetSettings().RuntimeTarget)

	// 正在执行切号流程时跳过探测，避免读取到写入中途的半态凭据
	if d.isSwitching != nil && d.isSwitching() {
		return d.LastResult()
	}
	startEpoch := d.getSyncEpoch()

	readFn := d.readLocal
	if readFn == nil {
		readFn = ReadActiveCredentialForTarget
	}

	cred, source, err := readFn(target)
	// 读取系统凭据 I/O 期间若发生了内部切号（进行中或已递增 syncEpoch），直接丢弃过时探测结果
	if d.isStaleDetection(startEpoch) {
		return d.LastResult()
	}
	if err != nil {
		// 读取系统凭据失败时保持当前已知状态，不误清 currentAccountID
		res := ActiveDetectionResult{
			TargetClient: target,
			DetectedAt:   now.Unix(),
			Error:        err.Error(),
		}
		return res
	}

	res := ActiveDetectionResult{
		Source:       source,
		TargetClient: target,
		DetectedAt:   now.Unix(),
	}

	if cred == nil {
		// 本机所有受检凭据来源均明确为空（未登录或已退出）
		if d.isStaleDetection(startEpoch) {
			return d.LastResult()
		}
		prevID := d.store.GetCurrentAccountID()
		if prevID != "" {
			if setErr := d.store.SetCurrentAccountID(""); setErr == nil {
				res.Changed = true
			}
		}
		d.commitAndNotify(res, emitCallback, startEpoch)
		return res
	}

	res.HasCredential = true
	accounts := d.store.ListAccounts()
	matched, detectedEmail, resolveErr := d.matchAccount(ctx, cred, accounts, now)
	// 匹配与 OAuth 联网解析（最长 10s）返回后再次核验是否发生内部切号，封堵第二段 TOCTOU 竞态窗口
	if d.isStaleDetection(startEpoch) {
		return d.LastResult()
	}
	if resolveErr != nil {
		// 断网或 Google OAuth 临时不可达导致无法识别未知凭据身份：保留现有激活态，不误清空 currentAccountID。
		res.Error = resolveErr.Error()
		return res
	}
	res.DetectedEmail = detectedEmail

	prevID := d.store.GetCurrentAccountID()
	if matched != nil {
		res.Managed = true
		res.MatchedAccountID = matched.ID
		res.DetectedEmail = matched.Email

		// 若外部程序通过同邮箱重新登录产生了新的 RefreshToken，将其原子回写到本地账号库，避免后续刷新报 invalid_grant
		credRT := strings.TrimSpace(cred.RefreshToken)
		if credRT != "" && credRT != strings.TrimSpace(matched.Token.RefreshToken) {
			d.syncRotatedCredentialToStore(matched.ID, cred)
		}

		if d.isStaleDetection(startEpoch) {
			return d.LastResult()
		}
		if prevID != matched.ID {
			if setErr := d.store.SetCurrentAccountID(matched.ID); setErr == nil {
				res.Changed = true
			}
		}
	} else {
		// 探测到未在本程序纳管的外部账号：清空旧的错误激活标记，由前端展示外部账号并支持一键导入
		res.Managed = false
		if prevID != "" {
			if setErr := d.store.SetCurrentAccountID(""); setErr == nil {
				res.Changed = true
			}
		}
	}

	if !d.commitAndNotify(res, emitCallback, startEpoch) {
		return d.LastResult()
	}
	return res
}

// syncRotatedCredentialToStore 在外部客户端通过同邮箱重新登录并轮换 RefreshToken 时，
// 原子回写最新令牌字段并自动解除因旧令牌失效引起的 invalid_grant 禁用态。
func (d *ActiveDetector) syncRotatedCredentialToStore(accountID string, cred *SystemCredential) {
	if d.store == nil || cred == nil {
		return
	}
	newRT := strings.TrimSpace(cred.RefreshToken)
	if newRT == "" {
		return
	}
	newAT := strings.TrimSpace(cred.AccessToken)
	newIDTok := strings.TrimSpace(cred.IDToken)
	var newExpiry int64
	if expStr := strings.TrimSpace(cred.Expiry); expStr != "" {
		if ts, err := time.Parse(time.RFC3339Nano, expStr); err == nil {
			newExpiry = ts.Unix()
		} else if ts, err := time.Parse(time.RFC3339, expStr); err == nil {
			newExpiry = ts.Unix()
		}
	}

	_, _ = d.store.UpdateAccount(accountID, func(target *model.Account) error {
		target.Token.RefreshToken = newRT
		if newAT != "" {
			target.Token.AccessToken = newAT
			if newExpiry > 0 {
				target.Token.ExpiryTimestamp = newExpiry
			}
		}
		if newIDTok != "" {
			target.Token.IDToken = newIDTok
		}
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

func (d *ActiveDetector) commitAndNotify(res ActiveDetectionResult, emitCallback bool, startEpoch uint64) bool {
	key := buildDetectionStateKey(res)

	d.stateMu.Lock()
	if d.syncEpoch != startEpoch || (d.isSwitching != nil && d.isSwitching()) {
		d.stateMu.Unlock()
		return false
	}
	stateChanged := key != d.lastStateKey
	d.lastStateKey = key
	d.lastResult = res
	d.stateMu.Unlock()

	if emitCallback && (res.Changed || stateChanged) && d.onChanged != nil {
		d.onChanged(res)
	}
	return true
}

func (d *ActiveDetector) matchAccount(
	ctx context.Context,
	cred *SystemCredential,
	accounts []model.Account,
	now time.Time,
) (*model.Account, string, error) {
	if cred == nil {
		return nil, "", nil
	}

	// 1. 优先本地精确匹配 RefreshToken（零网络开销）
	rt := strings.TrimSpace(cred.RefreshToken)
	if rt != "" {
		for i := range accounts {
			if strings.TrimSpace(accounts[i].Token.RefreshToken) == rt {
				acc := accounts[i]
				return &acc, acc.Email, nil
			}
		}
	}

	// 2. 其次使用凭据内自带的邮箱（来自 id_token JWT 或 state.vscdb userStatus，零网络开销）
	email := strings.TrimSpace(cred.Email)
	if email != "" {
		if acc, ok := findAccountByEmail(accounts, email); ok {
			return &acc, acc.Email, nil
		}
		return nil, email, nil
	}

	// 3. 兜底：若第三方程序写入的凭据不含 id_token 且 RT 未命中，按 RT 哈希缓存查询一次 Google UserInfo
	resolved, err := d.resolveEmailViaOAuth(ctx, cred, now)
	if err != nil {
		return nil, "", err
	}
	if resolved != "" {
		if acc, ok := findAccountByEmail(accounts, resolved); ok {
			return &acc, acc.Email, nil
		}
		return nil, resolved, nil
	}
	return nil, "", nil
}

func (d *ActiveDetector) resolveEmailViaOAuth(ctx context.Context, cred *SystemCredential, now time.Time) (string, error) {
	if d.oauthClient == nil || cred == nil {
		return "", nil
	}
	cacheSeed := strings.TrimSpace(cred.RefreshToken)
	if cacheSeed == "" {
		cacheSeed = strings.TrimSpace(cred.AccessToken)
	}
	if cacheSeed == "" {
		return "", nil
	}
	sum := sha256.Sum256([]byte(cacheSeed))
	fp := hex.EncodeToString(sum[:])

	d.stateMu.Lock()
	if cached, ok := d.emailCache[fp]; ok {
		if cached.email != "" {
			d.stateMu.Unlock()
			return cached.email, nil
		}
		if now.Sub(cached.checkedAt) < emailResolveNegativeTTL {
			d.stateMu.Unlock()
			return "", fmt.Errorf("暂无法联网验证本机未知凭据身份，稍后重试")
		}
	}
	d.stateMu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var resolvedEmail string
	var lastErr error
	if at := strings.TrimSpace(cred.AccessToken); at != "" {
		info, err := d.oauthClient.FetchUserInfo(callCtx, at)
		if err == nil && info != nil {
			resolvedEmail = strings.TrimSpace(info.Email)
		} else if err != nil {
			lastErr = err
		}
	}
	if resolvedEmail == "" && strings.TrimSpace(cred.RefreshToken) != "" {
		tok, err := d.oauthClient.RefreshAccessToken(callCtx, cred.RefreshToken)
		if err == nil && tok != nil {
			if tok.IDToken != "" {
				resolvedEmail = ExtractEmailFromIDToken(tok.IDToken)
			}
			if resolvedEmail == "" && tok.AccessToken != "" {
				info, uErr := d.oauthClient.FetchUserInfo(callCtx, tok.AccessToken)
				if uErr == nil && info != nil {
					resolvedEmail = strings.TrimSpace(info.Email)
				} else if uErr != nil {
					lastErr = uErr
				}
			}
		} else if err != nil {
			lastErr = err
		}
	}

	d.stateMu.Lock()
	if len(d.emailCache) >= maxEmailCacheSize {
		d.emailCache = make(map[string]cachedResolvedEmail)
	}
	d.emailCache[fp] = cachedResolvedEmail{
		email:     resolvedEmail,
		checkedAt: now,
	}
	d.stateMu.Unlock()

	if resolvedEmail == "" && lastErr != nil {
		return "", fmt.Errorf("联网解析本机凭据邮箱失败: %w", lastErr)
	}
	return resolvedEmail, nil
}

func findAccountByEmail(accounts []model.Account, email string) (model.Account, bool) {
	target := strings.ToLower(strings.TrimSpace(email))
	if target == "" {
		return model.Account{}, false
	}
	for _, acc := range accounts {
		if strings.ToLower(strings.TrimSpace(acc.Email)) == target {
			return acc, true
		}
	}
	return model.Account{}, false
}

func buildDetectionStateKey(res ActiveDetectionResult) string {
	return fmt.Sprintf(
		"%t|%t|%s|%s|%s",
		res.HasCredential,
		res.Managed,
		res.MatchedAccountID,
		strings.ToLower(strings.TrimSpace(res.DetectedEmail)),
		res.TargetClient,
	)
}
