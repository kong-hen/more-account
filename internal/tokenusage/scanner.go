package tokenusage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"more-account/internal/model"
	"more-account/internal/storage"
)

const (
	cacheFileName      = "token_usage_cache.json"
	cacheSchemaVersion = 2
	unassignedWSName   = "未分配工作区"
)

type fileFingerprint struct {
	DBSize     int64 `json:"dbSize"`
	DBMtimeMs  int64 `json:"dbMtimeMs"`
	WALSize    int64 `json:"walSize"`
	WALMtimeMs int64 `json:"walMtimeMs"`
}

type cachedModelBucket struct {
	DayKey             string                 `json:"dayKey"`
	ModelID            string                 `json:"modelId"`
	ModelDisplayName   string                 `json:"modelDisplayName"`
	APIProvider        string                 `json:"apiProvider"`
	ExactTurnCount     int                    `json:"exactTurnCount"`
	EstimatedTurnCount int                    `json:"estimatedTurnCount"`
	Exact              model.ExactTokenTotals `json:"exact"`
	EstInput           uint64                 `json:"estInput"`
	EstOutput          uint64                 `json:"estOutput"`
}

type cachedConvEntry struct {
	Fingerprint    fileFingerprint     `json:"fp"`
	StepCount      int                 `json:"steps"`
	CreatedAtMs    int64               `json:"createdAtMs"`
	LastModifiedMs int64               `json:"lastModifiedMs"`
	Buckets        []cachedModelBucket `json:"buckets"`
}

type persistedScanCache struct {
	SchemaVersion int                                   `json:"schemaVersion"`
	ByDataDir     map[string]map[string]cachedConvEntry `json:"byDataDir"`
}

type summaryRow struct {
	ConversationID       string
	Title                string
	Preview              string
	StepCount            int
	LastModifiedMs       int64
	WorkspaceURIs        string
	AgentName            string
	ParentConversationID string
	NestingDepth         int
}

type scannedConversation struct {
	Summary        model.ConversationSummaryItem
	LastModifiedMs int64
	Buckets        []cachedModelBucket
}

type scanSnapshot struct {
	DataDir        string
	DirMarker      string
	Conversations  []scannedConversation
	ScanDurationMs int64
	ScannedFiles   int
	CachedFiles    int
	SkippedErrors  int
	LastSyncedAt   time.Time
}

// Service 提供严格只读的 Antigravity 本地 Token 用量扫描、缓存与多维聚合查询。
type Service struct {
	cacheDir       string
	getCustomDir   func() string
	scanMu         sync.Mutex
	snapMu         sync.RWMutex
	snapshot       *scanSnapshot
}

// NewService 创建 Token 用量只读分析服务。
func NewService(cacheDir string, getCustomDir func() string) *Service {
	return &Service{
		cacheDir:     cacheDir,
		getCustomDir: getCustomDir,
	}
}

// DefaultAntigravityDataDir 返回本机默认 ~/.gemini/antigravity 目录路径。
func DefaultAntigravityDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".gemini", "antigravity")
}

func (s *Service) resolveDataDir() (customDir, effectiveDir, defaultDir string) {
	defaultDir = DefaultAntigravityDataDir()
	if s.getCustomDir != nil {
		customDir = storage.ExpandHomePath(s.getCustomDir())
	}
	if customDir != "" {
		effectiveDir = customDir
	} else {
		effectiveDir = defaultDir
	}
	return customDir, effectiveDir, defaultDir
}

func computeQuickDirMarker(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	targets := []string{
		filepath.Join(dataDir, "conversation_summaries.db"),
		filepath.Join(dataDir, "conversation_summaries.db-wal"),
		filepath.Join(dataDir, "conversations"),
	}
	var sb strings.Builder
	for _, p := range targets {
		if st, err := os.Stat(p); err == nil {
			fmt.Fprintf(&sb, "%d:%d;", st.ModTime().UnixNano(), st.Size())
		} else {
			sb.WriteString("0;")
		}
	}
	return sb.String()
}

// Query 读取当前内存快照并按查询条件聚合；若尚无快照，或检测到本地会话摘要/目录发生变更（且距上次扫描超过 5 秒），自动执行增量保鲜扫描。
func (s *Service) Query(query model.TokenUsageQuery) (*model.TokenUsageBundle, error) {
	_, effectiveDir, _ := s.resolveDataDir()
	s.snapMu.RLock()
	snap := s.snapshot
	s.snapMu.RUnlock()

	if snap != nil && snap.DataDir == effectiveDir {
		if time.Since(snap.LastSyncedAt) < 5*time.Second || snap.DirMarker == computeQuickDirMarker(effectiveDir) {
			return s.buildBundle(snap, query), nil
		}
	}
	return s.Sync(false, query)
}

// Sync 对本地目录执行增量或全量重建扫描，并返回最新聚合面板结果。
func (s *Service) Sync(rebuild bool, query model.TokenUsageQuery) (*model.TokenUsageBundle, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	_, effectiveDir, _ := s.resolveDataDir()
	snap := s.runScan(effectiveDir, rebuild)

	s.snapMu.Lock()
	s.snapshot = snap
	s.snapMu.Unlock()

	return s.buildBundle(snap, query), nil
}

func (s *Service) runScan(dataDir string, rebuild bool) *scanSnapshot {
	started := time.Now()
	snap := &scanSnapshot{
		DataDir:      dataDir,
		DirMarker:    computeQuickDirMarker(dataDir),
		LastSyncedAt: time.Now(),
	}

	if st, err := os.Stat(dataDir); err != nil || !st.IsDir() {
		snap.ScanDurationMs = time.Since(started).Milliseconds()
		return snap
	}

	// 加载按扫描目录隔离的历史指纹缓存
	cachePath := filepath.Join(s.cacheDir, cacheFileName)
	dirCacheKey := filepath.Clean(dataDir)
	fullCache := persistedScanCache{
		SchemaVersion: cacheSchemaVersion,
		ByDataDir:     make(map[string]map[string]cachedConvEntry),
	}
	var dirEntries map[string]cachedConvEntry
	var loaded persistedScanCache
	if exists, err := storage.ReadJSONIfExists(cachePath, &loaded); err == nil && exists && loaded.SchemaVersion == cacheSchemaVersion && loaded.ByDataDir != nil {
		fullCache = loaded
		if !rebuild && fullCache.ByDataDir[dirCacheKey] != nil {
			dirEntries = fullCache.ByDataDir[dirCacheKey]
		}
	}
	if dirEntries == nil {
		dirEntries = make(map[string]cachedConvEntry)
	}

	// 1. 只读读取 conversation_summaries.db
	summaries := readConversationSummaries(filepath.Join(dataDir, "conversation_summaries.db"))

	// 2. 枚举 conversations/*.db 文件
	convDir := filepath.Join(dataDir, "conversations")
	entries, _ := os.ReadDir(convDir)
	type fileTask struct {
		convID string
		dbPath string
		fp     fileFingerprint
	}
	var tasks []fileTask
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		convID := strings.TrimSuffix(entry.Name(), ".db")
		dbPath := filepath.Join(convDir, entry.Name())
		fp := statFileFingerprint(dbPath)
		tasks = append(tasks, fileTask{
			convID: convID,
			dbPath: dbPath,
			fp:     fp,
		})
	}

	// 3. 并发扫描未命中缓存的对话 SQLite 文件
	workers := runtime.NumCPU()
	if workers < 2 {
		workers = 2
	}
	if workers > 8 {
		workers = 8
	}

	var (
		mu            sync.Mutex
		nextCache     = make(map[string]cachedConvEntry, len(tasks))
		cachedCount   atomic.Int32
		scannedCount  atomic.Int32
		skippedErrors atomic.Int32
	)

	jobs := make(chan fileTask, len(tasks))
	for _, t := range tasks {
		jobs <- t
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if !rebuild {
					if prev, ok := dirEntries[task.convID]; ok && prev.Fingerprint == task.fp {
						cachedCount.Add(1)
						mu.Lock()
						nextCache[task.convID] = prev
						mu.Unlock()
						continue
					}
				}
				scannedCount.Add(1)
				parsed, err := scanSingleConversationDB(task.dbPath, task.fp)
				if err != nil {
					skippedErrors.Add(1)
					continue
				}
				mu.Lock()
				nextCache[task.convID] = parsed
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// 按当前扫描目录更新缓存并原子落盘，切换自定义目录时互不覆盖
	fullCache.SchemaVersion = cacheSchemaVersion
	fullCache.ByDataDir[dirCacheKey] = nextCache
	_ = storage.WriteJSONAtomic(cachePath, fullCache, 0600)

	// 4. 合并摘要与遥测数据
	allIDs := make(map[string]struct{}, len(summaries)+len(nextCache))
	for id := range summaries {
		allIDs[id] = struct{}{}
	}
	for id := range nextCache {
		allIDs[id] = struct{}{}
	}

	convs := make([]scannedConversation, 0, len(allIDs))
	for id := range allIDs {
		sumRow, hasSum := summaries[id]
		tel, hasTel := nextCache[id]
		if !hasTel && (!hasSum || sumRow.StepCount == 0) {
			continue
		}

		wsPath, wsName := decodeWorkspace(sumRow.WorkspaceURIs)
		lastModMs := sumRow.LastModifiedMs
		if tel.LastModifiedMs > lastModMs {
			lastModMs = tel.LastModifiedMs
		}
		if lastModMs == 0 && tel.Fingerprint.DBMtimeMs > 0 {
			lastModMs = tel.Fingerprint.DBMtimeMs
		}

		kind := "main"
		if strings.Contains(strings.ToLower(sumRow.AgentName), "swarm") {
			kind = "swarm"
		} else if sumRow.ParentConversationID != "" || sumRow.NestingDepth > 0 {
			kind = "subagent"
		}

		var exact model.ExactTokenTotals
		var est model.EstimatedTokenTotals
		var exactTurns int
		modelTotals := make(map[string]uint64)
		modelDisplay := make(map[string]string)

		for _, b := range tel.Buckets {
			exact.Add(b.Exact)
			est.InputTokens += b.EstInput
			est.OutputTokens += b.EstOutput
			est.TotalTokens += b.EstInput + b.EstOutput
			est.EstimatedTurnCount += b.EstimatedTurnCount
			exactTurns += b.ExactTurnCount
			modelTotals[b.ModelID] += b.Exact.TotalTokens + b.EstInput + b.EstOutput
			modelDisplay[b.ModelID] = b.ModelDisplayName
		}

		if exact.TotalTokens == 0 && est.TotalTokens == 0 && exactTurns == 0 && est.EstimatedTurnCount == 0 {
			continue
		}

		modelsList := make([]string, 0, len(modelTotals))
		primaryModelID := "unknown"
		var bestTok uint64
		for mid, tot := range modelTotals {
			modelsList = append(modelsList, mid)
			if tot > bestTok || (tot == bestTok && (primaryModelID == "unknown" || mid < primaryModelID)) {
				bestTok = tot
				primaryModelID = mid
			}
		}
		sort.Strings(modelsList)

		title := strings.TrimSpace(sumRow.Title)
		displayTitle := title
		if displayTitle == "" {
			if prev := strings.TrimSpace(sumRow.Preview); prev != "" {
				displayTitle = truncateText(prev, 60)
			} else {
				displayTitle = id
			}
		}

		stepCount := sumRow.StepCount
		if tel.StepCount > stepCount {
			stepCount = tel.StepCount
		}

		item := model.ConversationSummaryItem{
			ConversationID:          id,
			Title:                   title,
			DisplayTitle:            displayTitle,
			Preview:                 truncateText(strings.TrimSpace(sumRow.Preview), 120),
			Kind:                    kind,
			AgentName:               sumRow.AgentName,
			ParentConversationID:    sumRow.ParentConversationID,
			WorkspacePath:           wsPath,
			WorkspaceName:           wsName,
			CreatedAt:               formatUTCMs(tel.CreatedAtMs),
			LastModifiedTime:        formatUTCMs(lastModMs),
			StepCount:               stepCount,
			TurnCount:               exactTurns + est.EstimatedTurnCount,
			ExactTurnCount:          exactTurns,
			EstimatedTurnCount:      est.EstimatedTurnCount,
			PrimaryModelID:          primaryModelID,
			PrimaryModelDisplayName: modelDisplay[primaryModelID],
			Models:                  modelsList,
			DataSource:              "sqlite_db",
			Exact:                   exact,
			Estimated:               est,
		}
		if item.PrimaryModelDisplayName == "" {
			item.PrimaryModelDisplayName = FormatModelDisplayName(primaryModelID)
		}

		convs = append(convs, scannedConversation{
			Summary:        item,
			LastModifiedMs: lastModMs,
			Buckets:        tel.Buckets,
		})
	}

	snap.Conversations = convs
	snap.ScannedFiles = int(scannedCount.Load())
	snap.CachedFiles = int(cachedCount.Load())
	snap.SkippedErrors = int(skippedErrors.Load())
	snap.ScanDurationMs = time.Since(started).Milliseconds()
	return snap
}

func statFileFingerprint(dbPath string) fileFingerprint {
	var fp fileFingerprint
	if st, err := os.Stat(dbPath); err == nil {
		fp.DBSize = st.Size()
		fp.DBMtimeMs = st.ModTime().UnixMilli()
	}
	if st, err := os.Stat(dbPath + "-wal"); err == nil {
		fp.WALSize = st.Size()
		fp.WALMtimeMs = st.ModTime().UnixMilli()
	}
	return fp
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	// 严格只读模式打开，使用安全转义的 file: URI 并通过 modernc.org/sqlite 识别的 _pragma 语法启用 busy_timeout。
	slashPath := filepath.ToSlash(path)
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		slashPath = "/" + slashPath
	}
	u := url.URL{
		Scheme: "file",
		Path:   slashPath,
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(1500)")
	u.RawQuery = q.Encode()
	return sql.Open("sqlite", u.String())
}

func readConversationSummaries(dbPath string) map[string]summaryRow {
	out := make(map[string]summaryRow)
	if _, err := os.Stat(dbPath); err != nil {
		return out
	}
	db, err := openSQLiteReadOnly(dbPath)
	if err != nil {
		return out
	}
	defer db.Close()

	fullSQL := `SELECT conversation_id, title, preview, step_count, last_modified_time,
		workspace_uris, agent_name, parent_conversation_id, nesting_depth
		FROM conversation_summaries`
	rows, err := db.Query(fullSQL)
	if err != nil {
		// 回退兼容精简表结构
		minSQL := `SELECT conversation_id, title, preview, step_count, last_modified_time, workspace_uris
			FROM conversation_summaries`
		rows, err = db.Query(minSQL)
		if err != nil {
			return out
		}
		defer rows.Close()
		for rows.Next() {
			var cid, title, preview, lastMod, ws sql.NullString
			var steps sql.NullInt64
			if err := rows.Scan(&cid, &title, &preview, &steps, &lastMod, &ws); err != nil {
				continue
			}
			id := strings.TrimSpace(cid.String)
			if id == "" {
				continue
			}
			out[id] = summaryRow{
				ConversationID: id,
				Title:          title.String,
				Preview:        preview.String,
				StepCount:      int(steps.Int64),
				LastModifiedMs: parseFlexibleTimestampMs(lastMod.String),
				WorkspaceURIs:  ws.String,
			}
		}
		return out
	}
	defer rows.Close()

	for rows.Next() {
		var cid, title, preview, lastMod, ws, agent, parent sql.NullString
		var steps, depth sql.NullInt64
		if err := rows.Scan(&cid, &title, &preview, &steps, &lastMod, &ws, &agent, &parent, &depth); err != nil {
			continue
		}
		id := strings.TrimSpace(cid.String)
		if id == "" {
			continue
		}
		out[id] = summaryRow{
			ConversationID:       id,
			Title:                title.String,
			Preview:              preview.String,
			StepCount:            int(steps.Int64),
			LastModifiedMs:       parseFlexibleTimestampMs(lastMod.String),
			WorkspaceURIs:        ws.String,
			AgentName:            strings.TrimSpace(agent.String),
			ParentConversationID: strings.TrimSpace(parent.String),
			NestingDepth:         int(depth.Int64),
		}
	}
	return out
}

func scanSingleConversationDB(dbPath string, fp fileFingerprint) (cachedConvEntry, error) {
	db, err := openSQLiteReadOnly(dbPath)
	if err != nil {
		return cachedConvEntry{}, err
	}
	defer db.Close()

	entry := cachedConvEntry{
		Fingerprint: fp,
	}

	var maxStep sql.NullInt64
	if err := db.QueryRow(`SELECT IFNULL(MAX(idx) + 1, 0) FROM steps`).Scan(&maxStep); err == nil {
		entry.StepCount = int(maxStep.Int64)
	}

	var firstMeta []byte
	if err := db.QueryRow(`SELECT metadata FROM steps WHERE idx = 0`).Scan(&firstMeta); err == nil && len(firstMeta) > 0 {
		parsed := ParseStepMetadata(firstMeta)
		entry.CreatedAtMs = parsed.TimestampMs
	}

	// 读取 gen_metadata 按 step_index 建立关联
	genByStep := make(map[int]ParsedGenMetadata)
	if genRows, err := db.Query(`SELECT data FROM gen_metadata`); err == nil {
		for genRows.Next() {
			var blob []byte
			if err := genRows.Scan(&blob); err != nil || len(blob) == 0 {
				continue
			}
			gm := ParseGenMetadata(blob)
			for _, idx := range gm.StepIndices {
				genByStep[idx] = gm
			}
		}
		_ = genRows.Close()
	}

	// 聚合每个 (dayKey, modelID) 桶
	type bucketKey struct {
		dayKey  string
		modelID string
	}
	bucketMap := make(map[bucketKey]*cachedModelBucket)

	rows, err := db.Query(`SELECT idx, metadata, COALESCE(LENGTH(step_payload), 0) FROM steps WHERE step_type = 15 ORDER BY idx ASC`)
	if err != nil {
		// 若表结构不同，尝试只读 metadata
		rows, err = db.Query(`SELECT idx, metadata, 0 FROM steps WHERE step_type = 15 ORDER BY idx ASC`)
		if err != nil {
			return entry, err
		}
	}
	defer rows.Close()

	for rows.Next() {
		var idx int
		var metaBlob []byte
		var payloadLen int64
		if err := rows.Scan(&idx, &metaBlob, &payloadLen); err != nil {
			continue
		}
		stepMeta := ParseStepMetadata(metaBlob)
		if stepMeta.TimestampMs > entry.LastModifiedMs {
			entry.LastModifiedMs = stepMeta.TimestampMs
		}
		if entry.CreatedAtMs == 0 && stepMeta.TimestampMs > 0 {
			entry.CreatedAtMs = stepMeta.TimestampMs
		}

		gm, hasGM := genByStep[idx]
		usage := stepMeta.Usage
		if usage == nil && hasGM {
			usage = gm.Usage
		}

		// 仅在存在精确遥测或有效载荷估算时才建桶统计，避免残留全零幽灵桶虚增对话数。
		hasExact := usage != nil && usage.TotalTokens > 0
		hasEst := !hasExact && payloadLen > 0
		if !hasExact && !hasEst {
			continue
		}

		rawEnum := ""
		rawProvider := ""
		respModel := ""
		if usage != nil {
			rawEnum = formatEnumKey(usage.ModelEnum)
			rawProvider = formatEnumKey(usage.APIProvider)
		}
		if rawEnum == "" && hasGM {
			if gm.ModelEnumPlaceholder != "" {
				rawEnum = gm.ModelEnumPlaceholder
			} else if gm.ModelEnumNumeric > 0 {
				rawEnum = formatEnumKey(gm.ModelEnumNumeric)
			}
		}
		if rawEnum == "" && stepMeta.FallbackModelEnum > 0 {
			rawEnum = formatEnumKey(stepMeta.FallbackModelEnum)
		}
		if hasGM {
			respModel = gm.ResponseModel
		}
		resolved := ResolveModelInfo(rawEnum, respModel, rawProvider)

		tsMs := stepMeta.TimestampMs
		if tsMs == 0 {
			tsMs = fp.DBMtimeMs
		}
		dayKey := time.UnixMilli(tsMs).Local().Format("2006-01-02")
		key := bucketKey{dayKey: dayKey, modelID: resolved.ModelID}
		bucket, ok := bucketMap[key]
		if !ok {
			bucket = &cachedModelBucket{
				DayKey:           dayKey,
				ModelID:          resolved.ModelID,
				ModelDisplayName: resolved.DisplayName,
				APIProvider:      resolved.APIProvider,
			}
			bucketMap[key] = bucket
		}

		if hasExact {
			bucket.ExactTurnCount++
			bucket.Exact.Add(model.ExactTokenTotals{
				InputTokens:      usage.InputTokens,
				OutputTokens:     usage.OutputTokens,
				ThinkingTokens:   usage.ThinkingTokens,
				CacheReadTokens:  usage.CacheReadTokens,
				CacheWriteTokens: usage.CacheWriteTokens,
				TotalTokens:      usage.TotalTokens,
			})
		} else if hasEst {
			estOut := estimateTokensFromPayloadLen(payloadLen)
			bucket.EstimatedTurnCount++
			bucket.EstOutput += estOut
		}
	}

	entry.Buckets = make([]cachedModelBucket, 0, len(bucketMap))
	for _, b := range bucketMap {
		entry.Buckets = append(entry.Buckets, *b)
	}
	return entry, nil
}

// isSafeConversationID 校验对话 ID 仅为单段安全文件名，阻止任何路径遍历（.. / \）。
func isSafeConversationID(id string) bool {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || trimmed == "." || trimmed == ".." {
		return false
	}
	if strings.ContainsAny(trimmed, `/\`) || strings.Contains(trimmed, "..") || strings.ContainsRune(trimmed, 0) {
		return false
	}
	return filepath.Base(trimmed) == trimmed
}

// GetConversationDetail 按需只读解析单条对话的每一步消耗记录。
func (s *Service) GetConversationDetail(conversationID string) (*model.ConversationDetail, error) {
	if !isSafeConversationID(conversationID) {
		return nil, fmt.Errorf("非法的对话 ID")
	}
	_, effectiveDir, _ := s.resolveDataDir()
	convDir := filepath.Clean(filepath.Join(effectiveDir, "conversations"))
	dbPath := filepath.Clean(filepath.Join(convDir, conversationID+".db"))
	if rel, err := filepath.Rel(convDir, dbPath); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, fmt.Errorf("非法的对话记录路径")
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("未找到对话记录文件: %s", conversationID)
	}

	var summary model.ConversationSummaryItem
	s.snapMu.RLock()
	if s.snapshot != nil {
		for _, c := range s.snapshot.Conversations {
			if c.Summary.ConversationID == conversationID {
				summary = c.Summary
				break
			}
		}
	}
	s.snapMu.RUnlock()
	if summary.ConversationID == "" {
		summary.ConversationID = conversationID
		summary.DisplayTitle = conversationID
		summary.Kind = "main"
		summary.WorkspaceName = unassignedWSName
	}

	db, err := openSQLiteReadOnly(dbPath)
	if err != nil {
		return nil, fmt.Errorf("只读打开对话库失败: %w", err)
	}
	defer db.Close()

	genByStep := make(map[int]ParsedGenMetadata)
	if genRows, err := db.Query(`SELECT data FROM gen_metadata`); err == nil {
		for genRows.Next() {
			var blob []byte
			if err := genRows.Scan(&blob); err != nil || len(blob) == 0 {
				continue
			}
			gm := ParseGenMetadata(blob)
			for _, idx := range gm.StepIndices {
				genByStep[idx] = gm
			}
		}
		_ = genRows.Close()
	}

	rows, err := db.Query(`SELECT idx, status, metadata, COALESCE(LENGTH(step_payload), 0) FROM steps WHERE step_type = 15 ORDER BY idx ASC`)
	if err != nil {
		rows, err = db.Query(`SELECT idx, 0, metadata, 0 FROM steps WHERE step_type = 15 ORDER BY idx ASC`)
		if err != nil {
			return nil, fmt.Errorf("查询对话步骤失败: %w", err)
		}
	}
	defer rows.Close()

	turns := make([]model.TurnUsageRecord, 0)
	sliceMap := make(map[string]*model.ConversationModelSlice)

	for rows.Next() {
		var idx, status int
		var metaBlob []byte
		var payloadLen int64
		if err := rows.Scan(&idx, &status, &metaBlob, &payloadLen); err != nil {
			continue
		}
		stepMeta := ParseStepMetadata(metaBlob)
		gm, hasGM := genByStep[idx]
		usage := stepMeta.Usage
		if usage == nil && hasGM {
			usage = gm.Usage
		}

		rawEnum := ""
		rawProvider := ""
		respModel := ""
		if usage != nil {
			rawEnum = formatEnumKey(usage.ModelEnum)
			rawProvider = formatEnumKey(usage.APIProvider)
		}
		if rawEnum == "" && hasGM {
			if gm.ModelEnumPlaceholder != "" {
				rawEnum = gm.ModelEnumPlaceholder
			} else if gm.ModelEnumNumeric > 0 {
				rawEnum = formatEnumKey(gm.ModelEnumNumeric)
			}
		}
		if rawEnum == "" && stepMeta.FallbackModelEnum > 0 {
			rawEnum = formatEnumKey(stepMeta.FallbackModelEnum)
		}
		if hasGM {
			respModel = gm.ResponseModel
		}
		resolved := ResolveModelInfo(rawEnum, respModel, rawProvider)

		hasExact := usage != nil && usage.TotalTokens > 0
		hasEst := !hasExact && payloadLen > 0
		if !hasExact && !hasEst {
			continue
		}

		turn := model.TurnUsageRecord{
			StepIndex:        idx,
			Timestamp:        formatUTCMs(stepMeta.TimestampMs),
			ModelID:          resolved.ModelID,
			ModelDisplayName: resolved.DisplayName,
			APIProvider:      resolved.APIProvider,
			Source:           "sqlite_db",
			Status:           status,
		}

		slice, ok := sliceMap[resolved.ModelID]
		if !ok {
			slice = &model.ConversationModelSlice{
				ModelID:          resolved.ModelID,
				ModelDisplayName: resolved.DisplayName,
				APIProvider:      resolved.APIProvider,
			}
			sliceMap[resolved.ModelID] = slice
		}

		if hasExact {
			turn.IsExact = true
			turn.MessageID = usage.MessageID
			turn.Exact = model.ExactTokenTotals{
				InputTokens:      usage.InputTokens,
				OutputTokens:     usage.OutputTokens,
				ThinkingTokens:   usage.ThinkingTokens,
				CacheReadTokens:  usage.CacheReadTokens,
				CacheWriteTokens: usage.CacheWriteTokens,
				TotalTokens:      usage.TotalTokens,
			}
			slice.TurnCount++
			slice.Exact.Add(turn.Exact)
		} else {
			estOut := estimateTokensFromPayloadLen(payloadLen)
			turn.Estimated = model.EstimatedTokenTotals{
				OutputTokens:       estOut,
				TotalTokens:        estOut,
				EstimatedTurnCount: 1,
			}
			slice.TurnCount++
			slice.EstimatedTotalTokens += estOut
		}

		turns = append(turns, turn)
	}

	breakdown := make([]model.ConversationModelSlice, 0, len(sliceMap))
	for _, sl := range sliceMap {
		breakdown = append(breakdown, *sl)
	}
	sort.Slice(breakdown, func(i, j int) bool {
		ti := breakdown[i].Exact.TotalTokens + breakdown[i].EstimatedTotalTokens
		tj := breakdown[j].Exact.TotalTokens + breakdown[j].EstimatedTotalTokens
		if ti != tj {
			return ti > tj
		}
		return breakdown[i].ModelID < breakdown[j].ModelID
	})

	return &model.ConversationDetail{
		Summary:        summary,
		ModelBreakdown: breakdown,
		Turns:          turns,
	}, nil
}

func decodeWorkspace(rawURIs string) (string, string) {
	trimmed := strings.TrimSpace(rawURIs)
	if trimmed == "" {
		return "", unassignedWSName
	}
	var uri string
	if strings.HasPrefix(trimmed, "[") {
		var list []string
		if err := json.Unmarshal([]byte(trimmed), &list); err == nil && len(list) > 0 {
			uri = strings.TrimSpace(list[0])
		}
	} else {
		uri = strings.Split(trimmed, ",")[0]
		uri = strings.Trim(strings.TrimSpace(uri), `"'[]`)
	}
	if uri == "" {
		return "", unassignedWSName
	}
	path := uri
	if strings.HasPrefix(strings.ToLower(uri), "file://") {
		if u, err := url.Parse(uri); err == nil {
			if unesc, err := url.PathUnescape(u.Path); err == nil && unesc != "" {
				path = unesc
			}
		}
	}
	base := filepath.Base(strings.TrimRight(path, "/\\"))
	if base == "" || base == "." || base == "/" {
		return path, unassignedWSName
	}
	return path, base
}

func parseFlexibleTimestampMs(raw string) int64 {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if num, err := strconv.ParseInt(s, 10, 64); err == nil {
		if num > 1_000_000_000_000 {
			return num
		}
		if num > 1_000_000_000 {
			return num * 1000
		}
	}
	tzLayouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, layout := range tzLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UnixMilli()
		}
	}
	localLayouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func truncateText(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}
