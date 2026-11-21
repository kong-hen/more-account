package switcher

import (
	"database/sql"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"more-account/internal/model"
)

func TestProtobufUnifiedOAuthRoundTrip(t *testing.T) {
	isGcp := true
	oauthInfo := CreateOAuthTokenInfo("acc-123", "ref-456", 1800000000, &isGcp, "id-tok-789", "dev@corp.example")
	topic := CreateUnifiedTopicEntry("otherKey", []byte("keep-me"))
	topic = append(topic, CreateUnifiedTopicEntry("oauthTokenInfoSentinelKey", oauthInfo)...)

	_, gotRT, _ := ExtractOAuthInfoFromUnifiedOAuth(topic)
	if gotRT != "ref-456" {
		t.Fatalf("期望提取 refresh_token=ref-456，实际 %q", gotRT)
	}

	// 替换 oauthTokenInfoSentinelKey 时必须保留其他 sentinel 条目
	cleaned, err := RemoveUnifiedTopicEntry(topic, "oauthTokenInfoSentinelKey")
	if err != nil {
		t.Fatalf("移除旧 sentinel 失败: %v", err)
	}
	if _, rtAfter, _ := ExtractOAuthInfoFromUnifiedOAuth(cleaned); rtAfter != "" {
		t.Fatal("移除后不应再提取到旧 refresh_token")
	}
	if len(cleaned) == 0 {
		t.Fatal("其他 sentinel 条目不应被误删")
	}
}

func TestInjectAndReadStateDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	acc := model.Account{
		ID:    "a1",
		Email: "alice@gmail.com",
		Token: model.TokenData{
			AccessToken:     "access-xyz",
			RefreshToken:    "refresh-xyz",
			ExpiryTimestamp: 1800000000,
			ProjectID:       "proj-1",
			IDToken:         "id-1",
		},
	}

	if err := InjectAccountToStateDB(dbPath, acc); err != nil {
		t.Fatalf("注入 state.vscdb 失败: %v", err)
	}

	gotRT, err := ReadRefreshTokenFromStateDB(dbPath)
	if err != nil {
		t.Fatalf("回读 state.vscdb 失败: %v", err)
	}
	if gotRT != "refresh-xyz" {
		t.Fatalf("期望回读 refresh-xyz，实际 %q", gotRT)
	}

	// 校验 Onboarding 与 enterprisePreferences 写入
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer db.Close()

	var onboarding string
	if err := db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", "antigravityOnboarding").Scan(&onboarding); err != nil {
		t.Fatalf("读取 onboarding 失败: %v", err)
	}
	if onboarding != "true" {
		t.Fatalf("期望 onboarding=true，实际 %q", onboarding)
	}
}

func TestSystemCredentialPayloadEncodeDecode(t *testing.T) {
	acc := model.Account{
		Email: "bob@example.com",
		Token: model.TokenData{
			AccessToken:     "at-1",
			RefreshToken:    "rt-1",
			ExpiryTimestamp: 1800000000,
			IDToken:         "idt-1",
		},
	}

	raw, err := BuildCredentialPayloadJSON(acc)
	if err != nil {
		t.Fatalf("构造凭据 JSON 失败: %v", err)
	}

	// 验证 go-keyring-base64 前缀解码兼容
	wrapped := "go-keyring-base64:" + base64.StdEncoding.EncodeToString([]byte(raw))
	parsed, err := ParseSystemCredential(wrapped)
	if err != nil {
		t.Fatalf("解析 go-keyring-base64 凭据失败: %v", err)
	}
	if parsed.RefreshToken != "rt-1" || parsed.AccessToken != "at-1" {
		t.Fatalf("解析结果不匹配: %+v", parsed)
	}
}

func TestProtobufMalformedVarintNoPanic(t *testing.T) {
	// 构造含超大 length varint（溢出 uint64/int）和截断负载的恶意 Protobuf 字节序列
	cases := [][]byte{
		// field 1 wireType=2 (0x0a), length = MaxInt64 (0xff*8 + 0x7f)
		{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f, 0x01, 0x02},
		// field 1 wireType=2, length = MaxUint64 (0xff*9 + 0x01)
		{0x0a, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
		// field 1 wireType=2, length = 500 但剩余仅 2 字节
		{0x0a, 0xf4, 0x03, 0x41, 0x42},
	}

	for i, payload := range cases {
		if _, err := RemoveUnifiedTopicEntry(payload, "oauthTokenInfoSentinelKey"); err == nil {
			t.Fatalf("用例 %d：恶意 Protobuf 应返回错误而非静默成功", i)
		}
		at, rt, idt := ExtractOAuthInfoFromUnifiedOAuth(payload)
		if at != "" || rt != "" || idt != "" {
			t.Fatalf("用例 %d：恶意 Protobuf 不应解析出令牌: %q %q %q", i, at, rt, idt)
		}
		if email := ExtractEmailFromUnifiedUserStatus(payload); email != "" {
			t.Fatalf("用例 %d：恶意 Protobuf 不应解析出邮箱: %q", i, email)
		}
	}
}

func TestInjectStateDBPreservesSiblingSentinelsAndHandlesEmptyDB(t *testing.T) {
	// 1. 空 SQLite 库（无 ItemTable）读取凭据不应报错
	emptyDBPath := filepath.Join(t.TempDir(), "empty.vscdb")
	emptyDB, err := sql.Open("sqlite", emptyDBPath)
	if err != nil {
		t.Fatalf("创建空测试库失败: %v", err)
	}
	if _, err := emptyDB.Exec("CREATE TABLE unrelated (k TEXT)"); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	_ = emptyDB.Close()

	if cred, err := ReadCredentialFromStateDB(emptyDBPath); err != nil || cred != nil {
		t.Fatalf("无 ItemTable 时应返回 (nil, nil)，实际 cred=%+v err=%v", cred, err)
	}
	if rt, err := ReadRefreshTokenFromStateDB(emptyDBPath); err != nil || rt != "" {
		t.Fatalf("无 ItemTable 时 ReadRefreshTokenFromStateDB 应返回空串无错误，实际 rt=%q err=%v", rt, err)
	}

	// 2. 注入 userStatus / enterprisePreferences 时必须保留同 topic 下的其他 sibling sentinel 键
	dbPath := filepath.Join(t.TempDir(), "preserve.vscdb")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`); err != nil {
		t.Fatalf("创建 ItemTable 失败: %v", err)
	}
	const (
		userStatusKey = "antigravityUnifiedStateSync.userStatus"
		enterpriseKey = "antigravityUnifiedStateSync.enterprisePreferences"
	)
	siblingUS := base64.StdEncoding.EncodeToString(CreateUnifiedTopicEntry("customUserStatusSibling", []byte("keep-us")))
	siblingEP := base64.StdEncoding.EncodeToString(CreateUnifiedTopicEntry("customEnterpriseSibling", []byte("keep-ep")))
	_, _ = db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`, userStatusKey, siblingUS)
	_, _ = db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`, enterpriseKey, siblingEP)
	_ = db.Close()

	acc := model.Account{
		Email: "keep@example.com",
		Token: model.TokenData{
			AccessToken:  "at-keep",
			RefreshToken: "rt-keep",
		},
	}
	if err := InjectAccountToStateDB(dbPath, acc); err != nil {
		t.Fatalf("注入 state.vscdb 失败: %v", err)
	}

	verifyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("重新打开测试库失败: %v", err)
	}
	defer verifyDB.Close()

	checkHasKey := func(itemKey, sentinelKey string) {
		t.Helper()
		var b64 string
		if err := verifyDB.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, itemKey).Scan(&b64); err != nil {
			t.Fatalf("读取 %s 失败: %v", itemKey, err)
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			t.Fatalf("解码 %s 失败: %v", itemKey, err)
		}
		// 移除该 sentinel 键后长度应变短，说明其被完整保留在 topic 中
		stripped, err := RemoveUnifiedTopicEntry(raw, sentinelKey)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", itemKey, err)
		}
		if len(stripped) >= len(raw) {
			t.Fatalf("注入后 %s 中的兄弟键 %q 被意外覆盖丢失", itemKey, sentinelKey)
		}
	}
	checkHasKey(userStatusKey, "customUserStatusSibling")
	checkHasKey(enterpriseKey, "customEnterpriseSibling")
}

func TestMatchMacProcessCommandAndAppPathValidation(t *testing.T) {
	// 1. 验证字面量前缀匹配免疫正则元字符且不误匹配将路径作为参数的编辑器进程
	appBundle := "/Applications/Antigravity (Beta)+1.app"
	if !matchMacProcessCommand(appBundle+"/Contents/MacOS/Antigravity --flag", appBundle, "") {
		t.Fatal("应当匹配 .app 包内可执行文件启动的进程")
	}
	if matchMacProcessCommand("/usr/bin/vim "+appBundle+"/Contents/Info.plist", appBundle, "") {
		t.Fatal("不应误匹配仅在参数中包含 .app 路径的无关编辑器进程")
	}
	if matchMacProcessCommand("/Applications/OtherApp.app/Contents/MacOS/Other", "/", "/") {
		t.Fatal("根路径或宽泛目录绝不允许匹配任何进程")
	}

	// 2. 验证客户端路径合法性检查拒绝普通目录（如 TempDir 或 /）
	plainDir := t.TempDir()
	if isValidClientAppPathForOS(plainDir, "darwin") || isValidClientAppPathForOS("/", "darwin") {
		t.Fatal("普通目录不应通过 macOS 客户端应用路径校验")
	}
	fakeApp := filepath.Join(plainDir, "Antigravity.app")
	if err := os.MkdirAll(filepath.Join(fakeApp, "Contents", "MacOS"), 0755); err != nil {
		t.Fatal(err)
	}
	if !isValidClientAppPathForOS(fakeApp, "darwin") {
		t.Fatal("包含 Contents/MacOS 的合法 .app 目录应当通过校验")
	}

	// 3. 验证 Protobuf 重复标量字段遵循 Last-One-Wins 规范
	var dupMsg []byte
	dupMsg = append(dupMsg, EncodeStringField(1, "stale-token")...)
	dupMsg = append(dupMsg, EncodeStringField(1, "latest-token")...)
	if got := extractStringField(dupMsg, 1); got != "latest-token" {
		t.Fatalf("重复标量字段应返回最后一个值 latest-token，实际 %q", got)
	}
}
