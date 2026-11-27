<script setup lang="ts">
/**
 * 空态落地页：新建任务不走弹窗，直接在这一页选空间、提问。默认就是普通对话，
 * 界面上没有任何模式/类型要先选。
 * 类型与空间仍是这条任务的属性，首次发送时随请求落库。
 *
 * 这里的输入框与聊天区输入区共用同一份 `input` / `attachments` / `urlDraft` 状态
 * （都在 AIPage），所以两边能无缝接上：在落地页贴了一张图，发出去之后
 * 聊天区那排缩略图显示的是同一张。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import type { Component } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  AlarmClock, AlertCircle, ArrowUp, Braces, CheckCircle2, ChevronDown, ClipboardList, Code,
  FileText, Folder, FolderOpen, History, Image as ImageIcon, Info, Languages, Link2, ListTree,
  Plus, Sparkles, Wrench, X,
} from '@lucide/vue'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AIConnector, AIMentionItem, AIPrompt, AISkill, AISpace } from '../types/ai'
import type { PendingImage } from '../types'
import type { AttachedFile } from '../composables/useAiAttachedFiles'
import AiAttachedFileChips from './AiAttachedFileChips.vue'
import AiPlusPanel from './AiPlusPanel.vue'
import AiSlashPanel from './AiSlashPanel.vue'
import AiRichInput from './AiRichInput.vue'
import AiMentionBox from './AiMentionBox.vue'
import AiPermissionBadge from './AiPermissionBadge.vue'
import AiWorktreeToggle from './AiWorktreeToggle.vue'
// 发送按钮 / 分支 chip / 模型胶囊与聊天区共用同一份实现（盒模型与禁用态只写一次）
import AiSendButton from './AiSendButton.vue'
import AiGitBranchChip from './AiGitBranchChip.vue'
import AiModelSelect from './AiModelSelect.vue'

const props = defineProps<{
  urlInputOpen: boolean
  urlDraft: string
  attachments: PendingImage[]
  input: string
  /** @ 引用：已选 chip 与补全弹层（与聊天输入框共用同一份状态） */
  mentions: AIMentionItem[]
  /** 插入的本地文件（后端按后缀解析成文本，与聊天输入框共用同一份状态） */
  attachedFiles: AttachedFile[]
  mentionOpen: boolean
  mentionItems: AIMentionItem[]
  mentionIdx: number
  hasKey: boolean
  newTaskSpace: string
  /** 普通任务选定的工作目录（空 = 由后端自动分配 ai_tasks/<id>） */
  newTaskDir: string
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
  /** 执行权限三档：'' 默认 / 'readonly' 只读 / 'full' 完全访问。新建任务默认档，在这里切的第一句就生效 */
  permissionLevel: string
  /** 隔离工作区（见 AiWorktreeToggle）：只在 git 仓库里显示 */
  worktreeActive: boolean
  worktreePath: string
  worktreeBranch: string
  worktreeEnabled: boolean
  worktreeBranches: string[]
  worktreeBranchOpen: boolean
}>()

const emit = defineEmits<{
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
  (e: 'remove-attached-file', i: number): void
  (e: 'pick-local-file'): void
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
  (e: 'change-permission', level: string): void
  (e: 'update:worktreeEnabled', v: boolean): void
  (e: 'toggle-worktree-branch'): void
  (e: 'pick-worktree-branch', name: string): void
  (e: 'remove-worktree'): void
}>()

const { t } = useI18n()

/**
 * 示例提示词的代表图标：按 i18n key 固定映射。
 * 文案由 i18n 决定，不能从渲染后的文字里猜图标；没列到的 key 用 Sparkles 兜底。
 */
const TIP_ICONS: Record<string, Component> = {
  aiHeroTip1: FileText,
  aiHeroTip2: AlertCircle,
  aiHeroTip3: ListTree,
  aiHeroTip4: Braces,
  aiHeroTip5: History,
  aiHeroTip6: CheckCircle2,
  aiHeroTip7: Wrench,
  aiHeroTip8: ClipboardList,
  aiHeroTip9: Code,
  aiHeroTip10: Languages,
}

// 落地页输入框：父组件在「新建任务 / 填示例」时要把焦点放回来，所以对外暴露 focus()；
// 技能胶囊是输入框内部的行内节点，插入也只能由富输入框自己完成。
const heroInputEl = ref<InstanceType<typeof AiRichInput> | null>(null)
defineExpose({
  focus: () => heroInputEl.value?.focus(),
  insertSkill: (name: string) => heroInputEl.value?.insertSkill(name),
  insertPromptText: (text: string) => heroInputEl.value?.insertPromptText(text),
  takeSelectedText: () => heroInputEl.value?.takeSelectedText() || '',
})

// ---- 归属空间下拉 ----
//
// 原来是原生 <select>：选项里塞不进图标，而侧栏空间列表已经用「闹钟 = 定时任务 / 目录 = 普通空间」
// 区分了，两处不一致会让人觉得不是同一个东西。换成自建下拉。
const spacePickOpen = ref(false)
const spacePickEl = ref<HTMLElement | null>(null)
const curSpace = computed(() => props.spaces.find((s) => s.id === props.newTaskSpace) || null)
const curSpaceLabel = computed(() => {
  if (props.newTaskSpace === props.newSpace) return t('aiNewTaskSpaceNew')
  return curSpace.value?.name || t('aiNewTaskSpaceNone')
})

/**
 * 归属空间下拉里能选的空间。
 *
 * 滤掉两类专属空间：
 *   - SSH 助手（kind='ssh'）：它是连接史的归档，没有目录；在这里选中它建出来的
 *     任务既不在终端里、也不知道对应哪台机器，纯粹是误操作入口。
 *   - 定时任务空间（kind='schedule' / 带 task_id）：一个任务固定一条会话，
 *     用户在这里「新建任务」只会往那个分组里塞一条与任务无关的对话。
 * 侧栏照常显示它们——那里是回看，不是新建。
 */
const pickableSpaces = computed(() =>
  props.spaces.filter((s) => s.kind !== 'ssh' && !isSchedSpace(s)),
)

// kind 是升级时补的列，老库迁移没跑完时是空串——用 task_id 兜一下，
// 否则同一类空间升级前后会在闹钟与目录之间跳（侧栏也是这个判断，见 AiConversationSidebar）。
function isSchedSpace(s: AISpace): boolean {
  return s.kind === 'schedule' || !!s.task_id
}

function pickSpace(v: string) {
  spacePickOpen.value = false
  emit('update:newTaskSpace', v)
}

/** 选工作目录 = 建空间任务：目录是空间的唯一键（后端按目录幂等复用）。 */
function pickPlainDir() {
  spacePickOpen.value = false
  emit('update:newTaskSpace', props.newSpace)
  emit('pick-dir')
}

function onSpaceDocDown(e: MouseEvent) {
  if (!spacePickOpen.value) return
  const el = spacePickEl.value
  if (el && e.target instanceof Node && el.contains(e.target)) return
  spacePickOpen.value = false
}
// Esc 关闭：这条底栏在落地页是主入口，键盘用户按 Esc 应当能退出菜单
function onSpaceDocKey(e: KeyboardEvent) {
  if (e.key === 'Escape') spacePickOpen.value = false
}
onMounted(() => {
  document.addEventListener('mousedown', onSpaceDocDown)
  document.addEventListener('keydown', onSpaceDocKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('mousedown', onSpaceDocDown)
  document.removeEventListener('keydown', onSpaceDocKey)
})

</script>

<template>
  <div class="ai-hero">
    <div class="ai-hero-inner">
      <h1 class="ai-hero-title">{{ t('aiHeroTitle') }}</h1>

      <!-- 标题下面直接是输入框：落地页不再摆模式/快捷提示词那排 pill。
           默认就是普通对话，话术去输入框左侧的 + 面板取（提示词库）。 -->
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
          <div v-for="(a, i) in props.attachments" :key="a.id" class="ai-attach-thumb">
            <img v-if="a.kind === 'file' && a.dataUrl" :src="a.dataUrl" class="ai-attach-img" />
            <img v-else-if="a.kind === 'url'" :src="a.url" class="ai-attach-img" />
            <button class="ai-attach-del" :title="t('delete')" @click="emit('remove-attachment', i)"><X :size="11" /></button>
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
        <!-- 插入的本地文件：发送时由后端按后缀解析成文本展开 -->
        <AiAttachedFileChips
          :files="props.attachedFiles"
          @remove="emit('remove-attached-file', $event)"
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
            <AiGitBranchChip v-if="props.gitRepo" :branch="props.gitBranch" :tip="t('aiGitBranchTip')" />
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
            <div ref="spacePickEl" class="ai-space-pick">
              <button
                type="button"
                class="ai-hero-select ai-space-pick-btn"
                :class="{ 'is-open': spacePickOpen }"
                :title="t('aiNewTaskSpace')"
                @click="spacePickOpen = !spacePickOpen"
              >
                <AlarmClock v-if="curSpace && isSchedSpace(curSpace)" :size="12" class="ai-space-pick-ico" />
                <Folder v-else-if="curSpace" :size="12" class="ai-space-pick-ico" />
                <span class="ai-space-pick-label">{{ curSpaceLabel }}</span>
                <ChevronDown :size="11" class="ai-space-pick-caret" />
              </button>
              <div v-if="spacePickOpen" class="ai-space-pick-menu">
                <button
                  type="button"
                  class="ai-space-pick-item"
                  :class="{ 'is-sel': !props.newTaskSpace }"
                  @click="pickSpace('')"
                >
                  <span class="ai-space-pick-ico"></span>
                  <span class="ai-space-pick-name">{{ t('aiNewTaskSpaceNone') }}</span>
                </button>
                <button
                  v-for="s in pickableSpaces"
                  :key="s.id"
                  type="button"
                  class="ai-space-pick-item"
                  :class="{ 'is-sel': s.id === props.newTaskSpace }"
                  :title="s.cwd || s.name"
                  @click="pickSpace(s.id)"
                >
                  <span class="ai-space-pick-ico">
                    <AlarmClock v-if="isSchedSpace(s)" :size="12" />
                    <Folder v-else :size="12" />
                  </span>
                  <span class="ai-space-pick-name">{{ s.name }}</span>
                </button>
                <div class="ai-space-pick-sep"></div>
                <!-- 选目录 = 建空间。以前这里另有「普通任务也能指定目录」一项，
                     但它和「新建空间…」挨在同一菜单里，用户点了前者选完目录，
                     任务却落在侧栏「普通任务」组——反馈就是「选了目录没建成空间任务」。
                     合成一个入口：目录就是空间的唯一键，不存在「有目录但不建空间」这回事。 -->
                <button
                  type="button"
                  class="ai-space-pick-item"
                  :class="{ 'is-sel': props.newTaskSpace === props.newSpace }"
                  :title="props.newTaskDir || t('aiNewTaskPickDirHint')"
                  @click="pickPlainDir"
                >
                  <span class="ai-space-pick-ico"><FolderOpen :size="12" /></span>
                  <span class="ai-space-pick-name">{{ t('aiNewTaskPickDir') }}</span>
                  <span v-if="props.newTaskDir" class="ai-space-pick-cwd">{{ t('aiNewTaskDirPicked') }}</span>
                </button>
              </div>
            </div>
            <!-- 没有可用模型（还没配 key）时不给这个空下拉：一个没有选项的胶囊
                 只会让人以为点它就能选。此时下方的「去设置」才是该走的路。 -->
            <AiModelSelect
              v-if="props.aiProfiles.length"
              variant="ghost"
              :profiles="props.aiProfiles"
              :active="props.aiActive"
              :title="t('aiSwitchModel')"
              @update:active="emit('update:aiActive', $event)"
              @model-change="emit('model-change')"
            />
          </div>
          <div class="ai-hero-bar-right">
            <!-- 当前权限：落地页也能切，第一句就按这个口径执行 -->
            <AiPermissionBadge :level="props.permissionLevel" @change="emit('change-permission', $event)" />
            <button v-if="props.allowFileImage" class="ai-tool-btn qd-btn" :title="t('aiAttachImage')" @click="emit('open-file-picker')">
              <ImageIcon :size="17" />
            </button>
            <button v-if="props.allowUrlImage" class="ai-tool-btn qd-btn" :title="t('aiAddImageUrl')" @click="emit('toggle-url')">
              <Link2 :size="17" />
            </button>
            <AiSendButton
              :disabled="!props.hasKey || (!props.canSend && !props.attachments.length && !props.attachedFiles.length)"
              :title="t('send')"
              @click="emit('send')"
            >
              <ArrowUp :size="18" />
            </AiSendButton>
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
          @pick-local-file="emit('pick-local-file')"
          @toggle-connector="emit('toggle-connector', $event)"
          @install-skill="emit('install-skill')"
          @manage-connectors="emit('manage-connectors')"
          @manage-prompts="emit('manage-prompts')"
        />
      </div>

      <!-- 产物去向：既是说明也是「空间是什么」的解释——比一个空下拉好懂。
           前面挂一枚 ⓘ，避免它被当成普通灰色小字略过。 -->
      <p class="ai-hero-out">
        <Info :size="12" class="ai-hero-out-ico" />
        <span>{{ props.outHint }}</span>
      </p>

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
          <component :is="TIP_ICONS[k] || Sparkles" :size="14" class="ai-hero-tip-ico" />
          <span class="ai-hero-tip-text">{{ t(k) }}</span>
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
  margin: 0 0 var(--space-2);
  font-size: var(--text-2xl);
  font-weight: 600;
  color: var(--color-text-primary);
}

/* 落地页曾有一排模式 pill（对话 / 帮助），默认就是对话、话术全在提示词库里，
   所以整排连同它的 pill 样式一起删掉了 —— 标题下面直接是输入框。 */
.ai-hero-box {
  width: 100%;
  /* 底色与页面只差两档，1px 用 border 而不是 border-light 时边界几乎看不见 */
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-lg);
  background: var(--color-bg-tertiary);
  padding: 10px var(--space-5) var(--space-4);
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
  color: var(--color-text-secondary);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}

.ai-hero-plus:hover,
.ai-hero-plus.is-open { background: var(--color-bg-active); color: var(--color-text-primary); }

/* 聚焦时整圈换成强调蓝：输入框是这一页唯一的主操作，边框是它最强的一处提示。
   只换 1px 边框色、不加外环——这么大一个盒子加 glow 会盖住页面里其他强调元素。 */
.ai-hero-box:focus-within {
  border-color: var(--color-accent);
}

/* 富输入框（AiRichInput）在落地页用大一号的字，与原来那个 textarea 一致 */
.ai-hero-box :deep(.ai-rich) { font-size: var(--text-base); line-height: 1.6; }

/* 底栏永远一行。原来是 flex-wrap: wrap，注释说「窄窗口下整组一起换行」，
   但实际表现是：右组 flex-shrink:0 + margin-left:auto，一旦判定放不下就整组
   掉到第二行并且贴右，而第一行还空着一大截——看起来就是「样式溢出了」。
   现在改成不换行：左组与中组可收缩（超长就自己截断），右组固定不缩，
   按钮永远在第一行最右侧。 */
.ai-hero-bar {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: nowrap;
  margin-top: var(--space-4);
  /* 与右侧按钮同高，避免「左组矮一截、右组高一截」的错位感。 */
  min-height: 26px;
}

/* 左组不加 overflow: hidden——AiWorktreeToggle 的分支菜单是向上弹的绝对定位元素，
   父级一裁就整块看不见。不溢出靠子项自己收缩（AiGitBranchChip 带 ellipsis）。 */
.ai-hero-bar-left {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
  flex: 0 1 auto;
}

/* 中组（归属空间 / 模型）：允许收缩到 0，两个下拉才不会把右组挤走。
   不能加 overflow: hidden——空间下拉是向上弹的绝对定位菜单，父级一裁
   整块菜单就看不见：点了「普通任务」胶囊，界面上像是什么都没发生，
   「选择目录 / 新建空间…」两个入口全被吞掉。和左组同理：不溢出靠
   两个子项自己的 min-width:0 + flex:0 1 auto 收缩，文字超长由 ellipsis 收尾。 */
.ai-hero-bar-mid {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
  flex: 1 1 auto;
}

/* 分隔线：左边是上下文（分支 / 工作区），右边是任务属性（空间 / 模型）。
   一条 12px 的细线比再加一种背景色干净。 */
.ai-hero-bar-div {
  width: 1px;
  height: 12px;
  margin: 0 var(--space-1);
  flex-shrink: 0;
  background: var(--color-border);
}

.ai-hero-bar-right { display: flex; align-items: center; gap: var(--space-3); margin-left: auto; flex-shrink: 0; }

/* 归属空间下拉的触发按钮：模型胶囊已抽到 AiModelSelect，这里只服务空间选择。
   做成与分支 chip、权限徽标同款的「小胶囊」，与原来那套 28px/6px 圆角的
   表单样式拉开——那套和旁边 22px 圆角 chip 摆一排时高低、圆角、字重全不齐。 */
.ai-hero-select {
  box-sizing: border-box;
  display: inline-block;
  height: 22px;
  max-width: 150px;
  /* 收缩下限放到 0：默认 min-width: auto 会让 select 撑到「最长选项」的宽度，
     空间名一长就把右组整个挤到下一行。压缩后文字会被裁掉，但 title 里有全名。 */
  min-width: 0;
  flex: 0 1 auto;
  /* 右侧留 8px：原生下拉箭头画在 padding 区里，留窄了文字会顶到箭头上。 */
  padding: 0 var(--space-4);
  border: 1px solid transparent;
  border-radius: var(--radius-full);
  background: transparent;
  /* 与模型胶囊、权限徽标同档：底栏一排都用 secondary，muted 在深底上读不出来 */
  color: var(--color-text-secondary);
  font-size: var(--text-xs);
  font-family: inherit;
  cursor: pointer;
  text-overflow: ellipsis;
  transition: color var(--transition-fast), border-color var(--transition-fast),
    background-color var(--transition-fast);
}

.ai-hero-select:hover {
  color: var(--color-text-primary);
  border-color: var(--color-border);
  background: var(--color-bg-secondary);
}

/* ---- 归属空间下拉 ---- */
.ai-space-pick {
  position: relative;
  display: inline-flex;
  min-width: 0;
  flex: 0 1 auto;
}

/* 按钮版胶囊：内容用 flex 排，文字过长时靠中间的 label 省略，
   不能让 max-width 落在整个 button 上——那样箭头会被裁掉 */
.ai-space-pick-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  max-width: 170px;
  overflow: hidden;
}

.ai-space-pick-label {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ai-space-pick-caret { flex-shrink: 0; opacity: 0.6; }
.ai-space-pick-btn.is-open {
  color: var(--color-text-primary);
  border-color: var(--color-border);
  background: var(--color-bg-active);
}

/* 向上弹：与旁边的分支下拉（AiWorktreeToggle）一致，往下会盖住输入区 */
.ai-space-pick-menu {
  position: absolute;
  left: 0;
  bottom: calc(100% + 6px);
  min-width: 180px;
  max-width: 300px;
  max-height: 260px;
  overflow: auto;
  padding: var(--space-2);
  background: var(--color-popover-bg);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  /* 用 md 而不是 lg：这是贴着输入框的小下拉，lg 的 40px 大扩散会把旁边那排
     按钮和输入框边缘一起压暗，看着像浮层把底下挖了个洞。 */
  box-shadow: var(--shadow-md);
  z-index: 40;
}

.ai-space-pick-item {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  width: 100%;
  padding: 5px var(--space-4);
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--text-sm);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
}
.ai-space-pick-item:hover { background: var(--color-bg-tertiary); color: var(--color-text-primary); }
.ai-space-pick-item.is-sel { color: var(--color-accent); }
.ai-space-pick-ico { display: inline-flex; flex-shrink: 0; width: 12px; opacity: 0.8; }
.ai-space-pick-name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 「已选目录」小标记：不写全路径（下拉窄），路径在 title 里 */
.ai-space-pick-cwd { margin-left: auto; flex: none; font-size: var(--text-2xs); color: var(--color-accent); }
.ai-space-pick-sep { height: 1px; margin: var(--space-2) var(--space-3); background: var(--color-border); }

.ai-hero-out {
  margin: 0;
  /* 产物去向是「文件写到哪里」的关键信息：左对齐贴输入框左边缘，
     不跟着 .ai-hero-inner 的 align-items:center 缩成一小撮居中小字。 */
  align-self: stretch;
  display: flex;
  align-items: flex-start;
  gap: var(--space-2);
  /* 与输入框拉开一点：紧贴下沿时它像是输入框的一部分，容易被当成占位说明略过 */
  margin-top: var(--space-1);
  font-size: var(--text-sm);
  color: var(--color-text-secondary);
  text-align: left;
  line-height: 1.5;
  word-break: break-all;
}

.ai-hero-out-ico { flex: none; margin-top: 2px; color: var(--color-text-muted); }

/* 缺 API Key：橙底 + 橙边 + 橙字 + 橙实心按钮，四层同色叠在深底上像贴了块
   膏药，把落地页的视觉重心从输入框拽走。降饱和后只留一颗 6px 语义点，
   容器和按钮都回到中性面层 —— 提示仍在，但不再和主操作抢注意力。 */
.ai-hero-needkey {
  margin: 0;
  display: inline-flex; align-items: center; gap: var(--space-4);
  align-self: center;
  padding: 3px var(--space-2) 3px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: var(--color-bg-tertiary);
  font-size: var(--text-sm);
  line-height: 1.5;
  color: var(--color-text-secondary);
}
.ai-hero-needkey::before {
  content: '';
  flex-shrink: 0;
  width: 6px; height: 6px;
  border-radius: var(--radius-full);
  background: var(--color-warning);
}

.ai-hero-needkey .ai-go-settings {
  padding: 3px 11px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
  font-family: inherit;
  font-size: var(--text-xs);
  font-weight: 600;
  line-height: 1.6;
  cursor: pointer;
  transition: background-color var(--transition-fast);
}
.ai-hero-needkey .ai-go-settings:hover { background: var(--color-bg-active); }

/* 两列卡片：胶囊状看着像分类标签，用户不会想到点一下就能执行；
   卡片自带代表图标 + 右上斜箭头（= 填入输入框），悬停上浮 1px。 */
.ai-hero-tips {
  width: 100%;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: var(--space-3);
  margin-top: var(--space-2);
}

/* 示例区头部：左标签 + 右侧「换一批」（点击重新随机一批示例）。
   与底栏同款小胶囊视觉语言，整页输入框上下风格统一。 */
.ai-hero-tips-head {
  width: 100%;
  display: flex;
  align-items: center;
  gap: var(--space-4);
  margin-top: var(--space-1);
}

.ai-hero-tips-label {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  flex-shrink: 0;
}

.ai-hero-tips-shuffle {
  margin-left: auto;
  padding: 3px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: var(--text-xs);
  font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.ai-hero-tips-shuffle:hover { color: var(--color-text-primary); border-color: var(--color-accent); }

.ai-hero-tip {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  min-width: 0;
  padding: 9px var(--space-4);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  font-size: var(--text-sm); font-family: inherit; text-align: left;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast),
    background-color var(--transition-fast), transform var(--transition-fast);
}

.ai-hero-tip:hover {
  color: var(--color-text-primary);
  border-color: var(--color-accent);
  background: var(--color-bg-hover);
  transform: translateY(-1px);
}

.ai-hero-tip-ico { flex: none; color: var(--color-text-muted); }
.ai-hero-tip:hover .ai-hero-tip-ico { color: var(--color-accent); }
/* 卡片宽度固定，长文案单行省略：两列等高才不会一条高一条矮 */
.ai-hero-tip-text { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ai-hero-tip-icon { flex-shrink: 0; transform: rotate(45deg); opacity: 0.55; }
.ai-hero-tip:hover .ai-hero-tip-icon { opacity: 1; }
</style>
