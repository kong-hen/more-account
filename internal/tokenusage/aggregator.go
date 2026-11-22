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
	return s.buildBundleWithOptions(snap, query, false)
}

func (s *Service) buildBundleWithOptions(snap *scanSnapshot, query model.TokenUsageQuery, unlimitedPage bool) *model.TokenUsageBundle {
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
	if !unlimitedPage && (pageSize <= 0 || pageSize > 100) {
		pageSize = 20
	}
	page := query.Page
	if unlimitedPage || page <= 0 {
		page = 1
	}

	minDayKey := presetStartDayKey(preset, time.Now().Local())

	// 收集全量可用模型、模型显示名映射与工作区列表（不受当前筛选条件裁剪，防止切换筛选后下拉选项显示名退化）
	availModelSet := make(map[string]struct{})
	availModelNames := make(map[string]string)
	availWSSet := make(map[string]struct{})
	for _, c := range snap.Conversations {
		for _, m := range c.Summary.Models {
			availModelSet[m] = struct{}{}
		}
		if c.Summary.PrimaryModelID != "" && c.Summary.PrimaryModelDisplayName != "" {
			if _, ok := availModelNames[c.Summary.PrimaryModelID]; !ok {
				availModelNames[c.Summary.PrimaryModelID] = c.Summary.PrimaryModelDisplayName
			}
		}
		for _, b := range c.Buckets {
			if b.ModelID == "" {
				continue
			}
			availModelSet[b.ModelID] = struct{}{}
			if b.ModelDisplayName != "" {
				availModelNames[b.ModelID] = b.ModelDisplayName
			}
		}
		if c.Summary.WorkspaceName != "" {
			availWSSet[c.Summary.WorkspaceName] = struct{}{}
		}
	}
	availModels := mapKeysSorted(availModelSet)
	for _, m := range availModels {
		if strings.TrimSpace(availModelNames[m]) == "" {
			availModelNames[m] = FormatModelDisplayName(m)
		}
	}
	availWS := mapKeysSorted(availWSSet)
	availWSOptions := buildWorkspaceFilterOptions(snap.Conversations)

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
		if wsFilter != "" && conv.Summary.WorkspacePath != wsFilter && conv.Summary.WorkspaceName != wsFilter {
			continue
		}
		if searchLower != "" {
			haystack := strings.ToLower(conv.Summary.DisplayTitle + " " + conv.Summary.Preview + " " + conv.Summary.WorkspaceName + " " + conv.Summary.WorkspacePath + " " + conv.Summary.ConversationID)
			if !strings.Contains(haystack, searchLower) {
				continue
			}
		}

		// 过滤命中的时间与模型桶，并同步按命中桶重算该对话在当前筛选窗口下的主力模型与模型列表
		var convExact model.ExactTokenTotals
		var convEst model.EstimatedTokenTotals
		var convExactTurns int
		convModelTotals := make(map[string]uint64)
		convModelDisplay := make(map[string]string)
		matchedAny := false

		for _, b := range conv.Buckets {
			// 防御旧缓存中可能残留的全零空桶，避免虚增对话数与空模型分布
			if b.Exact.TotalTokens == 0 && b.EstInput == 0 && b.EstOutput == 0 && b.ExactTurnCount == 0 && b.EstimatedTurnCount == 0 {
				continue
			}
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
			convModelTotals[b.ModelID] += b.Exact.TotalTokens + b.EstInput + b.EstOutput
			if b.ModelDisplayName != "" {
				convModelDisplay[b.ModelID] = b.ModelDisplayName
			}

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

		wsKey := conv.Summary.WorkspacePath
		if wsKey == "" {
			wsKey = conv.Summary.WorkspaceName
		}
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

		filteredModels := make([]string, 0, len(convModelTotals))
		primaryModelID := "unknown"
		var bestTok uint64
		for mid, tot := range convModelTotals {
			filteredModels = append(filteredModels, mid)
			if tot > bestTok || (tot == bestTok && (primaryModelID == "unknown" || mid < primaryModelID)) {
				bestTok = tot
				primaryModelID = mid
			}
		}
		sort.Strings(filteredModels)
		primaryDisplay := convModelDisplay[primaryModelID]
		if primaryDisplay == "" {
			primaryDisplay = FormatModelDisplayName(primaryModelID)
		}

		item := conv.Summary
		item.Exact = convExact
		item.Estimated = convEst
		item.ExactTurnCount = convExactTurns
		item.EstimatedTurnCount = convEst.EstimatedTurnCount
		item.TurnCount = convExactTurns + convEst.EstimatedTurnCount
		item.PrimaryModelID = primaryModelID
		item.PrimaryModelDisplayName = primaryDisplay
		item.Models = filteredModels
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
		ti := models[i].Exact.TotalTokens + models[i].EstimatedTotalTokens
		tj := models[j].Exact.TotalTokens + models[j].EstimatedTotalTokens
		if ti != tj {
			return ti > tj
		}
		return models[i].ModelID < models[j].ModelID
	})

	// 整理工作区列表
	workspaces := make([]model.WorkspaceUsageSummary, 0, len(wsMap))
	for _, ws := range wsMap {
		workspaces = append(workspaces, *ws)
	}
	sort.Slice(workspaces, func(i, j int) bool {
		ti := workspaces[i].Exact.TotalTokens + workspaces[i].EstimatedTotalTokens
		tj := workspaces[j].Exact.TotalTokens + workspaces[j].EstimatedTotalTokens
		if ti != tj {
			return ti > tj
		}
		if workspaces[i].WorkspaceName != workspaces[j].WorkspaceName {
			return workspaces[i].WorkspaceName < workspaces[j].WorkspaceName
		}
		return workspaces[i].WorkspacePath < workspaces[j].WorkspacePath
	})

	// 排序对话列表（严格满足弱序契约，相等时以 ConversationID 稳定打破平局）
	asc := strings.EqualFold(query.Order, "asc")
	sort.Slice(filteredItems, func(i, j int) bool {
		var cmp int
		switch query.Sort {
		case "totalTokens":
			ti := filteredItems[i].Exact.TotalTokens + filteredItems[i].Estimated.TotalTokens
			tj := filteredItems[j].Exact.TotalTokens + filteredItems[j].Estimated.TotalTokens
			if ti < tj {
				cmp = -1
			} else if ti > tj {
				cmp = 1
			}
		case "turnCount":
			if filteredItems[i].TurnCount < filteredItems[j].TurnCount {
				cmp = -1
			} else if filteredItems[i].TurnCount > filteredItems[j].TurnCount {
				cmp = 1
			}
		default:
			cmp = strings.Compare(filteredItems[i].LastModifiedTime, filteredItems[j].LastModifiedTime)
		}
		if cmp == 0 {
			return filteredItems[i].ConversationID < filteredItems[j].ConversationID
		}
		if asc {
			return cmp < 0
		}
		return cmp > 0
	})

	totalItems := len(filteredItems)
	var (
		totalPages int
		pagedItems []model.ConversationSummaryItem
	)
	if unlimitedPage {
		// 导出场景不设 100 条分页上限，返回当前筛选条件下的完整对话列表
		page = 1
		totalPages = 1
		pageSize = totalItems
		pagedItems = filteredItems
	} else {
		totalPages = (totalItems + pageSize - 1) / pageSize
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
		pagedItems = filteredItems[startIdx:endIdx]
	}
	if pagedItems == nil {
		pagedItems = []model.ConversationSummaryItem{}
	}

	return &model.TokenUsageBundle{
		Overview: model.TokenUsageOverview{
			Exact:                     totalExact,
			Estimated:                 totalEst,
			ConversationCount:         totalItems,
			TurnCount:                 totalTurns,
			ExactTurnCount:            totalExactTurn,
			Trend:                     trend,
			Models:                    models,
			Workspaces:                workspaces,
			AvailableModels:           availModels,
			AvailableModelNames:       availModelNames,
			AvailableWorkspace:        availWS,
			AvailableWorkspaceOptions: availWSOptions,
			ScanDurationMs:            snap.ScanDurationMs,
			ScannedFiles:              snap.ScannedFiles,
			CachedFiles:               snap.CachedFiles,
			SkippedErrors:             snap.SkippedErrors,
			LastSyncedAt:              snap.LastSyncedAt.Format(time.RFC3339),
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

// shortWorkspacePathHint 提取路径末尾两级目录作为同名工作区的简洁区分提示。
func shortWorkspacePathHint(rawPath string) string {
	cleaned := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(rawPath), `\`, "/"), "/")
	if cleaned == "" {
		return ""
	}
	parts := strings.Split(cleaned, "/")
	nonEmpty := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	if len(nonEmpty) <= 2 {
		return strings.Join(nonEmpty, "/")
	}
	return nonEmpty[len(nonEmpty)-2] + "/" + nonEmpty[len(nonEmpty)-1]
}

// buildWorkspaceFilterOptions 构建全量工作区筛选选项，对同名不同路径的目录自动附带区分路径提示。
func buildWorkspaceFilterOptions(convs []scannedConversation) []model.WorkspaceFilterOption {
	type wsMeta struct {
		name string
		path string
	}
	byKey := make(map[string]wsMeta)
	for _, c := range convs {
		name := strings.TrimSpace(c.Summary.WorkspaceName)
		path := strings.TrimSpace(c.Summary.WorkspacePath)
		if name == "" && path == "" {
			continue
		}
		key := path
		if key == "" {
			key = name
		}
		if _, exists := byKey[key]; !exists {
			byKey[key] = wsMeta{name: name, path: path}
		}
	}
	if len(byKey) == 0 {
		return []model.WorkspaceFilterOption{}
	}

	nameCount := make(map[string]int, len(byKey))
	for _, meta := range byKey {
		nameCount[meta.name]++
	}

	// 检测两级路径简写是否在同名目录间仍发生冲突，冲突时回退展示完整路径
	shortHintCount := make(map[string]int, len(byKey))
	for _, meta := range byKey {
		if nameCount[meta.name] > 1 && meta.path != "" {
			hintKey := meta.name + "\x00" + shortWorkspacePathHint(meta.path)
			shortHintCount[hintKey]++
		}
	}

	opts := make([]model.WorkspaceFilterOption, 0, len(byKey))
	for key, meta := range byKey {
		label := meta.name
		if nameCount[meta.name] > 1 && meta.path != "" {
			hint := shortWorkspacePathHint(meta.path)
			if shortHintCount[meta.name+"\x00"+hint] > 1 || hint == "" {
				hint = meta.path
			}
			label = fmt.Sprintf("%s (%s)", meta.name, hint)
		}
		opts = append(opts, model.WorkspaceFilterOption{
			Value: key,
			Label: label,
			Name:  meta.name,
			Path:  meta.path,
		})
	}

	sort.Slice(opts, func(i, j int) bool {
		if opts[i].Name != opts[j].Name {
			return opts[i].Name < opts[j].Name
		}
		if opts[i].Path != opts[j].Path {
			return opts[i].Path < opts[j].Path
		}
		return opts[i].Value < opts[j].Value
	})
	return opts
}
