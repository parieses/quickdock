<script setup lang="ts">
/**
 * 条目 ↔ 笔记 ↔ 待办 的正向关联 / 反向链接面板，三处编辑界面共用。
 *
 * 关联数据走独立 service，不塞进任何实体的保存 payload：后端 UpdateItem /
 * UpdateNoteDoc 有字段白名单，混进去只会静默丢弃或覆盖别的字段。
 * 跳转一律复用 bridge 里已有的深链（pendingOpenNoteId / pendingOpenTodoId /
 * pendingTarget），不另造事件或全局 ref。
 */
import { ref, computed, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { matchesTextOrPinyin } from '../utils/pinyin'
import { pendingOpenNoteId, pendingOpenTodoId, navigate, navigateTo } from '../composables/bridge'
import {
  ListEntityLinks,
  ListEntityBacklinks,
  AddEntityLink,
  RemoveEntityLink,
  ListLinkTargets,
  ResolveWikiTarget,
} from '../../bindings/quickdock/services/appservice'
import { Link2, FileText, ListTodo, Box, Trash2, Search, Plus, ArrowUpRight, ChevronDown } from '@lucide/vue'
import type { EntityLink, EntityType, LinkTarget, LinkTargetGroups, ToastAPI, WikiTarget } from '../types'

const props = withDefaults(defineProps<{
  srcType: EntityType
  srcId: string
  srcLabel?: string
  /** 高度吃紧的宿主（笔记编辑区）：默认收成一行，展开用浮层，不挤占正文高度 */
  compact?: boolean
}>(), { srcLabel: '', compact: false })

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 反链行的来源标题不在 EntityLink 出边表里，后端若补字段会带在这些可选键上；
// 都拿不到就按「已删除」降级，绝不抛错。
type BacklinkRow = EntityLink & { srcLabel?: string; label?: string; title?: string; name?: string }
type Candidate = LinkTarget & { type: EntityType }

const links = ref<EntityLink[]>([])
const backlinks = ref<BacklinkRow[]>([])
const targets = ref<LinkTargetGroups>({ notes: [], todos: [], items: [] })
const loading = ref(false)
const query = ref('')
const selectedTarget = ref<Candidate | null>(null)
// 候选默认收起：三处宿主里笔记页的编辑区最吃高度，不能让一个还没用的下拉常驻占位。
const candsOpen = ref(false)
// 关联面板默认收起：三处宿主（条目编辑、待办弹窗、笔记编辑区）里它都排在表单末尾，
// 展开态会把搜索框 + 前向链接 + 反链一口气铺开，没有关联的实体也占掉小半屏高度。
// 收起时只留一行标题 + 计数，点标题才展开。
const expanded = ref(false)
const canToggle = computed(() => !!props.srcId)

function toggle() {
  if (!canToggle.value) return
  expanded.value = !expanded.value
}

const manual = computed(() => links.value.filter(l => l.kind !== 'wiki'))
const wiki = computed(() => links.value.filter(l => l.kind === 'wiki'))

const flatTargets = computed<Candidate[]>(() => [
  ...(targets.value.notes || []).map(x => ({ ...x, type: 'note' as EntityType })),
  ...(targets.value.todos || []).map(x => ({ ...x, type: 'todo' as EntityType })),
  ...(targets.value.items || []).map(x => ({ ...x, type: 'item' as EntityType })),
])
const filteredTargets = computed(() =>
  flatTargets.value.filter(c => matchesTextOrPinyin([c.label], query.value, 'link:' + c.type + ':' + c.id)),
)

function iconFor(type: string) {
  if (type === 'note') return FileText
  if (type === 'todo') return ListTodo
  if (type === 'item') return Box
  return Link2
}

function typeLabel(type: string): string {
  if (type === 'note') return t('linkTypeNote')
  if (type === 'todo') return t('linkTypeTodo')
  if (type === 'item') return t('linkTypeItem')
  return type
}

function backlinkLabel(b: BacklinkRow): string {
  return b.srcLabel || b.label || b.title || b.name || t('deleted')
}

/** 三类目标统一的深链映射；返回 false 表示类型不认识或 id 为空。 */
function jump(type: string, id: string): boolean {
  if (!id) return false
  if (type === 'note') { pendingOpenNoteId.value = id; navigate('notes'); return true }
  if (type === 'todo') { pendingOpenTodoId.value = id; navigate('todo'); return true }
  if (type === 'item') { navigateTo('workspace', 'item:' + id); return true }
  return false
}

async function loadAll() {
  // 切到另一个实体（或新建未保存）时，上一次选中的候选必须清零：
  // 否则在 A 里选中一条、切到 B 直接点「添加」会把 A 的目标挂到 B 上。
  selectedTarget.value = null
  query.value = ''
  candsOpen.value = false
  // 新建未保存：没有 srcId 时既不能查也不能写，交给模板显示禁用提示。
  if (!props.srcId) { links.value = []; backlinks.value = []; return }
  loading.value = true
  try {
    const [a, b, c] = await Promise.all([
      ListEntityLinks(props.srcType, props.srcId),
      ListEntityBacklinks(props.srcType, props.srcId, props.srcLabel || ''),
      ListLinkTargets(),
    ])
    links.value = unwrap<EntityLink[]>(a) || []
    backlinks.value = unwrap<BacklinkRow[]>(b) || []
    targets.value = unwrap<LinkTargetGroups>(c) || { notes: [], todos: [], items: [] }
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

// 切换实体时把 compact 浮层收回去：不然在 A 文档点开后切到 B，浮层会一直盖在正文上
watch(() => [props.srcType, props.srcId], () => { expanded.value = false; void loadAll() }, { immediate: true })

function pickCandidate(c: Candidate) {
  selectedTarget.value = { type: c.type, id: c.id, label: c.label }
  candsOpen.value = false
}

async function addLink() {
  const c = selectedTarget.value
  if (!c || !props.srcId) return
  try {
    unwrap(await AddEntityLink(props.srcType, props.srcId, c.type, c.id, c.label))
    selectedTarget.value = null
    query.value = ''
    candsOpen.value = false
    await loadAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

async function removeLink(id: string) {
  try {
    unwrap(await RemoveEntityLink(id))
    await loadAll()
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

async function openLink(l: EntityLink) {
  if (l.dstId && jump(l.dstType, l.dstId)) return
  // wiki 出边可能只有 dstLabel（正文里的 [[...]] 未解析出 id）：按标题回查一次。
  if (l.dstLabel) {
    try {
      const target = unwrap<WikiTarget | null>(await ResolveWikiTarget(l.dstLabel))
      if (target && jump(target.type, target.id)) return
    } catch (e) {
      toast.error(getErrorMessage(e))
      return
    }
  }
  toast.error(t('wikiTargetNotFound'))
}

function openBacklink(b: BacklinkRow) {
  // 来源实体已被删除时 srcId 为空：标签已显示「已删除」，点击静默忽略即可。
  if (b.srcId) jump(b.srcType, b.srcId)
}
</script>

<template>
  <div class="elp" :class="{ 'elp-compact': compact, 'elp-open': expanded }">
    <div
      class="elp-head"
      :class="{ 'is-toggle': canToggle }"
      :role="canToggle ? 'button' : undefined"
      :tabindex="canToggle ? 0 : undefined"
      :aria-expanded="canToggle ? expanded : undefined"
      @click="toggle"
      @keydown.enter="toggle"
    >
      <Link2 :size="13" class="elp-head-icon" />
      <span class="elp-title">{{ t('linkTitle') }}</span>
      <span v-if="loading" class="elp-loading">{{ t('loading') }}</span>
      <span v-if="!srcId" class="elp-hint">{{ t('linkSaveFirst') }}</span>
      <span v-if="srcId" class="elp-count">{{ manual.length + wiki.length }} / {{ backlinks.length }}</span>
      <ChevronDown v-if="canToggle" :size="12" class="elp-chev" :class="{ on: expanded }" />
    </div>

    <div v-if="srcId && expanded" class="elp-scroll">
      <div class="elp-add">
        <div class="elp-add-row">
          <Search :size="12" class="elp-search-icon" />
          <input
            v-model="query"
            class="elp-add-input"
            :placeholder="t('search')"
            spellcheck="false"
            @focus="candsOpen = true"
            @blur="candsOpen = false"
            @keydown.esc="candsOpen = false"
          />
          <button type="button" class="elp-add-btn" :disabled="!selectedTarget" :title="t('add')" @click="addLink">
            <Plus :size="13" />
          </button>
        </div>
        <!-- mousedown.prevent：点候选时不让输入框失焦（否则 blur 先收起列表，click 就丢掉了） -->
        <div v-if="candsOpen" class="elp-cands" @mousedown.prevent>
          <div v-if="!filteredTargets.length" class="elp-empty">{{ t('linkEmpty') }}</div>
          <button
            v-for="c in filteredTargets"
            :key="c.type + ':' + c.id"
            type="button"
            class="elp-cand"
            :class="{ 'is-picked': !!selectedTarget && selectedTarget.type === c.type && selectedTarget.id === c.id }"
            @click="pickCandidate(c)"
          >
            <component :is="iconFor(c.type)" :size="12" />
            <span class="elp-cand-type">{{ typeLabel(c.type) }}</span>
            <span class="elp-cand-label">{{ c.label }}</span>
          </button>
        </div>
      </div>

      <div class="elp-sec">{{ t('linkForward') }}<span class="elp-n">{{ manual.length + wiki.length }}</span></div>
      <div v-if="!manual.length && !wiki.length" class="elp-empty">{{ t('linkEmpty') }}</div>
      <div v-for="l in manual" :key="'m-' + l.id" class="elp-row" @click="openLink(l)">
        <component :is="iconFor(l.dstType)" :size="12" class="elp-row-icon" />
        <span class="elp-type">{{ typeLabel(l.dstType) }}</span>
        <span class="elp-label">{{ l.dstLabel || t('deleted') }}</span>
        <button type="button" class="elp-del" :title="t('delete')" @click.stop="removeLink(l.id)">
          <Trash2 :size="12" />
        </button>
      </div>
      <div v-if="wiki.length" class="elp-sub">{{ t('linkFromBody') }}</div>
      <div v-for="l in wiki" :key="'w-' + l.id" class="elp-row is-wiki" @click="openLink(l)">
        <component :is="iconFor(l.dstType)" :size="12" class="elp-row-icon" />
        <span class="elp-type">{{ typeLabel(l.dstType) }}</span>
        <span class="elp-label">{{ l.dstLabel || t('deleted') }}</span>
      </div>

      <div class="elp-sec">{{ t('linkBacklinks') }}<span class="elp-n">{{ backlinks.length }}</span></div>
      <div v-if="!backlinks.length" class="elp-empty">{{ t('linkEmpty') }}</div>
      <div v-for="b in backlinks" :key="'b-' + b.id" class="elp-row" @click="openBacklink(b)">
        <ArrowUpRight :size="12" class="elp-row-icon" />
        <component :is="iconFor(b.srcType)" :size="12" class="elp-row-icon" />
        <span class="elp-type">{{ typeLabel(b.srcType) }}</span>
        <span class="elp-label">{{ backlinkLabel(b) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.elp { border-top: 1px solid var(--color-border); background: var(--color-bg-secondary); font-size: 12px; color: var(--color-text-secondary); }
/* 弹窗/表单里的宿主（非 compact）不铺二级底色：宿主已经是 popover-bg，
   再叠一层更暗的横带会像挖了个洞；内容也与表单字段左对齐，不二次缩进。 */
.elp:not(.elp-compact) { background: transparent; }
.elp:not(.elp-compact) > .elp-head { padding: var(--space-2) 0; border-radius: var(--radius-sm); }
.elp:not(.elp-compact) > .elp-scroll { padding: 0 0 var(--space-2); }
.elp-head { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-4); }
/* 头部即开关：收起态只有这一行，必须一眼看出可点 */
.elp-head.is-toggle { cursor: pointer; }
.elp-head.is-toggle:hover { background: var(--color-bg-hover); }
.elp-head.is-toggle:focus-visible { outline: 1px solid var(--color-border-focus-soft); }
.elp-head-icon { color: var(--color-text-muted); flex-shrink: 0; }
.elp-title { flex: 1; font-weight: 600; color: var(--color-text-primary); }
.elp-loading { color: var(--color-text-muted); }
.elp-count { color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }
/* compact：高度吃紧的宿主（笔记编辑区）专用。默认只剩一行摘要，
   展开的内容浮在正文上、不占文档流高度 —— 否则空面板也能吃掉一百多像素。 */
.elp-compact { position: relative; }
.elp-compact > .elp-head { padding: 4px 12px; }
.elp-hint { color: var(--color-text-disabled); font-size: var(--text-xs); }
.elp-chev { flex-shrink: 0; color: var(--color-text-muted); transition: transform var(--transition-fast); }
.elp-chev.on { transform: rotate(180deg); }
.elp-compact.elp-open .elp-scroll {
  position: absolute; top: 100%; left: 0; right: 0; z-index: 20;
  max-height: 280px; padding: 8px 12px 10px;
  border: 1px solid var(--popover-border-color); border-top: none;
  border-radius: 0 0 var(--radius-md) var(--radius-md);
  background: var(--color-popover-bg); box-shadow: var(--shadow-md);
}

/* 整个面板只有一个滚动容器：三个页面都要嵌在既有布局里（弹窗/编辑面板/文档头部），
   高度必须可预期，不能让关联列表把编辑区或弹窗撑爆。 */
.elp-scroll { max-height: 220px; overflow-y: auto; padding: 0 var(--space-4) var(--space-3); }

.elp-add-row { display: flex; align-items: center; gap: var(--space-2); position: relative; }
.elp-search-icon { color: var(--color-text-muted); flex-shrink: 0; }
.elp-add-input { flex: 1; min-width: 0; padding: var(--space-2) var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 12px; outline: none; font-family: inherit; }
.elp-add-input:focus { border-color: var(--color-border-focus-soft); }
.elp-add-btn { flex-shrink: 0; display: flex; align-items: center; justify-content: center; width: 26px; height: 26px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-secondary); cursor: pointer; }
.elp-add-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent-border); }
.elp-add-btn:disabled { opacity: 0.4; cursor: default; }

.elp-cands { max-height: 96px; overflow-y: auto; margin-top: var(--space-2); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-primary); }
.elp-cand { display: flex; align-items: center; gap: var(--space-2); width: 100%; padding: var(--space-2) var(--space-3); border: none; background: transparent; color: var(--color-text-secondary); text-align: left; cursor: pointer; font-family: inherit; font-size: 12px; }
.elp-cand:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.elp-cand.is-picked { background: var(--color-accent-bg); color: var(--color-accent); }
.elp-cand-type { flex-shrink: 0; color: var(--color-text-muted); }
.elp-cand-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.elp-sec { display: flex; align-items: center; gap: var(--space-2); margin-top: var(--space-3); padding-bottom: var(--space-1); color: var(--color-text-muted); font-weight: 600; }
.elp-n { color: var(--color-text-disabled); font-weight: 500; }
.elp-sub { margin-top: var(--space-2); color: var(--color-text-disabled); }
.elp-empty { padding: var(--space-2) var(--space-1); color: var(--color-text-disabled); }

.elp-row { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-1); border-radius: var(--radius-xs); cursor: pointer; }
.elp-row:hover { background: var(--color-bg-hover); }
.elp-row.is-wiki { cursor: default; }
.elp-row-icon { flex-shrink: 0; color: var(--color-text-muted); }
.elp-type { flex-shrink: 0; color: var(--color-text-muted); }
.elp-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-text-primary); }
.elp-del { flex-shrink: 0; display: flex; align-items: center; justify-content: center; width: 20px; height: 20px; border: none; border-radius: var(--radius-xs); background: transparent; color: var(--color-text-muted); cursor: pointer; }
.elp-del:hover { background: var(--color-danger-bg); color: var(--color-danger); }
</style>
