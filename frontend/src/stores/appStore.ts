// 全局应用状态：账号、自建分组、主题与设置、筛选条件、详情抽屉、切号历史与 Toast 提示。
import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api, extractErrorMessage, isWailsRuntime, onWailsEvent } from '@/services/api'
import {
  getMinQuotaPercentage,
  getTierLabel,
  isAccountAbnormal,
  isAccountLowQuota,
} from '@/utils/quota'
import type {
  Account,
  AccountDetailsUpdate,
  AccountGroup,
  AppSection,
  AppSettings,
  ClientInstallInfo,
  RuntimeTarget,
  SwitchHistoryItem,
  ThemeMode,
} from '@/types/models'

export interface ToastItem {
  id: number
  type: 'success' | 'error' | 'info' | 'warning'
  message: string
}

export type AccountSortBy = 'custom' | 'quota_desc' | 'quota_asc' | 'last_used' | 'email'
export type AccountViewMode = 'cards' | 'table'

export const useAppStore = defineStore('app', () => {
  const initialized = ref(false)
  const loading = ref(false)
  const activeSection = ref<AppSection>('accounts')

  const accounts = ref<Account[]>([])
  const currentAccountId = ref('')
  const groups = ref<AccountGroup[]>([])
  const switchHistory = ref<SwitchHistoryItem[]>([])
  const dataDir = ref('')
  const clientInfo = ref<ClientInstallInfo>({
    desktopPath: '',
    idePath: '',
    desktopOk: false,
    ideOk: false,
    cliDirOk: false,
  })

  // 默认严格为浅色主题，不跟随系统暗色；后台自动刷新默认每 5 分钟
  const settings = ref<AppSettings>({
    theme: 'light',
    privacyMode: false,
    runtimeTarget: 'desktop',
    launchOnSwitch: true,
    lowQuotaThreshold: 20,
    autoRefreshMinutes: 5,
  })

  // 账号工作台筛选与视图状态
  const selectedGroupFilter = ref<string>('all') // 'all' | 'ungrouped' | groupId
  const searchQuery = ref('')
  const tierFilter = ref<string>('all') // 'all' | 'ULTRA' | 'PRO' | 'FREE'
  const onlyLowQuota = ref(false)
  const onlyAbnormal = ref(false)
  const sortBy = ref<AccountSortBy>('custom')
  const viewMode = ref<AccountViewMode>('cards')

  // 多选与右侧详情抽屉
  const selectedAccountIds = ref<string[]>([])
  const activeDetailAccountId = ref<string | null>(null)

  // 运行态状态
  const refreshingIds = ref<Record<string, boolean>>({})
  const switchingId = ref<string | null>(null)
  const refreshingAll = ref(false)
  const autoRefreshing = ref(false)
  const refreshProgress = ref({ done: 0, total: 0 })
  // 响应式当前时间戳（每 30 秒推进一次，驱动额度重置倒计时实时更新）
  const nowMs = ref(Date.now())

  // Toast 队列
  const toasts = ref<ToastItem[]>([])
  let toastSeq = 1
  const cleanups: Array<() => void> = []

  function notify(message: string, type: ToastItem['type'] = 'info', duration = 3200) {
    const id = toastSeq++
    toasts.value.push({ id, type, message })
    window.setTimeout(() => {
      dismissToast(id)
    }, duration)
  }

  function dismissToast(id: number) {
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
      settings.value = {
        ...settings.value,
        ...(state.settings ?? {}),
        theme: state.settings?.theme === 'dark' ? 'dark' : 'light',
      }
      clientInfo.value = state.clientInfo ?? clientInfo.value
      dataDir.value = state.dataDir ?? ''
      applyTheme(settings.value.theme)

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
          void reloadAccounts()
        }),
      )

      // 每 30 秒推进倒计时时钟，保证卡片/表格/右栏重置倒计时实时变化
      const clockTimer = window.setInterval(() => {
        nowMs.value = Date.now()
      }, 30_000)
      cleanups.push(() => window.clearInterval(clockTimer))

      // 窗口回到前台时自动保鲜：更新时间并触发后端检查到期账号（防抖 15 秒）
      let lastFocusCheck = 0
      const handleForeground = () => {
        if (document.visibilityState === 'hidden') return
        nowMs.value = Date.now()
        const now = Date.now()
        if (now - lastFocusCheck < 15_000) return
        lastFocusCheck = now
        void api.triggerAutoRefreshIfDue().catch(() => {})
      }
      window.addEventListener('focus', handleForeground)
      document.addEventListener('visibilitychange', handleForeground)
      cleanups.push(() => {
        window.removeEventListener('focus', handleForeground)
        document.removeEventListener('visibilitychange', handleForeground)
      })

      initialized.value = true
      // 启动就绪后立即触发一次到期检查（补拉陈旧数据或缺失周额度桶的账号）
      void api.triggerAutoRefreshIfDue().catch(() => {})
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
  }

  async function reloadAccounts() {
    if (!isWailsRuntime()) return
    const [list, currId, groupList] = await Promise.all([
      api.listAccounts(),
      api.getCurrentAccountID(),
      api.listGroups(),
    ])
    accounts.value = list ?? []
    currentAccountId.value = currId ?? ''
    groups.value = groupList ?? []
    // 清理已不存在的选中项
    const validIds = new Set(accounts.value.map((a) => a.id))
    selectedAccountIds.value = selectedAccountIds.value.filter((id) => validIds.has(id))
    if (activeDetailAccountId.value && !validIds.has(activeDetailAccountId.value)) {
      activeDetailAccountId.value = null
    }
  }

  async function updateSettings(patch: Partial<AppSettings>) {
    const next: AppSettings = {
      ...settings.value,
      ...patch,
      theme: (patch.theme ?? settings.value.theme) === 'dark' ? 'dark' : 'light',
    }
    settings.value = next
    applyTheme(next.theme)
    if (!isWailsRuntime()) return
    try {
      settings.value = await api.saveSettings(next)
      clientInfo.value = await api.detectInstalledClients()
    } catch (err) {
      notify(`保存设置失败：${extractErrorMessage(err)}`, 'error')
    }
  }

  async function toggleTheme() {
    const nextTheme: ThemeMode = settings.value.theme === 'dark' ? 'light' : 'dark'
    await updateSettings({ theme: nextTheme })
  }

  async function togglePrivacyMode() {
    await updateSettings({ privacyMode: !settings.value.privacyMode })
  }

  async function refreshAccount(accountId: string) {
    if (refreshingIds.value[accountId]) return
    refreshingIds.value = { ...refreshingIds.value, [accountId]: true }
    try {
      const updated = await api.refreshAccountQuota(accountId)
      replaceAccountInList(updated)
      if (updated.quotaError) {
        notify(`${updated.email} 配额状态异常：${updated.quotaError.message}`, 'warning', 4500)
      } else {
        notify(`已更新 ${updated.email} 的最新配额`, 'success')
      }
    } catch (err) {
      await reloadAccounts()
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
        notify(`刷新完成：成功 ${stats.success} 个，失败 ${stats.failed} 个`, 'warning', 4500)
      } else {
        notify(`全部 ${stats.success} 个账号配额已更新`, 'success')
      }
    } catch (err) {
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
      if (outcome.launchWarning) {
        notify(`账号已切换为 ${outcome.account.email}，但重启客户端失败：${outcome.launchWarning}`, 'warning', 5500)
      } else if (outcome.launched) {
        notify(`已切换至 ${outcome.account.email} 并重启客户端`, 'success')
      } else {
        notify(`已将本地凭据切换至 ${outcome.account.email}`, 'success')
      }
      if (activeSection.value === 'history') {
        await loadSwitchHistory()
      }
    } catch (err) {
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

  function clearAdvancedFilters() {
    tierFilter.value = 'all'
    onlyLowQuota.value = false
    onlyAbnormal.value = false
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
    accounts.value.filter((a) => isAccountLowQuota(a, settings.value.lowQuotaThreshold)).length,
  )

  const abnormalCount = computed(() =>
    accounts.value.filter((a) => isAccountAbnormal(a)).length,
  )

  const activeFilterCount = computed(() => {
    let n = 0
    if (tierFilter.value !== 'all') n += 1
    if (onlyLowQuota.value) n += 1
    if (onlyAbnormal.value) n += 1
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

      // 可选的「低配额 / 异常」过滤：同时勾选时取并集（查看所有需要关注的账号），单选时精准匹配。
      if (onlyLowQuota.value && onlyAbnormal.value) {
        if (!isAccountLowQuota(acc, threshold) && !isAccountAbnormal(acc)) return false
      } else if (onlyLowQuota.value) {
        if (!isAccountLowQuota(acc, threshold)) return false
      } else if (onlyAbnormal.value) {
        if (!isAccountAbnormal(acc)) return false
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
        sorted.sort((a, b) => getMinQuotaPercentage(b) - getMinQuotaPercentage(a))
        break
      case 'quota_asc':
        sorted.sort((a, b) => getMinQuotaPercentage(a) - getMinQuotaPercentage(b))
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

  return {
    initialized,
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
    clientInfo,
    settings,
    selectedGroupFilter,
    searchQuery,
    tierFilter,
    onlyLowQuota,
    onlyAbnormal,
    lowQuotaCount,
    abnormalCount,
    activeFilterCount,
    sortBy,
    viewMode,
    selectedAccountIds,
    activeDetailAccountId,
    activeDetailAccount,
    filteredAccounts,
    refreshingIds,
    switchingId,
    refreshingAll,
    autoRefreshing,
    refreshProgress,
    nowMs,
    toasts,
    notify,
    dismissToast,
    bootstrap,
    disposeEvents,
    reloadAccounts,
    updateSettings,
    toggleTheme,
    togglePrivacyMode,
    refreshAccount,
    refreshAllAccounts,
    switchToAccount,
    saveAccountDetails,
    assignAccountsToGroup,
    deleteAccountIds,
    saveGroup,
    deleteGroup,
    loadSwitchHistory,
    clearSwitchHistory,
    replaceAccountInList,
    toggleSelectAccount,
    clearAdvancedFilters,
  }
})
