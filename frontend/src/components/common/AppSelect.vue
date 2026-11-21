<script setup lang="ts">
// 统一风格下拉选择框：基于原生 <select>（保证在 <dialog> 内不被裁切且支持键盘导航），
// 通过 appearance: none 彻底消除 macOS WKWebView 原生 Aqua 样式错位，并搭配 Lucide 下拉箭头图标。
import { ChevronDown } from 'lucide-vue-next'

export interface SelectOption {
  value: string
  label: string
}

withDefaults(
  defineProps<{
    modelValue: string
    options: SelectOption[]
    id?: string
    ariaLabel?: string
    size?: 'sm' | 'md'
    disabled?: boolean
    block?: boolean
  }>(),
  {
    id: undefined,
    ariaLabel: undefined,
    size: 'md',
    disabled: false,
    block: false,
  },
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
}>()

function onChange(e: Event) {
  const selectEl = e.target as HTMLSelectElement
  emit('update:modelValue', selectEl.value)
  // 鼠标点选完成后主动释放原生 <select> 焦点，消除移开鼠标后残留的外层选中态并避免误触滚轮/方向键改值
  if (document.documentElement.dataset.focusSource !== 'keyboard') {
    selectEl.blur()
  }
}
</script>

<template>
  <div
    class="app-select-wrap relative items-center rounded-lg"
    :class="[
      block ? 'flex w-full' : 'inline-flex',
      size === 'sm' ? 'h-7' : 'h-8',
      disabled ? 'opacity-50 pointer-events-none' : '',
    ]"
  >
    <select
      :id="id"
      :aria-label="ariaLabel"
      :value="modelValue"
      :disabled="disabled"
      class="app-select-native w-full h-full pl-2.5 pr-7 text-xs font-medium cursor-pointer truncate"
      style="color: var(--text-primary)"
      @change="onChange"
    >
      <option v-for="opt in options" :key="opt.value" :value="opt.value">
        {{ opt.label }}
      </option>
    </select>
    <ChevronDown
      class="w-3.5 h-3.5 absolute right-2 top-1/2 -translate-y-1/2 pointer-events-none shrink-0"
      style="color: var(--text-muted)"
    />
  </div>
</template>
