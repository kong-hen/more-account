<script setup lang="ts">
// 新建 / 编辑定时唤醒任务弹窗：支持每日、每周、固定间隔、Cron 与配额重置触发模式。
import { computed, ref, watch } from 'vue'
import BaseModal from '@/components/common/BaseModal.vue'
import AppSelect from '@/components/common/AppSelect.vue'
import { useAppStore } from '@/stores/appStore'
import { useWakeupStore } from '@/stores/wakeupStore'
import { maskEmail } from '@/utils/privacy'
import { mergeModelList, pickPreferredWakeupModel, type MergedModelQuota } from '@/utils/quota'
import type { ModelQuota, WakeupRepeatMode, WakeupTask } from '@/types/models'

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
const maxOutputTokens = ref(64)
const selectedAccounts = ref<string[]>([])
const selectedModels = ref<string[]>([])
const error = ref('')
const saving = ref(false)

const HH_MM_RE = /^([01]\d|2[0-3]):[0-5]\d$/
const CRON_FIELD_RE = /^[0-9*,/-]+$/

const allAccountsSelected = computed(
  () => appStore.accounts.length > 0 && selectedAccounts.value.length === appStore.accounts.length,
)

const repeatModeOptions = [
  { value: 'daily', label: '每日定点' },
  { value: 'weekly', label: '每周定点' },
  { value: 'interval', label: '固定间隔（小时）' },
  { value: 'crontab', label: 'Cron 表达式' },
  { value: 'quota_reset', label: '配额重置后自动唤醒' },
]

function setRepeatMode(val: string) {
  repeatMode.value = val as WakeupRepeatMode
}

const weekdayOptions = [
  { val: 1, label: '周一' },
  { val: 2, label: '周二' },
  { val: 3, label: '周三' },
  { val: 4, label: '周四' },
  { val: 5, label: '周五' },
  { val: 6, label: '周六' },
  { val: 0, label: '周日' },
]

// 汇总账号池中已探测到的模型列表（仅当账号池尚无模型数据时补充常用保底项），并合并同一模型的不同思考深度
const modelCandidates = computed<MergedModelQuota[]>(() => {
  const byName = new Map<string, ModelQuota>()
  for (const acc of appStore.accounts) {
    for (const m of acc.quota?.models ?? []) {
      if (m.name) {
        const existing = byName.get(m.name)
        if (!existing || (!existing.displayName && m.displayName)) {
          byName.set(m.name, m)
        }
      }
    }
  }
  if (byName.size === 0) {
    const defaults: ModelQuota[] = [
      { name: 'gemini-3.8-flash-high', displayName: 'Gemini 3.8 Flash (High)', percentage: 100 },
      { name: 'claude-sonnet-4-5', displayName: 'Claude Sonnet 4.5', percentage: 100 },
    ]
    for (const d of defaults) {
      byName.set(d.name, d)
    }
  }
  for (const sel of selectedModels.value) {
    if (sel && !byName.has(sel)) {
      byName.set(sel, { name: sel, percentage: 100 })
    }
  }
  return mergeModelList(Array.from(byName.values()))
})

function isCandidateSelected(candidate: MergedModelQuota): boolean {
  return candidate.variants.some((v) => selectedModels.value.includes(v.name))
}

function toggleModelCandidate(candidate: MergedModelQuota) {
  const variantNames = new Set(candidate.variants.map((v) => v.name))
  if (isCandidateSelected(candidate)) {
    selectedModels.value = selectedModels.value.filter((id) => !variantNames.has(id))
  } else {
    selectedModels.value = [...selectedModels.value, candidate.canonicalModelId]
  }
}

watch(
  () => [props.open, props.editingTask] as const,
  ([isOpen, task]) => {
    if (!isOpen) return
    error.value = ''
    saving.value = false
    const validAccountIds = new Set(appStore.accounts.map((a) => a.id))
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
      maxOutputTokens.value = task.schedule.maxOutputTokens || 64
      const fallbackModel = pickPreferredWakeupModel(modelCandidates.value)
      // 过滤掉已删除的无效账号 ID，避免勾选计数与全选状态失真
      selectedAccounts.value = (task.schedule.selectedAccounts ?? []).filter((id) => validAccountIds.has(id))
      selectedModels.value = task.schedule.selectedModels?.length
        ? [...task.schedule.selectedModels]
        : [fallbackModel]
    } else {
      const fallbackModel = pickPreferredWakeupModel(modelCandidates.value)
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
      maxOutputTokens.value = 64
      selectedAccounts.value =
        appStore.currentAccountId && validAccountIds.has(appStore.currentAccountId)
          ? [appStore.currentAccountId]
          : []
      selectedModels.value = [fallbackModel]
    }
  },
)

function toggleInArray<T>(arr: T[], val: T): T[] {
  return arr.includes(val) ? arr.filter((x) => x !== val) : [...arr, val]
}

async function handleSave() {
  if (saving.value) return
  error.value = ''

  if (!name.value.trim()) {
    error.value = '请输入任务名称'
    return
  }

  const validAccountIds = new Set(appStore.accounts.map((a) => a.id))
  const cleanAccounts = selectedAccounts.value.filter((id) => validAccountIds.has(id))
  if (cleanAccounts.length === 0) {
    error.value = '请至少选择一个有效的目标账号'
    return
  }
  if (selectedModels.value.length === 0) {
    error.value = '请至少选择一个唤醒模型'
    return
  }

  let parsedTimes: string[] | undefined
  let cleanIntervalHours: number | undefined
  let cleanIntervalStart: string | undefined
  let cleanIntervalEnd: string | undefined
  let cleanCron: string | undefined

  if (repeatMode.value === 'daily' || repeatMode.value === 'weekly') {
    if (repeatMode.value === 'weekly' && weeklyDays.value.length === 0) {
      error.value = '请至少选择一个执行星期'
      return
    }
    const rawTimes = Array.from(
      new Set(
        timesText.value
          .split(/[,，\s]+/)
          .map((s) => s.trim())
          .filter(Boolean),
      ),
    )
    if (rawTimes.length === 0) {
      error.value = '请至少填写一个执行时间点（24 小时制 HH:MM）'
      return
    }
    const invalidTime = rawTimes.find((t) => !HH_MM_RE.test(t))
    if (invalidTime) {
      error.value = `时间点格式不正确（需为 24 小时制 HH:MM）：${invalidTime}`
      return
    }
    parsedTimes = rawTimes
  } else if (repeatMode.value === 'interval') {
    const hours = Number(intervalHours.value)
    if (!Number.isInteger(hours) || hours < 1 || hours > 24) {
      error.value = '间隔小时数需为 1–24 之间的整数'
      return
    }
    const startStr = intervalStartTime.value.trim()
    const endStr = intervalEndTime.value.trim()
    if (startStr && !HH_MM_RE.test(startStr)) {
      error.value = '窗口开始时间格式不正确（需为 24 小时制 HH:MM）'
      return
    }
    if (endStr && !HH_MM_RE.test(endStr)) {
      error.value = '窗口结束时间格式不正确（需为 24 小时制 HH:MM）'
      return
    }
    cleanIntervalHours = hours
    cleanIntervalStart = startStr || undefined
    cleanIntervalEnd = endStr || undefined
  } else if (repeatMode.value === 'crontab') {
    const cronStr = crontab.value.trim()
    const fields = cronStr.split(/\s+/).filter(Boolean)
    if (fields.length !== 5 || fields.some((f) => !CRON_FIELD_RE.test(f))) {
      error.value = '请输入标准 5 段 Cron 表达式（仅支持数字与 * , - /）'
      return
    }
    cleanCron = fields.join(' ')
  }

  // 清洗最大输出 Token，防止输入框清空后序列化为 NaN/空字符串
  const rawMaxTokens = Number(maxOutputTokens.value)
  const cleanMaxTokens =
    Number.isFinite(rawMaxTokens) && rawMaxTokens > 0
      ? Math.min(512, Math.max(4, Math.round(rawMaxTokens)))
      : 64

  const task: WakeupTask = {
    id: props.editingTask?.id ?? '',
    name: name.value.trim(),
    enabled: enabled.value,
    lastRunAt: props.editingTask?.lastRunAt,
    createdAt: props.editingTask?.createdAt ?? 0,
    schedule: {
      repeatMode: repeatMode.value,
      dailyTimes: repeatMode.value === 'daily' ? parsedTimes : undefined,
      weeklyDays: repeatMode.value === 'weekly' ? [...weeklyDays.value] : undefined,
      weeklyTimes: repeatMode.value === 'weekly' ? parsedTimes : undefined,
      intervalHours: cleanIntervalHours,
      intervalStartTime: cleanIntervalStart,
      intervalEndTime: cleanIntervalEnd,
      crontab: cleanCron,
      wakeOnReset: repeatMode.value === 'quota_reset',
      selectedAccounts: cleanAccounts,
      selectedModels: [...selectedModels.value],
      customPrompt: customPrompt.value.trim() || 'hi',
      maxOutputTokens: cleanMaxTokens,
    },
  }

  saving.value = true
  try {
    await wakeupStore.upsertTask(task)
    emit('close')
  } catch {
    // wakeupStore 内已通过 toast 提示失败，保留弹窗便于修正后重试
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <BaseModal
    :open="open"
    :title="editingTask ? '编辑唤醒任务' : '新建定时唤醒任务'"
    subtitle="定时向指定账号发送极轻请求以触发配额重置计时窗口"
    width-class="w-[560px]"
    :prevent-close="saving"
    :close-on-outside="!saving"
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
          <AppSelect
            id="wk-mode"
            :model-value="repeatMode"
            :options="repeatModeOptions"
            block
            @update:model-value="setRepeatMode"
          />
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
            v-if="appStore.accounts.length > 0"
            type="button"
            class="text-[11px] cursor-pointer hover:underline"
            style="color: var(--accent)"
            @click="
              selectedAccounts = allAccountsSelected
                ? []
                : appStore.accounts.map((a) => a.id)
            "
          >
            {{ allAccountsSelected ? '取消全选' : '全选账号' }}
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
            :key="m.key"
            type="button"
            :title="m.canonicalModelId"
            class="px-2.5 py-1 rounded-lg border text-[11px] font-medium cursor-pointer transition-colors"
            :style="
              isCandidateSelected(m)
                ? { borderColor: 'var(--accent)', backgroundColor: 'var(--accent-soft)', color: 'var(--accent)' }
                : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-surface)', color: 'var(--text-secondary)' }
            "
            @click="toggleModelCandidate(m)"
          >
            {{ m.displayName }}
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
            max="512"
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
        :disabled="saving"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="emit('close')"
      >
        取消
      </button>
      <button
        type="button"
        :disabled="saving"
        class="px-4 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer disabled:opacity-50"
        style="background-color: var(--accent)"
        @click="handleSave"
      >
        {{ saving ? '保存中...' : '保存任务' }}
      </button>
    </template>
  </BaseModal>
</template>
