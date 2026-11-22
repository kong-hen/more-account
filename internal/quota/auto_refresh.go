package quota

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"more-account/internal/model"
	"more-account/internal/storage"
)

const (
	autoRefreshTickInterval   = 30 * time.Second
	autoRefreshStartupDelay   = 1500 * time.Millisecond
	autoRefreshResetGraceSecs = 5
)

// AutoRefresher 负责按配置周期、启动保鲜以及额度重置时间点在后台自动刷新账号配额。
type AutoRefresher struct {
	store          *storage.Store
	service        *Service
	onStatusChange func(running bool)
	onUpdated      func(refreshedCount int)
	nowFunc        func() time.Time

	mu                   sync.Mutex
	cancel               context.CancelFunc
	wakeCh               chan struct{}
	running              atomic.Bool
	summaryBackfillTried map[string]bool
}

// NewAutoRefresher 创建后台配额自动刷新调度器。
func NewAutoRefresher(
	store *storage.Store,
	service *Service,
	onStatusChange func(running bool),
	onUpdated func(refreshedCount int),
) *AutoRefresher {
	return &AutoRefresher{
		store:                store,
		service:              service,
		onStatusChange:       onStatusChange,
		onUpdated:            onUpdated,
		nowFunc:              time.Now,
		wakeCh:               make(chan struct{}, 1),
		summaryBackfillTried: make(map[string]bool),
	}
}

// Start 启动后台定时巡检协程，并在短延迟后执行一次启动保鲜检查。
func (r *AutoRefresher) Start(parent context.Context) {
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.mu.Unlock()

	go r.loop(ctx)
}

// Stop 优雅停止后台自动刷新调度器。
func (r *AutoRefresher) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// TriggerCheck 立即唤醒调度器检查是否有到期需要刷新的账号（如修改设置或窗口切回前台时调用）。
func (r *AutoRefresher) TriggerCheck() {
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

func (r *AutoRefresher) loop(ctx context.Context) {
	startupTimer := time.NewTimer(autoRefreshStartupDelay)
	ticker := time.NewTicker(autoRefreshTickInterval)
	defer startupTimer.Stop()
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-startupTimer.C:
			r.RunDueOnce(ctx)
		case <-ticker.C:
			r.RunDueOnce(ctx)
		case <-r.wakeCh:
			r.RunDueOnce(ctx)
		}
	}
}

// RunDueOnce 检查并并发刷新当前所有已到期（超时、缺少周额度桶或重置窗口刚到期）的可用账号。
// 当前正在使用的账号与未使用账号分别遵循各自配置的自动刷新周期。
func (r *AutoRefresher) RunDueOnce(ctx context.Context) int {
	if r.store == nil || r.service == nil {
		return 0
	}
	settings := r.store.GetSettings()
	quotaResetWatched := collectQuotaResetWatchedAccountIDs(r.store.GetWakeupState())
	if settings.AutoRefreshMinutes <= 0 && settings.InactiveAutoRefreshMinutes <= 0 && len(quotaResetWatched) == 0 {
		return 0
	}
	if !r.running.CompareAndSwap(false, true) {
		return 0
	}
	defer r.running.Store(false)

	now := r.nowFunc()
	var activeInterval time.Duration
	if settings.AutoRefreshMinutes > 0 {
		activeInterval = time.Duration(settings.AutoRefreshMinutes) * time.Minute
	}
	var inactiveInterval time.Duration
	if settings.InactiveAutoRefreshMinutes > 0 {
		inactiveInterval = time.Duration(settings.InactiveAutoRefreshMinutes) * time.Minute
	}
	currentAccountID := r.store.GetCurrentAccountID()
	accounts := r.store.ListAccounts()
	aliveIDs := make(map[string]struct{}, len(accounts))
	for _, acc := range accounts {
		aliveIDs[acc.ID] = struct{}{}
	}

	r.mu.Lock()
	for id := range r.summaryBackfillTried {
		if _, ok := aliveIDs[id]; !ok {
			delete(r.summaryBackfillTried, id)
		}
	}
	dueIDs, backfillIDs := selectDueAccountIDsWithResetWatch(
		accounts,
		currentAccountID,
		now,
		activeInterval,
		inactiveInterval,
		r.summaryBackfillTried,
		quotaResetWatched,
	)
	r.mu.Unlock()

	if len(dueIDs) == 0 {
		return 0
	}

	backfillSet := make(map[string]struct{}, len(backfillIDs))
	for _, id := range backfillIDs {
		backfillSet[id] = struct{}{}
	}

	if r.onStatusChange != nil {
		r.onStatusChange(true)
		defer r.onStatusChange(false)
	}

	refreshed, succeededIDs := r.refreshIDs(ctx, dueIDs, 4)
	if len(succeededIDs) > 0 {
		// 仅在刷新实际成功时记录（含首次拉取后确认无 summaryBuckets 的账号），避免 30s 后无意义二次补拉，同时保证断网失败后仍可重试。
		r.mu.Lock()
		for _, id := range succeededIDs {
			if _, ok := backfillSet[id]; ok {
				r.summaryBackfillTried[id] = true
				continue
			}
			if latest, ok := r.store.GetAccount(id); ok && latest.Quota != nil && len(latest.Quota.Models) > 0 && len(latest.Quota.SummaryBuckets) == 0 {
				r.summaryBackfillTried[id] = true
			}
		}
		r.mu.Unlock()
	}
	if r.onUpdated != nil {
		r.onUpdated(refreshed)
	}
	return refreshed
}

func (r *AutoRefresher) refreshIDs(ctx context.Context, ids []string, concurrency int) (int, []string) {
	if len(ids) == 0 {
		return 0, nil
	}
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > len(ids) {
		concurrency = len(ids)
	}

	jobs := make(chan string, len(ids))
	for _, id := range ids {
		jobs <- id
	}
	close(jobs)

	var (
		wg        sync.WaitGroup
		updated   atomic.Int32
		okMu      sync.Mutex
		succeeded []string
	)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				if ctx.Err() != nil {
					return
				}
				callCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
				_, err := r.service.RefreshAccount(callCtx, id)
				cancel()
				if err == nil {
					updated.Add(1)
					okMu.Lock()
					succeeded = append(succeeded, id)
					okMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return int(updated.Load()), succeeded
}

// collectQuotaResetWatchedAccountIDs 收集已启用的配额重置（quota_reset / wakeOnReset）唤醒任务所引用的账号 ID。
// 当用户关闭「未使用账号自动刷新」时，仍对这些账号在重置窗口到期后提供保底刷新，避免配额重置唤醒失效。
func collectQuotaResetWatchedAccountIDs(ws model.WakeupState) map[string]bool {
	if !ws.Enabled || len(ws.Tasks) == 0 {
		return nil
	}
	watched := make(map[string]bool)
	for _, task := range ws.Tasks {
		if !task.Enabled {
			continue
		}
		if task.Schedule.RepeatMode != "quota_reset" && !task.Schedule.WakeOnReset {
			continue
		}
		for _, id := range task.Schedule.SelectedAccounts {
			trimmed := strings.TrimSpace(id)
			if trimmed != "" {
				watched[trimmed] = true
			}
		}
	}
	if len(watched) == 0 {
		return nil
	}
	return watched
}

// selectDueAccountIDs 筛选需要自动刷新的账号 ID（兼容无 quota_reset 监听的既有调用）。
func selectDueAccountIDs(
	accounts []model.Account,
	currentAccountID string,
	now time.Time,
	activeInterval time.Duration,
	inactiveInterval time.Duration,
	summaryBackfillTried map[string]bool,
) (due []string, backfill []string) {
	return selectDueAccountIDsWithResetWatch(
		accounts,
		currentAccountID,
		now,
		activeInterval,
		inactiveInterval,
		summaryBackfillTried,
		nil,
	)
}

// selectDueAccountIDsWithResetWatch 筛选需要自动刷新的账号 ID（跳过已失效、该类别已关闭自动刷新且无配额重置唤醒依赖、以及处于失败退避期内的账号）：
// - 当前正在使用的账号使用 activeInterval，未使用账号使用 inactiveInterval；
// - 若某非当前账号关闭了常规自动刷新（interval<=0），但被已启用的 quota_reset 唤醒任务监听，则在缺少配额基线或重置窗口到期后保底刷新；
// 1) 从未拉取过配额；
// 2) 已有模型但缺少 5h/周共享额度桶（单次进程内仅立即补拉一次，避免空桶账号死循环）；
// 3) 距离上次刷新已达到该账号类别配置的自动刷新间隔；
// 4) 任意 5h/周额度或模型的重置时间刚到期（超过 5 秒宽限期）。
func selectDueAccountIDsWithResetWatch(
	accounts []model.Account,
	currentAccountID string,
	now time.Time,
	activeInterval time.Duration,
	inactiveInterval time.Duration,
	summaryBackfillTried map[string]bool,
	quotaResetWatched map[string]bool,
) (due []string, backfill []string) {
	nowUnix := now.Unix()
	activeSecs := int64(activeInterval.Seconds())
	inactiveSecs := int64(inactiveInterval.Seconds())
	if activeSecs <= 0 && inactiveSecs <= 0 && len(quotaResetWatched) == 0 {
		return nil, nil
	}

	due = make([]string, 0)
	backfill = make([]string, 0)
	for _, acc := range accounts {
		if acc.Disabled {
			continue
		}
		intervalSecs := inactiveSecs
		if currentAccountID != "" && acc.ID == currentAccountID {
			intervalSecs = activeSecs
		}
		watchedForReset := quotaResetWatched[acc.ID]
		// 当前账号所属类别（使用中 / 未使用）若关闭自动刷新，且未被已启用配额重置唤醒任务监听，则跳过
		if intervalSecs <= 0 && !watchedForReset {
			continue
		}

		// 若最近一次尝试以错误告终（如断网/代理波动），按退避窗口（2~10 分钟上限）等待后再重试，避免每 30 秒空转
		if acc.QuotaError != nil && acc.QuotaError.Timestamp > 0 {
			lastSuccess := int64(0)
			if acc.Quota != nil {
				lastSuccess = acc.Quota.LastUpdated
			}
			if acc.QuotaError.Timestamp >= lastSuccess {
				baseRetry := intervalSecs
				if baseRetry <= 0 {
					baseRetry = 5 * 60
				}
				retryInterval := maxInt64(minInt64(baseRetry, 10*60), 2*60)
				if nowUnix-acc.QuotaError.Timestamp < retryInterval {
					continue
				}
			}
		}
		if acc.Quota == nil {
			due = append(due, acc.ID)
			continue
		}
		// 处于明确 403 封禁态时按较长间隔低频复查（至少 30 分钟），避免高频无效请求
		if acc.Quota.IsForbidden {
			if intervalSecs > 0 && nowUnix-acc.Quota.LastUpdated >= maxInt64(intervalSecs, 30*60) {
				due = append(due, acc.ID)
			}
			continue
		}
		// 仅由 quota_reset 唤醒任务保底监听（未开启常规周期刷新）的账号：仅在重置窗口到期后触发保底刷新，避免平白产生常规轮询请求
		if intervalSecs <= 0 {
			if hasPassedResetWindow(acc.Quota, nowUnix) {
				due = append(due, acc.ID)
			}
			continue
		}
		if len(acc.Quota.Models) > 0 && len(acc.Quota.SummaryBuckets) == 0 && !summaryBackfillTried[acc.ID] {
			due = append(due, acc.ID)
			backfill = append(backfill, acc.ID)
			continue
		}
		if nowUnix-acc.Quota.LastUpdated >= intervalSecs {
			due = append(due, acc.ID)
			continue
		}
		if hasPassedResetWindow(acc.Quota, nowUnix) {
			due = append(due, acc.ID)
		}
	}
	return due, backfill
}

func hasPassedResetWindow(q *model.QuotaData, nowUnix int64) bool {
	if q == nil {
		return false
	}
	checkISO := func(raw string) bool {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return false
		}
		t, err := time.Parse(time.RFC3339, trimmed)
		if err != nil {
			return false
		}
		resetUnix := t.Unix()
		return resetUnix > q.LastUpdated && resetUnix+autoRefreshResetGraceSecs <= nowUnix
	}
	for _, b := range q.SummaryBuckets {
		if checkISO(b.ResetTime) {
			return true
		}
	}
	for _, m := range q.Models {
		if checkISO(m.ResetTime) {
			return true
		}
	}
	return false
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
