package switcher

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"more-account/internal/model"
)

// InjectAccountToStateDB 将账号凭据以 Protobuf + Base64 格式注入 Antigravity IDE / 旧版 state.vscdb。
func InjectAccountToStateDB(dbPath string, account model.Account) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return fmt.Errorf("创建 state.vscdb 目录失败: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value TEXT)`); err != nil {
		return fmt.Errorf("初始化 ItemTable 失败: %w", err)
	}

	// 读取现有 unified oauthToken 并仅替换 oauthTokenInfoSentinelKey
	var currentTopic []byte
	var existingVal string
	err = db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "antigravityUnifiedStateSync.oauthToken").Scan(&existingVal)
	if err == nil && strings.TrimSpace(existingVal) != "" {
		decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(existingVal))
		if decErr == nil {
			currentTopic = decoded
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("读取既有 Unified OAuth 失败: %w", err)
	}

	topic, err := RemoveUnifiedTopicEntry(currentTopic, "oauthTokenInfoSentinelKey")
	if err != nil {
		topic = nil
	}

	oauthInfo := CreateOAuthTokenInfo(
		account.Token.AccessToken,
		account.Token.RefreshToken,
		account.Token.ExpiryTimestamp,
		account.Token.IsGcpTos,
		account.Token.IDToken,
		account.Email,
	)
	topic = append(topic, CreateUnifiedTopicEntry("oauthTokenInfoSentinelKey", oauthInfo)...)
	topicB64 := base64.StdEncoding.EncodeToString(topic)

	if _, err := db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`,
		"antigravityUnifiedStateSync.oauthToken", topicB64); err != nil {
		return fmt.Errorf("写入 oauthToken 失败: %w", err)
	}

	if email := strings.TrimSpace(account.Email); email != "" {
		userStatus := CreateUnifiedTopicEntry("userStatusSentinelKey", CreateMinimalUserStatusPayload(email))
		if _, err := db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`,
			"antigravityUnifiedStateSync.userStatus", base64.StdEncoding.EncodeToString(userStatus)); err != nil {
			return fmt.Errorf("写入 userStatus 失败: %w", err)
		}
	}

	if proj := strings.TrimSpace(account.Token.ProjectID); proj != "" {
		pref := CreateUnifiedTopicEntry("enterpriseGcpProjectId", CreateStringValuePayload(proj))
		if _, err := db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`,
			"antigravityUnifiedStateSync.enterprisePreferences", base64.StdEncoding.EncodeToString(pref)); err != nil {
			return fmt.Errorf("写入 enterprisePreferences 失败: %w", err)
		}
	} else {
		_, _ = db.Exec(`DELETE FROM ItemTable WHERE key = ?`, "antigravityUnifiedStateSync.enterprisePreferences")
	}

	if _, err := db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`,
		"antigravityOnboarding", "true"); err != nil {
		return fmt.Errorf("写入 Onboarding 标记失败: %w", err)
	}

	return nil
}

// ReadRefreshTokenFromStateDB 以只读方式从 state.vscdb 中提取当前账号的 refresh_token。
func ReadRefreshTokenFromStateDB(dbPath string) (string, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return "", err
	}
	dsn := fmt.Sprintf("file:%s?mode=ro", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("只读打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()

	var encoded string
	err = db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "antigravityUnifiedStateSync.oauthToken").Scan(&encoded)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("读取 state.vscdb oauthToken 失败: %w", err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", fmt.Errorf("解码 state.vscdb Base64 失败: %w", err)
	}
	return ExtractRefreshTokenFromUnifiedOAuth(raw), nil
}
