package netutil

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateProxyURL(t *testing.T) {
	// 空代理允许（回退系统环境变量）
	if u, err := ValidateProxyURL("   "); err != nil || u != nil {
		t.Fatalf("空字符串应当返回 nil, nil，实际 u=%v err=%v", u, err)
	}

	valid := []string{
		"http://127.0.0.1:1",
		"http://127.0.0.1:7890",
		"https://proxy.example.com:8443",
		"socks5://127.0.0.1:1080",
		"socks5h://user:pass@127.0.0.1:65535",
	}
	for _, raw := range valid {
		if _, err := ValidateProxyURL(raw); err != nil {
			t.Fatalf("合法代理 %q 校验失败: %v", raw, err)
		}
	}

	invalid := []string{
		"ftp://127.0.0.1:21",
		"127.0.0.1:7890",
		"http://127.0.0.1",
		"http://127.0.0.1:0",
		"http://127.0.0.1:65536",
		"socks5://127.0.0.1:99999",
		"socks5://:1080",
		"javascript:alert(1)",
	}
	for _, raw := range invalid {
		if _, err := ValidateProxyURL(raw); err == nil {
			t.Fatalf("非法代理 %q 应当被拒绝", raw)
		}
	}
}

func TestDynamicTransportAndConnectivity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	dt, err := NewDynamicTransport("")
	if err != nil {
		t.Fatalf("初始化 DynamicTransport 失败: %v", err)
	}
	if err := dt.SetProxy("http://127.0.0.1:7890"); err != nil {
		t.Fatalf("热更新合法代理失败: %v", err)
	}
	if got := dt.ProxyURL(); got != "http://127.0.0.1:7890" {
		t.Fatalf("期望当前代理为 http://127.0.0.1:7890，实际 %q", got)
	}
	if err := dt.SetProxy("invalid-proxy"); err == nil {
		t.Fatal("设置非法代理应当报错")
	}
	// 清空代理后直接测试本地模拟端点连通性
	if err := dt.SetProxy(""); err != nil {
		t.Fatal(err)
	}
	ms, err := TestConnectivity(context.Background(), "", srv.URL)
	if err != nil || ms <= 0 {
		t.Fatalf("连通性探测应当成功并返回正数耗时，实际 ms=%d err=%v", ms, err)
	}
}

func TestConnectivityRejectsProxyAuthAndServerErrors(t *testing.T) {
	for _, status := range []int{http.StatusProxyAuthRequired, http.StatusBadGateway, http.StatusServiceUnavailable} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := TestConnectivity(context.Background(), "", srv.URL)
		srv.Close()
		if err == nil {
			t.Fatalf("HTTP %d 响应应当被连通性测试判定为失败", status)
		}
	}
}
