<script setup lang="ts">
// 账号卡片视图项：精密子网格卡片结构（头部标识与元数据 / 聚合配额面板 / 底部快捷操作），
// 通过顶部标题按钮 ::after 拉伸整卡点击热区，兼顾键盘无障碍与独立按钮分层交互。
import { computed, ref } from 'vue'
import {
  RefreshCw,
  ArrowRightLeft,
  AlertTriangle,
  ShieldCheck,
  Clock,
  Check,
  KeyRound,
  GripVertical,
  ExternalLink,
  Copy,
} from 'lucide-vue-next'
import QuotaBar from '@/components/common/QuotaBar.vue'
import { useAppStore } from '@/stores/appStore'
import { api, extractErrorMessage } from '@/services/api'
import { copyToClipboard, maskEmail } from '@/utils/privacy'
import {
  formatResetCountdown,
  getAccountValidationUrl,
  getEarliestResetTime,
  getQuotaTone,
  getTierBadgeClass,
  getTierLabel,
  isAccountAbnormal,
  isAccountLowQuota,
  isAccountValidationRequired,
  summarizeQuotaCategories,
} from '@/utils/quota'
import type { Account } from '@/types/models'

const props = defineProps<{
  account: Account
  selected: boolean
  activeDetail: boolean
  reorderable?: boolean
  dragging?: boolean
  dragOver?: boolean
}>()

const emit = defineEmits<{
  (e: 'open-detail', account: Account): void
  (e: 'toggle-select', accountId: string): void
  (e: 'drag-start', accountId: string, event: DragEvent): void
  (e: 'drag-over', accountId: string, event: DragEvent): void
  (e: 'drop', accountId: string, event: DragEvent): void
  (e: 'drag-end'): void
}>()

const appStore = useAppStore()

const isCurrent = computed(() => appStore.currentAccountId === props.account.id)
const isRefreshing = computed(() => Boolean(appStore.refreshingIds[props.account.id]))
const isSwitching = computed(() => appStore.switchingId === props.account.id)
const hasAnySelection = computed(() => appStore.selectedAccountIds.length > 0)
const displayEmail = computed(() => maskEmail(props.account.email, appStore.settings.privacyMode))
const tier = computed(() => getTierLabel(props.account))
const abnormal = computed(() => isAccountAbnormal(props.account))
const validationRequired = computed(() => isAccountValidationRequired(props.account))
const validationUrl = computed(() => getAccountValidationUrl(props.account))
const copiedVerifyUrl = ref(false)
const lowQuota = computed(() =>
  isAccountLowQuota(props.account, appStore.settings.lowQuotaThreshold, appStore.nowMs),
)
const quotaCategories = computed(() => summarizeQuotaCategories(props.account, appStore.nowMs))
const earliestReset = computed(() => getEarliestResetTime(props.account, appStore.nowMs))
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

async function handleOpenValidationUrl() {
  if (!validationUrl.value) return
  try {
    await api.openExternalURL(validationUrl.value)
  } catch (err) {
    appStore.notify(`打开验证链接失败：${extractErrorMessage(err)}`, 'error')
  }
}

async function handleCopyValidationUrl() {
  if (!validationUrl.value) return
  if (await copyToClipboard(validationUrl.value)) {
    copiedVerifyUrl.value = true
    appStore.notify('已复制网页验证链接', 'success', 2000)
    window.setTimeout(() => {
      copiedVerifyUrl.value = false
    }, 1800)
  }
}
</script>

<template>
  <article
    :draggable="Boolean(reorderable)"
    class="account-card group p-3.5 rounded-xl cursor-pointer"
    :class="[
      isCurrent ? 'account-card--current' : '',
      activeDetail ? 'account-card--active' : '',
      dragOver ? 'account-card--dragover' : '',
      dragging ? 'opacity-50' : '',
    ]"
    @dragstart="reorderable && emit('drag-start', account.id, $event)"
    @dragover="reorderable && emit('drag-over', account.id, $event)"
    @drop="reorderable && emit('drop', account.id, $event)"
    @dragend="reorderable && emit('drag-end')"
  >
    <!-- 当前正在使用账号的顶部翠绿细指示条 -->
    <div
      v-if="isCurrent"
      class="absolute inset-x-0 top-0 h-[3px] pointer-events-none"
      style="background: linear-gradient(90deg, var(--success), #34d399)"
    />

    <!-- 轨道 1：卡片头部（邮箱标题顶格左对齐 + 右侧状态与套餐徽章 + 备注 + 元数据/勾选行） -->
    <header class="space-y-1.5 min-w-0 self-start">
      <div class="flex items-center justify-between gap-2 min-w-0">
        <h3 class="min-w-0 flex-1 flex items-center text-sm font-semibold leading-snug">
          <button
            type="button"
            class="account-card__primary text-left min-w-0 flex-1 truncate cursor-pointer"
            style="color: var(--text-primary)"
            :title="displayEmail"
            @click="emit('open-detail', account)"
          >
            {{ displayEmail }}
          </button>
        </h3>

        <!-- 右上角状态与套餐徽章：使用中脉冲标 + cockpit-tools 风格订阅等级 -->
        <div class="flex items-center gap-1.5 shrink-0">
          <span
            v-if="isCurrent"
            class="inline-flex items-center gap-1 px-2 py-[3px] rounded-full text-[0.625rem] leading-none font-semibold tracking-wide shrink-0"
            style="background-color: var(--success); color: #ffffff"
            title="本机当前正在使用此账号"
          >
            <span class="relative flex w-1.5 h-1.5">
              <span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-white opacity-75" />
              <span class="relative inline-flex rounded-full w-1.5 h-1.5 bg-white" />
            </span>
            <span>使用中</span>
          </span>

          <span
            v-if="tier !== 'UNKNOWN'"
            class="tier-badge shrink-0"
            :class="getTierBadgeClass(tier)"
          >
            {{ tier }}
          </span>
        </div>
      </div>

      <!-- 备注（可选，清晰单行） -->
      <p
        v-if="account.notes"
        class="text-xs leading-snug truncate"
        style="color: var(--text-muted)"
        :title="account.notes"
      >
        {{ account.notes }}
      </p>

      <!-- 元数据行：左侧分组 / 状态 / 凭据小标，右侧紧贴放置拖拽手柄与多选复选框 -->
      <div class="flex items-center justify-between gap-2 min-w-0">
        <div class="flex items-center flex-wrap gap-1.5 min-w-0">
          <span
            v-if="groupInfo"
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border text-[0.6875rem] leading-none font-medium"
            :style="
              isCurrent
                ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-secondary)', borderColor: 'var(--border-subtle)' }
                : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-secondary)', borderColor: 'transparent' }
            "
          >
            <span class="w-1.5 h-1.5 rounded-full shrink-0" :style="{ backgroundColor: groupInfo.color }" />
            <span class="truncate max-w-[7.5rem]">{{ groupInfo.name }}</span>
          </span>
          <span
            v-else
            class="inline-flex items-center px-2 py-0.5 rounded-md border text-[0.6875rem] leading-none"
            :style="
              isCurrent
                ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-muted)', borderColor: 'var(--border-subtle)' }
                : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-muted)', borderColor: 'transparent' }
            "
          >
            未分组
          </span>

          <span
            v-if="abnormal"
            class="inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[0.6875rem] leading-none font-medium"
            style="background-color: var(--danger-soft); color: var(--danger-text)"
          >
            <AlertTriangle class="w-3 h-3 shrink-0" />
            <span>{{ account.disabled ? '令牌失效' : validationRequired ? '需验证' : '异常' }}</span>
          </span>
          <span
            v-else-if="lowQuota"
            class="inline-flex items-center px-2 py-0.5 rounded-md text-[0.6875rem] leading-none font-medium"
            style="background-color: var(--warning-soft); color: var(--warning-text)"
          >
            低配额
          </span>

          <span
            v-if="hasCredentials"
            class="inline-flex items-center p-0.5 rounded text-[0.6875rem]"
            style="color: var(--text-muted)"
            title="已保存密码或 2FA 密钥"
          >
            <ShieldCheck v-if="account.twoFactorSecret" class="w-3.5 h-3.5" />
            <KeyRound v-else class="w-3.5 h-3.5" />
          </span>
        </div>

        <!-- 拖拽提示与批量勾选框：平时保持极简干净，悬停/聚焦或进入多选态时自然显现 -->
        <div class="flex items-center gap-1 shrink-0">
          <span
            v-if="reorderable"
            title="拖拽调整账号顺序"
            aria-label="拖拽调整账号顺序"
            class="account-card__interactive p-0.5 rounded cursor-grab active:cursor-grabbing opacity-0 group-hover:opacity-75 hover:!opacity-100 transition-opacity"
            style="color: var(--text-muted)"
          >
            <GripVertical class="w-3.5 h-3.5" />
          </span>

          <button
            type="button"
            :aria-label="selected ? '取消选中' : '选中账号'"
            :aria-pressed="selected"
            class="w-4 h-4 rounded border flex items-center justify-center shrink-0 cursor-pointer transition-opacity"
            :class="
              selected || hasAnySelection
                ? 'opacity-100'
                : 'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100'
            "
            :style="
              selected
                ? { backgroundColor: 'var(--accent)', borderColor: 'var(--accent)', color: '#fff' }
                : { borderColor: 'var(--border-strong)', backgroundColor: 'var(--bg-surface)' }
            "
            @click="emit('toggle-select', account.id)"
          >
            <Check v-if="selected" class="w-3 h-3" />
          </button>
        </div>
      </div>
    </header>

    <!-- 轨道 2：核心数据区（需网页验证提示 + 单一轻底聚合配额面板） -->
    <div class="account-card__body space-y-1.5 min-w-0 self-start">
      <!-- 需网页验证快捷操作栏 -->
      <div
        v-if="validationUrl"
        class="p-2 rounded-lg border space-y-1.5"
        style="background-color: var(--danger-soft); border-color: rgba(239, 68, 68, 0.24)"
      >
        <p class="text-[0.6875rem] leading-snug line-clamp-2 overflow-clip" style="color: var(--danger-text)">
          {{ account.quotaError?.message || '此账号需完成 Google 网页安全验证后方可正常调用' }}
        </p>
        <div class="flex items-center gap-1.5">
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2 py-0.5 rounded-md text-[0.6875rem] leading-snug font-medium text-white cursor-pointer transition-opacity hover:opacity-95"
            style="background-color: var(--danger)"
            @click="handleOpenValidationUrl"
          >
            <ExternalLink class="w-2.5 h-2.5" />
            <span>网页验证</span>
          </button>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2 py-0.5 rounded-md border text-[0.6875rem] leading-snug font-medium cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
            style="border-color: rgba(239, 68, 68, 0.35); color: var(--danger-text); background-color: var(--bg-surface)"
            @click="handleCopyValidationUrl"
          >
            <Check v-if="copiedVerifyUrl" class="w-2.5 h-2.5" style="color: var(--success)" />
            <Copy v-else class="w-2.5 h-2.5" />
            <span>{{ copiedVerifyUrl ? '已复制' : '复制链接' }}</span>
          </button>
        </div>
      </div>

      <!-- 聚合配额面板：单一柔和底板 + 细分割线，保持精致小巧的模型名与纤细额度进度条 -->
      <template v-if="quotaCategories.length > 0 || !validationUrl">
        <div
          v-if="quotaCategories.length > 0"
          class="rounded-lg border px-2.5 py-1.5 divide-y divide-[var(--border-hairline)]"
          style="background-color: var(--bg-app); border-color: var(--border-hairline)"
        >
          <div
            v-for="cat in quotaCategories"
            :key="cat.key"
            class="py-1.5 first:pt-0.5 last:pb-0.5 space-y-1"
          >
            <!-- 双窗口：5 小时与周额度左右并列，中间以细竖线明确视觉分组 -->
            <template v-if="cat.fiveHour">
              <div
                class="text-[0.65625rem] font-medium leading-tight truncate"
                style="color: var(--text-secondary)"
              >
                {{ cat.label }}
              </div>
              <div class="account-card__quota-split grid grid-cols-2">
                <div class="space-y-1 min-w-0 pr-2">
                  <div class="flex items-baseline justify-between gap-1 text-[0.65625rem] leading-none">
                    <span class="inline-flex items-baseline gap-1 min-w-0">
                      <span class="text-[0.59375rem] font-medium shrink-0" style="color: var(--text-muted)">
                        5h
                      </span>
                      <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.fiveHour.percentage)">
                        {{ cat.fiveHour.percentage }}%
                      </span>
                    </span>
                    <span
                      v-if="cat.fiveHour.resetTime"
                      class="account-card__quota-reset text-[0.59375rem] font-mono-num truncate"
                      style="color: var(--text-muted)"
                      :title="`5小时额度重置：${formatResetCountdown(cat.fiveHour.resetTime, appStore.nowMs)}`"
                    >
                      {{ formatResetCountdown(cat.fiveHour.resetTime, appStore.nowMs) }}
                    </span>
                  </div>
                  <QuotaBar :percentage="cat.fiveHour.percentage" height="h-[3px]" />
                </div>

                <div class="space-y-1 min-w-0 pl-2 border-l" style="border-color: var(--border-hairline)">
                  <template v-if="cat.weekly">
                    <div class="flex items-baseline justify-between gap-1 text-[0.65625rem] leading-none">
                      <span class="inline-flex items-baseline gap-1 min-w-0">
                        <span class="text-[0.59375rem] font-medium shrink-0" style="color: var(--text-muted)">
                          周
                        </span>
                        <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.weekly.percentage)">
                          {{ cat.weekly.percentage }}%
                        </span>
                      </span>
                      <span
                        v-if="cat.weekly.resetTime"
                        class="account-card__quota-reset text-[0.59375rem] font-mono-num truncate"
                        style="color: var(--text-muted)"
                        :title="`周额度重置：${formatResetCountdown(cat.weekly.resetTime, appStore.nowMs)}`"
                      >
                        {{ formatResetCountdown(cat.weekly.resetTime, appStore.nowMs) }}
                      </span>
                    </div>
                    <QuotaBar :percentage="cat.weekly.percentage" height="h-[3px]" />
                  </template>
                  <template v-else>
                    <div class="flex items-baseline justify-between gap-1 text-[0.65625rem] leading-none">
                      <span class="inline-flex items-baseline gap-1 min-w-0">
                        <span class="text-[0.59375rem] font-medium shrink-0" style="color: var(--text-muted)">
                          周
                        </span>
                        <span class="font-mono-num font-medium" style="color: var(--text-muted)">
                          {{ isRefreshing ? '同步中' : '--' }}
                        </span>
                      </span>
                    </div>
                    <div
                      class="w-full h-[3px] rounded-full overflow-clip"
                      style="background-color: var(--bg-subtle)"
                      title="点击刷新获取云端周额度"
                    />
                  </template>
                </div>
              </div>
            </template>

            <!-- 单窗口：仅有周额度 -->
            <template v-else-if="cat.weekly">
              <div class="flex items-baseline justify-between gap-1.5 text-[0.65625rem] leading-tight">
                <span class="inline-flex items-baseline gap-1 min-w-0">
                  <span class="font-medium truncate" style="color: var(--text-secondary)">{{ cat.label }}</span>
                  <span class="text-[0.59375rem] font-medium shrink-0" style="color: var(--text-muted)">周</span>
                </span>
                <span class="inline-flex items-baseline gap-1.5 shrink-0 font-mono-num">
                  <span
                    v-if="cat.weekly.resetTime"
                    class="account-card__quota-reset text-[0.59375rem]"
                    style="color: var(--text-muted)"
                  >
                    {{ formatResetCountdown(cat.weekly.resetTime, appStore.nowMs) }}
                  </span>
                  <span class="font-semibold" :style="quotaValueStyle(cat.weekly.percentage)">
                    {{ cat.weekly.percentage }}%
                  </span>
                </span>
              </div>
              <QuotaBar :percentage="cat.weekly.percentage" height="h-[3px]" />
            </template>
          </div>
        </div>

        <div
          v-else-if="abnormal"
          class="py-2.5 px-3 rounded-lg text-xs leading-relaxed line-clamp-2 overflow-clip"
          style="background-color: var(--danger-soft); color: var(--danger-text)"
        >
          {{ account.quotaError?.message || account.disabledReason || '账号访问受限，点击查看详情' }}
        </div>

        <div
          v-else
          class="py-3 px-3 rounded-lg border text-xs text-center"
          style="background-color: var(--bg-app); border-color: var(--border-hairline); color: var(--text-muted)"
        >
          尚未拉取配额，点击刷新获取
        </div>
      </template>
    </div>

    <!-- 轨道 3：底部操作栏 -->
    <footer
      class="pt-2.5 border-t flex items-center justify-between gap-2 min-w-0 self-end"
      style="border-color: var(--border-subtle)"
    >
      <span
        class="inline-flex items-center gap-1 text-xs leading-none tabular-nums truncate min-w-0"
        style="color: var(--text-muted)"
      >
        <Clock class="w-3.5 h-3.5 shrink-0" />
        <span class="truncate">
          {{ earliestReset ? `重置 ${formatResetCountdown(earliestReset, appStore.nowMs)}` : '暂无重置时间' }}
        </span>
      </span>

      <div class="flex items-center gap-1.5 shrink-0">
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
          class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg border text-xs leading-none font-medium whitespace-nowrap shrink-0 cursor-pointer disabled:opacity-50"
          :class="isCurrent ? 'account-card__switch-btn--current' : 'account-card__switch-btn'"
          @click="appStore.switchToAccount(account.id)"
        >
          <ArrowRightLeft class="w-3.5 h-3.5 shrink-0" />
          <span>{{ isSwitching ? '切换中...' : isCurrent ? '重新应用' : '一键切换' }}</span>
        </button>
      </div>
    </footer>
  </article>
</template>
