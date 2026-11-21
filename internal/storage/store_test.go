package storage

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	// 更新备注、登录账号、密码与 2FA 密钥
	updated, err := store.UpdateAccountDetails(acc1.ID, model.AccountDetailsUpdate{
		Notes:           "团队主力号",
		LoginAccount:    "alice.corp@example.com",
		AccountPassword: "secret-password",
		TwoFactorSecret: "JBSWY3DPEHPK3PXP",
		GroupID:         group.ID,
	})
	if err != nil {
		t.Fatalf("更新账号详情失败: %v", err)
	}
	if updated.Notes != "团队主力号" || updated.LoginAccount != "alice.corp@example.com" || updated.AccountPassword != "secret-password" || updated.TwoFactorSecret != "JBSWY3DPEHPK3PXP" {
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

func TestStoreDeepCopyAndAtomicPartialUpdates(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}

	isGcp := true
	acc1, err := store.UpsertAccount(model.Account{
		Email:           "deep@example.com",
		Notes:           "初始备注",
		AccountPassword: "init-password",
		Token: model.TokenData{
			AccessToken:  "at-1",
			RefreshToken: "rt-1",
			IsGcpTos:     &isGcp,
		},
		Quota: &model.QuotaData{
			SubscriptionTier: "PRO",
			Models:           []model.ModelQuota{{Name: "gemini-3.8-flash-high", Percentage: 80}},
		},
		QuotaError: &model.QuotaErrorInfo{Message: "old-err"},
	})
	if err != nil {
		t.Fatalf("插入账号失败: %v", err)
	}

	// 1. 深拷贝隔离校验：修改返回副本内部指针/切片不得污染 Store 内部状态
	got, ok := store.GetAccount(acc1.ID)
	if !ok {
		t.Fatal("未找到刚插入的账号")
	}
	got.Quota.Models[0].Percentage = 0
	got.QuotaError.Message = "tampered"
	*got.Token.IsGcpTos = false

	fresh, _ := store.GetAccount(acc1.ID)
	if fresh.Quota.Models[0].Percentage != 80 || fresh.QuotaError.Message != "old-err" || fresh.Token.IsGcpTos == nil || !*fresh.Token.IsGcpTos {
		t.Fatalf("深拷贝隔离失效，Store 内部状态被外部修改污染: %+v", fresh)
	}

	// 2. UpdateAccountDetails 拒绝不存在的 GroupID
	if _, err := store.UpdateAccountDetails(acc1.ID, model.AccountDetailsUpdate{
		Notes:   "新备注",
		GroupID: "non-existent-group-id",
	}); err == nil {
		t.Fatal("传入不存在的 GroupID 应当返回错误")
	}

	// 3. UpdateAccount 原子局部更新仅修改配额，不覆盖期间用户写入的备注/密码
	if _, err := store.UpdateAccountDetails(acc1.ID, model.AccountDetailsUpdate{
		Notes:           "并发编辑后的备注",
		AccountPassword: "updated-password",
	}); err != nil {
		t.Fatalf("更新详情失败: %v", err)
	}
	updated, err := store.UpdateAccount(acc1.ID, func(target *model.Account) error {
		target.Quota = &model.QuotaData{SubscriptionTier: "ULTRA"}
		return nil
	})
	if err != nil {
		t.Fatalf("原子局部更新失败: %v", err)
	}
	if updated.Notes != "并发编辑后的备注" || updated.AccountPassword != "updated-password" || updated.Quota.SubscriptionTier != "ULTRA" {
		t.Fatalf("原子局部更新未保留并发字段: %+v", updated)
	}

	// 4. SortOrder 删除账号/分组后新增应严格大于当前最大值，不产生重复 SortOrder
	acc2, err := store.UpsertAccount(model.Account{
		Email: "second@example.com",
		Token: model.TokenData{RefreshToken: "rt-2"},
	})
	if err != nil {
		t.Fatalf("插入账号 2 失败: %v", err)
	}
	if err := store.DeleteAccounts([]string{acc1.ID}); err != nil {
		t.Fatalf("删除账号 1 失败: %v", err)
	}
	acc3, err := store.UpsertAccount(model.Account{
		Email: "third@example.com",
		Token: model.TokenData{RefreshToken: "rt-3"},
	})
	if err != nil {
		t.Fatalf("插入账号 3 失败: %v", err)
	}
	acc2Now, _ := store.GetAccount(acc2.ID)
	if acc3.SortOrder <= acc2Now.SortOrder {
		t.Fatalf("删除旧账号后新增账号 SortOrder 应严格大于已有最大值: acc2=%d acc3=%d", acc2Now.SortOrder, acc3.SortOrder)
	}

	g1, err := store.SaveGroup(model.AccountGroup{Name: "G1"})
	if err != nil {
		t.Fatalf("创建分组 1 失败: %v", err)
	}
	g2, err := store.SaveGroup(model.AccountGroup{Name: "G2"})
	if err != nil {
		t.Fatalf("创建分组 2 失败: %v", err)
	}
	if err := store.DeleteGroup(g1.ID); err != nil {
		t.Fatalf("删除分组 1 失败: %v", err)
	}
	g3, err := store.SaveGroup(model.AccountGroup{Name: "G3"})
	if err != nil {
		t.Fatalf("创建分组 3 失败: %v", err)
	}
	if g3.SortOrder <= g2.SortOrder {
		t.Fatalf("删除旧分组后新增分组 SortOrder 应严格大于已有最大值: g2=%d g3=%d", g2.SortOrder, g3.SortOrder)
	}

	// 5. WakeupState 深拷贝隔离 & UpdateWakeupTaskLastRun 不覆盖并发修改
	ws := model.WakeupState{
		Enabled: true,
		Tasks: []model.WakeupTask{
			{
				ID:      "task-1",
				Name:    "原任务名",
				Enabled: true,
				Schedule: model.WakeupSchedule{
					SelectedAccounts: []string{acc2.ID},
					SelectedModels:   []string{"gemini-3.8-flash-high"},
				},
			},
		},
	}
	if err := store.SaveWakeupState(ws); err != nil {
		t.Fatalf("保存唤醒状态失败: %v", err)
	}
	gotWS := store.GetWakeupState()
	gotWS.Tasks[0].Schedule.SelectedAccounts[0] = "tampered-acc"
	if store.GetWakeupState().Tasks[0].Schedule.SelectedAccounts[0] != acc2.ID {
		t.Fatal("WakeupState 深拷贝隔离失效")
	}
	// 并发修改任务名称后再调用 UpdateWakeupTaskLastRun
	ws2 := store.GetWakeupState()
	ws2.Tasks[0].Name = "改名后的任务"
	if err := store.SaveWakeupState(ws2); err != nil {
		t.Fatalf("更新任务名称失败: %v", err)
	}
	if err := store.UpdateWakeupTaskLastRun("task-1", 1700000000000); err != nil {
		t.Fatalf("原子更新任务 LastRunAt 失败: %v", err)
	}
	finalWS := store.GetWakeupState()
	if finalWS.Tasks[0].Name != "改名后的任务" || finalWS.Tasks[0].LastRunAt != 1700000000000 {
		t.Fatalf("UpdateWakeupTaskLastRun 状态异常: %+v", finalWS.Tasks[0])
	}

	// 6. 前端旧快照覆盖保存时不得回退后端较新的 LastRunAt，且删除账号必须级联清理唤醒任务引用
	staleSnapshot := store.GetWakeupState()
	staleSnapshot.Tasks[0].LastRunAt = 1600000000000 // 模拟旧快照
	if err := store.SaveWakeupState(staleSnapshot); err != nil {
		t.Fatalf("保存旧快照失败: %v", err)
	}
	if got := store.GetWakeupState().Tasks[0].LastRunAt; got != 1700000000000 {
		t.Fatalf("SaveWakeupState 不应把 LastRunAt 回退为旧值: got=%d", got)
	}

	if err := store.DeleteAccounts([]string{acc2.ID}); err != nil {
		t.Fatalf("删除账号 2 失败: %v", err)
	}
	if rem := store.GetWakeupState().Tasks[0].Schedule.SelectedAccounts; len(rem) != 0 {
		t.Fatalf("删除账号后唤醒任务仍残留失效账号 ID: %v", rem)
	}

	// 7. RecordTokenRefreshFailure / RecordTokenRefreshSuccess 闭环测试
	if _, err := store.RecordTokenRefreshFailure(acc3.ID, errors.New("token revoked"), true); err != nil {
		t.Fatalf("记录 invalid_grant 失败: %v", err)
	}
	disabledAcc, _ := store.GetAccount(acc3.ID)
	if !disabledAcc.Disabled || disabledAcc.QuotaError == nil || disabledAcc.QuotaError.Reason != "INVALID_GRANT" {
		t.Fatalf("期望账号进入 INVALID_GRANT 禁用状态，实际: %+v", disabledAcc)
	}
	if _, err := store.RecordTokenRefreshSuccess(acc3.ID, model.TokenData{AccessToken: "recovered", RefreshToken: "rt-3"}); err != nil {
		t.Fatalf("记录令牌恢复失败: %v", err)
	}
	recoveredAcc, _ := store.GetAccount(acc3.ID)
	if recoveredAcc.Disabled {
		t.Fatalf("令牌刷新恢复后应自动解除 invalid_grant 禁用标记: %+v", recoveredAcc)
	}
}

func TestWriteBytesAtomicTightensPermissionsAndExpandHome(t *testing.T) {
	target := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(target, []byte("public-old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := WriteBytesAtomic(target, []byte(`{"secret":"token"}`), 0600); err != nil {
		t.Fatalf("WriteBytesAtomic 失败: %v", err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0600 {
			t.Fatalf("覆盖已有 0644 文件后权限必须收紧为 0600，实际 %o", st.Mode().Perm())
		}
	}

	expanded := ExpandHomePath("~/.gemini/antigravity")
	if strings.HasPrefix(expanded, "~") {
		t.Fatalf("ExpandHomePath 未展开波浪号路径: %q", expanded)
	}
}
