<script setup lang="ts">
// 右下角轻量非阻塞 Toast 提示队列：使用原生 popover="manual" 提升至 Top Layer（永不被 <dialog> 遮挡），
// 支持悬停/键盘聚焦暂停自动消失，并对不支持 Popover API 的旧环境优雅降级。
import { nextTick, onMounted, ref, watch } from 'vue'
import { CheckCircle2, AlertTriangle, XCircle, Info, X } from 'lucide-vue-next'
import { useAppStore } from '@/stores/appStore'

const appStore = useAppStore()
const hostRef = ref<HTMLElement | null>(null)
const supportsPopover =
  typeof HTMLElement !== 'undefined' && Object.hasOwn(HTMLElement.prototype, 'popover')

function syncTopLayer(elevate: boolean) {
  const el = hostRef.value
  if (!el || !supportsPopover) return
  try {
    const hasItems = appStore.toasts.length > 0
    const isOpen = el.matches(':popover-open')
    if (hasItems) {
      // 有新通知时先 hide 再 show，保证始终置顶在最新打开的模态 <dialog> 上方
      if (isOpen && elevate) {
        el.hidePopover()
      }
      if (!el.matches(':popover-open')) {
        el.showPopover()
      }
    } else if (isOpen) {
      el.hidePopover()
    }
  } catch {
    // 降级为常规 fixed 定位，不影响展示
  }
}

function announceLatest(message: string) {
  const doc = document as Document & { ariaNotify?: (text: string) => void }
  if (typeof doc.ariaNotify === 'function') {
    doc.ariaNotify(message)
  }
}

watch(
  () => appStore.toasts.map((t) => t.id),
  async (nextIds, prevIds) => {
    const prevSet = new Set(prevIds ?? [])
    const added = nextIds.filter((id) => !prevSet.has(id))
    await nextTick()
    syncTopLayer(added.length > 0)
    if (added.length > 0) {
      const latest = appStore.toasts.find((t) => t.id === added[added.length - 1])
      if (latest) {
        announceLatest(latest.message)
      }
    }
  },
)

onMounted(() => {
  const el = hostRef.value
  if (el && supportsPopover) {
    el.setAttribute('popover', 'manual')
  }
  syncTopLayer(false)
})
</script>

<template>
  <div
    ref="hostRef"
    role="region"
    aria-label="系统通知"
    aria-live="polite"
    class="toast-popover-host fixed bottom-4 right-4 z-50 flex flex-col gap-2 pointer-events-none max-w-[380px]"
  >
    <TransitionGroup name="toast">
      <div
        v-for="item in appStore.toasts"
        :key="item.id"
        class="pointer-events-auto flex items-start gap-2.5 px-3.5 py-2.5 rounded-xl border text-xs leading-relaxed"
        style="background-color: var(--bg-surface); border-color: var(--border-subtle); box-shadow: var(--shadow-elevated); color: var(--text-primary)"
        @mouseenter="appStore.pauseToast(item.id)"
        @mouseleave="appStore.resumeToast(item.id)"
        @focusin="appStore.pauseToast(item.id)"
        @focusout="appStore.resumeToast(item.id)"
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
