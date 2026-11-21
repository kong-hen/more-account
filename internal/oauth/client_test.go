package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"more-account/internal/model"
)

func TestBuildAuthURLContainsRequiredScopes(t *testing.T) {
	raw := BuildAuthURL("http://127.0.0.1:12345/oauth-callback", "state-xyz")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析授权 URL 失败: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != DefaultClientID {
		t.Fatalf("client_id 不匹配: %s", q.Get("client_id"))
	}
	if q.Get("state") != "state-xyz" {
		t.Fatalf("state 不匹配: %s", q.Get("state"))
	}
	if !strings.Contains(q.Get("scope"), "https://www.googleapis.com/auth/aicode") {
		t.Fatalf("scope 缺少 aicode: %s", q.Get("scope"))
	}
}

func TestRefreshAccessTokenRetriesInvalidGrantOnce(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"transient"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken: "fresh-access",
			ExpiresIn:   3600,
			TokenType:   "Bearer",
		})
	}))
	defer srv.Close()

	client := &Client{
		HTTPClient: srv.Client(),
		TokenURL:   srv.URL,
	}

	resp, err := client.RefreshAccessToken(context.Background(), "rt-123")
	if err != nil {
		t.Fatalf("二次重试后刷新应当成功: %v", err)
	}
	if resp.AccessToken != "fresh-access" || attempts.Load() != 2 {
		t.Fatalf("期望重试 2 次并获得 fresh-access，实际 attempts=%d token=%s", attempts.Load(), resp.AccessToken)
	}
}

func TestEnsureFreshTokenSkipsWhenNotExpiring(t *testing.T) {
	c := NewClient()
	tok := model.TokenData{
		AccessToken:     "valid-token",
		RefreshToken:    "rt-1",
		ExpiryTimestamp: time.Now().Unix() + 3600,
	}
	got, refreshed, err := c.EnsureFreshToken(context.Background(), tok)
	if err != nil {
		t.Fatalf("不应返回错误: %v", err)
	}
	if refreshed || got.AccessToken != "valid-token" {
		t.Fatalf("未临期令牌不应触发刷新: refreshed=%v", refreshed)
	}
}

func TestLoginManagerManualCallbackSubmission(t *testing.T) {
	mgr := NewLoginManager()
	session, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动 OAuth 会话失败: %v", err)
	}
	defer mgr.Cancel()

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = mgr.SubmitCallbackURL(session.RedirectURI + "?code=auth-code-999&state=" + session.State)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code, err := mgr.WaitForCode(ctx)
	if err != nil {
		t.Fatalf("等待回调授权码失败: %v", err)
	}
	if code != "auth-code-999" {
		t.Fatalf("期望授权码 auth-code-999，实际 %s", code)
	}
}
