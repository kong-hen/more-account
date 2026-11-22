package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
	if q.Get("prompt") != "select_account consent" {
		t.Fatalf("期望 prompt 为 select_account consent，实际 %q", q.Get("prompt"))
	}
}

func TestResolveCredentialsPriorityAndPairValidation(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "env-id")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "env-sec")

	// 自定义参数成对提供时优先于环境变量
	id, sec, err := ResolveCredentials("custom-id", "custom-sec")
	if err != nil || id != "custom-id" || sec != "custom-sec" {
		t.Fatalf("自定义凭据优先级异常: (%q, %q, err=%v)", id, sec, err)
	}

	// 未传自定义参数时使用环境变量
	id2, sec2, err2 := ResolveCredentials("", "")
	if err2 != nil || id2 != "env-id" || sec2 != "env-sec" {
		t.Fatalf("环境变量凭据解析异常: (%q, %q, err=%v)", id2, sec2, err2)
	}

	// 单边自定义 ID 或 Secret 必须报错，严禁与环境变量另一边错配混拼
	if _, _, err := ResolveCredentials("only-custom-id", ""); !errors.Is(err, ErrIncompleteCredentials) {
		t.Fatalf("仅传自定义 ClientID 应返回 ErrIncompleteCredentials，实际 %v", err)
	}
	if _, _, err := ResolveCredentials("", "only-custom-secret"); !errors.Is(err, ErrIncompleteCredentials) {
		t.Fatalf("仅传自定义 ClientSecret 应返回 ErrIncompleteCredentials，实际 %v", err)
	}

	// 环境变量单边配置同样必须报错
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "only-env-id")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "")
	if _, _, err := ResolveCredentials("", ""); !errors.Is(err, ErrIncompleteCredentials) {
		t.Fatalf("环境变量单边配置应返回 ErrIncompleteCredentials，实际 %v", err)
	}
	if HasBuiltInCredentials() {
		t.Fatal("环境变量仅配置单边凭据时 HasBuiltInCredentials 必须为 false")
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

func TestLoginManagerRootAndTrailingSlashCallbackNo404(t *testing.T) {
	setupTestOAuthEnv(t)
	mgr := NewLoginManager()
	mgr.PostCallbackGrace = 300 * time.Millisecond
	session, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动 OAuth 会话失败: %v", err)
	}
	defer mgr.Cancel()

	// CurrentSession 应反映活跃监听会话
	cur, ok := mgr.CurrentSession()
	if !ok || cur.RedirectURI != session.RedirectURI || cur.State != session.State {
		t.Fatalf("CurrentSession 返回异常: ok=%v cur=%+v", ok, cur)
	}

	parsed, err := url.Parse(session.RedirectURI)
	if err != nil {
		t.Fatalf("解析 RedirectURI 失败: %v", err)
	}
	baseOrigin := parsed.Scheme + "://" + parsed.Host

	// 1. 访问根路径应返回 200 状态提示页，而非 404
	rootResp, err := http.Get(baseOrigin + "/")
	if err != nil {
		t.Fatalf("访问本地监听根路径失败: %v", err)
	}
	_ = rootResp.Body.Close()
	if rootResp.StatusCode != http.StatusOK {
		t.Fatalf("根路径应返回 200，实际 %d", rootResp.StatusCode)
	}

	// 2. OPTIONS 预检应返回 200
	optReq, _ := http.NewRequest(http.MethodOptions, session.RedirectURI, nil)
	optResp, err := http.DefaultClient.Do(optReq)
	if err != nil {
		t.Fatalf("OPTIONS 预检失败: %v", err)
	}
	_ = optResp.Body.Close()
	if optResp.StatusCode != http.StatusOK {
		t.Fatalf("OPTIONS 应返回 200，实际 %d", optResp.StatusCode)
	}

	// 3. favicon 请求应返回 204
	favResp, err := http.Get(baseOrigin + "/favicon.ico")
	if err != nil {
		t.Fatalf("favicon 请求失败: %v", err)
	}
	_ = favResp.Body.Close()
	if favResp.StatusCode != http.StatusNoContent {
		t.Fatalf("favicon 应返回 204，实际 %d", favResp.StatusCode)
	}

	// 4. 带尾斜杠的 /oauth-callback/ 也能成功接收合法回调
	waitDone := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if code, wErr := mgr.WaitForState(ctx, session.State); wErr == nil {
			waitDone <- code
		}
	}()

	cbURL := session.RedirectURI + "/?code=trailing-ok-123&state=" + url.QueryEscape(session.State)
	cbResp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("发送带尾斜杠回调失败: %v", err)
	}
	_ = cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK {
		t.Fatalf("带尾斜杠合法回调应返回 200，实际 %d", cbResp.StatusCode)
	}

	select {
	case got := <-waitDone:
		if got != "trailing-ok-123" {
			t.Fatalf("期望收到授权码 trailing-ok-123，实际 %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("等待回调授权码超时")
	}

	// 5. 在缓冲期内浏览器再次刷新回调页仍应返回 200，不立即断开连接
	retryResp, err := http.Get(cbURL)
	if err != nil {
		t.Fatalf("缓冲期内二次刷新回调页失败: %v", err)
	}
	_ = retryResp.Body.Close()
	if retryResp.StatusCode != http.StatusOK {
		t.Fatalf("缓冲期内二次刷新应返回 200，实际 %d", retryResp.StatusCode)
	}
}

func TestLoginManagerSubmitCallbackURLFlexibleFormats(t *testing.T) {
	setupTestOAuthEnv(t)
	mgr := NewLoginManager()
	session, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动 OAuth 会话失败: %v", err)
	}
	defer mgr.Cancel()

	// 支持仅粘贴查询参数部分
	if err := mgr.SubmitCallbackURL("?code=query-only-code&state=" + url.QueryEscape(session.State)); err != nil {
		t.Fatalf("提交纯查询参数回调失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := mgr.WaitForState(ctx, session.State)
	if err != nil || got != "query-only-code" {
		t.Fatalf("期望收到 query-only-code，实际 code=%q err=%v", got, err)
	}
}

func TestLoginManagerSubmitCallbackURLNormalizedVariants(t *testing.T) {
	setupTestOAuthEnv(t)

	cases := []struct {
		name     string
		buildRaw func(sess LoginSession) string
		wantCode string
	}{
		{
			name: "大写 HTTP scheme",
			buildRaw: func(s LoginSession) string {
				return "HTTP://127.0.0.1:54321/oauth-callback?state=" + url.QueryEscape(s.State) + "&code=upper-http"
			},
			wantCode: "upper-http",
		},
		{
			name: "大写 HTTPS scheme",
			buildRaw: func(s LoginSession) string {
				return "HTTPS://localhost:54321/oauth-callback?state=" + url.QueryEscape(s.State) + "&code=upper-https"
			},
			wantCode: "upper-https",
		},
		{
			name: "缺 scheme 的 127.0.0.1 完整地址",
			buildRaw: func(s LoginSession) string {
				return "127.0.0.1:54321/oauth-callback?state=" + url.QueryEscape(s.State) + "&code=no-scheme-ip"
			},
			wantCode: "no-scheme-ip",
		},
		{
			name: "缺 scheme 的 localhost 完整地址",
			buildRaw: func(s LoginSession) string {
				return "localhost:54321/oauth-callback?state=" + url.QueryEscape(s.State) + "&code=no-scheme-local"
			},
			wantCode: "no-scheme-local",
		},
		{
			name: "无前导斜杠的 oauth-callback 路径",
			buildRaw: func(s LoginSession) string {
				return "oauth-callback?state=" + url.QueryEscape(s.State) + "&code=rel-path"
			},
			wantCode: "rel-path",
		},
		{
			name: "相对路径 /oauth-callback",
			buildRaw: func(s LoginSession) string {
				return "/oauth-callback?state=" + url.QueryEscape(s.State) + "&code=slash-path"
			},
			wantCode: "slash-path",
		},
		{
			name: "无问号纯查询串",
			buildRaw: func(s LoginSession) string {
				return "state=" + url.QueryEscape(s.State) + "&code=raw-query"
			},
			wantCode: "raw-query",
		},
		{
			name: "含折行回车换行制表符与缩进空格",
			buildRaw: func(s LoginSession) string {
				return "  http://127.0.0.1:54321/oauth-callback?\r\n\t  state=" + url.QueryEscape(s.State) + "&\n  code=multiline-code \r\n"
			},
			wantCode: "multiline-code",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewLoginManager()
			mgr.PostCallbackGrace = 0
			sess, err := mgr.StartSession()
			if err != nil {
				t.Fatalf("启动 OAuth 会话失败: %v", err)
			}
			defer mgr.Cancel()

			if err := mgr.SubmitCallbackURL(tc.buildRaw(sess)); err != nil {
				t.Fatalf("SubmitCallbackURL 失败: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			got, err := mgr.WaitForState(ctx, sess.State)
			if err != nil || got != tc.wantCode {
				t.Fatalf("期望 code=%q，实际 code=%q err=%v", tc.wantCode, got, err)
			}
		})
	}
}

func TestLoginManagerEnsureSessionProcessingAndSilentCancel(t *testing.T) {
	setupTestOAuthEnv(t)
	mgr := NewLoginManager()
	mgr.PostCallbackGrace = 0
	defer mgr.Cancel()

	// 1. 并发 EnsureSession 只能创建一个会话，避免并发双监听
	const goroutines = 6
	var wg sync.WaitGroup
	var createdCount atomic.Int32
	states := make([]string, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			info, created, err := mgr.EnsureSession(SessionConfig{Silent: true})
			if err != nil {
				t.Errorf("并发 EnsureSession 失败: %v", err)
				return
			}
			if created {
				createdCount.Add(1)
			}
			states[idx] = info.State
		}(i)
	}
	wg.Wait()

	if createdCount.Load() != 1 {
		t.Fatalf("并发 EnsureSession 应仅创建 1 次，实际 created=%d", createdCount.Load())
	}
	firstState := states[0]
	for i := 1; i < goroutines; i++ {
		if states[i] != firstState {
			t.Fatalf("并发 EnsureSession 应复用同一会话 state=%q，实际 %q", firstState, states[i])
		}
	}
	if !mgr.IsSessionSilent(firstState) {
		t.Fatal("自动预监听创建的会话应标记为 Silent=true")
	}

	// 2. 用户未主动发起时 CancelIfSilent 应静默取消
	if !mgr.CancelIfSilent() {
		t.Fatal("处于 waiting 且 Silent=true 的会话应可被 CancelIfSilent 取消")
	}
	if _, ok := mgr.CurrentSessionInfo(); ok {
		t.Fatal("CancelIfSilent 后当前会话应为空")
	}

	// 3. 重新静默创建后升级为用户主动发起，CancelIfSilent 不得取消
	info, created, err := mgr.EnsureSession(SessionConfig{Silent: true})
	if err != nil || !created {
		t.Fatalf("重新创建静默会话失败: created=%v err=%v", created, err)
	}
	reused, created2, err := mgr.EnsureSession(SessionConfig{Silent: false})
	if err != nil || created2 || reused.State != info.State || reused.Silent {
		t.Fatalf("非静默 EnsureSession 应复用会话并清除 Silent 标记: info=%+v created=%v err=%v", reused, created2, err)
	}
	if mgr.CancelIfSilent() {
		t.Fatal("用户已主动发起的会话不应被 CancelIfSilent 取消")
	}

	// 4. 提交回调后会话进入 processing 状态且不置空 m.current，重复提交应返回 ErrOAuthProcessing
	if err := mgr.SubmitCallbackURL(info.RedirectURI + "?code=code-proc&state=" + info.State); err != nil {
		t.Fatalf("首次手动提交回调失败: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	gotCode, err := mgr.WaitForState(ctx, info.State)
	if err != nil || gotCode != "code-proc" {
		t.Fatalf("WaitForState 期望返回 code-proc，实际 code=%q err=%v", gotCode, err)
	}

	curInfo, ok := mgr.CurrentSessionInfo()
	if !ok || curInfo.State != info.State || curInfo.Status != SessionStatusProcessing {
		t.Fatalf("收到回调后会话应保留为 processing 状态，实际 ok=%v info=%+v", ok, curInfo)
	}

	// processing 期间二次提交应返回 ErrOAuthProcessing，不再误报“没有等待中的会话”
	if err := mgr.SubmitCallbackURL(info.RedirectURI + "?code=code-proc&state=" + info.State); !errors.Is(err, ErrOAuthProcessing) {
		t.Fatalf("processing 期间二次提交应返回 ErrOAuthProcessing，实际 %v", err)
	}

	// processing 期间重开弹窗调用 EnsureSession 应复用当前处理中会话，不得新建端口泄漏
	reopenInfo, reopenCreated, err := mgr.EnsureSession(SessionConfig{Silent: true})
	if err != nil || reopenCreated || reopenInfo.State != info.State || reopenInfo.Status != SessionStatusProcessing {
		t.Fatalf("processing 期间 EnsureSession 应复用当前会话，实际 info=%+v created=%v err=%v", reopenInfo, reopenCreated, err)
	}

	// processing 期间 CancelSilent 不得打断正在换取令牌的会话
	if mgr.CancelSilent() {
		t.Fatal("processing 期间 CancelSilent 不应取消正在处理的会话")
	}

	// FinishProcessing 传入错误 state 不应误删当前会话；传入匹配 state 才清理
	mgr.FinishProcessing("wrong-state")
	if _, stillAlive := mgr.CurrentSessionInfo(); !stillAlive {
		t.Fatal("FinishProcessing 传入不匹配 state 不应清理当前会话")
	}
	mgr.FinishProcessing(info.State)
	if _, stillAlive := mgr.CurrentSessionInfo(); stillAlive {
		t.Fatal("FinishProcessing 后当前会话应被清理")
	}
}

func TestStartSessionAndEnsureSessionRequireCompleteCredentials(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "env-id")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "env-sec")

	mgr := NewLoginManager()
	mgr.CredentialsProvider = func() (string, string) {
		return "only-id-in-settings", ""
	}
	if _, err := mgr.StartSession(); !errors.Is(err, ErrIncompleteCredentials) {
		t.Fatalf("StartSession 遇单边自定义凭据应拒绝启动，实际 %v", err)
	}
	if _, _, err := mgr.EnsureSession(SessionConfig{}); !errors.Is(err, ErrIncompleteCredentials) {
		t.Fatalf("EnsureSession 遇单边自定义凭据应拒绝启动，实际 %v", err)
	}
}

func TestExchangeAndRefreshRejectEmptyAccessToken(t *testing.T) {
	setupTestOAuthEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(TokenResponse{
			AccessToken:  "   ",
			RefreshToken: "rt-1",
			ExpiresIn:    3600,
		})
	}))
	defer srv.Close()

	client := &Client{
		HTTPClient: srv.Client(),
		TokenURL:   srv.URL,
	}
	if _, err := client.ExchangeCode(context.Background(), "code-1", "http://127.0.0.1:12345/oauth-callback"); err == nil {
		t.Fatal("ExchangeCode 收到空 access_token 应返回错误")
	}
	if _, err := client.RefreshAccessToken(context.Background(), "rt-1"); err == nil {
		t.Fatal("RefreshAccessToken 收到空 access_token 应返回错误")
	}
}
