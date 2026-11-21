<script setup lang="ts">
// 紧凑数据表格视图：适合账号数量较多时高效率浏览、批量多选、默认排序拖拽重排与快速切号。
import { computed } from 'vue'
import {
  RefreshCw,
  ArrowRightLeft,
  AlertTriangle,
  ShieldCheck,
  Check,
  GripVertical,
} from 'lucide-vue-next'
import { useAppStore } from '@/stores/appStore'
import { maskEmail } from '@/utils/privacy'
import {
  formatResetCountdown,
  getEarliestResetTime,
  getQuotaTone,
  getTierLabel,
  isAccountAbnormal,
  summarizeQuotaCategories,
  type QuotaCategorySummary,
} from '@/utils/quota'
import type { Account } from '@/types/models'

const props = defineProps<{
  accounts: Account[]
  reorderable?: boolean
  draggingId?: string | null
  dragOverId?: string | null
}>()

const emit = defineEmits<{
  (e: 'open-detail', account: Account): void
  (e: 'toggle-select', accountId: string): void
  (e: 'toggle-select-all'): void
  (e: 'drag-start', accountId: string, event: DragEvent): void
  (e: 'drag-over', accountId: string, event: DragEvent): void
  (e: 'drop', accountId: string, event: DragEvent): void
  (e: 'drag-end'): void
}>()

const appStore = useAppStore()

const allSelected = computed(
  () =>
    props.accounts.length > 0 &&
    props.accounts.every((a) => appStore.selectedAccountIds.includes(a.id)),
)

// 预先计算每行账号的配额分类、异常态、等级与最近重置时间，避免模板渲染期重复多次计算
const tableRows = computed(() =>
  props.accounts.map((acc) => ({
    account: acc,
    abnormal: isAccountAbnormal(acc),
    tier: getTierLabel(acc),
    categories: summarizeQuotaCategories(acc),
    earliestReset: getEarliestResetTime(acc),
    group: acc.groupId ? (appStore.groupMap.get(acc.groupId) ?? null) : null,
  })),
)

function toneDotColor(percentage: number): string {
  const tone = getQuotaTone(percentage)
  if (tone === 'danger') return 'var(--danger)'
  if (tone === 'warning') return 'var(--warning)'
  return 'var(--success)'
}

function toneTextColor(percentage: number): string {
  const tone = getQuotaTone(percentage)
  if (tone === 'danger') return 'var(--danger-text)'
  if (tone === 'warning') return 'var(--warning-text)'
  return 'var(--text-primary)'
}

function categoryTooltip(cat: QuotaCategorySummary): string {
  const parts: string[] = [cat.label]
  if (cat.fiveHour) {
    parts.push(`5h: ${cat.fiveHour.percentage}%${cat.fiveHour.resetTime ? ` (重置 ${formatResetCountdown(cat.fiveHour.resetTime, appStore.nowMs)})` : ''}`)
  }
  if (cat.weekly) {
    parts.push(`周: ${cat.weekly.percentage}%${cat.weekly.resetTime ? ` (重置 ${formatResetCountdown(cat.weekly.resetTime, appStore.nowMs)})` : ''}`)
  }
  return parts.join('\n')
}
</script>

<template>
  <div
    class="rounded-2xl border overflow-hidden"
    style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
  >
    <div class="overflow-x-auto custom-scroll">
      <table class="w-full text-left border-collapse">
        <thead>
          <tr
            class="border-b text-[11.5px] font-medium"
            style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-muted)"
          >
            <th v-if="reorderable" class="py-2.5 pl-2 pr-0 w-6" aria-label="拖拽排序" />
            <th class="py-2.5 pl-3 pr-2 w-9">
              <button
                type="button"
                aria-label="全选或取消全选"
                class="w-4 h-4 rounded border flex items-center justify-center cursor-pointer"
                :style="
                  allSelected
                    ? { backgroundColor: 'var(--accent)', borderColor: 'var(--accent)', color: '#fff' }
                    : { borderColor: 'var(--border-strong)', backgroundColor: 'var(--bg-surface)' }
                "
                @click="emit('toggle-select-all')"
              >
                <Check v-if="allSelected" class="w-3 h-3" />
              </button>
            </th>
            <th class="py-2.5 px-2.5 whitespace-nowrap">账号</th>
            <th class="py-2.5 px-2.5 whitespace-nowrap">分组</th>
            <th class="py-2.5 px-2.5 whitespace-nowrap">等级</th>
            <th class="py-2.5 px-2.5 min-w-[205px] whitespace-nowrap">额度概览（5h / 周）</th>
            <th class="py-2.5 px-2.5 whitespace-nowrap">最近重置</th>
            <th class="py-2.5 pl-2.5 pr-3 text-right whitespace-nowrap">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-[var(--border-hairline)] text-xs">
          <tr
            v-for="row in tableRows"
            :key="row.account.id"
            :draggable="Boolean(reorderable)"
            class="cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
            :class="[draggingId === row.account.id ? 'opacity-50' : '']"
            :style="
              dragOverId === row.account.id
                ? { boxShadow: 'inset 0 2px 0 0 var(--accent)', backgroundColor: 'var(--accent-soft)' }
                : appStore.activeDetailAccountId === row.account.id
                  ? { backgroundColor: 'var(--accent-soft)' }
                  : {}
            "
            @click="emit('open-detail', row.account)"
            @dragstart="reorderable && emit('drag-start', row.account.id, $event)"
            @dragover="reorderable && emit('drag-over', row.account.id, $event)"
            @drop="reorderable && emit('drop', row.account.id, $event)"
            @dragend="reorderable && emit('drag-end')"
          >
            <td v-if="reorderable" class="py-3 pl-2 pr-0 w-6" @click.stop>
              <span
                title="拖拽调整账号顺序"
                aria-label="拖拽调整账号顺序"
                class="inline-flex items-center justify-center p-0.5 rounded cursor-grab active:cursor-grabbing"
                style="color: var(--text-muted)"
              >
                <GripVertical class="w-3.5 h-3.5" />
              </span>
            </td>

            <td class="py-3 pl-3 pr-2" @click.stop>
              <button
                type="button"
                aria-label="选择账号"
                class="w-4 h-4 rounded border flex items-center justify-center cursor-pointer"
                :style="
                  appStore.selectedAccountIds.includes(row.account.id)
                    ? { backgroundColor: 'var(--accent)', borderColor: 'var(--accent)', color: '#fff' }
                    : { borderColor: 'var(--border-strong)', backgroundColor: 'var(--bg-surface)' }
                "
                @click="emit('toggle-select', row.account.id)"
              >
                <Check v-if="appStore.selectedAccountIds.includes(row.account.id)" class="w-3 h-3" />
              </button>
            </td>

            <td class="py-3 px-2.5">
              <div class="flex items-center gap-2 min-w-0">
                <span
                  class="w-2 h-2 rounded-full shrink-0"
                  :style="{
                    backgroundColor:
                      appStore.currentAccountId === row.account.id
                        ? 'var(--success)'
                        : row.abnormal
                          ? 'var(--danger)'
                          : 'transparent',
                  }"
                />
                <div class="min-w-0">
                  <div class="font-medium flex items-center gap-1.5" style="color: var(--text-primary)">
                    <span class="truncate max-w-[165px] xl:max-w-[220px]">
                      {{ maskEmail(row.account.email, appStore.settings.privacyMode) }}
                    </span>
                    <ShieldCheck
                      v-if="row.account.twoFactorSecret"
                      class="w-3.5 h-3.5 shrink-0"
                      style="color: var(--text-muted)"
                      title="已配置 2FA 动态码"
                    />
                  </div>
                  <div v-if="row.account.notes" class="text-[11px] truncate max-w-[165px] xl:max-w-[220px]" style="color: var(--text-muted)">
                    {{ row.account.notes }}
                  </div>
                </div>
              </div>
            </td>

            <td class="py-3 px-2.5 whitespace-nowrap">
              <span
                v-if="row.group"
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] whitespace-nowrap"
                style="background-color: var(--bg-subtle); color: var(--text-secondary)"
              >
                <span
                  class="w-1.5 h-1.5 rounded-full shrink-0"
                  :style="{ backgroundColor: row.group.color }"
                />
                {{ row.group.name }}
              </span>
              <span v-else class="text-[11px] whitespace-nowrap" style="color: var(--text-muted)">未分组</span>
            </td>

            <td class="py-3 px-2.5 whitespace-nowrap">
              <span
                v-if="row.tier !== 'UNKNOWN'"
                class="px-1.5 py-0.5 rounded text-[10.5px] font-semibold"
                style="background-color: var(--bg-subtle); color: var(--text-secondary)"
              >
                {{ row.tier }}
              </span>
              <span v-else style="color: var(--text-muted)">—</span>
            </td>

            <td class="py-3 px-2.5">
              <div v-if="row.abnormal" class="inline-flex items-center gap-1 text-[11.5px] font-medium whitespace-nowrap" style="color: var(--danger-text)">
                <AlertTriangle class="w-3.5 h-3.5 shrink-0" />
                <span>{{ row.account.disabled ? '令牌失效' : '状态异常' }}</span>
              </div>
              <div v-else-if="row.categories.length > 0" class="flex items-center flex-wrap gap-1.5">
                <span
                  v-for="cat in row.categories"
                  :key="cat.key"
                  :title="categoryTooltip(cat)"
                  class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border text-[11px] font-mono-num whitespace-nowrap"
                  style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-secondary)"
                >
                  <span class="w-1.5 h-1.5 rounded-full shrink-0" :style="{ backgroundColor: toneDotColor(cat.percentage) }" />
                  <span class="font-sans font-medium" style="color: var(--text-primary)">{{ cat.shortLabel }}</span>
                  <template v-if="cat.fiveHour">
                    <span :style="{ color: toneTextColor(cat.fiveHour.percentage) }">5h {{ cat.fiveHour.percentage }}%</span>
                    <span style="color: var(--text-muted)">/</span>
                    <span v-if="cat.weekly" :style="{ color: toneTextColor(cat.weekly.percentage) }">周 {{ cat.weekly.percentage }}%</span>
                    <span v-else style="color: var(--text-muted)">周 {{ appStore.refreshingIds[row.account.id] ? '同步中' : '--' }}</span>
                  </template>
                  <template v-else-if="cat.weekly">
                    <span :style="{ color: toneTextColor(cat.weekly.percentage) }">周 {{ cat.weekly.percentage }}%</span>
                  </template>
                </span>
              </div>
              <span v-else class="text-[11px] whitespace-nowrap" style="color: var(--text-muted)">未拉取</span>
            </td>

            <td class="py-3 px-2.5 font-mono-num text-[11.5px] whitespace-nowrap" style="color: var(--text-muted)">
              {{ formatResetCountdown(row.earliestReset, appStore.nowMs) }}
            </td>

            <td class="py-3 pl-2.5 pr-3 text-right whitespace-nowrap" @click.stop>
              <div class="inline-flex items-center gap-1.5">
                <button
                  type="button"
                  title="刷新配额"
                  aria-label="刷新配额"
                  :disabled="Boolean(appStore.refreshingIds[row.account.id])"
                  class="p-1.5 rounded-lg border shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
                  style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-surface)"
                  @click="appStore.refreshAccount(row.account.id)"
                >
                  <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': Boolean(appStore.refreshingIds[row.account.id]) }" />
                </button>
                <button
                  type="button"
                  :disabled="appStore.switchingId === row.account.id"
                  class="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-lg text-[11.5px] font-medium whitespace-nowrap shrink-0 cursor-pointer disabled:opacity-50"
                  :style="
                    appStore.currentAccountId === row.account.id
                      ? { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
                      : { backgroundColor: 'var(--accent)', color: '#ffffff' }
                  "
                  @click="appStore.switchToAccount(row.account.id)"
                >
                  <ArrowRightLeft class="w-3 h-3 shrink-0" />
                  <span>{{ appStore.currentAccountId === row.account.id ? '当前' : '切换' }}</span>
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
