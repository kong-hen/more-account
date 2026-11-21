<script setup lang="ts">
// Token 用量统计主视图：严格只读扫描本地 ~/.gemini/antigravity，展示 KPI 卡、SVG 趋势图、模型/工作区分布及分页对话表。
import { computed, onMounted } from 'vue'
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
} from 'lucide-vue-next'
import UsageTrendChart from '@/components/tokenusage/UsageTrendChart.vue'
import ConversationDetailDrawer from '@/components/tokenusage/ConversationDetailDrawer.vue'
import AppSelect from '@/components/common/AppSelect.vue'
import { useTokenUsageStore } from '@/stores/tokenUsageStore'
import { formatDateTime, formatExactNumber, formatTokenCount } from '@/utils/quota'
import type { TokenUsageQuery } from '@/types/models'

const usageStore = useTokenUsageStore()

const presets: Array<{ id: TokenUsageQuery['preset']; label: string }> = [
  { id: 'today', label: '今日' },
  { id: '7d', label: '近 7 天' },
  { id: '30d', label: '近 30 天' },
  { id: 'month', label: '本月' },
  { id: 'all', label: '全部' },
]

const overview = computed(() => usageStore.bundle?.overview ?? null)
const conversations = computed(() => usageStore.bundle?.conversations ?? null)
const totalAllTokens = computed(() => {
  if (!overview.value) return 0
  return overview.value.exact.totalTokens + overview.value.estimated.totalTokens
})

onMounted(() => {
  if (!usageStore.bundle) {
    void usageStore.fetchQuery()
  }
})

function setPreset(preset: TokenUsageQuery['preset']) {
  void usageStore.fetchQuery({ preset, page: 1 })
}

const kindOptions = [
  { value: '', label: '全部类型' },
  { value: 'main', label: '主对话' },
  { value: 'subagent', label: 'Subagent' },
  { value: 'swarm', label: 'Swarm' },
]

function setKind(kind: string) {
  void usageStore.fetchQuery({ kind, page: 1 })
}

function setSearch(val: string) {
  void usageStore.fetchQuery({ search: val, page: 1 })
}

function goPage(next: number) {
  if (!conversations.value) return
  if (next < 1 || next > conversations.value.totalPages) return
  void usageStore.fetchQuery({ page: next })
}

function pctShare(part: number, whole: number): number {
  if (!whole || whole <= 0) return 0
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
          <h1 class="text-base font-semibold tracking-tight whitespace-nowrap shrink-0" style="color: var(--text-primary)">
            Token 用量统计
          </h1>
          <span
            v-if="overview"
            class="text-[11.5px] font-mono-num truncate min-w-0"
            style="color: var(--text-muted)"
          >
            {{ overview.conversationCount }} 对话 · {{ overview.turnCount }} 轮
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
          v-if="usageStore.bundle && !usageStore.bundle.settings.dataDirExists"
          class="p-4 rounded-2xl border flex items-center gap-3 text-xs"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-secondary)"
        >
          <FolderOpen class="w-5 h-5 shrink-0" style="color: var(--text-muted)" />
          <div>
            未检测到本地反重力会话目录
            <code class="font-mono-num">{{ usageStore.bundle.settings.effectiveDataDir }}</code>，可在左下角「设置」中自定义扫描目录。
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

        <!-- 趋势柱状图 -->
        <UsageTrendChart :buckets="overview?.trend ?? []" />

        <!-- 模型分布 + 工作区分布 -->
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <div
            class="p-4 rounded-2xl border space-y-3"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <h3 class="text-xs font-semibold" style="color: var(--text-primary)">模型消耗排行</h3>
            <div v-if="overview?.models?.length" class="space-y-2.5">
              <div v-for="m in overview.models.slice(0, 6)" :key="m.modelId" class="space-y-1">
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
              </div>
            </div>
            <p v-else class="text-xs py-6 text-center" style="color: var(--text-muted)">暂无模型统计数据</p>
          </div>

          <div
            class="p-4 rounded-2xl border space-y-3"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <h3 class="text-xs font-semibold" style="color: var(--text-primary)">工作区消耗排行</h3>
            <div v-if="overview?.workspaces?.length" class="space-y-2.5">
              <div v-for="ws in overview.workspaces.slice(0, 6)" :key="ws.workspaceName" class="space-y-1">
                <div class="flex items-center justify-between text-xs gap-2">
                  <span class="truncate font-medium" style="color: var(--text-primary)">{{ ws.workspaceName }}</span>
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
              </div>
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
            <div class="flex items-center gap-2">
              <MessageSquare class="w-4 h-4" style="color: var(--text-muted)" />
              <h3 class="text-xs font-semibold" style="color: var(--text-primary)">对话消耗明细</h3>
            </div>

            <div class="flex items-center flex-wrap gap-2">
              <div class="relative w-48 max-w-full">
                <Search
                  class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none"
                  style="color: var(--text-muted)"
                />
                <input
                  :value="usageStore.query.search"
                  type="search"
                  placeholder="搜索对话标题或工作区..."
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
            </div>
          </div>

          <div class="overflow-x-auto custom-scroll">
            <table class="w-full text-left border-collapse text-xs">
              <thead>
                <tr
                  class="border-b text-[11px]"
                  style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-muted)"
                >
                  <th class="py-2.5 px-4 whitespace-nowrap">对话</th>
                  <th class="py-2.5 px-3 whitespace-nowrap">类型</th>
                  <th class="py-2.5 px-3 whitespace-nowrap">工作区</th>
                  <th class="py-2.5 px-3 whitespace-nowrap">主力模型</th>
                  <th class="py-2.5 px-3 text-right whitespace-nowrap">总消耗</th>
                  <th class="py-2.5 px-4 text-right whitespace-nowrap">最近活动</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-[var(--border-hairline)]">
                <tr
                  v-for="item in conversations?.items ?? []"
                  :key="item.conversationId"
                  class="cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
                  @click="usageStore.openConversationDetail(item.conversationId)"
                >
                  <td class="py-2.5 px-4 max-w-[280px]">
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
                  <td class="py-2.5 px-3 text-right font-mono-num font-semibold whitespace-nowrap" style="color: var(--text-primary)">
                    {{ formatTokenCount(item.exact.totalTokens + item.estimated.totalTokens) }}
                  </td>
                  <td class="py-2.5 px-4 text-right font-mono-num text-[11px] whitespace-nowrap" style="color: var(--text-muted)">
                    {{ formatDateTime(item.lastModifiedTime) }}
                  </td>
                </tr>
                <tr v-if="!conversations?.items?.length">
                  <td colspan="6" class="py-10 text-center text-xs" style="color: var(--text-muted)">
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
                :disabled="conversations.page <= 1"
                class="p-1 rounded-lg border cursor-pointer disabled:opacity-40"
                style="border-color: var(--border-strong); background-color: var(--bg-surface)"
                @click="goPage(conversations.page - 1)"
              >
                <ChevronLeft class="w-3.5 h-3.5" />
              </button>
              <button
                type="button"
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
