<script setup lang="ts">
/**
 * 用户消息正文：把 `<pasted_text>` 块折成可点开的 chip。
 *
 * 输入框里粘贴大段文字时 AiRichInput 会折成 chip，但发送时按约定把全文展开成
 * `<pasted_text lines=N>…</pasted_text>` 一起交给模型（全文必须给，不是省 token，
 * 是别让输入框没法用）。此前消息区直接渲染 m.content，于是「输入框是 chip、
 * 发出去又是一整屏原文」。这里把同一份标记在展示层还原成 chip，点开弹框看全文。
 *
 * 只动展示：m.content 原样保留（复制/重试/落库都用原文），不改发送链路。
 */
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Check, ClipboardPaste, Copy, FileText } from '@lucide/vue'
import QdModal from './QdModal.vue'
import { copyToClipboard } from '../utils/clipboard'
import { parsePastedSegments, type PastedSegment } from '../utils/aiPaste'

const props = defineProps<{ content: string }>()
const { t } = useI18n()

// 正则解析放在 utils 里（可单测）：pasted_text 是夹在正文里的标记，两侧还有用户自己打的字。
const segments = computed<PastedSegment[]>(() => parsePastedSegments(props.content))

const preview = ref<{ full: string; lines: string; n: number } | null>(null)
const copied = ref(false)

// 编号只数粘贴块，不把中间夹的普通文本算进去（对齐 [Pasted text #N]）。
function pasteIndex(upto: number): number {
  return segments.value.slice(0, upto).filter((s) => s.full !== undefined).length + 1
}

function openPreview(seg: PastedSegment, n: number) {
  copied.value = false
  preview.value = { full: seg.full || '', lines: seg.lines || '', n }
}

async function copyPreview() {
  if (!preview.value) return
  copied.value = await copyToClipboard(preview.value.full)
}

function lineCount(full: string, lines: string): string {
  return lines || String(full ? full.split('\n').length : 0)
}
</script>

<template>
  <span class="ai-pasted-content"><template v-for="(seg, i) in segments" :key="i"><span v-if="seg.text !== undefined" class="ai-pasted-plain">{{ seg.text }}</span><button v-else type="button" class="ai-pasted-chip" :title="t('aiPastedMsgTip')" @click="openPreview(seg, pasteIndex(i))"><FileText :size="11" /><span>{{ t('aiPastedMsgChip', { n: pasteIndex(i), lines: lineCount(seg.full || '', seg.lines || '') }) }}</span></button></template>
    <!-- QdModal 的关闭只 emit update:modelValue；这里必须把 preview 清空，
         否则 v-if 一直为真，关闭按钮 / 遮罩 / Esc 全都「点了没反应」。 -->
    <QdModal v-if="preview" :model-value="true" width="720px" @update:model-value="preview = null">
      <template #header>
        <span class="ai-paste-head-icon"><ClipboardPaste :size="15" /></span>
        <div class="ai-paste-head-text">
          <h3 class="ai-paste-head-title">{{ t('aiPastedMsgPreview', { n: preview.n }) }}</h3>
          <span class="ai-paste-head-sub">
            {{ t('aiPasteChip', { n: lineCount(preview.full, preview.lines) }) }} · {{ t('aiPastedMsgChars', { n: preview.full.length }) }}
          </span>
        </div>
      </template>
      <!-- 代码窗口式外壳：标题栏把「这是一段粘贴的纯文本」说清楚，
           正文用等宽字体 + 保留换行，跟项目里其他代码块/预览的观感一致。 -->
      <div class="ai-paste-window">
        <div class="ai-paste-window-bar">
          <span class="ai-paste-dots"><i></i><i></i><i></i></span>
          <span class="ai-paste-window-name">pasted_text #{{ preview.n }}</span>
        </div>
        <pre class="ai-paste-body">{{ preview.full }}</pre>
      </div>
      <template #footer>
        <button class="qd-btn qd-btn-sm qd-btn-primary" @click="copyPreview">
          <Check v-if="copied" :size="13" />
          <Copy v-else :size="13" />
          <span>{{ copied ? t('copied') : t('copy') }}</span>
        </button>
      </template>
    </QdModal>
  </span>
</template>

<style scoped>
/* 用户消息正文：保留原文换行；chip 与文字同行，别把一句话拆成两块。 */
.ai-pasted-content { white-space: pre-wrap; word-break: break-word; }
.ai-pasted-plain { white-space: inherit; }

.ai-pasted-chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 1px 7px; margin: 0 2px;
  vertical-align: baseline;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
  font-size: var(--text-xs); font-family: var(--font-mono);
  line-height: 1.4; cursor: pointer;
}
.ai-pasted-chip:hover { color: var(--color-accent); border-color: var(--color-accent-border); }

/* 弹框头部：图标 + 标题 + 一行元信息，别把「N 行 / N 字」堆在正文上方。 */
.ai-paste-head-icon {
  flex: none; width: 28px; height: 28px;
  display: inline-flex; align-items: center; justify-content: center;
  border-radius: var(--radius-sm);
  background: var(--color-accent-bg); color: var(--color-accent);
}
.ai-paste-head-text { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.ai-paste-head-title {
  margin: 0; font-size: var(--text-base); font-weight: 600;
  color: var(--color-text-primary); line-height: 1.3;
}
.ai-paste-head-sub { color: var(--color-text-muted); font-size: var(--text-xs); }

/* 代码窗口式预览：顶栏标出这是 pasted_text，正文等宽保留换行。
   滚动交给 QdModal 的 body（见 QdModal 用法约定），这里不再套一层滚动。 */
.ai-paste-window {
  overflow: hidden;
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  background: var(--color-bg-primary);
}
.ai-paste-window-bar {
  display: flex; align-items: center; gap: var(--space-3);
  padding: 6px var(--space-3);
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.ai-paste-dots { display: inline-flex; gap: 5px; }
.ai-paste-dots i { width: 7px; height: 7px; border-radius: 50%; background: var(--color-border); }
.ai-paste-dots i:first-child { background: var(--color-accent-border); }
.ai-paste-window-name {
  flex: 1; min-width: 0;
  font-family: var(--font-mono); font-size: var(--text-xs);
  color: var(--color-text-secondary);
}
.ai-paste-body {
  margin: 0; padding: var(--space-4);
  font-family: var(--font-mono); font-size: var(--text-sm);
  line-height: 1.7; white-space: pre-wrap; word-break: break-word;
  color: var(--color-text-primary);
  user-select: text;
}
</style>
