<script setup lang="ts">
/**
 * `@` 引用的两件 UI：已选 chip 与补全弹层。
 *
 * 两个输入框（落地页 hero、对话 composer）共用同一份 mentions 状态（都在 AIPage），
 * 所以这里只做渲染与事件回抛，不再各自维护一套样式。
 *
 * 弹层用 absolute 挂在输入容器上沿（向上展开）：输入区挨着窗口底部，往下弹会被切掉；
 * 也不跟光标跑（textarea 里算光标坐标要镜像一个 div，得不偿失）。
 * 依赖最近的 position:relative 祖先（composer 的 .ai-input-area、hero 的 .ai-hero-box）。
 */
import { useI18n } from 'vue-i18n'
import { FileText, Folder, MessageSquare } from '@lucide/vue'
import type { AIMentionItem } from '../types/ai'

const props = defineProps<{
  /** 已选引用 */
  mentions: AIMentionItem[]
  /** 补全弹层 */
  open: boolean
  items: AIMentionItem[]
  idx: number
}>()

const emit = defineEmits<{
  (e: 'pick', it: AIMentionItem): void
  (e: 'remove', i: number): void
}>()

const { t } = useI18n()

function iconOf(kind: string) {
  return kind === 'conv' ? MessageSquare : kind === 'dir' ? Folder : FileText
}

function keyOf(m: AIMentionItem, i: number): string {
  return m.kind + (m.path || m.convId || String(i))
}
</script>

<template>
  <div v-if="props.mentions.length" class="ai-mention-chips">
    <span
      v-for="(m, i) in props.mentions"
      :key="keyOf(m, i)"
      class="ai-mention-chip"
      :title="m.path || m.name"
    >
      <component :is="iconOf(m.kind)" :size="11" />
      <span class="ai-mention-chip-name">{{ m.kind === 'conv' ? m.name : (m.path || m.name) }}</span>
      <button class="ai-mention-chip-del" :title="t('delete')" @click="emit('remove', i)">×</button>
    </span>
  </div>
  <div v-if="props.open" class="ai-mention-pop">
    <div
      v-for="(it, i) in props.items"
      :key="keyOf(it, i)"
      class="ai-mention-item"
      :class="{ 'is-active': i === props.idx }"
      @mousedown.prevent="emit('pick', it)"
    >
      <component :is="iconOf(it.kind)" :size="12" />
      <span class="ai-mention-name">{{ it.name }}</span>
      <span v-if="it.path" class="ai-mention-path">{{ it.path }}</span>
      <span v-else-if="it.kind === 'conv'" class="ai-mention-path">{{ t('aiMentionConv') }}</span>
    </div>
  </div>
</template>

<style scoped>
.ai-mention-chips { display: flex; flex-wrap: wrap; gap: var(--space-3); }

.ai-mention-chip {
  display: inline-flex; align-items: center; gap: 3px; max-width: 260px;
  padding: var(--space-1) var(--space-2) var(--space-1) var(--space-3); font-size: var(--text-xs);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
}

.ai-mention-chip-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}

.ai-mention-chip-del {
  border: none; background: transparent; color: var(--color-text-muted);
  cursor: pointer; font-size: var(--text-md); line-height: 1; padding: 0 var(--space-1);
}

.ai-mention-chip-del:hover { color: var(--color-danger-ink); }

.ai-mention-pop {
  position: absolute; left: 16px; right: 16px; bottom: calc(100% - 6px);
  max-height: 240px; overflow: auto; z-index: 30; padding: var(--space-2);
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}

.ai-mention-item {
  display: flex; align-items: center; gap: var(--space-3); padding: var(--space-2) var(--space-3);
  font-size: var(--text-sm); color: var(--color-text-secondary); cursor: pointer;
  border-radius: var(--radius-sm);
}

.ai-mention-item.is-active { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-mention-name { flex-shrink: 0; }

.ai-mention-path {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono); font-size: var(--text-xs);
  color: var(--color-text-muted); text-align: right;
}
</style>
