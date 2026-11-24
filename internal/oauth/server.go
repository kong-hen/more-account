package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// CallbackHostLocalhost 使用 localhost 作为 OAuth 本地回调域名，兼容常见 PAC 与本地代理规则的自动直连放行。
	CallbackHostLocalhost = "localhost"
	// CallbackHostLoopback 使用 IPv4 回环地址 127.0.0.1 作为 OAuth 本地回调地址，避免 Fake-IP DNS 或 localhost HSTS 干扰。
	CallbackHostLoopback = "127.0.0.1"
)

var errTLSHandshakeOnHTTP = errors.New("收到 HTTPS TLS 握手请求，本地 OAuth 回调仅支持 HTTP")

// ErrOAuthCanceled 表示用户主动取消或由新会话替代了旧的 OAuth 授权等待。
var ErrOAuthCanceled = errors.New("已取消 OAuth 授权")

// ErrOAuthTimeout 表示等待 OAuth 授权回调超过预设时限。
var ErrOAuthTimeout = errors.New("等待 OAuth 授权超时")

// ErrOAuthProcessing 表示当前授权会话已收到回调，正在后台换取令牌并同步账号信息。
var ErrOAuthProcessing = errors.New("回调已提交，正在换取令牌并同步账号信息，请稍候")

const (
	// SessionStatusWaiting 表示本地回调监听已就绪，正在等待浏览器回跳或手动粘贴回调链接。
	SessionStatusWaiting = "waiting"
	// SessionStatusProcessing 表示已收到授权码，正在后台换取令牌、获取用户信息并入库。
	SessionStatusProcessing = "processing"
)

// LoginSession 表示一次进行中的浏览器 OAuth 授权会话。
type LoginSession struct {
	AuthURL      string `json:"authUrl"`
	RedirectURI  string `json:"redirectUri"`
	State        string `json:"state"`
	CallbackHost string `json:"callbackHost,omitempty"`
	Status       string `json:"status,omitempty"`
	CodeVerifier string `json:"-"`
}

// SessionConfig 配置原子创建或复用 OAuth 授权会话时的可选行为。
type SessionConfig struct {
	// Silent 标识该会话为弹窗打开时的自动预监听（用户尚未主动点击授权、复制链接或提交回调）。
	Silent bool
	// CallbackHost 指定本地回调域名或地址（"localhost" / "127.0.0.1"，留空时自动探测最优回环地址）。
	CallbackHost string
}

// SessionInfo 描述当前活跃 OAuth 会话的完整状态快照（waiting / processing）。
type SessionInfo struct {
	AuthURL      string `json:"authUrl"`
	RedirectURI  string `json:"redirectUri"`
	State        string `json:"state"`
	CallbackHost string `json:"callbackHost,omitempty"`
	Status       string `json:"status"`
	Silent       bool   `json:"silent,omitempty"`
	TLSDetected  bool   `json:"tlsDetected,omitempty"`
	CodeVerifier string `json:"-"`
}

// ToLoginSession 将完整会话快照转换为前端通用的 LoginSession 结构。
func (s *SessionInfo) ToLoginSession() LoginSession {
	if s == nil {
		return LoginSession{}
	}
	return LoginSession{
		AuthURL:      s.AuthURL,
		RedirectURI:  s.RedirectURI,
		State:        s.State,
		CallbackHost: s.CallbackHost,
		Status:       s.Status,
		CodeVerifier: s.CodeVerifier,
	}
}

type activeSession struct {
	authURL      string
	state        string
	codeVerifier string
	redirectURI  string
	callbackHost string
	server       *http.Server
	listeners    []net.Listener
	codeCh       chan string
	errCh        chan error
	cancelCh     chan struct{}
	statusMu     sync.RWMutex
	status       string
	silent       atomic.Bool
	completed    atomic.Bool
	tlsDetected  atomic.Bool
	deliverOnce  sync.Once
	cancelOnce   sync.Once
	closeOnce    sync.Once
	graceOnce    sync.Once
	tlsOnce      sync.Once
	graceTimerMu sync.Mutex
	graceTimer   *time.Timer
}

func (a *activeSession) getStatus() string {
	a.statusMu.RLock()
	defer a.statusMu.RUnlock()
	if a.status == "" {
		return SessionStatusWaiting
	}
	return a.status
}

func (a *activeSession) setStatus(status string) {
	a.statusMu.Lock()
	a.status = status
	a.statusMu.Unlock()
}

func (a *activeSession) toLoginSession() LoginSession {
	return LoginSession{
		AuthURL:      a.authURL,
		RedirectURI:  a.redirectURI,
		State:        a.state,
		CallbackHost: a.callbackHost,
		Status:       a.getStatus(),
		CodeVerifier: a.codeVerifier,
	}
}

func (a *activeSession) toSessionInfo() *SessionInfo {
	return &SessionInfo{
		AuthURL:      a.authURL,
		RedirectURI:  a.redirectURI,
		State:        a.state,
		CallbackHost: a.callbackHost,
		Status:       a.getStatus(),
		Silent:       a.silent.Load(),
		TLSDetected:  a.tlsDetected.Load(),
		CodeVerifier: a.codeVerifier,
	}
}

// deliverCode 仅允许首次合法授权码成功投递，同时原子切换会话为 processing 状态，避免重复提交误报。
func (a *activeSession) deliverCode(code string) bool {
	delivered := false
	a.deliverOnce.Do(func() {
		a.completed.Store(true)
		a.setStatus(SessionStatusProcessing)
		a.codeCh <- code
		delivered = true
	})
	return delivered
}

func (a *activeSession) deliverErr(err error) bool {
	delivered := false
	a.deliverOnce.Do(func() {
		a.errCh <- err
		delivered = true
	})
	return delivered
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
			if err := a.server.Shutdown(ctx); err != nil {
				// Shutdown 超时时强制关闭残留的活跃/挂起连接，防止监听句柄泄漏
				_ = a.server.Close()
			}
		}
		for _, ln := range a.listeners {
			_ = ln.Close()
		}
	})
}

func (a *activeSession) scheduleGraceShutdown(grace time.Duration) {
	if grace <= 0 {
		a.shutdownServer()
		return
	}
	a.graceOnce.Do(func() {
		t := time.AfterFunc(grace, a.shutdownServer)
		a.graceTimerMu.Lock()
		a.graceTimer = t
		a.graceTimerMu.Unlock()
	})
}

func (a *activeSession) stopGraceTimer() {
	a.graceTimerMu.Lock()
	if a.graceTimer != nil {
		a.graceTimer.Stop()
		a.graceTimer = nil
	}
	a.graceTimerMu.Unlock()
}

func (a *activeSession) close() {
	a.signalCancel()
	a.stopGraceTimer()
	a.shutdownServer()
}

// LoginManager 管理单活的本地 OAuth 回调服务，并支持手动粘贴回调 URL 兜底。
type LoginManager struct {
	// startMu 串行化会话的原子检查、创建与取消，避免并发打开导致双监听与误杀旧会话。
	startMu             sync.Mutex
	mu                  sync.Mutex
	current             *activeSession
	endedSilent         map[string]bool
	CredentialsProvider func() (clientID, clientSecret string)
	// OnTLSDetected 当本地纯 HTTP 回调端口收到浏览器发起的 TLS 握手字节（0x16）时触发，便于前端提示 HTTPS 升级冲突。
	OnTLSDetected func(state, callbackHost string)
	// PostCallbackGrace 收到合法回调并向浏览器写出成功页面后，额外保留本地监听的短暂缓冲时长，
	// 防止浏览器二次刷新或重定向重试时立即遇到连接断开。
	PostCallbackGrace time.Duration
}

// NewLoginManager 创建登录会话管理器。
func NewLoginManager() *LoginManager {
	return &LoginManager{
		endedSilent:       make(map[string]bool),
		PostCallbackGrace: 1500 * time.Millisecond,
	}
}

func (m *LoginManager) resolveCredentials() (string, string, error) {
	var customID, customSecret string
	if m.CredentialsProvider != nil {
		customID, customSecret = m.CredentialsProvider()
	}
	id, sec, err := ResolveCredentials(customID, customSecret)
	if err != nil {
		return "", "", err
	}
	if id == "" || sec == "" {
		return "", "", fmt.Errorf("尚未配置完整的 Google OAuth 客户端凭据（Client ID / Secret），请先在「设置」或本地 .env.local 中配置")
	}
	return id, sec, nil
}

func randomState() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("st_%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// CurrentSession 返回当前仍处于活跃等待或处理中的 OAuth 会话（若存在）。
func (m *LoginManager) CurrentSession() (LoginSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return LoginSession{}, false
	}
	return m.current.toLoginSession(), true
}

// CurrentSessionInfo 返回当前活跃 OAuth 会话的完整状态快照（含 waiting/processing 与静默标识）。
func (m *LoginManager) CurrentSessionInfo() (*SessionInfo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return nil, false
	}
	return m.current.toSessionInfo(), true
}

var lookupHostIPs = func(ctx context.Context, host string) ([]net.IP, error) {
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, nil
}

func normalizeCallbackHost(raw string) (string, error) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	switch trimmed {
	case "":
		return "", nil
	case CallbackHostLocalhost:
		return CallbackHostLocalhost, nil
	case CallbackHostLoopback:
		return CallbackHostLoopback, nil
	default:
		return "", fmt.Errorf("不支持的回调主机 %q（仅支持 localhost 或 127.0.0.1）", raw)
	}
}

func canUseLocalhostCallback(hasIPv6 bool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ips, err := lookupHostIPs(ctx, CallbackHostLocalhost)
	if err != nil || len(ips) == 0 {
		return false
	}
	hasV4Loopback := false
	for _, ip := range ips {
		if !ip.IsLoopback() {
			// 若 localhost 被 Fake-IP DNS 或异常 hosts 解析为非回环地址，自动回退 127.0.0.1
			return false
		}
		if ip.To4() != nil {
			hasV4Loopback = true
		} else if !hasIPv6 {
			// localhost 解析包含 ::1 但本机未成功绑定 IPv6 回环监听时，回退 127.0.0.1 避免浏览器命中空端口
			return false
		}
	}
	return hasV4Loopback || hasIPv6
}

func resolveCallbackHost(requested string, hasIPv6 bool) (string, error) {
	norm, err := normalizeCallbackHost(requested)
	if err != nil {
		return "", err
	}
	if norm != "" {
		return norm, nil
	}
	if canUseLocalhostCallback(hasIPv6) {
		return CallbackHostLocalhost, nil
	}
	return CallbackHostLoopback, nil
}

func supportsIPv6Loopback() bool {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// listenLoopbackDualStack 原子尝试在同一随机端口同时绑定 127.0.0.1 与 [::1]，避免 localhost 解析到 ::1 时端口未就绪。
func listenLoopbackDualStack() ([]net.Listener, int, bool, error) {
	hasIPv6Support := supportsIPv6Loopback()
	const maxAttempts = 8
	for attempt := 0; attempt < maxAttempts; attempt++ {
		ipv4Listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			ipv4Listener, err = net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, 0, false, fmt.Errorf("无法监听本地回调端口: %w", err)
			}
		}
		addr, ok := ipv4Listener.Addr().(*net.TCPAddr)
		if !ok || addr.Port <= 0 {
			_ = ipv4Listener.Close()
			return nil, 0, false, fmt.Errorf("无法获取有效的本地回调监听端口")
		}
		if !hasIPv6Support {
			return []net.Listener{ipv4Listener}, addr.Port, false, nil
		}
		ipv6Listener, v6Err := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", addr.Port))
		if v6Err == nil {
			return []net.Listener{ipv4Listener, ipv6Listener}, addr.Port, true, nil
		}
		if attempt+1 < maxAttempts {
			_ = ipv4Listener.Close()
			continue
		}
		return []net.Listener{ipv4Listener}, addr.Port, false, nil
	}
	return nil, 0, false, fmt.Errorf("无法绑定本地回调端口")
}

type tlsSniffListener struct {
	net.Listener
	onTLS func()
}

func (l *tlsSniffListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &tlsSniffConn{
		Conn:  conn,
		onTLS: l.onTLS,
	}, nil
}

type tlsSniffConn struct {
	net.Conn
	onTLS       func()
	checkedHead bool
}

func (c *tlsSniffConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if !c.checkedHead && n > 0 {
		c.checkedHead = true
		if p[0] == 0x16 {
			if c.onTLS != nil {
				c.onTLS()
			}
			_ = c.Conn.Close()
			return 0, errTLSHandshakeOnHTTP
		}
	}
	return n, err
}

// EnsureSession 原子检查并复用已有活跃会话（waiting 或 processing）；若无活跃会话或明确要求更换等待中的回调主机则创建新会话。
// 第二个返回值 created 为 true 时表示本次新建了监听会话，调用方需为其启动后台等待协程。
func (m *LoginManager) EnsureSession(cfg SessionConfig) (*SessionInfo, bool, error) {
	wantHost, err := normalizeCallbackHost(cfg.CallbackHost)
	if err != nil {
		return nil, false, err
	}
	cfg.CallbackHost = wantHost

	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	if m.current != nil {
		// 若已有会话由自动预监听升级为用户主动发起，立即清除静默标记
		if !cfg.Silent {
			m.current.silent.Store(false)
		}
		isProcessing := m.current.getStatus() == SessionStatusProcessing || m.current.completed.Load()
		sameHost := wantHost == "" || strings.EqualFold(m.current.callbackHost, wantHost)
		if isProcessing || sameHost {
			info := m.current.toSessionInfo()
			m.mu.Unlock()
			return info, false, nil
		}
	}
	m.mu.Unlock()

	sess, err := m.createAndActivateSessionLocked(cfg)
	if err != nil {
		return nil, false, err
	}
	return sess.toSessionInfo(), true, nil
}

// RestartSessionIfWaiting 原子替换仍处于 waiting 状态的旧会话并按新配置重建本地监听（例如切换 localhost / 127.0.0.1）。
// 若当前已有会话处于 processing 阶段，返回 ErrOAuthProcessing，绝不中断正在换取令牌的流程。
func (m *LoginManager) RestartSessionIfWaiting(cfg SessionConfig) (*SessionInfo, error) {
	wantHost, err := normalizeCallbackHost(cfg.CallbackHost)
	if err != nil {
		return nil, err
	}
	cfg.CallbackHost = wantHost

	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	if m.current != nil && (m.current.getStatus() == SessionStatusProcessing || m.current.completed.Load()) {
		m.mu.Unlock()
		return nil, ErrOAuthProcessing
	}
	m.mu.Unlock()

	sess, err := m.createAndActivateSessionLocked(cfg)
	if err != nil {
		return nil, err
	}
	return sess.toSessionInfo(), nil
}

// StartSession 启动新的本地回调监听服务并返回授权页链接（会立即关闭旧的未完成会话）。
func (m *LoginManager) StartSession() (LoginSession, error) {
	return m.StartSessionWithConfig(SessionConfig{Silent: false})
}

// StartSessionWithConfig 按指定会话配置启动新的本地回调监听服务并返回授权页链接。
func (m *LoginManager) StartSessionWithConfig(cfg SessionConfig) (LoginSession, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	cfg.Silent = false
	sess, err := m.createAndActivateSessionLocked(cfg)
	if err != nil {
		return LoginSession{}, err
	}
	return sess.toLoginSession(), nil
}

// createAndActivateSessionLocked 必须在持有 m.startMu 的情况下调用。
func (m *LoginManager) createAndActivateSessionLocked(cfg SessionConfig) (*activeSession, error) {
	clientID, _, err := m.resolveCredentials()
	if err != nil {
		return nil, err
	}

	listeners, port, hasIPv6, err := listenLoopbackDualStack()
	if err != nil {
		return nil, err
	}
	callbackHost, err := resolveCallbackHost(cfg.CallbackHost, hasIPv6)
	if err != nil {
		for _, ln := range listeners {
			_ = ln.Close()
		}
		return nil, err
	}

	redirectURI := fmt.Sprintf("http://%s:%d/oauth-callback", callbackHost, port)
	state := randomState()
	verifier, challenge := GeneratePKCE()
	authURL := BuildAuthURLWithClientAndPKCE(clientID, redirectURI, state, challenge)

	sess := &activeSession{
		authURL:      authURL,
		state:        state,
		codeVerifier: verifier,
		redirectURI:  redirectURI,
		callbackHost: callbackHost,
		listeners:    listeners,
		codeCh:       make(chan string, 1),
		errCh:        make(chan error, 1),
		cancelCh:     make(chan struct{}),
		status:       SessionStatusWaiting,
	}
	sess.silent.Store(cfg.Silent)

	handleCallback := func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Method, http.MethodOptions) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Connection", "close")
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
		// 通过 state 安全校验意味着已收到真实浏览器授权回调（无论授权成功、被拒绝还是缺 code），
		// 立即清除静默预监听标志，确保任何真实回调报错均向用户可见而绝不被静默吞掉。
		sess.silent.Store(false)
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
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Method, http.MethodOptions) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Connection", "close")
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
		ReadHeaderTimeout: 10 * time.Second,
	}
	// 关闭 HTTP Keep-Alive，避免浏览器复用或悬空持久连接拖延关停及引发二次重定向竞态
	sess.server.SetKeepAlivesEnabled(false)

	onTLS := func() {
		sess.tlsDetected.Store(true)
		sess.tlsOnce.Do(func() {
			if m.OnTLSDetected != nil {
				go m.OnTLSDetected(sess.state, sess.callbackHost)
			}
		})
	}

	m.mu.Lock()
	prev := m.current
	m.current = sess
	m.mu.Unlock()

	for _, ln := range listeners {
		listener := &tlsSniffListener{Listener: ln, onTLS: onTLS}
		go func() {
			_ = sess.server.Serve(listener)
		}()
	}

	if prev != nil {
		// 先同步发出取消信号使旧等待协程立即退出，再异步关闭旧服务，避免旧挂起连接阻塞新监听启动
		prev.signalCancel()
		prev.stopGraceTimer()
		go prev.shutdownServer()
	}

	return sess, nil
}

// MarkProcessing 显式将匹配 state 的当前活跃会话标记为处理中（processing）。
func (m *LoginManager) MarkProcessing(state string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return false
	}
	if state != "" && m.current.state != state {
		return false
	}
	m.current.setStatus(SessionStatusProcessing)
	return true
}

// FinishProcessing 在后台令牌交换与入库流程结束后清理匹配 state 的会话引用，绝不误删新会话。
func (m *LoginManager) FinishProcessing(state string) {
	m.mu.Lock()
	var target *activeSession
	if m.current != nil && (state == "" || m.current.state == state) {
		target = m.current
		m.current = nil
	}
	if state != "" && m.endedSilent != nil {
		delete(m.endedSilent, state)
	}
	grace := m.PostCallbackGrace
	m.mu.Unlock()

	if target != nil {
		target.signalCancel()
		if target.completed.Load() && grace > 0 {
			// 已通过回调或手动提交进入收尾时保留缓冲窗口，避免快速完成/失败时过早断开浏览器并发请求
			target.scheduleGraceShutdown(grace)
			return
		}
		target.close()
	}
}

// MarkUserInitiated 将匹配 state（或当前活跃会话）标记为用户主动发起的授权，清除自动预监听静默标记。
func (m *LoginManager) MarkUserInitiated(state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return
	}
	if state == "" || m.current.state == state {
		m.current.silent.Store(false)
	}
}

// IsSessionSilent 判断指定 state 的会话是否处于未主动发起授权的静默预监听态（兼容等待超时或出错后已脱离 m.current 的短时回查）。
func (m *LoginManager) IsSessionSilent(state string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil && (state == "" || m.current.state == state) {
		return m.current.silent.Load()
	}
	if state != "" && m.endedSilent != nil {
		return m.endedSilent[state]
	}
	return false
}

func (m *LoginManager) recordEndedSilent(sess *activeSession) {
	if sess == nil || sess.state == "" {
		return
	}
	m.mu.Lock()
	if m.endedSilent == nil {
		m.endedSilent = make(map[string]bool)
	}
	m.endedSilent[sess.state] = sess.silent.Load()
	m.mu.Unlock()
}

// WaitForState 按指定 state 绑定等待对应会话，收到授权码后切入 processing 状态并保留会话引用直至 FinishProcessing 清理。
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
		// 保持 m.current 处于 processing 状态，防止重开弹窗另起会话或重复提交误报“没有等待中的会话”
		sess.setStatus(SessionStatusProcessing)
		sess.scheduleGraceShutdown(grace)
		return code, nil
	case err := <-sess.errCh:
		m.recordEndedSilent(sess)
		m.cancelIfCurrent(sess)
		return "", err
	case <-sess.cancelCh:
		return "", ErrOAuthCanceled
	case <-ctx.Done():
		m.recordEndedSilent(sess)
		m.cancelIfCurrent(sess)
		return "", ErrOAuthTimeout
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

// normalizeManualCallbackCandidate 将用户手动粘贴的多种回调文本归一化为可解析的标准 HTTP URL。
func normalizeManualCallbackCandidate(cleaned, fallbackRedirectURI string) string {
	lower := strings.ToLower(cleaned)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return cleaned
	case strings.HasPrefix(cleaned, "//"):
		return "http:" + cleaned
	case strings.HasPrefix(cleaned, "/"):
		return "http://127.0.0.1" + cleaned
	case strings.HasPrefix(lower, "127.0.0.1"),
		strings.HasPrefix(lower, "localhost"),
		strings.HasPrefix(lower, "[::1]"):
		return "http://" + cleaned
	case strings.Contains(lower, "oauth-callback?"):
		qIdx := strings.IndexByte(cleaned, '?')
		beforeQuery := cleaned[:qIdx]
		if strings.Contains(beforeQuery, "/") && (strings.Contains(beforeQuery, ":") || strings.Contains(beforeQuery, ".")) {
			return "http://" + cleaned
		}
		return "http://127.0.0.1/" + strings.TrimPrefix(cleaned, "/")
	default:
		return fallbackRedirectURI + "?" + strings.TrimPrefix(cleaned, "?")
	}
}

func parseManualCallbackQuery(rawURL, fallbackRedirectURI string) (url.Values, error) {
	// 先移除复制折行常带入的内嵌回车、换行与制表符，再裁切首尾空格
	noLineBreaks := strings.NewReplacer("\r", "", "\n", "", "\t", "").Replace(rawURL)
	cleaned := strings.TrimSpace(noLineBreaks)
	if cleaned == "" {
		return nil, fmt.Errorf("回调链接不能为空")
	}

	candidate := normalizeManualCallbackCandidate(cleaned, fallbackRedirectURI)
	parsed, err := url.Parse(candidate)
	if err == nil {
		q := parsed.Query()
		if strings.TrimSpace(q.Get("state")) != "" ||
			strings.TrimSpace(q.Get("code")) != "" ||
			strings.TrimSpace(q.Get("error")) != "" {
			return q, nil
		}
	}

	// 若文本因跨行复制夹带多余空格导致首轮解析失败或参数键名被污染，回退清除裸空白后重试
	compact := strings.Join(strings.Fields(cleaned), "")
	if compact != cleaned && compact != "" {
		parsed2, err2 := url.Parse(normalizeManualCallbackCandidate(compact, fallbackRedirectURI))
		if err2 == nil {
			return parsed2.Query(), nil
		}
		if err != nil {
			err = err2
		}
	}

	if err != nil {
		return nil, fmt.Errorf("无法解析回调链接: %w", err)
	}
	return parsed.Query(), nil
}

// SubmitCallbackURL 允许用户手动粘贴浏览器重定向 URL（兼容完整链接、缺 scheme、大写 scheme、折行空白、相对路径或纯查询参数）以完成回调。
func (m *LoginManager) SubmitCallbackURL(rawURL string) error {
	m.mu.Lock()
	sess := m.current
	m.mu.Unlock()
	if sess == nil {
		return fmt.Errorf("没有等待中的 OAuth 授权会话，请先点击开始授权")
	}
	if sess.getStatus() == SessionStatusProcessing || sess.completed.Load() {
		return ErrOAuthProcessing
	}

	q, err := parseManualCallbackQuery(rawURL, sess.redirectURI)
	if err != nil {
		return err
	}

	if gotState := strings.TrimSpace(q.Get("state")); gotState == "" || gotState != sess.state {
		return fmt.Errorf("回调链接缺少 state 或与本次授权不匹配，请确认粘贴了包含完整 state 与 code 的回调地址")
	}
	if errParam := strings.TrimSpace(q.Get("error")); errParam != "" {
		sess.silent.Store(false)
		if !sess.deliverErr(fmt.Errorf("Google 授权返回错误: %s", errParam)) {
			return ErrOAuthProcessing
		}
		return nil
	}
	code := strings.TrimSpace(q.Get("code"))
	if code == "" {
		return fmt.Errorf("回调链接中缺少 code 参数")
	}

	sess.silent.Store(false)
	if !sess.deliverCode(code) {
		return ErrOAuthProcessing
	}
	return nil
}

// Cancel 中止当前进行中的授权监听或处理会话。
func (m *LoginManager) Cancel() {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	sess := m.current
	m.current = nil
	m.mu.Unlock()

	if sess != nil {
		sess.close()
	}
}

// CancelSilent 仅在当前会话仍处于等待回调阶段（尚未进入 processing）时静默中止监听，避免打断正在换取令牌的流程。
func (m *LoginManager) CancelSilent() bool {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	sess := m.current
	if sess == nil || sess.getStatus() != SessionStatusWaiting || sess.completed.Load() {
		m.mu.Unlock()
		return false
	}
	m.current = nil
	m.mu.Unlock()

	sess.close()
	return true
}

// CancelIfSilent 仅在当前会话为自动预监听（Silent=true）且仍处于 waiting 状态时静默中止监听。
func (m *LoginManager) CancelIfSilent() bool {
	m.startMu.Lock()
	defer m.startMu.Unlock()

	m.mu.Lock()
	sess := m.current
	if sess == nil || !sess.silent.Load() || sess.getStatus() != SessionStatusWaiting || sess.completed.Load() {
		m.mu.Unlock()
		return false
	}
	m.current = nil
	m.mu.Unlock()

	sess.close()
	return true
}

func writeCallbackHTML(w http.ResponseWriter, statusCode int, ok bool, message string) {
	body := []byte(callbackHTML(ok, message))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Connection", "close")
	w.WriteHeader(statusCode)
	_, _ = w.Write(body)
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
	safeTitle := html.EscapeString(title)
	safeMsg := html.EscapeString(message)
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
</html>`, safeTitle, color, safeTitle, safeMsg)
}
