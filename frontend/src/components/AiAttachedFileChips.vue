<script setup lang="ts">
/**
 * 「插入文件」的已选 chip 行：落地页 hero 与聊天区 composer 共用。
 * 只存 { path, name }，内容由后端在发送时按后缀解析（见 useAiAttachedFiles）。
 */
import { useI18n } from 'vue-i18n'
import { FileText } from '@lucide/vue'
import type { AttachedFile } from '../composables/useAiAttachedFiles'

const props = defineProps<{
  files: AttachedFile[]
}>()

const emit = defineEmits<{
  (e: 'remove', i: number): void
}>()

const { t } = useI18n()
</script>

<template>
  <div v-if="props.files.length" class="ai-attached-chips">
    <span
      v-for="(f, i) in props.files"
      :key="f.id"
      class="ai-attached-chip"
      :title="f.path || t('aiAttachPastedHint')"
    >
      <FileText :size="12" />
      <span class="ai-attached-chip-name">{{ f.name }}</span>
      <button class="ai-attached-chip-del" :title="t('delete')" @click="emit('remove', i)">×</button>
    </span>
  </div>
</template>

<style scoped>
.ai-attached-chips { display: flex; flex-wrap: wrap; gap: var(--space-3); }

.ai-attached-chip {
  display: inline-flex; align-items: center; gap: 4px; max-width: 260px;
  padding: var(--space-1) var(--space-2) var(--space-1) var(--space-3);
  font-size: var(--text-xs);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
}

.ai-attached-chip-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}

.ai-attached-chip-del {
  border: none; background: transparent; color: var(--color-text-muted);
  cursor: pointer; font-size: var(--text-md); line-height: 1; padding: 0 var(--space-1);
}

.ai-attached-chip-del:hover { color: var(--color-danger); }
</style>
