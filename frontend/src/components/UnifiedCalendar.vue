<script setup lang="ts">
// 统一日历：把待办的到期/开始/提醒时刻与定时任务的下一次运行放在同一张月视图上。
// 纯展示组件，自己从两个 binding 取数；状态留在组件内，不往侧栏塞导航项。
// 月网格用 CSS grid 自绘，7 列（日一二三四五六）× 5/6 行，不引入任何依赖。
import { computed, nextTick, onActivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CalendarDays, ChevronLeft, ChevronRight, ChevronRight as Arrow, ListTodo } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { ListTodos } from '../../bindings/quickdock/services/todo/todoservice'
import { ListScheduledTasks } from '../../bindings/quickdock/services/appservice'
import type { Todo, ScheduledTask } from '../../bindings/quickdock/internal/db/models'

const { t, locale } = useI18n()
const toast = useToast()

// 日历里点任务不该只是「选中这一天」——用户想知道它怎么配的。
// 这里只发事件，跳列表 + 展开编辑由页面负责（日历不认识列表的滚动/筛选）。
const emit = defineEmits<{
  (e: 'open-task', id: string): void
  (e: 'create'): void
}>()

const todos = ref<Todo[]>([])
const tasks = ref<ScheduledTask[]>([])
const loading = ref(false)
const error = ref('')

async function reload() {
  loading.value = true
  error.value = ''
  try {
    const [tds, sks] = await Promise.all([
      unwrap<Todo[]>(await ListTodos()),
      unwrap<ScheduledTask[]>(await ListScheduledTasks()),
    ])
    // 与列表页同口径：action='todo' 是待办重复的内部实现，日历靠 ListTodos 那条链路，
    // 再把它当定时任务画一次会让同一天出现两条一样的标题。
    todos.value = tds ?? []
    tasks.value = (sks ?? []).filter((x) => x.action !== 'todo')
  } catch (e) {
    error.value = getErrorMessage(e)
    toast.error(error.value)
    logErr('unifiedCalendar:load', e)
  } finally {
    loading.value = false
  }
}

onMounted(() => { void reload() })
// 本页在 App 的 KeepAlive 白名单里：切走再回来时数据可能已经变了，补拉一次。
onActivated(() => { void reload() })
defineExpose({ reload })

// —— 日期工具 ——
function ymd(d: Date): string {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
function parseYmd(s: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s)
  if (!m) return null
  const d = new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]))
  return Number.isNaN(d.getTime()) ? null : d
}
const intlLocale = computed(() => (locale.value === 'en-US' ? 'en-US' : 'zh-CN'))

const now = new Date()
const cursor = ref(new Date(now.getFullYear(), now.getMonth(), 1))
const selectedDate = ref(ymd(now))

const monthLabel = computed(() =>
  new Intl.DateTimeFormat(intlLocale.value, { year: 'numeric', month: 'long' }).format(cursor.value))
const weekdayLabels = computed(() => {
  const fmt = new Intl.DateTimeFormat(intlLocale.value, { weekday: 'short' })
  // 2024-01-07 是周日，从它起连续七天正好是「日一二三四五六」。
  return Array.from({ length: 7 }, (_, i) => fmt.format(new Date(2024, 0, 7 + i)))
})
const selectedLabel = computed(() => {
  const d = parseYmd(selectedDate.value) ?? new Date()
  return new Intl.DateTimeFormat(intlLocale.value, { month: 'long', day: 'numeric', weekday: 'long' }).format(d)
})

// —— 事件映射 ——
// action 决定色点：蓝=应用/网页，琥珀=目录，紫=命令/脚本，玫红=Webhook，青=AI。
// 和定时任务列表左侧图标同一套色，两处扫一眼就能对上。
interface CalEvent { id: string; title: string; time: string; type: 'todo' | 'sched'; action: string }
interface CalCell {
  date: string; day: number; inMonth: boolean; isToday: boolean; isSelected: boolean; events: CalEvent[]
}

function hueOf(e: CalEvent): string {
  if (e.type === 'todo') return 'todo'
  return e.action || 'app'
}
const eventsByDate = computed(() => {
  const map = new Map<string, CalEvent[]>()
  const push = (when: string, ev: CalEvent) => {
    const date = (when || '').slice(0, 10)
    if (!date) return
    const arr = map.get(date) ?? []
    // 同一个待办的三类时刻落在同一天时只留一条，免得格子被重复标题占满。
    if (arr.some((x) => x.id === ev.id)) return
    arr.push(ev)
    map.set(date, arr)
  }
  for (const td of todos.value) {
    // 待办：dueDate / startTime / reminderTime 哪个有日期都算当天的事；
    // 时间标签优先取开始/提醒时刻，纯日期待办就不显示时间。
    const time = (td.startTime || td.reminderTime || '').slice(11, 16)
    push(td.dueDate, { id: `todo-${td.id}`, title: td.title, time, type: 'todo', action: 'todo' })
    push(td.startTime, { id: `todo-${td.id}`, title: td.title, time, type: 'todo', action: 'todo' })
    push(td.reminderTime, { id: `todo-${td.id}`, title: td.title, time: (td.reminderTime || '').slice(11, 16), type: 'todo', action: 'todo' })
  }
  for (const sk of tasks.value) {
    // 定时任务：nextRun 是后端按 scheduleKind 算好的「下一次出现」（daily/weekly/
    // monthly/cron 都适用），直接拿来定位；once 且没算出 nextRun 时退回 runAt。
    // 完整重复序列需要后端展开，这里只标第一次/下一次。
    const when = sk.nextRun || (sk.scheduleKind === 'once' ? sk.runAt : '')
    push(when, { id: `sched-${sk.id}`, title: sk.name, time: (when || '').slice(11, 16), type: 'sched', action: sk.action })
  }
  return map
})

const cells = computed<CalCell[]>(() => {
  const first = cursor.value
  const y = first.getFullYear()
  const m = first.getMonth()
  const firstWeekday = new Date(y, m, 1).getDay()
  const daysInMonth = new Date(y, m + 1, 0).getDate()
  const weeks = Math.max(5, Math.ceil((firstWeekday + daysInMonth) / 7))
  const start = new Date(y, m, 1 - firstWeekday)
  const todayStr = ymd(new Date())
  const out: CalCell[] = []
  for (let i = 0; i < weeks * 7; i++) {
    const d = new Date(start.getFullYear(), start.getMonth(), start.getDate() + i)
    const ds = ymd(d)
    out.push({
      date: ds,
      day: d.getDate(),
      inMonth: d.getMonth() === m && d.getFullYear() === y,
      isToday: ds === todayStr,
      isSelected: ds === selectedDate.value,
      events: eventsByDate.value.get(ds) ?? [],
    })
  }
  return out
})

/** 本月（只算格子里属于当月的那几天）一共有几件事：0 时整块网格换成引导页。 */
const monthEventCount = computed(() =>
  cells.value.filter((c) => c.inMonth).reduce((n, c) => n + c.events.length, 0))

const selectedEvents = computed(() =>
  [...(eventsByDate.value.get(selectedDate.value) ?? [])].sort((a, b) => (a.time || '99:99').localeCompare(b.time || '99:99')))

/** 点开当天全部任务时高亮一下右侧时间轴，让「+3 项」的点击有落点。 */
const activeId = ref('')
function focusEvent(e: CalEvent) {
  activeId.value = e.id
  if (e.type === 'sched') emit('open-task', e.id.replace(/^sched-/, ''))
}

const MAX_EVENTS = 2
const overflowOf = (c: CalCell) => Math.max(0, c.events.length - MAX_EVENTS)

function prevMonth() { cursor.value = new Date(cursor.value.getFullYear(), cursor.value.getMonth() - 1, 1) }
function nextMonth() { cursor.value = new Date(cursor.value.getFullYear(), cursor.value.getMonth() + 1, 1) }
function goToday() {
  const n = new Date()
  cursor.value = new Date(n.getFullYear(), n.getMonth(), 1)
  selectedDate.value = ymd(n)
}
function selectDay(ds: string) {
  selectedDate.value = ds
  activeId.value = ''
  const d = parseYmd(ds)
  if (d && (d.getMonth() !== cursor.value.getMonth() || d.getFullYear() !== cursor.value.getFullYear())) {
    cursor.value = new Date(d.getFullYear(), d.getMonth(), 1)
  }
}

/** 点「+N 项」：选中当天，并把下方时间轴滚进视野——否则在长月份里点了没反应。 */
const detailRef = ref<HTMLElement | null>(null)
function showAllOfDay(ds: string) {
  selectDay(ds)
  void nextTick(() => detailRef.value?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }))
}
</script>

<template>
  <div class="cal-wrap">
    <div v-if="error" class="cal-error">{{ error }}</div>

    <!-- 导航收成一组：月份与「今天」不再分居两端，鼠标不用横跨整屏 -->
    <div class="cal-bar">
      <div class="cal-nav">
        <button class="cal-nav-btn" :title="t('calPrevMonth')" @click="prevMonth"><ChevronLeft :size="14" /></button>
        <span class="cal-month">{{ monthLabel }}</span>
        <button class="cal-nav-btn" :title="t('calNextMonth')" @click="nextMonth"><ChevronRight :size="14" /></button>
        <button class="cal-today" @click="goToday">{{ t('todoToday') }}</button>
      </div>
      <span v-if="monthEventCount" class="cal-month-count">{{ t('calMonthCount', { n: monthEventCount }) }}</span>
    </div>

    <!-- 整月没有任何安排：不留一张空网格，直接给插画 + 一个能立刻做完的动作 -->
    <div v-if="!loading && monthEventCount === 0" class="cal-empty">
      <svg class="cal-empty-art" viewBox="0 0 120 96" aria-hidden="true">
        <rect x="14" y="22" width="92" height="64" rx="10" class="art-card" />
        <rect x="14" y="22" width="92" height="18" rx="10" class="art-head" />
        <rect x="14" y="32" width="92" height="8" class="art-head" />
        <circle cx="34" cy="31" r="3" class="art-pin" />
        <circle cx="86" cy="31" r="3" class="art-pin" />
        <rect x="28" y="50" width="26" height="5" rx="2.5" class="art-line" />
        <rect x="28" y="62" width="40" height="5" rx="2.5" class="art-line dim" />
        <rect x="28" y="74" width="18" height="5" rx="2.5" class="art-line dim" />
        <g class="art-float">
          <circle cx="86" cy="64" r="15" class="art-clock-face" />
          <path d="M86 56v8l6 4" class="art-clock-hand" />
        </g>
      </svg>
      <p class="cal-empty-title">{{ t('calEmptyMonthTitle') }}</p>
      <p class="cal-empty-hint">{{ t('calEmptyMonthHint') }}</p>
      <button class="qd-btn qd-btn-primary" @click="emit('create')">{{ t('calCreateTask') }}</button>
    </div>

    <template v-else>
      <div class="cal-grid">
        <div v-for="(w, i) in weekdayLabels" :key="'w-' + i" class="cal-weekday">{{ w }}</div>
        <button
          v-for="c in cells"
          :key="c.date"
          :class="['cal-cell', { other: !c.inMonth, today: c.isToday, selected: c.isSelected }]"
          @click="selectDay(c.date)"
        >
          <span class="cal-day">{{ c.day }}</span>
          <span
            v-for="e in c.events.slice(0, MAX_EVENTS)"
            :key="e.id"
            :class="['cal-ev', 'hue-' + hueOf(e)]"
            :title="(e.time ? e.time + ' ' : '') + e.title"
            @click.stop="focusEvent(e)"
          >
            <i class="cal-ev-dot"></i>
            <span v-if="e.time" class="cal-ev-time">{{ e.time }}</span>
            <span class="cal-ev-title">{{ e.title }}</span>
          </span>
          <!-- 溢出不写「还有 N 条」：一颗带底色的胶囊，点一下右侧时间轴看全 -->
          <span v-if="overflowOf(c)" class="cal-more" role="button" tabindex="0"
                @click.stop="showAllOfDay(c.date)"
                @keydown.enter.prevent="showAllOfDay(c.date)">
            {{ t('calMore', { n: overflowOf(c) }) }}
          </span>
        </button>
      </div>

      <div ref="detailRef" class="cal-detail">
        <div class="cal-detail-head">
          <CalendarDays :size="13" />
          <span class="cd-title">{{ selectedLabel }}</span>
          <span class="cd-count">{{ t('calTimeline') }} · {{ selectedEvents.length }}</span>
        </div>
        <div v-if="loading" class="cal-day-empty">{{ t('calLoading') }}</div>
        <div v-else-if="!selectedEvents.length" class="cal-day-empty">{{ t('calDayEmpty') }}</div>
        <!-- 时间轴：时间靠左成列、竖向排列；右侧是任务名与类型图标 -->
        <div v-else class="cal-timeline">
          <button
            v-for="e in selectedEvents"
            :key="e.id"
            :class="['cal-row', 'hue-' + hueOf(e), { active: activeId === e.id, 'is-todo': e.type === 'todo' }]"
            :title="e.type === 'sched' ? t('calOpenTask') : e.title"
            @click="focusEvent(e)"
          >
            <span class="cal-row-time">{{ e.time || '—' }}</span>
            <span class="cal-row-node"><i></i></span>
            <span class="cal-row-body">
              <span class="cal-row-name">{{ e.title }}</span>
              <!-- 类型只用图标标，不再逐行写「待办」两个字：一列里每行都挂同一个词
                   等于没有信息量，还和任务名抢宽度。语义留给 title / aria-label。 -->
              <span v-if="e.type === 'todo'" class="cal-row-kind" :title="t('todoTitle')" :aria-label="t('todoTitle')"><ListTodo :size="11" /></span>
            </span>
            <Arrow v-if="e.type === 'sched'" :size="12" class="cal-row-go" />
          </button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.cal-wrap { display: flex; flex-direction: column; gap: var(--space-4); flex: 1; min-height: 0; overflow-y: auto; padding: var(--space-4) var(--space-5); }
.cal-error { padding: var(--space-2) var(--space-3); color: var(--color-danger); background: var(--color-danger-bg); border: 1px solid var(--color-danger-border); border-radius: var(--radius-sm); font-size: var(--text-xs); }

/* 导航：左键 / 月份 / 右键 / 今天 连成一组，右端只放一个计数 */
.cal-bar { display: flex; align-items: center; gap: var(--space-4); }
.cal-nav { display: flex; align-items: center; gap: var(--space-2); }
.cal-nav-btn { display: flex; align-items: center; justify-content: center; width: 2em; height: 2em; color: var(--color-text-secondary); background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); cursor: pointer; }
.cal-nav-btn:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }
.cal-month { min-width: 9em; text-align: center; font-size: var(--text-md); font-weight: 600; color: var(--color-text-primary); }
.cal-today { padding: var(--space-2) var(--space-3); font-size: var(--text-xs); font-family: inherit; color: var(--color-text-secondary); background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); cursor: pointer; }
.cal-today:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }
.cal-month-count { margin-left: auto; font-size: var(--text-xs); color: var(--color-text-muted); font-variant-numeric: tabular-nums; }

/* 月网格：1px 间隙 + border-light 画线。原来用 --color-border，#232936 在深底上
   几乎看不见，整片格子会糊成一块；提亮一档后边界才立得住。 */
.cal-grid { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 1px; background: var(--color-border-light); border: 1px solid var(--color-border-light); border-radius: var(--radius-sm); overflow: hidden; }
.cal-weekday { padding: var(--space-2); text-align: center; font-size: var(--text-xs); color: var(--color-text-muted); background: var(--color-bg-secondary); }
.cal-cell { display: flex; flex-direction: column; align-items: stretch; gap: 3px; min-height: 5.6em; padding: var(--space-2); background: var(--color-bg-primary); border: none; text-align: left; font-family: inherit; cursor: pointer; overflow: hidden; }
.cal-cell:hover { background: var(--color-bg-hover); }
.cal-cell.other { background: var(--color-bg-secondary); }
.cal-cell.selected { background: var(--color-accent-bg); box-shadow: inset 0 0 0 1px var(--color-accent-border); }
.cal-day { font-size: var(--text-xs); color: var(--color-text-secondary); font-variant-numeric: tabular-nums; }
.cal-cell.other .cal-day { color: var(--color-text-muted); }
.cal-cell.today .cal-day { color: var(--color-accent); font-weight: 700; }

/* 事件条：色点 + 时间 + 标题。色点取类型身份色，和列表图标同一套 */
.cal-ev {
  display: flex; align-items: center; gap: 4px; overflow: hidden;
  padding: 1px 4px; border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary);
  font-size: var(--text-2xs); color: var(--color-text-secondary);
}
.cal-ev:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.cal-ev-dot { width: 5px; height: 5px; border-radius: 50%; flex-shrink: 0; background: var(--hue, var(--color-accent)); }
.cal-ev-time { flex-shrink: 0; color: var(--color-text-muted); font-variant-numeric: tabular-nums; }
.cal-ev-title { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }

/* 类型身份色：一处定义，格子里的事件条与下方时间轴共用 */
.hue-app, .hue-url { --hue: var(--color-accent); }
.hue-dir { --hue: var(--color-warning); }
.hue-command, .hue-script { --hue: var(--color-cat-command); }
.hue-http { --hue: var(--color-cat-http); }
.hue-ai { --hue: var(--color-cat-ai); }
.hue-todo { --hue: var(--color-text-muted); }

/* 溢出胶囊：有底色才像一个「可以点」的入口，而不是一条注释 */
.cal-more {
  align-self: flex-start; margin-top: 1px; padding: 0 6px;
  font-size: var(--text-2xs); line-height: 1.6;
  color: var(--color-accent); background: var(--color-accent-bg);
  border: 1px solid var(--color-accent-border); border-radius: var(--radius-full);
  cursor: pointer;
}
.cal-more:hover { color: var(--color-accent-text); background: var(--color-accent); }
.cal-more:focus-visible { outline: 2px solid var(--color-border-focus); outline-offset: 1px; }

/* 当天时间轴 */
.cal-detail { padding: var(--space-3) var(--space-4); border: 1px solid var(--color-border); border-radius: var(--radius-sm); background: var(--color-card-bg); }
.cal-detail-head { display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2); font-size: var(--text-sm); color: var(--color-text-primary); }
.cd-title { font-weight: 600; }
.cd-count { margin-left: auto; font-size: var(--text-2xs); color: var(--color-text-muted); }
.cal-day-empty { padding: var(--space-3) 0; font-size: var(--text-xs); color: var(--color-text-muted); }

.cal-timeline { display: flex; flex-direction: column; position: relative; }
/* 竖向轨道：从第一颗节点连到最后一行，时间轴才成立。
   left = 行左内边距 + 时间列宽 + 列间距 + 半个节点宽，线正好穿过圆点圆心。 */
.cal-timeline::before {
  content: ''; position: absolute;
  left: calc(var(--space-2) + 44px + var(--space-3) + 3px);
  top: 8px; bottom: 8px; width: 1px;
  background: var(--color-border);
}
.cal-row {
  display: flex; align-items: center; gap: var(--space-3); width: 100%;
  padding: 5px var(--space-2); border: none; border-radius: var(--radius-sm);
  background: transparent; font-family: inherit; text-align: left; cursor: pointer;
  font-size: var(--text-xs); color: var(--color-text-secondary);
}
.cal-row:hover { background: var(--color-bg-hover); }
.cal-row.active { background: var(--color-accent-bg); }
.cal-row.is-todo { cursor: default; }
.cal-row.is-todo:hover { background: transparent; }
/* 时间列固定宽度 + 右对齐：竖着读时刻才对得齐 */
.cal-row-time {
  flex: 0 0 44px; text-align: right; font-family: var(--font-mono);
  font-size: var(--text-2xs); color: var(--color-text-muted); font-variant-numeric: tabular-nums;
}
.cal-row-node { flex: 0 0 8px; display: flex; align-items: center; justify-content: center; position: relative; z-index: 1; }
.cal-row-node i { width: 7px; height: 7px; border-radius: 50%; background: var(--hue, var(--color-accent)); box-shadow: 0 0 0 2px var(--color-card-bg); }
.cal-row-body { flex: 1; min-width: 0; display: flex; align-items: center; gap: var(--space-2); }
.cal-row-name { overflow: hidden; white-space: nowrap; text-overflow: ellipsis; color: var(--color-text-primary); }
.cal-row-kind { display: inline-flex; align-items: center; gap: 3px; flex-shrink: 0; font-size: var(--text-2xs); color: var(--color-text-muted); }
.cal-row-go { flex-shrink: 0; color: var(--color-text-muted); }
.cal-row:hover .cal-row-go { color: var(--color-accent); }

/* 空月份：柔和的插画 + 一个真正可点的下一步 */
.cal-empty { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--space-3); flex: 1; min-height: 240px; text-align: center; padding: var(--space-6) var(--space-4); }
.cal-empty-art { width: 132px; height: 106px; }
.art-card { fill: var(--color-bg-tertiary); stroke: var(--color-border-light); stroke-width: 1.5; }
.art-head { fill: var(--color-accent); opacity: 0.16; }
.art-pin { fill: var(--color-accent); opacity: 0.6; }
.art-line { fill: var(--color-text-muted); opacity: 0.45; }
.art-line.dim { opacity: 0.22; }
.art-clock-face { fill: var(--color-bg-secondary); stroke: var(--color-accent); stroke-width: 1.5; }
.art-clock-hand { fill: none; stroke: var(--color-accent); stroke-width: 1.8; stroke-linecap: round; }
.art-float { animation: art-float 4.5s ease-in-out infinite; transform-origin: 86px 64px; }
@keyframes art-float { 0%, 100% { transform: translateY(0); } 50% { transform: translateY(-4px); } }
@media (prefers-reduced-motion: reduce) {
  .art-float { animation: none; }
}
.cal-empty-title { margin: 0; font-size: var(--text-md); font-weight: 600; color: var(--color-text-secondary); }
.cal-empty-hint { margin: 0; font-size: var(--text-sm); color: var(--color-text-muted); }
</style>
