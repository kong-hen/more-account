package tokenusage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestStepType23AndDynamicEnumFallback(t *testing.T) {
	// 1. 验证新增真实高频模型枚举、大小写归一及 Argon 系列统一映射
	cases := []struct {
		rawEnum  string
		resp     string
		wantID   string
		wantName string
	}{
		{"1535", "", "gemini-4-neon", "Gemini 4 Neon"},
		{"model_placeholder_m535", "", "gemini-4-neon", "Gemini 4 Neon"},
		{"1530", "", "gemini-4-hydrogen-tiered-high", "Gemini 4 Hydrogen Tiered (High)"},
		{"1531", "", "gemini-4-hydrogen-tiered-xhigh", "Gemini 4 Hydrogen Tiered (XHigh)"},
		{"1668", "", "gemini-pro-orchestrator-g1-pro", "Gemini Pro Orchestrator G1 Pro"},
		{"1418", "", "gemini-4-argon-high", "Gemini 4 Argon (High)"},
		{"1419", "gemini-4-argon-trial", "gemini-4-argon-high", "Gemini 4 Argon (High)"},
		{"1526", "", "gemini-4-hydrogen", "Gemini 4 Hydrogen"},
	}
	for _, tc := range cases {
		got := ResolveModelInfo(tc.rawEnum, tc.resp, "24")
		if got.ModelID != tc.wantID || got.DisplayName != tc.wantName {
			t.Fatalf("ResolveModelInfo(%q, %q) = (%q, %q), 期望 (%q, %q)",
				tc.rawEnum, tc.resp, got.ModelID, got.DisplayName, tc.wantID, tc.wantName)
		}
	}

	// 2. 构造包含 step_type=15（未知新枚举 1799 + gen_metadata）与 step_type=23（无 gen_metadata）的对话库
	dataDir := t.TempDir()
	cacheDir := t.TempDir()
	convDir := filepath.Join(dataDir, "conversations")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(convDir, "conv-s23.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE gen_metadata (idx INTEGER, data BLOB)`)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	var tsBuf []byte
	tsBuf = append(tsBuf, switcher.EncodeVarintField(1, uint64(now.Unix()))...)

	// step 1: step_type=15，未知枚举 1799，1000 input + 200 output
	var u1 []byte
	u1 = append(u1, switcher.EncodeVarintField(1, 1799)...)
	u1 = append(u1, switcher.EncodeVarintField(2, 1000)...)
	u1 = append(u1, switcher.EncodeVarintField(10, 200)...)
	u1 = append(u1, switcher.EncodeVarintField(6, 24)...)
	var m1 []byte
	m1 = append(m1, switcher.EncodeLenDelimField(1, tsBuf)...)
	m1 = append(m1, switcher.EncodeLenDelimField(9, u1)...)

	// gen_metadata 仅关联 step 1，携带 responseModel = "gemini-5-nova"
	var gmInner []byte
	gmInner = append(gmInner, switcher.EncodeVarintField(3, 1799)...)
	gmInner = append(gmInner, switcher.EncodeLenDelimField(19, []byte("gemini-5-nova"))...)
	var gmBlob []byte
	gmBlob = append(gmBlob, switcher.EncodeLenDelimField(1, gmInner)...)
	gmBlob = append(gmBlob, switcher.EncodeVarintField(2, 1)...)

	// step 2: step_type=23（上下文压缩/Checkpoint），同枚举 1799，无 gen_metadata，500000 cache_read + 5000 input + 1000 out + 500 think
	var u2 []byte
	u2 = append(u2, switcher.EncodeVarintField(1, 1799)...)
	u2 = append(u2, switcher.EncodeVarintField(2, 5000)...)
	u2 = append(u2, switcher.EncodeVarintField(5, 500000)...)
	u2 = append(u2, switcher.EncodeVarintField(9, 500)...)
	u2 = append(u2, switcher.EncodeVarintField(10, 1000)...)
	u2 = append(u2, switcher.EncodeVarintField(6, 24)...)
	var m2 []byte
	m2 = append(m2, switcher.EncodeLenDelimField(1, tsBuf)...)
	m2 = append(m2, switcher.EncodeLenDelimField(9, u2)...)

	// step 3: step_type=23 无遥测但有大 payload，不应生成估算 Token
	// step 4: step_type=15 无遥测但有 400B payload，应保留 100 估算 Token
	_, err = db.Exec(`INSERT INTO gen_metadata (idx, data) VALUES (0, ?)`, gmBlob)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO steps VALUES (1, 15, 3, ?, NULL)`, m1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO steps VALUES (2, 23, 3, ?, NULL)`, m2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO steps VALUES (3, 23, 3, ?, ?)`, switcher.EncodeLenDelimField(1, tsBuf), make([]byte, 4000))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO steps VALUES (4, 15, 8, ?, ?)`, switcher.EncodeLenDelimField(1, tsBuf), make([]byte, 400))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	svc := NewService(cacheDir, func() string { return dataDir })
	bundle, err := svc.Sync(false, model.TokenUsageQuery{Preset: "7d", Granularity: "day", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("Sync 失败: %v", err)
	}

	wantExact := uint64(1200 + 506500)
	if bundle.Overview.Exact.TotalTokens != wantExact {
		t.Fatalf("期望精确总 Token=%d（含 step_type=23），实际 %d", wantExact, bundle.Overview.Exact.TotalTokens)
	}
	if bundle.Overview.Estimated.TotalTokens != 100 || bundle.Overview.Estimated.EstimatedTurnCount != 1 {
		t.Fatalf("期望仅 step_type=15 触发估算(100 tokens, 1 turn)，实际 %+v", bundle.Overview.Estimated)
	}
	// step 2 复用同会话 gen_metadata 动态学到的 gemini-5-nova，不应分裂出 model-m799
	for _, m := range bundle.Overview.Models {
		if m.ModelID == "model-m799" {
			t.Fatalf("step_type=23 不应降级分裂为 model-m799: %+v", bundle.Overview.Models)
		}
	}
	if bundle.Overview.Models[0].ModelID != "gemini-5-nova" {
		t.Fatalf("期望主模型动态解析为 gemini-5-nova，实际 %+v", bundle.Overview.Models[0])
	}

	// 验证 GetConversationDetail 与总览一致包含 step_type=23 且不估算 step 3
	detail, err := svc.GetConversationDetail("conv-s23")
	if err != nil {
		t.Fatalf("GetConversationDetail 失败: %v", err)
	}
	if len(detail.Turns) != 3 {
		t.Fatalf("期望 3 条有效步（step 1/2/4），实际 %d: %+v", len(detail.Turns), detail.Turns)
	}
	if detail.Turns[1].StepIndex != 2 || detail.Turns[1].ModelID != "gemini-5-nova" || detail.Turns[1].Exact.CacheReadTokens != 500000 {
		t.Fatalf("step_type=23 详情异常: %+v", detail.Turns[1])
	}
}

func TestParseFlexibleTimestampMsAndDecodeWorkspace(t *testing.T) {
	// 1. 验证真实 conversation_summaries.db 的空格+小数秒+时区格式与 Go 零值过滤
	ms := parseFlexibleTimestampMs("2026-08-24 13:38:18.37897+00:00")
	wantMs := time.Date(2026, 8, 24, 13, 38, 18, 378970000, time.UTC).UnixMilli()
	if ms != wantMs {
		t.Fatalf("parseFlexibleTimestampMs 解析真实摘要时间失败: got=%d want=%d", ms, wantMs)
	}
	if ms2 := parseFlexibleTimestampMs("2026-08-24 13:38:18+00:00"); ms2 <= 0 {
		t.Fatalf("无小数秒的空格时区时间解析失败: got=%d", ms2)
	}
	if zeroMs := parseFlexibleTimestampMs("0001-01-01 00:00:00+00:00"); zeroMs != 0 {
		t.Fatalf("Go 零值时间应返回 0 而非负数: got=%d", zeroMs)
	}

	// 2. 验证 decodeWorkspace 不对 u.Path 二次反转义，且正确剥离 Windows file:///C:/ 前导斜杠
	p1, n1 := decodeWorkspace(`["file:///Users/dev/100%25_project"]`)
	if p1 != "/Users/dev/100%_project" || n1 != "100%_project" {
		t.Fatalf("含字面量百分号的工作区路径解码异常: path=%q name=%q", p1, n1)
	}
	p2, n2 := decodeWorkspace(`["file:///Users/dev/my%20app"]`)
	if p2 != "/Users/dev/my app" || n2 != "my app" {
		t.Fatalf("含空格的工作区路径解码异常: path=%q name=%q", p2, n2)
	}
	p3, n3 := decodeWorkspace(`["file:///C:/Users/dev/demo"]`)
	if p3 != "C:/Users/dev/demo" || n3 != "demo" {
		t.Fatalf("Windows file URI 解码异常: path=%q name=%q", p3, n3)
	}
}

func TestQueryForExportUnlimitedPagination(t *testing.T) {
	now := time.Now().Local()
	dayKey := now.Format("2006-01-02")
	nowRFC := now.UTC().Format(time.RFC3339)

	convs := make([]scannedConversation, 0, 135)
	for i := 0; i < 135; i++ {
		id := fmt.Sprintf("conv-%03d", i)
		convs = append(convs, scannedConversation{
			Summary: model.ConversationSummaryItem{
				ConversationID:          id,
				Title:                   id,
				DisplayTitle:            id,
				Kind:                    "main",
				WorkspaceName:           "repo",
				WorkspacePath:           "/Users/dev/repo",
				LastModifiedTime:        nowRFC,
				TurnCount:               1,
				ExactTurnCount:          1,
				PrimaryModelID:          "gemini-4-neon",
				PrimaryModelDisplayName: "Gemini 4 Neon",
				Models:                  []string{"gemini-4-neon"},
				Exact:                   model.ExactTokenTotals{InputTokens: 100, TotalTokens: 100},
			},
			LastModifiedMs: now.UnixMilli(),
			Buckets: []cachedModelBucket{
				{
					DayKey:           dayKey,
					ModelID:          "gemini-4-neon",
					ModelDisplayName: "Gemini 4 Neon",
					APIProvider:      "Google Gemini",
					ExactTurnCount:   1,
					Exact:            model.ExactTokenTotals{InputTokens: 100, TotalTokens: 100},
				},
			},
		})
	}

	svc := NewService(t.TempDir(), func() string { return "/tmp/export-test" })
	svc.snapshot = &scanSnapshot{
		DataDir:       "/tmp/export-test",
		Conversations: convs,
		LastSyncedAt:  time.Now(),
	}

	// 普通 Query 传入 PageSize=10000 仍安全回退为 20
	paged, err := svc.Query(model.TokenUsageQuery{Preset: "7d", Page: 1, PageSize: 10000})
	if err != nil {
		t.Fatalf("Query 失败: %v", err)
	}
	if len(paged.Conversations.Items) != 20 || paged.Conversations.Total != 135 {
		t.Fatalf("普通分页查询异常: len=%d total=%d", len(paged.Conversations.Items), paged.Conversations.Total)
	}

	// QueryForExport 不截断，完整返回全部 135 条
	exported, err := svc.QueryForExport(model.TokenUsageQuery{Preset: "7d", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("QueryForExport 失败: %v", err)
	}
	if len(exported.Conversations.Items) != 135 || exported.Conversations.Total != 135 || exported.Conversations.TotalPages != 1 {
		t.Fatalf("导出查询被意外截断: len=%d total=%d totalPages=%d",
			len(exported.Conversations.Items), exported.Conversations.Total, exported.Conversations.TotalPages)
	}
}

func TestImmutableWALReadOnlyNoSidecarAndCacheTimezoneInvalidation(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := t.TempDir()
	convDir := filepath.Join(dataDir, "conversations")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(convDir, "wal-quiet.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	var tsBuf, uBuf, mBuf []byte
	tsBuf = append(tsBuf, switcher.EncodeVarintField(1, uint64(now.Unix()))...)
	uBuf = append(uBuf, switcher.EncodeVarintField(1, 1535)...)
	uBuf = append(uBuf, switcher.EncodeVarintField(2, 500)...)
	uBuf = append(uBuf, switcher.EncodeVarintField(10, 100)...)
	mBuf = append(mBuf, switcher.EncodeLenDelimField(1, tsBuf)...)
	mBuf = append(mBuf, switcher.EncodeLenDelimField(9, uBuf)...)
	if _, err := db.Exec(`INSERT INTO steps VALUES (1, 15, 3, ?, NULL)`, mBuf); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// 模拟已正常关闭并静止超过 5 秒的 WAL 库（无 -wal / -shm）
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")
	oldTime := time.Now().Add(-30 * time.Second)
	if err := os.Chtimes(dbPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	svc := NewService(cacheDir, func() string { return dataDir })
	b1, err := svc.Sync(false, model.TokenUsageQuery{Preset: "7d"})
	if err != nil {
		t.Fatalf("Sync 静止 WAL 库失败: %v", err)
	}
	if b1.Overview.Exact.TotalTokens != 600 {
		t.Fatalf("期望 Token=600，实际 %d", b1.Overview.Exact.TotalTokens)
	}
	if _, err := os.Stat(dbPath + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("只读扫描静止 WAL 库不应创建 -wal 边车文件")
	}
	if _, err := os.Stat(dbPath + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("只读扫描静止 WAL 库不应创建 -shm 边车文件")
	}

	// 验证 statFileFingerprint 忽略 0 字节空 -wal 抖动，但对非空 -wal 敏感
	fpNoWal := statFileFingerprint(dbPath)
	if err := os.WriteFile(dbPath+"-wal", []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	fpEmptyWal := statFileFingerprint(dbPath)
	if fpEmptyWal != fpNoWal {
		t.Fatalf("0 字节空 -wal 不应改变文件指纹: before=%+v after=%+v", fpNoWal, fpEmptyWal)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("non-empty-wal-frame"), 0600); err != nil {
		t.Fatal(err)
	}
	fpActiveWal := statFileFingerprint(dbPath)
	if fpActiveWal == fpNoWal {
		t.Fatalf("非空 -wal 必须触发指纹变更")
	}
	_ = os.Remove(dbPath + "-wal")

	// 验证缺少 conversation_summaries.db 时，原地更新已有 .db 会变更 computeQuickDirMarker
	markerBefore := computeQuickDirMarker(dataDir)
	newerTime := oldTime.Add(5 * time.Second)
	if err := os.Chtimes(dbPath, newerTime, newerTime); err != nil {
		t.Fatal(err)
	}
	markerAfter := computeQuickDirMarker(dataDir)
	if markerAfter == markerBefore {
		t.Fatalf("无 conversation_summaries.db 目录下修改已有 .db 应更新 DirMarker")
	}
	// 还原时间以便测试时区缓存失效
	_ = os.Chtimes(dbPath, oldTime, oldTime)

	// 二次增量同步命中缓存
	b2, err := svc.Sync(false, model.TokenUsageQuery{Preset: "7d"})
	if err != nil {
		t.Fatal(err)
	}
	if b2.Overview.CachedFiles != 1 || b2.Overview.ScannedFiles != 0 {
		t.Fatalf("期望命中缓存(cached=1, scanned=0)，实际 cached=%d scanned=%d", b2.Overview.CachedFiles, b2.Overview.ScannedFiles)
	}

	// 模拟持久化缓存中记录的时区与当前本机时区不一致，应自动触发重算而不误用旧 dayKey
	cachePath := filepath.Join(cacheDir, cacheFileName)
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted persistedScanCache
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	cleanKey := filepath.Clean(dataDir)
	ent := persisted.ByDataDir[cleanKey]["wal-quiet"]
	ent.TimezoneKey = "FAKE_TZ:-43200"
	persisted.ByDataDir[cleanKey]["wal-quiet"] = ent
	updatedRaw, _ := json.Marshal(persisted)
	if err := os.WriteFile(cachePath, updatedRaw, 0600); err != nil {
		t.Fatal(err)
	}

	b3, err := svc.Sync(false, model.TokenUsageQuery{Preset: "7d"})
	if err != nil {
		t.Fatal(err)
	}
	if b3.Overview.ScannedFiles != 1 || b3.Overview.CachedFiles != 0 {
		t.Fatalf("跨时区缓存应自动失效并重扫(scanned=1, cached=0)，实际 scanned=%d cached=%d", b3.Overview.ScannedFiles, b3.Overview.CachedFiles)
	}
}

func TestAggregatorFilteredPrimaryModelWorkspacePathAndDetailBackfill(t *testing.T) {
	now := time.Now().Local()
	todayKey := now.Format("2006-01-02")
	oldKey := now.AddDate(0, 0, -3).Format("2006-01-02")
	nowRFC := now.UTC().Format(time.RFC3339)

	// 1. 验证时间/模型筛选后重算对话行主力模型，且 AvailableModelNames 仍保留全量模型名称
	snap := &scanSnapshot{
		DataDir: "/tmp/filter-test",
		Conversations: []scannedConversation{
			{
				Summary: model.ConversationSummaryItem{
					ConversationID:          "conv-mix",
					Title:                   "双模型对话",
					DisplayTitle:            "双模型对话",
					Kind:                    "main",
					WorkspaceName:           "app",
					WorkspacePath:           "/Users/dev/alpha/app",
					LastModifiedTime:        nowRFC,
					TurnCount:               2,
					ExactTurnCount:          2,
					PrimaryModelID:          "gemini-4-carbon-high",
					PrimaryModelDisplayName: "Gemini 4 Carbon (High)",
					Models:                  []string{"gemini-4-carbon-high", "gemini-4-neon"},
					Exact:                   model.ExactTokenTotals{InputTokens: 1200, TotalTokens: 1200},
				},
				LastModifiedMs: now.UnixMilli(),
				Buckets: []cachedModelBucket{
					{
						DayKey:           oldKey,
						ModelID:          "gemini-4-carbon-high",
						ModelDisplayName: "Gemini 4 Carbon (High)",
						APIProvider:      "Google Gemini",
						ExactTurnCount:   1,
						Exact:            model.ExactTokenTotals{InputTokens: 1000, TotalTokens: 1000},
					},
					{
						DayKey:           todayKey,
						ModelID:          "gemini-4-neon",
						ModelDisplayName: "Gemini 4 Neon",
						APIProvider:      "Google Gemini",
						ExactTurnCount:   1,
						Exact:            model.ExactTokenTotals{InputTokens: 200, TotalTokens: 200},
					},
				},
			},
			{
				Summary: model.ConversationSummaryItem{
					ConversationID:          "conv-beta",
					Title:                   "同名工作区对话",
					DisplayTitle:            "同名工作区对话",
					Kind:                    "swarm",
					WorkspaceName:           "app",
					WorkspacePath:           "/Users/dev/beta/app",
					LastModifiedTime:        nowRFC,
					TurnCount:               1,
					ExactTurnCount:          1,
					PrimaryModelID:          "gemini-4-neon",
					PrimaryModelDisplayName: "Gemini 4 Neon",
					Models:                  []string{"gemini-4-neon"},
					Exact:                   model.ExactTokenTotals{InputTokens: 300, TotalTokens: 300},
				},
				LastModifiedMs: now.UnixMilli(),
				Buckets: []cachedModelBucket{
					{
						DayKey:           todayKey,
						ModelID:          "gemini-4-neon",
						ModelDisplayName: "Gemini 4 Neon",
						APIProvider:      "Google Gemini",
						ExactTurnCount:   1,
						Exact:            model.ExactTokenTotals{InputTokens: 300, TotalTokens: 300},
					},
				},
			},
		},
		LastSyncedAt: time.Now(),
	}

	svc := NewService(t.TempDir(), func() string { return "/tmp/filter-test" })
	todayBundle := svc.buildBundle(snap, model.TokenUsageQuery{
		Preset:    "today",
		Workspace: "/Users/dev/alpha/app",
		Page:      1,
		PageSize:  20,
	})
	if len(todayBundle.Conversations.Items) != 1 {
		t.Fatalf("按 WorkspacePath 精确筛选应只返回 alpha 的 1 条对话，实际 %d", len(todayBundle.Conversations.Items))
	}
	gotItem := todayBundle.Conversations.Items[0]
	if gotItem.PrimaryModelID != "gemini-4-neon" || gotItem.PrimaryModelDisplayName != "Gemini 4 Neon" {
		t.Fatalf("筛选今日后主力模型应重算为 gemini-4-neon，实际 id=%q name=%q", gotItem.PrimaryModelID, gotItem.PrimaryModelDisplayName)
	}
	if len(gotItem.Models) != 1 || gotItem.Models[0] != "gemini-4-neon" {
		t.Fatalf("筛选今日后对话 Models 应仅含命中模型，实际 %+v", gotItem.Models)
	}
	// 全量模型名称映射应始终包含未命中今日窗口的 gemini-4-carbon-high
	if todayBundle.Overview.AvailableModelNames["gemini-4-carbon-high"] != "Gemini 4 Carbon (High)" {
		t.Fatalf("AvailableModelNames 应保留全量模型名称映射，实际 %+v", todayBundle.Overview.AvailableModelNames)
	}
	// 同名工作区下拉选项应按路径拆分为 2 项并附区分路径提示
	if len(todayBundle.Overview.AvailableWorkspaceOptions) != 2 {
		t.Fatalf("期望 2 个同名工作区区分选项，实际 %+v", todayBundle.Overview.AvailableWorkspaceOptions)
	}
	if !strings.Contains(todayBundle.Overview.AvailableWorkspaceOptions[0].Label, "alpha/app") ||
		!strings.Contains(todayBundle.Overview.AvailableWorkspaceOptions[1].Label, "beta/app") {
		t.Fatalf("同名工作区标签应附区分路径提示: %+v", todayBundle.Overview.AvailableWorkspaceOptions)
	}
	// 兼容旧按 WorkspaceName 筛选仍命中同名两路径
	legacyWSBundle := svc.buildBundle(snap, model.TokenUsageQuery{Preset: "today", Workspace: "app"})
	if len(legacyWSBundle.Conversations.Items) != 2 {
		t.Fatalf("兼容旧 WorkspaceName 筛选应返回 2 条，实际 %d", len(legacyWSBundle.Conversations.Items))
	}

	// 2. 验证 classifyConversationKind 对 main / subagent / swarm 的判定
	if k := classifyConversationKind(summaryRow{}); k != "main" {
		t.Fatalf("主会话应判定为 main，实际 %q", k)
	}
	if k := classifyConversationKind(summaryRow{AgentName: "self", ParentConversationID: "p1", NestingDepth: 1}); k != "subagent" {
		t.Fatalf("内置 self 子会话应判定为 subagent，实际 %q", k)
	}
	if k := classifyConversationKind(summaryRow{AgentName: "research", ParentConversationID: "p1", NestingDepth: 1}); k != "subagent" {
		t.Fatalf("内置 research 子会话应判定为 subagent，实际 %q", k)
	}
	if k := classifyConversationKind(summaryRow{AgentName: "token-fixer", ParentConversationID: "p1", NestingDepth: 1}); k != "swarm" {
		t.Fatalf("具名协作体子会话应判定为 swarm，实际 %q", k)
	}
	if k := classifyConversationKind(summaryRow{AgentName: "monitor", ParentConversationID: "p1", NestingDepth: 1}); k != "swarm" {
		t.Fatalf("monitor 协作体子会话应判定为 swarm，实际 %q", k)
	}

	// 3. 验证冷启动或切换 DataDir 后调用 GetConversationDetail 能校验 DataDir 并从 DB 回填完整 summary 且 Models 非 nil
	coldDir := t.TempDir()
	convDir := filepath.Join(coldDir, "conversations")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(convDir, "cold-1.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`)
	var tsBuf, uBuf, mBuf []byte
	tsBuf = append(tsBuf, switcher.EncodeVarintField(1, uint64(now.Unix()))...)
	uBuf = append(uBuf, switcher.EncodeVarintField(1, 1535)...)
	uBuf = append(uBuf, switcher.EncodeVarintField(2, 700)...)
	uBuf = append(uBuf, switcher.EncodeVarintField(10, 300)...)
	mBuf = append(mBuf, switcher.EncodeLenDelimField(1, tsBuf)...)
	mBuf = append(mBuf, switcher.EncodeLenDelimField(9, uBuf)...)
	_, _ = db.Exec(`INSERT INTO steps VALUES (1, 15, 3, ?, NULL)`, mBuf)
	_ = db.Close()

	// 故意放一个不同 DataDir 的陈旧快照（含同 ID 假标题），验证不会被串用
	coldSvc := NewService(t.TempDir(), func() string { return coldDir })
	coldSvc.snapshot = &scanSnapshot{
		DataDir: "/some/other/dir",
		Conversations: []scannedConversation{
			{Summary: model.ConversationSummaryItem{ConversationID: "cold-1", DisplayTitle: "旧目录污染标题"}},
		},
	}
	detail, err := coldSvc.GetConversationDetail("cold-1")
	if err != nil {
		t.Fatalf("冷启动 GetConversationDetail 失败: %v", err)
	}
	if detail.Summary.DisplayTitle == "旧目录污染标题" {
		t.Fatalf("GetConversationDetail 不应复用不同 DataDir 的旧快照摘要")
	}
	if detail.Summary.Exact.TotalTokens != 1000 || detail.Summary.TurnCount != 1 || detail.Summary.PrimaryModelID != "gemini-4-neon" {
		t.Fatalf("未命中快照时应从步骤明细回填 Summary 统计: %+v", detail.Summary)
	}
	rawJSON, _ := json.Marshal(detail)
	if strings.Contains(string(rawJSON), `"models":null`) {
		t.Fatalf("Summary.Models 不应序列化为 null: %s", string(rawJSON))
	}
}

func TestQueryConcurrentDoubleCheck(t *testing.T) {
	dataDir := t.TempDir()
	cacheDir := t.TempDir()
	convDir := filepath.Join(dataDir, "conversations")
	if err := os.MkdirAll(convDir, 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(convDir, "conc-1.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.Exec(`CREATE TABLE steps (idx INTEGER, step_type INTEGER, status INTEGER, metadata BLOB, step_payload BLOB)`)
	now := time.Now().UTC()
	var tsBuf, uBuf, mBuf []byte
	tsBuf = append(tsBuf, switcher.EncodeVarintField(1, uint64(now.Unix()))...)
	uBuf = append(uBuf, switcher.EncodeVarintField(1, 1535)...)
	uBuf = append(uBuf, switcher.EncodeVarintField(2, 400)...)
	mBuf = append(mBuf, switcher.EncodeLenDelimField(1, tsBuf)...)
	mBuf = append(mBuf, switcher.EncodeLenDelimField(9, uBuf)...)
	_, _ = db.Exec(`INSERT INTO steps VALUES (1, 15, 3, ?, NULL)`, mBuf)
	_ = db.Close()

	svc := NewService(cacheDir, func() string { return dataDir })
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := svc.Query(model.TokenUsageQuery{Preset: "7d"})
			if err != nil {
				errs <- err
				return
			}
			if b.Overview.Exact.TotalTokens != 400 {
				errs <- fmt.Errorf("期望 400 tokens，实际 %d", b.Overview.Exact.TotalTokens)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
