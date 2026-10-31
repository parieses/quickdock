<script setup lang="ts">
/**
 * 大纲 / 摘要浮层：绝对定位挂在父级的 .ai-main 上（那个元素已是 position: relative）。
 *
 * 不做成模态框——它只是「翻一下聊到哪了」，看完点目录里的一条就该跳过去，
 * 模态框的遮罩与关闭动作用户根本没耐心走一遍。
 */
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import type { OutlineItem } from '../types/ai'

const props = defineProps<{
  /** 当前会话已有的摘要；空串＝还没生成过 */
  summary: string
  /** 正在生成摘要（禁用按钮防重复点） */
  busy: boolean
  /** 正在流式回答；此时生成摘要会把同一份上下文再送一遍，没意义 */
  streaming: boolean
  outline: OutlineItem[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'run'): void
  (e: 'jump', id: string): void
}>()

const { t } = useI18n()
</script>

<template>
  <div class="ai-digest">
    <div class="ai-digest-head">
      <span>{{ t('aiDigestTitle') }}</span>
      <button class="ai-digest-x" :title="t('close')" @click="emit('close')"><X :size="12" /></button>
    </div>
    <div class="ai-digest-sum">
      <template v-if="props.summary">
        <pre class="ai-digest-text">{{ props.summary }}</pre>
      </template>
      <div v-else class="ai-digest-empty">{{ t('aiDigestEmpty') }}</div>
      <button class="ai-digest-gen" :disabled="props.busy || props.streaming" @click="emit('run')">
        {{ props.busy ? t('aiDigestBusy') : (props.summary ? t('aiDigestRegen') : t('aiDigestGen')) }}
      </button>
      <p class="ai-digest-hint">{{ t('aiDigestHint') }}</p>
    </div>
    <div class="ai-digest-sub">{{ t('aiOutlineSub') }} ({{ props.outline.length }})</div>
    <div class="ai-digest-list">
      <button
        v-for="(it, i) in props.outline"
        :key="it.id"
        class="ai-digest-item"
        :title="it.text"
        @click="emit('jump', it.id)"
      >
        <span class="ai-digest-n">{{ i + 1 }}</span>
        <span class="ai-digest-q">{{ it.text }}</span>
      </button>
      <div v-if="!props.outline.length" class="ai-digest-empty">{{ t('aiOutlineEmpty') }}</div>
    </div>
  </div>
</template>

<style scoped>
.ai-digest {
  position: absolute;
  top: 52px;
  right: 16px;
  z-index: 20;
  width: 320px;
  max-height: 70vh;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg, 0 12px 32px rgba(0,0,0,.28));
  overflow: hidden;
}

.ai-digest-head {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 10px; border-bottom: 1px solid var(--color-border);
  font-size: 12px; font-weight: 600; color: var(--color-text-secondary);
}

.ai-digest-x { background: none; border: none; color: var(--color-text-muted); cursor: pointer; display: flex; padding: 2px; border-radius: var(--radius-sm); }

.ai-digest-x:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.ai-digest-sum { padding: 8px 10px 4px; border-bottom: 1px solid var(--color-border); }

.ai-digest-text {
  margin: 0 0 6px; font-family: inherit; font-size: 12px; line-height: 1.55;
  color: var(--color-text-secondary); white-space: pre-wrap; word-break: break-word;
  max-height: 180px; overflow: auto;
}

.ai-digest-empty { font-size: 11.5px; color: var(--color-text-disabled); padding: 2px 0 6px; }

.ai-digest-gen {
  font-size: 11.5px; font-family: inherit; padding: 4px 10px; cursor: pointer;
  color: var(--color-accent); background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border); border-radius: var(--radius-sm);
}

.ai-digest-gen:disabled { opacity: .55; cursor: default; }

.ai-digest-hint { margin: 6px 0 4px; font-size: 11px; color: var(--color-text-disabled); line-height: 1.4; }

.ai-digest-sub { padding: 8px 10px 4px; font-size: 11px; color: var(--color-text-disabled); }

.ai-digest-list { overflow-y: auto; padding: 0 6px 8px; }

.ai-digest-item {
  display: flex; gap: 6px; width: 100%; text-align: left;
  padding: 5px 6px; border: none; background: none; cursor: pointer;
  border-radius: var(--radius-sm); color: var(--color-text-secondary);
  font-family: inherit; font-size: 12px;
}

.ai-digest-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-digest-n { flex-shrink: 0; width: 16px; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }

.ai-digest-q { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
</style>
