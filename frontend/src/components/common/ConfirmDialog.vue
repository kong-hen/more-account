<script setup lang="ts">
// 通用破坏性操作二次确认对话框，默认将焦点放在安全取消按钮上，提交中禁止误关。
import BaseModal from './BaseModal.vue'

withDefaults(
  defineProps<{
    open: boolean
    title: string
    message: string
    confirmText?: string
    danger?: boolean
    loading?: boolean
  }>(),
  {
    confirmText: '确认',
    danger: true,
    loading: false,
  },
)

const emit = defineEmits<{
  (e: 'cancel'): void
  (e: 'confirm'): void
}>()
</script>

<template>
  <BaseModal
    :open="open"
    :title="title"
    width-class="w-[420px]"
    :close-on-outside="!loading"
    :prevent-close="loading"
    @close="emit('cancel')"
  >
    <p class="text-sm leading-relaxed" style="color: var(--text-secondary)">
      {{ message }}
    </p>
    <template #footer>
      <button
        type="button"
        autofocus
        :disabled="loading"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="emit('cancel')"
      >
        取消
      </button>
      <button
        type="button"
        :disabled="loading"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer transition-opacity disabled:opacity-60"
        :style="{ backgroundColor: danger ? 'var(--danger)' : 'var(--accent)' }"
        @click="emit('confirm')"
      >
        {{ loading ? '处理中...' : confirmText }}
      </button>
    </template>
  </BaseModal>
</template>
