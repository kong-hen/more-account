package switcher

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"more-account/internal/model"
)

// buildSQLiteURI 构造安全转义后的 SQLite file: URI，避免路径中的 ?/#/% 与 Windows 反斜杠破坏 DSN，并正确启用 busy_timeout。
func buildSQLiteURI(dbPath string, readOnly bool) string {
	slashPath := filepath.ToSlash(dbPath)
	if len(slashPath) >= 2 && slashPath[1] == ':' {
		// Windows 盘符路径转为 file:///C:/... 形式
		slashPath = "/" + slashPath
	}
	u := url.URL{
		Scheme: "file",
		Path:   slashPath,
	}
	q := url.Values{}
	if readOnly {
		q.Set("mode", "ro")
	}
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	return u.String()
}

type sqlQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func readExistingTopic(ctx context.Context, q sqlQuerier, key string) ([]byte, error) {
	var existingVal string
	err := q.QueryRowContext(ctx, `SELECT value FROM ItemTable WHERE key = ?`, key).Scan(&existingVal)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(existingVal) == "" {
		return nil, nil
	}
	decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(existingVal))
	if decErr != nil {
		return nil, nil
	}
	return decoded, nil
}

func upsertTopicSentinel(ctx context.Context, q sqlQuerier, tableKey, sentinelKey string, payload []byte) error {
	currentTopic, err := readExistingTopic(ctx, q, tableKey)
	if err != nil {
		return fmt.Errorf("读取既有 %s 失败: %w", tableKey, err)
	}
	topic, err := RemoveUnifiedTopicEntry(currentTopic, sentinelKey)
	if err != nil {
		topic = nil
	}
	if len(payload) > 0 {
		topic = append(topic, CreateUnifiedTopicEntry(sentinelKey, payload)...)
	}
	if len(topic) == 0 {
		_, err = q.ExecContext(ctx, `DELETE FROM ItemTable WHERE key = ?`, tableKey)
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`,
		tableKey, base64.StdEncoding.EncodeToString(topic))
	return err
}

// InjectAccountToStateDB 将账号凭据以 BEGIN IMMEDIATE 写事务 + Protobuf/Base64 格式原子注入 Antigravity IDE / 旧版 state.vscdb，
// 避免 DEFERRED 事务先读后写锁升级触发 SQLITE_BUSY，并在落盘后收紧文件权限至 0600。
func InjectAccountToStateDB(dbPath string, account model.Account) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return fmt.Errorf("创建 state.vscdb 目录失败: %w", err)
	}

	db, err := sql.Open("sqlite", buildSQLiteURI(dbPath, false))
	if err != nil {
		return fmt.Errorf("打开 state.vscdb 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("获取 state.vscdb 连接失败: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
		}
		_ = conn.Close()
	}()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("开启 state.vscdb 写事务失败: %w", err)
	}

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value TEXT)`); err != nil {
		return fmt.Errorf("初始化 ItemTable 失败: %w", err)
	}

	oauthInfo := CreateOAuthTokenInfo(
		account.Token.AccessToken,
		account.Token.RefreshToken,
		account.Token.ExpiryTimestamp,
		account.Token.IsGcpTos,
		account.Token.IDToken,
		account.Email,
	)
	if err := upsertTopicSentinel(ctx, conn, "antigravityUnifiedStateSync.oauthToken", "oauthTokenInfoSentinelKey", oauthInfo); err != nil {
		return fmt.Errorf("写入 oauthToken 失败: %w", err)
	}

	if email := strings.TrimSpace(account.Email); email != "" {
		if err := upsertTopicSentinel(ctx, conn, "antigravityUnifiedStateSync.userStatus", "userStatusSentinelKey", CreateMinimalUserStatusPayload(email)); err != nil {
			return fmt.Errorf("写入 userStatus 失败: %w", err)
		}
	}

	if proj := strings.TrimSpace(account.Token.ProjectID); proj != "" {
		if err := upsertTopicSentinel(ctx, conn, "antigravityUnifiedStateSync.enterprisePreferences", "enterpriseGcpProjectId", CreateStringValuePayload(proj)); err != nil {
			return fmt.Errorf("写入 enterprisePreferences 失败: %w", err)
		}
	} else {
		// 仅移除 enterpriseGcpProjectId sentinel，保留同 key 下其他偏好项。
		if err := upsertTopicSentinel(ctx, conn, "antigravityUnifiedStateSync.enterprisePreferences", "enterpriseGcpProjectId", nil); err != nil {
			return fmt.Errorf("清理 enterpriseGcpProjectId 失败: %w", err)
		}
	}

	if _, err := conn.ExecContext(ctx, `INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`,
		"antigravityOnboarding", "true"); err != nil {
		return fmt.Errorf("写入 Onboarding 标记失败: %w", err)
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("提交 state.vscdb 事务失败: %w", err)
	}
	committed = true

	// 收紧包含明文令牌的 SQLite 数据库权限至仅当前用户可读写
	_ = os.Chmod(dbPath, 0600)
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
// 若数据库文件不存在、尚无 ItemTable 表或未写入登录态，返回 (nil, nil)。
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
	db, err := sql.Open("sqlite", buildSQLiteURI(dbPath, true))
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
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil, nil
		}
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
