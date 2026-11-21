<script setup lang="ts">
// 账号工作台主视图：宽松单行顶栏（搜索 + 聚合筛选下拉含可选低配额/异常 + 排序 + 双视图切换 + 刷新 + 添加）、
// 浮现式多选批量栏、高舒适度卡片网格 / 紧凑表格双视图。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
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
import { useAppStore, type AccountSortBy } from '@/stores/appStore'
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
    appStore.activeDetailAccountId = null
  } else {
    appStore.activeDetailAccountId = account.id
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
  await appStore.assignAccountsToGroup(appStore.selectedAccountIds, batchGroupTarget.value)
}

function setSort(val: string) {
  appStore.sortBy = val as AccountSortBy
}
</script>

<template>
  <section class="flex-1 min-w-0 h-full flex flex-col overflow-hidden">
    <!-- 顶部宽松单行操作栏（含 macOS 拖拽兼容区） -->
    <header
      class="wails-drag shrink-0 px-6 pt-5 pb-4 border-b flex items-center justify-between gap-4"
      style="border-color: var(--border-subtle); background-color: var(--bg-app)"
    >
      <div class="wails-no-drag flex items-center gap-3 min-w-0">
        <h1 class="text-base font-semibold tracking-tight truncate" style="color: var(--text-primary)">
          {{ currentGroupTitle }}
        </h1>
        <span
          class="px-2 py-0.5 rounded-full text-xs font-mono-num"
          style="background-color: var(--bg-subtle); color: var(--text-muted)"
        >
          {{ appStore.filteredAccounts.length }}
        </span>
      </div>

      <div class="wails-no-drag flex items-center gap-2.5">
        <!-- 紧凑搜索框 -->
        <div class="relative w-52">
          <Search
            class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none"
            style="color: var(--text-muted)"
          />
          <input
            v-model="appStore.searchQuery"
            type="search"
            placeholder="搜索邮箱、备注或分组..."
            class="w-full pl-8 pr-3 py-1.5 text-xs rounded-lg border"
            style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>

        <!-- 聚合筛选下拉：订阅等级 + 可选「仅看低配额」与「仅看异常」，保持顶栏不拥挤 -->
        <div ref="filterWrapRef" class="relative">
          <button
            type="button"
            class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
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
              class="px-1.5 py-0.2 rounded-full text-[10.5px] font-semibold text-white tabular-nums"
              style="background-color: var(--accent)"
            >
              {{ appStore.activeFilterCount }}
            </span>
          </button>

          <div
            v-if="filterMenuOpen"
            class="absolute right-0 mt-2 w-64 p-3.5 rounded-2xl border z-30 space-y-3.5"
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
        <select
          aria-label="排序方式"
          :value="appStore.sortBy"
          class="px-2.5 py-1.5 text-xs rounded-lg border cursor-pointer"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-secondary)"
          @change="setSort(($event.target as HTMLSelectElement).value)"
        >
          <option value="custom">默认排序</option>
          <option value="quota_desc">配额从高到低</option>
          <option value="quota_asc">配额从低到高</option>
          <option value="last_used">最近使用</option>
          <option value="email">按邮箱名称</option>
        </select>

        <!-- 视图切换：卡片 / 表格 -->
        <div
          class="inline-flex p-0.5 rounded-lg border"
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

        <!-- 刷新全部 -->
        <button
          type="button"
          :disabled="appStore.refreshingAll || appStore.accounts.length === 0"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="appStore.refreshAllAccounts"
        >
          <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': appStore.refreshingAll }" />
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
          class="inline-flex items-center gap-1.5 px-3.5 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer transition-colors"
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
      class="shrink-0 px-6 py-2 border-b flex items-center gap-2 text-xs"
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
      class="shrink-0 px-6 py-2.5 border-b flex items-center justify-between gap-3 text-xs"
      style="border-color: var(--accent-border); background-color: var(--accent-soft)"
    >
      <div class="flex items-center gap-2.5">
        <span class="font-medium" style="color: var(--accent)">
          已选中 {{ appStore.selectedAccountIds.length }} 个账号
        </span>
        <button
          type="button"
          class="text-[11.5px] cursor-pointer hover:underline"
          style="color: var(--text-secondary)"
          @click="toggleSelectAllVisible"
        >
          全选当前列表
        </button>
      </div>

      <div class="flex items-center gap-2">
        <div class="flex items-center gap-1.5">
          <select
            v-model="batchGroupTarget"
            aria-label="选择目标分组"
            class="px-2 py-1 text-xs rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          >
            <option value="">移至未分组</option>
            <option v-for="g in appStore.groups" :key="g.id" :value="g.id">
              移至 {{ g.name }}
            </option>
          </select>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)]"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            @click="applyBatchGroup"
          >
            <FolderInput class="w-3.5 h-3.5" />
            归组
          </button>
        </div>

        <button
          type="button"
          class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)]"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="emit('open-export', [...appStore.selectedAccountIds])"
        >
          <Download class="w-3.5 h-3.5" />
          导出
        </button>

        <button
          type="button"
          class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg text-xs font-medium text-white cursor-pointer"
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
    <div class="flex-1 overflow-y-auto custom-scroll p-6">
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

      <!-- 卡片网格视图 -->
      <div
        v-else-if="appStore.viewMode === 'cards'"
        class="grid gap-4"
        :class="
          appStore.activeDetailAccount
            ? 'grid-cols-1 xl:grid-cols-2'
            : 'grid-cols-1 md:grid-cols-2 xl:grid-cols-3'
        "
      >
        <AccountCard
          v-for="acc in appStore.filteredAccounts"
          :key="acc.id"
          :account="acc"
          :selected="appStore.selectedAccountIds.includes(acc.id)"
          :active-detail="appStore.activeDetailAccountId === acc.id"
          @open-detail="handleOpenDetail"
          @toggle-select="appStore.toggleSelectAccount"
        />
      </div>

      <!-- 紧凑表格视图 -->
      <AccountTable
        v-else
        :accounts="appStore.filteredAccounts"
        @open-detail="handleOpenDetail"
        @toggle-select="appStore.toggleSelectAccount"
        @toggle-select-all="toggleSelectAllVisible"
      />
    </div>
  </section>
</template>
