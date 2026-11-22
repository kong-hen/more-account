package netutil

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultProbeURL = "https://oauth2.googleapis.com/token"

// ValidateProxyURL 校验代理地址格式：空字符串表示不启用自定义代理（跟随系统环境变量）；
// 非空时仅允许 http / https / socks5 / socks5h 协议且必须包含有效主机名。
func ValidateProxyURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("代理解析失败: %w", err)
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q（仅支持 http、https、socks5、socks5h）", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, fmt.Errorf("代理地址缺少有效主机名")
	}
	p := parsed.Port()
	if p == "" {
		return nil, fmt.Errorf("代理地址请显式指定端口号（例如 http://127.0.0.1:7890 或 socks5://127.0.0.1:1080）")
	}
	portNum, err := strconv.Atoi(p)
	if err != nil || portNum < 1 || portNum > 65535 {
		return nil, fmt.Errorf("代理端口号 %q 无效（必须为 1-65535 之间的整数）", p)
	}
	return parsed, nil
}

// BuildTransport 根据可选代理地址创建独立的 HTTP Transport。
func BuildTransport(rawProxyURL string) (*http.Transport, error) {
	parsed, err := ValidateProxyURL(rawProxyURL)
	if err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	var tr *http.Transport
	if ok && base != nil {
		tr = base.Clone()
	} else {
		tr = &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   15 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          64,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	}
	if parsed != nil {
		tr.Proxy = http.ProxyURL(parsed)
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return tr, nil
}

// DynamicTransport 支持在运行期线程安全地热切换 HTTP / SOCKS5 代理配置，无需重启应用。
type DynamicTransport struct {
	mu       sync.RWMutex
	proxyURL string
	inner    *http.Transport
}

// NewDynamicTransport 创建支持热更新的代理传输层。
func NewDynamicTransport(initialProxyURL string) (*DynamicTransport, error) {
	tr, err := BuildTransport(initialProxyURL)
	if err != nil {
		return nil, err
	}
	return &DynamicTransport{
		proxyURL: strings.TrimSpace(initialProxyURL),
		inner:    tr,
	}, nil
}

// SetProxy 校验并热更新代理地址，同时关闭旧连接池中的空闲连接。
func (d *DynamicTransport) SetProxy(rawProxyURL string) error {
	trimmed := strings.TrimSpace(rawProxyURL)
	next, err := BuildTransport(trimmed)
	if err != nil {
		return err
	}
	d.mu.Lock()
	prev := d.inner
	d.proxyURL = trimmed
	d.inner = next
	d.mu.Unlock()

	if prev != nil {
		prev.CloseIdleConnections()
	}
	return nil
}

// ProxyURL 返回当前生效的自定义代理地址。
func (d *DynamicTransport) ProxyURL() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.proxyURL
}

// RoundTrip 实现 http.RoundTripper 接口。
func (d *DynamicTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.mu.RLock()
	tr := d.inner
	d.mu.RUnlock()
	return tr.RoundTrip(req)
}

// TestConnectivity 使用指定代理配置探测目标端点连通性并返回往返耗时（毫秒）。
func TestConnectivity(ctx context.Context, rawProxyURL string, probeURL string) (int64, error) {
	tr, err := BuildTransport(rawProxyURL)
	if err != nil {
		return 0, err
	}
	defer tr.CloseIdleConnections()

	target := strings.TrimSpace(probeURL)
	if target == "" {
		target = DefaultProbeURL
	}

	client := &http.Client{
		Transport: tr,
		Timeout:   10 * time.Second,
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
	if err != nil {
		return 0, fmt.Errorf("构建探测请求失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("连接测试失败: %w", err)
	}
	_ = resp.Body.Close()

	// 拦截代理认证失败与代理/上游网关异常响应（HEAD /token 正常时会返回 405，故允许 2xx-4xx 中除 407 外的状态）
	if resp.StatusCode == http.StatusProxyAuthRequired {
		return 0, fmt.Errorf("代理需要身份认证 (HTTP 407)，请在代理地址中配置用户名与密码")
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return 0, fmt.Errorf("代理或目标端点返回异常状态 (HTTP %d)", resp.StatusCode)
	}

	ms := time.Since(started).Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	return ms, nil
}
