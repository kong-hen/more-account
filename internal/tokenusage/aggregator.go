package tokenusage

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"more-account/internal/model"
)

func (s *Service) buildBundle(snap *scanSnapshot, query model.TokenUsageQuery) *model.TokenUsageBundle {
	customDir, effectiveDir, defaultDir := s.resolveDataDir()
	_, statErr := os.Stat(effectiveDir)

	preset := strings.ToLower(strings.TrimSpace(query.Preset))
	if preset == "" {
		preset = "7d"
	}
	granularity := strings.ToLower(strings.TrimSpace(query.Granularity))
	if granularity != "week" && granularity != "month" {
		granularity = "day"
	}
	pageSize := query.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}

	minDayKey := presetStartDayKey(preset, time.Now().Local())

	// 收集可用模型与工作区列表
	availModelSet := make(map[string]struct{})
	availWSSet := make(map[string]struct{})
	for _, c := range snap.Conversations {
		for _, m := range c.Summary.Models {
			availModelSet[m] = struct{}{}
		}
		if c.Summary.WorkspaceName != "" {
			availWSSet[c.Summary.WorkspaceName] = struct{}{}
		}
	}
	availModels := mapKeysSorted(availModelSet)
	availWS := mapKeysSorted(availWSSet)

	var (
		totalExact     model.ExactTokenTotals
		totalEst       model.EstimatedTokenTotals
		totalTurns     int
		totalExactTurn int
		trendMap       = make(map[string]*model.UsageTrendBucket)
		trendConvSeen  = make(map[string]map[string]struct{})
		modelMap       = make(map[string]*model.ModelUsageSummary)
		modelConvSeen  = make(map[string]map[string]struct{})
		wsMap          = make(map[string]*model.WorkspaceUsageSummary)
		filteredItems  []model.ConversationSummaryItem
	)

	searchLower := strings.ToLower(strings.TrimSpace(query.Search))
	kindFilter := strings.ToLower(strings.TrimSpace(query.Kind))
	modelFilter := strings.ToLower(strings.TrimSpace(query.Model))
	wsFilter := strings.TrimSpace(query.Workspace)

	for _, conv := range snap.Conversations {
		if kindFilter != "" && conv.Summary.Kind != kindFilter {
			continue
		}
		if wsFilter != "" && conv.Summary.WorkspaceName != wsFilter {
			continue
		}
		if searchLower != "" {
			haystack := strings.ToLower(conv.Summary.DisplayTitle + " " + conv.Summary.Preview + " " + conv.Summary.WorkspaceName + " " + conv.Summary.ConversationID)
			if !strings.Contains(haystack, searchLower) {
				continue
			}
		}

		// 过滤命中的时间与模型桶
		var convExact model.ExactTokenTotals
		var convEst model.EstimatedTokenTotals
		var convExactTurns int
		matchedAny := false

		for _, b := range conv.Buckets {
			if minDayKey != "" && b.DayKey < minDayKey {
				continue
			}
			if modelFilter != "" && strings.ToLower(b.ModelID) != modelFilter {
				continue
			}
			matchedAny = true

			convExact.Add(b.Exact)
			convEst.InputTokens += b.EstInput
			convEst.OutputTokens += b.EstOutput
			convEst.TotalTokens += b.EstInput + b.EstOutput
			convEst.EstimatedTurnCount += b.EstimatedTurnCount
			convExactTurns += b.ExactTurnCount

			// 趋势桶聚合
			tKey, tLabel := bucketKeyForGranularity(b.DayKey, granularity)
			tb, ok := trendMap[tKey]
			if !ok {
				tb = &model.UsageTrendBucket{Key: tKey, Label: tLabel}
				trendMap[tKey] = tb
				trendConvSeen[tKey] = make(map[string]struct{})
			}
			tb.Exact.Add(b.Exact)
			tb.EstimatedTotalTokens += b.EstInput + b.EstOutput
			tb.TurnCount += b.ExactTurnCount + b.EstimatedTurnCount
			trendConvSeen[tKey][conv.Summary.ConversationID] = struct{}{}

			// 模型分布聚合
			ms, ok := modelMap[b.ModelID]
			if !ok {
				ms = &model.ModelUsageSummary{
					ModelID:          b.ModelID,
					ModelDisplayName: b.ModelDisplayName,
					APIProvider:      b.APIProvider,
				}
				modelMap[b.ModelID] = ms
				modelConvSeen[b.ModelID] = make(map[string]struct{})
			}
			ms.Exact.Add(b.Exact)
			ms.EstimatedTotalTokens += b.EstInput + b.EstOutput
			ms.TurnCount += b.ExactTurnCount + b.EstimatedTurnCount
			modelConvSeen[b.ModelID][conv.Summary.ConversationID] = struct{}{}
		}

		if !matchedAny {
			continue
		}

		totalExact.Add(convExact)
		totalEst.InputTokens += convEst.InputTokens
		totalEst.OutputTokens += convEst.OutputTokens
		totalEst.TotalTokens += convEst.TotalTokens
		totalEst.EstimatedTurnCount += convEst.EstimatedTurnCount
		totalExactTurn += convExactTurns
		totalTurns += convExactTurns + convEst.EstimatedTurnCount

		wsKey := conv.Summary.WorkspaceName
		wsSummary, ok := wsMap[wsKey]
		if !ok {
			wsSummary = &model.WorkspaceUsageSummary{
				WorkspaceName: conv.Summary.WorkspaceName,
				WorkspacePath: conv.Summary.WorkspacePath,
			}
			wsMap[wsKey] = wsSummary
		}
		wsSummary.ConversationCount++
		wsSummary.TurnCount += convExactTurns + convEst.EstimatedTurnCount
		wsSummary.Exact.Add(convExact)
		wsSummary.EstimatedTotalTokens += convEst.TotalTokens

		item := conv.Summary
		item.Exact = convExact
		item.Estimated = convEst
		item.ExactTurnCount = convExactTurns
		item.EstimatedTurnCount = convEst.EstimatedTurnCount
		item.TurnCount = convExactTurns + convEst.EstimatedTurnCount
		filteredItems = append(filteredItems, item)
	}

	// 整理趋势列表
	trend := make([]model.UsageTrendBucket, 0, len(trendMap))
	for k, tb := range trendMap {
		tb.ConversationCount = len(trendConvSeen[k])
		trend = append(trend, *tb)
	}
	sort.Slice(trend, func(i, j int) bool {
		return trend[i].Key < trend[j].Key
	})

	// 整理模型列表
	models := make([]model.ModelUsageSummary, 0, len(modelMap))
	for k, ms := range modelMap {
		ms.ConversationCount = len(modelConvSeen[k])
		models = append(models, *ms)
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].Exact.TotalTokens+models[i].EstimatedTotalTokens >
			models[j].Exact.TotalTokens+models[j].EstimatedTotalTokens
	})

	// 整理工作区列表
	workspaces := make([]model.WorkspaceUsageSummary, 0, len(wsMap))
	for _, ws := range wsMap {
		workspaces = append(workspaces, *ws)
	}
	sort.Slice(workspaces, func(i, j int) bool {
		return workspaces[i].Exact.TotalTokens+workspaces[i].EstimatedTotalTokens >
			workspaces[j].Exact.TotalTokens+workspaces[j].EstimatedTotalTokens
	})

	// 排序对话列表
	asc := strings.EqualFold(query.Order, "asc")
	sort.Slice(filteredItems, func(i, j int) bool {
		var less bool
		switch query.Sort {
		case "totalTokens":
			ti := filteredItems[i].Exact.TotalTokens + filteredItems[i].Estimated.TotalTokens
			tj := filteredItems[j].Exact.TotalTokens + filteredItems[j].Estimated.TotalTokens
			less = ti < tj
		case "turnCount":
			less = filteredItems[i].TurnCount < filteredItems[j].TurnCount
		default:
			less = filteredItems[i].LastModifiedTime < filteredItems[j].LastModifiedTime
		}
		if asc {
			return less
		}
		return !less
	})

	totalItems := len(filteredItems)
	totalPages := (totalItems + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page > totalPages {
		page = totalPages
	}
	startIdx := (page - 1) * pageSize
	endIdx := startIdx + pageSize
	if startIdx > totalItems {
		startIdx = totalItems
	}
	if endIdx > totalItems {
		endIdx = totalItems
	}
	pagedItems := filteredItems[startIdx:endIdx]
	if pagedItems == nil {
		pagedItems = []model.ConversationSummaryItem{}
	}

	return &model.TokenUsageBundle{
		Overview: model.TokenUsageOverview{
			Exact:              totalExact,
			Estimated:          totalEst,
			ConversationCount:  totalItems,
			TurnCount:          totalTurns,
			ExactTurnCount:     totalExactTurn,
			Trend:              trend,
			Models:             models,
			Workspaces:         workspaces,
			AvailableModels:    availModels,
			AvailableWorkspace: availWS,
			ScanDurationMs:     snap.ScanDurationMs,
			ScannedFiles:       snap.ScannedFiles,
			CachedFiles:        snap.CachedFiles,
			SkippedErrors:      snap.SkippedErrors,
			LastSyncedAt:       snap.LastSyncedAt.Format(time.RFC3339),
		},
		Conversations: model.PaginatedConversations{
			Items:      pagedItems,
			Total:      totalItems,
			Page:       page,
			PageSize:   pageSize,
			TotalPages: totalPages,
		},
		Settings: model.TokenUsageSettings{
			CustomDataDir:    customDir,
			EffectiveDataDir: effectiveDir,
			DefaultDataDir:   defaultDir,
			DataDirExists:    statErr == nil,
		},
	}
}

func presetStartDayKey(preset string, now time.Time) string {
	switch preset {
	case "today":
		return now.Format("2006-01-02")
	case "7d":
		return now.AddDate(0, 0, -6).Format("2006-01-02")
	case "30d":
		return now.AddDate(0, 0, -29).Format("2006-01-02")
	case "month":
		return time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	default:
		return ""
	}
}

func bucketKeyForGranularity(dayKey, granularity string) (string, string) {
	t, err := time.Parse("2006-01-02", dayKey)
	if err != nil {
		return dayKey, dayKey
	}
	switch granularity {
	case "month":
		k := t.Format("2006-01")
		return k, k
	case "week":
		year, week := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week), fmt.Sprintf("W%02d", week)
	default:
		return dayKey, t.Format("01-02")
	}
}

func mapKeysSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
