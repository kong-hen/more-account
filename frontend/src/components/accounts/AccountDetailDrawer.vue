<script setup lang="ts">
// 右侧悬浮账号详情抽屉（不挤压主工作区）：
// 包含切号动作、刷新与立即唤醒测试、实时 2FA TOTP 30s 圆环动态码、登录账号与密码分离编辑展示、Refresh Token 默认脱敏展示、备注、所属分组与全量模型配额明细。
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  X,
  Copy,
  Check,
  Eye,
  EyeOff,
  RefreshCw,
  ArrowRightLeft,
  Zap,
  ShieldCheck,
  User,
  KeyRound,
  Key,
  Save,
  Download,
  Trash2,
  ExternalLink,
  AlertTriangle,
  Sparkles,
} from 'lucide-vue-next'
import QuotaBar from '@/components/common/QuotaBar.vue'
import AppSelect from '@/components/common/AppSelect.vue'
import { useAppStore } from '@/stores/appStore'
import { useWakeupStore } from '@/stores/wakeupStore'
import { api, extractErrorMessage } from '@/services/api'
import { copyToClipboard, maskEmail, maskToken } from '@/utils/privacy'
import {
  formatDateTime,
  formatResetCountdown,
  getQuotaTone,
  getTierLabel,
  isAccountAbnormal,
  mergeAccountModels,
  summarizeQuotaCategories,
  type MergedModelQuota,
} from '@/utils/quota'
import type { Account, TotpCodeResult } from '@/types/models'

const props = defineProps<{
  account: Account | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'export-account', accountId: string): void
  (e: 'delete-account', account: Account): void
}>()

const appStore = useAppStore()
const wakeupStore = useWakeupStore()

// 可编辑凭据与备注表单（明确不含辅助邮箱；登录账号与登录密码分开填写及展示）
const formGroupId = ref('')
const formLoginAccount = ref('')
const formNotes = ref('')
const formPassword = ref('')
const formTwoFactorSecret = ref('')
const savingDetails = ref(false)
const detailsDirty = ref(false)

const showPassword = ref(false)
const showSecretInput = ref(false)
const showRefreshToken = ref(false)
const copiedField = ref('')

// 实时 2FA TOTP 状态
const totpResult = ref<TotpCodeResult | null>(null)
const totpError = ref('')
let totpTimer: number | null = null

const isCurrent = computed(() => props.account && appStore.currentAccountId === props.account.id)
const isRefreshing = computed(() => Boolean(props.account && appStore.refreshingIds[props.account.id]))
const isSwitching = computed(() => Boolean(props.account && appStore.switchingId === props.account.id))
const abnormal = computed(() => (props.account ? isAccountAbnormal(props.account) : false))
const tier = computed(() => (props.account ? getTierLabel(props.account) : 'UNKNOWN'))
const groupOptions = computed(() => [
  { value: '', label: '未分组' },
  ...appStore.groups.map((g) => ({ value: g.id, label: g.name })),
])

const refreshTokenValue = computed(() => props.account?.token?.refreshToken?.trim() ?? '')
const refreshTokenRevealed = computed(() => showRefreshToken.value && !appStore.settings.privacyMode)
const displayedRefreshToken = computed(() => {
  const rt = refreshTokenValue.value
  if (!rt) return ''
  return refreshTokenRevealed.value ? rt : maskToken(rt)
})
const quotaCategories = computed(() => summarizeQuotaCategories(props.account))
const mergedModels = computed(() => mergeAccountModels(props.account))

function quotaValueStyle(percentage: number) {
  const tone = getQuotaTone(percentage)
  if (tone === 'danger') return { color: 'var(--danger-text)' }
  if (tone === 'warning') return { color: 'var(--warning-text)' }
  return { color: 'var(--text-primary)' }
}

function formatMergedModelTooltip(m: MergedModelQuota): string {
  if (m.variants.length <= 1) return m.baseId
  return m.variants.map((v) => `${v.name} (${v.percentage}%)`).join('\n')
}

function getEffectiveLoginAccount(acc: Account): string {
  return acc.loginAccount?.trim() || acc.email
}

const formattedTotpCode = computed(() => {
  const code = totpResult.value?.code ?? ''
  if (!code) return '--- ---'
  if (appStore.settings.privacyMode) return '••• •••'
  if (code.length === 6) return `${code.slice(0, 3)} ${code.slice(3)}`
  return code
})

const totpDashOffset = computed(() => {
  const period = totpResult.value?.period || 30
  const rem = totpResult.value?.remainingSeconds ?? 0
  const circumference = 2 * Math.PI * 14
  return circumference * (1 - Math.max(0, Math.min(1, rem / period)))
})

function syncFormFromAccount(acc: Account | null) {
  if (!acc) {
    stopTotpLoop()
    return
  }
  formGroupId.value = acc.groupId ?? ''
  formLoginAccount.value = getEffectiveLoginAccount(acc)
  formNotes.value = acc.notes ?? ''
  formPassword.value = acc.accountPassword ?? ''
  formTwoFactorSecret.value = acc.twoFactorSecret ?? ''
  detailsDirty.value = false
  showPassword.value = false
  showSecretInput.value = false
  showRefreshToken.value = false
  void refreshTotp(acc.twoFactorSecret ?? '')
}

watch(
  () => props.account,
  (acc) => {
    syncFormFromAccount(acc)
  },
  { immediate: true },
)

watch([formGroupId, formLoginAccount, formNotes, formPassword, formTwoFactorSecret], () => {
  if (!props.account) return
  detailsDirty.value =
    formGroupId.value !== (props.account.groupId ?? '') ||
    formLoginAccount.value.trim() !== getEffectiveLoginAccount(props.account) ||
    formNotes.value !== (props.account.notes ?? '') ||
    formPassword.value !== (props.account.accountPassword ?? '') ||
    formTwoFactorSecret.value !== (props.account.twoFactorSecret ?? '')
})

function stopTotpLoop() {
  if (totpTimer !== null) {
    window.clearInterval(totpTimer)
    totpTimer = null
  }
  totpResult.value = null
  totpError.value = ''
}

async function refreshTotp(secret: string) {
  stopTotpLoop()
  const trimmed = secret.trim()
  if (!trimmed) return

  const pull = async () => {
    try {
      totpResult.value = await api.generateTOTP(trimmed)
      totpError.value = ''
    } catch (err) {
      totpResult.value = null
      totpError.value = extractErrorMessage(err)
    }
  }

  await pull()
  totpTimer = window.setInterval(() => {
    if (!totpResult.value) return
    const nextRem = totpResult.value.remainingSeconds - 1
    if (nextRem <= 0) {
      void pull()
    } else {
      totpResult.value = {
        ...totpResult.value,
        remainingSeconds: nextRem,
      }
    }
  }, 1000)
}

onBeforeUnmount(() => {
  stopTotpLoop()
})

async function handleCopy(key: string, val: string, label: string) {
  if (!val) return
  if (await copyToClipboard(val)) {
    copiedField.value = key
    appStore.notify(`已复制${label}`, 'success', 2000)
    window.setTimeout(() => {
      if (copiedField.value === key) copiedField.value = ''
    }, 1800)
  }
}

async function handleSaveDetails() {
  if (!props.account) return
  savingDetails.value = true
  try {
    const trimmedLogin = formLoginAccount.value.trim()
    // 若登录账号留空或与默认邮箱一致，后端存为空串以自动跟随邮箱
    const loginAccountToSave =
      trimmedLogin === '' || trimmedLogin === props.account.email ? '' : trimmedLogin

    const updated = await appStore.saveAccountDetails(props.account.id, {
      groupId: formGroupId.value,
      loginAccount: loginAccountToSave,
      notes: formNotes.value,
      accountPassword: formPassword.value,
      twoFactorSecret: formTwoFactorSecret.value,
    })
    formLoginAccount.value = getEffectiveLoginAccount(updated)
    detailsDirty.value = false
    void refreshTotp(updated.twoFactorSecret ?? '')
  } catch (err) {
    appStore.notify(`保存失败：${extractErrorMessage(err)}`, 'error')
  } finally {
    savingDetails.value = false
  }
}

async function handleQuickWakeup() {
  if (!props.account) return
  const defaultModel =
    mergedModels.value[0]?.canonicalModelId ||
    props.account.quota?.models?.[0]?.name ||
    'gemini-3.8-flash-high'
  await wakeupStore.runSingleTest({
    accountId: props.account.id,
    model: defaultModel,
    prompt: 'hi',
    maxOutputTokens: 16,
  })
}

async function openValidationUrl(url: string) {
  try {
    await api.openExternalURL(url)
  } catch (err) {
    appStore.notify(`打开链接失败：${extractErrorMessage(err)}`, 'error')
  }
}
</script>

<template>
  <!-- 右侧悬浮遮罩：点击空白区域轻关闭，完全脱离文档流不挤压主工作区 -->
  <Transition name="drawer-fade">
    <div
      v-if="account"
      class="fixed inset-0 z-30 drawer-backdrop"
      aria-hidden="true"
      @click="emit('close')"
    />
  </Transition>

  <Transition name="drawer-slide">
    <aside
      v-if="account"
      class="fixed top-0 right-0 z-40 h-full w-[min(392px,calc(100vw-48px))] border-l flex flex-col overflow-hidden"
      style="background-color: var(--bg-surface); border-color: var(--border-subtle); box-shadow: var(--shadow-elevated)"
      aria-label="账号详情抽屉"
    >
      <!-- 顶部标题栏 -->
      <header
        class="shrink-0 px-5 pt-5 pb-3.5 border-b flex items-start justify-between gap-3"
        style="border-color: var(--border-subtle)"
      >
      <div class="min-w-0">
        <div class="flex items-center gap-1.5">
          <span
            v-if="isCurrent"
            class="w-2 h-2 rounded-full shrink-0"
            style="background-color: var(--success)"
          />
          <h2 class="text-sm font-semibold truncate" style="color: var(--text-primary)">
            {{ maskEmail(account.email, appStore.settings.privacyMode) }}
          </h2>
          <button
            type="button"
            title="复制邮箱"
            aria-label="复制邮箱"
            class="p-1 rounded cursor-pointer hover:bg-[var(--bg-hover)] shrink-0"
            style="color: var(--text-muted)"
            @click="handleCopy('email', account.email, '邮箱')"
          >
            <Check v-if="copiedField === 'email'" class="w-3.5 h-3.5" style="color: var(--success)" />
            <Copy v-else class="w-3.5 h-3.5" />
          </button>
        </div>

        <div class="mt-1.5 flex items-center flex-wrap gap-1.5">
          <span
            v-if="tier !== 'UNKNOWN'"
            class="px-1.5 py-0.5 rounded text-[10.5px] font-semibold"
            style="background-color: var(--accent-soft); color: var(--accent)"
          >
            {{ tier }}
          </span>
          <span
            v-if="account.token.isGcpTos"
            class="px-1.5 py-0.5 rounded text-[10.5px] font-medium"
            style="background-color: var(--bg-subtle); color: var(--text-secondary)"
          >
            GCP ToS
          </span>
          <span
            v-for="credit in account.quota?.credits ?? []"
            :key="credit.creditType"
            class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10.5px] font-mono-num"
            style="background-color: var(--success-soft); color: var(--success-text)"
          >
            <Sparkles class="w-3 h-3" />
            积分 {{ credit.creditAmount }}
          </span>
        </div>
      </div>

      <button
        type="button"
        aria-label="收起详情抽屉"
        class="p-1.5 rounded-lg shrink-0 cursor-pointer hover:bg-[var(--bg-hover)]"
        style="color: var(--text-muted)"
        @click="emit('close')"
      >
        <X class="w-4 h-4" />
      </button>
    </header>

    <!-- 可滚动详情体 -->
    <div class="flex-1 min-h-0 overflow-y-auto custom-scroll px-5 py-4 space-y-5">
      <!-- 快捷操作按钮组 -->
      <section class="grid grid-cols-2 gap-2">
        <button
          type="button"
          :disabled="isSwitching"
          class="inline-flex items-center justify-center gap-1.5 py-2 px-3 rounded-xl text-xs font-medium text-white cursor-pointer disabled:opacity-60"
          style="background-color: var(--accent)"
          @click="appStore.switchToAccount(account.id, true)"
        >
          <ArrowRightLeft class="w-3.5 h-3.5" />
          <span>{{ isSwitching ? '切换中...' : '切换并重启' }}</span>
        </button>

        <button
          type="button"
          :disabled="isSwitching"
          class="inline-flex items-center justify-center gap-1.5 py-2 px-3 rounded-xl border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-60"
          style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
          @click="appStore.switchToAccount(account.id, false)"
        >
          <span>仅写入凭据</span>
        </button>

        <button
          type="button"
          :disabled="isRefreshing"
          class="inline-flex items-center justify-center gap-1.5 py-1.5 px-3 rounded-xl border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-60"
          style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-app)"
          @click="appStore.refreshAccount(account.id)"
        >
          <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': isRefreshing }" />
          <span>刷新配额</span>
        </button>

        <button
          type="button"
          :disabled="Boolean(wakeupStore.testingScopeId)"
          class="inline-flex items-center justify-center gap-1.5 py-1.5 px-3 rounded-xl border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-60"
          style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-app)"
          @click="handleQuickWakeup"
        >
          <Zap class="w-3.5 h-3.5" />
          <span>{{ wakeupStore.testingScopeId ? '唤醒中...' : '唤醒测试' }}</span>
        </button>
      </section>

      <!-- 异常诊断与验证链接 -->
      <section
        v-if="abnormal"
        class="p-3.5 rounded-xl border space-y-2"
        style="background-color: var(--danger-soft); border-color: rgba(239, 68, 68, 0.28)"
      >
        <div class="flex items-center gap-1.5 text-xs font-semibold" style="color: var(--danger-text)">
          <AlertTriangle class="w-4 h-4 shrink-0" />
          <span>{{ account.quotaError?.reason || (account.disabled ? '令牌已失效' : '账号异常') }}</span>
        </div>
        <p class="text-[11.5px] leading-relaxed break-words" style="color: var(--danger-text)">
          {{ account.quotaError?.message || account.disabledReason || '请尝试重新授权或完成 Google 账号安全验证。' }}
        </p>
        <div v-if="account.quotaError?.validationUrl" class="flex items-center gap-2 pt-1">
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg text-[11.5px] font-medium text-white cursor-pointer"
            style="background-color: var(--danger)"
            @click="openValidationUrl(account.quotaError!.validationUrl!)"
          >
            <ExternalLink class="w-3 h-3" />
            打开验证页
          </button>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg border text-[11.5px] cursor-pointer"
            style="border-color: rgba(239, 68, 68, 0.35); color: var(--danger-text); background-color: var(--bg-surface)"
            @click="handleCopy('validationUrl', account.quotaError!.validationUrl!, '验证链接')"
          >
            <Copy class="w-3 h-3" />
            复制链接
          </button>
        </div>
      </section>

      <!-- 实时 2FA TOTP 验证码卡片 -->
      <section
        v-if="account.twoFactorSecret"
        class="p-3.5 rounded-xl border"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div class="flex items-center justify-between mb-2">
          <span class="inline-flex items-center gap-1.5 text-xs font-semibold" style="color: var(--text-primary)">
            <ShieldCheck class="w-3.5 h-3.5" style="color: var(--accent)" />
            2FA 动态验证码
          </span>
          <span v-if="totpResult" class="text-[11px] font-mono-num" style="color: var(--text-muted)">
            剩余 {{ totpResult.remainingSeconds }}s
          </span>
        </div>

        <div v-if="totpResult" class="flex items-center justify-between gap-3">
          <div class="text-2xl font-bold tracking-widest font-mono-num" style="color: var(--text-primary)">
            {{ formattedTotpCode }}
          </div>

          <div class="flex items-center gap-2">
            <!-- 30秒倒计时圆环 -->
            <svg class="w-8 h-8 -rotate-90" viewBox="0 0 36 36" aria-hidden="true">
              <circle
                cx="18"
                cy="18"
                r="14"
                fill="none"
                stroke="var(--border-strong)"
                stroke-width="3"
              />
              <circle
                cx="18"
                cy="18"
                r="14"
                fill="none"
                :stroke="totpResult.remainingSeconds <= 5 ? 'var(--danger)' : 'var(--accent)'"
                stroke-width="3"
                stroke-linecap="round"
                :stroke-dasharray="2 * Math.PI * 14"
                :stroke-dashoffset="totpDashOffset"
                class="transition-all duration-300"
              />
            </svg>

            <button
              type="button"
              class="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-lg border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)]"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
              @click="handleCopy('totp', totpResult.code, '2FA 验证码')"
            >
              <Check v-if="copiedField === 'totp'" class="w-3.5 h-3.5" style="color: var(--success)" />
              <Copy v-else class="w-3.5 h-3.5" />
              <span>{{ copiedField === 'totp' ? '已复制' : '复制' }}</span>
            </button>
          </div>
        </div>
        <p v-else-if="totpError" class="text-xs" style="color: var(--danger-text)">{{ totpError }}</p>
      </section>

      <!-- 分组、密码、2FA 密钥与备注编辑区 -->
      <section class="space-y-3">
        <div class="flex items-center justify-between">
          <h3 class="text-xs font-semibold" style="color: var(--text-primary)">分组与凭据备注</h3>
          <button
            v-if="detailsDirty"
            type="button"
            :disabled="savingDetails"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg text-[11.5px] font-medium text-white cursor-pointer disabled:opacity-60"
            style="background-color: var(--accent)"
            @click="handleSaveDetails"
          >
            <Save class="w-3 h-3" />
            <span>{{ savingDetails ? '保存中...' : '保存更改' }}</span>
          </button>
        </div>

        <!-- 所属分组 -->
        <div>
          <label for="detail-group-select" class="block text-[11.5px] mb-1" style="color: var(--text-muted)">
            所属分组
          </label>
          <AppSelect
            id="detail-group-select"
            v-model="formGroupId"
            :options="groupOptions"
            block
          />
        </div>

        <!-- 登录账号（与登录密码分开填写与展示） -->
        <div>
          <div class="flex items-center justify-between gap-2 mb-1">
            <label for="detail-login-account-input" class="block text-[11.5px]" style="color: var(--text-muted)">
              登录账号
            </label>
            <span
              v-if="!account.loginAccount"
              class="text-[10.5px] truncate"
              style="color: var(--text-muted)"
            >
              默认同邮箱，可自定义
            </span>
          </div>
          <div class="flex items-center gap-1.5">
            <div class="relative flex-1 min-w-0">
              <User
                class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none"
                style="color: var(--text-muted)"
              />
              <input
                id="detail-login-account-input"
                v-model="formLoginAccount"
                :type="appStore.settings.privacyMode ? 'password' : 'text'"
                autocomplete="off"
                placeholder="填写登录账号（默认使用当前邮箱）"
                class="w-full pl-8 pr-2.5 py-1.5 text-xs rounded-lg border font-mono-num"
                style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
              />
            </div>
            <button
              type="button"
              :disabled="!formLoginAccount.trim()"
              title="复制登录账号"
              aria-label="复制登录账号"
              class="p-1.5 rounded-lg border shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-40"
              style="border-color: var(--border-strong); color: var(--text-secondary)"
              @click="handleCopy('loginAccount', formLoginAccount.trim() || account.email, '登录账号')"
            >
              <Check v-if="copiedField === 'loginAccount'" class="w-3.5 h-3.5" style="color: var(--success)" />
              <Copy v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- 登录密码（独立填写与脱敏展示） -->
        <div>
          <label for="detail-password-input" class="block text-[11.5px] mb-1" style="color: var(--text-muted)">
            登录密码
          </label>
          <div class="flex items-center gap-1.5">
            <div class="relative flex-1 min-w-0">
              <KeyRound
                class="w-3.5 h-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 pointer-events-none"
                style="color: var(--text-muted)"
              />
              <input
                id="detail-password-input"
                v-model="formPassword"
                :type="showPassword && !appStore.settings.privacyMode ? 'text' : 'password'"
                autocomplete="off"
                placeholder="可选，记录登录密码"
                class="w-full pl-8 pr-8 py-1.5 text-xs rounded-lg border font-mono-num"
                style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
              />
              <button
                type="button"
                :disabled="appStore.settings.privacyMode"
                :title="appStore.settings.privacyMode ? '全局隐私模式已开启' : showPassword ? '隐藏密码' : '显示密码'"
                aria-label="切换密码显示"
                class="absolute right-2 top-1/2 -translate-y-1/2 p-0.5 cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
                style="color: var(--text-muted)"
                @click="showPassword = !showPassword"
              >
                <EyeOff v-if="showPassword && !appStore.settings.privacyMode" class="w-3.5 h-3.5" />
                <Eye v-else class="w-3.5 h-3.5" />
              </button>
            </div>
            <button
              type="button"
              :disabled="!formPassword"
              title="复制登录密码"
              aria-label="复制登录密码"
              class="p-1.5 rounded-lg border shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-40"
              style="border-color: var(--border-strong); color: var(--text-secondary)"
              @click="handleCopy('password', formPassword, '登录密码')"
            >
              <Check v-if="copiedField === 'password'" class="w-3.5 h-3.5" style="color: var(--success)" />
              <Copy v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- 2FA TOTP 密钥 -->
        <div>
          <label for="detail-2fa-input" class="block text-[11.5px] mb-1" style="color: var(--text-muted)">
            2FA 密钥（支持 Base32 或 otpauth:// URI）
          </label>
          <div class="relative">
            <input
              id="detail-2fa-input"
              v-model="formTwoFactorSecret"
              :type="showSecretInput && !appStore.settings.privacyMode ? 'text' : 'password'"
              placeholder="例如：JBSWY3DPEHPK3PXP"
              class="w-full pl-2.5 pr-8 py-1.5 text-xs rounded-lg border font-mono-num"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
            <button
              type="button"
              :disabled="appStore.settings.privacyMode"
              :title="appStore.settings.privacyMode ? '全局隐私模式已开启' : showSecretInput ? '隐藏密钥' : '显示密钥'"
              aria-label="切换密钥显示"
              class="absolute right-2 top-1/2 -translate-y-1/2 p-0.5 cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed"
              style="color: var(--text-muted)"
              @click="showSecretInput = !showSecretInput"
            >
              <EyeOff v-if="showSecretInput && !appStore.settings.privacyMode" class="w-3.5 h-3.5" />
              <Eye v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- 自由备注 -->
        <div>
          <label for="detail-notes-input" class="block text-[11.5px] mb-1" style="color: var(--text-muted)">
            备注
          </label>
          <textarea
            id="detail-notes-input"
            v-model="formNotes"
            rows="3"
            placeholder="记录用途、到期提醒等信息..."
            class="w-full p-2.5 text-xs rounded-lg border resize-none custom-scroll"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>
      </section>

      <!-- OAuth Refresh Token 展示区（默认脱敏，支持显隐切换与一键复制） -->
      <section
        class="p-3.5 rounded-xl border space-y-2"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div class="flex items-center justify-between gap-2">
          <div class="flex items-center gap-1.5 min-w-0">
            <Key class="w-3.5 h-3.5 shrink-0" style="color: var(--accent)" />
            <h3 class="text-xs font-semibold truncate" style="color: var(--text-primary)">Refresh Token</h3>
            <span
              class="px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0"
              style="background-color: var(--bg-subtle); color: var(--text-muted)"
            >
              {{ refreshTokenRevealed ? '明文' : '已脱敏' }}
            </span>
          </div>

          <div class="flex items-center gap-1 shrink-0">
            <button
              type="button"
              :disabled="!refreshTokenValue || appStore.settings.privacyMode"
              :title="
                appStore.settings.privacyMode
                  ? '全局隐私模式已开启'
                  : refreshTokenRevealed
                    ? '隐藏 Refresh Token'
                    : '显示完整 Refresh Token'
              "
              aria-label="切换 Refresh Token 脱敏状态"
              class="p-1.5 rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-40 disabled:cursor-not-allowed"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-secondary)"
              @click="showRefreshToken = !showRefreshToken"
            >
              <EyeOff v-if="refreshTokenRevealed" class="w-3.5 h-3.5" />
              <Eye v-else class="w-3.5 h-3.5" />
            </button>
            <button
              type="button"
              :disabled="!refreshTokenValue"
              title="复制完整 Refresh Token"
              aria-label="复制完整 Refresh Token"
              class="p-1.5 rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-40"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-secondary)"
              @click="handleCopy('refreshToken', refreshTokenValue, ' Refresh Token')"
            >
              <Check v-if="copiedField === 'refreshToken'" class="w-3.5 h-3.5" style="color: var(--success)" />
              <Copy v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <div
          v-if="refreshTokenValue"
          class="px-2.5 py-2 rounded-lg border text-[11.5px] font-mono-num leading-relaxed"
          :class="
            refreshTokenRevealed
              ? 'break-all select-all max-h-24 overflow-y-auto custom-scroll'
              : 'truncate select-none'
          "
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-primary)"
        >
          {{ displayedRefreshToken }}
        </div>
        <div
          v-else
          class="px-2.5 py-2 rounded-lg border text-xs"
          style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-muted)"
        >
          暂无 Refresh Token
        </div>
      </section>

      <!-- 额度总览（三大分类：Gemini Argon / Gemini Flash / Claude/GPT，支持 5h 与周额度双窗口展示） -->
      <section class="space-y-2.5">
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-xs font-semibold" style="color: var(--text-primary)">
            额度总览 ({{ quotaCategories.length }})
          </h3>
          <span v-if="account.quota?.lastUpdated" class="text-[11px] tabular-nums shrink-0" style="color: var(--text-muted)">
            更新于 {{ formatDateTime(account.quota.lastUpdated) }}
          </span>
        </div>

        <div v-if="quotaCategories.length" class="space-y-2.5">
          <div
            v-for="cat in quotaCategories"
            :key="cat.key"
            class="p-3 rounded-xl border space-y-2"
            style="border-color: var(--border-subtle); background-color: var(--bg-app)"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-xs font-semibold truncate" style="color: var(--text-primary)">
                {{ cat.label }}
              </span>
              <span
                class="px-1.5 py-0.5 rounded text-[10px] font-medium shrink-0"
                style="background-color: var(--bg-subtle); color: var(--text-muted)"
              >
                {{ cat.fiveHour && cat.weekly ? '5h + 周额度' : cat.weekly ? '仅周额度' : '5h 额度' }}
              </span>
            </div>

            <!-- 5 小时额度行（存在时展示） -->
            <div v-if="cat.fiveHour" class="space-y-1">
              <div class="flex items-center justify-between gap-2 text-[11.5px]">
                <span class="inline-flex items-center gap-1.5" style="color: var(--text-secondary)">
                  <span
                    class="px-1.5 py-0.2 rounded text-[10px] font-semibold"
                    style="background-color: var(--bg-subtle); color: var(--text-muted)"
                  >
                    5小时
                  </span>
                  <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.fiveHour.percentage)">
                    {{ cat.fiveHour.percentage }}%
                  </span>
                </span>
                <span class="text-[11px] tabular-nums shrink-0" style="color: var(--text-muted)">
                  {{ cat.fiveHour.resetTime ? `重置 ${formatResetCountdown(cat.fiveHour.resetTime)}` : '未返回重置时间' }}
                </span>
              </div>
              <QuotaBar :percentage="cat.fiveHour.percentage" />
            </div>

            <!-- 周额度行（存在时展示） -->
            <div v-if="cat.weekly" class="space-y-1">
              <div class="flex items-center justify-between gap-2 text-[11.5px]">
                <span class="inline-flex items-center gap-1.5" style="color: var(--text-secondary)">
                  <span
                    class="px-1.5 py-0.2 rounded text-[10px] font-semibold"
                    style="background-color: var(--bg-subtle); color: var(--text-muted)"
                  >
                    周额度
                  </span>
                  <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.weekly.percentage)">
                    {{ cat.weekly.percentage }}%
                  </span>
                </span>
                <span class="text-[11px] tabular-nums shrink-0" style="color: var(--text-muted)">
                  {{ cat.weekly.resetTime ? `重置 ${formatResetCountdown(cat.weekly.resetTime)}` : '未返回重置时间' }}
                </span>
              </div>
              <QuotaBar :percentage="cat.weekly.percentage" />
            </div>
          </div>
        </div>
        <div
          v-else
          class="p-4 rounded-xl border text-center text-xs"
          style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-muted)"
        >
          暂无额度数据，请点击上方「刷新配额」获取。
        </div>
      </section>

      <!-- 模型配额明细（独立展示各模型，仅合并同一模型的不同思考深度，不附加额外后缀） -->
      <section v-if="mergedModels.length" class="space-y-2.5">
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-xs font-semibold" style="color: var(--text-primary)">
            模型明细 ({{ mergedModels.length }})
          </h3>
        </div>

        <div class="space-y-2">
          <div
            v-for="m in mergedModels"
            :key="m.key"
            class="p-2.5 rounded-xl border space-y-1.5"
            style="border-color: var(--border-subtle); background-color: var(--bg-app)"
          >
            <div class="flex items-center justify-between gap-2 text-xs">
              <span class="font-medium truncate" style="color: var(--text-primary)" :title="formatMergedModelTooltip(m)">
                {{ m.displayName }}
              </span>
              <span class="font-mono-num font-semibold shrink-0" style="color: var(--text-primary)">
                {{ m.percentage }}%
              </span>
            </div>

            <QuotaBar :percentage="m.percentage" />
            <div class="flex items-center justify-between gap-2 text-[11px]" style="color: var(--text-muted)">
              <span class="font-mono-num truncate min-w-0" :title="formatMergedModelTooltip(m)">{{ m.baseId }}</span>
              <span class="tabular-nums shrink-0">
                {{ m.resetTime ? `重置 ${formatResetCountdown(m.resetTime)}` : '未返回重置时间' }}
              </span>
            </div>
          </div>
        </div>
      </section>

      <!-- 元数据时间线 -->
      <section class="pt-1 text-[11px] space-y-1 tabular-nums" style="color: var(--text-muted)">
        <div class="flex justify-between">
          <span>创建时间</span>
          <span>{{ formatDateTime(account.createdAt) }}</span>
        </div>
        <div class="flex justify-between">
          <span>最近使用</span>
          <span>{{ formatDateTime(account.lastUsed) }}</span>
        </div>
        <div v-if="account.token.projectId" class="flex justify-between gap-3">
          <span class="shrink-0">云项目 ID</span>
          <span class="font-mono-num truncate">{{ account.token.projectId }}</span>
        </div>
      </section>
    </div>

    <!-- 底部导出与删除栏 -->
    <footer
      class="shrink-0 px-5 py-3 border-t flex items-center justify-between gap-2"
      style="border-color: var(--border-subtle); background-color: var(--bg-app)"
    >
      <button
        type="button"
        class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:bg-[var(--bg-hover)]"
        style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
        @click="emit('export-account', account.id)"
      >
        <Download class="w-3.5 h-3.5" />
        导出 JSON
      </button>

      <button
        type="button"
        class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium whitespace-nowrap shrink-0 cursor-pointer hover:opacity-90"
        style="background-color: var(--danger-soft); color: var(--danger-text)"
        @click="emit('delete-account', account)"
      >
        <Trash2 class="w-3.5 h-3.5" />
        删除账号
      </button>
    </footer>
    </aside>
  </Transition>
</template>
