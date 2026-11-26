<script setup lang="ts">
/**
 * 模型选择胶囊：落地页与聊天区输入条共用。
 * 两处只有视觉档位不同（落地页透明 ghost / 聊天区实心 solid），
 * 选择行为与 emits 完全一致。
 */
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'

defineProps<{
  profiles: AIProfile[]
  active: string
  title?: string
  variant?: 'ghost' | 'solid'
}>()

const emit = defineEmits<{
  (e: 'update:active', v: string): void
  (e: 'model-change'): void
}>()

/** 没填显示名时，模型 id 常是 provider/model 的长串：只留最后一段，
 *  完整值仍在 option 的 value 与 title 里。 */
function shortModel(m: string): string {
  const seg = (m || '').split('/').filter(Boolean)
  return seg.length ? seg[seg.length - 1] : m
}

function onChange(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  emit('update:active', v)
  emit('model-change')
}
</script>

<template>
  <select
    class="ai-bit-model"
    :class="variant === 'ghost' ? 'is-ghost' : 'is-solid'"
    :value="active"
    :title="title"
    @change="onChange"
  >
    <option v-for="p in profiles" :key="p.id" :value="p.id">{{ p.name || shortModel(p.model) }}</option>
  </select>
</template>

<style scoped>
.ai-bit-model {
  box-sizing: border-box;
  height: 22px;
  max-width: 150px;
  min-width: 0;
  flex: 0 1 auto;
  padding: 0 var(--space-4);
  border-radius: var(--radius-full);
  font-size: var(--text-xs);
  font-family: inherit;
  cursor: pointer;
  text-overflow: ellipsis;
  transition: color var(--transition-fast), border-color var(--transition-fast),
    background-color var(--transition-fast);
}
.ai-bit-model.is-ghost {
  border: 1px solid transparent;
  background: transparent;
  /* 底栏这一排原来用 text-muted，在深色底上几乎看不清是「可点」的 */
  color: var(--color-text-secondary);
}
.ai-bit-model.is-ghost:hover {
  color: var(--color-text-primary);
  border-color: var(--color-border);
  background: var(--color-bg-secondary);
}
.ai-bit-model.is-ghost:focus-visible {
  outline: none;
  color: var(--color-text-primary);
  border-color: var(--color-border-light);
  background: var(--color-bg-active);
}
.ai-bit-model.is-solid {
  border: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
}
.ai-bit-model.is-solid:hover {
  color: var(--color-text-primary);
  border-color: var(--color-border-light);
  background: var(--color-bg-active);
}
.ai-bit-model.is-solid:focus-visible {
  outline: none;
  border-color: var(--color-border-focus-soft);
}
</style>
