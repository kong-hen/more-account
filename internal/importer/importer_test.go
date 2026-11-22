package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"more-account/internal/model"
	"more-account/internal/oauth"
	"more-account/internal/storage"
)

func TestExportAccountsRespectsIncludeSecretsFlag(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	acc, err := store.UpsertAccount(model.Account{
		Email:           "alice@example.com",
		Notes:           "测试备注",
		LoginAccount:    "alice-login",
		AccountPassword: "top-secret-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP",
		Token: model.TokenData{
			AccessToken:  "at",
			RefreshToken: "rt-123",
		},
	})
	if err != nil {
		t.Fatalf("写入测试账号失败: %v", err)
	}

	svc := NewService(store, nil, nil)

	// 默认不含登录账号、密码与 2FA 密钥
	rawNoSecret, err := svc.ExportJSON([]string{acc.ID}, false)
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	var list1 []ExportAccountItem
	if err := json.Unmarshal([]byte(rawNoSecret), &list1); err != nil {
		t.Fatalf("反序列化导出结果失败: %v", err)
	}
	if len(list1) != 1 || list1[0].RefreshToken != "rt-123" || list1[0].LoginAccount != "" || list1[0].AccountPassword != "" || list1[0].TwoFactorSecret != "" {
		t.Fatalf("不导出密钥时不应包含登录账号、密码和 2FA: %+v", list1)
	}

	// 勾选包含登录账号、密码与 2FA 密钥
	rawWithSecret, err := svc.ExportJSON([]string{acc.ID}, true)
	if err != nil {
		t.Fatalf("含密钥导出失败: %v", err)
	}
	var list2 []ExportAccountItem
	if err := json.Unmarshal([]byte(rawWithSecret), &list2); err != nil {
		t.Fatalf("反序列化导出结果失败: %v", err)
	}
	if list2[0].LoginAccount != "alice-login" || list2[0].AccountPassword != "top-secret-password" || list2[0].TwoFactorSecret != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("含密钥导出应完整保留登录账号、密码与 2FA: %+v", list2[0])
	}
}

func TestImportJSONPreservesPasswordSpacesAndAvoidsOrphanGroups(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.FormValue("refresh_token") == "rt-bad" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
				AccessToken: "at-good",
				ExpiresIn:   3600,
				TokenType:   "Bearer",
			})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(oauth.UserInfo{
				Email: "ok@example.com",
				Name:  "OK User",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oauthClient := &oauth.Client{
		HTTPClient:  srv.Client(),
		TokenURL:    srv.URL + "/token",
		UserInfoURL: srv.URL + "/userinfo",
	}
	svc := NewService(store, oauthClient, nil)

	payload := `[
		{
			"email": "bad@example.com",
			"refresh_token": "rt-bad",
			"group_name": "GhostGroup"
		},
		{
			"email": "badtotp@example.com",
			"refresh_token": "rt-good-invalid-totp",
			"group_name": "InvalidTotpGroup",
			"two_factor_secret": "!!!invalid!!!"
		},
		{
			"email": "ok@example.com",
			"refresh_token": "rt-good",
			"group_name": "RealGroup",
			"account_password": "  spaced pass  ",
			"two_factor_secret": "otpauth://totp/Test:ok?secret=gezd-gnbv-gy3t-qojq&algorithm=SHA256&digits=8&period=60"
		}
	]`

	res, err := svc.ImportJSON(context.Background(), payload, "")
	if err != nil {
		t.Fatalf("ImportJSON 返回顶层错误: %v", err)
	}
	if len(res.Imported) != 1 || len(res.Failed) != 2 {
		t.Fatalf("期望 1 成功 2 失败，实际 imported=%d failed=%d (%+v)", len(res.Imported), len(res.Failed), res.Failed)
	}

	imported := res.Imported[0]
	if imported.AccountPassword != "  spaced pass  " {
		t.Fatalf("密码首尾空格不应被裁切: got %q", imported.AccountPassword)
	}
	if !strings.HasPrefix(imported.TwoFactorSecret, "otpauth://totp/") {
		t.Fatalf("非默认参数 2FA URI 应归一化保留: got %q", imported.TwoFactorSecret)
	}

	// 验证失败条目涉及的 GhostGroup 与 InvalidTotpGroup 未残留空分组，仅创建了 RealGroup
	groups := store.ListGroups()
	if len(groups) != 1 || groups[0].Name != "RealGroup" {
		t.Fatalf("失败条目不应残留幽灵分组，实际分组列表: %+v", groups)
	}
	if imported.GroupID != groups[0].ID {
		t.Fatalf("成功导入账号未正确绑定到 RealGroup: acc.GroupID=%q group.ID=%q", imported.GroupID, groups[0].ID)
	}
}

func TestAddByTokenResponseSkipsRedundantRefreshTokenCall(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	grp, err := store.SaveGroup(model.AccountGroup{Name: "OAuth分组", Color: "#2563EB"})
	if err != nil {
		t.Fatalf("创建分组失败: %v", err)
	}

	var tokenCalls atomic.Int32
	var userInfoCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case "/userinfo":
			userInfoCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer fresh-at-from-exchange" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(oauth.UserInfo{
				Email: "oauth-direct@example.com",
				Name:  "OAuth Direct",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oauthClient := &oauth.Client{
		HTTPClient:  srv.Client(),
		TokenURL:    srv.URL + "/token",
		UserInfoURL: srv.URL + "/userinfo",
	}
	svc := NewService(store, oauthClient, nil)

	acc, err := svc.AddByTokenResponse(context.Background(), &oauth.TokenResponse{
		AccessToken:  "fresh-at-from-exchange",
		RefreshToken: "rt-from-exchange",
		ExpiresIn:    3600,
		TokenType:    "Bearer",
		IDToken:      "id-tok-1",
	}, grp.ID, nil)
	if err != nil {
		t.Fatalf("AddByTokenResponse 失败: %v", err)
	}
	if tokenCalls.Load() != 0 {
		t.Fatalf("AddByTokenResponse 不应再次请求 /token 端点，实际请求 %d 次", tokenCalls.Load())
	}
	if userInfoCalls.Load() != 1 {
		t.Fatalf("期望调用 /userinfo 1 次，实际 %d 次", userInfoCalls.Load())
	}
	if acc.Email != "oauth-direct@example.com" || acc.GroupID != grp.ID || acc.Token.AccessToken != "fresh-at-from-exchange" || acc.Token.IDToken != "id-tok-1" {
		t.Fatalf("入库账号信息不符合预期: %+v", acc)
	}

	// 缺少必要字段时返回明确错误
	if _, err := svc.AddByTokenResponse(context.Background(), nil, "", nil); err == nil {
		t.Fatal("nil TokenResponse 应当报错")
	}
	if _, err := svc.AddByTokenResponse(context.Background(), &oauth.TokenResponse{RefreshToken: "rt"}, "", nil); err == nil {
		t.Fatal("空 AccessToken 应当报错")
	}
	if _, err := svc.AddByTokenResponse(context.Background(), &oauth.TokenResponse{AccessToken: "at"}, "", nil); err == nil {
		t.Fatal("空 RefreshToken 应当报错")
	}
}

func TestImportJSONCountsNonObjectElementsAsFailedAndPreservesOrder(t *testing.T) {
	store, err := storage.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			rt := r.FormValue("refresh_token")
			_ = json.NewEncoder(w).Encode(oauth.TokenResponse{
				AccessToken: "at-" + rt,
				ExpiresIn:   3600,
				TokenType:   "Bearer",
			})
		case "/userinfo":
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer at-")
			_ = json.NewEncoder(w).Encode(oauth.UserInfo{
				Email: auth + "@example.com",
				Name:  "User " + auth,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	oauthClient := &oauth.Client{
		HTTPClient:  srv.Client(),
		TokenURL:    srv.URL + "/token",
		UserInfoURL: srv.URL + "/userinfo",
	}
	svc := NewService(store, oauthClient, nil)

	// 1. 包装格式 { "accounts": [...] } 混入非对象元素时，非对象元素必须计入 Failed 且合法项正常并发导入并去重建组
	wrapped := `{
		"accounts": [
			{"refresh_token": "u1", "group_name": "SharedGroup"},
			"not-an-object",
			12345,
			{"refresh_token": "u2", "group_name": "SharedGroup"},
			null,
			{"refresh_token": "u3", "group_name": "SharedGroup"},
			{"refresh_token": "u4", "group_name": "SharedGroup"},
			{"refresh_token": "u5", "group_name": "SharedGroup"}
		]
	}`
	res, err := svc.ImportJSON(context.Background(), wrapped, "")
	if err != nil {
		t.Fatalf("ImportJSON 返回顶层错误: %v", err)
	}
	if len(res.Imported) != 5 || len(res.Failed) != 3 {
		t.Fatalf("期望 5 成功 3 失败，实际 imported=%d failed=%d (%+v)", len(res.Imported), len(res.Failed), res.Failed)
	}
	for i := 0; i < 5; i++ {
		wantEmail := fmt.Sprintf("u%d@example.com", i+1)
		if res.Imported[i].Email != wantEmail {
			t.Fatalf("并发导入结果未保持原始顺序: idx=%d got=%q want=%q", i, res.Imported[i].Email, wantEmail)
		}
	}
	if groups := store.ListGroups(); len(groups) != 1 || groups[0].Name != "SharedGroup" {
		t.Fatalf("多条共享同名新分组应只创建 1 个分组，实际 %+v", groups)
	}

	// 2. 顶层数组混入非对象元素同样不整体失败，而是把非法条目计入 Failed
	arrPayload := `[{"refresh_token": "u6"}, "bad-elem", null]`
	res2, err := svc.ImportJSON(context.Background(), arrPayload, "")
	if err != nil {
		t.Fatalf("顶层数组含非对象元素不应直接报错: %v", err)
	}
	if len(res2.Imported) != 1 || len(res2.Failed) != 2 {
		t.Fatalf("期望 1 成功 2 失败，实际 imported=%d failed=%d", len(res2.Imported), len(res2.Failed))
	}

	// 3. accounts 字段非数组时应返回顶层解析错误
	if _, err := svc.ImportJSON(context.Background(), `{"accounts": "invalid"}`, ""); err == nil {
		t.Fatal("accounts 字段非数组应返回错误")
	}

	// 4. 多行纯文本 Refresh Token 并发导入保持顺序
	lines := "t1\n# comment\nt2\nt3\nt4\nt5\n"
	res3, err := svc.ImportRefreshTokens(context.Background(), lines, "")
	if err != nil {
		t.Fatalf("ImportRefreshTokens 失败: %v", err)
	}
	if len(res3.Imported) != 5 || len(res3.Failed) != 0 {
		t.Fatalf("期望 5 成功 0 失败，实际 imported=%d failed=%d", len(res3.Imported), len(res3.Failed))
	}
	for i := 0; i < 5; i++ {
		wantEmail := fmt.Sprintf("t%d@example.com", i+1)
		if res3.Imported[i].Email != wantEmail {
			t.Fatalf("多行 Token 并发导入未保持顺序: idx=%d got=%q want=%q", i, res3.Imported[i].Email, wantEmail)
		}
	}
}
