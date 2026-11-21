<script setup lang="ts">
// 定时唤醒主视图：全局调度开关、任务列表卡片（新建/编辑/启停/立即执行/删除）与唤醒历史记录双分段。
import { onMounted, ref } from 'vue'
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
  Loader2,
  History,
  ListChecks,
} from 'lucide-vue-next'
import WakeupTaskModal from '@/components/wakeup/WakeupTaskModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import { useAppStore } from '@/stores/appStore'
import { useWakeupStore } from '@/stores/wakeupStore'
import { api, extractErrorMessage } from '@/services/api'
import { maskEmail } from '@/utils/privacy'
import { formatDateTime } from '@/utils/quota'
import type { WakeupHistoryItem, WakeupTask } from '@/types/models'

const appStore = useAppStore()
const wakeupStore = useWakeupStore()

const activeTab = ref<'tasks' | 'history'>('tasks')
const taskModalOpen = ref(false)
const editingTask = ref<WakeupTask | null>(null)
const deletingTask = ref<WakeupTask | null>(null)
const deleting = ref(false)
const clearHistoryConfirm = ref(false)
const clearingHistory = ref(false)

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

function describeSchedule(task: WakeupTask): string {
  const s = task.schedule
  switch (s.repeatMode) {
    case 'daily':
      return `每日 ${(s.dailyTimes ?? []).join(' / ') || '未设定时间'}`
    case 'weekly': {
      const names = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']
      const days = (s.weeklyDays ?? []).map((d) => names[d] ?? '').filter(Boolean).join('、')
      return `${days || '每周'} ${(s.weeklyTimes ?? []).join(' / ')}`
    }
    case 'interval':
      return `每 ${s.intervalHours || 4} 小时（${s.intervalStartTime || '00:00'}–${s.intervalEndTime || '23:59'}）`
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
</script>

<template>
  <section class="flex-1 min-w-0 h-full flex flex-col overflow-hidden">
    <!-- 顶栏 -->
    <header
      class="wails-drag shrink-0 px-5 pt-5 pb-4 border-b flex items-center justify-between gap-3"
      style="border-color: var(--border-subtle); background-color: var(--bg-app)"
    >
      <div class="wails-no-drag flex items-center gap-2.5 min-w-0">
        <h1 class="text-base font-semibold tracking-tight whitespace-nowrap shrink-0" style="color: var(--text-primary)">
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
          class="h-8 inline-flex items-center gap-1.5 px-3 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer transition-colors"
          :style="
            wakeupStore.state.enabled
              ? { borderColor: 'var(--success)', backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
              : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-surface)', color: 'var(--text-muted)' }
          "
          @click="wakeupStore.toggleGlobalEnabled"
        >
          <Power class="w-3.5 h-3.5" />
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
                      :style="
                        task.enabled
                          ? { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
                          : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-muted)' }
                      "
                    >
                      {{ task.enabled ? '启用中' : '已停用' }}
                    </span>
                  </div>
                  <p class="mt-1 text-xs" style="color: var(--text-secondary)">
                    {{ describeSchedule(task) }}
                  </p>
                </div>

                <label class="inline-flex items-center cursor-pointer shrink-0">
                  <input
                    type="checkbox"
                    :checked="task.enabled"
                    class="rounded accent-[var(--accent)] w-4 h-4"
                    :aria-label="`切换任务 ${task.name} 启停`"
                    @change="wakeupStore.toggleTaskEnabled(task.id)"
                  />
                </label>
              </div>

              <div class="flex flex-wrap gap-1.5 text-[11px]">
                <span
                  class="px-2 py-0.5 rounded-md font-mono-num"
                  style="background-color: var(--bg-subtle); color: var(--text-secondary)"
                >
                  {{ task.schedule.selectedAccounts.length }} 个账号
                </span>
                <span
                  v-for="m in task.schedule.selectedModels"
                  :key="m"
                  class="px-2 py-0.5 rounded-md font-mono-num"
                  style="background-color: var(--accent-soft); color: var(--accent)"
                >
                  {{ m }}
                </span>
              </div>
            </div>

            <div
              class="pt-3 border-t flex items-center justify-between flex-wrap gap-2"
              style="border-color: var(--border-subtle)"
            >
              <span class="text-[11px] font-mono-num truncate min-w-0" style="color: var(--text-muted)">
                {{ task.lastRunAt ? `上次运行：${formatDateTime(task.lastRunAt)}` : '尚未执行过' }}
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
                  <td class="py-3 px-3 font-medium max-w-[200px] truncate whitespace-nowrap" style="color: var(--text-primary)">
                    {{ maskEmail(item.accountEmail, appStore.settings.privacyMode) }}
                  </td>
                  <td class="py-3 px-3 font-mono-num text-[11.5px] whitespace-nowrap" style="color: var(--text-secondary)">
                    {{ item.model }}
                  </td>
                  <td class="py-3 px-3 max-w-[280px]">
                    <div v-if="item.success" class="flex items-center gap-1.5" style="color: var(--success-text)">
                      <CheckCircle2 class="w-3.5 h-3.5 shrink-0" />
                      <span class="truncate">{{ item.reply || '唤醒成功' }}</span>
                    </div>
                    <div v-else class="space-y-1">
                      <div class="flex items-center gap-1.5" style="color: var(--danger-text)">
                        <XCircle class="w-3.5 h-3.5 shrink-0" />
                        <span class="truncate">{{ item.errorMessage || '执行失败' }}</span>
                      </div>
                      <button
                        v-if="item.validationUrl"
                        type="button"
                        class="inline-flex items-center gap-1 text-[11px] underline cursor-pointer"
                        style="color: var(--accent)"
                        @click="openUrl(item.validationUrl!)"
                      >
                        <ExternalLink class="w-3 h-3" />
                        打开验证链接
                      </button>
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
