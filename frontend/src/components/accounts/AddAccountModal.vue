<script setup lang="ts">
// 三入口添加账号弹窗：1) Google OAuth 授权（支持直接跳转网页验证或仅复制授权链接，含手动回调兜底）2) 读取本机已登录账号 3) Refresh Token / JSON 批量导入，并内置需网页验证账号直达操作。
import { computed, ref, watch } from 'vue'
import {
  Globe,
  HardDriveDownload,
  KeyRound,
  Copy,
  Check,
  ExternalLink,
  Loader2,
  AlertTriangle,
  RefreshCw,
} from 'lucide-vue-next'
import BaseModal from '@/components/common/BaseModal.vue'
import AppSelect from '@/components/common/AppSelect.vue'
import { useAppStore } from '@/stores/appStore'
import { api, extractErrorMessage, isWailsRuntime } from '@/services/api'
import { copyToClipboard, maskEmail, maskToken } from '@/utils/privacy'
import { getAccountValidationUrl, isAccountValidationRequired } from '@/utils/quota'
import type { Account } from '@/types/models'

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const appStore = useAppStore()

type AddTab = 'oauth' | 'local' | 'token'
const activeTab = ref<AddTab>('oauth')
const targetGroupId = ref('')
const groupOptions = computed(() => [
  { value: '', label: '未分组' },
  ...appStore.groups.map((g) => ({ value: g.id, label: g.name })),
])

// OAuth 状态
const oauthWaiting = ref(false)
const oauthStartingMode = ref<'init' | 'open' | 'copy' | 'reset' | null>(null)
const oauthAuthUrl = ref('')
const oauthRedirectUri = ref('')
const oauthCallbackInput = ref('')
const oauthUrlCopied = ref(false)
const oauthError = ref('')

// 本机导入状态
const localImporting = ref(false)
const localError = ref('')

// Token / JSON 批量导入状态
const tokenPlaceholder = '1//0g...\n或粘贴 [{"email":"...","refresh_token":"1//0g..."}]'

const tokenInput = ref('')
const tokenImporting = ref(false)
const tokenError = ref('')
const tokenSummary = ref('')

// 导入后检测到需要网页验证的账号列表（支持在弹窗内直接复制链接或打开网页验证）
const pendingVerifyAccounts = ref<Account[]>([])
const copiedVerifyId = ref('')

// 等待 OAuth 回调期间若用户切换了目标分组，实时同步至后端会话
watch(targetGroupId, (nextGroupId) => {
  if (oauthWaiting.value && isWailsRuntime()) {
    void api.setOAuthTargetGroup(nextGroupId).catch(() => {})
  }
})

// 接收 appStore 全局 OAuth 入库完成通知，自动收起弹窗并清理本地临时状态
watch(
  () => appStore.oauthCompletedSeq,
  () => {
    oauthWaiting.value = false
    oauthStartingMode.value = null
    oauthAuthUrl.value = ''
    oauthRedirectUri.value = ''
    oauthCallbackInput.value = ''
    oauthError.value = ''
    if (props.open) {
      emit('close')
    }
  },
)

// 接收 appStore 全局 OAuth 失败/超时通知，同步复位等待态并展示错误详情
watch(
  () => appStore.oauthFailedSeq,
  () => {
    oauthWaiting.value = false
    oauthStartingMode.value = null
    oauthAuthUrl.value = ''
    oauthRedirectUri.value = ''
    oauthError.value = appStore.oauthLastError || 'OAuth 授权失败'
  },
)

// 确保本地 OAuth 回调监听已开启（优先复用后台仍在运行中的监听端口，避免重复生成使旧链接失效）
async function ensureOAuthListening(mode: 'init' | 'open' | 'copy' = 'init') {
  if (!isWailsRuntime()) return null
  if (oauthStartingMode.value) return null
  oauthError.value = ''
  oauthStartingMode.value = mode
  try {
    const sess =
      mode === 'open'
        ? await api.startOAuthLogin(targetGroupId.value)
        : await api.prepareOAuthLogin(targetGroupId.value)
    oauthAuthUrl.value = sess.authUrl
    oauthRedirectUri.value = sess.redirectUri
    oauthWaiting.value = true
    return sess
  } catch (err) {
    oauthWaiting.value = false
    oauthAuthUrl.value = ''
    oauthRedirectUri.value = ''
    oauthError.value = extractErrorMessage(err)
    return null
  } finally {
    oauthStartingMode.value = null
  }
}

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) {
      oauthError.value = ''
      localError.value = ''
      tokenError.value = ''
      tokenSummary.value = ''
      oauthCallbackInput.value = ''
      pendingVerifyAccounts.value = []
      copiedVerifyId.value = ''
      // 若当前正筛选某个自定义分组，默认归入该分组
      if (
        appStore.selectedGroupFilter !== 'all' &&
        appStore.selectedGroupFilter !== 'ungrouped'
      ) {
        targetGroupId.value = appStore.selectedGroupFilter
      } else {
        targetGroupId.value = ''
      }
      // 打开弹窗并处于 OAuth 标签页时立即同步或预开启本地回调监听，确保复制/跳转前端口已就绪
      if (activeTab.value === 'oauth') {
        void ensureOAuthListening('init')
      }
    }
    // 注意：关闭弹窗时不再调用 cancelOAuthLogin()，让本地回调监听在后台继续保持活跃直至完成、超时或用户主动停止
  },
)

watch(activeTab, (tab) => {
  if (props.open && tab === 'oauth' && !oauthWaiting.value && !oauthStartingMode.value) {
    void ensureOAuthListening('init')
  }
})

// 方式 A：确保本地回调监听开启并直接打开系统默认浏览器跳转网页验证
async function startOAuthAndOpen() {
  await ensureOAuthListening('open')
}

// 方式 B：确保本地回调监听开启并复制授权链接（不自动弹系统浏览器，便于粘贴到指纹浏览器或无痕窗口）
async function prepareAndCopyOAuthUrl() {
  const sess = oauthWaiting.value && oauthAuthUrl.value
    ? { authUrl: oauthAuthUrl.value }
    : await ensureOAuthListening('copy')
  if (!sess?.authUrl) return
  if (await copyToClipboard(sess.authUrl)) {
    oauthUrlCopied.value = true
    appStore.notify('已复制授权链接，本地回调监听保持开启中', 'success', 2800)
    window.setTimeout(() => {
      oauthUrlCopied.value = false
    }, 2000)
  } else {
    appStore.notify('已生成授权链接，请手动点击复制授权链接', 'info')
  }
}

// 显式重置并重新生成全新的本地回调监听端口与授权链接
async function resetOAuthSession() {
  if (!isWailsRuntime() || oauthStartingMode.value) return
  oauthError.value = ''
  oauthStartingMode.value = 'reset'
  try {
    await api.cancelOAuthLogin().catch(() => {})
    const sess = await api.prepareOAuthLogin(targetGroupId.value)
    oauthAuthUrl.value = sess.authUrl
    oauthRedirectUri.value = sess.redirectUri
    oauthCallbackInput.value = ''
    oauthWaiting.value = true
    appStore.notify('已重新生成授权链接并刷新本地回调端口', 'info', 2200)
  } catch (err) {
    oauthWaiting.value = false
    oauthAuthUrl.value = ''
    oauthRedirectUri.value = ''
    oauthError.value = extractErrorMessage(err)
  } finally {
    oauthStartingMode.value = null
  }
}

// 在等待回调期间直接打开当前会话的授权链接（复用当前监听端口，不重新生成覆盖已复制的链接）
async function openCurrentOAuthUrl() {
  if (!oauthAuthUrl.value) {
    await startOAuthAndOpen()
    return
  }
  try {
    await api.openExternalURL(oauthAuthUrl.value)
  } catch (err) {
    oauthError.value = extractErrorMessage(err)
  }
}

async function cancelOAuth() {
  oauthWaiting.value = false
  oauthStartingMode.value = null
  oauthAuthUrl.value = ''
  oauthRedirectUri.value = ''
  oauthCallbackInput.value = ''
  await api.cancelOAuthLogin().catch(() => {})
}

async function submitManualCallback() {
  if (!oauthCallbackInput.value.trim()) {
    oauthError.value = '请粘贴浏览器地址栏中的回调链接'
    return
  }
  oauthError.value = ''
  try {
    await api.submitOAuthCallbackURL(oauthCallbackInput.value.trim())
  } catch (err) {
    oauthError.value = extractErrorMessage(err)
  }
}

async function copyAuthUrl() {
  if (!oauthAuthUrl.value) return
  if (await copyToClipboard(oauthAuthUrl.value)) {
    oauthUrlCopied.value = true
    appStore.notify('已复制授权链接', 'success', 2000)
    window.setTimeout(() => {
      oauthUrlCopied.value = false
    }, 1800)
  }
}

async function openVerifyUrl(url: string) {
  if (!url) return
  try {
    await api.openExternalURL(url)
  } catch (err) {
    appStore.notify(`打开验证链接失败：${extractErrorMessage(err)}`, 'error')
  }
}

async function copyVerifyUrl(accountId: string, url: string) {
  if (!url) return
  if (await copyToClipboard(url)) {
    copiedVerifyId.value = accountId
    appStore.notify('已复制网页验证链接', 'success', 2000)
    window.setTimeout(() => {
      if (copiedVerifyId.value === accountId) {
        copiedVerifyId.value = ''
      }
    }, 1800)
  }
}

async function handleImportLocal() {
  localImporting.value = true
  localError.value = ''
  pendingVerifyAccounts.value = []
  try {
    const acc = await api.importFromLocal(targetGroupId.value)
    await appStore.reloadAccounts()
    // 同步侧边栏本机激活探测状态，消除外部未纳管账号提示残留
    appStore.activeDetection = {
      hasCredential: true,
      managed: true,
      matchedAccountId: acc.id,
      detectedEmail: acc.email,
      targetClient: appStore.settings.runtimeTarget,
      changed: false,
      detectedAt: Math.floor(Date.now() / 1000),
    }
    appStore.activeDetailAccountId = acc.id
    const emailLabel = maskEmail(acc.email, appStore.settings.privacyMode)
    if (isAccountValidationRequired(acc)) {
      pendingVerifyAccounts.value = [acc]
      appStore.notify(`已导入账号 ${emailLabel}，该账号需完成网页验证`, 'warning', 4500)
    } else {
      appStore.notify(`已从本机导入账号 ${emailLabel}`, 'success')
      emit('close')
    }
  } catch (err) {
    localError.value = extractErrorMessage(err)
  } finally {
    localImporting.value = false
  }
}

function formatFailedSource(source: string): string {
  if (!appStore.settings.privacyMode) return source
  if (source.includes('@')) return maskEmail(source, true)
  if (source.startsWith('第 ')) return source
  return maskToken(source)
}

async function handleImportTokens() {
  if (!tokenInput.value.trim()) {
    tokenError.value = '请输入 Refresh Token（每行一条）或粘贴账号 JSON'
    return
  }
  tokenImporting.value = true
  tokenError.value = ''
  tokenSummary.value = ''
  pendingVerifyAccounts.value = []
  try {
    const res = await api.importRefreshTokens(tokenInput.value.trim(), targetGroupId.value)
    await appStore.reloadAccounts()
    const importedList = res.imported ?? []
    const okCount = importedList.length
    const failCount = res.failed?.length ?? 0
    const verifyList = importedList.filter((acc) => isAccountValidationRequired(acc))
    pendingVerifyAccounts.value = verifyList

    if (failCount === 0 && okCount > 0) {
      tokenInput.value = ''
      if (verifyList.length > 0) {
        appStore.notify(
          `成功导入 ${okCount} 个账号，其中 ${verifyList.length} 个需完成网页验证`,
          'warning',
          4800,
        )
      } else {
        appStore.notify(`成功导入 ${okCount} 个账号`, 'success')
        emit('close')
      }
    } else if (okCount > 0) {
      tokenSummary.value = `成功导入 ${okCount} 个，失败 ${failCount} 个：${res.failed.map((f) => `${formatFailedSource(f.source)}(${f.error})`).join('；')}`
      appStore.notify(`部分导入成功（${okCount} 成功 / ${failCount} 失败）`, 'warning')
    } else {
      tokenError.value = res.failed?.[0]?.error || '导入失败，请检查令牌或 JSON 格式'
    }
  } catch (err) {
    tokenError.value = extractErrorMessage(err)
  } finally {
    tokenImporting.value = false
  }
}
</script>

<template>
  <BaseModal
    :open="open"
    title="添加反重力账号"
    subtitle="支持网页验证授权 / 复制授权链接、读取本机账号或批量导入 Refresh Token / JSON"
    width-class="w-[548px]"
    :prevent-close="localImporting || tokenImporting"
    :close-on-outside="!(localImporting || tokenImporting)"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <!-- 三种方式分段切换 -->
      <div
        class="grid grid-cols-3 gap-1 p-1 rounded-xl"
        style="background-color: var(--bg-subtle)"
      >
        <button
          type="button"
          class="flex items-center justify-center gap-1.5 py-2 rounded-lg text-xs font-medium cursor-pointer transition-colors"
          :style="
            activeTab === 'oauth'
              ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-primary)', boxShadow: 'var(--shadow-card)' }
              : { color: 'var(--text-secondary)' }
          "
          @click="activeTab = 'oauth'"
        >
          <Globe class="w-3.5 h-3.5" />
          <span>OAuth 授权</span>
        </button>

        <button
          type="button"
          class="flex items-center justify-center gap-1.5 py-2 rounded-lg text-xs font-medium cursor-pointer transition-colors"
          :style="
            activeTab === 'local'
              ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-primary)', boxShadow: 'var(--shadow-card)' }
              : { color: 'var(--text-secondary)' }
          "
          @click="activeTab = 'local'"
        >
          <HardDriveDownload class="w-3.5 h-3.5" />
          <span>读取本机</span>
        </button>

        <button
          type="button"
          class="flex items-center justify-center gap-1.5 py-2 rounded-lg text-xs font-medium cursor-pointer transition-colors"
          :style="
            activeTab === 'token'
              ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-primary)', boxShadow: 'var(--shadow-card)' }
              : { color: 'var(--text-secondary)' }
          "
          @click="activeTab = 'token'"
        >
          <KeyRound class="w-3.5 h-3.5" />
          <span>Token / JSON</span>
        </button>
      </div>

      <!-- 目标归属分组选择 -->
      <div class="flex items-center justify-between gap-3">
        <label for="add-target-group" class="text-xs font-medium" style="color: var(--text-secondary)">
          导入后自动归入分组
        </label>
        <AppSelect
          id="add-target-group"
          v-model="targetGroupId"
          :options="groupOptions"
          class="w-52"
        />
      </div>

      <!-- Tab 1: Google OAuth -->
      <div v-if="activeTab === 'oauth'" class="space-y-3.5 pt-1">
        <div
          class="p-4 rounded-xl border space-y-3"
          style="border-color: var(--border-subtle); background-color: var(--bg-app)"
        >
          <p class="text-xs leading-relaxed" style="color: var(--text-secondary)">
            可直接点击「网页验证授权」跳转到浏览器完成登录，或点击「复制授权链接」粘贴至无痕窗口 / 指纹浏览器完成授权回调。
          </p>

          <div v-if="!oauthWaiting" class="flex items-center flex-wrap gap-2.5">
            <button
              type="button"
              :disabled="Boolean(oauthStartingMode)"
              class="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg text-xs font-medium text-white cursor-pointer transition-opacity hover:opacity-95 disabled:opacity-60"
              style="background-color: var(--accent)"
              @click="startOAuthAndOpen"
            >
              <Loader2 v-if="oauthStartingMode === 'open'" class="w-3.5 h-3.5 animate-spin" />
              <ExternalLink v-else class="w-3.5 h-3.5" />
              <span>网页验证授权</span>
            </button>

            <button
              type="button"
              :disabled="Boolean(oauthStartingMode)"
              class="inline-flex items-center gap-1.5 px-3.5 py-2 rounded-lg border text-xs font-medium cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-60"
              style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
              @click="prepareAndCopyOAuthUrl"
            >
              <Loader2 v-if="oauthStartingMode === 'copy'" class="w-3.5 h-3.5 animate-spin" />
              <Copy v-else class="w-3.5 h-3.5" />
              <span>复制授权链接</span>
            </button>
          </div>

          <div v-else class="space-y-3">
            <div class="flex items-center justify-between flex-wrap gap-2">
              <span class="inline-flex items-center gap-2 text-xs font-medium" style="color: var(--success-text)">
                <span class="relative flex w-2 h-2 shrink-0">
                  <span
                    class="animate-ping absolute inline-flex h-full w-full rounded-full opacity-75"
                    style="background-color: var(--success)"
                  />
                  <span
                    class="relative inline-flex rounded-full w-2 h-2"
                    style="background-color: var(--success)"
                  />
                </span>
                本地回调监听已开启，等待授权回跳...
              </span>
              <div class="flex items-center flex-wrap gap-1.5">
                <button
                  type="button"
                  class="inline-flex items-center gap-1 px-2.5 py-1 rounded-md text-[11px] font-medium text-white cursor-pointer transition-opacity hover:opacity-95"
                  style="background-color: var(--accent)"
                  @click="openCurrentOAuthUrl"
                >
                  <ExternalLink class="w-3 h-3" />
                  网页验证
                </button>
                <button
                  type="button"
                  class="inline-flex items-center gap-1 px-2.5 py-1 rounded-md border text-[11px] cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
                  style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
                  @click="copyAuthUrl"
                >
                  <Check v-if="oauthUrlCopied" class="w-3 h-3" style="color: var(--success)" />
                  <Copy v-else class="w-3 h-3" />
                  {{ oauthUrlCopied ? '已复制链接' : '复制授权链接' }}
                </button>
                <button
                  type="button"
                  title="重新生成授权链接与本地监听端口"
                  :disabled="Boolean(oauthStartingMode)"
                  class="inline-flex items-center gap-1 px-2 py-1 rounded-md border text-[11px] cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50"
                  style="border-color: var(--border-strong); color: var(--text-secondary); background-color: var(--bg-surface)"
                  @click="resetOAuthSession"
                >
                  <RefreshCw class="w-3 h-3" :class="{ 'animate-spin': oauthStartingMode === 'reset' }" />
                  换新
                </button>
                <button
                  type="button"
                  class="px-2.5 py-1 rounded-md border text-[11px] cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
                  style="border-color: var(--border-strong); color: var(--text-secondary); background-color: var(--bg-surface)"
                  @click="cancelOAuth"
                >
                  停止监听
                </button>
              </div>
            </div>

            <!-- 当前实时监听的回调地址提示，关闭弹窗后后台仍保持监听 -->
            <div
              v-if="oauthRedirectUri"
              class="px-2.5 py-1.5 rounded-lg border text-[11px] flex items-center justify-between gap-2"
              style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-muted)"
            >
              <span class="truncate font-mono-num" :title="oauthRedirectUri">
                监听地址：{{ oauthRedirectUri }}
              </span>
              <span class="shrink-0">关闭弹窗不影响后台监听</span>
            </div>

            <!-- 授权链接只读预览栏，方便确认或直接复制 -->
            <div v-if="oauthAuthUrl" class="flex items-center gap-1.5">
              <input
                :value="oauthAuthUrl"
                type="text"
                readonly
                aria-label="当前生成的 OAuth 授权链接"
                class="w-full px-2.5 py-1.5 text-[11px] rounded-lg border font-mono-num select-all"
                style="border-color: var(--border-subtle); background-color: var(--bg-surface); color: var(--text-secondary)"
                @click="($event.target as HTMLInputElement).select()"
              />
            </div>

            <!-- 手动粘贴回调 URL 兜底 -->
            <div class="pt-2 border-t space-y-1.5" style="border-color: var(--border-subtle)">
              <label for="manual-callback-url" class="block text-[11px]" style="color: var(--text-muted)">
                若跨环境或浏览器未能自动跳回，可复制地址栏中的完整回调 URL 粘贴至此处：
              </label>
              <div class="flex gap-2">
                <input
                  id="manual-callback-url"
                  v-model="oauthCallbackInput"
                  type="text"
                  :placeholder="oauthRedirectUri ? `${oauthRedirectUri}?code=...` : 'http://127.0.0.1:xxxxx/oauth-callback?code=...'"
                  class="flex-1 px-2.5 py-1.5 text-xs rounded-lg border font-mono-num"
                  style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
                />
                <button
                  type="button"
                  class="px-3 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer shrink-0"
                  style="background-color: var(--accent)"
                  @click="submitManualCallback"
                >
                  提交回调
                </button>
              </div>
            </div>
          </div>
        </div>

        <p v-if="oauthError" class="text-xs px-3 py-2 rounded-lg" style="background-color: var(--danger-soft); color: var(--danger-text)">
          {{ oauthError }}
        </p>
      </div>

      <!-- Tab 2: 读取本机已登录账号 -->
      <div v-else-if="activeTab === 'local'" class="space-y-3.5 pt-1">
        <div
          class="p-4 rounded-xl border space-y-3"
          style="border-color: var(--border-subtle); background-color: var(--bg-app)"
        >
          <p class="text-xs leading-relaxed" style="color: var(--text-secondary)">
            自动从本机当前正在使用的 Antigravity 客户端读取登录态（优先读取 2.0 系统凭据库与桌面回退文件，其次兼容旧版 / IDE 的
            <code class="font-mono-num">state.vscdb</code>），校验并同步最新配额。
          </p>
          <button
            type="button"
            :disabled="localImporting"
            class="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-medium text-white cursor-pointer disabled:opacity-60"
            style="background-color: var(--accent)"
            @click="handleImportLocal"
          >
            <Loader2 v-if="localImporting" class="w-3.5 h-3.5 animate-spin" />
            <HardDriveDownload v-else class="w-3.5 h-3.5" />
            {{ localImporting ? '正在读取并验证...' : '一键读取本机当前登录账号' }}
          </button>
        </div>

        <p v-if="localError" class="text-xs px-3 py-2 rounded-lg" style="background-color: var(--danger-soft); color: var(--danger-text)">
          {{ localError }}
        </p>
      </div>

      <!-- Tab 3: Refresh Token / JSON 批量导入 -->
      <div v-else class="space-y-3 pt-1">
        <div>
          <label for="token-import-textarea" class="block text-xs font-medium mb-1.5" style="color: var(--text-secondary)">
            每行一条 Refresh Token，或直接粘贴导出的 JSON 数组 / 对象
          </label>
          <textarea
            id="token-import-textarea"
            v-model="tokenInput"
            rows="6"
            :placeholder="tokenPlaceholder"
            class="w-full p-3 text-xs font-mono-num rounded-xl border resize-y custom-scroll"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>

        <p v-if="tokenError" class="text-xs px-3 py-2 rounded-lg" style="background-color: var(--danger-soft); color: var(--danger-text)">
          {{ tokenError }}
        </p>
        <p v-if="tokenSummary" class="text-xs px-3 py-2 rounded-lg" style="background-color: var(--warning-soft); color: var(--warning-text)">
          {{ tokenSummary }}
        </p>

        <div class="flex justify-end gap-2">
          <button
            v-if="pendingVerifyAccounts.length > 0"
            type="button"
            class="px-3.5 py-2 rounded-lg border text-xs font-medium cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
            style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
            @click="emit('close')"
          >
            完成并关闭
          </button>
          <button
            type="button"
            :disabled="tokenImporting"
            class="inline-flex items-center gap-1.5 px-4 py-2 rounded-lg text-xs font-medium text-white cursor-pointer disabled:opacity-60"
            style="background-color: var(--accent)"
            @click="handleImportTokens"
          >
            <Loader2 v-if="tokenImporting" class="w-3.5 h-3.5 animate-spin" />
            <span>{{ tokenImporting ? '验证并导入中...' : '开始导入' }}</span>
          </button>
        </div>
      </div>

      <!-- 导入后检测到需网页验证的账号快捷处理面板 -->
      <div
        v-if="pendingVerifyAccounts.length > 0 && activeTab !== 'oauth'"
        class="p-3.5 rounded-xl border space-y-2.5"
        style="background-color: var(--danger-soft); border-color: rgba(239, 68, 68, 0.3)"
      >
        <div class="flex items-center justify-between gap-2">
          <span class="inline-flex items-center gap-1.5 text-xs font-semibold" style="color: var(--danger-text)">
            <AlertTriangle class="w-3.5 h-3.5 shrink-0" />
            <span>检测到 {{ pendingVerifyAccounts.length }} 个已导入账号需完成网页验证</span>
          </span>
          <button
            v-if="activeTab === 'local'"
            type="button"
            class="text-[11px] underline cursor-pointer"
            style="color: var(--danger-text)"
            @click="emit('close')"
          >
            关闭弹窗
          </button>
        </div>

        <div class="space-y-1.5 max-h-36 overflow-y-auto custom-scroll pr-0.5">
          <div
            v-for="acc in pendingVerifyAccounts"
            :key="acc.id"
            class="flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-lg border text-xs"
            style="background-color: var(--bg-surface); border-color: var(--border-subtle)"
          >
            <span class="font-medium truncate min-w-0" style="color: var(--text-primary)">
              {{ maskEmail(acc.email, appStore.settings.privacyMode) }}
            </span>
            <div v-if="getAccountValidationUrl(acc)" class="flex items-center gap-1.5 shrink-0">
              <button
                type="button"
                class="inline-flex items-center gap-1 px-2 py-1 rounded-md text-[11px] font-medium text-white cursor-pointer"
                style="background-color: var(--danger)"
                @click="openVerifyUrl(getAccountValidationUrl(acc))"
              >
                <ExternalLink class="w-3 h-3" />
                网页验证
              </button>
              <button
                type="button"
                class="inline-flex items-center gap-1 px-2 py-1 rounded-md border text-[11px] cursor-pointer hover:bg-[var(--bg-hover)]"
                style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
                @click="copyVerifyUrl(acc.id, getAccountValidationUrl(acc))"
              >
                <Check v-if="copiedVerifyId === acc.id" class="w-3 h-3" style="color: var(--success)" />
                <Copy v-else class="w-3 h-3" />
                {{ copiedVerifyId === acc.id ? '已复制' : '复制链接' }}
              </button>
            </div>
            <span v-else class="text-[11px] shrink-0" style="color: var(--danger-text)">
              请在详情中查看
            </span>
          </div>
        </div>
      </div>
    </div>
  </BaseModal>
</template>
