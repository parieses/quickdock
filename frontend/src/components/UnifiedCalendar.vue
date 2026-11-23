<script setup lang="ts">
// 统一日历：把待办的到期/开始/提醒时刻与定时任务的下一次运行放在同一张月视图上。
// 纯展示组件，自己从两个 binding 取数；状态留在组件内，不往侧栏塞导航项。
// 月网格用 CSS grid 自绘，7 列（日一二三四五六）× 5/6 行，不引入任何依赖。
import { computed, onActivated, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { CalendarDays, ChevronLeft, ChevronRight } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { useToast } from '../composables/useToast'
import { ListTodos } from '../../bindings/quickdock/services/todo/todoservice'
import { ListScheduledTasks } from '../../bindings/quickdock/services/appservice'
import type { Todo, ScheduledTask } from '../../bindings/quickdock/internal/db/models'

const { t, locale } = useI18n()
const toast = useToast()

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
    todos.value = tds ?? []
    tasks.value = sks ?? []
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
interface CalEvent { id: string; title: string; time: string; type: 'todo' | 'sched' }
interface CalCell {
  date: string; day: number; inMonth: boolean; isToday: boolean; isSelected: boolean; events: CalEvent[]
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
    push(td.dueDate, { id: `todo-${td.id}`, title: td.title, time, type: 'todo' })
    push(td.startTime, { id: `todo-${td.id}`, title: td.title, time, type: 'todo' })
    push(td.reminderTime, { id: `todo-${td.id}`, title: td.title, time: (td.reminderTime || '').slice(11, 16), type: 'todo' })
  }
  for (const sk of tasks.value) {
    // 定时任务：nextRun 是后端按 scheduleKind 算好的「下一次出现」（daily/weekly/
    // monthly/cron 都适用），直接拿来定位；once 且没算出 nextRun 时退回 runAt。
    // 完整重复序列需要后端展开，这里只标第一次/下一次，见实现报告。
    const when = sk.nextRun || (sk.scheduleKind === 'once' ? sk.runAt : '')
    push(when, { id: `sched-${sk.id}`, title: sk.name, time: (when || '').slice(11, 16), type: 'sched' })
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

const selectedEvents = computed(() => eventsByDate.value.get(selectedDate.value) ?? [])

function prevMonth() { cursor.value = new Date(cursor.value.getFullYear(), cursor.value.getMonth() - 1, 1) }
function nextMonth() { cursor.value = new Date(cursor.value.getFullYear(), cursor.value.getMonth() + 1, 1) }
function goToday() {
  const n = new Date()
  cursor.value = new Date(n.getFullYear(), n.getMonth(), 1)
  selectedDate.value = ymd(n)
}
function selectDay(ds: string) {
  selectedDate.value = ds
  const d = parseYmd(ds)
  if (d && (d.getMonth() !== cursor.value.getMonth() || d.getFullYear() !== cursor.value.getFullYear())) {
    cursor.value = new Date(d.getFullYear(), d.getMonth(), 1)
  }
}
</script>

<template>
  <div class="cal-wrap">
    <div v-if="error" class="cal-error">{{ error }}</div>

    <div class="cal-bar">
      <div class="cal-nav">
        <button class="cal-nav-btn" @click="prevMonth"><ChevronLeft :size="14" /></button>
        <span class="cal-month">{{ monthLabel }}</span>
        <button class="cal-nav-btn" @click="nextMonth"><ChevronRight :size="14" /></button>
      </div>
      <button class="cal-today" @click="goToday">{{ t('todoToday') }}</button>
    </div>

    <div class="cal-grid">
      <div v-for="(w, i) in weekdayLabels" :key="'w-' + i" class="cal-weekday">{{ w }}</div>
      <button
        v-for="c in cells"
        :key="c.date"
        :class="['cal-cell', { other: !c.inMonth, today: c.isToday, selected: c.isSelected }]"
        @click="selectDay(c.date)"
      >
        <span class="cal-day">{{ c.day }}</span>
        <span v-for="e in c.events.slice(0, 2)" :key="e.id" :class="['cal-ev', e.type]" :title="e.title">
          <span v-if="e.time" class="cal-ev-time">{{ e.time }}</span>{{ e.title }}
        </span>
        <span v-if="c.events.length > 2" class="cal-more">{{ t('calMore', { n: c.events.length - 2 }) }}</span>
      </button>
    </div>

    <div class="cal-detail">
      <div class="cal-detail-head"><CalendarDays :size="13" /> {{ selectedLabel }}</div>
      <div v-if="loading" class="cal-day-empty">{{ t('calLoading') }}</div>
      <div v-else-if="!selectedEvents.length" class="cal-day-empty">{{ t('calDayEmpty') }}</div>
      <div v-for="e in selectedEvents" :key="e.id" class="cal-detail-row">
        <span :class="['cal-tag', e.type]">{{ e.type === 'todo' ? t('todoTitle') : t('sched_title') }}</span>
        <span class="cal-detail-title">{{ e.title }}</span>
        <span v-if="e.time" class="cal-detail-time">{{ e.time }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cal-wrap { display: flex; flex-direction: column; gap: var(--space-4); flex: 1; min-height: 0; overflow-y: auto; padding: var(--space-4) var(--space-5); }
.cal-error { padding: var(--space-2) var(--space-3); color: var(--color-danger); background: var(--color-danger-bg); border: 1px solid var(--color-danger-border); border-radius: var(--radius-sm); font-size: var(--text-xs); }
.cal-bar { display: flex; align-items: center; justify-content: space-between; }
.cal-nav { display: flex; align-items: center; gap: var(--space-2); }
.cal-nav-btn { display: flex; align-items: center; justify-content: center; width: 2em; height: 2em; color: var(--color-text-secondary); background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); cursor: pointer; }
.cal-nav-btn:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }
.cal-month { min-width: 10em; text-align: center; font-size: var(--text-md); color: var(--color-text-primary); }
.cal-today { padding: var(--space-2) var(--space-3); font-size: var(--text-xs); font-family: inherit; color: var(--color-text-secondary); background: var(--color-bg-secondary); border: 1px solid var(--color-border); border-radius: var(--radius-sm); cursor: pointer; }
.cal-today:hover { color: var(--color-text-primary); border-color: var(--color-border-light); }

/* 月网格：用 1px 间隙 + 容器底色画表格线，不叠圆角/阴影 */
.cal-grid { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 1px; background: var(--color-border); border: 1px solid var(--color-border); border-radius: var(--radius-sm); overflow: hidden; }
.cal-weekday { padding: var(--space-2); text-align: center; font-size: var(--text-xs); color: var(--color-text-muted); background: var(--color-bg-secondary); }
.cal-cell { display: flex; flex-direction: column; align-items: stretch; gap: var(--space-1); min-height: 5em; padding: var(--space-2); background: var(--color-bg-primary); border: none; text-align: left; font-family: inherit; cursor: pointer; overflow: hidden; }
.cal-cell:hover { background: var(--color-bg-hover); }
.cal-cell.other { background: var(--color-bg-secondary); }
.cal-cell.selected { background: var(--color-accent-bg); }
.cal-day { font-size: var(--text-xs); color: var(--color-text-secondary); }
.cal-cell.other .cal-day { color: var(--color-text-disabled); }
.cal-cell.today .cal-day { color: var(--color-accent); font-weight: 600; }
.cal-ev { display: block; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; padding-left: var(--space-1); border-left: 2px solid var(--color-border); font-size: var(--text-2xs); color: var(--color-text-secondary); }
.cal-ev.sched { color: var(--color-accent); border-left-color: var(--color-accent); }
.cal-ev-time { color: var(--color-text-muted); margin-right: var(--space-1); }
.cal-more { font-size: var(--text-2xs); color: var(--color-text-muted); }

.cal-detail { padding: var(--space-3); border: 1px solid var(--color-border); border-radius: var(--radius-sm); }
.cal-detail-head { display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2); font-size: var(--text-sm); color: var(--color-text-primary); }
.cal-day-empty { padding: var(--space-3) 0; font-size: var(--text-xs); color: var(--color-text-muted); }
.cal-detail-row { display: flex; align-items: center; gap: var(--space-2); padding: var(--space-2) 0; border-top: 1px solid var(--color-hairline); font-size: var(--text-xs); }
.cal-tag { padding: 1px var(--space-2); border-radius: var(--radius-xs); font-size: var(--text-2xs); color: var(--color-text-secondary); background: var(--color-bg-secondary); }
.cal-tag.sched { color: var(--color-accent); background: var(--color-accent-bg); }
.cal-detail-title { flex: 1; min-width: 0; color: var(--color-text-primary); }
.cal-detail-time { color: var(--color-text-muted); }
</style>
