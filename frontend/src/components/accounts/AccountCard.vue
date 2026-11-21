<script setup lang="ts">
// 账号卡片视图项：展示当前激活标识、脱敏邮箱、订阅与自建分组、核心模型配额条与一键切换操作。
import { computed, ref } from 'vue'
import {
  RefreshCw,
  ArrowRightLeft,
  AlertTriangle,
  ShieldCheck,
  Clock,
  Check,
  CheckCircle2,
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
    class="group relative overflow-hidden flex flex-col justify-between p-4 rounded-2xl border cursor-pointer transition-all"
    :class="[dragging ? 'opacity-50 scale-[0.98]' : '']"
    :style="{
      background: isCurrent
        ? 'linear-gradient(180deg, var(--success-soft) 0%, var(--bg-surface) 38%)'
        : 'var(--bg-surface)',
      borderColor: dragOver
        ? 'var(--accent)'
        : activeDetail
          ? 'var(--accent)'
          : isCurrent
            ? 'var(--success)'
            : 'var(--border-subtle)',
      boxShadow: dragOver
        ? '0 0 0 2px var(--accent), var(--shadow-elevated)'
        : activeDetail
          ? isCurrent
            ? '0 0 0 1px var(--accent), 0 8px 20px -6px rgba(16, 185, 129, 0.24)'
            : '0 0 0 1px var(--accent), var(--shadow-card)'
          : isCurrent
            ? '0 0 0 1px var(--success), 0 8px 20px -6px rgba(16, 185, 129, 0.24)'
            : 'var(--shadow-card)',
    }"
    @click="emit('open-detail', account)"
    @dragstart="reorderable && emit('drag-start', account.id, $event)"
    @dragover="reorderable && emit('drag-over', account.id, $event)"
    @drop="reorderable && emit('drop', account.id, $event)"
    @dragend="reorderable && emit('drag-end')"
  >
    <!-- 当前正在使用账号的顶部翠绿强调条，一眼识别 -->
    <div
      v-if="isCurrent"
      class="absolute inset-x-0 top-0 h-[3px] pointer-events-none"
      style="background: linear-gradient(90deg, var(--success), #34d399)"
    />

    <!-- 顶部信息行 -->
    <div class="space-y-2.5">
      <div class="flex items-start justify-between gap-2">
        <div class="flex items-start gap-2 min-w-0">
          <!-- 默认排序模式下提供拖拽手柄，悬停显示 -->
          <span
            v-if="reorderable"
            title="拖拽调整账号顺序"
            aria-label="拖拽调整账号顺序"
            class="mt-0.5 -ml-1 p-0.5 rounded cursor-grab active:cursor-grabbing opacity-0 group-hover:opacity-100 transition-opacity shrink-0"
            style="color: var(--text-muted)"
            @click.stop
          >
            <GripVertical class="w-3.5 h-3.5" />
          </span>

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
              <CheckCircle2
                v-if="isCurrent"
                class="w-4 h-4 shrink-0"
                style="color: var(--success)"
                title="本机当前正在使用此账号"
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

        <!-- 右上角状态徽章簇：使用中醒目标签 + 订阅等级 -->
        <div class="flex items-center gap-1.5 shrink-0">
          <span
            v-if="isCurrent"
            class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full text-[10.5px] font-semibold tracking-wide shrink-0"
            style="background-color: var(--success); color: #ffffff"
            title="本机当前正在使用此账号"
          >
            <span class="relative flex w-1.5 h-1.5">
              <span class="animate-ping absolute inline-flex h-full w-full rounded-full bg-white opacity-75" />
              <span class="relative inline-flex rounded-full w-1.5 h-1.5 bg-white" />
            </span>
            使用中
          </span>

          <span
            v-if="tier !== 'UNKNOWN'"
            class="px-1.5 py-0.5 rounded-md border text-[10.5px] font-semibold tracking-wide shrink-0"
            :style="
              tier === 'ULTRA'
                ? { backgroundColor: 'var(--accent-soft)', color: 'var(--accent)', borderColor: 'transparent' }
                : tier === 'PRO'
                  ? isCurrent
                    ? { backgroundColor: 'var(--bg-surface)', color: 'var(--success-text)', borderColor: 'rgba(16, 185, 129, 0.35)' }
                    : { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)', borderColor: 'transparent' }
                  : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-secondary)', borderColor: 'transparent' }
            "
          >
            {{ tier }}
          </span>
        </div>
      </div>

      <!-- 元数据胶囊行：自建分组 + 状态提示 + 2FA/密码小图标 -->
      <div class="flex items-center flex-wrap gap-1.5">
        <span
          v-if="groupInfo"
          class="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-full border text-[11px] font-medium"
          :style="
            isCurrent
              ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-secondary)', borderColor: 'var(--border-subtle)' }
              : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-secondary)', borderColor: 'transparent' }
          "
        >
          <span class="w-1.5 h-1.5 rounded-full" :style="{ backgroundColor: groupInfo.color }" />
          {{ groupInfo.name }}
        </span>
        <span
          v-else
          class="inline-flex items-center px-2 py-0.5 rounded-full border text-[11px]"
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
          class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[11px] font-medium"
          style="background-color: var(--danger-soft); color: var(--danger-text)"
        >
          <AlertTriangle class="w-3 h-3" />
          {{ account.disabled ? '令牌失效' : validationRequired ? '需网页验证' : '异常' }}
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

      <!-- 需网页验证快捷操作栏（即使账号保留有历史配额缓存也直接展示，无需点进抽屉即可跳转验证或复制链接） -->
      <div
        v-if="validationUrl"
        class="p-2.5 rounded-xl border space-y-2"
        style="background-color: var(--danger-soft); border-color: rgba(239, 68, 68, 0.25)"
        @click.stop
      >
        <p class="text-[11px] leading-snug line-clamp-2" style="color: var(--danger-text)">
          {{ account.quotaError?.message || '此账号需完成 Google 网页安全验证后方可正常调用' }}
        </p>
        <div class="flex items-center gap-1.5">
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg text-[11px] font-medium text-white cursor-pointer transition-opacity hover:opacity-95"
            style="background-color: var(--danger)"
            @click="handleOpenValidationUrl"
          >
            <ExternalLink class="w-3 h-3" />
            <span>网页验证</span>
          </button>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg border text-[11px] font-medium cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
            style="border-color: rgba(239, 68, 68, 0.35); color: var(--danger-text); background-color: var(--bg-surface)"
            @click="handleCopyValidationUrl"
          >
            <Check v-if="copiedVerifyUrl" class="w-3 h-3" style="color: var(--success)" />
            <Copy v-else class="w-3 h-3" />
            <span>{{ copiedVerifyUrl ? '已复制' : '复制链接' }}</span>
          </button>
        </div>
      </div>

      <!-- 三大配额分类进度条：Gemini Argon / Gemini Flash / Claude/GPT（有 5h 和周额度则双窗并列，仅有周额度则单行展示） -->
      <div v-if="quotaCategories.length > 0 || !validationUrl" class="pt-1.5 space-y-2">
        <template v-if="quotaCategories.length > 0">
          <div
            v-for="cat in quotaCategories"
            :key="cat.key"
            class="px-2.5 py-2 rounded-xl border space-y-1.5"
            style="background-color: var(--bg-app); border-color: var(--border-subtle)"
          >
            <!-- 双窗口：凡是有 5 小时限额的分类必然也有周限额，始终左右并列展示 -->
            <template v-if="cat.fiveHour">
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
                      :title="`5小时额度重置：${formatResetCountdown(cat.fiveHour.resetTime, appStore.nowMs)}`"
                    >
                      {{ formatResetCountdown(cat.fiveHour.resetTime, appStore.nowMs) }}
                    </span>
                  </div>
                  <QuotaBar :percentage="cat.fiveHour.percentage" />
                </div>

                <div class="space-y-1 min-w-0">
                  <template v-if="cat.weekly">
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
                        :title="`周额度重置：${formatResetCountdown(cat.weekly.resetTime, appStore.nowMs)}`"
                      >
                        {{ formatResetCountdown(cat.weekly.resetTime, appStore.nowMs) }}
                      </span>
                    </div>
                    <QuotaBar :percentage="cat.weekly.percentage" />
                  </template>
                  <template v-else>
                    <div class="flex items-baseline justify-between gap-1 text-[11px]">
                      <span class="inline-flex items-center gap-1 min-w-0">
                        <span
                          class="px-1 rounded text-[9.5px] font-semibold shrink-0"
                          style="background-color: var(--bg-subtle); color: var(--text-muted)"
                        >
                          周
                        </span>
                        <span class="font-mono-num font-medium" style="color: var(--text-muted)">
                          {{ isRefreshing ? '同步中' : '--' }}
                        </span>
                      </span>
                    </div>
                    <div
                      class="w-full h-[5px] rounded-full overflow-hidden"
                      style="background-color: var(--bg-subtle)"
                      title="点击刷新获取云端周额度"
                    />
                  </template>
                </div>
              </div>
            </template>

            <!-- 单窗口：仅有周额度（如 Gemini Argon / FREE 账号） -->
            <template v-else-if="cat.weekly">
              <div class="flex items-center justify-between gap-2 text-[11.5px]">
                <span class="inline-flex items-center gap-1.5 min-w-0">
                  <span class="font-semibold truncate" style="color: var(--text-primary)">{{ cat.label }}</span>
                  <span
                    class="px-1 rounded text-[9.5px] font-semibold shrink-0"
                    style="background-color: var(--bg-subtle); color: var(--text-muted)"
                  >
                    周
                  </span>
                </span>
                <span class="inline-flex items-center gap-1.5 shrink-0 font-mono-num">
                  <span
                    v-if="cat.weekly.resetTime"
                    class="text-[10.5px]"
                    style="color: var(--text-muted)"
                  >
                    {{ formatResetCountdown(cat.weekly.resetTime, appStore.nowMs) }}
                  </span>
                  <span
                    class="font-semibold"
                    :style="quotaValueStyle(cat.weekly.percentage)"
                  >
                    {{ cat.weekly.percentage }}%
                  </span>
                </span>
              </div>
              <QuotaBar :percentage="cat.weekly.percentage" />
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
        <span class="truncate">{{ earliestReset ? `重置 ${formatResetCountdown(earliestReset, appStore.nowMs)}` : '暂无重置时间' }}</span>
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
          class="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer transition-colors disabled:opacity-50"
          :style="
            isCurrent
              ? {
                  backgroundColor: 'var(--success-soft)',
                  color: 'var(--success-text)',
                  borderColor: 'var(--success)',
                }
              : {
                  backgroundColor: 'var(--accent)',
                  color: '#ffffff',
                  borderColor: 'transparent',
                }
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
