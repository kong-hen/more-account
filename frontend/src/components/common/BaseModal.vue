<script setup lang="ts">
// 基于原生 <dialog> + showModal() 的模态弹窗封装，支持 Esc 与外点击轻关闭（兼容 Safari/WKWebView），
// 并支持通过 preventClose 在加载/破坏性操作执行期间拦截关闭。
import { computed, ref, watch, onMounted, onBeforeUnmount } from 'vue'
import { X } from 'lucide-vue-next'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
    subtitle?: string
    widthClass?: string
    closeOnOutside?: boolean
    preventClose?: boolean
  }>(),
  {
    subtitle: '',
    widthClass: 'w-[520px]',
    closeOnOutside: true,
    preventClose: false,
  },
)

const emit = defineEmits<{
  (e: 'close'): void
}>()

const dialogRef = ref<HTMLDialogElement | null>(null)
const titleId = `dlg-title-${Math.random().toString(36).slice(2, 9)}`
const supportsNativeClosedBy =
  typeof HTMLDialogElement !== 'undefined' && 'closedBy' in HTMLDialogElement.prototype
let pressedOutside = false

const closedByAttr = computed(() => {
  if (props.preventClose) return 'none'
  return props.closeOnOutside ? 'any' : 'closerequest'
})

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
  if (supportsNativeClosedBy || props.preventClose || !props.closeOnOutside) return
  const el = dialogRef.value
  // 仅当点击事件直接落在 <dialog> 元素自身（即 backdrop 区域）时记录，忽略内部子元素或原生下拉菜单点击
  if (!el || e.target !== el) {
    pressedOutside = false
    return
  }
  pressedOutside = isPointerOutside(e)
}

function handlePointerUp(e: PointerEvent) {
  if (supportsNativeClosedBy || props.preventClose || !props.closeOnOutside) return
  const el = dialogRef.value
  if (pressedOutside && el && e.target === el && isPointerOutside(e)) {
    emit('close')
  }
  pressedOutside = false
}

// 兼容 Safari/WKWebView 等尚未原生支持 closedby="none" 的环境，在 cancel 事件中拦截 Esc。
function handleNativeCancel(e: Event) {
  if (props.preventClose) {
    e.preventDefault()
  }
}

function handleNativeClose() {
  const el = dialogRef.value
  if (el) el.returnValue = ''
  if (props.open && !props.preventClose) {
    emit('close')
  } else if (props.open && props.preventClose && el && !el.open) {
    el.showModal()
  }
}
</script>

<template>
  <dialog
    ref="dialogRef"
    class="app-dialog"
    :class="widthClass"
    :aria-labelledby="titleId"
    :closedby="closedByAttr"
    @pointerdown="handlePointerDown"
    @pointerup="handlePointerUp"
    @cancel="handleNativeCancel"
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
        :disabled="preventClose"
        class="p-1.5 rounded-lg cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-40 disabled:cursor-not-allowed"
        style="color: var(--text-muted)"
        @click="emit('close')"
      >
        <X class="w-4 h-4" />
      </button>
    </header>

    <div
      class="px-5 py-4 min-h-0 overflow-y-auto custom-scroll"
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
