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
	cacheSchemaVersion = 3
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
	TimezoneKey    string              `json:"tz,omitempty"`
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
	cacheDir     string
	getCustomDir func() string
	scanMu       sync.Mutex
	snapMu       sync.RWMutex
	snapshot     *scanSnapshot
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

func currentTimezoneKey() string {
	now := time.Now().Local()
	name, offset := now.Zone()
	return fmt.Sprintf("%s:%d", name, offset)
}

func computeQuickDirMarker(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	sumDB := filepath.Join(dataDir, "conversation_summaries.db")
	sumWAL := filepath.Join(dataDir, "conversation_summaries.db-wal")
	convDir := filepath.Join(dataDir, "conversations")
	targets := []string{sumDB, sumWAL, convDir}
	var sb strings.Builder
	hasSummary := false
	for i, p := range targets {
		if st, err := os.Stat(p); err == nil {
			if i == 0 {
				hasSummary = true
			}
			fmt.Fprintf(&sb, "%d:%d;", st.ModTime().UnixNano(), st.Size())
		} else {
			sb.WriteString("0;")
		}
	}
	// 当扫描目录缺少 conversation_summaries.db（如仅含 conversations/ 的自定义目录）时，
	// 汇总 conversations/*.db 及其非空 -wal 的最新 mtime 与总大小，确保原地更新已有对话库也能触发自动保鲜。
	if !hasSummary {
		if entries, err := os.ReadDir(convDir); err == nil {
			var dbCount int
			var totalBytes int64
			var maxMtime int64
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
					continue
				}
				dbCount++
				p := filepath.Join(convDir, entry.Name())
				if st, err := os.Stat(p); err == nil {
					totalBytes += st.Size()
					if mt := st.ModTime().UnixNano(); mt > maxMtime {
						maxMtime = mt
					}
				}
				if st, err := os.Stat(p + "-wal"); err == nil && st.Size() > 0 {
					totalBytes += st.Size()
					if mt := st.ModTime().UnixNano(); mt > maxMtime {
						maxMtime = mt
					}
				}
			}
			fmt.Fprintf(&sb, "nosum:%d:%d:%d;", dbCount, totalBytes, maxMtime)
		}
	}
	return sb.String()
}

func isSnapshotFresh(snap *scanSnapshot, effectiveDir string) bool {
	if snap == nil || snap.DataDir != effectiveDir {
		return false
	}
	return time.Since(snap.LastSyncedAt) < 5*time.Second || snap.DirMarker == computeQuickDirMarker(effectiveDir)
}

func (s *Service) getSnapshotForQuery() *scanSnapshot {
	_, effectiveDir, _ := s.resolveDataDir()
	s.snapMu.RLock()
	snap := s.snapshot
	s.snapMu.RUnlock()

	if isSnapshotFresh(snap, effectiveDir) {
		return snap
	}

	// 进入 scanMu 后二次校验快照新鲜度（double-check），防止高并发首次访问或过期瞬间触发重复全量扫描。
	s.scanMu.Lock()
	defer s.scanMu.Unlock()

	_, effectiveDir, _ = s.resolveDataDir()
	s.snapMu.RLock()
	snap = s.snapshot
	s.snapMu.RUnlock()
	if isSnapshotFresh(snap, effectiveDir) {
		return snap
	}

	snap = s.runScan(effectiveDir, false)
	s.snapMu.Lock()
	s.snapshot = snap
	s.snapMu.Unlock()
	return snap
}

// Query 读取当前内存快照并按查询条件聚合；若尚无快照，或检测到本地会话摘要/目录发生变更（且距上次扫描超过 5 秒），自动执行增量保鲜扫描。
func (s *Service) Query(query model.TokenUsageQuery) (*model.TokenUsageBundle, error) {
	snap := s.getSnapshotForQuery()
	return s.buildBundle(snap, query), nil
}

// QueryForExport 按查询条件返回不截断分页上限的完整对话聚合结果，专供 CSV 等全量明细导出使用。
func (s *Service) QueryForExport(query model.TokenUsageQuery) (*model.TokenUsageBundle, error) {
	snap := s.getSnapshotForQuery()
	return s.buildBundleWithOptions(snap, query, true), nil
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

func isBuiltinSubagentName(agentLower string) bool {
	switch agentLower {
	case "", "self", "subagent", "research", "browser", "code_executor", "image-generator", "image_generator":
		return true
	default:
		return false
	}
}

func classifyConversationKind(sumRow summaryRow) string {
	agentLower := strings.ToLower(strings.TrimSpace(sumRow.AgentName))
	if strings.Contains(agentLower, "swarm") {
		return "swarm"
	}
	if sumRow.ParentConversationID != "" || sumRow.NestingDepth > 0 {
		// 具有自定义角色名（非内置通用 subagent 角色）的子会话归类为 Swarm 协作体
		if agentLower != "" && !isBuiltinSubagentName(agentLower) {
			return "swarm"
		}
		return "subagent"
	}
	return "main"
}

func (s *Service) runScan(dataDir string, rebuild bool) *scanSnapshot {
	started := time.Now()
	tzKey := currentTimezoneKey()
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
					// 同时核验文件指纹与本机时区标识，跨时区切换后自动重算 dayKey
					if prev, ok := dirEntries[task.convID]; ok && prev.Fingerprint == task.fp && prev.TimezoneKey == tzKey {
						cachedCount.Add(1)
						mu.Lock()
						nextCache[task.convID] = prev
						mu.Unlock()
						continue
					}
				}
				scannedCount.Add(1)
				parsed, err := scanSingleConversationDB(task.dbPath, task.fp, tzKey)
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

		kind := classifyConversationKind(sumRow)

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
	// 扫描结束后刷新 DirMarker 与 LastSyncedAt，避免扫描打开 WAL 库本身导致的目录/边车变化在 5 秒后触发冗余二次重扫。
	snap.DirMarker = computeQuickDirMarker(dataDir)
	snap.LastSyncedAt = time.Now()
	return snap
}

func statFileFingerprint(dbPath string) fileFingerprint {
	var fp fileFingerprint
	if st, err := os.Stat(dbPath); err == nil {
		fp.DBSize = st.Size()
		fp.DBMtimeMs = st.ModTime().UnixMilli()
	}
	// 忽略 0 字节空 -wal 的 mtime 抖动，仅当存在未 checkpoint 的有效 WAL 数据时纳入指纹比较。
	if st, err := os.Stat(dbPath + "-wal"); err == nil && st.Size() > 0 {
		fp.WALSize = st.Size()
		fp.WALMtimeMs = st.ModTime().UnixMilli()
	}
	return fp
}

func shouldOpenSQLiteImmutable(path string) bool {
	// 当 -wal 与 -shm 边车文件均不存在且主库已静止超过 5 秒时，采用 immutable=1 严格只读打开，
	// 避免 SQLite 对已正常关闭的 WAL 模式数据库反向创建并遗留 0B -wal 与 32KB -shm 边车文件。
	if _, err := os.Stat(path + "-wal"); err == nil {
		return false
	} else if !os.IsNotExist(err) {
		return false
	}
	if _, err := os.Stat(path + "-shm"); err == nil {
		return false
	} else if !os.IsNotExist(err) {
		return false
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(st.ModTime()) > 5*time.Second
}

func openSQLiteReadOnly(path string) (*sql.DB, error) {
	// 严格只读模式打开，使用安全转义的 file: URI；静止无边车 WAL 库采用 immutable=1 防止写副作用，其余启用 busy_timeout。
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
	if shouldOpenSQLiteImmutable(path) {
		q.Set("immutable", "1")
	} else {
		q.Add("_pragma", "busy_timeout(1500)")
	}
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

func readSingleConversationSummary(dbPath, conversationID string) (summaryRow, bool) {
	if _, err := os.Stat(dbPath); err != nil {
		return summaryRow{}, false
	}
	db, err := openSQLiteReadOnly(dbPath)
	if err != nil {
		return summaryRow{}, false
	}
	defer db.Close()

	var cid, title, preview, lastMod, ws, agent, parent sql.NullString
	var steps, depth sql.NullInt64
	fullSQL := `SELECT conversation_id, title, preview, step_count, last_modified_time,
		workspace_uris, agent_name, parent_conversation_id, nesting_depth
		FROM conversation_summaries WHERE conversation_id = ? LIMIT 1`
	if err := db.QueryRow(fullSQL, conversationID).Scan(&cid, &title, &preview, &steps, &lastMod, &ws, &agent, &parent, &depth); err == nil {
		id := strings.TrimSpace(cid.String)
		if id == "" {
			return summaryRow{}, false
		}
		return summaryRow{
			ConversationID:       id,
			Title:                title.String,
			Preview:              preview.String,
			StepCount:            int(steps.Int64),
			LastModifiedMs:       parseFlexibleTimestampMs(lastMod.String),
			WorkspaceURIs:        ws.String,
			AgentName:            strings.TrimSpace(agent.String),
			ParentConversationID: strings.TrimSpace(parent.String),
			NestingDepth:         int(depth.Int64),
		}, true
	}

	minSQL := `SELECT conversation_id, title, preview, step_count, last_modified_time, workspace_uris
		FROM conversation_summaries WHERE conversation_id = ? LIMIT 1`
	if err := db.QueryRow(minSQL, conversationID).Scan(&cid, &title, &preview, &steps, &lastMod, &ws); err == nil {
		id := strings.TrimSpace(cid.String)
		if id == "" {
			return summaryRow{}, false
		}
		return summaryRow{
			ConversationID: id,
			Title:          title.String,
			Preview:        preview.String,
			StepCount:      int(steps.Int64),
			LastModifiedMs: parseFlexibleTimestampMs(lastMod.String),
			WorkspaceURIs:  ws.String,
		}, true
	}
	return summaryRow{}, false
}

// readGenMetadataMaps 读取单会话的 gen_metadata，返回按 step_index 关联表及会话级 enum -> responseModel 动态兜底映射表。
func readGenMetadataMaps(db *sql.DB) (map[int]ParsedGenMetadata, map[string]string) {
	genByStep := make(map[int]ParsedGenMetadata)
	enumToRespModel := make(map[string]string)
	genRows, err := db.Query(`SELECT data FROM gen_metadata`)
	if err != nil {
		return genByStep, enumToRespModel
	}
	defer genRows.Close()

	for genRows.Next() {
		var blob []byte
		if err := genRows.Scan(&blob); err != nil || len(blob) == 0 {
			continue
		}
		gm := ParseGenMetadata(blob)
		for _, idx := range gm.StepIndices {
			genByStep[idx] = gm
		}
		resp := strings.TrimSpace(gm.ResponseModel)
		if resp == "" || isPlaceholderOrNumeric(resp) {
			continue
		}
		if gm.Usage != nil && gm.Usage.ModelEnum > 0 {
			enumToRespModel[formatEnumKey(gm.Usage.ModelEnum)] = resp
		}
		if gm.ModelEnumNumeric > 0 {
			enumToRespModel[formatEnumKey(gm.ModelEnumNumeric)] = resp
		}
		if ph := strings.ToUpper(strings.TrimSpace(gm.ModelEnumPlaceholder)); ph != "" {
			enumToRespModel[ph] = resp
		}
	}
	return genByStep, enumToRespModel
}

func resolveStepModel(
	stepMeta ParsedStepMetadata,
	gm ParsedGenMetadata,
	hasGM bool,
	usage *ParsedUsageProto,
	enumToRespModel map[string]string,
) ResolvedModelInfo {
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
	// 当当前步骤缺少 gen_metadata（如 step_type=23 或无 responseModel）时，复用同会话动态学到的 enum -> responseModel 兜底映射。
	if strings.TrimSpace(respModel) == "" && rawEnum != "" {
		if dynamicResp, ok := enumToRespModel[strings.ToUpper(strings.TrimSpace(rawEnum))]; ok {
			respModel = dynamicResp
		}
	}
	return ResolveModelInfo(rawEnum, respModel, rawProvider)
}

func scanSingleConversationDB(dbPath string, fp fileFingerprint, tzKey string) (cachedConvEntry, error) {
	db, err := openSQLiteReadOnly(dbPath)
	if err != nil {
		return cachedConvEntry{}, err
	}
	defer db.Close()

	entry := cachedConvEntry{
		Fingerprint: fp,
		TimezoneKey: tzKey,
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

	// 读取 gen_metadata 按 step_index 建立关联并学习会话内 enum -> responseModel 兜底表
	genByStep, enumToRespModel := readGenMetadataMaps(db)

	// 聚合每个 (dayKey, modelID) 桶
	type bucketKey struct {
		dayKey  string
		modelID string
	}
	bucketMap := make(map[bucketKey]*cachedModelBucket)

	// 同时纳入 step_type=15（主模型回复）与 step_type=23（上下文压缩/Checkpoint 真实 LLM 调用）
	rows, err := db.Query(`SELECT idx, step_type, metadata, COALESCE(LENGTH(step_payload), 0) FROM steps WHERE step_type IN (15, 23) ORDER BY idx ASC`)
	if err != nil {
		// 若表结构不同，尝试只读 metadata
		rows, err = db.Query(`SELECT idx, step_type, metadata, 0 FROM steps WHERE step_type IN (15, 23) ORDER BY idx ASC`)
		if err != nil {
			return entry, err
		}
	}
	defer rows.Close()

	for rows.Next() {
		var idx, stepType int
		var metaBlob []byte
		var payloadLen int64
		if err := rows.Scan(&idx, &stepType, &metaBlob, &payloadLen); err != nil {
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

		// 仅对 step_type=15 保留缺失精确遥测时的 step_payload 启发式估算；step_type=23 仅统计精确遥测，避免误估非回复载荷。
		hasExact := usage != nil && usage.TotalTokens > 0
		hasEst := !hasExact && stepType == 15 && payloadLen > 0
		if !hasExact && !hasEst {
			continue
		}

		resolved := resolveStepModel(stepMeta, gm, hasGM, usage, enumToRespModel)

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
	foundInSnap := false
	s.snapMu.RLock()
	// 严格校验快照 DataDir 与当前有效目录一致，防止切换自定义目录后串读旧目录摘要
	if s.snapshot != nil && filepath.Clean(s.snapshot.DataDir) == filepath.Clean(effectiveDir) {
		for _, c := range s.snapshot.Conversations {
			if c.Summary.ConversationID == conversationID {
				summary = c.Summary
				foundInSnap = true
				break
			}
		}
	}
	s.snapMu.RUnlock()

	if !foundInSnap {
		summary = model.ConversationSummaryItem{
			ConversationID: conversationID,
			DisplayTitle:   conversationID,
			Kind:           "main",
			WorkspaceName:  unassignedWSName,
			Models:         []string{},
			DataSource:     "sqlite_db",
		}
		if sumRow, ok := readSingleConversationSummary(filepath.Join(effectiveDir, "conversation_summaries.db"), conversationID); ok {
			wsPath, wsName := decodeWorkspace(sumRow.WorkspaceURIs)
			title := strings.TrimSpace(sumRow.Title)
			displayTitle := title
			if displayTitle == "" {
				if prev := strings.TrimSpace(sumRow.Preview); prev != "" {
					displayTitle = truncateText(prev, 60)
				} else {
					displayTitle = conversationID
				}
			}
			summary.Title = title
			summary.DisplayTitle = displayTitle
			summary.Preview = truncateText(strings.TrimSpace(sumRow.Preview), 120)
			summary.Kind = classifyConversationKind(sumRow)
			summary.AgentName = sumRow.AgentName
			summary.ParentConversationID = sumRow.ParentConversationID
			summary.WorkspacePath = wsPath
			summary.WorkspaceName = wsName
			summary.StepCount = sumRow.StepCount
			if sumRow.LastModifiedMs > 0 {
				summary.LastModifiedTime = formatUTCMs(sumRow.LastModifiedMs)
			}
		}
	}
	if summary.Models == nil {
		summary.Models = []string{}
	}

	db, err := openSQLiteReadOnly(dbPath)
	if err != nil {
		return nil, fmt.Errorf("只读打开对话库失败: %w", err)
	}
	defer db.Close()

	genByStep, enumToRespModel := readGenMetadataMaps(db)

	rows, err := db.Query(`SELECT idx, step_type, status, metadata, COALESCE(LENGTH(step_payload), 0) FROM steps WHERE step_type IN (15, 23) ORDER BY idx ASC`)
	if err != nil {
		rows, err = db.Query(`SELECT idx, step_type, 0, metadata, 0 FROM steps WHERE step_type IN (15, 23) ORDER BY idx ASC`)
		if err != nil {
			return nil, fmt.Errorf("查询对话步骤失败: %w", err)
		}
	}
	defer rows.Close()

	turns := make([]model.TurnUsageRecord, 0)
	sliceMap := make(map[string]*model.ConversationModelSlice)
	var (
		parsedExact      model.ExactTokenTotals
		parsedEst        model.EstimatedTokenTotals
		parsedExactTurns int
		firstTurnMs      int64
		lastTurnMs       int64
	)

	for rows.Next() {
		var idx, stepType, status int
		var metaBlob []byte
		var payloadLen int64
		if err := rows.Scan(&idx, &stepType, &status, &metaBlob, &payloadLen); err != nil {
			continue
		}
		stepMeta := ParseStepMetadata(metaBlob)
		if stepMeta.TimestampMs > 0 {
			if firstTurnMs == 0 || stepMeta.TimestampMs < firstTurnMs {
				firstTurnMs = stepMeta.TimestampMs
			}
			if stepMeta.TimestampMs > lastTurnMs {
				lastTurnMs = stepMeta.TimestampMs
			}
		}
		gm, hasGM := genByStep[idx]
		usage := stepMeta.Usage
		if usage == nil && hasGM {
			usage = gm.Usage
		}

		hasExact := usage != nil && usage.TotalTokens > 0
		hasEst := !hasExact && stepType == 15 && payloadLen > 0
		if !hasExact && !hasEst {
			continue
		}

		resolved := resolveStepModel(stepMeta, gm, hasGM, usage, enumToRespModel)

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
			parsedExact.Add(turn.Exact)
			parsedExactTurns++
		} else {
			estOut := estimateTokensFromPayloadLen(payloadLen)
			turn.Estimated = model.EstimatedTokenTotals{
				OutputTokens:       estOut,
				TotalTokens:        estOut,
				EstimatedTurnCount: 1,
			}
			slice.TurnCount++
			slice.EstimatedTotalTokens += estOut
			parsedEst.OutputTokens += estOut
			parsedEst.TotalTokens += estOut
			parsedEst.EstimatedTurnCount++
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

	// 未命中内存快照时，按本次解析出的真实步骤与模型切片回填 summary 统计，避免返回全零摘要与 nil Models
	if !foundInSnap {
		var maxStep sql.NullInt64
		if err := db.QueryRow(`SELECT IFNULL(MAX(idx) + 1, 0) FROM steps`).Scan(&maxStep); err == nil && int(maxStep.Int64) > summary.StepCount {
			summary.StepCount = int(maxStep.Int64)
		}
		var firstMeta []byte
		if err := db.QueryRow(`SELECT metadata FROM steps WHERE idx = 0`).Scan(&firstMeta); err == nil && len(firstMeta) > 0 {
			if ts := ParseStepMetadata(firstMeta).TimestampMs; ts > 0 {
				summary.CreatedAt = formatUTCMs(ts)
			}
		}
		if summary.CreatedAt == "" && firstTurnMs > 0 {
			summary.CreatedAt = formatUTCMs(firstTurnMs)
		}
		if lastTurnMs > 0 {
			if summary.LastModifiedTime == "" || formatUTCMs(lastTurnMs) > summary.LastModifiedTime {
				summary.LastModifiedTime = formatUTCMs(lastTurnMs)
			}
		}
		modelsList := make([]string, 0, len(breakdown))
		for _, sl := range breakdown {
			modelsList = append(modelsList, sl.ModelID)
		}
		sort.Strings(modelsList)
		summary.Models = modelsList
		if len(breakdown) > 0 {
			summary.PrimaryModelID = breakdown[0].ModelID
			summary.PrimaryModelDisplayName = breakdown[0].ModelDisplayName
		} else {
			summary.PrimaryModelID = "unknown"
			summary.PrimaryModelDisplayName = FormatModelDisplayName("unknown")
		}
		summary.Exact = parsedExact
		summary.Estimated = parsedEst
		summary.ExactTurnCount = parsedExactTurns
		summary.EstimatedTurnCount = parsedEst.EstimatedTurnCount
		summary.TurnCount = parsedExactTurns + parsedEst.EstimatedTurnCount
	}

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
		if u, err := url.Parse(uri); err == nil && u.Path != "" {
			// url.Parse 已完成百分比解码，不可再次调用 url.PathUnescape 避免含字面量 % 的目录名被二次反转义
			p := u.Path
			// 兼容 Windows file:///C:/... 格式，剥离盘符前多余斜杠
			if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
				p = p[1:]
			}
			path = p
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
		if num <= 0 {
			return 0
		}
		if num > 1_000_000_000_000 {
			return num
		}
		if num > 1_000_000_000 {
			return num * 1000
		}
		return 0
	}
	tzLayouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999-0700",
		"2006-01-02 15:04:05-0700",
		"2006-01-02 15:04:05.999999999 -0700 MST",
	}
	for _, layout := range tzLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			ms := t.UnixMilli()
			if ms <= 0 {
				return 0
			}
			return ms
		}
	}
	localLayouts := []string{
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
	}
	for _, layout := range localLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			ms := t.UnixMilli()
			if ms <= 0 {
				return 0
			}
			return ms
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
