<script setup lang="ts">
/**
 * 条目 ↔ 笔记 ↔ 待办 的正向关联 / 反向链接面板，三处编辑界面共用。
 *
 * 关联数据走独立 service，不塞进任何实体的保存 payload：后端 UpdateItem /
 * UpdateNoteDoc 有字段白名单，混进去只会静默丢弃或覆盖别的字段。
 * 跳转一律复用 bridge 里已有的深链（pendingOpenNoteId / pendingOpenTodoId /
 * pendingTarget），不另造事件或全局 ref。
 */
import { ref, computed, watch, inject, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { debounce } from '../utils/debounce'
import { matchesTextOrPinyin } from '../utils/pinyin'
import { LINK_TARGETS_INVALIDATED } from '../constants/linkEvents'
import { pendingOpenNoteId, pendingOpenTodoId, navigate, navigateTo } from '../composables/bridge'
import {
  ListEntityLinks,
  ListEntityBacklinks,
  AddEntityLink,
  RemoveEntityLink,
  ListLinkTargets,
  SearchLinkTargets,
  ResolveWikiTarget,
} from '../../bindings/quickdock/services/appservice'
import { Link2, FileText, ListTodo, Box, Trash2, Search, Plus, ArrowUpRight, ChevronDown } from '@lucide/vue'
import type { EntityLink, EntityType, LinkTarget, LinkTargetGroups, ToastAPI, WikiTarget } from '../types'

import { reportError } from '../utils/reportError'
const props = withDefaults(defineProps<{
  srcType: EntityType
  srcId: string
  srcLabel?: string
  /** 高度吃紧的宿主（笔记编辑区）：默认收成一行，展开用浮层，不挤占正文高度 */
  compact?: boolean
}>(), { srcLabel: '', compact: false })

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 反链行的来源标题由服务端补在 srcLabel 上（边表本身没有来源快照）；
// 目标已被删除时才拿不到，走「已删除」降级，绝不抛错。
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

// manual 与 wiki 边可能指向同一个目标（界面上加过，正文里又写了 [[...]]）：
// 唯一索引含 kind，两条都会留着。不合并的话，同一标题会在「正向关联」和
// 「来自正文」里各出现一次，看着像重复关联了两次。
function linkKey(dstType: string, dstId: string, dstLabel: string): string {
  // 有 id 就只按 id 合并：两篇同名笔记是两条不同的边，不能被标题错并到一起。
  // 只有悬空 wiki 边（写 [[标题]] 当时没解析出目标，dstId 为空）才退化到标题。
  const id = (dstId || '').trim()
  return dstType + '\x00' + (id || 'label:' + dstLabel)
}

const manual = computed(() => links.value.filter(l => l.kind !== 'wiki'))
const wikiAll = computed(() => links.value.filter(l => l.kind === 'wiki'))
const manualKeys = computed(() => new Set(manual.value.map(l => linkKey(l.dstType, l.dstId, l.dstLabel))))
const wikiKeySet = computed(() => new Set(wikiAll.value.map(l => linkKey(l.dstType, l.dstId, l.dstLabel))))
// 「来自正文」只列手动关联没覆盖到的目标
const wiki = computed(() =>
  wikiAll.value.filter(l => !manualKeys.value.has(linkKey(l.dstType, l.dstId, l.dstLabel))),
)
// 手动行如果正文也提到了，加角标说明还有一条 wiki 边
function alsoInBody(l: EntityLink): boolean {
  return wikiKeySet.value.has(linkKey(l.dstType, l.dstId, l.dstLabel))
}

const flatTargets = computed<Candidate[]>(() => [
  ...(targets.value.notes || []).map(x => ({ ...x, type: 'note' as EntityType })),
  ...(targets.value.todos || []).map(x => ({ ...x, type: 'todo' as EntityType })),
  ...(targets.value.items || []).map(x => ({ ...x, type: 'item' as EntityType })),
])

// 一级筛选：按类型分档。三类候选混在一个小列表里时，每行前面重复的「笔记 / 待办」
// 类型字样要占掉一截宽度，条目一多就只能靠肉眼逐行比对，扫视成本很高。
// 这里先选类型、再在类型内搜索，等于把平铺列表拆成两级；
// 不用 hover 飞出菜单，是因为候选容器本身就是一百多像素的滚动区，浮层会被裁掉。
const activeType = ref<'all' | EntityType>('all')

const typeTabs = computed(() => {
  const n = {
    note: targets.value.notes?.length || 0,
    todo: targets.value.todos?.length || 0,
    item: targets.value.items?.length || 0,
  }
  const all = [{ key: 'all' as const, label: t('linkTypeAll'), n: n.note + n.todo + n.item }]
  return [
    ...all,
    { key: 'note' as EntityType, label: t('linkTypeNote'), n: n.note },
    { key: 'todo' as EntityType, label: t('linkTypeTodo'), n: n.todo },
    { key: 'item' as EntityType, label: t('linkTypeItem'), n: n.item },
  ].filter(x => x.key === 'all' || x.n > 0)
})

// 只选了一个类型时就没必要留「全部」这一档，直接落到该类型，少一次点击。
function autoPickType() {
  const rest = typeTabs.value.filter(x => x.key !== 'all')
  activeType.value = rest.length === 1 ? (rest[0].key as EntityType) : 'all'
}

// 候选懒加载：候选是三类全量集合（各上限 2000），而面板默认收起，多数时候根本用不到。
// 改成第一次打开候选时才拉，之后 60s 内复用缓存；原来每次切换实体都拉一遍，
// 在笔记页来回切文档时纯属白搬几百 KB。
const targetsLoaded = ref(false)
const targetsLoading = ref(false)
let targetsAt = 0
const targetsTTL = 60_000
// 写操作后主动失效用的轻量信号（常量定义在 constants/linkEvents.ts，派发方共用）。
// 新建笔记/待办/条目成功后宿主 dispatch 一次这个事件，下次打开候选就会重拉，
// 而不是吃 TTL 内的旧全量集合（服务端补齐仍由 SearchLinkTargets 负责）。

async function ensureTargets() {
  if (targetsLoading.value) return
  if (targetsLoaded.value && Date.now() - targetsAt < targetsTTL) return
  targetsLoading.value = true
  try {
    targets.value = unwrap<LinkTargetGroups>(await ListLinkTargets()) || { notes: [], todos: [], items: [] }
    targetsLoaded.value = true
    targetsAt = Date.now()
    // 档位依赖各类型的候选数量，候选到位后才能判断该不该收成单档
    if (!typeTabs.value.some(x => x.key === activeType.value)) autoPickType()
  } catch (e) {
    reportError(e, 'EntityLinkPicker.ensureTargets')
  } finally {
    targetsLoading.value = false
  }
}

// 服务端搜索：本地候选有 2000/类 的上限，第 2001 条起本地再怎么筛也筛不到
// （表现为「明明有这篇笔记，就是搜不出来」）。输入变化时防抖补一次服务端
// 标题 LIKE，结果并到本地命中之后。拼音匹配仍只作用于本地集合。
const remoteHits = ref<Candidate[]>([])
let searchSeq = 0

/** 让候选缓存立即过期：宿主新建实体后调用，或收到失效事件时自动触发。 */
function invalidateTargets() {
  targetsLoaded.value = false
  targetsAt = 0
  remoteHits.value = []
}

async function runRemoteSearch() {
  const kw = query.value.trim()
  if (!kw) { remoteHits.value = []; return }
  const seq = ++searchSeq
  try {
    const typ = activeType.value === 'all' ? '' : activeType.value
    const res = unwrap<LinkTargetGroups>(await SearchLinkTargets(kw, typ, 50))
    if (seq !== searchSeq) return // 后发先至的旧结果直接丢弃
    remoteHits.value = [
      ...(res?.notes || []).map(x => ({ ...x, type: 'note' as EntityType })),
      ...(res?.todos || []).map(x => ({ ...x, type: 'todo' as EntityType })),
      ...(res?.items || []).map(x => ({ ...x, type: 'item' as EntityType })),
    ]
  } catch {
    // 服务端搜索只是补齐手段，失败时本地结果照常可用，不打断输入
  }
}

// 250ms 输入防抖：每敲一个字都会打一次服务端 LIKE。
const scheduledRemoteSearch = debounce(() => { void runRemoteSearch() }, 250)

function scheduleRemoteSearch() {
  // 清空输入要立刻撤掉旧结果，不能等 250ms 后自己发现——那段时间里
  // 列表还挂着上一次的关键词命中，看起来像没响应。
  if (!query.value.trim()) {
    scheduledRemoteSearch.cancel()
    remoteHits.value = []
    return
  }
  scheduledRemoteSearch()
}

function openCands() {
  candsOpen.value = true
  void ensureTargets()
  if (query.value.trim()) scheduleRemoteSearch()
}

const scopedTargets = computed(() =>
  activeType.value === 'all' ? flatTargets.value : flatTargets.value.filter(c => c.type === activeType.value),
)
const filteredTargets = computed(() => {
  const local = scopedTargets.value.filter(c =>
    matchesTextOrPinyin([c.label], query.value, 'link:' + c.type + ':' + c.id),
  )
  if (!query.value.trim() || !remoteHits.value.length) return local
  const seen = new Set(local.map(c => c.type + ':' + c.id))
  const extra = remoteHits.value.filter(c =>
    (activeType.value === 'all' || c.type === activeType.value) && !seen.has(c.type + ':' + c.id),
  )
  return [...local, ...extra]
})

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
    const [a, b] = await Promise.all([
      ListEntityLinks(props.srcType, props.srcId),
      ListEntityBacklinks(props.srcType, props.srcId, props.srcLabel || ''),
    ])
    links.value = unwrap<EntityLink[]>(a) || []
    backlinks.value = unwrap<BacklinkRow[]>(b) || []
  } catch (e) {
    reportError(e, 'EntityLinkPicker.loadAll')
  } finally {
    loading.value = false
  }
}

// 切换实体时把 compact 浮层收回去：不然在 A 文档点开后切到 B，浮层会一直盖在正文上
watch(() => [props.srcType, props.srcId], () => { expanded.value = false; void loadAll() }, { immediate: true })

// 展开面板就预取候选：类型档位的数量依赖它，等用户点到搜索框才拉会先闪一下空档。
// 仍然只在「真的要用」时才请求 —— 面板默认收起，绝大多数笔记页停留都不会触发。
watch(expanded, (v) => { if (v) void ensureTargets() })

// compact 宿主（笔记编辑区）里展开的是浮层，压在正文上：点正文或别处就该收回，
// 否则只能再点一次标题，浮层一直挡着。非 compact 是表单内的一段，用户还要在里面操作，不做点外收起。
const rootEl = ref<HTMLElement | null>(null)
function onDocMouseDown(e: MouseEvent) {
  if (!props.compact || !expanded.value) return
  const target = e.target as Node | null
  if (!target || rootEl.value?.contains(target)) return
  expanded.value = false
}
onMounted(() => {
  document.addEventListener('mousedown', onDocMouseDown, true)
  window.addEventListener(LINK_TARGETS_INVALIDATED, invalidateTargets)
})
onUnmounted(() => {
  document.removeEventListener('mousedown', onDocMouseDown, true)
  window.removeEventListener(LINK_TARGETS_INVALIDATED, invalidateTargets)
  scheduledRemoteSearch.cancel()
})

// 供宿主在保存新实体后直接调 ref.value.invalidateTargets()，无需派发 DOM 事件。
defineExpose({ invalidateTargets })

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
    reportError(e, 'EntityLinkPicker.addLink')
  }
}

async function removeLink(id: string) {
  try {
    unwrap(await RemoveEntityLink(id))
    await loadAll()
  } catch (e) {
    reportError(e, 'EntityLinkPicker.removeLink')
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
      reportError(e, 'EntityLinkPicker.openLink')
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
  <div ref="rootEl" class="elp" :class="{ 'elp-compact': compact, 'elp-open': expanded }">
    <div v-key-click
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
            @focus="openCands"
            @blur="candsOpen = false"
            @keydown.esc="candsOpen = false"
          />
          <button type="button" class="elp-add-btn qd-btn" :disabled="!selectedTarget" :title="t('add')" @click="addLink">
            <Plus :size="13" />
          </button>
        </div>
        <!-- mousedown.prevent：点候选时不让输入框失焦（否则 blur 先收起列表，click 就丢掉了） -->
        <div v-if="candsOpen" class="elp-cands" @mousedown.prevent>
          <!-- 一级：先按类型收窄。tab 常驻，不藏进下拉，切换只花一次点击。
               只有一类候选时纯属多余，直接不渲染。 -->
          <div v-if="typeTabs.length > 1" class="elp-tabs">
            <button
              v-for="tab in typeTabs"
              :key="tab.key"
              type="button"
              class="elp-tab"
              :class="{ on: activeType === tab.key }"
              @click="activeType = tab.key"
            >{{ tab.label }}<span class="elp-tab-n">{{ tab.n }}</span></button>
          </div>
          <div v-if="!filteredTargets.length" class="elp-empty qd-empty">{{ t('linkEmpty') }}</div>
          <button
            v-for="c in filteredTargets"
            :key="c.type + ':' + c.id"
            type="button"
            class="elp-cand"
            :class="{ 'is-picked': !!selectedTarget && selectedTarget.type === c.type && selectedTarget.id === c.id }"
            @click="pickCandidate(c)"
          >
            <component :is="iconFor(c.type)" :size="12" />
            <!-- 已按类型收窄时类型名是废话，让位给标题 -->
            <span v-if="activeType === 'all'" class="elp-cand-type">{{ typeLabel(c.type) }}</span>
            <span class="elp-cand-label">{{ c.label }}</span>
            <!-- 重名靠 hint 区分：同名日报看父文件夹，同名待办看到期日 -->
            <span v-if="c.hint" class="elp-cand-hint">{{ c.hint }}</span>
          </button>
        </div>
      </div>

      <div class="elp-sec">{{ t('linkForward') }}<span class="elp-n">{{ manual.length + wiki.length }}</span></div>
      <div v-if="!manual.length && !wiki.length" class="elp-empty qd-empty">{{ t('linkEmpty') }}</div>
      <div v-key-click v-for="l in manual" :key="'m-' + l.id" class="elp-row" @click="openLink(l)">
        <component :is="iconFor(l.dstType)" :size="12" class="elp-row-icon" />
        <span class="elp-type">{{ typeLabel(l.dstType) }}</span>
        <span class="elp-label">{{ l.dstLabel || t('deleted') }}</span>
        <!-- 正文里也写了 [[...]]：只剩这一条，但标出来省得用户以为漏了 -->
        <span v-if="alsoInBody(l)" class="elp-wiki-mark" :title="t('linkFromBody')">[[]]</span>
        <button type="button" class="elp-del" :title="t('delete')" @click.stop="removeLink(l.id)">
          <Trash2 :size="12" />
        </button>
      </div>
      <div v-if="wiki.length" class="elp-sub">{{ t('linkFromBody') }}</div>
      <div v-key-click v-for="l in wiki" :key="'w-' + l.id" class="elp-row is-wiki" @click="openLink(l)">
        <component :is="iconFor(l.dstType)" :size="12" class="elp-row-icon" />
        <span class="elp-type">{{ typeLabel(l.dstType) }}</span>
        <span class="elp-label">{{ l.dstLabel || t('deleted') }}</span>
      </div>

      <div class="elp-sec">{{ t('linkBacklinks') }}<span class="elp-n">{{ backlinks.length }}</span></div>
      <div v-if="!backlinks.length" class="elp-empty qd-empty">{{ t('linkEmpty') }}</div>
      <div v-key-click v-for="b in backlinks" :key="'b-' + b.id" class="elp-row" @click="openBacklink(b)">
        <ArrowUpRight :size="12" class="elp-row-icon" />
        <component :is="iconFor(b.srcType)" :size="12" class="elp-row-icon" />
        <span class="elp-type">{{ typeLabel(b.srcType) }}</span>
        <span class="elp-label">{{ backlinkLabel(b) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.elp { border-top: 1px solid var(--color-border); background: var(--color-bg-secondary); font-size: var(--text-sm); color: var(--color-text-secondary); }
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
.elp-count { color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
/* compact：高度吃紧的宿主（笔记编辑区）专用。默认只剩一行摘要，
   展开的内容浮在正文上、不占文档流高度 —— 否则空面板也能吃掉一百多像素。 */
.elp-compact { position: relative; }
.elp-compact > .elp-head { padding: var(--space-2) var(--space-5); }
.elp-hint { color: var(--color-text-muted); font-size: var(--text-xs); }
.elp-chev { flex-shrink: 0; color: var(--color-text-muted); transition: transform var(--transition-fast); }
.elp-chev.on { transform: rotate(180deg); }
.elp-compact.elp-open .elp-scroll {
  position: absolute; top: 100%; left: 0; right: 0; z-index: 20;
  max-height: 280px; padding: var(--space-4) var(--space-5) 10px;
  border: 1px solid var(--popover-border-color); border-top: none;
  border-radius: 0 0 var(--radius-md) var(--radius-md);
  background: var(--color-popover-bg); box-shadow: var(--shadow-md);
}

/* 整个面板只有一个滚动容器：三个页面都要嵌在既有布局里（弹窗/编辑面板/文档头部），
   高度必须可预期，不能让关联列表把编辑区或弹窗撑爆。 */
.elp-scroll { max-height: 220px; overflow-y: auto; padding: 0 var(--space-4) var(--space-3); }

.elp-add-row { display: flex; align-items: center; gap: var(--space-2); position: relative; }
.elp-search-icon { color: var(--color-text-muted); flex-shrink: 0; }
.elp-add-input { flex: 1; min-width: 0; padding: var(--space-2) var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: var(--text-sm); outline: none; font-family: inherit; }
.elp-add-input:focus { border-color: var(--color-border-focus-soft); }
.elp-add-btn { flex-shrink: 0; display: flex; align-items: center; justify-content: center; width: 26px; height: 26px; border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-tertiary); color: var(--color-text-secondary); cursor: pointer; }
.elp-add-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent-border); }
.elp-add-btn:disabled { color: var(--color-text-disabled); cursor: default; }

.elp-cands { max-height: 132px; overflow-y: auto; margin-top: var(--space-2); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-bg-primary); }
/* 一级类型栏吸顶：候选一多要滚，类型档位不能跟着滚出去 */
.elp-tabs { position: sticky; top: 0; display: flex; gap: 2px; padding: 4px; background: var(--color-bg-primary); border-bottom: 1px solid var(--color-border); }
.elp-tab { display: inline-flex; align-items: center; gap: 4px; padding: 2px 8px; border: none; border-radius: var(--radius-xs); background: transparent; color: var(--color-text-muted); font-family: inherit; font-size: var(--text-xs); cursor: pointer; }
.elp-tab:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.elp-tab.on { background: var(--color-accent-bg); color: var(--color-accent); }
.elp-tab-n { color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
.elp-tab.on .elp-tab-n { color: inherit; opacity: 0.7; }
.elp-cand { display: flex; align-items: center; gap: var(--space-2); width: 100%; padding: var(--space-2) var(--space-3); border: none; background: transparent; color: var(--color-text-secondary); text-align: left; cursor: pointer; font-family: inherit; font-size: var(--text-sm); }
.elp-cand:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.elp-cand.is-picked { background: var(--color-accent-bg); color: var(--color-accent); }
.elp-cand-type { flex-shrink: 0; color: var(--color-text-muted); }
.elp-cand-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
/* 次要信息（父文件夹 / 到期日 / 所属集合）：只用来区分重名，不该抢标题的宽度 */
.elp-cand-hint { flex-shrink: 0; max-width: 42%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-text-muted); font-size: var(--text-xs); }
/* 「正文也写了 [[...]]」角标 */
.elp-wiki-mark { flex-shrink: 0; color: var(--color-text-muted); font-size: var(--text-xs); }

.elp-sec { display: flex; align-items: center; gap: var(--space-2); margin-top: var(--space-3); padding-bottom: var(--space-1); color: var(--color-text-muted); font-weight: 600; }
.elp-n { color: var(--color-text-muted); font-weight: 500; }
.elp-sub { margin-top: var(--space-2); color: var(--color-text-muted); }
.elp-empty { padding: var(--space-2) var(--space-1); color: var(--color-text-muted); }

.elp-row { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) var(--space-1); border-radius: var(--radius-xs); cursor: pointer; }
.elp-row:hover { background: var(--color-bg-hover); }
.elp-row.is-wiki { cursor: default; }
.elp-row-icon { flex-shrink: 0; color: var(--color-text-muted); }
.elp-type { flex-shrink: 0; color: var(--color-text-muted); }
.elp-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-text-primary); }
.elp-del { flex-shrink: 0; display: flex; align-items: center; justify-content: center; width: 20px; height: 20px; border: none; border-radius: var(--radius-xs); background: transparent; color: var(--color-text-muted); cursor: pointer; }
.elp-del:hover { background: var(--color-danger-bg); color: var(--color-danger-ink); }
</style>
