package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrOAuthCanceled 表示用户主动取消或由新会话替代了旧的 OAuth 授权等待。
var ErrOAuthCanceled = errors.New("已取消 OAuth 授权")

// LoginSession 表示一次进行中的浏览器 OAuth 授权会话。
type LoginSession struct {
	AuthURL      string `json:"authUrl"`
	RedirectURI  string `json:"redirectUri"`
	State        string `json:"state"`
	CodeVerifier string `json:"-"`
}

type activeSession struct {
	authURL      string
	state        string
	codeVerifier string
	redirectURI  string
	server       *http.Server
	listeners    []net.Listener
	codeCh       chan string
	errCh        chan error
	cancelCh     chan struct{}
	completed    atomic.Bool
	deliverOnce  sync.Once
	cancelOnce   sync.Once
	closeOnce    sync.Once
}

func (a *activeSession) toLoginSession() LoginSession {
	return LoginSession{
		AuthURL:      a.authURL,
		RedirectURI:  a.redirectURI,
		State:        a.state,
		CodeVerifier: a.codeVerifier,
	}
}

func (a *activeSession) deliverCode(code string) {
	a.deliverOnce.Do(func() {
		a.completed.Store(true)
		a.codeCh <- code
	})
}

func (a *activeSession) deliverErr(err error) {
	a.deliverOnce.Do(func() {
		a.errCh <- err
	})
}

func (a *activeSession) signalCancel() {
	a.cancelOnce.Do(func() {
		close(a.cancelCh)
	})
}

func (a *activeSession) shutdownServer() {
	a.closeOnce.Do(func() {
		if a.server != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = a.server.Shutdown(ctx)
		}
		for _, ln := range a.listeners {
			_ = ln.Close()
		}
	})
}

func (a *activeSession) close() {
	a.signalCancel()
	a.shutdownServer()
}

// LoginManager 管理单活的本地 OAuth 回调服务，并支持手动粘贴回调 URL 兜底。
type LoginManager struct {
	mu                  sync.Mutex
	current             *activeSession
	CredentialsProvider func() (clientID, clientSecret string)
	// PostCallbackGrace 收到合法回调并向浏览器写出成功页面后，额外保留本地监听的短暂缓冲时长，
	// 防止浏览器二次刷新或重定向重试时立即遇到连接断开。
	PostCallbackGrace time.Duration
}

// NewLoginManager 创建登录会话管理器。
func NewLoginManager() *LoginManager {
	return &LoginManager{
		PostCallbackGrace: 1500 * time.Millisecond,
	}
}

func (m *LoginManager) resolveClientID() string {
	var customID, customSecret string
	if m.CredentialsProvider != nil {
		customID, customSecret = m.CredentialsProvider()
	}
	id, _ := ResolveCredentials(customID, customSecret)
	return id
}

func randomState() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("st_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// CurrentSession 返回当前仍处于活跃监听中的 OAuth 会话（若存在）。
func (m *LoginManager) CurrentSession() (LoginSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return LoginSession{}, false
	}
	return m.current.toLoginSession(), true
}

// StartSession 启动新的本地回调监听服务并返回授权页链接（会立即关闭旧的未完成会话）。
func (m *LoginManager) StartSession() (LoginSession, error) {
	clientID := strings.TrimSpace(m.resolveClientID())
	if clientID == "" {
		return LoginSession{}, fmt.Errorf("尚未配置 Google OAuth Client ID，请先在「设置」或本地 .env.local 中配置")
	}

	m.Cancel()

	ipv4Listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		ipv4Listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return LoginSession{}, fmt.Errorf("无法监听本地回调端口: %w", err)
		}
	}

	addr := ipv4Listener.Addr().(*net.TCPAddr)
	listeners := []net.Listener{ipv4Listener}
	// 同时尽力绑定同端口 IPv6 回环 [::1]，确保浏览器无论访问 127.0.0.1 还是 localhost 均可命中回调服务
	if ipv6Listener, v6Err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", addr.Port)); v6Err == nil {
		listeners = append(listeners, ipv6Listener)
	}

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/oauth-callback", addr.Port)
	state := randomState()
	verifier, challenge := GeneratePKCE()
	authURL := BuildAuthURLWithClientAndPKCE(clientID, redirectURI, state, challenge)

	sess := &activeSession{
		authURL:      authURL,
		state:        state,
		codeVerifier: verifier,
		redirectURI:  redirectURI,
		listeners:    listeners,
		codeCh:       make(chan string, 1),
		errCh:        make(chan error, 1),
		cancelCh:     make(chan struct{}),
	}

	handleCallback := func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Method, http.MethodOptions) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
			return
		}

		if sess.completed.Load() {
			writeCallbackHTML(w, http.StatusOK, true, "授权回调已收到，More Account 正在完成账号入库，您可以关闭此标签页。")
			return
		}

		q := r.URL.Query()
		// 第一步强制校验 state 非空且与当前会话一致：任何缺少或不匹配 state 的本地扫描/预取/跨站请求仅返回 400，
		// 绝不消费 deliverOnce 或销毁当前等待 Google 合法重定向的授权会话（防止本地跨站 DoS 与 Login CSRF）。
		if gotState := strings.TrimSpace(q.Get("state")); gotState == "" || gotState != sess.state {
			writeCallbackHTML(w, http.StatusBadRequest, false, "安全校验失败（state 缺失或不匹配），请返回原有授权页面完成操作。")
			return
		}
		if errParam := strings.TrimSpace(q.Get("error")); errParam != "" {
			writeCallbackHTML(w, http.StatusBadRequest, false, "授权失败，请返回 More Account 查看详情。")
			sess.deliverErr(fmt.Errorf("Google 授权被拒绝: %s", errParam))
			return
		}
		code := strings.TrimSpace(q.Get("code"))
		if code == "" {
			writeCallbackHTML(w, http.StatusBadRequest, false, "回调参数缺少 code，请重试。")
			sess.deliverErr(fmt.Errorf("Google 授权回调缺少 code 参数"))
			return
		}

		// 先将成功页面完整写出并 Flush 给浏览器，再通知后台协程开始换取令牌，彻底消除响应与停服竞态
		writeCallbackHTML(w, http.StatusOK, true, "授权成功！您可以关闭此浏览器标签页并返回 More Account。")
		sess.deliverCode(code)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", handleCallback)
	mux.HandleFunc("/oauth-callback/", handleCallback)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Method, http.MethodOptions) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.WriteHeader(http.StatusOK)
			return
		}
		// 若某些浏览器或代理改写了路径但保留了 state 与 code/error 查询参数，依然交给回调处理器兼容接管
		q := r.URL.Query()
		if strings.TrimSpace(q.Get("state")) != "" && (strings.TrimSpace(q.Get("code")) != "" || strings.TrimSpace(q.Get("error")) != "") {
			handleCallback(w, r)
			return
		}
		if r.URL.Path == "/" {
			if sess.completed.Load() {
				writeCallbackHTML(w, http.StatusOK, true, "授权回调已完成，您可以返回 More Account 查看账号列表。")
				return
			}
			writeCallbackHTML(w, http.StatusOK, true, "More Account 本地授权回调服务已就绪，请在打开的 Google 授权页面完成登录。")
			return
		}
		writeCallbackHTML(w, http.StatusNotFound, false, "未匹配到授权回调路径，请确认回调地址为 /oauth-callback。")
	})

	sess.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	m.mu.Lock()
	prev := m.current
	m.current = sess
	m.mu.Unlock()
	if prev != nil {
		prev.close()
	}

	for _, ln := range listeners {
		listener := ln
		go func() {
			_ = sess.server.Serve(listener)
		}()
	}

	return sess.toLoginSession(), nil
}

// WaitForState 按指定 state 绑定等待对应会话，退出时仅清理自身会话，绝不误杀并发重启的新会话。
func (m *LoginManager) WaitForState(ctx context.Context, expectedState string) (string, error) {
	m.mu.Lock()
	sess := m.current
	grace := m.PostCallbackGrace
	m.mu.Unlock()
	if sess == nil {
		if expectedState != "" {
			return "", ErrOAuthCanceled
		}
		return "", fmt.Errorf("当前没有进行中的 OAuth 授权会话")
	}
	if expectedState != "" && sess.state != expectedState {
		return "", ErrOAuthCanceled
	}

	select {
	case code := <-sess.codeCh:
		m.detachIfCurrent(sess)
		if grace > 0 {
			time.AfterFunc(grace, sess.shutdownServer)
		} else {
			sess.shutdownServer()
		}
		return code, nil
	case err := <-sess.errCh:
		m.cancelIfCurrent(sess)
		return "", err
	case <-sess.cancelCh:
		return "", ErrOAuthCanceled
	case <-ctx.Done():
		m.cancelIfCurrent(sess)
		return "", fmt.Errorf("等待 OAuth 授权超时")
	}
}

func (m *LoginManager) detachIfCurrent(target *activeSession) {
	m.mu.Lock()
	if m.current == target {
		m.current = nil
	}
	m.mu.Unlock()
}

func (m *LoginManager) cancelIfCurrent(target *activeSession) {
	m.detachIfCurrent(target)
	if target != nil {
		target.close()
	}
}

// SubmitCallbackURL 允许用户手动粘贴浏览器重定向 URL（兼容完整链接、相对路径或纯查询参数）以完成回调。
func (m *LoginManager) SubmitCallbackURL(rawURL string) error {
	m.mu.Lock()
	sess := m.current
	m.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("没有等待中的 OAuth 授权会话，请先点击开始授权")
	}

	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("回调链接不能为空")
	}

	candidate := trimmed
	if !strings.HasPrefix(candidate, "http://") && !strings.HasPrefix(candidate, "https://") {
		if strings.HasPrefix(candidate, "/") {
			candidate = "http://127.0.0.1" + candidate
		} else {
			candidate = sess.redirectURI + "?" + strings.TrimPrefix(candidate, "?")
		}
	}

	parsed, err := url.Parse(candidate)
	if err != nil {
		return fmt.Errorf("无法解析回调链接: %w", err)
	}
	q := parsed.Query()
	if gotState := strings.TrimSpace(q.Get("state")); gotState == "" || gotState != sess.state {
		return fmt.Errorf("回调链接缺少 state 或与本次授权不匹配，请确认粘贴了正确的回调地址")
	}
	if errParam := strings.TrimSpace(q.Get("error")); errParam != "" {
		sess.deliverErr(fmt.Errorf("Google 授权返回错误: %s", errParam))
		return nil
	}
	code := strings.TrimSpace(q.Get("code"))
	if code == "" {
		return fmt.Errorf("回调链接中缺少 code 参数")
	}
	sess.deliverCode(code)
	return nil
}

// Cancel 中止当前进行中的授权监听服务。
func (m *LoginManager) Cancel() {
	m.mu.Lock()
	sess := m.current
	m.current = nil
	m.mu.Unlock()

	if sess != nil {
		sess.close()
	}
}

func writeCallbackHTML(w http.ResponseWriter, statusCode int, ok bool, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_, _ = w.Write([]byte(callbackHTML(ok, message)))
	if flusher, okFlush := w.(http.Flusher); okFlush {
		flusher.Flush()
	}
}

func callbackHTML(ok bool, message string) string {
	color := "#2563EB"
	title := "授权已完成"
	if !ok {
		color = "#EF4444"
		title = "授权未完成"
	}
	return fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<title>%s - More Account</title>
<style>
  body { margin:0; font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif; background:#f7f8fa; color:#111827; display:flex; align-items:center; justify-content:center; height:100vh; }
  .card { background:#fff; border:1px solid #e5e7eb; border-radius:14px; padding:32px 36px; max-width:420px; text-align:center; box-shadow:0 10px 30px rgba(0,0,0,0.06); }
  h1 { margin:0 0 10px; font-size:20px; color:%s; }
  p { margin:0; font-size:14px; line-height:1.6; color:#4b5563; }
</style>
</head>
<body>
  <div class="card">
    <h1>%s</h1>
    <p>%s</p>
  </div>
</body>
</html>`, title, color, title, message)
}
