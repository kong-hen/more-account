<script setup lang="ts">
// 响应式 Flexbox Token 堆叠趋势柱状图，支持悬浮查看精确输入 / 输出 / Thinking / 缓存与估算分布。
import { computed, ref } from 'vue'
import { formatExactNumber, formatTokenCount } from '@/utils/quota'
import type { UsageTrendBucket } from '@/types/models'

const props = defineProps<{
  buckets: UsageTrendBucket[]
}>()

const hoveredIndex = ref<number | null>(null)

const visibleBuckets = computed(() => {
  // 最多优雅展示最近 24 个时间桶，保证柱宽清晰不拥挤
  if (props.buckets.length <= 24) return props.buckets
  return props.buckets.slice(-24)
})

// 仅基于当前展示的最近 24 桶计算高度上限，避免更早的历史极端峰值把近期柱子压扁
const maxTotal = computed(() => {
  let max = 0
  for (const b of visibleBuckets.value) {
    const t = b.exact.totalTokens + b.estimatedTotalTokens
    if (t > max) max = t
  }
  return max > 0 ? max : 1
})

const hasAnyEstimated = computed(() =>
  visibleBuckets.value.some((b) => b.estimatedTotalTokens > 0),
)

// 预计算每根柱体的各分段高度百分比，避免模板渲染期对同一桶重复计算
const chartBars = computed(() =>
  visibleBuckets.value.map((b) => {
    const total = b.exact.totalTokens + b.estimatedTotalTokens
    if (total === 0) {
      return {
        bucket: b,
        totalHeightPct: 0,
        inputPct: 0,
        outputPct: 0,
        thinkingPct: 0,
        cachePct: 0,
        estimatedPct: 0,
      }
    }
    const input = b.exact.inputTokens
    const output = b.exact.outputTokens
    const thinking = b.exact.thinkingTokens
    const cache = b.exact.cacheReadTokens + b.exact.cacheWriteTokens
    const estimated = b.estimatedTotalTokens
    return {
      bucket: b,
      totalHeightPct: Math.max(2, (total / maxTotal.value) * 100),
      inputPct: (input / total) * 100,
      outputPct: (output / total) * 100,
      thinkingPct: (thinking / total) * 100,
      cachePct: (cache / total) * 100,
      estimatedPct: (estimated / total) * 100,
    }
  }),
)

const hoveredBucket = computed(() => {
  if (hoveredIndex.value === null) return null
  return visibleBuckets.value[hoveredIndex.value] ?? null
})
</script>

<template>
  <div
    class="p-4 rounded-2xl border space-y-3"
    style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
  >
    <div class="flex items-center justify-between gap-3 flex-wrap">
      <div>
        <h3 class="text-xs font-semibold" style="color: var(--text-primary)">Token 消耗趋势</h3>
        <p class="text-[11px] mt-0.5" style="color: var(--text-muted)">
          悬浮柱体可查看精确构成（Thinking 与纯输出分离计数）
        </p>
      </div>

      <!-- 图例 -->
      <div class="flex items-center gap-3 text-[11px]" style="color: var(--text-muted)">
        <span class="inline-flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded-sm" style="background-color: var(--accent)" />输入
        </span>
        <span class="inline-flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded-sm" style="background-color: var(--success)" />输出
        </span>
        <span class="inline-flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded-sm" style="background-color: #8b5cf6" />Thinking
        </span>
        <span class="inline-flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded-sm" style="background-color: var(--warning)" />缓存
        </span>
        <span v-if="hasAnyEstimated" class="inline-flex items-center gap-1.5">
          <span class="w-2.5 h-2.5 rounded-sm" style="background-color: var(--text-muted)" />估算
        </span>
      </div>
    </div>

    <div v-if="chartBars.length === 0" class="h-44 flex items-center justify-center text-xs" style="color: var(--text-muted)">
      所选时间范围内暂无用量记录
    </div>

    <div v-else class="relative pt-2">
      <!-- 悬浮明细提示条 -->
      <div
        class="min-h-[26px] mb-2 flex items-center justify-between flex-wrap gap-x-3 gap-y-1 text-[11.5px] px-2.5 py-1 rounded-lg"
        style="background-color: var(--bg-app); color: var(--text-secondary)"
      >
        <template v-if="hoveredBucket">
          <span class="font-medium font-mono-num" style="color: var(--text-primary)">
            {{ hoveredBucket.key }} · {{ hoveredBucket.conversationCount }} 对话 / {{ hoveredBucket.turnCount }} 轮
          </span>
          <span class="font-mono-num flex items-center flex-wrap gap-x-3 gap-y-0.5">
            <span>总计 {{ formatExactNumber(hoveredBucket.exact.totalTokens + hoveredBucket.estimatedTotalTokens) }}</span>
            <span>输入 {{ formatTokenCount(hoveredBucket.exact.inputTokens) }}</span>
            <span>输出 {{ formatTokenCount(hoveredBucket.exact.outputTokens) }}</span>
            <span>思考 {{ formatTokenCount(hoveredBucket.exact.thinkingTokens) }}</span>
            <span>缓存 {{ formatTokenCount(hoveredBucket.exact.cacheReadTokens + hoveredBucket.exact.cacheWriteTokens) }}</span>
            <span v-if="hoveredBucket.estimatedTotalTokens > 0">估算 {{ formatTokenCount(hoveredBucket.estimatedTotalTokens) }}</span>
          </span>
        </template>
        <span v-else style="color: var(--text-muted)">
          将鼠标移动至柱状图查看单日消耗详情
        </span>
      </div>

      <!-- 堆叠柱状区 -->
      <div class="h-44 flex items-end gap-1.5 px-1">
        <div
          v-for="(bar, idx) in chartBars"
          :key="bar.bucket.key"
          class="flex-1 h-full flex flex-col items-center justify-end gap-1.5 group cursor-pointer"
          @mouseenter="hoveredIndex = idx"
          @mouseleave="hoveredIndex = null"
        >
          <div class="w-full flex-1 flex items-end justify-center">
            <div
              class="w-full max-w-[28px] rounded-t-md overflow-hidden flex flex-col-reverse transition-opacity"
              :style="{
                height: `${bar.totalHeightPct}%`,
                opacity: hoveredIndex === null || hoveredIndex === idx ? 1 : 0.45,
              }"
            >
              <div v-if="bar.inputPct > 0" :style="{ height: `${bar.inputPct}%`, backgroundColor: 'var(--accent)' }" />
              <div v-if="bar.outputPct > 0" :style="{ height: `${bar.outputPct}%`, backgroundColor: 'var(--success)' }" />
              <div v-if="bar.thinkingPct > 0" :style="{ height: `${bar.thinkingPct}%`, backgroundColor: '#8b5cf6' }" />
              <div v-if="bar.cachePct > 0" :style="{ height: `${bar.cachePct}%`, backgroundColor: 'var(--warning)' }" />
              <div v-if="bar.estimatedPct > 0" :style="{ height: `${bar.estimatedPct}%`, backgroundColor: 'var(--text-muted)' }" />
            </div>
          </div>

          <span
            class="text-[10px] font-mono-num truncate max-w-full"
            style="color: var(--text-muted)"
          >
            {{ bar.bucket.label }}
          </span>
        </div>
      </div>
    </div>
  </div>
</template>
