<script setup lang="ts">
// 精简三入口添加账号弹窗：1) Google OAuth 浏览器授权（含手动回调兜底）2) 导入本机已登录账号 3) Refresh Token / JSON 批量导入。
import { ref, watch, onBeforeUnmount } from 'vue'
import { Globe, HardDriveDownload, KeyRound, Copy, Check, ExternalLink, Loader2 } from 'lucide-vue-next'
import BaseModal from '@/components/common/BaseModal.vue'
import { useAppStore } from '@/stores/appStore'
import { api, extractErrorMessage, onWailsEvent } from '@/services/api'
import { copyToClipboard } from '@/utils/privacy'
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

// OAuth 状态
const oauthWaiting = ref(false)
const oauthAuthUrl = ref('')
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

let offComplete: (() => void) | null = null
let offFailed: (() => void) | null = null

function bindOAuthEvents() {
  unbindOAuthEvents()
  offComplete = onWailsEvent('oauth:completed', async (payload: unknown) => {
    const p = payload as { account?: Account } | null
    oauthWaiting.value = false
    oauthAuthUrl.value = ''
    oauthCallbackInput.value = ''
    await appStore.reloadAccounts()
    if (p?.account?.email) {
      appStore.notify(`已通过 Google 授权添加账号 ${p.account.email}`, 'success')
      appStore.activeDetailAccountId = p.account.id
    } else {
      appStore.notify('OAuth 授权成功', 'success')
    }
    emit('close')
  })
  offFailed = onWailsEvent('oauth:failed', (payload: unknown) => {
    const p = payload as { error?: string } | null
    oauthWaiting.value = false
    oauthError.value = p?.error || 'OAuth 授权失败'
  })
}

function unbindOAuthEvents() {
  offComplete?.()
  offFailed?.()
  offComplete = null
  offFailed = null
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
      // 若当前正筛选某个自定义分组，默认归入该分组
      if (
        appStore.selectedGroupFilter !== 'all' &&
        appStore.selectedGroupFilter !== 'ungrouped'
      ) {
        targetGroupId.value = appStore.selectedGroupFilter
      } else {
        targetGroupId.value = ''
      }
      bindOAuthEvents()
    } else {
      unbindOAuthEvents()
      if (oauthWaiting.value) {
        oauthWaiting.value = false
        oauthAuthUrl.value = ''
        void api.cancelOAuthLogin().catch(() => {})
      }
    }
  },
)

onBeforeUnmount(() => {
  unbindOAuthEvents()
})

async function startOAuth() {
  oauthError.value = ''
  oauthWaiting.value = true
  try {
    const sess = await api.startOAuthLogin(targetGroupId.value)
    oauthAuthUrl.value = sess.authUrl
  } catch (err) {
    oauthWaiting.value = false
    oauthError.value = extractErrorMessage(err)
  }
}

async function cancelOAuth() {
  oauthWaiting.value = false
  oauthAuthUrl.value = ''
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
  if (await copyToClipboard(oauthAuthUrl.value)) {
    oauthUrlCopied.value = true
    window.setTimeout(() => {
      oauthUrlCopied.value = false
    }, 1800)
  }
}

async function handleImportLocal() {
  localImporting.value = true
  localError.value = ''
  try {
    const acc = await api.importFromLocal(targetGroupId.value)
    await appStore.reloadAccounts()
    appStore.activeDetailAccountId = acc.id
    appStore.notify(`已从本机导入账号 ${acc.email}`, 'success')
    emit('close')
  } catch (err) {
    localError.value = extractErrorMessage(err)
  } finally {
    localImporting.value = false
  }
}

async function handleImportTokens() {
  if (!tokenInput.value.trim()) {
    tokenError.value = '请输入 Refresh Token（每行一条）或粘贴账号 JSON'
    return
  }
  tokenImporting.value = true
  tokenError.value = ''
  tokenSummary.value = ''
  try {
    const res = await api.importRefreshTokens(tokenInput.value.trim(), targetGroupId.value)
    await appStore.reloadAccounts()
    const okCount = res.imported?.length ?? 0
    const failCount = res.failed?.length ?? 0
    if (failCount === 0 && okCount > 0) {
      appStore.notify(`成功导入 ${okCount} 个账号`, 'success')
      tokenInput.value = ''
      emit('close')
    } else if (okCount > 0) {
      tokenSummary.value = `成功导入 ${okCount} 个，失败 ${failCount} 个：${res.failed.map((f) => `${f.source}(${f.error})`).join('；')}`
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
    subtitle="支持 Google OAuth 授权、读取本机已登录账号或批量导入 Refresh Token / JSON"
    width-class="w-[540px]"
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
        <select
          id="add-target-group"
          v-model="targetGroupId"
          class="w-52 px-2.5 py-1.5 text-xs rounded-lg border"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
        >
          <option value="">未分组</option>
          <option v-for="g in appStore.groups" :key="g.id" :value="g.id">
            {{ g.name }}
          </option>
        </select>
      </div>

      <!-- Tab 1: Google OAuth -->
      <div v-if="activeTab === 'oauth'" class="space-y-3.5 pt-1">
        <div
          class="p-4 rounded-xl border space-y-3"
          style="border-color: var(--border-subtle); background-color: var(--bg-app)"
        >
          <p class="text-xs leading-relaxed" style="color: var(--text-secondary)">
            点击下方按钮将在系统默认浏览器打开 Google 授权页。登录并同意权限后，浏览器会自动回调本地端口完成入库。
          </p>

          <div v-if="!oauthWaiting" class="flex items-center gap-2">
            <button
              type="button"
              class="inline-flex items-center gap-2 px-4 py-2 rounded-lg text-xs font-medium text-white cursor-pointer"
              style="background-color: var(--accent)"
              @click="startOAuth"
            >
              <ExternalLink class="w-3.5 h-3.5" />
              打开浏览器开始授权
            </button>
          </div>

          <div v-else class="space-y-3">
            <div class="flex items-center justify-between gap-2">
              <span class="inline-flex items-center gap-2 text-xs font-medium" style="color: var(--accent)">
                <Loader2 class="w-3.5 h-3.5 animate-spin" />
                正在等待浏览器回调...
              </span>
              <div class="flex items-center gap-1.5">
                <button
                  type="button"
                  class="inline-flex items-center gap-1 px-2.5 py-1 rounded-md border text-[11px] cursor-pointer hover:bg-[var(--bg-hover)]"
                  style="border-color: var(--border-strong); color: var(--text-secondary); background-color: var(--bg-surface)"
                  @click="copyAuthUrl"
                >
                  <Check v-if="oauthUrlCopied" class="w-3 h-3" style="color: var(--success)" />
                  <Copy v-else class="w-3 h-3" />
                  {{ oauthUrlCopied ? '已复制链接' : '复制授权链接' }}
                </button>
                <button
                  type="button"
                  class="px-2.5 py-1 rounded-md border text-[11px] cursor-pointer hover:bg-[var(--bg-hover)]"
                  style="border-color: var(--border-strong); color: var(--text-secondary); background-color: var(--bg-surface)"
                  @click="cancelOAuth"
                >
                  取消等待
                </button>
              </div>
            </div>

            <!-- 手动粘贴回调 URL 兜底 -->
            <div class="pt-2 border-t space-y-1.5" style="border-color: var(--border-subtle)">
              <label for="manual-callback-url" class="block text-[11px]" style="color: var(--text-muted)">
                若浏览器未能自动跳回，可复制地址栏中的完整回调 URL 粘贴至此处：
              </label>
              <div class="flex gap-2">
                <input
                  id="manual-callback-url"
                  v-model="oauthCallbackInput"
                  type="text"
                  placeholder="http://127.0.0.1:xxxxx/oauth-callback?code=..."
                  class="flex-1 px-2.5 py-1.5 text-xs rounded-lg border font-mono-num"
                  style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
                />
                <button
                  type="button"
                  class="px-3 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer"
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

        <div class="flex justify-end">
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
    </div>
  </BaseModal>
</template>
