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

// RefreshAccount 刷新单个账号的令牌（如有需要）与模型配额，并采用原子局部更新持久化，
// 避免并发覆盖用户在刷新耗时期间编辑的备注、密码、2FA 密钥或分组。
func (s *Service) RefreshAccount(ctx context.Context, accountID string) (model.Account, error) {
	acc, ok := s.store.GetAccount(accountID)
	if !ok {
		return model.Account{}, fmt.Errorf("账号不存在: %s", accountID)
	}

	freshToken, refreshed, err := s.oauthClient.EnsureFreshToken(ctx, acc.Token)
	if err != nil {
		isInvalid := errors.Is(err, oauth.ErrInvalidGrant)
		if saved, updateErr := s.store.RecordTokenRefreshFailure(accountID, err, isInvalid); updateErr == nil {
			return saved, err
		}
		acc.QuotaError = &model.QuotaErrorInfo{
			Message:   err.Error(),
			Timestamp: time.Now().Unix(),
		}
		return acc, err
	}

	tokenForFetch := acc.Token
	if refreshed {
		tokenForFetch = freshToken
	}

	res, err := s.quotaClient.FetchQuota(ctx, tokenForFetch, acc.Email)
	if err != nil {
		errInfo := &model.QuotaErrorInfo{
			Message:   err.Error(),
			Timestamp: time.Now().Unix(),
		}
		saved, updateErr := s.store.UpdateAccount(accountID, func(target *model.Account) error {
			if refreshed {
				target.Token = freshToken
				if target.Disabled && strings.HasPrefix(target.DisabledReason, "invalid_grant") {
					target.Disabled = false
					target.DisabledReason = ""
					target.DisabledAt = 0
				}
			}
			target.QuotaError = errInfo
			return nil
		})
		if updateErr == nil {
			return saved, err
		}
		acc.QuotaError = errInfo
		return acc, err
	}

	saved, saveErr := s.store.UpdateAccount(accountID, func(target *model.Account) error {
		if refreshed {
			target.Token = freshToken
			// 令牌刷新恢复后，自动清除此前因 invalid_grant 造成的禁用标记。
			if target.Disabled && strings.HasPrefix(target.DisabledReason, "invalid_grant") {
				target.Disabled = false
				target.DisabledReason = ""
				target.DisabledAt = 0
			}
		}
		if res.ProjectID != "" {
			target.Token.ProjectID = res.ProjectID
		}

		quotaCopy := res.Quota
		// 若 loadCodeAssist 或 retrieveUserQuotaSummary 本次瞬时失败，保留账号既有订阅、积分与周额度桶，防止清空有效数据。
		if target.Quota != nil {
			if !res.TierLoaded {
				if quotaCopy.SubscriptionTier == "" {
					quotaCopy.SubscriptionTier = target.Quota.SubscriptionTier
				}
				if quotaCopy.TierID == "" {
					quotaCopy.TierID = target.Quota.TierID
				}
				if len(quotaCopy.Credits) == 0 && len(target.Quota.Credits) > 0 {
					quotaCopy.Credits = append([]model.CreditInfo(nil), target.Quota.Credits...)
				}
			}
			if !res.SummaryLoaded && len(quotaCopy.SummaryBuckets) == 0 && len(target.Quota.SummaryBuckets) > 0 {
				quotaCopy.SummaryBuckets = append([]model.QuotaBucket(nil), target.Quota.SummaryBuckets...)
			}
		}

		target.Quota = &quotaCopy
		target.QuotaError = res.Error
		if res.Error == nil && target.Disabled && strings.HasPrefix(target.DisabledReason, "invalid_grant") {
			target.Disabled = false
			target.DisabledReason = ""
			target.DisabledAt = 0
		}
		return nil
	})
	if saveErr != nil {
		return acc, saveErr
	}
	return saved, nil
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
