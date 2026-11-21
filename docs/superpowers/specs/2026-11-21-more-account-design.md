# More Account（反重力账号管理）系统设计文档

- **日期**：2026-11-21
- **目标**：从 `cockpit-tools` 中抽离并重写「反重力（Antigravity）」单平台账号管理功能，构建轻量、独立、界面全新的原生桌面应用。
- **技术栈**：Wails v2 (`v2.12.0`) + Go (`1.26.1`) + Vue 3 (TypeScript) + Pinia + Tailwind CSS v4 + `lucide-vue-next`

---

## 1. 范围与边界

### 1.1 明确纳入的功能
1. **账号接入与维护**
   - Google OAuth 2.0 浏览器授权登录（自动启动本地 HTTP 回调监听，同时支持手动粘贴回调 URL 兜底）
   - Refresh Token 单条/批量导入
   - 一键导入本机当前已登录的 Antigravity 账号（兼容 2.0 系统凭据/回退文件 与 旧版/IDE `state.vscdb`）
   - JSON 账号批量导入与导出（导出时可选是否包含密码与 2FA 密钥）
   - 账号编辑、手动排序、单个/批量删除、搜索、筛选（包含「全部 / 订阅等级 / 低配额 / 异常」可选筛选，布局保持简洁不拥挤）、排序与全局隐私脱敏模式
2. **自定义账号分组（彻底取代原有标签）**
   - 移除原有 `tags` 机制，改为单归属自建分组（`groupId`，未分配的归入「未分组」）
   - 分组支持新建、重命名、配色标识、排序与删除（删除分组仅将组内账号移至「未分组」，不删除账号）
   - 支持单个/批量将账号归入分组
3. **配额监控**
   - 单账号与全部账号并发刷新配额（基于 Google CloudCode `loadCodeAssist` / `onboardUser` / `fetchAvailableModels`）
   - 展示模型分组剩余百分比、精确重置倒计时、订阅等级（FREE / PRO / ULTRA）、AI Credits、GCP ToS 标识
   - 解析并展示 403 / `VALIDATION_REQUIRED` / `SUBSCRIPTION_REQUIRED` / `invalid_grant` 等异常状态及 Google 验证链接
4. **一键切换账号**
   - 切换前自动检查并刷新临期/过期 Token（含单飞并发锁，防止竞争写入）
   - 兼容三种运行目标切换：
     - **Antigravity Desktop（2.0 桌面版）**：写入系统凭据（macOS Keychain `gemini/antigravity`、Windows Credential Manager `gemini:antigravity`、Linux `secret-tool`）+ 同步写入 `~/.gemini/jetski-standalone-oauth-token` + 若 `~/.gemini/antigravity-cli` 已初始化则同步 CLI 凭据；可选自动关闭运行中的 Antigravity 及 macOS `agy --hub` 协同重启
     - **Antigravity IDE / 旧版 Desktop**：写入 `state.vscdb` 的 `ItemTable`（Protobuf + Base64 编码的 `antigravityUnifiedStateSync.oauthToken`、`userStatus`、`enterprisePreferences` 及 `antigravityOnboarding`）
     - **Antigravity CLI (`agy`)**：同步写入系统凭据、桌面回退文件及 `~/.gemini/antigravity-cli/antigravity-oauth-token`
   - 支持「切换并重启客户端」与「仅写入本地凭据」两种动作
5. **账号凭据与备注保险箱（右侧详情抽屉内）**
   - 自由备注（内联保存）
   - 账号密码（默认掩码，支持显隐切换与一键复制）
   - 2FA TOTP 密钥与实时 6 位动态验证码（RFC 6238，支持 Base32 密钥与 `otpauth://` URI，带 30 秒倒计时圆环与一键复制）
   - **明确移除**：辅助邮箱、手机号、邮件预览链接等冗余字段
6. **切号历史记录**
   - 持久化记录每次切号的时间、目标账号、目标客户端、是否重启、耗时（ms）、执行结果及错误阶段详情，支持清空
7. **Token 用量统计**
   - 严格只读扫描 `~/.gemini/antigravity`（支持自定义数据目录），兼容读取 `conversation_summaries.db`、`annotations/*.pbtxt`、`brain/*/.system_generated/` 与 `conversations/*.db`
   - 文件指纹增量缓存（存放于程序独立数据目录 `~/.more-account/token_usage_cache.json`）
   - 严格区分 API 精确统计（Input / Output / Thinking / Cache Read / Cache Write，确保 Thinking 不重复计入 Output）与启发式估算
   - 支持时间预设（今日 / 7 天 / 30 天 / 本月 / 全部）、按天/周/月趋势图、模型分布、工作区分布、对话列表（Main / Subagent / Swarm）及点击展开单对话步级消耗明细
8. **定时唤醒任务**
   - 支持新建、编辑、启停、删除唤醒任务及全局调度器总开关
   - 调度规则：每日定点、每周定点、固定间隔（带时间窗）、Cron 表达式、配额重置后自动触发
   - 唤醒执行：向 CloudCode `/v1internal:streamGenerateContent?alt=sse` 发送极轻请求（默认提示词 `hi`，限制 `maxOutputTokens`），支持立即手动测试、随时取消正在运行的测试，并持久化唤醒历史记录

### 1.2 明确排除的功能
- 所有其他平台（Codex / Cursor / Windsurf / Kiro / Trae / Qoder / Zed / WorkBuddy / CodeBuddy / Copilot / Claude / Grok / Minimax / Zcode 等）
- 应用多开（实例管理、隔离用户目录、多实例并行启动）
- 原有账号标签（`tags`）
- 辅助邮箱、手机号与外部邮箱正文拉取
- 自动跟随操作系统暗色模式（默认始终为浅色模式，仅在用户手动切换时进入暗色模式）
- 定时自动刷新配额与低配额自动切号（保持切号完全可控）

---

## 2. 系统架构与目录规划

### 2.1 技术选型原则
- **纯 Go 无 CGO**：使用 `modernc.org/sqlite` 读取/写入 `state.vscdb` 及只读扫描 `conversations/*.db`，保证跨平台编译和 Wails 打包无需 C 工具链。
- **独立数据目录**：默认使用 `~/.more-account/`（支持环境变量 `MORE_ACCOUNT_DATA_DIR` 覆盖便于测试隔离），绝不污染 `cockpit-tools` 或 `~/.gemini/antigravity` 内部文件。
- **原子持久化**：所有本地 JSON 文件统一通过「写入同目录临时文件 -> `Sync` -> `Rename`」原子替换，并配合内存读写锁保护并发一致性。

### 2.2 代码仓库结构

```text
more-account/
├── wails.json                          # Wails v2 项目配置
├── go.mod / go.sum                     # Go 模块依赖
├── main.go                             # 桌面窗口启动入口（macOS 隐式标题栏融合、窗口尺寸记忆）
├── app.go                              # Wails Binding 薄封装层与事件桥接
├── internal/
│   ├── model/                          # 核心领域模型定义（全 JSON camelCase 对齐前端）
│   │   ├── account.go                  # Account / TokenData / QuotaData / ModelQuota / CreditInfo / QuotaErrorInfo
│   │   ├── group.go                    # AccountGroup
│   │   ├── history.go                  # SwitchHistoryItem
│   │   ├── wakeup.go                   # WakeupTask / WakeupSchedule / WakeupHistoryItem
│   │   ├── tokenusage.go               # TokenUsageQuery / DashboardBundle / ConversationDetail
│   │   └── settings.go                 # AppSettings（主题、隐私模式、运行目标、客户端路径、低配额阈值等）
│   ├── storage/                        # 独立数据目录管理与原子化 JSON 仓储
│   │   ├── atomic.go                   # 原子文件写入工具（0600 敏感文件权限）
│   │   ├── store.go                    # 账号/分组/配置/历史/唤醒数据的统一线程安全仓储
│   │   └── store_test.go
│   ├── oauth/                          # Google OAuth 2.0 登录与 Token 刷新
│   │   ├── client.go                   # OAuth URL 构建、Code 换 Token、Refresh Token 刷新（含 invalid_grant 退避重试）、UserInfo 获取
│   │   ├── server.go                   # 临时本地回调 HTTP 服务、状态机管理、手动粘贴回调 URL 兜底
│   │   └── client_test.go
│   ├── quota/                          # CloudCode 配额查询引擎
│   │   ├── client.go                   # CloudCode 域名路由（prod/daily）、loadCodeAssist、onboardUser、fetchAvailableModels
│   │   ├── parser.go                   # 订阅等级、AI Credits、模型配额、错误原因与 validation_url 解析
│   │   ├── refresh.go                  # 并发批量刷新池（受控并发 + 进度事件推送）
│   │   └── parser_test.go
│   ├── switcher/                       # Antigravity 账号切换与客户端进程控制
│   │   ├── protobuf.go                 # 零依赖手写 Protobuf Wire 编解码器（OAuthTokenInfo / UnifiedTopic / UserStatus / EnterprisePref）
│   │   ├── credential.go               # 2.0 系统凭据（macOS Keychain / Win CredMan / Linux secret-tool）+ 回退文件 + CLI 文件读写
│   │   ├── statedb.go                  # IDE/旧版 state.vscdb ItemTable 读写
│   │   ├── paths.go                    # 跨平台 Antigravity / Antigravity IDE 安装路径与数据目录探测
│   │   ├── process.go                  # 客户端进程检测、优雅退出、macOS agy --hub 协同、客户端拉起
│   │   ├── switcher.go                 # 单飞锁切号编排（Token 预刷新 -> 停进程 -> 写凭据/DB -> 提交状态 -> 记历史 -> 重启）
│   │   └── switcher_test.go
│   ├── importer/                       # 多源账号导入与导出
│   │   ├── importer.go                 # 本机登录账号提取、Refresh Token 批量导入、JSON 导入导出
│   │   └── importer_test.go
│   ├── totp/                           # RFC 6238 TOTP 生成器
│   │   ├── totp.go                     # Base32 / otpauth:// 解析、6 位验证码与剩余秒数计算
│   │   └── totp_test.go
│   ├── tokenusage/                     # 本地 Token 用量只读分析引擎
│   │   ├── protobuf.go                 # 对话步骤与生成元数据 Protobuf 解码器
│   │   ├── models.go                   # 模型 ID 规范化与显示名映射
│   │   ├── scanner.go                  # 只读并发扫描 ~/.gemini/antigravity + 文件指纹增量缓存
│   │   ├── aggregator.go               # 时间区间、趋势桶、模型/工作区占比与分页对话聚合
│   │   └── tokenusage_test.go
│   └── wakeup/                         # 定时唤醒执行与后台调度器
│       ├── executor.go                 # CloudCode SSE 极轻请求发送、流式响应解析、可取消上下文
│       ├── scheduler.go                # 后台轮询调度（每日/每周/间隔/Cron/配额重置触发）、防重与唤醒历史记录
│       └── scheduler_test.go
└── frontend/                           # Vue 3 前端工程
    ├── package.json
    ├── vite.config.ts
    ├── index.html
    └── src/
        ├── main.ts
        ├── App.vue
        ├── assets/styles/              # Tailwind v4 入口 + 浅/深色设计令牌（CSS Variables）
        ├── types/                      # 前后端数据契约类型与 Wails 调用适配层
        ├── stores/                     # Pinia 状态管理（accounts / groups / settings / tokenUsage / wakeup / history）
        ├── utils/                      # 配额分组归类、倒计时格式化、隐私脱敏、低配额/异常判断、前端本地 TOTP 刷新
        ├── components/
        │   ├── layout/                 # Sidebar（模块导航+自建分组树+当前账号胶囊）、TopBar
        │   ├── accounts/               # AccountCard、AccountTable、AccountDetailDrawer、AddAccountModal、GroupManageModal、ExportModal
        │   ├── tokenusage/             # UsageKpiCards、UsageTrendChart（纯 SVG）、UsageBreakdown、ConversationDetailDrawer
        │   ├── wakeup/                 # WakeupTaskCard、WakeupTaskModal、WakeupHistoryList
        │   └── common/                 # ConfirmDialog、ToastContainer、QuotaBar、Badge
        └── views/                      # AccountsView、TokenUsageView、WakeupView、SwitchHistoryView、SettingsModal
```

---

## 3. 核心协议与数据模型设计

### 3.1 本地数据文件布局（`~/.more-account/`）
- `accounts.json`：账号列表、排序顺序与当前激活账号 ID（文件权限 `0600`）
- `groups.json`：自建分组列表
- `settings.json`：应用配置
  - `theme`: `"light"` | `"dark"`（**默认 `"light"`，不跟随系统暗色**）
  - `privacyMode`: `boolean`（默认 `false`）
  - `runtimeTarget`: `"desktop"` | `"ide"` | `"cli"`（默认 `"desktop"`）
  - `launchOnSwitch`: `boolean`（默认 `true`）
  - `lowQuotaThreshold`: `number`（默认 `20`，用于「低配额」筛选判定）
  - `desktopAppPath` / `ideAppPath`: 可选自定义客户端可执行路径
  - `tokenUsageDataDir`: 可选自定义扫描目录（留空默认 `~/.gemini/antigravity`）
- `switch_history.json`：切号历史（最多保留最近 200 条）
- `wakeup_state.json`：唤醒总开关与任务列表
- `wakeup_history.json`：唤醒执行历史（最多保留最近 200 条）
- `token_usage_cache.json`：Token 用量只读扫描增量指纹缓存

### 3.2 关键业务规则定义
1. **模型分组展示与配额归类**
   - 将 CloudCode 返回的原始模型按系列归类（如 Gemini Pro、Gemini Flash、Claude 等），主列表卡片/表格展示核心模型组剩余百分比与最近重置时间，右侧抽屉展示账号下全部原始模型明细。
2. **「低配额 / 异常」筛选判定规则**
   - **低配额**：账号未禁用且已拉取配额，其任意核心模型剩余百分比 `<= lowQuotaThreshold`（默认 `20%`）。
   - **异常**：满足以下任一条件——`disabled == true`、`quota.isForbidden == true`、`quotaError != nil`（如 403、需验证 `VALIDATION_REQUIRED`、需订阅 `SUBSCRIPTION_REQUIRED`、`invalid_grant`）。
3. **2FA TOTP 实时验证码**
   - 后端提供 `GenerateTOTP(secret)` 校验与解析接口（支持标准 Base32 密钥与 `otpauth://totp/...` URI），前端在打开账号详情抽屉时每秒刷新剩余倒计时并在 30 秒周期切换时更新 6 位动态码（隐私模式下自动掩码为 `••• •••`，仍支持显隐与一键复制）。
4. **OAuth 授权与安全兜底**
   - 启动本地随机端口 `127.0.0.1:<port>/oauth-callback` 监听并用系统默认浏览器打开 Google 授权页；
   - 若因网络代理或浏览器跳转受阻，用户可直接将浏览器地址栏的回调链接粘贴到弹窗的「手动提交回调 URL」输入框完成 Token 交换。
5. **切换账号数据一致性**
   - 全程由 `switcher` 内的单飞互斥锁保护；
   - 先校验并刷新临期 Token（距离过期 ≤15 分钟自动刷新）-> 若需重启则先优雅停止客户端与 macOS 共享 `agy --hub` -> 写入凭据（2.0 Keychain/CredMan/secret-tool + 回退文件，或 IDE `state.vscdb`）-> 仅在凭据写入成功后才提交当前账号状态并记录切号历史 -> 重启客户端；
   - 若凭据已切换成功但重启客户端失败（例如未找到安装路径），明确向前端返回「凭据已切换，但启动客户端失败」并引导用户在设置中指定路径，不回滚已写入状态。

---

## 4. 全新前端页面布局与视觉设计规范

### 4.1 视觉主题与图标约束
- **主题策略**：**默认浅色主题**，绝不自动跟随系统暗色模式；左下角提供一键「浅色 / 暗色」手动切换按钮并持久化至 `settings.json`。
- **浅色主色调（默认）**：
  - 侧边栏与画布背景：`#F7F8FA` / `#FFFFFF`
  - 边框分割线：`#E5E7EB`
  - 主文字 / 次文字：`#111827` / `#6B7280`
  - 品牌强调色：沉稳靛蓝 `#2563EB`（悬浮 `#1D4ED8`，柔底 `#EFF6FF`）
- **暗色主色调（手动切换）**：
  - 侧边栏与画布背景：`#0F1115` / `#161920`
  - 边框分割线：`#262B36`
  - 主文字 / 次文字：`#F3F4F6` / `#9CA3AF`
  - 品牌强调色：`#3B82F6`
- **配额健康度语义色**：
  - 充足（≥60%）：翡翠绿 `#10B981`
  - 偏低（20%–59%）：琥珀橙 `#F59E0B`
  - 告警/枯竭（<20% 或异常）：珊瑚红 `#EF4444`
- **图标与排版规范**：
  - 全部图标统一使用 `lucide-vue-next` 线性矢量图标（16px/18px），**严禁使用任何 Emoji 表情**。
  - 所有百分比、倒计时、Token 计数、耗时、2FA 验证码统一启用 `font-variant-numeric: tabular-nums` 保证对齐不抖动。

### 4.2 三栏工作台布局规范
1. **左栏（固定 220px，含 macOS 红绿灯顶部安全留白）**
   - **主导航区**：4 个精简入口（账号工作台 / Token 用量 / 定时唤醒 / 切号历史）
   - **自建分组树**（主导航下方常驻）：
     - 「全部账号」（显示总数）、「未分组」（显示数量）、以及用户创建的各自定义分组（彩色圆点 + 名称 + 数量徽章）
     - 标题行右侧提供 `+` 按钮新建分组，悬浮分组项可编辑/删除
   - **底部状态区**：
     - **当前激活账号胶囊**：展示当前生效账号的邮箱（遵循隐私脱敏）、订阅等级微章、运行目标标签（Desktop / IDE / CLI），点击可直接展开右侧详情抽屉
     - 底部轻量工具行：隐私脱敏开关、浅/暗色手动切换、设置弹窗入口
2. **中栏（自适应主工作区）**
   - **账号工作台视图**：
     - **单行精简顶栏（避免拥挤）**：左侧搜索输入框 + 一个聚合「筛选」下拉按钮（内部可选：订阅等级 `FREE/PRO/ULTRA`、以及可选的**「仅看低配额」**与**「仅看异常」**开关，选中时以轻量小胶囊提示当前筛选状态，可一键清除）+ 排序下拉 + 卡片/表格切换按钮；右侧「刷新全部」按钮 + 主按钮「添加账号」。
     - **多选批量条**：仅在勾选 ≥1 个账号时在列表顶部平滑浮现（批量归组、批量导出、批量删除、取消选择），平时零占位。
     - **双视图内容区**：
       - **卡片网格视图（默认）**：响应式 2–3 列，单张卡片包含：当前激活左侧色条/状态点、脱敏邮箱、订阅等级与分组小徽章、2–3 条核心模型组配额条与最近重置倒计时、异常状态提示图标、底部「一键切换」与「刷新」操作。
       - **紧凑表格视图**：高密度单行展示账号、分组、订阅、核心模型进度条、最近重置时间、状态与操作按钮，适合账号数较多时快速扫视。
   - **Token 用量视图**：单行筛选顶栏（时间范围预设 + 类型筛选 + 增量同步/重建缓存 + 数据目录设置）-> 4 卡核心指标 -> 纯 SVG 趋势柱状图 + 模型/工作区分布条 -> 可搜索分页对话表（点击在右侧抽屉查看步级详情）。
   - **定时唤醒视图**：单行顶栏（全局调度器总开关 + 新建唤醒任务 + 切换「任务列表 / 执行历史」）-> 任务卡片网格（调度规则摘要、绑定账号数与模型、上次执行状态、启停开关、立即测试、编辑/删除）。
   - **切号历史视图**：单行顶栏（状态筛选 + 清空历史）-> 清晰记录列表（时间、目标账号、目标端、模式、耗时、结果/诊断信息）。
3. **右栏（可收起 360px 账号详情抽屉）**
   - 仅在选中账号时平滑滑出，点击关闭按钮或按 `Esc` 收起，不阻塞中栏操作。
   - 内容分区：
     1. **头部操作区**：完整邮箱（支持一键复制）、订阅等级 / GCP ToS / AI Credits 徽章、所属分组下拉切换、主操作按钮组（切换并重启 / 仅写凭据 / 刷新配额 / 测试唤醒）。
     2. **凭据与备注区**：
        - 实时 2FA TOTP 动态码卡片（6 位分段数字 + 30s SVG 圆环倒计时 + 一键复制 + 配置/修改密钥）
        - 账号密码行（掩码/明文切换 + 一键复制 + 编辑）
        - 备注多行文本框（失焦或点击保存）
     3. **异常诊断区**（仅账号有异常时出现）：清晰展示错误原因、状态码及可一键打开/复制的 Google 验证链接。
     4. **全量模型配额明细**：列出该账号全部可用模型、剩余百分比进度条与精确重置时间。
     5. **底部危险区**：导出该账号 JSON、删除账号（带二次确认）。

---

## 5. 错误处理与测试验证策略

### 5.1 错误边界与可观测性
- 所有 Wails 绑定方法返回结构化中文可读错误信息，前端通过轻量 Toast 与内联错误条展示，不做静默吞错。
- Token 刷新遇到 Google `invalid_grant` 时按原项目策略短暂退避重试一次，确认失效后将账号标记为异常状态（并在「异常」筛选中直接可见），刷新恢复成功后自动解除禁用标记。
- 本地扫描 `~/.gemini/antigravity` 时若目录不存在或某个单个 SQLite 文件正在被写入/损坏，跳过坏文件并记录告警计数，不导致整个用量面板崩溃。

### 5.2 验证计划
1. **Go 单元测试（`go test ./...`）**：
   - `internal/storage`：原子写、并发读写、分组删除归回未分组
   - `internal/totp`：标准 RFC 6238 测试向量、Base32 空格/大小写兼容、`otpauth://` URI 解析
   - `internal/switcher`：Protobuf `OAuthTokenInfo` / `UnifiedTopic` 编解码与原 `cockpit-tools` 字节级一致、`state.vscdb` 注入与回读、2.0 凭据 payload 序列化/反序列化（含 `go-keyring-base64:`）
   - `internal/quota`：CloudCode 域名路由、订阅等级与 Credits 提取、`VALIDATION_REQUIRED` / `validation_url` 解析
   - `internal/tokenusage`：模拟 SQLite 对话库与 Protobuf 步骤解码、增量缓存命中、Thinking 不重复计入 Output 不变量校验
   - `internal/wakeup`：每日/每周/间隔/Cron/配额重置触发时间判定
2. **前端校验与构建**：
   - `pnpm typecheck`（`vue-tsc --noEmit`）零错误
   - `pnpm build` 产出纯净静态资源
3. **Wails 原生应用集成打包**：
   - `wails build` 在本机 macOS 完整编译出 `.app` 可执行包并验证启动无报错。
