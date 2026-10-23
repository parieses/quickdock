<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, nextTick, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ScrollText, FolderOpen, RefreshCw, Search, Copy, Check, AlertTriangle, Trash2 } from '@lucide/vue'
import { ListLogFiles, ReadLogFile, ListCrashFiles, ClearCrashFiles } from '../../bindings/quickdock/services/diag/diagservice'
import { OpenLogsDir } from '../../bindings/quickdock/services/system/systemservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { copyToClipboard } from '../utils/clipboard'

const { t } = useI18n()

interface LogFile { name: string; size: number; time: string }
interface CrashFile { name: string; size: number; time: string; kind: string }

const files = ref<LogFile[]>([])
// 崩溃记录单独取一份：ListLogFiles 里虽然也带 "crash/" 前缀的条目，
// 但只有 ListCrashFiles 给出 kind（panic / js）—— 那是后端按落盘规则判定的，
// 前端照文件名前缀猜会在命名规则变动时静默错位。
const crashFiles = ref<CrashFile[]>([])
const selected = ref('')
const isCrash = ref(false)
const rawLines = ref<string[]>([])
const keyword = ref('')
const levels = ref<Record<string, boolean>>({ I: true, W: true, E: true })
const follow = ref(false)
const loading = ref(false)
let followTimer: ReturnType<typeof setInterval> | null = null
let lastCount = 0

const logFiles = computed(() => files.value.filter((f) => !f.name.startsWith('crash/')))

const levelOf = (line: string): string => {
  const m = line.match(/\]\s*\[([IWE])\]/)
  if (m) return m[1]
  if (/\[W\]/.test(line)) return 'W'
  if (/\[E\]/.test(line)) return 'E'
  return 'I'
}

const filteredLines = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  return rawLines.value.filter((l) => {
    const lv = levelOf(l)
    if (!levels.value[lv]) return false
    if (kw && !l.toLowerCase().includes(kw)) return false
    return true
  })
})

// 把一行拆成「时间戳 / 级别 / 正文」三段分别排版。
//
// 不拆就只能整行一个颜色，警告和错误埋在正文里看不出来。拆开后时间戳可以弱化，
// 级别独占一列上色，正文才是阅读主体。
//
// 匹配不上的行（Go panic 栈、第三方程序输出、插件透传的多行文本）整体进正文，
// 级别走 levelOf 的兜底判定——这类行本来就该原样呈现，不该被排版逻辑改写。
const LINE_RE = /^(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:[.,]\d{1,3})?)\s+\[([IWE])\]\s?([\s\S]*)$/

interface LogRow {
  /** 原始整行，复制时用这个（带上时间戳才有上下文） */
  raw: string
  ts: string
  level: string
  msg: string
}

function parseLine(raw: string): LogRow {
  const m = raw.match(LINE_RE)
  if (m) return { raw, ts: m[1], level: m[2], msg: m[3] }
  return { raw, ts: '', level: levelOf(raw), msg: raw }
}

// 崩溃文件不是「带时间戳的日志流」，而是「一次现场」：首部 time/kind/label/message
// 头块 + 其后一堆堆栈/详情行。这些行本身没有时间戳，直接套用日志的三段栅格会整列空着、
// 级别也落不到（stack 里没有 [E]/[W]），看起来就像坏了。
//
// 处置：把头块抽成 meta，整段详情共用「现场时间」与「按 kind 定的级别」
// （js→W、panic→E），这样崩溃记录也按日志的格式对齐，时间一眼可见。
interface CrashMeta { time: string; kind: string; label: string; message: string }

const crashMeta = computed<CrashMeta | null>(() => {
  if (!isCrash.value) return null
  const m: CrashMeta = { time: '', kind: '', label: '', message: '' }
  for (const l of rawLines.value) {
    let x: RegExpMatchArray | null
    if ((x = l.match(/^time:\s*(.+)$/))) m.time = x[1].trim()
    else if ((x = l.match(/^kind:\s*(.+)$/))) m.kind = x[1].trim()
    else if ((x = l.match(/^label:\s*(.+)$/))) m.label = x[1].trim()
    else if ((x = l.match(/^message:\s*(.+)$/))) m.message = x[1].trim()
    else if (l.trim() === '') break
  }
  return m
})

function crashLevel(kind: string): string {
  return kind.toLowerCase() === 'js' ? 'W' : 'E'
}

const crashDisplayLines = computed<LogRow[]>(() => {
  const meta = crashMeta.value
  if (!meta) return []
  const lvl = crashLevel(meta.kind)
  if (!levels.value[lvl]) return []
  const kw = keyword.value.trim().toLowerCase()
  // 头块（time/kind/label/message + 空行）之后才是堆栈/详情
  const blank = rawLines.value.findIndex((l) => l.trim() === '')
  const detail = blank >= 0 ? rawLines.value.slice(blank + 1) : rawLines.value
  return detail
    .filter((l) => !kw || l.toLowerCase().includes(kw))
    .map((l) => ({ raw: l, ts: meta.time, level: lvl, msg: l }))
})

const displayLines = computed<LogRow[]>(() =>
  isCrash.value ? crashDisplayLines.value : filteredLines.value.map(parseLine),
)

function escapeHtml(s: string): string {
  return s.replace(/[&<>]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;' }[c] as string))
}

function highlight(line: string): string {
  const kw = keyword.value.trim()
  const html = escapeHtml(line)
  if (!kw) return html
  const re = new RegExp(`(${kw.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')})`, 'gi')
  return html.replace(re, '<mark>$1</mark>')
}

async function loadFiles() {
  try {
    const res = unwrap<LogFile[]>(await ListLogFiles())
    files.value = res || []
    if (!selected.value && logFiles.value.length) {
      selected.value = logFiles.value[0].name
      isCrash.value = false
    }
  } catch (_) { /* ignore */ }
  try {
    crashFiles.value = unwrap<CrashFile[]>(await ListCrashFiles()) || []
  } catch (_) { crashFiles.value = [] }
}

// 崩溃文件复用 ReadLogFile 读（它接受 "crash/x.log" 相对路径并做穿越校验，
// 还带 tail 截断）；ReadCrashFile 返回的是 []byte，过 JSON 会变成 base64，
// 前端还得再解一次，只在 MCP 那条通道上更合适。
function currentPath(): string {
  return isCrash.value ? 'crash/' + selected.value : selected.value
}

function selectFile(name: string, crash: boolean) {
  if (selected.value === name && isCrash.value === crash) return
  selected.value = name
  isCrash.value = crash
  loadContent()
}

async function loadContent() {
  if (!selected.value) return
  loading.value = true
  try {
    const res = unwrap<string[]>(await ReadLogFile(currentPath(), 3000))
    rawLines.value = res || []
    lastCount = rawLines.value.length
    await nextTick()
    scrollToBottom()
  } catch (e: any) {
    rawLines.value = ['[error] ' + getErrorMessage(e)]
  } finally {
    loading.value = false
  }
}

// 清空崩溃记录：二次点击确认（与 SSH 生产机执行同一套「再点一次」模式），
// 不额外引入确认弹窗——这个动作不可逆但也不珍贵，两次点击够了。
const clearArmed = ref(false)
let clearTimer: ReturnType<typeof setTimeout> | null = null

async function clearCrash() {
  if (!clearArmed.value) {
    clearArmed.value = true
    if (clearTimer) clearTimeout(clearTimer)
    clearTimer = setTimeout(() => { clearArmed.value = false }, 3000)
    return
  }
  if (clearTimer) clearTimeout(clearTimer)
  clearArmed.value = false
  try {
    unwrap(await ClearCrashFiles())
    crashFiles.value = []
    if (isCrash.value) {
      isCrash.value = false
      selected.value = logFiles.value[0]?.name || ''
      rawLines.value = []
      if (selected.value) await loadContent()
    }
  } catch (e: any) {
    rawLines.value = ['[error] ' + getErrorMessage(e)]
  }
}

// 用 ref 而不是 document.querySelector('.log-body')：设置面板里将来若同时挂
// 两个日志视图（比如崩溃详情侧栏），全局查询会抓到另一个实例，滚动就跑到别人身上了。
const bodyEl = ref<HTMLElement | null>(null)

function scrollToBottom() {
  const el = bodyEl.value
  if (el) el.scrollTop = el.scrollHeight
}

function toggleFollow() {
  follow.value = !follow.value
  if (follow.value) {
    followTimer = setInterval(async () => {
      if (!selected.value) return
      try {
        const res = unwrap<string[]>(await ReadLogFile(currentPath(), 3000))
        const lines = res || []
        // 仅当新增行时更新，避免无谓重绘
        if (lines.length >= lastCount && lines.slice(0, lastCount).join('\n') === rawLines.value.join('\n')) {
          rawLines.value = lines
          lastCount = lines.length
          await nextTick()
          scrollToBottom()
        } else {
          rawLines.value = lines
          lastCount = lines.length
        }
      } catch (_) { /* ignore */ }
    }, 2000)
  } else if (followTimer) {
    clearInterval(followTimer)
    followTimer = null
  }
}

// 文件切换由 selectFile 显式触发加载（它同时设置 isCrash，watch(selected)
// 会在「同名文件、不同来源」时不触发，容易读到上一个文件的内容）。
watch([levels, keyword], () => { /* 纯前端过滤，computed 自动更新 */ })

const copied = ref(false)
let copyTimer: ReturnType<typeof setTimeout> | null = null
// 复制统一走 utils/clipboard：后端 Win32 写入，不依赖 WebView2 的焦点/激活状态。
async function copyText(text: string) {
  if (!text) return
  const ok = await copyToClipboard(text)
  if (ok) {
    copied.value = true
    if (copyTimer) clearTimeout(copyTimer)
    copyTimer = setTimeout(() => { copied.value = false }, 1500)
  }
}
function copyAll() { copyText(displayLines.value.map((r) => r.raw).join('\n')) }
function copyLine(l: string) { copyText(l) }

onMounted(async () => {
  await loadFiles()
  await loadContent()
})
onUnmounted(() => { if (followTimer) clearInterval(followTimer) })
</script>

<template>
  <div class="log-viewer">
    <aside class="log-side">
      <div class="log-side-head">
        <ScrollText :size="14" />
        <span>{{ t('logFiles') }}</span>
      </div>
      <div class="log-file-list">
        <button
          v-for="f in logFiles"
          :key="f.name"
          :class="['log-file', { active: !isCrash && f.name === selected }]"
          @click="selectFile(f.name, false)"
        >
          <span class="log-file-name" :title="f.name">{{ f.name }}</span>
          <span class="log-file-meta">{{ (f.size / 1024).toFixed(1) }} KB</span>
        </button>
        <p v-if="!logFiles.length" class="log-empty">{{ t('logEmpty') }}</p>
      </div>

      <div class="log-side-head crash-head">
        <AlertTriangle :size="14" />
        <span>{{ t('crashRecords') }}</span>
        <span v-if="crashFiles.length" class="crash-num">{{ crashFiles.length }}</span>
      </div>
      <div class="crash-list">
        <button
          v-for="c in crashFiles"
          :key="c.name"
          :class="['log-file', { active: isCrash && c.name === selected }]"
          @click="selectFile(c.name, true)"
        >
          <span class="log-file-name" :title="c.name">
            <span class="crash-badge" :data-kind="c.kind">{{ c.kind }}</span>{{ c.name }}
          </span>
          <span class="log-file-meta">{{ c.time }} · {{ (c.size / 1024).toFixed(1) }} KB</span>
        </button>
        <p v-if="!crashFiles.length" class="log-empty">{{ t('crashEmpty') }}</p>
      </div>
      <div v-if="crashFiles.length" class="crash-actions">
        <button class="log-btn" :class="{ armed: clearArmed }" @click="clearCrash">
          <Trash2 :size="13" />
          {{ clearArmed ? t('crashClearConfirm') : t('crashClear') }}
        </button>
      </div>
    </aside>

    <section class="log-main">
      <div class="log-toolbar">
        <div class="log-toolbar-left">
          <div class="log-levels" role="group" :aria-label="t('logFiles')">
            <button
              v-for="lv in ['I', 'W', 'E']"
              :key="lv"
              :class="['lv', lv, { off: !levels[lv] }]"
              :aria-pressed="levels[lv]"
              @click="levels[lv] = !levels[lv]"
            >{{ lv }}</button>
          </div>
          <div class="log-search">
            <Search :size="13" class="log-search-icon" />
            <input v-model="keyword" :placeholder="t('logSearch')" class="log-search-input" />
          </div>
          <label class="log-follow">
            <input type="checkbox" :checked="follow" @change="toggleFollow" />
            {{ t('logFollow') }}
          </label>
        </div>
        <div class="log-toolbar-right">
          <span v-if="rawLines.length" class="log-count">
            {{ t('logLines', { shown: displayLines.length, total: rawLines.length }) }}
          </span>
          <button class="log-btn" :disabled="loading" @click="loadContent">
            <RefreshCw :size="13" /> {{ t('logRefresh') }}
          </button>
          <button class="log-btn" @click="OpenLogsDir()">
            <FolderOpen :size="13" /> {{ t('logOpenDir') }}
          </button>
          <button class="log-btn" @click="copyAll" :title="t('logCopyLine')">
            <Copy v-if="!copied" :size="13" />
            <Check v-else :size="13" />
            {{ copied ? t('logCopied') : t('logCopy') }}
          </button>
        </div>
      </div>

      <div ref="bodyEl" class="log-body">
        <div v-if="isCrash && crashMeta" class="crash-meta" :data-kind="crashMeta.kind">
          <div class="crash-meta-head">
            <span class="crash-badge" :data-kind="crashMeta.kind">{{ crashMeta.kind }}</span>
            <span class="crash-meta-time">{{ crashMeta.time }}</span>
            <span class="crash-meta-label">{{ crashMeta.label }}</span>
          </div>
          <div class="crash-meta-msg">{{ crashMeta.message }}</div>
        </div>
        <div
          v-for="(row, i) in displayLines"
          :key="i"
          :class="['log-line-row', 'lv-' + row.level]"
        >
          <!-- 时间戳不做高亮：一列里出现一排小黄块会破坏对齐，
               而按时间搜（"21:06"）远少于按错误内容搜，收益不值这个代价 -->
          <span class="log-ts">{{ row.ts }}</span>
          <span class="log-lv">{{ row.level }}</span>
          <pre class="log-line"><span v-html="highlight(row.msg)"></span></pre>
          <button class="line-copy" :title="t('logCopyLine')" @click="copyLine(row.raw)">
            <Copy :size="12" />
          </button>
        </div>
        <p v-if="!displayLines.length" class="log-empty log-empty-body">
          {{ rawLines.length ? t('logNoMatch') : t('logEmpty') }}
        </p>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* 日志查看器
 *
 * 排版原则：一行三段（时间戳 / 级别 / 正文），信息密度靠对齐而不是靠颜色。
 * 时间戳弱化成参照物，级别独占一列承担信号，正文才是阅读主体——
 * 原来是整行同一个颜色，警告和错误埋在几千行里根本挑不出来。
 */
.log-viewer {
  display: flex; height: 100%; overflow: hidden;
  background: var(--color-bg-primary);
}

/* === 左侧文件列表 === */
.log-side {
  width: 216px; flex-shrink: 0;
  display: flex; flex-direction: column; overflow: hidden;
  background: var(--color-bg-secondary);
  border-right: 1px solid var(--color-border);
}
/* 分组标题做成小节标签而非整条 bar：侧栏里有两个分组，双 bar 会显得像两个工具栏 */
.log-side-head {
  display: flex; align-items: center; gap: 6px; flex-shrink: 0;
  padding: 14px 14px 6px;
  font-size: 11px; font-weight: 600; letter-spacing: 0.06em;
  color: var(--color-text-muted);
  user-select: none;
}
.log-side-head svg { opacity: 0.75; flex-shrink: 0; }
.crash-head { border-top: 1px solid var(--color-border); margin-top: 8px; padding-top: 13px; }
.crash-num {
  margin-left: auto; min-width: 16px; padding: 0 5px;
  border-radius: var(--radius-full); text-align: center;
  font-size: 10px; font-weight: 700; font-variant-numeric: tabular-nums;
  background: var(--color-danger-bg); color: var(--color-danger-text);
}

.log-file-list {
  flex: 1 1 auto; min-height: 56px;
  overflow-y: auto; padding: 0 8px 8px;
}
/* 崩溃分组不抢主列表的空间：给上限，多出来的自己滚 */
.crash-list {
  flex: 0 1 auto; min-height: 0; max-height: 40%;
  overflow-y: auto; padding: 0 8px 8px;
}

.log-file {
  display: flex; flex-direction: column; gap: 3px;
  width: 100%; padding: 7px 9px; text-align: left;
  background: transparent; color: var(--color-text-primary);
  /* 透明边框占位：选中时才加边框，避免整行高度跳动 */
  border: 1px solid transparent; border-radius: var(--radius-sm);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast),
              border-color var(--transition-fast);
}
.log-file:hover { background: var(--color-bg-hover); }
/* 选中用淡底 + 主色文字，不用实心色块：实心在暗色主题下过于抢眼，
   而且会让崩溃徽章失去对比（它只能反色成 inherit）。 */
.log-file.active {
  background: var(--color-accent-bg);
  border-color: var(--color-accent-border);
  color: var(--color-accent);
}
/* 不能用 flex：徽章是行内元素，一旦容器变 flex，后面的文件名会变成匿名 flex item，
   容器的 text-overflow: ellipsis 就不再作用于它，长文件名会直接溢出。 */
.log-file-name {
  display: block;
  font-family: var(--font-mono); font-size: 11.5px; line-height: 1.5;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.log-file-meta {
  font-size: 10px; line-height: 1.4;
  color: var(--color-text-disabled); font-variant-numeric: tabular-nums;
}
.log-file.active .log-file-meta { color: var(--color-accent); opacity: 0.7; }

.crash-badge {
  display: inline-block; flex-shrink: 0;
  margin-right: 5px; padding: 0 4px; border-radius: var(--radius-xs);
  font-family: var(--font-family); font-size: 9.5px; font-weight: 700;
  line-height: 15px; letter-spacing: 0.02em; text-transform: uppercase;
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
}
.crash-badge[data-kind='panic'] { background: var(--color-danger-bg); color: var(--color-danger-text); }
.crash-badge[data-kind='js'] { background: var(--color-warning-bg); color: var(--color-warning); }
.log-file.active .crash-badge { background: transparent; color: inherit; }

.crash-actions { padding: 8px; border-top: 1px solid var(--color-border); flex-shrink: 0; }
.crash-actions .log-btn { width: 100%; justify-content: center; }
.crash-actions .log-btn.armed {
  border-color: var(--color-danger-border); color: var(--color-danger-text);
  background: var(--color-danger-bg);
}
.log-empty { padding: 12px 6px; font-size: 11.5px; color: var(--color-text-disabled); }

/* === 右侧日志区 === */
.log-main { flex: 1; display: flex; flex-direction: column; overflow: hidden; min-width: 0; }

.log-toolbar {
  display: flex; align-items: center; gap: 10px 12px; flex-wrap: wrap;
  padding: 8px 14px; flex-shrink: 0;
  border-bottom: 1px solid var(--color-border);
}
/* 左侧「过滤条件」与右侧「动作」分组：中间留白把它们分开，
   否则七个控件排成一排，找按钮时得逐个读文字。 */
.log-toolbar-left { display: flex; align-items: center; gap: 10px; flex: 1 1 auto; min-width: 0; }
.log-toolbar-right { display: flex; align-items: center; gap: 8px; flex-shrink: 0; margin-left: auto; }
.log-count {
  font-size: 11px; color: var(--color-text-disabled);
  font-variant-numeric: tabular-nums; white-space: nowrap; padding-right: 2px;
}

/* 级别开关：开着的按钮有淡底，关掉的透明且半透明——状态一眼可辨，
   比原来「都一样、只差 0.3 透明度」清楚 */
.log-levels {
  display: flex; gap: 2px; flex-shrink: 0; padding: 2px;
  border-radius: var(--radius-sm); background: var(--color-bg-secondary);
}
.lv {
  width: 22px; height: 20px; border: none; border-radius: var(--radius-xs);
  background: transparent; cursor: pointer;
  font-family: var(--font-mono); font-size: 11px; font-weight: 700;
  line-height: 1;
  transition: background var(--transition-fast), opacity var(--transition-fast);
}
.lv:not(.off) { background: var(--color-bg-tertiary); }
.lv.off { opacity: 0.32; }
.lv.I { color: var(--color-log-info); }
.lv.W { color: var(--color-warning); }
.lv.E { color: var(--color-log-error); }
.lv:hover { opacity: 1; }

.log-search {
  display: flex; align-items: center; gap: 6px;
  flex: 1 1 auto; min-width: 120px; max-width: 340px; height: 26px;
  padding: 0 9px;
  color: var(--color-text-disabled);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
  transition: border-color var(--transition-fast);
}
.log-search:focus-within { border-color: var(--color-border-focus); }
.log-search-icon { flex-shrink: 0; }
.log-search-input {
  flex: 1; min-width: 0;
  border: none; outline: none; background: transparent;
  font-family: var(--font-family); font-size: 12px;
  color: var(--color-text-primary);
}

.log-follow {
  display: flex; align-items: center; gap: 5px; flex-shrink: 0;
  font-size: 12px; color: var(--color-text-muted);
  cursor: pointer; user-select: none;
  transition: color var(--transition-fast);
}
.log-follow:hover { color: var(--color-text-primary); }
/* 自绘复选框：项目没有全局 checkbox 样式，WebView2 在暗色主题下仍渲染成白底方块，
   与周围按钮明显割裂。这里不引 color-scheme（会影响整个页面），只就地画一个。 */
.log-follow input {
  appearance: none; -webkit-appearance: none;
  position: relative; width: 13px; height: 13px; margin: 0;
  border: 1px solid var(--color-border); border-radius: 3px;
  background: var(--color-bg-secondary); cursor: pointer;
  transition: background var(--transition-fast), border-color var(--transition-fast);
}
.log-follow input:checked {
  background: var(--color-accent); border-color: var(--color-accent);
}
.log-follow input:checked::after {
  content: ''; position: absolute; left: 3.5px; top: 0.5px;
  width: 4px; height: 7.5px;
  border: solid var(--color-accent-text);
  border-width: 0 1.5px 1.5px 0;
  transform: rotate(45deg);
}

.log-btn {
  display: inline-flex; align-items: center; justify-content: center; gap: 5px;
  height: 26px; padding: 0 10px;
  font-family: var(--font-family); font-size: 12px; white-space: nowrap;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-secondary); color: var(--color-text-muted);
  cursor: pointer;
  transition: background var(--transition-fast), color var(--transition-fast),
              border-color var(--transition-fast);
}
.log-btn:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.log-btn:disabled { opacity: 0.5; cursor: default; }

/* === 日志正文 === */
.log-body {
  flex: 1; overflow-y: auto; overflow-x: hidden;
  padding: 6px 0 12px;
  background: var(--color-bg-primary);
  font-family: var(--font-mono); font-size: 12px; line-height: 1.6;
}

.log-line-row {
  position: relative;
  display: grid;
  /* 时间戳列宽按 "2026-10-23 21:06:29.648" 在 11px 等宽下量出，再留一点余量；
     三列共用同一套栅格，栈追踪这类没有前缀的行也自动对齐到正文列。 */
  grid-template-columns: 154px 14px minmax(0, 1fr);
  align-items: start; column-gap: 8px;
  padding: 0 32px 0 12px;   /* 右侧 32px 给悬浮复制按钮留位，不压正文 */
  border-left: 2px solid transparent;
  transition: background var(--transition-fast);
}
.log-line-row:hover { background: var(--color-bg-hover); }
/* 警告/错误在左边缘留色条：滚动时余光就能看到异常位置，
   不用逐行读级别字母 */
.log-line-row.lv-W { border-left-color: var(--color-warning); }
.log-line-row.lv-E { border-left-color: var(--color-log-error); }

.log-ts {
  font-size: 11px; color: var(--color-text-muted);
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  font-variant-numeric: tabular-nums;
  user-select: text;
}
/* info 是基线，不上色：一旦整屏每行都带颜色，警告和错误就失去信号意义 */
.log-lv { font-size: 11px; font-weight: 700; text-align: center; color: var(--color-text-disabled); }
.lv-W .log-lv { color: var(--color-warning); }
.lv-E .log-lv { color: var(--color-log-error); }

.log-line {
  margin: 0;
  /* anywhere 而不是 break-all：日志里的路径、URL、堆栈符号需要在可断点处换行，
     break-all 会把整个标识符从中间劈开，反而更难读 */
  white-space: pre-wrap; overflow-wrap: anywhere;
  color: var(--color-text-primary);
}
/* 警告只染级别列和左色条，正文保持主色：
   整段黄字读起来比真正的错误还刺眼，反而抢走了「错误」的信号。
   错误行整段染红是刻意的——它就是要打断阅读。 */
.lv-E .log-line { color: var(--color-log-error); }

.line-copy {
  position: absolute; top: 1px; right: 8px;
  width: 20px; height: 20px;
  display: flex; align-items: center; justify-content: center; cursor: pointer;
  border: 1px solid var(--color-border); border-radius: var(--radius-xs);
  background: var(--color-bg-secondary); color: var(--color-text-disabled);
  opacity: 0;
  transition: opacity var(--transition-fast), color var(--transition-fast);
}
.log-line-row:hover .line-copy { opacity: 1; }
.line-copy:hover { color: var(--color-accent); border-color: var(--color-accent-border); }

.log-empty-body { padding: 48px 0; text-align: center; font-size: 12.5px; }

/* 崩溃现场头块：把 time/kind/label/message 从「正文堆」里拎出来做成卡片，
   下方堆栈每行才有统一的现场时间与级别上色。否则这些头字段会和堆栈混在一块，
   时间列要么空着要么在「time: ...」这种伪行上重复。 */
.crash-meta {
  margin: 6px 12px 10px; padding: 9px 12px;
  border: 1px solid var(--color-border);
  border-left: 3px solid var(--color-log-error);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}
.crash-meta[data-kind='js'] { border-left-color: var(--color-warning); }
.crash-meta-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.crash-meta-time {
  font-family: var(--font-mono); font-size: 11px; color: var(--color-text-muted);
  font-variant-numeric: tabular-nums;
}
.crash-meta-label {
  font-size: 11.5px; color: var(--color-text-secondary); font-weight: 600;
}
.crash-meta-msg {
  margin-top: 5px;
  font-family: var(--font-mono); font-size: 12px; line-height: 1.5;
  color: var(--color-text-primary); white-space: pre-wrap; overflow-wrap: anywhere;
}

.log-line :deep(mark) {
  background: var(--color-mark-bg); color: var(--color-mark-text);
  border-radius: var(--radius-xs); padding: 0 1px;
}
</style>
