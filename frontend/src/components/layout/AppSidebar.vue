<script setup lang="ts">
// 三栏工作台左栏：整合 macOS 隐式标题栏拖拽区、模块导航、自建分组树、当前激活账号胶囊与底部工具行。
import { computed } from 'vue'
import {
  Users,
  BarChart3,
  Clock,
  History,
  Plus,
  Pencil,
  Trash2,
  Eye,
  EyeOff,
  Sun,
  Moon,
  Settings,
  Layers,
  FolderOpen,
  Inbox,
  Radar,
  Download,
} from 'lucide-vue-next'
import { useAppStore } from '@/stores/appStore'
import { maskEmail } from '@/utils/privacy'
import { getTierLabel } from '@/utils/quota'
import type { AccountGroup, AppSection } from '@/types/models'

const appStore = useAppStore()

const emit = defineEmits<{
  (e: 'open-group-modal', group?: AccountGroup): void
  (e: 'request-delete-group', group: AccountGroup): void
  (e: 'open-settings'): void
}>()

const navItems: Array<{ id: AppSection; label: string; icon: typeof Users }> = [
  { id: 'accounts', label: '账号工作台', icon: Users },
  { id: 'usage', label: 'Token 用量', icon: BarChart3 },
  { id: 'wakeup', label: '定时唤醒', icon: Clock },
  { id: 'history', label: '切号历史', icon: History },
]

function selectSection(section: AppSection) {
  appStore.activeSection = section
}

function selectGroup(groupId: string) {
  appStore.activeSection = 'accounts'
  appStore.selectedGroupFilter = groupId
}

function openCurrentAccountDetail() {
  if (!appStore.currentAccount) return
  appStore.activeSection = 'accounts'
  appStore.requestSelectDetailAccount(appStore.currentAccount.id)
}

function runtimeTargetLabel(target: string): string {
  if (target === 'ide') return 'IDE'
  if (target === 'cli') return 'CLI'
  return 'Desktop'
}

const isExternalUnmanaged = computed(() =>
  Boolean(!appStore.currentAccount && appStore.activeDetection?.hasCredential && !appStore.activeDetection.managed),
)
</script>

<template>
  <aside
    class="w-[232px] shrink-0 h-full flex flex-col border-r select-none"
    style="background-color: var(--bg-sidebar); border-color: var(--border-subtle)"
  >
    <!-- 顶部 macOS 标题栏安全拖拽区 + 品牌标识 -->
    <div class="wails-drag shrink-0 pt-9 pb-3 px-4 flex items-center gap-2.5">
      <div
        class="w-7 h-7 rounded-lg flex items-center justify-center shrink-0 text-white"
        style="background-color: var(--accent)"
      >
        <Layers class="w-4 h-4" />
      </div>
      <div class="min-w-0">
        <div class="text-[13.5px] font-semibold leading-tight tracking-tight truncate" style="color: var(--text-primary)">
          More Account
        </div>
        <div class="text-[11px] leading-tight truncate" style="color: var(--text-muted)">
          Antigravity 账号管理
        </div>
      </div>
    </div>

    <!-- 主模块导航 -->
    <nav class="shrink-0 px-2.5 pt-1 space-y-0.5" aria-label="主导航">
      <button
        v-for="item in navItems"
        :key="item.id"
        type="button"
        class="w-full flex items-center gap-2.5 px-2.5 py-2 rounded-lg text-[13px] font-medium cursor-pointer transition-colors"
        :style="
          appStore.activeSection === item.id
            ? { backgroundColor: 'var(--bg-active)', color: 'var(--accent)' }
            : { color: 'var(--text-secondary)' }
        "
        :class="appStore.activeSection !== item.id ? 'hover:bg-[var(--bg-hover)]' : ''"
        @click="selectSection(item.id)"
      >
        <component :is="item.icon" class="w-4 h-4 shrink-0" />
        <span class="truncate">{{ item.label }}</span>
      </button>
    </nav>

    <!-- 自建分组树（彻底取代原有标签） -->
    <div class="mt-5 px-2.5 flex-1 min-h-0 flex flex-col">
      <div class="px-2.5 pb-1.5 flex items-center justify-between">
        <span class="text-[11px] font-semibold uppercase tracking-wider" style="color: var(--text-muted)">
          账号分组
        </span>
        <button
          type="button"
          title="新建分组"
          aria-label="新建分组"
          class="p-1 rounded-md cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
          style="color: var(--text-muted)"
          @click="emit('open-group-modal')"
        >
          <Plus class="w-3.5 h-3.5" />
        </button>
      </div>

      <div class="flex-1 min-h-0 overflow-y-auto custom-scroll space-y-0.5 pr-0.5">
        <!-- 全部账号 -->
        <button
          type="button"
          class="w-full flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-lg text-[12.5px] cursor-pointer transition-colors"
          :style="
            appStore.activeSection === 'accounts' && appStore.selectedGroupFilter === 'all'
              ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-primary)', boxShadow: 'var(--shadow-card)' }
              : { color: 'var(--text-secondary)' }
          "
          :class="!(appStore.activeSection === 'accounts' && appStore.selectedGroupFilter === 'all') ? 'hover:bg-[var(--bg-hover)]' : ''"
          @click="selectGroup('all')"
        >
          <span class="flex items-center gap-2 min-w-0">
            <FolderOpen class="w-3.5 h-3.5 shrink-0" style="color: var(--text-muted)" />
            <span class="truncate">全部账号</span>
          </span>
          <span class="text-[11px] tabular-nums" style="color: var(--text-muted)">
            {{ appStore.groupCounts.all ?? 0 }}
          </span>
        </button>

        <!-- 未分组 -->
        <button
          type="button"
          class="w-full flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-lg text-[12.5px] cursor-pointer transition-colors"
          :style="
            appStore.activeSection === 'accounts' && appStore.selectedGroupFilter === 'ungrouped'
              ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-primary)', boxShadow: 'var(--shadow-card)' }
              : { color: 'var(--text-secondary)' }
          "
          :class="!(appStore.activeSection === 'accounts' && appStore.selectedGroupFilter === 'ungrouped') ? 'hover:bg-[var(--bg-hover)]' : ''"
          @click="selectGroup('ungrouped')"
        >
          <span class="flex items-center gap-2 min-w-0">
            <Inbox class="w-3.5 h-3.5 shrink-0" style="color: var(--text-muted)" />
            <span class="truncate">未分组</span>
          </span>
          <span class="text-[11px] tabular-nums" style="color: var(--text-muted)">
            {{ appStore.groupCounts.ungrouped ?? 0 }}
          </span>
        </button>

        <!-- 用户自建分组列表 -->
        <div
          v-for="group in appStore.groups"
          :key="group.id"
          class="group/item relative flex items-center"
        >
          <button
            type="button"
            class="w-full flex items-center justify-between gap-2 px-2.5 py-1.5 rounded-lg text-[12.5px] cursor-pointer transition-colors"
            :style="
              appStore.activeSection === 'accounts' && appStore.selectedGroupFilter === group.id
                ? { backgroundColor: 'var(--bg-surface)', color: 'var(--text-primary)', boxShadow: 'var(--shadow-card)' }
                : { color: 'var(--text-secondary)' }
            "
            :class="!(appStore.activeSection === 'accounts' && appStore.selectedGroupFilter === group.id) ? 'hover:bg-[var(--bg-hover)]' : ''"
            @click="selectGroup(group.id)"
          >
            <span class="flex items-center gap-2 min-w-0">
              <span
                class="w-2 h-2 rounded-full shrink-0"
                :style="{ backgroundColor: group.color || '#2563EB' }"
              />
              <span class="truncate">{{ group.name }}</span>
            </span>
            <span class="text-[11px] tabular-nums group-hover/item:opacity-0 transition-opacity" style="color: var(--text-muted)">
              {{ appStore.groupCounts[group.id] ?? 0 }}
            </span>
          </button>

          <div
            class="absolute right-1.5 flex items-center gap-0.5 opacity-0 group-hover/item:opacity-100 transition-opacity"
          >
            <button
              type="button"
              title="编辑分组"
              aria-label="编辑分组"
              class="p-1 rounded cursor-pointer hover:bg-[var(--bg-hover)]"
              style="color: var(--text-muted)"
              @click.stop="emit('open-group-modal', group)"
            >
              <Pencil class="w-3 h-3" />
            </button>
            <button
              type="button"
              title="删除分组"
              aria-label="删除分组"
              class="p-1 rounded cursor-pointer hover:bg-[var(--danger-soft)]"
              style="color: var(--danger)"
              @click.stop="emit('request-delete-group', group)"
            >
              <Trash2 class="w-3 h-3" />
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- 底部当前激活账号胶囊（支持自动探测其他程序切号与手动立即探测） + 快捷工具行 -->
    <div class="shrink-0 p-3 border-t space-y-2.5" style="border-color: var(--border-subtle)">
      <div
        class="w-full p-2.5 rounded-xl border transition-colors"
        :style="
          appStore.currentAccount
            ? { backgroundColor: 'var(--bg-surface)', borderColor: 'var(--success)' }
            : isExternalUnmanaged
              ? { backgroundColor: 'var(--warning-soft)', borderColor: 'var(--warning)' }
              : { backgroundColor: 'var(--bg-surface)', borderColor: 'var(--border-subtle)' }
        "
      >
        <div class="flex items-center justify-between gap-1.5 mb-1">
          <span
            class="flex items-center gap-1.5 text-[11px] font-medium min-w-0"
            :style="{
              color: appStore.currentAccount
                ? 'var(--success-text)'
                : isExternalUnmanaged
                  ? 'var(--warning-text)'
                  : 'var(--text-muted)',
            }"
          >
            <span
              class="w-1.5 h-1.5 rounded-full shrink-0"
              :style="{
                backgroundColor: appStore.currentAccount
                  ? 'var(--success)'
                  : isExternalUnmanaged
                    ? 'var(--warning)'
                    : 'var(--text-muted)',
              }"
            />
            <span class="truncate">{{ isExternalUnmanaged ? '外部账号（未纳管）' : '当前使用' }}</span>
          </span>

          <div class="flex items-center gap-1 shrink-0">
            <span
              class="text-[10px] font-medium px-1.5 py-0.5 rounded"
              style="background-color: var(--bg-subtle); color: var(--text-secondary)"
            >
              {{ runtimeTargetLabel(appStore.settings.runtimeTarget) }}
            </span>
            <button
              type="button"
              :disabled="appStore.detectingActive"
              title="立即探测本机当前正在使用的账号"
              aria-label="探测当前账号"
              class="p-1 rounded cursor-pointer transition-colors hover:bg-[var(--bg-hover)] disabled:opacity-50"
              style="color: var(--text-muted)"
              @click="appStore.detectActiveAccount(false)"
            >
              <Radar class="w-3.5 h-3.5" :class="{ 'animate-spin': appStore.detectingActive }" />
            </button>
          </div>
        </div>

        <button
          v-if="appStore.currentAccount"
          type="button"
          class="w-full text-left flex items-center justify-between gap-1.5 cursor-pointer rounded-md py-0.5 transition-opacity hover:opacity-85"
          @click="openCurrentAccountDetail"
        >
          <span class="text-xs font-semibold truncate" style="color: var(--text-primary)">
            {{ maskEmail(appStore.currentAccount.email, appStore.settings.privacyMode) }}
          </span>
          <span
            v-if="getTierLabel(appStore.currentAccount) !== 'UNKNOWN'"
            class="text-[10px] font-semibold px-1.5 py-0.5 rounded shrink-0"
            style="background-color: var(--accent-soft); color: var(--accent)"
          >
            {{ getTierLabel(appStore.currentAccount) }}
          </span>
        </button>

        <div v-else-if="isExternalUnmanaged" class="space-y-1.5">
          <div class="text-xs font-medium truncate" style="color: var(--text-primary)">
            {{
              appStore.activeDetection?.detectedEmail
                ? maskEmail(appStore.activeDetection.detectedEmail, appStore.settings.privacyMode)
                : '检测到本机登录凭据'
            }}
          </div>
          <button
            type="button"
            :disabled="appStore.importingLocal"
            class="w-full inline-flex items-center justify-center gap-1.5 px-2 py-1 rounded-lg text-[11px] font-medium cursor-pointer transition-opacity disabled:opacity-60"
            style="background-color: var(--accent); color: #ffffff"
            @click="appStore.importLocalActiveAccount()"
          >
            <Download class="w-3 h-3" />
            <span>{{ appStore.importingLocal ? '导入中...' : '一键导入此账号' }}</span>
          </button>
        </div>

        <div v-else class="text-xs" style="color: var(--text-muted)">
          暂未检测到本机登录账号
        </div>
      </div>

      <!-- 工具操作行：隐私脱敏 / 手动浅暗主题切换 / 设置 -->
      <div class="flex items-center justify-between gap-1">
        <button
          type="button"
          class="flex-1 flex items-center justify-center gap-1.5 py-1.5 rounded-lg text-xs cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
          :style="
            appStore.settings.privacyMode
              ? { backgroundColor: 'var(--accent-soft)', color: 'var(--accent)' }
              : { color: 'var(--text-secondary)' }
          "
          :title="appStore.settings.privacyMode ? '关闭隐私脱敏' : '开启隐私脱敏'"
          @click="appStore.togglePrivacyMode"
        >
          <EyeOff v-if="appStore.settings.privacyMode" class="w-3.5 h-3.5" />
          <Eye v-else class="w-3.5 h-3.5" />
          <span>隐私</span>
        </button>

        <button
          type="button"
          class="flex-1 flex items-center justify-center gap-1.5 py-1.5 rounded-lg text-xs cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
          style="color: var(--text-secondary)"
          :title="appStore.settings.theme === 'dark' ? '切换为浅色主题' : '切换为暗色主题'"
          @click="appStore.toggleTheme"
        >
          <Sun v-if="appStore.settings.theme === 'dark'" class="w-3.5 h-3.5" />
          <Moon v-else class="w-3.5 h-3.5" />
          <span>{{ appStore.settings.theme === 'dark' ? '浅色' : '暗色' }}</span>
        </button>

        <button
          type="button"
          class="flex-1 flex items-center justify-center gap-1.5 py-1.5 rounded-lg text-xs cursor-pointer transition-colors hover:bg-[var(--bg-hover)]"
          style="color: var(--text-secondary)"
          title="打开设置"
          @click="emit('open-settings')"
        >
          <Settings class="w-3.5 h-3.5" />
          <span>设置</span>
        </button>
      </div>
    </div>
  </aside>
</template>
