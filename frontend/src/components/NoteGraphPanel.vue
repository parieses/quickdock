<script setup lang="ts">
/**
 * 关系图谱：自绘 SVG 力导向图，不依赖任何图表 / 图形库。
 * 数据由后端 GraphData 一次性构图；节点与边坐标全部在本组件内计算，
 * 用 requestAnimationFrame 分帧推进仿真，避免同步循环阻塞 UI。
 */
import { ref, shallowRef, triggerRef, computed, watch, inject, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { GraphData } from '../../bindings/quickdock/services/appservice'
import { RefreshCw, Maximize2, RotateCcw, Search, X, ArrowUpRight, Link2 } from '@lucide/vue'
import type { ToastAPI } from '../types'

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'openTarget', type: string, id: string, label: string): void
}>()
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 后端边数上限，同时也是前端渲染节点上限：力导向是 O(n^2)，
// 打开「显示孤立节点」后候选可能上千，截断后由 graphTooMany 提示。
const GRAPH_LIMIT = 800
const SIM_FRAMES = 120

type GraphNode = { id: string; type: string; label: string }
type GraphLink = { source: string; target: string; kind: string }
type GraphPayload = { nodes: GraphNode[]; links: GraphLink[] }
type SimNode = GraphNode & { x: number; y: number; vx: number; vy: number; fixed: boolean }
type SideRow = { node: GraphNode; kind: string; incoming: boolean }

const loading = ref(false)
const loadFailed = ref(false)
const rawNodes = ref<GraphNode[]>([])
const rawLinks = ref<GraphLink[]>([])
const showIsolated = ref(false)
const query = ref('')
const selectedId = ref<string | null>(null)

const canvasRef = ref<HTMLDivElement | null>(null)
const svgRef = ref<SVGSVGElement | null>(null)
const viewport = ref({ w: 800, h: 600 })
const scale = ref(1)
const offsetX = ref(0)
const offsetY = ref(0)

// 仿真节点用 shallowRef：每帧只改普通对象的 x/y，再 triggerRef 统一触发一次
// 重渲染，避免 deep reactive 对成百上千个坐标逐个追踪带来的开销。
const simNodes = shallowRef<SimNode[]>([])

const connectedIds = computed(() => {
  const s = new Set<string>()
  for (const l of rawLinks.value) {
    s.add(l.source)
    s.add(l.target)
  }
  return s
})
// 默认只画参与连线的节点：三类候选里绝大多数是孤立点，全画会淹没关联。
const visibleNodes = computed(() =>
  showIsolated.value
    ? rawNodes.value
    : rawNodes.value.filter((n) => connectedIds.value.has(n.id)),
)
const tooMany = computed(() => visibleNodes.value.length > GRAPH_LIMIT)
const renderNodeList = computed(() =>
  tooMany.value ? visibleNodes.value.slice(0, GRAPH_LIMIT) : visibleNodes.value,
)
const renderLinks = computed(() => {
  const ids = new Set(renderNodeList.value.map((n) => n.id))
  return rawLinks.value.filter((l) => ids.has(l.source) && ids.has(l.target))
})
const matchedIds = computed<Set<string> | null>(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return null
  const s = new Set<string>()
  for (const n of renderNodeList.value) {
    if (n.label.toLowerCase().includes(q)) s.add(n.id)
  }
  return s
})
const searchNoMatch = computed(
  () => !!query.value.trim() && matchedIds.value !== null && matchedIds.value.size === 0,
)
const isEmpty = computed(
  () => !loading.value && !loadFailed.value && renderNodeList.value.length === 0,
)
// 边坐标 / 节点查找都读这份索引，它随 simNodes 的 triggerRef 一起刷新。
const nodeIndex = computed(() => {
  const m = new Map<string, SimNode>()
  for (const n of simNodes.value) m.set(n.id, n)
  return m
})
function nodeCoord(id: string, axis: 'x' | 'y'): number {
  const n = nodeIndex.value.get(id)
  return n ? n[axis] : 0
}
const selectedNode = computed(
  () => renderNodeList.value.find((n) => n.id === selectedId.value) || null,
)
const sideRows = computed<SideRow[]>(() => {
  if (!selectedId.value) return []
  const byId = new Map(renderNodeList.value.map((n) => [n.id, n]))
  const out: SideRow[] = []
  for (const l of renderLinks.value) {
    if (l.source === selectedId.value) {
      const node = byId.get(l.target)
      if (node) out.push({ node, kind: l.kind, incoming: false })
    } else if (l.target === selectedId.value) {
      const node = byId.get(l.source)
      if (node) out.push({ node, kind: l.kind, incoming: true })
    }
  }
  return out
})

// 节点 id 形如 "type:原始id"；悬空占位节点的原始 id 以 "~" 开头，
// 没有真实实体可跳转，emit 空 id 让宿主忽略，避免拿伪 id 去深链。
function rawId(n: GraphNode): string {
  const rest = n.id.slice(n.type.length + 1)
  return rest.startsWith('~') ? '' : rest
}
function shortLabel(label: string): string {
  return label.length > 12 ? label.slice(0, 12) + '…' : label
}
function nodeClasses(n: SimNode) {
  return {
    'is-selected': n.id === selectedId.value,
    'is-match': !!matchedIds.value?.has(n.id),
    'is-dimmed': !!matchedIds.value && !matchedIds.value.has(n.id),
  }
}
function linkClasses(l: GraphLink) {
  return {
    'is-dimmed':
      !!matchedIds.value && !(matchedIds.value.has(l.source) || matchedIds.value.has(l.target)),
  }
}

async function load() {
  loading.value = true
  loadFailed.value = false
  try {
    const data = unwrap<GraphPayload>(await GraphData(GRAPH_LIMIT)) || { nodes: [], links: [] }
    rawNodes.value = Array.isArray(data.nodes) ? data.nodes : []
    rawLinks.value = Array.isArray(data.links) ? data.links : []
    selectedId.value = null
    rebuild(true)
  } catch (e) {
    logErr('NoteGraphPanel', e)
    toast.error(getErrorMessage(e))
    loadFailed.value = true
    rawNodes.value = []
    rawLinks.value = []
    // 刷新失败时别让上一次的仿真继续在空数据上空转。
    cancelAnimationFrame(rafId)
    simNodes.value = []
  } finally {
    loading.value = false
  }
}

// ---- 轻量力导向：斥力 + 弹簧，逐帧推进 ---------------------------------
let rafId = 0
let frame = 0
let fitPending = false

function rebuild(doFit: boolean) {
  cancelAnimationFrame(rafId)
  const nodes = renderNodeList.value
  const cx = viewport.value.w / 2
  const cy = viewport.value.h / 2
  const radius = Math.max(60, Math.min(cx, cy) * 0.6)
  // 确定性抖动：不用 Math.random，重置布局每次都得到同样的初始态。
  simNodes.value = nodes.map((n, i) => {
    const a = (i / Math.max(1, nodes.length)) * Math.PI * 2
    return {
      ...n,
      x: cx + Math.cos(a) * radius + Math.sin(i * 12.9898) * 12,
      y: cy + Math.sin(a) * radius + Math.cos(i * 78.233) * 12,
      vx: 0,
      vy: 0,
      fixed: false,
    }
  })
  frame = 0
  fitPending = doFit
  rafId = requestAnimationFrame(tick)
}

function tick() {
  const arr = simNodes.value
  const n = arr.length
  const cx = viewport.value.w / 2
  const cy = viewport.value.h / 2
  const fx = new Float64Array(n)
  const fy = new Float64Array(n)
  const REPULSION = 9000
  const SPRING = 0.02
  const REST = 90
  const CENTER = 0.006
  const DAMPING = 0.85
  const MAX_SPEED = 30
  const CUTOFF2 = 220 * 220

  // 斥力：两两之间的库仑斥力，太远的点对直接跳过以省算力。
  for (let i = 0; i < n; i++) {
    for (let j = i + 1; j < n; j++) {
      const dx = arr[j].x - arr[i].x
      const dy = arr[j].y - arr[i].y
      const d2 = dx * dx + dy * dy + 0.01
      if (d2 > CUTOFF2) continue
      const d = Math.sqrt(d2)
      const f = REPULSION / d2
      fx[i] -= (f * dx) / d
      fy[i] -= (f * dy) / d
      fx[j] += (f * dx) / d
      fy[j] += (f * dy) / d
    }
  }
  // 弹簧：有边的两节点被拉向自然长度 REST。
  const idx = new Map<string, number>()
  for (let i = 0; i < n; i++) idx.set(arr[i].id, i)
  for (const l of renderLinks.value) {
    const u = idx.get(l.source)
    const v = idx.get(l.target)
    if (u === undefined || v === undefined) continue
    const dx = arr[v].x - arr[u].x
    const dy = arr[v].y - arr[u].y
    const d = Math.sqrt(dx * dx + dy * dy) + 0.01
    const f = SPRING * (d - REST)
    fx[u] += (f * dx) / d
    fy[u] += (f * dy) / d
    fx[v] -= (f * dx) / d
    fy[v] -= (f * dy) / d
  }
  // 中心引力 + 积分；拖拽中的节点固定不动。
  let maxMove = 0
  for (let i = 0; i < n; i++) {
    const node = arr[i]
    if (node.fixed) {
      node.vx = 0
      node.vy = 0
      continue
    }
    const ax = fx[i] + (cx - node.x) * CENTER
    const ay = fy[i] + (cy - node.y) * CENTER
    let vx = (node.vx + ax) * DAMPING
    let vy = (node.vy + ay) * DAMPING
    vx = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, vx))
    vy = Math.max(-MAX_SPEED, Math.min(MAX_SPEED, vy))
    node.vx = vx
    node.vy = vy
    node.x += vx
    node.y += vy
    maxMove = Math.max(maxMove, Math.abs(vx) + Math.abs(vy))
  }
  triggerRef(simNodes)
  frame++
  // 收敛或跑满帧数即停，避免无意义地一直占用 rAF。
  if (frame < SIM_FRAMES && maxMove > 0.4 && n > 1) {
    rafId = requestAnimationFrame(tick)
  } else {
    if (fitPending) {
      fitView()
      fitPending = false
    }
  }
}

function fitView() {
  const arr = simNodes.value
  const w = viewport.value.w
  const h = viewport.value.h
  if (!arr.length || w <= 0 || h <= 0) return
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
  for (const n of arr) {
    minX = Math.min(minX, n.x)
    minY = Math.min(minY, n.y)
    maxX = Math.max(maxX, n.x)
    maxY = Math.max(maxY, n.y)
  }
  const pad = 48
  const bw = Math.max(1, maxX - minX)
  const bh = Math.max(1, maxY - minY)
  const s = Math.max(0.2, Math.min(2, Math.min(w / (bw + pad * 2), h / (bh + pad * 2))))
  scale.value = s
  offsetX.value = w / 2 - ((minX + maxX) / 2) * s
  offsetY.value = h / 2 - ((minY + maxY) / 2) * s
}

// ---- 交互：拖拽 / 平移 / 缩放 -------------------------------------------
type PointerState = {
  mode: 'drag' | 'pan'
  nodeId: string
  startX: number
  startY: number
  origOffsetX: number
  origOffsetY: number
  moved: boolean
}
let pointer: PointerState | null = null

function graphPoint(clientX: number, clientY: number) {
  const rect = svgRef.value?.getBoundingClientRect()
  const px = clientX - (rect?.left ?? 0)
  const py = clientY - (rect?.top ?? 0)
  return { x: (px - offsetX.value) / scale.value, y: (py - offsetY.value) / scale.value }
}

function onPointerDown(e: PointerEvent) {
  if (e.button !== 0) return
  const g = (e.target as Element).closest('[data-node]')
  const nodeId = g?.getAttribute('data-node') || ''
  pointer = {
    mode: nodeId ? 'drag' : 'pan',
    nodeId,
    startX: e.clientX,
    startY: e.clientY,
    origOffsetX: offsetX.value,
    origOffsetY: offsetY.value,
    moved: false,
  }
  if (nodeId) {
    const node = simNodes.value.find((n) => n.id === nodeId)
    if (node) {
      node.fixed = true
      node.vx = 0
      node.vy = 0
    }
  }
  svgRef.value?.setPointerCapture(e.pointerId)
}

function onPointerMove(e: PointerEvent) {
  if (!pointer) return
  const dx = e.clientX - pointer.startX
  const dy = e.clientY - pointer.startY
  if (Math.abs(dx) > 3 || Math.abs(dy) > 3) pointer.moved = true
  if (pointer.mode === 'drag') {
    const p = graphPoint(e.clientX, e.clientY)
    const node = simNodes.value.find((n) => n.id === pointer!.nodeId)
    if (node) {
      node.x = p.x
      node.y = p.y
      triggerRef(simNodes)
    }
  } else {
    offsetX.value = pointer.origOffsetX + dx
    offsetY.value = pointer.origOffsetY + dy
  }
}

function onPointerUp(e: PointerEvent) {
  const p = pointer
  pointer = null
  if (!p) return
  if (svgRef.value?.hasPointerCapture(e.pointerId)) svgRef.value.releasePointerCapture(e.pointerId)
  if (p.mode === 'drag') {
    const node = simNodes.value.find((n) => n.id === p.nodeId)
    if (node) {
      node.fixed = false
      if (!p.moved) onNodeClick(node)
    }
  } else if (!p.moved) {
    // 点空白取消选中，顺手收起侧栏。
    selectedId.value = null
  }
}

function onWheel(e: WheelEvent) {
  const rect = svgRef.value?.getBoundingClientRect()
  if (!rect) return
  const px = e.clientX - rect.left
  const py = e.clientY - rect.top
  const old = scale.value
  const next = Math.max(0.2, Math.min(4, old * (e.deltaY < 0 ? 1.12 : 1 / 1.12)))
  offsetX.value = px - (px - offsetX.value) * (next / old)
  offsetY.value = py - (py - offsetY.value) * (next / old)
  scale.value = next
}

function onNodeClick(node: SimNode) {
  selectedId.value = node.id
  emit('openTarget', node.type, rawId(node), node.label)
}
function openSide(row: SideRow) {
  emit('openTarget', row.node.type, rawId(row.node), row.node.label)
}

function refresh() {
  void load()
}
function resetLayout() {
  if (!loading.value && !loadFailed.value) rebuild(true)
}
function fit() {
  fitView()
}

let ro: ResizeObserver | null = null
watch(showIsolated, () => {
  // 孤立节点开关改变渲染集合，重建仿真（重置布局）。
  if (!loading.value && !loadFailed.value) rebuild(true)
})
onMounted(() => {
  const el = canvasRef.value
  if (el) {
    viewport.value = { w: el.clientWidth || 800, h: el.clientHeight || 600 }
    ro = new ResizeObserver(() => {
      viewport.value = { w: el.clientWidth || 800, h: el.clientHeight || 600 }
    })
    ro.observe(el)
  }
  void load()
})
onBeforeUnmount(() => {
  cancelAnimationFrame(rafId)
  ro?.disconnect()
})
</script>

<template>
  <div class="qd-graph" :aria-label="t('graphTab')">
    <div class="qd-graph-toolbar">
      <Link2 :size="14" class="qd-graph-title-icon" />
      <span class="qd-graph-title">{{ t('graphTitle') }}</span>
      <span class="qd-graph-count">{{ t('graphNodes') }} {{ renderNodeList.length }}</span>
      <span class="qd-graph-count">{{ t('graphLinks') }} {{ renderLinks.length }}</span>
      <span class="qd-graph-spacer" />

      <div class="qd-graph-search">
        <Search :size="12" class="qd-graph-search-icon" />
        <input
          v-model="query"
          class="qd-graph-search-input"
          :placeholder="t('graphSearch')"
          spellcheck="false"
        />
        <button v-if="query" type="button" class="qd-graph-icon-btn" @click="query = ''">
          <X :size="12" />
        </button>
      </div>

      <label class="qd-graph-toggle">
        <input v-model="showIsolated" type="checkbox" class="qd-graph-toggle-input" />
        <span>{{ t('graphIsolated') }}</span>
      </label>

      <button type="button" class="qd-graph-icon-btn" :title="t('graphRefresh')" :disabled="loading" @click="refresh">
        <RefreshCw :size="13" :class="{ 'qd-graph-spin': loading }" />
      </button>
      <button type="button" class="qd-graph-icon-btn" :title="t('graphFit')" @click="fit">
        <Maximize2 :size="13" />
      </button>
      <button type="button" class="qd-graph-icon-btn" :title="t('graphReset')" @click="resetLayout">
        <RotateCcw :size="13" />
      </button>
      <button type="button" class="qd-graph-icon-btn" @click="emit('close')">
        <X :size="13" />
      </button>
    </div>

    <div class="qd-graph-body">
      <div ref="canvasRef" class="qd-graph-canvas">
        <!-- 运行时坐标属性说明：下面 <g> 的 :transform 由平移/缩放交互算出，
             <line> 的 :x1/:y1/:x2/:y2、<circle> 的 :cx/:cy/:r、<text> 的 :x/:y
             均由力导向仿真逐帧写出的坐标驱动，属 R7 允许的运行时计算值。 -->
        <svg
          ref="svgRef"
          class="qd-graph-svg"
          width="100%"
          height="100%"
          @pointerdown="onPointerDown"
          @pointermove="onPointerMove"
          @pointerup="onPointerUp"
          @pointercancel="onPointerUp"
          @wheel.prevent="onWheel"
        >
          <!-- :transform 是平移 / 缩放的运行时坐标，非静态排版 -->
          <g :transform="'translate(' + offsetX + ',' + offsetY + ') scale(' + scale + ')'">
            <!-- 边：坐标取自两端节点的实时仿真位置；kind 决定实线 / 虚线 -->
            <line
              v-for="(l, i) in renderLinks"
              :key="'l-' + i"
              :x1="nodeCoord(l.source, 'x')"
              :y1="nodeCoord(l.source, 'y')"
              :x2="nodeCoord(l.target, 'x')"
              :y2="nodeCoord(l.target, 'y')"
              class="qd-graph-link"
              :class="[l.kind === 'manual' ? 'qd-graph-link--manual' : 'qd-graph-link--wiki', linkClasses(l)]"
            />
            <!-- 节点：:cx/:cy/:r/:x/:y 为力导向每帧写出的运行时坐标 -->
            <g v-for="n in simNodes" :key="n.id" :data-node="n.id" class="qd-graph-node-group">
              <circle
                :cx="n.x"
                :cy="n.y"
                :r="n.id === selectedId ? 9 : 7"
                class="qd-graph-node"
                :class="['qd-graph-node--' + n.type, nodeClasses(n)]"
              />
              <text :x="n.x" :y="n.y + 18" class="qd-graph-label" text-anchor="middle">
                {{ shortLabel(n.label) }}
              </text>
            </g>
          </g>
        </svg>

        <div class="qd-graph-warns">
          <span v-if="tooMany" class="qd-graph-warn">{{ t('graphTooMany', { count: GRAPH_LIMIT }) }}</span>
          <span v-if="searchNoMatch" class="qd-graph-warn">{{ t('graphSearchEmpty') }}</span>
        </div>

        <div v-if="loadFailed" class="qd-graph-overlay">
          <div class="qd-graph-overlay-card">
            <span class="qd-graph-overlay-text">{{ t('graphLoadFailed') }}</span>
            <button type="button" class="qd-graph-overlay-btn" @click="refresh">
              {{ t('graphRefresh') }}
            </button>
          </div>
        </div>
        <div v-else-if="loading" class="qd-graph-overlay">
          <div class="qd-graph-spinner" />
        </div>
        <div v-else-if="isEmpty" class="qd-graph-overlay">
          <span class="qd-graph-overlay-text">{{ t('graphEmpty') }}</span>
        </div>

        <div class="qd-graph-legend">
          <div class="qd-graph-legend-title">{{ t('graphLegend') }}</div>
          <div class="qd-graph-legend-row">
            <span class="qd-graph-dot qd-graph-dot--note" /><span>{{ t('graphTypeNote') }}</span>
            <span class="qd-graph-dot qd-graph-dot--todo" /><span>{{ t('graphTypeTodo') }}</span>
            <span class="qd-graph-dot qd-graph-dot--item" /><span>{{ t('graphTypeItem') }}</span>
          </div>
          <div class="qd-graph-legend-row">
            <span class="qd-graph-line-sample qd-graph-line-sample--wiki" /><span>{{ t('graphKindWiki') }}</span>
            <span class="qd-graph-line-sample qd-graph-line-sample--manual" /><span>{{ t('graphKindManual') }}</span>
          </div>
        </div>
        <div class="qd-graph-hint">{{ t('graphHint') }}</div>
      </div>

      <aside v-if="selectedNode" class="qd-graph-side">
        <div class="qd-graph-side-head">
          <span class="qd-graph-side-label" :title="selectedNode.label">{{ selectedNode.label }}</span>
        </div>
        <div class="qd-graph-side-sec">
          {{ t('graphLinks') }} <span class="qd-graph-side-n">{{ sideRows.length }}</span>
        </div>
        <div v-if="!sideRows.length" class="qd-graph-side-empty">{{ t('graphEmpty') }}</div>
        <div v-for="(row, i) in sideRows" :key="'s-' + i" class="qd-graph-side-row">
          <ArrowUpRight
            :size="12"
            class="qd-graph-side-arrow"
            :class="{ 'is-in': row.incoming }"
          />
          <span class="qd-graph-side-row-label" :title="row.node.label">{{ row.node.label }}</span>
          <span class="qd-graph-side-kind">
            {{ row.kind === 'wiki' ? t('graphKindWiki') : t('graphKindManual') }}
          </span>
          <button type="button" class="qd-graph-side-open" @click="openSide(row)">
            {{ t('graphOpenTarget') }}
          </button>
        </div>
      </aside>
    </div>
  </div>
</template>

<style scoped>
.qd-graph {
  display: flex;
  flex-direction: column;
  width: 100%;
  height: 100%;
  min-height: 0;
  background: var(--color-bg-secondary);
  color: var(--color-text-secondary);
}
.qd-graph-toolbar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-4);
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
}
.qd-graph-title-icon { color: var(--color-accent); flex-shrink: 0; }
.qd-graph-title { font-weight: 600; color: var(--color-text-primary); font-size: var(--text-md); }
.qd-graph-count { color: var(--color-text-muted); font-size: var(--text-xs); font-variant-numeric: tabular-nums; }
.qd-graph-spacer { flex: 1; min-width: var(--space-2); }
.qd-graph-search {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 0 var(--space-2);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}
.qd-graph-search-icon { color: var(--color-text-muted); flex-shrink: 0; }
.qd-graph-search-input {
  width: 140px;
  border: none;
  outline: none;
  background: transparent;
  color: var(--color-text-primary);
  font-size: var(--text-xs);
  font-family: inherit;
}
.qd-graph-search-input::placeholder { color: var(--color-text-disabled); }
.qd-graph-toggle {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-size: var(--text-xs);
  cursor: pointer;
  white-space: nowrap;
  color: var(--color-text-secondary);
}
.qd-graph-toggle-input { accent-color: var(--color-accent); cursor: pointer; }
.qd-graph-icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  flex-shrink: 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  cursor: pointer;
}
.qd-graph-icon-btn:hover:not(:disabled) {
  color: var(--color-accent);
  border-color: var(--color-accent-border);
}
.qd-graph-icon-btn:disabled { opacity: 0.4; cursor: default; }
.qd-graph-spin { animation: qd-graph-rotate 0.9s linear infinite; }
@keyframes qd-graph-rotate { to { transform: rotate(360deg); } }

.qd-graph-body { flex: 1; min-height: 0; display: flex; }
.qd-graph-canvas {
  position: relative;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  background: var(--color-bg-primary);
}
.qd-graph-svg { display: block; width: 100%; height: 100%; touch-action: none; cursor: grab; }
.qd-graph-svg:active { cursor: grabbing; }
.qd-graph-node-group { cursor: pointer; }
.qd-graph-node { stroke: var(--color-bg-primary); stroke-width: 2; }
.qd-graph-node--note { fill: var(--color-accent); }
.qd-graph-node--todo { fill: var(--color-success); }
.qd-graph-node--item { fill: var(--color-warning); }
.qd-graph-node.is-selected { stroke: var(--color-warning); stroke-width: 3; }
.qd-graph-node.is-match { stroke: var(--color-warning); }
.qd-graph-node.is-dimmed { opacity: 0.22; }
.qd-graph-label {
  fill: var(--color-text-secondary);
  font-size: var(--text-2xs);
  pointer-events: none;
  user-select: none;
}
.qd-graph-link { stroke: var(--color-text-muted); stroke-width: 1; opacity: 0.55; }
.qd-graph-link--manual { stroke-dasharray: 5 4; }
.qd-graph-link.is-dimmed { opacity: 0.12; }

.qd-graph-warns {
  position: absolute;
  top: var(--space-3);
  left: 50%;
  transform: translateX(-50%);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  z-index: 2;
  pointer-events: none;
}
.qd-graph-warn {
  padding: var(--space-2) var(--space-4);
  border: 1px solid var(--color-warning-border);
  border-radius: var(--radius-sm);
  background: var(--color-warning-bg);
  color: var(--color-warning);
  font-size: var(--text-xs);
  white-space: nowrap;
}
.qd-graph-legend {
  position: absolute;
  left: var(--space-3);
  bottom: var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  box-shadow: var(--shadow-sm);
  font-size: var(--text-xs);
  z-index: 2;
  pointer-events: none;
}
.qd-graph-legend-title { color: var(--color-text-muted); font-weight: 600; margin-bottom: var(--space-2); }
.qd-graph-legend-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-top: var(--space-1);
  color: var(--color-text-secondary);
}
.qd-graph-dot { width: 8px; height: 8px; border-radius: var(--radius-full); display: inline-block; }
.qd-graph-dot--note { background: var(--color-accent); }
.qd-graph-dot--todo { background: var(--color-success); }
.qd-graph-dot--item { background: var(--color-warning); }
.qd-graph-line-sample {
  width: 18px;
  height: 0;
  border-top: 2px solid var(--color-text-muted);
  display: inline-block;
}
.qd-graph-line-sample--manual { border-top-style: dashed; }
.qd-graph-hint {
  position: absolute;
  right: var(--space-3);
  bottom: var(--space-3);
  color: var(--color-text-disabled);
  font-size: var(--text-xs);
  z-index: 2;
  pointer-events: none;
}
.qd-graph-overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-3);
  z-index: 3;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
.qd-graph-overlay-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-5) var(--space-6);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-secondary);
  box-shadow: var(--shadow-md);
}
.qd-graph-overlay-btn {
  padding: var(--space-2) var(--space-5);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-sm);
  background: var(--color-accent-bg);
  color: var(--color-accent);
  cursor: pointer;
  font-family: inherit;
  font-size: var(--text-xs);
}
.qd-graph-spinner {
  width: 22px;
  height: 22px;
  border: 2px solid var(--color-border);
  border-top-color: var(--color-accent);
  border-radius: var(--radius-full);
  animation: qd-graph-rotate 0.9s linear infinite;
}

.qd-graph-side {
  width: 240px;
  flex-shrink: 0;
  border-left: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
  display: flex;
  flex-direction: column;
  overflow-y: auto;
  padding: var(--space-3);
}
.qd-graph-side-head { border-bottom: 1px solid var(--color-border); padding-bottom: var(--space-3); }
.qd-graph-side-label {
  display: block;
  color: var(--color-text-primary);
  font-weight: 600;
  font-size: var(--text-sm);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qd-graph-side-sec {
  margin-top: var(--space-3);
  color: var(--color-text-muted);
  font-weight: 600;
  font-size: var(--text-xs);
}
.qd-graph-side-n { color: var(--color-text-disabled); }
.qd-graph-side-empty { padding: var(--space-3) 0; color: var(--color-text-disabled); font-size: var(--text-xs); }
.qd-graph-side-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-1);
  border-radius: var(--radius-xs);
}
.qd-graph-side-row:hover { background: var(--color-bg-hover); }
.qd-graph-side-arrow { color: var(--color-text-muted); flex-shrink: 0; }
.qd-graph-side-arrow.is-in { transform: rotate(180deg); color: var(--color-accent); }
.qd-graph-side-row-label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--color-text-primary);
  font-size: var(--text-xs);
}
.qd-graph-side-kind { color: var(--color-text-disabled); font-size: var(--text-2xs); flex-shrink: 0; }
.qd-graph-side-open {
  flex-shrink: 0;
  padding: 0 var(--space-2);
  border: 1px solid var(--color-accent-border);
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-accent);
  cursor: pointer;
  font-family: inherit;
  font-size: var(--text-2xs);
}
.qd-graph-side-open:hover { background: var(--color-accent-bg); }
</style>
