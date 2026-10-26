<script setup lang="ts">
import { ref, computed, reactive, onMounted, onUnmounted, inject, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { Bot, Plus, Trash2, Copy, Square, RefreshCw, Eraser, StickyNote, ArrowDown, Image as ImageIcon, Link2 } from '@lucide/vue'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'
import { unwrap } from '../utils/api'
import { copyToClipboard } from '../utils/clipboard'
import { getErrorMessage } from '../utils/error'
import { splitMarkdownBlocks as splitBlocks } from '../utils/markdownBlocks'
import { useFloatMenu } from '../composables/useFloatMenu'
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
  AIListConversations,
  AIDeleteConversation,
  AIGetMessages,
  AILoadImage,
} from '../../bindings/quickdock/services/ai/aiservice'

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface AIConversation { id: string; title: string; summary: string; prompt_tokens: number; completion_tokens: number; created_at: string; updated_at: string }
interface AIMessage { id: string; conv_id: string; role: string; content: string; created_at: string; reasoning_content?: string; images?: string; imgList?: AIMsgImage[] }

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

// DOMPurify 默认白名单已包含 button / svg / pre / code，且默认放行 data-* 属性
// （ALLOW_DATA_ATTR=true）与 aria-*（ALLOW_ARIA_ATTR=true），
// 因此复制按钮可以安全地写在 v-html 里，用事件委托处理点击。
// 注意：viewBox 在白名单里是全小写 'viewbox'，DOMPurify 会把属性名统一转小写后比对，
// 故 SVG 必须用小写 viewbox 才能通过。
md.use({
  renderer: {
    code(token: { text: string; lang?: string }) {
      const lang = (token.lang || '').split(/\s+/)[0] || ''
      const body = escapeHtml(token.text.replace(/\n$/, ''))
      const label = lang || 'text'
      return `<div class="md-code" data-md-code>` +
        `<div class="md-code-bar">` +
          `<span class="md-code-lang">${escapeHtml(label)}</span>` +
          `<button type="button" class="md-code-copy" data-md-copy="1" title="${escapeHtml(t('copyCode'))}" aria-label="${escapeHtml(t('copyCode'))}">` +
            `<svg viewbox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="13" height="13" rx="2" ry="2"></rect><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"></path></svg>` +
          `</button>` +
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
  } catch (e) {
    aiProfiles.value = []
    errorMsg.value = getErrorMessage(e)
  }
}

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
  try {
    unwrap(await AIDeleteConversation(id))
    if (activeId.value === id) newConv()
    await loadConversations()
  } catch (e) {
    toast.error(getErrorMessage(e))
  }
}

function onModelChange() {
  if (aiActive.value) AISetActiveProfile(aiActive.value)
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
interface AIStreamStat { elapsedMs: number; firstTokenMs: number; promptTokens: number; completionTokens: number }
// messageId -> 统计。挂在消息对象上随消息一起渲染。
const statMap = reactive(new Map<string, AIStreamStat>())

// 把统计渲染成一行小字。只展示有意义的项：
// 全耗时 + 首字延迟（用户能感知的等待）+ token 用量。
// 首字延迟为 0 说明是中断的回复（没等到首字），此时不展示该项。
function formatStat(stat: AIStreamStat): string {
  const parts: string[] = []
  parts.push(t('aiStatElapsed', { ms: formatDuration(stat.elapsedMs) }))
  if (stat.firstTokenMs > 0) {
    parts.push(t('aiStatFirstToken', { ms: formatDuration(stat.firstTokenMs) }))
  }
  if (stat.promptTokens > 0 || stat.completionTokens > 0) {
    parts.push(t('aiStatTokens', { in: stat.promptTokens, out: stat.completionTokens }))
  }
  return parts.join(' · ')
}

// 毫秒 → 人类可读：<1s 用 ms，否则一位小数的秒
function formatDuration(ms: number): string {
  if (ms < 1000) return `${Math.max(0, Math.round(ms))}ms`
  return `${(ms / 1000).toFixed(1)}s`
}

async function send() {
  if (streaming.value) return
  const text = input.value.trim()
  if (!text && attachments.value.length === 0) return
  if (!hasKey.value) {
    toast.error(t('aiNeedKey'))
    emit('open-settings', 'ai')
    return
  }
  const convId = activeId.value
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
  messages.value.push(msg)
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
            if (data.id) activeId.value = data.id
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
                }
                statMap.set(msg.id as string, stat)
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
      if (content || reasoning) {
        const aid = `ast-${Date.now()}-stop`
        const msg: Record<string, any> = { id: aid, conv_id: activeId.value, role: 'assistant', content: content || '', created_at: new Date().toISOString(), stopped: true }
        if (reasoning) msg.reasoning_content = reasoning
        messages.value.push(msg as AIMessage)
        // 同样复用流式期已渲染的块
        primeMessageBlocks(aid, content, streamBlocks.value.map((b) => b.html))
        const stat: AIStreamStat = {
          elapsedMs: Math.round(performance.now() - startedAt),
          firstTokenMs: 0,
          promptTokens: 0,
          completionTokens: 0,
        }
        statMap.set(aid, stat)
      }
    }
    streamingText.value = ''
    streamBlocks.value = []
    reasoningText.value = ''
    streamBuf = ''
    streamReasonBuf = ''
    streaming.value = false
    streamCtrl = null
  }
}

function stop() {
  if (streamCtrl) streamCtrl.abort()
}

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

// —— 代码块复制按钮 ——
// 按钮在 v-html 里（DOMPurify 放行 button），无法直接绑 Vue 事件，
// 故在消息区做事件委托：点击 [data-md-copy] 时取其所在代码块的 <code> 文本。
function onMsgClick(e: MouseEvent) {
  const target = e.target as HTMLElement | null
  if (!target) return
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
  if (e.key === 'Escape' && imgViewer.value) closeImgViewer()
}

onMounted(async () => {
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
  if (streamCtrl) streamCtrl.abort()
  if (streamTimer) { clearTimeout(streamTimer); streamTimer = null }
  if (resizeObs) { resizeObs.disconnect(); resizeObs = null }
})
</script>

<template>
  <div class="ai-page">
    <!-- 左侧会话列表 -->
    <aside class="ai-sidebar">
      <button class="ai-new-btn" @click="newConv">
        <Plus :size="14" /> {{ t('aiNewChat') }}
      </button>
      <div class="ai-conv-list">
        <div
          v-for="c in conversations"
          :key="c.id"
          :class="['ai-conv-item', { active: c.id === activeId }]"
          @click="selectConv(c.id)"
        >
          <span class="ai-conv-title">{{ c.title || t('aiNewChat') }}</span>
          <span v-if="c.prompt_tokens > 0 || c.completion_tokens > 0" class="ai-conv-tokens" :title="t('aiConvTokenTip', { in: c.prompt_tokens, out: c.completion_tokens })">
            {{ c.prompt_tokens }}/{{ c.completion_tokens }}
          </span>
          <button class="ai-conv-del" :title="t('delete')" @click.stop="delConv(c.id)">
            <Trash2 :size="13" />
          </button>
        </div>
        <div v-if="conversations.length === 0" class="ai-conv-empty">{{ t('aiNoConversations') }}</div>
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
      <div ref="msgArea" class="ai-messages" @scroll="onMsgScroll" @mouseup="onMsgMouseUp" @keyup="onMsgMouseUp" @click="onMsgClick">
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
              <div class="ai-msg-role">{{ m.role === 'user' ? t('aiYou') : 'AI' }}</div>
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
                <button v-if="m.content" class="ai-copy" @click="copy(m.content)" :title="t('copy')">
                  <Copy :size="12" /> {{ t('copy') }}
                </button>
                <button v-if="m.role === 'assistant' && m.content" class="ai-copy" @click="saveAsNote(m.content)" :title="t('aiSaveToNote')">
                  <StickyNote :size="12" /> {{ t('aiSaveToNote') }}
                </button>
                <!-- 单条回复统计：耗时 / 首字延迟 / token 用量 -->
                <span v-if="statMap.get(m.id)" class="ai-msg-stat">
                  {{ formatStat(statMap.get(m.id)!) }}
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

            <div v-if="errorMsg" class="ai-error">{{ errorMsg }}</div>

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
.ai-msg-role { font-size: 11px; color: var(--color-text-disabled); margin-bottom: 1px; }

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
/* 单条回复统计：淡色小字，鼠标悬停可看完整信息 */
.ai-msg-stat {
  font-size: 10px;
  color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
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
.ai-error {
  align-self: flex-start;
  padding: 10px 14px; border-radius: var(--radius-lg);
  background: var(--color-danger-bg); color: var(--color-danger);
  font-size: 13px; max-width: 80%;
}
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
.ai-md :deep(table) { border-collapse: collapse; margin: 8px 0; }
.ai-md :deep(th), .ai-md :deep(td) { border: 1px solid var(--color-border); padding: 5px 9px; }
.ai-md :deep(th) { background: var(--color-bg-secondary); font-weight: 600; }
.ai-md :deep(hr) { border: none; border-top: 1px solid var(--color-border); margin: 10px 0; }
.ai-md :deep(img) { max-width: 100%; border-radius: var(--radius-sm); }

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
