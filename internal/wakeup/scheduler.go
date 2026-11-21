package wakeup

import (
	"context"
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
	lastResetSeen map[string]string // key: taskID:accountID:model -> resetTime
	cancelScopes  map[string]context.CancelFunc
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
		cancelScopes:  make(map[string]context.CancelFunc),
	}
}

// Start 启动后台每 30 秒轮询一次的轻量定时调度器。
func (s *Service) Start(parent context.Context) {
	s.mu.Lock()
	if s.stopCancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s.stopCancel = cancel
	s.mu.Unlock()

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

// Stop 停止后台定时调度器与所有进行中的取消作用域。
func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.stopCancel
	s.stopCancel = nil
	for k, fn := range s.cancelScopes {
		fn()
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
	if fn, ok := s.cancelScopes[scopeID]; ok {
		fn()
		delete(s.cancelScopes, scopeID)
	}
	s.mu.Unlock()
}

// TestNow 手动发起一次单账号单模型唤醒测试，可随时通过 CancelScope 取消。
func (s *Service) TestNow(parent context.Context, req model.WakeupTestRequest) (*model.WakeupHistoryItem, error) {
	ctx, cancel := context.WithTimeout(parent, 50*time.Second)
	scopeID := strings.TrimSpace(req.ScopeID)
	if scopeID != "" {
		s.mu.Lock()
		if old, exists := s.cancelScopes[scopeID]; exists {
			old()
		}
		s.cancelScopes[scopeID] = cancel
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.cancelScopes, scopeID)
			s.mu.Unlock()
			cancel()
		}()
	} else {
		defer cancel()
	}

	item, err := s.runSingleAccountWakeup(ctx, "", "", "manual", req.AccountID, req.Model, req.Prompt, req.MaxOutputTokens)
	if s.onUpdated != nil {
		s.onUpdated()
	}
	return item, err
}

// RunTaskNow 立即手动触发执行某条定时唤醒任务（覆盖该任务选中的所有账号与模型）。
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
	results := s.executeTask(parent, *target, "manual")
	return results, nil
}

func (s *Service) tick(ctx context.Context, now time.Time) {
	state := s.store.GetWakeupState()
	if !state.Enabled {
		return
	}

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

func (s *Service) checkQuotaResetTrigger(task model.WakeupTask, now time.Time) bool {
	if task.LastRunAt > 0 && now.Sub(time.UnixMilli(task.LastRunAt)) < 10*time.Minute {
		return false
	}
	triggered := false
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, accID := range task.Schedule.SelectedAccounts {
		acc, ok := s.store.GetAccount(accID)
		if !ok || acc.Disabled || acc.Quota == nil {
			continue
		}
		for _, m := range acc.Quota.Models {
			if m.ResetTime == "" || m.Percentage < 95 {
				continue
			}
			key := task.ID + ":" + accID + ":" + m.Name
			prevReset := s.lastResetSeen[key]
			if prevReset != "" && prevReset != m.ResetTime {
				triggered = true
			}
			s.lastResetSeen[key] = m.ResetTime
		}
	}
	return triggered
}

func (s *Service) executeTask(parent context.Context, task model.WakeupTask, triggerType string) []model.WakeupHistoryItem {
	s.mu.Lock()
	if s.runningTasks[task.ID] {
		s.mu.Unlock()
		return nil
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
		models = []string{"gemini-3-flash"}
	}
	prompt := strings.TrimSpace(task.Schedule.CustomPrompt)
	if prompt == "" {
		prompt = defaultWakeupPrompt
	}
	maxTokens := task.Schedule.MaxOutputTokens
	if maxTokens <= 0 {
		maxTokens = 16
	}

	var results []model.WakeupHistoryItem
	for _, accID := range task.Schedule.SelectedAccounts {
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

	// 更新该任务的最近执行时间戳
	state := s.store.GetWakeupState()
	nowMs := time.Now().UnixMilli()
	for i := range state.Tasks {
		if state.Tasks[i].ID == task.ID {
			state.Tasks[i].LastRunAt = nowMs
			break
		}
	}
	_ = s.store.SaveWakeupState(state)

	if s.onUpdated != nil {
		s.onUpdated()
	}
	return results
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
		modelName = "gemini-3-flash"
	}

	started := time.Now()
	freshTok, refreshed, err := s.oauthClient.EnsureFreshToken(ctx, acc.Token)
	if err != nil {
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
		acc.Token = freshTok
		_, _ = s.store.SaveAccount(acc)
	}

	// 若尚未有 projectId，可尝试先拉取一次配额补全项目上下文
	if strings.TrimSpace(acc.Token.ProjectID) == "" && s.quotaService != nil {
		if updatedAcc, qErr := s.quotaService.RefreshAccount(ctx, acc.ID); qErr == nil {
			acc = updatedAcc
		}
	}

	res, errInfo, reqErr := ExecuteWakeupRequest(ctx, s.httpClient, acc.Token, modelName, prompt, maxOutputTokens)
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
		if errInfo != nil && errInfo.ValidationURL != "" {
			item.ValidationURL = errInfo.ValidationURL
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

// matchFiveFieldCron 解析标准 5 段 Cron（分 时 日 月 周，支持 *, */n, a-b, a,b）。
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
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		step := 1
		rangePart := part
		if strings.Contains(part, "/") {
			segs := strings.SplitN(part, "/", 2)
			rangePart = strings.TrimSpace(segs[0])
			s, err := strconv.Atoi(strings.TrimSpace(segs[1]))
			if err != nil || s <= 0 {
				continue
			}
			step = s
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
			if lo < minVal {
				lo = minVal
			}
			if hi > maxVal {
				hi = maxVal
			}
			if value >= lo && value <= hi && (value-lo)%step == 0 {
				return true
			}
			continue
		}

		exact, err := strconv.Atoi(rangePart)
		if err != nil {
			continue
		}
		if exact == 7 && maxVal == 6 {
			exact = 0 // 兼容 7 表示周日
		}
		if exact == value {
			return true
		}
	}
	return false
}
