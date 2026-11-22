<script setup lang="ts">
// 定时唤醒主视图：全局调度开关、暂停警示横幅、任务列表卡片（新建/编辑/受控启停/立即执行/删除）与唤醒历史记录双分段。
import { computed, onMounted, ref } from 'vue'
import {
  Plus,
  Play,
  Square,
  Pencil,
  Trash2,
  Clock,
  CheckCircle2,
  XCircle,
  Power,
  ExternalLink,
  Copy,
  Check,
  Loader2,
  History,
  ListChecks,
  AlertTriangle,
} from 'lucide-vue-next'
import WakeupTaskModal from '@/components/wakeup/WakeupTaskModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { useAppStore } from '@/stores/appStore'
import { useWakeupStore } from '@/stores/wakeupStore'
import { api, extractErrorMessage } from '@/services/api'
import { copyToClipboard, maskEmail } from '@/utils/privacy'
import { formatDateTime, mergeModelList } from '@/utils/quota'
import type { ModelQuota, WakeupHistoryItem, WakeupTask } from '@/types/models'

const appStore = useAppStore()
const wakeupStore = useWakeupStore()

const activeTab = ref<'tasks' | 'history'>('tasks')
const taskModalOpen = ref(false)
const editingTask = ref<WakeupTask | null>(null)
const deletingTask = ref<WakeupTask | null>(null)
const deleting = ref(false)
const clearHistoryConfirm = ref(false)
const clearingHistory = ref(false)
const copiedHistoryId = ref('')

const validAccountIdSet = computed(() => new Set(appStore.accounts.map((a) => a.id)))

// 全局调度器处于暂停态且存在已启用任务时，提示用户一键开启总开关，避免误以为定时任务已生效
const hasEnabledTasksWhileGlobalPaused = computed(
  () => !wakeupStore.state.enabled && wakeupStore.state.tasks.some((t) => t.enabled),
)

// 汇总账号池中全部模型的友好显示名称（覆盖合并模型与 MODEL_PLACEHOLDER_* 占位 ID）
const modelDisplayNameMap = computed(() => {
  const rawModels: ModelQuota[] = []
  const direct = new Map<string, string>()
  for (const acc of appStore.accounts) {
    for (const m of acc.quota?.models ?? []) {
      if (!m.name) continue
      rawModels.push(m)
      if (m.displayName && !direct.has(m.name)) {
        direct.set(m.name, m.displayName)
      }
    }
  }
  const merged = mergeModelList(rawModels)
  const map = new Map<string, string>(direct)
  for (const group of merged) {
    if (group.canonicalModelId && group.displayName) {
      map.set(group.canonicalModelId, group.displayName)
    }
    for (const v of group.variants) {
      if (v.name && !map.has(v.name)) {
        map.set(v.name, v.displayName || group.displayName)
      }
    }
  }
  return map
})

function resolveModelLabel(modelId: string): string {
  return modelDisplayNameMap.value.get(modelId) || modelId
}

function validTaskAccountCount(task: WakeupTask): number {
  return (task.schedule.selectedAccounts ?? []).filter((id) => validAccountIdSet.value.has(id)).length
}

function taskStatusBadge(task: WakeupTask): { text: string; style: Record<string, string> } {
  if (!task.enabled) {
    return {
      text: '已停用',
      style: { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-muted)' },
    }
  }
  if (!wakeupStore.state.enabled) {
    return {
      text: '待总开关启用',
      style: { backgroundColor: 'var(--warning-soft)', color: 'var(--warning-text)' },
    }
  }
  return {
    text: '启用中',
    style: { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' },
  }
}

onMounted(() => {
  void wakeupStore.loadAll()
})

function openCreateTask() {
  editingTask.value = null
  taskModalOpen.value = true
}

function openEditTask(task: WakeupTask) {
  editingTask.value = task
  taskModalOpen.value = true
}

function sortWeekdaysMonToSun(days: number[]): number[] {
  const unique = Array.from(new Set(days.filter((d) => Number.isInteger(d) && d >= 0 && d <= 6)))
  return unique.sort((a, b) => (a === 0 ? 7 : a) - (b === 0 ? 7 : b))
}

function describeSchedule(task: WakeupTask): string {
  const s = task.schedule
  switch (s.repeatMode) {
    case 'daily':
      return `每日 ${(s.dailyTimes ?? []).join(' / ') || '未设定时间'}`
    case 'weekly': {
      const names = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
      const days = sortWeekdaysMonToSun(s.weeklyDays ?? [])
        .map((d) => names[d] ?? '')
        .filter(Boolean)
        .join('、')
      return `${days || '每周'} ${(s.weeklyTimes ?? []).join(' / ')}`
    }
    case 'interval': {
      const start = (s.intervalStartTime ?? '').trim()
      const end = (s.intervalEndTime ?? '').trim()
      if (!start && !end) {
        return `每 ${s.intervalHours || 4} 小时（全天）`
      }
      return `每 ${s.intervalHours || 4} 小时（${start || '00:00'}–${end || '23:59'}）`
    }
    case 'crontab':
      return `Cron: ${s.crontab || '未配置'}`
    case 'quota_reset':
      return '配额重置后自动触发'
    default:
      return s.repeatMode
  }
}

function formatHistorySource(item: WakeupHistoryItem): string {
  if (item.taskName) {
    return item.triggerType === 'scheduled' ? item.taskName : `${item.taskName}（手动）`
  }
  return item.triggerType === 'scheduled' ? '定时任务' : '手动测试'
}

async function confirmDeleteTask() {
  if (!deletingTask.value) return
  deleting.value = true
  try {
    await wakeupStore.deleteTask(deletingTask.value.id)
    deletingTask.value = null
  } catch {
    // store 内已提示错误，保留对话框状态
  } finally {
    deleting.value = false
  }
}

async function confirmClearHistory() {
  clearingHistory.value = true
  try {
    await wakeupStore.clearHistory()
    clearHistoryConfirm.value = false
  } catch {
    // store 内已提示错误
  } finally {
    clearingHistory.value = false
  }
}

async function openUrl(url: string) {
  try {
    await api.openExternalURL(url)
  } catch (err) {
    appStore.notify(`打开验证链接失败：${extractErrorMessage(err)}`, 'error')
  }
}

async function copyHistoryUrl(itemId: string, url: string) {
  if (!url) return
  if (await copyToClipboard(url)) {
    copiedHistoryId.value = itemId
    appStore.notify('已复制网页验证链接', 'success', 2000)
    window.setTimeout(() => {
      if (copiedHistoryId.value === itemId) {
        copiedHistoryId.value = ''
      }
    }, 1800)
  }
}
</script>

<template>
  <section class="flex-1 min-w-0 h-full flex flex-col overflow-hidden">
    <!-- 顶栏 -->
    <header
      class="wails-drag shrink-0 px-5 pt-5 pb-4 border-b flex items-center justify-between gap-3"
      style="border-color: var(--border-subtle); background-color: var(--bg-app)"
    >
      <div class="wails-no-drag flex items-center gap-2.5 min-w-0">
        <h1 class="text-[1.0625rem] font-semibold tracking-tight whitespace-nowrap shrink-0" style="color: var(--text-primary)">
          定时唤醒
        </h1>

        <div
          class="h-8 inline-flex items-center p-0.5 rounded-lg border shrink-0"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface)"
        >
          <button
            type="button"
            class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium whitespace-nowrap cursor-pointer"
            :style="
              activeTab === 'tasks'
                ? { backgroundColor: 'var(--bg-active)', color: 'var(--accent)' }
                : { color: 'var(--text-secondary)' }
            "
            @click="activeTab = 'tasks'"
          >
            <ListChecks class="w-3.5 h-3.5" />
            <span>任务列表 ({{ wakeupStore.state.tasks.length }})</span>
          </button>
          <button
            type="button"
            class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs font-medium whitespace-nowrap cursor-pointer"
            :style="
              activeTab === 'history'
                ? { backgroundColor: 'var(--bg-active)', color: 'var(--accent)' }
                : { color: 'var(--text-secondary)' }
            "
            @click="activeTab = 'history'"
          >
            <History class="w-3.5 h-3.5" />
            <span>执行历史 ({{ wakeupStore.history.length }})</span>
          </button>
        </div>
      </div>

      <div class="wails-no-drag flex items-center gap-1.5 shrink-0">
        <!-- 若有进行中的单账号唤醒测试，提供顶栏一键中断入口 -->
        <button
          v-if="wakeupStore.testingScopeId"
          type="button"
          class="h-8 inline-flex items-center gap-1.5 px-3 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer transition-colors hover:opacity-90"
          style="border-color: var(--danger); background-color: var(--danger-soft); color: var(--danger-text)"
          @click="wakeupStore.cancelCurrentTest()"
        >
          <Square class="w-3.5 h-3.5" />
          <span>取消唤醒测试</span>
        </button>

        <!-- 调度器总开关 -->
        <button
          type="button"
          :disabled="wakeupStore.savingState"
          class="h-8 inline-flex items-center gap-1.5 px-3 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer transition-colors disabled:opacity-60"
          :style="
            wakeupStore.state.enabled
              ? { borderColor: 'var(--success)', backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
              : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-surface)', color: 'var(--text-muted)' }
          "
          @click="wakeupStore.toggleGlobalEnabled"
        >
          <Loader2 v-if="wakeupStore.savingState" class="w-3.5 h-3.5 animate-spin" />
          <Power v-else class="w-3.5 h-3.5" />
          <span>{{ wakeupStore.state.enabled ? '调度器已启用' : '调度器已暂停' }}</span>
        </button>

        <button
          v-if="activeTab === 'tasks'"
          type="button"
          class="h-8 inline-flex items-center gap-1.5 px-3.5 rounded-lg text-xs font-medium text-white whitespace-nowrap shrink-0 cursor-pointer"
          style="background-color: var(--accent)"
          @click="openCreateTask"
        >
          <Plus class="w-3.5 h-3.5" />
          <span>新建任务</span>
        </button>

        <button
          v-else-if="wakeupStore.history.length > 0"
          type="button"
          class="h-8 inline-flex items-center gap-1.5 px-3 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--danger-soft)]"
          style="border-color: var(--border-subtle); color: var(--danger-text); background-color: var(--bg-surface)"
          @click="clearHistoryConfirm = true"
        >
          <Trash2 class="w-3.5 h-3.5" />
          <span>清空历史</span>
        </button>
      </div>
    </header>

    <!-- 内容区 -->
    <div class="flex-1 min-h-0 overflow-y-auto custom-scroll p-6">
      <!-- 任务列表 Tab -->
      <template v-if="activeTab === 'tasks'">
        <!-- 全局调度器暂停警示横幅 -->
        <div
          v-if="hasEnabledTasksWhileGlobalPaused"
          class="mb-4 px-4 py-3 rounded-xl border flex items-center justify-between gap-3 flex-wrap"
          style="background-color: var(--warning-soft); border-color: var(--warning); color: var(--warning-text)"
        >
          <div class="flex items-center gap-2 text-xs font-medium min-w-0">
            <AlertTriangle class="w-4 h-4 shrink-0" style="color: var(--warning)" />
            <span>全局调度器当前已暂停，以下已启用任务不会按计划自动执行，请先开启调度器总开关。</span>
          </div>
          <button
            type="button"
            :disabled="wakeupStore.savingState"
            class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium text-white whitespace-nowrap shrink-0 cursor-pointer hover:opacity-90 disabled:opacity-60"
            style="background-color: var(--warning)"
            @click="wakeupStore.toggleGlobalEnabled"
          >
            <Power class="w-3.5 h-3.5" />
            <span>一键启用调度器</span>
          </button>
        </div>

        <div
          v-if="wakeupStore.state.tasks.length === 0"
          class="h-full min-h-[320px] flex flex-col items-center justify-center text-center"
        >
          <div
            class="w-12 h-12 rounded-2xl flex items-center justify-center mb-3.5"
            style="background-color: var(--bg-subtle); color: var(--text-muted)"
          >
            <Clock class="w-6 h-6" />
          </div>
          <h2 class="text-sm font-semibold" style="color: var(--text-primary)">暂无定时唤醒任务</h2>
          <p class="mt-1.5 text-xs max-w-sm leading-relaxed" style="color: var(--text-muted)">
            配置定时发送极轻提示词，帮助提前开启 5 小时配额刷新计时周期，保持账号池活跃。
          </p>
          <button
            type="button"
            class="mt-4 inline-flex items-center gap-1.5 px-4 py-2 rounded-xl text-xs font-medium text-white cursor-pointer"
            style="background-color: var(--accent)"
            @click="openCreateTask"
          >
            <Plus class="w-3.5 h-3.5" />
            创建首个任务
          </button>
        </div>

        <div v-else class="grid grid-cols-1 lg:grid-cols-2 gap-4">
          <article
            v-for="task in wakeupStore.state.tasks"
            :key="task.id"
            class="p-4 rounded-2xl border flex flex-col justify-between gap-3.5"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle); box-shadow: var(--shadow-card)"
          >
            <div class="space-y-2.5">
              <div class="flex items-start justify-between gap-3">
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <h3 class="text-sm font-semibold truncate" style="color: var(--text-primary)">
                      {{ task.name }}
                    </h3>
                    <span
                      class="px-2 py-0.5 rounded-full text-[10.5px] font-medium shrink-0"
                      :style="taskStatusBadge(task).style"
                    >
                      {{ taskStatusBadge(task).text }}
                    </span>
                  </div>
                  <p class="mt-1 text-xs" style="color: var(--text-secondary)">
                    {{ describeSchedule(task) }}
                  </p>
                </div>

                <!-- 受控启停开关：状态完全由 store 驱动，落盘失败时不会发生 DOM 视觉错位 -->
                <button
                  type="button"
                  role="switch"
                  :aria-checked="task.enabled"
                  :aria-label="`切换任务 ${task.name} 启停`"
                  :disabled="wakeupStore.savingState"
                  class="relative inline-flex h-5 w-9 shrink-0 items-center rounded-full transition-colors cursor-pointer disabled:opacity-50"
                  :style="{
                    backgroundColor: task.enabled ? 'var(--accent)' : 'var(--border-strong)',
                  }"
                  @click="wakeupStore.toggleTaskEnabled(task.id)"
                >
                  <span
                    class="inline-block h-3.5 w-3.5 rounded-full bg-white transition-transform shadow-sm"
                    :class="task.enabled ? 'translate-x-[18px]' : 'translate-x-[3px]'"
                  />
                </button>
              </div>

              <div class="flex flex-wrap gap-1.5 text-[11px]">
                <span
                  class="px-2 py-0.5 rounded-md font-mono-num"
                  style="background-color: var(--bg-subtle); color: var(--text-secondary)"
                >
                  {{ validTaskAccountCount(task) }} 个账号
                </span>
                <span
                  v-for="m in task.schedule.selectedModels"
                  :key="m"
                  :title="m"
                  class="px-2 py-0.5 rounded-md font-medium"
                  style="background-color: var(--accent-soft); color: var(--accent)"
                >
                  {{ resolveModelLabel(m) }}
                </span>
              </div>
            </div>

            <div
              class="pt-3 border-t flex items-center justify-between flex-wrap gap-2"
              style="border-color: var(--border-subtle)"
            >
              <span class="text-[11px] font-mono-num truncate min-w-0" style="color: var(--text-muted)">
                {{ task.lastRunAt ? `上次定时运行：${formatDateTime(task.lastRunAt)}` : '尚未定时执行过' }}
              </span>

              <div class="flex items-center gap-1.5 shrink-0">
                <button
                  type="button"
                  :disabled="Boolean(wakeupStore.runningTaskId)"
                  class="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
                  style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
                  @click="wakeupStore.runTaskNow(task.id)"
                >
                  <Loader2 v-if="wakeupStore.runningTaskId === task.id" class="w-3.5 h-3.5 animate-spin" />
                  <Play v-else class="w-3.5 h-3.5" />
                  <span>{{ wakeupStore.runningTaskId === task.id ? '执行中...' : '立即执行' }}</span>
                </button>
                <button
                  type="button"
                  title="编辑任务"
                  aria-label="编辑任务"
                  class="p-1.5 rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)]"
                  style="border-color: var(--border-subtle); color: var(--text-secondary)"
                  @click="openEditTask(task)"
                >
                  <Pencil class="w-3.5 h-3.5" />
                </button>
                <button
                  type="button"
                  title="删除任务"
                  aria-label="删除任务"
                  class="p-1.5 rounded-lg border cursor-pointer hover:bg-[var(--danger-soft)]"
                  style="border-color: var(--border-subtle); color: var(--danger)"
                  @click="deletingTask = task"
                >
                  <Trash2 class="w-3.5 h-3.5" />
                </button>
              </div>
            </div>
          </article>
        </div>
      </template>

      <!-- 执行历史 Tab -->
      <template v-else>
        <div
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
                  <th class="py-2.5 px-4 whitespace-nowrap">时间</th>
                  <th class="py-2.5 px-3 whitespace-nowrap">来源</th>
                  <th class="py-2.5 px-3 whitespace-nowrap">账号</th>
                  <th class="py-2.5 px-3 whitespace-nowrap">模型</th>
                  <th class="py-2.5 px-3 whitespace-nowrap">结果 / 回复</th>
                  <th class="py-2.5 px-4 text-right whitespace-nowrap">耗时</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-[var(--border-hairline)]">
                <tr v-for="item in wakeupStore.history" :key="item.id">
                  <td class="py-3 px-4 font-mono-num text-[11.5px] whitespace-nowrap" style="color: var(--text-muted)">
                    {{ formatDateTime(item.timestamp) }}
                  </td>
                  <td class="py-3 px-3 whitespace-nowrap">
                    <span
                      class="px-1.5 py-0.5 rounded text-[10.5px] font-medium"
                      style="background-color: var(--bg-subtle); color: var(--text-secondary)"
                    >
                      {{ formatHistorySource(item) }}
                    </span>
                  </td>
                  <td
                    class="py-3 px-3 font-medium max-w-[200px] truncate whitespace-nowrap"
                    style="color: var(--text-primary)"
                    :title="maskEmail(item.accountEmail, appStore.settings.privacyMode)"
                  >
                    {{ maskEmail(item.accountEmail, appStore.settings.privacyMode) }}
                  </td>
                  <td
                    class="py-3 px-3 font-mono-num text-[11.5px] whitespace-nowrap"
                    style="color: var(--text-secondary)"
                    :title="item.model"
                  >
                    {{ resolveModelLabel(item.model) }}
                  </td>
                  <td class="py-3 px-3 max-w-[280px]">
                    <div v-if="item.success" class="flex items-center gap-1.5" style="color: var(--success-text)">
                      <CheckCircle2 class="w-3.5 h-3.5 shrink-0" />
                      <span class="truncate" :title="item.reply || '唤醒成功'">{{ item.reply || '唤醒成功' }}</span>
                      <span
                        v-if="item.totalTokens"
                        :title="`输入 ${item.promptTokens ?? 0} / 输出 ${item.completionTokens ?? 0}`"
                        class="px-1.5 py-0.5 rounded text-[10px] font-mono-num shrink-0"
                        style="background-color: var(--bg-subtle); color: var(--text-muted)"
                      >
                        {{ item.totalTokens }} tok
                      </span>
                    </div>
                    <div v-else class="space-y-1">
                      <div class="flex items-center gap-1.5" style="color: var(--danger-text)">
                        <XCircle class="w-3.5 h-3.5 shrink-0" />
                        <span class="truncate" :title="item.errorMessage || '执行失败'">
                          {{ item.errorMessage || '执行失败' }}
                        </span>
                      </div>
                      <div v-if="item.validationUrl" class="flex items-center gap-2.5 pt-0.5">
                        <button
                          type="button"
                          class="inline-flex items-center gap-1 text-[11px] font-medium underline cursor-pointer"
                          style="color: var(--accent)"
                          @click="openUrl(item.validationUrl!)"
                        >
                          <ExternalLink class="w-3 h-3" />
                          网页验证
                        </button>
                        <button
                          type="button"
                          class="inline-flex items-center gap-1 text-[11px] underline cursor-pointer"
                          style="color: var(--text-secondary)"
                          @click="copyHistoryUrl(item.id, item.validationUrl!)"
                        >
                          <Check v-if="copiedHistoryId === item.id" class="w-3 h-3" style="color: var(--success)" />
                          <Copy v-else class="w-3 h-3" />
                          {{ copiedHistoryId === item.id ? '已复制' : '复制链接' }}
                        </button>
                      </div>
                    </div>
                  </td>
                  <td class="py-3 px-4 text-right font-mono-num text-[11.5px] whitespace-nowrap" style="color: var(--text-muted)">
                    {{ item.durationMs }}ms
                  </td>
                </tr>
                <tr v-if="wakeupStore.history.length === 0">
                  <td colspan="6" class="py-12 text-center text-xs" style="color: var(--text-muted)">
                    暂无唤醒执行记录
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </template>
    </div>

    <WakeupTaskModal
      :open="taskModalOpen"
      :editing-task="editingTask"
      @close="taskModalOpen = false"
    />

    <ConfirmDialog
      :open="Boolean(deletingTask)"
      title="删除唤醒任务"
      :message="`确定删除定时唤醒任务「${deletingTask?.name ?? ''}」吗？此操作无法撤销。`"
      confirm-text="删除任务"
      :loading="deleting"
      @cancel="deletingTask = null"
      @confirm="confirmDeleteTask"
    />

    <ConfirmDialog
      :open="clearHistoryConfirm"
      title="清空唤醒历史"
      message="确定清空全部唤醒执行历史记录吗？"
      confirm-text="清空"
      :loading="clearingHistory"
      @cancel="clearHistoryConfirm = false"
      @confirm="confirmClearHistory"
    />
  </section>
</template>
