<script lang="ts">
// 模块级留存命令面板的定位载荷。这不是新增导航机制：切页仍由 App.vue 监听
// palette:locate 后走 navigateTo(page, target) → pendingTarget；这里只是补存载荷。
//
// 为什么必须在模块顶层、而不是组件内 Events.On：本页不在 KeepAlive 白名单里，
// 应用默认停在 dashboard，从别处点面板结果跳过来时本组件尚未挂载，组件内注册
// 监听会漏掉这一次事件；而 navigateTo 的 target 只有 "scene:<id>" /
// "collection:<id>"，定位必需的 workspaceId / sceneId 带不过来。本文件被
// registry.ts 静态 import，模块顶层随应用启动即注册，与组件挂载时机无关。
import { Events } from '@wailsio/runtime'

let lastPaletteLocate: { id: string; workspaceId: string; sceneId: string } | null = null

function rememberPaletteLocate(ev: any) {
  let p: any = ev?.data ?? ev
  if (typeof p === 'string') {
    try { p = JSON.parse(p) } catch { return }
  }
  if (!p || p.page !== 'workspace' || !p.target) return
  lastPaletteLocate = {
    id: p.id || '',
    workspaceId: p.workspaceId || '',
    sceneId: p.sceneId || '',
  }
}
// HMR 重算模块会重复注册：先摘掉上一次的监听（Events.On 返回 off）。
// 整段包 try/catch：模块顶层在应用启动最早期求值，万一运行时还没就绪，
// 也绝不能因为一个「定位」的边角逻辑把整个界面拖成白屏。
try {
  const g = globalThis as any
  if (typeof g.__qdPaletteLocateOff__ === 'function') g.__qdPaletteLocateOff__()
  g.__qdPaletteLocateOff__ = Events.On('palette:locate', rememberPaletteLocate)
} catch { /* 监听没挂上：定位会降级为静默忽略，不影响切页 */ }
</script>

<script setup lang="ts">
/**
 * 工作空间复合页：首次引导 / 无场景 / 无集合 / 常规三栏 四档状态。
 *
 * 为什么四档留在一个组件里而不是拆四个：它们共用同一份状态（store 的
 * workspaces / scenes / collections 与两个 loading 标记），拆开就得把
 * 4 个 computed 传进来传出去，而每档之间的切换条件正是那些 computed。
 *
 * 为什么从 App.vue 搬出来：App 的内容区原本是「workspace 专属分支 + 注册页
 * <component :is>」两套写法，前者有 4 层嵌套。搬出来之后 App 只剩一套通用渲染，
 * 工作空间在注册表里跟 notes / monitor 等页完全同权 —— 差别只在本组件内部。
 *
 * 布局注意：外层 .page-shell 是 display:contents（见 App.vue），所以本组件的
 * 根节点会直接成为 .app-content（flex column）的子项，得自己撑满并管好纵向排列。
 */
import { computed, defineAsyncComponent, watch, onMounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceStore } from '../stores/workspace'
// 命令面板「点结果 → 落到具体场景/集合」：目标由 bridge 的 pendingTarget 带过来
import { pendingTarget } from '../composables/bridge'
import { unwrap } from '../utils/api'
import { ListAllItems } from '../../bindings/quickdock/services/appservice'
import { ListScenes } from '../../bindings/quickdock/services/scene/sceneservice'
import { ListCollections } from '../../bindings/quickdock/services/collection/collectionservice'
import type { CollectionItem } from '../types'
import SceneTags from './SceneTags.vue'
import SceneRail from './SceneRail.vue'
import CollectionList from './CollectionList.vue'
import ItemList from './ItemList.vue'

// 两档整页引导 + 首次引导只在工作空间出现，异步化避免进主 chunk
const OnboardingPage = defineAsyncComponent(() => import('./OnboardingPage.vue'))
const EmptyGuide = defineAsyncComponent(() => import('./EmptyGuide.vue'))

const store = useWorkspaceStore()
const { t } = useI18n()
const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

// 已选到工作空间、场景列表加载完毕、但一个场景都没有。
// 必须带 !scenesLoading，否则切换工作空间时 scenes 先被清空，引导页会闪一下。
// 这一档要占满整个内容区（连场景栏一起收掉），否则会留下一个空荡荡的场景列。
const showWorkspaceGuide = computed(() =>
  store.workspaces.length > 0 &&
  store.scenes.length === 0 &&
  !store.scenesLoading &&
  !store.hasSearch
)

// 「当前场景下一个集合都没有」这一档：引导页占满集合 + 项目两块区域。
// 只有两个空列表并排、各自写一句「点击上方 +」时，用户看到的是两片空白。
// 同样要带 !collectionsLoading，否则切换场景时 collections 先被清空会闪。
const showCollectionsGuide = computed(() =>
  store.workspaces.length > 0 &&
  !!store.activeSceneId &&
  store.collections.length === 0 &&
  !store.collectionsLoading &&
  !store.hasSearch
)

// ---- 命令面板深链定位（scene / collection / item）----
// target 形如 "scene:<id>" / "collection:<id>" / "item:<id>"；workspaceId / sceneId
// 在事件载荷里，由上面模块级监听留存。按「工作空间 → 场景 → 集合」逐级落下，
// 任一级找不到就静默忽略。item 额外反查所属集合，见 applyItemLocate。
interface LocateReq {
  kind: 'scene' | 'collection' | 'item'
  id: string
  workspaceId: string
  sceneId: string
}
let activeLocate: LocateReq | null = null
let locating = false

/** 解析 target：兼容 "kind:id"，也兼容主 agent 把整包载荷 JSON 化后透传的形式。 */
function parseLocate(raw: string): LocateReq | null {
  let kind = '', id = '', workspaceId = '', sceneId = '', target = raw
  if (raw.trim().startsWith('{')) {
    try {
      const o = JSON.parse(raw) as Record<string, string>
      target = o.target || o.navTarget || ''
      kind = o.kind || ''
      id = o.id || ''
      workspaceId = o.workspaceId || ''
      sceneId = o.sceneId || ''
    } catch { /* 非 JSON，落到下面的 kind:id 解析 */ }
  }
  if (!kind || !id) {
    const m = /^(scene|collection|item):(.+)$/.exec(target)
    if (!m) return null
    kind = m[1]
    id = m[2]
  }
  // 简单形式缺的 workspaceId / sceneId 从事件载荷补齐（按 id 对齐，别用到上一条的）
  if (lastPaletteLocate && lastPaletteLocate.id === id) {
    if (!workspaceId) workspaceId = lastPaletteLocate.workspaceId
    if (!sceneId) sceneId = lastPaletteLocate.sceneId
  }
  if (kind !== 'scene' && kind !== 'collection' && kind !== 'item') return null
  return { kind, id, workspaceId, sceneId }
}

function consumePendingTarget() {
  const p = pendingTarget.value
  if (!p || p.page !== 'workspace' || !p.target) return
  const raw = p.target
  // 取走即清空：本页不保活，留着下次从侧栏进来会重复定位到旧条目
  pendingTarget.value = null
  const req = parseLocate(raw)
  if (!req) return // 解析不出 → 静默忽略
  activeLocate = req
  if (lastPaletteLocate && lastPaletteLocate.id === req.id) lastPaletteLocate = null
  void tryApply()
}

/** store.initialize() 在 App.vue.onMounted 里跑，子页更早挂载 → 清单可能还没拉回来。 */
async function tryApply() {
  if (!activeLocate || locating) return
  if (store.loading) return // 清单还在拉，等下面的 watch 在数据到位后重试
  if (store.workspaces.length === 0) {
    // 数据已到位且确实一个工作空间都没有：目标不可能存在，静默放弃（别把请求一直挂着）
    activeLocate = null
    return
  }
  const req = activeLocate
  locating = true
  try {
    await applyLocate(req)
  } catch { /* 任一级查询失败都当「定位不到」，静默忽略即可 */ }
  finally {
    locating = false
    activeLocate = null
  }
}

async function applyLocate(req: LocateReq) {
  // 搜索态下三栏显示的是搜索结果，不是常规树，先退回常规视图
  store.clearSearch()
  // item:<id> 由 wiki 链接直接发起，没有 palette:locate 事件载荷（workspaceId 为空），
  // 必须在下面那条「workspaceId 必须有值」的早退之前单独处理，否则永远走不到。
  if (req.kind === 'item') {
    await applyItemLocate(req.id)
    return
  }
  // 工作空间已删 / id 对不上：静默忽略，不切到一个不存在的工作空间
  if (!req.workspaceId || !store.workspaces.some(w => w.id === req.workspaceId)) return
  if (store.activeWorkspaceId !== req.workspaceId) await store.selectWorkspace(req.workspaceId)
  if (req.kind === 'scene') {
    // 目标场景不在清单里（已删）→ 静默忽略；selectWorkspace 可能已自动选中唯一场景
    if (store.scenes.some(s => s.id === req.id)) await store.selectScene(req.id)
    return
  }
  // 集合：先落到所属场景，再选中集合；任一层已被删除都静默忽略
  if (req.sceneId && store.scenes.some(s => s.id === req.sceneId)) {
    await store.selectScene(req.sceneId)
  }
  if (store.collections.some(c => c.id === req.id)) await store.selectCollection(req.id)
}

/**
 * item:<id>：wiki 链接跳到具体条目。条目按集合懒加载，内存里只有当前集合，
 * 故用 ListAllItems 的全量池反查 collectionId/workspaceId，再按
 * 「工作空间 → 场景 → 集合」逐级落下（与 scene/collection 分支同一套 store 动作）。
 * 任一级查不到就静默返回，不弹错、也不切到不存在的层级。
 */
async function applyItemLocate(id: string) {
  store.clearSearch()
  const all = unwrap<CollectionItem[]>(await ListAllItems()) || []
  const item = all.find(x => x.id === id)
  if (!item) return
  const wsId = item.workspaceId || store.activeWorkspaceId
  if (!wsId) return
  if (store.workspaces.some(w => w.id === wsId) && store.activeWorkspaceId !== wsId) {
    await store.selectWorkspace(wsId)
  }
  // 该集合可能不在当前场景：展开工作空间的场景逐个找，找到才继续。
  const scenes = unwrap<any[]>(await ListScenes(wsId)) || []
  let sceneId = ''
  for (const s of scenes) {
    const cols = unwrap<any[]>(await ListCollections(s.id)) || []
    if (cols.some(c => c.id === item.collectionId)) { sceneId = s.id; break }
  }
  if (!sceneId) return
  if (store.activeSceneId !== sceneId) await store.selectScene(sceneId)
  if (store.activeCollectionId !== item.collectionId) await store.selectCollection(item.collectionId)
  await nextTick()
  flashItemRow(item.name)
}

/** 选中后的滚动 + 短暂高亮。ItemList 行没有 id 属性，按标题文本定位，找不到就静默跳过。 */
function flashItemRow(name: string) {
  const rows = Array.from(document.querySelectorAll<HTMLElement>('.item-list .qd-row'))
  const el = rows.find(r => (r.querySelector('.qd-row-title')?.textContent || '').trim() === name)
  if (!el) return
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  el.classList.add('qd-link-flash')
  window.setTimeout(() => el.classList.remove('qd-link-flash'), 1800)
}

watch(pendingTarget, consumePendingTarget, { immediate: true })
// 双重兜底：异步组件竞态下若 immediate 那次因数据未就绪被跳过，挂载后再补一次
onMounted(consumePendingTarget)
// initialize() 晚于子页挂载 / 清单回填时，数据就绪后再触发一次定位
watch([() => store.loading, () => store.workspaces.length], () => { void tryApply() })
</script>

<template>
  <div class="ws-root">
    <!-- 空状态引导页（首次使用，无工作空间） -->
    <OnboardingPage
      v-if="store.workspaces.length === 0"
      @open-settings="emit('open-settings', $event)"
    />

    <!-- 空状态引导页（已有工作空间，但里面一个场景都没有）：占满整个内容区 -->
    <EmptyGuide
      v-else-if="showWorkspaceGuide"
      variant="scene"
      @open-settings="emit('open-settings', $event)"
    />

    <!-- 常规内容 -->
    <template v-else>
      <div v-if="store.error" class="ws-error-bar">
        <span class="ws-error-text">{{ store.error }}</span>
        <button class="ws-error-close" @click="store.error = ''" :title="t('close')">✕</button>
      </div>
      <SceneTags />
      <div class="ws-body">
        <!-- 三栏起点：场景 → 集合 → 项目，共用同一条顶部基线 -->
        <SceneRail />
        <!-- 当前场景下没有集合：引导页占满「集合 + 项目」两块区域 -->
        <EmptyGuide
          v-if="showCollectionsGuide"
          variant="collection"
          @open-settings="emit('open-settings', $event)"
        />
        <template v-else>
          <CollectionList class="ws-collections" />
          <ItemList class="ws-items" />
        </template>
      </div>
    </template>
  </div>
</template>

<style scoped>
/* 根节点直接作为 .app-content（flex column）的子项：撑满剩余高度、自己管纵向排列 */
.ws-root {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  overflow: hidden;
}

/* 场景 → 集合 → 项目 同一行，共用一条顶部基线 */
.ws-body {
  flex: 1;
  display: flex;
  overflow: hidden;
}

.ws-collections { flex-shrink: 0; }
.ws-items { flex: 1; min-width: 0; }

.ws-error-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 8px 12px 0;
  padding: 6px 10px;
  border-radius: var(--radius-sm);
  background: var(--color-danger-bg);
  border: 1px solid var(--color-danger-border);
  color: var(--color-danger);
  font-size: 12px;
}
.ws-error-text { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-error-close {
  background: none;
  border: none;
  cursor: pointer;
  color: inherit;
  font-size: 12px;
  padding: 2px 4px;
  opacity: 0.7;
}
.ws-error-close:hover { opacity: 1; }
/* 条目深链落点：配合 flashItemRow 的短暂高亮（ItemList 行由 :deep 命中） */
:deep(.qd-row.qd-link-flash) { background: var(--color-accent-bg); outline: 1px solid var(--color-accent-border); }
</style>
