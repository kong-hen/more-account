<script setup lang="ts">
// JSON 导出弹窗：支持预览、一键复制到剪贴板、保存为本地 .json 文件，且可选是否附带密码与 2FA 密钥。
import { ref, watch } from 'vue'
import { Copy, Check, Download, ShieldAlert } from 'lucide-vue-next'
import BaseModal from '@/components/common/BaseModal.vue'
import { api, extractErrorMessage } from '@/services/api'
import { copyToClipboard } from '@/utils/privacy'
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
const loading = ref(false)
const copied = ref(false)
const savingFile = ref(false)

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
      // 每次打开导出弹窗均重置为默认不包含密码与 2FA 密钥，防止上次勾选导致意外明文暴露
      includeSecrets.value = false
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

      <div class="relative">
        <pre
          class="p-3.5 rounded-xl border text-[11.5px] font-mono-num overflow-auto custom-scroll max-h-[300px] leading-relaxed"
          style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-secondary)"
        >{{ loading ? '生成中...' : jsonText }}</pre>
      </div>
    </div>

    <template #footer>
      <button
        type="button"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)]"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="emit('close')"
      >
        关闭
      </button>
      <button
        type="button"
        class="inline-flex items-center gap-1.5 px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)]"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="handleCopy"
      >
        <Check v-if="copied" class="w-3.5 h-3.5" style="color: var(--success)" />
        <Copy v-else class="w-3.5 h-3.5" />
        {{ copied ? '已复制' : '复制 JSON' }}
      </button>
      <button
        type="button"
        :disabled="savingFile"
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
