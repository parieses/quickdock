<script setup lang="ts">
import { ref, computed, reactive, onMounted, onUnmounted, onActivated, onDeactivated, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { Bot, Plus, Trash2, Copy, Square, RefreshCw, Eraser, StickyNote, ArrowDown, Image as ImageIcon, Link2, X, Download, Pencil, Search } from '@lucide/vue'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { unwrap } from '../utils/api'
import { SetAIPageVisible } from '../../bindings/quickdock/services/appservice'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { splitMarkdownBlocks as splitBlocks } from '../utils/markdownBlocks'
import { isMathFenceLang, mathExtension, renderMath } from '../utils/mathMarkdown'
import {
  handleMermaidToolbarClick,
  isMermaidFenceLang,
  mermaidPlaceholder,
  renderMermaidIn,
  setMermaidFolded,
  setMermaidSourceMode,
  type MermaidLabels,
} from '../utils/mermaidMarkdown'
import { copyElementAsImage, downloadBlob, serializeMermaidSvg, svgToPngBlob, resolveBackground } from '../utils/elementImage'
// 代码块语法高亮：common 子集覆盖 AI 常见语言（js/ts/python/go/bash/json/sql/html...），
// 比全量包小很多。主题配色见 styles/hljs-theme.css（跟随 <html data-theme>）。
import hljs from 'highlight.js/lib/common'
import '../styles/hljs-theme.css'
import { useFloatMenu } from '../composables/useFloatMenu'
import { aiGenerating, aiFinishedUnread } from '../composables/bridge'
import { CreateNoteDoc } from '../../bindings/quickdock/services/note/noteservice'
import type { ToastAPI } from '../types'
import type { AIProfile } from '../../bindings/quickdock/services/ai/models'
import type { AIProfilesResult } from '../types/ai'

import {
  AIListProfiles,
  AISetActiveProfile,
  AIStreamInfo,
  AIClearMessages,
  AIRegenerateTitle,
  AIRenameConversation,
  AIListConversations,
  AIDeleteConversation,
  AIGetMessages,
  AILoadImage,
} from '../../bindings/quickdock/services/ai/aiservice'

// 这个 name 是 App.vue 里 <KeepAlive :include> 的匹配依据，改它等于让 AI 页失去保活。
// 为什么必须显式声明：页面是经 defineAsyncComponent(import(...)) 注册的，而 KeepAlive 判断
// 异步组件时读的是 `vnode.type.__asyncResolved`（内层 SFC 定义）上的名字 ——
// 在包装器上赋 name 完全无效，不声明就永远匹配不上，切页照样销毁组件。
defineOptions({ name: 'AIPageKeepAlive' })

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface AIConversation { id: string; title: string; summary: string; prompt_tokens: number; completion_tokens: number; created_at: string; updated_at: string }
interface AIMessage {
  id: string; conv_id: string; role: string; content: string; created_at: string
  reasoning_content?: string; images?: string; imgList?: AIMsgImage[]
  // 该条回复自己那一次调用的用量（assistant 才有值，老消息为 0/undefined）
  model?: string; prompt_tokens?: number; completion_tokens?: number; cached_tokens?: number
  // 这条回复的估算费用（元）。后端按当前价目表现算，未收录的模型没有这个字段。
  cost?: number
  // 这条回复被用户中途停止（内容是半截的）。后端同样落库，刷新后标记仍在。
  stopped?: boolean
}

// 一条图片引用：url 类直发链接；file 类本地图（dataUrl 为内存态，仅当前会话/已懒加载后存在，
// path 为落盘后相对数据目录的路径，重载会话时由 AILoadImage 读回 dataUrl）。
interface AIMsgImage { kind: 'file' | 'url'; url?: string; path?: string; name?: string; dataUrl?: string }

// 待发送附件（发送前预览，尚未落库/组包）。
interface PendingImage { kind: 'file' | 'url'; url?: string; dataUrl?: string; name: string }

const emit = defineEmits<{ (e: 'open-settings', page?: string): void }>()

const conversations = ref<AIConversation[]>([])
const activeId = ref('')
const messages = ref<AIMessage[]>([])
const input = ref('')
const mode = ref('chat')
const streaming = ref(false)
const errorMsg = ref('')

// —— 流式文本缓冲 ——
// 关键性能点：token 到达时**不**直接写响应式 ref。改为先追加到普通字符串缓冲，
// 再按 ~16ms 的节奏批量提交到 streamingText。原因是每个 token 都触发一次
// 响应式更新 → 整个 AIPage 组件（含全部历史消息的 v-html diff）重渲染，
// 上游吐字越快掉帧越明显。批量提交把渲染次数从「每 token 一次」压到「每帧一次」。
const streamingText = ref('')
let streamBuf = ''
let streamReasonBuf = ''
let streamTimer: ReturnType<typeof setTimeout> | null = null
const STREAM_FLUSH_MS = 16

// —— Markdown 分块增量渲染 ——
// 性能根因：流式期间若每帧都对**全文**做 md.parse + v-html 整段替换，成本随回复
// 变长线性上升（实测 15KB 全文单帧 md.parse 约 10.5ms，其中 lexer 占 5ms），
// 而 v-html 的 innerHTML 赋值会销毁并重建该消息的**全部** DOM 节点。
// 两者叠加，就是「AI 一直在回复但界面发卡」的根因。
// 改为按顶层块增量渲染：
//   · 文本以「围栏代码块外的空行」为界切块，块内结构不跨空行拆散
//   · 每块的 HTML 单独缓存；已完成块复用同一字符串引用 → Vue 判定值未变，不写 DOM
//   · 每帧只重新解析最后一个未完成块（实测 10.5ms/帧 → 0.75ms/帧，约 14×）
// 流式态与完成态使用完全相同的 DOM 结构（都是 .ai-md-block 列表），
// 因此排版逐像素一致，消息落地时不会出现视觉跳变。
interface LiveBlock { raw: string; html: string }
const streamBlocks = ref<LiveBlock[]>([])

// 历史消息的块 HTML：按消息 id 记忆化。
//
// 为什么不用「整段文本 → 块数组」的全局 LRU：
// 之前 mdCache 上限 200 段，缓存淘汰采用「删最旧一条」。当一个会话的消息数
// 超过上限后，模板每帧遍历历史消息时，最早的那批条目正好被挤在淘汰线上，
// 于是每帧都要把全部历史消息重新 md.parse 一遍。
// 实测：400 段 × 3 块的会话，稳态帧 md.parse 次数 = 1200（= 消息数 × 块数），
// 也就是「越长的对话越卡」，而且是持续卡，不是只在首帧卡。
//
// AI 消息落库后内容不再变化，按消息 id 记忆化即可：
// 无容量上限（随会话消息数自然增长）、不会抖动、也不会误伤其他会话。
const msgBlockCache = new Map<string, { src: string; html: string[] }>()

// 流式块的 HTML 缓存（与历史块**分开**存放）。
// 分开的原因：两者共用一个 LRU 时，长对话的历史块会把流式块挤出去，
// 导致流式期间「已完成的块」每帧都被重新解析 —— 恰好是最需要流畅的时候掉帧。
// 流式块的规模只与单次回复的段落数有关，不会无限增长，故不设上限。
const liveBlockCache = new Map<string, string>()

// 以「围栏代码块之外的空行」为界切块。
// 切分点必须是 markdown 语义上独立的边界，否则分块渲染与整段渲染结果不一致
// （表现为流式过程中内容突然变形）。具体规则与边界场景测试见 utils/markdownBlocks.ts。
function splitMarkdownBlocks(src: string): string[] {
  return splitBlocks(src)
}

// 解析单个块（带缓存）。命中缓存时返回同一字符串引用，
// Vue 的 patchElement 对 innerHTML 只在值变化时赋值 → 不会产生 DOM 写入。
//
// 注意：只有**已完成**的块才写入缓存。流式中最后一个未完成块每帧原文都变，
// 若也缓存会产生大量一次性条目。未完成块每帧重解析本身开销就很小
// （实测末块仅占全文 0.2%）。
function renderBlock(raw: string, cacheable = true): string {
  const cached = liveBlockCache.get(raw)
  if (cached !== undefined) return cached
  const html = DOMPurify.sanitize(md.parse(raw, { async: false }) as string)
  if (cacheable) liveBlockCache.set(raw, html)
  return html
}

// 历史消息的块 HTML 数组：按消息 id 记忆化。
// 消息内容不可变，故可安全缓存；src 用于兜底校验（内容若被改动则重新解析）。
function renderMessageBlocks(m: AIMessage): string[] {
  const key = m.id
  const hit = msgBlockCache.get(key)
  if (hit && hit.src === m.content) return hit.html
  const raws = splitMarkdownBlocks(m.content ?? '')
  const html = raws.map((raw) => DOMPurify.sanitize(md.parse(raw, { async: false }) as string))
  msgBlockCache.set(key, { src: m.content ?? '', html })
  return html
}

// 流式增量渲染：只重新解析最后一个未完成块，其余块直接复用缓存。
//
// 对齐方式：只有**除最后一块外**的块才按原文做公共前缀匹配。
// 原因是最后一块是「正在写」的块，它的原文每帧都在变，若也要求原文完全相等，
// 就永远匹配不上 → 每次都从第 0 块重建，导致已渲染内容被顶替（实测复现）。
// 最后一块无条件重渲染（它的体量小，开销低），前面的块原文一旦稳定就不会再变。
function renderLiveBlocks(src: string) {
  const raws = splitMarkdownBlocks(src)
  const arr = streamBlocks.value
  const n = raws.length
  // 已完成块的数量：raws 的前 n-1 块（最后一块是进行中的）
  const doneCount = n > 0 ? n - 1 : 0
  // 计算与上一帧的公共前缀（只比较已完成块）
  let common = 0
  const limit = Math.min(doneCount, arr.length)
  while (common < limit && arr[common].raw === raws[common]) {
    common++
  }
  // 公共前缀的 HTML 必然一致（同一原文 → 同一缓存结果），无需重写
  for (let i = common; i < doneCount; i++) {
    const html = renderBlock(raws[i])
    if (i < arr.length) {
      if (arr[i].html !== html) arr[i].html = html
      arr[i].raw = raws[i]
    } else {
      arr.push({ raw: raws[i], html })
    }
  }
  // 最后一块（进行中）：无条件重渲染，且不写入缓存（原文每帧都变）
  if (n > 0) {
    const html = renderBlock(raws[n - 1], false)
    if (doneCount < arr.length) {
      if (arr[doneCount].html !== html) arr[doneCount].html = html
      arr[doneCount].raw = raws[n - 1]
    } else {
      arr.push({ raw: raws[n - 1], html })
    }
  }
  if (arr.length > n) arr.length = n
}

// 流式结束、消息落地时，把已渲染好的块直接交给历史消息缓存。
// 否则落地瞬间要把整条回复重新 md.parse 一遍（实测 15KB ≈ 10.5ms），
// 恰好发生在用户刚看到完整回复的时刻 —— 表现为「回复刚出来就卡一下」。
function primeMessageBlocks(id: string, content: string, html: string[]) {
  msgBlockCache.set(id, { src: content, html })
}

// 只保留当前会话仍在显示的消息缓存，丢弃其他会话的条目。
function pruneMessageBlocks(list: AIMessage[]) {
  if (msgBlockCache.size === 0) return
  const alive = new Set(list.map((m) => m.id))
  for (const id of Array.from(msgBlockCache.keys())) {
    if (!alive.has(id)) msgBlockCache.delete(id)
  }
}

// —— Mermaid 图渲染调度 ——
// 图不是同步渲染出来的：marked 只吐源码占位块，真正画图要等 DOM 挂上后走异步 API。
// 这个调度点用 nextTick 合并同一批更新（流式每帧都会触发，语义上只需要「这帧之后扫一遍」）。
let mermaidScanPending = false

function skipStreamTail(el: HTMLElement): boolean {
  // 流式尾块的围栏还没闭合，此时渲染必然失败；等它变成普通块（下一帧或回答结束）再画
  const block = el.closest('.ai-md-block') as HTMLElement | null
  if (!block || !block.closest('.ai-msg.streaming')) return false
  return block === block.parentElement?.lastElementChild
}

function scheduleMermaidScan() {
  if (mermaidScanPending) return
  mermaidScanPending = true
  nextTick(() => {
    mermaidScanPending = false
    renderMermaidIn(msgContent.value, { skip: skipStreamTail })
    enhanceTables(msgContent.value)
  })
}

// —— 表格增强：横向滚动 / 表头点击排序 / 导出 CSV ——
// 这些作用在 v-html 渲染出的真实 DOM 上（Vue 不托管），故走原生 API。
// 已处理的表打 data-tbl-enhanced，避免流式每帧重扫时重复注入。
function enhanceTables(root: HTMLElement | null) {
  if (!root) return
  const tables = root.querySelectorAll('table:not([data-tbl-enhanced])')
  for (const tbl of Array.from(tables) as HTMLTableElement[]) {
    tbl.setAttribute('data-tbl-enhanced', '1')
    const parent = tbl.parentElement
    // 横向滚动容器：表格可能比气泡宽，不包一层表格自己不会滚
    let wrap = parent?.classList.contains('md-table-wrap')
      ? (parent as HTMLElement)
      : null
    if (!wrap && parent) {
      wrap = document.createElement('div')
      wrap.className = 'md-table-wrap'
      parent.insertBefore(wrap, tbl)
      wrap.appendChild(tbl)
    }
    // 表头排序：给每个 thead th 打列索引 + 可点标记（marked 表格第一行即 thead>th）
    tbl.querySelectorAll('thead th').forEach((th, i) => {
      th.setAttribute('data-col', String(i))
      th.classList.add('md-th-sortable')
    })
    // 导出 CSV 按钮条（插在滚动容器上方）
    if (wrap && !wrap.previousElementSibling?.classList.contains('md-table-bar')) {
      const bar = document.createElement('div')
      bar.className = 'md-table-bar'
      const btn = document.createElement('button')
      btn.type = 'button'
      btn.className = 'md-table-export'
      btn.textContent = t('aiExportCsv')
      btn.addEventListener('click', () => exportTableCsv(tbl))
      bar.appendChild(btn)
      wrap.parentElement?.insertBefore(bar, wrap)
    }
  }
}

function csvCell(s: string): string {
  const v = (s || '').replace(/\r?\n/g, ' ').trim()
  return /[",\r\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v
}

function exportTableCsv(tbl: HTMLTableElement) {
  const lines = Array.from(tbl.querySelectorAll('tr')).map((r) =>
    Array.from(r.children)
      .map((c) => csvCell(c.textContent ?? ''))
      .join(','),
  )
  const csv = '﻿' + lines.join('\r\n') // 前导 BOM：避免 Excel 把中文 CSV 读成乱码
  downloadBlob(new Blob([csv], { type: 'text/csv;charset=utf-8' }), 'table.csv')
  toast.success(t('aiDownloadOk', { name: 'table.csv' }))
}

function sortTableByColumn(th: HTMLElement) {
  const tbl = th.closest('table') as HTMLTableElement | null
  if (!tbl) return
  const col = Number(th.dataset.col ?? '0')
  const tbody = (tbl.querySelector('tbody') as HTMLTableSectionElement | null) || tbl
  const rows = Array.from(tbody.querySelectorAll('tr'))
  if (!rows.length) return
  const prevCol = tbl.dataset.sortCol
  const prevDir = tbl.dataset.sortDir
  const dir = prevCol === String(col) && prevDir === 'asc' ? 'desc' : 'asc'
  const numeric = rows.every((r) => {
    const v = (r.children[col]?.textContent ?? '').trim()
    return v !== '' && !Number.isNaN(Number(v))
  })
  rows.sort((a, b) => {
    const av = (a.children[col]?.textContent ?? '').trim()
    const bv = (b.children[col]?.textContent ?? '').trim()
    const cmp = numeric ? Number(av) - Number(bv) : av.localeCompare(bv, undefined, { numeric: true })
    return dir === 'asc' ? cmp : -cmp
  })
  rows.forEach((r) => tbody.appendChild(r))
  tbl.dataset.sortCol = String(col)
  tbl.dataset.sortDir = dir
  // 指示符：只有当前列显示 ▲/▼，其余清掉
  tbl.querySelectorAll('.md-th-sortable').forEach((el) => {
    if (el !== th) el.removeAttribute('data-sort-dir')
  })
  th.setAttribute('data-sort-dir', dir)
}

// 提交缓冲区到响应式状态。done/停止时必须立即 flush，否则尾部文字会丢。
function flushStream() {
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  if (streamBuf) {
    streamingText.value += streamBuf
    streamBuf = ''
  }
  if (streamReasonBuf) {
    reasoningText.value += streamReasonBuf
    streamReasonBuf = ''
  }
  renderLiveBlocks(streamingText.value)
  scheduleMermaidScan()
  followBottom()
}

function scheduleStreamFlush() {
  if (streamTimer) return
  streamTimer = setTimeout(() => {
    streamTimer = null
    flushStream()
  }, STREAM_FLUSH_MS)
}

const reasoningText = ref('')  // 思考过程（reasoning_content），折叠显示

// 本地 AI 流式服务信息（端口 + 随机令牌，通过 Header 传递）
const aiStreamPort = ref(0)
const aiStreamToken = ref('')
let streamCtrl: AbortController | null = null

// —— 滚动跟随 ——
// 根因：原先用 watch(streamingText) 驱动滚动，但真正改变 DOM 高度的是
// markdown 渲染结果，两者时间点错位，scrollHeight 读到的是旧值。
// 现在改为：ResizeObserver 观察消息容器内容高度变化 → 变化即滚到底。
// 同时记录用户是否主动上滑（stickToBottom），上滑后不再强制抢滚动，改为显示「回到底部」按钮。
const msgArea = ref<HTMLElement | null>(null)
const msgContent = ref<HTMLElement | null>(null)
const stickToBottom = ref(true)
const showJumpBtn = ref(false)
let resizeObs: ResizeObserver | null = null
const BOTTOM_THRESHOLD = 48  // 距底部小于该像素即视为「贴底」

function onMsgScroll() {
  const el = msgArea.value
  if (!el) return
  const dist = el.scrollHeight - el.scrollTop - el.clientHeight
  stickToBottom.value = dist <= BOTTOM_THRESHOLD
  showJumpBtn.value = !stickToBottom.value
}

function scrollToBottom(smooth = false) {
  const el = msgArea.value
  if (!el) return
  nextTick(() => {
    const e = msgArea.value
    if (!e) return
    if (smooth) {
      e.scrollTo({ top: e.scrollHeight, behavior: 'smooth' })
    } else {
      e.scrollTop = e.scrollHeight
    }
  })
}

// 流式过程中跟随底部：内容追加后把视口拉到底。
// 用户主动上滑查看历史时（stickToBottom=false）不抢滚动，只显示「回到底部」按钮。
//
// 必须走 nextTick：调用时 Vue 还没把新的 markdown 渲染进 DOM，
// 此时读 scrollHeight 拿到的是上一帧的旧高度，滚不到位。
function followBottom() {
  if (!stickToBottom.value) return
  nextTick(() => {
    // 下一帧用户可能已主动上滑，这时不该再抢滚动
    if (!stickToBottom.value) return
    const el = msgArea.value
    if (!el) return
    // 直接写 scrollTop 会触发 onMsgScroll，把贴底状态重新判定为 true —— 正是期望行为
    el.scrollTop = el.scrollHeight
  })
}

function jumpToBottom() {
  stickToBottom.value = true
  showJumpBtn.value = false
  scrollToBottom(true)
}

// 多档案 / 模型切换
const aiProfiles = ref<AIProfile[]>([])
const aiActive = ref('')
const hasKey = computed(() => {
  const p = aiProfiles.value.find((x) => x.id === aiActive.value)
  return !!(p && p.apiKey)
})
const modes = [
  { key: 'chat', label: 'aiModeChat' },
  { key: 'explain', label: 'aiModeExplain' },
  { key: 'translate', label: 'aiModeTranslate' },
  { key: 'summarize', label: 'aiModeSummarize' },
  { key: 'tutorial', label: 'aiModeTutorial' },
]

// —— 图片附件 ——
// attachments 为待发送预览；发送后清空。file 类存 dataUrl（base64），直接随请求发给模型；
// url 类存公网链接。imageInputMode 来自当前激活档案的声明（both/url/base64），
// 决定允许哪种附件——Agnes 这类只收 URL 的模型要禁用本地图选择（见 ai_image.go 闸门）。
const attachments = ref<PendingImage[]>([])

// 当前激活档案的 imageInput 能力；空（旧档案）按 both 处理，与后端 normalizeImageInput 一致。
const imageInputMode = computed<string>(() => {
  const p = aiProfiles.value.find((x) => x.id === aiActive.value)
  const v = (p?.imageInput || 'both').toLowerCase()
  if (v === 'url' || v === 'base64') return v
  return 'both'
})
// 是否允许添加本地图片 / 公网链接（两者在 both 下都允许）。
const allowFileImage = computed(() => imageInputMode.value === 'both' || imageInputMode.value === 'base64')
const allowUrlImage = computed(() => imageInputMode.value === 'both' || imageInputMode.value === 'url')

// 本地图片大小上限（与后端 10 MiB 对齐，前端先拦一道，避免把巨大 base64 塞进请求体）。
const maxImageBytes = 10 * 1024 * 1024

// 把 File 转 data URL（前端预览 + 随请求发送）。仅读取为 base64，格式校验交给后端
// （magic bytes 校验，比前端扩展名更可靠），前端不做硬性拒绝以免误伤。
function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(r.result as string)
    r.onerror = () => reject(new Error('读取失败'))
    r.readAsDataURL(file)
  })
}

async function addFiles(files: FileList | File[]) {
  if (!allowFileImage.value) { toast.error(t('aiImgOnlyUrl')); return }
  for (const file of Array.from(files)) {
    if (!file.type.startsWith('image/')) { toast.error(t('aiImgOnlyLocal')); continue }
    if (file.size > maxImageBytes) { toast.error(t('aiImageTooLarge')); continue }
    const dataUrl = await fileToDataURL(file)
    attachments.value.push({ kind: 'file', dataUrl, name: file.name || 'image' })
  }
  if (attachments.value.length) toast.success(t('aiImageAdded', { n: attachments.value.length }))
}

function addUrlImage(url: string) {
  const u = url.trim()
  if (!u) return
  if (!allowUrlImage.value) { toast.error(t('aiImgOnlyLocal')); return }
  if (!/^https?:\/\//i.test(u)) { toast.error(t('aiImageUrlInvalid')); return }
  attachments.value.push({ kind: 'url', url: u, name: u })
}

function removeAttachment(i: number) {
  attachments.value.splice(i, 1)
}

// 剪贴板粘贴图片：在输入框按 Ctrl+V 时，若剪贴板含图片文件则作为本地图附件。
function onInputPaste(e: ClipboardEvent) {
  const items = e.clipboardData?.items
  if (!items) return
  const imgs: File[] = []
  for (const it of Array.from(items)) {
    if (it.kind === 'file' && it.type.startsWith('image/')) {
      const f = it.getAsFile()
      if (f) imgs.push(f)
    }
  }
  if (imgs.length) {
    e.preventDefault()
    addFiles(imgs)
  }
}

// 拖拽图片到输入区：drop 时取图片文件作为本地图附件。
function onInputDrop(e: DragEvent) {
  const files = e.dataTransfer?.files
  if (files && files.length) {
    e.preventDefault()
    const imgs = Array.from(files).filter((f) => f.type.startsWith('image/'))
    if (imgs.length) addFiles(imgs)
  }
}

// 历史消息图片懒加载：把后端存盘（file 类）的路径读回 dataUrl，回填到消息的 imgList。
// url 类直发链接，无需加载。返回供模板渲染用的图片数组（file 类未加载完毕时 dataUrl 为空）。
async function resolveMessageImages(msgs: AIMessage[]) {
  for (const m of msgs) {
    if (!m.images) { m.imgList = []; continue }
    let arr: AIMsgImage[] = []
    try {
      const raw = JSON.parse(m.images)
      if (Array.isArray(raw)) {
        arr = raw.map((x: any) => ({ kind: x.kind, url: x.url, path: x.path, name: x.name }))
      }
    } catch { arr = [] }
    for (const img of arr) {
      if (img.kind === 'file' && img.path) {
        try {
          const d = unwrap<string>(await AILoadImage(img.path))
          if (d) img.dataUrl = d
        } catch { /* 读不到图不阻断对话，缩略图留空 */ }
      }
    }
    m.imgList = arr
  }
}

// 图片链接输入框（点击「图片链接」后展开的小输入框），避免每次都弹 prompt。
const urlInputOpen = ref(false)
const urlDraft = ref('')
const fileInputEl = ref<HTMLInputElement | null>(null)

function onPickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  if (input.files && input.files.length) addFiles(input.files)
  input.value = '' // 允许重复选择同一文件
}

function openFilePicker() {
  fileInputEl.value?.click()
}

function toggleUrlInput() {
  urlInputOpen.value = !urlInputOpen.value
  if (urlInputOpen.value) urlDraft.value = ''
}

function confirmUrlImage() {
  addUrlImage(urlDraft.value)
  urlDraft.value = ''
  urlInputOpen.value = false
}

// —— Markdown 渲染 ——
// 用独立 Marked 实例（而非全局 marked），避免污染 NoteManagerPage 的全局配置，
// 同时便于扩展 code renderer：给代码块加语言标签 + 复制按钮。
const md = new Marked({ breaks: true, gfm: true })

// 流程图工具栏的文案。utils 层是纯函数、没有 i18n 上下文，所以由这里翻好再传下去；
// 做成函数而不是常量，切语言后新渲染的图能立刻用上新文案。
function mermaidLabels(): MermaidLabels {
  return {
    error: t('aiMermaidError'),
    source: t('aiMermaidSource'),
    diagram: t('aiMermaidDiagram'),
    fold: t('aiMermaidFold'),
    expand: t('aiMermaidExpand'),
  }
}

// 数学公式：AI 回答里的公式一律是 LaTeX 源码，没有这一步就会被当成正文原样吐出来。
// 规则与边界见 utils/mathMarkdown.ts（含流式半截公式的降级处理）。
md.use(mathExtension())

// DOMPurify 默认白名单已包含 button / svg / pre / code，且默认放行 data-* 属性
// （ALLOW_DATA_ATTR=true）与 aria-*（ALLOW_ARIA_ATTR=true），
// 因此复制按钮可以安全地写在 v-html 里，用事件委托处理点击。
// 注意：viewBox 在白名单里是全小写 'viewbox'，DOMPurify 会把属性名统一转小写后比对，
// 故 SVG 必须用小写 viewbox 才能通过。
md.use({
  renderer: {
    code(token: { text: string; lang?: string }) {
      const lang = (token.lang || '').split(/\s+/)[0] || ''
      // ```latex / ```math 这类围栏装的是公式而不是代码：渲染成公式，
      // 也不再给「复制代码」按钮——复制一段 LaTeX 源码回聊天框没有意义。
      if (isMathFenceLang(lang)) return renderMath(token.text, true)
      // ```mermaid 装的是图定义：先吐源码占位块，等 DOM 挂上后再异步渲染成 SVG。
      // 为什么不能在这里直接渲染：Mermaid 只有异步 API，而这条链路是同步的（详见 utils/mermaidMarkdown.ts）。
      if (isMermaidFenceLang(lang)) return mermaidPlaceholder(token.text, mermaidLabels())
      const codeText = token.text.replace(/\n$/, '')
      const label = lang || 'text'
      // 语法高亮：已知语言走精确高亮，未知语言交给 highlightAuto（按内容猜）。
      // hljs 返回的 value 已是「转义后 + <span class="hljs-*" >」的 HTML，无需再 escapeHtml。
      let body: string
      try {
        const langLower = lang.toLowerCase()
        body = lang && hljs.getLanguage(langLower)
          ? hljs.highlight(codeText, { language: langLower }).value
          : hljs.highlightAuto(codeText).value
      } catch {
        body = escapeHtml(codeText)
      }
      // html 代码块给一个「预览」按钮：模型给的示例页只有真的跑起来才知道对不对，
      // 存成文件再双击打开太绕。实际执行放在沙箱 iframe 里，见 openHtmlPreview。
      const runnable = /^(html|htm)$/i.test(lang)
      const runBtn = runnable
        ? `<button type="button" class="md-code-run" data-md-run="1" title="${escapeHtml(t('aiPreviewHtml'))}" aria-label="${escapeHtml(t('aiPreviewHtml'))}">` +
            `<svg viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polygon points="6 3 20 12 6 21 6 3"></polygon></svg>` +
          `</button>`
        : ''
      return `<div class="md-code" data-md-code>` +
        `<div class="md-code-bar">` +
          `<span class="md-code-lang">${escapeHtml(label)}</span>` +
          `<span class="md-code-actions">` +
            runBtn +
            `<button type="button" class="md-code-copy" data-md-copy="1" title="${escapeHtml(t('copyCode'))}" aria-label="${escapeHtml(t('copyCode'))}">` +
              `<svg viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>` +
            `</button>` +
          `</span>` +
        `</div>` +
        `<pre><code${lang ? ` class="language-${escapeHtml(lang)}"` : ''}>${body}</code></pre>` +
      `</div>`
    },
  },
})

function escapeHtml(s: string): string {
  return String(s ?? '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}

async function loadProfiles() {
  try {
    const res = unwrap<AIProfilesResult>(await AIListProfiles())
    if (!res) return
    aiProfiles.value = res.profiles ?? []
    aiActive.value = res.active || (aiProfiles.value[0]?.id ?? '')
    // 以服务端返回的「当前生效档案」为准，作为切模型失败时的还原基准
    lastActiveProfile = aiActive.value
  } catch (e) {
    aiProfiles.value = []
    errorMsg.value = getErrorMessage(e)
  }
}

// ---- 会话搜索 / 分批渲染 ----
// 列表一次全量拉、全量渲染：上百个会话时侧栏本身就成了负担，而真正要找的
// 往往是最近几条。搜 + 分批显示，两个毛病一起解决。
const convQuery = ref('')
const CONV_PAGE = 30
const convLimit = ref(CONV_PAGE)

const filteredConversations = computed(() => {
  const q = convQuery.value.trim().toLowerCase()
  if (!q) return conversations.value
  return conversations.value.filter((c) => (c.title || '').toLowerCase().includes(q))
})
const shownConversations = computed(() => filteredConversations.value.slice(0, convLimit.value))

watch(convQuery, () => { convLimit.value = CONV_PAGE })

async function loadConversations() {
  try {
    const list = unwrap<AIConversation[]>(await AIListConversations())
    conversations.value = list ?? []
  } catch (e) {
    errorMsg.value = getErrorMessage(e)
  }
}

let messagesGen = 0
async function loadMessages(convID: string) {
  const gen = ++messagesGen
  try {
    const msgs = unwrap<AIMessage[]>(await AIGetMessages(convID))
    if (gen !== messagesGen) return // 已有更新的会话选择，丢弃过期响应
    // 解析历史消息图片：file 类从盘上读回 dataUrl（异步），url 类直发链接。
    // 在赋值 messages.value 之前完成，避免先渲染无图再补图导致的重渲染与闪烁。
    const list = msgs ?? []
    await resolveMessageImages(list)
    if (gen !== messagesGen) return // 等待图片加载期间用户已切换会话，丢弃过期响应
    messages.value = list
  } catch (e) {
    if (gen !== messagesGen) return
    toast.error(getErrorMessage(e))
    return
  }
  // 只保留当前会话的消息缓存：按 id 记忆化没有容量上限，
  // 若不清理，反复切换长会话会让缓存无限增长（这是内存问题，不是性能问题）。
  pruneMessageBlocks(messages.value)
  // 切换会话后强制贴底（用户主动切会话，期望看最新一条）
  stickToBottom.value = true
  showJumpBtn.value = false
  scrollToBottom()
  // 历史消息里可能有 ```mermaid（图不是同步渲染的，得在 DOM 挂上后补扫一遍）
  scheduleMermaidScan()
}

async function selectConv(id: string) {
  // 如果有正在流的对话，先打断
  if (streaming.value) stop()
  activeId.value = id
  resetStreamState()
  errorMsg.value = ''
  await loadMessages(id)
}

function newConv() {
  if (streaming.value) stop()
  activeId.value = ''
  messages.value = []
  resetStreamState()
  errorMsg.value = ''
}

// 重置所有流式状态（切会话/新会话时调用）
function resetStreamState() {
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  streamBuf = ''
  streamReasonBuf = ''
  streamingText.value = ''
  streamBlocks.value = []
  reasoningText.value = ''
  // 流式块缓存按回复隔离：一条回复的块原文可能与另一条完全相同
  // （如「好的」这类短回复），跨回复复用是安全的，但没必要长期持有。
  liveBlockCache.clear()
}

async function delConv(id: string) {
  if (!(await toast.confirm(t('confirmDelete')))) return
  // 删的正是当前在跑的会话：先掐流再删。否则后端会先发现「会话已被删除」
  // 而把内容写失败，前端还会抱着一条指向已删会话的流继续读。
  if (streaming.value && activeId.value === id) stop()
  try {
    unwrap(await AIDeleteConversation(id))
    if (activeId.value === id) newConv()
    await loadConversations()
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 上一个真正生效的模型：切模型失败时把下拉框拉回去。
// 不还原的话，界面显示的是新模型、后端实际请求还是旧模型——
// 这种「看起来切了其实没切」的不一致是最难排查的一类问题。
let lastActiveProfile = ''

async function onModelChange() {
  const want = aiActive.value
  if (!want) return
  try {
    unwrap(await AISetActiveProfile(want))
    lastActiveProfile = want
  } catch (e: any) {
    toast.error(getErrorMessage(e))
    aiActive.value = lastActiveProfile
  }
}

// ---- 失败重试 ----
// 后端在调模型之前就把用户消息落库了（所以失败后历史不会丢），
// 但没有重试入口时用户只能手动重打一遍问题——图片还得重新贴。
// retryPending 让 send() 跳过再插一个 user 气泡：那条消息本来就在列表里，
// 后端 saveUserTurn 对「同内容 + 同图片」也是幂等的，重发不会产生重复行。
let retryPending = false

function canRetry(): boolean {
  if (streaming.value) return false
  return messages.value.some((m) => m.role === 'user')
}

function retryLast() {
  if (!canRetry()) return
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const m = messages.value[i]
    if (m.role !== 'user') continue
    errorMsg.value = ''
    retryPending = true
    input.value = m.content
    attachments.value = (m.imgList || []).map((img) => ({
      kind: img.kind,
      url: img.url,
      dataUrl: img.dataUrl,
      name: img.name || 'image',
    }))
    send()
    return
  }
}

async function regenTitle() {
  if (!activeId.value || streaming.value) return
  try {
    const title = unwrap<string>(await AIRegenerateTitle(activeId.value))
    if (title) {
      const i = conversations.value.findIndex((c) => c.id === activeId.value)
      if (i >= 0) conversations.value[i].title = title
      else loadConversations()
    }
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// ---- 会话改名 ----
//
// 自动生成的标题常常只是「大概意思」，用户想换成自己能记住的说法；而重生成
// 要再花一次模型调用（输入是整段对话），改两个字不该有这种代价。
// 底层 UpdateAIConversationMeta 一直都在（摘要压缩也在用它），此前只是没有对外入口。
//
// 做成行内编辑而不是弹窗：为一个短标题开一层模态框，输入成本比标题本身还高。
const renamingId = ref('')
const renameText = ref('')
const renameInput = ref<HTMLInputElement | null>(null)

function startRename(c: AIConversation) {
  renamingId.value = c.id
  renameText.value = c.title || ''
  // 等 input 渲染出来再聚焦并全选：不全选的话，用户得先手动删掉旧标题才能打新的。
  nextTick(() => {
    renameInput.value?.focus()
    renameInput.value?.select()
  })
}

function cancelRename() {
  renamingId.value = ''
  renameText.value = ''
}

/**
 * 提交改名。
 *
 * Enter 与 blur 都会走到这里（Enter 后输入框随即失焦），所以进函数先清 renamingId：
 * 第二次调用读到空 id 直接返回，避免同一份标题被提交两遍。
 */
async function commitRename(c: AIConversation) {
  const id = renamingId.value
  if (!id) return
  const title = renameText.value.trim()
  renamingId.value = ''
  // 没改或删空都当作放弃：后端会拒绝空标题，为「改到一半又不想改」弹个错误太吵。
  if (!title || title === (c.title || '')) return
  try {
    const saved = unwrap<string>(await AIRenameConversation(id, title))
    const i = conversations.value.findIndex((x) => x.id === id)
    if (i >= 0 && saved) conversations.value[i].title = saved
  } catch (e) {
    toast.error(getErrorMessage(e))
    // 失败时拉回真实值：界面上的标题若停在用户改过的样子，会与库里不一致。
    await loadConversations()
  }
}

async function clearContext() {
  if (!activeId.value) return
  if (!(await toast.confirm(t('aiClearConfirm')))) return
  try {
    unwrap(await AIClearMessages(activeId.value))
    messages.value = []
    resetStreamState()
    errorMsg.value = ''
    toast.success(t('aiCleared'))
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

// 确保已拿到本地流式服务端口+令牌
async function ensureStreamInfo() {
  if (aiStreamPort.value) return
  const info = unwrap<{ port: number; token: string }>(await AIStreamInfo())
  if (!info) throw new Error('流式服务不可用')
  aiStreamPort.value = info.port
  aiStreamToken.value = info.token
}

// 单条回复的统计信息（耗时 / 首字延迟 / token 用量）
interface AIStreamStat { elapsedMs: number; firstTokenMs: number; promptTokens: number; completionTokens: number; cachedTokens: number; cost?: number }
// messageId -> 统计。挂在消息对象上随消息一起渲染。
const statMap = reactive(new Map<string, AIStreamStat>())

// 把统计渲染成一行小字。只展示有意义的项：
// 全耗时 + 首字延迟（用户能感知的等待）+ token 用量。
// 首字延迟为 0 说明是中断的回复（没等到首字），此时不展示该项。
function formatStat(stat: AIStreamStat): string {
  const parts: string[] = []
  // 耗时只在「本次会话刚生成」时才有（从库里读回来的消息没有），0 即隐藏
  if (stat.elapsedMs > 0) parts.push(t('aiStatElapsed', { ms: formatDuration(stat.elapsedMs) }))
  if (stat.firstTokenMs > 0) {
    parts.push(t('aiStatFirstToken', { ms: formatDuration(stat.firstTokenMs) }))
  }
  if (stat.promptTokens > 0 || stat.completionTokens > 0) {
    const pct = stat.promptTokens > 0 ? Math.round((stat.cachedTokens / stat.promptTokens) * 1000) / 10 : 0
    parts.push(pct > 0
      ? t('aiStatTokensCached', { in: fmtTokens(stat.promptTokens), out: fmtTokens(stat.completionTokens), pct })
      : t('aiStatTokens', { in: fmtTokens(stat.promptTokens), out: fmtTokens(stat.completionTokens) }))
  }
  // 费用由后端算（价目表只有 ai_pricing.go 一份）；未收录的模型没有 cost，
  // 这里就不显示——与设置页「未知模型不计价」保持同一口径。
  if (stat.cost && stat.cost > 0) parts.push(t('aiStatCost', { c: fmtCost(stat.cost) }))
  return parts.join(' · ')
}

// token 数折算成 k/M：真实对话的输入常是几万到几十万，原样打印会把这一行拖得很长
function fmtTokens(n: number): string {
  if (!n) return '0'
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 1 : 2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(n >= 1e4 ? 0 : 1) + 'k'
  return String(n)
}

// 费用格式化：与设置页用量面板同一口径（小额多给两位小数，否则 ¥0.00 会把
// 「这条几乎不要钱」和「这条免费」显示成一个样子）。
function fmtCost(n: number): string {
  if (!n) return '¥0'
  if (n >= 1) return '¥' + n.toFixed(2)
  if (n >= 0.01) return '¥' + n.toFixed(3)
  return '¥' + n.toFixed(4)
}

// 单条消息要显示的统计：优先用本次会话内存里的（含耗时/首字），
// 否则回落到消息自带的用量 —— 这样刷新页面或换会话回来，token 与缓存命中仍在。
function msgStat(m: AIMessage): AIStreamStat | null {
  const live = statMap.get(m.id)
  if (live) return live
  if (!m.prompt_tokens && !m.completion_tokens) return null
  return {
    elapsedMs: 0, firstTokenMs: 0,
    promptTokens: m.prompt_tokens || 0,
    completionTokens: m.completion_tokens || 0,
    cachedTokens: m.cached_tokens || 0,
    cost: m.cost || 0,
  }
}

// 毫秒 → 人类可读：<1s 用 ms，否则一位小数的秒
function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

async function send() {
  // 先读后清：任何提前 return（没配 Key / 空消息 / 正在流式）都不能把标志
  // 留给下一次手动发送，否则那条用户发言将不再出现在气泡里。
  const reuseUserBubble = retryPending
  retryPending = false
  if (streaming.value) return
  const text = input.value.trim()
  if (!text && attachments.value.length === 0) return
  if (!hasKey.value) {
    toast.error(t('aiNeedKey'))
    emit('open-settings', 'ai')
    return
  }
  const convId = activeId.value
  // 发送时的模型名：流式结束前用户可能切档案（onModelChange 会回滚），
  // 事后读 activeProfile 拿到的是新值，所以此刻先定下来。
  const streamModel = aiProfiles.value.find((x) => x.id === aiActive.value)?.model || ''
  // 本轮流式归属的会话：新建会话时由 conv 事件回填。中断/失败时都要按它判断
  // 「用户是否还停留在同一会话」，不能读 activeId —— 用户中途可能已经切走。
  let streamConvId = convId
  // 把待发送附件组包为发送给模型的图片数组（file 带 dataUrl，url 带链接），
  // 同时生成渲染用的 imgList（file 直用 dataUrl，无需二次加载）。
  const imgs: PendingImage[] = attachments.value
  const imgList: AIMsgImage[] = imgs.map((a) => a.kind === 'url'
    ? { kind: 'url', url: a.url, name: a.name }
    : { kind: 'file', dataUrl: a.dataUrl, name: a.name })
  const imagesPayload = imgs.map((a) => a.kind === 'url'
    ? { kind: 'url', url: a.url, name: a.name }
    : { kind: 'file', dataUrl: a.dataUrl, name: a.name })
  // 立即把用户消息显示出来，随后在消息区内等待 AI 流式回复
  const msg: AIMessage = {
    id: 'usr-' + Date.now() + '-' + Math.random().toString(36).slice(2, 7), conv_id: convId, role: 'user', content: text,
    created_at: new Date().toISOString(), imgList,
  }
  // 重试：那条 user 消息本来就在列表里，别再插一个重复气泡。
  // （后端 saveUserTurn 对「同内容 + 同图片」幂等，DB 里也不会多出一行）
  if (!reuseUserBubble) messages.value.push(msg)
  stickToBottom.value = true
  showJumpBtn.value = false
  scrollToBottom()
  input.value = ''
  attachments.value = []
  streaming.value = true
  resetStreamState()
  errorMsg.value = ''
  const startedAt = performance.now()
  try {
    await ensureStreamInfo()
    const ctrl = new AbortController()
    streamCtrl = ctrl
    const resp = await fetch(`http://127.0.0.1:${aiStreamPort.value}/ai/stream`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-Stream-Token': aiStreamToken.value,
      },
      body: JSON.stringify({ convId, mode: mode.value, message: text, images: imagesPayload }),
      signal: ctrl.signal,
    })
    if (!resp.body) throw new Error('无响应流')
    const reader = resp.body.getReader()
    const dec = new TextDecoder()
    let buf = ''
    // SSE 解析：消息以 \n\n 分隔，每条消息含 event: 和 data: 行
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      let sep
      while ((sep = buf.indexOf('\n\n')) >= 0) {
        const rawMsg = buf.slice(0, sep)
        buf = buf.slice(sep + 2)

        let event = 'message'
        let dataStr = ''
        for (const line of rawMsg.split('\n')) {
          if (line.startsWith('event: ')) {
            event = line.slice(7).trim()
          } else if (line.startsWith('data: ')) {
            dataStr += line.slice(6)
          }
          // 忽略注释行（以 : 开头）和空行
        }
        if (!dataStr) continue

        let data: any
        try { data = JSON.parse(dataStr) } catch { continue }

        switch (event) {
          case 'token':
            // 写入普通缓冲，按帧批量提交到响应式状态
            if (data.text) { streamBuf += data.text; scheduleStreamFlush() }
            break
          case 'reasoning':
            if (data.text) { streamReasonBuf += data.text; scheduleStreamFlush() }
            break
          case 'conv':
            if (data.id) { activeId.value = data.id; streamConvId = data.id }
            if (data.title !== undefined) {
              const i = conversations.value.findIndex((c) => c.id === data.id)
              if (i >= 0) conversations.value[i].title = data.title
              else loadConversations()
            }
            break
          case 'done':
            if (data.convId) {
              if (data.convId !== activeId.value) break // 对话已被切换，忽略过期事件
              // 立即 flush，把缓冲里的尾部文字全部落到响应式状态
              flushStream()
              const content = streamingText.value
              const reasoning = reasoningText.value
              if (content || reasoning) {
                const msg: Record<string, any> = { id: `ast-${Date.now()}`, conv_id: data.convId, role: 'assistant', content: content || '', created_at: new Date().toISOString() }
                // 模型名由后端回传（它就是落库的那份），拿不到才退回发送时的档案
                const usedModel = String(data.model || streamModel || '').trim()
                if (usedModel) msg.model = usedModel
                if (reasoning) msg.reasoning_content = reasoning
                messages.value.push(msg as AIMessage)
                // 复用流式期已渲染好的块 HTML，避免落地瞬间整条重新解析
                primeMessageBlocks(msg.id as string, content, streamBlocks.value.map((b) => b.html))
                // 记录本条回复的耗时/token，用于在消息下方展示
                const stat: AIStreamStat = {
                  elapsedMs: Math.round(performance.now() - startedAt),
                  firstTokenMs: Number(data.firstTokenMs) || 0,
                  promptTokens: Number(data.promptTokens) || 0,
                  completionTokens: Number(data.completionTokens) || 0,
                  cachedTokens: Number(data.cachedTokens) || 0,
                  // 后端算好带回来；未收录模型没有这个字段 → 0 即隐藏
                  cost: Number(data.cost) || 0,
                }
                statMap.set(msg.id as string, stat)
                // 同步到消息对象：刷新后由 DB 里的同名字段接管，展示逻辑不用分两套
                Object.assign(msg, {
                  prompt_tokens: stat.promptTokens,
                  completion_tokens: stat.completionTokens,
                  cached_tokens: stat.cachedTokens,
                })
                scrollToBottom()
              }
              streamingText.value = ''
              streamBlocks.value = []
              reasoningText.value = ''
              loadConversations()
            }
            break
          case 'error':
            flushStream()
            errorMsg.value = data.message || data.error || '未知错误'
            break
        }
      }
    }
  } catch (e: any) {
    if (e && e.name !== 'AbortError') errorMsg.value = getErrorMessage(e)
  } finally {
    // 中断（用户点停止）时：把已生成的内容固化成一条正式消息，避免内容凭空消失。
    // 之前 stop() 只是 abort，已收到的文字既没进 messages 也没清空，
    // 结果是「AI 一直在回复」但内容卡在半截、也无法复制。
    // 必须先 flush：否则还留在缓冲里（streamBuf）的尾部文字会随 clear 一起丢掉。
    flushStream()
    if (streamingText.value || reasoningText.value) {
      const content = streamingText.value
      const reasoning = reasoningText.value
      // 只往「用户还停留着的那个会话」里补气泡。切走之后后端仍会把半截内容落库
      // （带 stopped 标记），但绝不能塞进另一个会话的消息列表——以前这里用的是
      // activeId.value，selectConv 先 stop 再改 activeId，半截内容就串到新会话里了。
      if (content || reasoning) {
        if (streamConvId && activeId.value === streamConvId) {
          const aid = `ast-${Date.now()}-stop`
          const msg: Record<string, any> = { id: aid, conv_id: streamConvId, role: 'assistant', content: content || '', created_at: new Date().toISOString(), stopped: true }
          // 中断时后端还没发 done，模型名用发送时记下的那份
          if (streamModel) msg.model = streamModel
          if (reasoning) msg.reasoning_content = reasoning
          messages.value.push(msg as AIMessage)
          // 同样复用流式期已渲染的块
          primeMessageBlocks(aid, content, streamBlocks.value.map((b) => b.html))
          const stat: AIStreamStat = {
            elapsedMs: Math.round(performance.now() - startedAt),
            firstTokenMs: 0,
            promptTokens: 0,
            completionTokens: 0,
            cachedTokens: 0,
          }
          statMap.set(aid, stat)
        }
      }
    }
    streamingText.value = ''
    streamBlocks.value = []
    reasoningText.value = ''
    streamBuf = ''
    streamReasonBuf = ''
    streaming.value = false
    streamCtrl = null
    // 流式结束后尾块不再是「进行中」：补一次扫描，把最后一张图（可能就在结尾）画出来
    scheduleMermaidScan()
    // 在别的页面期间回答跑完了：给侧栏一个未读点，否则用户不知道已经能看
    if (!pageActive.value) aiFinishedUnread.value = true
  }
}

function stop() {
  if (streamCtrl) streamCtrl.abort()
}

// ---- 保活（KeepAlive）后的可见性 ----
// 本页被 App 的 KeepAlive 白名单留住：切到别的导航页时组件只是 deactivate，
// 正在进行的 fetch 流和已生成的文字都还在（以前是销毁 → onUnmounted 里 abort →
// 回答整段消失）。代价是用户在别的页看不到任何迹象，所以把状态外抛给侧栏：
// 生成中 = 转圈，已生成且用户还没回来 = 未读点。
const pageActive = ref(true)
watch(streaming, (v) => { aiGenerating.value = v })

// 告诉后端「AI 页是不是在前台」。
//
// 为什么需要：完成通知的判定不能只看窗口焦点 —— 用户在 AI 页盯着回答生成时，
// 可能去点了一下别的应用（窗口失焦），按焦点判断就会往通知中心塞一条
// 「AI 回答已完成」，而他刚刚就看着这句话写完。后端逻辑见 services/ai/ai_stream.go。
//
// 失败无妨（后端是旧二进制时没有这个方法）：最坏退化成按窗口焦点判断的老行为。
function reportAIPageVisible(visible: boolean) {
  try {
    const p = SetAIPageVisible(visible)
    if (p && typeof p.catch === 'function') p.catch(() => undefined)
  } catch {
    // 绑定不存在（应用未重启）时忽略
  }
}

onActivated(() => {
  pageActive.value = true
  aiFinishedUnread.value = false
  reportAIPageVisible(true)
  // 回来时停在原位会看不到后面生成的内容：流式期间一直是贴底的，直接拉到底
  if (streaming.value) nextTick(scrollToBottom)
})
onDeactivated(() => {
  pageActive.value = false
  reportAIPageVisible(false)
})

async function copy(text: string) {
  // 统一入口见 utils/clipboard：navigator.clipboard 在 WebView2 里要么被静默拒绝，
  // 要么（execCommand 兜底）返回 true 但其实没写进去——两种都会让用户看到「已复制」的假象。
  const ok = await copyToClipboard(text ?? '')
  if (ok) {
    toast.success(t('copied'))
  } else {
    toast.error(t('copyFailed'))
  }
}

// —— 代码块按钮（复制 / html 预览）——
// 按钮在 v-html 里（DOMPurify 放行 button），无法直接绑 Vue 事件，
// 故在消息区做事件委托。
function onMsgClick(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  if (!target) return

  // 流程图工具栏：看源码 / 折叠。状态挂在容器属性上（见 utils/mermaidMarkdown.ts），
  // 这里只做事件委托，真正的翻转逻辑与按钮文案都在 utils 里。
  if (handleMermaidToolbarClick(target, mermaidLabels())) return
  // 表格表头点击排序（v-html 真实 DOM，原生事件委托）
  const th = target.closest?.('.md-th-sortable') as HTMLElement | null
  if (th) { sortTableByColumn(th); return }

  // html 代码块的「预览」：取源码塞进沙箱 iframe（见模板里的 ai-html-preview）
  const runBtn = target.closest?.('[data-md-run]') as HTMLElement | null
  if (runBtn) {
    const code = runBtn.closest('.md-code')?.querySelector('code')?.textContent ?? ''
    if (code.trim()) {
      // 没有 <!doctype> / <html> 的片段也当成整页渲染：浏览器本来就会补全
      htmlPreview.value = code
    }
    return
  }

  const btn = target.closest?.('[data-md-copy]') as HTMLElement | null
  if (!btn) return
  const wrap = btn.closest?.('.md-code') as HTMLElement | null
  const code = wrap?.querySelector('code')?.textContent ?? ''
  if (!code) return
  copy(code)
  // 按钮短暂显示「已复制」反馈
  btn.classList.add('copied')
  btn.setAttribute('aria-label', t('copied'))
  window.setTimeout(() => {
    btn.classList.remove('copied')
    btn.setAttribute('aria-label', t('copyCode'))
  }, 1200)
}

// —— HTML 预览（沙箱 iframe，全屏遮罩）——
// sandbox 里**绝对不能**带 allow-same-origin：那样 iframe 与宿主同源，
// 里面的脚本就能拿到 QuickDock 的 window 与 Wails 运行时。允许 scripts /
// modals（示例页常用 alert）/ forms 就够了，opaque origin 下它连相对路径都取不到。
const htmlPreview = ref('')

function closeHtmlPreview() {
  htmlPreview.value = ''
}

// —— 右键：把流程图 / 公式复制成图片 ——
// 原生右键菜单只能「复制」成文本（复制到的是 LaTeX 源码或 SVG 标签），
// 而这类内容的去向通常是文档或聊天框——那里需要一张图。
interface CtxTarget {
  el: HTMLElement
  kind: 'mermaid' | 'math' | 'code'
}
const ctxTarget = ref<CtxTarget | null>(null)
const ctxMenu = useFloatMenu({ placement: 'right-start', offset: 4 })

function onMsgContextMenu(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  if (!target) return
  // 只认**已经画出来**的图和公式：未渲染的 mermaid 只是一段源码，走原生菜单更合适
  const mmd = target.closest('.md-mermaid[data-mermaid-done="1"]') as HTMLElement | null
  const math = mmd ? null : (target.closest('.md-math') as HTMLElement | null)
  // 普通代码块（.md-code）：右键也能「复制为图片」，与图/公式体验统一
  const code = mmd || math ? null : (target.closest('.md-code') as HTMLElement | null)
  const el = mmd || math || code
  if (!el) return
  // 折叠状态的图尺寸是 0（内容 display:none），转图会得到空图 —— 先展开再弹菜单。
  // 右键本身就意味着「我要对这张图做点什么」，展开是符合预期的，也不留后遗症。
  if (mmd?.hasAttribute('data-mermaid-folded')) setMermaidFolded(mmd, false, mermaidLabels())
  // 源码视图下 SVG 也是 display:none，「复制为图片」会截到代码块而不是图。
  // 与折叠态同样处理：右键即代表要对图操作，自动切到图形视图。
  if (mmd?.hasAttribute('data-mermaid-src')) setMermaidSourceMode(mmd, false, mermaidLabels())
  e.preventDefault()
  ctxTarget.value = { el, kind: mmd ? 'mermaid' : math ? 'math' : 'code' }
  ctxMenu.showAt(e.clientX, e.clientY)
}

function closeCtxMenu() {
  ctxMenu.hide()
  ctxTarget.value = null
}

async function ctxCopyImage() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  try {
    const ok = await copyElementAsImage(t0.el)
    if (ok) toast.success(t('aiCopyImageOk'))
    else toast.error(t('aiCopyImageFail'))
  } catch {
    toast.error(t('aiCopyImageFail'))
  }
}

function ctxCopySource() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  let code = ''
  if (t0.kind === 'math') {
    // 公式源码存在渲染外层 span 的 data-tex（见 utils/mathMarkdown.ts renderMath）
    code = t0.el.dataset.tex || ''
  } else {
    // 渲染成功后 DOM 里只剩 SVG，源码在 data-mermaid-code 上（见 utils/mermaidMarkdown.ts）
    code = t0.el.dataset.mermaidCode || t0.el.querySelector('.md-mermaid-src code')?.textContent || ''
  }
  if (code) copy(code)
}

async function ctxDownloadSvg() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  const xml = serializeMermaidSvg(t0.el)
  if (!xml) { toast.error(t('aiDownloadFail')); return }
  downloadBlob(new Blob([xml], { type: 'image/svg+xml;charset=utf-8' }), 'diagram.svg')
  toast.success(t('aiDownloadOk', { name: 'diagram.svg' }))
}

async function ctxDownloadPng() {
  const t0 = ctxTarget.value
  closeCtxMenu()
  if (!t0) return
  const svg = t0.el.querySelector('.md-mermaid-svg svg') as SVGSVGElement | null
  if (!svg) { toast.error(t('aiDownloadFail')); return }
  const blob = await svgToPngBlob(svg, resolveBackground(t0.el))
  if (blob) {
    downloadBlob(blob, 'diagram.png')
    toast.success(t('aiDownloadOk', { name: 'diagram.png' }))
  } else {
    toast.error(t('aiDownloadFail'))
  }
}

// —— 划词（选择文本）复制 ——
const selPop = reactive({ text: '' })
// floating-ui：跟随选区矩形，选区上方显示，放不下自动翻到下方，靠边自动内收
const selCtx = useFloatMenu({ placement: 'top', offset: 8 })

function hideSelPop() {
  selCtx.hide()
  selPop.text = ''
}

function checkSelection() {
  const sel = window.getSelection()
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) { hideSelPop(); return }
  const text = sel.toString().trim()
  if (!text) { hideSelPop(); return }
  const range = sel.getRangeAt(0)
  const container = msgArea.value
  if (!container || !container.contains(range.commonAncestorContainer)) { hideSelPop(); return }
  selPop.text = text
  selCtx.showRect(range.getBoundingClientRect())
}

function onMsgMouseUp() {
  // 延迟到选择完成后再判定
  setTimeout(checkSelection, 0)
}

async function copySelection() {
  const text = selPop.text
  hideSelPop()
  const sel = window.getSelection()
  if (sel) sel.removeAllRanges()
  await copy(text)
}

// 笔记 ↔ AI：AI 回复转存为笔记树里的一篇文档（与剪贴板「存为笔记」一致）。
// 之前写的是「快捷笔记」（独立窗口那一份），主界面看不到，所以表现为「转存没生效」。
function noteTitleOf(text: string, max = 30): string {
  const line = (text || '').split('\n').map(s => s.trim()).find(Boolean) || ''
  return line.length > max ? line.slice(0, max) + '…' : (line || t('aiSaveToNote'))
}

async function saveAsNote(content: string) {
  if (!content || !content.trim()) return
  try {
    unwrap(await CreateNoteDoc('', noteTitleOf(content), content.trim(), 'markdown'))
    toast.success(t('aiSavedToNote'))
  } catch (e: any) {
    toast.error(getErrorMessage(e))
  }
}

function onDocMouseDown(e: MouseEvent) {
  // 右键菜单自己吞掉 mousedown（@mousedown.stop），落到这里的点击都在菜单之外
  if (ctxTarget.value) closeCtxMenu()
  const pop = document.querySelector('.ai-sel-copy')
  if (pop && pop.contains(e.target as Node)) return
  hideSelPop()
}

// 图片全屏查看器：缩略图点击放大。放组件级状态而非每条消息各自持有，
// 同一时刻最多预览一张图；点遮罩 / Esc 关闭，点图不关（防误触）。
const imgViewer = ref<AIMsgImage | null>(null)
function openImgViewer(img: AIMsgImage) {
  imgViewer.value = img
}
function closeImgViewer() {
  imgViewer.value = null
}
function onGlobalKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (imgViewer.value) closeImgViewer()
  else if (htmlPreview.value) closeHtmlPreview()
  else if (ctxTarget.value) closeCtxMenu()
}

onMounted(async () => {
  // KeepAlive 首次挂载也会走 onActivated，但那条路依赖 App.vue 的包裹结构；
  // 这里显式再置一次，保证「一进来就停在 AI 页」这个事实后端一定知道。
  reportAIPageVisible(true)
  await loadProfiles()
  await loadConversations()

  // 仅保留标题重生成的单条事件；AI 问答流式已改为本地 HTTP 流式（fetch 读取）
  Events.On('ai:conv', (ev: any) => {
    const data = ev?.data
    if (!data) return
    if (data.id && data.id !== activeId.value) activeId.value = data.id
    if (data.title !== undefined) {
      const idx = conversations.value.findIndex((c) => c.id === data.id)
      if (idx >= 0) conversations.value[idx].title = data.title
      else loadConversations()
    }
  })
  // 设置在 SettingsModal 中保存配置后刷新下拉列表
  Events.On('ai:profiles-updated', loadProfiles)
  // 点击消息区之外时隐藏划词复制浮层
  document.addEventListener('mousedown', onDocMouseDown, true)
  // 图片查看器的 Esc 关闭（capture 不必要，普通冒泡即可；避免干扰输入框内 Esc 行为的判断）
  document.addEventListener('keydown', onGlobalKeydown)

  // ResizeObserver 观察消息内容容器：内容变高（流式输出、图片加载、代码块撑开）
  // 时把视口拉到底部。这是「AI 一直在回复但视口不跟随」的正确修法——
  // 滚动应由 DOM 实际高度变化驱动，而不是由某个响应式变量间接推断。
  if (typeof ResizeObserver !== 'undefined') {
    resizeObs = new ResizeObserver(() => {
      if (!stickToBottom.value) return
      const el = msgArea.value
      if (!el) return
      el.scrollTop = el.scrollHeight
    })
    if (msgContent.value) resizeObs.observe(msgContent.value)
  }
})

onUnmounted(() => {
  Events.Off('ai:conv')
  Events.Off('ai:profiles-updated')
  document.removeEventListener('mousedown', onDocMouseDown, true)
  document.removeEventListener('keydown', onGlobalKeydown)
  // 这里的 abort 现在只会在「应用退出」时跑到：切到别的导航页是 deactivate，
  // 组件还活着，流照常继续（见下方 onActivated / onDeactivated）。
  if (streamCtrl) streamCtrl.abort()
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  if (resizeObs) { resizeObs.disconnect(); resizeObs = null }
  aiGenerating.value = false
  aiFinishedUnread.value = false
  reportAIPageVisible(false)
})
</script>

<template>
  <div class="ai-page">
    <!-- 左侧会话列表 -->
    <aside class="ai-sidebar">
      <button class="ai-new-btn" @click="newConv">
        <Plus :size="14" /> {{ t('aiNewChat') }}
      </button>
      <div v-if="conversations.length > 5" class="ai-conv-search">
        <Search :size="12" class="ai-conv-search-icon" />
        <input v-model="convQuery" class="ai-conv-search-input" :placeholder="t('aiSearchConvPh')" />
        <button v-if="convQuery" class="ai-conv-search-clear" @click="convQuery = ''"><X :size="11" /></button>
      </div>
      <div class="ai-conv-list">
        <div
          v-for="c in shownConversations"
          :key="c.id"
          :class="['ai-conv-item', { active: c.id === activeId }]"
          @click="selectConv(c.id)"
        >
          <input
            v-if="renamingId === c.id"
            ref="renameInput"
            v-model="renameText"
            class="ai-conv-rename"
            spellcheck="false"
            maxlength="60"
            @click.stop
            @keydown.enter.prevent="commitRename(c)"
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
          <!-- 会话累计用量：只统计「问答」，标题生成 / 摘要压缩不计入（见 ai_usage.go），
               所以这里的数字应该等于该会话各条回复下方统计之和。
               折算成 k/M 显示，精确值放 title。 -->
          <span v-if="renamingId !== c.id && (c.prompt_tokens > 0 || c.completion_tokens > 0)" class="ai-conv-tokens" :title="t('aiConvTokenTip', { in: c.prompt_tokens, out: c.completion_tokens })">
            {{ fmtTokens(c.prompt_tokens + c.completion_tokens) }}
          </span>
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
        <div v-if="conversations.length === 0" class="ai-conv-empty">{{ t('aiNoConversations') }}</div>
        <div v-else-if="filteredConversations.length === 0" class="ai-conv-empty">{{ t('aiNoMatchConv') }}</div>
        <button
          v-else-if="filteredConversations.length > shownConversations.length"
          class="ai-conv-more"
          @click="convLimit += CONV_PAGE"
        >{{ t('aiShowMoreConv', { n: filteredConversations.length - shownConversations.length }) }}</button>
      </div>
    </aside>

    <!-- 右侧聊天区 -->
    <section class="ai-main">
      <!-- 模式切换 + 模型选择 + 操作 -->
      <div class="ai-toolbar">
        <div class="ai-modes">
          <button
            v-for="m in modes"
            :key="m.key"
            :class="['ai-mode-tab', { active: mode === m.key }]"
            @click="mode = m.key"
          >{{ t(m.label) }}</button>
        </div>
        <div class="ai-toolbar-right">
          <select v-model="aiActive" class="ai-model-select" :title="t('aiSwitchModel')" @change="onModelChange">
            <option v-for="p in aiProfiles" :key="p.id" :value="p.id">{{ p.name || p.model }}</option>
          </select>
          <button v-if="activeId" class="ai-icon-btn" :title="t('aiRegenTitle')" @click="regenTitle">
            <RefreshCw :size="13" />
          </button>
          <button v-if="activeId" class="ai-icon-btn" :title="t('aiClearContext')" @click="clearContext">
            <Eraser :size="13" />
          </button>
          <button v-if="streaming" class="ai-stop" @click="stop">
            <Square :size="13" /> {{ t('aiStop') }}
          </button>
        </div>
      </div>

      <!-- 消息区 -->
      <div ref="msgArea" class="ai-messages" @scroll="onMsgScroll" @mouseup="onMsgMouseUp" @keyup="onMsgMouseUp" @click="onMsgClick" @contextmenu="onMsgContextMenu">
        <div ref="msgContent" class="ai-msg-content-wrap">
          <div v-if="!hasKey" class="ai-need-key">
            <Bot :size="40" />
            <p>{{ t('aiNeedKey') }}</p>
            <button class="ai-go-settings" @click="emit('open-settings', 'ai')">{{ t('aiGoSettings') }}</button>
          </div>

          <template v-else>
            <!-- v-memo：历史消息在流式期间内容不变，命中缓存即可整棵子树跳过重建。
                 依赖数组必须覆盖子树内所有响应式来源，缺一个就会出现「数据变了但界面不变」：
                   · m.content / m.reasoning_content —— 正文与思考过程
                   · statMap.get(m.id)               —— 该条回复的耗时/token 统计
                   · locale                         —— 按钮 title 等文案随语言切换刷新
                 编译器生成的代码含 `_cached.key === m.id` 校验，
                 切换会话（消息 id 变化）不会误命中旧缓存。 -->
            <div
              v-for="m in messages"
              :key="m.id"
              v-memo="[m.content, m.reasoning_content, statMap.get(m.id), locale, (m.imgList?.length ?? 0)]"
              :class="['ai-msg', m.role]"
            >
              <div class="ai-msg-role">
                {{ m.role === 'user' ? t('aiYou') : 'AI' }}
                <!-- 多档案（不同模型/档案）混着聊时，不写模型名就分不清这句是谁答的 -->
                <span v-if="m.role === 'assistant' && m.model" class="ai-msg-model">{{ m.model }}</span>
              </div>
              <!-- 图片缩略图：url 类直发链接，file 类用已加载的 dataUrl；历史 file 图由 loadMessages 懒加载填回 -->
              <div v-if="m.imgList && m.imgList.length" class="ai-msg-imgs">
                <div v-for="(img, bi) in m.imgList" :key="bi" class="ai-msg-img">
                  <img v-if="img.url" :src="img.url" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="openImgViewer(img)" />
                  <img v-else-if="img.dataUrl" :src="img.dataUrl" :alt="img.name" class="ai-msg-img-el is-clickable" loading="lazy" @click.stop="openImgViewer(img)" />
                  <div v-else class="ai-msg-img-el ai-img-broken">{{ t('aiImageBroken') }}</div>
                </div>
              </div>
              <details v-if="m.reasoning_content" class="ai-reasoning">
                <summary>{{ t('aiThought') }} · {{ t('aiThoughtChars', { n: m.reasoning_content.length }) }}</summary>
                <div class="ai-reasoning-content">{{ m.reasoning_content }}</div>
              </details>
              <div v-if="m.role === 'assistant'" class="ai-msg-content ai-md">
                <div v-for="(b, bi) in renderMessageBlocks(m)" :key="bi" class="ai-md-block" v-html="b"></div>
              </div>
              <div v-else class="ai-msg-content ai-plain">{{ m.content }}</div>
              <!-- 操作按钮同一行：.ai-msg 是纵向 flex，不包一层的话每个按钮都会各占一行 -->
              <div class="ai-msg-actions">
                <!-- 中断标记：内容照样保留（后端也把半截内容连同 stopped 一起落库），
                     但要让人一眼看出「这是我按了停止」，而不是模型只答了这么多 -->
                <span v-if="m.stopped" class="ai-msg-stopped" :title="t('aiStoppedHint')">{{ t('aiStopped') }}</span>
                <button v-if="m.content" class="ai-copy" @click="copy(m.content)" :title="t('copy')">
                  <Copy :size="12" /> {{ t('copy') }}
                </button>
                <button v-if="m.role === 'assistant' && m.content" class="ai-copy" @click="saveAsNote(m.content)" :title="t('aiSaveToNote')">
                  <StickyNote :size="12" /> {{ t('aiSaveToNote') }}
                </button>
                <!-- 单条回复统计：耗时 / 首字延迟 / token 用量 -->
                <span v-if="msgStat(m)" class="ai-msg-stat">
                  {{ formatStat(msgStat(m)!) }}
                </span>
              </div>
            </div>

            <div v-if="reasoningText" class="ai-reasoning">
              <details open>
                <summary>{{ t('aiThinking') }}</summary>
                <div class="ai-reasoning-content">{{ reasoningText }}</div>
              </details>
            </div>
            <div v-if="streamingText" class="ai-msg assistant streaming">
              <div class="ai-msg-role">AI</div>
              <div class="ai-msg-content ai-md">
                <!-- key 用「块原文 + 序号」：原文可能重复（两段相同的话），
                     仅用原文会触发 Vue 的重复 key 警告。仅末尾块会变，
                     前面的块 key 稳定 → 不会被重建。 -->
                <div v-for="(b, bi) in streamBlocks" :key="bi + ':' + b.raw.length + ':' + b.raw.slice(0, 24)" class="ai-md-block" v-html="b.html"></div>
              </div>
              <span class="ai-cursor">▋</span>
            </div>

            <div v-if="errorMsg" class="ai-error">
              <span class="ai-error-text">{{ errorMsg }}</span>
              <!-- 失败时用户消息已落库，重试就是把它再发一次（图片一起带回去） -->
              <button v-if="canRetry()" class="ai-retry" @click="retryLast">{{ t('aiRetry') }}</button>
            </div>

            <div v-if="messages.length === 0 && !streamingText && !errorMsg" class="ai-empty">
              <Bot :size="40" />
              <p>{{ t('aiEmptyHint') }}</p>
            </div>
          </template>
        </div>
      </div>

      <!-- 「回到底部」按钮：用户主动上滑查看历史时出现 -->
      <button v-if="showJumpBtn" class="ai-jump-bottom" @click="jumpToBottom" :title="t('aiBackToBottom')">
        <ArrowDown :size="14" />
      </button>

      <!-- 划词复制浮层（Teleport 到 body，floating-ui 负责定位与防溢出） -->
      <Teleport to="body">
        <button
          v-if="selCtx.visible.value"
          class="ai-sel-copy"
          :ref="el => (selCtx.floatingRef.value = el as HTMLElement | null)"
          :style="selCtx.floatingStyles.value"
          @click="copySelection"
          @mousedown.stop
        >
          <Copy :size="12" /> {{ t('copySelection') }}
        </button>
      </Teleport>

      <!-- 流程图 / 公式的右键菜单：原生菜单只能复制成文本，这里要的是「一张图」 -->
      <Teleport to="body">
        <div
          v-if="ctxMenu.visible.value"
          class="ai-ctx"
          :ref="el => (ctxMenu.floatingRef.value = el as HTMLElement | null)"
          :style="ctxMenu.floatingStyles.value"
          @mousedown.stop
          @contextmenu.prevent
        >
          <button class="ai-ctx-item" @click="ctxCopyImage">
            <ImageIcon :size="13" /> {{ t('aiCopyImage') }}
          </button>
          <button v-if="ctxTarget?.kind === 'mermaid' || ctxTarget?.kind === 'math'" class="ai-ctx-item" @click="ctxCopySource">
            <Copy :size="13" /> {{ t('aiCopySource') }}
          </button>
          <template v-if="ctxTarget?.kind === 'mermaid'">
            <button class="ai-ctx-item" @click="ctxDownloadPng">
              <Download :size="13" /> {{ t('aiDownloadPng') }}
            </button>
            <button class="ai-ctx-item" @click="ctxDownloadSvg">
              <Download :size="13" /> {{ t('aiDownloadSvg') }}
            </button>
          </template>
        </div>
      </Teleport>

      <!-- html 代码块预览：sandbox 无 allow-same-origin，脚本跑在自己的不透明源里 -->
      <Teleport to="body">
        <div v-if="htmlPreview" class="ai-html-preview" @click.self="closeHtmlPreview">
          <div class="ai-html-preview-box">
            <div class="ai-html-preview-bar">
              <span class="ai-html-preview-title">{{ t('aiPreviewTitle') }}</span>
              <button class="qd-btn qd-btn-sm" @click="copy(htmlPreview)">
                <Copy :size="12" /> {{ t('copyCode') }}
              </button>
              <button class="qd-icon-btn" :title="t('close')" @click="closeHtmlPreview">
                <X :size="14" />
              </button>
            </div>
            <iframe
              class="ai-html-preview-frame"
              :srcdoc="htmlPreview"
              sandbox="allow-scripts allow-modals allow-forms allow-popups"
              referrerpolicy="no-referrer"
              title="preview"
            ></iframe>
          </div>
        </div>
      </Teleport>

      <!-- 输入区 -->
      <div class="ai-input-area" @dragover.prevent @drop="onInputDrop">
        <!-- 待发送图片预览 -->
        <div v-if="attachments.length" class="ai-attach-preview">
          <div v-for="(a, i) in attachments" :key="i" class="ai-attach-thumb">
            <img v-if="a.kind === 'file' && a.dataUrl" :src="a.dataUrl" class="ai-attach-img" />
            <img v-else-if="a.kind === 'url'" :src="a.url" class="ai-attach-img" />
            <button class="ai-attach-del" :title="t('delete')" @click="removeAttachment(i)">×</button>
          </div>
        </div>
        <!-- 图片链接输入框（点击「图片链接」后展开） -->
        <div v-if="urlInputOpen" class="ai-url-row">
          <input
            v-model="urlDraft"
            type="text"
            class="ai-url-input"
            :placeholder="t('aiAddImageUrlPh')"
            @keydown.enter.prevent="confirmUrlImage"
          />
          <button class="ai-url-ok" @click="confirmUrlImage">{{ t('add') }}</button>
        </div>
        <div class="ai-input-row">
          <textarea
            v-model="input"
            class="ai-input"
            :placeholder="t('aiPlaceholder')"
            :disabled="!hasKey || streaming"
            @keydown.enter.exact.prevent="send"
            @paste="onInputPaste"
          />
          <div class="ai-input-tools">
            <button v-if="allowFileImage" class="ai-tool-btn" :title="t('aiAttachImage')" @click="openFilePicker">
              <ImageIcon :size="16" />
            </button>
            <button v-if="allowUrlImage" class="ai-tool-btn" :title="t('aiAddImageUrl')" @click="toggleUrlInput">
              <Link2 :size="16" />
            </button>
            <button class="ai-send" :disabled="!hasKey || streaming || (!input.trim() && !attachments.length)" @click="send">
              {{ streaming ? t('aiThinking') : t('send') }}
            </button>
          </div>
        </div>
        <input ref="fileInputEl" type="file" accept="image/*" multiple hidden @change="onPickFiles" />
      </div>
    </section>

    <!-- 图片全屏查看器：点遮罩关闭，点图不关（防误触），Esc 同样关闭 -->
    <Teleport to="body">
      <div v-if="imgViewer" class="ai-img-viewer" @click="closeImgViewer">
        <img :src="imgViewer.dataUrl || imgViewer.url || ''" :alt="imgViewer.name" @click.stop />
      </div>
    </Teleport>
  </div>
</template>

<style scoped>
.ai-page {
  flex: 1;
  display: flex;
  min-width: 0;
  height: 100%;
  overflow: hidden;
}

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
.ai-conv-del {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: var(--radius-sm);
}
.ai-conv-item:hover .ai-conv-del { display: flex; }
.ai-conv-del:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.ai-conv-ren {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: none; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: var(--radius-sm);
}
.ai-conv-item:hover .ai-conv-ren { display: flex; }
.ai-conv-ren:hover { color: var(--color-text-primary); background: var(--color-bg-tertiary); }
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

/* 主区 */
.ai-main { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; position: relative; }
.ai-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 16px;
  border-bottom: 1px solid var(--color-border);
  flex-shrink: 0;
  gap: 10px;
}
.ai-modes { display: flex; gap: 6px; flex-wrap: wrap; }
.ai-mode-tab {
  padding: 5px 12px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text-muted);
  border-radius: var(--radius-lg);
  font-size: 12px;
  cursor: pointer;
  font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-mode-tab:hover { color: var(--color-text-primary); }
.ai-mode-tab.active { border-color: var(--color-accent); color: var(--color-accent); background: var(--color-accent-bg); }
.ai-toolbar-right { display: flex; align-items: center; gap: 6px; flex-shrink: 0; }
.ai-model-select {
  max-width: 160px;
  padding: 5px 8px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 12px;
  font-family: inherit;
  outline: none;
}
.ai-model-select:focus { border-color: var(--color-accent); }
.ai-icon-btn {
  display: flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border: 1px solid var(--color-border);
  background: transparent; color: var(--color-text-muted);
  border-radius: var(--radius-md); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.ai-icon-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-stop {
  display: flex; align-items: center; gap: 4px;
  padding: 5px 12px; border: none; border-radius: var(--radius-sm);
  background: var(--color-bg-active); color: var(--color-text-secondary);
  font-size: 12px; cursor: pointer; font-family: inherit;
}
.ai-stop:hover { color: var(--color-danger); }

/* 思考过程折叠块 */
.ai-reasoning {
  font-size: 12px; margin: 2px 0; max-width: 80%;
}
.ai-reasoning details { background: var(--color-bg-active); border-radius: var(--radius-sm); padding: 4px 8px; }
.ai-reasoning summary {
  cursor: pointer; color: var(--color-text-muted); font-size: 11px;
  user-select: none; outline: none;
}
.ai-reasoning summary:hover { color: var(--color-text-primary); }
.ai-reasoning-content {
  margin-top: 4px; padding-top: 4px; border-top: 1px solid var(--color-border);
  color: var(--color-text-secondary); font-size: 12px; line-height: 1.5;
  white-space: pre-wrap; word-break: break-word;
}

/* 消息区：滚动容器 + 内容层（内容层被 ResizeObserver 观察） */
.ai-messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px 20px;
  display: flex;
  flex-direction: column;
  overflow-anchor: none; /* 关掉浏览器自动锚定，避免与我们的贴底逻辑打架 */
}
/* 内容层的高度必须跟随内容增长，ResizeObserver 才能感知到「AI 又吐了一段」。
   原来这里写的是 flex:1 + min-height:0 —— flex-basis:0% 让盒高被 flex 固定成
   容器高度，内容溢出并不会改变 border-box 尺寸，观察器因此永远不触发，
   这就是「AI 一直在回复但视口不往下走」的直接原因。
   改为 flex:0 0 auto（高度由内容决定）+ min-height:100%（内容少时仍撑满，
   空状态才能垂直居中）。 */
.ai-msg-content-wrap {
  display: flex;
  flex-direction: column;
  gap: 14px;
  flex: 0 0 auto;
  min-height: 100%;
}
.ai-msg { display: flex; flex-direction: column; gap: 4px; max-width: 88%; }
.ai-msg.user { align-self: flex-end; align-items: flex-end; }
.ai-msg.assistant { align-self: flex-start; }
.ai-msg-role { font-size: 11px; color: var(--color-text-disabled); margin-bottom: 1px; display: flex; align-items: center; gap: 6px; }
/* 模型名淡到几乎只作注脚，但比"猜这句是谁答的"强 */
.ai-msg-model {
  font-size: 10px; color: var(--color-text-disabled);
  background: var(--color-bg-tertiary); padding: 0 5px;
  border-radius: var(--radius-xs); opacity: 0.85;
}

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

/* AI 气泡去掉底色：原来整块套 --color-bg-tertiary 灰底，
   里面再套深色代码块、外面再套引用块，三层嵌套导致视觉很乱。
   现在 AI 回复走「纯净排版」，只有代码块/引用块自身带底色，靠留白分层。 */
.ai-msg-content {
  padding: 0;
  font-size: 13px;
  line-height: 1.65;
  word-break: break-word;
  color: var(--color-text-primary);
  user-select: text;
  -webkit-user-select: text;
  cursor: text;
  min-width: 0;
}
/* 用户消息仍保留气泡，但改为描边风格，与 AI 的无底色形成层级区分 */
.ai-msg.user .ai-msg-content {
  background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border);
  color: var(--color-text-primary);
  padding: 8px 12px;
  border-radius: var(--radius-md);
  white-space: pre-wrap;
}
.ai-msg-actions {
  display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap;
}
.ai-msg.user .ai-msg-actions { justify-content: flex-end; }
.ai-copy {
  display: flex; align-items: center; gap: 3px;
  background: none; border: none; color: var(--color-text-disabled);
  font-size: 11px; cursor: pointer; padding: 2px 4px; border-radius: var(--radius-xs);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-copy:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.ai-msg.user .ai-copy { color: var(--color-text-muted); }
.ai-msg.user .ai-copy:hover { color: var(--color-accent); background: var(--color-accent-bg); }
/* 中断标记：贴在操作行最左侧，用警示色区别于普通小字（stat 行） */
.ai-msg-stopped {
  font-size: 10px; padding: 1px 6px; border-radius: var(--radius-xs);
  color: var(--color-warning);
  background: var(--color-warning-bg);
  border: 1px solid var(--color-warning-border);
  cursor: default;
}
/* 单条回复统计：淡色小字，鼠标悬停可看完整信息 */
.ai-msg-stat {
  font-size: 10px;
  color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
  /* 允许换行：这行可能同时有「耗时 / 首字 / 输入输出 / 缓存命中」四段，
     窄窗口下 nowrap 会把后面的内容顶出可视区（数字被裁比换行更难接受） */
  white-space: normal;
  line-height: 1.6;
  cursor: default;
}

/* 划词复制浮层（position/left/top 由 floating-ui 注入） */
.ai-sel-copy {
  z-index: 50;
  display: flex; align-items: center; gap: 4px;
  padding: 5px 10px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 12px; cursor: pointer; font-family: inherit;
  box-shadow: 0 4px 16px var(--color-bg-overlay);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-sel-copy:hover { color: var(--color-accent); border-color: var(--color-accent); }
.ai-cursor { animation: ai-blink 1s steps(1) infinite; color: var(--color-accent); }
@keyframes ai-blink { 50% { opacity: 0; } }

/* 图 / 公式的右键菜单（position 由 floating-ui 注入） */
.ai-ctx {
  z-index: 700;
  display: flex; flex-direction: column;
  min-width: 132px; padding: 4px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  box-shadow: var(--shadow-md);
}
.ai-ctx-item {
  display: flex; align-items: center; gap: 7px;
  height: 28px; padding: 0 9px;
  border: none; border-radius: var(--radius-sm);
  background: transparent; color: var(--color-text-primary);
  font-family: inherit; font-size: 12px; text-align: left; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-ctx-item:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-ctx-item svg { flex: 0 0 auto; }

/* html 代码块预览：铺满窗口的遮罩 + 白色画布（示例页大多假设白底，
   跟着应用主题变暗会让「我的网页」看起来坏了） */
.ai-html-preview {
  position: fixed; inset: 0; z-index: 800;
  display: flex; align-items: center; justify-content: center;
  padding: 24px;
  background: var(--color-bg-overlay);
}
.ai-html-preview-box {
  display: flex; flex-direction: column;
  width: 100%; height: 100%;
  max-width: 1080px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  background: var(--color-bg-primary);
  box-shadow: var(--shadow-md);
  overflow: hidden;
}
.ai-html-preview-bar {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 8px 6px 14px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-bg-secondary);
}
.ai-html-preview-title {
  margin-right: auto;
  font-size: 11px; letter-spacing: 0.04em; text-transform: uppercase;
  color: var(--color-text-muted);
}
.ai-html-preview-frame {
  flex: 1; width: 100%; min-height: 0;
  border: none; background: #fff;
}
.ai-error {
  align-self: flex-start;
  padding: 10px 14px; border-radius: var(--radius-lg);
  background: var(--color-danger-bg); color: var(--color-danger);
  font-size: 13px; max-width: 80%;
  /* 错误文案 + 重试按钮同一行；长错误换行，按钮不跟着被挤压 */
  display: flex; align-items: center; gap: 10px; flex-wrap: wrap;
}
.ai-error-text { flex: 1 1 auto; min-width: 0; word-break: break-word; }
.ai-retry {
  flex: 0 0 auto;
  padding: 3px 12px; border-radius: var(--radius-sm);
  border: 1px solid var(--color-danger-border);
  background: transparent; color: var(--color-danger);
  font-size: 12px; font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast);
}
.ai-retry:hover { background: var(--color-danger-bg); }
.ai-need-key, .ai-empty {
  margin: auto;
  display: flex; flex-direction: column; align-items: center; gap: 12px;
  color: var(--color-text-disabled); text-align: center;
}
.ai-need-key p, .ai-empty p { font-size: 13px; margin: 0; }
.ai-go-settings {
  padding: 7px 16px; border: none; border-radius: var(--radius-md);
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 13px; cursor: pointer; font-family: inherit;
}

/* 「回到底部」浮动按钮 */
.ai-jump-bottom {
  position: absolute;
  bottom: 96px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 20;
  display: flex; align-items: center; justify-content: center;
  width: 32px; height: 32px;
  border-radius: var(--radius-full);
  border: 1px solid var(--color-border);
  background: var(--color-surface-elevated);
  color: var(--color-text-secondary);
  cursor: pointer;
  box-shadow: var(--shadow-md);
  transition: background-color var(--transition-fast), color var(--transition-fast),
    border-color var(--transition-fast), opacity var(--transition-fast);
}
.ai-jump-bottom:hover { color: var(--color-accent); border-color: var(--color-accent); }

/* Markdown 内容——紧凑排版 */
/* 分块渲染容器：display:contents 让块内元素不生成盒子、直接参与父级布局，
   因此「分块渲染」与「整段渲染」的排版结果完全一致（无额外间距/缩进差异）。 */
.ai-md-block { display: contents; }
.ai-md :deep(h1), .ai-md :deep(h2), .ai-md :deep(h3), .ai-md :deep(h4) {
  font-size: 14px; margin: 10px 0 4px; font-weight: 600; line-height: 1.4;
}
.ai-md :deep(h1:first-child), .ai-md :deep(h2:first-child), .ai-md :deep(h3:first-child) { margin-top: 0; }
.ai-md :deep(p) { margin: 5px 0; }
.ai-md :deep(ul), .ai-md :deep(ol) { padding-left: 20px; margin: 5px 0; }
.ai-md :deep(li) { margin: 2px 0; line-height: 1.6; }
.ai-md :deep(li > p) { margin: 0; }
/* 换行：marked 开了 breaks:true，段内单换行会渲染成 <br>。
   这里必须让 <br> 生效，否则 AI 回复里的多行要点会被挤成一行
   （实测 marked 输出 <p>第一行<br>第二行</p>，而 display:none 会把 <br> 抹掉）。 */
.ai-md :deep(br) { display: inline; }
.ai-md :deep(a) { color: var(--color-accent); text-decoration: none; }
.ai-md :deep(a:hover) { text-decoration: underline; }
/* 引用块：左侧竖线 + 淡底，不再嵌套成整块灰盒 */
.ai-md :deep(blockquote) {
  border-left: 3px solid var(--color-accent-muted);
  padding: 6px 12px;
  margin: 8px 0;
  background: var(--color-bg-secondary);
  border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  color: var(--color-text-secondary);
}
.ai-md :deep(blockquote p) { margin: 0; }

/* 代码块：独立卡片 + 顶部工具条（语言标签 + 复制按钮） */
.ai-md :deep(.md-code) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  overflow: hidden;
  background: var(--color-code-bg);
}
.ai-md :deep(.md-code-bar) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px 4px 10px;
  background: var(--color-code-bar-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: 11px;
  color: var(--color-text-muted);
}
.ai-md :deep(.md-code-lang) {
  text-transform: lowercase;
  letter-spacing: 0.02em;
}
.ai-md :deep(.md-code-copy) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-md :deep(.md-code-actions) { display: flex; align-items: center; gap: 2px; }
/* html 代码块的「预览」按钮与复制按钮同构，只是悬停时给成主色 ——
   它是这一行里唯一会「做事」的按钮，值得比复制更显眼一点 */
.ai-md :deep(.md-code-run) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-md :deep(.md-code-run:hover) { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-md :deep(.md-code-run svg) { width: 13px; height: 13px; }
.ai-md :deep(.md-code-copy:hover) { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-md :deep(.md-code-copy.copied) { color: var(--color-success); }
.ai-md :deep(.md-code-copy svg) { width: 13px; height: 13px; }
.ai-md :deep(pre) {
  margin: 0;
  padding: 10px 12px;
  overflow-x: auto;
  background: transparent;
  font-size: 12px;
  line-height: 1.6;
}
.ai-md :deep(code) {
  font-family: var(--font-mono, monospace); font-size: 12px;
  background: var(--color-code-inline-bg); padding: 1px 5px; border-radius: var(--radius-xs);
}
.ai-md :deep(pre code) { background: none; padding: 0; font-size: 12px; }
.ai-md :deep(table) { border-collapse: collapse; }
.ai-md :deep(th), .ai-md :deep(td) { border: 1px solid var(--color-border); padding: 5px 9px; }
.ai-md :deep(th) { background: var(--color-bg-secondary); font-weight: 600; }
/* 表格增强：横向滚动容器 + 导出条 + 可排序列（见 enhanceTables） */
.ai-md :deep(.md-table-wrap) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  overflow-x: auto;
}
.ai-md :deep(.md-table-bar) {
  display: flex; justify-content: flex-end;
  padding: 3px 6px;
  border: 1px solid var(--color-border);
  border-bottom: none;
  border-radius: var(--radius-md) var(--radius-md) 0 0;
  background: var(--color-code-bar-bg);
}
.ai-md :deep(.md-table-export) {
  display: inline-flex; align-items: center; gap: 3px;
  font-size: 11px; padding: 2px 8px;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-primary); color: var(--color-text-muted); cursor: pointer;
}
.ai-md :deep(.md-table-export:hover) { color: var(--color-accent); border-color: var(--color-accent); }
.ai-md :deep(.md-th-sortable) { cursor: pointer; user-select: none; white-space: nowrap; }
.ai-md :deep(.md-th-sortable:hover) { color: var(--color-accent); }
.ai-md :deep(.md-th-sortable[data-sort-dir="asc"]::after) { content: " ▲"; font-size: 9px; }
.ai-md :deep(.md-th-sortable[data-sort-dir="desc"]::after) { content: " ▼"; font-size: 9px; }
.ai-md :deep(hr) { border: none; border-top: 1px solid var(--color-border); margin: 10px 0; }
.ai-md :deep(img) { max-width: 100%; border-radius: var(--radius-sm); }

/* 数学公式。katex.min.css 负责字形，这里只管容器：块级公式横向可滚
   （长公式会顶破消息气泡），行内公式不要影响行高节奏。 */
.ai-md :deep(.md-math-inline) { display: inline; }
.ai-md :deep(.md-math-inline .katex) { font-size: 1.05em; }
.ai-md :deep(.md-math-block) {
  display: block;
  margin: 8px 0;
  padding: 2px 0;
  overflow-x: auto;
  overflow-y: hidden;
}
.ai-md :deep(.md-math-block .katex-display) { margin: 0; }
/* 公式还没写完 / 写法不支持时的降级形态：等宽小字，别跟正文混在一起看不出是源码 */
.ai-md :deep(.md-math-raw) {
  font-family: var(--font-mono, monospace);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-word;
  color: var(--color-text-secondary);
}

/* Mermaid 图。结构是「工具栏 + 内容区」两层：
   - 工具栏常驻（画出来之后也是），长图占满一屏时折叠 / 看源码仍然点得到；
   - 内容区按容器属性切三种形态：图形（data-mermaid-done）/ 源码（data-mermaid-src）/
     折叠（data-mermaid-folded）。源码 <pre> 一直留在 DOM 里，只是被藏起来。 */
.ai-md :deep(.md-mermaid) {
  margin: 8px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-code-bg);
  /* 圆角要裁得住里面的工具栏；横向滚动交给内容区，见 .md-mermaid-body */
  overflow: hidden;
}
.ai-md :deep(.md-mermaid-bar) {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 4px 6px 4px 10px;
  background: var(--color-code-bar-bg);
  border-bottom: 1px solid var(--color-border);
  font-size: 11px;
  color: var(--color-text-muted);
  /* 工具栏只是外壳，别让它参与划词复制 */
  user-select: none;
}
/* 折叠后只剩这一条：分隔线留着会像半张空卡片 */
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-bar) {
  border-bottom-color: transparent;
}
.ai-md :deep(.md-mermaid-lang) {
  text-transform: lowercase;
  letter-spacing: 0.02em;
}
.ai-md :deep(.md-mermaid-acts) { display: flex; align-items: center; gap: 2px; }
.ai-md :deep(.md-mermaid-act) {
  display: flex; align-items: center; justify-content: center;
  width: 24px; height: 22px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ai-md :deep(.md-mermaid-act:hover) { background: var(--color-bg-hover); color: var(--color-accent); }
.ai-md :deep(.md-mermaid-act svg) { width: 13px; height: 13px; display: block; }

/* 按钮里的两个图标靠容器属性切换，JS 只改属性、不重排 DOM ——
   看图时显示 `</>`（点了去看源码），看源码时显示眼睛（点了回来看图）。
   ⚠️ 这里的隐藏必须写成 `.md-mermaid-act .md-mermaid-i-*`（三个 class）：
   上面那条 `.md-mermaid-act svg { display: block }` 是「两 class + 一标签」，
   比单纯 `.md-mermaid-i-eye`（两 class）更具体，写短了会四个图标一起显示。 */
.ai-md :deep(.md-mermaid-act .md-mermaid-i-eye) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-act .md-mermaid-i-code) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-act .md-mermaid-i-eye) { display: block; }
/* 折叠态：展开时是向上箭头（点了收起），折叠后换成向下箭头（点了展开） */
.ai-md :deep(.md-mermaid-act .md-mermaid-i-down) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-act .md-mermaid-i-up) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-act .md-mermaid-i-down) { display: block; }

.ai-md :deep(.md-mermaid-body) { overflow-x: auto; }
/* 默认（占位块已带 data-mermaid-src）显示源码、藏起图形；点了「查看图」移除该属性就反过来。
   刻意不用 `:not([data-mermaid-src])` 压成一条：拆成两条后写的覆盖前面的，
   意图更直白，也不必依赖 :not 的参数写法（预览脚本抽 :deep 时会在 `:not(` 的括号上截断）。 */
.ai-md :deep(.md-mermaid-svg) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-done="1"] .md-mermaid-svg) {
  display: block;
  padding: 10px;
  background: var(--color-bg-primary);
}
.ai-md :deep(.md-mermaid[data-mermaid-done="1"] .md-mermaid-src) { display: none; }
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-svg) { display: none; }
/* 源码视图限高：13 个节点的图定义能有一屏长，别让它把整条回答推走 */
.ai-md :deep(.md-mermaid[data-mermaid-src] .md-mermaid-src) {
  display: block;
  max-height: 320px;
  overflow: auto;
}
.ai-md :deep(.md-mermaid[data-mermaid-folded] .md-mermaid-body) { display: none; }

/* Mermaid 输出带内联 width/max-width，这里只兜住「别溢出、别被拉变形」 */
.ai-md :deep(.md-mermaid svg) {
  display: block;
  margin: 0 auto;
  max-width: 100%;
  height: auto;
}
/* 失败提示压在源码上方：流式半截定义每帧都会失败，没有这行用户会以为是卡片坏了 */
.ai-md :deep(.md-mermaid-err) {
  padding: 4px 10px;
  border-bottom: 1px solid var(--color-border);
  background: var(--color-code-bar-bg);
  font-size: 11px;
  color: var(--color-text-muted);
}
/* 流式尾块（正在写的那一块）不显示工具栏。
   原因：最后一块每帧都会整块重新渲染（见 renderLiveBlocks），点上去的状态下一帧
   就被冲掉，表现成「点了没反应」；而且此时图还没画出来，两个按钮也没有可操作的对象。
   前面已完成的块不受影响，照常可折叠。 */
.ai-msg.streaming :deep(.ai-md-block:last-child .md-mermaid-bar) { display: none; }

/* 输入 */
.ai-input-area {
  /* 输入行控件的唯一高度来源：输入框 / 图标按钮 / 发送按钮全部引用它，
     避免各处硬编码不同数值出现高度参差 */
  --ai-input-h: 56px;
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 12px 16px;
  border-top: 1px solid var(--color-border);
  flex-shrink: 0;
}
.ai-input-row { display: flex; gap: 10px; align-items: flex-end; }
.ai-input-tools { display: flex; align-items: flex-end; gap: 6px; flex-shrink: 0; }
.ai-tool-btn {
  display: flex; align-items: center; justify-content: center;
  width: 40px; height: var(--ai-input-h); border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  border-radius: var(--radius-md); cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.ai-tool-btn:hover { color: var(--color-accent); border-color: var(--color-accent); }
/* 待发送图片预览缩略图 */
.ai-attach-preview { display: flex; flex-wrap: wrap; gap: 8px; }
.ai-attach-thumb {
  position: relative; width: 56px; height: 56px; border-radius: var(--radius-sm);
  overflow: hidden; border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
}
.ai-attach-img { width: 100%; height: 100%; object-fit: cover; display: block; }
.ai-attach-del {
  position: absolute; top: 1px; right: 1px; width: 16px; height: 16px;
  display: flex; align-items: center; justify-content: center; line-height: 1;
  border: none; border-radius: 50%; background: rgba(0,0,0,0.55); color: #fff;
  font-size: 12px; cursor: pointer; padding: 0;
}
.ai-attach-del:hover { background: var(--color-danger); }
.ai-url-row { display: flex; gap: 6px; }
.ai-url-input {
  flex: 1; height: 32px; padding: 0 10px; border: 1px solid var(--color-border);
  border-radius: var(--radius-md); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 12px; font-family: inherit; outline: none;
}
.ai-url-input:focus { border-color: var(--color-accent); }
.ai-url-ok {
  padding: 0 14px; height: 32px; border: none; border-radius: var(--radius-md);
  background: var(--color-accent); color: var(--color-accent-text);
  font-size: 12px; cursor: pointer; font-family: inherit;
}
/* 消息内图片缩略图（用户气泡 / 历史消息） */
.ai-msg-imgs { display: flex; flex-wrap: wrap; gap: 8px; }
.ai-msg.user .ai-msg-imgs { justify-content: flex-end; }
/* 缩略图尺寸：120px 在实际气泡里被 max-width 二次压缩后几乎看不清内容，
   放大到 180px/55% 并提高 max-height，保证缩略阶段就能辨认图片主体 */
.ai-msg-img { width: 180px; max-width: 55%; border-radius: var(--radius-sm); overflow: hidden; border: 1px solid var(--color-border); }
.ai-msg-img-el { width: 100%; display: block; max-height: 220px; object-fit: cover; background: var(--color-bg-tertiary); }
.ai-msg-img-el.is-clickable { cursor: zoom-in; }
.ai-img-broken { height: 80px; display: flex; align-items: center; justify-content: center; font-size: 20px; color: var(--color-text-disabled); }

/* 图片全屏查看器（Teleport 到 body，scoped 属性仍在元素上，样式生效） */
.ai-img-viewer {
  position: fixed;
  inset: 0;
  z-index: 1000;
  background: var(--color-bg-overlay-deep);
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: zoom-out;
  padding: var(--space-4);
}
.ai-img-viewer img {
  max-width: 90vw;
  max-height: 90vh;
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  cursor: default;
}
.ai-input {
  flex: 1;
  resize: none;
  height: var(--ai-input-h);
  padding: 10px 12px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  color: var(--color-text-primary);
  font-size: 13px;
  font-family: inherit;
  outline: none;
  line-height: 1.5;
}
.ai-input:focus { border-color: var(--color-accent); }
.ai-input:disabled { opacity: 0.6; cursor: not-allowed; }
.ai-send {
  align-self: flex-end;
  padding: 0 20px;
  height: var(--ai-input-h);
  border: none;
  border-radius: var(--radius-md);
  background: var(--color-accent);
  color: var(--color-accent-text);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  font-family: inherit;
  white-space: nowrap;
}
.ai-send:disabled { opacity: 0.5; cursor: not-allowed; }

</style>
