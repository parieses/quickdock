<script lang="ts" setup>
/**
 * 多行粘贴二次确认（#15）。
 *
 * 为什么必须有：xterm 的 paste 会把多行文本一次性交给 onData，
 * 而 onData 是按字节直接发给远端 shell 的。用户从文档里复制一段
 * 「说明 + 命令」（最常见的形态）粘进终端，第一行是命令、后面几行是正文，
 * shell 会把每一行都当独立命令执行——`rm -rf` 就这么来的。
 *
 * 只拦多行：单行粘贴是终端的日常操作，加确认等于每天骚扰用户几十次。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldAlert } from '@lucide/vue'
import { splitPasteLines } from '../utils/ssh'
import QdModal from './QdModal.vue'

const { t } = useI18n()

const props = defineProps<{
  visible: boolean
  /** 待发送的原始文本 */
  data: string
}>()

const emit = defineEmits<{
  confirm: []
  cancel: []
}>()

const parsed = computed(() => splitPasteLines(props.data, 6))

// 初始焦点交给 QdModal 的 v-dialog：本弹窗没有表单控件，会落到头部关闭按钮（≡ 取消），
// 仍然满足「默认动作必须是不发」——误触回车不会把内容投到远端。
</script>

<template>
  <!-- inline：只盖住终端区域（终端页 keep-alive，全局模态会在切走后飘到别的页面）。 -->
  <QdModal
    v-if="visible"
    :model-value="true"
    inline
    width="460px"
    @update:model-value="emit('cancel')"
  >
    <template #header>
      <ShieldAlert :size="16" class="paste-warn-icon" />
      <h3 class="paste-title">{{ t('sshPasteTitle') }}</h3>
    </template>

    <p class="paste-desc">{{ t('sshPasteDesc', { lines: parsed.total }) }}</p>

    <div class="paste-preview">
      <div class="paste-preview-label">{{ t('sshPastePreview') }}</div>
      <pre class="paste-code">{{ parsed.lines.join('\n') }}</pre>
      <p v-if="parsed.truncated" class="paste-more">
        {{ t('sshPasteMore', { n: parsed.total - parsed.lines.length }) }}
      </p>
    </div>

    <p class="paste-hint">{{ t('sshPasteDangerHint') }}</p>

    <template #footer>
      <button class="qd-btn" @click="emit('cancel')">{{ t('cancel') }}</button>
      <button class="qd-btn qd-btn-danger" @click="emit('confirm')">
        {{ t('sshPasteConfirm') }}
      </button>
    </template>
  </QdModal>
</template>

<style scoped>
/* 外壳（遮罩 / 卡片 / 头部 / 底部）由 QdModal 提供，这里只留内容样式。 */
.paste-warn-icon {
  color: var(--color-warning);
  flex: 0 0 auto;
}

.paste-title {
  flex: 1;
  min-width: 0;
  margin: 0;
  font-size: var(--text-base);
  font-weight: 600;
  color: var(--color-text-primary);
}

.paste-desc {
  margin: 0 0 var(--space-4);
  font-size: var(--text-sm);
  line-height: 1.7;
  color: var(--color-text-secondary);
}

.paste-preview {
  padding: var(--space-4);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
}

.paste-preview-label {
  margin-bottom: var(--space-2);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.paste-code {
  margin: 0;
  max-height: 130px;
  overflow-y: auto;
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-text-primary);
  white-space: pre-wrap;
  word-break: break-all;
  user-select: text;
}

.paste-more {
  margin: var(--space-2) 0 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.paste-hint {
  margin: var(--space-4) 0 0;
  font-size: var(--text-xs);
  line-height: 1.6;
  color: var(--color-warning);
}
</style>
