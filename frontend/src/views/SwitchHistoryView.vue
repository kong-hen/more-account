<script setup lang="ts">
// 切号历史视图：展示每次切号的目标账号、目标客户端（Desktop / IDE / CLI）、是否重启、耗时与诊断状态。
import { computed, onMounted, ref } from 'vue'
import { CheckCircle2, XCircle, AlertTriangle, Trash2, RefreshCw, History } from 'lucide-vue-next'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { useAppStore } from '@/stores/appStore'
import { maskEmail } from '@/utils/privacy'
import { formatDateTime } from '@/utils/quota'

const appStore = useAppStore()
const statusFilter = ref<'all' | 'success' | 'failed'>('all')
const clearConfirmOpen = ref(false)
const loading = ref(false)

async function refresh() {
  loading.value = true
  try {
    await appStore.loadSwitchHistory()
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  void refresh()
})

const items = computed(() => {
  return appStore.switchHistory.filter((item) => {
    if (statusFilter.value === 'success') return item.success
    if (statusFilter.value === 'failed') return !item.success
    return true
  })
})

function targetLabel(target: string): string {
  if (target === 'ide') return 'Antigravity IDE'
  if (target === 'cli') return 'Antigravity CLI'
  return 'Antigravity Desktop'
}

async function handleClear() {
  await appStore.clearSwitchHistory()
  clearConfirmOpen.value = false
}
</script>

<template>
  <section class="flex-1 min-w-0 h-full flex flex-col overflow-hidden">
    <header
      class="wails-drag shrink-0 px-5 pt-5 pb-4 border-b flex items-center justify-between gap-3"
      style="border-color: var(--border-subtle); background-color: var(--bg-app)"
    >
      <div class="wails-no-drag flex items-center gap-2.5 min-w-0">
        <h1 class="text-base font-semibold tracking-tight whitespace-nowrap" style="color: var(--text-primary)">
          切号历史
        </h1>
        <span
          class="px-2 py-0.5 rounded-full text-xs font-mono-num shrink-0"
          style="background-color: var(--bg-subtle); color: var(--text-muted)"
        >
          {{ items.length }}
        </span>
      </div>

      <div class="wails-no-drag flex items-center gap-1.5 shrink-0">
        <div
          class="h-8 inline-flex items-center p-0.5 rounded-lg border shrink-0"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface)"
        >
          <button
            v-for="opt in [
              { id: 'all', label: '全部' },
              { id: 'success', label: '成功' },
              { id: 'failed', label: '失败' },
            ]"
            :key="opt.id"
            type="button"
            class="px-2.5 py-1 rounded-md text-xs font-medium whitespace-nowrap cursor-pointer"
            :style="
              statusFilter === opt.id
                ? { backgroundColor: 'var(--bg-active)', color: 'var(--accent)' }
                : { color: 'var(--text-secondary)' }
            "
            @click="statusFilter = opt.id as 'all' | 'success' | 'failed'"
          >
            {{ opt.label }}
          </button>
        </div>

        <button
          type="button"
          class="h-8 inline-flex items-center gap-1.5 px-3 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)]"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="refresh"
        >
          <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': loading }" />
          <span>刷新</span>
        </button>

        <button
          v-if="appStore.switchHistory.length > 0"
          type="button"
          class="h-8 inline-flex items-center gap-1.5 px-3 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--danger-soft)]"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--danger-text)"
          @click="clearConfirmOpen = true"
        >
          <Trash2 class="w-3.5 h-3.5" />
          <span>清空</span>
        </button>
      </div>
    </header>

    <div class="flex-1 min-h-0 overflow-y-auto custom-scroll p-6">
      <div
        v-if="items.length === 0"
        class="h-full min-h-[320px] flex flex-col items-center justify-center text-center"
      >
        <div
          class="w-12 h-12 rounded-2xl flex items-center justify-center mb-3.5"
          style="background-color: var(--bg-subtle); color: var(--text-muted)"
        >
          <History class="w-6 h-6" />
        </div>
        <h2 class="text-sm font-semibold" style="color: var(--text-primary)">暂无切号记录</h2>
        <p class="mt-1.5 text-xs" style="color: var(--text-muted)">
          每次在账号工作台执行切号后，都会在此保留详细耗时与状态日志。
        </p>
      </div>

      <div
        v-else
        class="rounded-2xl border overflow-hidden"
        style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
      >
        <div class="overflow-x-auto custom-scroll">
          <table class="w-full text-left border-collapse text-xs">
            <thead>
              <tr
                class="border-b text-[11px]"
                style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-muted)"
              >
                <th class="py-2.5 px-4 whitespace-nowrap">切换时间</th>
                <th class="py-2.5 px-3 whitespace-nowrap">目标账号</th>
                <th class="py-2.5 px-3 whitespace-nowrap">目标客户端</th>
                <th class="py-2.5 px-3 whitespace-nowrap">模式</th>
                <th class="py-2.5 px-3 whitespace-nowrap">状态与诊断</th>
                <th class="py-2.5 px-4 text-right whitespace-nowrap">耗时</th>
              </tr>
            </thead>
            <tbody class="divide-y" style="border-color: var(--border-subtle)">
              <tr v-for="item in items" :key="item.id">
                <td class="py-3 px-4 font-mono-num text-[11.5px] whitespace-nowrap" style="color: var(--text-muted)">
                  {{ formatDateTime(item.timestamp) }}
                </td>
                <td class="py-3 px-3 font-medium max-w-[220px] truncate whitespace-nowrap" style="color: var(--text-primary)">
                  {{ maskEmail(item.targetEmail, appStore.settings.privacyMode) }}
                </td>
                <td class="py-3 px-3 whitespace-nowrap">
                  <span
                    class="px-2 py-0.5 rounded-md text-[11px]"
                    style="background-color: var(--bg-subtle); color: var(--text-secondary)"
                  >
                    {{ targetLabel(item.targetClient) }}
                  </span>
                </td>
                <td class="py-3 px-3 text-[11.5px] whitespace-nowrap" style="color: var(--text-secondary)">
                  {{ item.launched ? '切换并重启' : '仅写入凭据' }}
                </td>
                <td class="py-3 px-3 max-w-[320px]">
                  <div
                    v-if="item.success && !item.errorMessage"
                    class="inline-flex items-center gap-1.5 font-medium whitespace-nowrap"
                    style="color: var(--success-text)"
                  >
                    <CheckCircle2 class="w-3.5 h-3.5 shrink-0" />
                    <span>切换成功</span>
                  </div>
                  <div
                    v-else-if="item.success && item.errorMessage"
                    class="inline-flex items-center gap-1.5"
                    style="color: var(--warning-text)"
                  >
                    <AlertTriangle class="w-3.5 h-3.5 shrink-0" />
                    <span class="truncate" :title="item.errorMessage">
                      凭据已切换（{{ item.errorMessage }}）
                    </span>
                  </div>
                  <div v-else class="inline-flex items-center gap-1.5" style="color: var(--danger-text)">
                    <XCircle class="w-3.5 h-3.5 shrink-0" />
                    <span class="truncate" :title="item.errorMessage">
                      {{ item.errorMessage || '切换失败' }}
                    </span>
                  </div>
                </td>
                <td class="py-3 px-4 text-right font-mono-num text-[11.5px] whitespace-nowrap" style="color: var(--text-muted)">
                  {{ item.durationMs }}ms
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :open="clearConfirmOpen"
      title="清空切号历史"
      message="确定要清空全部切号历史记录吗？此操作无法撤销。"
      confirm-text="清空历史"
      @cancel="clearConfirmOpen = false"
      @confirm="handleClear"
    />
  </section>
</template>
