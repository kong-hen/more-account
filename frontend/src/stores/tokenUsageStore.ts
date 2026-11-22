// Token 用量统计只读查询与单对话详情状态管理。
import { ref } from 'vue'
import { defineStore } from 'pinia'
import { api, extractErrorMessage, isWailsRuntime } from '@/services/api'
import { useAppStore } from '@/stores/appStore'
import type {
  ConversationDetail,
  TokenUsageBundle,
  TokenUsageQuery,
} from '@/types/models'

export const useTokenUsageStore = defineStore('tokenUsage', () => {
  const query = ref<TokenUsageQuery>({
    preset: '7d',
    granularity: 'day',
    kind: '',
    model: '',
    workspace: '',
    search: '',
    sort: 'lastModifiedTime',
    order: 'desc',
    page: 1,
    pageSize: 20,
  })

  const bundle = ref<TokenUsageBundle | null>(null)
  const loading = ref(false)
  const syncing = ref(false)
  const error = ref('')

  const detailOpen = ref(false)
  const detailLoading = ref(false)
  const activeDetail = ref<ConversationDetail | null>(null)

  let querySeq = 0
  let detailSeq = 0
  // 独立追踪查询与同步的在途请求数，避免共享 querySeq 竞态导致 loading/syncing 永久锁死
  let activeQueryCount = 0
  let activeSyncCount = 0

  async function fetchQuery(patch?: Partial<TokenUsageQuery>) {
    if (patch) {
      query.value = { ...query.value, ...patch }
    }
    if (!isWailsRuntime()) return
    const seq = ++querySeq
    activeQueryCount++
    loading.value = true
    error.value = ''
    try {
      const res = await api.queryTokenUsage(query.value)
      if (seq !== querySeq) return
      bundle.value = res
    } catch (err) {
      if (seq !== querySeq) return
      error.value = extractErrorMessage(err)
    } finally {
      activeQueryCount = Math.max(0, activeQueryCount - 1)
      if (activeQueryCount === 0) {
        loading.value = false
      }
    }
  }

  async function sync(rebuild = false) {
    if (!isWailsRuntime()) return
    const appStore = useAppStore()
    const seq = ++querySeq
    activeSyncCount++
    syncing.value = true
    error.value = ''
    try {
      const res = await api.syncTokenUsage(rebuild, query.value)
      if (seq !== querySeq) return
      bundle.value = res
      appStore.notify(
        rebuild
          ? `已重建索引（扫描 ${res.overview.scannedFiles} 个会话）`
          : `增量同步完成（耗时 ${res.overview.scanDurationMs}ms）`,
        'success',
      )
    } catch (err) {
      if (seq !== querySeq) return
      error.value = extractErrorMessage(err)
      appStore.notify(`同步 Token 用量失败：${error.value}`, 'error')
    } finally {
      activeSyncCount = Math.max(0, activeSyncCount - 1)
      if (activeSyncCount === 0) {
        syncing.value = false
      }
    }
  }

  // 当用户在设置中更改自定义扫描目录时清空旧缓存、复位页码与错误状态并按需刷新
  function invalidate(reloadNow = false) {
    querySeq++
    bundle.value = null
    error.value = ''
    query.value = { ...query.value, page: 1 }
    closeConversationDetail()
    if (reloadNow) {
      void fetchQuery({ page: 1 })
    }
  }

  async function openConversationDetail(conversationId: string) {
    if (!isWailsRuntime()) return
    const appStore = useAppStore()
    const seq = ++detailSeq
    detailOpen.value = true
    detailLoading.value = true
    activeDetail.value = null
    try {
      const res = await api.getConversationDetail(conversationId)
      if (seq !== detailSeq || !detailOpen.value) return
      activeDetail.value = res
    } catch (err) {
      if (seq !== detailSeq) return
      appStore.notify(`加载对话明细失败：${extractErrorMessage(err)}`, 'error')
      detailOpen.value = false
    } finally {
      if (seq === detailSeq) {
        detailLoading.value = false
      }
    }
  }

  function closeConversationDetail() {
    detailSeq++
    detailOpen.value = false
    detailLoading.value = false
    activeDetail.value = null
  }

  return {
    query,
    bundle,
    loading,
    syncing,
    error,
    detailOpen,
    detailLoading,
    activeDetail,
    fetchQuery,
    sync,
    invalidate,
    openConversationDetail,
    closeConversationDetail,
  }
})
