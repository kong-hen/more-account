// 全局应用状态：账号、自建分组、主题与设置、筛选条件、详情抽屉、切号历史与 Toast 提示。
import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api, extractErrorMessage, isWailsRuntime, onWailsEvent } from '@/services/api'
import { maskEmail } from '@/utils/privacy'
import {
  getMinQuotaPercentage,
  getTierLabel,
  isAccountAbnormal,
  isAccountLowQuota,
  isAccountValidationRequired,
} from '@/utils/quota'
import type {
  Account,
  AccountDetailsUpdate,
  AccountGroup,
  ActiveDetectionResult,
  AppSection,
  AppSettings,
  ClientInstallInfo,
  RuntimeTarget,
  SwitchHistoryItem,
  ThemeMode,
  UpdateCheckResult,
} from '@/types/models'

interface ToastItem {
  id: number
  type: 'success' | 'error' | 'info' | 'warning'
  message: string
  duration: number
}

export type AccountSortBy = 'custom' | 'quota_desc' | 'quota_asc' | 'last_used' | 'email'
type AccountViewMode = 'cards' | 'table'

export const useAppStore = defineStore('app', () => {
  const initialized = ref(false)
  const loading = ref(false)
  const activeSection = ref<AppSection>('accounts')

  const accounts = ref<Account[]>([])
  const currentAccountId = ref('')
  const groups = ref<AccountGroup[]>([])
  const switchHistory = ref<SwitchHistoryItem[]>([])
  const dataDir = ref('')
  const appVersion = ref('1.0.0')
  const defaultGitHubRepo = ref('kong-hen/more-account')
  const hasBuiltInOAuth = ref(false)
  const platform = ref<string>('darwin')
  const startupWarnings = ref<string[]>([])
  const updateInfo = ref<UpdateCheckResult | null>(null)
  const checkingUpdate = ref(false)
  const updateModalOpen = ref(false)
  const clientInfo = ref<ClientInstallInfo>({
    desktopPath: '',
    idePath: '',
    desktopOk: false,
    ideOk: false,
    cliDirOk: false,
  })

  // 默认严格为浅色主题，不跟随系统暗色；默认点击关闭按钮隐藏到后台运行、macOS 状态栏显示图标+额度
  const settings = ref<AppSettings>({
    theme: 'light',
    privacyMode: false,
    runtimeTarget: 'desktop',
    launchOnSwitch: true,
    lowQuotaThreshold: 20,
    autoRefreshMinutes: 5,
    inactiveAutoRefreshMinutes: 60,
    closeBehavior: 'background',
    macStatusBarMode: 'icon_quota',
    autoCheckUpdate: true,
  })

  // 账号工作台筛选与视图状态
  const selectedGroupFilter = ref<string>('all') // 'all' | 'ungrouped' | groupId
  const searchQuery = ref('')
  const tierFilter = ref<string>('all') // 'all' | 'ULTRA' | 'PRO' | 'FREE'
  const onlyLowQuota = ref(false)
  const onlyAbnormal = ref(false)
  const onlyValidationRequired = ref(false)
  const sortBy = ref<AccountSortBy>('custom')
  const viewMode = ref<AccountViewMode>('cards')

  // 多选与右侧详情抽屉（含跨账号与跨模块导航的未保存修改保护）
  const selectedAccountIds = ref<string[]>([])
  const activeDetailAccountId = ref<string | null>(null)
  const detailDrawerDirty = ref(false)
  const pendingDetailTargetId = ref<string | null | undefined>(undefined)
  const pendingNavigation = ref<{ section: AppSection; groupFilter?: string } | null>(null)
  const hasPendingUnsavedDetail = computed(
    () => pendingDetailTargetId.value !== undefined || pendingNavigation.value !== null,
  )
  const oauthCompletedSeq = ref(0)
  const oauthFailedSeq = ref(0)
  const oauthLastError = ref('')

  // 运行态状态
  const refreshingIds = ref<Record<string, boolean>>({})
  const switchingId = ref<string | null>(null)
  const refreshingAll = ref(false)
  const autoRefreshing = ref(false)
  const detectingActive = ref(false)
  const importingLocal = ref(false)
  const activeDetection = ref<ActiveDetectionResult | null>(null)
  const refreshProgress = ref({ done: 0, total: 0 })
  // 响应式当前时间戳（每 30 秒推进一次，驱动额度重置倒计时实时更新）
  const nowMs = ref(Date.now())

  // Toast 队列（支持悬停/聚焦暂停自动消失，符合可访问性规范）
  const toasts = ref<ToastItem[]>([])
  let toastSeq = 1
  const toastTimers = new Map<number, number>()
  const cleanups: Array<() => void> = []
  let reloadSeq = 0
  let lastCurrentHealthAlertKey = ''

  function checkCurrentAccountHealthAlert() {
    const cur = accounts.value.find((a) => a.id === currentAccountId.value)
    if (!cur) {
      lastCurrentHealthAlertKey = ''
      return
    }
    if (isAccountValidationRequired(cur)) {
      const key = `${cur.id}:verify`
      if (lastCurrentHealthAlertKey !== key) {
        lastCurrentHealthAlertKey = key
        notify(
          `当前账号 ${maskEmail(cur.email, settings.value.privacyMode)} 需完成网页验证，可点击「网页验证」或切换其他备用账号`,
          'warning',
          5200,
        )
      }
      return
    }
    if (isAccountLowQuota(cur, settings.value.lowQuotaThreshold, nowMs.value)) {
      const minPct = getMinQuotaPercentage(cur, nowMs.value)
      const key = `${cur.id}:low:${minPct <= 0 ? '0' : 'warn'}`
      if (lastCurrentHealthAlertKey !== key) {
        lastCurrentHealthAlertKey = key
        notify(
          `当前账号 ${maskEmail(cur.email, settings.value.privacyMode)} 剩余额度较低（${minPct}%），可切换其他备用账号`,
          'warning',
          4800,
        )
      }
      return
    }
    lastCurrentHealthAlertKey = ''
  }

  function scheduleToastDismiss(id: number, duration: number) {
    clearToastTimer(id)
    const timer = window.setTimeout(() => {
      dismissToast(id)
    }, duration)
    toastTimers.set(id, timer)
  }

  function clearToastTimer(id: number) {
    const timer = toastTimers.get(id)
    if (timer !== undefined) {
      window.clearTimeout(timer)
      toastTimers.delete(id)
    }
  }

  function notify(message: string, type: ToastItem['type'] = 'info', duration = 3200) {
    const id = toastSeq++
    toasts.value.push({ id, type, message, duration })
    scheduleToastDismiss(id, duration)
  }

  function pauseToast(id: number) {
    clearToastTimer(id)
  }

  function resumeToast(id: number) {
    const item = toasts.value.find((t) => t.id === id)
    if (!item) return
    scheduleToastDismiss(id, Math.min(item.duration, 2200))
  }

  function dismissToast(id: number) {
    clearToastTimer(id)
    toasts.value = toasts.value.filter((t) => t.id !== id)
  }

  function applyTheme(theme: ThemeMode) {
    const resolved: ThemeMode = theme === 'dark' ? 'dark' : 'light'
    document.documentElement.setAttribute('data-theme', resolved)
    const meta = document.querySelector('meta[name="color-scheme"]')
    if (meta) {
      meta.setAttribute('content', resolved)
    }
    try {
      localStorage.setItem('more-account-theme', resolved)
    } catch {
      // 忽略存储受限异常
    }
  }

  async function bootstrap() {
    if (initialized.value) return
    loading.value = true
    try {
      if (!isWailsRuntime()) {
        applyTheme(settings.value.theme)
        initialized.value = true
        return
      }
      const state = await api.getBootstrapState()
      accounts.value = state.accounts ?? []
      currentAccountId.value = state.currentAccountId ?? ''
      groups.value = state.groups ?? []
      if (state.platform) {
        platform.value = state.platform
      }
      const defaultCloseBehavior = platform.value === 'darwin' ? 'background' : 'quit'
      settings.value = {
        ...settings.value,
        ...(state.settings ?? {}),
        theme: state.settings?.theme === 'dark' ? 'dark' : 'light',
        closeBehavior:
          state.settings?.closeBehavior === 'quit' || state.settings?.closeBehavior === 'background'
            ? state.settings.closeBehavior
            : defaultCloseBehavior,
        macStatusBarMode:
          state.settings?.macStatusBarMode === 'icon_only' || state.settings?.macStatusBarMode === 'off'
            ? state.settings.macStatusBarMode
            : 'icon_quota',
      }
      clientInfo.value = state.clientInfo ?? clientInfo.value
      dataDir.value = state.dataDir ?? ''
      if (state.appVersion) {
        appVersion.value = state.appVersion
      }
      if (typeof state.defaultGitHubRepo === 'string') {
        defaultGitHubRepo.value = state.defaultGitHubRepo
      }
      hasBuiltInOAuth.value = Boolean(state.hasBuiltInOAuth)
      startupWarnings.value = Array.isArray(state.startupWarnings) ? [...state.startupWarnings] : []
      applyTheme(settings.value.theme)
      for (const warn of startupWarnings.value) {
        const isCritical = warn.includes('【高危】')
        notify(warn, isCritical ? 'error' : 'warning', isCritical ? 10_000 : 7_000)
      }

      // 注册后端事件推送（手动批量进度 + 后台自动刷新状态与完成通知）
      cleanups.push(
        onWailsEvent('quota:progress', (payload: unknown) => {
          const p = payload as { done?: number; total?: number } | null
          if (p) {
            refreshProgress.value = {
              done: p.done ?? 0,
              total: p.total ?? 0,
            }
          }
        }),
      )
      cleanups.push(
        onWailsEvent('quota:auto_status', (payload: unknown) => {
          const p = payload as { running?: boolean } | null
          autoRefreshing.value = Boolean(p?.running)
        }),
      )
      cleanups.push(
        onWailsEvent('quota:updated', () => {
          nowMs.value = Date.now()
          void reloadAccounts().catch(() => {})
        }),
      )
      cleanups.push(
        onWailsEvent('account:active_detected', (payload: unknown) => {
          const res = payload as ActiveDetectionResult | null
          if (!res) return
          applyActiveDetectionResult(res, true)
        }),
      )
      // 全局监听 OAuth 授权入库完成事件：即使授权换取令牌期间提前关闭了添加弹窗，也能自动刷新列表并提示用户
      cleanups.push(
        onWailsEvent('oauth:completed', (payload: unknown) => {
          const p = payload as { account?: Account } | null
          void reloadAccounts().then(() => {
            if (p?.account?.id) {
              if (!detailDrawerDirty.value) {
                activeDetailAccountId.value = p.account.id
              }
            }
          })
          if (p?.account?.email) {
            const emailLabel = maskEmail(p.account.email, settings.value.privacyMode)
            if (isAccountValidationRequired(p.account)) {
              notify(
                `已添加账号 ${emailLabel}，该账号需完成网页验证后方可使用`,
                'warning',
                5000,
              )
            } else {
              notify(`已通过 Google 授权添加账号 ${emailLabel}`, 'success')
            }
          } else {
            notify('OAuth 授权成功', 'success')
          }
          oauthCompletedSeq.value += 1
        }),
      )
      // 全局监听 OAuth 授权失败或超时事件：兼容后端 silent=true 静默取消/超时，不误弹红色错误提示框
      cleanups.push(
        onWailsEvent('oauth:failed', (payload: unknown) => {
          const p = payload as { error?: string; silent?: boolean } | null
          const message = p?.error || 'OAuth 授权失败'
          oauthLastError.value = p?.silent ? '' : message
          oauthFailedSeq.value += 1
          if (!p?.silent) {
            notify(`OAuth 授权失败：${message}`, 'error', 4800)
          }
        }),
      )
      // 监听状态栏发起的切号完成事件：同步刷新账号列表与切号历史
      cleanups.push(
        onWailsEvent('account:switched', (payload: unknown) => {
          const p = payload as { accountId?: string; email?: string } | null
          void reloadAccounts().catch(() => {})
          void loadSwitchHistory().catch(() => {})
          if (p?.email) {
            notify(`已通过状态栏切换至 ${maskEmail(p.email, settings.value.privacyMode)}`, 'success')
          }
        }),
      )
      // 监听状态栏等后台入口触发的全局设置同步（例如隐私脱敏模式切换）
      cleanups.push(
        onWailsEvent('settings:updated', (payload: unknown) => {
          const next = payload as Partial<AppSettings> | null
          if (!next) return
          const defaultCloseBehavior = platform.value === 'darwin' ? 'background' : 'quit'
          settings.value = {
            ...settings.value,
            ...next,
            theme: next.theme === 'dark' ? 'dark' : 'light',
            closeBehavior:
              next.closeBehavior === 'quit' || next.closeBehavior === 'background'
                ? next.closeBehavior
                : defaultCloseBehavior,
            macStatusBarMode:
              next.macStatusBarMode === 'icon_only' || next.macStatusBarMode === 'off'
                ? next.macStatusBarMode
                : 'icon_quota',
          }
          applyTheme(settings.value.theme)
        }),
      )
      // 监听状态栏后台动作失败提示
      cleanups.push(
        onWailsEvent('statusbar:error', (payload: unknown) => {
          const p = payload as { message?: string } | null
          if (p?.message) {
            notify(p.message, 'error', 4600)
          }
        }),
      )

      // 每 30 秒推进倒计时时钟，保证卡片/表格/右栏重置倒计时实时变化
      const clockTimer = window.setInterval(() => {
        nowMs.value = Date.now()
      }, 30_000)
      cleanups.push(() => window.clearInterval(clockTimer))

      // 窗口回到前台时：立即探测外部切号（2 秒防抖）并触发配额到期检查（15 秒防抖）
      let lastQuotaFocusCheck = 0
      let lastActiveFocusCheck = 0
      const handleForeground = () => {
        if (document.visibilityState === 'hidden') return
        const now = Date.now()
        nowMs.value = now
        if (now - lastActiveFocusCheck >= 2_000) {
          lastActiveFocusCheck = now
          void api.triggerActiveDetect().catch(() => {})
        }
        if (now - lastQuotaFocusCheck >= 15_000) {
          lastQuotaFocusCheck = now
          void api.triggerAutoRefreshIfDue().catch(() => {})
        }
      }
      window.addEventListener('focus', handleForeground)
      document.addEventListener('visibilitychange', handleForeground)
      cleanups.push(() => {
        window.removeEventListener('focus', handleForeground)
        document.removeEventListener('visibilitychange', handleForeground)
      })

      initialized.value = true
      // 启动就绪后立即触发一次本机当前账号探测与配额到期检查
      void detectActiveAccount(true)
      void api.triggerAutoRefreshIfDue().catch(() => {})

      // 启动后延迟 2.5 秒静默检查 GitHub 最新版本（若仓库尚未公开或网络受限则静默跳过不弹窗报错）
      if (settings.value.autoCheckUpdate && (settings.value.githubRepo || defaultGitHubRepo.value)) {
        const updateTimer = window.setTimeout(() => {
          void checkForUpdates(true)
        }, 2_500)
        cleanups.push(() => window.clearTimeout(updateTimer))
      }
    } catch (err) {
      notify(`加载应用数据失败：${extractErrorMessage(err)}`, 'error', 5000)
    } finally {
      loading.value = false
    }
  }

  function disposeEvents() {
    while (cleanups.length > 0) {
      const fn = cleanups.pop()
      fn?.()
    }
    for (const timer of toastTimers.values()) {
      window.clearTimeout(timer)
    }
    toastTimers.clear()
  }

  async function reloadAccounts() {
    if (!isWailsRuntime()) return
    const seq = ++reloadSeq
    const [list, currId, groupList] = await Promise.all([
      api.listAccounts(),
      api.getCurrentAccountID(),
      api.listGroups(),
    ])
    // 防止较早发起的慢请求覆盖较新的刷新结果
    if (seq !== reloadSeq) return
    accounts.value = list ?? []
    currentAccountId.value = currId ?? ''
    groups.value = groupList ?? []
    // 清理已不存在的选中项
    const validIds = new Set(accounts.value.map((a) => a.id))
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => validIds.has(id))
    if (activeDetailAccountId.value && !validIds.has(activeDetailAccountId.value)) {
      activeDetailAccountId.value = null
    }
    checkCurrentAccountHealthAlert()
  }

  async function updateSettings(patch: Partial<AppSettings>): Promise<AppSettings> {
    const previous = { ...settings.value }
    const next: AppSettings = {
      ...settings.value,
      ...patch,
      theme: (patch.theme ?? settings.value.theme) === 'dark' ? 'dark' : 'light',
    }
    settings.value = next
    applyTheme(next.theme)
    if (!isWailsRuntime()) return next
    try {
      const saved = await api.saveSettings(next)
      settings.value = saved
      applyTheme(saved.theme)
      clientInfo.value = await api.detectInstalledClients()
      return saved
    } catch (err) {
      // 落盘失败时回滚本地内存与主题，并向上抛出供设置弹窗阻止关闭
      settings.value = previous
      applyTheme(previous.theme)
      notify(`保存设置失败：${extractErrorMessage(err)}`, 'error')
      throw err
    }
  }

  async function checkForUpdates(silent = false, repoOverride = ''): Promise<UpdateCheckResult | null> {
    if (!isWailsRuntime() || checkingUpdate.value) return null
    checkingUpdate.value = true
    try {
      const res = await api.checkForUpdates(repoOverride)
      updateInfo.value = res
      if (res.hasUpdate) {
        if (!silent) {
          updateModalOpen.value = true
        } else {
          notify(`发现新版本 v${res.latestVersion}，可在左侧边栏点击查看并升级`, 'info', 5200)
        }
      } else if (!silent) {
        if (res.noReleaseYet) {
          notify(`仓库 ${res.repo} 暂未发布任何 Release 版本`, 'info', 4200)
        } else {
          notify(`当前已是最新版本 (v${res.currentVersion})`, 'success')
        }
      }
      return res
    } catch (err) {
      if (!silent) {
        notify(`检查更新失败：${extractErrorMessage(err)}`, 'warning', 5000)
      }
      return null
    } finally {
      checkingUpdate.value = false
    }
  }

  async function toggleTheme() {
    const nextTheme: ThemeMode = settings.value.theme === 'dark' ? 'light' : 'dark'
    await updateSettings({ theme: nextTheme }).catch(() => {})
  }

  async function togglePrivacyMode() {
    await updateSettings({ privacyMode: !settings.value.privacyMode }).catch(() => {})
  }

  async function refreshAccount(accountId: string) {
    if (refreshingIds.value[accountId]) return
    refreshingIds.value = { ...refreshingIds.value, [accountId]: true }
    try {
      const updated = await api.refreshAccountQuota(accountId)
      replaceAccountInList(updated)
      const displayEmail = maskEmail(updated.email, settings.value.privacyMode)
      if (isAccountValidationRequired(updated)) {
        notify(`${displayEmail} 需要网页验证，可点击「网页验证」或「复制链接」处理`, 'warning', 4800)
      } else if (updated.quotaError) {
        notify(`${displayEmail} 配额状态异常：${updated.quotaError.message}`, 'warning', 4500)
      } else {
        notify(`已更新 ${displayEmail} 的最新配额`, 'success')
      }
    } catch (err) {
      await reloadAccounts().catch(() => {})
      notify(`刷新配额失败：${extractErrorMessage(err)}`, 'error', 4500)
    } finally {
      const next = { ...refreshingIds.value }
      delete next[accountId]
      refreshingIds.value = next
    }
  }

  async function refreshAllAccounts() {
    if (refreshingAll.value) return
    refreshingAll.value = true
    refreshProgress.value = { done: 0, total: accounts.value.length }
    try {
      const stats = await api.refreshAllQuotas()
      await reloadAccounts()
      if (stats.failed > 0) {
        const firstErr = stats.errors?.[0] ? `（${stats.errors[0]}）` : ''
        notify(`刷新完成：成功 ${stats.success} 个，失败 ${stats.failed} 个${firstErr}`, 'warning', 5200)
      } else {
        notify(`全部 ${stats.success} 个账号配额已更新`, 'success')
      }
    } catch (err) {
      // 超时或部分失败后仍重新拉取账号列表，将已完成刷新的部分账号结果同步至界面
      await reloadAccounts().catch(() => {})
      notify(`批量刷新失败：${extractErrorMessage(err)}`, 'error')
    } finally {
      refreshingAll.value = false
    }
  }

  async function switchToAccount(accountId: string, launchOverride?: boolean, targetOverride?: RuntimeTarget) {
    if (switchingId.value) return
    switchingId.value = accountId
    const targetClient = targetOverride ?? settings.value.runtimeTarget
    const launch = launchOverride ?? settings.value.launchOnSwitch
    try {
      const outcome = await api.switchAccount({
        accountId,
        targetClient,
        launch,
      })
      replaceAccountInList(outcome.account)
      currentAccountId.value = outcome.account.id
      activeDetection.value = {
        hasCredential: true,
        managed: true,
        matchedAccountId: outcome.account.id,
        detectedEmail: outcome.account.email,
        targetClient,
        changed: false,
        detectedAt: Math.floor(Date.now() / 1000),
      }
      const displayEmail = maskEmail(outcome.account.email, settings.value.privacyMode)
      if (outcome.launchWarning) {
        notify(`账号已切换为 ${displayEmail}，但重启客户端失败：${outcome.launchWarning}`, 'warning', 5500)
      } else if (outcome.launched) {
        notify(`已切换至 ${displayEmail} 并重启客户端`, 'success')
      } else {
        notify(`已将本地凭据切换至 ${displayEmail}`, 'success')
      }
      if (activeSection.value === 'history') {
        await loadSwitchHistory()
      }
    } catch (err) {
      // 切号失败（例如预刷新遇到 invalid_grant 被后端标记禁用并记录失败历史）时同步刷新列表与历史，即时反映最新状态
      await reloadAccounts().catch(() => {})
      if (activeSection.value === 'history') {
        await loadSwitchHistory().catch(() => {})
      }
      notify(`切号失败：${extractErrorMessage(err)}`, 'error', 5000)
    } finally {
      switchingId.value = null
    }
  }

  async function saveAccountDetails(accountId: string, update: AccountDetailsUpdate) {
    const updated = await api.updateAccountDetails(accountId, update)
    replaceAccountInList(updated)
    notify('账号信息已保存', 'success')
    return updated
  }

  async function assignAccountsToGroup(accountIds: string[], groupId: string) {
    if (accountIds.length === 0) return
    await api.updateAccountGroup(accountIds, groupId)
    await reloadAccounts()
    selectedAccountIds.value = []
    notify(`已更新 ${accountIds.length} 个账号的分组`, 'success')
  }

  async function deleteAccountIds(accountIds: string[]) {
    if (accountIds.length === 0) return
    await api.deleteAccounts(accountIds)
    await reloadAccounts()
    selectedAccountIds.value = []
    notify(`已删除 ${accountIds.length} 个账号`, 'success')
  }

  // 自定义拖拽排序并持久化到后端 ReorderAccounts
  async function reorderAccounts(orderedIds: string[]) {
    if (orderedIds.length === 0) return
    const previous = accounts.value.map((a) => ({ ...a }))
    const rank = new Map<string, number>()
    orderedIds.forEach((id, idx) => rank.set(id, idx))

    const next = [...accounts.value].sort((a, b) => {
      const ra = rank.has(a.id) ? rank.get(a.id)! : Number.MAX_SAFE_INTEGER
      const rb = rank.has(b.id) ? rank.get(b.id)! : Number.MAX_SAFE_INTEGER
      if (ra !== rb) return ra - rb
      return a.sortOrder - b.sortOrder
    })
    next.forEach((acc, idx) => {
      acc.sortOrder = idx
    })
    accounts.value = next

    if (!isWailsRuntime()) return
    try {
      await api.reorderAccounts(next.map((a) => a.id))
    } catch (err) {
      accounts.value = previous
      notify(`保存排序失败：${extractErrorMessage(err)}`, 'error')
    }
  }

  async function saveGroup(group: Partial<AccountGroup> & { name: string; color: string }) {
    const saved = await api.saveGroup({
      id: group.id ?? '',
      name: group.name,
      color: group.color,
      sortOrder: group.sortOrder ?? groups.value.length,
      createdAt: group.createdAt ?? 0,
    })
    await reloadAccounts()
    notify(group.id ? '分组已更新' : `已新建分组「${saved.name}」`, 'success')
    return saved
  }

  async function deleteGroup(groupId: string) {
    await api.deleteGroup(groupId)
    if (selectedGroupFilter.value === groupId) {
      selectedGroupFilter.value = 'all'
    }
    await reloadAccounts()
    notify('分组已删除，原成员已移至未分组', 'success')
  }

  async function loadSwitchHistory() {
    if (!isWailsRuntime()) return
    switchHistory.value = (await api.listSwitchHistory()) ?? []
  }

  async function clearSwitchHistory() {
    await api.clearSwitchHistory()
    switchHistory.value = []
    notify('切号历史已清空', 'success')
  }

  function replaceAccountInList(updated: Account) {
    const idx = accounts.value.findIndex((a) => a.id === updated.id)
    if (idx >= 0) {
      accounts.value.splice(idx, 1, updated)
    } else {
      accounts.value.push(updated)
    }
  }

  function toggleSelectAccount(accountId: string) {
    if (selectedAccountIds.value.includes(accountId)) {
      selectedAccountIds.value = selectedAccountIds.value.filter((id) => id !== accountId)
    } else {
      selectedAccountIds.value = [...selectedAccountIds.value, accountId]
    }
  }

  function applyActiveDetectionResult(res: ActiveDetectionResult, fromBackgroundEvent: boolean) {
    activeDetection.value = res
    if (res.error) return

    const nextId = res.managed ? (res.matchedAccountId ?? '') : ''
    const idChanged = nextId !== currentAccountId.value
    if (idChanged) {
      currentAccountId.value = nextId
      void reloadAccounts().catch(() => {})
    }

    if (fromBackgroundEvent && res.changed) {
      if (res.managed && res.detectedEmail) {
        notify(
          `检测到本机已切换账号：${maskEmail(res.detectedEmail, settings.value.privacyMode)}`,
          'info',
          4000,
        )
      } else if (res.hasCredential && !res.managed) {
        const label = res.detectedEmail
          ? `：${maskEmail(res.detectedEmail, settings.value.privacyMode)}`
          : ''
        notify(`检测到本机切换至未纳管外部账号${label}`, 'warning', 4500)
      } else if (!res.hasCredential) {
        notify('检测到本机 Antigravity 账号已退出登录', 'info', 3800)
      }
    }
  }

  async function detectActiveAccount(silent = false) {
    if (!isWailsRuntime() || detectingActive.value) return
    detectingActive.value = true
    try {
      const res = await api.detectActiveAccount()
      applyActiveDetectionResult(res, false)
      if (!silent) {
        if (res.error) {
          notify(`探测本机账号失败：${res.error}`, 'error', 4200)
        } else if (res.managed && res.detectedEmail) {
          const emailText = maskEmail(res.detectedEmail, settings.value.privacyMode)
          notify(
            res.changed ? `已同步本机当前账号：${emailText}` : `本机当前正在使用：${emailText}`,
            'success',
          )
        } else if (res.hasCredential && !res.managed) {
          const emailText = res.detectedEmail
            ? maskEmail(res.detectedEmail, settings.value.privacyMode)
            : '未知账号'
          notify(`检测到外部未纳管账号（${emailText}），可点击导入`, 'warning', 4500)
        } else {
          notify('本机暂未检测到已登录的 Antigravity 账号', 'info')
        }
      }
    } catch (err) {
      if (!silent) {
        notify(`探测本机账号失败：${extractErrorMessage(err)}`, 'error', 4200)
      }
    } finally {
      detectingActive.value = false
    }
  }

  async function importLocalActiveAccount(groupId = '') {
    if (!isWailsRuntime() || importingLocal.value) return
    importingLocal.value = true
    try {
      const acc = await api.importFromLocal(groupId)
      await reloadAccounts()
      activeDetection.value = {
        hasCredential: true,
        managed: true,
        matchedAccountId: acc.id,
        detectedEmail: acc.email,
        targetClient: settings.value.runtimeTarget,
        changed: false,
        detectedAt: Math.floor(Date.now() / 1000),
      }
      notify(`已导入并同步当前账号：${maskEmail(acc.email, settings.value.privacyMode)}`, 'success')
    } catch (err) {
      notify(`导入本机账号失败：${extractErrorMessage(err)}`, 'error', 4500)
    } finally {
      importingLocal.value = false
    }
  }

  function clearAdvancedFilters() {
    tierFilter.value = 'all'
    onlyLowQuota.value = false
    onlyAbnormal.value = false
    onlyValidationRequired.value = false
  }

  function requestSelectDetailAccount(nextId: string | null) {
    if (nextId === activeDetailAccountId.value) return
    if (detailDrawerDirty.value && activeDetailAccountId.value) {
      pendingNavigation.value = null
      pendingDetailTargetId.value = nextId
      return
    }
    activeDetailAccountId.value = nextId
  }

  // 切换主功能模块时检查详情抽屉是否存在未保存编辑，避免抽屉随 v-if 卸载导致修改静默丢失
  function requestSwitchSection(section: AppSection) {
    if (section === activeSection.value) return
    if (activeSection.value === 'accounts' && detailDrawerDirty.value && activeDetailAccountId.value) {
      pendingDetailTargetId.value = undefined
      pendingNavigation.value = { section }
      return
    }
    if (section !== 'accounts') {
      activeDetailAccountId.value = null
    }
    activeSection.value = section
  }

  function requestSelectGroup(groupId: string) {
    if (activeSection.value === 'accounts' && selectedGroupFilter.value === groupId) return
    if (activeSection.value === 'accounts' && detailDrawerDirty.value && activeDetailAccountId.value) {
      pendingDetailTargetId.value = undefined
      pendingNavigation.value = { section: 'accounts', groupFilter: groupId }
      return
    }
    activeSection.value = 'accounts'
    selectedGroupFilter.value = groupId
  }

  function confirmDiscardDetailChanges() {
    const nextDetail = pendingDetailTargetId.value
    const nextNav = pendingNavigation.value
    pendingDetailTargetId.value = undefined
    pendingNavigation.value = null
    detailDrawerDirty.value = false
    if (nextDetail !== undefined) {
      activeDetailAccountId.value = nextDetail
    }
    if (nextNav) {
      if (nextNav.section !== 'accounts') {
        activeDetailAccountId.value = null
      }
      activeSection.value = nextNav.section
      if (nextNav.groupFilter !== undefined) {
        selectedGroupFilter.value = nextNav.groupFilter
      }
    }
  }

  function cancelDiscardDetailChanges() {
    pendingDetailTargetId.value = undefined
    pendingNavigation.value = null
  }

  // 计算属性
  const currentAccount = computed(() =>
    accounts.value.find((a) => a.id === currentAccountId.value) ?? null,
  )

  const activeDetailAccount = computed(() =>
    accounts.value.find((a) => a.id === activeDetailAccountId.value) ?? null,
  )

  const groupMap = computed(() => {
    const map = new Map<string, AccountGroup>()
    for (const g of groups.value) {
      map.set(g.id, g)
    }
    return map
  })

  const groupCounts = computed(() => {
    const counts: Record<string, number> = {
      all: accounts.value.length,
      ungrouped: 0,
    }
    for (const acc of accounts.value) {
      if (!acc.groupId || !groupMap.value.has(acc.groupId)) {
        counts.ungrouped += 1
      } else {
        counts[acc.groupId] = (counts[acc.groupId] ?? 0) + 1
      }
    }
    return counts
  })

  const lowQuotaCount = computed(() =>
    accounts.value.filter((a) => isAccountLowQuota(a, settings.value.lowQuotaThreshold, nowMs.value)).length,
  )

  const abnormalCount = computed(() =>
    accounts.value.filter((a) => isAccountAbnormal(a)).length,
  )

  const validationRequiredCount = computed(() =>
    accounts.value.filter((a) => isAccountValidationRequired(a)).length,
  )

  const activeFilterCount = computed(() => {
    let n = 0
    if (tierFilter.value !== 'all') n += 1
    if (onlyLowQuota.value) n += 1
    if (onlyAbnormal.value) n += 1
    if (onlyValidationRequired.value) n += 1
    return n
  })

  const filteredAccounts = computed(() => {
    const q = searchQuery.value.trim().toLowerCase()
    const threshold = settings.value.lowQuotaThreshold

    const list = accounts.value.filter((acc) => {
      if (selectedGroupFilter.value === 'ungrouped') {
        if (acc.groupId && groupMap.value.has(acc.groupId)) return false
      } else if (selectedGroupFilter.value !== 'all') {
        if (acc.groupId !== selectedGroupFilter.value) return false
      }

      if (tierFilter.value !== 'all' && getTierLabel(acc) !== tierFilter.value) {
        return false
      }

      // 可选的健康状态过滤（低配额 / 需网页验证 / 异常）：多项同时勾选时取并集，单选时精准匹配。
      if (onlyLowQuota.value || onlyAbnormal.value || onlyValidationRequired.value) {
        const hitLow = onlyLowQuota.value && isAccountLowQuota(acc, threshold, nowMs.value)
        const hitVerify = onlyValidationRequired.value && isAccountValidationRequired(acc)
        const hitAbnormal = onlyAbnormal.value && isAccountAbnormal(acc)
        if (!hitLow && !hitVerify && !hitAbnormal) return false
      }

      if (q) {
        const groupName = acc.groupId ? (groupMap.value.get(acc.groupId)?.name ?? '') : ''
        const haystack = `${acc.email} ${acc.loginAccount ?? ''} ${acc.name ?? ''} ${acc.notes ?? ''} ${groupName}`.toLowerCase()
        if (!haystack.includes(q)) return false
      }

      return true
    })

    const sorted = [...list]
    switch (sortBy.value) {
      case 'quota_desc':
        sorted.sort((a, b) => {
          const qa = getMinQuotaPercentage(a, nowMs.value)
          const qb = getMinQuotaPercentage(b, nowMs.value)
          if (qa < 0 && qb < 0) return a.sortOrder - b.sortOrder
          if (qa < 0) return 1
          if (qb < 0) return -1
          return qb - qa
        })
        break
      case 'quota_asc':
        sorted.sort((a, b) => {
          const qa = getMinQuotaPercentage(a, nowMs.value)
          const qb = getMinQuotaPercentage(b, nowMs.value)
          if (qa < 0 && qb < 0) return a.sortOrder - b.sortOrder
          if (qa < 0) return 1
          if (qb < 0) return -1
          return qa - qb
        })
        break
      case 'last_used':
        sorted.sort((a, b) => (b.lastUsed || 0) - (a.lastUsed || 0))
        break
      case 'email':
        sorted.sort((a, b) => a.email.localeCompare(b.email))
        break
      default:
        sorted.sort((a, b) => a.sortOrder - b.sortOrder)
    }
    return sorted
  })

  function dismissStartupWarnings() {
    startupWarnings.value = []
  }

  async function quitApp() {
    if (!isWailsRuntime()) return
    try {
      await api.quitApp()
    } catch (err) {
      notify(`退出应用失败：${extractErrorMessage(err)}`, 'error')
    }
  }

  return {
    loading,
    activeSection,
    accounts,
    currentAccountId,
    currentAccount,
    groups,
    groupMap,
    groupCounts,
    switchHistory,
    dataDir,
    appVersion,
    defaultGitHubRepo,
    hasBuiltInOAuth,
    platform,
    startupWarnings,
    dismissStartupWarnings,
    updateInfo,
    checkingUpdate,
    updateModalOpen,
    clientInfo,
    settings,
    selectedGroupFilter,
    searchQuery,
    tierFilter,
    onlyLowQuota,
    onlyAbnormal,
    onlyValidationRequired,
    lowQuotaCount,
    abnormalCount,
    validationRequiredCount,
    activeFilterCount,
    sortBy,
    viewMode,
    selectedAccountIds,
    activeDetailAccountId,
    activeDetailAccount,
    detailDrawerDirty,
    hasPendingUnsavedDetail,
    oauthCompletedSeq,
    oauthFailedSeq,
    oauthLastError,
    filteredAccounts,
    refreshingIds,
    switchingId,
    refreshingAll,
    autoRefreshing,
    detectingActive,
    importingLocal,
    activeDetection,
    refreshProgress,
    nowMs,
    toasts,
    notify,
    pauseToast,
    resumeToast,
    dismissToast,
    bootstrap,
    disposeEvents,
    reloadAccounts,
    updateSettings,
    checkForUpdates,
    toggleTheme,
    togglePrivacyMode,
    refreshAccount,
    refreshAllAccounts,
    detectActiveAccount,
    importLocalActiveAccount,
    switchToAccount,
    saveAccountDetails,
    assignAccountsToGroup,
    deleteAccountIds,
    reorderAccounts,
    saveGroup,
    deleteGroup,
    loadSwitchHistory,
    clearSwitchHistory,
    toggleSelectAccount,
    clearAdvancedFilters,
    requestSelectDetailAccount,
    requestSwitchSection,
    requestSelectGroup,
    confirmDiscardDetailChanges,
    cancelDiscardDetailChanges,
    quitApp,
  }
})
