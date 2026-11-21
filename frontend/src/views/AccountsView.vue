<script setup lang="ts">
// 账号工作台主视图：宽松单行顶栏（搜索 + 聚合筛选下拉含可选低配额/异常 + 排序 + 双视图切换 + 刷新 + 添加）、
// 浮现式多选批量栏、高舒适度卡片网格 / 紧凑表格双视图。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  Search,
  SlidersHorizontal,
  LayoutGrid,
  List,
  RefreshCw,
  Plus,
  Download,
  Trash2,
  FolderInput,
  X,
  UserPlus,
  AlertTriangle,
  BatteryLow,
} from 'lucide-vue-next'
import AccountCard from '@/components/accounts/AccountCard.vue'
import AccountTable from '@/components/accounts/AccountTable.vue'
import AppSelect from '@/components/common/AppSelect.vue'
import { useAppStore, type AccountSortBy } from '@/stores/appStore'
import { extractErrorMessage } from '@/services/api'
import type { Account } from '@/types/models'

const emit = defineEmits<{
  (e: 'open-add'): void
  (e: 'open-export', ids: string[]): void
  (e: 'request-delete', ids: string[], label: string): void
}>()

const appStore = useAppStore()
const filterMenuOpen = ref(false)
const filterWrapRef = ref<HTMLElement | null>(null)
const batchGroupTarget = ref('')
const draggingAccountId = ref<string | null>(null)
const dragOverAccountId = ref<string | null>(null)

const canReorder = computed(
  () => appStore.sortBy === 'custom' && appStore.filteredAccounts.length > 1,
)

// 分组被删除时自动复位失效的批量归组目标，避免提交已不存在的 groupId
watch(
  () => appStore.groups,
  (groups) => {
    if (batchGroupTarget.value && !groups.some((g) => g.id === batchGroupTarget.value)) {
      batchGroupTarget.value = ''
    }
  },
)

function handleDragStart(accountId: string, event: DragEvent) {
  if (!canReorder.value) return
  draggingAccountId.value = accountId
  if (event.dataTransfer) {
    event.dataTransfer.effectAllowed = 'move'
    event.dataTransfer.setData('text/plain', accountId)
  }
}

function handleDragOver(accountId: string, event: DragEvent) {
  if (!canReorder.value || !draggingAccountId.value) return
  event.preventDefault()
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = 'move'
  }
  if (draggingAccountId.value !== accountId) {
    dragOverAccountId.value = accountId
  }
}

function handleDragEnd() {
  draggingAccountId.value = null
  dragOverAccountId.value = null
}

async function handleDrop(targetId: string, event: DragEvent) {
  event.preventDefault()
  const sourceId = draggingAccountId.value
  handleDragEnd()
  if (!canReorder.value || !sourceId || sourceId === targetId) return

  const visibleIds = appStore.filteredAccounts.map((a) => a.id)
  const fromIdx = visibleIds.indexOf(sourceId)
  const toIdx = visibleIds.indexOf(targetId)
  if (fromIdx < 0 || toIdx < 0) return

  const reorderedVisible = [...visibleIds]
  const [moved] = reorderedVisible.splice(fromIdx, 1)
  reorderedVisible.splice(toIdx, 0, moved)

  // 将可见子列表的新顺序回填到全量账号对应的槽位中，确保不影响其他分组的相对顺序
  const visibleSet = new Set(visibleIds)
  let cursor = 0
  const nextAllIds = appStore.accounts.map((acc) =>
    visibleSet.has(acc.id) ? reorderedVisible[cursor++] : acc.id,
  )
  await appStore.reorderAccounts(nextAllIds)
}

const currentGroupTitle = computed(() => {
  if (appStore.selectedGroupFilter === 'all') return '全部账号'
  if (appStore.selectedGroupFilter === 'ungrouped') return '未分组'
  return appStore.groupMap.get(appStore.selectedGroupFilter)?.name ?? '全部账号'
})

function handleOutsideClick(e: MouseEvent) {
  if (!filterMenuOpen.value) return
  if (filterWrapRef.value && !filterWrapRef.value.contains(e.target as Node)) {
    filterMenuOpen.value = false
  }
}

onMounted(() => {
  window.addEventListener('mousedown', handleOutsideClick)
})

onBeforeUnmount(() => {
  window.removeEventListener('mousedown', handleOutsideClick)
})

function handleOpenDetail(account: Account) {
  if (appStore.activeDetailAccountId === account.id) {
    appStore.requestSelectDetailAccount(null)
  } else {
    appStore.requestSelectDetailAccount(account.id)
  }
}

function toggleSelectAllVisible() {
  const visibleIds = appStore.filteredAccounts.map((a) => a.id)
  const allSelected =
    visibleIds.length > 0 && visibleIds.every((id) => appStore.selectedAccountIds.includes(id))
  if (allSelected) {
    appStore.selectedAccountIds = appStore.selectedAccountIds.filter(
      (id) => !visibleIds.includes(id),
    )
  } else {
    const merged = new Set([...appStore.selectedAccountIds, ...visibleIds])
    appStore.selectedAccountIds = Array.from(merged)
  }
}

async function applyBatchGroup() {
  if (appStore.selectedAccountIds.length === 0) return
  try {
    await appStore.assignAccountsToGroup(appStore.selectedAccountIds, batchGroupTarget.value)
  } catch (err) {
    appStore.notify(`批量归组失败：${extractErrorMessage(err)}`, 'error')
  }
}

const sortOptions = [
  { value: 'custom', label: '默认排序' },
  { value: 'quota_desc', label: '配额从高到低' },
  { value: 'quota_asc', label: '配额从低到高' },
  { value: 'last_used', label: '最近使用' },
  { value: 'email', label: '按邮箱名称' },
]

const batchGroupOptions = computed(() => [
  { value: '', label: '移至未分组' },
  ...appStore.groups.map((g) => ({ value: g.id, label: `移至 ${g.name}` })),
])

function setSort(val: string) {
  appStore.sortBy = val as AccountSortBy
}

function formatRefreshInterval(minutes: number): string {
  if (minutes <= 0) return '关闭'
  if (minutes >= 60 && minutes % 60 === 0) return `每 ${minutes / 60} 小时`
  return `每 ${minutes} 分钟`
}

const refreshButtonTooltip = computed(() => {
  if (appStore.autoRefreshing) return '正在后台自动同步最新额度...'
  const activeMin = appStore.settings.autoRefreshMinutes
  const inactiveMin = appStore.settings.inactiveAutoRefreshMinutes
  if (activeMin <= 0 && inactiveMin <= 0) return '手动刷新全部账号额度'
  return `手动立即刷新全部额度（自动刷新：当前账号${formatRefreshInterval(activeMin)} / 未使用账号${formatRefreshInterval(inactiveMin)}）`
})
</script>

<template>
  <section class="flex-1 min-w-0 h-full flex flex-col overflow-hidden">
    <!-- 顶部单行操作栏（含 macOS 拖拽兼容区，通过弹性搜索框保证在 1024px 最小宽度下也稳居单行不折行移位） -->
    <header
      class="wails-drag shrink-0 px-5 pt-5 pb-4 border-b flex items-center justify-between gap-3"
      style="border-color: var(--border-subtle); background-color: var(--bg-app)"
    >
      <div class="wails-no-drag flex items-center gap-2 min-w-0 shrink">
        <h1 class="text-base font-semibold tracking-tight truncate" style="color: var(--text-primary)">
          {{ currentGroupTitle }}
        </h1>
        <span
          class="px-2 py-0.5 rounded-full text-xs font-mono-num shrink-0"
          style="background-color: var(--bg-subtle); color: var(--text-muted)"
        >
          {{ appStore.filteredAccounts.length }}
        </span>
      </div>

      <div class="wails-no-drag flex-1 min-w-0 flex items-center justify-end gap-1.5">
        <!-- 弹性搜索框：宽屏展开至 208px，窄屏平滑收缩以保证整条顶栏不换行移位 -->
        <div class="relative flex-1 min-w-[120px] max-w-[208px]">
          <Search
            class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none"
            style="color: var(--text-muted)"
          />
          <input
            v-model="appStore.searchQuery"
            type="search"
            placeholder="搜索邮箱、备注或分组..."
            class="w-full h-8 pl-8 pr-2.5 text-xs rounded-lg border"
            style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>

        <!-- 聚合筛选下拉：订阅等级 + 可选「仅看低配额」与「仅看异常」，保持顶栏不拥挤 -->
        <div ref="filterWrapRef" class="relative shrink-0">
          <button
            type="button"
            class="h-8 inline-flex items-center gap-1.5 px-2.5 text-xs font-medium rounded-lg border whitespace-nowrap shrink-0 cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
            :style="
              appStore.activeFilterCount > 0
                ? { borderColor: 'var(--accent)', backgroundColor: 'var(--accent-soft)', color: 'var(--accent)' }
                : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-surface)', color: 'var(--text-secondary)' }
            "
            @click="filterMenuOpen = !filterMenuOpen"
          >
            <SlidersHorizontal class="w-3.5 h-3.5" />
            <span>筛选</span>
            <span
              v-if="appStore.activeFilterCount > 0"
              class="px-1.5 py-0.5 rounded-full text-[10.5px] font-semibold text-white tabular-nums"
              style="background-color: var(--accent)"
            >
              {{ appStore.activeFilterCount }}
            </span>
          </button>

          <div
            v-if="filterMenuOpen"
            class="absolute right-0 mt-2 w-64 p-3.5 rounded-2xl border z-20 space-y-3.5"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle); box-shadow: var(--shadow-elevated)"
          >
            <!-- 订阅等级 -->
            <div class="space-y-1.5">
              <div class="text-[11px] font-semibold" style="color: var(--text-muted)">订阅等级</div>
              <div class="grid grid-cols-4 gap-1">
                <button
                  v-for="opt in ['all', 'ULTRA', 'PRO', 'FREE']"
                  :key="opt"
                  type="button"
                  class="py-1 rounded-lg text-[11px] font-medium cursor-pointer transition-colors"
                  :style="
                    appStore.tierFilter === opt
                      ? { backgroundColor: 'var(--accent)', color: '#fff' }
                      : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-secondary)' }
                  "
                  @click="appStore.tierFilter = opt"
                >
                  {{ opt === 'all' ? '全部' : opt }}
                </button>
              </div>
            </div>

            <!-- 状态筛选：低配额 / 异常 -->
            <div class="space-y-1.5 pt-2 border-t" style="border-color: var(--border-subtle)">
              <div class="text-[11px] font-semibold" style="color: var(--text-muted)">健康状态筛选（可选）</div>

              <label
                class="flex items-center justify-between gap-2 p-2 rounded-lg cursor-pointer hover:bg-[var(--bg-hover)]"
              >
                <span class="flex items-center gap-2 text-xs" style="color: var(--text-primary)">
                  <input
                    v-model="appStore.onlyLowQuota"
                    type="checkbox"
                    class="rounded accent-[var(--accent)]"
                  />
                  <BatteryLow class="w-3.5 h-3.5" style="color: var(--warning)" />
                  <span>仅看低配额 (≤{{ appStore.settings.lowQuotaThreshold }}%)</span>
                </span>
                <span class="text-[11px] font-mono-num" style="color: var(--text-muted)">
                  {{ appStore.lowQuotaCount }}
                </span>
              </label>

              <label
                class="flex items-center justify-between gap-2 p-2 rounded-lg cursor-pointer hover:bg-[var(--bg-hover)]"
              >
                <span class="flex items-center gap-2 text-xs" style="color: var(--text-primary)">
                  <input
                    v-model="appStore.onlyAbnormal"
                    type="checkbox"
                    class="rounded accent-[var(--accent)]"
                  />
                  <AlertTriangle class="w-3.5 h-3.5" style="color: var(--danger)" />
                  <span>仅看异常 / 失效</span>
                </span>
                <span class="text-[11px] font-mono-num" style="color: var(--text-muted)">
                  {{ appStore.abnormalCount }}
                </span>
              </label>
            </div>

            <!-- 底部重置 -->
            <div class="pt-2 border-t flex items-center justify-between" style="border-color: var(--border-subtle)">
              <button
                type="button"
                class="text-[11.5px] cursor-pointer hover:underline"
                style="color: var(--text-muted)"
                @click="appStore.clearAdvancedFilters"
              >
                重置全部条件
              </button>
              <button
                type="button"
                class="px-2.5 py-1 rounded-lg text-[11.5px] font-medium cursor-pointer"
                style="background-color: var(--bg-subtle); color: var(--text-primary)"
                @click="filterMenuOpen = false"
              >
                完成
              </button>
            </div>
          </div>
        </div>

        <!-- 排序 -->
        <AppSelect
          aria-label="排序方式"
          :model-value="appStore.sortBy"
          :options="sortOptions"
          class="w-[114px] shrink-0"
          @update:model-value="setSort"
        />

        <!-- 视图切换：卡片 / 表格 -->
        <div
          class="h-8 inline-flex items-center p-0.5 rounded-lg border shrink-0"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface)"
        >
          <button
            type="button"
            title="卡片网格视图"
            aria-label="卡片网格视图"
            class="p-1.5 rounded-md cursor-pointer transition-colors"
            :style="
              appStore.viewMode === 'cards'
                ? { backgroundColor: 'var(--bg-active)', color: 'var(--accent)' }
                : { color: 'var(--text-muted)' }
            "
            @click="appStore.viewMode = 'cards'"
          >
            <LayoutGrid class="w-3.5 h-3.5" />
          </button>
          <button
            type="button"
            title="紧凑表格视图"
            aria-label="紧凑表格视图"
            class="p-1.5 rounded-md cursor-pointer transition-colors"
            :style="
              appStore.viewMode === 'table'
                ? { backgroundColor: 'var(--bg-active)', color: 'var(--accent)' }
                : { color: 'var(--text-muted)' }
            "
            @click="appStore.viewMode = 'table'"
          >
            <List class="w-3.5 h-3.5" />
          </button>
        </div>

        <!-- 刷新全部（后台自动刷新运行中也会柔和旋转图标提示） -->
        <button
          type="button"
          :disabled="appStore.refreshingAll || appStore.accounts.length === 0"
          :title="refreshButtonTooltip"
          class="h-8 inline-flex items-center gap-1.5 px-2.5 text-xs font-medium rounded-lg border whitespace-nowrap shrink-0 cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="appStore.refreshAllAccounts"
        >
          <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': appStore.refreshingAll || appStore.autoRefreshing }" />
          <span class="tabular-nums">
            {{
              appStore.refreshingAll
                ? `${appStore.refreshProgress.done}/${appStore.refreshProgress.total}`
                : '刷新'
            }}
          </span>
        </button>

        <!-- 添加账号主按钮 -->
        <button
          type="button"
          class="h-8 inline-flex items-center gap-1.5 px-3 text-xs font-medium rounded-lg text-white whitespace-nowrap shrink-0 cursor-pointer transition-colors"
          style="background-color: var(--accent)"
          @click="emit('open-add')"
        >
          <Plus class="w-3.5 h-3.5" />
          <span>添加账号</span>
        </button>
      </div>
    </header>

    <!-- 激活的筛选条件胶囊（仅当存在激活筛选时出现，保持平时极致清爽） -->
    <div
      v-if="appStore.activeFilterCount > 0"
      class="shrink-0 px-5 py-2 border-b flex items-center flex-wrap gap-2 text-xs"
      style="border-color: var(--border-subtle); background-color: var(--bg-surface)"
    >
      <span style="color: var(--text-muted)">当前筛选：</span>

      <span
        v-if="appStore.tierFilter !== 'all'"
        class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium"
        style="background-color: var(--accent-soft); color: var(--accent)"
      >
        等级 {{ appStore.tierFilter }}
        <button type="button" class="cursor-pointer" @click="appStore.tierFilter = 'all'">
          <X class="w-3 h-3" />
        </button>
      </span>

      <span
        v-if="appStore.onlyLowQuota"
        class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium"
        style="background-color: var(--warning-soft); color: var(--warning-text)"
      >
        低配额 ≤{{ appStore.settings.lowQuotaThreshold }}%
        <button type="button" class="cursor-pointer" @click="appStore.onlyLowQuota = false">
          <X class="w-3 h-3" />
        </button>
      </span>

      <span
        v-if="appStore.onlyAbnormal"
        class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium"
        style="background-color: var(--danger-soft); color: var(--danger-text)"
      >
        异常 / 失效
        <button type="button" class="cursor-pointer" @click="appStore.onlyAbnormal = false">
          <X class="w-3 h-3" />
        </button>
      </span>

      <button
        type="button"
        class="ml-auto text-[11.5px] cursor-pointer hover:underline"
        style="color: var(--text-muted)"
        @click="appStore.clearAdvancedFilters"
      >
        清除筛选
      </button>
    </div>

    <!-- 多选批量操作条（仅勾选 ≥1 项时显示） -->
    <div
      v-if="appStore.selectedAccountIds.length > 0"
      class="shrink-0 px-5 py-2.5 border-b flex items-center justify-between flex-wrap gap-x-3 gap-y-2 text-xs"
      style="border-color: var(--accent-border); background-color: var(--accent-soft)"
    >
      <div class="flex items-center gap-2.5 shrink-0">
        <span class="font-medium whitespace-nowrap" style="color: var(--accent)">
          已选中 {{ appStore.selectedAccountIds.length }} 个账号
        </span>
        <button
          type="button"
          class="text-[11.5px] whitespace-nowrap cursor-pointer hover:underline"
          style="color: var(--text-secondary)"
          @click="toggleSelectAllVisible"
        >
          全选当前列表
        </button>
      </div>

      <div class="flex items-center flex-wrap gap-2">
        <div class="flex items-center gap-1.5">
          <AppSelect
            v-model="batchGroupTarget"
            aria-label="选择目标分组"
            size="sm"
            :options="batchGroupOptions"
            class="w-36"
          />
          <button
            type="button"
            class="h-7 inline-flex items-center gap-1 px-2.5 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)]"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            @click="applyBatchGroup"
          >
            <FolderInput class="w-3.5 h-3.5" />
            归组
          </button>
        </div>

        <button
          type="button"
          class="h-7 inline-flex items-center gap-1 px-2.5 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)]"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="emit('open-export', [...appStore.selectedAccountIds])"
        >
          <Download class="w-3.5 h-3.5" />
          导出
        </button>

        <button
          type="button"
          class="h-7 inline-flex items-center gap-1 px-2.5 rounded-lg text-xs font-medium text-white whitespace-nowrap shrink-0 cursor-pointer"
          style="background-color: var(--danger)"
          @click="
            emit(
              'request-delete',
              [...appStore.selectedAccountIds],
              `已选中的 ${appStore.selectedAccountIds.length} 个账号`,
            )
          "
        >
          <Trash2 class="w-3.5 h-3.5" />
          删除
        </button>

        <button
          type="button"
          aria-label="取消多选"
          class="p-1 rounded cursor-pointer hover:bg-[var(--bg-hover)]"
          style="color: var(--text-secondary)"
          @click="appStore.selectedAccountIds = []"
        >
          <X class="w-3.5 h-3.5" />
        </button>
      </div>
    </div>

    <!-- 主内容滚动区 -->
    <div class="flex-1 min-h-0 overflow-y-auto custom-scroll p-5">
      <!-- 空状态 -->
      <div
        v-if="appStore.filteredAccounts.length === 0"
        class="h-full min-h-[340px] flex flex-col items-center justify-center text-center px-6"
      >
        <div
          class="w-12 h-12 rounded-2xl flex items-center justify-center mb-3.5"
          style="background-color: var(--bg-subtle); color: var(--text-muted)"
        >
          <UserPlus class="w-6 h-6" />
        </div>
        <h2 class="text-sm font-semibold" style="color: var(--text-primary)">
          {{ appStore.accounts.length === 0 ? '还没有添加反重力账号' : '当前条件下暂无匹配账号' }}
        </h2>
        <p class="mt-1.5 text-xs max-w-sm leading-relaxed" style="color: var(--text-muted)">
          {{
            appStore.accounts.length === 0
              ? '支持 Google OAuth 授权登录、一键读取本机已登录的反重力账号，或导入 Refresh Token / JSON。'
              : '您可以尝试清空搜索关键词、切换分组，或重置右侧筛选条件。'
          }}
        </p>
        <div class="mt-4 flex items-center gap-2.5">
          <button
            v-if="appStore.accounts.length === 0"
            type="button"
            class="inline-flex items-center gap-1.5 px-4 py-2 rounded-xl text-xs font-medium text-white cursor-pointer"
            style="background-color: var(--accent)"
            @click="emit('open-add')"
          >
            <Plus class="w-3.5 h-3.5" />
            添加首个账号
          </button>
          <button
            v-else
            type="button"
            class="px-3.5 py-1.5 rounded-xl border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)]"
            style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
            @click="
              appStore.searchQuery = '';
              appStore.clearAdvancedFilters();
              appStore.selectedGroupFilter = 'all';
            "
          >
            查看全部账号
          </button>
        </div>
      </div>

      <!-- 卡片网格视图：基于容器自然宽度的自适应网格，不受悬浮详情抽屉影响 -->
      <div
        v-else-if="appStore.viewMode === 'cards'"
        class="grid gap-4 grid-cols-[repeat(auto-fill,minmax(280px,1fr))]"
      >
        <AccountCard
          v-for="acc in appStore.filteredAccounts"
          :key="acc.id"
          :account="acc"
          :selected="appStore.selectedAccountIds.includes(acc.id)"
          :active-detail="appStore.activeDetailAccountId === acc.id"
          :reorderable="canReorder"
          :dragging="draggingAccountId === acc.id"
          :drag-over="dragOverAccountId === acc.id"
          @open-detail="handleOpenDetail"
          @toggle-select="appStore.toggleSelectAccount"
          @drag-start="handleDragStart"
          @drag-over="handleDragOver"
          @drop="handleDrop"
          @drag-end="handleDragEnd"
        />
      </div>

      <!-- 紧凑表格视图 -->
      <AccountTable
        v-else
        :accounts="appStore.filteredAccounts"
        :reorderable="canReorder"
        :dragging-id="draggingAccountId"
        :drag-over-id="dragOverAccountId"
        @open-detail="handleOpenDetail"
        @toggle-select="appStore.toggleSelectAccount"
        @toggle-select-all="toggleSelectAllVisible"
        @drag-start="handleDragStart"
        @drag-over="handleDragOver"
        @drop="handleDrop"
        @drag-end="handleDragEnd"
      />
    </div>
  </section>
</template>
