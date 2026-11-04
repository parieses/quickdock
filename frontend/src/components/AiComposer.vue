<script setup lang="ts">
/**
 * 聊天区底部输入区：钩子批准条 + 待发送图片 + 图片链接 + @ 引用 + 输入行。
 *
 * 状态全部归 AIPage（与落地页 hero 共用同一份 input / attachments / urlDraft），
 * 这里只负责渲染与把交互 emit 回去。`@` 补全弹层用 absolute 挂在输入区上沿——
 * 输入区贴着窗口底部，往下弹会被切掉；也不跟光标跑（textarea 里算光标坐标
 * 要镜像一个 div，得不偿失）。
 */
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { GitBranch, Image as ImageIcon, Link2, ArrowUp, Plus, Square } from '@lucide/vue'
import type { AIConnector, AIHooksInfo, AIMentionItem, AIPrompt, AISkill } from '../types/ai'
import type { PendingImage } from '../types'
import AiPlusPanel from './AiPlusPanel.vue'
import AiSlashPanel from './AiSlashPanel.vue'
import AiRichInput from './AiRichInput.vue'
import AiMentionBox from './AiMentionBox.vue'
import AiPermissionBadge from './AiPermissionBadge.vue'
import AiWorktreeToggle from './AiWorktreeToggle.vue'

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
  gitRepo: boolean
  gitBranch: string
  // 「技能和连接器」面板：状态归 AIPage，这里只渲染。
  plusOpen: boolean
  skills: AISkill[]
  connectors: AIConnector[]
  // 提示词库：点一下把正文填进输入框（与技能胶囊不是一回事，见 AiPlusPanel）
  prompts: AIPrompt[]
  // 输入框里已选中的技能（行内胶囊，见 AiRichInput）：只给面板标「已选」用
  pickedSkills: string[]
  // + 面板「引用」：本对话文件 / 工作目录候选 / 已引用的 path
  files: AIMentionItem[]
  cwdFiles: AIMentionItem[]
  pickedFiles: string[]
  // 「/ 选技能」补全：打字时按名字过滤候选
  slashOpen: boolean
  slashItems: AISkill[]
  slashIdx: number
  // 正文非空：只挂了技能胶囊、正文空着时发送按钮该是灰的
  canSend: boolean
  // 这条对话的执行权限：true = 完全访问（不再逐条确认）
  permissionFull: boolean
  // 隔离工作区（见 AiWorktreeToggle）：只在 git 仓库里显示
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
  (e: 'approve-hooks', on: boolean): void
  (e: 'remove-attachment', i: number): void
  (e: 'confirm-url'): void
  (e: 'remove-mention', i: number): void
  (e: 'pick-mention', it: AIMentionItem): void
  (e: 'composer-key', ev: KeyboardEvent): void
  (e: 'send'): void
  (e: 'stop'): void
  (e: 'paste', ev: ClipboardEvent): void
  (e: 'drop', ev: DragEvent): void
  (e: 'open-file-picker'): void
  (e: 'toggle-url'): void
  (e: 'toggle-plus'): void
  (e: 'pick-skill', sk: AISkill): void
  (e: 'pick-prompt', p: AIPrompt): void
  (e: 'pick-file', it: AIMentionItem): void
  (e: 'manage-prompts'): void
  (e: 'update:skills', names: string[]): void
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

// 从 + 面板点技能后要把焦点还给输入框，否则光标停在面板按钮上，
// 用户得先点一下输入框才能接着打正文。
const inputEl = ref<InstanceType<typeof AiRichInput> | null>(null)
defineExpose({
  focus: () => inputEl.value?.focus(),
  // 技能胶囊在输入框内部（行内），所以「插入技能」这件事只能由富输入框自己做
  insertSkill: (name: string) => inputEl.value?.insertSkill(name),
  // 提示词是普通正文（不是胶囊），同样只能由富输入框插到光标处
  insertPromptText: (text: string) => inputEl.value?.insertPromptText(text),
  // 最后一段非空选区（点面板时焦点已移走，只能靠输入框缓存的那份）
  takeSelectedText: () => inputEl.value?.takeSelectedText() || '',
})

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
    <!-- @ 引用（B2）：已选 chip 与补全弹层，与落地页共用同一个组件 -->
    <AiMentionBox
      :mentions="props.mentions"
      :open="props.mentionOpen"
      :items="props.mentionItems"
      :idx="props.mentionIdx"
      @pick="emit('pick-mention', $event)"
      @remove="emit('remove-mention', $event)"
    />
    <!-- 输入框本体：透明、无边框，靠外层圆角框承载容器质感（与「新建任务」输入框同款）。 -->
    <!-- 运行中**不禁用**输入：这一句会被插进正在跑的那一轮（见 ai_inject.go），
         而不是另起一轮。禁用输入等于告诉用户「现在只能看着」，而他往往正是要
         补一句刚想到的要求。 -->
    <AiRichInput
      ref="inputEl"
      :model-value="props.input"
      :placeholder="props.streaming ? t('aiInjectPlaceholder') : t('aiPlaceholder')"
      :disabled="!props.hasKey"
      :min-height="44"
      :max-height="200"
      @update:model-value="emit('update:input', $event)"
      @update:skills="emit('update:skills', $event)"
      @key="emit('composer-key', $event)"
      @paste="emit('paste', $event)"
    />
    <!-- 底部工具条：左边是「+ 技能和连接器」与当前目录的 git 分支（在仓库里才显示），
         右边是图片 / 链接 / 发送。 -->
    <div class="ai-input-bar">
      <div class="ai-input-bar-left">
        <!-- 运行中只收文字：技能要跟着一条新消息一起做上下文组装，图片要过闸门
             与落盘，两者都没法「插进已经跑起来的那一轮」。 -->
        <button
          class="ai-plus-btn"
          :class="{ 'is-open': props.plusOpen }"
          :disabled="props.streaming"
          :title="t('aiPlusTip')"
          @click="emit('toggle-plus')"
        >
          <Plus :size="16" />
        </button>
        <span v-if="props.gitRepo" class="ai-git-branch" :title="t('aiGitBranchTip')">
          <GitBranch :size="12" />
          <span class="ai-git-branch-name">{{ props.gitBranch }}</span>
        </span>
        <!-- 隔离工作区：勾上后这条任务在独立 git worktree 里干活，原目录不动 -->
        <AiWorktreeToggle
          :git-repo="props.gitRepo"
          :active="props.worktreeActive"
          :path="props.worktreePath"
          :branch="props.worktreeBranch"
          :enabled="props.worktreeEnabled"
          :branches="props.worktreeBranches"
          :branch-open="props.worktreeBranchOpen"
          :disabled="props.streaming"
          @update:enabled="emit('update:worktreeEnabled', $event)"
          @toggle-branch="emit('toggle-worktree-branch')"
          @pick-branch="emit('pick-worktree-branch', $event)"
          @remove="emit('remove-worktree')"
        />
      </div>
      <div class="ai-input-bar-right">
        <!-- 当前权限：默认权限会逐个问，完全访问直接做。生成中不给切——
             这一轮已经按旧口径在跑，切了也不生效，只会让人以为切了。 -->
        <AiPermissionBadge
          :full="props.permissionFull"
          :disabled="props.streaming"
          @change="emit('change-permission', $event)"
        />
        <button v-if="props.allowFileImage" class="ai-tool-btn" :disabled="props.streaming" :title="t('aiAttachImage')" @click="emit('open-file-picker')">
          <ImageIcon :size="16" />
        </button>
        <button v-if="props.allowUrlImage" class="ai-tool-btn" :disabled="props.streaming" :title="t('aiAddImageUrl')" @click="emit('toggle-url')">
          <Link2 :size="16" />
        </button>
        <!-- 生成中：红色的停止按钮 + （有字时）一枚插入按钮。
             两个按钮并排而不是把发送换成停止：以前只有停止，用户想补一句就得先
             打断这一轮再说——而他往往并不想打断，只是想让它知道。 -->
        <template v-if="props.streaming">
          <button
            v-if="props.canSend"
            class="ai-send ai-send-inject"
            :title="t('aiInjectTip')"
            @click="emit('send')"
          >
            <ArrowUp :size="16" />
          </button>
          <button class="ai-send ai-send-stop" :title="t('aiStop')" @click="emit('stop')">
            <Square :size="15" />
          </button>
        </template>
        <button
          v-else
          class="ai-send"
          :disabled="!props.hasKey || (!props.canSend && !props.attachments.length)"
          :title="t('send')"
          @click="emit('send')"
        >
          <ArrowUp :size="16" />
        </button>
      </div>
    </div>
    <!-- 「技能和连接器」面板：内容与「新建任务」那个输入框共用一份组件。 -->
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
</template>

<style scoped>
.ai-input-area {
  display: flex;
  flex-direction: column;
  gap: 8px;
  /* 单圆角框容器：去顶边框、改为整框描边 + 圆角 + 浅底，输入框本体透明无边框，
     整体质感与落地页「新建任务」输入框一致（参考截图）。 */
  padding: 10px 12px 8px;
  /* 与落地页 hero 的输入框同一个宽度（--ai-column-w，见 style.css）；
     原来是不带上限的 80%，大屏上比 hero 还宽，切页面时会跳一下。 */
  width: var(--ai-column-w);
  margin: 0 auto 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-bg-tertiary);
  flex-shrink: 0;
  /* @ 补全弹层的定位锚点：它要贴着输入区上沿向上展开，
     而不是跟着光标跑（textarea 里算光标坐标要镜像一个 div，得不偿失）。 */
  position: relative;
  transition: border-color var(--transition-fast);
}

.ai-input-area:focus-within { border-color: var(--color-accent); }

.ai-input-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 2px;
}

.ai-input-bar-left { display: flex; align-items: center; gap: 8px; min-width: 0; }

/* 左下角的 +：与右侧图片/链接同尺寸的图标按钮，只多一个「打开中」的底色。
   它承载「技能和连接器」——不写成文字按钮是因为这里已经有 git 分支 chip，
   再塞一段文字会把发送按钮挤到换行。 */
.ai-plus-btn {
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

.ai-plus-btn:hover,
.ai-plus-btn.is-open { background: var(--color-bg-active); color: var(--color-text-primary); }

.ai-input-bar-right { display: flex; align-items: center; gap: 6px; margin-left: auto; flex-shrink: 0; }

/* 当前目录的 git 分支：在仓库里才出现的轻量提示 chip，放在底部工具条左上角。
   高度与 Worktree chip、权限徽标对齐（都是 22px）——按 padding 撑开的话，字号
   一变高度就跟着变，一排控件的上沿就开始参差。 */
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
}

.ai-git-branch-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ai-hook-banner {
  margin: 0;
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

/* 发送 / 停止按钮：与「新建任务」同款的圆形图标按钮（只显示图标，不显示文字）。
   32px 正圆、accent 填充、白色图标；禁用态沿用 AiHero 的灰底淡字。 */
.ai-send {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 32px;
  height: 32px;
  border: none;
  border-radius: 50%;
  background: var(--color-accent);
  color: #fff;
  cursor: pointer;
  flex-shrink: 0;
  transition: opacity var(--transition-fast), background-color var(--transition-fast);
}

.ai-send:disabled { background: var(--color-bg-active); color: var(--color-text-disabled); cursor: default; }

/* 插入按钮：与发送同色同形，靠颜色与右边的红色停止按钮区分（蓝=继续说，红=停）。
   不要再描一圈「底色」环来分隔：环的颜色必须等于输入区的底色，而输入区底色
   换主题会变，写死的环在另一套主题下就是一圈错色的光晕。 */

.ai-plus-btn:disabled { opacity: 0.4; cursor: default; }
.ai-plus-btn:disabled:hover { background: transparent; color: var(--color-text-muted); }

/* 生成中：圆形发送按钮原地翻成红色停止按钮，承载「中止」语义，一眼可辨。 */
.ai-send-stop { background: var(--color-danger); color: #fff; }
.ai-send-stop:hover { background: color-mix(in srgb, var(--color-danger) 85%, #000); }
</style>
