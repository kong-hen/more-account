package oauth

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"more-account/internal/model"
)

var (
	// DefaultClientID 与 DefaultClientSecret 默认留空（严禁在源码仓库硬编码任何私密凭据与密钥）。
	// 支持通过以下任一方式注入：
	// 1) 应用「设置」界面保存至本机 ~/.more-account/settings.json（0600 权限）
	// 2) 环境变量 MORE_ACCOUNT_OAUTH_CLIENT_ID / MORE_ACCOUNT_OAUTH_CLIENT_SECRET
	// 3) 编译期 -ldflags "-X more-account/internal/oauth.DefaultClientID=... -X more-account/internal/oauth.DefaultClientSecret=..."
	// 4) 本地未纳入版本控制的 .env.local 文件
	DefaultClientID     = ""
	DefaultClientSecret = ""
)

const (
	DefaultTokenURL         = "https://oauth2.googleapis.com/token"
	DefaultUserInfoURL      = "https://www.googleapis.com/oauth2/v2/userinfo"
	DefaultAuthURL          = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultOAuthClientKey   = "antigravity_enterprise"
	NativeOAuthUserAgent    = "vscode/1.X.X (Antigravity/4.3.0)"
	TokenRefreshSkewSeconds = 900
)

// ErrInvalidGrant 表示 Google 返回 invalid_grant（Refresh Token 已被撤销或失效）。
var ErrInvalidGrant = errors.New("invalid_grant")

var (
	dotEnvOnce   sync.Once
	dotEnvID     string
	dotEnvSecret string
)

func loadDotEnvLocal() (string, string) {
	dotEnvOnce.Do(func() {
		candidates := []string{".env.local"}
		if exePath, err := os.Executable(); err == nil && exePath != "" {
			exeDir := filepath.Dir(exePath)
			candidates = append(candidates,
				filepath.Join(exeDir, ".env.local"),
				filepath.Join(exeDir, "..", "..", "..", ".env.local"),
			)
		}
		for _, p := range candidates {
			id, sec := parseDotEnvFile(p)
			if dotEnvID == "" && id != "" {
				dotEnvID = id
			}
			if dotEnvSecret == "" && sec != "" {
				dotEnvSecret = sec
			}
			if dotEnvID != "" && dotEnvSecret != "" {
				break
			}
		}
	})
	return dotEnvID, dotEnvSecret
}

func parseDotEnvFile(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	var id, sec string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"'`)
		switch key {
		case "MORE_ACCOUNT_OAUTH_CLIENT_ID":
			id = val
		case "MORE_ACCOUNT_OAUTH_CLIENT_SECRET":
			sec = val
		}
	}
	return id, sec
}

// ResolveCredentials 按优先级解析 OAuth 客户端凭据：自定义参数 > 环境变量 > 编译期注入 > 本地 .env.local。
func ResolveCredentials(customID, customSecret string) (clientID, clientSecret string) {
	localID, localSec := loadDotEnvLocal()

	for _, candidate := range []string{
		customID,
		os.Getenv("MORE_ACCOUNT_OAUTH_CLIENT_ID"),
		DefaultClientID,
		localID,
	} {
		if s := strings.TrimSpace(candidate); s != "" {
			clientID = s
			break
		}
	}

	for _, candidate := range []string{
		customSecret,
		os.Getenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET"),
		DefaultClientSecret,
		localSec,
	} {
		if s := strings.TrimSpace(candidate); s != "" {
			clientSecret = s
			break
		}
	}
	return clientID, clientSecret
}

// HasBuiltInCredentials 判断运行环境（环境变量、编译期 ldflags 或本地 .env.local）是否已具备默认 OAuth 凭据。
func HasBuiltInCredentials() bool {
	id, sec := ResolveCredentials("", "")
	return id != "" && sec != ""
}

// TokenResponse 对应 Google OAuth2 Token 端点的响应体。
type TokenResponse struct {
	AccessToken    string `json:"access_token"`
	ExpiresIn      int64  `json:"expires_in"`
	TokenType      string `json:"token_type"`
	RefreshToken   string `json:"refresh_token"`
	IDToken        string `json:"id_token"`
	OAuthClientKey string `json:"-"`
}

// UserInfo 对应 Google UserInfo 响应体。
type UserInfo struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
}

// DisplayName 返回优先展示的用户名。
func (u *UserInfo) DisplayName() string {
	if s := strings.TrimSpace(u.Name); s != "" {
		return s
	}
	parts := make([]string, 0, 2)
	if s := strings.TrimSpace(u.GivenName); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(u.FamilyName); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

// Client 封装 Google OAuth2 HTTP 调用，支持动态凭据提供器与测试端点替换。
type Client struct {
	HTTPClient          *http.Client
	TokenURL            string
	UserInfoURL         string
	ClientID            string
	ClientSecret        string
	CredentialsProvider func() (clientID, clientSecret string)
}

// NewClient 创建默认 OAuth 客户端。
func NewClient() *Client {
	return &Client{
		HTTPClient:  &http.Client{Timeout: 15 * time.Second},
		TokenURL:    DefaultTokenURL,
		UserInfoURL: DefaultUserInfoURL,
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) tokenURL() string {
	if strings.TrimSpace(c.TokenURL) != "" {
		return c.TokenURL
	}
	return DefaultTokenURL
}

func (c *Client) userInfoURL() string {
	if strings.TrimSpace(c.UserInfoURL) != "" {
		return c.UserInfoURL
	}
	return DefaultUserInfoURL
}

func (c *Client) resolveCredentials() (string, string) {
	customID, customSecret := c.ClientID, c.ClientSecret
	if c.CredentialsProvider != nil {
		pid, psec := c.CredentialsProvider()
		if strings.TrimSpace(pid) != "" {
			customID = pid
		}
		if strings.TrimSpace(psec) != "" {
			customSecret = psec
		}
	}
	return ResolveCredentials(customID, customSecret)
}

// GeneratePKCE 生成符合 RFC 7636 的 S256 PKCE code_verifier 与 code_challenge。
func GeneratePKCE() (verifier, challenge string) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		fallback := fmt.Sprintf("pkce_fallback_%d_antigravity_more_account", time.Now().UnixNano())
		sum := sha256.Sum256([]byte(fallback))
		return fallback, base64.RawURLEncoding.EncodeToString(sum[:])
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge
}

// BuildAuthURL 生成 Google OAuth2 授权页地址。
func BuildAuthURL(redirectURI, state string) string {
	return BuildAuthURLWithPKCE(redirectURI, state, "")
}

// BuildAuthURLWithPKCE 生成带可选 S256 PKCE 参数的 Google OAuth2 授权页地址。
func BuildAuthURLWithPKCE(redirectURI, state, codeChallenge string) string {
	clientID, _ := ResolveCredentials("", "")
	return BuildAuthURLWithClientAndPKCE(clientID, redirectURI, state, codeChallenge)
}

// BuildAuthURLWithClientAndPKCE 使用指定 ClientID 与可选 S256 PKCE 参数生成 Google OAuth2 授权页地址。
func BuildAuthURLWithClientAndPKCE(clientID, redirectURI, state, codeChallenge string) string {
	if strings.TrimSpace(clientID) == "" {
		clientID, _ = ResolveCredentials("", "")
	}
	scopes := strings.Join([]string{
		"openid",
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog",
		"https://www.googleapis.com/auth/experimentsandconfigs",
		"https://www.googleapis.com/auth/aicode",
	}, " ")

	params := url.Values{}
	params.Set("client_id", strings.TrimSpace(clientID))
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", scopes)
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("include_granted_scopes", "true")
	if strings.TrimSpace(state) != "" {
		params.Set("state", strings.TrimSpace(state))
	}
	if strings.TrimSpace(codeChallenge) != "" {
		params.Set("code_challenge", strings.TrimSpace(codeChallenge))
		params.Set("code_challenge_method", "S256")
	}

	return DefaultAuthURL + "?" + params.Encode()
}

// ExchangeCode 使用授权码（及可选 PKCE code_verifier）向 Google 换取 Access Token 和 Refresh Token。
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string, codeVerifier ...string) (*TokenResponse, error) {
	clientID, clientSecret := c.resolveCredentials()
	if c.tokenURL() == DefaultTokenURL && (clientID == "" || clientSecret == "") {
		return nil, fmt.Errorf("尚未配置 Google OAuth 客户端凭据（Client ID / Secret），请在「设置」或本地 .env.local 中配置")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")
	if len(codeVerifier) > 0 && strings.TrimSpace(codeVerifier[0]) != "" {
		form.Set("code_verifier", strings.TrimSpace(codeVerifier[0]))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("构建 Token 交换请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", NativeOAuthUserAgent)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("Token 交换网络请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Token 交换失败 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tokenResp TokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("解析 Token 响应失败: %w", err)
	}
	tokenResp.OAuthClientKey = DefaultOAuthClientKey
	if tokenResp.TokenType == "" {
		tokenResp.TokenType = "Bearer"
	}
	return &tokenResp, nil
}

// RefreshAccessToken 使用 Refresh Token 刷新访问令牌，遇 invalid_grant 时短暂退避重试一次。
func (c *Client) RefreshAccessToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return nil, fmt.Errorf("refresh_token 不能为空")
	}

	clientID, clientSecret := c.resolveCredentials()
	if c.tokenURL() == DefaultTokenURL && (clientID == "" || clientSecret == "") {
		return nil, fmt.Errorf("尚未配置 Google OAuth 客户端凭据（Client ID / Secret），请在「设置」或本地 .env.local 中配置")
	}

	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("refresh_token", rt)
	form.Set("grant_type", "refresh_token")

	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(form.Encode()))
		if err != nil {
			return nil, fmt.Errorf("构建令牌刷新请求失败: %w", err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", NativeOAuthUserAgent)

		resp, err := c.httpClient().Do(req)
		if err != nil {
			return nil, fmt.Errorf("刷新令牌网络失败: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			var tokenResp TokenResponse
			if err := json.Unmarshal(body, &tokenResp); err != nil {
				return nil, fmt.Errorf("解析刷新令牌响应失败: %w", err)
			}
			tokenResp.OAuthClientKey = DefaultOAuthClientKey
			if tokenResp.TokenType == "" {
				tokenResp.TokenType = "Bearer"
			}
			return &tokenResp, nil
		}

		isInvalidGrant := isInvalidGrantPayload(body)
		if attempt == 0 && isInvalidGrant {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(500 * time.Millisecond):
				continue
			}
		}

		if isInvalidGrant {
			return nil, fmt.Errorf("%w: %s", ErrInvalidGrant, strings.TrimSpace(string(body)))
		}
		return nil, fmt.Errorf("刷新令牌失败 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return nil, fmt.Errorf("刷新令牌重试次数已耗尽")
}

func isInvalidGrantPayload(body []byte) bool {
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(parsed.Error), "invalid_grant")
}

// FetchUserInfo 使用 Access Token 获取账号邮箱与显示名称。
func (c *Client) FetchUserInfo(ctx context.Context, accessToken string) (*UserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.userInfoURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("构建用户信息请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("获取用户信息网络失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("获取用户信息失败 (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var info UserInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("解析用户信息失败: %w", err)
	}
	if strings.TrimSpace(info.Email) == "" {
		return nil, fmt.Errorf("Google 未返回账号邮箱")
	}
	return &info, nil
}

// EnsureFreshToken 检查令牌是否临期（<=15分钟），若临期则自动刷新并返回更新后的 TokenData。
func (c *Client) EnsureFreshToken(ctx context.Context, current model.TokenData) (model.TokenData, bool, error) {
	now := time.Now().Unix()
	if strings.TrimSpace(current.AccessToken) != "" && current.ExpiryTimestamp > now+TokenRefreshSkewSeconds {
		return current, false, nil
	}
	resp, err := c.RefreshAccessToken(ctx, current.RefreshToken)
	if err != nil {
		return current, false, err
	}
	updated := MergeRefreshedToken(current, resp)
	return updated, true, nil
}

// MergeRefreshedToken 将刷新结果合并至现有 TokenData，保留原有项目 ID、客户端标识与深拷贝元数据。
func MergeRefreshedToken(current model.TokenData, resp *TokenResponse) model.TokenData {
	now := time.Now().Unix()
	refresh := strings.TrimSpace(resp.RefreshToken)
	if refresh == "" {
		refresh = current.RefreshToken
	}
	idToken := strings.TrimSpace(resp.IDToken)
	if idToken == "" {
		idToken = current.IDToken
	}
	clientKey := strings.TrimSpace(current.OAuthClientKey)
	if clientKey == "" {
		clientKey = DefaultOAuthClientKey
	}
	var gcpCopy *bool
	if current.IsGcpTos != nil {
		v := *current.IsGcpTos
		gcpCopy = &v
	}
	return model.TokenData{
		AccessToken:     resp.AccessToken,
		RefreshToken:    refresh,
		ExpiresIn:       resp.ExpiresIn,
		ExpiryTimestamp: now + resp.ExpiresIn,
		TokenType:       "Bearer",
		Email:           current.Email,
		ProjectID:       current.ProjectID,
		IsGcpTos:        gcpCopy,
		OAuthClientKey:  clientKey,
		IDToken:         idToken,
	}
}
