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
import { ArrowUp, GitBranch, Image as ImageIcon, Link2, Plus } from '@lucide/vue'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AIConnector, AIMentionItem, AIPrompt, AISkill, AISpace } from '../types/ai'
import type { PendingImage } from '../types'
import AiPlusPanel from './AiPlusPanel.vue'
import AiSlashPanel from './AiSlashPanel.vue'
import AiRichInput from './AiRichInput.vue'
import AiMentionBox from './AiMentionBox.vue'
import AiPermissionBadge from './AiPermissionBadge.vue'
import AiWorktreeToggle from './AiWorktreeToggle.vue'

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
  /** @ 引用：已选 chip 与补全弹层（与聊天输入框共用同一份状态） */
  mentions: AIMentionItem[]
  mentionOpen: boolean
  mentionItems: AIMentionItem[]
  mentionIdx: number
  hasKey: boolean
  newTaskSpace: string
  spaces: AISpace[]
  /** 「新建空间」的哨兵值；选中它时父组件会立刻弹目录选择器 */
  newSpace: string
  aiActive: string
  aiProfiles: AIProfile[]
  allowFileImage: boolean
  allowUrlImage: boolean
  /** 当前目录若在 git 仓库里，显示分支名（由父组件探测后传入） */
  gitRepo: boolean
  gitBranch: string
  /** 产物去向说明（普通任务 / 空间任务文案不同，由父组件算） */
  outHint: string
  heroTips: string[]
  /** 「技能和连接器」面板：与聊天输入框共用同一份状态，见 AIPage */
  plusOpen: boolean
  skills: AISkill[]
  connectors: AIConnector[]
  /** 提示词库：与聊天输入框共用同一个 + 面板，点一下把正文填进输入框 */
  prompts: AIPrompt[]
  /** 输入框里已选中的技能（行内胶囊，见 AiRichInput）：只给面板标「已选」用 */
  pickedSkills: string[]
  /** + 面板「引用」：本对话文件 / 工作目录候选 / 已引用的 path */
  files: AIMentionItem[]
  cwdFiles: AIMentionItem[]
  pickedFiles: string[]
  /** 「/ 选技能」补全 */
  slashOpen: boolean
  slashItems: AISkill[]
  slashIdx: number
  /** 正文非空：只挂了技能胶囊、正文空着时发送按钮该是灰的 */
  canSend: boolean
  /** 执行权限：true = 完全访问。新建任务默认是「默认权限」，在这里切的第一句就生效 */
  permissionFull: boolean
  /** 隔离工作区（见 AiWorktreeToggle）：只在 git 仓库里显示 */
  worktreeActive: boolean
  worktreePath: string
  worktreeBranch: string
  worktreeEnabled: boolean
  worktreeBranches: string[]
  worktreeBranchOpen: boolean
}>()

const emit = defineEmits<{
  (e: 'update:newTaskMode', v: string): void
  (e: 'update:urlDraft', v: string): void
  (e: 'update:input', v: string): void
  (e: 'update:newTaskSpace', v: string): void
  (e: 'update:aiActive', v: string): void
  (e: 'model-change'): void
  (e: 'key', ev: KeyboardEvent): void
  (e: 'send'): void
  (e: 'paste', ev: ClipboardEvent): void
  (e: 'drop', ev: DragEvent): void
  (e: 'confirm-url'): void
  (e: 'remove-attachment', i: number): void
  (e: 'pick-dir'): void
  (e: 'open-file-picker'): void
  (e: 'toggle-url'): void
  (e: 'fill-tip', key: string): void
  (e: 'shuffle-tips'): void
  (e: 'open-settings'): void
  (e: 'toggle-plus'): void
  (e: 'pick-skill', sk: AISkill): void
  (e: 'pick-prompt', p: AIPrompt): void
  (e: 'pick-file', it: AIMentionItem): void
  (e: 'manage-prompts'): void
  (e: 'update:skills', names: string[]): void
  (e: 'pick-mention', it: AIMentionItem): void
  (e: 'remove-mention', i: number): void
  (e: 'toggle-connector', c: AIConnector): void
  (e: 'install-skill'): void
  (e: 'manage-connectors'): void
  (e: 'change-permission', full: boolean): void
  (e: 'update:worktreeEnabled', v: boolean): void
  (e: 'toggle-worktree-branch'): void
  (e: 'pick-worktree-branch', name: string): void
  (e: 'remove-worktree'): void
}>()

const { t } = useI18n()

// 落地页输入框：父组件在「新建任务 / 填示例」时要把焦点放回来，所以对外暴露 focus()；
// 技能胶囊是输入框内部的行内节点，插入也只能由富输入框自己完成。
const heroInputEl = ref<InstanceType<typeof AiRichInput> | null>(null)
defineExpose({
  focus: () => heroInputEl.value?.focus(),
  insertSkill: (name: string) => heroInputEl.value?.insertSkill(name),
  insertPromptText: (text: string) => heroInputEl.value?.insertPromptText(text),
  takeSelectedText: () => heroInputEl.value?.takeSelectedText() || '',
})

function onSpaceSelect(e: Event) {
  const v = (e.target as HTMLSelectElement).value
  emit('update:newTaskSpace', v)
  // 「新建空间…」= 选一个目录（空间按目录唯一）：选中就当场上目录选择器，
  // 而不是让下拉先停在「新建空间…」等用户再点一次右边的按钮。
  if (v === props.newSpace) emit('pick-dir')
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
        <!-- @ 引用：与聊天输入框同一套状态，落地页也能引用文件/历史对话 -->
        <AiMentionBox
          :mentions="props.mentions"
          :open="props.mentionOpen"
          :items="props.mentionItems"
          :idx="props.mentionIdx"
          @pick="emit('pick-mention', $event)"
          @remove="emit('remove-mention', $event)"
        />
        <AiRichInput
          ref="heroInputEl"
          :model-value="props.input"
          :placeholder="t('aiHeroPlaceholder')"
          :disabled="!props.hasKey"
          :min-height="64"
          :max-height="240"
          @update:model-value="emit('update:input', $event)"
          @update:skills="emit('update:skills', $event)"
          @key="emit('key', $event)"
          @paste="emit('paste', $event)"
        />
        <div class="ai-hero-bar">
          <!-- 左组：这次任务「在哪儿做」——技能面板、当前分支、隔离工作区。
               与聊天输入区的底栏同一套排布与尺寸，两边看起来才是同一个东西。 -->
          <div class="ai-hero-bar-left">
            <!-- 技能和连接器：与聊天输入框同一个面板、同一份状态（技能按会话、连接器按本机）。 -->
            <button
              class="ai-hero-plus"
              :class="{ 'is-open': props.plusOpen }"
              :title="t('aiPlusTip')"
              @click="emit('toggle-plus')"
            >
              <Plus :size="16" />
            </button>
            <!-- 当前目录若在 git 仓库里，左侧显示分支（与聊天输入框同款提示）。 -->
            <div v-if="props.gitRepo" class="ai-git-branch" :title="t('aiGitBranchTip')">
              <GitBranch :size="12" />
              <span class="ai-git-branch-name">{{ props.gitBranch }}</span>
            </div>
            <!-- 隔离工作区：新建任务时也能勾，第一句就落在独立 worktree 里 -->
            <AiWorktreeToggle
              :git-repo="props.gitRepo"
              :active="props.worktreeActive"
              :path="props.worktreePath"
              :branch="props.worktreeBranch"
              :enabled="props.worktreeEnabled"
              :branches="props.worktreeBranches"
              :branch-open="props.worktreeBranchOpen"
              :disabled="false"
              @update:enabled="emit('update:worktreeEnabled', $event)"
              @toggle-branch="emit('toggle-worktree-branch')"
              @pick-branch="emit('pick-worktree-branch', $event)"
              @remove="emit('remove-worktree')"
            />
          </div>
          <!-- 分隔线：左边是「在哪儿做」，右边是「这条任务算什么」。混成一排时，
               一眼分不清哪个是上下文、哪个是设置。 -->
          <span class="ai-hero-bar-div" aria-hidden="true"></span>
          <!-- 中组：任务属性（归属空间 / 用哪个模型）。两者用同一种「小胶囊」，
               窄窗口下整组一起换行，不会出现「两个下拉被拆到两行」。 -->
          <div class="ai-hero-bar-mid">
            <!-- 归属空间：不选就是普通任务（后端会自动给它建一个专属目录）。
                 已有空间直接选；选「新建空间…」会立刻弹出目录选择器——空间按目录唯一，
                 所以不需要「先选新建、再点一个按钮选目录」这两步。选到的目录见下方产物去向。 -->
            <select :value="props.newTaskSpace" class="ai-hero-select" :title="t('aiNewTaskSpace')" @change="onSpaceSelect($event)">
              <option value="">{{ t('aiNewTaskSpaceNone') }}</option>
              <option v-for="s in props.spaces" :key="s.id" :value="s.id" :title="s.cwd">{{ s.name }}</option>
              <option :value="props.newSpace">{{ t('aiNewTaskSpaceNew') }}</option>
            </select>
            <!-- 没有可用模型（还没配 key）时不给这个空下拉：一个没有选项的胶囊
                 只会让人以为点它就能选。此时下方的「去设置」才是该走的路。 -->
            <select
              v-if="props.aiProfiles.length"
              :value="props.aiActive"
              class="ai-hero-select"
              :title="t('aiSwitchModel')"
              @change="onModelSelect($event)"
            >
              <option v-for="p in props.aiProfiles" :key="p.id" :value="p.id">{{ p.name || p.model }}</option>
            </select>
          </div>
          <div class="ai-hero-bar-right">
            <!-- 当前权限：落地页也能切，第一句就按这个口径执行 -->
            <AiPermissionBadge :full="props.permissionFull" @change="emit('change-permission', $event)" />
            <button v-if="props.allowFileImage" class="ai-tool-btn" :title="t('aiAttachImage')" @click="emit('open-file-picker')">
              <ImageIcon :size="16" />
            </button>
            <button v-if="props.allowUrlImage" class="ai-tool-btn" :title="t('aiAddImageUrl')" @click="emit('toggle-url')">
              <Link2 :size="16" />
            </button>
            <button
              class="ai-hero-send"
              :disabled="!props.hasKey || (!props.canSend && !props.attachments.length)"
              :title="t('send')"
              @click="emit('send')"
            >
              <ArrowUp :size="16" />
            </button>
          </div>
        </div>
        <!-- 技能和连接器面板（与聊天输入框共用组件），挂在 .ai-hero-box 内以便向上展开 -->
        <AiSlashPanel
          v-if="props.slashOpen"
          :items="props.slashItems"
          :idx="props.slashIdx"
          :picked="props.pickedSkills"
          @pick="emit('pick-skill', $event)"
        />
        <AiPlusPanel
          v-if="props.plusOpen"
          :skills="props.skills"
          :connectors="props.connectors"
          :prompts="props.prompts"
          :picked="props.pickedSkills"
          :files="props.files"
          :cwd-files="props.cwdFiles"
          :picked-files="props.pickedFiles"
          @close="emit('toggle-plus')"
          @pick-skill="emit('pick-skill', $event)"
          @pick-prompt="emit('pick-prompt', $event)"
          @pick-file="emit('pick-file', $event)"
          @toggle-connector="emit('toggle-connector', $event)"
          @install-skill="emit('install-skill')"
          @manage-connectors="emit('manage-connectors')"
          @manage-prompts="emit('manage-prompts')"
        />
      </div>

      <!-- 产物去向：既是说明也是「空间是什么」的解释——比一个空下拉好懂 -->
      <p class="ai-hero-out">{{ props.outHint }}</p>

      <p v-if="!props.hasKey" class="ai-hero-needkey">
        {{ t('aiNeedKey') }}
        <button class="ai-go-settings" @click="emit('open-settings')">{{ t('aiGoSettings') }}</button>
      </p>

      <div class="ai-hero-tips-head">
        <span class="ai-hero-tips-label">{{ t('aiHeroTipsLabel') }}</span>
        <button class="ai-hero-tips-shuffle" type="button" :title="t('aiHeroTipShuffle')" @click="emit('shuffle-tips')">{{ t('aiHeroTipShuffle') }}</button>
      </div>
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
  /* 左右必须与 .ai-dock 的左右内边距同源（--space-6）：两页输入框的左边缘
     靠它对齐，一边写字面 16px、一边用 token，改 token 时就会错开。 */
  padding: var(--space-8) var(--space-6) var(--space-10);
}

.ai-hero-inner {
  /* 宽度走 --ai-column-w：与对话页底部输入框同一个口径（见 style.css）。
     这里不再重复写 min(90vw,1200px)——两处各写一份，改一处就悄悄错位。 */
  width: var(--ai-column-w);
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
  /* 技能和连接器面板的定位锚点（向上展开）。 */
  position: relative;
}

/* 左下角的 +：与聊天输入区同款，只承载「技能和连接器」面板。 */
.ai-hero-plus {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  flex-shrink: 0;
  border: none;
  border-radius: 50%;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-hero-plus:hover,
.ai-hero-plus.is-open { background: var(--color-bg-active); color: var(--color-text-primary); }

.ai-hero-box:focus-within { border-color: var(--color-accent); }

/* 富输入框（AiRichInput）在落地页用大一号的字，与原来那个 textarea 一致 */
.ai-hero-box :deep(.ai-rich) { font-size: 14px; line-height: 1.6; }

/* 底栏永远一行。原来是 flex-wrap: wrap，注释说「窄窗口下整组一起换行」，
   但实际表现是：右组 flex-shrink:0 + margin-left:auto，一旦判定放不下就整组
   掉到第二行并且贴右，而第一行还空着一大截——看起来就是「样式溢出了」。
   现在改成不换行：左组与中组可收缩（超长就自己截断），右组固定不缩，
   按钮永远在第一行最右侧。 */
.ai-hero-bar {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: nowrap;
  margin-top: 8px;
  /* 与右侧按钮同高，避免「左组矮一截、右组高一截」的错位感。 */
  min-height: 26px;
}

/* 左组不加 overflow: hidden——AiWorktreeToggle 的分支菜单是向上弹的绝对定位元素，
   父级一裁就整块看不见。不溢出靠子项自己收缩（.ai-git-branch 带 ellipsis）。 */
.ai-hero-bar-left {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  flex: 0 1 auto;
}

/* 中组（归属空间 / 模型）：允许收缩到 0，两个下拉才不会把右组挤走。 */
.ai-hero-bar-mid {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  flex: 1 1 auto;
  overflow: hidden;
}

/* 分隔线：左边是上下文（分支 / 工作区），右边是任务属性（空间 / 模型）。
   一条 12px 的细线比再加一种背景色干净。 */
.ai-hero-bar-div {
  width: 1px;
  height: 12px;
  margin: 0 2px;
  flex-shrink: 0;
  background: var(--color-border);
}

.ai-hero-bar-right { display: flex; align-items: center; gap: 6px; margin-left: auto; flex-shrink: 0; }

/* 当前目录的 git 分支：在仓库里才出现的轻量提示 chip，放在底部工具条左侧。
   高度与 Worktree chip、权限徽标对齐（都是 22px），否则一排控件高低不齐。 */
.ai-git-branch {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 22px;
  max-width: 100%;
  padding: 0 8px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
  font-size: 11px;
  font-family: var(--font-mono);
  /* 可收缩：分支名可能是 feature/xxx-yyy 这种长串，固定不缩会把左组撑出底栏。
     收缩后由 .ai-git-branch-name 的 ellipsis 收尾。 */
  flex-shrink: 1;
  min-width: 0;
}

.ai-git-branch-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 底栏下拉（归属空间 / 模型）：做成与分支 chip、权限徽标同款的「小胶囊」。
   原来它是一套独立的表单样式——28px 高、6px 圆角、实心边框、12px 字，
   跟旁边 22px 的圆角 chip 摆在一排，高低、圆角、字重三种都不一样，
   整条底栏就显乱；而且聚焦时整块描成强调色，看着像「被选中了」。 */
select.ai-hero-select {
  box-sizing: border-box;
  display: inline-block;
  height: 22px;
  max-width: 150px;
  /* 收缩下限放到 0：默认 min-width: auto 会让 select 撑到「最长选项」的宽度，
     空间名一长就把右组整个挤到下一行。压缩后文字会被裁掉，但 title 里有全名。 */
  min-width: 0;
  flex: 0 1 auto;
  /* 右侧留 8px：原生下拉箭头画在 padding 区里，留窄了文字会顶到箭头上。 */
  padding: 0 8px;
  border: 1px solid transparent;
  border-radius: 999px;
  background: transparent;
  color: var(--color-text-muted);
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  text-overflow: ellipsis;
  transition: color var(--transition-fast), border-color var(--transition-fast),
    background-color var(--transition-fast);
}

select.ai-hero-select:hover {
  color: var(--color-text-primary);
  border-color: var(--color-border);
  background: var(--color-bg-secondary);
}

/* 聚焦只给一层很淡的底 + 边框：外层输入框已经会因为 focus-within 变成强调色，
   这里再描一遍，界面上就有两处在「发光」，反而看不出焦点在哪。 */
select.ai-hero-select:focus-visible {
  outline: none;
  color: var(--color-text-primary);
  border-color: var(--color-border-light);
  background: var(--color-bg-active);
}

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

/* 示例区头部：左标签 + 右侧「换一批」（点击重新随机一批示例）。
   与底栏同款小胶囊视觉语言，整页输入框上下风格统一。 */
.ai-hero-tips-head {
  width: 100%;
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
}

.ai-hero-tips-label {
  font-size: 11px;
  color: var(--color-text-disabled);
  flex-shrink: 0;
}

.ai-hero-tips-shuffle {
  margin-left: auto;
  padding: 3px 10px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 11px;
  font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.ai-hero-tips-shuffle:hover { color: var(--color-text-primary); border-color: var(--color-accent); }

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
