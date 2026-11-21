package tokenusage

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"more-account/internal/model"
	"more-account/internal/switcher"
)

func TestParseUsageProtoZeroDoubleCounting(t *testing.T) {
	// 构造 Usage Protobuf:
	// f1=1427 (gemini-4-carbon-high), f2=1000 (input), f3=500 (combined output),
	// f4=200 (cache_write), f5=300 (cache_read), f9=350 (thinking)
	// 不提供 f10 时，纯 output 应自动扣减 thinking -> 500 - 350 = 150
	var buf []byte
	buf = append(buf, switcher.EncodeVarintField(1, 1427)...)
	buf = append(buf, switcher.EncodeVarintField(2, 1000)...)
	buf = append(buf, switcher.EncodeVarintField(3, 500)...)
	buf = append(buf, switcher.EncodeVarintField(4, 200)...)
	buf = append(buf, switcher.EncodeVarintField(5, 300)...)
	buf = append(buf, switcher.EncodeVarintField(9, 350)...)

	usage, ok := ParseUsageProto(buf)
	if !ok {
		t.Fatal("解析 Usage Protobuf 失败")
	}
	if usage.InputTokens != 1000 || usage.OutputTokens != 150 || usage.ThinkingTokens != 350 {
		t.Fatalf("Thinking 零双计校验失败: %+v", usage)
	}
	wantTotal := uint64(1000 + 150 + 350 + 300 + 200)
	if usage.TotalTokens != wantTotal {
		t.Fatalf("期望 TotalTokens=%d，实际 %d", wantTotal, usage.TotalTokens)
	}

	// 同时提供 f10=180 时优先采用 f10，且不重复计入 thinking
	buf2 := append(append([]byte(nil), buf...), switcher.EncodeVarintField(10, 180)...)
	usage2, ok := ParseUsageProto(buf2)
	if !ok || usage2.OutputTokens != 180 || usage2.TotalTokens != 1000+180+350+300+200 {
		t.Fatalf("显式 f10 校验失败: %+v", usage2)
	}
}

func TestScannerAndAggregatorEndToEnd(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := t.TempDir()
	convDir := filepath.Join(dataDir, "conversations")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	nowRFC := now.Format(time.RFC3339)

	// 1. 构造模拟 conversation_summaries.db
	sumDB, err := sql.Open("sqlite", filepath.Join(dataDir, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = sumDB.Exec(`CREATE TABLE conversation_summaries (
		conversation_id TEXT,
		title TEXT,
		preview TEXT,
		step_count INTEGER,
		last_modified_time TEXT,
		workspace_uris TEXT,
		status TEXT,
		project_id TEXT,
		agent_name TEXT,
		parent_conversation_id TEXT,
		nesting_depth INTEGER
	)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sumDB.Exec(`INSERT INTO conversation_summaries VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"conv-1", "重构账号管理架构", "设计三栏工作台", 4, nowRFC, `["file:///Users/dev/more-account"]`, "DONE", "proj-1", "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = sumDB.Close()

	// 2. 构造模拟 conversations/conv-1.db
	cDB, err := sql.Open("sqlite", filepath.Join(convDir, "conv-1.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cDB.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`)
	if err != nil {
		t.Fatal(err)
	}

	var tsBuf []byte
	tsBuf = append(tsBuf, switcher.EncodeVarintField(1, uint64(now.Unix()))...)
	var usageBuf []byte
	usageBuf = append(usageBuf, switcher.EncodeVarintField(1, 1402)...) // claude-opus-5-5-high
	usageBuf = append(usageBuf, switcher.EncodeVarintField(2, 2000)...)
	usageBuf = append(usageBuf, switcher.EncodeVarintField(9, 400)...)
	usageBuf = append(usageBuf, switcher.EncodeVarintField(10, 600)...)
	usageBuf = append(usageBuf, switcher.EncodeVarintField(5, 500)...)

	var metaBuf []byte
	metaBuf = append(metaBuf, switcher.EncodeLenDelimField(1, tsBuf)...)
	metaBuf = append(metaBuf, switcher.EncodeLenDelimField(9, usageBuf)...)

	_, err = cDB.Exec(`INSERT INTO steps (idx, step_type, status, metadata, step_payload) VALUES (1, 15, 1, ?, NULL)`, metaBuf)
	if err != nil {
		t.Fatal(err)
	}
	_ = cDB.Close()

	svc := NewService(cacheDir, func() string { return dataDir })
	bundle, err := svc.Sync(false, model.TokenUsageQuery{
		Preset:      "7d",
		Granularity: "day",
		Page:        1,
		PageSize:    20,
	})
	if err != nil {
		t.Fatalf("扫描 Token 用量失败: %v", err)
	}

	if bundle.Overview.Exact.TotalTokens != 3500 {
		t.Fatalf("期望精确总 Token=3500，实际 %d", bundle.Overview.Exact.TotalTokens)
	}
	if bundle.Overview.ConversationCount != 1 || len(bundle.Conversations.Items) != 1 {
		t.Fatalf("期望 1 条对话，实际 count=%d items=%d", bundle.Overview.ConversationCount, len(bundle.Conversations.Items))
	}
	if bundle.Conversations.Items[0].WorkspaceName != "more-account" {
		t.Fatalf("期望工作区名 more-account，实际 %q", bundle.Conversations.Items[0].WorkspaceName)
	}

	// 二次增量同步应命中文件指纹缓存
	bundle2, err := svc.Sync(false, model.TokenUsageQuery{Preset: "7d", Granularity: "day", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("二次同步失败: %v", err)
	}
	if bundle2.Overview.CachedFiles < 1 {
		t.Fatalf("期望二次增量同步命中缓存，实际 CachedFiles=%d", bundle2.Overview.CachedFiles)
	}

	// 校验单对话步级详情懒加载
	detail, err := svc.GetConversationDetail("conv-1")
	if err != nil {
		t.Fatalf("获取对话详情失败: %v", err)
	}
	if len(detail.Turns) != 1 || detail.Turns[0].Exact.ThinkingTokens != 400 {
		t.Fatalf("对话步级详情异常: %+v", detail.Turns)
	}
}

func TestGetConversationDetailBlocksPathTraversal(t *testing.T) {
	rootDir := t.TempDir()
	dataDir := filepath.Join(rootDir, "data")
	convDir := filepath.Join(dataDir, "conversations")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}
	// 在 dataDir 根目录放置一个合法 SQLite 文件，验证路径穿越无法读取
	secretDBPath := filepath.Join(dataDir, "secret.db")
	secretDB, err := sql.Open("sqlite", secretDBPath)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = secretDB.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`)
	_ = secretDB.Close()

	svc := NewService(t.TempDir(), func() string { return dataDir })
	badIDs := []string{
		"",
		".",
		"..",
		"../secret",
		"..\\secret",
		"sub/conv-1",
		"sub\\conv-1",
		"/etc/passwd",
	}
	for _, id := range badIDs {
		if _, err := svc.GetConversationDetail(id); err == nil {
			t.Fatalf("危险 conversationID %q 应当被拦截", id)
		}
	}
}

func TestAggregatorStrictWeakOrderingWorkspaceAndCacheIsolation(t *testing.T) {
	now := time.Now().Local()
	dayKey := now.Format("2006-01-02")
	nowRFC := now.Format(time.RFC3339)

	// 1. 验证相同数值的降序排序不触发 panic，且同 basename 不同路径的工作区不被合并
	makeConv := func(id, title, wsPath string) scannedConversation {
		return scannedConversation{
			Summary: model.ConversationSummaryItem{
				ConversationID:   id,
				Title:            title,
				DisplayTitle:     title,
				WorkspaceName:    "app",
				WorkspacePath:    wsPath,
				LastModifiedTime: nowRFC,
				StepCount:        2,
				TurnCount:        1,
				ExactTurnCount:   1,
				Models:           []string{"claude-opus-5-5-high"},
				Exact:            model.ExactTokenTotals{InputTokens: 100, TotalTokens: 100},
			},
			LastModifiedMs: now.UnixMilli(),
			Buckets: []cachedModelBucket{
				{
					DayKey:           dayKey,
					ModelID:          "claude-opus-5-5-high",
					ModelDisplayName: "Claude Opus 5.5 (High)",
					APIProvider:      "Anthropic",
					ExactTurnCount:   1,
					Exact:            model.ExactTokenTotals{InputTokens: 100, TotalTokens: 100},
				},
			},
		}
	}

	snap := &scanSnapshot{
		DataDir: "/tmp/data",
		Conversations: []scannedConversation{
			makeConv("conv-b", "B", "/Users/dev/alpha/app"),
			makeConv("conv-a", "A", "/Users/dev/beta/app"),
			makeConv("conv-c", "C", "/Users/dev/alpha/app"),
		},
		LastSyncedAt: now,
	}

	aggSvc := NewService(t.TempDir(), func() string { return "/tmp/data" })
	for _, sortBy := range []string{"lastModifiedTime", "totalTokens", "turnCount"} {
		bundle := aggSvc.buildBundle(snap, model.TokenUsageQuery{
			Preset:   "7d",
			Sort:     sortBy,
			Order:    "desc",
			Page:     1,
			PageSize: 10,
		})
		if len(bundle.Conversations.Items) != 3 {
			t.Fatalf("sort=%s 期望 3 条对话，实际 %d", sortBy, len(bundle.Conversations.Items))
		}
		if len(bundle.Overview.Workspaces) != 2 {
			t.Fatalf("不同路径的同名工作区应分开统计（2 个），实际 %d: %+v", len(bundle.Overview.Workspaces), bundle.Overview.Workspaces)
		}
	}

	// 2. 验证零用量步不产生幽灵桶/模型切片，且切换 dataDir 时持久化缓存按目录严格隔离
	cacheDir := t.TempDir()
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir1, "conversations"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir2, "conversations"), 0755); err != nil {
		t.Fatal(err)
	}

	// dir1 写入一个纯空步骤对话（无任何 exact/estimated 用量）
	db1, err := sql.Open("sqlite", filepath.Join(dir1, "conversations", "zero-conv.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db1.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`)
	_, _ = db1.Exec(`INSERT INTO steps VALUES (1, 15, 1, NULL, NULL)`)
	_ = db1.Close()

	activeDir := dir1
	svc := NewService(cacheDir, func() string { return activeDir })
	b1, err := svc.Sync(false, model.TokenUsageQuery{Preset: "7d", Granularity: "day", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("扫描 dir1 失败: %v", err)
	}
	if len(b1.Overview.Models) != 0 {
		t.Fatalf("零用量步骤不应生成幽灵模型切片: %+v", b1.Overview.Models)
	}
	for _, pt := range b1.Overview.Trend {
		if pt.Exact.TotalTokens != 0 || pt.EstimatedTotalTokens != 0 {
			t.Fatalf("零用量步骤不应在时间序列产生非零桶: %+v", pt)
		}
	}

	// 验证零有效步骤对话的 GetConversationDetail 始终序列化为空数组 [] 而非 null
	zeroDetail, err := svc.GetConversationDetail("zero-conv")
	if err != nil {
		t.Fatalf("读取 zero-conv 详情失败: %v", err)
	}
	rawDetailJSON, err := json.Marshal(zeroDetail)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawDetailJSON), `"turns":null`) {
		t.Fatalf("ConversationDetail.Turns 绝不允许序列化为 null: %s", string(rawDetailJSON))
	}

	// 切换到空目录 dir2，重新创建 Service 读取磁盘缓存，不应串联出 dir1 的对话
	activeDir = dir2
	svc2 := NewService(cacheDir, func() string { return activeDir })
	b2, err := svc2.Sync(false, model.TokenUsageQuery{Preset: "7d", Granularity: "day", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("扫描 dir2 失败: %v", err)
	}
	if b2.Overview.ConversationCount != 0 {
		t.Fatalf("切换 dataDir 后不应复用旧目录缓存对话: count=%d", b2.Overview.ConversationCount)
	}
}
