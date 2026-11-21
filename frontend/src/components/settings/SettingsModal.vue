<script setup lang="ts">
// 全局应用设置弹窗：运行目标、切号重启策略、低配额筛选阈值、手动浅暗色切换与本机路径配置。
import { computed, ref, watch } from 'vue'
import { FolderOpen, CheckCircle2, AlertCircle, Sun, Moon } from 'lucide-vue-next'
import BaseModal from '@/components/common/BaseModal.vue'
import AppSelect from '@/components/common/AppSelect.vue'
import { useAppStore } from '@/stores/appStore'
import { useTokenUsageStore } from '@/stores/tokenUsageStore'
import { api, extractErrorMessage } from '@/services/api'
import type { AppSettings, RuntimeTarget } from '@/types/models'

const props = defineProps<{
  open: boolean
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const appStore = useAppStore()
const usageStore = useTokenUsageStore()
const form = ref<AppSettings>({ ...appStore.settings })
const saving = ref(false)

watch(
  () => props.open,
  (isOpen) => {
    if (isOpen) {
      form.value = { ...appStore.settings }
    }
  },
)

const baseAutoRefreshOptions = [
  { value: '-1', label: '关闭自动刷新' },
  { value: '1', label: '每 1 分钟' },
  { value: '3', label: '每 3 分钟' },
  { value: '5', label: '每 5 分钟（推荐）' },
  { value: '10', label: '每 10 分钟' },
  { value: '15', label: '每 15 分钟' },
  { value: '30', label: '每 30 分钟' },
]

const baseInactiveAutoRefreshOptions = [
  { value: '-1', label: '关闭自动刷新' },
  { value: '15', label: '每 15 分钟' },
  { value: '30', label: '每 30 分钟' },
  { value: '60', label: '每 1 小时（推荐）' },
  { value: '120', label: '每 2 小时' },
  { value: '360', label: '每 6 小时' },
  { value: '720', label: '每 12 小时' },
  { value: '1440', label: '每 24 小时' },
]

const autoRefreshOptions = computed(() => {
  const cur = String(form.value.autoRefreshMinutes ?? 5)
  if (baseAutoRefreshOptions.some((o) => o.value === cur)) return baseAutoRefreshOptions
  return [...baseAutoRefreshOptions, { value: cur, label: `每 ${cur} 分钟（自定义）` }]
})

const inactiveAutoRefreshOptions = computed(() => {
  const cur = String(form.value.inactiveAutoRefreshMinutes ?? 60)
  if (baseInactiveAutoRefreshOptions.some((o) => o.value === cur)) return baseInactiveAutoRefreshOptions
  return [...baseInactiveAutoRefreshOptions, { value: cur, label: `每 ${cur} 分钟（自定义）` }]
})

function setAutoRefreshMinutes(val: string) {
  const num = Number(val)
  form.value.autoRefreshMinutes = Number.isFinite(num) ? num : 5
}

function setInactiveAutoRefreshMinutes(val: string) {
  const num = Number(val)
  form.value.inactiveAutoRefreshMinutes = Number.isFinite(num) ? num : 60
}

const runtimeOptions: Array<{ value: RuntimeTarget; label: string; desc: string }> = [
  {
    value: 'desktop',
    label: 'Antigravity 桌面版 (2.0)',
    desc: '写入系统凭据库（Keychain）与 ~/.gemini 回退文件，支持自动重启。',
  },
  {
    value: 'ide',
    label: 'Antigravity IDE',
    desc: '同时注入 state.vscdb Protobuf 凭据与共享系统凭据。',
  },
  {
    value: 'cli',
    label: 'Antigravity CLI (agy)',
    desc: '同步写入系统凭据与 ~/.gemini/antigravity-cli 凭据，不重启桌面端。',
  },
]

async function pickUsageDir() {
  try {
    const dir = await api.pickDirectory('选择 Antigravity 数据目录')
    if (dir) {
      form.value.tokenUsageDataDir = dir
    }
  } catch (err) {
    appStore.notify(`选择目录失败：${extractErrorMessage(err)}`, 'error')
  }
}

async function openDataDir() {
  try {
    await api.openDataFolder()
  } catch (err) {
    appStore.notify(`打开数据目录失败：${extractErrorMessage(err)}`, 'error')
  }
}

async function handleSave() {
  saving.value = true
  try {
    const prevUsageDir = (appStore.settings.tokenUsageDataDir ?? '').trim()
    const parsedThreshold = Number(form.value.lowQuotaThreshold)
    const cleanThreshold =
      Number.isFinite(parsedThreshold) && parsedThreshold >= 1 && parsedThreshold <= 100
        ? Math.round(parsedThreshold)
        : 20

    const payload: AppSettings = {
      ...form.value,
      lowQuotaThreshold: cleanThreshold,
      desktopAppPath: (form.value.desktopAppPath ?? '').trim(),
      ideAppPath: (form.value.ideAppPath ?? '').trim(),
      tokenUsageDataDir: (form.value.tokenUsageDataDir ?? '').trim(),
    }

    await appStore.updateSettings(payload)
    if (payload.tokenUsageDataDir !== prevUsageDir) {
      usageStore.invalidate(appStore.activeSection === 'usage')
    }
    appStore.notify('设置已保存', 'success')
    emit('close')
  } catch {
    // updateSettings 内部已回滚并提示错误，此处保持弹窗打开以便用户修正
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <BaseModal
    :open="open"
    title="应用设置"
    subtitle="配置反重力切号目标、外观主题与低配额阈值"
    width-class="w-[560px]"
    :prevent-close="saving"
    :close-on-outside="!saving"
    @close="emit('close')"
  >
    <div class="space-y-5">
      <!-- 外观主题（明确手动两态，不跟随系统暗色） -->
      <section class="space-y-2">
        <label class="block text-xs font-semibold" style="color: var(--text-primary)">
          界面主题（默认浅色，支持手动切换）
        </label>
        <div class="grid grid-cols-2 gap-2.5">
          <button
            type="button"
            class="flex items-center gap-2.5 p-3 rounded-xl border text-left cursor-pointer transition-colors"
            :style="
              form.theme === 'light'
                ? { borderColor: 'var(--accent)', backgroundColor: 'var(--accent-soft)' }
                : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-app)' }
            "
            @click="form.theme = 'light'"
          >
            <Sun class="w-4 h-4 shrink-0" :style="{ color: form.theme === 'light' ? 'var(--accent)' : 'var(--text-muted)' }" />
            <div>
              <div class="text-xs font-semibold" style="color: var(--text-primary)">浅色模式</div>
              <div class="text-[11px]" style="color: var(--text-muted)">清晰明亮（默认）</div>
            </div>
          </button>

          <button
            type="button"
            class="flex items-center gap-2.5 p-3 rounded-xl border text-left cursor-pointer transition-colors"
            :style="
              form.theme === 'dark'
                ? { borderColor: 'var(--accent)', backgroundColor: 'var(--accent-soft)' }
                : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-app)' }
            "
            @click="form.theme = 'dark'"
          >
            <Moon class="w-4 h-4 shrink-0" :style="{ color: form.theme === 'dark' ? 'var(--accent)' : 'var(--text-muted)' }" />
            <div>
              <div class="text-xs font-semibold" style="color: var(--text-primary)">暗色模式</div>
              <div class="text-[11px]" style="color: var(--text-muted)">低亮度护眼</div>
            </div>
          </button>
        </div>
      </section>

      <!-- 默认切号目标 -->
      <section class="space-y-2">
        <label class="block text-xs font-semibold" style="color: var(--text-primary)">
          默认切号目标客户端
        </label>
        <div class="space-y-2">
          <button
            v-for="opt in runtimeOptions"
            :key="opt.value"
            type="button"
            class="w-full p-3 rounded-xl border text-left cursor-pointer transition-colors"
            :style="
              form.runtimeTarget === opt.value
                ? { borderColor: 'var(--accent)', backgroundColor: 'var(--accent-soft)' }
                : { borderColor: 'var(--border-subtle)', backgroundColor: 'var(--bg-app)' }
            "
            @click="form.runtimeTarget = opt.value"
          >
            <div class="flex items-center justify-between gap-2">
              <span class="text-xs font-semibold" style="color: var(--text-primary)">{{ opt.label }}</span>
              <span
                v-if="opt.value === 'desktop'"
                class="inline-flex items-center gap-1 text-[11px]"
                :style="{ color: appStore.clientInfo.desktopOk ? 'var(--success-text)' : 'var(--text-muted)' }"
              >
                <CheckCircle2 v-if="appStore.clientInfo.desktopOk" class="w-3.5 h-3.5" />
                <AlertCircle v-else class="w-3.5 h-3.5" />
                {{ appStore.clientInfo.desktopOk ? '已检测到安装' : '未自动检测到' }}
              </span>
              <span
                v-else-if="opt.value === 'ide'"
                class="inline-flex items-center gap-1 text-[11px]"
                :style="{ color: appStore.clientInfo.ideOk ? 'var(--success-text)' : 'var(--text-muted)' }"
              >
                <CheckCircle2 v-if="appStore.clientInfo.ideOk" class="w-3.5 h-3.5" />
                <AlertCircle v-else class="w-3.5 h-3.5" />
                {{ appStore.clientInfo.ideOk ? '已检测到安装' : '未自动检测到' }}
              </span>
            </div>
            <p class="mt-1 text-[11.5px]" style="color: var(--text-muted)">{{ opt.desc }}</p>
          </button>
        </div>
      </section>

      <!-- 后台自动刷新配额：区分当前使用账号与未使用账号 -->
      <section
        class="p-3.5 rounded-xl border space-y-3"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div>
          <div class="text-xs font-semibold" style="color: var(--text-primary)">后台自动刷新账号额度</div>
          <div class="mt-0.5 text-[11px]" style="color: var(--text-muted)">
            定时静默同步配额，并在 5h / 周额度重置到期后自动拉取最新余量；未使用账号建议设置较长周期以减少请求
          </div>
        </div>

        <div class="space-y-2.5 pt-1 border-t" style="border-color: var(--border-subtle)">
          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0">
              <div class="text-xs font-medium" style="color: var(--text-primary)">当前使用账号</div>
              <div class="text-[11px]" style="color: var(--text-muted)">实时追踪正在消耗的账号余量</div>
            </div>
            <AppSelect
              aria-label="当前使用账号自动刷新间隔"
              :model-value="String(form.autoRefreshMinutes ?? 5)"
              :options="autoRefreshOptions"
              class="w-[152px] shrink-0"
              @update:model-value="setAutoRefreshMinutes"
            />
          </div>

          <div class="flex items-center justify-between gap-3">
            <div class="min-w-0">
              <div class="text-xs font-medium" style="color: var(--text-primary)">未使用账号</div>
              <div class="text-[11px]" style="color: var(--text-muted)">低频保鲜备用账号，默认每 1 小时刷新</div>
            </div>
            <AppSelect
              aria-label="未使用账号自动刷新间隔"
              :model-value="String(form.inactiveAutoRefreshMinutes ?? 60)"
              :options="inactiveAutoRefreshOptions"
              class="w-[152px] shrink-0"
              @update:model-value="setInactiveAutoRefreshMinutes"
            />
          </div>
        </div>
      </section>

      <!-- 切号重启与低配额阈值 -->
      <section class="grid grid-cols-2 gap-4 pt-1">
        <label class="flex items-start gap-2.5 p-3 rounded-xl border cursor-pointer" style="border-color: var(--border-subtle); background-color: var(--bg-app)">
          <input
            v-model="form.launchOnSwitch"
            type="checkbox"
            class="mt-0.5 rounded accent-[var(--accent)]"
          />
          <div>
            <div class="text-xs font-semibold" style="color: var(--text-primary)">切号后自动重启客户端</div>
            <div class="mt-0.5 text-[11px]" style="color: var(--text-muted)">先优雅关闭并同步凭据，再重新拉起</div>
          </div>
        </label>

        <div class="p-3 rounded-xl border" style="border-color: var(--border-subtle); background-color: var(--bg-app)">
          <label for="low-quota-input" class="block text-xs font-semibold" style="color: var(--text-primary)">
            低配额筛选阈值 (%)
          </label>
          <div class="mt-1.5 flex items-center gap-2">
            <input
              id="low-quota-input"
              v-model.number="form.lowQuotaThreshold"
              type="number"
              min="1"
              max="99"
              class="w-20 px-2.5 py-1 text-xs rounded-lg border tabular-nums"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
            <span class="text-[11px]" style="color: var(--text-muted)">任意核心模型 ≤ 此值</span>
          </div>
        </div>
      </section>

      <!-- 可选路径配置 -->
      <section class="space-y-3 pt-1">
        <div>
          <label for="desktop-path" class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">
            自定义桌面版应用路径（留空自动探测）
          </label>
          <input
            id="desktop-path"
            v-model="form.desktopAppPath"
            type="text"
            :placeholder="appStore.clientInfo.desktopPath || '/Applications/Antigravity.app'"
            class="w-full px-3 py-1.5 text-xs rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>

        <div>
          <label for="ide-path" class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">
            自定义 Antigravity IDE 应用路径（留空自动探测）
          </label>
          <input
            id="ide-path"
            v-model="form.ideAppPath"
            type="text"
            :placeholder="appStore.clientInfo.idePath || '/Applications/Antigravity IDE.app'"
            class="w-full px-3 py-1.5 text-xs rounded-lg border"
            style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          />
        </div>

        <div>
          <label for="usage-dir" class="block text-xs font-medium mb-1" style="color: var(--text-secondary)">
            Token 用量只读扫描目录（留空默认 ~/.gemini/antigravity）
          </label>
          <div class="flex gap-2">
            <input
              id="usage-dir"
              v-model="form.tokenUsageDataDir"
              type="text"
              placeholder="~/.gemini/antigravity"
              class="flex-1 px-3 py-1.5 text-xs rounded-lg border"
              style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
            />
            <button
              type="button"
              class="px-3 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
              style="border-color: var(--border-strong); color: var(--text-primary)"
              @click="pickUsageDir"
            >
              浏览
            </button>
          </div>
        </div>
      </section>

      <!-- 数据存储位置 -->
      <section
        class="flex items-center justify-between gap-3 p-3 rounded-xl border"
        style="border-color: var(--border-subtle); background-color: var(--bg-app)"
      >
        <div class="min-w-0">
          <div class="text-xs font-medium" style="color: var(--text-primary)">本地独立数据目录</div>
          <div class="text-[11px] font-mono-num truncate mt-0.5" style="color: var(--text-muted)">
            {{ appStore.dataDir || '~/.more-account' }}
          </div>
        </div>
        <button
          type="button"
          class="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg border cursor-pointer shrink-0 transition-colors hover:bg-[var(--bg-hover)]"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
          @click="openDataDir"
        >
          <FolderOpen class="w-3.5 h-3.5" />
          打开目录
        </button>
      </section>
    </div>

    <template #footer>
      <button
        type="button"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="emit('close')"
      >
        取消
      </button>
      <button
        type="button"
        :disabled="saving"
        class="px-4 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer transition-opacity disabled:opacity-60"
        style="background-color: var(--accent)"
        @click="handleSave"
      >
        {{ saving ? '保存中...' : '保存设置' }}
      </button>
    </template>
  </BaseModal>
</template>
