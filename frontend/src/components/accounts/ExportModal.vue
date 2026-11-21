<script setup lang="ts">
// JSON 导出弹窗：支持隐私脱敏预览、一键复制完整 JSON 到剪贴板、保存为本地 0600 .json 文件，且可选是否附带密码与 2FA 密钥。
import { computed, ref, watch } from 'vue'
import { Copy, Check, Download, Eye, EyeOff, ShieldAlert } from 'lucide-vue-next'
import BaseModal from '@/components/common/BaseModal.vue'
import { api, extractErrorMessage } from '@/services/api'
import { copyToClipboard, maskEmail, maskToken } from '@/utils/privacy'
import { useAppStore } from '@/stores/appStore'

const props = defineProps<{
  open: boolean
  accountIds: string[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const appStore = useAppStore()
const includeSecrets = ref(false)
const jsonText = ref('')
const revealInPrivacy = ref(false)
const loading = ref(false)
const copied = ref(false)
const savingFile = ref(false)

const previewText = computed(() => {
  if (!jsonText.value) return ''
  if (!appStore.settings.privacyMode || revealInPrivacy.value) {
    return jsonText.value
  }
  try {
    const parsed = JSON.parse(jsonText.value) as Array<Record<string, unknown>>
    if (!Array.isArray(parsed)) return jsonText.value
    const masked = parsed.map((item) => {
      const copy: Record<string, unknown> = { ...item }
      if (typeof copy.email === 'string' && copy.email) {
        copy.email = maskEmail(copy.email, true)
      }
      if (typeof copy.refresh_token === 'string' && copy.refresh_token) {
        copy.refresh_token = maskToken(copy.refresh_token)
      }
      if (typeof copy.login_account === 'string' && copy.login_account) {
        copy.login_account = copy.login_account.includes('@')
          ? maskEmail(copy.login_account, true)
          : '••••••'
      }
      if (typeof copy.account_password === 'string' && copy.account_password) {
        copy.account_password = '••••••••'
      }
      if (typeof copy.two_factor_secret === 'string' && copy.two_factor_secret) {
        copy.two_factor_secret = '••••••••'
      }
      return copy
    })
    return JSON.stringify(masked, null, 2)
  } catch {
    return jsonText.value
  }
})

async function loadPreview() {
  loading.value = true
  try {
    jsonText.value = await api.exportAccounts(props.accountIds, includeSecrets.value)
  } catch (err) {
    appStore.notify(`生成导出数据失败：${extractErrorMessage(err)}`, 'error')
  } finally {
    loading.value = false
  }
}

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) {
      // 每次打开导出弹窗均重置为默认不包含密码与 2FA 密钥，并在隐私模式下默认隐藏预览明文
      includeSecrets.value = false
      revealInPrivacy.value = false
      copied.value = false
      void loadPreview()
    } else {
      jsonText.value = ''
    }
  },
)

watch(
  () => [includeSecrets.value, props.accountIds.join(',')] as const,
  () => {
    if (props.open) {
      void loadPreview()
    }
  },
)

async function handleCopy() {
  if (await copyToClipboard(jsonText.value)) {
    copied.value = true
    appStore.notify('已复制导出 JSON 到剪贴板', 'success')
    window.setTimeout(() => {
      copied.value = false
    }, 1800)
  }
}

async function handleSaveFile() {
  savingFile.value = true
  try {
    const savedPath = await api.saveExportToFile(props.accountIds, includeSecrets.value)
    if (savedPath) {
      appStore.notify(`已导出至 ${savedPath}`, 'success')
      emit('close')
    }
  } catch (err) {
    appStore.notify(`保存导出文件失败：${extractErrorMessage(err)}`, 'error')
  } finally {
    savingFile.value = false
  }
}
</script>

<template>
  <BaseModal
    :open="open"
    title="导出账号 JSON"
    :subtitle="accountIds.length > 0 ? `将导出已选中的 ${accountIds.length} 个账号` : '将导出全部账号'"
    width-class="w-[560px]"
    :prevent-close="savingFile"
    :close-on-outside="!savingFile"
    @close="emit('close')"
  >
    <div class="space-y-3.5">
      <label
        class="flex items-start gap-2.5 p-3 rounded-xl border cursor-pointer"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <input
          v-model="includeSecrets"
          type="checkbox"
          class="mt-0.5 rounded accent-[var(--accent)]"
        />
        <div class="text-xs">
          <div class="font-medium flex items-center gap-1.5" style="color: var(--text-primary)">
            <ShieldAlert class="w-3.5 h-3.5" style="color: var(--warning)" />
            附带导出登录账号、密码与 2FA 密钥
          </div>
          <p class="mt-0.5 text-[11px]" style="color: var(--text-muted)">
            默认仅导出邮箱、Refresh Token、所属分组与备注；勾选后请妥善保管导出文件。
          </p>
        </div>
      </label>

      <div class="space-y-1.5">
        <div v-if="appStore.settings.privacyMode" class="flex items-center justify-between text-[11px]" style="color: var(--text-muted)">
          <span>当前处于隐私脱敏模式，预览已对敏感字段脱敏（复制与导出文件仍为完整 JSON）</span>
          <button
            type="button"
            class="inline-flex items-center gap-1 px-2 py-0.5 rounded border cursor-pointer hover:bg-[var(--bg-hover)]"
            style="border-color: var(--border-subtle); color: var(--text-secondary); background-color: var(--bg-surface)"
            @click="revealInPrivacy = !revealInPrivacy"
          >
            <EyeOff v-if="revealInPrivacy" class="w-3 h-3" />
            <Eye v-else class="w-3 h-3" />
            <span>{{ revealInPrivacy ? '恢复脱敏预览' : '显示明文预览' }}</span>
          </button>
        </div>

        <pre
          class="p-3.5 rounded-xl border text-[11.5px] font-mono-num overflow-auto custom-scroll max-h-[300px] leading-relaxed"
          style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-secondary)"
        >{{ loading ? '生成中...' : previewText }}</pre>
      </div>
    </div>

    <template #footer>
      <button
        type="button"
        :disabled="savingFile"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="emit('close')"
      >
        关闭
      </button>
      <button
        type="button"
        :disabled="loading || !jsonText"
        class="inline-flex items-center gap-1.5 px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="handleCopy"
      >
        <Check v-if="copied" class="w-3.5 h-3.5" style="color: var(--success)" />
        <Copy v-else class="w-3.5 h-3.5" />
        {{ copied ? '已复制' : '复制 JSON' }}
      </button>
      <button
        type="button"
        :disabled="savingFile || loading"
        class="inline-flex items-center gap-1.5 px-4 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer disabled:opacity-60"
        style="background-color: var(--accent)"
        @click="handleSaveFile"
      >
        <Download class="w-3.5 h-3.5" />
        {{ savingFile ? '保存中...' : '保存为文件' }}
      </button>
    </template>
  </BaseModal>
</template>
