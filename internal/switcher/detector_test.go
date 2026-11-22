package switcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/storage"
)

func makeFakeIDToken(email string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payloadBytes, _ := json.Marshal(map[string]string{"email": email})
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	return header + "." + payload + ".sig"
}

func TestExtractEmailFromIDTokenAndParseCredential(t *testing.T) {
	idTok := makeFakeIDToken("alice@gmail.com")
	raw, err := BuildCredentialPayloadJSON(model.Account{
		Email: "alice@gmail.com",
		Token: model.TokenData{
			AccessToken:  "at-alice",
			RefreshToken: "rt-alice",
			IDToken:      idTok,
		},
	})
	if err != nil {
		t.Fatalf("构造凭据失败: %v", err)
	}
	parsed, err := ParseSystemCredential(raw)
	if err != nil {
		t.Fatalf("解析凭据失败: %v", err)
	}
	if parsed.Email != "alice@gmail.com" {
		t.Fatalf("期望从 id_token 解出 alice@gmail.com，实际 %q", parsed.Email)
	}
}

func TestReadCredentialFromStateDBExtractsEmailAndTokens(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	acc := model.Account{
		ID:    "a1",
		Email: "ide-user@gmail.com",
		Token: model.TokenData{
			AccessToken:     "acc-ide",
			RefreshToken:    "rt-ide",
			ExpiryTimestamp: 1800000000,
			IDToken:         "", // 故意不放 id_token，验证从 userStatus 兜底提取邮箱
		},
	}
	if err := InjectAccountToStateDB(dbPath, acc); err != nil {
		t.Fatalf("注入 state.vscdb 失败: %v", err)
	}
	cred, err := ReadCredentialFromStateDB(dbPath)
	if err != nil || cred == nil {
		t.Fatalf("读取 state.vscdb 凭据失败: cred=%+v err=%v", cred, err)
	}
	if cred.RefreshToken != "rt-ide" || cred.AccessToken != "acc-ide" || cred.Email != "ide-user@gmail.com" {
		t.Fatalf("state.vscdb 提取字段不匹配: %+v", cred)
	}
}

func TestActiveDetectorDetectsExternalSwitches(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化 store 失败: %v", err)
	}
	accA, err := store.UpsertAccount(model.Account{
		Email: "a@gmail.com",
		Token: model.TokenData{RefreshToken: "rt-a"},
	})
	if err != nil {
		t.Fatalf("写入账号 A 失败: %v", err)
	}
	accB, err := store.UpsertAccount(model.Account{
		Email: "b@gmail.com",
		Token: model.TokenData{RefreshToken: "rt-b"},
	})
	if err != nil {
		t.Fatalf("写入账号 B 失败: %v", err)
	}
	if err := store.SetCurrentAccountID(accA.ID); err != nil {
		t.Fatalf("设置初始激活账号失败: %v", err)
	}

	var (
		mockCred    *SystemCredential
		mockSource  = "keychain"
		mockErr     error
		switching   atomic.Bool
		notifyCount int
		lastNotify  ActiveDetectionResult
	)

	detector := NewActiveDetector(
		store,
		nil,
		func() bool { return switching.Load() },
		func(res ActiveDetectionResult) {
			notifyCount++
			lastNotify = res
		},
	)
	detector.readLocal = func(target string) (*SystemCredential, string, error) {
		return mockCred, mockSource, mockErr
	}

	// 场景 1：其他程序通过精确 RT 切换到账号 B
	mockCred = &SystemCredential{RefreshToken: "rt-b"}
	res := detector.DetectOnce(context.Background())
	if !res.Changed || !res.Managed || res.MatchedAccountID != accB.ID {
		t.Fatalf("期望切换到账号 B，实际 %+v", res)
	}
	if store.GetCurrentAccountID() != accB.ID {
		t.Fatalf("store currentAccountID 期望 %s，实际 %s", accB.ID, store.GetCurrentAccountID())
	}
	if notifyCount != 1 {
		t.Fatalf("期望触发 1 次通知，实际 %d", notifyCount)
	}

	// 重复轮询同一账号不应再次触发变更或通知
	res = detector.DetectOnce(context.Background())
	if res.Changed || notifyCount != 1 {
		t.Fatalf("重复轮询不应误报变更: res=%+v notifyCount=%d", res, notifyCount)
	}

	// 场景 2：用户在 Antigravity 中重新登录了账号 A（全新 RT，靠 id_token 邮箱命中）
	mockCred = &SystemCredential{
		RefreshToken: "rt-a-brand-new",
		Email:        "A@GMAIL.COM",
	}
	res = detector.DetectOnce(context.Background())
	if !res.Changed || !res.Managed || res.MatchedAccountID != accA.ID {
		t.Fatalf("期望通过邮箱命中账号 A，实际 %+v", res)
	}
	if store.GetCurrentAccountID() != accA.ID {
		t.Fatalf("store currentAccountID 期望 %s，实际 %s", accA.ID, store.GetCurrentAccountID())
	}

	// 场景 3：切号进行中应跳过探测，不覆盖状态
	switching.Store(true)
	mockCred = &SystemCredential{RefreshToken: "rt-b"}
	res = detector.DetectOnce(context.Background())
	if store.GetCurrentAccountID() != accA.ID {
		t.Fatalf("切号中不应修改 currentAccountID，实际 %s", store.GetCurrentAccountID())
	}
	switching.Store(false)

	// 场景 4：读取凭据临时报错不应清空当前激活账号
	mockCred = nil
	mockErr = errors.New("keychain busy")
	res = detector.DetectOnce(context.Background())
	if res.Error == "" || store.GetCurrentAccountID() != accA.ID {
		t.Fatalf("读取报错时应保留原激活账号: res=%+v current=%s", res, store.GetCurrentAccountID())
	}
	mockErr = nil

	// 场景 5：外部切换到了未纳管的新账号 C，应清空旧激活态并上报外部邮箱
	mockCred = &SystemCredential{
		RefreshToken: "rt-external-c",
		Email:        "external@gmail.com",
	}
	res = detector.DetectOnce(context.Background())
	if !res.Changed || res.Managed || !res.HasCredential || res.DetectedEmail != "external@gmail.com" {
		t.Fatalf("期望识别未纳管外部账号，实际 %+v", res)
	}
	if store.GetCurrentAccountID() != "" {
		t.Fatalf("未纳管外部账号时应清空旧的 currentAccountID，实际 %q", store.GetCurrentAccountID())
	}
	if lastNotify.DetectedEmail != "external@gmail.com" {
		t.Fatalf("回调应收到外部账号信息，实际 %+v", lastNotify)
	}

	// 场景 6：本程序内部切号后调用 SyncAfterInternalSwitch，下一次探测不应触发误报通知
	_ = store.SetCurrentAccountID(accB.ID)
	detector.SyncAfterInternalSwitch(accB)
	prevNotify := notifyCount
	mockCred = &SystemCredential{RefreshToken: "rt-b", Email: accB.Email}
	res = detector.DetectOnce(context.Background())
	if res.Changed || notifyCount != prevNotify {
		t.Fatalf("内部切号同步基线后不应重复通知: res=%+v notifyCount=%d prev=%d", res, notifyCount, prevNotify)
	}

	// 场景 7：用户退出登录（无任何凭据）时清空 currentAccountID
	mockCred = nil
	res = detector.DetectOnce(context.Background())
	if !res.Changed || res.HasCredential || store.GetCurrentAccountID() != "" {
		t.Fatalf("退出登录后应清空激活态: res=%+v current=%q", res, store.GetCurrentAccountID())
	}
}

func TestActiveDetectorPreservesCurrentOnOAuthNetworkFailureAndSilentMode(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化 store 失败: %v", err)
	}
	accA, err := store.UpsertAccount(model.Account{
		Email: "keep@gmail.com",
		Token: model.TokenData{RefreshToken: "rt-old"},
	})
	if err != nil {
		t.Fatalf("写入账号失败: %v", err)
	}
	if err := store.SetCurrentAccountID(accA.ID); err != nil {
		t.Fatalf("设置激活账号失败: %v", err)
	}

	// 模拟 Google OAuth/UserInfo 网络不可用（返回 500）
	failingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer failingSrv.Close()

	oauthClient := &oauth.Client{
		HTTPClient:  failingSrv.Client(),
		TokenURL:    failingSrv.URL + "/token",
		UserInfoURL: failingSrv.URL + "/userinfo",
	}

	notifyCount := 0
	detector := NewActiveDetector(store, oauthClient, nil, func(res ActiveDetectionResult) {
		notifyCount++
	})
	detector.readLocal = func(target string) (*SystemCredential, string, error) {
		// 返回一个与 store 中不同的 RT 且无 id_token 邮箱，触发远程解析但网络失败
		return &SystemCredential{RefreshToken: "rt-rotated-no-email"}, "keychain", nil
	}

	res := detector.DetectOnce(context.Background())
	if res.Error == "" {
		t.Fatalf("OAuth 远程邮箱解析失败时应返回错误信息: %+v", res)
	}
	if store.GetCurrentAccountID() != accA.ID {
		t.Fatalf("网络故障期间不应清空已有激活账号: 期望 %s，实际 %q", accA.ID, store.GetCurrentAccountID())
	}
	if notifyCount != 0 {
		t.Fatalf("网络瞬时失败不应触发切换通知，实际 notifyCount=%d", notifyCount)
	}

	// 验证 DetectOnceSilent 在确实发生变更时不触发 onChange 回调（避免前端手动刷新时重复弹通知）
	detector.readLocal = func(target string) (*SystemCredential, string, error) {
		return &SystemCredential{RefreshToken: "rt-external", Email: "ext@example.com"}, "keychain", nil
	}
	resSilent := detector.DetectOnceSilent(context.Background())
	if !resSilent.Changed || resSilent.DetectedEmail != "ext@example.com" {
		t.Fatalf("DetectOnceSilent 应正确返回变更结果: %+v", resSilent)
	}
	if notifyCount != 0 {
		t.Fatalf("DetectOnceSilent 不应触发 onChange 回调，实际 notifyCount=%d", notifyCount)
	}
}

func TestActiveDetectorTOCTOURaceBlockedAndRotatedRTSynced(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化 store 失败: %v", err)
	}
	accA, err := store.UpsertAccount(model.Account{
		Email:          "old@gmail.com",
		Disabled:       true,
		DisabledReason: "invalid_grant: old token revoked",
		Token:          model.TokenData{RefreshToken: "rt-old-revoked"},
		QuotaError:     &model.QuotaErrorInfo{Reason: "INVALID_GRANT", Message: "revoked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	accB, err := store.UpsertAccount(model.Account{
		Email: "new@gmail.com",
		Token: model.TokenData{RefreshToken: "rt-new-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrentAccountID(accA.ID); err != nil {
		t.Fatal(err)
	}

	// 场景 1：第一段 TOCTOU 窗口——readLocal 耗时期间发生内部切号至 accB，滞后返回的旧凭据 accA 不得回退覆盖 store 或误发通知
	notifyCount := 0
	var detector *ActiveDetector
	detector = NewActiveDetector(store, nil, nil, func(res ActiveDetectionResult) {
		notifyCount++
	})
	detector.readLocal = func(target string) (*SystemCredential, string, error) {
		_ = store.SetCurrentAccountID(accB.ID)
		detector.SyncAfterInternalSwitch(accB)
		return &SystemCredential{RefreshToken: "rt-old-revoked", Email: accA.Email}, "keychain", nil
	}
	res := detector.DetectOnce(context.Background())
	if store.GetCurrentAccountID() != accB.ID {
		t.Fatalf("readLocal 期间内部切号后，store.CurrentAccountID 必须保持 accB(%s)，实际 %q", accB.ID, store.GetCurrentAccountID())
	}
	if notifyCount != 0 || res.MatchedAccountID != accB.ID {
		t.Fatalf("过时探测结果必须被丢弃且不得误报通知: notifyCount=%d res=%+v", notifyCount, res)
	}

	// 场景 2：第二段 TOCTOU 窗口——matchAccount 联网请求 UserInfo 期间发生内部切号，仍必须丢弃过时结果
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模拟 OAuth 请求耗时期间内部切号至 accB
		_ = store.SetCurrentAccountID(accB.ID)
		detector.SyncAfterInternalSwitch(accB)
		_ = json.NewEncoder(w).Encode(map[string]any{"email": accA.Email})
	}))
	defer srv.Close()

	detector.oauthClient = &oauth.Client{
		HTTPClient:  srv.Client(),
		UserInfoURL: srv.URL + "/userinfo",
	}
	detector.readLocal = func(target string) (*SystemCredential, string, error) {
		return &SystemCredential{AccessToken: "at-stale", RefreshToken: "rt-unknown-no-email"}, "keychain", nil
	}
	res2 := detector.DetectOnce(context.Background())
	if store.GetCurrentAccountID() != accB.ID || notifyCount != 0 || res2.MatchedAccountID != accB.ID {
		t.Fatalf("matchAccount 联网期间内部切号必须阻断过时写回与误报: current=%q notifyCount=%d res=%+v",
			store.GetCurrentAccountID(), notifyCount, res2)
	}

	// 场景 3：无并发切号时，外部通过同邮箱重新登录并轮换了新 RT，应自动回写新 RT 并解除 invalid_grant 禁用态
	detector.readLocal = func(target string) (*SystemCredential, string, error) {
		return &SystemCredential{
			AccessToken:  "at-fresh-from-ext",
			RefreshToken: "rt-rotated-fresh",
			Email:        "OLD@GMAIL.COM",
		}, "keychain", nil
	}
	res3 := detector.DetectOnce(context.Background())
	if !res3.Changed || !res3.Managed || res3.MatchedAccountID != accA.ID {
		t.Fatalf("期望通过邮箱匹配 accA 并标记变更，实际 %+v", res3)
	}
	updatedA, ok := store.GetAccount(accA.ID)
	if !ok {
		t.Fatal("未找到 accA")
	}
	if updatedA.Token.RefreshToken != "rt-rotated-fresh" || updatedA.Token.AccessToken != "at-fresh-from-ext" {
		t.Fatalf("外部重登产生的新令牌应回写到 store，实际 %+v", updatedA.Token)
	}
	if updatedA.Disabled || updatedA.QuotaError != nil {
		t.Fatalf("回写有效新令牌后应自动解除 invalid_grant 禁用与令牌错误态，实际 disabled=%v err=%+v", updatedA.Disabled, updatedA.QuotaError)
	}
}
