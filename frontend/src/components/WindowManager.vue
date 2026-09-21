<script setup lang="ts">
import { computed, ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { GetTilingTarget, ApplyTiling, HideWinmgrWindow } from '../../bindings/quickdock/services/appservice'
import type { TilingCell } from '../../bindings/quickdock/internal/winmgr/models'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'

const { t } = useI18n()

// ---- 排版上下文 ----
// ApiResult.data 是 any，后端不为它生成 TS model，这里按 Go 侧 tilingTargetInfo 对齐。
// ScreenW/ScreenH 是目标屏工作区的物理尺寸：模板缩略图照这个比例画，
// 「缩略图里的半屏」就等于屏幕上真实的半屏，点之前心里有数。
interface TilingTargetInfo {
  HWND: number
  Title: string
  ScreenW: number
  ScreenH: number
  Peers: number
}

// 排版结果（Go 侧 winmgr.TilingResult）。
interface TilingResult {
  Applied: number
  Peers: number
  Titles: string[]
}

// ---- 模板定义：cells 为归一化矩形（相对工作区 0~1），后端按目标屏工作区换算物理像素 ----
interface Cell { x: number; y: number; w: number; h: number }
interface Tpl { id: string; nameKey: string; cells: Cell[] }

const third = 1 / 3
const templates: Tpl[] = [
  { id: 'split2', nameKey: 'winmgrTplSplit2', cells: [
    { x: 0, y: 0, w: 0.5, h: 1 }, { x: 0.5, y: 0, w: 0.5, h: 1 } ] },
  { id: 'mainLeft', nameKey: 'winmgrTplMainLeft', cells: [
    { x: 0, y: 0, w: 0.5, h: 1 }, { x: 0.5, y: 0, w: 0.5, h: 0.5 }, { x: 0.5, y: 0.5, w: 0.5, h: 0.5 } ] },
  { id: 'mainRight', nameKey: 'winmgrTplMainRight', cells: [
    { x: 0.5, y: 0, w: 0.5, h: 1 }, { x: 0, y: 0, w: 0.5, h: 0.5 }, { x: 0, y: 0.5, w: 0.5, h: 0.5 } ] },
  { id: 'mainTop', nameKey: 'winmgrTplMainTop', cells: [
    { x: 0, y: 0, w: 1, h: 0.5 }, { x: 0, y: 0.5, w: 0.5, h: 0.5 }, { x: 0.5, y: 0.5, w: 0.5, h: 0.5 } ] },
  { id: 'mainBottom', nameKey: 'winmgrTplMainBottom', cells: [
    { x: 0, y: 0.5, w: 1, h: 0.5 }, { x: 0, y: 0, w: 0.5, h: 0.5 }, { x: 0.5, y: 0, w: 0.5, h: 0.5 } ] },
  { id: 'grid4', nameKey: 'winmgrTplGrid4', cells: [
    { x: 0, y: 0, w: 0.5, h: 0.5 }, { x: 0.5, y: 0, w: 0.5, h: 0.5 },
    { x: 0, y: 0.5, w: 0.5, h: 0.5 }, { x: 0.5, y: 0.5, w: 0.5, h: 0.5 } ] },
  { id: 'col3', nameKey: 'winmgrTplCol3', cells: [
    { x: 0, y: 0, w: third, h: 1 }, { x: third, y: 0, w: third, h: 1 }, { x: third * 2, y: 0, w: third, h: 1 } ] },
  { id: 'row3', nameKey: 'winmgrTplRow3', cells: [
    { x: 0, y: 0, w: 1, h: third }, { x: 0, y: third, w: 1, h: third }, { x: 0, y: third * 2, w: 1, h: third } ] },
  { id: 'grid9', nameKey: 'winmgrTplGrid9', cells: [
    { x: 0, y: 0, w: third, h: third }, { x: third, y: 0, w: third, h: third }, { x: third * 2, y: 0, w: third, h: third },
    { x: 0, y: third, w: third, h: third }, { x: third, y: third, w: third, h: third }, { x: third * 2, y: third, w: third, h: third },
    { x: 0, y: third * 2, w: third, h: third }, { x: third, y: third * 2, w: third, h: third }, { x: third * 2, y: third * 2, w: third, h: third } ] },
]

const info = ref<TilingTargetInfo | null>(null)
const message = ref('')
const result = ref('')
const busy = ref(false)

const target = computed(() => (info.value && info.value.HWND ? info.value : null))

// 缩略图按目标屏真实宽高比绘制（未取到屏尺寸时退回 16:9）。
// 固定 100x56 的缩略图在 16:10 / 21:9 屏上会失真，让人误判「半屏」到底有多宽。
const SVG_W = 100
const PAD = 1.6
const svgH = computed(() => {
  const i = info.value
  const aspect = i && i.ScreenW > 0 && i.ScreenH > 0 ? i.ScreenW / i.ScreenH : 16 / 9
  return Math.round(SVG_W / aspect)
})

// 排版目标由后端在显示浮层前捕获（浮层拿到焦点后就取不到用户的目标窗口了）。
async function loadTarget() {
  try {
    const data = unwrap<TilingTargetInfo>(await GetTilingTarget())
    info.value = data && data.HWND ? data : null
  } catch {
    info.value = null
  }
}

// 点格子 = 应用整块模板：目标窗口进被点的这一格，其余格子由后端自动用同屏其他
// 可见窗口填充（不够则留空）。点卡片本身等同于点第 1 格（主位）。
// 前端只负责把模板几何 + 点击下标传下去，选窗逻辑全在后端。
async function place(tpl: Tpl, ci: number) {
  if (busy.value) return
  if (!target.value) {
    message.value = t('winmgrNoTarget')
    return
  }
  busy.value = true
  message.value = ''
  result.value = ''
  const cells: TilingCell[] = tpl.cells.map(c => ({ X: c.x, Y: c.y, W: c.w, H: c.h }))
  try {
    const res = unwrap<TilingResult>(await ApplyTiling(cells, ci, true))
    // 先把「排了几个窗口」显示出来再收起浮层：否则用户只看到浮层闪一下，
    // 无从判断到底排没排、排了几个。
    result.value = t('winmgrTiled', { n: res ? res.Applied : 0 })
    setTimeout(close, 700)
  } catch (e) {
    message.value = getErrorMessage(e)
  } finally {
    busy.value = false
  }
}

// 隐藏走 service：WebView2 顶层窗口的 window.close() 会被忽略（点了 X 没反应的根因）。
async function close() {
  try {
    await HideWinmgrWindow()
  } catch {
    /* 窗口可能已被失焦隐藏，忽略 */
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') close()
}

// 浮层窗口是懒创建 + 复用的：Hide/Show 不会让 Vue 重新 mount，所以不能在 onMounted
// 里只拉一次目标窗口——首次创建时那次必然是空的，之后就会永远提示「未捕获到目标窗口」。
// 后端每次显示浮层都会 emit winmgr:shown，这里跟着重新拉取。
function onShown() {
  message.value = ''
  result.value = ''
  loadTarget()
}

onMounted(() => {
  loadTarget()
  Events.On('winmgr:shown', onShown)
  window.addEventListener('keydown', onKey)
})
onUnmounted(() => {
  Events.Off('winmgr:shown')
  window.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div class="wm-root">
    <!-- 顶部栏（可拖动）：显示将要移动哪个窗口 -->
    <div class="wm-bar">
      <div class="wm-bar-left">
        <span class="wm-bar-title">{{ t('winmgrPickTitle') }}</span>
        <span v-if="target" class="wm-target" :title="target.Title">
          <span class="wm-target-dot" />
          <span class="wm-target-title">{{ target.Title }}</span>
        </span>
        <span v-else class="wm-target wm-target-none">{{ t('winmgrNoTarget') }}</span>
        <span v-if="target && target.Peers > 0" class="wm-peers">
          {{ t('winmgrPeers', { n: target.Peers }) }}
        </span>
      </div>
      <button class="wm-icon-btn" title="Esc" @click="close">
        <X :size="15" />
      </button>
    </div>

    <div class="wm-body">
      <p :class="['wm-hint', { 'wm-hint-warn': !!message, 'wm-hint-ok': !!result }]">
        {{ message || result || t('winmgrPickDesc') }}
      </p>

      <!-- 9 种布局模板：每个小块都能直接点；点卡片本身 = 主位第 1 格 -->
      <div class="wm-tpl-grid">
        <div v-for="tpl in templates" :key="tpl.id" class="wm-tpl" @click="place(tpl, 0)">
          <svg :viewBox="`0 0 ${SVG_W} ${svgH}`" class="wm-tpl-svg">
            <rect
              v-for="(c, ci) in tpl.cells" :key="ci"
              :x="c.x * SVG_W + PAD" :y="c.y * svgH + PAD"
              :width="c.w * SVG_W - 2 * PAD" :height="c.h * svgH - 2 * PAD"
              rx="2.5" class="wm-tpl-cell"
              @click.stop="place(tpl, ci)"
            >
              <title>{{ t(tpl.nameKey) }} · {{ ci + 1 }}</title>
            </rect>
          </svg>
          <span class="wm-tpl-name">{{ t(tpl.nameKey) }}</span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.wm-root {
  height: 100vh; width: 100vw; display: flex; flex-direction: column;
  background: var(--color-bg-primary); color: var(--color-text-primary);
  font-size: 12px; overflow: hidden;
}

/* 顶部栏 */
.wm-bar {
  display: flex; align-items: center; gap: 8px;
  padding: 9px 12px; border-bottom: 1px solid var(--color-border);
  --wails-draggable: drag;
}
.wm-bar-left { display: flex; align-items: center; gap: 10px; flex: 1; min-width: 0; }
.wm-bar-title { font-size: 13px; font-weight: 600; white-space: nowrap; flex-shrink: 0; }
.wm-icon-btn {
  width: 26px; height: 26px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-muted);
  border-radius: var(--radius-xs); cursor: pointer; flex-shrink: 0; --wails-draggable: no-drag;
}
.wm-icon-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

/* 目标窗口提示 */
.wm-target {
  display: inline-flex; align-items: center; gap: 6px; min-width: 0;
  max-width: 460px; padding: 2px 9px; border-radius: 999px;
  background: var(--color-accent-bg); color: var(--color-accent); font-size: 11px;
}
.wm-target-dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex-shrink: 0; }
.wm-target-title { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.wm-target-none { background: transparent; color: var(--color-warning); }

/* 本屏可参与排版的窗口数：让「有些格子空着」变得可解释 */
.wm-peers {
  font-size: 11px; color: var(--color-text-disabled); white-space: nowrap;
  overflow: hidden; text-overflow: ellipsis; flex-shrink: 0;
}

.wm-body { flex: 1; overflow-y: auto; padding: 10px 16px 14px; }
.wm-hint { font-size: 11px; color: var(--color-text-disabled); margin: 0 0 10px; }
.wm-hint-warn { color: var(--color-warning); }
.wm-hint-ok { color: var(--color-success); }

/* 模板网格：每张卡片里的每个小块都是命中区 */
.wm-tpl-grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 10px; }
.wm-tpl {
  display: flex; flex-direction: column; align-items: center; gap: 5px;
  padding: 8px; background: var(--color-surface);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  transition: border-color 0.15s, background 0.15s; cursor: pointer;
}
.wm-tpl:hover { border-color: var(--color-border-focus); background: var(--color-bg-hover); }
.wm-tpl-svg { width: 100%; height: auto; display: block; }
.wm-tpl-cell {
  fill: var(--color-bg-tertiary); stroke: var(--color-border-focus); stroke-width: 1;
  cursor: pointer; transition: fill 0.12s, stroke 0.12s;
}
.wm-tpl-cell:hover { fill: var(--color-accent); stroke: var(--color-accent); }
.wm-tpl-name { font-size: 11px; color: var(--color-text-secondary); white-space: nowrap; }
</style>
