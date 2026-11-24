package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHandleCallbackClearsSilentFlagOnAccessDeniedAndMissingCode(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "test-client-secret-placeholder")

	cases := []struct {
		name      string
		queryFmt  string
		wantInErr string
	}{
		{
			name:      "浏览器回调返回access_denied错误",
			queryFmt:  "%s?state=%s&error=access_denied",
			wantInErr: "access_denied",
		},
		{
			name:      "浏览器回调缺少code参数",
			queryFmt:  "%s?state=%s",
			wantInErr: "缺少 code",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewLoginManager()
			mgr.PostCallbackGrace = 0
			info, created, err := mgr.EnsureSession(SessionConfig{Silent: true})
			if err != nil || !created {
				t.Fatalf("创建静默预监听会话失败: created=%v err=%v", created, err)
			}
			defer mgr.FinishProcessing(info.State)

			if !mgr.IsSessionSilent(info.State) {
				t.Fatal("新建预监听会话初始应处于 silent=true 状态")
			}

			waitCh := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_, wErr := mgr.WaitForState(ctx, info.State)
				waitCh <- wErr
			}()

			cbURL := fmt.Sprintf(tc.queryFmt, info.RedirectURI, info.State)
			resp, err := http.Get(cbURL) //nolint:gosec // 本地测试回环地址
			if err != nil {
				t.Fatalf("发起本地回调请求失败: %v", err)
			}
			_ = resp.Body.Close()

			select {
			case wErr := <-waitCh:
				if wErr == nil || !strings.Contains(wErr.Error(), tc.wantInErr) {
					t.Fatalf("期望 WaitForState 返回包含 %q 的错误，实际: %v", tc.wantInErr, wErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("等待 WaitForState 返回超时")
			}

			if mgr.IsSessionSilent(info.State) {
				t.Fatalf("%s 后 IsSessionSilent 必须为 false，绝不能静默吞错", tc.name)
			}
		})
	}
}

func TestWaitForStateTimeoutDistinguishesSilentAndUserInitiated(t *testing.T) {
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_ID", "test-client-id.apps.googleusercontent.com")
	t.Setenv("MORE_ACCOUNT_OAUTH_CLIENT_SECRET", "test-client-secret-placeholder")

	// 1. 纯静默预监听超时：返回 ErrOAuthTimeout 且 IsSessionSilent 保持 true
	mgr1 := NewLoginManager()
	info1, _, err := mgr1.EnsureSession(SessionConfig{Silent: true})
	if err != nil {
		t.Fatalf("创建静默会话失败: %v", err)
	}
	ctx1, cancel1 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel1()
	if _, err := mgr1.WaitForState(ctx1, info1.State); !errors.Is(err, ErrOAuthTimeout) {
		t.Fatalf("期望静默超时返回 ErrOAuthTimeout，实际: %v", err)
	}
	if !mgr1.IsSessionSilent(info1.State) {
		t.Fatal("未主动发起的静默会话超时后 IsSessionSilent 应返回 true")
	}
	mgr1.FinishProcessing(info1.State)

	// 2. 经 MarkUserInitiated 升级后超时：返回 ErrOAuthTimeout 且 IsSessionSilent 为 false
	mgr2 := NewLoginManager()
	info2, _, err := mgr2.EnsureSession(SessionConfig{Silent: true})
	if err != nil {
		t.Fatalf("创建静默会话失败: %v", err)
	}
	mgr2.MarkUserInitiated(info2.State)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if _, err := mgr2.WaitForState(ctx2, info2.State); !errors.Is(err, ErrOAuthTimeout) {
		t.Fatalf("期望主动发起超时返回 ErrOAuthTimeout，实际: %v", err)
	}
	if mgr2.IsSessionSilent(info2.State) {
		t.Fatal("已标记用户主动发起的会话超时后 IsSessionSilent 必须为 false")
	}
	mgr2.FinishProcessing(info2.State)
}

func TestLoginManagerDualStackCallbackReachableAndCleanHTTPHeaders(t *testing.T) {
	setupTestOAuthEnv(t)

	mgr := NewLoginManager()
	mgr.PostCallbackGrace = 300 * time.Millisecond
	sess, err := mgr.StartSession()
	if err != nil {
		t.Fatalf("启动 OAuth 会话失败: %v", err)
	}
	defer mgr.Cancel()

	parsed, err := url.Parse(sess.RedirectURI)
	if err != nil {
		t.Fatalf("解析 RedirectURI 失败: %v", err)
	}
	port := parsed.Port()
	if port == "" {
		t.Fatalf("RedirectURI 缺少端口: %s", sess.RedirectURI)
	}

	// 验证 AuthURL 中包含的 redirect_uri、协议与端口与当前监听完全一致
	authParsed, err := url.Parse(sess.AuthURL)
	if err != nil {
		t.Fatalf("解析 AuthURL 失败: %v", err)
	}
	if gotRedirect := authParsed.Query().Get("redirect_uri"); gotRedirect != sess.RedirectURI {
		t.Fatalf("AuthURL 中的 redirect_uri (%q) 与监听地址 (%q) 不一致", gotRedirect, sess.RedirectURI)
	}
	if parsed.Scheme != "http" {
		t.Fatalf("本地回调协议必须为 http，实际 %q", parsed.Scheme)
	}

	// 验证通过 127.0.0.1、localhost 与 [::1]（若系统支持 IPv6 回环）均可访问同一会话根路径并收到明确 Content-Length 与 Connection: close
	targets := []string{
		fmt.Sprintf("http://127.0.0.1:%s/", port),
		fmt.Sprintf("http://localhost:%s/", port),
	}
	if supportsIPv6Loopback() {
		targets = append(targets, fmt.Sprintf("http://[::1]:%s/", port))
	}

	client := &http.Client{Timeout: 2 * time.Second}
	for _, target := range targets {
		resp, reqErr := client.Get(target) //nolint:gosec // 本地测试回环地址
		if reqErr != nil {
			t.Fatalf("请求 %s 失败: %v", target, reqErr)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Fatalf("请求 %s 期望 200，实际 %d", target, resp.StatusCode)
		}
		if !resp.Close {
			t.Fatalf("请求 %s 期望响应关闭连接 (Connection: close)，实际未关闭", target)
		}
		if len(resp.TransferEncoding) != 0 {
			t.Fatalf("请求 %s 不应使用分块传输，实际 TransferEncoding=%v", target, resp.TransferEncoding)
		}
		if gotLen, convErr := strconv.Atoi(resp.Header.Get("Content-Length")); convErr != nil || gotLen != len(body) {
			t.Fatalf("请求 %s Content-Length (%q) 与响应体长度 %d 不匹配", target, resp.Header.Get("Content-Length"), len(body))
		}
	}

	// 模拟真实浏览器通过 127.0.0.1 发起最终 OAuth 回调（即便会话默认为 localhost 也能命中）
	waitDone := make(chan string, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if code, wErr := mgr.WaitForState(ctx, sess.State); wErr == nil {
			waitDone <- code
		}
	}()

	cbResp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%s/oauth-callback?state=%s&code=dual-stack-code", port, url.QueryEscape(sess.State))) //nolint:gosec // 本地测试回环地址
	if err != nil {
		t.Fatalf("发起 127.0.0.1 回调请求失败: %v", err)
	}
	_ = cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusOK || !cbResp.Close {
		t.Fatalf("回调响应异常: status=%d close=%v", cbResp.StatusCode, cbResp.Close)
	}

	select {
	case got := <-waitDone:
		if got != "dual-stack-code" {
			t.Fatalf("期望收到 dual-stack-code，实际 %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待 WaitForState 返回超时")
	}
}

func TestLoginManagerCallbackHostSelectionAndFallback(t *testing.T) {
	setupTestOAuthEnv(t)

	origLookup := lookupHostIPs
	defer func() {
		lookupHostIPs = origLookup
	}()

	// 1. 若 localhost 被 Fake-IP DNS 或 hosts 解析为非回环地址，自动模式必须回退 127.0.0.1
	lookupHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("198.18.0.1")}, nil
	}
	mgrFakeIP := NewLoginManager()
	defer mgrFakeIP.Cancel()
	sFake, err := mgrFakeIP.StartSession()
	if err != nil {
		t.Fatalf("Fake-IP 场景启动会话失败: %v", err)
	}
	if sFake.CallbackHost != CallbackHostLoopback || !strings.HasPrefix(sFake.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("Fake-IP 解析时应自动回退 127.0.0.1，实际 host=%q uri=%q", sFake.CallbackHost, sFake.RedirectURI)
	}

	// 2. 若 localhost 正常解析为回环 IP，自动模式优先选择 localhost（兼容 PAC 与本地代理自动直连放行）
	lookupHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	mgrAuto := NewLoginManager()
	defer mgrAuto.Cancel()
	sAuto, err := mgrAuto.StartSession()
	if err != nil {
		t.Fatalf("正常回环解析启动会话失败: %v", err)
	}
	if sAuto.CallbackHost != CallbackHostLocalhost || !strings.HasPrefix(sAuto.RedirectURI, "http://localhost:") {
		t.Fatalf("正常回环解析时默认应使用 localhost，实际 host=%q uri=%q", sAuto.CallbackHost, sAuto.RedirectURI)
	}

	// 3. 显式切换为 127.0.0.1 与切回 localhost
	info127, err := mgrAuto.RestartSessionIfWaiting(SessionConfig{CallbackHost: CallbackHostLoopback})
	if err != nil {
		t.Fatalf("切换至 127.0.0.1 失败: %v", err)
	}
	if info127.CallbackHost != CallbackHostLoopback || !strings.HasPrefix(info127.RedirectURI, "http://127.0.0.1:") {
		t.Fatalf("切换主机未生效: %+v", info127)
	}

	infoLocal, err := mgrAuto.RestartSessionIfWaiting(SessionConfig{CallbackHost: CallbackHostLocalhost})
	if err != nil {
		t.Fatalf("切换回 localhost 失败: %v", err)
	}
	if infoLocal.CallbackHost != CallbackHostLocalhost || !strings.HasPrefix(infoLocal.RedirectURI, "http://localhost:") {
		t.Fatalf("切回 localhost 未生效: %+v", infoLocal)
	}

	// 4. 非法回调主机应被明确拒绝
	if _, _, err := mgrAuto.EnsureSession(SessionConfig{CallbackHost: "evil.example.com"}); err == nil {
		t.Fatal("非法 CallbackHost 应当报错")
	}

	// 5. canUseLocalhostCallback 在 localhost 包含 ::1 且 hasIPv6=false 时必须回退 false
	lookupHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")}, nil
	}
	if canUseLocalhostCallback(false) {
		t.Fatal("未绑定 IPv6 监听且 localhost 包含 ::1 时必须回退 127.0.0.1")
	}
	if !canUseLocalhostCallback(true) {
		t.Fatal("已成功绑定双栈监听时应允许使用 localhost")
	}
}

func TestLoginManagerTLSUpgradeDetectedWithoutStalling(t *testing.T) {
	setupTestOAuthEnv(t)

	mgr := NewLoginManager()
	mgr.PostCallbackGrace = 0
	tlsEventCh := make(chan string, 1)
	mgr.OnTLSDetected = func(state, callbackHost string) {
		select {
		case tlsEventCh <- state + "|" + callbackHost:
		default:
		}
	}

	info, _, err := mgr.EnsureSession(SessionConfig{CallbackHost: CallbackHostLocalhost})
	if err != nil {
		t.Fatalf("创建 OAuth 会话失败: %v", err)
	}
	defer mgr.Cancel()

	parsed, _ := url.Parse(info.RedirectURI)
	conn, err := net.DialTimeout("tcp", "127.0.0.1:"+parsed.Port(), time.Second)
	if err != nil {
		t.Fatalf("连接本地监听端口失败: %v", err)
	}

	// 发送无换行符的 TLS ClientHello 记录头（0x16），验证立即检测并关闭连接（不阻塞 10s ReadHeaderTimeout）
	started := time.Now()
	_, _ = conn.Write([]byte{0x16, 0x03, 0x01, 0x00, 0x05, 0x01, 0x00, 0x00, 0x01, 0x00})
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 16)
	_, readErr := conn.Read(buf)
	_ = conn.Close()
	if readErr == nil {
		t.Fatal("发送 TLS 握手后连接应当被立即关闭")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("TLS 握手检测应当快速返回，实际耗时 %v", elapsed)
	}

	select {
	case got := <-tlsEventCh:
		if got != info.State+"|"+CallbackHostLocalhost {
			t.Fatalf("OnTLSDetected 参数不匹配: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("未触发 OnTLSDetected 诊断回调")
	}

	cur, ok := mgr.CurrentSessionInfo()
	if !ok || !cur.TLSDetected {
		t.Fatalf("期望会话快照记录 TLSDetected=true，实际 ok=%v cur=%+v", ok, cur)
	}

	// 收到 TLS 探测后原监听会话必须依然健壮存活，可正常接收后续 HTTP 回调
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/oauth-callback?state=%s&code=after-tls", parsed.Port(), url.QueryEscape(info.State))) //nolint:gosec // 本地测试回环地址
	if err != nil {
		t.Fatalf("TLS 探测后合法 HTTP 回调失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", resp.StatusCode)
	}
}

func TestLoginManagerPostCallbackGracePreservedThroughFinishProcessing(t *testing.T) {
	setupTestOAuthEnv(t)

	mgr := NewLoginManager()
	mgr.PostCallbackGrace = 250 * time.Millisecond
	info, _, err := mgr.EnsureSession(SessionConfig{CallbackHost: CallbackHostLoopback})
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	defer mgr.Cancel()

	resp, err := http.Get(fmt.Sprintf("%s?state=%s&code=grace-code", info.RedirectURI, url.QueryEscape(info.State))) //nolint:gosec // 本地测试回环地址
	if err != nil {
		t.Fatalf("发送回调请求失败: %v", err)
	}
	_ = resp.Body.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := mgr.WaitForState(ctx, info.State); err != nil {
		t.Fatalf("WaitForState 失败: %v", err)
	}

	// 即使后台快速完成并调用 FinishProcessing，缓冲窗口内浏览器二次刷新或 /favicon.ico 仍可收到正常响应
	mgr.FinishProcessing(info.State)
	if _, ok := mgr.CurrentSessionInfo(); ok {
		t.Fatal("FinishProcessing 后当前会话引用应立即置空")
	}

	parsed, _ := url.Parse(info.RedirectURI)
	favResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/favicon.ico", parsed.Port())) //nolint:gosec // 本地测试回环地址
	if err != nil {
		t.Fatalf("缓冲期内 /favicon.ico 请求不应立即断开: %v", err)
	}
	_ = favResp.Body.Close()
	if favResp.StatusCode != http.StatusNoContent {
		t.Fatalf("期望 204，实际 %d", favResp.StatusCode)
	}
}

func TestLoginManagerHangingConnectionDoesNotBlockSessionReplacement(t *testing.T) {
	setupTestOAuthEnv(t)

	mgr := NewLoginManager()
	mgr.PostCallbackGrace = 0
	s1, err := mgr.StartSessionWithConfig(SessionConfig{CallbackHost: CallbackHostLoopback})
	if err != nil {
		t.Fatalf("启动会话 1 失败: %v", err)
	}
	defer mgr.Cancel()

	parsed1, _ := url.Parse(s1.RedirectURI)
	hangingConn, err := net.DialTimeout("tcp", "127.0.0.1:"+parsed1.Port(), time.Second)
	if err != nil {
		t.Fatalf("建立慢连接失败: %v", err)
	}
	defer hangingConn.Close()
	// 发送半截 HTTP 请求行占用连接，模拟浏览器未完成请求时用户点击切换主机/换新
	_, _ = hangingConn.Write([]byte("GET /oauth-callback"))

	started := time.Now()
	s2, err := mgr.RestartSessionIfWaiting(SessionConfig{CallbackHost: CallbackHostLocalhost})
	if err != nil {
		t.Fatalf("替换会话失败: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("旧会话存在挂起连接时切换会话不应阻塞 Shutdown 超时，实际耗时 %v", elapsed)
	}
	if s2.State == s1.State {
		t.Fatal("重建会话应生成全新 state")
	}

	// 验证新会话立即可用，且 processing 期间拒绝强行重建
	parsed2, _ := url.Parse(s2.RedirectURI)
	rootResp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%s/", parsed2.Port())) //nolint:gosec // 本地测试回环地址
	if err != nil {
		t.Fatalf("新会话应立即可响应请求: %v", err)
	}
	_ = rootResp.Body.Close()

	mgr.MarkProcessing(s2.State)
	if _, err := mgr.RestartSessionIfWaiting(SessionConfig{CallbackHost: CallbackHostLoopback}); !errors.Is(err, ErrOAuthProcessing) {
		t.Fatalf("processing 阶段切换主机应返回 ErrOAuthProcessing，实际 %v", err)
	}
}

func TestBuildAuthURLSafeForExternalBrowserAndHTMLEscape(t *testing.T) {
	setupTestOAuthEnv(t)
	raw := BuildAuthURLWithClientAndPKCE("client-id.apps.googleusercontent.com", "http://localhost:54321/oauth-callback", "state-123", "challenge_abc-123")
	for _, forbidden := range []string{" ", "\t", "\n", "\r", ";", "|", "`", "$", "\\", "<", ">", "(", ")"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("生成的 AuthURL 包含系统浏览器校验/Shell 敏感字符 %q: %s", forbidden, raw)
		}
	}
	rendered := callbackHTML(false, "<script>alert(1)</script>")
	if strings.Contains(rendered, "<script>") || !strings.Contains(rendered, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("callbackHTML 未对消息执行 HTML 转义: %s", rendered)
	}
}
