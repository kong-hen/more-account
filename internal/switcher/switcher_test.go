package switcher

import (
	"database/sql"
	"encoding/base64"
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

	gotRT := ExtractRefreshTokenFromUnifiedOAuth(topic)
	if gotRT != "ref-456" {
		t.Fatalf("期望提取 refresh_token=ref-456，实际 %q", gotRT)
	}

	// 替换 oauthTokenInfoSentinelKey 时必须保留其他 sentinel 条目
	cleaned, err := RemoveUnifiedTopicEntry(topic, "oauthTokenInfoSentinelKey")
	if err != nil {
		t.Fatalf("移除旧 sentinel 失败: %v", err)
	}
	if ExtractRefreshTokenFromUnifiedOAuth(cleaned) != "" {
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
