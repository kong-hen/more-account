<script setup lang="ts">
// 右侧悬浮账号详情抽屉（不挤压主工作区）：
// 包含切号动作、刷新与立即唤醒测试、实时 2FA TOTP 30s 圆环动态码、登录账号与密码分离编辑展示、Refresh Token 默认脱敏展示、备注、所属分组与三大分类额度总览。
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
  Square,
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
import { copyToClipboard, maskToken } from '@/utils/privacy'
import {
  formatDateTime,
  formatResetCountdown,
  getAccountValidationUrl,
  getQuotaTone,
  getTierLabel,
  isAccountAbnormal,
  isAccountValidationRequired,
  mergeAccountModels,
  pickPreferredWakeupModel,
  summarizeQuotaCategories,
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
const showRefreshToken = ref(false)
const copiedField = ref('')

// 实时 2FA TOTP 状态（2FA 密钥与验证码均直接明文显示，不做隐藏遮挡）
const totpResult = ref<TotpCodeResult | null>(null)
const totpError = ref('')
let totpTimer: number | null = null
let totpDebounceTimer: number | null = null
let totpGeneration = 0
let unmounted = false

const isCurrent = computed(() => props.account && appStore.currentAccountId === props.account.id)
const isRefreshing = computed(() => Boolean(props.account && appStore.refreshingIds[props.account.id]))
const isSwitching = computed(() => Boolean(props.account && appStore.switchingId === props.account.id))
const isTestingCurrent = computed(
  () => Boolean(props.account && wakeupStore.testingScopeId && wakeupStore.testingAccountId === props.account.id),
)
const abnormal = computed(() => (props.account ? isAccountAbnormal(props.account) : false))
const validationRequired = computed(() => isAccountValidationRequired(props.account))
const validationUrl = computed(() => getAccountValidationUrl(props.account))
const tier = computed(() => (props.account ? getTierLabel(props.account) : 'UNKNOWN'))
const groupOptions = computed(() => [
  { value: '', label: '未分组' },
  ...appStore.groups.map((g) => ({ value: g.id, label: g.name })),
])

const refreshTokenValue = computed(() => props.account?.token?.refreshToken?.trim() ?? '')
const refreshTokenRevealed = computed(() => showRefreshToken.value)
const displayedRefreshToken = computed(() => {
  const rt = refreshTokenValue.value
  if (!rt) return ''
  return refreshTokenRevealed.value ? rt : maskToken(rt)
})
const quotaCategories = computed(() => summarizeQuotaCategories(props.account, appStore.nowMs))
const mergedModels = computed(() => mergeAccountModels(props.account))

function quotaValueStyle(percentage: number) {
  const tone = getQuotaTone(percentage)
  if (tone === 'danger') return { color: 'var(--danger-text)' }
  if (tone === 'warning') return { color: 'var(--warning-text)' }
  return { color: 'var(--text-primary)' }
}

function getCustomLoginAccount(acc: Account): string {
  return acc.loginAccount?.trim() ?? ''
}

const hasTwoFactorSecret = computed(() => Boolean(formTwoFactorSecret.value.trim()))
const isSecretUnsaved = computed(() =>
  Boolean(
    props.account &&
      formTwoFactorSecret.value.trim() !== (props.account.twoFactorSecret ?? '').trim(),
  ),
)

const formattedTotpCode = computed(() => {
  const code = totpResult.value?.code ?? ''
  if (!code) return '--- ---'
  if (code.length === 6) return `${code.slice(0, 3)} ${code.slice(3)}`
  return code
})

const totpDashOffset = computed(() => {
  const period = totpResult.value?.period || 30
  const rem = totpResult.value?.remainingSeconds ?? 0
  const circumference = 2 * Math.PI * 14
  return circumference * (1 - Math.max(0, Math.min(1, rem / period)))
})

let lastSyncedSecret = ''

function syncFormFromAccount(acc: Account | null, resetVisibility: boolean) {
  if (!acc) {
    lastSyncedSecret = ''
    detailsDirty.value = false
    appStore.detailDrawerDirty = false
    stopTotpLoop()
    return
  }
  formGroupId.value = acc.groupId ?? ''
  formLoginAccount.value = getCustomLoginAccount(acc) || acc.email
  formNotes.value = acc.notes ?? ''
  formPassword.value = acc.accountPassword ?? ''
  formTwoFactorSecret.value = acc.twoFactorSecret ?? ''
  detailsDirty.value = false
  appStore.detailDrawerDirty = false
  if (resetVisibility) {
    showPassword.value = false
    showRefreshToken.value = false
  }
  const nextSecret = (acc.twoFactorSecret ?? '').trim()
  if (resetVisibility || nextSecret !== lastSyncedSecret) {
    lastSyncedSecret = nextSecret
    void refreshTotp(nextSecret)
  }
}

watch(
  () => props.account,
  (acc, prevAcc) => {
    const switchedAccount = (acc?.id ?? '') !== (prevAcc?.id ?? '')
    if (!acc || switchedAccount) {
      syncFormFromAccount(acc, true)
      return
    }
    // 同一账号被后台自动刷新更新引用时，若用户正在编辑表单则绝不覆盖未保存内容
    if (!detailsDirty.value) {
      syncFormFromAccount(acc, false)
    }
  },
  { immediate: true },
)

watch([formGroupId, formLoginAccount, formNotes, formPassword, formTwoFactorSecret], () => {
  if (!props.account) return
  const normalizedLogin =
    formLoginAccount.value.trim() === props.account.email ? '' : formLoginAccount.value.trim()
  const dirty =
    formGroupId.value !== (props.account.groupId ?? '') ||
    normalizedLogin !== getCustomLoginAccount(props.account) ||
    formNotes.value.trim() !== (props.account.notes ?? '').trim() ||
    formPassword.value !== (props.account.accountPassword ?? '') ||
    formTwoFactorSecret.value.trim() !== (props.account.twoFactorSecret ?? '').trim()
  detailsDirty.value = dirty
  appStore.detailDrawerDirty = dirty
})

function clearTotpDebounce() {
  if (totpDebounceTimer !== null) {
    window.clearTimeout(totpDebounceTimer)
    totpDebounceTimer = null
  }
}

function stopTotpLoop() {
  clearTotpDebounce()
  totpGeneration++
  if (totpTimer !== null) {
    window.clearInterval(totpTimer)
    totpTimer = null
  }
  totpResult.value = null
  totpError.value = ''
}

async function refreshTotp(secret: string) {
  stopTotpLoop()
  const gen = totpGeneration
  const trimmed = secret.trim()
  if (!trimmed) return

  const pull = async () => {
    try {
      const res = await api.generateTOTP(trimmed)
      if (unmounted || gen !== totpGeneration) return
      totpResult.value = res
      totpError.value = ''
    } catch (err) {
      if (unmounted || gen !== totpGeneration) return
      totpResult.value = null
      totpError.value = extractErrorMessage(err)
    }
  }

  await pull()
  // 防止在 IPC 异步等待期间切换账号或卸载组件后创建孤儿定时器
  if (unmounted || gen !== totpGeneration || !totpResult.value) return
  totpTimer = window.setInterval(() => {
    if (unmounted || gen !== totpGeneration) {
      if (totpTimer !== null) {
        window.clearInterval(totpTimer)
        totpTimer = null
      }
      return
    }
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

// 实时响应 2FA 密钥填写与清空：填写后立即在下方直接明文展示验证码与复制按钮
watch(formTwoFactorSecret, (raw) => {
  clearTotpDebounce()
  const nextSecret = raw.trim()
  if (nextSecret === lastSyncedSecret) return
  if (!nextSecret) {
    lastSyncedSecret = ''
    stopTotpLoop()
    return
  }
  totpDebounceTimer = window.setTimeout(() => {
    totpDebounceTimer = null
    lastSyncedSecret = nextSecret
    void refreshTotp(nextSecret)
  }, 100)
})

onBeforeUnmount(() => {
  unmounted = true
  appStore.detailDrawerDirty = false
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
    formGroupId.value = updated.groupId ?? ''
    formLoginAccount.value = getCustomLoginAccount(updated) || updated.email
    formNotes.value = updated.notes ?? ''
    formPassword.value = updated.accountPassword ?? ''
    formTwoFactorSecret.value = updated.twoFactorSecret ?? ''
    detailsDirty.value = false
    appStore.detailDrawerDirty = false
    lastSyncedSecret = (updated.twoFactorSecret ?? '').trim()
    void refreshTotp(lastSyncedSecret)
  } catch (err) {
    appStore.notify(`保存失败：${extractErrorMessage(err)}`, 'error')
  } finally {
    savingDetails.value = false
  }
}

async function handleQuickWakeup() {
  if (!props.account) return
  const defaultModel = pickPreferredWakeupModel(mergedModels.value)
  await wakeupStore.runSingleTest({
    accountId: props.account.id,
    model: defaultModel,
    prompt: 'hi',
    maxOutputTokens: 64,
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
            {{ account.email }}
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
            :title="
              credit.minimumCreditAmountForUsage
                ? `${credit.creditType}（最低起用门槛：${credit.minimumCreditAmountForUsage}）`
                : credit.creditType
            "
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
          v-if="isTestingCurrent"
          type="button"
          class="inline-flex items-center justify-center gap-1.5 py-1.5 px-3 rounded-xl border text-xs font-medium cursor-pointer hover:opacity-90"
          style="border-color: var(--danger); color: var(--danger-text); background-color: var(--danger-soft)"
          @click="wakeupStore.cancelCurrentTest()"
        >
          <Square class="w-3.5 h-3.5" />
          <span>取消唤醒</span>
        </button>

        <button
          v-else
          type="button"
          :disabled="Boolean(wakeupStore.testingScopeId)"
          class="inline-flex items-center justify-center gap-1.5 py-1.5 px-3 rounded-xl border text-xs font-medium cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-60"
          style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-app)"
          @click="handleQuickWakeup"
        >
          <Zap class="w-3.5 h-3.5" />
          <span>{{ wakeupStore.testingScopeId ? '其他唤醒中...' : '唤醒测试' }}</span>
        </button>
      </section>

      <!-- 异常诊断与验证链接 -->
      <section
        v-if="abnormal"
        class="p-3.5 rounded-xl border space-y-2"
        style="background-color: var(--danger-soft); border-color: rgba(239, 68, 68, 0.28)"
      >
        <div class="flex items-center justify-between gap-2 text-xs font-semibold" style="color: var(--danger-text)">
          <span class="inline-flex items-center gap-1.5">
            <AlertTriangle class="w-4 h-4 shrink-0" />
            <span>{{ validationRequired ? '需完成网页验证' : account.quotaError?.reason || (account.disabled ? '令牌已失效' : '账号异常') }}</span>
          </span>
          <span v-if="account.disabled && account.disabledAt" class="text-[10.5px] font-normal font-mono-num">
            {{ formatDateTime(account.disabledAt) }}
          </span>
        </div>
        <p class="text-[11.5px] leading-relaxed break-words" style="color: var(--danger-text)">
          {{ account.quotaError?.message || account.disabledReason || '请尝试重新授权或完成 Google 账号安全验证。' }}
        </p>
        <div v-if="validationUrl" class="flex items-center gap-2 pt-1">
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg text-[11.5px] font-medium text-white cursor-pointer"
            style="background-color: var(--danger)"
            @click="openValidationUrl(validationUrl)"
          >
            <ExternalLink class="w-3 h-3" />
            网页验证
          </button>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2.5 py-1 rounded-lg border text-[11.5px] cursor-pointer hover:bg-[var(--bg-hover)]"
            style="border-color: rgba(239, 68, 68, 0.35); color: var(--danger-text); background-color: var(--bg-surface)"
            @click="handleCopy('validationUrl', validationUrl, '验证链接')"
          >
            <Check v-if="copiedField === 'validationUrl'" class="w-3 h-3" style="color: var(--success)" />
            <Copy v-else class="w-3 h-3" />
            {{ copiedField === 'validationUrl' ? '已复制链接' : '复制验证链接' }}
          </button>
        </div>
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

        <!-- 登录账号（直接明文显示，未自定义时默认展示账号邮箱） -->
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
              默认同邮箱，可修改
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
                type="text"
                autocomplete="off"
                autocorrect="off"
                autocapitalize="none"
                spellcheck="false"
                :placeholder="`默认同邮箱：${account.email}`"
                class="w-full pl-8 pr-2.5 py-1.5 text-xs rounded-lg border font-mono-num"
                style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
              />
            </div>
            <button
              type="button"
              title="复制登录账号"
              aria-label="复制登录账号"
              class="p-1.5 rounded-lg border shrink-0 cursor-pointer hover:bg-[var(--bg-hover)]"
              style="border-color: var(--border-strong); color: var(--text-secondary)"
              @click="handleCopy('loginAccount', formLoginAccount.trim() || account.email, '登录账号')"
            >
              <Check v-if="copiedField === 'loginAccount'" class="w-3.5 h-3.5" style="color: var(--success)" />
              <Copy v-else class="w-3.5 h-3.5" />
            </button>
          </div>
        </div>

        <!-- 登录密码（默认隐藏，始终允许点击小眼睛按钮切换显示） -->
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
                :type="showPassword ? 'text' : 'password'"
                autocomplete="off"
                placeholder="可选，记录登录密码"
                class="w-full pl-8 pr-8 py-1.5 text-xs rounded-lg border font-mono-num"
                style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
              />
              <button
                type="button"
                :aria-pressed="showPassword"
                :title="showPassword ? '隐藏密码' : '显示密码'"
                aria-label="切换密码显示"
                class="absolute right-2 top-1/2 -translate-y-1/2 p-0.5 cursor-pointer transition-colors hover:opacity-80"
                style="color: var(--text-muted)"
                @click="showPassword = !showPassword"
              >
                <EyeOff v-if="showPassword" class="w-3.5 h-3.5" />
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

        <!-- 2FA TOTP 密钥及正下方动态验证码（均直接明文显示，仅在填写了 2FA 密钥后显示验证码与复制按钮） -->
        <div class="space-y-2">
          <div>
            <label for="detail-2fa-input" class="block text-[11.5px] mb-1" style="color: var(--text-muted)">
              2FA 密钥（支持 Base32 或 otpauth:// URI）
            </label>
            <div class="flex items-center gap-1.5">
              <input
                id="detail-2fa-input"
                v-model="formTwoFactorSecret"
                type="text"
                autocomplete="off"
                autocorrect="off"
                autocapitalize="none"
                spellcheck="false"
                :aria-invalid="Boolean(hasTwoFactorSecret && totpError)"
                :aria-describedby="hasTwoFactorSecret && totpError ? 'detail-2fa-error' : undefined"
                placeholder="例如：JBSWY3DPEHPK3PXP"
                class="flex-1 min-w-0 px-2.5 py-1.5 text-xs rounded-lg border font-mono-num"
                style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
              />
              <button
                type="button"
                :disabled="!hasTwoFactorSecret"
                title="复制 2FA 密钥"
                aria-label="复制 2FA 密钥"
                class="p-1.5 rounded-lg border shrink-0 cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-40"
                style="border-color: var(--border-strong); color: var(--text-secondary)"
                @click="handleCopy('twoFactorSecret', formTwoFactorSecret.trim(), '2FA 密钥')"
              >
                <Check v-if="copiedField === 'twoFactorSecret'" class="w-3.5 h-3.5" style="color: var(--success)" />
                <Copy v-else class="w-3.5 h-3.5" />
              </button>
            </div>
          </div>

          <!-- 仅在填写了 2FA 密钥后，直接在正下方明文显示 6 位验证码与一键复制 -->
          <div
            v-if="hasTwoFactorSecret"
            class="p-3 rounded-xl border"
            style="border-color: var(--border-subtle); background-color: var(--bg-app)"
          >
            <template v-if="!totpError">
              <div class="flex items-center justify-between gap-2 mb-1.5">
                <span class="inline-flex items-center gap-1.5 text-[11.5px] font-medium" style="color: var(--text-secondary)">
                  <ShieldCheck class="w-3.5 h-3.5 shrink-0" style="color: var(--accent)" />
                  <span>2FA 验证码</span>
                  <span
                    v-if="isSecretUnsaved"
                    class="px-1.5 py-0.5 rounded text-[10px] font-medium"
                    style="background-color: var(--warning-soft); color: var(--warning-text)"
                  >
                    未保存预览
                  </span>
                </span>
                <span
                  v-if="totpResult"
                  :title="`有效期至 ${formatDateTime(totpResult.validUntilUnix)}`"
                  class="text-[11px] font-mono-num"
                  style="color: var(--text-muted)"
                >
                  剩余 {{ totpResult.remainingSeconds }}s
                </span>
              </div>

              <div class="flex items-center justify-between gap-3">
                <button
                  type="button"
                  :disabled="!totpResult?.code"
                  title="点击复制 2FA 验证码"
                  aria-label="点击验证码数字直接复制"
                  class="text-xl font-bold tracking-widest font-mono-num cursor-pointer text-left transition-opacity hover:opacity-80 disabled:cursor-default"
                  style="color: var(--text-primary)"
                  @click="totpResult && handleCopy('totp', totpResult.code, '2FA 验证码')"
                >
                  {{ formattedTotpCode }}
                </button>

                <div class="flex items-center gap-2 shrink-0">
                  <!-- 30秒倒计时圆环 -->
                  <svg v-if="totpResult" class="w-7 h-7 -rotate-90" viewBox="0 0 36 36" aria-hidden="true">
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
                    :disabled="!totpResult?.code"
                    aria-label="复制 2FA 验证码"
                    class="inline-flex items-center gap-1 px-2.5 py-1.5 rounded-lg border text-xs font-medium cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50"
                    style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
                    @click="totpResult && handleCopy('totp', totpResult.code, '2FA 验证码')"
                  >
                    <Check v-if="copiedField === 'totp'" class="w-3.5 h-3.5" style="color: var(--success)" />
                    <Copy v-else class="w-3.5 h-3.5" />
                    <span>{{ copiedField === 'totp' ? '已复制' : '复制验证码' }}</span>
                  </button>
                </div>
              </div>
            </template>

            <p v-else id="detail-2fa-error" class="text-xs" style="color: var(--danger-text)">
              {{ totpError }}
            </p>
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
              :disabled="!refreshTokenValue"
              :aria-pressed="refreshTokenRevealed"
              :title="refreshTokenRevealed ? '隐藏 Refresh Token' : '显示完整 Refresh Token'"
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
              @click="handleCopy('refreshToken', refreshTokenValue, 'Refresh Token')"
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

      <!-- 额度总览（主分类：Gemini Argon / Gemini Carbon / Gemini Flash / Claude/GPT，支持 5h 与周额度双窗口展示） -->
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
                {{ cat.fiveHour ? '5h + 周额度' : '仅周额度' }}
              </span>
            </div>

            <!-- 5 小时额度行（存在时展示） -->
            <div v-if="cat.fiveHour" class="space-y-1">
              <div class="flex items-center justify-between gap-2 text-[11.5px]">
                <span class="inline-flex items-center gap-1.5" style="color: var(--text-secondary)">
                  <span
                    class="px-1.5 py-0.5 rounded text-[10px] font-semibold"
                    style="background-color: var(--bg-subtle); color: var(--text-muted)"
                  >
                    5小时
                  </span>
                  <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.fiveHour.percentage)">
                    {{ cat.fiveHour.percentage }}%
                  </span>
                  <span
                    v-if="cat.fiveHour.exhausted"
                    class="px-1 py-0.5 rounded text-[9.5px] font-medium"
                    style="background-color: var(--danger-soft); color: var(--danger-text)"
                  >
                    已耗尽
                  </span>
                </span>
                <span class="text-[11px] tabular-nums shrink-0" style="color: var(--text-muted)">
                  {{ cat.fiveHour.resetTime ? `重置 ${formatResetCountdown(cat.fiveHour.resetTime, appStore.nowMs)}` : '未返回重置时间' }}
                </span>
              </div>
              <QuotaBar :percentage="cat.fiveHour.percentage" />
            </div>

            <!-- 周额度行：有周额度数据直接展示；有 5h 限额但尚未拉到周桶时显示待同步占位 -->
            <div v-if="cat.weekly || cat.fiveHour" class="space-y-1">
              <template v-if="cat.weekly">
                <div class="flex items-center justify-between gap-2 text-[11.5px]">
                  <span class="inline-flex items-center gap-1.5" style="color: var(--text-secondary)">
                    <span
                      class="px-1.5 py-0.5 rounded text-[10px] font-semibold"
                      style="background-color: var(--bg-subtle); color: var(--text-muted)"
                    >
                      周额度
                    </span>
                    <span class="font-mono-num font-semibold" :style="quotaValueStyle(cat.weekly.percentage)">
                      {{ cat.weekly.percentage }}%
                    </span>
                    <span
                      v-if="cat.weekly.exhausted"
                      class="px-1 py-0.5 rounded text-[9.5px] font-medium"
                      style="background-color: var(--danger-soft); color: var(--danger-text)"
                    >
                      已耗尽
                    </span>
                  </span>
                  <span class="text-[11px] tabular-nums shrink-0" style="color: var(--text-muted)">
                    {{ cat.weekly.resetTime ? `重置 ${formatResetCountdown(cat.weekly.resetTime, appStore.nowMs)}` : '未返回重置时间' }}
                  </span>
                </div>
                <QuotaBar :percentage="cat.weekly.percentage" />
              </template>
              <template v-else>
                <div class="flex items-center justify-between gap-2 text-[11.5px]">
                  <span class="inline-flex items-center gap-1.5" style="color: var(--text-secondary)">
                    <span
                      class="px-1.5 py-0.5 rounded text-[10px] font-semibold"
                      style="background-color: var(--bg-subtle); color: var(--text-muted)"
                    >
                      周额度
                    </span>
                    <span class="font-mono-num font-medium" style="color: var(--text-muted)">
                      {{ isRefreshing ? '正在同步...' : '--' }}
                    </span>
                  </span>
                  <span class="text-[11px] shrink-0" style="color: var(--text-muted)">
                    {{ isRefreshing ? '拉取云端摘要' : '点击上方刷新配额' }}
                  </span>
                </div>
                <div
                  class="w-full h-[5px] rounded-full overflow-hidden"
                  style="background-color: var(--bg-subtle)"
                />
              </template>
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

      <!-- 全量模型配额明细（展示云端返回的全部客户端模型、思考深度变体与各自重置时间） -->
      <section v-if="mergedModels.length > 0" class="space-y-2.5">
        <div class="flex items-center justify-between gap-2">
          <h3 class="text-xs font-semibold" style="color: var(--text-primary)">
            全量模型配额明细 ({{ mergedModels.length }})
          </h3>
          <span class="text-[11px]" style="color: var(--text-muted)">同模型多深度已合并</span>
        </div>

        <div class="space-y-2">
          <div
            v-for="model in mergedModels"
            :key="model.key"
            class="p-2.5 rounded-xl border space-y-1.5"
            style="border-color: var(--border-subtle); background-color: var(--bg-app)"
          >
            <div class="flex items-center justify-between gap-2 text-xs">
              <span
                class="font-medium truncate"
                :title="`${model.displayName} (${model.baseId})`"
                style="color: var(--text-primary)"
              >
                {{ model.displayName }}
              </span>
              <span class="inline-flex items-center gap-2 shrink-0">
                <span v-if="model.resetTime" class="text-[11px] tabular-nums" style="color: var(--text-muted)">
                  {{ formatResetCountdown(model.resetTime, appStore.nowMs) }}
                </span>
                <span class="font-mono-num font-semibold" :style="quotaValueStyle(model.percentage)">
                  {{ model.percentage }}%
                </span>
              </span>
            </div>

            <QuotaBar :percentage="model.percentage" />

            <div
              v-if="model.variants.length > 1 || model.variants[0]?.depthLabel !== '标准'"
              class="flex items-center flex-wrap gap-1 pt-0.5"
            >
              <span
                v-for="v in model.variants"
                :key="v.name"
                :title="v.name"
                class="px-1.5 py-0.5 rounded text-[10px] font-mono-num"
                style="background-color: var(--bg-subtle); color: var(--text-muted)"
              >
                {{ model.hasMixedPercentages ? `${v.depthLabel}: ${v.percentage}%` : v.depthLabel }}
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
