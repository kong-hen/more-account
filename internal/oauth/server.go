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
	state        string
	codeVerifier string
	redirectURI  string
	server       *http.Server
	codeCh       chan string
	errCh        chan error
	cancelCh     chan struct{}
	once         sync.Once
}

func (a *activeSession) deliverCode(code string) {
	a.once.Do(func() {
		a.codeCh <- code
	})
}

func (a *activeSession) deliverErr(err error) {
	a.once.Do(func() {
		a.errCh <- err
	})
}

func (a *activeSession) close() {
	a.once.Do(func() {
		close(a.cancelCh)
	})
	if a.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.server.Shutdown(ctx)
	}
}

// LoginManager 管理单活的本地 OAuth 回调服务，并支持手动粘贴回调 URL 兜底。
type LoginManager struct {
	mu      sync.Mutex
	current *activeSession
}

// NewLoginManager 创建登录会话管理器。
func NewLoginManager() *LoginManager {
	return &LoginManager{}
}

func randomState() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("st_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// StartSession 启动本地临时回调服务并返回授权页链接。
func (m *LoginManager) StartSession() (LoginSession, error) {
	m.Cancel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return LoginSession{}, fmt.Errorf("无法监听本地回调端口: %w", err)
	}

	addr := listener.Addr().(*net.TCPAddr)
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/oauth-callback", addr.Port)
	state := randomState()
	verifier, challenge := GeneratePKCE()

	sess := &activeSession{
		state:        state,
		codeVerifier: verifier,
		redirectURI:  redirectURI,
		codeCh:       make(chan string, 1),
		errCh:        make(chan error, 1),
		cancelCh:     make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/oauth-callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// 第一步强制校验 state 非空且与当前会话一致：任何缺少或不匹配 state 的本地扫描/预取/跨站请求仅返回 400，
		// 绝不消费 sync.Once 或销毁当前等待 Google 合法重定向的授权会话（防止本地跨站 DoS 与 Login CSRF）。
		if gotState := strings.TrimSpace(q.Get("state")); gotState == "" || gotState != sess.state {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(callbackHTML(false, "安全校验失败（state 缺失或不匹配），请返回原有授权页面完成操作。")))
			return
		}
		if errParam := strings.TrimSpace(q.Get("error")); errParam != "" {
			sess.deliverErr(fmt.Errorf("Google 授权被拒绝: %s", errParam))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(callbackHTML(false, "授权失败，请返回 More Account 查看详情。")))
			return
		}
		code := strings.TrimSpace(q.Get("code"))
		if code == "" {
			sess.deliverErr(fmt.Errorf("Google 授权回调缺少 code 参数"))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(callbackHTML(false, "回调参数缺少 code，请重试。")))
			return
		}
		sess.deliverCode(code)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(callbackHTML(true, "授权成功！您可以关闭此浏览器标签页并返回 More Account。")))
	})

	sess.server = &http.Server{Handler: mux}

	m.mu.Lock()
	prev := m.current
	m.current = sess
	m.mu.Unlock()
	if prev != nil {
		prev.close()
	}

	go func() {
		_ = sess.server.Serve(listener)
	}()

	return LoginSession{
		AuthURL:      BuildAuthURLWithPKCE(redirectURI, state, challenge),
		RedirectURI:  redirectURI,
		State:        state,
		CodeVerifier: verifier,
	}, nil
}

// WaitForState 按指定 state 绑定等待对应会话，退出时仅清理自身会话，绝不误杀并发重启的新会话。
func (m *LoginManager) WaitForState(ctx context.Context, expectedState string) (string, error) {
	m.mu.Lock()
	sess := m.current
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
		m.cancelIfCurrent(sess)
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

func (m *LoginManager) cancelIfCurrent(target *activeSession) {
	m.mu.Lock()
	if m.current == target {
		m.current = nil
	}
	m.mu.Unlock()
	if target != nil {
		target.close()
	}
}

// SubmitCallbackURL 允许用户手动粘贴浏览器重定向 URL 以完成回调（兜底方案）。
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
	parsed, err := url.Parse(trimmed)
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
