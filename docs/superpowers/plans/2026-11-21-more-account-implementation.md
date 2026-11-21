# More Account（反重力账号管理）实施计划

> **执行指引**：按任务顺序逐项推进，严格执行先写/运行测试再提交（TDD + 频繁 Commit），确保所有代码注释与文档使用中文，前端图标全部采用 `lucide-vue-next` 矢量图标（禁用 Emoji）。

**目标**：从 `cockpit-tools` 中抽离并重写反重力（Antigravity）单平台账号管理程序，构建轻量独立、采用全新三栏工作台布局的原生桌面应用。

**架构**：使用 Wails v2 + Go 1.26（纯 Go 无 CGO，使用 `modernc.org/sqlite` 读写 Antigravity 本地 SQLite，程序自身数据以原子化 JSON 存于 `~/.more-account/`）作为后端核心引擎，搭配 Vue 3 + TypeScript + Pinia + Tailwind CSS v4 作为前端界面。

**技术栈**：Go 1.26.1、Wails v2.12.0、`modernc.org/sqlite`、Vue 3.5、Vite 6、TypeScript 5、Pinia、Tailwind CSS v4、`lucide-vue-next`

## 全局约束
1. **单平台聚焦**：仅支持反重力（Antigravity），不包含其他任何平台，不包含「应用多开」功能。
2. **分组取代标签**：彻底移除账号标签（`tags`），改用自建单归属分组（`groupId`），删除分组时成员自动归入「未分组」。
3. **备注凭据裁剪**：账号详情保留备注、账号密码与 2FA TOTP 实时动态验证码；**不包含**辅助邮箱、手机号及邮件预览功能。
4. **主题策略**：**默认浅色主题，不跟随系统暗色模式**，仅支持用户手动切换浅色 / 暗色主题并持久化。
5. **宽松顶栏与可选筛选**：账号列表顶栏保持宽松不拥挤，在聚合筛选下拉内提供可选的「订阅等级 / 仅看低配额 / 仅看异常」筛选。
6. **无 Emoji 图标**：前端界面所有图标统一使用 `lucide-vue-next`，不使用 Emoji 表情。
7. **只读安全扫描**：Token 用量统计扫描 `~/.gemini/antigravity` 全程严格只读，不产生写锁、不修改任何原客户端数据。
8. **中文规范与版本控制**：所有代码注释、任务/计划文档均使用中文；每个里程碑完成后执行测试验证并提交 Git。

---

## 任务拆分与接口契约

### Task 1：初始化 Wails v2 + Go + Vue 3 工程脚手架

**涉及文件**：
- Create: `.gitignore`
- Create: `wails.json`
- Create: `go.mod`
- Create: `main.go`（最小可启动骨架）
- Create: `app.go`（最小绑定骨架）
- Create: `frontend/package.json`
- Create: `frontend/tsconfig.json`
- Create: `frontend/vite.config.ts`
- Create: `frontend/index.html`
- Create: `frontend/src/main.ts`
- Create: `frontend/src/App.vue`
- Create: `frontend/src/assets/styles/main.css`

**步骤**：
- [ ] **Step 1**：创建 `.gitignore`（忽略 `build/bin`, `frontend/node_modules`, `frontend/dist`, `frontend/wailsjs`, `.DS_Store`）和 `wails.json`（配置 `pnpm install` / `pnpm build` / `pnpm dev` 与产物名称 `MoreAccount`）。
- [ ] **Step 2**：初始化 `go.mod`（模块名 `more-account`），拉取 `github.com/wailsapp/wails/v2` 与 `modernc.org/sqlite`。
- [ ] **Step 3**：初始化 `frontend/` Vue 3 + TypeScript + Vite + Pinia + Tailwind CSS v4 + `lucide-vue-next`，执行 `pnpm install`。
- [ ] **Step 4**：编写最小 `main.go`、`app.go`、`frontend/src/App.vue`，执行 `pnpm build` 与 `go build ./...` 确认工具链畅通。
- [ ] **Step 5**：提交 Git：`chore: 初始化 Wails v2 + Go + Vue 3 工程脚手架`。

---

### Task 2：领域模型（`internal/model`）与原子化本地存储（`internal/storage`）

**涉及文件**：
- Create: `internal/model/account.go`
- Create: `internal/model/group.go`
- Create: `internal/model/history.go`
- Create: `internal/model/wakeup.go`
- Create: `internal/model/tokenusage.go`
- Create: `internal/model/settings.go`
- Create: `internal/storage/atomic.go`
- Create: `internal/storage/store.go`
- Test: `internal/storage/store_test.go`

**接口契约**：
- 产出核心结构体：`model.Account`、`model.TokenData`、`model.QuotaData`、`model.ModelQuota`、`model.CreditInfo`、`model.QuotaErrorInfo`、`model.AccountGroup`、`model.AppSettings`、`model.SwitchHistoryItem`、`model.WakeupState`、`model.WakeupTask`、`model.WakeupHistoryItem`、`model.TokenUsageQuery`、`model.TokenUsageBundle`、`model.ConversationDetail`。
- 产出仓储接口：`storage.NewStore(dataDir string) (*storage.Store, error)`，提供线程安全的账号 CRUD/重排/批量归组/当前账号设置、分组 CRUD（删除自动清空成员 `groupId`）、配置读写（默认 `Theme: "light"`, `LowQuotaThreshold: 20`, `RuntimeTarget: "desktop"`, `LaunchOnSwitch: true`）、切号历史与唤醒状态持久化。

**步骤**：
- [ ] **Step 1**：编写 `internal/storage/store_test.go`，覆盖原子写权限（0600）、账号增删改查去重、删除分组后账号 `groupId` 自动置空、默认配置检查（`Theme == "light"`）、历史上限裁剪（200 条）。
- [ ] **Step 2**：运行 `go test ./internal/storage/...` 确认编译失败（红灯）。
- [ ] **Step 3**：实现 `internal/model/*.go` 与 `internal/storage/atomic.go`、`store.go`。
- [ ] **Step 4**：运行 `go test -v ./internal/storage/...` 全部通过（绿灯）。
- [ ] **Step 5**：提交 Git：`feat(storage): 实现领域模型与原子化 JSON 存储层`。

---

### Task 3：TOTP 2FA 验证码引擎（`internal/totp`）与 Google OAuth 客户端（`internal/oauth`）

**涉及文件**：
- Create: `internal/totp/totp.go`
- Test: `internal/totp/totp_test.go`
- Create: `internal/oauth/client.go`
- Create: `internal/oauth/server.go`
- Test: `internal/oauth/client_test.go`

**接口契约**：
- `totp.GenerateAt(rawSecret string, at time.Time) (totp.CodeResult, error)` 与 `totp.NormalizeSecret(raw string) (string, error)`：支持纯 Base32（含空格、小写、无 `=` 填充）与 `otpauth://totp/...` URI，返回 6 位验证码、`Period`、`RemainingSeconds`。
- `oauth.BuildAuthURL(redirectURI, state string) string`
- `oauth.ExchangeCode(ctx context.Context, code, redirectURI string) (*oauth.TokenResponse, error)`
- `oauth.RefreshAccessToken(ctx context.Context, refreshToken string) (*oauth.TokenResponse, error)`（包含 `invalid_grant` 500ms 退避二次确认）
- `oauth.FetchUserInfo(ctx context.Context, accessToken string) (*oauth.UserInfo, error)`
- `oauth.EnsureFreshToken(ctx context.Context, token model.TokenData) (model.TokenData, bool, error)`（距离过期 ≤900s 自动刷新）
- `oauth.LoginManager`：启动 `127.0.0.1:0` 临时回调服务、等待授权回调或接收 `SubmitCallbackURL(rawURL string)` 手动粘贴兜底、支持 `Cancel()`。

**步骤**：
- [ ] **Step 1**：编写 `internal/totp/totp_test.go`（RFC 6238 标准向量 + `otpauth://` 解析 + 无效密钥报错）与 `internal/oauth/client_test.go`（基于 `httptest.Server` 测试授权 URL 参数、Token 刷新、`invalid_grant` 重试判定、手动提交回调 URL）。
- [ ] **Step 2**：运行 `go test ./internal/totp/... ./internal/oauth/...` 验证初始失败。
- [ ] **Step 3**：实现 `internal/totp` 与 `internal/oauth`。
- [ ] **Step 4**：运行 `go test -v ./internal/totp/... ./internal/oauth/...` 全部通过。
- [ ] **Step 5**：提交 Git：`feat(auth): 实现 TOTP 2FA 动态码引擎与 Google OAuth 登录模块`。

---

### Task 4：CloudCode 配额查询与并发刷新引擎（`internal/quota`）

**涉及文件**：
- Create: `internal/quota/client.go`
- Create: `internal/quota/parser.go`
- Create: `internal/quota/refresh.go`
- Test: `internal/quota/parser_test.go`

**接口契约**：
- `quota.ResolveBaseURL(ctx CloudCodeContext) string`（对齐原路由：override > GCP ToS+Project走 prod > daily）
- `quota.FetchAccountQuota(ctx context.Context, token model.TokenData, email string) (*quota.FetchResult, error)`：依次请求 `v1internal:loadCodeAssist`（提取订阅等级、AI 积分、ProjectID，必要时轮询 `v1internal:onboardUser`）与 `v1internal:fetchAvailableModels`（提取全部模型配额与 403/验证链接解析）。
- `quota.RefreshAll(ctx context.Context, store *storage.Store, concurrency int, onProgress func(done, total int)) (*model.RefreshStats, error)`。

**步骤**：
- [ ] **Step 1**：编写 `internal/quota/parser_test.go`，覆盖 CloudCode 域名路由、`loadCodeAssist` 响应解析（FREE/PRO/ULTRA + Credits）、`fetchAvailableModels` 模型过滤与百分比计算、Google 403 `VALIDATION_REQUIRED` + `validation_url` 错误提取。
- [ ] **Step 2**：运行 `go test ./internal/quota/...` 验证初始失败。
- [ ] **Step 3**：实现 `internal/quota/client.go`、`parser.go`、`refresh.go`。
- [ ] **Step 4**：运行 `go test -v ./internal/quota/...` 全部通过。
- [ ] **Step 5**：提交 Git：`feat(quota): 实现 CloudCode 配额查询与并发刷新模块`。

---

### Task 5：Antigravity 切号引擎（`internal/switcher`）与多源导入导出（`internal/importer`）

**涉及文件**：
- Create: `internal/switcher/protobuf.go`
- Create: `internal/switcher/credential.go`
- Create: `internal/switcher/credential_darwin.go`
- Create: `internal/switcher/credential_other.go`
- Create: `internal/switcher/statedb.go`
- Create: `internal/switcher/paths.go`
- Create: `internal/switcher/process.go`
- Create: `internal/switcher/switcher.go`
- Test: `internal/switcher/switcher_test.go`
- Create: `internal/importer/importer.go`
- Test: `internal/importer/importer_test.go`

**接口契约**：
- `switcher.CreateOAuthTokenInfo(...)` / `CreateUnifiedTopicEntry(...)` / `RemoveUnifiedTopicEntry(...)` / `ExtractRefreshTokenFromUnifiedOAuth(...)`：与 `cockpit-tools` 字节级兼容。
- `switcher.WriteSystemCredentials(account model.Account, syncCLI bool)` 与 `switcher.ReadDesktopCredential()`（macOS `security` Keychain + `go-keyring-base64:` + `~/.gemini/jetski-standalone-oauth-token` + `~/.gemini/antigravity-cli/antigravity-oauth-token`）。
- `switcher.InjectAccountToStateDB(dbPath string, account model.Account)` 与 `switcher.ReadRefreshTokenFromStateDB(dbPath string)`（使用 `modernc.org/sqlite`）。
- `switcher.Service.SwitchAccount(ctx context.Context, req SwitchRequest) (*SwitchOutcome, error)`：带单飞锁、预刷新 Token、按需关闭客户端及 macOS `agy --hub`、写入凭据、提交当前账号与切号历史、按需重启客户端。
- `importer.Service`：支持 `ImportFromLocal`（优先读 2.0 系统凭据/回退文件，回退读 IDE `state.vscdb`）、`ImportRefreshTokens`（单条/多行批量）、`ImportJSON`、`ExportJSON(ids []string, includeSecrets bool)`。

**步骤**：
- [ ] **Step 1**：编写 `internal/switcher/switcher_test.go`（Protobuf 往返编解码一致性、移除旧 sentinel 保留其他条目、临时 SQLite `state.vscdb` 注入与回读、2.0 `go-keyring-base64:` payload 编解码）与 `internal/importer/importer_test.go`（JSON 导入导出脱敏选项测试）。
- [ ] **Step 2**：运行 `go test ./internal/switcher/... ./internal/importer/...` 验证初始失败。
- [ ] **Step 3**：实现 `internal/switcher/*.go` 与 `internal/importer/importer.go`。
- [ ] **Step 4**：运行 `go test -v ./internal/switcher/... ./internal/importer/...` 全部通过。
- [ ] **Step 5**：提交 Git：`feat(switcher): 实现 Antigravity 2.0/IDE/CLI 切号引擎与多源导入导出`。

---

### Task 6：Token 用量只读分析引擎（`internal/tokenusage`）与定时唤醒调度器（`internal/wakeup`）

**涉及文件**：
- Create: `internal/tokenusage/protobuf.go`
- Create: `internal/tokenusage/models.go`
- Create: `internal/tokenusage/scanner.go`
- Create: `internal/tokenusage/aggregator.go`
- Test: `internal/tokenusage/tokenusage_test.go`
- Create: `internal/wakeup/executor.go`
- Create: `internal/wakeup/scheduler.go`
- Test: `internal/wakeup/scheduler_test.go`

**接口契约**：
- `tokenusage.Service`：
  - `Query(query model.TokenUsageQuery) (*model.TokenUsageBundle, error)`（有内存快照时零阻塞返回）
  - `Sync(rebuild bool, query model.TokenUsageQuery) (*model.TokenUsageBundle, error)`（严格只读打开 `conversation_summaries.db` 与 `conversations/*.db`，结合文件指纹增量缓存 `~/.more-account/token_usage_cache.json`）
  - `GetConversationDetail(conversationID string) (*model.ConversationDetail, error)`（按需解析步级消耗）
- `wakeup.Service`：
  - `Start(ctx context.Context)` / `Stop()`（后台轻量轮询调度器）
  - `TestNow(ctx context.Context, req model.WakeupTestRequest) (*model.WakeupHistoryItem, error)` & `CancelTest(scopeID string)`
  - `SaveState(state model.WakeupState) error` / `GetState() model.WakeupState` / `GetHistory() []model.WakeupHistoryItem` / `ClearHistory() error`
  - 支持调度模式：`daily`、`weekly`、`interval`、`crontab`、`quota_reset`

**步骤**：
- [ ] **Step 1**：编写 `internal/tokenusage/tokenusage_test.go`（Protobuf 解码校验 Thinking 零双计不变量、模型枚举规范化、临时目录构造真实 `conversation_summaries.db` + `conversations/<id>.db` 验证扫描聚合与缓存命中）与 `internal/wakeup/scheduler_test.go`（SSE 响应解析、每日/每周/间隔/Cron 触发时间判定）。
- [ ] **Step 2**：运行 `go test ./internal/tokenusage/... ./internal/wakeup/...` 验证初始失败。
- [ ] **Step 3**：实现 `internal/tokenusage/*.go` 与 `internal/wakeup/*.go`。
- [ ] **Step 4**：运行 `go test -v ./internal/tokenusage/... ./internal/wakeup/...` 全部通过。
- [ ] **Step 5**：提交 Git：`feat(analytics): 实现只读 Token 用量统计与定时唤醒调度器`。

---

### Task 7：Wails 后端绑定层（`app.go`）与桌面窗口配置（`main.go`）

**涉及文件**：
- Modify: `app.go`
- Modify: `main.go`

**接口契约**：
- 统一暴露前端所需的全部绑定方法：
  - 账号：`ListAccounts`、`GetCurrentAccountId`、`AddAccountByRefreshToken`、`ImportRefreshTokens`、`ImportFromLocal`、`ImportFromJSON`、`ExportAccounts`、`DeleteAccount`、`DeleteAccounts`、`ReorderAccounts`、`UpdateAccountGroup`、`BatchUpdateAccountGroup`、`UpdateAccountDetails`（备注/密码/2FA 密钥）、`GenerateTOTP`
  - OAuth：`StartOAuthLogin`、`SubmitOAuthCallbackURL`、`CancelOAuthLogin`
  - 配额：`RefreshAccountQuota`、`RefreshAllQuotas`
  - 切号与历史：`SwitchAccount`、`GetSwitchHistory`、`ClearSwitchHistory`、`DetectInstalledClients`
  - 分组：`ListGroups`、`SaveGroup`、`DeleteGroup`
  - Token 用量：`QueryTokenUsage`、`SyncTokenUsage`、`GetConversationDetail`
  - 定时唤醒：`GetWakeupState`、`SaveWakeupState`、`RunWakeupTest`、`RunWakeupTaskNow`、`CancelWakeupTest`、`GetWakeupHistory`、`ClearWakeupHistory`
  - 设置与工具：`GetSettings`、`SaveSettings`、`OpenExternalURL`、`OpenDataFolder`
- 推送 Wails Events：`quota:progress`、`wakeup:updated`、`accounts:changed`。

**步骤**：
- [ ] **Step 1**：完整编写 `app.go` 与 `main.go`（配置 macOS TitlebarAppearsTransparent / HideTitlebar / 默认宽敞窗口 `1280x820`，最小尺寸 `980x640`）。
- [ ] **Step 2**：运行 `go test ./...` 与 `go vet ./...` 验证全后端编译与测试零报错。
- [ ] **Step 3**：提交 Git：`feat(app): 完成 Wails 后端服务绑定与桌面窗口集成`。

---

### Task 8：全新前端基础层（设计令牌、Wails 桥接、Pinia Stores 与领域工具）

**涉及文件**：
- Modify: `frontend/src/assets/styles/main.css`
- Create: `frontend/src/types/models.ts`
- Create: `frontend/src/services/api.ts`（优先调用 Wails 绑定，无 Wails 运行时提供开发提示）
- Create: `frontend/src/utils/quota.ts`（模型系列归类、配额语义色、倒计时格式化、低配额/异常筛选判断）
- Create: `frontend/src/utils/privacy.ts`（邮箱脱敏与剪贴板辅助）
- Create: `frontend/src/stores/appStore.ts`（账号、分组、设置、当前视图、选中的抽屉账号、Toast 队列）
- Create: `frontend/src/stores/tokenUsageStore.ts`
- Create: `frontend/src/stores/wakeupStore.ts`
- Create: `frontend/src/components/common/ToastContainer.vue`
- Create: `frontend/src/components/common/ConfirmDialog.vue`
- Create: `frontend/src/components/common/QuotaBar.vue`

**步骤**：
- [ ] **Step 1**：在编写前端代码前先读取 `modern-web-guidance` 与 `ui-ux-pro-max` 技能，确保现代 CSS/交互与可访问性最佳实践。
- [ ] **Step 2**：实现浅色默认 + 手动暗色切换的 CSS 变量系统（绝不绑定 `prefers-color-scheme` 自动切换暗色）、`tabular-nums` 数字排版、自定义清爽滚动条。
- [ ] **Step 3**：实现 `types/models.ts`、`services/api.ts`、`utils/*`、`stores/*` 及通用组件。
- [ ] **Step 4**：执行 `pnpm typecheck` 确认零类型错误。
- [ ] **Step 5**：提交 Git：`feat(frontend): 搭建 Vue 3 主题系统、状态管理与 Wails 服务桥接`。

---

### Task 9：三栏工作台布局与账号管理核心视图

**涉及文件**：
- Create: `frontend/src/components/layout/AppSidebar.vue`（左栏：macOS 拖拽顶区 + 4 模块导航 + 自建分组树 + 当前账号胶囊 + 隐私/主题/设置工具栏）
- Create: `frontend/src/components/accounts/AccountCard.vue`（高信息密度但留白舒适的账号卡片）
- Create: `frontend/src/components/accounts/AccountTable.vue`（紧凑数据表格视图）
- Create: `frontend/src/components/accounts/AccountDetailDrawer.vue`（右侧可收起 360px 详情抽屉：切号/刷新/唤醒操作 + 实时 2FA TOTP 30s 圆环动态码 + 密码 + 备注 + 全量模型配额 + 验证链接）
- Create: `frontend/src/components/accounts/AddAccountModal.vue`（3 Tab：OAuth 浏览器登录含手动回调兜底 / 本机当前登录导入 / Refresh Token 与 JSON 批量导入）
- Create: `frontend/src/components/accounts/GroupModal.vue`（新建/编辑分组，含配色选择）
- Create: `frontend/src/components/accounts/ExportModal.vue`（JSON 导出，可选是否含密码与 2FA 密钥）
- Create: `frontend/src/views/AccountsView.vue`（宽松单行顶栏 + 聚合筛选下拉[订阅/低配额/异常] + 多选批量栏 + 卡片/表格切换）
- Create: `frontend/src/components/settings/SettingsModal.vue`（运行目标 Desktop/IDE/CLI、切号后自动重启开关、低配额阈值、客户端路径配置、打开数据目录）

**步骤**：
- [ ] **Step 1**：实现左栏 `AppSidebar.vue` 与设置弹窗 `SettingsModal.vue`。
- [ ] **Step 2**：实现账号卡片、表格、详情抽屉（含每秒刷新 2FA TOTP 圆环倒计时与一键复制）以及添加/分组/导出弹窗。
- [ ] **Step 3**：组装 `AccountsView.vue`，确保搜索、聚合筛选（订阅等级 + 可选低配额 + 可选异常）、排序、自建分组切换、批量归组/删除流畅无拥挤感。
- [ ] **Step 4**：执行 `pnpm typecheck` 验证零错误。
- [ ] **Step 5**：提交 Git：`feat(frontend): 实现全新三栏工作台布局与账号管理核心视图`。

---

### Task 10：Token 用量统计、定时唤醒任务与切号历史视图

**涉及文件**：
- Create: `frontend/src/components/tokenusage/UsageTrendChart.vue`（纯 SVG 响应式堆叠柱状图 + 悬浮 Tooltip）
- Create: `frontend/src/components/tokenusage/ConversationDetailDrawer.vue`（右侧滑出单对话步级明细抽屉）
- Create: `frontend/src/views/TokenUsageView.vue`（KPI 卡片 + 趋势图 + 模型/工作区分布条 + 分页对话表 + 同步/重建/目录设置）
- Create: `frontend/src/components/wakeup/WakeupTaskModal.vue`（新建/编辑定时唤醒任务：每日/每周/间隔/Cron/配额重置、多选账号与模型、极简 Prompt）
- Create: `frontend/src/views/WakeupView.vue`（全局开关 + 任务卡片列表 + 即时测试与取消 + 唤醒执行历史 Tab）
- Create: `frontend/src/views/SwitchHistoryView.vue`（切号历史时间线表格 + 筛选 + 清空）
- Modify: `frontend/src/App.vue`（整合三栏框架、四视图路由切换与全局快捷键 `Esc` 收起抽屉）

**步骤**：
- [ ] **Step 1**：实现 `TokenUsageView.vue` 及趋势图与对话详情抽屉。
- [ ] **Step 2**：实现 `WakeupView.vue`、`WakeupTaskModal.vue` 与 `SwitchHistoryView.vue`。
- [ ] **Step 3**：完成 `App.vue` 集成与初始化数据加载（启动时拉取配置应用浅/暗主题、加载账号/分组、监听 Wails 后端进度事件）。
- [ ] **Step 4**：执行 `pnpm typecheck && pnpm build` 验证前端编译通过。
- [ ] **Step 5**：提交 Git：`feat(frontend): 完成 Token 用量统计、定时唤醒与切号历史视图`。

---

### Task 11：Wails 绑定生成、真实原生打包验证与质量核查

**步骤**：
- [ ] **Step 1**：运行 `wails generate module` 或 `wails build` 生成正式 `frontend/wailsjs/` 绑定并验证前后端方法签名完全对齐。
- [ ] **Step 2**：执行全量后端单元测试：`go test -race -v ./...`。
- [ ] **Step 3**：执行全量前端类型检查与生产构建：`cd frontend && pnpm typecheck && pnpm build`。
- [ ] **Step 4**：执行 `wails build` 生成真实 macOS 原生 `.app` 应用包，检查产物完整性，确认零 Emoji 残留（通过正则搜索 `frontend/src`）。
- [ ] **Step 5**：提交最终收尾更改（确保工作区 `git status` 干净）。
