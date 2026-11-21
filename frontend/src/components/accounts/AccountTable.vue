<script setup lang="ts">
// 紧凑数据表格视图：适合账号数量较多时高效率浏览、批量多选与快速切号。
import { computed } from 'vue'
import {
  RefreshCw,
  ArrowRightLeft,
  AlertTriangle,
  ShieldCheck,
  Check,
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
}>()

const emit = defineEmits<{
  (e: 'open-detail', account: Account): void
  (e: 'toggle-select', accountId: string): void
  (e: 'toggle-select-all'): void
}>()

const appStore = useAppStore()

const allSelected = computed(
  () =>
    props.accounts.length > 0 &&
    props.accounts.every((a) => appStore.selectedAccountIds.includes(a.id)),
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
    parts.push(`5h: ${cat.fiveHour.percentage}%${cat.fiveHour.resetTime ? ` (重置 ${formatResetCountdown(cat.fiveHour.resetTime)})` : ''}`)
  }
  if (cat.weekly) {
    parts.push(`周: ${cat.weekly.percentage}%${cat.weekly.resetTime ? ` (重置 ${formatResetCountdown(cat.weekly.resetTime)})` : ''}`)
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
            <th class="py-2.5 pl-4 pr-2 w-10">
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
            <th class="py-2.5 px-3 whitespace-nowrap">账号</th>
            <th class="py-2.5 px-3 whitespace-nowrap">分组</th>
            <th class="py-2.5 px-3 whitespace-nowrap">等级</th>
            <th class="py-2.5 px-3 min-w-[260px] whitespace-nowrap">额度概览（5h / 周）</th>
            <th class="py-2.5 px-3 whitespace-nowrap">最近重置</th>
            <th class="py-2.5 px-3 text-right pr-4 whitespace-nowrap">操作</th>
          </tr>
        </thead>
        <tbody class="divide-y text-xs" style="border-color: var(--border-subtle)">
          <tr
            v-for="acc in accounts"
            :key="acc.id"
            class="cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
            :style="
              appStore.activeDetailAccountId === acc.id
                ? { backgroundColor: 'var(--accent-soft)' }
                : {}
            "
            @click="emit('open-detail', acc)"
          >
            <td class="py-3 pl-4 pr-2" @click.stop>
              <button
                type="button"
                aria-label="选择账号"
                class="w-4 h-4 rounded border flex items-center justify-center cursor-pointer"
                :style="
                  appStore.selectedAccountIds.includes(acc.id)
                    ? { backgroundColor: 'var(--accent)', borderColor: 'var(--accent)', color: '#fff' }
                    : { borderColor: 'var(--border-strong)', backgroundColor: 'var(--bg-surface)' }
                "
                @click="emit('toggle-select', acc.id)"
              >
                <Check v-if="appStore.selectedAccountIds.includes(acc.id)" class="w-3 h-3" />
              </button>
            </td>

            <td class="py-3 px-3">
              <div class="flex items-center gap-2 min-w-0">
                <span
                  class="w-2 h-2 rounded-full shrink-0"
                  :style="{
                    backgroundColor:
                      appStore.currentAccountId === acc.id
                        ? 'var(--success)'
                        : isAccountAbnormal(acc)
                          ? 'var(--danger)'
                          : 'transparent',
                  }"
                />
                <div class="min-w-0">
                  <div class="font-medium flex items-center gap-1.5" style="color: var(--text-primary)">
                    <span class="truncate max-w-[220px]">
                      {{ maskEmail(acc.email, appStore.settings.privacyMode) }}
                    </span>
                    <ShieldCheck
                      v-if="acc.twoFactorSecret"
                      class="w-3.5 h-3.5 shrink-0"
                      style="color: var(--text-muted)"
                      title="已配置 2FA 动态码"
                    />
                  </div>
                  <div v-if="acc.notes" class="text-[11px] truncate max-w-[220px]" style="color: var(--text-muted)">
                    {{ acc.notes }}
                  </div>
                </div>
              </div>
            </td>

            <td class="py-3 px-3 whitespace-nowrap">
              <span
                v-if="acc.groupId && appStore.groupMap.get(acc.groupId)"
                class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] whitespace-nowrap"
                style="background-color: var(--bg-subtle); color: var(--text-secondary)"
              >
                <span
                  class="w-1.5 h-1.5 rounded-full shrink-0"
                  :style="{ backgroundColor: appStore.groupMap.get(acc.groupId)?.color }"
                />
                {{ appStore.groupMap.get(acc.groupId)?.name }}
              </span>
              <span v-else class="text-[11px] whitespace-nowrap" style="color: var(--text-muted)">未分组</span>
            </td>

            <td class="py-3 px-3 whitespace-nowrap">
              <span
                v-if="getTierLabel(acc) !== 'UNKNOWN'"
                class="px-1.5 py-0.5 rounded text-[10.5px] font-semibold"
                style="background-color: var(--bg-subtle); color: var(--text-secondary)"
              >
                {{ getTierLabel(acc) }}
              </span>
              <span v-else style="color: var(--text-muted)">—</span>
            </td>

            <td class="py-3 px-3">
              <div v-if="isAccountAbnormal(acc)" class="inline-flex items-center gap-1 text-[11.5px] font-medium whitespace-nowrap" style="color: var(--danger-text)">
                <AlertTriangle class="w-3.5 h-3.5 shrink-0" />
                <span>{{ acc.disabled ? '令牌失效' : '状态异常' }}</span>
              </div>
              <div v-else-if="summarizeQuotaCategories(acc).length > 0" class="flex items-center flex-wrap gap-1.5">
                <span
                  v-for="cat in summarizeQuotaCategories(acc)"
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
                    <span v-else style="color: var(--text-muted)">周 {{ appStore.refreshingIds[acc.id] ? '同步中' : '--' }}</span>
                  </template>
                  <template v-else-if="cat.weekly">
                    <span :style="{ color: toneTextColor(cat.weekly.percentage) }">周 {{ cat.weekly.percentage }}%</span>
                  </template>
                </span>
              </div>
              <span v-else class="text-[11px] whitespace-nowrap" style="color: var(--text-muted)">未拉取</span>
            </td>

            <td class="py-3 px-3 font-mono-num text-[11.5px] whitespace-nowrap" style="color: var(--text-muted)">
              {{ formatResetCountdown(getEarliestResetTime(acc)) }}
            </td>

            <td class="py-3 pl-3 pr-4 text-right whitespace-nowrap" @click.stop>
              <div class="inline-flex items-center gap-1.5">
                <button
                  type="button"
                  title="刷新配额"
                  aria-label="刷新配额"
                  :disabled="Boolean(appStore.refreshingIds[acc.id])"
                  class="p-1.5 rounded-lg border shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
                  style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-surface)"
                  @click="appStore.refreshAccount(acc.id)"
                >
                  <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': Boolean(appStore.refreshingIds[acc.id]) }" />
                </button>
                <button
                  type="button"
                  :disabled="appStore.switchingId === acc.id"
                  class="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-lg text-[11.5px] font-medium whitespace-nowrap shrink-0 cursor-pointer disabled:opacity-50"
                  :style="
                    appStore.currentAccountId === acc.id
                      ? { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
                      : { backgroundColor: 'var(--accent)', color: '#ffffff' }
                  "
                  @click="appStore.switchToAccount(acc.id)"
                >
                  <ArrowRightLeft class="w-3 h-3 shrink-0" />
                  <span>{{ appStore.currentAccountId === acc.id ? '已激活' : '切换' }}</span>
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
