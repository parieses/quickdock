<script setup lang="ts">
/**
 * 输入框里打 `/` 弹出的技能候选列表。
 *
 * 与 AiPlusPanel 的「技能」一组是同一个东西的两种入口：那边从 + 号浏览全部，
 * 这边按打字过滤。选中都是**在输入框里行内插一枚胶囊**（名字不带斜杠），没有任何
 * 勾选状态——DOM 里那几枚胶囊就是唯一的选择记录（见 AiRichInput）。
 *
 * 定位与 @ 补全一致：贴着输入区上沿向上弹（输入区挨着窗口底部，往下弹会被切掉），
 * 所以两个父容器都得是 position:relative（composer 的 .ai-input-area、hero 的 .ai-hero-box）。
 */
import { useI18n } from 'vue-i18n'
import type { AISkill } from '../types/ai'

const props = defineProps<{
  items: AISkill[]
  idx: number
  /** 输入框里已经点到的技能名（行尾标「已选」，再点一次是取消） */
  picked: string[]
}>()

const emit = defineEmits<{
  (e: 'pick', sk: AISkill): void
}>()

const { t } = useI18n()

function isPicked(sk: AISkill): boolean {
  return (props.picked || []).includes(sk.name)
}
</script>

<template>
  <div class="ai-slash-pop">
    <button
      v-for="(sk, i) in props.items"
      :key="sk.name"
      class="ai-slash-row"
      :class="{ 'is-active': i === props.idx, 'is-picked': isPicked(sk) }"
      :title="sk.path"
      @mousedown.prevent="emit('pick', sk)"
    >
      <span class="ai-slash-name">/{{ sk.name }}</span>
      <span class="ai-slash-desc">{{ sk.description || sk.dir }}</span>
      <span v-if="isPicked(sk)" class="ai-slash-state">{{ t('aiSkillPicked') }}</span>
    </button>
    <div class="ai-slash-foot">{{ t('aiSlashHint') }}</div>
  </div>
</template>

<style scoped>
.ai-slash-pop {
  position: absolute;
  left: 16px;
  right: 16px;
  bottom: calc(100% - 6px);
  z-index: 30;
  max-height: 260px;
  overflow: auto;
  padding: 4px;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
}

.ai-slash-row {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 5px 6px;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}

.ai-slash-row.is-active { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-slash-name {
  flex-shrink: 0;
  max-width: 45%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--font-mono);
  font-size: 12px;
}

.ai-slash-row.is-picked .ai-slash-name { color: var(--color-accent); }

.ai-slash-desc {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  color: var(--color-text-muted);
}

/* 「已选」是文字标记而不是勾选框：勾选框会让人以为还有个可以单独保存的勾选状态，
   而这里的选择就在输入框里，再点一次即取消。 */
.ai-slash-state {
  flex: none;
  font-size: 10px;
  color: var(--color-accent);
}

.ai-slash-foot {
  padding: 4px 6px 2px;
  font-size: 10px;
  color: var(--color-text-disabled);
  border-top: 1px solid var(--color-border);
  margin-top: 2px;
}
</style>
