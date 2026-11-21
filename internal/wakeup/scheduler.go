package wakeup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/quota"
	"more-account/internal/storage"
)

// ErrWakeupCanceled 表示唤醒请求被用户主动取消或随应用退出中止。
var ErrWakeupCanceled = errors.New("已取消唤醒测试")

type cancelScopeEntry struct {
	cancel context.CancelFunc
}

// Service 管理定时唤醒后台调度器、手动立即测试、取消控制与执行历史记录。
type Service struct {
	store        *storage.Store
	oauthClient  *oauth.Client
	quotaService *quota.Service
	httpClient   *http.Client
	onUpdated    func()

	mu            sync.Mutex
	stopCancel    context.CancelFunc
	runningTasks  map[string]bool
	lastResetSeen map[string]string // key: taskID:accountID:modelOrBucket -> resetTime
	cancelScopes  map[string]*cancelScopeEntry
}

// NewService 创建定时唤醒服务实例。
func NewService(store *storage.Store, oauthClient *oauth.Client, quotaService *quota.Service, onUpdated func()) *Service {
	if oauthClient == nil {
		oauthClient = oauth.NewClient()
	}
	return &Service{
		store:         store,
		oauthClient:   oauthClient,
		quotaService:  quotaService,
		httpClient:    &http.Client{Timeout: 45 * time.Second},
		onUpdated:     onUpdated,
		runningTasks:  make(map[string]bool),
		lastResetSeen: make(map[string]string),
		cancelScopes:  make(map[string]*cancelScopeEntry),
	}
}

// Start 启动后台每 30 秒轮询一次的轻量定时调度器，并在启动瞬间先行建立本地持久化配额的重置基线，
// 防止 AutoRefresher 在 1.5s 后率先刷新覆盖旧 ResetTime 导致离线重置漏触发。
func (s *Service) Start(parent context.Context) {
	s.mu.Lock()
	if s.stopCancel != nil {
		s.mu.Unlock()
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.stopCancel = cancel
	s.mu.Unlock()

	s.SyncResetBaseline()

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.tick(ctx, now)
			}
		}
	}()
}

// SyncResetBaseline 为当前所有启用配额重置监测的任务补齐缺失的初始 resetTime 基线（仅填充未见过的键，不覆盖待触发变更）。
func (s *Service) SyncResetBaseline() {
	if s.store == nil {
		return
	}
	state := s.store.GetWakeupState()
	activeTaskIDs := make(map[string]struct{}, len(state.Tasks))
	for _, task := range state.Tasks {
		activeTaskIDs[task.ID] = struct{}{}
	}
	s.pruneResetCache(activeTaskIDs)

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, task := range state.Tasks {
		if !(task.Schedule.RepeatMode == "quota_reset" || task.Schedule.WakeOnReset) {
			continue
		}
		selectedModels := buildSelectedModelSet(task.Schedule.SelectedModels)
		for _, accID := range task.Schedule.SelectedAccounts {
			acc, ok := s.store.GetAccount(accID)
			if !ok || acc.Disabled || acc.Quota == nil {
				continue
			}
			for _, entry := range collectTrackableResets(acc.Quota, selectedModels) {
				key := task.ID + ":" + accID + ":" + entry.id
				if s.lastResetSeen[key] == "" {
					s.lastResetSeen[key] = entry.resetTime
				}
			}
		}
	}
}

// Stop 停止后台定时调度器与所有进行中的取消作用域。
func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.stopCancel
	s.stopCancel = nil
	for k, entry := range s.cancelScopes {
		entry.cancel()
		delete(s.cancelScopes, k)
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// CancelScope 取消指定手动唤醒测试请求。
func (s *Service) CancelScope(scopeID string) {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return
	}
	s.mu.Lock()
	if entry, ok := s.cancelScopes[scopeID]; ok {
		entry.cancel()
		delete(s.cancelScopes, scopeID)
	}
	s.mu.Unlock()
}

// TestNow 手动发起一次单账号单模型唤醒测试，可随时通过 CancelScope 取消。
func (s *Service) TestNow(parent context.Context, req model.WakeupTestRequest) (*model.WakeupHistoryItem, error) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 50*time.Second)
	scopeID := strings.TrimSpace(req.ScopeID)
	if scopeID != "" {
		entry := &cancelScopeEntry{cancel: cancel}
		s.mu.Lock()
		if old, exists := s.cancelScopes[scopeID]; exists {
			old.cancel()
		}
		s.cancelScopes[scopeID] = entry
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			// 仅当当前注册项仍为本次调用创建的句柄时才移除，防止并发重入误删新测试的取消句柄。
			if s.cancelScopes[scopeID] == entry {
				delete(s.cancelScopes, scopeID)
			}
			s.mu.Unlock()
			cancel()
		}()
	} else {
		defer cancel()
	}

	item, err := s.runSingleAccountWakeup(ctx, "", "", "manual", req.AccountID, req.Model, req.Prompt, req.MaxOutputTokens)
	if item != nil && s.onUpdated != nil {
		s.onUpdated()
	}
	return item, err
}

// RunTaskNow 立即手动触发执行某条定时唤醒任务（覆盖该任务选中的所有有效账号与模型）。
func (s *Service) RunTaskNow(parent context.Context, taskID string) ([]model.WakeupHistoryItem, error) {
	state := s.store.GetWakeupState()
	var target *model.WakeupTask
	for i := range state.Tasks {
		if state.Tasks[i].ID == taskID {
			t := state.Tasks[i]
			target = &t
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("未找到唤醒任务: %s", taskID)
	}

	validAccounts := 0
	for _, accID := range target.Schedule.SelectedAccounts {
		if _, ok := s.store.GetAccount(accID); ok {
			validAccounts++
		}
	}
	if validAccounts == 0 {
		return nil, fmt.Errorf("唤醒任务「%s」未绑定任何有效账号，请先编辑任务选择账号", target.Name)
	}

	results, started := s.executeTask(parent, *target, "manual")
	if !started {
		return nil, fmt.Errorf("唤醒任务「%s」正在执行中，请稍候", target.Name)
	}
	return results, nil
}

func (s *Service) tick(ctx context.Context, now time.Time) {
	state := s.store.GetWakeupState()
	if !state.Enabled {
		return
	}

	activeTaskIDs := make(map[string]struct{}, len(state.Tasks))
	for _, task := range state.Tasks {
		activeTaskIDs[task.ID] = struct{}{}
	}
	s.pruneResetCache(activeTaskIDs)

	for _, task := range state.Tasks {
		if !task.Enabled {
			continue
		}
		if task.Schedule.RepeatMode == "quota_reset" || task.Schedule.WakeOnReset {
			if s.checkQuotaResetTrigger(task, now) {
				go s.executeTask(ctx, task, "scheduled")
				continue
			}
		}
		if ShouldTriggerTask(task, now, task.LastRunAt) {
			go s.executeTask(ctx, task, "scheduled")
		}
	}
}

func (s *Service) pruneResetCache(activeTaskIDs map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.lastResetSeen {
		colon := strings.IndexByte(k, ':')
		if colon <= 0 {
			delete(s.lastResetSeen, k)
			continue
		}
		if _, ok := activeTaskIDs[k[:colon]]; !ok {
			delete(s.lastResetSeen, k)
		}
	}
}

type trackableReset struct {
	id         string
	percentage int
	resetTime  string
}

func buildSelectedModelSet(models []string) map[string]struct{} {
	out := make(map[string]struct{}, len(models))
	for _, m := range models {
		if trimmed := strings.ToLower(strings.TrimSpace(m)); trimmed != "" {
			out[trimmed] = struct{}{}
		}
	}
	return out
}

func modelMatchesBucketFamily(selectedModels map[string]struct{}, bucketID string) bool {
	if len(selectedModels) == 0 {
		return true
	}
	lowerBucket := strings.ToLower(bucketID)
	var family string
	switch {
	case strings.Contains(lowerBucket, "carbon"):
		family = "carbon"
	case strings.Contains(lowerBucket, "flash"):
		family = "flash"
	case strings.Contains(lowerBucket, "claude") || strings.Contains(lowerBucket, "anthropic") || strings.Contains(lowerBucket, "gpt-oss"):
		family = "claude"
	case strings.Contains(lowerBucket, "argon") || strings.Contains(lowerBucket, "neon") || strings.Contains(lowerBucket, "gemini"):
		family = "gemini-pro"
	default:
		return false
	}

	for sel := range selectedModels {
		switch family {
		case "carbon":
			if strings.Contains(sel, "carbon") {
				return true
			}
		case "flash":
			if strings.Contains(sel, "flash") {
				return true
			}
		case "claude":
			if strings.Contains(sel, "claude") || strings.Contains(sel, "sonnet") || strings.Contains(sel, "opus") || strings.Contains(sel, "gpt-oss") {
				return true
			}
		case "gemini-pro":
			if !strings.Contains(sel, "flash") && !strings.Contains(sel, "carbon") &&
				(strings.Contains(sel, "argon") || strings.Contains(sel, "neon") || strings.Contains(sel, "gemini")) {
				return true
			}
		}
	}
	return false
}

func collectTrackableResets(q *model.QuotaData, selectedModels map[string]struct{}) []trackableReset {
	if q == nil {
		return nil
	}
	var out []trackableReset
	for _, m := range q.Models {
		rt := strings.TrimSpace(m.ResetTime)
		if rt == "" {
			continue
		}
		if len(selectedModels) > 0 {
			if _, want := selectedModels[strings.ToLower(strings.TrimSpace(m.Name))]; !want {
				continue
			}
		}
		out = append(out, trackableReset{
			id:         m.Name,
			percentage: m.Percentage,
			resetTime:  rt,
		})
	}
	for _, b := range q.SummaryBuckets {
		rt := strings.TrimSpace(b.ResetTime)
		if rt == "" || !modelMatchesBucketFamily(selectedModels, b.BucketID) {
			continue
		}
		out = append(out, trackableReset{
			id:         "bucket:" + b.BucketID,
			percentage: b.Percentage,
			resetTime:  rt,
		})
	}
	return out
}

// checkQuotaResetTrigger 追踪任务选中模型（或全部模型及共享配额桶）的 resetTime 变更。
// 处于 10 分钟冷却期内或刚推进窗口但配额尚为 0% 时不提前覆写基线，确保冷却结束后不漏触发。
func (s *Service) checkQuotaResetTrigger(task model.WakeupTask, now time.Time) bool {
	inCooldown := task.LastRunAt > 0 && now.Sub(time.UnixMilli(task.LastRunAt)) < 10*time.Minute
	selectedModels := buildSelectedModelSet(task.Schedule.SelectedModels)

	triggered := false
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, accID := range task.Schedule.SelectedAccounts {
		acc, ok := s.store.GetAccount(accID)
		if !ok || acc.Disabled || acc.Quota == nil {
			continue
		}
		for _, entry := range collectTrackableResets(acc.Quota, selectedModels) {
			key := task.ID + ":" + accID + ":" + entry.id
			prevReset := s.lastResetSeen[key]
			if prevReset == "" {
				s.lastResetSeen[key] = entry.resetTime
				continue
			}
			if prevReset == entry.resetTime {
				continue
			}
			if entry.percentage >= 95 {
				if !inCooldown {
					triggered = true
					s.lastResetSeen[key] = entry.resetTime
				}
				// 若处于冷却期内，保留旧 prevReset，待冷却期结束后下一轮 tick 正常触发并推进基线
			} else if entry.percentage > 0 {
				// 非满额但非 0% 的新窗口，正常推进基线
				s.lastResetSeen[key] = entry.resetTime
			}
		}
	}
	return triggered
}

func (s *Service) executeTask(parent context.Context, task model.WakeupTask, triggerType string) ([]model.WakeupHistoryItem, bool) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	if s.runningTasks[task.ID] {
		s.mu.Unlock()
		return nil, false
	}
	s.runningTasks[task.ID] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.runningTasks, task.ID)
		s.mu.Unlock()
	}()

	models := task.Schedule.SelectedModels
	if len(models) == 0 {
		models = []string{defaultWakeupModel}
	}
	prompt := strings.TrimSpace(task.Schedule.CustomPrompt)
	if prompt == "" {
		prompt = defaultWakeupPrompt
	}
	maxTokens := task.Schedule.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxOutputTokens
	}

	var results []model.WakeupHistoryItem
	for _, accID := range task.Schedule.SelectedAccounts {
		acc, ok := s.store.GetAccount(accID)
		if !ok {
			continue
		}
		// 后台自动定时调度跳过已失效/禁用账号，避免持续发送无效请求
		if triggerType == "scheduled" && acc.Disabled {
			continue
		}
		for _, modelName := range models {
			if parent.Err() != nil {
				break
			}
			ctx, cancel := context.WithTimeout(parent, 50*time.Second)
			item, _ := s.runSingleAccountWakeup(ctx, task.ID, task.Name, triggerType, accID, modelName, prompt, maxTokens)
			cancel()
			if item != nil {
				results = append(results, *item)
			}
		}
	}

	// 原子更新该任务的最近运行时间戳，避免覆盖并发编辑的其他任务配置
	_ = s.store.UpdateWakeupTaskLastRun(task.ID, time.Now().UnixMilli())

	if s.onUpdated != nil {
		s.onUpdated()
	}
	return results, true
}

func isWakeupContextCanceled(ctx context.Context, err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if ctx != nil && errors.Is(ctx.Err(), context.Canceled) {
		return true
	}
	return false
}

func (s *Service) runSingleAccountWakeup(
	ctx context.Context,
	taskID, taskName, triggerType, accountID, modelName, prompt string,
	maxOutputTokens int,
) (*model.WakeupHistoryItem, error) {
	acc, ok := s.store.GetAccount(accountID)
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", accountID)
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultWakeupPrompt
	}
	if strings.TrimSpace(modelName) == "" {
		modelName = defaultWakeupModel
	}

	started := time.Now()
	freshTok, refreshed, err := s.oauthClient.EnsureFreshToken(ctx, acc.Token)
	if err != nil {
		// 用户主动取消时不污染唤醒执行历史记录
		if isWakeupContextCanceled(ctx, err) {
			return nil, fmt.Errorf("%w: %v", ErrWakeupCanceled, err)
		}
		_, _ = s.store.RecordTokenRefreshFailure(acc.ID, err, errors.Is(err, oauth.ErrInvalidGrant))
		item := model.WakeupHistoryItem{
			Timestamp:    time.Now().UnixMilli(),
			TaskID:       taskID,
			TaskName:     taskName,
			TriggerType:  triggerType,
			AccountID:    acc.ID,
			AccountEmail: acc.Email,
			Model:        modelName,
			Prompt:       prompt,
			Success:      false,
			DurationMs:   time.Since(started).Milliseconds(),
			ErrorMessage: fmt.Sprintf("令牌刷新失败: %v", err),
		}
		_ = s.store.AppendWakeupHistory(item)
		return &item, err
	}
	if refreshed {
		if saved, uErr := s.store.RecordTokenRefreshSuccess(acc.ID, freshTok); uErr == nil {
			acc = saved
		} else {
			acc.Token = freshTok
		}
	}

	// 若尚未有 projectId，可尝试先拉取一次配额补全项目上下文
	if strings.TrimSpace(acc.Token.ProjectID) == "" && s.quotaService != nil {
		if updatedAcc, qErr := s.quotaService.RefreshAccount(ctx, acc.ID); qErr == nil {
			acc = updatedAcc
		}
	}

	res, errInfo, reqErr := ExecuteWakeupRequest(ctx, s.httpClient, acc.Token, modelName, prompt, maxOutputTokens)
	if reqErr != nil && isWakeupContextCanceled(ctx, reqErr) {
		return nil, fmt.Errorf("%w: %v", ErrWakeupCanceled, reqErr)
	}

	item := model.WakeupHistoryItem{
		Timestamp:    time.Now().UnixMilli(),
		TaskID:       taskID,
		TaskName:     taskName,
		TriggerType:  triggerType,
		AccountID:    acc.ID,
		AccountEmail: acc.Email,
		Model:        modelName,
		Prompt:       prompt,
		DurationMs:   time.Since(started).Milliseconds(),
	}

	if reqErr != nil {
		item.Success = false
		item.ErrorMessage = reqErr.Error()
		if errInfo != nil {
			if errInfo.ValidationURL != "" {
				item.ValidationURL = errInfo.ValidationURL
			}
			// 若唤醒时检测到账号需要网页验证或明确配额拒绝，同步回写至账号 QuotaError，方便工作台直接点击验证或复制链接
			if errInfo.ValidationURL != "" || errInfo.Reason == "VALIDATION_REQUIRED" || errInfo.Reason == "SUBSCRIPTION_REQUIRED" {
				errCopy := *errInfo
				_, _ = s.store.UpdateAccount(acc.ID, func(target *model.Account) error {
					target.QuotaError = &errCopy
					if errCopy.Code == http.StatusForbidden && target.Quota != nil {
						target.Quota.IsForbidden = true
					}
					return nil
				})
			}
		}
		_ = s.store.AppendWakeupHistory(item)
		return &item, reqErr
	}

	item.Success = true
	item.Reply = res.Reply
	item.PromptTokens = res.PromptTokens
	item.CompletionTokens = res.CompletionTokens
	item.TotalTokens = res.TotalTokens
	if res.DurationMs > 0 {
		item.DurationMs = res.DurationMs
	}
	_ = s.store.AppendWakeupHistory(item)
	return &item, nil
}

// ShouldTriggerTask 判断给定定时任务在当前本地时间 now 是否命中触发条件（带同分钟防重）。
func ShouldTriggerTask(task model.WakeupTask, now time.Time, lastRunAtMs int64) bool {
	if !task.Enabled {
		return false
	}
	if lastRunAtMs > 0 {
		lastRun := time.UnixMilli(lastRunAtMs).In(now.Location())
		if lastRun.Year() == now.Year() && lastRun.YearDay() == now.YearDay() &&
			lastRun.Hour() == now.Hour() && lastRun.Minute() == now.Minute() {
			return false
		}
	}

	hhmm := now.Format("15:04")
	switch strings.ToLower(strings.TrimSpace(task.Schedule.RepeatMode)) {
	case "daily":
		for _, t := range task.Schedule.DailyTimes {
			if strings.TrimSpace(t) == hhmm {
				return true
			}
		}
		return false

	case "weekly":
		weekdayMatch := false
		today := int(now.Weekday())
		for _, d := range task.Schedule.WeeklyDays {
			if d == today {
				weekdayMatch = true
				break
			}
		}
		if !weekdayMatch {
			return false
		}
		for _, t := range task.Schedule.WeeklyTimes {
			if strings.TrimSpace(t) == hhmm {
				return true
			}
		}
		return false

	case "interval":
		hours := task.Schedule.IntervalHours
		if hours <= 0 {
			hours = 4
		}
		if !withinTimeWindow(hhmm, task.Schedule.IntervalStartTime, task.Schedule.IntervalEndTime) {
			return false
		}
		if lastRunAtMs == 0 {
			return true
		}
		return now.Sub(time.UnixMilli(lastRunAtMs)) >= time.Duration(hours)*time.Hour

	case "crontab":
		return matchFiveFieldCron(task.Schedule.Crontab, now)

	default:
		return false
	}
}

func withinTimeWindow(nowHHMM, startHHMM, endHHMM string) bool {
	startHHMM = strings.TrimSpace(startHHMM)
	endHHMM = strings.TrimSpace(endHHMM)
	if startHHMM == "" || endHHMM == "" {
		return true
	}
	if startHHMM <= endHHMM {
		return nowHHMM >= startHHMM && nowHHMM <= endHHMM
	}
	// 跨午夜时间窗
	return nowHHMM >= startHHMM || nowHHMM <= endHHMM
}

// matchFiveFieldCron 解析标准 5 段 Cron（分 时 日 月 周，支持 *, */n, a/n, a-b, a-b/n, a,b 及周字段 7=周日）。
func matchFiveFieldCron(expr string, t time.Time) bool {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return false
	}
	return matchCronField(fields[0], t.Minute(), 0, 59) &&
		matchCronField(fields[1], t.Hour(), 0, 23) &&
		matchCronField(fields[2], t.Day(), 1, 31) &&
		matchCronField(fields[3], int(t.Month()), 1, 12) &&
		matchCronField(fields[4], int(t.Weekday()), 0, 6)
}

func matchCronField(field string, value, minVal, maxVal int) bool {
	isWeekday := minVal == 0 && maxVal == 6

	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step := 1
		hasStep := false
		rangePart := part
		if strings.Contains(part, "/") {
			segs := strings.SplitN(part, "/", 2)
			rangePart = strings.TrimSpace(segs[0])
			s, err := strconv.Atoi(strings.TrimSpace(segs[1]))
			if err != nil || s <= 0 {
				continue
			}
			step = s
			hasStep = true
		}

		if rangePart == "*" {
			if (value-minVal)%step == 0 {
				return true
			}
			continue
		}

		if strings.Contains(rangePart, "-") {
			bounds := strings.SplitN(rangePart, "-", 2)
			lo, err1 := strconv.Atoi(strings.TrimSpace(bounds[0]))
			hi, err2 := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err1 != nil || err2 != nil {
				continue
			}
			// 兼容周字段 1-7 / 5-7 中 7 表示周日（0）
			if isWeekday && hi == 7 && lo <= 7 {
				if lo < 0 {
					lo = 0
				}
				for v := lo; v <= 7; v += step {
					if v%7 == value {
						return true
					}
				}
				continue
			}
			if lo < minVal {
				lo = minVal
			}
			if hi > maxVal {
				hi = maxVal
			}
			if lo <= hi && value >= lo && value <= hi && (value-lo)%step == 0 {
				return true
			}
			continue
		}

		exact, err := strconv.Atoi(rangePart)
		if err != nil {
			continue
		}
		if isWeekday && exact == 7 {
			exact = 0
		}
		if exact < minVal || exact > maxVal {
			continue
		}
		// 兼容标准 Cron 的 a/step 语法（从 exact 到 maxVal 按 step 递增）
		if hasStep {
			if value >= exact && value <= maxVal && (value-exact)%step == 0 {
				return true
			}
			continue
		}
		if exact == value {
			return true
		}
	}
	return false
}
