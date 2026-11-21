# More Account

轻量级独立的 **Antigravity（反重力）** 账号管理桌面程序。提取自 `cockpit-tools` 的反重力核心能力并彻底剥离多平台冗余与应用多开功能，采用 **Wails v2 + Go + Vue 3** 全新构建。

## 核心功能

- **多方式接入账号**：Google OAuth 2.0 浏览器授权（含本地自动回调与手动粘贴回调 URL 兜底）、一键读取本机当前登录的反重力账号、Refresh Token 与 JSON 批量导入导出。
- **自定义分组管理**：移除零散标签体系，改用单归属自建分组（支持配色与一键归组，删除分组自动归入「未分组」）。
- **实时配额监控与可选筛选**：并发拉取 CloudCode 模型剩余额度、重置倒计时、订阅等级（FREE / PRO / ULTRA）、AI 积分及异常/验证链接诊断；顶栏聚合筛选支持按订阅等级与可选的「低配额 / 异常」过滤。
- **一键切换账号**：内置单飞并发锁，自动刷新临期令牌，兼容 Antigravity 2.0 桌面版（系统凭据 + `~/.gemini/jetski-standalone-oauth-token` + 可选同步 CLI）、Antigravity IDE（`state.vscdb` Protobuf 注入）与 Antigravity CLI，支持切换并自动重启或仅写凭据。
- **账号详情抽屉**：实时 2FA TOTP 6 位动态验证码（30 秒倒计时圆环 + 一键复制）、密码掩码查看与复制、自由备注、全量模型配额明细。
- **Token 用量统计**：严格只读扫描 `~/.gemini/antigravity`（文件指纹增量缓存），精确区分 Input / Output / Thinking / Cache，提供趋势图、模型/工作区排行及单对话步级明细。
- **定时唤醒任务与切号历史**：支持每日、每周、固定间隔、Cron 及配额重置触发唤醒，保留完整唤醒与切号执行历史。
- **轻量界面体验**：全新三栏工作台布局，**默认浅色主题（不跟随系统暗色）**，支持左下角手动切换浅色 / 暗色模式与全局隐私脱敏。

## 本地开发与打包

### 环境要求
- Go 1.24+
- Node.js 20+ 与 `pnpm`
- Wails CLI v2（`wails version`）

### 常用命令

```bash
# 前端依赖安装、类型检查与构建
cd frontend && pnpm install && pnpm typecheck && pnpm build

# 运行全部 Go 后端单元测试（含并发竞争检测）
go test -race ./...

# 开发调试模式（热重载）
wails dev

# 编译原生桌面安装包（输出至 build/bin/）
wails build
```

## 数据目录

所有配置与账号数据独立保存在 `~/.more-account/`（可通过环境变量 `MORE_ACCOUNT_DATA_DIR` 覆盖），不修改或污染其他工具目录；敏感文件均以原子写入及 `0600` 权限落盘。
