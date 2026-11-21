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

// SystemCredential 表示从本机系统凭据库、桌面回退文件、CLI 凭据或 state.vscdb 读取出的反重力登录态。
type SystemCredential struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Expiry       string
	AuthMethod   string
	IDToken      string
	Email        string
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
			IDToken      string `json:"id_token"`
		} `json:"token"`
		AuthMethod string `json:"auth_method"`
		IDToken    string `json:"id_token"`
		Email      string `json:"email"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil, fmt.Errorf("解析系统凭据 JSON 失败: %w", err)
	}
	rt := strings.TrimSpace(parsed.Token.RefreshToken)
	if rt == "" {
		return nil, fmt.Errorf("系统凭据中缺少 refresh_token")
	}
	idTok := strings.TrimSpace(parsed.IDToken)
	if idTok == "" {
		idTok = strings.TrimSpace(parsed.Token.IDToken)
	}
	email := strings.TrimSpace(parsed.Email)
	if email == "" && idTok != "" {
		email = ExtractEmailFromIDToken(idTok)
	}
	return &SystemCredential{
		AccessToken:  strings.TrimSpace(parsed.Token.AccessToken),
		RefreshToken: rt,
		TokenType:    strings.TrimSpace(parsed.Token.TokenType),
		Expiry:       strings.TrimSpace(parsed.Token.Expiry),
		AuthMethod:   strings.TrimSpace(parsed.AuthMethod),
		IDToken:      idTok,
		Email:        email,
	}, nil
}

// ExtractEmailFromIDToken 从 OpenID Connect JWT（id_token）的 payload 段本地解码邮箱，无需网络请求。
func ExtractEmailFromIDToken(idToken string) string {
	parts := strings.Split(strings.TrimSpace(idToken), ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return strings.TrimSpace(claims.Email)
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
	if cred, err := readParsedOSSystemCredential(); err == nil && cred != nil {
		return cred, nil
	}
	return readDesktopFallbackCredential()
}

func readParsedOSSystemCredential() (*SystemCredential, error) {
	raw, err := readOSSystemCredential()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return ParseSystemCredential(raw)
}

func readDesktopFallbackCredential() (*SystemCredential, error) {
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
	if strings.TrimSpace(string(rawBytes)) == "" {
		return nil, nil
	}
	return ParseSystemCredential(string(rawBytes))
}

// ReadCLICredential 从 ~/.gemini/antigravity-cli/antigravity-oauth-token 读取 CLI 当前登录凭据。
func ReadCLICredential() (*SystemCredential, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	cliPath := filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	rawBytes, err := os.ReadFile(cliPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取 Antigravity CLI 凭据文件失败: %w", err)
	}
	if strings.TrimSpace(string(rawBytes)) == "" {
		return nil, nil
	}
	return ParseSystemCredential(string(rawBytes))
}

// ReadActiveCredentialForTarget 按目标客户端类型（desktop / ide / cli）的权威优先顺序读取本机当前激活凭据。
// 返回值中 cred == nil 且 err == nil 表示全部已检查来源均明确无登录凭据（未登录或已退出）。
func ReadActiveCredentialForTarget(target string) (*SystemCredential, string, error) {
	type credProbe struct {
		source string
		read   func() (*SystemCredential, error)
	}

	var probes []credProbe
	switch normalizeTarget(target) {
	case "ide":
		probes = []credProbe{
			{source: "ide_statedb", read: func() (*SystemCredential, error) { return ReadCredentialFromStateDB(DefaultIDEStateDBPath()) }},
			{source: "keychain", read: readParsedOSSystemCredential},
			{source: "desktop_fallback", read: readDesktopFallbackCredential},
			{source: "desktop_statedb", read: func() (*SystemCredential, error) { return ReadCredentialFromStateDB(DefaultDesktopStateDBPath()) }},
		}
	case "cli":
		probes = []credProbe{
			{source: "cli_file", read: ReadCLICredential},
			{source: "keychain", read: readParsedOSSystemCredential},
			{source: "desktop_fallback", read: readDesktopFallbackCredential},
		}
	default:
		probes = []credProbe{
			{source: "keychain", read: readParsedOSSystemCredential},
			{source: "desktop_fallback", read: readDesktopFallbackCredential},
			{source: "desktop_statedb", read: func() (*SystemCredential, error) { return ReadCredentialFromStateDB(DefaultDesktopStateDBPath()) }},
			{source: "ide_statedb", read: func() (*SystemCredential, error) { return ReadCredentialFromStateDB(DefaultIDEStateDBPath()) }},
		}
	}

	var firstErr error
	for _, probe := range probes {
		cred, err := probe.read()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if cred != nil && (strings.TrimSpace(cred.RefreshToken) != "" || strings.TrimSpace(cred.Email) != "") {
			return cred, probe.source, nil
		}
	}
	if firstErr != nil {
		return nil, "", firstErr
	}
	return nil, "", nil
}
