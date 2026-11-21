package switcher

import (
	"context"
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

// IsSwitching 返回当前是否正在执行切换操作，用于避免后台账号探测器在写凭据中途并发误判。
func (s *Service) IsSwitching() bool {
	return s.switching.Load()
}

// SwitchAccount 执行完整切号流程：令牌预刷新 -> 停客户端 -> 写入系统凭据/数据库 -> 提交状态 -> 记历史 -> 重启客户端。
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
			Launched:     req.Launch,
			Success:      false,
			DurationMs:   time.Since(started).Milliseconds(),
			ErrorStage:   stage,
			ErrorMessage: err.Error(),
		})
		return nil, err
	}

	// 1. 切号前检查并自动刷新临期令牌
	freshTok, refreshed, err := s.oauthClient.EnsureFreshToken(ctx, acc.Token)
	if err != nil {
		return recordFailure("token_refresh", fmt.Errorf("切号前刷新令牌失败: %w", err))
	}
	if refreshed {
		acc.Token = freshTok
		if saved, saveErr := s.store.SaveAccount(acc); saveErr == nil {
			acc = saved
		}
	}

	settings := s.store.GetSettings()

	// 2. 若需重启客户端，先停止客户端及 macOS 共享 agy --hub
	var stoppedHubs []runningHub
	if req.Launch && target != "cli" {
		_ = stopClientProcesses(target)
		stoppedHubs = snapshotAndStopMacHubs()
	}

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
		if ideDB != "" {
			writeErr = InjectAccountToStateDB(ideDB, acc)
		}
		if writeErr == nil {
			_ = WriteSystemCredentials(acc, false)
		}
	}

	if len(stoppedHubs) > 0 {
		restartMacHubs(stoppedHubs)
	}

	if writeErr != nil {
		return recordFailure("credential_write", writeErr)
	}

	// 4. 仅在真实凭据写入成功后提交当前激活账号状态
	if err := s.store.SetCurrentAccountID(acc.ID); err != nil {
		return recordFailure("state_commit", fmt.Errorf("凭据已写入，但保存当前账号状态失败: %w", err))
	}
	if updated, found := s.store.GetAccount(acc.ID); found {
		acc = updated
	}

	// 5. 按需重启客户端（重启失败不回滚已成功的凭据切换）
	var launchWarning string
	if req.Launch && target != "cli" {
		if err := launchClientApp(target, settings.DesktopAppPath, settings.IdeAppPath); err != nil {
			launchWarning = err.Error()
		}
	}

	durationMs := time.Since(started).Milliseconds()
	_ = s.store.AppendSwitchHistory(model.SwitchHistoryItem{
		AccountID:    acc.ID,
		TargetEmail:  acc.Email,
		TargetClient: target,
		Launched:     req.Launch && target != "cli",
		Success:      true,
		DurationMs:   durationMs,
		ErrorStage:   warningStage(launchWarning),
		ErrorMessage: launchWarning,
	})

	return &SwitchOutcome{
		Account:       acc,
		TargetClient:  target,
		Launched:      req.Launch && target != "cli" && launchWarning == "",
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
