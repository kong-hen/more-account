# 后台常驻运行与 macOS 状态栏功能设计文档

- **日期**：2026-11-22
- **适用项目**：More Account（Wails v2 + Go 1.26 + Vue 3 / TypeScript）
- **核心目标**：
  1. 将点击窗口关闭按钮的默认行为改为「仅关闭页面、程序在后台继续运行」（双端生效且在设置中可切换，启用单实例锁保证重复打开可唤回窗口）。
  2. 开发原生 macOS 顶部状态栏（菜单栏）功能，支持模板图标 + 可选当前账号最低额度百分比显示与原生下拉菜单（配额概览、快速切号、刷新、隐私模式、打开主窗口、退出）。

---

## 1. 范围与边界

### 1.1 纳入范围
1. **关闭按钮默认后台运行（macOS + Windows 双端）**：
   - 应用设置新增「关闭窗口按钮行为」配置项，默认值固定为「隐藏到后台继续运行」，支持随时切换为「直接退出程序」。
   - 旧版本配置文件升级时自动默认启用后台运行模式。
   - **macOS**：点击左上角关闭按钮或按 `Cmd+W` 时隐藏主窗口（`orderOut:`），保留 Dock 图标常驻；点击 Dock 图标或状态栏「打开主窗口」可立即恢复窗口；`Cmd+Q`、Dock 右键退出、状态栏「退出」及应用内退出始终真正退出程序。
   - **Windows**：点击右上角关闭按钮时按设置隐藏主窗口或直接退出；提供应用内「彻底退出应用」入口，方便后台运行模式下随时退出。
   - **双端单实例唤回**：启用 Wails `SingleInstanceLock`，程序在后台运行时再次启动（如 Windows 双击快捷方式 / macOS 再次打开）自动唤醒并前置主窗口，避免多开冲突。
2. **macOS 顶部状态栏（原生 AppKit）**：
   - 仅在 `darwin` 平台启用，非 `darwin` 平台采用零开销 stub，确保 Windows `CGO_ENABLED=0` 交叉编译与 NSIS 打包完全不受影响。
   - 状态栏按钮：专用 36×36px Retina 单色模板图标（18×18pt，自适应系统深浅色菜单栏）+ 可配置的紧凑等宽数字额度百分比。
   - 应用设置中提供「macOS 菜单栏显示」三态配置：`图标 + 额度百分比（默认）` / `仅显示图标` / `关闭状态栏`，保存即时生效。
   - 原生下拉菜单（`NSMenu`）：展示当前激活账号与 4 大核心模型分类配额摘要（含重置倒计时）、快速切换账号子菜单、刷新当前账号配额、刷新全部账号配额、隐私脱敏模式切换、打开主窗口、退出 More Account（`⌘Q`）。
   - 所有界面与菜单文本均严格禁止使用 emoji 图标。

### 1.2 排除范围
- Windows 系统托盘图标与菜单（Windows 端本阶段采用单实例唤回 + 应用内彻底退出按钮，暂不引入 Win32 托盘）。
- 自定义 NSPopover 浮窗卡片（采用更轻快、零 WebView 开销的原生 `NSMenu`）。
- 后台运行时隐藏 macOS Dock 图标（保持标准 macOS `NSApplicationActivationPolicyRegular` 常驻 Dock 体验）。
- 开机自启动配置（不属于本次需求范围）。

---

## 2. 配置模型与数据兼容设计

### 2.1 Go 与前端设置字段
在 `internal/model/settings.go`（`AppSettings`）与 `frontend/src/types/models.ts`（`AppSettings`）新增两个字符串枚举字段：

| 字段（Go / JSON） | 可选值 | 默认值 | 说明 |
|---|---|---|---|
| `CloseBehavior` / `closeBehavior` | `"background"` \| `"quit"` | `"background"` | 点击关闭窗口按钮时的行为：隐藏到后台或直接退出 |
| `MacStatusBarMode` / `macStatusBarMode` | `"icon_quota"` \| `"icon_only"` \| `"off"` | `"icon_quota"` | macOS 顶部状态栏展示模式（非 macOS 端保留字段但不渲染 UI） |

使用字符串枚举而非 `bool`，确保已有用户的旧 `settings.json` 缺少这两个字段时，反序列化为空字符串后由 `storage.normalizeSettings` 统一升级为默认值（`"background"` 与 `"icon_quota"`），不会因 Go 布尔零值退化为关闭状态。

同时在 `BootstrapState` 新增只读字段：
- `Platform string`（`json:"platform"`）：注入 `runtime.GOOS`（`"darwin"` / `"windows"` / `"linux"`），前端设置弹窗据此按平台展示或隐藏 macOS 状态栏配置段，并呈现贴合当前操作系统的后台运行提示语。

---

## 3. 窗口关闭、单实例唤回与原生菜单架构

### 3.1 后端生命周期控制（`App`）
在 `app.go` 中扩展以下状态与方法：
- `quitting atomic.Bool`：标志本次关闭是由主动退出（如应用内退出按钮、状态栏退出）触发。
- `ShowMainWindow()`：
  1. 若窗口处于最小化状态则先执行 `wailsRuntime.WindowUnminimise(ctx)`；
  2. 执行 `wailsRuntime.WindowShow(ctx)` 与 `wailsRuntime.Show(ctx)` 恢复并置顶窗口；
  3. 触发一次静默的 `TriggerAutoRefreshIfDue()` 与 `TriggerActiveDetect()`，确保窗口重新打开时展示最新配额与本机激活账号。
- `QuitApp()`（绑定给前端调用）：
  1. `a.quitting.Store(true)`；
  2. `wailsRuntime.Quit(ctx)`。
- `beforeClose(ctx context.Context) bool`（绑定到 `options.App.OnBeforeClose`）：
  - 在 `darwin` 上，窗口红叉与 `Cmd+W` 已由原生 `windowShouldClose:` 精准处理，进入 `OnBeforeClose` 的均为真正退出意图（`Cmd+Q`、Dock 右键退出、系统注销/重启、状态栏退出、应用内退出），直接返回 `false` 放行。
  - 在非 `darwin` 平台（如 Windows）上，若 `!a.quitting.Load() && a.shouldCloseToBackground()`，则执行 `wailsRuntime.WindowHide(ctx)` 并返回 `true`（阻止退出、隐藏到后台）；否则返回 `false` 正常退出。

### 3.2 macOS 原生窗口钩子与主菜单
在 `main.go` 中：
- 保持 `HideWindowOnClose: false`（因需支持用户在设置里动态切换为「直接退出」）。
- 仅在 `runtime.GOOS == "darwin"` 时配置 `options.App.Menu`（Windows 保持 `Menu: nil`，避免 Windows 窗口顶部出现多余菜单条）：
  - `AppMenu`（关于、隐藏、`Cmd+Q` 退出）；
  - `EditMenu`（保障 WebView 内 `Cmd+C / V / X / Z / A` 快捷键正常工作）；
  - `Window` 菜单：包含「关闭窗口（`Cmd+W`）」（触发与点击关闭按钮一致的后台隐藏或退出逻辑），以及最小化/缩放等标准项。
- 在 `OnStartup` 时，`internal/statusbar`（`darwin` 实现）在主线程一次性安装两处 Objective-C 运行时钩子：
  1. **窗口关闭拦截（`windowShouldClose:`）**：通过 `method_exchangeImplementations` 包装 Wails `WindowDelegate` 的 `windowShouldClose:` 方法。触发时读取原生层维护的原子开关 `closeToBackground`：
     - 为 `YES`：调用 `[sender orderOut:nil]` 仅隐藏主窗口，返回 `NO`；
     - 为 `NO`：调用原实现（走正常退出流程）。
     - 每次 `SaveSettings` 后立即调用 `statusBar.SetCloseToBackground(saved.CloseBehavior == "background")` 热同步。
  2. **Dock 图标重开窗口（`applicationShouldHandleReopen:hasVisibleWindows:`）**：通过 `class_addMethod` 为 Wails `AppDelegate` 补齐此代理方法。当用户点击 Dock 图标且 `flag == NO`（当前无可见窗口）时，回调 Go 侧 `ShowMainWindow()` 恢复并激活主窗口，返回 `YES`。

### 3.3 全平台单实例锁（`SingleInstanceLock`）
在 `main.go` 配置：
```go
SingleInstanceLock: &options.SingleInstanceLock{
    UniqueId: "com.more-account.desktop.singleton",
    OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
        app.ShowMainWindow()
    },
}
```
无论 macOS 还是 Windows，当程序在后台运行且窗口隐藏时，用户再次打开应用程序，新进程会向主实例发送信号并自行退出，主实例立即执行 `ShowMainWindow()` 将窗口拉回前台。

---

## 4. 后端配额摘要引擎（`internal/quota/summary.go`）

为了在主窗口隐藏（WebView 可能休眠）时也能准确刷新 macOS 状态栏，在 Go 后端新增纯函数模块 `internal/quota/summary.go`，与 `frontend/src/utils/quota.ts` 保持口径一致：

1. **4 大核心分类汇总（`SummarizeAccountCategories(account, now)`）**：
   - 固定顺序：`Gemini Argon`、`Gemini Carbon`、`Gemini Flash`、`Claude/GPT`。
   - 优先消费 `account.Quota.SummaryBuckets`（兼容旧缓存中混入 `Models` 的 summary bucket ID）；缺失分类时从过滤后的真实客户端模型兜底推导（排除 `gemini-2.5-pro`、`gemini-3-flash`、`flash-image`、`flash-lite` 等非反重力客户端条目）。
   - 同时保留 5h 与周额度（`FiveHour` / `Weekly`）双窗口；当账号套餐为 `FREE` 且存在周额度时，不展示 5h 窗口。
2. **综合最低额度（`MinQuotaPercentage(account, now)`）**：
   - 遍历全部核心分类双窗口及合并后的客户端模型，返回最低剩余百分比；尚未拉取任何配额时返回 `-1`。
3. **异常状态判定（`IsAccountAbnormal(account)`）**：
   - 账号禁用、`IsForbidden`、需要 Google 验证或存在 `QuotaError.Message` 均判定为异常。
4. **辅助格式化**：
   - `FormatResetCountdown(iso, now)`：输出 `Xd Yh`、`Xh Ym`、`Xm` 或 `即将重置`。
   - `MaskEmail(email, privacyMode)`：复用与前端 `utils/privacy.ts` 一致的脱敏规则，保证状态栏与主界面隐私展示统一。

---

## 5. macOS 原生状态栏设计（`internal/statusbar`）

### 5.1 包结构与接口边界
```text
internal/statusbar/
├── assets/
│   └── tray_icon_template.png   # 36×36px 单色黑+透明 Retina 模板图标（双叠卡+双向箭头）
├── model.go                     # 跨平台纯数据结构（Snapshot / CategoryLine / AccountOption / Callbacks）与 BuildSnapshot()
├── model_test.go                # BuildSnapshot 单元测试（全平台可跑）
├── statusbar_darwin.go          # //go:build darwin — CGo 封装、JSON 快照同步与 Go 回调路由
├── statusbar_darwin.h           # //go:build darwin — Objective-C 桥接声明
├── statusbar_darwin.m           # //go:build darwin — NSStatusItem / NSMenu / NSMenuDelegate / 窗口与 Dock 钩子
└── statusbar_other.go           # //go:build !darwin — 空实现 stub（Windows/Linux 零依赖）
```

对外暴露极简统一控制器接口 `Controller`：
- `Start(closeToBackground bool)`：初始化原生钩子并挂载状态栏（按模式显示/隐藏）。
- `SetCloseToBackground(enabled bool)`：热更新 macOS 窗口关闭按钮拦截开关。
- `Update(snapshot Snapshot)`：将最新快照 JSON 同步至主线程更新状态栏按钮与菜单。
- `Stop()`：在 `OnShutdown` 阶段移除 `NSStatusItem` 并清空全局回调指针，防止退出竞态。

### 5.2 状态栏按钮展示规则
- **图标**：通过 `//go:embed assets/tray_icon_template.png` 内嵌，Objective-C 层用 `NSImage` 加载并设置 `size = NSMakeSize(18, 18)`、`template = YES`。
- **模式表现**：
  - `off`：移除 `NSStatusItem`（不占用菜单栏位置）。
  - `icon_only`：仅显示模板图标，按钮 `title = ""`。
  - `icon_quota`（默认）：显示模板图标，并根据当前激活账号状态设置紧凑标题（采用 `[NSFont monospacedDigitSystemFontOfSize:12 weight:NSFontWeightMedium]` 等宽数字字体）：
    - 当前有激活账号且无异常且 `MinPercentage >= 0`：显示 `" 85%"`（带细微间距）；
    - 当前有激活账号且处于异常状态：显示 `" !"`；
    - 无激活账号或未拉取配额：不附加文字，自动回落为纯图标。
- **Tooltip**：悬停显示 `More Account · <当前邮箱(受隐私模式控制)> · 最低额度 XX%`（无账号时显示 `More Account`）。

### 5.3 原生下拉菜单结构（`NSMenu`）
实现 `NSMenuDelegate` 的 `menuWillOpen:`：每次用户点击菜单栏图标展开菜单前，在当前主线程同步回调 Go 导出函数获取最新快照 JSON（Go 侧仅读内存 `Store` 组装 JSON 返回，不执行网络 I/O 也不回派主线程，杜绝死锁），随后在主线程就地重建菜单项，保证无需后台高频定时器也能看到精确到分钟的重置倒计时。

菜单项自上而下固定结构（无 emoji）：
1. **当前账号信息区（disabled 只读项，使用细粒度排版区分主次）**：
   - 主行：`当前账号：<邮箱> [<套餐>]`（若无激活账号则显示 `当前未检测到使用中账号`）
   - 若存在忙碌态提示行：`正在刷新配额...` 或 `正在切换账号...`
   - 若存在最近一次状态栏操作失败提示：`上次操作失败：<简短原因>`（下次成功或重开主窗口时自动清除）
   - 配额明细行（每类一行）：
     - 双窗口示例：`Gemini Flash  ·  5h 90% / 周 95%  (2h 15m)`
     - 单窗口示例：`Gemini Argon  ·  周 100%  (4d 12h)`
     - 异常账号显示：`状态异常：请打开主窗口查看详情`
     - 未拉取配额显示：`尚未拉取配额`
2. 分隔线
3. **`快速切换账号`（二级子菜单）**：
   - 列出全部纳管账号（按当前列表顺序）：
     - 格式：`<邮箱>  [<套餐>]  <最低额度%>`（未拉取显示 `--`，异常显示 `异常`）
     - 当前激活账号设置 `state = NSControlStateValueOn`（macOS 原生勾选标记）且点击不重复触发切号；
     - 正在切号时所有子项置灰防重入；
     - 无任何账号时显示只读项 `暂无账号`。
   - 点击目标账号后异步调用 `SwitchAccount`（复用 `settings.RuntimeTarget` 与 `settings.LaunchOnSwitch`）。
4. 分隔线
5. **快捷操作区**：
   - `刷新当前账号配额`（无当前账号或正在刷新时置灰；执行中标题变为 `正在刷新配额...`）
   - `刷新全部账号配额`（无账号或正在刷新时置灰）
   - `隐私脱敏模式`（原生复选菜单项，`state = On/Off`，点击切换并持久化设置，同步更新主窗口）
6. 分隔线
7. **窗口与退出区**：
   - `打开主窗口`（调用 `ShowMainWindow()`）
   - `退出 More Account`（快捷键显示 `⌘Q`，调用 `QuitApp()`）

---

## 6. 数据流与前后端事件联动

### 6.1 触发状态栏同步的时机
在 `App` 中封装防抖/线程安全的 `syncStatusBar()`，并在以下全部状态变更点调用：
- `startup()` 启动初始化完成；
- `SaveSettings()`（同时热同步 `closeToBackground` 标志）；
- 账号变动：`ImportRefreshTokens`、`ImportFromLocal`、`UpdateAccountDetails`、`UpdateAccountGroup`、`DeleteAccounts`、`ReorderAccounts`、OAuth 授权完成；
- 配额变动：`RefreshAccountQuota`、`RefreshAllQuotas`、`autoRefresher` 更新回调、`wakeupService` 更新回调；
- 切号与探测：`SwitchAccount`、`activeDetector` 探测到激活账号变化、状态栏快速切号完成。

### 6.2 状态栏动作 → 前端同步事件
为保证主窗口打开或隐藏后重新唤回时 Vue/Pinia 状态树始终与后端一致，状态栏发起的操作完成后向前端广播事件（并在 `frontend/src/stores/appStore.ts` 中订阅）：
- 状态栏切号成功：发送 `account:switched`（前端重载账号列表、当前账号与切号历史，并显示成功 Toast）；
- 状态栏切号/刷新失败：发送 `statusbar:error`（前端若打开则弹出错误 Toast，同时状态栏菜单暂存简短失败摘要）；
- 状态栏刷新配额完成：复用现有 `quota:updated` 事件（前端已有监听自动刷新列表）；
- 状态栏切换隐私脱敏模式：发送 `settings:updated`（携带最新 `AppSettings`，前端立即更新 `appStore.settings` 并同步 UI 脱敏展示）。

---

## 7. 前端 UI 改动范围

1. **`frontend/src/types/models.ts`**：
   - `AppSettings` 新增 `closeBehavior: 'background' | 'quit'` 与 `macStatusBarMode: 'icon_quota' | 'icon_only' | 'off'`；
   - `BootstrapState` 新增 `platform: string`。
2. **`frontend/src/services/api.ts`**：
   - 新增 `quitApp()` 绑定调用（调用 Go `QuitApp()`）。
3. **`frontend/src/stores/appStore.ts`**：
   - 保存 `platform`（默认从 `BootstrapState.platform` 读取）；
   - 新增对 `account:switched`、`settings:updated`、`statusbar:error` 三个后端事件的监听与清理。
4. **`frontend/src/components/settings/SettingsModal.vue`**：
   - 新增「窗口与后台运行」配置卡片：
     - 两态按钮卡片选择关闭窗口行为：**隐藏到后台运行（默认）** / **直接退出程序**（使用 Lucide `Minimize2` / `LogOut` 图标，不引入 emoji）；
     - 根据 `appStore.platform === 'darwin'` 显示对应提示（macOS 提示“点击关闭仅隐藏页面，可通过状态栏或 Dock 重新打开，⌘Q 彻底退出”；Windows 提示“点击关闭隐藏到后台运行，再次打开桌面图标可恢复窗口”）；
     - 当 `appStore.platform === 'darwin'` 时，显示「macOS 顶部状态栏显示」三态选择器（图标 + 额度 / 仅图标 / 关闭）；
   - 弹窗底栏左侧增加次要按钮「彻底退出应用」（Lucide `Power` 图标），点击直接调用 `api.quitApp()`，便于双端用户随时完整退出。

---

## 8. 边界条件、错误处理与测试计划

### 8.1 边界与异常防护
- **并发防重入**：状态栏触发的「快速切号」和「刷新配额」均使用原子布尔锁保护，执行期间禁用对应菜单项；若切号锁已被主窗口占用，立即给出明确错误提示不死锁。
- **主线程安全**：Objective-C 层所有对 `NSStatusBar` / `NSStatusItem` / `NSMenu` / `NSWindow` 的访问都通过主线程判断与 `dispatch_async(dispatch_get_main_queue(), ...)` 保护；退出阶段 `Stop()` 先卸载回调再移除 `NSStatusItem`。
- **菜单展开同步重算**：`menuWillOpen:` 在主线程回调 Go 重建快照时，若涉及读锁仅访问内存 `Store`（毫秒级无 I/O、无网络请求），绝不阻塞 UI 主线程。
- **跨平台编译保障**：所有 CGo/ObjC 文件严格加注 `//go:build darwin`，确保 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build` 零告警通过。

### 8.2 测试与验收清单
1. **Go 单元测试**：
   - `internal/storage/store_test.go`：旧配置升级默认归一化（`background` + `icon_quota`）、非法枚举回退、持久化读写。
   - `internal/quota/summary_test.go`：4 分类双窗口汇总、FREE 账号隐藏 5h 窗口、最低额度计算、异常判定、重置倒计时与邮箱脱敏。
   - `internal/statusbar/model_test.go`：三种状态栏模式、隐私脱敏、异常状态、无账号、刷新中/切号中、错误提示行等全覆盖快照单测。
   - `app_test.go`：非 darwin 下 `beforeClose` 在 `background` / `quit` / `quitting=true` 下的拦截决策测试。
2. **构建与真机验证**：
   - `go test ./...` 全量通过；
   - `cd frontend && pnpm build`（含 TypeScript 编译检查）通过；
   - `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...` 交叉编译验证通过；
   - `wails build` 构建 macOS `.app` 并实测验证：点击红叉隐藏窗口且进程存活 → 点击状态栏与 Dock 图标均能恢复窗口 → 状态栏额度文字与菜单项正常 → 切换「直接退出」模式或按 `Cmd+Q` / 状态栏退出可彻底退出。
