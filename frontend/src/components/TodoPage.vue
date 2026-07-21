<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import ConfirmDialog from './ConfirmDialog.vue'
import {
  CreateTodo, ListTodos, UpdateTodo, ToggleTodo, DeleteTodo, ClearCompletedTodos, SendTestNotification,
  CreateSubtask, SetTodoStatus,
} from '../../bindings/quickdock/services/appservice'
import {
  Plus, Check, Trash2, Pencil, ChevronLeft, ChevronRight, CalendarDays, Bell, Clock, BellRing,
  RefreshCw, ListTree,
} from '@lucide/vue'

const { t } = useI18n()

interface Todo {
  id: string
  title: string
  done: boolean
  priority: string // none | low | medium | high
  dueDate: string  // '' 或 YYYY-MM-DD
  note: string
  startTime: string  // '' 或 YYYY-MM-DD HH:MM:SS
  endTime: string    // '' 或 YYYY-MM-DD HH:MM:SS
  reminderTime: string // '' 或 YYYY-MM-DD HH:MM:SS
  reminderSent: boolean
  tags: string      // 标签 JSON 数组字符串，如 '["工作","紧急"]'
  recurrence: string // 重复配置 JSON：{"kind":"daily|weekly|monthly","timeOfDay":"09:00","weekdays":"1,2,3"}
  parentId: string   // 子任务所属父待办 ID；空=顶层
  status: string     // todo | doing | done（权威字段）
  sort: number
  createdAt: string
  completedAt: string
}

// 4.4 重复配置结构（前端编辑态）
interface RecurrenceForm {
  kind: string      // none | daily | weekly | monthly
  timeOfDay: string // HH:MM
  weekdays: string  // 逗号分隔 0-6（0=周日）
}

const todos = ref<Todo[]>([])
const loading = ref(false)
const error = ref('')

// ---- 日历状态 ----
const viewYear = ref(new Date().getFullYear())
const viewMonth = ref(new Date().getMonth()) // 0-based
const selectedDate = ref('')                  // '' = 未排期；否则 YYYY-MM-DD
const todayStr = computed(() => {
  const d = new Date()
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
})

const WEEKDAYS = ['一', '二', '三', '四', '五', '六', '日']

const monthLabel = computed(() => `${viewYear.value}年${viewMonth.value + 1}月`)

// 时间显示：YYYY-MM-DD HH:MM:SS -> HH:MM（或带日期）
function shortTime(s: string): string {
  return s ? s.slice(11, 19) : ''
}
function hasTime(todo: Todo): boolean {
  return !!(todo.startTime || todo.endTime || todo.reminderTime)
}

// ---- 4.2 标签 ----
function parseTags(jsonStr: string): string[] {
  if (!jsonStr) return []
  try {
    const arr = JSON.parse(jsonStr)
    if (Array.isArray(arr)) return arr.filter((x: unknown) => typeof x === 'string')
  } catch { /* ignore */ }
  return []
}
// 逗号/空格/分号分隔的文本 -> JSON 数组字符串
function tagsTextToJSON(text: string): string {
  const arr = text.split(/[,，;；\s]+/).map(s => s.trim()).filter(Boolean)
  return JSON.stringify(arr)
}
function tagsJSONToText(jsonStr: string): string {
  return parseTags(jsonStr).join(', ')
}

// ---- 4.4 重复 ----
const RECUR_KINDS = ['none', 'daily', 'weekly', 'monthly']
function parseRecurrence(jsonStr: string): RecurrenceForm {
  const def: RecurrenceForm = { kind: 'none', timeOfDay: '09:00', weekdays: '' }
  if (!jsonStr) return def
  try {
    const o = JSON.parse(jsonStr)
    if (o && typeof o === 'object') {
      return {
        kind: o.kind || 'none',
        timeOfDay: o.timeOfDay || '09:00',
        weekdays: o.weekdays || '',
      }
    }
  } catch { /* ignore */ }
  return def
}
function recurrenceToJSON(f: RecurrenceForm): string {
  if (!f.kind || f.kind === 'none') return ''
  return JSON.stringify({ kind: f.kind, timeOfDay: f.timeOfDay || '09:00', weekdays: f.weekdays || '' })
}
const WEEKDAY_LABELS = ['日', '一', '二', '三', '四', '五', '六']
function recurrenceLabel(todo: Todo): string {
  const f = parseRecurrence(todo.recurrence)
  if (!f.kind || f.kind === 'none') return ''
  const time = f.timeOfDay || '09:00'
  if (f.kind === 'daily') return t('todoRecDaily', { time })
  if (f.kind === 'monthly') return t('todoRecMonthly', { time })
  if (f.kind === 'weekly') {
    const days = f.weekdays.split(',').map(s => s.trim()).filter(Boolean)
      .map(d => WEEKDAY_LABELS[Number(d)] || '').filter(Boolean)
    const wk = days.join('')
    return t('todoRecWeekly', { days: wk || '—', time })
  }
  return ''
}

// ---- 4.2 标签筛选 ----
const activeTag = ref('')
const allTags = computed(() => {
  const set = new Set<string>()
  for (const x of todos.value) for (const tg of parseTags(x.tags as unknown as string)) set.add(tg)
  return Array.from(set).sort()
})

// ---- 4.3 状态 + 看板视图 ----
const viewMode = ref<'list' | 'kanban'>('list')
const KANBAN_STATUSES = ['todo', 'doing', 'done'] as const
const STATUS_ORDER: Record<string, string[]> = {
  todo: ['doing', 'done', 'todo'],
  doing: ['done', 'todo', 'doing'],
  done: ['todo', 'doing', 'done'],
}
function statusLabel(s: string | undefined): string {
  if (s === 'doing') return t('todoStatusDoing')
  if (s === 'done') return t('todoStatusDone')
  return t('todoStatusTodo')
}
function cycleStatus(todo: Todo) {
  const next = (STATUS_ORDER[todo.status] || STATUS_ORDER.todo)[0]
  setStatus(todo, next)
}
async function setStatus(todo: Todo, status: string) {
  try {
    await unwrap(await SetTodoStatus(todo.id, status))
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// ---- 4.1 子任务（单层级 checklist）----
function subtasksOf(parentId: string): Todo[] {
  return todos.value
    .filter(x => x.parentId === parentId)
    .sort((a, b) => Number(a.done) - Number(b.done) || a.sort - b.sort)
}
const addingSubFor = ref('')
const newSubTitle = ref('')
function toggleSubInput(todo: Todo) {
  addingSubFor.value = addingSubFor.value === todo.id ? '' : todo.id
  newSubTitle.value = ''
}
async function addSubtask(parent: Todo) {
  const title = newSubTitle.value.trim()
  if (!title) return
  try {
    await unwrap(await CreateSubtask(parent.id, title))
    newSubTitle.value = ''
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// 看板：顶层待办按状态分组（忽略日期，按工作流状态；受标签筛选影响）
function statusTodos(status: string): Todo[] {
  return todos.value.filter(x =>
    x.parentId === '' && x.status === status &&
    (!activeTag.value || parseTags(x.tags).includes(activeTag.value)),
  ).sort((a, b) => a.sort - b.sort)
}
const dragId = ref('')
function onDragStart(id: string) { dragId.value = id }
async function moveToStatus(status: string) {
  const id = dragId.value
  dragId.value = ''
  if (!id) return
  const todo = todos.value.find(x => x.id === id)
  if (!todo || todo.status === status) return
  await setStatus(todo, status)
}

// 构建 6×7 共 42 个单元格（周一为起始列）
const cells = computed(() => {
  const first = new Date(viewYear.value, viewMonth.value, 1)
  let lead = first.getDay() - 1
  if (lead < 0) lead = 6
  const start = new Date(viewYear.value, viewMonth.value, 1 - lead)
  const out: {
    date: string; day: number; inMonth: boolean; isToday: boolean
    isSelected: boolean; items: Todo[]; hasReminder: boolean
  }[] = []
  for (let i = 0; i < 42; i++) {
    const d = new Date(start.getFullYear(), start.getMonth(), start.getDate() + i)
    const ds = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
    const items = todos.value.filter(x => x.dueDate === ds)
    out.push({
      date: ds,
      day: d.getDate(),
      inMonth: d.getMonth() === viewMonth.value,
      isToday: ds === todayStr.value,
      isSelected: ds === selectedDate.value,
      items,
      hasReminder: items.some(x => x.reminderTime !== '' && !x.reminderSent),
    })
  }
  return out
})

const selectedLabel = computed(() => {
  if (selectedDate.value === '') return t('todoUnscheduled')
  const [y, m, d] = selectedDate.value.split('-').map(Number)
  const wk = ['日', '一', '二', '三', '四', '五', '六'][new Date(y, m - 1, d).getDay()]
  return `${m}月${d}日 周${wk}`
})

const unscheduledCount = computed(() => todos.value.filter(x => x.dueDate === '').length)

const selectedTodos = computed(() => {
  let list = selectedDate.value === ''
    ? todos.value.filter(x => x.dueDate === '' && x.parentId === '')
    : todos.value.filter(x => x.dueDate === selectedDate.value && x.parentId === '')
  if (activeTag.value) {
    list = list.filter(x => parseTags(x.tags).includes(activeTag.value))
  }
  return [...list].sort((a, b) => Number(a.done) - Number(b.done) || a.sort - b.sort)
})

const remainingCount = computed(() => selectedTodos.value.filter(x => !x.done).length)

// ---- 操作 ----
async function refresh() {
  loading.value = true
  error.value = ''
  try {
    todos.value = unwrap<Todo[]>(await ListTodos()) ?? []
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function prevMonth() {
  if (viewMonth.value === 0) { viewMonth.value = 11; viewYear.value-- } else { viewMonth.value-- }
}
function nextMonth() {
  if (viewMonth.value === 11) { viewMonth.value = 0; viewYear.value++ } else { viewMonth.value++ }
}
function goToday() {
  const d = new Date()
  viewYear.value = d.getFullYear(); viewMonth.value = d.getMonth()
  selectedDate.value = todayStr.value
}
function selectDate(ds: string) {
  selectedDate.value = ds
  const [y, m] = ds.split('-').map(Number)
  viewYear.value = y; viewMonth.value = m - 1
}
function selectUnscheduled() {
  selectedDate.value = ''
}

// ---- 新增（点击 + 按钮弹出弹窗）----
const showCreate = ref(false)
const cTitle = ref('')
const cPriority = ref('none')
const cDueDate = ref('')
const cNote = ref('')
const cStart = ref('')
const cEnd = ref('')
const cReminder = ref('')
const cTags = ref('')
const cRecKind = ref('none')
const cRecTime = ref('09:00')
const cRecWeekdays = ref('')

function openCreate() {
  cTitle.value = ''
  cPriority.value = 'none'
  cDueDate.value = selectedDate.value
  cNote.value = ''
  cStart.value = ''
  cEnd.value = ''
  cReminder.value = ''
  cTags.value = ''
  cRecKind.value = 'none'
  cRecTime.value = '09:00'
  cRecWeekdays.value = ''
  showCreate.value = true
}
async function createTodo() {
  const title = cTitle.value.trim()
  if (!title) return
  try {
    await unwrap(await CreateTodo(
      title, cPriority.value, cDueDate.value, cNote.value,
      fromInput(cStart.value), fromInput(cEnd.value), fromInput(cReminder.value),
      recurrenceToJSON({ kind: cRecKind.value, timeOfDay: cRecTime.value, weekdays: cRecWeekdays.value }),
      tagsTextToJSON(cTags.value),
    ))
    showCreate.value = false
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

// ---- 编辑 ----
const showEdit = ref(false)
const editing = ref<Todo | null>(null)
const editTitle = ref('')
const editPriority = ref('none')
const editDueDate = ref('')
const editNote = ref('')
const editStart = ref('')   // datetime-local 格式 YYYY-MM-DDTHH:MM:SS
const editEnd = ref('')
const editReminder = ref('')
const editTags = ref('')
const editRecKind = ref('none')
const editRecTime = ref('09:00')
const editRecWeekdays = ref('')
const editStatus = ref('todo')

function toInput(s: string): string { return s ? s.replace(' ', 'T') : '' }
function fromInput(s: string): string { return s ? s.replace('T', ' ') : '' }

function startEdit(todo: Todo) {
  editing.value = todo
  editTitle.value = todo.title
  editPriority.value = todo.priority
  editDueDate.value = todo.dueDate
  editNote.value = todo.note
  editStart.value = toInput(todo.startTime)
  editEnd.value = toInput(todo.endTime)
  editReminder.value = toInput(todo.reminderTime)
  editTags.value = tagsJSONToText(todo.tags)
  const rf = parseRecurrence(todo.recurrence)
  editRecKind.value = rf.kind
  editRecTime.value = rf.timeOfDay
  editRecWeekdays.value = rf.weekdays
  editStatus.value = todo.status || 'todo'
  showEdit.value = true
}
async function saveEdit() {
  if (!editing.value) return
  const title = editTitle.value.trim()
  if (!title) return
  try {
    await unwrap(await UpdateTodo(
      editing.value.id, title, editPriority.value, editDueDate.value, editNote.value,
      fromInput(editStart.value), fromInput(editEnd.value), fromInput(editReminder.value),
      recurrenceToJSON({ kind: editRecKind.value, timeOfDay: editRecTime.value, weekdays: editRecWeekdays.value }),
      tagsTextToJSON(editTags.value),
      editStatus.value,
    ))
    showEdit.value = false
    editing.value = null
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  }
}

async function toggle(todo: Todo) {
  try { await unwrap(await ToggleTodo(todo.id)); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}
async function remove(todo: Todo) {
  delTodo.value = todo
  showDelConfirm.value = true
}
async function clearCompleted() {
  showClearConfirm.value = true
}

const delTodo = ref<Todo | null>(null)
const showDelConfirm = ref(false)
const showClearConfirm = ref(false)

async function confirmDel() {
  const todo = delTodo.value
  if (!todo) return
  showDelConfirm.value = false
  delTodo.value = null
  try { await unwrap(await DeleteTodo(todo.id)); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}
async function confirmClear() {
  showClearConfirm.value = false
  try { await unwrap(await ClearCompletedTodos()); await refresh() } catch (e) { error.value = getErrorMessage(e) }
}

// 测试提醒
const testing = ref(false)
async function testReminder() {
  testing.value = true
  try {
    await unwrap(await SendTestNotification('', ''))
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    testing.value = false
  }
}

function isOverdue(todo: Todo) {
  return todo.dueDate !== '' && todo.dueDate < todayStr.value && !todo.done
}

onMounted(() => {
  selectedDate.value = todayStr.value
  refresh()
})
</script>

<template>
  <div class="todo-page">
    <!-- 顶部栏 -->
    <div class="todo-header">
      <div class="todo-title-wrap">
        <h2 class="todo-title">{{ t('todoTitle') }}</h2>
        <span class="todo-sub">{{ t('todoSubtitle') }}</span>
      </div>
      <div class="cal-nav">
        <button class="cal-btn" @click="prevMonth" :title="t('prevMonth')"><ChevronLeft :size="16" /></button>
        <span class="cal-month">{{ monthLabel }}</span>
        <button class="cal-btn" @click="nextMonth" :title="t('nextMonth')"><ChevronRight :size="16" /></button>
        <button class="cal-today" @click="goToday">{{ t('todoToday') }}</button>
      </div>
    </div>

    <div v-if="error" class="todo-error">{{ error }}</div>

    <div class="todo-body">
      <!-- 左侧：月历 -->
      <div class="cal-card">
        <div class="cal-weekdays">
          <span v-for="w in WEEKDAYS" :key="w" class="cal-weekday">{{ w }}</span>
        </div>
        <div class="cal-grid">
          <button
            v-for="c in cells"
            :key="c.date"
            :class="['cal-cell', { 'out-month': !c.inMonth, 'is-today': c.isToday, 'is-selected': c.isSelected }]"
            @click="selectDate(c.date)"
          >
            <span class="cal-day">{{ c.day }}</span>
            <span class="cal-marks">
              <i v-for="it in c.items.slice(0, 3)" :key="it.id"
                 :class="['dot', 'p-' + it.priority, { done: it.done }]"></i>
              <Bell v-if="c.hasReminder" :size="9" class="cal-bell" />
              <em v-if="c.items.length > 3" class="cal-more">+{{ c.items.length - 3 }}</em>
            </span>
          </button>
        </div>
        <button class="unscheduled-chip" :class="{ active: selectedDate === '' }" @click="selectUnscheduled">
          <CalendarDays :size="14" />
          <span>{{ t('todoUnscheduled') }}</span>
          <em v-if="unscheduledCount" class="chip-count">{{ unscheduledCount }}</em>
        </button>
      </div>

      <!-- 右侧：选中日任务 -->
      <div class="todo-panel">
        <div class="panel-head">
          <span class="panel-date">{{ selectedLabel }}</span>
          <div class="panel-head-right">
            <div class="view-toggle">
              <button :class="['vt-btn', { active: viewMode === 'list' }]" @click="viewMode = 'list'">{{ t('todoViewList') }}</button>
              <button :class="['vt-btn', { active: viewMode === 'kanban' }]" @click="viewMode = 'kanban'">{{ t('todoViewKanban') }}</button>
            </div>
            <span class="panel-remaining">{{ t('todoRemaining', { count: remainingCount }) }}</span>
            <button class="add-fab" @click="openCreate" :title="t('todoAdd')"><Plus :size="16" /></button>
          </div>
        </div>

        <!-- 列表视图 -->
        <template v-if="viewMode === 'list'">
          <!-- 4.2 标签筛选 -->
          <div v-if="allTags.length" class="tag-filter">
            <button :class="['tag-fbtn', { active: activeTag === '' }]" @click="activeTag = ''">{{ t('todoAllTags') }}</button>
            <button
              v-for="tg in allTags" :key="tg"
              :class="['tag-fbtn', { active: activeTag === tg }]"
              @click="activeTag = tg"
            >{{ tg }}</button>
          </div>

          <!-- 列表 -->
          <div class="todo-list">
            <div v-if="selectedTodos.length === 0" class="todo-empty">
              <CalendarDays :size="28" class="empty-icon" />
              <p>{{ t('todoNoTodosThisDay') }}</p>
              <p class="empty-hint">{{ t('todoClickDayHint') }}</p>
            </div>
            <div
              v-for="todo in selectedTodos"
              :key="todo.id"
              :class="['todo-item', { done: todo.done, overdue: isOverdue(todo) }]"
            >
              <button class="check" :class="{ checked: todo.done }" @click="toggle(todo)">
                <Check v-if="todo.done" :size="13" />
              </button>
              <i :class="['pri-dot', 'p-' + todo.priority]"></i>
              <div class="todo-main">
                <span class="todo-name">{{ todo.title }}</span>
                <div class="todo-meta">
                  <span v-if="isOverdue(todo)" class="overdue-text">{{ t('todoOverdue') }}</span>
                  <span v-if="todo.reminderTime" class="meta-pill" :class="{ sent: todo.reminderSent }">
                    <Bell v-if="!todo.reminderSent" :size="11" />
                    <BellRing v-else :size="11" />
                    {{ todo.reminderTime.slice(5, 16) }}{{ todo.reminderSent ? ' · ' + t('todoReminderSent') : '' }}
                  </span>
                  <span v-if="todo.startTime" class="meta-pill">
                    <Clock :size="11" />
                    {{ shortTime(todo.startTime) }}<template v-if="todo.endTime">–{{ shortTime(todo.endTime) }}</template>
                  </span>
                  <span v-if="recurrenceLabel(todo)" class="meta-pill rec-pill">
                    <RefreshCw :size="11" /> {{ recurrenceLabel(todo) }}
                  </span>
                  <template v-for="tg in parseTags(todo.tags)" :key="tg">
                    <span class="tag-chip" @click.stop="activeTag = tg">{{ tg }}</span>
                  </template>
                  <button class="status-pill" :class="'st-' + (todo.status || 'todo')" @click.stop="cycleStatus(todo)">
                    {{ statusLabel(todo.status) }}
                  </button>
                  <span v-if="todo.note" class="todo-note">· {{ todo.note }}</span>
                </div>
              </div>
              <div class="todo-actions">
                <button class="act" @click="toggleSubInput(todo)" :title="t('todoSubtaskAdd')"><ListTree :size="13" /></button>
                <button class="act" @click="startEdit(todo)" :title="t('todoEdit')"><Pencil :size="13" /></button>
                <button class="act danger" @click="remove(todo)" :title="t('todoDelete')"><Trash2 :size="13" /></button>
              </div>

              <!-- 4.1 子任务（单层级 checklist）-->
              <div v-if="subtasksOf(todo.id).length || addingSubFor === todo.id" class="subtasks">
                <div v-for="st in subtasksOf(todo.id)" :key="st.id" :class="['sub-item', { done: st.done }]">
                  <button class="check sub-check" :class="{ checked: st.done }" @click="toggle(st)">
                    <Check v-if="st.done" :size="11" />
                  </button>
                  <span class="sub-name">{{ st.title }}</span>
                  <button class="act danger sub-del" @click="remove(st)" :title="t('todoDelete')"><Trash2 :size="11" /></button>
                </div>
                <div v-if="addingSubFor === todo.id" class="sub-add">
                  <input v-model="newSubTitle" class="modal-input sub-input" :placeholder="t('todoSubtaskPh')"
                         @keyup.enter="addSubtask(todo)" @blur="toggleSubInput(todo)" />
                </div>
              </div>
            </div>
          </div>
        </template>

        <!-- 看板视图（4.3）-->
        <div v-else class="kanban">
          <div
            v-for="st in KANBAN_STATUSES"
            :key="st"
            class="kanban-col"
            @dragover.prevent
            @drop="moveToStatus(st)"
          >
            <div class="kanban-col-head">
              <span :class="['kanban-dot', 'st-' + st]"></span>
              <span class="kanban-col-title">{{ statusLabel(st) }}</span>
              <span class="kanban-count">{{ statusTodos(st).length }}</span>
            </div>
            <div class="kanban-cards">
              <div
                v-for="card in statusTodos(st)"
                :key="card.id"
                class="kanban-card"
                draggable="true"
                @dragstart="onDragStart(card.id)"
              >
                <span class="kanban-card-name">{{ card.title }}</span>
                <div class="kanban-card-meta">
                  <i :class="['pri-dot', 'p-' + card.priority]"></i>
                  <template v-for="tg in parseTags(card.tags)" :key="tg">
                    <span class="tag-chip" @click.stop="activeTag = tg">{{ tg }}</span>
                  </template>
                  <span v-if="subtasksOf(card.id).length" class="sub-badge">▸{{ subtasksOf(card.id).length }}</span>
                </div>
              </div>
              <div v-if="statusTodos(st).length === 0" class="kanban-empty">{{ t('todoKanbanEmpty') }}</div>
            </div>
          </div>
        </div>

        <div class="panel-foot">
          <button class="clear-btn" @click="clearCompleted">{{ t('todoClearCompleted') }}</button>
          <button class="test-btn" :disabled="testing" @click="testReminder">
            <Bell :size="12" /> {{ t('todoTestReminder') }}
          </button>
        </div>
        <p class="reminder-hint">{{ t('todoReminderHint') }}</p>
      </div>
    </div>

    <!-- 编辑弹窗 -->
    <div v-if="showEdit" class="modal-mask" @click.self="showEdit = false">
      <div class="modal">
        <h3>{{ t('todoEdit') }}</h3>

        <label>{{ t('todoTitle') }}</label>
        <input v-model="editTitle" class="modal-input" placeholder="..." />

        <label>{{ t('todoPriority') }}</label>
        <select v-model="editPriority" class="modal-input">
          <option value="none">{{ t('todoPriorityNone') }}</option>
          <option value="low">{{ t('todoPriorityLow') }}</option>
          <option value="medium">{{ t('todoPriorityMedium') }}</option>
          <option value="high">{{ t('todoPriorityHigh') }}</option>
        </select>

        <label>{{ t('todoStatus') }}</label>
        <select v-model="editStatus" class="modal-input">
          <option value="todo">{{ t('todoStatusTodo') }}</option>
          <option value="doing">{{ t('todoStatusDoing') }}</option>
          <option value="done">{{ t('todoStatusDone') }}</option>
        </select>

        <div class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoStartTime') }}</label>
            <input v-model="editStart" type="datetime-local" step="1" class="modal-input" />
          </div>
          <div>
            <label><Clock :size="12" /> {{ t('todoEndTime') }}</label>
            <input v-model="editEnd" type="datetime-local" step="1" class="modal-input" />
          </div>
        </div>

        <label><Bell :size="12" /> {{ t('todoReminderTime') }}</label>
        <input v-model="editReminder" type="datetime-local" step="1" class="modal-input" />

        <!-- 4.4 重复 -->
        <label>{{ t('todoRecurrence') }}</label>
        <select v-model="editRecKind" class="modal-input">
          <option v-for="rk in RECUR_KINDS" :key="rk" :value="rk">{{ t('todoRec_' + rk) }}</option>
        </select>
        <div v-if="editRecKind !== 'none'" class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoRecTime') }}</label>
            <input v-model="editRecTime" type="time" step="1" class="modal-input" />
          </div>
          <div v-if="editRecKind === 'weekly'">
            <label>{{ t('todoRecWeekdays') }}</label>
            <input v-model="editRecWeekdays" class="modal-input" :placeholder="t('todoRecWeekdaysPh')" />
          </div>
        </div>

        <!-- 4.2 标签 -->
        <label>{{ t('todoTags') }}</label>
        <input v-model="editTags" class="modal-input" :placeholder="t('todoTagsPh')" />

        <label>{{ t('todoDue') }}</label>
        <input v-model="editDueDate" type="date" class="modal-input" />

        <label>{{ t('todoNote') }}</label>
        <input v-model="editNote" class="modal-input" />

        <div class="modal-actions">
          <button class="btn-ghost" @click="showEdit = false">{{ t('cancel') }}</button>
          <button class="btn-primary" @click="saveEdit">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <!-- 新增弹窗 -->
    <div v-if="showCreate" class="modal-mask" @click.self="showCreate = false">
      <div class="modal">
        <h3>{{ t('todoAdd') }}</h3>

        <label>{{ t('todoTitle') }}</label>
        <input v-model="cTitle" class="modal-input" :placeholder="t('todoAddPlaceholder')" />

        <label>{{ t('todoPriority') }}</label>
        <select v-model="cPriority" class="modal-input">
          <option value="none">{{ t('todoPriorityNone') }}</option>
          <option value="low">{{ t('todoPriorityLow') }}</option>
          <option value="medium">{{ t('todoPriorityMedium') }}</option>
          <option value="high">{{ t('todoPriorityHigh') }}</option>
        </select>

        <div class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoStartTime') }}</label>
            <input v-model="cStart" type="datetime-local" step="1" class="modal-input" />
          </div>
          <div>
            <label><Clock :size="12" /> {{ t('todoEndTime') }}</label>
            <input v-model="cEnd" type="datetime-local" step="1" class="modal-input" />
          </div>
        </div>

        <label><Bell :size="12" /> {{ t('todoReminderTime') }}</label>
        <input v-model="cReminder" type="datetime-local" step="1" class="modal-input" />

        <!-- 4.4 重复 -->
        <label>{{ t('todoRecurrence') }}</label>
        <select v-model="cRecKind" class="modal-input">
          <option v-for="rk in RECUR_KINDS" :key="rk" :value="rk">{{ t('todoRec_' + rk) }}</option>
        </select>
        <div v-if="cRecKind !== 'none'" class="modal-grid">
          <div>
            <label><Clock :size="12" /> {{ t('todoRecTime') }}</label>
            <input v-model="cRecTime" type="time" step="1" class="modal-input" />
          </div>
          <div v-if="cRecKind === 'weekly'">
            <label>{{ t('todoRecWeekdays') }}</label>
            <input v-model="cRecWeekdays" class="modal-input" :placeholder="t('todoRecWeekdaysPh')" />
          </div>
        </div>

        <!-- 4.2 标签 -->
        <label>{{ t('todoTags') }}</label>
        <input v-model="cTags" class="modal-input" :placeholder="t('todoTagsPh')" />

        <label>{{ t('todoDue') }}</label>
        <input v-model="cDueDate" type="date" class="modal-input" />

        <label>{{ t('todoNote') }}</label>
        <input v-model="cNote" class="modal-input" />

        <div class="modal-actions">
          <button class="btn-ghost" @click="showCreate = false">{{ t('cancel') }}</button>
          <button class="btn-primary" @click="createTodo">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :visible="showDelConfirm"
      :message="t('todoDelete') + '?'"
      @confirm="confirmDel"
      @cancel="showDelConfirm = false"
    />
    <ConfirmDialog
      :visible="showClearConfirm"
      :message="t('todoClearCompleted') + '?'"
      @confirm="confirmClear"
      @cancel="showClearConfirm = false"
    />
  </div>
</template>

<style scoped>
.todo-page {
  display: flex;
  flex-direction: column;
  height: 100%;
  padding: var(--space-6) var(--space-8);
  overflow: hidden;
}
.todo-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  margin-bottom: var(--space-5);
  flex-shrink: 0;
}
.todo-title-wrap { display: flex; align-items: baseline; gap: var(--space-3); }
.todo-title { font-size: 18px; font-weight: 600; color: var(--color-text-primary); margin: 0; }
.todo-sub { font-size: 12px; color: var(--color-text-disabled); }
.cal-nav { display: flex; align-items: center; gap: var(--space-2); }
.cal-btn {
  display: flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border-radius: var(--radius-md); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.cal-btn:hover { color: var(--color-accent); border-color: var(--color-border-focus); }
.cal-month { font-size: 14px; font-weight: 600; color: var(--color-text-primary); min-width: 88px; text-align: center; }
.cal-today {
  margin-left: var(--space-2); padding: 5px 12px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  border-radius: var(--radius-md); font-size: 12px; cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.cal-today:hover { color: var(--color-accent); border-color: var(--color-border-focus); }

.todo-error {
  margin-bottom: var(--space-4); padding: 8px 12px; font-size: 12px;
  color: var(--color-danger); background: rgba(232, 76, 76, 0.1);
  border: 1px solid rgba(232, 76, 76, 0.3); border-radius: var(--radius-md); flex-shrink: 0;
}

.todo-body { display: flex; gap: var(--space-6); flex: 1; min-height: 0; }

/* 日历卡片 */
.cal-card {
  width: 332px; flex-shrink: 0; display: flex; flex-direction: column;
  background: var(--color-bg-secondary);
  border-radius: var(--radius-lg);
  box-shadow: inset 0 0 0 1px var(--color-border);
  padding: var(--space-4);
}
.cal-weekdays { display: grid; grid-template-columns: repeat(7, 1fr); margin-bottom: var(--space-1); }
.cal-weekday { text-align: center; font-size: 11px; color: var(--color-text-disabled); font-weight: 500; padding-bottom: var(--space-1); }
.cal-grid { display: grid; grid-template-columns: repeat(7, 1fr); grid-auto-rows: 1fr; gap: 2px; flex: 1; }
.cal-cell {
  position: relative;
  display: flex; flex-direction: column; align-items: center; justify-content: flex-start; gap: 3px;
  padding: 5px 0; border: none; outline: none; background: transparent;
  cursor: pointer; font-family: inherit; border-radius: var(--radius-md);
  transition: background var(--transition-fast);
}
.cal-cell:hover { background: var(--color-bg-hover); }
.cal-cell.out-month { opacity: 0.45; }
.cal-day {
  display: flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 50%;
  font-size: 12px; font-weight: 500; color: var(--color-text-primary);
  transition: color var(--transition-fast), background var(--transition-fast), box-shadow var(--transition-fast);
}
.cal-cell.out-month .cal-day { color: var(--color-text-disabled); }
.cal-cell.is-today .cal-day { color: var(--color-accent); box-shadow: inset 0 0 0 1px var(--color-accent); }
.cal-cell.is-selected .cal-day { background: var(--color-accent); color: #fff; box-shadow: none; }
.cal-cell.is-selected.is-today .cal-day { background: var(--color-accent); color: #fff; }
.cal-marks { display: flex; align-items: center; gap: 3px; height: 7px; flex-wrap: wrap; justify-content: center; }
.dot { width: 4px; height: 4px; border-radius: 50%; display: inline-block; }
.dot.done { opacity: 0.3; }
.cal-bell { color: var(--color-accent); }
.cal-more { font-size: 9px; color: var(--color-text-disabled); font-style: normal; }

.unscheduled-chip {
  display: flex; align-items: center; gap: 6px; margin-top: var(--space-3);
  padding: 8px 10px; border: 1px solid var(--color-border); background: var(--color-bg-primary);
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.unscheduled-chip:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.unscheduled-chip.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }
.chip-count { margin-left: auto; font-style: normal; background: var(--color-bg-tertiary); border-radius: 8px; padding: 0 6px; font-size: 11px; }

/* 右侧面板 */
.todo-panel { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.panel-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: var(--space-4); }
.panel-date { font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.panel-remaining { font-size: 12px; color: var(--color-text-disabled); }

.panel-head-right { display: flex; align-items: center; gap: var(--space-3); }

/* 视图切换（列表 / 看板）*/
.view-toggle { display: inline-flex; border: 1px solid var(--color-border); border-radius: var(--radius-md); overflow: hidden; }
.vt-btn {
  padding: 4px 10px; border: none; background: var(--color-bg-tertiary);
  color: var(--color-text-muted); font-size: 12px; cursor: pointer; font-family: inherit;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.vt-btn + .vt-btn { border-left: 1px solid var(--color-border); }
.vt-btn.active { background: var(--color-accent); color: #fff; }

/* 状态徽章（4.3）*/
.status-pill {
  font-size: 10px; padding: 0 7px; line-height: 16px; border-radius: 8px; cursor: pointer;
  border: 1px solid transparent; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.status-pill.st-todo { background: var(--color-bg-tertiary); color: var(--color-text-muted); }
.status-pill.st-doing { background: rgba(245, 166, 35, 0.15); color: #f5a623; border-color: rgba(245, 166, 35, 0.35); }
.status-pill.st-done { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }

/* 子任务（4.1）*/
.todo-item { flex-wrap: wrap; }
.subtasks { flex-basis: 100%; margin: 6px 0 2px 26px; display: flex; flex-direction: column; gap: 3px; }
.sub-item { display: flex; align-items: center; gap: 7px; padding: 3px 0; }
.sub-item.done .sub-name { text-decoration: line-through; color: var(--color-text-disabled); }
.sub-check { width: 15px; height: 15px; border-radius: 4px; }
.sub-name { flex: 1; font-size: 12px; color: var(--color-text-secondary); min-width: 0; }
.sub-del { opacity: 0; width: 22px; height: 22px; }
.sub-item:hover .sub-del { opacity: 1; }
.sub-add { margin-top: 2px; }
.sub-input { height: 28px; font-size: 12px; }

/* 看板视图（4.3）*/
.kanban { flex: 1; display: grid; grid-template-columns: repeat(3, 1fr); gap: var(--space-3); min-height: 0; overflow: hidden; }
.kanban-col {
  display: flex; flex-direction: column; min-height: 0;
  background: var(--color-bg-secondary); border-radius: var(--radius-lg);
  box-shadow: inset 0 0 0 1px var(--color-border); padding: var(--space-3);
}
.kanban-col-head { display: flex; align-items: center; gap: 6px; margin-bottom: var(--space-2); flex-shrink: 0; }
.kanban-dot { width: 8px; height: 8px; border-radius: 50%; }
.kanban-dot.st-todo { background: var(--color-text-disabled); }
.kanban-dot.st-doing { background: #f5a623; }
.kanban-dot.st-done { background: var(--color-accent); }
.kanban-col-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.kanban-count { margin-left: auto; font-size: 11px; color: var(--color-text-disabled); background: var(--color-bg-tertiary); border-radius: 8px; padding: 0 7px; }
.kanban-cards { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.kanban-card {
  padding: 9px 11px; background: var(--color-bg-primary); box-shadow: inset 0 0 0 1px var(--color-border);
  border-radius: var(--radius-md); cursor: grab; transition: background var(--transition-fast);
}
.kanban-card:hover { background: var(--color-bg-hover); }
.kanban-card:active { cursor: grabbing; }
.kanban-card-name { font-size: 13px; color: var(--color-text-primary); }
.kanban-card-meta { margin-top: 5px; display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
.kanban-empty { text-align: center; font-size: 11px; color: var(--color-text-disabled); padding: var(--space-5) 0; }

.tag-filter { display: flex; flex-wrap: wrap; gap: 4px; margin-bottom: var(--space-3); }
.tag-fbtn {
  font-size: 11px; padding: 2px 10px; border-radius: 8px; cursor: pointer;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-muted); font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.tag-fbtn:hover { color: var(--color-text-primary); border-color: var(--color-border-focus); }
.tag-fbtn.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }
.add-fab {
  width: 30px; height: 30px; flex-shrink: 0; display: flex; align-items: center; justify-content: center;
  background: var(--color-accent); color: #fff; border: none; border-radius: var(--radius-md);
  cursor: pointer; transition: background var(--transition-fast);
}
.add-fab:hover { background: var(--color-accent-hover); }

.todo-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.todo-empty { text-align: center; padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.empty-icon { opacity: 0.4; margin-bottom: var(--space-2); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); margin-top: var(--space-2); }

.todo-item {
  display: flex; align-items: center; gap: var(--space-3); padding: 10px 12px;
  background: var(--color-bg-secondary); box-shadow: inset 0 0 0 1px var(--color-border);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
.todo-item:hover { background: var(--color-bg-hover); }
.todo-item.done .todo-name { text-decoration: line-through; color: var(--color-text-disabled); }
.todo-item.overdue { box-shadow: inset 0 0 0 1px rgba(232, 76, 76, 0.4); }
.check {
  flex-shrink: 0; width: 18px; height: 18px; border: 1.5px solid var(--color-border);
  background: transparent; border-radius: 5px; cursor: pointer; display: flex;
  align-items: center; justify-content: center; color: #fff; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.check:hover { border-color: var(--color-accent); }
.check.checked { background: var(--color-accent); border-color: var(--color-accent); }
.pri-dot { flex-shrink: 0; width: 8px; height: 8px; border-radius: 50%; }
.todo-main { flex: 1; min-width: 0; }
.todo-name { font-size: 13px; color: var(--color-text-primary); }
.todo-meta { font-size: 11px; color: var(--color-text-disabled); margin-top: 3px; display: flex; gap: 6px; flex-wrap: wrap; align-items: center; }
.overdue-text { color: var(--color-danger); }
.meta-pill { display: inline-flex; align-items: center; gap: 3px; color: var(--color-text-muted); }
.meta-pill.sent { color: var(--color-text-disabled); }
.todo-note { color: var(--color-text-muted); }
.tag-chip {
  font-size: 10px; padding: 0 7px; line-height: 16px; border-radius: 8px;
  background: var(--color-accent-bg); color: var(--color-accent);
  cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.tag-chip:hover { filter: brightness(1.15); }
.rec-pill { color: var(--color-text-secondary); }
.todo-actions { display: flex; gap: 2px; opacity: 0; transition: opacity var(--transition-fast); }
.todo-item:hover .todo-actions { opacity: 1; }
.act {
  width: 26px; height: 26px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled);
  border-radius: var(--radius-sm); cursor: pointer; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.act:hover { color: var(--color-text-muted); background: var(--color-bg-active); }
.act.danger:hover { color: var(--color-danger); background: rgba(232, 76, 76, 0.1); }

.panel-foot { margin-top: var(--space-3); flex-shrink: 0; display: flex; align-items: center; gap: var(--space-3); }
.clear-btn {
  padding: 6px 12px; border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-disabled); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.clear-btn:hover { color: var(--color-danger); border-color: rgba(232, 76, 76, 0.3); }
.test-btn {
  display: inline-flex; align-items: center; gap: 5px; margin-left: auto;
  padding: 6px 12px; border: 1px solid var(--color-border); background: transparent;
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.test-btn:hover:not(:disabled) { color: var(--color-accent); border-color: var(--color-accent-border); }
.test-btn:disabled { opacity: 0.5; cursor: default; }
.reminder-hint { margin: var(--space-2) 0 0; font-size: 11px; color: var(--color-text-disabled); }

/* 优先级配色 */
.p-none { background: var(--color-text-disabled); }
.p-low { background: var(--color-accent); }
.p-medium { background: #f5a623; }
.p-high { background: var(--color-danger); }

/* 弹窗 */
.modal-mask {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5); display: flex;
  align-items: center; justify-content: center; z-index: 100;
}
.modal {
  width: 380px; max-height: 94vh; overflow: visible; background: var(--color-surface);
  border-radius: var(--radius-lg); padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.modal h3 { margin: 0 0 var(--space-3); font-size: 15px; color: var(--color-text-primary); }
.modal label { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--color-text-muted); margin: var(--space-2) 0 var(--space-1); }
.modal-input {
  width: 100%; box-sizing: border-box; height: 34px; padding: 0 10px; background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  color: var(--color-text-primary); font-size: 13px; font-family: inherit; outline: none;
}
.modal-input:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }
.modal-grid input[type="datetime-local"] { width: 100%; box-sizing: border-box; }
.modal-actions { display: flex; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-4); }
.btn-ghost { padding: 6px 14px; border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-ghost:hover { background: var(--color-bg-hover); }
.btn-primary { padding: 6px 14px; border: none; background: var(--color-accent); color: #fff; border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-primary:hover { background: var(--color-accent-hover); }
</style>
