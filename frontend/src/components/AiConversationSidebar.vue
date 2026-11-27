<script setup lang="ts">
// 左侧会话列表（任务 / 空间 / 星标 / 正文命中）。
//
// 这一层刻意做成「纯展示壳」：折叠状态、搜索词、改名草稿全部留在 AIPage。
// 它们不是局部 UI 状态——convQuery 一变就要去后端查正文命中（runMsgSearch），
// 改名提交后要重取列表，把状态搬进来反而让父组件等子组件回调，
// 顺序更难保证。这里的 props 是单向数据 + 父提供的动作。
import type { Component } from 'vue'
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlarmClock, ChevronDown, ChevronRight, Copy, Folder, FolderOpen, Loader2, Pencil, Play, Plus, Search, Star, Terminal, Trash2, Zap } from '@lucide/vue'
import type { AIConvHit, AIConversation, AISpace } from '../types/ai'
import AiConvRunDot from './AiConvRunDot.vue'
import QdSearchBox from './QdSearchBox.vue'

interface SpaceGroup { space: AISpace; tasks: AIConversation[] }

interface Props {
  conversations: AIConversation[]
  spaces: AISpace[]
  activeId: string
  /** 每条会话的运行状态（running / done / error），键是会话 id。见 AIPage 的 convRunState */
  runState: Record<string, 'running' | 'done' | 'error'>

  convQuery: string
  starredConvs: AIConversation[]
  starredOpen: boolean
  msgHits: AIConvHit[]
  msgHitsLoading: boolean
  msgHitsOpen: boolean
  taskConversations: AIConversation[]
  shownTaskConversations: AIConversation[]
  tasksOpen: boolean
  tasksExpanded: boolean
  shownSpaces: SpaceGroup[]
  visibleSpaces: SpaceGroup[]
  spacesOpen: boolean
  spacesExpanded: boolean
  expandedSpaceTasks: string[]
  // 刚被「跳转定位」到的空间：亮一下再淡回常态（见 AIPage.revealSpace）。
  flashSpaceId: string
  // 正在后台跑的定时任务 id：空间行上的运行按钮据此转圈并禁用。
  spaceRunBusy: string[]
  // 空间行右侧「立即运行一次」（仅定时任务空间）。
  runSpaceTask: (s: AISpace) => void
  totalMatched: number

  renamingId: string
  renameText: string
  renamingSpaceId: string
  spaceNameText: string

  foldTasks: number
  foldSpaces: number
  foldSpaceTasks: number

  openNewTask: () => void
  /** msgId 只在「正文命中的搜索入口」传：切过去之后还要落到那句话上 */
  selectConv: (id: string, msgId?: string) => void
  /** 这条任务的目录：隔离工作区 > 会话目录 > 所属空间目录；空串 = 还没有目录 */
  convDir: (c: AIConversation) => string
  /** 在系统文件管理器里打开某个目录（任务与空间共用；失败提示由父组件统一 toast） */
  openDir: (dir: string) => void
  /** 复制路径：打开资源管理器之后常要 cd 过去，路径得能直接粘 */
  copyPath: (dir: string) => void
  modeIcon: (m?: string) => Component
  toggleStar: (c: AIConversation) => void
  delConv: (id: string) => void
  delSpace: (s: AISpace) => void
  newTaskInSpace: (s: AISpace) => void
  startRename: (c: AIConversation) => void
  cancelRename: () => void
  commitRename: (c: AIConversation) => void
  startRenameSpace: (s: AISpace) => void
  cancelRenameSpace: () => void
  commitRenameSpace: (s: AISpace) => void
  isSpaceExpanded: (id: string) => boolean
  toggleSpace: (id: string) => void
  spaceTasksShown: (g: SpaceGroup) => AIConversation[]
  toggleSpaceTasks: (id: string) => void
  onEnterSend: (e: KeyboardEvent, fn: () => void) => void
  fmtTokens: (n: number) => string
  /** 会话分页：还有没有下一页 / 是否正在翻页 / 翻页动作（状态都在 AIPage） */
  hasMore: boolean
  loadingMore: boolean
  loadMore: () => void
}

const emit = defineEmits<{
  (e: 'update:convQuery', v: string): void
  (e: 'update:starredOpen', v: boolean): void
  (e: 'update:msgHitsOpen', v: boolean): void
  (e: 'update:tasksOpen', v: boolean): void
  (e: 'update:tasksExpanded', v: boolean): void
  (e: 'update:spacesOpen', v: boolean): void
  (e: 'update:spacesExpanded', v: boolean): void
  (e: 'update:renameText', v: string): void
  (e: 'update:spaceNameText', v: string): void
}>()

const { t } = useI18n()
const props = defineProps<Props>()

// 空间行上的聚合状态：空间收起时，里面那条正在跑/刚跑完的对话就完全看不见了，
// 所以把最强的一个状态提到空间行上。优先级 running > error > done——
// 「还在跑」最要紧，其次是「失败了」。
function spaceRunState(g: SpaceGroup): 'running' | 'done' | 'error' | undefined {
  let out: 'done' | 'error' | undefined
  for (const c of g.tasks) {
    // 用户正打开着的那条不算：内容就在眼前，行上再挂个点只是噪音
    if (c.id === props.activeId) continue
    const st = props.runState[c.id]
    if (!st) continue
    if (st === 'running') return 'running'
    if (st === 'error') out = 'error'
    else if (!out) out = 'done'
  }
  return out
}

// 空间行上的未读汇总：空间收起时，会话行上那个未读点根本看不见，
// 定时任务在后台跑完就完全没有提示——这个点就是给它留的。
function spaceUnread(g: SpaceGroup): boolean {
  return g.tasks.some((c) => !!c.unread)
}

// 定时任务专属空间用闹钟图标。kind 是升级时补的列，迁移没跑完的老库它是空串，
// 但 task_id 已经填了——拿它兜一下，否则同一类空间升级前后会换图标（目录 ↔ 闹钟）。
function isSchedSpace(s: AISpace): boolean {
  return s.kind === 'schedule' || !!s.task_id
}

// SSH 助手专属空间用终端图标（由后端在第一次 SSH 提问时自动建，见 db.EnsureSSHSpace）。
//
// 它和定时任务空间一样不是「目录容器」：没有 cwd、也不该让用户往里手工新建任务
// ——里面的任务是连接行为的产物。所以行内的「+」与右键的「新建任务」都对它隐藏，
// 否则点下去会建出一条没有目录、也不知道属于哪台机器的空任务。
function isSshSpace(s: AISpace): boolean {
  return s.kind === 'ssh'
}

// 两个 v-model 走 input 事件而不是 defineModel：项目 Vue 是 ^3.2，
// defineModel 要 3.4+，这里宁可多两行也不押依赖版本。
// （会话搜索框已换成 QdSearchBox，事件签名由它统一提供，不再需要手写转换。）
function onRenameTextInput(e: Event) {
  emit('update:renameText', (e.target as HTMLInputElement).value)
}
function onSpaceNameTextInput(e: Event) {
  emit('update:spaceNameText', (e.target as HTMLInputElement).value)
}

// ---- 任务行 / 空间行右键菜单 ----
// 为什么需要：对话页顶栏只有「打开当前任务的目录」，想打开**另一条**任务的目录
// 得先把它点开、等消息加载完、再点一次——而这本来是个看一眼就走的小动作。
//
// 菜单同时承载「这一行能做的全部动作」：行内原先那几个悬停小按钮（星标/改名/删除）
// 只在鼠标悬停时显形却要常驻占位，扫视列表时是一排噪音；收进右键后行里只剩标题。
// 空间行同一套菜单，但它没有星标（空间本来就没有这个概念），那一格换成「新建任务」。
type CtxTarget =
  | { kind: 'conv'; conv: AIConversation }
  | { kind: 'space'; space: AISpace }

interface CtxItem {
  id: string
  label?: string
  icon?: Component
  /** 星标：已收藏时画实心，一眼看出当前状态 */
  filled?: boolean
  danger?: boolean
  disabled?: boolean
  tip?: string
  /** 这一项之前画一条分隔线：把「打开目录」与「改这一行」分成两组 */
  sep?: boolean
  run?: () => void
}

const ctx = ref<{ x: number; y: number; target: CtxTarget } | null>(null)
const ctxMenuEl = ref<HTMLElement | null>(null)

// 正文命中行只带 conv_id（后端搜的是消息表），要拿它的目录得回会话列表里查。
// 查不到时给一个只有 id 的空壳：菜单照弹——比右键毫无反应更像「有功能，只是没目录」。
function convById(id: string): AIConversation {
  return props.conversations.find((c) => c.id === id) ?? ({ id } as AIConversation)
}

// 菜单里「目录」取哪一条：任务走 convDir（隔离工作区 > 会话目录 > 所属空间目录），
// 空间直接用自己的 cwd。空串时两个目录动作置灰并给出原因。
const ctxDir = computed(() => {
  const v = ctx.value
  if (!v) return ''
  return v.target.kind === 'conv' ? props.convDir(v.target.conv) : (v.target.space.cwd || '').trim()
})

function openCtxMenu(e: MouseEvent, target: CtxTarget) {
  // 改名中让输入框拿原生菜单：那时右键要的是「复制/粘贴标题文字」，不是行动作
  const renaming = target.kind === 'conv'
    ? props.renamingId === target.conv.id
    : props.renamingSpaceId === target.space.id
  if (renaming) return
  e.preventDefault()
  ctx.value = { x: e.clientX, y: e.clientY, target }
  // 贴右/贴底时往回收。尺寸必须量而不能写死：任务 5 项、空间 5 项但分隔线位置不同，
  // 以后再加一项常量就立刻失准。量完在同一个 tick 内改坐标，绘制前就位，肉眼看不出跳。
  nextTick(() => {
    const v = ctx.value
    const el = ctxMenuEl.value
    if (!v || !el) return
    const r = el.getBoundingClientRect()
    v.x = Math.max(8, Math.min(v.x, window.innerWidth - r.width - 8))
    v.y = Math.max(8, Math.min(v.y, window.innerHeight - r.height - 8))
  })
}

function closeCtx() {
  ctx.value = null
}

// 每项都「先关菜单再执行」：删除会弹确认框，菜单悬在对话框边上很难看，
// 也容易被误认为还能再点别的项。
function act(fn: () => void) {
  return () => {
    closeCtx()
    fn()
  }
}

const ctxItems = computed<CtxItem[]>(() => {
  const v = ctx.value
  if (!v) return []
  const dir = ctxDir.value
  const noDir = !dir
  if (v.target.kind === 'conv') {
    const c = v.target.conv
    return [
      { id: 'reveal', label: t('openInExplorer'), icon: FolderOpen, disabled: noDir, tip: dir || t('aiConvNoDir'), run: act(() => props.openDir(dir)) },
      { id: 'copy', label: t('actCopyPath'), icon: Copy, disabled: noDir, tip: dir || t('aiConvNoDir'), run: act(() => props.copyPath(dir)) },
      { id: 'star', label: t(c.starred ? 'aiStarOff' : 'aiStarOn'), icon: Star, filled: !!c.starred, sep: true, run: act(() => props.toggleStar(c)) },
      { id: 'rename', label: t('aiRename'), icon: Pencil, run: act(() => props.startRename(c)) },
      { id: 'del', label: t('delete'), icon: Trash2, danger: true, run: act(() => props.delConv(c.id)) },
    ]
  }
  const sp = v.target.space
  const items: CtxItem[] = [
    { id: 'reveal', label: t('openInExplorer'), icon: FolderOpen, disabled: noDir, tip: dir || t('aiSpaceNoDir'), run: act(() => props.openDir(dir)) },
    { id: 'copy', label: t('actCopyPath'), icon: Copy, disabled: noDir, tip: dir || t('aiSpaceNoDir'), run: act(() => props.copyPath(dir)) },
  ]
  // 「在这个空间里新建任务」对 SSH 空间没有意义：那个空间没有目录，
  // 里面的任务只能由连接产生。其余动作（改名 / 删除）照常给。
  if (!isSshSpace(sp)) {
    items.push({ id: 'new', label: t('aiNewTaskInSpace'), icon: Plus, sep: true, run: act(() => props.newTaskInSpace(sp)) })
  }
  items.push(
    { id: 'rename', label: t('aiRename'), icon: Pencil, sep: isSshSpace(sp), run: act(() => props.startRenameSpace(sp)) },
    { id: 'del', label: t('delete'), icon: Trash2, danger: true, run: act(() => props.delSpace(sp)) },
  )
  return items
})

// Esc 关菜单是最容易想到的一手；滚动也关——菜单是 fixed 定位，
// 列表一动它就悬在别的行上了，继续留着只会点错。
function onCtxKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && ctx.value) closeCtx()
}
onMounted(() => {
  document.addEventListener('keydown', onCtxKeydown)
  window.addEventListener('resize', closeCtx)
  window.addEventListener('scroll', closeCtx, true)
})
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onCtxKeydown)
  window.removeEventListener('resize', closeCtx)
  window.removeEventListener('scroll', closeCtx, true)
})
</script>

<template>
  <aside class="ai-sidebar">
    <button class="ai-new-btn qd-btn" @click="openNewTask">
      <Plus :size="14" /> {{ t('aiNewTask') }}
    </button>
    <QdSearchBox
      v-if="conversations.length > 5"
      :model-value="convQuery"
      class="ai-conv-search"
      :icon-size="12"
      :placeholder="t('aiSearchConvPh')"
      @update:model-value="emit('update:convQuery', $event)"
    />
    <div class="ai-conv-list">
      <!-- 星标：横切任务与空间，所以单独一组放最上面。
           原位置不移走 —— 星标是快捷入口，不是搬进收藏夹。 -->
      <template v-if="starredConvs.length">
        <div v-key-click class="ai-group-head ai-group-head-btn" :aria-expanded="starredOpen" @click="emit('update:starredOpen', !starredOpen)">
          <component :is="starredOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
          <Star :size="11" class="ai-group-caret" />
          <span>{{ t('aiStarGroup') }} ({{ starredConvs.length }})</span>
        </div>
        <template v-if="starredOpen">
          <div v-key-click
            v-for="c in starredConvs"
            :key="'star-' + c.id"
            :class="['ai-conv-item', { active: c.id === activeId }]"
            @click="selectConv(c.id)"
            @contextmenu.stop="openCtxMenu($event, { kind: 'conv', conv: c })"
          >
            <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
            <AiConvRunDot :state="c.id === activeId ? undefined : runState[c.id]" />
            <span class="ai-conv-title">{{ c.title || t('aiNewChat') }}</span>
            <span v-if="c.unread" class="ai-conv-unread" :title="t('aiUnreadHint')" />
          </div>
        </template>
      </template>

      <!-- 消息命中：搜索框同时查正文。只匹配标题的话，「上次那句报错在哪条任务里」
           这种最常用的找法永远搜不到——而标题往往只写「修复配置」。
           放在任务组之上：搜索时用户要的几乎总是它。 -->
      <template v-if="msgHits.length || msgHitsLoading">
        <div v-key-click class="ai-group-head ai-group-head-btn" :aria-expanded="msgHitsOpen" @click="emit('update:msgHitsOpen', !msgHitsOpen)">
          <component :is="msgHitsOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
          <span>{{ t('aiGroupMsgHits') }} ({{ msgHits.length }})</span>
        </div>
        <template v-if="msgHitsOpen">
          <div v-key-click
            v-for="h in msgHits"
            :key="h.conv_id"
            :class="['ai-conv-item', 'is-msg-hit', { active: h.conv_id === activeId }]"
            @click="selectConv(h.conv_id, h.latest.msg_id)"
            @contextmenu.stop="openCtxMenu($event, { kind: 'conv', conv: convById(h.conv_id) })"
          >
            <Search :size="11" class="ai-conv-mode" />
            <AiConvRunDot :state="h.conv_id === activeId ? undefined : runState[h.conv_id]" />
            <span v-if="convById(h.conv_id)?.unread" class="ai-conv-unread" :title="t('aiUnreadHint')" />
            <div class="ai-msg-hit-body">
              <div class="ai-msg-hit-head">
                <span class="ai-conv-title">{{ h.conv_title || t('aiNewChat') }}</span>
                <span v-if="h.count > 1" class="ai-msg-hit-count">+{{ h.count - 1 }}</span>
              </div>
              <div class="ai-msg-hit-snippet">{{ h.latest.snippet }}</div>
            </div>
          </div>
          <div v-if="msgHitsLoading && !msgHits.length" class="ai-space-empty">{{ t('aiMsgHitSearching') }}</div>
        </template>
      </template>

      <!-- 任务组：不属于任何空间的任务。组标题可点 = 整组折叠 -->
      <div v-key-click class="ai-group-head ai-group-head-btn" :aria-expanded="tasksOpen" @click="emit('update:tasksOpen', !tasksOpen)">
        <component :is="tasksOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
        <span>{{ t('aiGroupTasks') }} ({{ taskConversations.length }})</span>
      </div>
      <template v-if="tasksOpen">
      <template v-for="c in shownTaskConversations" :key="c.id">
        <div v-key-click
          :class="['ai-conv-item', { active: c.id === activeId }]"
          @click="selectConv(c.id)"
          @contextmenu.stop="openCtxMenu($event, { kind: 'conv', conv: c })"
        >
          <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
          <AiConvRunDot :state="c.id === activeId ? undefined : runState[c.id]" />
          <input
            v-if="renamingId === c.id"
            ref="renameInput"
            :value="renameText" @input="onRenameTextInput"
            class="ai-conv-rename"
            spellcheck="false"
            maxlength="60"
            @click.stop
            @keydown.enter="onEnterSend($event, () => commitRename(c))"
            @keydown.esc.prevent="cancelRename()"
            @blur="commitRename(c)"
          />
          <!-- 双击 / F2 都能改名：改名是低频操作，给它常驻按钮不如给个不占位的手势。
               v-key-click 让标题可 Tab 聚焦，@keydown.f2 提供键盘触发（Enter 留给选中会话）。 -->
          <span
            v-else
            v-key-click
            class="ai-conv-title"
            :title="t('aiRenameHint')"
            @dblclick.stop="startRename(c)"
            @keydown.f2.stop.prevent="startRename(c)"
          >{{ c.title || t('aiNewChat') }}</span>
          <!-- 任务累计用量：只统计「问答」，标题生成 / 摘要压缩不计入（见 ai_usage.go），
               所以这里的数字应该等于该任务各条回复下方统计之和。
               折算成 k/M 显示，精确值放 title。 -->
          <span v-if="renamingId !== c.id && (c.prompt_tokens > 0 || c.completion_tokens > 0)" class="ai-conv-tokens" :title="t('aiConvTokenTip', { in: c.prompt_tokens, out: c.completion_tokens })">
            <Zap :size="10" class="ai-conv-tokens-ico" />{{ fmtTokens(c.prompt_tokens + c.completion_tokens) }}
          </span>
          <span v-if="c.unread" class="ai-conv-unread" :title="t('aiUnreadHint')" />
        </div>
      </template>
      <!-- 超过阈值才出现：展开后同一个按钮变「收起」，来回都走这一处 -->
      <button
        v-if="taskConversations.length > foldTasks"
        class="ai-conv-more"
        @click="emit('update:tasksExpanded', !tasksExpanded)"
      >{{ tasksExpanded ? t('aiFoldList') : t('aiShowMoreConv', { n: taskConversations.length - foldTasks }) }}</button>
      </template>

      <!-- 空间组：绑定目录的任务容器 -->
      <div v-key-click class="ai-group-head ai-group-head-space ai-group-head-btn" :aria-expanded="spacesOpen" @click="emit('update:spacesOpen', !spacesOpen)">
        <component :is="spacesOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
        <span>{{ t('aiGroupSpaces') }} ({{ shownSpaces.length }})</span>
      </div>
      <template v-if="spacesOpen">
      <div v-for="g in visibleSpaces" :key="g.space.id" class="ai-space">
        <div v-key-click
          :id="'ai-space-' + g.space.id"
          :class="['ai-space-row', { 'is-flash': flashSpaceId === g.space.id }]"
          @click="toggleSpace(g.space.id)"
          @contextmenu.stop="openCtxMenu($event, { kind: 'space', space: g.space })"
        >
          <component
            :is="isSpaceExpanded(g.space.id) ? ChevronDown : ChevronRight"
            :size="12"
            class="ai-space-caret"
          />
          <!-- 定时任务专属空间用闹钟，和普通目录一眼分开；名字往往带
               「任务名 · 创建时间」后缀，侧栏窄，完整名放 tooltip 里。
               SSH 助手空间用终端：它是连接史的归档，同样不是目录。 -->
          <AlarmClock v-if="isSchedSpace(g.space)" :size="12" class="ai-space-icon is-sched" />
          <Terminal v-else-if="isSshSpace(g.space)" :size="12" class="ai-space-icon is-sched" />
          <Folder v-else :size="12" class="ai-space-icon" />
          <!-- 与任务标题同一套行内改名：为一个短名字弹系统对话框（window.prompt）
               不跟主题、字体也不一致，而且输入法组合中容易丢字。 -->
          <input
            v-if="renamingSpaceId === g.space.id"
            ref="spaceNameInput"
            :value="spaceNameText" @input="onSpaceNameTextInput"
            class="ai-conv-rename"
            spellcheck="false"
            maxlength="40"
            @click.stop
            @keydown.enter="onEnterSend($event, () => commitRenameSpace(g.space))"
            @keydown.esc.prevent="cancelRenameSpace()"
            @blur="commitRenameSpace(g.space)"
          />
          <span v-else v-key-click class="ai-space-name" :title="g.space.task_gone ? `${g.space.cwd || g.space.name} · ${t('aiSpaceTaskGone')}` : (g.space.cwd || g.space.name)" @dblclick.stop="startRenameSpace(g.space)" @keydown.f2.stop.prevent="startRenameSpace(g.space)">{{ g.space.name }}</span>
          <AiConvRunDot :state="spaceRunState(g)" />
          <span v-if="spaceUnread(g)" class="ai-conv-unread" :title="t('aiUnreadHint')" />
          <span class="ai-space-count">{{ g.tasks.length }}</span>
          <!-- 立即运行一次：定时任务在侧栏就能重跑，不用切到定时任务页。
               任务被删后（task_gone）不再出现——点下去只会得到「任务不存在」。 -->
          <button
            v-if="isSchedSpace(g.space) && g.space.task_id && !g.space.task_gone"
            class="ai-space-run"
            :class="{ 'is-busy': spaceRunBusy.includes(g.space.task_id || '') }"
            :disabled="spaceRunBusy.includes(g.space.task_id || '')"
            :title="spaceRunBusy.includes(g.space.task_id || '') ? t('aiSpaceRunBusy') : t('aiSpaceRunNow')"
            @click.stop="runSpaceTask(g.space)"
          >
            <Loader2 v-if="spaceRunBusy.includes(g.space.task_id || '')" :size="12" class="spin" />
            <Play v-else :size="12" />
          </button>
          <!-- 在这个空间里新建任务：否则每次都要去落地页的「归属空间」下拉里翻一遍。
               SSH 空间不给这个入口——它没有目录，任务只能由连接产生。 -->
          <button
            v-if="renamingSpaceId !== g.space.id && !isSshSpace(g.space)"
            class="ai-conv-add"
            :title="t('aiNewTaskInSpace')"
            @click.stop="newTaskInSpace(g.space)"
          >
            <Plus :size="12" />
          </button>
        </div>
        <template v-if="isSpaceExpanded(g.space.id)">
          <div v-key-click
            v-for="c in spaceTasksShown(g)"
            :key="c.id"
            :class="['ai-conv-item', 'is-in-space', { active: c.id === activeId }]"
            @click="selectConv(c.id)"
            @contextmenu.stop="openCtxMenu($event, { kind: 'conv', conv: c })"
          >
            <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
            <AiConvRunDot :state="c.id === activeId ? undefined : runState[c.id]" />
            <!-- 空间内的任务同样支持双击/F2 改名：输入框必须在这一支里也渲染，
                 否则 startRename 把 renamingId 置上却没有对应 input，
                 openCtxMenu 又会因「正在改名」吞掉右键，表现是右键菜单也不出来。 -->
            <input
              v-if="renamingId === c.id"
              ref="renameInput"
              :value="renameText" @input="onRenameTextInput"
              class="ai-conv-rename"
              spellcheck="false"
              maxlength="60"
              @click.stop
              @keydown.enter="onEnterSend($event, () => commitRename(c))"
              @keydown.esc.prevent="cancelRename()"
              @blur="commitRename(c)"
            />
            <span v-else v-key-click class="ai-conv-title" :title="t('aiRenameHint')" @dblclick.stop="startRename(c)" @keydown.f2.stop.prevent="startRename(c)">{{ c.title || t('aiNewChat') }}</span>
            <span v-if="c.unread" class="ai-conv-unread" :title="t('aiUnreadHint')" />
          </div>
          <button
            v-if="g.tasks.length > foldSpaceTasks"
            class="ai-conv-more is-in-space"
            @click="toggleSpaceTasks(g.space.id)"
          >{{ expandedSpaceTasks.includes(g.space.id) ? t('aiFoldList') : t('aiShowMoreConv', { n: g.tasks.length - foldSpaceTasks }) }}</button>
          <div v-if="g.tasks.length === 0" class="ai-space-empty">{{ t('aiSpaceNoTasks') }}</div>
        </template>
      </div>
      <button
        v-if="shownSpaces.length > foldSpaces"
        class="ai-conv-more"
        @click="emit('update:spacesExpanded', !spacesExpanded)"
      >{{ spacesExpanded ? t('aiFoldList') : t('aiShowMoreSpaces', { n: shownSpaces.length - foldSpaces }) }}</button>
      </template>

      <div v-if="conversations.length === 0" class="ai-conv-empty">{{ t('aiNoConversations') }}</div>
      <!-- 正文命中也算「搜到了东西」，不算空：否则上面列着三条消息命中，
           下面紧跟着一句「没有匹配的会话」。 -->
      <div v-else-if="totalMatched === 0 && !msgHits.length && !msgHitsLoading" class="ai-conv-empty">{{ t('aiNoMatchConv') }}</div>
      <!-- 会话分页入口：搜索时翻页没有意义（命中已经在本地结果里），所以只在没搜时显示。
           复用 .ai-conv-more 的整行虚线样式，与「显示更多空间」保持一致。 -->
      <button
        v-if="hasMore && !convQuery"
        class="ai-conv-more"
        :disabled="loadingMore"
        @click="loadMore"
      >{{ loadingMore ? t('aiConvLoadingMore') : t('aiConvLoadMore') }}</button>
    </div>

    <!-- 任务行右键菜单：透明遮罩负责「点别处 / 再右键 / Esc 即关」，菜单本体 stop 掉冒泡。
         left/top 用内联 style 是运行时算出来的鼠标坐标 + 边界回收，没有对应的静态类。 -->
    <!-- Teleport 到 body：侧栏是 overflow:hidden 的 flex 容器（.ai-sidebar），
         菜单留在里面时宽高与阴影都会被它裁一道边，看着就像「框是残缺的」。
         挂到 body 后定位只跟视口有关，遮罩也能盖住主区，与 SftpPanel 行菜单一致。 -->
    <Teleport to="body">
      <div v-if="ctx" class="ai-conv-ctx-backdrop" @click="closeCtx" @contextmenu.prevent="closeCtx">
        <div
          ref="ctxMenuEl"
          class="ai-conv-ctx-menu"
          :style="{ left: ctx.x + 'px', top: ctx.y + 'px' }"
          @click.stop
        >
          <!-- 项由 ctxItems 生成：任务与空间各一套，顺序即用户的操作频次排序 -->
          <template v-for="it in ctxItems" :key="it.id">
            <div v-if="it.sep" class="ai-conv-ctx-sep" />
            <button
              :class="['ai-conv-ctx-item', { danger: it.danger }]"
              :disabled="it.disabled"
              :title="it.tip"
              @click="it.run?.()"
            >
              <component :is="it.icon" :size="13" :fill="it.filled ? 'currentColor' : 'none'" class="ai-conv-ctx-ico" />
              <span class="ai-conv-ctx-label">{{ it.label }}</span>
            </button>
          </template>
        </div>
      </div>
    </Teleport>
  </aside>
</template>

<style scoped>

/* 会话列表 */
.ai-sidebar {
  width: 220px;
  min-width: 220px;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
  border-right: 1px solid var(--color-border);
  overflow: hidden;
}
.ai-new-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  margin: var(--space-5);
  padding: var(--space-4);
  border: 1px dashed var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  border-radius: var(--radius-md);
  cursor: pointer;
  font-family: inherit;
  font-size: var(--text-md);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-new-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-conv-list { flex: 1; overflow-y: auto; padding: 0 var(--space-4) var(--space-5); }

/* 分组标题：任务 / 空间。两组用同一套样式，视觉上是对等的两个区域，
   而不是「任务」为主、「空间」为附属——空间才是带目录的那个。 */
.ai-group-head {
  padding: var(--space-5) 10px var(--space-3);
  font-size: var(--text-xs);
  font-weight: 600;
  /* disabled 太弱，两个分组标题在长列表里像是「不存在」；muted 仍低于条目正文，
     但能一眼看出这是分区 */
  color: var(--color-text-muted);
  letter-spacing: 0.4px;
}
.ai-group-head-space { margin-top: var(--space-4); border-top: 1px solid var(--color-border); padding-top: var(--space-5); }
/* 组标题可点（收起整组）：条目一多时，先收起一整组比在里面翻找更快。
   箭头与空间行同一套语义——展开是向下箭头。 */
.ai-group-head-btn {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  cursor: pointer;
  user-select: none;
  transition: color var(--transition-fast);
}
.ai-group-head-btn:hover { color: var(--color-text-secondary); }
.ai-group-caret { flex-shrink: 0; opacity: 0.75; }

/* 类型图标：不标出来，侧栏一长串标题里看不出哪条是翻译、哪条是闲聊 */
.ai-conv-mode { flex-shrink: 0; opacity: 0.7; }
.ai-conv-item.active .ai-conv-mode { opacity: 1; }

.ai-space { margin-bottom: var(--space-1); }
.ai-space-row {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 7px 10px 7px var(--space-2);
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: var(--text-md);
  transition: background var(--transition-fast);
}
.ai-space-row:hover { background: var(--color-bg-hover); }
/* 跳转定位到某个空间时亮一下再淡回去。用动画而不是常驻高亮：常驻会和
   「当前任务」的选择态混在一起，用户分不清哪个是「我在这」哪个是「刚跳到这」。 */
.ai-space-row.is-flash { animation: ai-space-flash 1.8s ease-out; }
@keyframes ai-space-flash {
  0% { background: var(--color-accent-flash); }
  60% { background: var(--color-accent-bg); }
  100% { background: transparent; }
}
/* 立即运行一次：常驻显示而不是 hover 才出——它是个「动作入口」，
   藏起来就得先猜哪儿有按钮（「新建任务」那种辅助操作才适合 hover 显隐）。 */
.ai-space-run {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  padding: 0;
  background: none;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  cursor: pointer;
}
.ai-space-run:hover { color: var(--color-accent); background: var(--color-bg-tertiary); }
.ai-space-run.is-busy { display: flex; color: var(--color-accent); cursor: default; }
.ai-space-run.is-busy:hover { background: none; }
.ai-space-run .spin { animation: ai-space-run-spin 0.9s linear infinite; }
@keyframes ai-space-run-spin { to { transform: rotate(360deg); } }
/* 行内按钮只剩「运行」和「新建任务」两个：删除/改名/星标都进了右键菜单，
   别再把 .ai-conv-del 抄回来——那个类已经没有对应节点了。
   「新建任务」的显隐规则统一收在下面 .ai-conv-add 那一组，这里不再重复。 */
.ai-space-caret { flex-shrink: 0; opacity: 0.6; }
.ai-space-icon { flex-shrink: 0; color: var(--color-accent); opacity: 0.85; }
/* 定时任务空间是全自动产出物，和「我建的项目目录」不是一类东西：
   闹钟不透明，在侧栏里更容易一眼扫到。 */
.ai-space-icon.is-sched { opacity: 1; }
.ai-space-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 任务数常驻可见（不是 hover 才出现）：空间收起时它是唯一能说明
   「这里面有多少东西」的信息，藏起来等于逼用户一个个展开。 */
.ai-space-count {
  flex-shrink: 0;
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  background: var(--color-bg-tertiary);
  border-radius: var(--radius-md);
  padding: 0 var(--space-3);
}
.ai-conv-item.is-in-space { margin-left: 18px; padding: var(--space-3) 10px; font-size: var(--text-md); }
.ai-space-empty { padding: var(--space-2) 10px var(--space-4) 28px; font-size: var(--text-xs); color: var(--color-text-muted); }

.ai-conv-item {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-4) 10px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: var(--text-md);
  transition: background var(--transition-fast);
}
.ai-conv-item:hover { background: var(--color-bg-hover); }
.ai-conv-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.ai-conv-title {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
/* 定时任务跑完的会话标未读，点开才消。与 AiConvRunDot 是两个独立信号：
   「已经跑完了但还没看过」和「正在生成」可以同时成立。 */
.ai-conv-unread {
  flex: 0 0 6px;
  width: 6px; height: 6px;
  border-radius: 50%;
  background: var(--color-accent);
}

/* 消息命中的行比普通任务行高：标题下面还要放一行片段。
   片段限两行——侧栏是导航不是阅读器，一条工具输出能占几十行。 */
.ai-conv-item.is-msg-hit { align-items: flex-start; padding: 7px 10px; }
.ai-conv-item.is-msg-hit .ai-conv-mode { margin-top: 3px; }
.ai-msg-hit-body { flex: 1; min-width: 0; }
.ai-msg-hit-head { display: flex; align-items: center; gap: var(--space-2); }
/* 「还有 N 条」用数字而不是一条分隔线：同一段话被追问、被复述都会重复命中，
   这个数说明「这条对话里提过好几次」，是选哪条进去的关键。 */
.ai-msg-hit-count {
  flex: none; font-size: var(--text-2xs); color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
.ai-msg-hit-snippet {
  margin-top: var(--space-1); font-size: var(--text-xs); line-height: 1.45; color: var(--color-text-muted);
  overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
}
.ai-conv-item.active .ai-msg-hit-snippet { color: var(--color-text-secondary); }
/* 空间行的「+ 在本空间新建任务」：星标/改名/删除都收进了右键菜单（见文件末的菜单样式），
   只有这一个动作因为高频留在行内。常驻占位 + opacity 显隐（而不是 display:none → flex），
   行宽不会在 hover 时跳变，键盘 / 触屏也能摸到它。 */
.ai-conv-add {
  position: relative;
  flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; padding: 0;
  background: none; border: none; border-radius: var(--radius-sm);
  color: var(--color-text-muted);
  cursor: pointer;
  opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast), background-color var(--transition-fast);
}
/* 行 hover / 行内任意控件聚焦 / 按钮自身键盘聚焦时显形 */
.ai-space-row:hover .ai-conv-add,
.ai-space-row:focus-within .ai-conv-add,
.ai-conv-add:focus-visible { opacity: 1; }
.ai-conv-add:hover { color: var(--color-accent); background: var(--color-bg-tertiary); opacity: 1; }
/* 命中区补到 24px：伪元素外扩 1px，不撑大布局、不改行高 */
.ai-conv-add::before {
  content: '';
  position: absolute;
  inset: -1px;
  border-radius: var(--radius-sm);
}
/* 行内编辑：撑满标题那一格，且必须能盖住行本身的点击（改选中了会话就白改了） */
.ai-conv-rename {
  flex: 1;
  min-width: 0;
  padding: 0 var(--space-2);
  font: inherit;
  font-size: var(--text-md);
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-accent);
  border-radius: var(--radius-sm);
  outline: none;
}
.ai-conv-tokens {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: var(--text-2xs); color: var(--color-text-muted); white-space: nowrap;
  margin-right: var(--space-2); font-variant-numeric: tabular-nums;
}
/* 一枚闪电标出「这是用量，不是文件大小 / 行数」；数字用 muted 而不是 disabled：
   它是内容（会随对话增长的用量），不是禁用态，浅色下 disabled 对白底只有 2.4:1。 */
.ai-conv-tokens-ico { flex: none; opacity: 0.8; }
.ai-conv-empty { padding: var(--space-8) var(--space-5); text-align: center; color: var(--color-text-muted); font-size: var(--text-sm); }

/* 会话搜索：与新建按钮同宽，同一视觉层级 */
/* 皮肤由 QdSearchBox 统一提供（无边框 / 28px / bg-tertiary），这里只留外侧留白。 */
.ai-conv-search {
  margin: var(--space-3) var(--space-4) var(--space-1);
}
/* 加载更多：整行按钮，不要做成小链接——点中的概率太低 */
.ai-conv-more {
  margin: var(--space-1) var(--space-4) var(--space-4); padding: 5px 0; flex-shrink: 0;
  border: 1px dashed var(--color-border); border-radius: var(--radius-sm);
  background: none; color: var(--color-text-muted); font-size: var(--text-xs);
  font-family: inherit; cursor: pointer;
}
.ai-conv-more:hover { color: var(--color-text-primary); border-color: var(--color-accent); }
.ai-conv-more:disabled { color: var(--color-text-disabled); cursor: default; }
/* 空间内的「更多」跟着任务缩进，否则会和空间名对齐，看着像另一个空间 */
.ai-conv-more.is-in-space { margin-left: 26px; margin-right: 0; }

/* ---- 任务行右键菜单 ----
   fixed + 全屏透明遮罩：菜单贴鼠标出现，点遮罩任意位置即关。
   z-index 取 --z-context（右键菜单档，要能盖住弹窗）：这个菜单从 body 挂出，
   低于这个档位的任何带栈浮层（@ 补全、+ 面板）都会让菜单被切成两半。
   前缀用 ai-conv- 而不是 ai-ctx-：AIPage 里的图片/图表右键菜单已经占了
   ai-ctx 这一组类名，scoped 虽然能隔开，但全仓搜的时候两处会混在一起。 */
.ai-conv-ctx-backdrop {
  position: fixed; inset: 0; z-index: var(--z-context);
}
.ai-conv-ctx-menu {
  position: fixed;
  /* 定宽而不是 min-width：两项文字长度差一半，按内容撑会让菜单框跟着长度变，
     一左一右两次右键像两个不同的控件。196 刚好放下最长的那一项。 */
  width: 196px;
  padding: 5px;
  background: var(--color-popover-bg);
  /* 描边用 border-light：surface-elevated 与侧栏只差一档明度，
     普通 border 在深色下几乎看不见，菜单会像直接浮在文字上。 */
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-md);
  /* 项目自带的 --shadow-lg 是 22% 黑，压在深色底上留不下轮廓，这里换成
     --color-bg-overlay：它本身就是深浅两套值（深 55% / 浅 14%），
     不用为浅色再写一条 :global 覆盖（那个在 scoped 里会被编译坏）。 */
  box-shadow: 0 10px 28px var(--color-bg-overlay);
  display: flex; flex-direction: column; gap: var(--space-1);
}
.ai-conv-ctx-item {
  display: flex; align-items: center; gap: var(--space-4);
  width: 100%; height: 30px; padding: 0 9px;
  border: none; border-radius: var(--radius-sm); background: none;
  color: var(--color-text-secondary);
  font-size: var(--text-md); line-height: 1; font-family: inherit;
  text-align: left; cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast);
}
/* 分组线：上面是「打开/复制目录」，下面是「改这一行」 */
.ai-conv-ctx-sep {
  height: 1px; margin: 3px var(--space-2);
  background: var(--color-border);
}
/* 图标不许被文字挤扁；透明度单独调，hover 时跟文字一起提亮 */
.ai-conv-ctx-ico { flex-shrink: 0; opacity: 0.85; }
.ai-conv-ctx-label {
  flex: 1; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.ai-conv-ctx-item:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.ai-conv-ctx-item:hover:not(:disabled) .ai-conv-ctx-ico { opacity: 1; }
.ai-conv-ctx-item:disabled { color: var(--color-text-disabled); cursor: default; }
/* 删除：平时就用危险色，hover 再加一层淡红底——比「和别的项一样、只是字更红」更容易辨认 */
.ai-conv-ctx-item.danger { color: var(--color-danger-ink); }
.ai-conv-ctx-item.danger:hover:not(:disabled) { background: var(--color-danger-bg); color: var(--color-danger-ink); }
</style>
