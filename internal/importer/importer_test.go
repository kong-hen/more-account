package importer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
