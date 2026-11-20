<script setup lang="ts">
/**
 * 聊天区顶部工具栏：目录指示 + 轨迹入口 + 几个会话级动作。
 *
 * 父级 AIPage 用 v-if="!isNewTask" 控制：落地页不渲染，进入对话后才出现——
 * 没进对话时没有 run 可看，轨迹入口挂在那里只会点出空抽屉。
 * 所以组件本身不存在「落地页」态，早先那个 newTask prop 恒为 false、纯属死代码，已删。
 *
 * 这里是纯视图，所有状态归 AIPage，动作通过 emit 回传。
 *
 * 模型选择已移到输入框底部：它现在是「这条任务用哪个模型」的属性，
 * 和输入框里的技能、权限、工作区摆在一起，而不是一个全局开关藏在右上角。
 */
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Activity, Download, Eraser, FolderOpen, ListTree, PanelRight, RefreshCw, Star, Terminal, Users } from '@lucide/vue'
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
  /** 最近起的那个运行中任务的开始时间，用于在按钮上显示已运行时长 */
  runningJobStartedAt: string
  /** 轨迹入口文案（父级按运行态算好，如「轨迹」/「12 步 · 3m20s」） */
  traceLabel: string
  /** 轨迹入口状态：决定圆点颜色与是否脉冲 */
  traceState: 'empty' | 'running' | 'done'
  /** 整段会话派出去的子 agent 总数，0 时不显示徽标 */
  childTaskCount: number
  /** 有没有工作目录可以清理辅助文件（没有就不显示那个按钮） */
  canCleanScratch: boolean
  /** 清理按钮是否处在「再点一次确认」的状态 */
  scratchArmed: boolean
}>()

const emit = defineEmits<{
  (e: 'update:digestOpen', v: boolean): void
  (e: 'open-cwd'): void
  (e: 'regen-title'): void
  (e: 'toggle-star', c: AIConversation): void
  (e: 'export-md'): void
  (e: 'toggle-side'): void
  (e: 'toggle-jobs'): void
  (e: 'open-trace'): void
  (e: 'open-children'): void
  (e: 'clean-scratch'): void
}>()

const { t } = useI18n()

// 右上角后台任务按钮显示的是「什么时候开始的」，不是跑了多久——
// 用户要对照的是「这个任务是几点起的」，一个静态时间戳就够，不需要 1Hz 计时器。
// 后端给的是 RFC3339 本地时间串，直接截 HH:mm 展示；title 里保留完整起止信息。
const startedClock = computed(() => {
  const s = props.runningJobStartedAt || ''
  if (!props.runningJobs || !s) return ''
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return ''
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
})
</script>

<template>
  <div class="ai-toolbar">
    <!-- 左组：目录指示 + 轨迹入口。两者都必须能在没有目录时各自独立呈现——
         目录标签只在绑了目录时出现，轨迹入口则常驻（可观测性不挂在空态判断上）。 -->
    <div class="ai-toolbar-left">
      <!-- 目录指示：绑了目录才允许 AI 改文件、跑命令（见后端 writeRoots），
           点一下在资源管理器里打开——产物的目录在数据目录下（普通任务）或项目里（空间任务），
           不给入口的话用户只能从 title 里抄路径自己去翻。
           它只负责「打开」当前已绑定的目录，不是「选目录」（绑定入口在新建任务时）。 -->
      <button v-if="props.convCwd" class="ai-cwd-tag" :title="t('aiCwdTip', { dir: props.convCwd })" @click="emit('open-cwd')">
        <FolderOpen :size="12" />
        <span class="ai-cwd-name">{{ props.cwdLabel }}</span>
      </button>
      <button
        class="ai-trace-tag"
        :class="`is-${props.traceState}`"
        :title="t('aiTraceEntryTip')"
        @click="emit('open-trace')"
      >
        <Activity :size="12" />
        <span class="ai-trace-name">{{ props.traceLabel }}</span>
      </button>
      <!-- 子任务入口：和轨迹共用抽屉，但只看整段会话派出去的子 agent。
           派活多时不必在轨迹里翻 run，直接点这里。 -->
      <button
        class="ai-trace-tag"
        :title="t('aiSubtasksTip')"
        @click="emit('open-children')"
      >
        <Users :size="12" />
        <span class="ai-trace-name">{{ t('aiSubtasksTitle') }}</span>
        <span v-if="props.childTaskCount > 0" class="ai-child-count">{{ props.childTaskCount }}</span>
      </button>
    </div>
    <div class="ai-toolbar-right">
      <button v-if="props.activeId" class="ai-icon-btn" :title="t('aiRegenTitle')" :aria-label="t('aiRegenTitle')" @click="emit('regen-title')">
        <RefreshCw :size="13" />
      </button>
      <button
        v-if="props.activeId"
        class="ai-icon-btn"
        :class="{ 'is-active': !!props.activeConv?.starred }"
        :title="t(props.activeConv?.starred ? 'aiStarOff' : 'aiStarOn')"
        :aria-label="t(props.activeConv?.starred ? 'aiStarOff' : 'aiStarOn')"
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
        :aria-label="t('aiOutline')"
        @click="emit('update:digestOpen', !props.digestOpen)"
      >
        <ListTree :size="13" />
      </button>
      <!-- 后台任务：只要有任务在跑就一直亮着，和当前会话无关——
           这类进程是宿主级的，用户在别处聊天时也该知道它还在跑。 -->
      <button
        class="ai-icon-btn ai-jobs-btn"
        :class="{ 'is-active': props.jobsOpen }"
        :title="startedClock ? `${t('aiJobsTitle')} · ${t('aiJobStartedAt', { time: props.runningJobStartedAt })}` : t('aiJobsTitle')"
        :aria-label="t('aiJobsTitle')"
        @click="emit('toggle-jobs')"
      >
        <Terminal :size="13" />
        <span v-if="props.runningJobs" class="ai-jobs-badge">{{ props.runningJobs }}</span>
        <span v-if="startedClock" class="ai-jobs-started">{{ startedClock }}</span>
      </button>
      <button v-if="props.activeId" class="ai-icon-btn" :title="t('aiExportMd')" :aria-label="t('aiExportMd')" @click="emit('export-md')">
        <Download :size="13" />
      </button>
      <!-- 清理辅助文件（WP18）：删 `<工作目录>/quickdock/`。
           做成两次点击而不是弹确认框：这个目录的语义就是「AI 的临时产物」，
           但删目录仍然不可逆，所以第一次点击只上膛（3 秒后自动复位）。
           没有工作目录时不显示——那种情况下 AI 的产物在数据目录里，不归这里管。 -->
      <button
        v-if="props.canCleanScratch"
        class="ai-icon-btn"
        :class="{ 'is-armed': props.scratchArmed }"
        :title="t(props.scratchArmed ? 'aiScratchCleanConfirm' : 'aiScratchClean')"
        :aria-label="t(props.scratchArmed ? 'aiScratchCleanConfirm' : 'aiScratchClean')"
        @click="emit('clean-scratch')"
      >
        <Eraser :size="13" />
      </button>
      <!-- 右侧面板开关：常驻最右（与常见 AI 工作台同位同快捷键） -->
      <button class="ai-icon-btn" :class="{ 'is-active': props.sideOpen }" :title="t(props.sideOpen ? 'aiSideClose' : 'aiSideOpen')" :aria-label="t(props.sideOpen ? 'aiSideClose' : 'aiSideOpen')" @click="emit('toggle-side')">
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
  padding: 10px var(--space-6);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  gap: 10px;
}

.ai-toolbar-right { display: flex; align-items: center; gap: var(--space-3); flex-shrink: 0; }

/* 左组整体顶到左边。
   这个 margin-right:auto 原来挂在 .ai-cwd-tag 上——一旦目录标签不渲染（未绑目录），
   整组就没有东西把右侧推开，轨迹入口会被 justify-content:flex-end 挤到最右。
   所以把它提到组容器上，与「组里有哪些元素」解耦。 */
.ai-toolbar-left {
  display: flex; align-items: center; gap: 10px;
  margin-right: auto;
  min-width: 0;
}

/* 工作目录：显示当前绑到哪个目录，点一下在资源管理器里打开。
   它只「打开」已绑定的目录，不是「选目录」——绑定入口已收进「新建任务 → 选空间」。
   保留它是因为它同时回答两件事：AI 能不能改文件，以及生成的文件去哪了。
   宽度上限必须有：项目路径动辄五六层，不夹住会把模型下拉挤到换行。
   原来是 max-width:40%，窄窗时它和轨迹入口按百分比一起收缩，两个都只剩省略号；
   改成固定上限 + 可收缩，让空间紧张时只压缩这里，轨迹标签始终完整。 */
.ai-cwd-tag {
  display: inline-flex; align-items: center; gap: 5px;
  flex: 0 1 auto; max-width: 220px; min-width: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-disabled);
  font-size: var(--text-sm); font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.ai-cwd-tag:hover { color: var(--color-text-primary); border-color: var(--color-border); }

.ai-cwd-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 轨迹入口：常驻在最左（目录标签右侧）。
   三种状态只靠圆点与文字区分，不加背景块——工具栏已经有一排图标按钮，
   再加一块实色会长得像「当前选中项」。 */
.ai-trace-tag {
  display: inline-flex; align-items: center; gap: 5px;
  /* 不收缩：它内容就「轨迹 / ● 12 步 / 12 步 · 3m20s」这么长，
     让位给目录标签没有收益——省下的几个像素换来两个都看不清。 */
  flex-shrink: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-disabled);
  font-size: var(--text-sm); font-family: inherit;
  cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}

.ai-trace-tag:hover { color: var(--color-text-primary); border-color: var(--color-border); }
.ai-trace-tag.is-running { color: var(--color-accent); }

.ai-trace-name {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-variant-numeric: tabular-nums;
}

/* 子任务数量徽标：胶囊小圆点，0 时不渲染（见模板）。
   独立上色而非继承按钮色，没子任务时按钮仍是中性灰。 */
.ai-child-count {
  display: inline-flex; align-items: center; justify-content: center;
  min-width: 16px; height: 16px; padding: 0 4px;
  border-radius: var(--radius-full);
  background: var(--color-accent-bg); color: var(--color-accent-text);
  font-size: var(--text-2xs); font-variant-numeric: tabular-nums;
}

/* 运行中：图标用呼吸动画提示「还在跑」。
   不加无限旋转——工具栏常驻，转久了会抢注意力。 */
.ai-trace-tag.is-running :deep(svg) { animation: ai-trace-pulse 1.6s ease-in-out infinite; }

@keyframes ai-trace-pulse {
  0%, 100% { opacity: 1; }
  50% { opacity: 0.35; }
}

/* 尊重系统的「减少动态效果」：脉冲关掉，只保留颜色区分。 */
@media (prefers-reduced-motion: reduce) {
  .ai-trace-tag.is-running :deep(svg) { animation: none; }
}

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
/* 清理按钮的「已上膛」态：与普通激活态区分开——它表示「再点一次就真删」，
   用 warning 而不是 accent，避免和「面板已打开」那种无害的激活混淆。 */
.ai-icon-btn.is-armed { color: var(--color-warning); border-color: var(--color-warning); background: transparent; }
.ai-icon-btn:disabled { opacity: 0.5; cursor: not-allowed; }

/* 后台任务按钮：运行中数量做成右上角的小徽标。
   不用「(2)」写在图标旁边——那会让按钮变宽，工具栏右组在窄窗口下要换行。 */
.ai-jobs-btn { position: relative; min-width: 30px; width: auto; padding: 0 7px; gap: 5px; }

/* 开始时刻跟图标并排，不挤进圆点徽标：徽标回答「几个」，
   这里回答「几点起的」，两个都是定位信息，但别塞进同一个小圆点。 */
.ai-jobs-started {
  font-size: var(--text-2xs);
  font-variant-numeric: tabular-nums;
  color: var(--color-accent);
}

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
  font-size: var(--text-2xs);
  line-height: 14px;
  text-align: center;
  font-variant-numeric: tabular-nums;
  pointer-events: none;
}
</style>
