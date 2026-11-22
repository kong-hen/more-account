package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
