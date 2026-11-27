<script setup lang="ts">
/**
 * 任务清单卡片（plan_update 工具产生）。
 *
 * 为什么单独成组件：清单要挂在**生成它的那条 assistant 消息**里，跟着对话一起滚；
 * 早先在消息流末尾放一个固定插槽，结果上一轮的清单一直挂在最新回复下面——
 * 用户点开最新一条，看到的却是上一次任务的进度。抽成组件后历史消息和流式气泡
 * 各挂各的，折叠状态也随卡片走，不再由父组件按当前会话统一控制。
 *
 * 数据形状与父组件 PlanCardState 一致，这里就地声明，避免子组件依赖父组件类型。
 */
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, ChevronDown, ListChecks } from '@lucide/vue'

// 归档卡（被后续清单接续的旧清单）：默认折叠，标题标「历史」。
// 续跑时模型另建清单会让旧清单冻结在半途，展开平铺会让人误以为任务回到起点。
const props = defineProps<{
  card: { items: Array<{ text: string; status: string }>; note: string }
  archived?: boolean
}>()
const { t } = useI18n()

// 每张卡自带折叠状态：切会话 / 生成下一轮都是新卡片，不需要父组件去重置。
// 归档卡初值为折叠；用户仍可手动点开看旧清单详细。
const collapsed = ref(!!props.archived)
function onToggle(e: Event) {
  const el = e.target as HTMLDetailsElement
  if (el.open === !collapsed.value) return // 我们自己改 :open 引起的，不是用户点的
  collapsed.value = !el.open
}

function doneCount(card: { items: Array<{ status: string }> }): number {
  return card.items.filter((x) => x.status === 'done').length
}
</script>

<template>
  <details class="ai-plan-card" :open="!collapsed" @toggle="onToggle">
    <summary
      class="ai-plan-head"
      :title="collapsed ? t('aiPlanExpand') : t('aiPlanCollapse')"
    >
      <ListChecks :size="13" class="ai-plan-ico" />
      <span class="ai-plan-title">{{ t('aiPlanTitle') }}</span>
      <span v-if="archived" class="ai-plan-archived">{{ t('aiPlanArchived') }}</span>
      <span class="ai-plan-count">{{ t('aiPlanCount', { done: doneCount(card), total: card.items.length }) }}</span>
      <ChevronDown :size="13" class="ai-plan-chev" />
    </summary>
    <ul class="ai-plan-list">
      <li v-for="(it, i) in card.items" :key="i" class="ai-plan-item" :class="`is-${it.status}`">
        <span class="ai-plan-box">
          <Check v-if="it.status === 'done'" :size="10" />
          <span v-else-if="it.status === 'doing'" class="ai-plan-doing" />
          <span v-else-if="it.status === 'stopped'" class="ai-plan-stopped" />
        </span>
        <span class="ai-plan-text">{{ it.text }}</span>
      </li>
    </ul>
    <div v-if="card.note" class="ai-plan-note">{{ card.note }}</div>
  </details>
</template>

<style scoped>
/* 样式与原先 AIPage 里那张卡逐条一致，只改成 scoped：
   宽度 100%——父级 .ai-msg-content-wrap 已被 .ai-messages 的 padding 夹过一次，
   再套 --ai-column-w 会比正文窄一截。圆角+描边与工具卡/产物卡同一层级。 */
.ai-plan-card {
  width: 100%;
  padding: var(--space-4) var(--space-5) 10px;
  border: 1px solid var(--card-border-color);
  border-radius: var(--radius-md);
  background: var(--color-card-bg);
  font-size: var(--text-sm);
}
.ai-plan-head {
  display: flex; align-items: center; gap: var(--space-4);
  list-style: none; cursor: pointer; user-select: none; outline: none;
  color: var(--color-text-secondary);
}
.ai-plan-head::-webkit-details-marker { display: none; }
.ai-plan-head:hover { color: var(--color-text-primary); }
.ai-plan-ico { flex-shrink: 0; color: var(--color-text-muted); }
.ai-plan-title { font-weight: 600; color: var(--color-text-primary); }
.ai-plan-archived {
  flex-shrink: 0;
  padding: 0 5px; border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  font-size: var(--text-2xs); font-weight: 400;
}
.ai-plan-count { color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
.ai-plan-chev {
  flex: 0 0 auto; margin-left: auto;
  color: var(--color-text-muted); transition: transform var(--transition-fast);
}
.ai-plan-card:not([open]) .ai-plan-chev { transform: rotate(-90deg); }
.ai-plan-list {
  display: flex; flex-direction: column; gap: var(--space-2);
  margin: 9px 0 0; padding: 0; list-style: none;
}
.ai-plan-item { display: flex; align-items: flex-start; gap: var(--space-3); min-width: 0; color: var(--color-text-secondary); }
.ai-plan-box {
  flex: 0 0 auto; display: flex; align-items: center; justify-content: center;
  width: 14px; height: 14px; margin-top: var(--space-1);
  border: 1px solid var(--color-border); border-radius: 50%;
  color: var(--color-accent-text);
}
.ai-plan-item.is-done .ai-plan-box { border-color: var(--color-success); color: var(--color-success-ink); }
.ai-plan-item.is-done .ai-plan-text { color: var(--color-text-muted); }
.ai-plan-item.is-doing .ai-plan-box { border-color: var(--color-accent); }
.ai-plan-doing {
  width: 6px; height: 6px; border-radius: 50%;
  background: var(--color-accent);
  animation: ai-inject-pulse 1.2s ease-in-out infinite;
}
.ai-plan-item.is-stopped .ai-plan-box { border-color: var(--color-warning-border); }
.ai-plan-stopped { width: 6px; height: 2px; border-radius: 1px; background: var(--color-warning); }
.ai-plan-text { min-width: 0; overflow-wrap: anywhere; }
.ai-plan-note { margin-top: 7px; color: var(--color-text-muted); font-size: var(--text-sm); }
/* 关键帧必须在本组件里也有一份：父组件 AIPage 的 @keyframes 带 scoped 哈希，
   子组件引用不到——否则「正在做」的呼吸点静止成一个灰点。 */
@keyframes ai-inject-pulse {
  0%, 100% { opacity: 0.25; }
  50% { opacity: 1; }
}
</style>
