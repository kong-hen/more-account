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

  async function fetchQuery(patch?: Partial<TokenUsageQuery>) {
    if (patch) {
      query.value = { ...query.value, ...patch }
    }
    if (!isWailsRuntime()) return
    loading.value = true
    error.value = ''
    try {
      bundle.value = await api.queryTokenUsage(query.value)
    } catch (err) {
      error.value = extractErrorMessage(err)
    } finally {
      loading.value = false
    }
  }

  async function sync(rebuild = false) {
    if (!isWailsRuntime()) return
    const appStore = useAppStore()
    syncing.value = true
    error.value = ''
    try {
      bundle.value = await api.syncTokenUsage(rebuild, query.value)
      appStore.notify(
        rebuild
          ? `已重建索引（扫描 ${bundle.value.overview.scannedFiles} 个会话）`
          : `增量同步完成（耗时 ${bundle.value.overview.scanDurationMs}ms）`,
        'success',
      )
    } catch (err) {
      error.value = extractErrorMessage(err)
      appStore.notify(`同步 Token 用量失败：${error.value}`, 'error')
    } finally {
      syncing.value = false
    }
  }

  async function openConversationDetail(conversationId: string) {
    if (!isWailsRuntime()) return
    const appStore = useAppStore()
    detailOpen.value = true
    detailLoading.value = true
    activeDetail.value = null
    try {
      activeDetail.value = await api.getConversationDetail(conversationId)
    } catch (err) {
      appStore.notify(`加载对话明细失败：${extractErrorMessage(err)}`, 'error')
      detailOpen.value = false
    } finally {
      detailLoading.value = false
    }
  }

  function closeConversationDetail() {
    detailOpen.value = false
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
    openConversationDetail,
    closeConversationDetail,
  }
})
