<script setup lang="ts">
// 运行时日志查看器：搜索高亮 / 级别着色 / 跟随末尾 / 只看异常。
//
// 为什么不复用 components/LogViewer.vue：那个是「应用日志中心」（文件列表 +
// tail 流 + 崩溃转储），跟这里「一次取回一段运行日志」的模型完全不同。
//
// 为什么单独成组件：同一套「筛选 + 滚到底 + 定时刷新」逻辑原本在通用日志弹窗
// 和 Redis 日志页各写一份（logEl / filteredLog / scrollLogBottom / logAuto 两套 state），
// 而且 PHP 错误日志只能看不能搜。抽出来之后三处共用一份实现。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

interface Props {
  /** 原始日志全文（按 \n 切行） */
  text: string
  /** 空日志时的提示文案 */
  emptyText?: string
  /** 不显示刷新按钮（纯只读快照，如 PHP 错误日志） */
  hideRefresh?: boolean
}
const props = withDefaults(defineProps<Props>(), { emptyText: '', hideRefresh: false })
const emit = defineEmits<{ (e: 'refresh'): void }>()

const { t } = useI18n()

// 渲染上限：日志动辄几万行，全量铺 DOM 会把弹窗卡死。
// 取尾部而不是头部——排查问题看的就是最后发生了什么。
const MAX_LINES = 1500

type Level = 'error' | 'warn' | 'info'
interface Part { s: string; hit: boolean }
interface Row { i: number; no: number; text: string; level: Level; parts: Part[] }

const query = ref('')
const issuesOnly = ref(false)
const follow = ref(true)
const cur = ref(0)
const scroller = ref<HTMLElement | null>(null)

// 级别判定只看行内特征词，不解析日志格式（各家格式都不一样，硬解析必然漏）。
// 5xx/4xx 用「前后带空格」匹配，避免把 "500ms" 这种耗时误判成错误。
const ERR_RE = /(\b(?:error|err|fatal|panic|critical|crit|failed|failure|exception|traceback|refused|unreachable|deadline exceeded)\b)|(\bstatus[=: ]\s*5\d\d\b)|( 5\d\d )/i
const WARN_RE = /(\b(?:warn|warning|deprecated|deprecation|retry|retries|slow|timeout|timed out|throttl\w*)\b)|( 4\d\d )/i

function levelOf(line: string): Level {
  if (ERR_RE.test(line)) return 'error'
  if (WARN_RE.test(line)) return 'warn'
  return 'info'
}

function splitParts(line: string, q: string): Part[] {
  if (!q) return [{ s: line, hit: false }]
  const lower = line.toLowerCase()
  const out: Part[] = []
  let from = 0
  for (;;) {
    const at = lower.indexOf(q, from)
    if (at < 0) break
    if (at > from) out.push({ s: line.slice(from, at), hit: false })
    out.push({ s: line.slice(at, at + q.length), hit: true })
    from = at + q.length
  }
  if (from < line.length) out.push({ s: line.slice(from), hit: false })
  return out.length ? out : [{ s: line, hit: false }]
}

const totalLines = computed(() => props.text.split('\n').filter((l) => l.trim() !== '').length)

const rows = computed<Row[]>(() => {
  const all = props.text.split('\n')
  const q = query.value.trim().toLowerCase()
  const onlyIssues = issuesOnly.value
  const out: Row[] = []
  for (let i = 0; i < all.length; i++) {
    const line = all[i]
    if (line.trim() === '') continue
    const level = levelOf(line)
    if (onlyIssues && level === 'info') continue
    if (q && !line.toLowerCase().includes(q)) continue
    out.push({ i: 0, no: i + 1, text: line, level, parts: splitParts(line, q) })
  }
  const sliced = out.length > MAX_LINES ? out.slice(out.length - MAX_LINES) : out
  // i 用来给「上/下」定位 DOM，必须在切片后重排
  for (let k = 0; k < sliced.length; k++) sliced[k].i = k
  return sliced
})

const truncated = computed(() => rows.value.length >= MAX_LINES)
const matchCount = computed(() => (query.value.trim() ? rows.value.length : 0))
// 异常数独立于当前筛选算：按钮上的角标是「全文有多少条异常」，
// 打开「只看异常」之后不能因为 info 被过滤掉就变成总数。
const issueCount = computed(() => {
  let n = 0
  for (const line of props.text.split('\n')) {
    if (line.trim() !== '' && levelOf(line) !== 'info') n++
  }
  return n
})

function scrollBottom() {
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

// 用户往上翻时自动关掉跟随：否则每 2s 拉一次新日志就把人拽回底部，
// 想复看某一行根本做不到。滚回底部再自动打开。
function onScroll() {
  const el = scroller.value
  if (!el) return
  const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
  if (nearBottom !== follow.value) follow.value = nearBottom
}

function toggleFollow() {
  follow.value = !follow.value
  if (follow.value) scrollBottom()
}

// 行号用 offsetTop 算，不用 scrollIntoView：后者会连带把外层弹窗/页面一起滚
function scrollToRow(i: number) {
  const el = scroller.value
  if (!el) return
  const row = el.querySelector<HTMLElement>(`[data-i="${i}"]`)
  if (row) el.scrollTop = Math.max(0, row.offsetTop - el.clientHeight / 2)
}

function step(dir: 1 | -1) {
  const n = rows.value.length
  if (!n) return
  cur.value = (cur.value + dir + n) % n
  nextTick(() => scrollToRow(cur.value))
}

watch(query, async () => {
  cur.value = 0
  await nextTick()
  // 搜索时跳到第一处命中；没在搜就按跟随策略回到底部
  if (query.value.trim()) scrollToRow(0)
  else if (follow.value) scrollBottom()
})

watch(issuesOnly, async () => {
  cur.value = 0
  await nextTick()
  if (follow.value) scrollBottom()
})

// 自动刷新会不断替换 text：只有用户仍在底部时才跟着滚，避免抢滚动条
watch(() => props.text, async () => {
  if (!follow.value) return
  await nextTick()
  scrollBottom()
})

onMounted(() => nextTick(scrollBottom))
</script>

<template>
  <div class="rv">
    <div class="rv-toolbar">
      <div class="rv-search">
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/></svg>
        <input v-model="query" class="rv-input" :placeholder="t('logFilterPlaceholder')" />
        <span v-if="matchCount" class="rv-hits">{{ cur + 1 }}/{{ matchCount }}</span>
        <template v-if="matchCount > 1">
          <button class="rv-nav" :title="t('logPrev')" @click="step(-1)">↑</button>
          <button class="rv-nav" :title="t('logNext')" @click="step(1)">↓</button>
        </template>
        <button v-if="query" class="rv-nav" :title="t('clear')" @click="query = ''">×</button>
      </div>
      <button class="rv-btn" :class="{ active: issuesOnly }" :title="t('logOnlyIssuesHint')" @click="issuesOnly = !issuesOnly">
        {{ t('logOnlyIssues') }}<span v-if="issueCount" class="rv-badge">{{ issueCount }}</span>
      </button>
      <button class="rv-btn" :class="{ active: follow }" @click="toggleFollow()">
        {{ follow ? t('logFollowOn') : t('logFollowOff') }}
      </button>
      <button v-if="!hideRefresh" class="rv-btn" @click="emit('refresh')">{{ t('logManualRefresh') }}</button>
      <span class="rv-count">{{ rows.length }} / {{ totalLines }}</span>
      <slot name="actions" />
    </div>

    <div ref="scroller" class="rv-body" @scroll.passive="onScroll">
      <div v-if="!rows.length" class="rv-empty">{{ emptyText || t('logEmpty') }}</div>
      <template v-else>
        <div v-for="r in rows" :key="r.no" :data-i="r.i" :class="['rv-line', r.level]">
          <span class="rv-no">{{ r.no }}</span>
          <span class="rv-txt"><template v-for="(p, k) in r.parts" :key="k"><mark v-if="p.hit" class="rv-hit">{{ p.s }}</mark><template v-else>{{ p.s }}</template></template></span>
        </div>
      </template>
    </div>
    <div v-if="truncated" class="rv-trunc">{{ t('logTruncated', { n: MAX_LINES }) }}</div>
  </div>
</template>

<style scoped>
.rv { display: flex; flex-direction: column; gap: 6px; min-height: 0; }

.rv-toolbar { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.rv-search {
  flex: 1 1 180px; min-width: 140px; height: 28px;
  display: flex; align-items: center; gap: 5px; padding: 0 8px;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); color: var(--color-text-disabled);
}
.rv-search:focus-within { border-color: var(--color-accent); }
.rv-input {
  flex: 1; min-width: 0; height: 100%; border: none; outline: none; background: transparent;
  font-size: 12px; font-family: inherit; color: var(--color-text-primary);
}
.rv-hits { font-size: 10px; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; flex-shrink: 0; }
.rv-nav {
  flex-shrink: 0; width: 16px; height: 16px; line-height: 1; padding: 0; cursor: pointer;
  border: none; border-radius: var(--radius-xs); background: var(--color-bg-tertiary);
  color: var(--color-text-secondary); font-size: 10px;
}
.rv-nav:hover { background: var(--color-accent-bg); color: var(--color-accent); }

.rv-btn {
  height: 28px; padding: 0 10px; font-size: 12px; cursor: pointer; white-space: nowrap;
  display: inline-flex; align-items: center; gap: 4px; font-family: inherit;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
}
.rv-btn:hover { border-color: var(--color-accent); color: var(--color-accent); }
.rv-btn.active { background: var(--color-accent); color: var(--color-accent-text); border-color: var(--color-accent); }
.rv-badge {
  min-width: 14px; padding: 0 3px; border-radius: 7px; font-size: 9px; line-height: 14px;
  text-align: center; background: var(--color-danger); color: #fff;
}
.rv-btn.active .rv-badge { background: var(--color-bg-primary); color: var(--color-danger); }
.rv-count { font-size: 10px; color: var(--color-text-disabled); margin-left: auto; font-variant-numeric: tabular-nums; }

/* position: relative 是「上/下」按 offsetTop 定位的前提 */
.rv-body {
  position: relative; max-height: 260px; overflow: auto;
  background: var(--color-bg-primary); border: 1px solid var(--color-border);
  border-radius: var(--radius-sm); padding: 4px 0;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 11px; line-height: 1.6;
}
.rv-empty { padding: 12px 10px; color: var(--color-text-muted); font-family: inherit; }

.rv-line { display: flex; gap: 8px; padding: 0 10px; white-space: pre-wrap; word-break: break-all; color: var(--color-text-muted); }
.rv-line:hover { background: var(--color-bg-hover); }
/* 错误行整体染色 + 左侧色条：扫一眼就能定位，不依赖读文案 */
.rv-line.error { color: var(--color-danger); box-shadow: inset 2px 0 0 var(--color-danger); background: var(--color-danger-bg); }
.rv-line.warn { color: var(--color-warning); box-shadow: inset 2px 0 0 var(--color-warning); }
.rv-no {
  flex-shrink: 0; width: 3.2em; text-align: right; color: var(--color-text-disabled);
  user-select: none; font-variant-numeric: tabular-nums;
}
.rv-txt { flex: 1; min-width: 0; }
.rv-hit { background: var(--color-accent); color: var(--color-accent-text); border-radius: 2px; }
.rv-trunc { font-size: 10px; color: var(--color-text-disabled); }
</style>
