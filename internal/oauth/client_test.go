package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"more-account/internal/model"
)

func setupTestOAuthEnv(t *testing.T) {
	t.Helper()
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "test-client-secret-placeholder")
}

func TestBuildAuthURLContainsRequiredScopes(t *testing.T) {
	setupTestOAuthEnv(t)
	raw := BuildAuthURL("http://127.0.0.1:12345/oauth-callback", "state-xyz")
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析授权 URL 失败: %v", err)
	}
	q := parsed.Query()
	if q.Get("client_id") != "test-client-id.apps.googleusercontent.com" {
		t.Fatalf("client_id 不匹配: %s", q.Get("client_id"))
	}
	if q.Get("state") != "state-xyz" {
		t.Fatalf("state 不匹配: %s", q.Get("state"))
	}
	if !strings.Contains(q.Get("scope"), "https://www.googleapis.com/auth/aicode") {
		t.Fatalf("scope 缺少 aicode: %s", q.Get("scope"))
	}
}

func TestResolveCredentialsPriority(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "env-id")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "env-sec")

	// 自定义参数优先于环境变量
	id, sec := ResolveCredentials("custom-id", "custom-sec")
	if id != "custom-id" || sec != "custom-sec" {
		t.Fatalf("自定义凭据优先级异常: (%q, %q)", id, sec)
	}

	// 未传自定义参数时使用环境变量
	id2, sec2 := ResolveCredentials("", "")
	if id2 != "env-id" || sec2 != "env-sec" {
		t.Fatalf("环境变量凭据解析异常: (%q, %q)", id2, sec2)
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
	setupTestOAuthEnv(t)
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
	code, err := mgr.WaitForState(ctx, session.State)
	if err != nil {
		t.Fatalf("等待回调授权码失败: %v", err)
	}
	if code != "auth-code-999" {
		t.Fatalf("期望授权码 auth-code-999，实际 %s", code)
	}
}

func TestLoginManagerRejectsMissingAndMismatchedStateWithoutKillingSession(t *testing.T) {
	setupTestOAuthEnv(t)
	mgr := NewLoginManager()
	session, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动 OAuth 会话失败: %v", err)
	}
	defer mgr.Cancel()

	// 手动粘贴缺少或不匹配 state 的回调链接应被拒绝
	if err := mgr.SubmitCallbackURL(session.RedirectURI + "?code=forged-code"); err == nil {
		t.Fatal("缺少 state 的手动回调链接应当被拒绝")
	}
	if err := mgr.SubmitCallbackURL(session.RedirectURI + "?code=forged-code&state=wrong-state"); err == nil {
		t.Fatal("state 不匹配的手动回调链接应当被拒绝")
	}

	// HTTP 回调缺少 state、state 错误、或伪造无 state 的 error 参数均应返回 400，但不得销毁正在等待的合法会话
	for _, forgedQuery := range []string{
		"?code=forged-http-code",
		"?code=forged-http-code&state=wrong-state",
		"?error=access_denied",
		"?error=access_denied&state=wrong-state",
	} {
		resp, err := http.Get(session.RedirectURI + forgedQuery)
		if err != nil {
			t.Fatalf("请求本地 OAuth 回调 (%s) 失败: %v", forgedQuery, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("伪造回调 (%s) 应返回 400，实际 %d", forgedQuery, resp.StatusCode)
		}
	}

	// 随后的合法回调依然可以正常完成授权
	okResp, err := http.Get(session.RedirectURI + "?code=legit-code-777&state=" + url.QueryEscape(session.State))
	if err != nil {
		t.Fatalf("发送合法 OAuth 回调失败: %v", err)
	}
	_ = okResp.Body.Close()
	if okResp.StatusCode != http.StatusOK {
		t.Fatalf("合法回调应返回 200，实际 %d", okResp.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	gotCode, err := mgr.WaitForState(ctx, session.State)
	if err != nil || gotCode != "legit-code-777" {
		t.Fatalf("伪造请求不应中断会话，期望收到 legit-code-777，实际 code=%q err=%v", gotCode, err)
	}
}

func TestPKCEGenerationAndExchangeCode(t *testing.T) {
	setupTestOAuthEnv(t)
	mgr := NewLoginManager()
	session, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动 OAuth 会话失败: %v", err)
	}
	defer mgr.Cancel()

	if strings.TrimSpace(session.CodeVerifier) == "" {
		t.Fatal("StartSession 必须生成非空 PKCE CodeVerifier")
	}
	parsedAuth, err := url.Parse(session.AuthURL)
	if err != nil {
		t.Fatalf("解析 AuthURL 失败: %v", err)
	}
	if parsedAuth.Query().Get("code_challenge_method") != "S256" || parsedAuth.Query().Get("code_challenge") == "" {
		t.Fatalf("AuthURL 缺少 PKCE S256 参数: %s", session.AuthURL)
	}

	var gotVerifier string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotVerifier = r.FormValue("code_verifier")
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "at-pkce",
			RefreshToken: "rt-pkce",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	client := &Client{
		HTTPClient: srv.Client(),
		TokenURL:   srv.URL,
	}
	tok, err := client.ExchangeCode(context.Background(), "code-1", session.RedirectURI, session.CodeVerifier)
	if err != nil {
		t.Fatalf("ExchangeCode 失败: %v", err)
	}
	if gotVerifier != session.CodeVerifier || tok.AccessToken != "at-pkce" {
		t.Fatalf("ExchangeCode 未透传 code_verifier: got=%q want=%q", gotVerifier, session.CodeVerifier)
	}
}

func TestLoginManagerWaitForStateDoesNotCancelNewerSession(t *testing.T) {
	setupTestOAuthEnv(t)
	mgr := NewLoginManager()
	defer mgr.Cancel()

	s1, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动会话 1 失败: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, waitErr := mgr.WaitForState(ctx, s1.State)
		errCh <- waitErr
	}()

	time.Sleep(25 * time.Millisecond)

	// 并发发起会话 2 应取消会话 1，且会话 1 退出时不得误杀会话 2
	s2, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动会话 2 失败: %v", err)
	}

	select {
	case waitErr := <-errCh:
		if !errors.Is(waitErr, ErrOAuthCanceled) {
			t.Fatalf("会话 1 应返回 ErrOAuthCanceled，实际 %v", waitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("等待会话 1 退出超时")
	}

	if err := mgr.SubmitCallbackURL(s2.RedirectURI + "?code=code-from-s2&state=" + s2.State); err != nil {
		t.Fatalf("向会话 2 提交回调失败（可能被会话 1 误杀）: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	gotCode, err := mgr.WaitForState(ctx, s2.State)
	if err != nil || gotCode != "code-from-s2" {
		t.Fatalf("会话 2 期望收到 code-from-s2，实际 code=%q err=%v", gotCode, err)
	}
}
