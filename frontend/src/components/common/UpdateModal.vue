<script setup lang="ts">
// 版本更新详情模态弹窗：展示当前版本与 GitHub 最新 Release 对比、更新说明、推荐安装包一键下载、复制下载链接与打开 Release 页面。
import { computed, onBeforeUnmount, ref } from 'vue'
import {
  ArrowUpCircle,
  Check,
  Copy,
  Download,
  ExternalLink,
  Package,
  RefreshCw,
} from 'lucide-vue-next'
import BaseModal from '@/components/common/BaseModal.vue'
import { useAppStore } from '@/stores/appStore'
import { api, extractErrorMessage } from '@/services/api'
import { copyToClipboard } from '@/utils/privacy'
import type { ReleaseAsset } from '@/types/models'

defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const appStore = useAppStore()

const copyState = ref<'idle' | 'copied' | 'failed'>('idle')
const liveStatusText = ref('')
let lastAnnounced = ''
let copyResetTimer: number | null = null

onBeforeUnmount(() => {
  if (copyResetTimer !== null) {
    window.clearTimeout(copyResetTimer)
    copyResetTimer = null
  }
})

const info = computed(() => appStore.updateInfo)

const primaryDownloadUrl = computed(() => {
  if (!info.value) return ''
  return info.value.recommendedAsset?.downloadUrl || info.value.releaseUrl || ''
})

const formattedDate = computed(() => {
  const raw = info.value?.publishedAt
  if (!raw) return ''
  const dt = new Date(raw)
  if (Number.isNaN(dt.getTime())) return raw
  return dt.toLocaleString('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  })
})

function formatSize(bytes: number): string {
  if (!bytes || bytes <= 0) return ''
  const mb = bytes / (1024 * 1024)
  if (mb >= 1) return `${mb.toFixed(1)} MB`
  return `${Math.max(1, Math.round(bytes / 1024))} KB`
}

async function openUrl(url: string) {
  if (!url) return
  try {
    await api.openExternalURL(url)
  } catch (err) {
    appStore.notify(`打开链接失败：${extractErrorMessage(err)}`, 'error')
  }
}

async function handleDownloadAsset(asset: ReleaseAsset) {
  await openUrl(asset.downloadUrl)
}

function announceCopyResult(ok: boolean) {
  if (copyResetTimer !== null) {
    window.clearTimeout(copyResetTimer)
  }
  copyState.value = ok ? 'copied' : 'failed'
  let message = ok ? '已复制下载链接' : '复制下载链接失败'
  // 遵循 VoiceOver 连续重复播报去重规范，相同文案追加不间断空格确保每次点击都能播报
  if (message === lastAnnounced) {
    message += '\u00A0'
  }
  lastAnnounced = message
  liveStatusText.value = ''
  window.requestAnimationFrame(() => {
    liveStatusText.value = message
  })
  copyResetTimer = window.setTimeout(() => {
    copyState.value = 'idle'
    liveStatusText.value = ''
    copyResetTimer = null
  }, 2000)
}

async function handleCopyPrimaryLink() {
  const target = primaryDownloadUrl.value
  if (!target) return
  const ok = await copyToClipboard(target)
  announceCopyResult(ok)
  if (ok) {
    appStore.notify('已复制下载链接到剪贴板', 'success', 2400)
  } else {
    appStore.notify('复制链接失败，请手动打开 Release 页面获取', 'error')
  }
}

async function handleRecheck() {
  await appStore.checkForUpdates(false)
}
</script>

<template>
  <BaseModal
    :open="open"
    :title="info?.hasUpdate ? `发现新版本 v${info.latestVersion}` : '版本与更新信息'"
    :subtitle="info?.repo ? `数据源：github.com/${info.repo}` : '检查 GitHub Releases 最新发行版'"
    width-class="w-[540px]"
    @close="emit('close')"
  >
    <div v-if="info" class="space-y-4">
      <!-- 版本对比横幅 -->
      <div
        class="p-3.5 rounded-xl border flex items-center justify-between gap-3"
        :style="
          info.hasUpdate
            ? { backgroundColor: 'var(--accent-soft)', borderColor: 'var(--accent-border)' }
            : { backgroundColor: 'var(--bg-app)', borderColor: 'var(--border-subtle)' }
        "
      >
        <div class="flex items-center gap-3 min-w-0">
          <div
            class="w-9 h-9 rounded-xl flex items-center justify-center shrink-0"
            :style="
              info.hasUpdate
                ? { backgroundColor: 'var(--accent)', color: '#ffffff' }
                : { backgroundColor: 'var(--bg-subtle)', color: 'var(--text-secondary)' }
            "
          >
            <ArrowUpCircle class="w-5 h-5" aria-hidden="true" />
          </div>
          <div class="min-w-0">
            <div class="flex items-center gap-2 flex-wrap">
              <span
                class="text-xs font-mono-num px-2 py-0.5 rounded-md border"
                style="background-color: var(--bg-surface); border-color: var(--border-subtle); color: var(--text-secondary)"
              >
                当前 v{{ info.currentVersion }}
              </span>
              <span class="text-xs" style="color: var(--text-muted)">→</span>
              <span
                class="text-xs font-semibold font-mono-num px-2 py-0.5 rounded-md"
                :style="
                  info.hasUpdate
                    ? { backgroundColor: 'var(--accent)', color: '#ffffff' }
                    : { backgroundColor: 'var(--success-soft)', color: 'var(--success-text)' }
                "
              >
                最新 v{{ info.latestVersion }}
              </span>
            </div>
            <div v-if="formattedDate" class="mt-1 text-[11px] tabular-nums" style="color: var(--text-muted)">
              发布时间：{{ formattedDate }}
            </div>
          </div>
        </div>

        <button
          type="button"
          :disabled="appStore.checkingUpdate"
          class="inline-flex items-center gap-1.5 px-2.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer shrink-0 transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50 min-h-[28px]"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="handleRecheck"
        >
          <RefreshCw class="w-3.5 h-3.5" :class="{ 'animate-spin': appStore.checkingUpdate }" aria-hidden="true" />
          <span>重新检查</span>
        </button>
      </div>

      <!-- 当前平台推荐安装包 -->
      <div
        v-if="info.recommendedAsset"
        class="p-3 rounded-xl border flex items-center justify-between gap-3"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div class="flex items-center gap-2.5 min-w-0">
          <Package class="w-4 h-4 shrink-0" style="color: var(--accent)" aria-hidden="true" />
          <div class="min-w-0">
            <div class="text-xs font-semibold truncate" style="color: var(--text-primary)">
              {{ info.recommendedAsset.name }}
            </div>
            <div class="text-[11px] tabular-nums" style="color: var(--text-muted)">
              已自动匹配当前系统架构{{ info.recommendedAsset.size ? ` · ${formatSize(info.recommendedAsset.size)}` : '' }}
            </div>
          </div>
        </div>

        <button
          type="button"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border cursor-pointer shrink-0 transition-colors hover:bg-[var(--bg-hover)] min-h-[28px]"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="handleDownloadAsset(info.recommendedAsset)"
        >
          <Download class="w-3.5 h-3.5" aria-hidden="true" />
          <span>下载此包</span>
        </button>
      </div>

      <!-- 更新日志 -->
      <div class="space-y-1.5">
        <div class="flex items-center justify-between">
          <h3 class="text-xs font-semibold" style="color: var(--text-primary)">
            {{ info.releaseName || `v${info.latestVersion} 更新说明` }}
          </h3>
        </div>
        <div
          class="p-3.5 rounded-xl border max-h-[220px] overflow-y-auto custom-scroll text-xs leading-relaxed whitespace-pre-wrap break-words"
          style="border-color: var(--border-subtle); background-color: var(--bg-app); color: var(--text-secondary)"
        >
          {{ info.releaseNotes || '该版本暂未填写详细更新说明，可前往 GitHub Release 页面查看提交记录与附件列表。' }}
        </div>
      </div>

      <!-- 全部平台安装包折叠列表（当存在多个资产时供跨端下载） -->
      <details
        v-if="info.assets && info.assets.length > 1"
        class="rounded-xl border px-3.5 py-2.5"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <summary class="text-xs font-medium cursor-pointer select-none" style="color: var(--text-secondary)">
          查看全部平台安装包（{{ info.assets.length }} 个文件）
        </summary>
        <ul class="mt-2.5 space-y-1.5 pt-2 border-t" style="border-color: var(--border-subtle)">
          <li
            v-for="asset in info.assets"
            :key="asset.downloadUrl"
            class="flex items-center justify-between gap-2 text-xs py-1"
          >
            <span class="truncate font-mono-num" style="color: var(--text-primary)">
              {{ asset.name }}
              <span v-if="asset.size" class="ml-1 text-[11px]" style="color: var(--text-muted)">
                ({{ formatSize(asset.size) }})
              </span>
            </span>
            <button
              type="button"
              class="inline-flex items-center gap-1 px-2 py-1 text-[11px] font-medium rounded-md border cursor-pointer shrink-0 hover:bg-[var(--bg-hover)] min-h-[24px]"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
              @click="handleDownloadAsset(asset)"
            >
              <Download class="w-3 h-3" aria-hidden="true" />
              <span>下载</span>
            </button>
          </li>
        </ul>
      </details>
    </div>

    <div v-else class="py-6 text-center text-xs" style="color: var(--text-muted)">
      暂无版本检查记录，请点击下方按钮检查 GitHub 最新版本。
    </div>

    <template #footer>
      <div class="w-full flex items-center justify-between gap-2 flex-wrap">
        <div class="inline-flex items-center gap-2">
          <!-- 复制下载链接按钮：保持不禁用以免丢失键盘焦点，并将 role="status" 置于按钮外部 -->
          <span class="inline-flex items-center">
            <button
              v-if="primaryDownloadUrl"
              type="button"
              class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)] min-h-[30px] min-w-[24px]"
              :style="
                copyState === 'copied'
                  ? { borderColor: 'var(--success)', color: 'var(--success-text)', backgroundColor: 'var(--success-soft)' }
                  : { borderColor: 'var(--border-strong)', color: 'var(--text-primary)', backgroundColor: 'var(--bg-surface)' }
              "
              @click="handleCopyPrimaryLink"
            >
              <Check v-if="copyState === 'copied'" class="w-3.5 h-3.5" aria-hidden="true" />
              <Copy v-else class="w-3.5 h-3.5" aria-hidden="true" />
              <span>{{ copyState === 'copied' ? '已复制链接' : '复制下载链接' }}</span>
            </button>
            <span class="visually-hidden" role="status">{{ liveStatusText }}</span>
          </span>

          <button
            v-if="info?.releaseUrl"
            type="button"
            class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)] min-h-[30px]"
            style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
            @click="openUrl(info.releaseUrl)"
          >
            <ExternalLink class="w-3.5 h-3.5" aria-hidden="true" />
            <span>GitHub Release</span>
          </button>
        </div>

        <div class="inline-flex items-center gap-2">
          <button
            type="button"
            class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)] min-h-[30px]"
            style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
            @click="emit('close')"
          >
            稍后
          </button>

          <button
            v-if="info?.recommendedAsset"
            type="button"
            autofocus
            class="inline-flex items-center gap-1.5 px-4 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer transition-opacity hover:opacity-90 min-h-[30px]"
            style="background-color: var(--accent)"
            @click="handleDownloadAsset(info.recommendedAsset)"
          >
            <Download class="w-3.5 h-3.5" aria-hidden="true" />
            <span>立即下载更新</span>
          </button>
          <button
            v-else-if="info?.releaseUrl"
            type="button"
            autofocus
            class="inline-flex items-center gap-1.5 px-4 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer transition-opacity hover:opacity-90 min-h-[30px]"
            style="background-color: var(--accent)"
            @click="openUrl(info.releaseUrl)"
          >
            <ExternalLink class="w-3.5 h-3.5" aria-hidden="true" />
            <span>前往下载新版</span>
          </button>
        </div>
      </div>
    </template>
  </BaseModal>
</template>
