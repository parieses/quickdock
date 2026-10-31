<script setup lang="ts">
/**
 * 工具审批条：固定在输入区上方，而不是挂在消息流里。
 *
 * 原来审批卡是流式工具卡片内部的一块 `<div class="ai-approval">`，位置跟着消息走：
 * 工具调用一多、或者用户往上翻了两屏，按钮就不在视野里了。而这期间整个会话是
 * 停住的（后端在等这个答复），「找不到按钮」的直接体感就是「卡死」。
 *
 * 挪到输入区上方后位置恒定——它跟「打字」在同一个视觉区域，不需要滚动去找。
 * 消息流里仍保留「等待确认」的状态字样，只是不再放按钮：两处各有一套按钮，
 * 点了一处另一处的状态同步容易慢半拍，反而让人以为没点上。
 */
import { useI18n } from 'vue-i18n'
import { ShieldAlert } from '@lucide/vue'
import type { AIToolCall } from '../types/ai'
import { toolTarget, toolVerb as aiToolVerb } from '../utils/aiTools'

const props = defineProps<{
  call: AIToolCall
  /** 同时待审的条数。>1 时标出来，否则点完一条又冒出一条会以为是没生效 */
  count?: number
}>()

const emit = defineEmits<{
  (e: 'allow'): void
  (e: 'remember'): void
  (e: 'deny'): void
}>()

const { t } = useI18n()

/** i18n 的 t 由本组件提供，工具动词的翻译在这里接上 */
function toolVerb(name: string): string {
  return aiToolVerb(name, t)
}
</script>

<template>
  <div class="ai-approve-bar">
    <ShieldAlert :size="15" class="ai-approve-ico" />
    <div class="ai-approve-main">
      <div class="ai-approve-head">
        <span class="ai-approve-title">{{ t('aiApprovalTitle') }}</span>
        <span class="ai-approve-op">
          <strong class="ai-approve-verb">{{ toolVerb(call.name) }}</strong>
          <span v-if="toolTarget(call)" class="ai-approve-target" :title="toolTarget(call)">{{ toolTarget(call) }}</span>
        </span>
        <span v-if="(count || 0) > 1" class="ai-approve-count">{{ t('aiApprovalProgress', { n: count }) }}</span>
      </div>
      <!-- 风险说明排在按钮之前：先让人看见「为什么问你」，再决定点哪个。
           反过来等于让人盲点。 -->
      <div v-if="call.riskNote" class="ai-approve-risk">{{ call.riskNote }}</div>
    </div>
    <div class="ai-approve-actions">
      <button class="ai-approve-btn is-deny" @click="emit('deny')">{{ t('aiApprovalDeny') }}</button>
      <!-- 高危类不给「本次会话都允许」，那是后端 rememberable 决定的 -->
      <button v-if="call.rememberable" class="ai-approve-btn" @click="emit('remember')">
        {{ call.grantScope ? t('aiApprovalRememberScope', { scope: call.grantScope }) : t('aiApprovalRemember') }}
      </button>
      <button class="ai-approve-btn is-allow" @click="emit('allow')">{{ t('aiApprovalAllow') }}</button>
    </div>
  </div>
</template>

<style scoped>
.ai-approve-bar {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  margin: 0 16px 6px;
  padding: 9px 11px;
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-md);
  background: var(--color-warning-bg);
  animation: ai-approve-in 0.16s ease-out;
}

@keyframes ai-approve-in {
  from { opacity: 0; transform: translateY(4px); }
  to { opacity: 1; transform: none; }
}

.ai-approve-ico {
  flex: 0 0 auto;
  margin-top: 1px;
  color: var(--color-warning);
}

/* 图标与按钮都不收缩，中间说明区吃掉剩余宽度 */
.ai-approve-main { flex: 1; min-width: 0; }

.ai-approve-head {
  display: flex;
  align-items: baseline;
  gap: 7px;
  flex-wrap: wrap;
}

.ai-approve-title {
  flex-shrink: 0;
  font-size: 12px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.ai-approve-op {
  display: inline-flex;
  align-items: baseline;
  gap: 6px;
  min-width: 0;
}

.ai-approve-verb { font-size: 12px; color: var(--color-text-primary); }

.ai-approve-target {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--color-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 340px;
}

.ai-approve-count {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--color-text-muted);
}

.ai-approve-risk {
  margin-top: 4px;
  font-size: 11.5px;
  line-height: 1.55;
  color: var(--color-text-secondary);
  word-break: break-word;
}

.ai-approve-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex: 0 0 auto;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.ai-approve-btn {
  padding: 4px 11px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-primary);
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: 11.5px;
  cursor: pointer;
  white-space: nowrap;
  transition: background-color var(--transition-fast), border-color var(--transition-fast), color var(--transition-fast);
}

.ai-approve-btn:hover { background: var(--color-bg-hover); border-color: var(--color-border-focus); }

.ai-approve-btn.is-allow {
  background: var(--color-accent);
  border-color: var(--color-accent);
  color: var(--color-accent-text);
}

.ai-approve-btn.is-allow:hover { background: var(--color-accent-hover); border-color: var(--color-accent-hover); }

.ai-approve-btn.is-deny { color: var(--color-danger); border-color: var(--color-danger-border); }

.ai-approve-btn.is-deny:hover { background: var(--color-danger-bg); border-color: var(--color-danger); }
</style>
