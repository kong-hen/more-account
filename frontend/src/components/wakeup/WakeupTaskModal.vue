<script setup lang="ts">
// 新建 / 编辑定时唤醒任务弹窗：支持每日、每周、固定间隔、Cron 与配额重置触发模式。
import { computed, ref, watch } from 'vue'
import BaseModal from '@/components/common/BaseModal.vue'
import { useAppStore } from '@/stores/appStore'
import { useWakeupStore } from '@/stores/wakeupStore'
import { maskEmail } from '@/utils/privacy'
import type { WakeupRepeatMode, WakeupTask } from '@/types/models'

const props = defineProps<{
  open: boolean
  editingTask?: WakeupTask | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const appStore = useAppStore()
const wakeupStore = useWakeupStore()

const name = ref('')
const enabled = ref(true)
const repeatMode = ref<WakeupRepeatMode>('daily')
const timesText = ref('08:30, 14:00')
const weeklyDays = ref<number[]>([1, 2, 3, 4, 5])
const intervalHours = ref(5)
const intervalStartTime = ref('08:00')
const intervalEndTime = ref('22:00')
const crontab = ref('0 9 * * *')
const customPrompt = ref('hi')
const maxOutputTokens = ref(16)
const selectedAccounts = ref<string[]>([])
const selectedModels = ref<string[]>(['gemini-3-flash'])
const error = ref('')

const weekdayOptions = [
  { val: 1, label: '周一' },
  { val: 2, label: '周二' },
  { val: 3, label: '周三' },
  { val: 4, label: '周四' },
  { val: 5, label: '周五' },
  { val: 6, label: '周六' },
  { val: 0, label: '周日' },
]

// 汇总账号池中已探测到的模型列表 + 常用保底模型
const modelCandidates = computed(() => {
  const set = new Set<string>(['gemini-3-flash', 'gemini-3-pro-high', 'claude-sonnet-4-5'])
  for (const acc of appStore.accounts) {
    for (const m of acc.quota?.models ?? []) {
      if (m.name) set.add(m.name)
    }
  }
  return Array.from(set).sort()
})

watch(
  () => [props.open, props.editingTask] as const,
  ([isOpen, task]) => {
    if (!isOpen) return
    error.value = ''
    if (task) {
      name.value = task.name
      enabled.value = task.enabled
      repeatMode.value = task.schedule.repeatMode || 'daily'
      timesText.value = (
        task.schedule.repeatMode === 'weekly'
          ? task.schedule.weeklyTimes
          : task.schedule.dailyTimes
      )?.join(', ') || '09:00'
      weeklyDays.value = task.schedule.weeklyDays?.length ? [...task.schedule.weeklyDays] : [1, 2, 3, 4, 5]
      intervalHours.value = task.schedule.intervalHours || 5
      intervalStartTime.value = task.schedule.intervalStartTime || '08:00'
      intervalEndTime.value = task.schedule.intervalEndTime || '22:00'
      crontab.value = task.schedule.crontab || '0 9 * * *'
      customPrompt.value = task.schedule.customPrompt || 'hi'
      maxOutputTokens.value = task.schedule.maxOutputTokens || 16
      selectedAccounts.value = [...(task.schedule.selectedAccounts ?? [])]
      selectedModels.value = task.schedule.selectedModels?.length
        ? [...task.schedule.selectedModels]
        : ['gemini-3-flash']
    } else {
      name.value = ''
      enabled.value = true
      repeatMode.value = 'daily'
      timesText.value = '08:30'
      weeklyDays.value = [1, 2, 3, 4, 5]
      intervalHours.value = 5
      intervalStartTime.value = '08:00'
      intervalEndTime.value = '22:00'
      crontab.value = '0 9 * * *'
      customPrompt.value = 'hi'
      maxOutputTokens.value = 16
      selectedAccounts.value = appStore.currentAccountId ? [appStore.currentAccountId] : []
      selectedModels.value = ['gemini-3-flash']
    }
  },
)

function toggleInArray<T>(arr: T[], val: T): T[] {
  return arr.includes(val) ? arr.filter((x) => x !== val) : [...arr, val]
}

async function handleSave() {
  if (!name.value.trim()) {
    error.value = '请输入任务名称'
    return
  }
  if (selectedAccounts.value.length === 0) {
    error.value = '请至少选择一个目标账号'
    return
  }
  if (selectedModels.value.length === 0) {
    error.value = '请至少选择一个唤醒模型'
    return
  }

  const parsedTimes = timesText.value
    .split(/[,，\s]+/)
    .map((s) => s.trim())
    .filter(Boolean)

  const task: WakeupTask = {
    id: props.editingTask?.id ?? '',
    name: name.value.trim(),
    enabled: enabled.value,
    lastRunAt: props.editingTask?.lastRunAt,
    createdAt: props.editingTask?.createdAt ?? 0,
    schedule: {
      repeatMode: repeatMode.value,
      dailyTimes: repeatMode.value === 'daily' ? parsedTimes : undefined,
      weeklyDays: repeatMode.value === 'weekly' ? weeklyDays.value : undefined,
      weeklyTimes: repeatMode.value === 'weekly' ? parsedTimes : undefined,
      intervalHours: repeatMode.value === 'interval' ? intervalHours.value : undefined,
      intervalStartTime: repeatMode.value === 'interval' ? intervalStartTime.value : undefined,
      intervalEndTime: repeatMode.value === 'interval' ? intervalEndTime.value : undefined,
      crontab: repeatMode.value === 'crontab' ? crontab.value.trim() : undefined,
      wakeOnReset: repeatMode.value === 'quota_reset',
      selectedAccounts: selectedAccounts.value,
      selectedModels: selectedModels.value,
      customPrompt: customPrompt.value.trim() || 'hi',
      maxOutputTokens: maxOutputTokens.value || 16,
    },
  }

  await wakeupStore.upsertTask(task)
  emit('close')
}
</script>

<template>
  <BaseModal
    :open="open"
    :title="editingTask ? '编辑唤醒任务' : '新建定时唤醒任务'"
    subtitle="定时向指定账号发送极轻请求以触发配额重置计时窗口"
    width-class="w-[560px]"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <div class="grid grid-cols-2 gap-3">
        <div>
          <label for="wk-name" class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">任务名称</label>
          <input
            id="wk-name"
            v-model="name"
            type="text"
            placeholder="例如：早间主力号唤醒"
            class="w-full px-3 py-1.5 text-xs rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
        <div>
          <label for="wk-mode" class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">触发模式</label>
          <select
            id="wk-mode"
            v-model="repeatMode"
            class="w-full px-2.5 py-1.5 text-xs rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          >
            <option value="daily">每日定点</option>
            <option value="weekly">每周定点</option>
            <option value="interval">固定间隔（小时）</option>
            <option value="crontab">Cron 表达式</option>
            <option value="quota_reset">配额重置后自动唤醒</option>
          </select>
        </div>
      </div>

      <!-- 模式专属参数 -->
      <div
        v-if="repeatMode === 'daily' || repeatMode === 'weekly'"
        class="p-3 rounded-xl border space-y-2.5"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div v-if="repeatMode === 'weekly'" class="flex items-center flex-wrap gap-1.5">
          <button
            v-for="w in weekdayOptions"
            :key="w.val"
            type="button"
            class="px-2.5 py-1 rounded-lg text-[11px] font-medium cursor-pointer"
            :style="
              weeklyDays.includes(w.val)
                ? { backgroundColor: 'var(--accent)', color: '#fff' }
                : { backgroundColor: 'var(--bg-surface)', color: 'var(--text-secondary)' }
            "
            @click="weeklyDays = toggleInArray(weeklyDays, w.val)"
          >
            {{ w.label }}
          </button>
        </div>
        <div>
          <label for="wk-times" class="block text-[11.5px] mb-1" style="color: var(--text-muted)">
            执行时间点（24 小时制 HH:MM，多个用逗号分隔）
          </label>
          <input
            id="wk-times"
            v-model="timesText"
            type="text"
            placeholder="08:30, 14:00, 19:30"
            class="w-full px-3 py-1.5 text-xs font-mono-num rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
      </div>

      <div
        v-else-if="repeatMode === 'interval'"
        class="p-3 rounded-xl border grid grid-cols-3 gap-2.5"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div>
          <label class="block text-[11px] mb-1" style="color: var(--text-muted)">间隔小时数</label>
          <input
            v-model.number="intervalHours"
            type="number"
            min="1"
            max="24"
            class="w-full px-2.5 py-1.5 text-xs font-mono-num rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
        <div>
          <label class="block text-[11px] mb-1" style="color: var(--text-muted)">窗口开始</label>
          <input
            v-model="intervalStartTime"
            type="text"
            placeholder="08:00"
            class="w-full px-2.5 py-1.5 text-xs font-mono-num rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
        <div>
          <label class="block text-[11px] mb-1" style="color: var(--text-muted)">窗口结束</label>
          <input
            v-model="intervalEndTime"
            type="text"
            placeholder="22:00"
            class="w-full px-2.5 py-1.5 text-xs font-mono-num rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
      </div>

      <div
        v-else-if="repeatMode === 'crontab'"
        class="p-3 rounded-xl border"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <label class="block text-[11.5px] mb-1" style="color: var(--text-muted)">
          标准 5 段 Cron 表达式（分 时 日 月 周，例如 0 9 * * 1-5）
        </label>
        <input
          v-model="crontab"
          type="text"
          class="w-full px-3 py-1.5 text-xs font-mono-num rounded-lg border"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
        />
      </div>

      <!-- 目标账号多选 -->
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <span class="text-xs font-medium" style="color: var(--text-secondary)">
            目标账号 ({{ selectedAccounts.length }})
          </span>
          <button
            type="button"
            class="text-[11px] cursor-pointer hover:underline"
            style="color: var(--accent)"
            @click="
              selectedAccounts =
                selectedAccounts.length === appStore.accounts.length
                  ? []
                  : appStore.accounts.map((a) => a.id)
            "
          >
            {{ selectedAccounts.length === appStore.accounts.length ? '取消全选' : '全选账号' }}
          </button>
        </div>
        <div
          class="max-h-32 overflow-y-auto custom-scroll p-2 rounded-xl border space-y-1"
          style="border-color: var(--border-subtle); background-color: var(--bg-app)"
        >
          <label
            v-for="acc in appStore.accounts"
            :key="acc.id"
            class="flex items-center gap-2 px-2 py-1 rounded-lg text-xs cursor-pointer hover:bg-[var(--bg-hover)]"
          >
            <input
              type="checkbox"
              :checked="selectedAccounts.includes(acc.id)"
              class="rounded accent-[var(--accent)]"
              @change="selectedAccounts = toggleInArray(selectedAccounts, acc.id)"
            />
            <span class="truncate" style="color: var(--text-primary)">
              {{ maskEmail(acc.email, appStore.settings.privacyMode) }}
            </span>
          </label>
          <p v-if="appStore.accounts.length === 0" class="text-xs text-center py-3" style="color: var(--text-muted)">
            暂无可选账号
          </p>
        </div>
      </div>

      <!-- 模型多选 -->
      <div class="space-y-1.5">
        <span class="text-xs font-medium block" style="color: var(--text-secondary)">
          唤醒模型 ({{ selectedModels.length }})
        </span>
        <div class="flex flex-wrap gap-1.5 max-h-24 overflow-y-auto custom-scroll">
          <button
            v-for="m in modelCandidates"
            :key="m"
            type="button"
            class="px-2.5 py-1 rounded-lg border text-[11px] font-mono-num cursor-pointer transition-colors"
            :style="
              selectedModels.includes(m)
                ? { borderColor: 'var(--accent)', backgroundColor: 'var(--accent-soft)', color: 'var(--accent)' }
                : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-surface)', color: 'var(--text-secondary)' }
            "
            @click="selectedModels = toggleInArray(selectedModels, m)"
          >
            {{ m }}
          </button>
        </div>
      </div>

      <!-- 极简提示词与最大输出 -->
      <div class="grid grid-cols-3 gap-3">
        <div class="col-span-2">
          <label class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">唤醒提示词</label>
          <input
            v-model="customPrompt"
            type="text"
            placeholder="hi"
            class="w-full px-3 py-1.5 text-xs rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
        <div>
          <label class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">最大输出 Token</label>
          <input
            v-model.number="maxOutputTokens"
            type="number"
            min="4"
            max="128"
            class="w-full px-3 py-1.5 text-xs font-mono-num rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
      </div>

      <p v-if="error" class="text-xs" style="color: var(--danger-text)">{{ error }}</p>
    </div>

    <template #footer>
      <button
        type="button"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)]"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="emit('close')"
      >
        取消
      </button>
      <button
        type="button"
        class="px-4 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer"
        style="background-color: var(--accent)"
        @click="handleSave"
      >
        保存任务
      </button>
    </template>
  </BaseModal>
</template>
