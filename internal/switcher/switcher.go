package switcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/storage"
)

// SwitchRequest 描述一次前端发起的切号请求。
type SwitchRequest struct {
	AccountID    string `json:"accountId"`
	TargetClient string `json:"targetClient"` // desktop | ide | cli
	Launch       bool   `json:"launch"`
}

// SwitchOutcome 描述切号完成后的返回结果。
type SwitchOutcome struct {
	Account       model.Account `json:"account"`
	TargetClient  string        `json:"targetClient"`
	Launched      bool          `json:"launched"`
	LaunchWarning string        `json:"launchWarning,omitempty"`
	DurationMs    int64         `json:"durationMs"`
}

// Service 编排反重力账号切换，内置单飞锁防止并发切号导致凭据竞争写入。
type Service struct {
	mu          sync.Mutex
	switching   atomic.Bool
	store       *storage.Store
	oauthClient *oauth.Client
	onSwitched  func(acc model.Account)
}

// NewService 创建切号服务实例。
func NewService(store *storage.Store, oauthClient *oauth.Client) *Service {
	if oauthClient == nil {
		oauthClient = oauth.NewClient()
	}
	return &Service{
		store:       store,
		oauthClient: oauthClient,
	}
}

// SetOnSwitched 注册内部切号成功回调，在释放 switching 状态前同步探测器基线，彻底消除误报外部切号的竞态窗口。
func (s *Service) SetOnSwitched(fn func(acc model.Account)) {
	s.mu.Lock()
	s.onSwitched = fn
	s.mu.Unlock()
}

// IsSwitching 返回当前是否正在执行切换操作，用于避免后台账号探测器在写凭据中途并发误判。
func (s *Service) IsSwitching() bool {
	return s.switching.Load()
}

// SwitchAccount 执行完整切号流程：令牌预刷新 -> 停客户端/共享 hub -> 写入系统凭据/数据库 -> 提交状态 -> 记历史 -> 重启客户端。
func (s *Service) SwitchAccount(ctx context.Context, req SwitchRequest) (*SwitchOutcome, error) {
	if !s.mu.TryLock() {
		return nil, fmt.Errorf("正在执行另一次账号切换，请稍候再试")
	}
	s.switching.Store(true)
	defer func() {
		s.switching.Store(false)
		s.mu.Unlock()
	}()

	started := time.Now()
	target := normalizeTarget(req.TargetClient)

	acc, ok := s.store.GetAccount(req.AccountID)
	if !ok {
		return nil, fmt.Errorf("目标账号不存在: %s", req.AccountID)
	}

	recordFailure := func(stage string, err error) (*SwitchOutcome, error) {
		_ = s.store.AppendSwitchHistory(model.SwitchHistoryItem{
			AccountID:    acc.ID,
			TargetEmail:  acc.Email,
			TargetClient: target,
			Launched:     false,
			Success:      false,
			DurationMs:   time.Since(started).Milliseconds(),
			ErrorStage:   stage,
			ErrorMessage: err.Error(),
		})
		return nil, err
	}

	// 1. 切号前检查并自动刷新临期令牌（使用原子局部更新，同步 invalid_grant 禁用与恢复状态）
	freshTok, refreshed, err := s.oauthClient.EnsureFreshToken(ctx, acc.Token)
	if err != nil {
		_, _ = s.store.RecordTokenRefreshFailure(acc.ID, err, errors.Is(err, oauth.ErrInvalidGrant))
		return recordFailure("token_refresh", fmt.Errorf("切号前刷新令牌失败: %w", err))
	}
	if refreshed {
		if saved, saveErr := s.store.RecordTokenRefreshSuccess(acc.ID, freshTok); saveErr == nil {
			acc = saved
		} else {
			acc.Token = freshTok
		}
	}

	settings := s.store.GetSettings()

	// 2. 停止客户端（若需重启且非 CLI）并协同暂停 macOS 共享 agy --hub（所有模式均共享 Keychain/回退凭据，避免 hub 内存缓存覆写新凭据）
	if target != "cli" && req.Launch {
		_ = stopClientProcesses(target, settings.DesktopAppPath, settings.IdeAppPath)
	}
	stoppedHubs := snapshotAndStopMacHubs()

	// 3. 写入目标客户端凭据
	var writeErr error
	switch target {
	case "desktop":
		writeErr = WriteSystemCredentials(acc, false)
		// 若本机存在旧版 desktop state.vscdb，一并同步兼容
		if writeErr == nil {
			if legacyDB := DefaultDesktopStateDBPath(); legacyDB != "" {
				for _, existing := range AllExistingStateDBPaths() {
					if existing == legacyDB {
						_ = InjectAccountToStateDB(legacyDB, acc)
					}
				}
			}
		}
	case "cli":
		writeErr = WriteSystemCredentials(acc, true)
	case "ide":
		// Antigravity IDE 同时写入 state.vscdb 与共享系统凭据，兼容新旧版本
		ideDB := DefaultIDEStateDBPath()
		if ideDB == "" {
			writeErr = fmt.Errorf("无法定位 Antigravity IDE state.vscdb 路径")
		} else {
			writeErr = InjectAccountToStateDB(ideDB, acc)
		}
		if writeErr == nil {
			if sysErr := WriteSystemCredentials(acc, false); sysErr != nil {
				writeErr = sysErr
			}
		}
	}

	if len(stoppedHubs) > 0 {
		restartMacHubs(stoppedHubs)
	}

	if writeErr != nil {
		return recordFailure("credential_write", writeErr)
	}

	// 4. 仅在真实凭据写入成功后提交当前激活账号状态，并在释放 switching 锁前同步内部切号基线
	if err := s.store.SetCurrentAccountID(acc.ID); err != nil {
		return recordFailure("state_commit", fmt.Errorf("凭据已写入，但保存当前账号状态失败: %w", err))
	}
	if updated, found := s.store.GetAccount(acc.ID); found {
		acc = updated
	}
	if s.onSwitched != nil {
		s.onSwitched(acc)
	}

	// 5. 按需重启客户端（重启失败不回滚已成功的凭据切换）
	var launchWarning string
	if req.Launch && target != "cli" {
		if err := launchClientApp(target, settings.DesktopAppPath, settings.IdeAppPath); err != nil {
			launchWarning = err.Error()
		}
	}

	launchedOK := req.Launch && target != "cli" && launchWarning == ""
	durationMs := time.Since(started).Milliseconds()
	_ = s.store.AppendSwitchHistory(model.SwitchHistoryItem{
		AccountID:    acc.ID,
		TargetEmail:  acc.Email,
		TargetClient: target,
		Launched:     launchedOK,
		Success:      true,
		DurationMs:   durationMs,
		ErrorStage:   warningStage(launchWarning),
		ErrorMessage: launchWarning,
	})

	return &SwitchOutcome{
		Account:       acc,
		TargetClient:  target,
		Launched:      launchedOK,
		LaunchWarning: launchWarning,
		DurationMs:    durationMs,
	}, nil
}

func warningStage(warn string) string {
	if strings.TrimSpace(warn) != "" {
		return "client_launch"
	}
	return ""
}

func normalizeTarget(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "ide":
		return "ide"
	case "cli":
		return "cli"
	default:
		return "desktop"
	}
}
