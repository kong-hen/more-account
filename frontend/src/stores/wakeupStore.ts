// 定时唤醒任务配置、即时测试与执行历史状态管理。
import { ref } from 'vue'
import { defineStore } from 'pinia'
import { api, extractErrorMessage, isWailsRuntime } from '@/services/api'
import { useAppStore } from '@/stores/appStore'
import type {
  WakeupHistoryItem,
  WakeupState,
  WakeupTask,
  WakeupTestRequest,
} from '@/types/models'

export const useWakeupStore = defineStore('wakeup', () => {
  const state = ref<WakeupState>({
    enabled: false,
    tasks: [],
  })
  const history = ref<WakeupHistoryItem[]>([])
  const loading = ref(false)
  const savingState = ref(false)
  const runningTaskId = ref<string | null>(null)
  const testingScopeId = ref<string | null>(null)
  const testingAccountId = ref<string | null>(null)

  // 请求序号守卫：防止较早发起的慢拉取响应覆盖较新的配置与历史快照
  let loadSeq = 0

  async function loadAll() {
    if (!isWailsRuntime()) return
    const seq = ++loadSeq
    loading.value = true
    try {
      const [s, h] = await Promise.all([api.getWakeupState(), api.listWakeupHistory()])
      if (seq !== loadSeq) return
      state.value = s ?? { enabled: false, tasks: [] }
      history.value = h ?? []
    } finally {
      if (seq === loadSeq) {
        loading.value = false
      }
    }
  }

  async function saveState(next: WakeupState) {
    const appStore = useAppStore()
    if (savingState.value) {
      throw new Error('正在保存配置，请稍候')
    }
    savingState.value = true
    try {
      await api.saveWakeupState(next)
      const seq = ++loadSeq
      const latest = await api.getWakeupState()
      if (seq === loadSeq) {
        state.value = latest ?? { enabled: false, tasks: [] }
      }
      appStore.notify('定时唤醒配置已保存', 'success')
    } catch (err) {
      appStore.notify(`保存唤醒配置失败：${extractErrorMessage(err)}`, 'error')
      throw err
    } finally {
      savingState.value = false
    }
  }

  async function toggleGlobalEnabled() {
    if (savingState.value) return
    await saveState({
      ...state.value,
      enabled: !state.value.enabled,
    }).catch(() => {})
  }

  async function upsertTask(task: WakeupTask) {
    const tasks = [...state.value.tasks]
    const idx = tasks.findIndex((t) => t.id && t.id === task.id)
    if (idx >= 0) {
      tasks.splice(idx, 1, task)
    } else {
      tasks.push(task)
    }
    await saveState({ ...state.value, tasks })
  }

  async function deleteTask(taskId: string) {
    const tasks = state.value.tasks.filter((t) => t.id !== taskId)
    await saveState({ ...state.value, tasks })
  }

  async function toggleTaskEnabled(taskId: string) {
    if (savingState.value) return
    const tasks = state.value.tasks.map((t) =>
      t.id === taskId ? { ...t, enabled: !t.enabled } : t,
    )
    await saveState({ ...state.value, tasks }).catch(() => {})
  }

  async function runTaskNow(taskId: string) {
    const appStore = useAppStore()
    if (runningTaskId.value) return
    runningTaskId.value = taskId
    try {
      const items = (await api.runWakeupTaskNow(taskId)) ?? []
      await loadAll()
      if (items.length === 0) {
        appStore.notify('当前任务无可用账号可唤醒（账号可能已被删除或不可用）', 'warning')
        return
      }
      const okCount = items.filter((i) => i.success).length
      const failCount = items.length - okCount
      if (failCount > 0) {
        appStore.notify(`唤醒执行完毕：成功 ${okCount}，失败 ${failCount}`, 'warning')
      } else {
        appStore.notify(`唤醒任务执行成功（共 ${okCount} 次请求）`, 'success')
      }
    } catch (err) {
      await loadAll().catch(() => {})
      appStore.notify(`唤醒任务执行失败：${extractErrorMessage(err)}`, 'error')
    } finally {
      runningTaskId.value = null
    }
  }

  async function runSingleTest(req: WakeupTestRequest) {
    const appStore = useAppStore()
    if (testingScopeId.value) {
      await api.cancelWakeupTest(testingScopeId.value).catch(() => {})
    }
    const scopeId = req.scopeId || `scope_${Date.now()}`
    testingScopeId.value = scopeId
    testingAccountId.value = req.accountId
    try {
      const result = await api.runWakeupTest({ ...req, scopeId })
      await loadAll()
      if (result) {
        appStore.notify(`唤醒测试成功（${result.durationMs}ms，回复：${result.reply || 'OK'}）`, 'success')
      }
      return result
    } catch (err) {
      // 失败记录已由后端写入执行历史，主动刷新以确保界面实时同步；用户主动取消时不再弹红错
      await loadAll().catch(() => {})
      if (testingScopeId.value === scopeId) {
        appStore.notify(`唤醒测试失败：${extractErrorMessage(err)}`, 'error', 4500)
      }
      return null
    } finally {
      if (testingScopeId.value === scopeId) {
        testingScopeId.value = null
        testingAccountId.value = null
      }
    }
  }

  async function cancelCurrentTest() {
    const scopeId = testingScopeId.value
    if (!scopeId) return
    const appStore = useAppStore()
    testingScopeId.value = null
    testingAccountId.value = null
    try {
      await api.cancelWakeupTest(scopeId)
      appStore.notify('已取消当前唤醒测试', 'info')
    } catch (err) {
      appStore.notify(`取消唤醒测试失败：${extractErrorMessage(err)}`, 'error')
    }
  }

  async function clearHistory() {
    const appStore = useAppStore()
    try {
      await api.clearWakeupHistory()
      history.value = []
      appStore.notify('唤醒历史已清空', 'success')
    } catch (err) {
      appStore.notify(`清空唤醒历史失败：${extractErrorMessage(err)}`, 'error')
      throw err
    }
  }

  return {
    state,
    history,
    loading,
    savingState,
    runningTaskId,
    testingScopeId,
    testingAccountId,
    loadAll,
    toggleGlobalEnabled,
    upsertTask,
    deleteTask,
    toggleTaskEnabled,
    runTaskNow,
    runSingleTest,
    cancelCurrentTest,
    clearHistory,
  }
})
