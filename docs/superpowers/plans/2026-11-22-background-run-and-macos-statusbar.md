# 后台常驻运行与 macOS 状态栏功能实现计划

> **面向执行者：** 必须按任务顺序逐步实现并验证（TDD 优先），每完成一个任务运行对应测试后再提交 git commit。

**目标：** 将点击窗口关闭按钮默认改为隐藏页面、程序后台常驻运行（双端生效 + 设置可切 + 单实例唤回），并基于原生 AppKit 为 macOS 开发顶部状态栏（模板图标 + 可选最低额度百分比 + 原生下拉菜单）。

**架构：** 在 `internal/model` 与 `internal/storage` 中以字符串枚举扩展设置并保障旧配置平滑升级；在 `internal/quota/summary.go` 实现与前端同口径的后端配额摘要与隐私脱敏；在 `internal/statusbar` 封装跨平台快照模型及 `darwin` 原生 AppKit 状态栏与窗口/Dock 钩子（非 `darwin` 零开销 stub）；在 `app.go` 与 `main.go` 接通生命周期、单实例锁、macOS 原生菜单与事件广播；最后在前端设置弹窗与状态库中补齐配置界面和同步监听。

**技术栈：** Wails v2 (`v2.12.0`)、Go 1.26、Objective-C / AppKit (CGo, `//go:build darwin`)、Vue 3 + TypeScript + Pinia + Tailwind CSS v4 + `lucide-vue-next`

## 全局约束
- 所有回答、计划、代码注释均使用中文。
- 前端 UI 与原生状态栏菜单严禁使用任何 emoji 图标。
- `CloseBehavior` 默认值为 `"background"`，可选 `"background"` / `"quit"`；旧 `settings.json` 缺少该字段时自动升级为 `"background"`。
- `MacStatusBarMode` 默认值为 `"icon_quota"`，可选 `"icon_quota"` / `"icon_only"` / `"off"`；旧 `settings.json` 缺少该字段时自动升级为 `"icon_quota"`。
- macOS 后台运行时始终保留 Dock 图标（`NSApplicationActivationPolicyRegular`）；Windows 端保持 `Menu: nil` 且必须确保 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...` 零报错通过。
- Objective-C / AppKit 层所有 UI 访问必须在主线程执行；`menuWillOpen:` 仅同步读取内存快照 JSON，绝不执行网络 I/O 或回派主线程。

---

## 文件清单与职责

| 文件路径 | 操作 | 职责 |
|---|---|---|
| `internal/model/settings.go` | 修改 | 新增 `CloseBehavior`、`MacStatusBarMode` 常量与 `AppSettings` 字段 |
| `internal/storage/store.go` | 修改 | 在 `normalizeSettings` 中为新增设置字段补齐默认值与非法值归一化 |
| `internal/storage/store_test.go` | 修改 | 验证旧配置升级默认归一化及新设置字段读写 |
| `internal/quota/summary.go` | 新建 | 后端 4 分类配额摘要、5h/周双窗口汇总、异常判定、倒计时与邮箱脱敏 |
| `internal/quota/summary_test.go` | 新建 | 覆盖 4 分类汇总、FREE 账号规则、过滤非客户端模型、异常与脱敏单测 |
| `internal/statusbar/assets/tray_icon_template.png` | 新建 | 36×36px Retina 单色模板图标（双叠卡 + 双向箭头，纯黑+透明通道） |
| `internal/statusbar/model.go` | 新建 | 状态栏渲染快照模型、`Callbacks`、控制器接口与纯函数 `BuildSnapshot` |
| `internal/statusbar/model_test.go` | 新建 | 覆盖三态显示、隐私脱敏、异常、忙碌态、错误行与子菜单单测 |
| `internal/statusbar/statusbar_other.go` | 新建 | `//go:build !darwin` 零开销空实现 |
| `internal/statusbar/statusbar_darwin.h` | 新建 | `//go:build darwin` C/ObjC 桥接声明 |
| `internal/statusbar/statusbar_darwin.m` | 新建 | `//go:build darwin` `NSStatusItem` / `NSMenu` / `windowShouldClose:` / Dock 重开实现 |
| `internal/statusbar/statusbar_darwin.go` | 新建 | `//go:build darwin` Go↔CGo 状态栏控制器与回调路由 |
| `app.go` | 修改 | 集成状态栏同步、窗口显隐与退出控制、非 darwin `beforeClose`、状态栏动作回调 |
| `app_test.go` | 修改 | 验证 `BootstrapState.Platform`、默认新设置项与关闭拦截决策 |
| `main.go` | 修改 | 接入 `OnBeforeClose`、`SingleInstanceLock` 与 macOS 专属原生 `Menu`（含 `Cmd+W`） |
| `frontend/src/types/models.ts` | 修改 | 扩展 `AppSettings` 与 `BootstrapState` 类型 |
| `frontend/src/services/api.ts` | 修改 | 新增 `quitApp()` 绑定桥接 |
| `frontend/src/stores/appStore.ts` | 修改 | 保存 `platform`、暴露 `quitApp()`、监听状态栏事件（切号/设置/报错） |
| `frontend/src/components/settings/SettingsModal.vue` | 修改 | 新增「窗口与后台运行」配置卡片（含 macOS 状态栏三态）与「彻底退出应用」按钮 |

---

### Task 1: 配置模型扩展与旧配置平滑升级

**Files:**
- Modify: `internal/model/settings.go:1-35`
- Modify: `internal/storage/store.go:174-212`
- Test: `internal/storage/store_test.go`

**Interfaces:**
- Produces:
  - `model.CloseBehaviorBackground = "background"`, `model.CloseBehaviorQuit = "quit"`
  - `model.MacStatusBarIconQuota = "icon_quota"`, `model.MacStatusBarIconOnly = "icon_only"`, `model.MacStatusBarOff = "off"`
  - `model.AppSettings.CloseBehavior`, `model.AppSettings.MacStatusBarMode`

- [ ] **Step 1: 编写旧配置升级与新字段归一化失败测试**

在 `internal/storage/store_test.go` 末尾新增测试 `TestStoreSettingsCloseBehaviorAndStatusBarMigration`：

```go
func TestStoreSettingsCloseBehaviorAndStatusBarMigration(t *testing.T) {
	dir := t.TempDir()
	// 模拟历史版本仅含旧字段的 settings.json（缺少 closeBehavior 和 macStatusBarMode）
	legacyJSON := `{"theme":"dark","runtimeTarget":"ide","lowQuotaThreshold":15,"autoRefreshMinutes":5,"inactiveAutoRefreshMinutes":60}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(legacyJSON), 0600); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("加载旧配置失败: %v", err)
	}
	got := store.GetSettings()
	if got.CloseBehavior != model.CloseBehaviorBackground {
		t.Fatalf("旧配置升级后期望默认 closeBehavior=%q，实际 %q", model.CloseBehaviorBackground, got.CloseBehavior)
	}
	if got.MacStatusBarMode != model.MacStatusBarIconQuota {
		t.Fatalf("旧配置升级后期望默认 macStatusBarMode=%q，实际 %q", model.MacStatusBarIconQuota, got.MacStatusBarMode)
	}

	// 验证合法值保存与非法值归一化回退
	got.CloseBehavior = model.CloseBehaviorQuit
	got.MacStatusBarMode = model.MacStatusBarIconOnly
	saved, err := store.SaveSettings(got)
	if err != nil {
		t.Fatalf("保存合法设置失败: %v", err)
	}
	if saved.CloseBehavior != model.CloseBehaviorQuit || saved.MacStatusBarMode != model.MacStatusBarIconOnly {
		t.Fatalf("保存后设置不符合预期: %+v", saved)
	}

	saved.CloseBehavior = "invalid"
	saved.MacStatusBarMode = "unknown"
	normalized, err := store.SaveSettings(saved)
	if err != nil {
		t.Fatalf("保存非法枚举测试失败: %v", err)
	}
	if normalized.CloseBehavior != model.CloseBehaviorBackground {
		t.Fatalf("非法 closeBehavior 应回退为 %q，实际 %q", model.CloseBehaviorBackground, normalized.CloseBehavior)
	}
	if normalized.MacStatusBarMode != model.MacStatusBarIconQuota {
		t.Fatalf("非法 macStatusBarMode 应回退为 %q，实际 %q", model.MacStatusBarIconQuota, normalized.MacStatusBarMode)
	}
}
```

- [ ] **Step 2: 运行测试确认编译失败（TDD 红灯）**

Run: `go test ./internal/storage -run TestStoreSettingsCloseBehaviorAndStatusBarMigration -v`
Expected: FAIL（`CloseBehaviorBackground` / `MacStatusBarIconQuota` 等未定义）

- [ ] **Step 3: 更新 `internal/model/settings.go` 与 `internal/storage/store.go`**

在 `internal/model/settings.go` 新增常量、字段和默认值：

```go
const (
	CloseBehaviorBackground = "background" // 点击关闭按钮隐藏窗口、程序在后台继续运行
	CloseBehaviorQuit       = "quit"       // 点击关闭按钮直接退出程序

	MacStatusBarIconQuota = "icon_quota" // macOS 状态栏显示模板图标 + 当前账号最低额度百分比
	MacStatusBarIconOnly  = "icon_only"  // macOS 状态栏仅显示模板图标
	MacStatusBarOff       = "off"        // 关闭 macOS 状态栏图标
)
```

在 `AppSettings` 结构体追加：
```go
	CloseBehavior              string `json:"closeBehavior,omitempty"`      // background（默认）| quit
	MacStatusBarMode           string `json:"macStatusBarMode,omitempty"`   // icon_quota（默认）| icon_only | off
```

在 `DefaultSettings()` 中设置：
```go
		CloseBehavior:              CloseBehaviorBackground,
		MacStatusBarMode:           MacStatusBarIconQuota,
```

在 `internal/storage/store.go` 的 `normalizeSettings` 中追加归一化逻辑：
```go
	switch strings.TrimSpace(in.CloseBehavior) {
	case model.CloseBehaviorQuit:
		in.CloseBehavior = model.CloseBehaviorQuit
	default:
		in.CloseBehavior = model.CloseBehaviorBackground
	}
	switch strings.TrimSpace(in.MacStatusBarMode) {
	case model.MacStatusBarIconOnly, model.MacStatusBarOff:
		in.MacStatusBarMode = strings.TrimSpace(in.MacStatusBarMode)
	default:
		in.MacStatusBarMode = model.MacStatusBarIconQuota
	}
```

- [ ] **Step 4: 运行存储单元测试确认通过**

Run: `go test ./internal/storage -v`
Expected: PASS

- [ ] **Step 5: 提交 Task 1**

```bash
git add internal/model/settings.go internal/storage/store.go internal/storage/store_test.go
git commit -m "feat(settings): 新增关闭后台运行与macOS状态栏显示模式配置项"
```

---

### Task 2: 后端配额 4 分类摘要、倒计时与隐私脱敏模块

**Files:**
- Create: `internal/quota/summary.go`
- Create: `internal/quota/summary_test.go`

**Interfaces:**
- Consumes: `model.Account`, `model.QuotaData`, `model.ModelQuota`, `model.QuotaBucket`
- Produces:
  - `quota.WindowSlot` (`Percentage int`, `ResetTime string`, `Exhausted bool`)
  - `quota.CategorySummary` (`Key string`, `Label string`, `ShortLabel string`, `FiveHour *WindowSlot`, `Weekly *WindowSlot`, `Percentage int`, `ResetTime string`)
  - `quota.SummarizeAccountCategories(acc model.Account, now time.Time) []CategorySummary`
  - `quota.MinQuotaPercentage(acc model.Account, now time.Time) int`
  - `quota.IsAccountAbnormal(acc model.Account) bool`
  - `quota.TierLabel(acc model.Account) string`
  - `quota.FormatResetCountdown(iso string, now time.Time) string`
  - `quota.MaskEmail(email string, enabled bool) string`

- [ ] **Step 1: 编写 `internal/quota/summary_test.go`（TDD 红灯）**

测试覆盖：
1. PRO 账号含 5h 与周额度双窗口 summaryBuckets 汇总顺序（`Gemini Argon`, `Gemini Carbon`, `Gemini Flash`, `Claude/GPT`）及综合最低百分比计算；
2. FREE 账号同时有 5h 与周额度时仅保留周额度；
3. 缺少 summaryBuckets 时从 `models` 兜底推导并过滤 `gemini-2.5-pro` / `gemini-3-flash` / `flash-image` / `flash-lite`；
4. 异常账号判定（禁用 / 403 封禁 / `VALIDATION_REQUIRED` / 报错文本）；
5. `FormatResetCountdown`（天+小时 / 小时+分钟 / 分钟 / 已过期「即将重置」 / 空值）；
6. `MaskEmail`（开启/关闭脱敏、短前缀、无 `@` 字符串）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/quota -run TestSummarize -v`
Expected: FAIL（符号未定义）

- [ ] **Step 3: 实现 `internal/quota/summary.go`**

完整实现与 `frontend/src/utils/quota.ts` 和 `frontend/src/utils/privacy.ts` 保持 100% 同口径的分类聚合、模型过滤、窗口筛选、异常判定、倒计时与邮箱脱敏。

- [ ] **Step 4: 运行测试确认全部通过**

Run: `go test ./internal/quota -v`
Expected: PASS

- [ ] **Step 5: 提交 Task 2**

```bash
git add internal/quota/summary.go internal/quota/summary_test.go
git commit -m "feat(quota): 新增后端配额分类摘要与隐私脱敏汇总工具"
```

---

### Task 3: 状态栏快照模型、Retina 模板图标与跨平台 Stub

**Files:**
- Create: `internal/statusbar/assets/tray_icon_template.png`
- Create: `internal/statusbar/model.go`
- Create: `internal/statusbar/model_test.go`
- Create: `internal/statusbar/statusbar_other.go`

**Interfaces:**
- Consumes: `model.Account`, `model.AppSettings`, `internal/quota` summary functions
- Produces:
  - `statusbar.Snapshot`（含 `Visible`, `ButtonTitle`, `Tooltip`, `CurrentHeader`, `StatusHint`, `ErrorBanner`, `Categories`, `Accounts`, `CanRefreshCurrent`, `CanRefreshAll`, `Refreshing`, `Switching`, `PrivacyMode`）
  - `statusbar.Callbacks`（`BuildSnapshot func() Snapshot`, `OnShowWindow func()`, `OnSwitchAccount func(accountID string)`, `OnRefreshCurrent func()`, `OnRefreshAll func()`, `OnTogglePrivacy func()`, `OnQuit func()`）
  - `statusbar.BuildSnapshot(input BuildInput) Snapshot`
  - `statusbar.Controller` 接口与 `statusbar.NewController(cb Callbacks) Controller`

- [ ] **Step 1: 生成高质量 36×36px 单色透明 Retina 模板图标**

使用脚本（Python Pillow 或 Go `image/png` 抗锯齿绘制，用完即删临时脚本）生成 `internal/statusbar/assets/tray_icon_template.png`：
- 尺寸 36×36px（在 macOS 状态栏中逻辑尺寸为 18×18pt @2x Retina）；
- 纯黑（RGB=0,0,0）+ 抗锯齿 Alpha 通道，视觉呼应应用 `logo.svg` 的双叠圆角卡片 + 双向切换箭头轮廓，在小尺寸下线条清晰不糊。

- [ ] **Step 2: 编写 `internal/statusbar/model_test.go`（TDD 红灯）**

测试覆盖：
1. `off` 模式：`Visible == false`；
2. `icon_only` 模式：`Visible == true`, `ButtonTitle == ""`；
3. `icon_quota` 模式：
   - 正常账号：`ButtonTitle == "72%"`（最低额度），带分类配额行与重置倒计时；
   - 异常账号：`ButtonTitle == "!"`，菜单展示异常提示行；
   - 无当前账号或无配额：`ButtonTitle == ""`；
4. 隐私脱敏开启时，Tooltip、当前账号头、快速切换列表邮箱全部脱敏；
5. `Refreshing` / `Switching` 忙碌态禁用对应菜单操作并给出状态提示，`LastError` 展示错误提示行；
6. 确认所有生成文案中不含任何 emoji 字符。

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/statusbar -v`
Expected: FAIL

- [ ] **Step 4: 实现 `internal/statusbar/model.go` 与 `internal/statusbar/statusbar_other.go`**

在 `model.go` 中定义数据结构、`Controller` 接口、`BuildInput` 与纯函数 `BuildSnapshot`；在 `statusbar_other.go`（`//go:build !darwin`）实现 `NewController` 返回空实现 stub。

- [ ] **Step 5: 运行测试确认跨平台单测与 Windows 编译检查通过**

Run: `go test ./internal/statusbar -v`
Expected: PASS（注意：在 darwin 上需先完成 Task 4 才有 `statusbar_darwin.go` 提供的 `NewController`，因此 Task 3 先完成 `model.go` + `model_test.go` + `statusbar_other.go` + 图标资源，并与 Task 4 紧接实现或在 `GOOS=windows` 下验证 `statusbar_other.go`，然后在 Task 4 完成后统一在 darwin 上跑通 `go test ./internal/statusbar -v`）。
> 说明：为保证每次提交都 `go test ./...` 绿灯，将 `NewController` 的声明与 `darwin`/`!darwin` 实现收敛在 Task 4 一起提交，Task 3 聚焦纯模型 `BuildSnapshot` 与图标资源并立刻通过 `go test ./internal/statusbar -v`！

- [ ] **Step 6: 提交 Task 3**

```bash
git add internal/statusbar/assets/tray_icon_template.png internal/statusbar/model.go internal/statusbar/model_test.go
git commit -m "feat(statusbar): 新增状态栏渲染快照模型、单测与Retina模板图标"
```

---

### Task 4: macOS 原生 AppKit 状态栏与窗口/Dock 钩子实现

**Files:**
- Create: `internal/statusbar/statusbar_darwin.h`
- Create: `internal/statusbar/statusbar_darwin.m`
- Create: `internal/statusbar/statusbar_darwin.go`
- Create: `internal/statusbar/statusbar_other.go`

**Interfaces:**
- Consumes: `statusbar.Snapshot`, `statusbar.Callbacks`, `assets/tray_icon_template.png`
- Produces:
  - `statusbar.Controller` 接口：`Start(closeToBackground bool)`, `SetCloseToBackground(enabled bool)`, `Update(snap Snapshot)`, `Stop()`
  - `statusbar.NewController(cb Callbacks) Controller`（darwin 原生实现 / 非 darwin no-op）

- [ ] **Step 1: 编写 `internal/statusbar/statusbar_darwin.h` 与 `statusbar_darwin.m`**

核心实现要点：
1. **全局状态与线程安全**：
   - 使用原子标志 `atomic_bool gCloseToBackground` 保存关闭按钮是否隐藏到后台。
   - 统一 `MAOnMainThread(block)` 辅助宏，若当前已在主线程直接执行，否则 `dispatch_async(dispatch_get_main_queue(), block)`。
2. **原生窗口与 Dock 钩子（`MAInstallWindowHooks`）**：
   - 使用 `dispatch_once` 确保进程生命周期仅安装一次：
   - 对 `NSApp.mainWindow` 或 `[NSApp windows].firstObject` 的 `delegate` 类，使用 `class_getInstanceMethod` + `method_exchangeImplementations` 包装 `windowShouldClose:`：
     - 当 `atomic_load(&gCloseToBackground)` 为 `true` 时执行 `[sender orderOut:nil]; return NO;`；
     - 否则调用原始 `windowShouldClose:`。
   - 若启动初期 `delegate` 尚未就绪，延迟极短时间重试挂载（最多重试数次）确保 100% 挂载成功。
   - 对 `NSApp.delegate` 类，检查是否已有 `applicationShouldHandleReopen:hasVisibleWindows:`，用 `class_addMethod`（或 swizzle）注入：当 `!hasVisibleWindows` 时触发 Go 导出回调 `maStatusBarOnShowWindow()` 并返回 `YES`。
3. **原生状态栏与菜单（`MAStatusBarDelegate`）**：
   - 加载内嵌 PNG 字节为 `NSImage`，设置 `size = NSMakeSize(18, 18)`、`template = YES`。
   - 收到 `MAStatusBarUpdateJSON(const char *jsonStr)` 后在主线程解析 `NSJSONSerialization`：
     - `visible == false`：从 `[NSStatusBar systemStatusBar]` 移除 `statusItem`；
     - `visible == true`：按需创建 `NSStatusItem`（`NSVariableStatusItemLength`），绑定 `NSMenu`（`delegate = self`, `autoenablesItems = NO`），设置按钮模板图标、等宽数字标题（`NSAttributedString` 使用 `monospacedDigitSystemFontOfSize:12 weight:NSFontWeightMedium`）和 `toolTip`。
   - `menuWillOpen:(NSMenu *)menu`：在当前主线程同步调用 Go 导出函数 `char *maStatusBarFetchSnapshotJSON()`，拿到最新快照 JSON 后调用 `free`，立刻调用 `[self rebuildMenuWithSnapshot:dict]` 刷新菜单项（含最新倒计时）。
   - 菜单项构建：
     - 只读信息区（当前账号、忙碌提示、错误行、4 分类配额明细，使用简洁系统字体与等宽数字）；
     - `快速切换账号` 子菜单（`representedObject` 存 `accountId`，激活账号 `state = NSControlStateValueOn`）；
     - `刷新当前账号配额`、`刷新全部账号配额`、`隐私脱敏模式`（`state = On/Off`）；
     - `打开主窗口`、`退出 More Account`（keyEquivalent `@"q"` + `NSEventModifierFlagCommand`）。
   - 所有菜单动作方法分别回调 Go 导出函数（Go 侧异步执行耗时操作，绝不卡死 AppKit 主线程）。
   - `MAStatusBarStop()`：主线程清空 `menu.delegate = nil`、移除 `statusItem` 并置空引用。

- [ ] **Step 2: 编写 `internal/statusbar/statusbar_darwin.go` 与 `internal/statusbar/statusbar_other.go`**

- `statusbar_darwin.go`：用 `//go:embed assets/tray_icon_template.png` 嵌入图标，通过 `sync.RWMutex` 保护全局活跃回调注册，导出 `maStatusBarFetchSnapshotJSON`、`maStatusBarOnShowWindow`、`maStatusBarOnSwitchAccount`、`maStatusBarOnRefreshCurrent`、`maStatusBarOnRefreshAll`、`maStatusBarOnTogglePrivacy`、`maStatusBarOnQuit`。
- `statusbar_other.go`：`//go:build !darwin` 提供零开销空控制器。

- [ ] **Step 3: 运行双平台编译与单测校验**

Run:
```bash
go test ./internal/statusbar -v
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./internal/statusbar/...
```
Expected: 全部 PASS 且 Windows 交叉编译零报错。

- [ ] **Step 4: 提交 Task 4**

```bash
git add internal/statusbar/
git commit -m "feat(macos): 实现原生AppKit状态栏、窗口关闭拦截与Dock重开钩子"
```

---

### Task 5: 集成 `App` 生命周期、`main.go` 单实例锁与 macOS 菜单栏

**Files:**
- Modify: `app.go`
- Modify: `main.go`
- Modify: `app_test.go`

**Interfaces:**
- Consumes: `statusbar.Controller`, `statusbar.NewController`, `statusbar.BuildSnapshot`, `model.CloseBehavior*`, `model.MacStatusBar*`
- Produces:
  - `BootstrapState.Platform string`（`json:"platform"`）
  - `App.ShowMainWindow()`
  - `App.QuitApp()`（绑定给前端）
  - `App.HandleCloseWindowShortcut()`（供 macOS `Cmd+W` 菜单项调用）
  - `App.beforeClose(ctx context.Context) bool`（绑定到 `options.App.OnBeforeClose`）

- [ ] **Step 1: 在 `app_test.go` 中编写失败测试（TDD 红灯）**

测试覆盖：
1. `GetBootstrapState()` 返回非空 `Platform == runtime.GOOS`，且默认设置 `CloseBehavior == "background"`、`MacStatusBarMode == "icon_quota"`；
2. 抽取可单测纯判定函数 `shouldPreventCloseOnPlatform(goos string, closeBehavior string, quitting bool) bool`：
   - `windows` + `background` + `quitting=false` → `true`（拦截退出并隐藏窗口）；
   - `windows` + `quit` + `quitting=false` → `false`（直接退出）；
   - `windows` + `background` + `quitting=true` → `false`（主动退出放行）；
   - `darwin` 任何参数 → `false`（macOS 窗口红叉已在原生层处理，走到 `OnBeforeClose` 的均为真正退出，始终放行）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test . -run TestAppCloseBehaviorDecision -v`
Expected: FAIL

- [ ] **Step 3: 改造 `app.go`**

1. `BootstrapState` 增加 `Platform string \`json:"platform"\``，在 `GetBootstrapState()` 填充 `runtime.GOOS`。
2. `App` 增加字段：
   - `statusBar statusbar.Controller`
   - `quitting atomic.Bool`
   - `statusRefreshing atomic.Bool`
   - `statusSwitching atomic.Bool`
   - `statusErrMu sync.RWMutex`、`statusLastError string`
3. 在 `NewApp()` 中初始化 `app.statusBar = statusbar.NewController(...)`，绑定全部回调：
   - `BuildSnapshot`：读取 `a.store` 与原子忙碌态，调用 `statusbar.BuildSnapshot(...)`；
   - `OnShowWindow`：调用 `a.ShowMainWindow()`；
   - `OnSwitchAccount(id)`：防重入异步执行 `a.switchAccountFromStatusBar(id)`（成功后广播 `account:switched` 与 `quota:updated`，失败记录简短错误并广播 `statusbar:error`）；
   - `OnRefreshCurrent` / `OnRefreshAll`：防重入异步执行刷新（成功后广播 `quota:updated`，失败记录简短错误并广播 `statusbar:error`）；
   - `OnTogglePrivacy`：持久化切换 `PrivacyMode`，广播 `settings:updated` 并刷新状态栏；
   - `OnQuit`：调用 `a.QuitApp()`。
4. `startup(ctx)` 中启动状态栏并同步初始快照与 `closeToBackground`；`shutdown(ctx)` 中调用 `a.statusBar.Stop()`。
5. 实现 `ShowMainWindow()`、`QuitApp()`、`HandleCloseWindowShortcut()`、`beforeClose(ctx)` 与 `syncStatusBar()`，并在所有账号、配额、设置、切号、自动刷新、唤醒、外部探测变更点触发 `a.syncStatusBar()`。

- [ ] **Step 4: 改造 `main.go`**

1. `options.App` 增加：
   - `OnBeforeClose: app.beforeClose`
   - `SingleInstanceLock: &options.SingleInstanceLock{ UniqueId: "com.more-account.desktop.singleton", OnSecondInstanceLaunch: func(_ options.SecondInstanceData) { app.ShowMainWindow() } }`
   - 仅 `runtime.GOOS == "darwin"` 时构造标准 macOS `Menu`（AppMenu + EditMenu + 含「关闭窗口 `Cmd+W`」的窗口菜单），Windows 端保持 `Menu: nil`。

- [ ] **Step 5: 运行全量 Go 测试与双端编译检查**

Run:
```bash
go test ./... -v
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build .
```
Expected: 全量 Go 测试 PASS，Windows 交叉编译 PASS。

- [ ] **Step 6: 提交 Task 5**

```bash
git add app.go main.go app_test.go
git commit -m "feat(lifecycle): 接入默认关闭后台运行、双端单实例唤回与macOS状态栏联动"
```

---

### Task 6: 前端设置弹窗、退出按钮与事件同步 + 完整真机验证

**Files:**
- Modify: `frontend/src/types/models.ts`
- Modify: `frontend/src/services/api.ts`
- Modify: `frontend/src/stores/appStore.ts`
- Modify: `frontend/src/components/settings/SettingsModal.vue`

**Interfaces:**
- Consumes: Go `QuitApp()`, `BootstrapState.platform`, `AppSettings.closeBehavior`, `AppSettings.macStatusBarMode`, Wails 事件 `account:switched` / `settings:updated` / `statusbar:error`

- [ ] **Step 1: 更新前端模型与 API 桥接**

1. `frontend/src/types/models.ts`：
   - 新增类型 `CloseBehavior = 'background' | 'quit'`、`MacStatusBarMode = 'icon_quota' | 'icon_only' | 'off'`；
   - `AppSettings` 新增 `closeBehavior: CloseBehavior`、`macStatusBarMode: MacStatusBarMode`；
   - `BootstrapState` 新增 `platform?: string`。
2. `frontend/src/services/api.ts`：
   - `WailsAppBridge` 增加 `QuitApp(): Promise<void>`；
   - `api` 对象增加 `quitApp: () => getBridge().QuitApp()`。

- [ ] **Step 2: 更新 `frontend/src/stores/appStore.ts`**

1. 新增 `const platform = ref<string>('darwin')`（在 `bootstrap()` 从 `state.platform` 初始化），默认值兼容 `settings` 的 `closeBehavior: 'background'`、`macStatusBarMode: 'icon_quota'`。
2. 订阅状态栏同步事件：
   - `account:switched`：重新加载账号列表与切号历史并弹出成功 Toast；
   - `settings:updated`：同步更新 `settings.value` 与主题（确保状态栏切换隐私脱敏时主界面即时响应）；
   - `statusbar:error`：弹出错误 Toast 提示。
3. 新增 `quitApp()` action 并从 store 返回 `platform` 与 `quitApp`。

- [ ] **Step 3: 更新 `frontend/src/components/settings/SettingsModal.vue`**

1. 引入 Lucide 图标：`Minimize2`、`LogOut`、`Power`、`PanelTop`（严禁使用 emoji）。
2. 新增「窗口与后台运行」配置卡片：
   - 关闭窗口按钮行为两态切换按钮：**隐藏到后台运行（默认）** / **直接退出程序**；
   - 动态展示符合当前平台的简明提示文案（macOS 提示状态栏/Dock 恢复与 `⌘Q` 退出；Windows 提示再次打开桌面图标恢复窗口）；
   - 当 `appStore.platform === 'darwin'` 时，同卡内提供「macOS 顶部状态栏显示」三态选择（`图标 + 额度` / `仅图标` / `关闭`）。
3. 在弹窗底栏左侧增加次要危险/中性操作按钮「彻底退出应用」（带 `Power` 图标），点击调用 `appStore.quitApp()`。
4. 在 `handleSave()` 中显式规范提交 `closeBehavior` 和 `macStatusBarMode`。

- [ ] **Step 4: 全量构建与测试验证**

Run:
```bash
cd frontend && pnpm build
cd ..
go test ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build .
wails build -clean -platform darwin/arm64
```
Expected:
- 前端 TypeScript 编译与 Vite 打包零报错；
- 全量 Go 单元测试 PASS；
- Windows amd64 交叉编译零报错；
- macOS 原生 `.app` 构建成功，并验证生成的二进制与状态栏资源完整。

- [ ] **Step 5: 提交 Task 6**

```bash
git add frontend/src/types/models.ts frontend/src/services/api.ts frontend/src/stores/appStore.ts frontend/src/components/settings/SettingsModal.vue
git commit -m "feat(ui): 新增后台运行与macOS状态栏设置项、彻底退出入口及事件同步"
```
