<script setup lang="ts">
// Token 用量统计主视图：严格只读扫描本地 ~/.gemini/antigravity，展示 KPI 卡、堆叠趋势图、模型/工作区分布及可筛选排序的分页对话表。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  RefreshCw,
  RotateCcw,
  Search,
  Database,
  Cpu,
  Brain,
  HardDrive,
  MessageSquare,
  ChevronLeft,
  ChevronRight,
  FolderOpen,
  AlertTriangle,
  ArrowUp,
  ArrowDown,
  ArrowUpDown,
  Download,
  X,
} from 'lucide-vue-next'
import UsageTrendChart from '@/components/tokenusage/UsageTrendChart.vue'
import ConversationDetailDrawer from '@/components/tokenusage/ConversationDetailDrawer.vue'
import AppSelect from '@/components/common/AppSelect.vue'
import { useAppStore } from '@/stores/appStore'
import { useTokenUsageStore } from '@/stores/tokenUsageStore'
import { api, extractErrorMessage } from '@/services/api'
import { formatDateTime, formatExactNumber, formatTokenCount } from '@/utils/quota'
import type { TokenUsageOverview, TokenUsageQuery, WorkspaceUsageSummary } from '@/types/models'

const appStore = useAppStore()
const usageStore = useTokenUsageStore()
const exportingCSV = ref(false)

async function handleExportCSV() {
  if (exportingCSV.value) return
  exportingCSV.value = true
  try {
    const savedPath = await api.saveTokenUsageCSV(usageStore.query)
    if (savedPath) {
      appStore.notify(`已导出 CSV 报表至 ${savedPath}`, 'success', 4200)
    }
  } catch (err) {
    appStore.notify(`导出 CSV 失败：${extractErrorMessage(err)}`, 'error')
  } finally {
    exportingCSV.value = false
  }
}

type SortColumn = TokenUsageQuery['sort']

const presets: Array<{ id: TokenUsageQuery['preset']; label: string }> = [
  { id: 'today', label: '今日' },
  { id: '7d', label: '近 7 天' },
  { id: '30d', label: '近 30 天' },
  { id: 'month', label: '本月' },
  { id: 'all', label: '全部' },
]

const overview = computed<TokenUsageOverview | null>(
  () => usageStore.bundle?.overview ?? null,
)
const conversations = computed(() => usageStore.bundle?.conversations ?? null)
const usageSettings = computed(() => usageStore.bundle?.settings ?? null)
const totalAllTokens = computed(() => {
  if (!overview.value) return 0
  return overview.value.exact.totalTokens + overview.value.estimated.totalTokens
})
const totalIndexedFiles = computed(() => {
  if (!overview.value) return 0
  return (overview.value.scannedFiles ?? 0) + (overview.value.cachedFiles ?? 0)
})

const searchInput = ref(usageStore.query.search)
const sortStatusAnnouncement = ref('')
let searchDebounceTimer: number | null = null

onMounted(() => {
  void usageStore.fetchQuery()
})

onBeforeUnmount(() => {
  if (searchDebounceTimer !== null) {
    window.clearTimeout(searchDebounceTimer)
    searchDebounceTimer = null
  }
})

function setPreset(preset: TokenUsageQuery['preset']) {
  void usageStore.fetchQuery({ preset, page: 1 })
}

function setGranularity(granularity: TokenUsageQuery['granularity']) {
  void usageStore.fetchQuery({ granularity })
}

const kindOptions = [
  { value: '', label: '全部类型' },
  { value: 'main', label: '主对话' },
  { value: 'subagent', label: 'Subagent' },
  { value: 'swarm', label: 'Swarm' },
]

const modelFilterOptions = computed(() => {
  const displayById = new Map<string, string>()
  // 优先读取后端全量模型显示名映射，避免切换时间范围或选中单个模型后其他选项退化为原始 slug
  const allNames = overview.value?.availableModelNames
  if (allNames) {
    for (const [id, name] of Object.entries(allNames)) {
      if (id && name) displayById.set(id, name)
    }
  }
  for (const m of overview.value?.models ?? []) {
    if (m.modelId && !displayById.has(m.modelId)) {
      displayById.set(m.modelId, m.modelDisplayName || m.modelId)
    }
  }
  const list = (overview.value?.availableModels ?? []).map((id) => ({
    value: id,
    label: displayById.get(id) || id,
  }))
  if (usageStore.query.model && !list.some((opt) => opt.value === usageStore.query.model)) {
    list.unshift({
      value: usageStore.query.model,
      label: displayById.get(usageStore.query.model) || usageStore.query.model,
    })
  }
  return [{ value: '', label: '全部模型' }, ...list]
})

function shortPathHint(rawPath?: string): string {
  const cleaned = (rawPath ?? '').trim().replace(/\\/g, '/').replace(/\/+$/, '')
  if (!cleaned) return ''
  const parts = cleaned.split('/').filter(Boolean)
  if (parts.length <= 2) return parts.join('/')
  return `${parts[parts.length - 2]}/${parts[parts.length - 1]}`
}

function workspaceFilterKey(ws: WorkspaceUsageSummary): string {
  return ws.workspacePath || ws.workspaceName
}

function isWorkspaceSelected(ws: WorkspaceUsageSummary): boolean {
  const active = usageStore.query.workspace
  if (!active) return false
  return active === workspaceFilterKey(ws) || (!ws.workspacePath && active === ws.workspaceName)
}

const duplicateWorkspaceNames = computed(() => {
  const counts = new Map<string, number>()
  const opts = overview.value?.availableWorkspaceOptions
  if (opts && opts.length > 0) {
    for (const opt of opts) {
      counts.set(opt.name, (counts.get(opt.name) ?? 0) + 1)
    }
  } else {
    for (const ws of overview.value?.workspaces ?? []) {
      counts.set(ws.workspaceName, (counts.get(ws.workspaceName) ?? 0) + 1)
    }
  }
  const dup = new Set<string>()
  for (const [name, cnt] of counts.entries()) {
    if (cnt > 1) dup.add(name)
  }
  return dup
})

// 为同名工作区卡片构造清晰无碰撞的路径区分标签
function workspaceCardHint(ws: WorkspaceUsageSummary): string {
  if (!ws.workspacePath || !duplicateWorkspaceNames.value.has(ws.workspaceName)) return ''
  const matchedOpt = overview.value?.availableWorkspaceOptions?.find(
    (opt) => opt.value === workspaceFilterKey(ws),
  )
  if (matchedOpt && matchedOpt.label !== matchedOpt.name) {
    const start = matchedOpt.label.indexOf('(')
    const end = matchedOpt.label.lastIndexOf(')')
    if (start >= 0 && end > start) {
      return matchedOpt.label.slice(start + 1, end)
    }
  }
  return shortPathHint(ws.workspacePath)
}

const workspaceFilterOptions = computed(() => {
  const opts = overview.value?.availableWorkspaceOptions
  const list =
    opts && opts.length > 0
      ? opts.map((opt) => ({ value: opt.value, label: opt.label }))
      : (overview.value?.availableWorkspaces ?? []).map((ws) => ({
          value: ws,
          label: ws,
        }))
  if (usageStore.query.workspace && !list.some((opt) => opt.value === usageStore.query.workspace)) {
    const active = usageStore.query.workspace
    list.unshift({
      value: active,
      label: shortPathHint(active) || active,
    })
  }
  return [{ value: '', label: '全部工作区' }, ...list]
})

const hasActiveTableFilters = computed(() =>
  Boolean(
    usageStore.query.kind ||
      usageStore.query.model ||
      usageStore.query.workspace ||
      usageStore.query.search.trim(),
  ),
)

function setKind(kind: string) {
  void usageStore.fetchQuery({ kind, page: 1 })
}

function setModel(model: string) {
  void usageStore.fetchQuery({ model, page: 1 })
}

function setWorkspace(workspace: string) {
  void usageStore.fetchQuery({ workspace, page: 1 })
}

function clearTableFilters() {
  searchInput.value = ''
  if (searchDebounceTimer !== null) {
    window.clearTimeout(searchDebounceTimer)
    searchDebounceTimer = null
  }
  void usageStore.fetchQuery({
    kind: '',
    model: '',
    workspace: '',
    search: '',
    page: 1,
  })
}

function setSearch(val: string) {
  searchInput.value = val
  if (searchDebounceTimer !== null) {
    window.clearTimeout(searchDebounceTimer)
  }
  searchDebounceTimer = window.setTimeout(() => {
    searchDebounceTimer = null
    void usageStore.fetchQuery({ search: val, page: 1 })
  }, 250)
}

const sortColumnLabels: Record<SortColumn, string> = {
  turnCount: '轮数',
  totalTokens: '总消耗',
  lastModifiedTime: '最近活动',
}

// 仅在当前激活排序的 <th> 上设置 aria-sort="ascending" | "descending"，未排序列省略属性
function getAriaSort(col: SortColumn): 'ascending' | 'descending' | undefined {
  if (usageStore.query.sort !== col) return undefined
  return usageStore.query.order === 'asc' ? 'ascending' : 'descending'
}

function toggleSort(col: SortColumn) {
  let nextOrder: TokenUsageQuery['order'] = 'desc'
  if (usageStore.query.sort === col) {
    nextOrder = usageStore.query.order === 'desc' ? 'asc' : 'desc'
  }
  sortStatusAnnouncement.value = `已按${sortColumnLabels[col]}${nextOrder === 'asc' ? '升序' : '降序'}排列`
  void usageStore.fetchQuery({ sort: col, order: nextOrder, page: 1 })
}

function goPage(next: number) {
  if (!conversations.value) return
  if (next < 1 || next > conversations.value.totalPages) return
  void usageStore.fetchQuery({ page: next })
}

function pctShare(part: number, whole: number): number {
  if (!part || part <= 0 || !whole || whole <= 0) return 0
  return Math.max(1, Math.min(100, Math.round((part / whole) * 100)))
}

function kindLabel(kind: string): string {
  if (kind === 'swarm') return 'Swarm'
  if (kind === 'subagent') return 'Subagent'
  return '主对话'
}
</script>

<template>
  <div class="flex-1 min-w-0 h-full flex overflow-hidden">
    <section class="flex-1 min-w-0 h-full flex flex-col overflow-hidden">
      <!-- 顶栏（稳居单行，窄窗口下不换行移位） -->
      <header
        class="wails-drag shrink-0 px-5 pt-5 pb-4 border-b flex items-center justify-between gap-3"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div class="wails-no-drag flex items-center gap-2.5 min-w-0">
          <h1 class="text-[1.0625rem] font-semibold tracking-tight whitespace-nowrap shrink-0" style="color: var(--text-primary)">
            Token 用量统计
          </h1>
          <span
            v-if="overview"
            class="text-[11.5px] font-mono-num truncate min-w-0"
            style="color: var(--text-muted)"
          >
            {{ overview.conversationCount }} 对话 · {{ overview.turnCount }} 轮（精确 {{ overview.exactTurnCount }} 轮）
          </span>
          <span
            v-if="overview && overview.skippedErrors > 0"
            :title="`扫描期间已安全跳过 ${overview.skippedErrors} 个无法解析或已损坏的会话文件`"
            class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10.5px] font-medium shrink-0"
            style="background-color: var(--warning-soft); color: var(--warning-text)"
          >
            <AlertTriangle class="w-3 h-3" />
            跳过 {{ overview.skippedErrors }} 异常
          </span>
        </div>

        <div class="wails-no-drag flex items-center gap-1.5 shrink-0">
          <!-- 时间范围分段 -->
          <div
            class="h-8 inline-flex items-center p-0.5 rounded-lg border shrink-0"
            style="border-color: var(--border-subtle); background-color: var(--bg-surface)"
          >
            <button
              v-for="p in presets"
              :key="p.id"
              type="button"
              class="px-2 py-1 rounded-md text-xs font-medium whitespace-nowrap cursor-pointer transition-colors"
              :style="
                usageStore.query.preset === p.id
                  ? { backgroundColor: 'var(--bg-active)', color: 'var(--accent)' }
                  : { color: 'var(--text-secondary)' }
              "
              @click="setPreset(p.id)"
            >
              {{ p.label }}
            </button>
          </div>

          <button
            type="button"
            :disabled="usageStore.syncing"
            class="h-8 inline-flex items-center gap-1.5 px-2.5 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
            style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
            @click="usageStore.sync(false)"
          >
            <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': usageStore.syncing }" />
            <span>增量同步</span>
          </button>

          <button
            type="button"
            title="忽略文件指纹缓存全量重建"
            :disabled="usageStore.syncing"
            class="h-8 inline-flex items-center gap-1.5 px-2.5 rounded-lg border text-xs whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
            style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-secondary)"
            @click="usageStore.sync(true)"
          >
            <RotateCcw class="w-3.5 h-3.5" />
            <span>重建</span>
          </button>

          <button
            type="button"
            title="将当前筛选条件下的完整对话 Token 明细导出为 CSV"
            :disabled="exportingCSV || (conversations?.total ?? 0) === 0"
            class="h-8 inline-flex items-center gap-1.5 px-2.5 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
            style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
            @click="handleExportCSV"
          >
            <Download class="w-3.5 h-3.5" />
            <span>{{ exportingCSV ? '导出中...' : '导出 CSV' }}</span>
          </button>
        </div>
      </header>

      <!-- 主内容滚动区 -->
      <div class="flex-1 min-h-0 overflow-y-auto custom-scroll p-6 space-y-5">
        <div
          v-if="usageStore.error"
          class="p-3.5 rounded-xl text-xs"
          style="background-color: var(--danger-soft); color: var(--danger-text)"
        >
          {{ usageStore.error }}
        </div>

        <div
          v-if="usageSettings && !usageSettings.dataDirExists"
          class="p-4 rounded-2xl border flex items-center gap-3 text-xs"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-secondary)"
        >
          <FolderOpen class="w-5 h-5 shrink-0" style="color: var(--text-muted)" />
          <div>
            未检测到本地反重力会话目录
            <code class="font-mono-num">{{ usageSettings.effectiveDataDir }}</code>
            <span v-if="usageSettings.customDataDir && usageSettings.defaultDataDir">
              （默认目录：<code class="font-mono-num">{{ usageSettings.defaultDataDir }}</code>）
            </span>，可在左下角「设置」中自定义扫描目录。
          </div>
        </div>

        <!-- 4 张核心 KPI 卡片 -->
        <div class="grid grid-cols-2 xl:grid-cols-4 gap-3.5">
          <div
            class="p-4 rounded-2xl border"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <div class="flex items-center justify-between text-xs" style="color: var(--text-muted)">
              <span>总消耗 Token</span>
              <Database class="w-4 h-4" style="color: var(--accent)" />
            </div>
            <div
              class="mt-2 text-2xl font-bold font-mono-num"
              :title="formatExactNumber(totalAllTokens)"
              style="color: var(--text-primary)"
            >
              {{ formatTokenCount(totalAllTokens) }}
            </div>
            <div class="mt-1 text-[11px] font-mono-num" style="color: var(--text-muted)">
              精确 {{ formatTokenCount(overview?.exact.totalTokens) }}
            </div>
          </div>

          <div
            class="p-4 rounded-2xl border"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <div class="flex items-center justify-between text-xs" style="color: var(--text-muted)">
              <span>输入 / 纯输出</span>
              <Cpu class="w-4 h-4" style="color: var(--success)" />
            </div>
            <div class="mt-2 text-2xl font-bold font-mono-num" style="color: var(--text-primary)">
              {{ formatTokenCount(overview?.exact.inputTokens) }}
            </div>
            <div class="mt-1 text-[11px] font-mono-num" style="color: var(--text-muted)">
              回复输出 {{ formatTokenCount(overview?.exact.outputTokens) }}
            </div>
          </div>

          <div
            class="p-4 rounded-2xl border"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <div class="flex items-center justify-between text-xs" style="color: var(--text-muted)">
              <span>Thinking 推理</span>
              <Brain class="w-4 h-4" style="color: #8b5cf6" />
            </div>
            <div class="mt-2 text-2xl font-bold font-mono-num" style="color: var(--text-primary)">
              {{ formatTokenCount(overview?.exact.thinkingTokens) }}
            </div>
            <div class="mt-1 text-[11px]" style="color: var(--text-muted)">
              已与纯输出去重分离统计
            </div>
          </div>

          <div
            class="p-4 rounded-2xl border"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <div class="flex items-center justify-between text-xs" style="color: var(--text-muted)">
              <span>提示词缓存</span>
              <HardDrive class="w-4 h-4" style="color: var(--warning)" />
            </div>
            <div class="mt-2 text-2xl font-bold font-mono-num" style="color: var(--text-primary)">
              {{ formatTokenCount((overview?.exact.cacheReadTokens ?? 0) + (overview?.exact.cacheWriteTokens ?? 0)) }}
            </div>
            <div class="mt-1 text-[11px] font-mono-num" style="color: var(--text-muted)">
              读 {{ formatTokenCount(overview?.exact.cacheReadTokens) }} · 写 {{ formatTokenCount(overview?.exact.cacheWriteTokens) }}
            </div>
          </div>
        </div>

        <!-- 趋势柱状图（含按天 / 按周 / 按月粒度切换） -->
        <UsageTrendChart
          :buckets="overview?.trend ?? []"
          :granularity="usageStore.query.granularity"
          @update:granularity="setGranularity"
        />

        <!-- 模型分布 + 工作区分布（支持点击联动过滤下方对话表） -->
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <div
            class="p-4 rounded-2xl border space-y-3"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <div class="flex items-center justify-between gap-2">
              <h3 class="text-xs font-semibold" style="color: var(--text-primary)">模型消耗排行</h3>
              <span class="text-[11px]" style="color: var(--text-muted)">点击按模型筛选</span>
            </div>
            <div v-if="overview?.models?.length" class="space-y-2.5">
              <button
                v-for="m in overview.models.slice(0, 6)"
                :key="m.modelId"
                type="button"
                class="w-full text-left p-1.5 -mx-1.5 rounded-lg space-y-1 cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
                :style="usageStore.query.model === m.modelId ? { backgroundColor: 'var(--accent-soft)' } : undefined"
                @click="setModel(usageStore.query.model === m.modelId ? '' : m.modelId)"
              >
                <div class="flex items-center justify-between text-xs gap-2">
                  <span class="truncate font-medium" style="color: var(--text-primary)">{{ m.modelDisplayName }}</span>
                  <span class="font-mono-num text-[11.5px] shrink-0" style="color: var(--text-secondary)">
                    {{ formatTokenCount(m.exact.totalTokens + m.estimatedTotalTokens) }}
                  </span>
                </div>
                <div class="h-1.5 w-full rounded-full overflow-hidden" style="background-color: var(--bg-subtle)">
                  <div
                    class="h-full rounded-full"
                    :style="{
                      width: `${pctShare(m.exact.totalTokens + m.estimatedTotalTokens, totalAllTokens)}%`,
                      backgroundColor: 'var(--accent)',
                    }"
                  />
                </div>
              </button>
            </div>
            <p v-else class="text-xs py-6 text-center" style="color: var(--text-muted)">暂无模型统计数据</p>
          </div>

          <div
            class="p-4 rounded-2xl border space-y-3"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <div class="flex items-center justify-between gap-2">
              <h3 class="text-xs font-semibold" style="color: var(--text-primary)">工作区消耗排行</h3>
              <span class="text-[11px]" style="color: var(--text-muted)">点击按工作区筛选</span>
            </div>
            <div v-if="overview?.workspaces?.length" class="space-y-2.5">
              <button
                v-for="ws in overview.workspaces.slice(0, 6)"
                :key="workspaceFilterKey(ws)"
                type="button"
                class="w-full text-left p-1.5 -mx-1.5 rounded-lg space-y-1 cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
                :style="isWorkspaceSelected(ws) ? { backgroundColor: 'var(--success-soft)' } : undefined"
                @click="setWorkspace(isWorkspaceSelected(ws) ? '' : workspaceFilterKey(ws))"
              >
                <div class="flex items-center justify-between text-xs gap-2">
                  <span
                    class="truncate font-medium min-w-0"
                    :title="ws.workspacePath || ws.workspaceName"
                    style="color: var(--text-primary)"
                  >
                    {{ ws.workspaceName }}
                    <span
                      v-if="workspaceCardHint(ws)"
                      class="ml-1 font-normal font-mono-num text-[10.5px]"
                      style="color: var(--text-muted)"
                    >
                      ({{ workspaceCardHint(ws) }})
                    </span>
                  </span>
                  <span class="font-mono-num text-[11.5px] shrink-0" style="color: var(--text-secondary)">
                    {{ formatTokenCount(ws.exact.totalTokens + ws.estimatedTotalTokens) }}
                    <span class="ml-1" style="color: var(--text-muted)">({{ ws.conversationCount }})</span>
                  </span>
                </div>
                <div class="h-1.5 w-full rounded-full overflow-hidden" style="background-color: var(--bg-subtle)">
                  <div
                    class="h-full rounded-full"
                    :style="{
                      width: `${pctShare(ws.exact.totalTokens + ws.estimatedTotalTokens, totalAllTokens)}%`,
                      backgroundColor: 'var(--success)',
                    }"
                  />
                </div>
              </button>
            </div>
            <p v-else class="text-xs py-6 text-center" style="color: var(--text-muted)">暂无工作区统计数据</p>
          </div>
        </div>

        <!-- 对话明细表 -->
        <div
          class="rounded-2xl border overflow-hidden"
          style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
        >
          <div
            class="px-4 py-3 border-b flex items-center justify-between gap-3 flex-wrap"
            style="border-color: var(--border-subtle)"
          >
            <div class="flex items-center gap-2 min-w-0">
              <MessageSquare class="w-4 h-4 shrink-0" style="color: var(--text-muted)" />
              <h3 class="text-xs font-semibold shrink-0" style="color: var(--text-primary)">对话消耗明细</h3>
              <span
                v-if="overview?.lastSyncedAt"
                :title="`总索引 ${totalIndexedFiles} 个会话文件（本次新扫 ${overview.scannedFiles} · 缓存命中 ${overview.cachedFiles}）`"
                class="text-[11px] font-mono-num truncate"
                style="color: var(--text-muted)"
              >
                索引 {{ totalIndexedFiles }} 文件 · 同步于 {{ formatDateTime(overview.lastSyncedAt) }}
              </span>
            </div>

            <div class="flex items-center flex-wrap gap-2">
              <div class="relative w-44 max-w-full">
                <Search
                  class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none"
                  style="color: var(--text-muted)"
                />
                <input
                  :value="searchInput"
                  type="search"
                  placeholder="搜索标题或工作区..."
                  class="w-full h-7 pl-8 pr-2.5 text-xs rounded-lg border"
                  style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-primary)"
                  @input="setSearch(($event.target as HTMLInputElement).value)"
                />
              </div>

              <AppSelect
                :model-value="usageStore.query.kind"
                aria-label="筛选对话类型"
                size="sm"
                :options="kindOptions"
                class="w-28 shrink-0"
                @update:model-value="setKind"
              />

              <AppSelect
                :model-value="usageStore.query.model"
                aria-label="筛选使用模型"
                size="sm"
                :options="modelFilterOptions"
                class="w-36 shrink-0"
                @update:model-value="setModel"
              />

              <AppSelect
                :model-value="usageStore.query.workspace"
                aria-label="筛选所属工作区"
                size="sm"
                :options="workspaceFilterOptions"
                class="w-36 shrink-0"
                @update:model-value="setWorkspace"
              />

              <button
                v-if="hasActiveTableFilters"
                type="button"
                class="h-7 inline-flex items-center gap-1 px-2 rounded-lg border text-[11px] cursor-pointer hover:bg-[var(--bg-hover)]"
                style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-app)"
                @click="clearTableFilters"
              >
                <X class="w-3 h-3" />
                <span>重置</span>
              </button>
            </div>
          </div>

          <div role="status" aria-live="polite" class="sr-only">
            {{ sortStatusAnnouncement }}
          </div>

          <div
            class="overflow-x-auto custom-scroll"
            role="region"
            aria-label="对话消耗明细表"
            tabindex="0"
          >
            <table class="w-full text-left border-collapse text-xs sortable-usage-table">
              <thead>
                <tr
                  class="border-b text-[11px]"
                  style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-muted)"
                >
                  <th scope="col" class="py-2.5 px-4 whitespace-nowrap">对话</th>
                  <th scope="col" class="py-2.5 px-3 whitespace-nowrap">类型</th>
                  <th scope="col" class="py-2.5 px-3 whitespace-nowrap">工作区</th>
                  <th scope="col" class="py-2.5 px-3 whitespace-nowrap">主力模型</th>
                  <th scope="col" :aria-sort="getAriaSort('turnCount')" class="p-0 whitespace-nowrap">
                    <button
                      type="button"
                      class="w-full py-2.5 px-3 inline-flex items-center justify-end gap-1 font-medium cursor-pointer hover:bg-[var(--bg-hover)] transition-colors"
                      :style="{ color: usageStore.query.sort === 'turnCount' ? 'var(--text-primary)' : 'var(--text-muted)' }"
                      @click="toggleSort('turnCount')"
                    >
                      <span>轮数</span>
                      <ArrowUp
                        v-if="usageStore.query.sort === 'turnCount' && usageStore.query.order === 'asc'"
                        class="w-3 h-3 shrink-0"
                        aria-hidden="true"
                      />
                      <ArrowDown
                        v-else-if="usageStore.query.sort === 'turnCount'"
                        class="w-3 h-3 shrink-0"
                        aria-hidden="true"
                      />
                      <ArrowUpDown v-else class="w-3 h-3 shrink-0 opacity-45" aria-hidden="true" />
                    </button>
                  </th>
                  <th scope="col" :aria-sort="getAriaSort('totalTokens')" class="p-0 whitespace-nowrap">
                    <button
                      type="button"
                      class="w-full py-2.5 px-3 inline-flex items-center justify-end gap-1 font-medium cursor-pointer hover:bg-[var(--bg-hover)] transition-colors"
                      :style="{ color: usageStore.query.sort === 'totalTokens' ? 'var(--text-primary)' : 'var(--text-muted)' }"
                      @click="toggleSort('totalTokens')"
                    >
                      <span>总消耗</span>
                      <ArrowUp
                        v-if="usageStore.query.sort === 'totalTokens' && usageStore.query.order === 'asc'"
                        class="w-3 h-3 shrink-0"
                        aria-hidden="true"
                      />
                      <ArrowDown
                        v-else-if="usageStore.query.sort === 'totalTokens'"
                        class="w-3 h-3 shrink-0"
                        aria-hidden="true"
                      />
                      <ArrowUpDown v-else class="w-3 h-3 shrink-0 opacity-45" aria-hidden="true" />
                    </button>
                  </th>
                  <th scope="col" :aria-sort="getAriaSort('lastModifiedTime')" class="p-0 whitespace-nowrap">
                    <button
                      type="button"
                      class="w-full py-2.5 px-4 inline-flex items-center justify-end gap-1 font-medium cursor-pointer hover:bg-[var(--bg-hover)] transition-colors"
                      :style="{ color: usageStore.query.sort === 'lastModifiedTime' ? 'var(--text-primary)' : 'var(--text-muted)' }"
                      @click="toggleSort('lastModifiedTime')"
                    >
                      <span>最近活动</span>
                      <ArrowUp
                        v-if="usageStore.query.sort === 'lastModifiedTime' && usageStore.query.order === 'asc'"
                        class="w-3 h-3 shrink-0"
                        aria-hidden="true"
                      />
                      <ArrowDown
                        v-else-if="usageStore.query.sort === 'lastModifiedTime'"
                        class="w-3 h-3 shrink-0"
                        aria-hidden="true"
                      />
                      <ArrowUpDown v-else class="w-3 h-3 shrink-0 opacity-45" aria-hidden="true" />
                    </button>
                  </th>
                </tr>
              </thead>
              <tbody class="divide-y divide-[var(--border-hairline)]">
                <tr
                  v-for="item in conversations?.items ?? []"
                  :key="item.conversationId"
                  class="cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
                  @click="usageStore.openConversationDetail(item.conversationId)"
                >
                  <td class="py-2.5 px-4 max-w-[260px]">
                    <div class="font-medium truncate" style="color: var(--text-primary)">
                      {{ item.displayTitle }}
                    </div>
                    <div v-if="item.preview" class="text-[11px] truncate" style="color: var(--text-muted)">
                      {{ item.preview }}
                    </div>
                  </td>
                  <td class="py-2.5 px-3 whitespace-nowrap">
                    <span
                      class="px-1.5 py-0.5 rounded text-[10.5px] font-medium"
                      style="background-color: var(--bg-subtle); color: var(--text-secondary)"
                    >
                      {{ kindLabel(item.kind) }}
                    </span>
                  </td>
                  <td class="py-2.5 px-3 truncate max-w-[140px]" style="color: var(--text-secondary)">
                    {{ item.workspaceName }}
                  </td>
                  <td class="py-2.5 px-3 truncate max-w-[160px]" style="color: var(--text-secondary)">
                    {{ item.primaryModelDisplayName }}
                  </td>
                  <td class="py-2.5 px-3 text-right font-mono-num whitespace-nowrap" style="color: var(--text-secondary)">
                    {{ item.turnCount }}
                  </td>
                  <td class="py-2.5 px-3 text-right font-mono-num font-semibold whitespace-nowrap" style="color: var(--text-primary)">
                    {{ formatTokenCount(item.exact.totalTokens + item.estimated.totalTokens) }}
                  </td>
                  <td class="py-2.5 px-4 text-right font-mono-num text-[11px] whitespace-nowrap" style="color: var(--text-muted)">
                    {{ formatDateTime(item.lastModifiedTime) }}
                  </td>
                </tr>
                <tr v-if="!conversations?.items?.length">
                  <td colspan="7" class="py-10 text-center text-xs" style="color: var(--text-muted)">
                    {{ usageStore.loading ? '正在只读扫描本地对话记录...' : '暂无符合条件的对话消耗记录' }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- 分页控件 -->
          <div
            v-if="conversations && conversations.totalPages > 1"
            class="px-4 py-2.5 border-t flex items-center justify-between flex-wrap gap-2 text-xs"
            style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-muted)"
          >
            <span class="tabular-nums">共 {{ conversations.total }} 条，第 {{ conversations.page }} / {{ conversations.totalPages }} 页</span>
            <div class="flex items-center gap-1.5">
              <button
                type="button"
                aria-label="上一页"
                :disabled="conversations.page <= 1"
                class="p-1 rounded-lg border cursor-pointer disabled:opacity-40"
                style="border-color: var(--border-strong); background-color: var(--bg-surface)"
                @click="goPage(conversations.page - 1)"
              >
                <ChevronLeft class="w-3.5 h-3.5" />
              </button>
              <button
                type="button"
                aria-label="下一页"
                :disabled="conversations.page >= conversations.totalPages"
                class="p-1 rounded-lg border cursor-pointer disabled:opacity-40"
                style="border-color: var(--border-strong); background-color: var(--bg-surface)"
                @click="goPage(conversations.page + 1)"
              >
                <ChevronRight class="w-3.5 h-3.5" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </section>

    <!-- 右侧单对话步级消耗详情抽屉 -->
    <ConversationDetailDrawer
      :open="usageStore.detailOpen"
      :loading="usageStore.detailLoading"
      :detail="usageStore.activeDetail"
      @close="usageStore.closeConversationDetail"
    />
  </div>
</template>
