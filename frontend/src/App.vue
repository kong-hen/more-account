<script setup lang="ts">
// 应用根组件：装配三栏工作台（左栏导航与自建分组树 + 中栏主视图 + 右侧悬浮详情抽屉）及全部模态弹窗。
import { onBeforeUnmount, onMounted, ref } from 'vue'
import AppSidebar from '@/components/layout/AppSidebar.vue'
import AccountsView from '@/views/AccountsView.vue'
import TokenUsageView from '@/views/TokenUsageView.vue'
import WakeupView from '@/views/WakeupView.vue'
import SwitchHistoryView from '@/views/SwitchHistoryView.vue'
import AccountDetailDrawer from '@/components/accounts/AccountDetailDrawer.vue'
import AddAccountModal from '@/components/accounts/AddAccountModal.vue'
import GroupModal from '@/components/accounts/GroupModal.vue'
import ExportModal from '@/components/accounts/ExportModal.vue'
import SettingsModal from '@/components/settings/SettingsModal.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import ToastContainer from '@/components/common/ToastContainer.vue'
import { useAppStore } from '@/stores/appStore'
import { useTokenUsageStore } from '@/stores/tokenUsageStore'
import { useWakeupStore } from '@/stores/wakeupStore'
import { onWailsEvent } from '@/services/api'
import type { Account, AccountGroup } from '@/types/models'

const appStore = useAppStore()
const usageStore = useTokenUsageStore()
const wakeupStore = useWakeupStore()

const addModalOpen = ref(false)
const settingsModalOpen = ref(false)
const groupModalOpen = ref(false)
const editingGroup = ref<AccountGroup | null>(null)
const exportModalOpen = ref(false)
const exportIds = ref<string[]>([])

const deleteAccountConfirm = ref<{ ids: string[]; label: string } | null>(null)
const deleteGroupConfirm = ref<AccountGroup | null>(null)
const deleting = ref(false)

let offWakeup: (() => void) | null = null

function handleGlobalKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  // 如果当前已有原生 <dialog> 打开，交由浏览器原生 Esc 处理
  if (document.querySelector('dialog[open]')) return
  if (appStore.activeDetailAccountId) {
    appStore.activeDetailAccountId = null
  }
  if (usageStore.detailOpen) {
    usageStore.closeConversationDetail()
  }
}

onMounted(async () => {
  await appStore.bootstrap()
  offWakeup = onWailsEvent('wakeup:updated', () => {
    void wakeupStore.loadAll()
  })
  window.addEventListener('keydown', handleGlobalKeydown)
})

onBeforeUnmount(() => {
  offWakeup?.()
  appStore.disposeEvents()
  window.removeEventListener('keydown', handleGlobalKeydown)
})

function openGroupModal(group?: AccountGroup) {
  editingGroup.value = group ?? null
  groupModalOpen.value = true
}

function openExportModal(ids: string[]) {
  exportIds.value = ids
  exportModalOpen.value = true
}

function requestDeleteAccounts(ids: string[], label: string) {
  deleteAccountConfirm.value = { ids, label }
}

function requestDeleteSingleAccount(account: Account) {
  deleteAccountConfirm.value = { ids: [account.id], label: account.email }
}

async function confirmDeleteAccounts() {
  if (!deleteAccountConfirm.value) return
  deleting.value = true
  try {
    await appStore.deleteAccountIds(deleteAccountConfirm.value.ids)
    deleteAccountConfirm.value = null
  } finally {
    deleting.value = false
  }
}

async function confirmDeleteGroup() {
  if (!deleteGroupConfirm.value) return
  deleting.value = true
  try {
    await appStore.deleteGroup(deleteGroupConfirm.value.id)
    deleteGroupConfirm.value = null
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <div class="h-screen w-screen flex overflow-hidden" style="background-color: var(--bg-app); color: var(--text-primary)">
    <!-- 左栏：主导航 + 自建分组树 + 当前激活账号胶囊 + 底部工具行 -->
    <AppSidebar
      @open-group-modal="openGroupModal"
      @request-delete-group="(g) => (deleteGroupConfirm = g)"
      @open-settings="settingsModalOpen = true"
    />

    <!-- 中栏 + 右栏容器 -->
    <main class="flex-1 min-w-0 h-full flex overflow-hidden">
      <template v-if="appStore.activeSection === 'accounts'">
        <AccountsView
          @open-add="addModalOpen = true"
          @open-export="openExportModal"
          @request-delete="requestDeleteAccounts"
        />

        <!-- 右侧悬浮账号详情抽屉：脱离文档流覆盖在主视图上方，不挤压原有布局 -->
        <AccountDetailDrawer
          :account="appStore.activeDetailAccount"
          @close="appStore.activeDetailAccountId = null"
          @export-account="(id) => openExportModal([id])"
          @delete-account="requestDeleteSingleAccount"
        />
      </template>

      <TokenUsageView v-else-if="appStore.activeSection === 'usage'" />

      <WakeupView v-else-if="appStore.activeSection === 'wakeup'" />

      <SwitchHistoryView v-else-if="appStore.activeSection === 'history'" />
    </main>

    <!-- 模态弹窗集合 -->
    <AddAccountModal :open="addModalOpen" @close="addModalOpen = false" />

    <GroupModal
      :open="groupModalOpen"
      :editing-group="editingGroup"
      @close="groupModalOpen = false"
    />

    <ExportModal
      :open="exportModalOpen"
      :account-ids="exportIds"
      @close="exportModalOpen = false"
    />

    <SettingsModal :open="settingsModalOpen" @close="settingsModalOpen = false" />

    <ConfirmDialog
      :open="Boolean(deleteAccountConfirm)"
      title="删除反重力账号"
      :message="`确定删除 ${deleteAccountConfirm?.label ?? ''} 吗？删除后不会影响远程 Google 账号，但本地保存的备注、密码与 2FA 密钥将被清除。`"
      confirm-text="确认删除"
      :loading="deleting"
      @cancel="deleteAccountConfirm = null"
      @confirm="confirmDeleteAccounts"
    />

    <ConfirmDialog
      :open="Boolean(deleteGroupConfirm)"
      title="删除自建分组"
      :message="`确定删除分组「${deleteGroupConfirm?.name ?? ''}」吗？组内账号不会被删除，将自动归入「未分组」。`"
      confirm-text="删除分组"
      :loading="deleting"
      @cancel="deleteGroupConfirm = null"
      @confirm="confirmDeleteGroup"
    />

    <ToastContainer />
  </div>
</template>
