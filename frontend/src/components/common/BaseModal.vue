<script setup lang="ts">
// 基于原生 <dialog> + showModal() 的模态弹窗封装，支持 Esc 与外点击轻关闭（兼容 Safari/WKWebView）。
import { ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { X } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    subtitle?: string
    widthClass?: string
    closeOnOutside?: boolean
  }>(),
  {
    subtitle: '',
    widthClass: 'w-[520px]',
    closeOnOutside: true,
  },
)

const emit = defineEmits<{
  (e: 'close'): void
}>()

const dialogRef = ref<HTMLDialogElement | null>(null)
const titleId = `dlg-title-${Math.random().toString(36).slice(2, 9)}`
let pressedOutside = false

function syncOpenState(isOpen: boolean) {
  const el = dialogRef.value
  if (!el) return
  if (isOpen && !el.open) {
    el.showModal()
  } else if (!isOpen && el.open) {
    el.close()
  }
}

watch(
  () => props.open,
  (val) => syncOpenState(val),
)

onMounted(() => {
  syncOpenState(props.open)
})

onBeforeUnmount(() => {
  const el = dialogRef.value
  if (el?.open) el.close()
})

function isPointerOutside(e: PointerEvent): boolean {
  const el = dialogRef.value
  if (!el) return false
  const rect = el.getBoundingClientRect()
  return (
    e.clientX < rect.left ||
    e.clientX > rect.right ||
    e.clientY < rect.top ||
    e.clientY > rect.bottom
  )
}

function handlePointerDown(e: PointerEvent) {
  if (!props.closeOnOutside) return
  pressedOutside = isPointerOutside(e)
}

function handlePointerUp(e: PointerEvent) {
  if (!props.closeOnOutside) return
  if (pressedOutside && isPointerOutside(e)) {
    emit('close')
  }
  pressedOutside = false
}

function handleNativeClose() {
  const el = dialogRef.value
  if (el) el.returnValue = ''
  if (props.open) {
    emit('close')
  }
}
</script>

<template>
  <dialog
    ref="dialogRef"
    class="app-dialog"
    :class="widthClass"
    :aria-labelledby="titleId"
    @pointerdown="handlePointerDown"
    @pointerup="handlePointerUp"
    @close="handleNativeClose"
  >
    <header
      class="shrink-0 flex items-start justify-between gap-4 px-5 py-4 border-b"
      style="border-color: var(--border-subtle)"
    >
      <div class="min-w-0">
        <h2 :id="titleId" class="text-[15px] font-semibold tracking-tight" style="color: var(--text-primary)">
          {{ title }}
        </h2>
        <p v-if="subtitle" class="mt-0.5 text-xs" style="color: var(--text-muted)">
          {{ subtitle }}
        </p>
      </div>
      <button
        type="button"
        aria-label="关闭弹窗"
        class="p-1.5 rounded-lg cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
        style="color: var(--text-muted)"
        @click="emit('close')"
      >
        <X class="w-4 h-4" />
      </button>
    </header>

    <div
      class="px-5 py-4 overflow-y-auto custom-scroll"
      :style="{ maxHeight: $slots.footer ? 'calc(100vh - 180px)' : 'calc(100vh - 128px)' }"
    >
      <slot />
    </div>

    <footer
      v-if="$slots.footer"
      class="shrink-0 flex items-center justify-end gap-2.5 px-5 py-3.5 border-t"
      style="border-color: var(--border-subtle); background-color: var(--bg-app)"
    >
      <slot name="footer" />
    </footer>
  </dialog>
</template>
