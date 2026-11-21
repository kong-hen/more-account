package switcher

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"more-account/internal/model"
	"more-account/internal/storage"
)

const desktopFallbackFileName = "jetski-standalone-oauth-token"

type antigravityCredentialToken struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
	Expiry       string `json:"expiry"`
}

type antigravityCredentialPayload struct {
	Token      antigravityCredentialToken `json:"token"`
	AuthMethod string                     `json:"auth_method"`
	IDToken    string                     `json:"id_token,omitempty"`
}

// SystemCredential 表示从本机系统凭据库或桌面回退文件读取出的反重力登录态。
type SystemCredential struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Expiry       string
	AuthMethod   string
}

// BuildCredentialPayloadJSON 构造与 Antigravity 2.0 兼容的凭据 JSON。
func BuildCredentialPayloadJSON(account model.Account) (string, error) {
	expiryTime := time.Unix(account.Token.ExpiryTimestamp, 0).UTC()
	if account.Token.ExpiryTimestamp <= 0 {
		expiryTime = time.Now().UTC().Add(time.Hour)
	}
	payload := antigravityCredentialPayload{
		Token: antigravityCredentialToken{
			AccessToken:  account.Token.AccessToken,
			TokenType:    "Bearer",
			RefreshToken: account.Token.RefreshToken,
			Expiry:       expiryTime.Format("2006-01-02T15:04:05.000000Z"),
		},
		AuthMethod: "consumer",
		IDToken:    strings.TrimSpace(account.Token.IDToken),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("序列化系统凭据失败: %w", err)
	}
	return string(raw), nil
}

// ParseSystemCredential 解析原始系统凭据（支持 macOS go-keyring-base64: 前缀）。
func ParseSystemCredential(rawSecret string) (*SystemCredential, error) {
	trimmed := strings.TrimSpace(rawSecret)
	if trimmed == "" {
		return nil, fmt.Errorf("系统凭据为空")
	}
	if strings.HasPrefix(trimmed, "go-keyring-base64:") {
		encoded := strings.TrimPrefix(trimmed, "go-keyring-base64:")
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("Keychain Base64 凭据解码失败: %w", err)
		}
		trimmed = strings.TrimSpace(string(decoded))
	}

	var parsed struct {
		Token struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			TokenType    string `json:"token_type"`
			Expiry       string `json:"expiry"`
		} `json:"token"`
		AuthMethod string `json:"auth_method"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("解析系统凭据 JSON 失败: %w", err)
	}
	rt := strings.TrimSpace(parsed.Token.RefreshToken)
	if rt == "" {
		return nil, fmt.Errorf("系统凭据中缺少 refresh_token")
	}
	return &SystemCredential{
		AccessToken:  strings.TrimSpace(parsed.Token.AccessToken),
		RefreshToken: rt,
		TokenType:    strings.TrimSpace(parsed.Token.TokenType),
		Expiry:       strings.TrimSpace(parsed.Token.Expiry),
		AuthMethod:   strings.TrimSpace(parsed.AuthMethod),
	}, nil
}

// WriteSystemCredentials 写入 Antigravity 2.0 系统凭据、桌面回退文件，并按需同步 CLI 凭据。
func WriteSystemCredentials(account model.Account, forceSyncCLI bool) error {
	payloadJSON, err := BuildCredentialPayloadJSON(account)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("无法获取用户主目录: %w", err)
	}
	geminiDir := filepath.Join(home, ".gemini")
	fallbackPath := filepath.Join(geminiDir, desktopFallbackFileName)

	// 1. 写入操作系统凭据库（macOS Keychain / Linux secret-tool / Windows 回退兼容）
	if err := writeOSSystemCredential(payloadJSON); err != nil {
		return err
	}

	// 2. 同步写入桌面回退文件，避免系统凭据超时后回弹旧账号
	if err := storage.WriteBytesAtomic(fallbackPath, []byte(payloadJSON), 0600); err != nil {
		return fmt.Errorf("系统凭据已更新，但写入桌面回退文件失败: %w", err)
	}

	// 3. 若目标是 CLI 或 ~/.gemini/antigravity-cli 已初始化，同步写入 CLI 凭据
	cliDir := filepath.Join(geminiDir, "antigravity-cli")
	shouldWriteCLI := forceSyncCLI
	if !shouldWriteCLI {
		if st, statErr := os.Stat(cliDir); statErr == nil && st.IsDir() {
			shouldWriteCLI = true
		}
	}
	if shouldWriteCLI {
		cliPath := filepath.Join(cliDir, "antigravity-oauth-token")
		if err := storage.WriteBytesAtomic(cliPath, []byte(payloadJSON), 0600); err != nil {
			return fmt.Errorf("同步写入 Antigravity CLI 凭据失败: %w", err)
		}
	}
	return nil
}

// ReadDesktopCredential 依次从系统凭据库和桌面回退文件读取当前登录的反重力凭据。
func ReadDesktopCredential() (*SystemCredential, error) {
	if raw, err := readOSSystemCredential(); err == nil && strings.TrimSpace(raw) != "" {
		if cred, parseErr := ParseSystemCredential(raw); parseErr == nil {
			return cred, nil
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	fallbackPath := filepath.Join(home, ".gemini", desktopFallbackFileName)
	rawBytes, err := os.ReadFile(fallbackPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取桌面回退凭据文件失败: %w", err)
	}
	return ParseSystemCredential(string(rawBytes))
}
