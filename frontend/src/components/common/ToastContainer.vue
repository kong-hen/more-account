<script setup lang="ts">
// 右下角轻量非阻塞 Toast 提示队列，全套使用 Lucide 矢量图标。
import { CheckCircle2, AlertTriangle, XCircle, Info, X } from 'lucide-vue-next'
import { useAppStore } from '@/stores/appStore'

const appStore = useAppStore()
</script>

<template>
  <div
    class="fixed bottom-4 right-4 z-50 flex flex-col gap-2 pointer-events-none max-w-[380px]"
    aria-live="polite"
  >
    <TransitionGroup name="toast">
      <div
        v-for="item in appStore.toasts"
        :key="item.id"
        class="pointer-events-auto flex items-start gap-2.5 px-3.5 py-2.5 rounded-xl border text-xs leading-relaxed"
        style="background-color: var(--bg-surface); border-color: var(--border-subtle); box-shadow: var(--shadow-elevated); color: var(--text-primary)"
      >
        <CheckCircle2 v-if="item.type === 'success'" class="w-4 h-4 shrink-0 mt-0.5" style="color: var(--success)" />
        <AlertTriangle v-else-if="item.type === 'warning'" class="w-4 h-4 shrink-0 mt-0.5" style="color: var(--warning)" />
        <XCircle v-else-if="item.type === 'error'" class="w-4 h-4 shrink-0 mt-0.5" style="color: var(--danger)" />
        <Info v-else class="w-4 h-4 shrink-0 mt-0.5" style="color: var(--accent)" />

        <span class="flex-1 break-words">{{ item.message }}</span>

        <button
          type="button"
          aria-label="关闭通知"
          class="p-0.5 rounded cursor-pointer hover:bg-[var(--bg-hover)]"
          style="color: var(--text-muted)"
          @click="appStore.dismissToast(item.id)"
        >
          <X class="w-3.5 h-3.5" />
        </button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: opacity 0.18s ease, transform 0.18s ease;
}
.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translateY(8px);
}
</style>
