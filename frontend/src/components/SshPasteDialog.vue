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
import { computed, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ShieldAlert } from '@lucide/vue'
import { splitPasteLines } from '../utils/ssh'
import { useFocusTrap } from '../utils/focusTrap'

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

const boxRef = ref<HTMLElement | null>(null)
const { onKeydown } = useFocusTrap(toRef(props, 'visible'), boxRef)

// 每次重新打开都把初始焦点落在「取消」上。
//
// 这个弹窗的默认动作必须是「不发」——用户一进来手指就在键盘上，
// 若焦点落在「仍然发送」上，一个误触的回车就把内容投到远端 shell 了。
watch(
  () => props.visible,
  v => {
    if (v) boxRef.value?.querySelector<HTMLButtonElement>('.paste-btn')?.focus()
  },
)
</script>

<template>
  <div
    v-if="visible"
    class="paste-mask"
    @click.self="emit('cancel')"
    @keydown="onKeydown"
  >
    <div ref="boxRef" class="paste-box" role="dialog" aria-modal="true">
      <header class="paste-head">
        <ShieldAlert :size="16" class="paste-warn-icon" />
        <h3 class="paste-title">{{ t('sshPasteTitle') }}</h3>
      </header>

      <p class="paste-desc">{{ t('sshPasteDesc', { lines: parsed.total }) }}</p>

      <div class="paste-preview">
        <div class="paste-preview-label">{{ t('sshPastePreview') }}</div>
        <pre class="paste-code">{{ parsed.lines.join('\n') }}</pre>
        <p v-if="parsed.truncated" class="paste-more">
          {{ t('sshPasteMore', { n: parsed.total - parsed.lines.length }) }}
        </p>
      </div>

      <p class="paste-hint">{{ t('sshPasteDangerHint') }}</p>

      <footer class="paste-actions">
        <button class="paste-btn" @click="emit('cancel')">{{ t('cancel') }}</button>
        <button class="paste-btn paste-btn--danger" @click="emit('confirm')">
          {{ t('sshPasteConfirm') }}
        </button>
      </footer>
    </div>
  </div>
</template>

<style scoped>
.paste-mask {
  position: absolute;
  inset: 0;
  z-index: 40;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: var(--space-6);
  background: var(--color-bg-overlay);
}

.paste-box {
  width: 460px;
  max-width: 100%;
  max-height: 100%;
  display: flex;
  flex-direction: column;
  padding: var(--space-6);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-toast);
}

.paste-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
}

.paste-warn-icon {
  color: var(--color-warning);
  flex: 0 0 auto;
}

.paste-title {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.paste-desc {
  margin: 0 0 var(--space-4);
  font-size: 11.5px;
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
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.paste-code {
  margin: 0;
  max-height: 130px;
  overflow-y: auto;
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 1.6;
  color: var(--color-text-primary);
  white-space: pre-wrap;
  word-break: break-all;
  user-select: text;
}

.paste-more {
  margin: var(--space-2) 0 0;
  font-size: 10.5px;
  color: var(--color-text-muted);
}

.paste-hint {
  margin: var(--space-4) 0 0;
  font-size: 11px;
  line-height: 1.6;
  color: var(--color-warning);
}

.paste-actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
  margin-top: var(--space-6);
}

.paste-btn {
  padding: var(--space-2) var(--space-5);
  font-size: 11.5px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast),
    border-color var(--transition-fast);
}

.paste-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.paste-btn--danger {
  color: var(--color-danger);
  border-color: var(--color-danger-bg);
  background: var(--color-danger-bg);
}

.paste-btn--danger:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
  border-color: var(--color-danger);
}
</style>
