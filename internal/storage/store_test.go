package storage

import (
	"os"
	"path/filepath"
	"testing"

	"more-account/internal/model"
)

func TestStoreAccountsAndGroupsLifecycle(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}

	// 验证默认配置：默认浅色主题，低配额阈值 20
	settings := store.GetSettings()
	if settings.Theme != "light" {
		t.Fatalf("期望默认主题为 light，实际为 %q", settings.Theme)
	}
	if settings.LowQuotaThreshold != 20 {
		t.Fatalf("期望默认低配额阈值为 20，实际为 %d", settings.LowQuotaThreshold)
	}

	// 创建自建分组
	group, err := store.SaveGroup(model.AccountGroup{
		Name:  "主力开发",
		Color: "#2563EB",
	})
	if err != nil {
		t.Fatalf("保存分组失败: %v", err)
	}
	if group.ID == "" {
		t.Fatal("新建分组未自动分配 ID")
	}

	// 创建并按邮箱去重更新账号
	acc1, err := store.UpsertAccount(model.Account{
		Email:   "Alice@Example.com",
		Name:    "Alice",
		GroupID: group.ID,
		Token: model.TokenData{
			AccessToken:  "access-1",
			RefreshToken: "refresh-1",
		},
	})
	if err != nil {
		t.Fatalf("插入账号失败: %v", err)
	}

	// 验证账号文件以 0600 权限原子落盘
	info, err := os.Stat(filepath.Join(dir, "accounts.json"))
	if err != nil {
		t.Fatalf("未找到 accounts.json: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Fatalf("期望 accounts.json 权限为 0600，实际为 %o", perm)
	}

	// 相同邮箱大小写不同应触发原地更新而非重复插入
	acc1Updated, err := store.UpsertAccount(model.Account{
		Email: "alice@example.com",
		Name:  "Alice New",
		Token: model.TokenData{
			AccessToken:  "access-2",
			RefreshToken: "refresh-2",
		},
	})
	if err != nil {
		t.Fatalf("更新同邮箱账号失败: %v", err)
	}
	if acc1Updated.ID != acc1.ID {
		t.Fatalf("同邮箱账号 ID 不一致: %s != %s", acc1Updated.ID, acc1.ID)
	}
	if acc1Updated.GroupID != group.ID {
		t.Fatalf("更新令牌时不应丢失已有分组归属: got %q", acc1Updated.GroupID)
	}

	// 更新备注、密码与 2FA 密钥
	updated, err := store.UpdateAccountDetails(acc1.ID, model.AccountDetailsUpdate{
		Notes:           "团队主力号",
		AccountPassword: "secret-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP",
		GroupID:         group.ID,
	})
	if err != nil {
		t.Fatalf("更新账号详情失败: %v", err)
	}
	if updated.Notes != "团队主力号" || updated.AccountPassword != "secret-password" || updated.TwoFactorSecret != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("账号详情字段未正确保存: %+v", updated)
	}

	// 删除分组时，原分组内账号应归入未分组而不被删除
	if err := store.DeleteGroup(group.ID); err != nil {
		t.Fatalf("删除分组失败: %v", err)
	}
	reloaded, ok := store.GetAccount(acc1.ID)
	if !ok {
		t.Fatal("删除分组不应删除账号")
	}
	if reloaded.GroupID != "" {
		t.Fatalf("删除分组后账号 groupId 应置空，实际为 %q", reloaded.GroupID)
	}

	// 重新加载仓储验证持久化完整性
	store2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("重新加载存储失败: %v", err)
	}
	if len(store2.ListAccounts()) != 1 {
		t.Fatalf("期望重新加载后有 1 个账号，实际 %d", len(store2.ListAccounts()))
	}
}

func TestStoreHistoryCapAndTrim(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}

	for i := 0; i < 220; i++ {
		if err := store.AppendSwitchHistory(model.SwitchHistoryItem{
			TargetEmail:  "user@example.com",
			TargetClient: "desktop",
			Success:      true,
		}); err != nil {
			t.Fatalf("写入切号历史失败: %v", err)
		}
	}

	items := store.ListSwitchHistory()
	if len(items) != maxHistoryItems {
		t.Fatalf("期望历史被裁剪为 %d 条，实际 %d 条", maxHistoryItems, len(items))
	}

	if err := store.ClearSwitchHistory(); err != nil {
		t.Fatalf("清空历史失败: %v", err)
	}
	if len(store.ListSwitchHistory()) != 0 {
		t.Fatal("清空后历史记录应为空")
	}
}
