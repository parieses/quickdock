<script setup lang="ts">
/**
 * 聊天区底部输入区：钩子批准条 + 待发送图片 + 图片链接 + @ 引用 + 输入行。
 *
 * 状态全部归 AIPage（与落地页 hero 共用同一份 input / attachments / urlDraft），
 * 这里只负责渲染与把交互 emit 回去。`@` 补全弹层用 absolute 挂在输入区上沿——
 * 输入区贴着窗口底部，往下弹会被切掉；也不跟光标跑（textarea 里算光标坐标
 * 要镜像一个 div，得不偿失）。
 */
import { useI18n } from 'vue-i18n'
import { FileText, Folder, Image as ImageIcon, Link2, MessageSquare } from '@lucide/vue'
import type { AIHooksInfo, AIMentionItem } from '../types/ai'
import type { PendingImage } from '../types'

const props = defineProps<{
  hooks: AIHooksInfo | null
  attachments: PendingImage[]
  urlInputOpen: boolean
  urlDraft: string
  mentions: AIMentionItem[]
  mentionOpen: boolean
  mentionItems: AIMentionItem[]
  mentionIdx: number
  input: string
  hasKey: boolean
  streaming: boolean
  allowFileImage: boolean
  allowUrlImage: boolean
}>()

const emit = defineEmits<{
  (e: 'update:urlDraft', v: string): void
  (e: 'update:input', v: string): void
  (e: 'approve-hooks', on: boolean): void
  (e: 'remove-attachment', i: number): void
  (e: 'confirm-url'): void
  (e: 'remove-mention', i: number): void
  (e: 'pick-mention', it: AIMentionItem): void
  (e: 'composer-key', ev: KeyboardEvent): void
  (e: 'send'): void
  (e: 'paste', ev: ClipboardEvent): void
  (e: 'drop', ev: DragEvent): void
  (e: 'open-file-picker'): void
  (e: 'toggle-url'): void
}>()

const { t } = useI18n()

function text(e: Event): string {
  return (e.target as HTMLTextAreaElement).value
}

function iconOf(kind: string) {
  return kind === 'conv' ? MessageSquare : kind === 'dir' ? Folder : FileText
}
</script>

<template>
  <div class="ai-input-area" @dragover.prevent @drop="emit('drop', $event)">
    <!-- 项目钩子批准条（C3）：只在「配置了但还没批」时出现。 -->
    <div v-if="props.hooks && props.hooks.found && !props.hooks.approved" class="ai-hook-banner">
      <template v-if="props.hooks.error">
        <div class="ai-hook-banner-title">{{ t('aiHookBannerTitle') }}</div>
        <div class="ai-hook-banner-desc">{{ t('aiHookBannerBad', { path: props.hooks.path, err: props.hooks.error }) }}</div>
      </template>
      <template v-else>
        <div class="ai-hook-banner-title">{{ t('aiHookBannerTitle') }}</div>
        <div class="ai-hook-banner-desc">{{ t('aiHookBannerDesc', { path: props.hooks.path, n1: props.hooks.afterEdit.length, n2: props.hooks.beforeCommand.length }) }}</div>
        <div class="ai-hook-banner-actions">
          <button class="ai-hook-ok" @click="emit('approve-hooks', true)">{{ t('aiHookApprove') }}</button>
          <button class="ai-hook-no" @click="emit('approve-hooks', false)">{{ t('aiHookDecline') }}</button>
        </div>
      </template>
    </div>
    <!-- 待发送图片预览 -->
    <div v-if="props.attachments.length" class="ai-attach-preview">
      <div v-for="(a, i) in props.attachments" :key="i" class="ai-attach-thumb">
        <img v-if="a.kind === 'file' && a.dataUrl" :src="a.dataUrl" class="ai-attach-img" />
        <img v-else-if="a.kind === 'url'" :src="a.url" class="ai-attach-img" />
        <button class="ai-attach-del" :title="t('delete')" @click="emit('remove-attachment', i)">×</button>
      </div>
    </div>
    <!-- 图片链接输入框（点击「图片链接」后展开） -->
    <div v-if="props.urlInputOpen" class="ai-url-row">
      <input
        :value="props.urlDraft"
        type="text"
        class="ai-url-input"
        :placeholder="t('aiAddImageUrlPh')"
        @input="emit('update:urlDraft', ($event.target as HTMLInputElement).value)"
        @keydown.enter.prevent="emit('confirm-url')"
      />
      <button class="ai-url-ok" @click="emit('confirm-url')">{{ t('add') }}</button>
    </div>
    <!-- @ 引用（B2）：已选 chip 与补全弹层。
         @ 只在主输入框生效：落地页那个 hero 输入框是同一份 v-model，但弹出的
         弹层是相对输入区定位的，在 hero 里打 @ 只会留下纯文本（后端 mentions 为空
         时不会展开任何东西），不会出现「弹层在屏幕另一头」这种怪状。 -->
    <div v-if="props.mentions.length" class="ai-mention-chips">
      <span
        v-for="(m, i) in props.mentions"
        :key="m.kind + (m.path || m.convId || String(i))"
        class="ai-mention-chip"
        :title="m.path || m.name"
      >
        <component :is="iconOf(m.kind)" :size="11" />
        <span class="ai-mention-chip-name">{{ m.kind === 'conv' ? m.name : (m.path || m.name) }}</span>
        <button class="ai-mention-chip-del" :title="t('delete')" @click="emit('remove-mention', i)">×</button>
      </span>
    </div>
    <div v-if="props.mentionOpen" class="ai-mention-pop">
      <div
        v-for="(it, i) in props.mentionItems"
        :key="it.kind + (it.path || it.convId || String(i))"
        class="ai-mention-item"
        :class="{ 'is-active': i === props.mentionIdx }"
        @mousedown.prevent="emit('pick-mention', it)"
      >
        <component :is="iconOf(it.kind)" :size="12" />
        <span class="ai-mention-name">{{ it.name }}</span>
        <span v-if="it.path" class="ai-mention-path">{{ it.path }}</span>
        <span v-else-if="it.kind === 'conv'" class="ai-mention-path">{{ t('aiMentionConv') }}</span>
      </div>
    </div>
    <div class="ai-input-row">
      <textarea
        :value="props.input"
        class="ai-input"
        :placeholder="t('aiPlaceholder')"
        :disabled="!props.hasKey || props.streaming"
        @input="emit('update:input', text($event))"
        @keydown="emit('composer-key', $event)"
        @paste="emit('paste', $event)"
      />
      <div class="ai-input-tools">
        <button v-if="props.allowFileImage" class="ai-tool-btn" :title="t('aiAttachImage')" @click="emit('open-file-picker')">
          <ImageIcon :size="16" />
        </button>
        <button v-if="props.allowUrlImage" class="ai-tool-btn" :title="t('aiAddImageUrl')" @click="emit('toggle-url')">
          <Link2 :size="16" />
        </button>
        <button class="ai-send" :disabled="!props.hasKey || props.streaming || (!props.input.trim() && !props.attachments.length)" @click="emit('send')">
          {{ props.streaming ? t('aiThinking') : t('send') }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ai-input-area {
  /* 输入行控件的唯一高度来源：输入框 / 图标按钮 / 发送按钮全部引用它，
     避免各处硬编码不同数值出现高度参差 */
  --ai-input-h: 56px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
  /* @ 补全弹层的定位锚点：它要贴着输入区上沿向上展开，
     而不是跟着光标跑（textarea 里算光标坐标要镜像一个 div，得不偿失）。 */
  position: relative;
}

.ai-input-row { display: flex; gap: 10px; align-items: flex-end; }

.ai-input-tools { display: flex; align-items: flex-end; gap: 6px; flex-shrink: 0; }

.ai-hook-banner {
  margin: 0 12px;
  padding: 8px 12px;
  border: 1px solid color-mix(in srgb, var(--color-warning) 45%, transparent);
  border-radius: 6px;
  background: color-mix(in srgb, var(--color-warning) 10%, var(--color-bg-primary));
}

.ai-hook-banner-title { font-size: 12px; font-weight: 600; color: var(--color-text-primary); }

.ai-hook-banner-desc { margin-top: 2px; font-size: 11px; line-height: 1.5; color: var(--color-text-secondary); word-break: break-all; }

.ai-hook-banner-actions { display: flex; gap: 8px; margin-top: 6px; }

.ai-hook-ok {
  padding: 2px 10px; font-size: 11px; border-radius: 4px; cursor: pointer;
  border: 1px solid var(--color-accent); background: var(--color-accent); color: #fff;
}

.ai-hook-no {
  padding: 2px 10px; font-size: 11px; border-radius: 4px; cursor: pointer;
  border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary);
}

.ai-hook-no:hover { color: var(--color-text-primary); }

.ai-mention-chips { display: flex; flex-wrap: wrap; gap: 6px; }

.ai-mention-chip {
  display: inline-flex; align-items: center; gap: 3px; max-width: 260px;
  padding: 2px 4px 2px 6px; font-size: 11px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
}

.ai-mention-chip-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono);
}

.ai-mention-chip-del {
  border: none; background: transparent; color: var(--color-text-muted);
  cursor: pointer; font-size: 13px; line-height: 1; padding: 0 2px;
}

.ai-mention-chip-del:hover { color: var(--color-danger); }

.ai-mention-pop {
  position: absolute; left: 16px; right: 16px; bottom: calc(100% - 6px);
  max-height: 240px; overflow: auto; z-index: 30; padding: 4px;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.18);
}

.ai-mention-item {
  display: flex; align-items: center; gap: 6px; padding: 4px 6px;
  font-size: 12px; color: var(--color-text-secondary); cursor: pointer;
  border-radius: var(--radius-sm);
}

.ai-mention-item.is-active { background: var(--color-bg-hover); color: var(--color-text-primary); }

.ai-mention-name { flex-shrink: 0; }

.ai-mention-path {
  flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-family: var(--font-mono); font-size: 11px;
  color: var(--color-text-muted); text-align: right;
}

.ai-input {
  flex: 1;
  resize: none;
  height: var(--ai-input-h);
  padding: 10px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 13px;
  font-family: inherit;
  outline: none;
  line-height: 1.5;
}

.ai-input:focus { border-color: var(--color-accent); }

.ai-input:disabled { opacity: 0.6; cursor: not-allowed; }

.ai-send {
  align-self: flex-end;
  padding: 0 20px;
  height: var(--ai-input-h);
  border: none;
  border-radius: var(--radius-md);
  background: var(--color-accent);
  color: var(--color-accent-text);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  font-family: inherit;
  white-space: nowrap;
}

.ai-send:disabled { opacity: 0.5; cursor: not-allowed; }
</style>
