package quota

import (
	"fmt"
	"strings"
	"time"

	"more-account/internal/model"
)

// WindowSlot 表示单个配额窗口（5h 或周额度）的剩余百分比与重置时间。
type WindowSlot struct {
	Percentage int
	ResetTime  string
	Exhausted  bool
}

// CategorySummary 表示账号某一大模型分类的汇总配额（可能同时含 5h 与周额度双窗口）。
type CategorySummary struct {
	Key        string
	Label      string
	ShortLabel string
	FiveHour   *WindowSlot
	Weekly     *WindowSlot
	Percentage int
	ResetTime  string
}

var summaryBucketIDSet = map[string]struct{}{
	"3p-5h":                    {},
	"3p-weekly":                {},
	"claude:5h":                {},
	"claude:weekly":            {},
	"claude-5h":                {},
	"claude-weekly":            {},
	"gemini-5h":                {},
	"gemini-weekly":            {},
	"gemini:5h":                {},
	"gemini:weekly":            {},
	"gemini-flash-5h":          {},
	"gemini-flash-weekly":      {},
	"flash:5h":                 {},
	"flash:weekly":             {},
	"gemini-argon-5h":          {},
	"argon:5h":                 {},
	"argon:weekly":             {},
	"gemini-argon-wtu-weekly":  {},
	"gemini-carbon-5h":         {},
	"carbon:5h":                {},
	"carbon:weekly":            {},
	"gemini-carbon-wtu-weekly": {},
}

func isSummaryBucketID(rawName string) bool {
	lower := strings.ToLower(strings.TrimSpace(rawName))
	if lower == "" {
		return false
	}
	if _, ok := summaryBucketIDSet[lower]; ok {
		return true
	}
	if (strings.HasPrefix(lower, "gemini-argon-") || strings.HasPrefix(lower, "gemini-carbon-")) &&
		(strings.HasSuffix(lower, "-weekly") || strings.HasSuffix(lower, "-5h")) {
		return true
	}
	return false
}

func isExcludedSummaryModel(rawName, rawDisplay string) bool {
	if isSummaryBucketID(rawName) {
		return true
	}
	return isExcludedNonAntigravityModel(rawName, rawDisplay)
}

// ClassifySummaryBucket 将云端配额摘要桶 ID 归一化到统一大模型家族标识（gemini-argon / gemini-carbon / gemini-flash / claude-gpt），
// 未命中时返回空串，供跨模块（如定时唤醒配额重置检测）统一复用。
func ClassifySummaryBucket(bucketID string) string {
	key, _, ok := classifySummaryBucket(bucketID)
	if !ok {
		return ""
	}
	return key
}

// ClassifyModelCategory 根据模型原始 ID 与展示名（兼容 MODEL_PLACEHOLDER_*）归类到统一大模型家族标识，
// 未命中时返回空串，供跨模块统一复用。
func ClassifyModelCategory(modelName, displayName string) string {
	return classifyModelCategory(modelName, displayName)
}

func classifySummaryBucket(bucketID string) (key string, isWeekly bool, ok bool) {
	id := strings.ToLower(strings.TrimSpace(bucketID))
	if id == "" {
		return "", false, false
	}
	is5h := strings.HasSuffix(id, "5h") || strings.Contains(id, ":5h") || strings.Contains(id, "-5h")
	weekly := strings.Contains(id, "weekly") || strings.HasSuffix(id, "7d") || strings.Contains(id, ":week")
	if !is5h && !weekly {
		return "", false, false
	}

	switch {
	case strings.Contains(id, "argon"):
		return "gemini-argon", weekly, true
	case strings.Contains(id, "carbon"):
		return "gemini-carbon", weekly, true
	case strings.Contains(id, "3p") || strings.Contains(id, "claude") || strings.Contains(id, "gpt"):
		return "claude-gpt", weekly, true
	case strings.Contains(id, "gemini") || strings.Contains(id, "flash") || strings.Contains(id, "neon") || strings.Contains(id, "hydrogen"):
		return "gemini-flash", weekly, true
	default:
		return "", false, false
	}
}

func classifyModelCategory(rawName, rawDisplay string) string {
	lower := strings.ToLower(strings.TrimSpace(rawName) + " " + strings.TrimSpace(rawDisplay))
	switch {
	case strings.Contains(lower, "argon"):
		return "gemini-argon"
	case strings.Contains(lower, "carbon"):
		return "gemini-carbon"
	case strings.Contains(lower, "claude") || strings.Contains(lower, "gpt"):
		return "claude-gpt"
	case strings.Contains(lower, "flash") || strings.Contains(lower, "neon") || strings.Contains(lower, "hydrogen"):
		return "gemini-flash"
	case strings.Contains(lower, "gemini") && !strings.Contains(lower, "image"):
		return "gemini-flash"
	default:
		return ""
	}
}

func parseISO(raw string) (time.Time, bool) {
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

func isEarlierISO(candidate, current string) bool {
	if strings.TrimSpace(candidate) == "" {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return true
	}
	candTS, ok1 := parseISO(candidate)
	currTS, ok2 := parseISO(current)
	if ok1 && ok2 {
		return candTS.Before(currTS)
	}
	return candidate < current
}

func isWeeklyResetTime(iso string, now time.Time) bool {
	ts, ok := parseISO(iso)
	if !ok {
		return false
	}
	return ts.Sub(now) > 12*time.Hour
}

// TierLabel 返回规范化的账号订阅套餐标识（ULTRA / PRO / FREE / 空串）。
func TierLabel(acc model.Account) string {
	if acc.Quota == nil {
		return ""
	}
	tier := strings.ToUpper(strings.TrimSpace(acc.Quota.SubscriptionTier))
	switch tier {
	case "ULTRA", "PRO", "FREE":
		return tier
	default:
		return ""
	}
}

// SummarizeAccountCategories 汇总账号的 4 大核心配额分类（与前端 utils/quota.ts 同口径）。
func SummarizeAccountCategories(acc model.Account, now time.Time) []CategorySummary {
	if acc.Quota == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	isFree := TierLabel(acc) == "FREE"
	defs := []struct {
		key        string
		label      string
		shortLabel string
	}{
		{key: "gemini-argon", label: "Gemini Argon", shortLabel: "Argon"},
		{key: "gemini-carbon", label: "Gemini Carbon", shortLabel: "Carbon"},
		{key: "gemini-flash", label: "Gemini Flash", shortLabel: "Flash"},
		{key: "claude-gpt", label: "Claude/GPT", shortLabel: "Claude/GPT"},
	}

	type catSlots struct {
		fiveHour *WindowSlot
		weekly   *WindowSlot
	}
	state := make(map[string]*catSlots, len(defs))
	for _, d := range defs {
		state[d.key] = &catSlots{}
	}

	// 1. 优先消费云端 summaryBuckets（兼容旧缓存混入 models 的 bucket 条目）
	rawBuckets := make([]model.QuotaBucket, 0, len(acc.Quota.SummaryBuckets))
	rawBuckets = append(rawBuckets, acc.Quota.SummaryBuckets...)
	for _, m := range acc.Quota.Models {
		if isSummaryBucketID(m.Name) {
			rawBuckets = append(rawBuckets, model.QuotaBucket{
				BucketID:    m.Name,
				DisplayName: m.DisplayName,
				Percentage:  m.Percentage,
				ResetTime:   m.ResetTime,
				Exhausted:   m.Percentage <= 0,
			})
		}
	}

	for _, b := range rawBuckets {
		key, weekly, ok := classifySummaryBucket(b.BucketID)
		if !ok {
			continue
		}
		entry := state[key]
		slot := WindowSlot{
			Percentage: b.Percentage,
			ResetTime:  strings.TrimSpace(b.ResetTime),
			Exhausted:  b.Exhausted || b.Percentage <= 0,
		}
		if weekly {
			if entry.weekly == nil || slot.Percentage < entry.weekly.Percentage {
				cp := slot
				entry.weekly = &cp
			}
		} else {
			if entry.fiveHour == nil || slot.Percentage < entry.fiveHour.Percentage {
				cp := slot
				entry.fiveHour = &cp
			}
		}
	}

	// 免费账号已有周额度时隐藏 5h 额度
	if isFree {
		for _, entry := range state {
			if entry.weekly != nil {
				entry.fiveHour = nil
			}
		}
	}

	// 2. 缺失分类时从过滤后的客户端模型额度兜底推导
	fallback := make(map[string][]model.ModelQuota, len(defs))
	for _, m := range acc.Quota.Models {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		if isExcludedSummaryModel(m.Name, m.DisplayName) {
			continue
		}
		cat := classifyModelCategory(m.Name, m.DisplayName)
		if cat != "" {
			fallback[cat] = append(fallback[cat], m)
		}
	}

	for _, d := range defs {
		entry := state[d.key]
		if entry.fiveHour != nil || entry.weekly != nil {
			continue
		}
		items := fallback[d.key]
		if len(items) == 0 {
			continue
		}
		minPct := 100
		earliestReset := ""
		for _, item := range items {
			if item.Percentage < minPct {
				minPct = item.Percentage
			}
			if isEarlierISO(item.ResetTime, earliestReset) {
				earliestReset = strings.TrimSpace(item.ResetTime)
			}
		}
		slot := &WindowSlot{
			Percentage: minPct,
			ResetTime:  earliestReset,
			Exhausted:  minPct <= 0,
		}
		if d.key == "gemini-argon" || d.key == "gemini-carbon" || isFree || isWeeklyResetTime(earliestReset, now) {
			entry.weekly = slot
		} else {
			entry.fiveHour = slot
		}
	}

	result := make([]CategorySummary, 0, len(defs))
	for _, d := range defs {
		entry := state[d.key]
		if entry.fiveHour == nil && entry.weekly == nil {
			continue
		}
		minPct := 100
		earliestReset := ""
		for _, s := range []*WindowSlot{entry.fiveHour, entry.weekly} {
			if s == nil {
				continue
			}
			if s.Percentage < minPct {
				minPct = s.Percentage
			}
			if isEarlierISO(s.ResetTime, earliestReset) {
				earliestReset = s.ResetTime
			}
		}
		result = append(result, CategorySummary{
			Key:        d.key,
			Label:      d.label,
			ShortLabel: d.shortLabel,
			FiveHour:   entry.fiveHour,
			Weekly:     entry.weekly,
			Percentage: minPct,
			ResetTime:  earliestReset,
		})
	}
	return result
}

// MinQuotaPercentage 返回账号综合最低剩余配额百分比；若尚未拉取配额则返回 -1。
func MinQuotaPercentage(acc model.Account, now time.Time) int {
	if acc.Quota == nil {
		return -1
	}
	cats := SummarizeAccountCategories(acc, now)
	filteredModels := make([]model.ModelQuota, 0, len(acc.Quota.Models))
	for _, m := range acc.Quota.Models {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		if isExcludedSummaryModel(m.Name, m.DisplayName) {
			continue
		}
		filteredModels = append(filteredModels, m)
	}

	if len(cats) == 0 && len(filteredModels) == 0 {
		if len(acc.Quota.Models) == 0 {
			return -1
		}
		minPct := 100
		for _, m := range acc.Quota.Models {
			if m.Percentage < minPct {
				minPct = m.Percentage
			}
		}
		return minPct
	}

	coveredCategories := make(map[string]struct{}, len(cats))
	minPct := 100
	for _, c := range cats {
		coveredCategories[c.Key] = struct{}{}
		if c.Percentage < minPct {
			minPct = c.Percentage
		}
	}
	for _, m := range filteredModels {
		// 若该模型所属分类已由 SummarizeAccountCategories 权威汇总（例如 FREE 账号隐藏 5h 后以周额度展示），
		// 不再用原始模型可能携带的隐藏窗口数值覆盖，保持状态栏、低配额告警与卡片口径一致。
		if cat := classifyModelCategory(m.Name, m.DisplayName); cat != "" {
			if _, covered := coveredCategories[cat]; covered {
				continue
			}
		}
		if m.Percentage < minPct {
			minPct = m.Percentage
		}
	}
	return minPct
}

// IsAccountAbnormal 判断账号是否处于异常状态（禁用、403 封禁、需验证或存在配额报错）。
func IsAccountAbnormal(acc model.Account) bool {
	if acc.Disabled {
		return true
	}
	if acc.Quota != nil && acc.Quota.IsForbidden {
		return true
	}
	if acc.QuotaError != nil {
		if strings.TrimSpace(acc.QuotaError.Reason) == "VALIDATION_REQUIRED" {
			return true
		}
		if sanitizeValidationURL(acc.QuotaError.ValidationURL) != "" {
			return true
		}
		if strings.TrimSpace(acc.QuotaError.Message) != "" {
			return true
		}
	}
	return false
}

// FormatResetCountdown 将 ISO 时间戳格式化为紧凑倒计时（例如 4d 12h / 2h 14m / 35m / 即将重置）。
func FormatResetCountdown(iso string, now time.Time) string {
	trimmed := strings.TrimSpace(iso)
	if trimmed == "" {
		return "—"
	}
	if now.IsZero() {
		now = time.Now()
	}
	ts, ok := parseISO(trimmed)
	if !ok {
		return trimmed
	}
	diffSec := int64(ts.Sub(now) / time.Second)
	if diffSec <= 0 {
		return "即将重置"
	}
	days := diffSec / 86400
	hours := (diffSec % 86400) / 3600
	mins := (diffSec % 3600) / 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	if mins < 1 {
		mins = 1
	}
	return fmt.Sprintf("%dm", mins)
}

// MaskEmail 按与前端隐私模式一致的规则脱敏邮箱地址。
func MaskEmail(email string, enabled bool) string {
	if !enabled {
		return email
	}
	trimmed := strings.TrimSpace(email)
	atIdx := strings.IndexByte(trimmed, '@')
	if atIdx <= 0 {
		runes := []rune(trimmed)
		if len(runes) <= 2 {
			return "***"
		}
		return string(runes[:2]) + "***"
	}
	local := []rune(trimmed[:atIdx])
	domain := trimmed[atIdx+1:]
	var visibleLocal string
	if len(local) <= 2 {
		visibleLocal = string(local[:1]) + "*"
	} else {
		visibleLocal = string(local[:2]) + "***"
	}
	return visibleLocal + "@" + domain
}
