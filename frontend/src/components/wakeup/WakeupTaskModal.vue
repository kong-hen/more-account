<script setup lang="ts">
// 新建 / 编辑定时唤醒任务弹窗：支持每日、每周、固定间隔、严格 5 段 Cron 与配额重置触发模式。
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
const NON_NEG_INT_RE = /^\d+$/

interface CronFieldRule {
  name: string
  min: number
  max: number
}

const CRON_FIELD_RULES: CronFieldRule[] = [
  { name: '分钟', min: 0, max: 59 },
  { name: '小时', min: 0, max: 23 },
  { name: '日期', min: 1, max: 31 },
  { name: '月份', min: 1, max: 12 },
  { name: '星期', min: 0, max: 7 },
]

// 与后端 ValidateCronExpr 保持 100% 一致的严格 5 段 Cron 校验（拒绝越界、倒置范围与非法步长）
function validateCronExpr(expr: string): string | null {
  const fields = expr.trim().split(/\s+/).filter(Boolean)
  if (fields.length !== 5) {
    return `Cron 表达式必须包含 5 段（分 时 日 月 周），当前为 ${fields.length} 段`
  }
  for (let i = 0; i < 5; i++) {
    const rule = CRON_FIELD_RULES[i]
    const parts = fields[i].split(',')
    for (const rawPart of parts) {
      const part = rawPart.trim()
      if (!part) {
        return `Cron 第 ${i + 1} 段（${rule.name}）包含空逗号分隔项`
      }
      const slashParts = part.split('/')
      if (slashParts.length > 2) {
        return `Cron 第 ${i + 1} 段（${rule.name}）包含多余的 '/'：${part}`
      }
      const rangePart = slashParts[0].trim()
      if (slashParts.length === 2) {
        const stepStr = slashParts[1].trim()
        if (!rangePart || !NON_NEG_INT_RE.test(stepStr)) {
          return `Cron 第 ${i + 1} 段（${rule.name}）步长非法：${part}`
        }
        const step = Number(stepStr)
        if (step <= 0 || step > rule.max) {
          return `Cron 第 ${i + 1} 段（${rule.name}）步长需为 1–${rule.max} 的正整数：${part}`
        }
      }
      if (rangePart === '*') {
        continue
      }
      if (rangePart.includes('-')) {
        const bounds = rangePart.split('-')
        if (bounds.length !== 2) {
          return `Cron 第 ${i + 1} 段（${rule.name}）范围语法非法：${part}`
        }
        const loStr = bounds[0].trim()
        const hiStr = bounds[1].trim()
        if (!NON_NEG_INT_RE.test(loStr) || !NON_NEG_INT_RE.test(hiStr)) {
          return `Cron 第 ${i + 1} 段（${rule.name}）范围必须为整数：${part}`
        }
        const lo = Number(loStr)
        const hi = Number(hiStr)
        if (lo < rule.min || lo > rule.max || hi < rule.min || hi > rule.max) {
          return `Cron 第 ${i + 1} 段（${rule.name}）取值超出 ${rule.min}–${rule.max}：${part}`
        }
        if (lo > hi) {
          return `Cron 第 ${i + 1} 段（${rule.name}）范围起始值不能大于结束值：${part}`
        }
        continue
      }
      if (!NON_NEG_INT_RE.test(rangePart)) {
        return `Cron 第 ${i + 1} 段（${rule.name}）包含非法字符：${part}`
      }
      const exact = Number(rangePart)
      if (exact < rule.min || exact > rule.max) {
        return `Cron 第 ${i + 1} 段（${rule.name}）取值超出 ${rule.min}–${rule.max}：${part}`
      }
    }
  }
  return null
}

// 默认全选操作优先覆盖所有可用（非禁用）账号；若账号池全部为禁用态则回退全部账号
const selectableAccountsForSelectAll = computed(() => {
  const enabledList = appStore.accounts.filter((a) => !a.disabled)
  return enabledList.length > 0 ? enabledList : appStore.accounts
})

const hasDisabledAccounts = computed(() => appStore.accounts.some((a) => a.disabled))

const allSelectableAccountsChecked = computed(() => {
  const targets = selectableAccountsForSelectAll.value
  return targets.length > 0 && targets.every((a) => selectedAccounts.value.includes(a.id))
})

function toggleSelectAllAccounts() {
  const targets = selectableAccountsForSelectAll.value
  if (allSelectableAccountsChecked.value) {
    selectedAccounts.value = []
  } else {
    selectedAccounts.value = targets.map((a) => a.id)
  }
}

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

// 汇总账号池中已探测到的模型列表（仅当账号池尚无模型数据时补充常用保底项），并合并同一模型的不同思考深度；
// 同时显式保留旧任务中可能已被过滤掉的退役模型 ID，确保界面可见且可手动取消，杜绝“幽灵选中”。
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

  const merged = mergeModelList(Array.from(byName.values()))
  const coveredNames = new Set<string>()
  for (const group of merged) {
    for (const v of group.variants) {
      coveredNames.add(v.name)
    }
  }

  // 若旧任务保留了已被 mergeModelList 过滤掉的退役模型（如 gemini-3-flash），显式追加为可取消条目
  for (const sel of selectedModels.value) {
    if (sel && !coveredNames.has(sel)) {
      coveredNames.add(sel)
      merged.push({
        key: `legacy:${sel.toLowerCase()}`,
        baseId: sel,
        displayName: `${sel}（旧配置）`,
        percentage: 100,
        resetTime: '',
        hasMixedPercentages: false,
        canonicalModelId: sel,
        variants: [
          {
            name: sel,
            displayName: sel,
            depthLabel: '旧配置',
            depthRank: 0,
            percentage: 100,
            resetTime: '',
          },
        ],
      })
    }
  }
  return merged
})

function isCandidateSelected(candidate: MergedModelQuota): boolean {
  return candidate.variants.some((v) => selectedModels.value.includes(v.name))
}

const selectedCandidateCount = computed(
  () => modelCandidates.value.filter((c) => isCandidateSelected(c)).length,
)

function toggleModelCandidate(candidate: MergedModelQuota) {
  const variantNames = new Set(candidate.variants.map((v) => v.name))
  if (isCandidateSelected(candidate)) {
    selectedModels.value = selectedModels.value.filter((id) => !variantNames.has(id))
  } else {
    selectedModels.value = Array.from(new Set([...selectedModels.value, candidate.canonicalModelId]))
  }
}

watch(
  () => [props.open, props.editingTask] as const,
  ([isOpen, task]) => {
    if (!isOpen) return
    error.value = ''
    saving.value = false
    // 第一时间清空旧选中模型，防止新建任务读取 modelCandidates 时串台上一次编辑残留的自定义/退役模型
    selectedModels.value = []

    const validAccountIds = new Set(appStore.accounts.map((a) => a.id))
    const fallbackModel = pickPreferredWakeupModel(modelCandidates.value)

    if (task) {
      name.value = task.name
      enabled.value = task.enabled
      repeatMode.value = task.schedule.repeatMode || 'daily'
      timesText.value =
        (task.schedule.repeatMode === 'weekly'
          ? task.schedule.weeklyTimes
          : task.schedule.dailyTimes
        )?.join(', ') || '09:00'
      weeklyDays.value = task.schedule.weeklyDays?.length ? [...task.schedule.weeklyDays] : [1, 2, 3, 4, 5]
      intervalHours.value = task.schedule.intervalHours || 5
      // 编辑已有 interval 任务时完整保留用户刻意留空（全天）的时间窗，不再强行覆写为 08:00/22:00
      if (task.schedule.repeatMode === 'interval') {
        intervalStartTime.value = task.schedule.intervalStartTime ?? ''
        intervalEndTime.value = task.schedule.intervalEndTime ?? ''
      } else {
        intervalStartTime.value = task.schedule.intervalStartTime || '08:00'
        intervalEndTime.value = task.schedule.intervalEndTime || '22:00'
      }
      crontab.value = task.schedule.crontab || '0 9 * * *'
      customPrompt.value = task.schedule.customPrompt || 'hi'
      maxOutputTokens.value = task.schedule.maxOutputTokens || 64
      // 过滤掉已删除的无效账号 ID，避免勾选计数与全选状态失真
      selectedAccounts.value = (task.schedule.selectedAccounts ?? []).filter((id) => validAccountIds.has(id))
      selectedModels.value = task.schedule.selectedModels?.length
        ? Array.from(new Set(task.schedule.selectedModels.filter(Boolean)))
        : [fallbackModel]
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
      maxOutputTokens.value = 64
      const curAcc = appStore.accounts.find((a) => a.id === appStore.currentAccountId)
      selectedAccounts.value = curAcc && !curAcc.disabled ? [curAcc.id] : []
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
  const cleanModels = Array.from(new Set(selectedModels.value.map((m) => m.trim()).filter(Boolean)))
  if (cleanModels.length === 0) {
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
    const cronErr = validateCronExpr(crontab.value)
    if (cronErr) {
      error.value = cronErr
      return
    }
    cleanCron = crontab.value.trim().split(/\s+/).filter(Boolean).join(' ')
  }

  // 清洗最大输出 Token，防止输入框清空后序列化为 NaN/空字符串
  const rawMaxTokens = Number(maxOutputTokens.value)
  const cleanMaxTokens =
    Number.isFinite(rawMaxTokens) && rawMaxTokens > 0
      ? Math.min(512, Math.max(4, Math.round(rawMaxTokens)))
      : 64

  const sortedWeeklyDays = [...weeklyDays.value].sort((a, b) => (a === 0 ? 7 : a) - (b === 0 ? 7 : b))

  const task: WakeupTask = {
    id: props.editingTask?.id ?? '',
    name: name.value.trim(),
    enabled: enabled.value,
    lastRunAt: props.editingTask?.lastRunAt,
    createdAt: props.editingTask?.createdAt ?? 0,
    schedule: {
      repeatMode: repeatMode.value,
      dailyTimes: repeatMode.value === 'daily' ? parsedTimes : undefined,
      weeklyDays: repeatMode.value === 'weekly' ? sortedWeeklyDays : undefined,
      weeklyTimes: repeatMode.value === 'weekly' ? parsedTimes : undefined,
      intervalHours: cleanIntervalHours,
      intervalStartTime: cleanIntervalStart,
      intervalEndTime: cleanIntervalEnd,
      crontab: cleanCron,
      wakeOnReset: repeatMode.value === 'quota_reset',
      selectedAccounts: cleanAccounts,
      selectedModels: cleanModels,
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
        class="p-3 rounded-xl border space-y-2"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div class="grid grid-cols-3 gap-2.5">
          <div>
            <label for="wk-interval-hours" class="block text-[11px] mb-1" style="color: var(--text-muted)">间隔小时数</label>
            <input
              id="wk-interval-hours"
              v-model.number="intervalHours"
              type="number"
              min="1"
              max="24"
              class="w-full px-2.5 py-1.5 text-xs font-mono-num rounded-lg border"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
          </div>
          <div>
            <label for="wk-interval-start" class="block text-[11px] mb-1" style="color: var(--text-muted)">窗口开始</label>
            <input
              id="wk-interval-start"
              v-model="intervalStartTime"
              type="text"
              placeholder="留空默认 00:00"
              class="w-full px-2.5 py-1.5 text-xs font-mono-num rounded-lg border"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
          </div>
          <div>
            <label for="wk-interval-end" class="block text-[11px] mb-1" style="color: var(--text-muted)">窗口结束</label>
            <input
              id="wk-interval-end"
              v-model="intervalEndTime"
              type="text"
              placeholder="留空默认 23:59"
              class="w-full px-2.5 py-1.5 text-xs font-mono-num rounded-lg border"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
          </div>
        </div>
        <p class="text-[11px]" style="color: var(--text-muted)">
          单边留空将自动按 00:00 / 23:59 补齐，两端均留空表示全天生效（支持跨午夜如 22:00–06:00）。
        </p>
      </div>

      <div
        v-else-if="repeatMode === 'crontab'"
        class="p-3 rounded-xl border space-y-1.5"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <label for="wk-cron" class="block text-[11.5px]" style="color: var(--text-muted)">
          标准 5 段 Cron 表达式（分 时 日 月 周，例如 0 9 * * 1-5，周日支持 0 或 7）
        </label>
        <input
          id="wk-cron"
          v-model="crontab"
          type="text"
          placeholder="0 9 * * 1-5"
          class="w-full px-3 py-1.5 text-xs font-mono-num rounded-lg border"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
        />
        <p class="text-[11px]" style="color: var(--text-muted)">
          支持 *、*/步长、起-止、逗号分隔；日期与星期同时限定时按 AND 匹配。
        </p>
      </div>

      <div
        v-else-if="repeatMode === 'quota_reset'"
        class="p-3 rounded-xl border text-[11.5px] leading-relaxed"
        style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-secondary)"
      >
        配额重置触发依赖后台配额自动刷新，请确保在全局设置中未关闭自动刷新；多账号任务仅会独立唤醒实际发生配额重置的账号，避免误触发其他账号。
      </div>

      <!-- 目标账号多选 -->
      <div class="space-y-1.5">
        <div class="flex items-center justify-between gap-2">
          <span class="text-xs font-medium" style="color: var(--text-secondary)">
            目标账号 ({{ selectedAccounts.length }})
          </span>
          <button
            v-if="appStore.accounts.length > 0"
            type="button"
            class="text-[11px] cursor-pointer hover:underline"
            style="color: var(--accent)"
            @click="toggleSelectAllAccounts"
          >
            {{
              allSelectableAccountsChecked
                ? '取消全选'
                : hasDisabledAccounts
                  ? '全选可用账号'
                  : '全选账号'
            }}
          </button>
        </div>
        <div
          class="max-h-32 overflow-y-auto custom-scroll p-2 rounded-xl border space-y-1"
          style="border-color: var(--border-subtle); background-color: var(--bg-app)"
        >
          <label
            v-for="acc in appStore.accounts"
            :key="acc.id"
            class="flex items-center justify-between gap-2 px-2 py-1 rounded-lg text-xs cursor-pointer hover:bg-[var(--bg-hover)]"
            :class="acc.disabled ? 'opacity-75' : ''"
          >
            <span class="flex items-center gap-2 min-w-0">
              <input
                type="checkbox"
                :checked="selectedAccounts.includes(acc.id)"
                class="rounded accent-[var(--accent)] shrink-0"
                @change="selectedAccounts = toggleInArray(selectedAccounts, acc.id)"
              />
              <span class="truncate" style="color: var(--text-primary)">
                {{ maskEmail(acc.email, appStore.settings.privacyMode) }}
              </span>
            </span>
            <span
              v-if="acc.disabled"
              class="px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0"
              style="background-color: var(--danger-soft); color: var(--danger-text)"
            >
              已禁用
            </span>
          </label>
          <p v-if="appStore.accounts.length === 0" class="text-xs text-center py-3" style="color: var(--text-muted)">
            暂无可选账号
          </p>
        </div>
        <p v-if="hasDisabledAccounts" class="text-[11px]" style="color: var(--text-muted)">
          后台定时调度会自动跳过“已禁用”账号，请重新授权恢复后再启用。
        </p>
      </div>

      <!-- 模型多选 -->
      <div class="space-y-1.5">
        <span class="text-xs font-medium block" style="color: var(--text-secondary)">
          唤醒模型 ({{ selectedCandidateCount }})
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
      <div class="space-y-1.5">
        <div class="grid grid-cols-3 gap-3">
          <div class="col-span-2">
            <label for="wk-prompt" class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">唤醒提示词</label>
            <input
              id="wk-prompt"
              v-model="customPrompt"
              type="text"
              placeholder="hi"
              class="w-full px-3 py-1.5 text-xs rounded-lg border"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
          </div>
          <div>
            <label for="wk-max-tokens" class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">最大输出 Token</label>
            <input
              id="wk-max-tokens"
              v-model.number="maxOutputTokens"
              type="number"
              min="4"
              max="512"
              class="w-full px-3 py-1.5 text-xs font-mono-num rounded-lg border"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
          </div>
        </div>
        <p class="text-[11px]" style="color: var(--text-muted)">
          思考模型（如 Gemini 3/4、Claude、Argon/Carbon 及占位模型）后端将自动抬升输出上限至至少 256 Token，防止思考阶段提前截断。
        </p>
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
