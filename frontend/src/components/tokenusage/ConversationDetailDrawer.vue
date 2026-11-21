<script setup lang="ts">
// 右侧滑出的单对话步级 Token 消耗详情抽屉。
import { computed } from 'vue'
import { X, Loader2, Folder, Cpu } from 'lucide-vue-next'
import { formatDateTime, formatExactNumber, formatTokenCount } from '@/utils/quota'
import type { ConversationDetail } from '@/types/models'

const props = defineProps<{
  open: boolean
  loading: boolean
  detail: ConversationDetail | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const safeTurns = computed(() => props.detail?.turns ?? [])
const safeBreakdown = computed(() => props.detail?.modelBreakdown ?? [])

function formatProviderLabel(raw?: string): string {
  const clean = (raw ?? '').trim()
  if (!clean) return ''
  const upper = clean.toUpperCase()
  if (upper.includes('ANTHROPIC')) return 'Anthropic'
  if (upper.includes('GOOGLE') || upper.includes('VERTEX') || upper.includes('GEMINI')) return 'Google'
  if (upper.includes('OPENAI')) return 'OpenAI'
  return clean
}

function formatStepStatus(status?: number): { label: string; isError: boolean } | null {
  if (!status || status === 1 || status === 3) return null
  if (status === 4 || status === 5) return { label: '异常/中断', isError: true }
  return { label: `状态 ${status}`, isError: false }
}
</script>

<template>
  <!-- 悬浮右侧滑出对话明细抽屉，不挤压 Token 用量主页面 -->
  <Transition name="drawer-fade">
    <div
      v-if="open"
      class="fixed inset-0 z-30 drawer-backdrop"
      aria-hidden="true"
      @click="emit('close')"
    />
  </Transition>

  <Transition name="drawer-slide">
    <aside
      v-if="open"
      class="fixed top-0 right-0 z-40 h-full w-[min(420px,calc(100vw-48px))] border-l flex flex-col overflow-hidden"
      style="background-color: var(--bg-surface); border-color: var(--border-subtle); box-shadow: var(--shadow-elevated)"
      aria-label="对话 Token 明细抽屉"
    >
      <header
        class="shrink-0 px-5 pt-5 pb-4 border-b flex items-start justify-between gap-3"
        style="border-color: var(--border-subtle)"
      >
        <div class="min-w-0">
          <h2 class="text-sm font-semibold truncate" style="color: var(--text-primary)">
            {{ detail?.summary.displayTitle || '加载对话详情...' }}
          </h2>
          <div v-if="detail" class="mt-1 flex items-center gap-2 text-[11px]" style="color: var(--text-muted)">
            <span class="inline-flex items-center gap-1 truncate">
              <Folder class="w-3 h-3" />
              {{ detail.summary.workspaceName }}
            </span>
            <span>·</span>
            <span class="font-mono-num">{{ safeTurns.length }} 步</span>
          </div>
        </div>

        <button
          type="button"
          aria-label="关闭对话详情"
          class="p-1.5 rounded-lg shrink-0 cursor-pointer hover:bg-[var(--bg-hover)]"
          style="color: var(--text-muted)"
          @click="emit('close')"
        >
          <X class="w-4 h-4" />
        </button>
      </header>

      <div v-if="loading" class="flex-1 min-h-0 flex items-center justify-center gap-2 text-xs" style="color: var(--text-muted)">
        <Loader2 class="w-4 h-4 animate-spin" />
        <span>正在只读解析对话步骤...</span>
      </div>

      <div v-else-if="detail" class="flex-1 min-h-0 overflow-y-auto custom-scroll px-5 py-4 space-y-5">
        <!-- 模型分布切片 -->
        <section class="space-y-2">
          <h3 class="text-xs font-semibold" style="color: var(--text-primary)">模型消耗构成</h3>
          <div v-if="safeBreakdown.length" class="space-y-2">
            <div
              v-for="sl in safeBreakdown"
              :key="sl.modelId"
              class="p-3 rounded-xl border space-y-1.5"
              style="border-color: var(--border-subtle); background-color: var(--bg-app)"
            >
              <div class="flex items-center justify-between gap-2 text-xs">
                <span class="inline-flex items-center gap-1.5 font-medium truncate min-w-0" style="color: var(--text-primary)">
                  <Cpu class="w-3.5 h-3.5 shrink-0" style="color: var(--accent)" />
                  <span class="truncate">{{ sl.modelDisplayName }}</span>
                  <span
                    v-if="formatProviderLabel(sl.apiProvider)"
                    class="px-1.5 py-0.5 rounded text-[10px] font-normal shrink-0"
                    style="background-color: var(--bg-subtle); color: var(--text-muted)"
                  >
                    {{ formatProviderLabel(sl.apiProvider) }}
                  </span>
                </span>
                <span class="font-mono-num font-semibold shrink-0" style="color: var(--text-primary)">
                  {{ formatTokenCount(sl.exact.totalTokens + sl.estimatedTotalTokens) }}
                </span>
              </div>
              <div class="grid grid-cols-2 gap-1 text-[11px] font-mono-num" style="color: var(--text-muted)">
                <span>输入：{{ formatTokenCount(sl.exact.inputTokens) }}</span>
                <span>输出：{{ formatTokenCount(sl.exact.outputTokens) }}</span>
                <span>Thinking：{{ formatTokenCount(sl.exact.thinkingTokens) }}</span>
                <span>缓存：{{ formatTokenCount(sl.exact.cacheReadTokens + sl.exact.cacheWriteTokens) }}</span>
              </div>
            </div>
          </div>
          <p v-else class="text-xs py-3 text-center" style="color: var(--text-muted)">暂无模型切片记录</p>
        </section>

        <!-- 步级消耗记录 -->
        <section class="space-y-2">
          <h3 class="text-xs font-semibold" style="color: var(--text-primary)">
            步级明细（{{ safeTurns.length }}）
          </h3>
          <div v-if="safeTurns.length" class="space-y-2">
            <div
              v-for="turn in safeTurns"
              :key="`${turn.stepIndex}-${turn.messageId || ''}`"
              class="p-2.5 rounded-xl border text-[11.5px] space-y-1"
              style="border-color: var(--border-subtle); background-color: var(--bg-app)"
            >
              <div class="flex items-center justify-between gap-2">
                <span class="inline-flex items-center gap-1.5 font-medium truncate min-w-0" style="color: var(--text-primary)">
                  <span class="truncate">#{{ turn.stepIndex }} · {{ turn.modelDisplayName }}</span>
                  <span
                    v-if="formatProviderLabel(turn.apiProvider)"
                    class="px-1.5 py-0.5 rounded text-[9.5px] font-normal shrink-0"
                    style="background-color: var(--bg-subtle); color: var(--text-muted)"
                  >
                    {{ formatProviderLabel(turn.apiProvider) }}
                  </span>
                  <span
                    v-if="formatStepStatus(turn.status)"
                    class="px-1.5 py-0.5 rounded text-[9.5px] font-medium shrink-0"
                    :style="
                      formatStepStatus(turn.status)?.isError
                        ? { backgroundColor: 'var(--danger-soft)', color: 'var(--danger-text)' }
                        : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-muted)' }
                    "
                  >
                    {{ formatStepStatus(turn.status)?.label }}
                  </span>
                </span>
                <span
                  class="font-mono-num font-semibold shrink-0"
                  :title="formatExactNumber(turn.isExact ? turn.exact.totalTokens : turn.estimated.totalTokens)"
                  style="color: var(--text-primary)"
                >
                  {{ formatTokenCount(turn.isExact ? turn.exact.totalTokens : turn.estimated.totalTokens) }}
                </span>
              </div>

              <div class="flex items-center justify-between flex-wrap gap-x-2 gap-y-0.5 text-[11px] font-mono-num" style="color: var(--text-muted)">
                <span v-if="turn.isExact">
                  入 {{ formatTokenCount(turn.exact.inputTokens) }} · 出 {{ formatTokenCount(turn.exact.outputTokens) }} · 思 {{ formatTokenCount(turn.exact.thinkingTokens) }} · 缓 {{ formatTokenCount(turn.exact.cacheReadTokens + turn.exact.cacheWriteTokens) }}
                </span>
                <span v-else>启发式估算</span>
                <span class="shrink-0">{{ formatDateTime(turn.timestamp) }}</span>
              </div>
            </div>
          </div>
          <p v-else class="text-xs py-4 text-center" style="color: var(--text-muted)">该会话暂无有效推理步骤记录</p>
        </section>
      </div>
    </aside>
  </Transition>
</template>
