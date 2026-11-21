<script setup lang="ts">
// 账号卡片视图项：展示当前激活标识、脱敏邮箱、订阅与自建分组、核心模型配额条与一键切换操作。
import { computed } from 'vue'
import {
  RefreshCw,
  ArrowRightLeft,
  AlertTriangle,
  ShieldCheck,
  Clock,
  Check,
  KeyRound,
} from 'lucide-vue-next'
import QuotaBar from '@/components/common/QuotaBar.vue'
import { useAppStore } from '@/stores/appStore'
import { maskEmail } from '@/utils/privacy'
import {
  formatResetCountdown,
  getEarliestResetTime,
  getQuotaTone,
  getTierLabel,
  isAccountAbnormal,
  isAccountLowQuota,
  summarizeQuotaCategories,
} from '@/utils/quota'
import type { Account } from '@/types/models'

const props = defineProps<{
  account: Account
  selected: boolean
  activeDetail: boolean
}>()

const emit = defineEmits<{
  (e: 'open-detail', account: Account): void
  (e: 'toggle-select', accountId: string): void
}>()

const appStore = useAppStore()

const isCurrent = computed(() => appStore.currentAccountId === props.account.id)
const isRefreshing = computed(() => Boolean(appStore.refreshingIds[props.account.id]))
const isSwitching = computed(() => appStore.switchingId === props.account.id)
const tier = computed(() => getTierLabel(props.account))
const abnormal = computed(() => isAccountAbnormal(props.account))
const lowQuota = computed(() => isAccountLowQuota(props.account, appStore.settings.lowQuotaThreshold))
const quotaCategories = computed(() => summarizeQuotaCategories(props.account))
const earliestReset = computed(() => getEarliestResetTime(props.account))
const groupInfo = computed(() =>
  props.account.groupId ? appStore.groupMap.get(props.account.groupId) ?? null : null,
)
const hasCredentials = computed(() =>
  Boolean(props.account.twoFactorSecret || props.account.accountPassword),
)

function quotaValueStyle(percentage: number) {
  const tone = getQuotaTone(percentage)
  if (tone === 'danger') return { color: 'var(--danger-text)' }
  if (tone === 'warning') return { color: 'var(--warning-text)' }
  return { color: 'var(--text-primary)' }
}
</script>

<template>
  <article
    class="group relative flex flex-col justify-between p-4 rounded-2xl border cursor-pointer transition-colors"
    :style="{
      backgroundColor: 'var(--bg-surface)',
      borderColor: activeDetail
        ? 'var(--accent)'
        : isCurrent
          ? 'var(--accent-border)'
          : 'var(--border-subtle)',
      boxShadow: 'var(--shadow-card)',
    }"
    @click="emit('open-detail', account)"
  >
    <!-- 顶部信息行 -->
    <div class="space-y-2.5">
      <div class="flex items-start justify-between gap-2">
        <div class="flex items-start gap-2.5 min-w-0">
          <!-- 多选复选框（平时悬浮或已勾选可见，保持卡片干净） -->
          <button
            type="button"
            :aria-label="selected ? '取消选中' : '选中账号'"
            class="mt-0.5 w-4 h-4 rounded border flex items-center justify-center shrink-0 cursor-pointer transition-opacity"
            :class="selected ? 'opacity-100' : 'opacity-0 group-hover:opacity-100'"
            :style="
              selected
                ? { backgroundColor: 'var(--accent)', borderColor: 'var(--accent)', color: '#fff' }
                : { borderColor: 'var(--border-strong)', backgroundColor: 'var(--bg-surface)' }
            "
            @click.stop="emit('toggle-select', account.id)"
          >
            <Check v-if="selected" class="w-3 h-3" />
          </button>

          <div class="min-w-0">
            <div class="flex items-center gap-1.5 min-w-0">
              <span
                v-if="isCurrent"
                class="w-2 h-2 rounded-full shrink-0"
                style="background-color: var(--success)"
                title="当前正在使用"
              />
              <h3 class="text-[13.5px] font-semibold truncate" style="color: var(--text-primary)">
                {{ maskEmail(account.email, appStore.settings.privacyMode) }}
              </h3>
            </div>
            <p v-if="account.notes" class="mt-0.5 text-[11.5px] truncate" style="color: var(--text-muted)">
              {{ account.notes }}
            </p>
          </div>
        </div>

        <!-- 订阅等级徽章 -->
        <span
          v-if="tier !== 'UNKNOWN'"
          class="px-1.5 py-0.5 rounded-md text-[10.5px] font-semibold tracking-wide shrink-0"
          :style="
            tier === 'ULTRA'
              ? { backgroundColor: 'var(--accent-soft)', color: 'var(--accent)' }
              : tier === 'PRO'
                ? { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
                : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-secondary)' }
          "
        >
          {{ tier }}
        </span>
      </div>

      <!-- 元数据胶囊行：自建分组 + 状态提示 + 2FA/密码小图标 -->
      <div class="flex items-center flex-wrap gap-1.5">
        <span
          v-if="groupInfo"
          class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[11px] font-medium"
          style="background-color: var(--bg-subtle); color: var(--text-secondary)"
        >
          <span class="w-1.5 h-1.5 rounded-full" :style="{ backgroundColor: groupInfo.color }" />
          {{ groupInfo.name }}
        </span>
        <span
          v-else
          class="inline-flex items-center px-2 py-0.5 rounded-full text-[11px]"
          style="background-color: var(--bg-subtle); color: var(--text-muted)"
        >
          未分组
        </span>

        <span
          v-if="abnormal"
          class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium"
          style="background-color: var(--danger-soft); color: var(--danger-text)"
        >
          <AlertTriangle class="w-3 h-3" />
          {{ account.disabled ? '令牌失效' : account.quotaError?.reason === 'VALIDATION_REQUIRED' ? '需验证' : '异常' }}
        </span>
        <span
          v-else-if="lowQuota"
          class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium"
          style="background-color: var(--warning-soft); color: var(--warning-text)"
        >
          低配额
        </span>

        <span
          v-if="hasCredentials"
          class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded-full text-[11px]"
          style="background-color: var(--bg-subtle); color: var(--text-muted)"
          title="已保存密码或 2FA 密钥"
        >
          <ShieldCheck v-if="account.twoFactorSecret" class="w-3 h-3" />
          <KeyRound v-else class="w-3 h-3" />
        </span>
      </div>

      <!-- 三大配额分类进度条：Gemini Argon / Gemini Flash / Claude/GPT（有 5h 和周额度则双窗并列，仅有周额度则单行展示） -->
      <div class="pt-1.5 space-y-2">
        <template v-if="quotaCategories.length > 0">
          <div
            v-for="cat in quotaCategories"
            :key="cat.key"
            class="px-2.5 py-2 rounded-xl border space-y-1.5"
            style="background-color: var(--bg-app); border-color: var(--border-subtle)"
          >
            <!-- 双窗口：同时存在 5h 与周额度 -->
            <template v-if="cat.fiveHour && cat.weekly">
              <div class="text-[11.5px] font-semibold leading-tight truncate" style="color: var(--text-primary)">
                {{ cat.label }}
              </div>
              <div class="grid grid-cols-2 gap-2.5">
                <div class="space-y-1 min-w-0">
                  <div class="flex items-baseline justify-between gap-1 text-[11px]">
                    <span class="inline-flex items-center gap-1 min-w-0">
                      <span
                        class="px-1 rounded text-[9.5px] font-semibold shrink-0"
                        style="background-color: var(--bg-subtle); color: var(--text-muted)"
                      >
                        5h
                      </span>
                      <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.fiveHour.percentage)">
                        {{ cat.fiveHour.percentage }}%
                      </span>
                    </span>
                    <span
                      v-if="cat.fiveHour.resetTime"
                      class="text-[10px] font-mono-num truncate"
                      style="color: var(--text-muted)"
                      :title="`5小时额度重置：${formatResetCountdown(cat.fiveHour.resetTime)}`"
                    >
                      {{ formatResetCountdown(cat.fiveHour.resetTime) }}
                    </span>
                  </div>
                  <QuotaBar :percentage="cat.fiveHour.percentage" />
                </div>

                <div class="space-y-1 min-w-0">
                  <div class="flex items-baseline justify-between gap-1 text-[11px]">
                    <span class="inline-flex items-center gap-1 min-w-0">
                      <span
                        class="px-1 rounded text-[9.5px] font-semibold shrink-0"
                        style="background-color: var(--bg-subtle); color: var(--text-muted)"
                      >
                        周
                      </span>
                      <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.weekly.percentage)">
                        {{ cat.weekly.percentage }}%
                      </span>
                    </span>
                    <span
                      v-if="cat.weekly.resetTime"
                      class="text-[10px] font-mono-num truncate"
                      style="color: var(--text-muted)"
                      :title="`周额度重置：${formatResetCountdown(cat.weekly.resetTime)}`"
                    >
                      {{ formatResetCountdown(cat.weekly.resetTime) }}
                    </span>
                  </div>
                  <QuotaBar :percentage="cat.weekly.percentage" />
                </div>
              </div>
            </template>

            <!-- 单窗口：仅有周额度（如 Gemini Argon / FREE 账号）或仅有 5h 额度 -->
            <template v-else>
              <div class="flex items-center justify-between gap-2 text-[11.5px]">
                <span class="inline-flex items-center gap-1.5 min-w-0">
                  <span class="font-semibold truncate" style="color: var(--text-primary)">{{ cat.label }}</span>
                  <span
                    class="px-1 rounded text-[9.5px] font-semibold shrink-0"
                    style="background-color: var(--bg-subtle); color: var(--text-muted)"
                  >
                    {{ cat.weekly ? '周' : '5h' }}
                  </span>
                </span>
                <span class="inline-flex items-center gap-1.5 shrink-0 font-mono-num">
                  <span
                    v-if="(cat.weekly ?? cat.fiveHour)?.resetTime"
                    class="text-[10.5px]"
                    style="color: var(--text-muted)"
                  >
                    {{ formatResetCountdown((cat.weekly ?? cat.fiveHour)?.resetTime) }}
                  </span>
                  <span
                    class="font-semibold"
                    :style="quotaValueStyle((cat.weekly ?? cat.fiveHour)!.percentage)"
                  >
                    {{ (cat.weekly ?? cat.fiveHour)!.percentage }}%
                  </span>
                </span>
              </div>
              <QuotaBar :percentage="(cat.weekly ?? cat.fiveHour)!.percentage" />
            </template>
          </div>
        </template>

        <div
          v-else-if="abnormal"
          class="py-3 px-3 rounded-xl text-xs leading-relaxed line-clamp-2"
          style="background-color: var(--danger-soft); color: var(--danger-text)"
        >
          {{ account.quotaError?.message || account.disabledReason || '账号访问受限，点击查看详情' }}
        </div>

        <div
          v-else
          class="py-3 px-3 rounded-xl text-xs text-center"
          style="background-color: var(--bg-app); color: var(--text-muted)"
        >
          尚未拉取配额，点击刷新获取
        </div>
      </div>
    </div>

    <!-- 底部操作栏 -->
    <div
      class="mt-4 pt-3 border-t flex items-center justify-between gap-2"
      style="border-color: var(--border-subtle)"
    >
      <span class="inline-flex items-center gap-1 text-[11px] tabular-nums truncate min-w-0" style="color: var(--text-muted)">
        <Clock class="w-3 h-3 shrink-0" />
        <span class="truncate">{{ earliestReset ? `重置 ${formatResetCountdown(earliestReset)}` : '暂无重置时间' }}</span>
      </span>

      <div class="flex items-center gap-1.5 shrink-0" @click.stop>
        <button
          type="button"
          title="刷新配额"
          aria-label="刷新配额"
          :disabled="isRefreshing"
          class="p-1.5 rounded-lg border shrink-0 cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50"
          style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-surface)"
          @click="appStore.refreshAccount(account.id)"
        >
          <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': isRefreshing }" />
        </button>

        <button
          type="button"
          :disabled="isSwitching"
          class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer transition-colors disabled:opacity-50"
          :style="
            isCurrent
              ? { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
              : { backgroundColor: 'var(--accent)', color: '#ffffff' }
          "
          @click="appStore.switchToAccount(account.id)"
        >
          <ArrowRightLeft class="w-3.5 h-3.5" />
          <span>{{ isSwitching ? '切换中...' : isCurrent ? '重新应用' : '一键切换' }}</span>
        </button>
      </div>
    </div>
  </article>
</template>
