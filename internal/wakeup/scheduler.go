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

const (
	schedulerTickInterval     = 30 * time.Second
	maxMisfireCatchupWindow   = 20 * time.Minute
	quotaResetCooldownWindow  = 10 * time.Minute
	quotaResetMinAdvanceDelta = 1 * time.Hour
	maxTaskConcurrency        = 4
	singleWakeupTimeout       = 50 * time.Second
	postWakeupRefreshTimeout  = 25 * time.Second
)

// ErrWakeupCanceled 表示唤醒请求被用户主动取消或随应用退出中止。
var ErrWakeupCanceled = errors.New("已取消唤醒测试")

type cancelScopeEntry struct {
	cancel context.CancelFunc
}

// resetBaselineEntry 记录某任务下某账号某模型/配额桶最近一次观测到的重置状态基线。
type resetBaselineEntry struct {
	resetTime  string
	percentage int
	seenAt     time.Time
}

// Service 管理定时唤醒后台调度器、手动立即测试、取消控制与执行历史记录。
type Service struct {
	store        *storage.Store
	oauthClient  *oauth.Client
	quotaService *quota.Service
	onUpdated    func()

	httpMu     sync.RWMutex
	httpClient *http.Client

	mu                     sync.Mutex
	rootCtx                context.Context
	stopCancel             context.CancelFunc
	wg                     sync.WaitGroup
	checkCh                chan struct{}
	lastTickAt             time.Time
	runningTasks           map[string]context.CancelFunc // key: taskID -> cancel
	lastScheduledRunAt     map[string]int64              // 内存兜底最近一次定时触发时间（毫秒），防写盘失败重复触发
	lastResetSeen          map[string]resetBaselineEntry // key: taskID:accountID:modelOrBucket -> 最近基线
	quotaResetCooldown     map[string]time.Time          // key: taskID:accountID -> 每账号独立配额重置冷却时刻
	taskEnabledState       map[string]bool               // key: taskID -> 上次同步时的启停态，用于检测禁用->启用
	refreshingAccounts     map[string]bool               // 唤醒后异步配额刷新去重集合
	bgCancels              map[uint64]context.CancelFunc // 后台异步任务取消句柄，供 Stop 立即中止
	nextBgCancelID         uint64
	cancelScopes           map[string]*cancelScopeEntry
	activeTasksChangedHook func(bool)
	lastActiveTasksState   *bool
	nowFunc                func() time.Time
}

// NewService 创建定时唤醒服务实例。
func NewService(store *storage.Store, oauthClient *oauth.Client, quotaService *quota.Service, onUpdated func()) *Service {
	if oauthClient == nil {
		oauthClient = oauth.NewClient()
	}
	return &Service{
		store:              store,
		oauthClient:        oauthClient,
		quotaService:       quotaService,
		httpClient:         &http.Client{Timeout: 45 * time.Second},
		onUpdated:          onUpdated,
		checkCh:            make(chan struct{}, 1),
		runningTasks:       make(map[string]context.CancelFunc),
		lastScheduledRunAt: make(map[string]int64),
		lastResetSeen:      make(map[string]resetBaselineEntry),
		quotaResetCooldown: make(map[string]time.Time),
		taskEnabledState:   make(map[string]bool),
		refreshingAccounts: make(map[string]bool),
		bgCancels:          make(map[uint64]context.CancelFunc),
		cancelScopes:       make(map[string]*cancelScopeEntry),
		nowFunc:            time.Now,
	}
}

func (s *Service) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

// SetTransport 线程安全地为唤醒服务的 HTTP 客户端注入自定义传输层（如动态代理）。
func (s *Service) SetTransport(rt http.RoundTripper) {
	s.httpMu.Lock()
	defer s.httpMu.Unlock()
	if s.httpClient == nil {
		s.httpClient = &http.Client{Timeout: 45 * time.Second, Transport: rt}
		return
	}
	// 复制 http.Client 结构体后替换 Transport，避免并发请求期间修改正在读取的指针字段
	cloned := *s.httpClient
	cloned.Transport = rt
	s.httpClient = &cloned
}

func (s *Service) getHTTPClient() *http.Client {
	s.httpMu.RLock()
	client := s.httpClient
	s.httpMu.RUnlock()
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 45 * time.Second}
}

// HasActiveScheduledTasks 返回当前是否同时满足「全局调度器已启用」且「至少存在一条已启用的定时唤醒任务」。
func (s *Service) HasActiveScheduledTasks() bool {
	if s.store == nil {
		return false
	}
	state := s.store.GetWakeupState()
	if !state.Enabled {
		return false
	}
	for _, task := range state.Tasks {
		if task.Enabled {
			return true
		}
	}
	return false
}

// SetActiveTasksChangedHook 注册活跃定时唤醒任务状态变更回调（供宿主层联动 macOS 防 App Nap 等系统保活能力）。
func (s *Service) SetActiveTasksChangedHook(fn func(bool)) {
	active := s.HasActiveScheduledTasks()
	s.mu.Lock()
	s.activeTasksChangedHook = fn
	if fn != nil {
		val := active
		s.lastActiveTasksState = &val
	} else {
		s.lastActiveTasksState = nil
	}
	s.mu.Unlock()

	if fn != nil {
		fn(active)
	}
}

func (s *Service) notifyActiveTasksChanged(forceValue *bool) {
	active := false
	if forceValue != nil {
		active = *forceValue
	} else {
		active = s.HasActiveScheduledTasks()
	}

	s.mu.Lock()
	hook := s.activeTasksChangedHook
	if hook == nil {
		s.mu.Unlock()
		return
	}
	if s.lastActiveTasksState != nil && *s.lastActiveTasksState == active {
		s.mu.Unlock()
		return
	}
	val := active
	s.lastActiveTasksState = &val
	s.mu.Unlock()

	hook(active)
}

// TriggerCheckNow 立即唤醒后台调度循环执行一次调度与补触发检查（可用于系统休眠唤醒通知或配置保存后即时响应）。
func (s *Service) TriggerCheckNow() {
	select {
	case s.checkCh <- struct{}{}:
	default:
	}
}

// Start 启动后台每 30 秒轮询一次的轻量定时调度器，在启动瞬间先行建立本地持久化配额的重置基线，
// 并立即执行一次调度检查，防止启动后等待 30 秒漏掉当前分钟或被 AutoRefresher 覆盖离线重置。
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
	s.rootCtx = ctx
	s.stopCancel = cancel
	s.lastTickAt = time.Time{}
	s.wg.Add(1)
	s.mu.Unlock()

	s.SyncResetBaseline()

	go func() {
		defer s.wg.Done()
		// 启动后立即检查当前分钟一次，避免等待首个 30s 周期错过目标分钟
		s.tick(ctx, s.now())

		ticker := time.NewTicker(schedulerTickInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				s.tick(ctx, now)
			case <-s.checkCh:
				s.tick(ctx, s.now())
			}
		}
	}()
}

// ResetTaskBaseline 强制清除指定唤醒任务的配额重置基线与冷却缓存，并以当前最新配额重建基线。
func (s *Service) ResetTaskBaseline(taskID string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	now := s.now()

	s.mu.Lock()
	s.clearTaskResetStateLocked(taskID)
	s.mu.Unlock()

	if s.store == nil {
		return
	}
	state := s.store.GetWakeupState()
	for _, task := range state.Tasks {
		if task.ID != taskID {
			continue
		}
		s.mu.Lock()
		s.taskEnabledState[task.ID] = task.Enabled
		if task.Enabled && isQuotaResetTask(task) {
			s.fillTaskResetBaselineLocked(task, now, true)
		}
		s.mu.Unlock()
		break
	}
	s.notifyActiveTasksChanged(nil)
}

// SyncResetBaseline 为当前所有启用的配额重置任务补齐或重建基线：
// - 停用任务清理旧基线，避免停用期间的旧重置在重新启用后 30 秒内误触发；
// - 任务从停用切换为启用时，强制以当前最新配额重建基线；
// - 持续启用中的任务仅补齐缺失键，不覆盖等待触发的重置变更。
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

	now := s.now()
	s.mu.Lock()
	for _, task := range state.Tasks {
		prevEnabled, hadPrev := s.taskEnabledState[task.ID]
		s.taskEnabledState[task.ID] = task.Enabled

		if !task.Enabled || !isQuotaResetTask(task) {
			s.clearTaskResetStateLocked(task.ID)
			continue
		}
		// 检测到从停用切为启用时，清空历史陈旧基线并强制按当前实时配额重建基线
		forceRebuild := hadPrev && !prevEnabled
		if forceRebuild {
			s.clearTaskResetStateLocked(task.ID)
		}
		s.fillTaskResetBaselineLocked(task, now, forceRebuild)
	}
	s.mu.Unlock()

	s.notifyActiveTasksChanged(nil)
}

func isQuotaResetTask(task model.WakeupTask) bool {
	return strings.EqualFold(strings.TrimSpace(task.Schedule.RepeatMode), "quota_reset") || task.Schedule.WakeOnReset
}

func (s *Service) clearTaskResetStateLocked(taskID string) {
	prefix := taskID + ":"
	for k := range s.lastResetSeen {
		if strings.HasPrefix(k, prefix) {
			delete(s.lastResetSeen, k)
		}
	}
	for k := range s.quotaResetCooldown {
		if strings.HasPrefix(k, prefix) {
			delete(s.quotaResetCooldown, k)
		}
	}
}

func (s *Service) fillTaskResetBaselineLocked(task model.WakeupTask, now time.Time, overwrite bool) {
	if s.store == nil {
		return
	}
	selectedModels := buildSelectedModelSet(task.Schedule.SelectedModels)
	for _, accID := range task.Schedule.SelectedAccounts {
		acc, ok := s.store.GetAccount(accID)
		if !ok || acc.Disabled || acc.Quota == nil {
			continue
		}
		for _, entry := range collectTrackableResets(acc.Quota, selectedModels) {
			key := task.ID + ":" + accID + ":" + entry.id
			if _, exists := s.lastResetSeen[key]; overwrite || !exists {
				s.lastResetSeen[key] = resetBaselineEntry{
					resetTime:  entry.resetTime,
					percentage: entry.percentage,
					seenAt:     now,
				}
			}
		}
	}
}

// syncAccountResetBaseline 在唤醒后异步配额刷新完成时，将该账号最新配额覆盖同步到所有启用中的配额重置任务基线，
// 防止唤醒开启新 5 小时重置窗口导致 resetTime 变化后反触发二次 quota_reset 唤醒。
func (s *Service) syncAccountResetBaseline(acc model.Account) {
	if s.store == nil || acc.Quota == nil {
		return
	}
	state := s.store.GetWakeupState()
	now := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, task := range state.Tasks {
		if !task.Enabled || !isQuotaResetTask(task) {
			continue
		}
		hasAcc := false
		for _, id := range task.Schedule.SelectedAccounts {
			if id == acc.ID {
				hasAcc = true
				break
			}
		}
		if !hasAcc {
			continue
		}
		selectedModels := buildSelectedModelSet(task.Schedule.SelectedModels)
		for _, entry := range collectTrackableResets(acc.Quota, selectedModels) {
			key := task.ID + ":" + acc.ID + ":" + entry.id
			s.lastResetSeen[key] = resetBaselineEntry{
				resetTime:  entry.resetTime,
				percentage: entry.percentage,
				seenAt:     now,
			}
		}
	}
}

// Stop 优雅停止后台定时调度器、中止所有在途任务与手动测试并等待后台协程安全收尾。
func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.stopCancel
	s.stopCancel = nil
	s.rootCtx = nil

	cancels := make([]context.CancelFunc, 0, len(s.cancelScopes)+len(s.runningTasks)+len(s.bgCancels)+1)
	if cancel != nil {
		cancels = append(cancels, cancel)
	}
	for k, entry := range s.cancelScopes {
		cancels = append(cancels, entry.cancel)
		delete(s.cancelScopes, k)
	}
	for _, taskCancel := range s.runningTasks {
		if taskCancel != nil {
			cancels = append(cancels, taskCancel)
		}
	}
	for id, bgCancel := range s.bgCancels {
		if bgCancel != nil {
			cancels = append(cancels, bgCancel)
		}
		delete(s.bgCancels, id)
	}
	s.mu.Unlock()

	for _, fn := range cancels {
		fn()
	}
	// 等待调度循环、在途定时任务及异步配额刷新全部安全退出
	s.wg.Wait()

	inactive := false
	s.notifyActiveTasksChanged(&inactive)
}

// CancelTask 中断正在执行中的指定唤醒任务（覆盖手动立即执行与后台定时触发），返回是否命中运行中任务。
func (s *Service) CancelTask(taskID string) bool {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return false
	}
	s.mu.Lock()
	cancelFn, ok := s.runningTasks[taskID]
	s.mu.Unlock()
	if ok && cancelFn != nil {
		cancelFn()
		return true
	}
	return false
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
	ctx, cancel := context.WithTimeout(parent, singleWakeupTimeout)
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

// RunTaskNow 立即手动触发执行某条定时唤醒任务（覆盖该任务选中的所有有效账号与模型，不污染定时调度周期的 LastRunAt）。
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

	results, started := s.executeTaskForAccounts(parent, *target, "manual", nil, s.now(), nil)
	if !started {
		return nil, fmt.Errorf("唤醒任务「%s」正在执行中，请稍候", target.Name)
	}
	return results, nil
}

func (s *Service) tick(ctx context.Context, now time.Time) {
	if s.store == nil {
		return
	}
	// 读取并推进上次 tick 时刻（即使调度器当前暂停，也推进 lastTickAt，避免重新开启瞬间回溯暂停期间的历史时间点）
	s.mu.Lock()
	prevTick := s.lastTickAt
	s.lastTickAt = now
	s.mu.Unlock()

	state := s.store.GetWakeupState()
	s.notifyActiveTasksChanged(nil)
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
			s.mu.Lock()
			s.taskEnabledState[task.ID] = false
			s.clearTaskResetStateLocked(task.ID)
			s.mu.Unlock()
			continue
		}

		// 若该任务正在运行中，暂不消费其配额重置基线，等下一轮 tick 再正常触发，彻底防止撞锁丢事件
		if s.isTaskRunning(task.ID) {
			continue
		}

		if isQuotaResetTask(task) {
			triggeredAccounts, pendingBaselines := s.evaluateQuotaResetTrigger(task, now)
			if len(triggeredAccounts) > 0 {
				tCopy := task
				accCopy := append([]string(nil), triggeredAccounts...)
				pendingCopy := pendingBaselines
				s.launchScheduledTask(ctx, tCopy, accCopy, now, func() {
					// 在获取 runningTasks 锁的同一临界区内提交基线与按账号冷却期，保证不丢事件、不连坐
					for k, entry := range pendingCopy {
						s.lastResetSeen[k] = entry
					}
					for _, accID := range accCopy {
						s.quotaResetCooldown[tCopy.ID+":"+accID] = now
					}
				})
				continue
			}
		}

		effectiveLastRun := s.effectiveLastScheduledRunAt(task)
		if ShouldTriggerTaskInWindow(task, prevTick, now, effectiveLastRun) {
			tCopy := task
			s.launchScheduledTask(ctx, tCopy, nil, now, nil)
		}
	}
}

func (s *Service) isTaskRunning(taskID string) bool {
	s.mu.Lock()
	_, running := s.runningTasks[taskID]
	s.mu.Unlock()
	return running
}

func (s *Service) effectiveLastScheduledRunAt(task model.WakeupTask) int64 {
	last := task.LastRunAt
	s.mu.Lock()
	if mem := s.lastScheduledRunAt[task.ID]; mem > last {
		last = mem
	}
	s.mu.Unlock()
	return last
}

func (s *Service) launchScheduledTask(ctx context.Context, task model.WakeupTask, overrideAccounts []string, triggerTime time.Time, onStarted func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.executeTaskForAccounts(ctx, task, "scheduled", overrideAccounts, triggerTime, onStarted)
	}()
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
	for k := range s.quotaResetCooldown {
		colon := strings.IndexByte(k, ':')
		if colon <= 0 {
			delete(s.quotaResetCooldown, k)
			continue
		}
		if _, ok := activeTaskIDs[k[:colon]]; !ok {
			delete(s.quotaResetCooldown, k)
		}
	}
	for id := range s.taskEnabledState {
		if _, ok := activeTaskIDs[id]; !ok {
			delete(s.taskEnabledState, id)
		}
	}
	for id := range s.lastScheduledRunAt {
		if _, ok := activeTaskIDs[id]; !ok {
			delete(s.lastScheduledRunAt, id)
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

// builtinPlaceholderFamilies 为常见云端占位模型 ID 提供离线家族兜底（当账号配额中缺失 displayName 时生效）。
var builtinPlaceholderFamilies = map[string]string{
	"model_placeholder_m318": "gemini-flash",
	"model_placeholder_m319": "gemini-flash",
	"model_placeholder_m320": "gemini-flash",
	"model_placeholder_m322": "gemini-flash",
	"model_placeholder_m525": "gemini-flash",
	"model_placeholder_m528": "gemini-flash",
	"model_placeholder_m669": "gemini-flash",
	"model_placeholder_m401": "claude-gpt",
	"model_placeholder_m402": "claude-gpt",
	"model_placeholder_m417": "gemini-argon",
	"model_placeholder_m427": "gemini-carbon",
}

// classifyWakeupBucketFamily 将 CloudCode 共享配额桶 ID 归一化至四大家族（与 internal/quota/summary.go 保持完全一致）：
// gemini-argon / gemini-carbon / gemini-flash / claude-gpt。
func classifyWakeupBucketFamily(bucketID string) string {
	id := strings.ToLower(strings.TrimSpace(bucketID))
	if id == "" {
		return ""
	}
	switch {
	case strings.Contains(id, "argon"):
		return "gemini-argon"
	case strings.Contains(id, "carbon"):
		return "gemini-carbon"
	case strings.Contains(id, "3p") || strings.Contains(id, "claude") || strings.Contains(id, "anthropic") || strings.Contains(id, "gpt"):
		return "claude-gpt"
	case strings.Contains(id, "gemini") || strings.Contains(id, "flash") || strings.Contains(id, "neon") || strings.Contains(id, "hydrogen"):
		return "gemini-flash"
	default:
		return ""
	}
}

// classifyWakeupModelFamily 结合模型 ID 与显示名（含 MODEL_PLACEHOLDER_* 占位映射）将其归一化至四大家族。
func classifyWakeupModelFamily(modelName, displayName string) string {
	lowerID := strings.ToLower(strings.TrimSpace(modelName))
	lowerDisp := strings.ToLower(strings.TrimSpace(displayName))
	combined := strings.TrimSpace(lowerID + " " + lowerDisp)
	switch {
	case strings.Contains(combined, "argon"):
		return "gemini-argon"
	case strings.Contains(combined, "carbon"):
		return "gemini-carbon"
	case strings.Contains(combined, "claude") || strings.Contains(combined, "sonnet") ||
		strings.Contains(combined, "opus") || strings.Contains(combined, "anthropic") ||
		strings.Contains(combined, "gpt"):
		return "claude-gpt"
	case strings.Contains(combined, "flash") || strings.Contains(combined, "neon") || strings.Contains(combined, "hydrogen"):
		return "gemini-flash"
	case strings.Contains(combined, "gemini") && !strings.Contains(combined, "image"):
		return "gemini-flash"
	}
	if fam, ok := builtinPlaceholderFamilies[lowerID]; ok {
		return fam
	}
	return ""
}

// modelMatchesBucketFamily 判断选中的模型集合（可附带各模型的 displayName 映射）是否与指定共享配额桶属于同一家族。
func modelMatchesBucketFamily(selectedModels map[string]struct{}, bucketID string, displayByModel ...map[string]string) bool {
	if len(selectedModels) == 0 {
		return true
	}
	bucketFamily := classifyWakeupBucketFamily(bucketID)
	if bucketFamily == "" {
		return false
	}
	var dispMap map[string]string
	if len(displayByModel) > 0 {
		dispMap = displayByModel[0]
	}
	for sel := range selectedModels {
		disp := ""
		if dispMap != nil {
			disp = dispMap[sel]
		}
		if classifyWakeupModelFamily(sel, disp) == bucketFamily {
			return true
		}
	}
	return false
}

func collectTrackableResets(q *model.QuotaData, selectedModels map[string]struct{}) []trackableReset {
	if q == nil {
		return nil
	}
	displayByModel := make(map[string]string, len(q.Models))
	for _, m := range q.Models {
		key := strings.ToLower(strings.TrimSpace(m.Name))
		if key != "" && strings.TrimSpace(m.DisplayName) != "" {
			displayByModel[key] = strings.TrimSpace(m.DisplayName)
		}
	}

	var out []trackableReset
	for _, m := range q.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			continue
		}
		if len(selectedModels) > 0 {
			if _, want := selectedModels[strings.ToLower(name)]; !want {
				continue
			}
		}
		// 即使 resetTime 为空（例如配额回满 100% 但新 5 小时窗口尚未开启）也纳入追踪，避免漏检重置
		out = append(out, trackableReset{
			id:         name,
			percentage: m.Percentage,
			resetTime:  strings.TrimSpace(m.ResetTime),
		})
	}
	for _, b := range q.SummaryBuckets {
		bid := strings.TrimSpace(b.BucketID)
		if bid == "" || !modelMatchesBucketFamily(selectedModels, bid, displayByModel) {
			continue
		}
		out = append(out, trackableReset{
			id:         "bucket:" + bid,
			percentage: b.Percentage,
			resetTime:  strings.TrimSpace(b.ResetTime),
		})
	}
	return out
}

func parseResetISO(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return ts, true
	}
	if ts, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

// isResetTransitionTriggered 判断单条模型/配额桶从 prev 到 curr 是否发生了真实的配额重置事件：
//  1. 低配额回升（prev < 95% -> curr >= 95%）：若 resetTime 变化、或新窗口清空为 ""（未开窗待唤醒）、或旧 resetTime 已过期，则触发；
//  2. 高配额自然翻窗（prev >= 95% && curr >= 95%）：仅当旧 resetTime 已到期且新 resetTime 向后推进 >= 1 小时才触发，
//     彻底避免唤醒开窗微调 resetTime 或未开窗账号滚动 now+5h 导致的循环二次误触发。
func isResetTransitionTriggered(prev resetBaselineEntry, curr trackableReset, now time.Time) bool {
	if curr.percentage < 95 {
		return false
	}
	if prev.percentage < 95 {
		// 配额从消耗态回升至满额（>=95%）
		if curr.resetTime == "" {
			// 新 5h 计时窗口尚未开启，服务端可能清空 resetTime，此时应立即触发唤醒开窗
			return true
		}
		if prev.resetTime == "" || prev.resetTime != curr.resetTime {
			return true
		}
		if prevT, ok := parseResetISO(prev.resetTime); ok && !now.Before(prevT) {
			return true
		}
		return false
	}

	// prev >= 95% 且 curr >= 95%：必须确认旧重置时间已自然到期且新窗口明确推进至少 1 小时
	if prev.resetTime == "" || curr.resetTime == "" || prev.resetTime == curr.resetTime {
		return false
	}
	prevT, ok1 := parseResetISO(prev.resetTime)
	currT, ok2 := parseResetISO(curr.resetTime)
	if !ok1 || !ok2 {
		return false
	}
	return !now.Before(prevT) && currT.Sub(prevT) >= quotaResetMinAdvanceDelta
}

// evaluateQuotaResetTrigger 按 (taskID, accountID) 粒度检测发生真实配额重置的账号，
// 不提前覆写触发键的基线，返回 (待唤醒账号列表, 待提交的新基线映射)，由调用方在抢占 runningTasks 锁时原子提交。
func (s *Service) evaluateQuotaResetTrigger(task model.WakeupTask, now time.Time) ([]string, map[string]resetBaselineEntry) {
	if s.store == nil {
		return nil, nil
	}
	selectedModels := buildSelectedModelSet(task.Schedule.SelectedModels)

	var triggeredAccounts []string
	pendingBaselines := make(map[string]resetBaselineEntry)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 若本进程已为该任务记录过任意账号的独立冷却时间，说明该任务已由每账号独立冷却接管，
	// 此时不再回退参考任务级 LastRunAt，避免账号 A 触发后误阻塞账号 B。
	taskHasPerAccountCD := false
	taskPrefix := task.ID + ":"
	for k := range s.quotaResetCooldown {
		if strings.HasPrefix(k, taskPrefix) {
			taskHasPerAccountCD = true
			break
		}
	}

	for _, accID := range task.Schedule.SelectedAccounts {
		acc, ok := s.store.GetAccount(accID)
		if !ok || acc.Disabled || acc.Quota == nil {
			continue
		}

		// 每账号独立 10 分钟冷却期（仅在冷启动尚未建立独立冷却缓存时保守承接磁盘持久化的 task.LastRunAt）
		acctCooldownKey := task.ID + ":" + accID
		inCooldown := false
		if lastAcctAt, hasAcctCD := s.quotaResetCooldown[acctCooldownKey]; hasAcctCD {
			inCooldown = now.Sub(lastAcctAt) < quotaResetCooldownWindow
		} else if !taskHasPerAccountCD && task.LastRunAt > 0 && now.Sub(time.UnixMilli(task.LastRunAt)) < quotaResetCooldownWindow {
			inCooldown = true
		}

		accTriggered := false
		for _, entry := range collectTrackableResets(acc.Quota, selectedModels) {
			key := task.ID + ":" + accID + ":" + entry.id
			nextBaseline := resetBaselineEntry{
				resetTime:  entry.resetTime,
				percentage: entry.percentage,
				seenAt:     now,
			}

			prev, exists := s.lastResetSeen[key]
			if !exists {
				// 首次观测仅记录基线，不触发
				s.lastResetSeen[key] = nextBaseline
				continue
			}

			if isResetTransitionTriggered(prev, entry, now) {
				if !inCooldown {
					accTriggered = true
					pendingBaselines[key] = nextBaseline
				}
				// 若处于冷却期内，保留旧 prev 基线，待冷却期结束后下一轮 tick 正常触发
				continue
			}

			// 若窗口刚推进但配额瞬间仍为 0%（常见于服务端切换瞬间），不提前覆写基线，避免吞掉下一秒刷出的 100%
			if entry.resetTime != prev.resetTime && entry.percentage == 0 {
				continue
			}

			// 正常消耗、高配额微调/滚动 resetTime 或普通状态变化，静默同步基线
			if entry.resetTime != prev.resetTime || entry.percentage != prev.percentage {
				s.lastResetSeen[key] = nextBaseline
			}
		}

		if accTriggered {
			triggeredAccounts = append(triggeredAccounts, accID)
		}
	}

	return triggeredAccounts, pendingBaselines
}

// checkQuotaResetTrigger 追踪任务选中模型（或全部模型及共享配额桶）的重置变更，并即时提交命中基线。
// 处于 10 分钟冷却期内或刚推进窗口但配额尚为 0% 时不提前覆写基线，确保冷却结束后不漏触发。
func (s *Service) checkQuotaResetTrigger(task model.WakeupTask, now time.Time) bool {
	triggeredAccounts, pendingBaselines := s.evaluateQuotaResetTrigger(task, now)
	if len(triggeredAccounts) == 0 {
		return false
	}
	s.mu.Lock()
	for k, entry := range pendingBaselines {
		s.lastResetSeen[k] = entry
	}
	for _, accID := range triggeredAccounts {
		s.quotaResetCooldown[task.ID+":"+accID] = now
	}
	s.mu.Unlock()
	return true
}

func (s *Service) isScheduledTaskStillEnabled(taskID string) bool {
	if s.store == nil {
		return false
	}
	state := s.store.GetWakeupState()
	if !state.Enabled {
		return false
	}
	for _, t := range state.Tasks {
		if t.ID == taskID {
			return t.Enabled
		}
	}
	return false
}

type wakeupJob struct {
	index     int
	accountID string
	modelName string
}

func (s *Service) executeTask(parent context.Context, task model.WakeupTask, triggerType string) ([]model.WakeupHistoryItem, bool) {
	return s.executeTaskForAccounts(parent, task, triggerType, nil, s.now(), nil)
}

func (s *Service) executeTaskForAccounts(
	parent context.Context,
	task model.WakeupTask,
	triggerType string,
	overrideAccounts []string,
	triggerTime time.Time,
	onStarted func(),
) ([]model.WakeupHistoryItem, bool) {
	if parent == nil {
		parent = context.Background()
	}
	if triggerTime.IsZero() {
		triggerTime = s.now()
	}

	taskCtx, taskCancel := context.WithCancel(parent)
	s.mu.Lock()
	if s.runningTasks[task.ID] != nil {
		s.mu.Unlock()
		taskCancel()
		return nil, false
	}
	s.runningTasks[task.ID] = taskCancel
	if onStarted != nil {
		onStarted()
	}
	s.mu.Unlock()

	defer func() {
		taskCancel()
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

	targetAccounts := task.Schedule.SelectedAccounts
	if len(overrideAccounts) > 0 {
		targetAccounts = overrideAccounts
	}

	// 外层账号循环与内层模型循环均严格检查取消状态，避免取消后仍遍历剩余账号
	jobs := make([]wakeupJob, 0, len(targetAccounts)*len(models))
	for _, accID := range targetAccounts {
		if taskCtx.Err() != nil {
			break
		}
		acc, ok := s.store.GetAccount(accID)
		if !ok {
			continue
		}
		// 后台自动定时调度跳过已失效/禁用账号，避免持续发送无效请求
		if triggerType == "scheduled" && acc.Disabled {
			continue
		}
		for _, modelName := range models {
			if taskCtx.Err() != nil {
				break
			}
			jobs = append(jobs, wakeupJob{
				index:     len(jobs),
				accountID: accID,
				modelName: modelName,
			})
		}
	}

	orderedResults := make([]*model.WakeupHistoryItem, len(jobs))
	if len(jobs) > 0 && taskCtx.Err() == nil {
		concurrency := maxTaskConcurrency
		if len(jobs) < concurrency {
			concurrency = len(jobs)
		}
		jobCh := make(chan wakeupJob, len(jobs))
		for _, job := range jobs {
			jobCh <- job
		}
		close(jobCh)

		var workers sync.WaitGroup
		for i := 0; i < concurrency; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				for job := range jobCh {
					if taskCtx.Err() != nil {
						return
					}
					// 定时任务执行中途若用户停用/删除该任务或关闭全局开关，及时停止后续请求
					if triggerType == "scheduled" && !s.isScheduledTaskStillEnabled(task.ID) {
						taskCancel()
						return
					}
					callCtx, cancel := context.WithTimeout(taskCtx, singleWakeupTimeout)
					item, _ := s.runSingleAccountWakeup(callCtx, task.ID, task.Name, triggerType, job.accountID, job.modelName, prompt, maxTokens)
					cancel()
					if item != nil {
						orderedResults[job.index] = item
					}
				}
			}()
		}
		workers.Wait()
	}

	results := make([]model.WakeupHistoryItem, 0, len(orderedResults))
	for _, item := range orderedResults {
		if item != nil {
			results = append(results, *item)
		}
	}

	// 仅在「后台定时触发 + 实际产生执行记录 + 未被取消」时才推进 LastRunAt，
	// 避免手动立即执行、0 次执行或取消中断污染定时调度周期。
	if triggerType == "scheduled" && len(results) > 0 && taskCtx.Err() == nil {
		runTs := triggerTime.UnixMilli()
		s.mu.Lock()
		if runTs > s.lastScheduledRunAt[task.ID] {
			s.lastScheduledRunAt[task.ID] = runTs
		}
		s.mu.Unlock()
		// 即使落盘偶发失败，内存 lastScheduledRunAt 也会继续防重，避免 30s 后反复触发
		if err := s.store.UpdateWakeupTaskLastRun(task.ID, runTs); err != nil {
			_ = err
		}
	}

	if len(results) > 0 && s.onUpdated != nil {
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

func lookupModelDisplayName(acc model.Account, modelName string) string {
	if acc.Quota == nil {
		return ""
	}
	target := strings.ToLower(strings.TrimSpace(modelName))
	if target == "" {
		return ""
	}
	for _, m := range acc.Quota.Models {
		if strings.ToLower(strings.TrimSpace(m.Name)) == target {
			return strings.TrimSpace(m.DisplayName)
		}
	}
	return ""
}

func (s *Service) schedulePostWakeupQuotaRefresh(accountID string) {
	if s.quotaService == nil || strings.TrimSpace(accountID) == "" {
		return
	}
	s.mu.Lock()
	if s.refreshingAccounts[accountID] {
		s.mu.Unlock()
		return
	}
	parent := s.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	if parent.Err() != nil {
		s.mu.Unlock()
		return
	}
	refreshCtx, cancel := context.WithTimeout(parent, postWakeupRefreshTimeout)
	s.nextBgCancelID++
	bgID := s.nextBgCancelID
	s.bgCancels[bgID] = cancel
	s.refreshingAccounts[accountID] = true
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		defer func() {
			cancel()
			s.mu.Lock()
			delete(s.refreshingAccounts, accountID)
			delete(s.bgCancels, bgID)
			s.mu.Unlock()
		}()

		updatedAcc, err := s.quotaService.RefreshAccount(refreshCtx, accountID)
		if err != nil {
			return
		}
		// 同步唤醒开窗后的最新 resetTime 基线，防止反触发 quota_reset 二次唤醒
		s.syncAccountResetBaseline(updatedAcc)
		if s.onUpdated != nil {
			s.onUpdated()
		}
	}()
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

	started := s.now()
	freshTok, refreshed, err := s.oauthClient.EnsureFreshToken(ctx, acc.Token)
	if err != nil {
		// 用户主动取消时不污染唤醒执行历史记录
		if isWakeupContextCanceled(ctx, err) {
			return nil, fmt.Errorf("%w: %v", ErrWakeupCanceled, err)
		}
		_, _ = s.store.RecordTokenRefreshFailure(acc.ID, err, errors.Is(err, oauth.ErrInvalidGrant))
		item := model.WakeupHistoryItem{
			Timestamp:    s.now().UnixMilli(),
			TaskID:       taskID,
			TaskName:     taskName,
			TriggerType:  triggerType,
			AccountID:    acc.ID,
			AccountEmail: acc.Email,
			Model:        modelName,
			Prompt:       prompt,
			Success:      false,
			DurationMs:   s.now().Sub(started).Milliseconds(),
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

	// 过滤占位项目 ID（aicode-consumers）并优先复用账号配额快照中的有效项目 ID；仅在仍缺失时再拉取配额补全项目上下文
	effProject := effectiveWakeupProjectID(acc.Token, acc.Quota)
	if effProject == "" && s.quotaService != nil {
		if updatedAcc, qErr := s.quotaService.RefreshAccount(ctx, acc.ID); qErr == nil {
			acc = updatedAcc
			effProject = effectiveWakeupProjectID(acc.Token, acc.Quota)
		} else if isWakeupContextCanceled(ctx, qErr) {
			return nil, fmt.Errorf("%w: %v", ErrWakeupCanceled, qErr)
		}
	}

	reqToken := acc.Token
	reqToken.ProjectID = effProject
	modelDisplayName := lookupModelDisplayName(acc, modelName)
	httpClient := s.getHTTPClient()

	res, errInfo, reqErr := executeWakeupRequestWithDisplayName(ctx, httpClient, reqToken, modelName, modelDisplayName, prompt, maxOutputTokens)
	if reqErr != nil && isWakeupContextCanceled(ctx, reqErr) {
		return nil, fmt.Errorf("%w: %v", ErrWakeupCanceled, reqErr)
	}

	// 若本次使用的是缓存中的未刷新 AccessToken，却因云端提前撤销或本机时钟偏差返回 HTTP 401，强制刷新令牌持久化后重试一次
	if !refreshed && reqErr != nil && errInfo != nil && errInfo.Code == http.StatusUnauthorized && strings.TrimSpace(acc.Token.RefreshToken) != "" {
		tokenResp, forceErr := s.oauthClient.RefreshAccessToken(ctx, acc.Token.RefreshToken)
		if forceErr != nil {
			if isWakeupContextCanceled(ctx, forceErr) {
				return nil, fmt.Errorf("%w: %v", ErrWakeupCanceled, forceErr)
			}
			_, _ = s.store.RecordTokenRefreshFailure(acc.ID, forceErr, errors.Is(forceErr, oauth.ErrInvalidGrant))
			item := model.WakeupHistoryItem{
				Timestamp:    s.now().UnixMilli(),
				TaskID:       taskID,
				TaskName:     taskName,
				TriggerType:  triggerType,
				AccountID:    acc.ID,
				AccountEmail: acc.Email,
				Model:        modelName,
				Prompt:       prompt,
				Success:      false,
				DurationMs:   s.now().Sub(started).Milliseconds(),
				ErrorMessage: fmt.Sprintf("HTTP 401 后令牌强制刷新失败: %v", forceErr),
			}
			_ = s.store.AppendWakeupHistory(item)
			return &item, forceErr
		}

		mergedTok := oauth.MergeRefreshedToken(acc.Token, tokenResp)
		if saved, uErr := s.store.RecordTokenRefreshSuccess(acc.ID, mergedTok); uErr == nil {
			acc = saved
		} else {
			acc.Token = mergedTok
		}
		reqToken = acc.Token
		reqToken.ProjectID = effectiveWakeupProjectID(acc.Token, acc.Quota)

		res, errInfo, reqErr = executeWakeupRequestWithDisplayName(ctx, httpClient, reqToken, modelName, modelDisplayName, prompt, maxOutputTokens)
		if reqErr != nil && isWakeupContextCanceled(ctx, reqErr) {
			return nil, fmt.Errorf("%w: %v", ErrWakeupCanceled, reqErr)
		}
	}

	item := model.WakeupHistoryItem{
		Timestamp:    s.now().UnixMilli(),
		TaskID:       taskID,
		TaskName:     taskName,
		TriggerType:  triggerType,
		AccountID:    acc.ID,
		AccountEmail: acc.Email,
		Model:        modelName,
		Prompt:       prompt,
		DurationMs:   s.now().Sub(started).Milliseconds(),
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

	// 唤醒成功后清除账号已恢复的旧 QuotaError 与 403 封禁标记
	if acc.QuotaError != nil || (acc.Quota != nil && acc.Quota.IsForbidden) {
		_, _ = s.store.UpdateAccount(acc.ID, func(target *model.Account) error {
			target.QuotaError = nil
			if target.Quota != nil && target.Quota.IsForbidden {
				target.Quota.IsForbidden = false
			}
			return nil
		})
	}

	_ = s.store.AppendWakeupHistory(item)
	// 异步触发该账号轻量配额刷新并同步重置基线，实时展示新开启的 5h 重置倒计时且避免反触发 quota_reset
	s.schedulePostWakeupQuotaRefresh(acc.ID)
	return &item, nil
}

// ShouldTriggerTaskInWindow 判断任务在 (prevTick, now] 窗口内是否命中触发条件（含最大 20 分钟漏触发补齐，防休眠/App Nap 漏跑）。
// - interval 模式天然按间隔差值判断，直接以 now 校验；
// - daily / weekly / crontab 模式在跨分钟跳跃时扫描 (prevTick, now] 最多回溯 20 分钟，找到任意严格晚于 lastRunAtMs 的命中分钟即返回 true。
func ShouldTriggerTaskInWindow(task model.WakeupTask, prevTick, now time.Time, lastRunAtMs int64) bool {
	if !task.Enabled {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(task.Schedule.RepeatMode))
	if mode == "interval" {
		return ShouldTriggerTask(task, now, lastRunAtMs)
	}
	if mode != "daily" && mode != "weekly" && mode != "crontab" {
		return false
	}

	// 首次启动（prevTick 为零）或系统时钟回拨（now < prevTick）时，仅检查当前分钟 now
	if prevTick.IsZero() || now.Before(prevTick) {
		return ShouldTriggerTask(task, now, lastRunAtMs)
	}

	nowMin := now.Truncate(time.Minute)
	prevMin := prevTick.In(now.Location()).Truncate(time.Minute)
	if !nowMin.After(prevMin) {
		return ShouldTriggerTask(task, now, lastRunAtMs)
	}

	// 计算补触发窗口起点：严格在 prevMin 之后，且最多回溯 maxMisfireCatchupWindow（20 分钟）
	startMin := prevMin.Add(time.Minute)
	earliestAllowed := nowMin.Add(-maxMisfireCatchupWindow)
	if startMin.Before(earliestAllowed) {
		startMin = earliestAllowed
	}

	var lastRunMin time.Time
	hasLastRun := lastRunAtMs > 0
	if hasLastRun {
		lastRunMin = time.UnixMilli(lastRunAtMs).In(now.Location()).Truncate(time.Minute)
	}

	// 倒序扫描至多 21 个分钟点：只要存在严格晚于 lastRunMin 的错过计划点即补触发一次
	for cand := nowMin; !cand.Before(startMin); cand = cand.Add(-time.Minute) {
		if hasLastRun && !cand.After(lastRunMin) {
			break
		}
		if ShouldTriggerTask(task, cand, lastRunAtMs) {
			return true
		}
	}
	return false
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
			if norm, ok := normalizeHHMM(t); ok && norm == hhmm {
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
			if norm, ok := normalizeHHMM(t); ok && norm == hhmm {
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

// normalizeHHMM 将 HH:MM（兼容 8:00 / 08:00）规范化为两位补零的 24 小时制字符串。
func normalizeHHMM(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	parts := strings.Split(trimmed, ":")
	if len(parts) != 2 {
		return "", false
	}
	if len(parts[0]) == 0 || len(parts[0]) > 2 || len(parts[1]) != 2 {
		return "", false
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return "", false
	}
	return fmt.Sprintf("%02d:%02d", h, m), true
}

// withinTimeWindow 判断当前时刻是否在间隔执行时间窗内：
// 两端均为空表示全天生效；单边留空自动补全为 00:00（开始为空）或 23:59（结束为空），支持跨午夜窗口。
func withinTimeWindow(nowHHMM, startHHMM, endHHMM string) bool {
	startTrim := strings.TrimSpace(startHHMM)
	endTrim := strings.TrimSpace(endHHMM)
	if startTrim == "" && endTrim == "" {
		return true
	}
	if startTrim == "" {
		startTrim = "00:00"
	}
	if endTrim == "" {
		endTrim = "23:59"
	}
	startNorm, ok1 := normalizeHHMM(startTrim)
	endNorm, ok2 := normalizeHHMM(endTrim)
	nowNorm, okNow := normalizeHHMM(nowHHMM)
	if !ok1 || !ok2 || !okNow {
		return false
	}
	if startNorm <= endNorm {
		return nowNorm >= startNorm && nowNorm <= endNorm
	}
	// 跨午夜时间窗（如 22:00–06:00）
	return nowNorm >= startNorm || nowNorm <= endNorm
}

type cronFieldSpec struct {
	name      string
	minVal    int
	maxVal    int
	isWeekday bool
}

var fiveFieldCronSpecs = [5]cronFieldSpec{
	{name: "分钟", minVal: 0, maxVal: 59},
	{name: "小时", minVal: 0, maxVal: 23},
	{name: "日期", minVal: 1, maxVal: 31},
	{name: "月份", minVal: 1, maxVal: 12},
	{name: "星期", minVal: 0, maxVal: 6, isWeekday: true},
}

// ValidateCronExpr 严格校验标准 5 段 Cron 表达式（分 时 日 月 周，AND 语义，周日支持 0 或 7）。
// 拒绝缺段/多段、空项、步长 <=0、范围倒置（lo > hi）及越界数值（不做静默钳位）。
func ValidateCronExpr(expr string) error {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return fmt.Errorf("Cron 表达式必须包含 5 段（分 时 日 月 周），当前为 %d 段", len(fields))
	}
	for i, spec := range fiveFieldCronSpecs {
		if err := validateCronField(fields[i], spec); err != nil {
			return fmt.Errorf("Cron 第 %d 段（%s）非法: %w", i+1, spec.name, err)
		}
	}
	return nil
}

func validateCronField(field string, spec cronFieldSpec) error {
	if strings.TrimSpace(field) == "" {
		return fmt.Errorf("字段不能为空")
	}
	maxAllowed := spec.maxVal
	if spec.isWeekday {
		maxAllowed = 7
	}

	parts := strings.Split(field, ",")
	for _, rawPart := range parts {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return fmt.Errorf("包含空逗号分隔项")
		}
		if strings.Count(part, "/") > 1 {
			return fmt.Errorf("项 %q 包含多个步长分隔符 '/'", part)
		}

		rangePart := part
		if strings.Contains(part, "/") {
			segs := strings.SplitN(part, "/", 2)
			rangePart = strings.TrimSpace(segs[0])
			stepStr := strings.TrimSpace(segs[1])
			if rangePart == "" || stepStr == "" {
				return fmt.Errorf("项 %q 缺少步长基数或步长值", part)
			}
			step, err := strconv.Atoi(stepStr)
			if err != nil || step <= 0 || step > maxAllowed {
				return fmt.Errorf("项 %q 步长需为 1–%d 的正整数", part, maxAllowed)
			}
		}

		if rangePart == "*" {
			continue
		}

		if strings.Contains(rangePart, "-") {
			if strings.Count(rangePart, "-") > 1 {
				return fmt.Errorf("项 %q 包含非法多段连字符", part)
			}
			bounds := strings.SplitN(rangePart, "-", 2)
			loStr := strings.TrimSpace(bounds[0])
			hiStr := strings.TrimSpace(bounds[1])
			if loStr == "" || hiStr == "" {
				return fmt.Errorf("项 %q 范围缺少起止值", part)
			}
			lo, err1 := strconv.Atoi(loStr)
			hi, err2 := strconv.Atoi(hiStr)
			if err1 != nil || err2 != nil {
				return fmt.Errorf("项 %q 范围值必须为整数", part)
			}
			if lo < spec.minVal || lo > maxAllowed || hi < spec.minVal || hi > maxAllowed {
				return fmt.Errorf("项 %q 超出允许范围 %d–%d", part, spec.minVal, maxAllowed)
			}
			if lo > hi {
				return fmt.Errorf("项 %q 范围起始值不能大于结束值", part)
			}
			continue
		}

		exact, err := strconv.Atoi(rangePart)
		if err != nil {
			return fmt.Errorf("项 %q 不是合法整数或通配符", part)
		}
		if exact < spec.minVal || exact > maxAllowed {
			return fmt.Errorf("项 %q 超出允许范围 %d–%d", part, spec.minVal, maxAllowed)
		}
	}
	return nil
}

// ValidateWakeupTask 校验单条唤醒任务配置（名称、触发模式、时间点、间隔窗口、严格 Cron 表达式等）。
func ValidateWakeupTask(task model.WakeupTask) error {
	if strings.TrimSpace(task.Name) == "" {
		return fmt.Errorf("唤醒任务名称不能为空")
	}
	if task.Schedule.MaxOutputTokens < 0 || task.Schedule.MaxOutputTokens > 4096 {
		return fmt.Errorf("任务「%s」最大输出 Token 超出合法范围 (0–4096)", task.Name)
	}

	mode := strings.ToLower(strings.TrimSpace(task.Schedule.RepeatMode))
	switch mode {
	case "daily":
		if len(task.Schedule.DailyTimes) == 0 {
			return fmt.Errorf("任务「%s」至少需要配置一个每日执行时间点", task.Name)
		}
		for _, t := range task.Schedule.DailyTimes {
			if _, ok := normalizeHHMM(t); !ok {
				return fmt.Errorf("任务「%s」每日时间点格式非法（需为 24 小时制 HH:MM）: %q", task.Name, t)
			}
		}

	case "weekly":
		if len(task.Schedule.WeeklyDays) == 0 {
			return fmt.Errorf("任务「%s」至少需要选择一个执行星期", task.Name)
		}
		for _, d := range task.Schedule.WeeklyDays {
			if d < 0 || d > 6 {
				return fmt.Errorf("任务「%s」星期取值非法（需为 0–6）: %d", task.Name, d)
			}
		}
		if len(task.Schedule.WeeklyTimes) == 0 {
			return fmt.Errorf("任务「%s」至少需要配置一个每周执行时间点", task.Name)
		}
		for _, t := range task.Schedule.WeeklyTimes {
			if _, ok := normalizeHHMM(t); !ok {
				return fmt.Errorf("任务「%s」每周时间点格式非法（需为 24 小时制 HH:MM）: %q", task.Name, t)
			}
		}

	case "interval":
		if task.Schedule.IntervalHours < 1 || task.Schedule.IntervalHours > 24 {
			return fmt.Errorf("任务「%s」间隔小时数需为 1–24 之间的整数", task.Name)
		}
		if start := strings.TrimSpace(task.Schedule.IntervalStartTime); start != "" {
			if _, ok := normalizeHHMM(start); !ok {
				return fmt.Errorf("任务「%s」窗口开始时间格式非法（需为 24 小时制 HH:MM）: %q", task.Name, start)
			}
		}
		if end := strings.TrimSpace(task.Schedule.IntervalEndTime); end != "" {
			if _, ok := normalizeHHMM(end); !ok {
				return fmt.Errorf("任务「%s」窗口结束时间格式非法（需为 24 小时制 HH:MM）: %q", task.Name, end)
			}
		}

	case "crontab":
		if err := ValidateCronExpr(task.Schedule.Crontab); err != nil {
			return fmt.Errorf("任务「%s」%w", task.Name, err)
		}

	case "quota_reset":
		// 配额重置触发无需额外时间字段

	default:
		return fmt.Errorf("任务「%s」不支持的触发模式: %q", task.Name, task.Schedule.RepeatMode)
	}
	return nil
}

// ValidateWakeupState 校验完整唤醒状态中的所有任务配置，供宿主保存落盘前统一拦截非法参数。
func ValidateWakeupState(state model.WakeupState) error {
	for _, task := range state.Tasks {
		if err := ValidateWakeupTask(task); err != nil {
			return err
		}
	}
	return nil
}

// matchFiveFieldCron 解析并匹配标准 5 段 Cron（分 时 日 月 周，支持 *, */n, a/n, a-b, a-b/n, a,b 及周字段 7=周日）。
// 若表达式本身不合法（越界、倒置范围、非法步长等），直接返回 false，绝不做静默越界钳位。
func matchFiveFieldCron(expr string, t time.Time) bool {
	if err := ValidateCronExpr(expr); err != nil {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(expr))
	return matchCronField(fields[0], t.Minute(), fiveFieldCronSpecs[0]) &&
		matchCronField(fields[1], t.Hour(), fiveFieldCronSpecs[1]) &&
		matchCronField(fields[2], t.Day(), fiveFieldCronSpecs[2]) &&
		matchCronField(fields[3], int(t.Month()), fiveFieldCronSpecs[3]) &&
		matchCronField(fields[4], int(t.Weekday()), fiveFieldCronSpecs[4])
}

func matchCronField(field string, value int, spec cronFieldSpec) bool {
	minVal, maxVal := spec.minVal, spec.maxVal
	isWeekday := spec.isWeekday

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
			if err1 != nil || err2 != nil || lo > hi {
				continue
			}
			// 兼容周字段 1-7 / 5-7 中 7 表示周日（0）
			if isWeekday && hi == 7 && lo >= 0 && lo <= 7 {
				for v := lo; v <= 7; v += step {
					if v%7 == value {
						return true
					}
				}
				continue
			}
			// 严格校验合法范围，不再静默钳位越界值
			if lo < minVal || hi > maxVal {
				continue
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
