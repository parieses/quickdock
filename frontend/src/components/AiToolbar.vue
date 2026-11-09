<script setup lang="ts">
/**
 * 聊天区顶部工具栏：目录指示 + 几个会话级动作。
 *
 * 只在「有任务」时由父组件挂载。这里是纯视图，所有状态归 AIPage，动作通过 emit 回传。
 *
 * 模型选择已移到输入框底部：它现在是「这条任务用哪个模型」的属性，
 * 和输入框里的技能、权限、工作区摆在一起，而不是一个全局开关藏在右上角。
 */
import { useI18n } from 'vue-i18n'
import { Download, FolderOpen, ListTree, PanelRight, RefreshCw, Star, Terminal } from '@lucide/vue'
import type { AIConversation } from '../types/ai'

const props = defineProps<{
  /** 当前会话绑定的工作目录，空串表示没绑 */
  convCwd: string
  /** 目录显示名（尾部几段，见父组件 cwdLabel） */
  cwdLabel: string
  activeId: string
  activeConv: AIConversation | null
  /** 正在切换星标的会话 id，用来禁用按钮防连点 */
  starBusy: string
  digestOpen: boolean
  sideOpen: boolean
  /** 后台任务面板是否打开（按钮高亮用） */
  jobsOpen: boolean
  /** 运行中的后台任务数：只用来在按钮上点一个小圆点，为 0 时不显示 */
  runningJobs: number
}>()

const emit = defineEmits<{
  (e: 'update:digestOpen', v: boolean): void
  (e: 'open-cwd'): void
  (e: 'regen-title'): void
  (e: 'toggle-star', c: AIConversation): void
  (e: 'export-md'): void
  (e: 'toggle-side'): void
  (e: 'toggle-jobs'): void
}>()

const { t } = useI18n()
</script>

<template>
  <div class="ai-toolbar">
    <!-- 目录指示：绑了目录才允许 AI 改文件、跑命令（见后端 writeRoots），
         点一下在资源管理器里打开——产物的目录在数据目录下（普通任务）或项目里（空间任务），
         不给入口的话用户只能从 title 里抄路径自己去翻。
         它只负责「打开」当前已绑定的目录，不是「选目录」（绑定入口在新建任务时）。 -->
    <button v-if="props.convCwd" class="ai-cwd-tag" :title="t('aiCwdTip', { dir: props.convCwd })" @click="emit('open-cwd')">
      <FolderOpen :size="12" />
      <span class="ai-cwd-name">{{ props.cwdLabel }}</span>
    </button>
    <div class="ai-toolbar-right">
      <button v-if="props.activeId" class="ai-icon-btn" :title="t('aiRegenTitle')" @click="emit('regen-title')">
        <RefreshCw :size="13" />
      </button>
      <button
        v-if="props.activeId"
        class="ai-icon-btn"
        :class="{ 'is-active': !!props.activeConv?.starred }"
        :title="t(props.activeConv?.starred ? 'aiStarOff' : 'aiStarOn')"
        :disabled="props.starBusy === props.activeId"
        @click="props.activeConv && emit('toggle-star', props.activeConv)"
      >
        <Star :size="13" :fill="props.activeConv?.starred ? 'currentColor' : 'none'" />
      </button>
      <button
        v-if="props.activeId"
        class="ai-icon-btn"
        :class="{ 'is-active': props.digestOpen }"
        :title="t('aiOutline')"
        @click="emit('update:digestOpen', !props.digestOpen)"
      >
        <ListTree :size="13" />
      </button>
      <!-- 后台任务：只要有任务在跑就一直亮着，和当前会话无关——
           这类进程是宿主级的，用户在别处聊天时也该知道它还在跑。 -->
      <button
        class="ai-icon-btn ai-jobs-btn"
        :class="{ 'is-active': props.jobsOpen }"
        :title="t('aiJobsTitle')"
        @click="emit('toggle-jobs')"
      >
        <Terminal :size="13" />
        <span v-if="props.runningJobs" class="ai-jobs-badge">{{ props.runningJobs }}</span>
      </button>
      <button v-if="props.activeId" class="ai-icon-btn" :title="t('aiExportMd')" @click="emit('export-md')">
        <Download :size="13" />
      </button>
      <!-- 右侧面板开关：常驻最右（与常见 AI 工作台同位同快捷键） -->
      <button class="ai-icon-btn" :class="{ 'is-active': props.sideOpen }" :title="t(props.sideOpen ? 'aiSideClose' : 'aiSideOpen')" @click="emit('toggle-side')">
        <PanelRight :size="13" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.ai-toolbar {
  display: flex;
  align-items: center;
  /* 右侧操作组靠右；左侧若有目录指示，由它的 margin-right:auto 顶到左边。
     原来是 space-between + 左右两块都常驻，现在左侧那块只在绑了目录时才存在，
     用 space-between 会在没有它时把右侧整组推到最左。 */
  justify-content: flex-end;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  gap: 10px;
}

.ai-toolbar-right { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }

/* 工作目录：显示当前绑到哪个目录，点一下在资源管理器里打开。
   它只「打开」已绑定的目录，不是「选目录」——绑定入口已收进「新建任务 → 选空间」。
   保留它是因为它同时回答两件事：AI 能不能改文件，以及生成的文件去哪了。
   宽度上限必须有：项目路径动辄五六层，不夹住会把模型下拉挤到换行。 */
.ai-cwd-tag {
  display: inline-flex; align-items: center; gap: 5px;
  margin-right: auto;
  max-width: 40%; min-width: 0;
  padding: 4px 6px;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-disabled);
  font-size: 12px; font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.ai-cwd-tag:hover { color: var(--color-text-primary); border-color: var(--color-border); }

.ai-cwd-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.ai-icon-btn {
  display: flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border: 1px solid var(--color-border);
  background: transparent; color: var(--color-text-muted);
  border-radius: var(--radius-md); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}

.ai-icon-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
/* 激活态：星标已收藏 / 大纲已展开 / 右侧面板已打开。
   模板一直在切 is-active，但这条规则此前从未写过——三个按钮的「已开启」看不出来。 */
.ai-icon-btn.is-active { color: var(--color-accent); border-color: var(--color-accent); background: var(--color-accent-bg); }
.ai-icon-btn:disabled { opacity: 0.5; cursor: not-allowed; }

/* 后台任务按钮：运行中数量做成右上角的小徽标。
   不用「(2)」写在图标旁边——那会让按钮变宽，工具栏右组在窄窗口下要换行。 */
.ai-jobs-btn { position: relative; }

.ai-jobs-badge {
  position: absolute;
  top: -4px;
  right: -4px;
  min-width: 14px;
  height: 14px;
  padding: 0 3px;
  border-radius: var(--radius-full);
  background: var(--color-accent);
  color: var(--color-bg-primary);
  font-size: 10px;
  line-height: 14px;
  text-align: center;
  font-variant-numeric: tabular-nums;
  pointer-events: none;
}
</style>
