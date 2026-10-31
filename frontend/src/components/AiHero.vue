<script setup lang="ts">
/**
 * 空态落地页：新建任务不走弹窗，直接在这一页选类型、选空间、提问。
 * 类型与空间仍是这条任务的属性，首次发送时随请求落库。
 *
 * 这里的输入框与聊天区输入区共用同一份 `input` / `attachments` / `urlDraft` 状态
 * （都在 AIPage），所以两边能无缝接上：在落地页贴了一张图，发出去之后
 * 聊天区那排缩略图显示的是同一张。
 */
import { ref, type Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { ArrowUp, FolderOpen, Image as ImageIcon, Link2 } from '@lucide/vue'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AISpace } from '../types/ai'
import type { PendingImage } from '../types'

/** 一排类型 pill 的一项（icon 是 lucide 组件）。script setup 里不能 export，父组件按结构传即可。 */
interface HeroMode {
  key: string
  label: string
  desc: string
  icon: Component
}

const props = defineProps<{
  modes: HeroMode[]
  newTaskMode: string
  urlInputOpen: boolean
  urlDraft: string
  attachments: PendingImage[]
  input: string
  hasKey: boolean
  newTaskSpace: string
  spaces: AISpace[]
  /** 「新建空间」的哨兵值；等于它时才显示选目录按钮 */
  newSpace: string
  newTaskDir: string
  newTaskDirLabel: string
  aiActive: string
  aiProfiles: AIProfile[]
  allowFileImage: boolean
  allowUrlImage: boolean
  /** 产物去向说明（普通任务 / 空间任务文案不同，由父组件算） */
  outHint: string
  heroTips: string[]
}>()

const emit = defineEmits<{
  (e: 'update:newTaskMode', v: string): void
  (e: 'update:urlDraft', v: string): void
  (e: 'update:input', v: string): void
  (e: 'update:newTaskSpace', v: string): void
  (e: 'update:aiActive', v: string): void
  (e: 'model-change'): void
  (e: 'enter', ev: KeyboardEvent): void
  (e: 'send'): void
  (e: 'paste', ev: ClipboardEvent): void
  (e: 'drop', ev: DragEvent): void
  (e: 'confirm-url'): void
  (e: 'remove-attachment', i: number): void
  (e: 'pick-dir'): void
  (e: 'open-file-picker'): void
  (e: 'toggle-url'): void
  (e: 'fill-tip', key: string): void
  (e: 'open-settings'): void
}>()

const { t } = useI18n()

// 落地页输入框：父组件在「新建任务 / 填示例」时要把焦点放回来，所以对外暴露 focus()
const heroInputEl = ref<HTMLTextAreaElement | null>(null)
defineExpose({ focus: () => heroInputEl.value?.focus() })

function text(e: Event): string {
  return (e.target as HTMLTextAreaElement).value
}

function onSpaceSelect(e: Event) {
  emit('update:newTaskSpace', (e.target as HTMLSelectElement).value)
}

/** 先回写选中的档案，再通知父组件去应用（顺序反了 onModelChange 读到的还是旧值） */
function onModelSelect(e: Event) {
  emit('update:aiActive', (e.target as HTMLSelectElement).value)
  emit('model-change')
}
</script>

<template>
  <div class="ai-hero">
    <div class="ai-hero-inner">
      <h1 class="ai-hero-title">{{ t('aiHeroTitle') }}</h1>

      <!-- 一排类型 pill：默认对话，点一下换成本次任务的类型 -->
      <div class="ai-hero-modes">
        <button
          v-for="m in props.modes"
          :key="m.key"
          :class="['ai-hero-mode', { active: props.newTaskMode === m.key }]"
          :title="t(m.desc)"
          @click="emit('update:newTaskMode', m.key)"
        >
          <component :is="m.icon" :size="13" />
          {{ t(m.label) }}
        </button>
      </div>

      <div class="ai-hero-box" @dragover.prevent @drop="emit('drop', $event)">
        <!-- 图片链接输入（与聊天区同一套状态，落地页也保留这个入口） -->
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
        <div v-if="props.attachments.length" class="ai-attach-preview">
          <div v-for="(a, i) in props.attachments" :key="i" class="ai-attach-thumb">
            <img v-if="a.kind === 'file' && a.dataUrl" :src="a.dataUrl" class="ai-attach-img" />
            <img v-else-if="a.kind === 'url'" :src="a.url" class="ai-attach-img" />
            <button class="ai-attach-del" :title="t('delete')" @click="emit('remove-attachment', i)">×</button>
          </div>
        </div>
        <textarea
          ref="heroInputEl"
          :value="props.input"
          class="ai-hero-input"
          rows="3"
          :placeholder="t('aiHeroPlaceholder')"
          :disabled="!props.hasKey"
          @input="emit('update:input', text($event))"
          @keydown.enter="emit('enter', $event)"
          @paste="emit('paste', $event)"
        ></textarea>
        <div class="ai-hero-bar">
          <!-- 归属空间：不选就是普通任务（后端会自动给它建一个专属目录）。
               已有空间直接选；「新建空间…」才需要选目录——空间按目录唯一。 -->
          <select :value="props.newTaskSpace" class="ai-hero-select" :title="t('aiNewTaskSpace')" @change="onSpaceSelect($event)">
            <option value="">{{ t('aiNewTaskSpaceNone') }}</option>
            <option v-for="s in props.spaces" :key="s.id" :value="s.id" :title="s.cwd">{{ s.name }}</option>
            <option :value="props.newSpace">{{ t('aiNewTaskSpaceNew') }}</option>
          </select>
          <button
            v-if="props.newTaskSpace === props.newSpace"
            class="ai-hero-select"
            :title="props.newTaskDir || t('aiPickFolderTitle')"
            @click="emit('pick-dir')"
          >
            <FolderOpen :size="12" />
            <span class="ai-hero-dir">{{ props.newTaskDir ? props.newTaskDirLabel : t('aiNewTaskDirNone') }}</span>
          </button>
          <select :value="props.aiActive" class="ai-hero-select" :title="t('aiSwitchModel')" @change="onModelSelect($event)">
            <option v-for="p in props.aiProfiles" :key="p.id" :value="p.id">{{ p.name || p.model }}</option>
          </select>
          <div class="ai-hero-bar-right">
            <button v-if="props.allowFileImage" class="ai-tool-btn" :title="t('aiAttachImage')" @click="emit('open-file-picker')">
              <ImageIcon :size="16" />
            </button>
            <button v-if="props.allowUrlImage" class="ai-tool-btn" :title="t('aiAddImageUrl')" @click="emit('toggle-url')">
              <Link2 :size="16" />
            </button>
            <button
              class="ai-hero-send"
              :disabled="!props.hasKey || (!props.input.trim() && !props.attachments.length)"
              :title="t('send')"
              @click="emit('send')"
            >
              <ArrowUp :size="16" />
            </button>
          </div>
        </div>
      </div>

      <!-- 产物去向：既是说明也是「空间是什么」的解释——比一个空下拉好懂 -->
      <p class="ai-hero-out">{{ props.outHint }}</p>

      <p v-if="!props.hasKey" class="ai-hero-needkey">
        {{ t('aiNeedKey') }}
        <button class="ai-go-settings" @click="emit('open-settings')">{{ t('aiGoSettings') }}</button>
      </p>

      <div class="ai-hero-tips">
        <button v-for="k in props.heroTips" :key="k" class="ai-hero-tip" @click="emit('fill-tip', k)">
          {{ t(k) }}
          <ArrowUp :size="11" class="ai-hero-tip-icon" />
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ai-hero {
  flex: 1;
  overflow-y: auto;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 16px 40px;
}

.ai-hero-inner {
  width: 100%;
  max-width: 660px;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}

.ai-hero-title {
  margin: 0 0 2px;
  font-size: 26px;
  font-weight: 700;
  letter-spacing: -0.2px;
  color: var(--color-text-primary);
}

.ai-hero-modes { display: flex; flex-wrap: wrap; justify-content: center; gap: 6px; }

.ai-hero-mode {
  display: inline-flex; align-items: center; gap: 5px;
  padding: 5px 12px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px; font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast), background-color var(--transition-fast);
}

.ai-hero-mode:hover { color: var(--color-text-primary); border-color: var(--color-text-muted); }

.ai-hero-mode.active { color: var(--color-accent); border-color: var(--color-accent); background: var(--color-accent-bg); }

.ai-hero-box {
  width: 100%;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-bg-tertiary);
  padding: 10px 12px 8px;
  transition: border-color var(--transition-fast);
}

.ai-hero-box:focus-within { border-color: var(--color-accent); }

.ai-hero-input {
  width: 100%;
  min-height: 64px;
  max-height: 240px;
  resize: none;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: 14px;
  line-height: 1.6;
}

.ai-hero-input::placeholder { color: var(--color-text-disabled); }

.ai-hero-input:disabled { opacity: 0.6; cursor: not-allowed; }

.ai-hero-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: 6px;
}

.ai-hero-bar-right { display: flex; align-items: center; gap: 6px; margin-left: auto; }

.ai-hero-select {
  display: inline-flex; align-items: center; gap: 5px;
  max-width: 180px;
  padding: 4px 8px;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-muted);
  font-size: 12px; font-family: inherit;
  cursor: pointer;
  text-overflow: ellipsis;
}

.ai-hero-select:hover { color: var(--color-text-primary); border-color: var(--color-border); }

.ai-hero-select:focus-visible { outline: none; border-color: var(--color-accent); }

select.ai-hero-select {
  display: inline-block;
  background: var(--color-bg-tertiary);
  border-color: var(--color-border);
  color: var(--color-text-primary);
}

.ai-hero-dir { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.ai-hero-send {
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px;
  border: none; border-radius: 50%;
  background: var(--color-accent); color: #fff;
  cursor: pointer;
  transition: opacity var(--transition-fast), background-color var(--transition-fast);
}

.ai-hero-send:disabled { background: var(--color-bg-active); color: var(--color-text-disabled); cursor: default; }

.ai-hero-out {
  margin: 0;
  font-size: 11px;
  color: var(--color-text-disabled);
  text-align: center;
  line-height: 1.5;
  word-break: break-all;
}

.ai-hero-needkey {
  margin: 0;
  display: flex; align-items: center; gap: 8px;
  font-size: 12px; color: var(--color-warning);
}

.ai-hero-tips { width: 100%; display: flex; flex-direction: column; gap: 8px; margin-top: 4px; }

.ai-hero-tip {
  display: inline-flex; align-items: center; gap: 6px;
  align-self: flex-start;
  max-width: 100%;
  padding: 7px 12px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12.5px; font-family: inherit; text-align: left;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.ai-hero-tip:hover { color: var(--color-text-primary); border-color: var(--color-accent); }

.ai-hero-tip-icon { transform: rotate(45deg); opacity: 0.55; flex-shrink: 0; }
</style>
