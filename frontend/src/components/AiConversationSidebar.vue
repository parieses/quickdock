<script setup lang="ts">
// 左侧会话列表（任务 / 空间 / 星标 / 正文命中）。
//
// 这一层刻意做成「纯展示壳」：折叠状态、搜索词、改名草稿全部留在 AIPage。
// 它们不是局部 UI 状态——convQuery 一变就要去后端查正文命中（runMsgSearch），
// 改名提交后要重取列表，把状态搬进来反而让父组件等子组件回调，
// 顺序更难保证。这里的 props 是单向数据 + 父提供的动作。
import type { Component } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight, Folder, Pencil, Plus, Search, Star, Trash2, X } from '@lucide/vue'
import type { AIConvHit, AIConversation, AISpace } from '../types/ai'

interface SpaceGroup { space: AISpace; tasks: AIConversation[] }

interface Props {
  conversations: AIConversation[]
  spaces: AISpace[]
  activeId: string

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
  totalMatched: number

  renamingId: string
  renameText: string
  renamingSpaceId: string
  spaceNameText: string

  foldTasks: number
  foldSpaces: number
  foldSpaceTasks: number

  openNewTask: () => void
  selectConv: (id: string) => void
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
}

defineProps<Props>()
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

// 三个 v-model 走 input 事件而不是 defineModel：项目 Vue 是 ^3.2，
// defineModel 要 3.4+，这里宁可多两行也不押依赖版本。
function onConvQueryInput(e: Event) {
  emit('update:convQuery', (e.target as HTMLInputElement).value)
}
function onRenameTextInput(e: Event) {
  emit('update:renameText', (e.target as HTMLInputElement).value)
}
function onSpaceNameTextInput(e: Event) {
  emit('update:spaceNameText', (e.target as HTMLInputElement).value)
}
</script>

<template>
  <aside class="ai-sidebar">
    <button class="ai-new-btn" @click="openNewTask">
      <Plus :size="14" /> {{ t('aiNewTask') }}
    </button>
    <div v-if="conversations.length > 5" class="ai-conv-search">
      <Search :size="12" class="ai-conv-search-icon" />
      <input :value="convQuery" class="ai-conv-search-input" @input="onConvQueryInput" :placeholder="t('aiSearchConvPh')" />
      <button v-if="convQuery" class="ai-conv-search-clear" @click="emit('update:convQuery', '')"><X :size="11" /></button>
    </div>
    <div class="ai-conv-list">
      <!-- 星标：横切任务与空间，所以单独一组放最上面。
           原位置不移走 —— 星标是快捷入口，不是搬进收藏夹。 -->
      <template v-if="starredConvs.length">
        <div class="ai-group-head ai-group-head-btn" @click="emit('update:starredOpen', !starredOpen)">
          <component :is="starredOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
          <Star :size="11" class="ai-group-caret" />
          <span>{{ t('aiStarGroup') }} ({{ starredConvs.length }})</span>
        </div>
        <template v-if="starredOpen">
          <div
            v-for="c in starredConvs"
            :key="'star-' + c.id"
            :class="['ai-conv-item', { active: c.id === activeId }]"
            @click="selectConv(c.id)"
          >
            <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
            <span class="ai-conv-title">{{ c.title || t('aiNewChat') }}</span>
            <button class="ai-conv-ren is-starred" :title="t('aiStarOff')" @click.stop="toggleStar(c)">
              <Star :size="12" fill="currentColor" />
            </button>
          </div>
        </template>
      </template>

      <!-- 消息命中：搜索框同时查正文。只匹配标题的话，「上次那句报错在哪条任务里」
           这种最常用的找法永远搜不到——而标题往往只写「修复配置」。
           放在任务组之上：搜索时用户要的几乎总是它。 -->
      <template v-if="msgHits.length || msgHitsLoading">
        <div class="ai-group-head ai-group-head-btn" @click="emit('update:msgHitsOpen', !msgHitsOpen)">
          <component :is="msgHitsOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
          <span>{{ t('aiGroupMsgHits') }} ({{ msgHits.length }})</span>
        </div>
        <template v-if="msgHitsOpen">
          <div
            v-for="h in msgHits"
            :key="h.conv_id"
            :class="['ai-conv-item', 'is-msg-hit', { active: h.conv_id === activeId }]"
            @click="selectConv(h.conv_id)"
          >
            <Search :size="11" class="ai-conv-mode" />
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
      <div class="ai-group-head ai-group-head-btn" @click="emit('update:tasksOpen', !tasksOpen)">
        <component :is="tasksOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
        <span>{{ t('aiGroupTasks') }} ({{ taskConversations.length }})</span>
      </div>
      <template v-if="tasksOpen">
      <template v-for="c in shownTaskConversations" :key="c.id">
        <div
          :class="['ai-conv-item', { active: c.id === activeId }]"
          @click="selectConv(c.id)"
        >
          <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
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
          <!-- 双击标题也能改名：改名是低频操作，给它常驻按钮不如给个不占位的手势 -->
          <span
            v-else
            class="ai-conv-title"
            :title="t('aiRenameHint')"
            @dblclick.stop="startRename(c)"
          >{{ c.title || t('aiNewChat') }}</span>
          <!-- 任务累计用量：只统计「问答」，标题生成 / 摘要压缩不计入（见 ai_usage.go），
               所以这里的数字应该等于该任务各条回复下方统计之和。
               折算成 k/M 显示，精确值放 title。 -->
          <span v-if="renamingId !== c.id && (c.prompt_tokens > 0 || c.completion_tokens > 0)" class="ai-conv-tokens" :title="t('aiConvTokenTip', { in: c.prompt_tokens, out: c.completion_tokens })">
            {{ fmtTokens(c.prompt_tokens + c.completion_tokens) }}
          </span>
          <button
            v-if="renamingId !== c.id"
            class="ai-conv-ren"
            :class="{ 'is-starred': c.starred }"
            :title="t(c.starred ? 'aiStarOff' : 'aiStarOn')"
            @click.stop="toggleStar(c)"
          >
            <Star :size="12" :fill="c.starred ? 'currentColor' : 'none'" />
          </button>
          <button
            v-if="renamingId !== c.id"
            class="ai-conv-ren"
            :title="t('aiRename')"
            @click.stop="startRename(c)"
          >
            <Pencil :size="12" />
          </button>
          <button v-if="renamingId !== c.id" class="ai-conv-del" :title="t('delete')" @click.stop="delConv(c.id)">
            <Trash2 :size="13" />
          </button>
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
      <div class="ai-group-head ai-group-head-space ai-group-head-btn" @click="emit('update:spacesOpen', !spacesOpen)">
        <component :is="spacesOpen ? ChevronDown : ChevronRight" :size="11" class="ai-group-caret" />
        <span>{{ t('aiGroupSpaces') }} ({{ shownSpaces.length }})</span>
      </div>
      <template v-if="spacesOpen">
      <div v-for="g in visibleSpaces" :key="g.space.id" class="ai-space">
        <div class="ai-space-row" @click="toggleSpace(g.space.id)">
          <component
            :is="isSpaceExpanded(g.space.id) ? ChevronDown : ChevronRight"
            :size="12"
            class="ai-space-caret"
          />
          <Folder :size="12" class="ai-space-icon" />
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
          <span v-else class="ai-space-name" :title="g.space.cwd" @dblclick.stop="startRenameSpace(g.space)">{{ g.space.name }}</span>
          <span class="ai-space-count">{{ g.tasks.length }}</span>
          <!-- 在这个空间里新建任务：否则每次都要去落地页的「归属空间」下拉里翻一遍 -->
          <button
            v-if="renamingSpaceId !== g.space.id"
            class="ai-conv-add"
            :title="t('aiNewTaskInSpace')"
            @click.stop="newTaskInSpace(g.space)"
          >
            <Plus :size="12" />
          </button>
          <button
            v-if="renamingSpaceId !== g.space.id"
            class="ai-conv-ren"
            :title="t('aiRename')"
            @click.stop="startRenameSpace(g.space)"
          >
            <Pencil :size="11" />
          </button>
          <button v-if="renamingSpaceId !== g.space.id" class="ai-conv-del" :title="t('delete')" @click.stop="delSpace(g.space)">
            <Trash2 :size="12" />
          </button>
        </div>
        <template v-if="isSpaceExpanded(g.space.id)">
          <div
            v-for="c in spaceTasksShown(g)"
            :key="c.id"
            :class="['ai-conv-item', 'is-in-space', { active: c.id === activeId }]"
            @click="selectConv(c.id)"
          >
            <component :is="modeIcon(c.mode)" :size="12" class="ai-conv-mode" />
            <span class="ai-conv-title" @dblclick.stop="startRename(c)">{{ c.title || t('aiNewChat') }}</span>
            <button
              class="ai-conv-ren"
              :class="{ 'is-starred': c.starred }"
              :title="t(c.starred ? 'aiStarOff' : 'aiStarOn')"
              @click.stop="toggleStar(c)"
            >
              <Star :size="12" :fill="c.starred ? 'currentColor' : 'none'" />
            </button>
            <button class="ai-conv-del" :title="t('delete')" @click.stop="delConv(c.id)">
              <Trash2 :size="12" />
            </button>
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
    </div>
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
  gap: 6px;
  margin: 12px;
  padding: 8px;
  border: 1px dashed var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  border-radius: var(--radius-md);
  cursor: pointer;
  font-family: inherit;
  font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-new-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-conv-list { flex: 1; overflow-y: auto; padding: 0 8px 12px; }

/* 分组标题：任务 / 空间。两组用同一套样式，视觉上是对等的两个区域，
   而不是「任务」为主、「空间」为附属——空间才是带目录的那个。 */
.ai-group-head {
  padding: 12px 10px 6px;
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-disabled);
  letter-spacing: 0.4px;
}
.ai-group-head-space { margin-top: 4px; border-top: 1px solid var(--color-border); padding-top: 12px; }
/* 组标题可点（收起整组）：条目一多时，先收起一整组比在里面翻找更快。
   箭头与空间行同一套语义——展开是向下箭头。 */
.ai-group-head-btn {
  display: flex;
  align-items: center;
  gap: 4px;
  cursor: pointer;
  user-select: none;
  transition: color var(--transition-fast);
}
.ai-group-head-btn:hover { color: var(--color-text-secondary); }
.ai-group-caret { flex-shrink: 0; opacity: 0.75; }

/* 类型图标：不标出来，侧栏一长串标题里看不出哪条是翻译、哪条是闲聊 */
.ai-conv-mode { flex-shrink: 0; opacity: 0.7; }

/* 类型图标：不标出来，侧栏一长串标题里看不出哪条是翻译、哪条是闲聊 */
.ai-conv-mode { flex-shrink: 0; opacity: 0.7; }
.ai-conv-item.active .ai-conv-mode { opacity: 1; }

.ai-space { margin-bottom: 2px; }

.ai-space { margin-bottom: 2px; }
.ai-space-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 7px 10px 7px 4px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: 13px;
  transition: background var(--transition-fast);
}
.ai-space-row:hover { background: var(--color-bg-hover); }
.ai-space-row .ai-conv-del,
.ai-space-row .ai-conv-add { flex-shrink: 0; }
.ai-space-row:hover .ai-conv-del,
.ai-space-row:hover .ai-conv-add { display: flex; }
.ai-space-caret { flex-shrink: 0; opacity: 0.6; }
.ai-space-icon { flex-shrink: 0; color: var(--color-accent); opacity: 0.85; }
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
  font-size: 11px;
  color: var(--color-text-disabled);
  background: var(--color-bg-tertiary);
  border-radius: 8px;
  padding: 0 6px;
}
.ai-conv-item.is-in-space { margin-left: 18px; padding: 6px 10px; font-size: 12.5px; }
.ai-space-empty { padding: 4px 10px 8px 28px; font-size: 11px; color: var(--color-text-disabled); }

.ai-conv-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 10px;
  border-radius: var(--radius-md);
  cursor: pointer;
  color: var(--color-text-secondary);
  font-size: 13px;
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

/* 消息命中的行比普通任务行高：标题下面还要放一行片段。
   片段限两行——侧栏是导航不是阅读器，一条工具输出能占几十行。 */
.ai-conv-item.is-msg-hit { align-items: flex-start; padding: 7px 10px; }
.ai-conv-item.is-msg-hit .ai-conv-mode { margin-top: 3px; }
.ai-msg-hit-body { flex: 1; min-width: 0; }
.ai-msg-hit-head { display: flex; align-items: center; gap: 4px; }
/* 「还有 N 条」用数字而不是一条分隔线：同一段话被追问、被复述都会重复命中，
   这个数说明「这条对话里提过好几次」，是选哪条进去的关键。 */
.ai-msg-hit-count {
  flex: none; font-size: 10px; color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
}
.ai-msg-hit-snippet {
  margin-top: 2px; font-size: 11px; line-height: 1.45; color: var(--color-text-disabled);
  overflow: hidden; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
}
.ai-conv-item.active .ai-msg-hit-snippet { color: var(--color-text-secondary); }
.ai-conv-del {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: var(--radius-sm);
}
.ai-conv-item:hover .ai-conv-del { display: flex; }
.ai-conv-del:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.ai-conv-ren,
.ai-conv-add {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: var(--radius-sm);
}
.ai-conv-item:hover .ai-conv-ren,
.ai-conv-item:hover .ai-conv-add { display: flex; }
/* 已星标的行：按钮常驻并上色。按 hover 才出现的话，「哪条收藏过」要逐条扫过去才知道。 */
.ai-conv-ren.is-starred { display: flex; color: var(--color-warning, var(--color-accent)); }
.ai-conv-ren:hover,
.ai-conv-add:hover { color: var(--color-text-primary); background: var(--color-bg-tertiary); }
.ai-conv-add:hover { color: var(--color-accent); }
/* 行内编辑：撑满标题那一格，且必须能盖住行本身的点击（改选中了会话就白改了） */
.ai-conv-rename {
  flex: 1;
  min-width: 0;
  padding: 0 4px;
  font: inherit;
  font-size: 13px;
  color: var(--color-text-primary);
  background: var(--color-bg-primary);
  border: 1px solid var(--color-accent);
  border-radius: var(--radius-sm);
  outline: none;
}
.ai-conv-tokens {
  font-size: 10px; color: var(--color-text-disabled); white-space: nowrap;
  margin-right: 4px; font-variant-numeric: tabular-nums;
}
.ai-conv-empty { padding: 24px 12px; text-align: center; color: var(--color-text-disabled); font-size: 12px; }

/* 会话搜索：与新建按钮同宽，同一视觉层级 */
.ai-conv-search {
  display: flex; align-items: center; gap: 5px;
  margin: 6px 8px 2px; padding: 0 7px; height: 26px;
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); flex-shrink: 0;
}
.ai-conv-search:focus-within { border-color: var(--color-accent); }
.ai-conv-search-icon { color: var(--color-text-disabled); flex-shrink: 0; }
.ai-conv-search-input {
  flex: 1; min-width: 0; border: none; outline: none; background: none;
  color: var(--color-text-primary); font-size: 11px; font-family: inherit;
}
.ai-conv-search-input::placeholder { color: var(--color-text-disabled); }
.ai-conv-search-clear {
  display: flex; padding: 0; border: none; background: none;
  color: var(--color-text-disabled); cursor: pointer;
}
.ai-conv-search-clear:hover { color: var(--color-text-primary); }
/* 加载更多：整行按钮，不要做成小链接——点中的概率太低 */
.ai-conv-more {
  margin: 2px 8px 8px; padding: 5px 0; flex-shrink: 0;
  border: 1px dashed var(--color-border); border-radius: var(--radius-sm);
  background: none; color: var(--color-text-muted); font-size: 11px;
  font-family: inherit; cursor: pointer;
}
.ai-conv-more:hover { color: var(--color-text-primary); border-color: var(--color-accent); }
/* 空间内的「更多」跟着任务缩进，否则会和空间名对齐，看着像另一个空间 */
.ai-conv-more.is-in-space { margin-left: 26px; margin-right: 0; }
.ai-space-row:hover { background: var(--color-bg-hover); }
/* 空间行复用了任务行的改名/删除按钮，但「hover 才显示」那条规则是按 .ai-conv-item 写的
   —— 空间行不是 .ai-conv-item，于是这两个按钮一直是 display:none：
   用户根本删不掉、也改不了空间名。这里补上同一条显示规则。 */
.ai-space-row .ai-conv-ren,
.ai-space-row .ai-conv-del,
.ai-space-row .ai-conv-add { flex-shrink: 0; }
.ai-space-row:hover .ai-conv-ren,
.ai-space-row:hover .ai-conv-del,
.ai-space-row:hover .ai-conv-add { display: flex; }
</style>
