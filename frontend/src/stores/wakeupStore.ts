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
  const runningTaskId = ref<string | null>(null)
  const testingScopeId = ref<string | null>(null)

  async function loadAll() {
    if (!isWailsRuntime()) return
    loading.value = true
    try {
      const [s, h] = await Promise.all([api.getWakeupState(), api.listWakeupHistory()])
      state.value = s ?? { enabled: false, tasks: [] }
      history.value = h ?? []
    } finally {
      loading.value = false
    }
  }

  async function saveState(next: WakeupState) {
    const appStore = useAppStore()
    try {
      await api.saveWakeupState(next)
      state.value = await api.getWakeupState()
      appStore.notify('定时唤醒配置已保存', 'success')
    } catch (err) {
      appStore.notify(`保存唤醒配置失败：${extractErrorMessage(err)}`, 'error')
    }
  }

  async function toggleGlobalEnabled() {
    await saveState({
      ...state.value,
      enabled: !state.value.enabled,
    })
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
    const tasks = state.value.tasks.map((t) =>
      t.id === taskId ? { ...t, enabled: !t.enabled } : t,
    )
    await saveState({ ...state.value, tasks })
  }

  async function runTaskNow(taskId: string) {
    const appStore = useAppStore()
    if (runningTaskId.value) return
    runningTaskId.value = taskId
    try {
      const items = await api.runWakeupTaskNow(taskId)
      await loadAll()
      const okCount = (items ?? []).filter((i) => i.success).length
      const failCount = (items ?? []).length - okCount
      if (failCount > 0) {
        appStore.notify(`唤醒执行完毕：成功 ${okCount}，失败 ${failCount}`, 'warning')
      } else {
        appStore.notify(`唤醒任务执行成功（共 ${okCount} 次请求）`, 'success')
      }
    } catch (err) {
      appStore.notify(`唤醒任务执行失败：${extractErrorMessage(err)}`, 'error')
    } finally {
      runningTaskId.value = null
    }
  }

  async function runSingleTest(req: WakeupTestRequest) {
    const appStore = useAppStore()
    const scopeId = req.scopeId || `scope_${Date.now()}`
    testingScopeId.value = scopeId
    try {
      const result = await api.runWakeupTest({ ...req, scopeId })
      await loadAll()
      if (result.success) {
        appStore.notify(`唤醒测试成功（${result.durationMs}ms，回复：${result.reply || 'OK'}）`, 'success')
      } else {
        appStore.notify(`唤醒测试失败：${result.errorMessage || '未知错误'}`, 'warning', 4500)
      }
      return result
    } catch (err) {
      appStore.notify(`唤醒测试失败：${extractErrorMessage(err)}`, 'error', 4500)
      return null
    } finally {
      if (testingScopeId.value === scopeId) {
        testingScopeId.value = null
      }
    }
  }

  async function cancelCurrentTest() {
    if (!testingScopeId.value) return
    await api.cancelWakeupTest(testingScopeId.value)
    testingScopeId.value = null
  }

  async function clearHistory() {
    const appStore = useAppStore()
    await api.clearWakeupHistory()
    history.value = []
    appStore.notify('唤醒历史已清空', 'success')
  }

  return {
    state,
    history,
    loading,
    runningTaskId,
    testingScopeId,
    loadAll,
    saveState,
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
