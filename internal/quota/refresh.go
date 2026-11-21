package quota

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

// Service 协调令牌刷新、配额查询与持久化落盘。
type Service struct {
	store       *storage.Store
	oauthClient *oauth.Client
	quotaClient *Client
}

// NewService 创建配额刷新服务。
func NewService(store *storage.Store, oauthClient *oauth.Client, quotaClient *Client) *Service {
	if oauthClient == nil {
		oauthClient = oauth.NewClient()
	}
	if quotaClient == nil {
		quotaClient = NewClient()
	}
	return &Service{
		store:       store,
		oauthClient: oauthClient,
		quotaClient: quotaClient,
	}
}

// RefreshAccount 刷新单个账号的令牌（如有需要）与模型配额，并持久化结果。
func (s *Service) RefreshAccount(ctx context.Context, accountID string) (model.Account, error) {
	acc, ok := s.store.GetAccount(accountID)
	if !ok {
		return model.Account{}, fmt.Errorf("账号不存在: %s", accountID)
	}

	freshToken, refreshed, err := s.oauthClient.EnsureFreshToken(ctx, acc.Token)
	if err != nil {
		now := time.Now().Unix()
		if errors.Is(err, oauth.ErrInvalidGrant) {
			acc.Disabled = true
			acc.DisabledReason = "invalid_grant: " + err.Error()
			acc.DisabledAt = now
		}
		acc.QuotaError = &model.QuotaErrorInfo{
			Message:   err.Error(),
			Reason:    classifyTokenErrorReason(err),
			Timestamp: now,
		}
		_, _ = s.store.SaveAccount(acc)
		return acc, err
	}

	if refreshed {
		acc.Token = freshToken
		// 令牌刷新恢复后，自动清除此前因 invalid_grant 造成的禁用标记。
		if acc.Disabled && strings.HasPrefix(acc.DisabledReason, "invalid_grant") {
			acc.Disabled = false
			acc.DisabledReason = ""
			acc.DisabledAt = 0
		}
	}

	res, err := s.quotaClient.FetchQuota(ctx, acc.Token, acc.Email)
	if err != nil {
		acc.QuotaError = &model.QuotaErrorInfo{
			Message:   err.Error(),
			Timestamp: time.Now().Unix(),
		}
		saved, _ := s.store.SaveAccount(acc)
		return saved, err
	}

	if res.ProjectID != "" {
		acc.Token.ProjectID = res.ProjectID
	}
	quotaCopy := res.Quota
	acc.Quota = &quotaCopy
	acc.QuotaError = res.Error
	if res.Error == nil && acc.Disabled && strings.HasPrefix(acc.DisabledReason, "invalid_grant") {
		acc.Disabled = false
		acc.DisabledReason = ""
		acc.DisabledAt = 0
	}

	saved, err := s.store.SaveAccount(acc)
	if err != nil {
		return acc, err
	}
	return saved, nil
}

func classifyTokenErrorReason(err error) string {
	if errors.Is(err, oauth.ErrInvalidGrant) {
		return "INVALID_GRANT"
	}
	return "TOKEN_REFRESH_FAILED"
}

// RefreshAll 并发刷新全部账号配额，通过 onProgress 向前端汇报实时进度。
func (s *Service) RefreshAll(ctx context.Context, concurrency int, onProgress func(done, total int)) (*model.RefreshStats, error) {
	accounts := s.store.ListAccounts()
	total := len(accounts)
	stats := &model.RefreshStats{Total: total}
	if total == 0 {
		return stats, nil
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > total {
		concurrency = total
	}

	jobs := make(chan string, total)
	for _, acc := range accounts {
		jobs <- acc.ID
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		finished atomic.Int32
	)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				_, err := s.RefreshAccount(ctx, id)
				mu.Lock()
				if err != nil {
					stats.Failed++
					stats.Errors = append(stats.Errors, err.Error())
				} else {
					stats.Success++
				}
				mu.Unlock()

				done := int(finished.Add(1))
				if onProgress != nil {
					onProgress(done, total)
				}
			}
		}()
	}

	wg.Wait()
	return stats, nil
}
