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
	cred, err := ReadCredentialFromStateDB(dbPath)
	if err != nil || cred == nil {
		return "", err
	}
	return cred.RefreshToken, nil
}

// ReadCredentialFromStateDB 以只读方式从 state.vscdb 中提取当前账号的令牌与邮箱信息。
// 若数据库文件不存在或未写入登录态，返回 (nil, nil)。
func ReadCredentialFromStateDB(dbPath string) (*SystemCredential, error) {
	if strings.TrimSpace(dbPath) == "" {
		return nil, nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?mode=ro", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("只读打开 state.vscdb 失败: %w", err)
	}
	defer db.Close()

	var accessTok, refreshTok, idTok, email string

	var encodedOAuth string
	err = db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "antigravityUnifiedStateSync.oauthToken").Scan(&encodedOAuth)
	if err == nil && strings.TrimSpace(encodedOAuth) != "" {
		if raw, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedOAuth)); decErr == nil {
			accessTok, refreshTok, idTok = ExtractOAuthInfoFromUnifiedOAuth(raw)
		}
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("读取 state.vscdb oauthToken 失败: %w", err)
	}

	if idTok != "" {
		email = ExtractEmailFromIDToken(idTok)
	}
	if email == "" {
		var encodedStatus string
		if sErr := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "antigravityUnifiedStateSync.userStatus").Scan(&encodedStatus); sErr == nil && strings.TrimSpace(encodedStatus) != "" {
			if raw, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedStatus)); decErr == nil {
				email = ExtractEmailFromUnifiedUserStatus(raw)
			}
		}
	}

	if strings.TrimSpace(refreshTok) == "" && strings.TrimSpace(email) == "" {
		return nil, nil
	}
	return &SystemCredential{
		AccessToken:  strings.TrimSpace(accessTok),
		RefreshToken: strings.TrimSpace(refreshTok),
		IDToken:      strings.TrimSpace(idTok),
		Email:        strings.TrimSpace(email),
	}, nil
}
