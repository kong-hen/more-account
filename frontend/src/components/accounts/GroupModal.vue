<script setup lang="ts">
// 新建或编辑自定义账号分组弹窗。
import { ref, watch } from 'vue'
import BaseModal from '@/components/common/BaseModal.vue'
import { useAppStore } from '@/stores/appStore'
import { extractErrorMessage } from '@/services/api'
import type { AccountGroup } from '@/types/models'

const props = defineProps<{
  open: boolean
  editingGroup?: AccountGroup | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const appStore = useAppStore()

const colorPresets = [
  '#2563EB',
  '#10B981',
  '#F59E0B',
  '#EF4444',
  '#8B5CF6',
  '#EC4899',
  '#0EA5E9',
  '#64748B',
]

const name = ref('')
const color = ref(colorPresets[0])
const saving = ref(false)
const error = ref('')

watch(
  () => [props.open, props.editingGroup] as const,
  ([isOpen, grp]) => {
    if (!isOpen) return
    error.value = ''
    name.value = grp?.name ?? ''
    color.value = grp?.color || colorPresets[0]
  },
)

async function handleSubmit() {
  const trimmed = name.value.trim()
  if (!trimmed) {
    error.value = '请输入分组名称'
    return
  }
  saving.value = true
  error.value = ''
  try {
    await appStore.saveGroup({
      id: props.editingGroup?.id,
      name: trimmed,
      color: color.value,
      sortOrder: props.editingGroup?.sortOrder,
      createdAt: props.editingGroup?.createdAt,
    })
    emit('close')
  } catch (err) {
    error.value = extractErrorMessage(err)
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <BaseModal
    :open="open"
    :title="editingGroup ? '编辑分组' : '新建分组'"
    subtitle="用清晰的自建分组替代零散标签，方便分类管理账号池"
    width-class="w-[420px]"
    :prevent-close="saving"
    :close-on-outside="!saving"
    @close="emit('close')"
  >
    <form class="space-y-4" @submit.prevent="handleSubmit">
      <div>
        <label for="group-name-input" class="block text-xs font-medium mb-1.5" style="color: var(--text-secondary)">
          分组名称
        </label>
        <input
          id="group-name-input"
          v-model="name"
          type="text"
          autofocus
          maxlength="32"
          placeholder="例如：主力开发、团队共享、备用池"
          class="w-full px-3 py-2 text-sm rounded-lg border"
          style="border-color: var(--border-strong); background-color: var(--bg-surface); color: var(--text-primary)"
        />
      </div>

      <div>
        <span class="block text-xs font-medium mb-2" style="color: var(--text-secondary)">标识颜色</span>
        <div class="flex items-center gap-2.5">
          <button
            v-for="preset in colorPresets"
            :key="preset"
            type="button"
            class="w-6 h-6 rounded-full cursor-pointer transition-transform"
            :style="{
              backgroundColor: preset,
              boxShadow: color === preset ? `0 0 0 2px var(--bg-surface), 0 0 0 4px ${preset}` : 'none',
            }"
            :aria-label="`选择颜色 ${preset}`"
            @click="color = preset"
          />
        </div>
      </div>

      <p v-if="error" class="text-xs" style="color: var(--danger-text)">{{ error }}</p>
    </form>

    <template #footer>
      <button
        type="button"
        :disabled="saving"
        class="px-3.5 py-1.5 text-xs font-medium rounded-lg border cursor-pointer hover:bg-[var(--bg-hover)] disabled:opacity-50"
        style="border-color: var(--border-strong); color: var(--text-primary); background-color: var(--bg-surface)"
        @click="emit('close')"
      >
        取消
      </button>
      <button
        type="button"
        :disabled="saving"
        class="px-4 py-1.5 text-xs font-medium rounded-lg text-white cursor-pointer disabled:opacity-60"
        style="background-color: var(--accent)"
        @click="handleSubmit"
      >
        {{ saving ? '保存中...' : '保存' }}
      </button>
    </template>
  </BaseModal>
</template>
