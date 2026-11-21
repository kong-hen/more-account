<script setup lang="ts">
// 轻量配额健康度进度条组件，根据剩余水位自动切换翡翠绿 / 琥珀橙 / 珊瑚红语义色。
import { computed } from 'vue'
import { getQuotaTone } from '@/utils/quota'

const props = withDefaults(
  defineProps<{
    percentage: number
    height?: string
  }>(),
  {
    height: 'h-1.5',
  },
)

const clamped = computed(() => Math.max(0, Math.min(100, Math.round(props.percentage))))

const barColor = computed(() => {
  const tone = getQuotaTone(clamped.value)
  if (tone === 'success') return 'var(--success)'
  if (tone === 'warning') return 'var(--warning)'
  return 'var(--danger)'
})
</script>

<template>
  <div
    class="w-full rounded-full overflow-hidden"
    :class="height"
    style="background-color: var(--bg-subtle)"
  >
    <div
      class="h-full rounded-full transition-all duration-300"
      :style="{ width: `${clamped}%`, backgroundColor: barColor }"
    />
  </div>
</template>
