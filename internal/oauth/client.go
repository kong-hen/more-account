package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"more-account/internal/model"
)

const (
	// DefaultClientID 与 DefaultClientSecret 对齐 Antigravity 官方客户端 OAuth 参数。
	DefaultClientID          = \"\"
	DefaultClientSecret      = \"\"
	DefaultTokenURL          = "https://oauth2.googleapis.com/token"
	DefaultUserInfoURL       = "https://www.googleapis.com/oauth2/v2/userinfo"
	DefaultAuthURL           = "https://accounts.google.com/o/oauth2/v2/auth"
	DefaultOAuthClientKey    = "antigravity_enterprise"
	NativeOAuthUserAgent     = "vscode/1.X.X (Antigravity/4.3.0)"
	TokenRefreshSkewSeconds  = 900
)

// ErrInvalidGrant 表示 Google 返回 invalid_grant（Refresh Token 已被撤销或失效）。
var ErrInvalidGrant = errors.New("invalid_grant")

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
	ID         string `json:"id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	GivenName  string `json:"given_name"`
	FamilyName string `json:"family_name"`
	Picture    string `json:"picture"`
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

// Client 封装 Google OAuth2 HTTP 调用，支持测试时替换端点。
type Client struct {
	HTTPClient  *http.Client
	TokenURL    string
	UserInfoURL string
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

// BuildAuthURL 生成 Google OAuth2 授权页地址。
func BuildAuthURL(redirectURI, state string) string {
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
	params.Set("client_id", DefaultClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("response_type", "code")
	params.Set("scope", scopes)
	params.Set("access_type", "offline")
	params.Set("prompt", "consent")
	params.Set("include_granted_scopes", "true")
	if strings.TrimSpace(state) != "" {
		params.Set("state", strings.TrimSpace(state))
	}

	return DefaultAuthURL + "?" + params.Encode()
}

// ExchangeCode 使用授权码向 Google 换取 Access Token 和 Refresh Token。
func (c *Client) ExchangeCode(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", DefaultClientID)
	form.Set("client_secret", DefaultClientSecret)
	form.Set("code", strings.TrimSpace(code))
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

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

	form := url.Values{}
	form.Set("client_id", DefaultClientID)
	form.Set("client_secret", DefaultClientSecret)
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

// MergeRefreshedToken 将刷新结果合并至现有 TokenData，保留原有项目 ID 与元数据。
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
	return model.TokenData{
		AccessToken:     resp.AccessToken,
		RefreshToken:    refresh,
		ExpiresIn:       resp.ExpiresIn,
		ExpiryTimestamp: now + resp.ExpiresIn,
		TokenType:       "Bearer",
		Email:           current.Email,
		ProjectID:       current.ProjectID,
		IsGcpTos:        current.IsGcpTos,
		OAuthClientKey:  DefaultOAuthClientKey,
		IDToken:         idToken,
	}
}
