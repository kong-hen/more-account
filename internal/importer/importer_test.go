package importer

import (
	"encoding/json"
	"testing"

	"more-account/internal/model"
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
