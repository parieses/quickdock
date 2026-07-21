<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import {
  CreateScheduledTask, ListScheduledTasks, UpdateScheduledTask,
  DeleteScheduledTask, SetScheduledTaskEnabled, RunScheduledTaskNow,
} from '../../bindings/quickdock/services/appservice'
import type { ScheduledTask } from '../../bindings/quickdock/internal/db/models'
import WebhookSettingsModal from './WebhookSettingsModal.vue'
import ConfirmDialog from './ConfirmDialog.vue'
import {
  Plus, Play, Pencil, Trash2, AlarmClock, AppWindow, FolderOpen,
  Globe, TerminalSquare, Zap, CheckCircle2, XCircle,
} from '@lucide/vue'

const { t, locale } = useI18n()

const tasks = ref<ScheduledTask[]>([])
const loading = ref(false)
const error = ref('')

// ---- 动作类型元信息 ----
const ACTIONS = ['app', 'dir', 'url', 'command', 'http'] as const
const actionIcon: Record<string, any> = {
  app: AppWindow, dir: FolderOpen, url: Globe, command: TerminalSquare, http: Zap,
}
function actionLabel(a: string) { return t('sched_action_' + a) }

// ---- 调度类型 ----
const KINDS = ['once', 'interval', 'daily', 'weekly'] as const
// 周一为首列展示；v 使用 JS 星期编号（0=周日..6=周六）
const WEEKDAYS = computed(() => {
  const zh = ['一', '二', '三', '四', '五', '六', '日']
  const en = ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su']
  const labels = locale.value === 'en-US' ? en : zh
  return [1, 2, 3, 4, 5, 6, 0].map((v, i) => ({ v, l: labels[i] }))
})

// 目标输入框的标签/占位随动作变化
function targetLabel(a: string) { return t('sched_target_' + a) }
function targetPlaceholder(a: string) {
  switch (a) {
    case 'app': return 'C:\\Program Files\\App\\app.exe'
    case 'dir': return 'D:\\project\\quickdock'
    case 'url': return 'https://example.com'
    case 'command': return 'ping -n 1 example.com'
    case 'http': return 'https://api.example.com/webhook'
  }
  return ''
}

// ---- 展示辅助 ----
function scheduleDesc(t0: ScheduledTask): string {
  switch (t0.scheduleKind) {
    case 'once': return t('sched_kind_once') + (t0.runAt ? ' · ' + t0.runAt.slice(5, 16) : '')
    case 'interval': return t('sched_kind_interval') + ' · ' + humanInterval(t0.intervalSec)
    case 'daily': return t('sched_kind_daily') + ' · ' + (t0.timeOfDay || '')
    case 'weekly': return t('sched_kind_weekly') + ' · ' + weekdayText(t0.weekdays) + ' ' + (t0.timeOfDay || '')
  }
  return ''
}
function humanInterval(sec: number): string {
  if (sec % 3600 === 0) return (sec / 3600) + ' ' + t('sched_unit_hour')
  if (sec % 60 === 0) return (sec / 60) + ' ' + t('sched_unit_min')
  return sec + ' ' + t('sched_unit_sec')
}
function weekdayText(csv: string): string {
  if (!csv) return ''
  return csv.split(',').filter(Boolean).map(n => {
    const w = WEEKDAYS.value.find(x => x.v === Number(n))
    return w ? w.l : ''
  }).join('')
}

// ---- 加载 ----
async function refresh() {
  loading.value = true
  error.value = ''
  try {
    tasks.value = unwrap<ScheduledTask[]>(await ListScheduledTasks()) ?? []
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

// ---- 表单模型（新增 / 编辑共用） ----
const showModal = ref(false)
const editingId = ref('')            // '' = 新增
const modalTitle = computed(() => editingId.value ? t('sched_edit') : t('sched_add'))

const fName = ref('')
const fAction = ref<string>('app')
const fTarget = ref('')
const fWorkingDir = ref('')
const fHttpMethod = ref('GET')
const fHttpHeaders = ref('')
const fHttpBody = ref('')
const fKind = ref<string>('once')
const fRunAt = ref('')               // datetime-local
const fIntervalVal = ref(300)         // 秒
const fTimeOfDay = ref('09:00')
const fWeekdays = ref<number[]>([1, 2, 3, 4, 5])
const fEnabled = ref(true)
const fNotify = ref(true)

function toInput(s: string): string { return s ? s.replace(' ', 'T') : '' }
function fromInput(s: string): string { return s ? s.replace('T', ' ') : '' }

function openCreate() {
  editingId.value = ''
  fName.value = ''
  fAction.value = 'app'
  fTarget.value = ''
  fWorkingDir.value = ''
  fHttpMethod.value = 'GET'
  fHttpHeaders.value = ''
  fHttpBody.value = ''
  fKind.value = 'once'
  fRunAt.value = ''
  fIntervalVal.value = 300
  fTimeOfDay.value = '09:00'
  fWeekdays.value = [1, 2, 3, 4, 5]
  fEnabled.value = true
  fNotify.value = true
  showModal.value = true
}

function openEdit(t0: ScheduledTask) {
  editingId.value = t0.id
  fName.value = t0.name
  fAction.value = t0.action
  fTarget.value = t0.target
  fWorkingDir.value = t0.workingDir
  fHttpMethod.value = t0.httpMethod || 'GET'
  fHttpHeaders.value = t0.httpHeaders
  fHttpBody.value = t0.httpBody
  fKind.value = t0.scheduleKind
  fRunAt.value = toInput(t0.runAt)
  // 反解间隔
  const sec = t0.intervalSec || 1800
  fIntervalVal.value = Math.max(5, sec || 300)
  fTimeOfDay.value = (t0.timeOfDay || '09:00').slice(0, 5)
  fWeekdays.value = t0.weekdays ? t0.weekdays.split(',').filter(Boolean).map(Number) : []
  fEnabled.value = t0.enabled
  fNotify.value = t0.notify
  showModal.value = true
}

function toggleWeekday(v: number) {
  const i = fWeekdays.value.indexOf(v)
  if (i >= 0) fWeekdays.value.splice(i, 1)
  else fWeekdays.value.push(v)
}

function intervalVal(): number {
  return Math.max(5, Math.floor(fIntervalVal.value || 300))
}

function buildPayload(): ScheduledTask {
  return {
    id: editingId.value,
    name: fName.value.trim(),
    action: fAction.value,
    target: fTarget.value.trim(),
    workingDir: fWorkingDir.value.trim(),
    httpMethod: fHttpMethod.value,
    httpHeaders: fHttpHeaders.value,
    httpBody: fHttpBody.value,
    scheduleKind: fKind.value,
    runAt: fromInput(fRunAt.value),
    intervalSec: intervalVal(),
    timeOfDay: fKind.value === 'daily' || fKind.value === 'weekly' ? fTimeOfDay.value + ':00' : '',
    weekdays: fKind.value === 'weekly' ? [...fWeekdays.value].sort((a, b) => a - b).join(',') : '',
    enabled: fEnabled.value,
    notify: fNotify.value,
    nextRun: '', lastRun: '', lastStatus: '', lastResult: '', sort: 0, createdAt: '',
  } as ScheduledTask
}

const saving = ref(false)
async function save() {
  const p = buildPayload()
  if (!p.name) { error.value = t('sched_err_name'); return }
  if (!p.target) { error.value = t('sched_err_target'); return }
  if (p.scheduleKind === 'once' && !p.runAt) { error.value = t('sched_err_runat'); return }
  if (p.scheduleKind === 'weekly' && !p.weekdays) { error.value = t('sched_err_weekday'); return }
  saving.value = true
  error.value = ''
  try {
    if (editingId.value) {
      await unwrap(await UpdateScheduledTask(p))
    } else {
      await unwrap(await CreateScheduledTask(p))
    }
    showModal.value = false
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    saving.value = false
  }
}

async function toggleEnabled(t0: ScheduledTask) {
  try {
    await unwrap(await SetScheduledTaskEnabled(t0.id, !t0.enabled))
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) }
}

const runningId = ref('')
async function runNow(t0: ScheduledTask) {
  runningId.value = t0.id
  error.value = ''
  try {
    const r = await RunScheduledTaskNow(t0.id)
    // RunScheduledTaskNow 失败时 code=1，成功时 msg 为结果
    if (r && r.code !== 0) { error.value = r.msg }
    await refresh()
  } catch (e) {
    error.value = getErrorMessage(e)
  } finally {
    runningId.value = ''
  }
}

async function remove(t0: ScheduledTask) {
  delTask.value = t0
  showDelConfirm.value = true
}

const delTask = ref<ScheduledTask | null>(null)
const showDelConfirm = ref(false)

async function confirmDel() {
  const t = delTask.value
  if (!t) return
  showDelConfirm.value = false
  delTask.value = null
  try {
    await unwrap(await DeleteScheduledTask(t.id))
    await refresh()
  } catch (e) { error.value = getErrorMessage(e) }
}

onMounted(refresh)
</script>

<template>
  <div class="sched-page">
    <!-- 顶部栏 -->
    <div class="sched-header">
      <div class="sched-title-wrap">
        <h2 class="sched-title">{{ t('sched_title') }}</h2>
        <span class="sched-sub">{{ t('sched_subtitle') }}</span>
      </div>
      <div class="sched-header-actions">
        <WebhookSettingsModal :label="t('sched_nf_title')" />
        <button class="add-btn" @click="openCreate"><Plus :size="15" /> {{ t('sched_add') }}</button>
      </div>
    </div>

    <div v-if="error" class="sched-error">{{ error }}</div>

    <!-- 列表 -->
    <div class="sched-list">
      <div v-if="!loading && tasks.length === 0" class="sched-empty">
        <AlarmClock :size="30" class="empty-icon" />
        <p>{{ t('sched_empty') }}</p>
        <p class="empty-hint">{{ t('sched_empty_hint') }}</p>
      </div>

      <div v-for="task in tasks" :key="task.id" :class="['sched-item', { disabled: !task.enabled }]">
        <div class="item-icon" :class="'a-' + task.action">
          <component :is="actionIcon[task.action]" :size="16" />
        </div>

        <div class="item-main">
          <div class="item-line1">
            <span class="item-name">{{ task.name }}</span>
            <span class="item-badge">{{ actionLabel(task.action) }}</span>
          </div>
          <div class="item-target" :title="task.target">{{ task.target }}</div>
          <div class="item-meta">
            <span class="meta-sched">{{ scheduleDesc(task) }}</span>
            <span v-if="task.enabled && task.nextRun" class="meta-next">
              {{ t('sched_next') }} {{ task.nextRun.slice(5, 16) }}
            </span>
            <span v-if="task.lastRun" class="meta-last" :class="task.lastStatus">
              <CheckCircle2 v-if="task.lastStatus === 'ok'" :size="11" />
              <XCircle v-else :size="11" />
              {{ task.lastResult || task.lastStatus }}
            </span>
          </div>
        </div>

        <div class="item-actions">
          <!-- 启用开关 -->
          <button class="switch" :class="{ on: task.enabled }" @click="toggleEnabled(task)"
                  :title="task.enabled ? t('sched_enabled') : t('sched_disabled')">
            <span class="knob"></span>
          </button>
          <button class="act" :disabled="runningId === task.id" @click="runNow(task)" :title="t('sched_run_now')">
            <Play :size="14" />
          </button>
          <button class="act" @click="openEdit(task)" :title="t('sched_edit')"><Pencil :size="14" /></button>
          <button class="act danger" @click="remove(task)" :title="t('sched_delete')"><Trash2 :size="14" /></button>
        </div>
      </div>
    </div>

    <!-- 新增/编辑弹窗 -->
    <div v-if="showModal" class="modal-mask" @click.self="showModal = false">
      <div class="modal">
        <h3>{{ modalTitle }}</h3>

        <label>{{ t('sched_name') }}</label>
        <input v-model="fName" class="modal-input" :placeholder="t('sched_name_ph')" />

        <!-- 动作类型 -->
        <label>{{ t('sched_action') }}</label>
        <div class="seg">
          <button v-for="a in ACTIONS" :key="a" type="button"
                  :class="['seg-btn', { active: fAction === a }]" @click="fAction = a">
            <component :is="actionIcon[a]" :size="13" /> {{ actionLabel(a) }}
          </button>
        </div>

        <!-- 目标 -->
        <label>{{ targetLabel(fAction) }}</label>
        <input v-model="fTarget" class="modal-input" :placeholder="targetPlaceholder(fAction)" />

        <!-- 工作目录（app/command） -->
        <template v-if="fAction === 'app' || fAction === 'command'">
          <label>{{ t('sched_working_dir') }}</label>
          <input v-model="fWorkingDir" class="modal-input" :placeholder="t('sched_working_dir_ph')" />
        </template>

        <!-- HTTP 选项 -->
        <template v-if="fAction === 'http'">
          <div class="modal-grid">
            <div>
              <label>{{ t('sched_http_method') }}</label>
              <select v-model="fHttpMethod" class="modal-input">
                <option>GET</option><option>POST</option><option>PUT</option>
                <option>DELETE</option><option>PATCH</option><option>HEAD</option>
              </select>
            </div>
          </div>
          <label>{{ t('sched_http_headers') }}</label>
          <textarea v-model="fHttpHeaders" class="modal-input modal-area" rows="2"
                    :placeholder="'Content-Type: application/json'"></textarea>
          <label>{{ t('sched_http_body') }}</label>
          <textarea v-model="fHttpBody" class="modal-input modal-area" rows="2"
                    :placeholder="'{&quot;key&quot;:&quot;value&quot;}'"></textarea>
        </template>

        <!-- 调度类型 -->
        <label>{{ t('sched_kind') }}</label>
        <div class="seg">
          <button v-for="k in KINDS" :key="k" type="button"
                  :class="['seg-btn', { active: fKind === k }]" @click="fKind = k">
            {{ t('sched_kind_' + k) }}
          </button>
        </div>

        <!-- once -->
        <template v-if="fKind === 'once'">
          <label>{{ t('sched_runat') }}</label>
          <input v-model="fRunAt" type="datetime-local" step="1" class="modal-input" />
        </template>

        <!-- interval -->
        <template v-else-if="fKind === 'interval'">
          <label>{{ t('sched_interval') }}{{ t('sched_unit_sec') }}</label>
          <input v-model.number="fIntervalVal" type="number" min="5" class="modal-input" />
        </template>

        <!-- daily -->
        <template v-else-if="fKind === 'daily'">
          <label>{{ t('sched_time_of_day') }}</label>
          <input v-model="fTimeOfDay" type="time" class="modal-input" />
        </template>

        <!-- weekly -->
        <template v-else-if="fKind === 'weekly'">
          <label>{{ t('sched_weekdays') }}</label>
          <div class="wd-row">
            <button v-for="w in WEEKDAYS" :key="w.v" type="button"
                    :class="['wd-btn', { active: fWeekdays.includes(w.v) }]" @click="toggleWeekday(w.v)">
              {{ w.l }}
            </button>
          </div>
          <label>{{ t('sched_time_of_day') }}</label>
          <input v-model="fTimeOfDay" type="time" class="modal-input" />
        </template>

        <!-- 开关 -->
        <div class="toggle-row">
          <label class="toggle-label">
            <input type="checkbox" v-model="fEnabled" /> {{ t('sched_enable_now') }}
          </label>
          <label class="toggle-label">
            <input type="checkbox" v-model="fNotify" /> {{ t('sched_notify') }}
          </label>
        </div>

        <div class="modal-actions">
          <button class="btn-ghost" @click="showModal = false">{{ t('cancel') }}</button>
          <button class="btn-primary" :disabled="saving" @click="save">{{ t('save') }}</button>
        </div>
      </div>
    </div>

    <ConfirmDialog
      :visible="showDelConfirm"
      :message="t('sched_delete') + '?'"
      @confirm="confirmDel"
      @cancel="showDelConfirm = false"
    />
  </div>
</template>

<style scoped>
.sched-page {
  display: flex; flex-direction: column; height: 100%;
  padding: var(--space-6) var(--space-8); overflow: hidden;
}
.sched-header {
  display: flex; align-items: flex-end; justify-content: space-between;
  margin-bottom: var(--space-5); flex-shrink: 0;
}
.sched-header-actions { display: flex; align-items: center; gap: var(--space-2); }
.sched-title-wrap { display: flex; align-items: baseline; gap: var(--space-3); }
.sched-title { font-size: 18px; font-weight: 600; color: var(--color-text-primary); margin: 0; }
.sched-sub { font-size: 12px; color: var(--color-text-disabled); }
.add-btn {
  display: inline-flex; align-items: center; gap: 5px; padding: 7px 14px;
  background: var(--color-accent); color: #fff; border: none; border-radius: var(--radius-md);
  font-size: 13px; cursor: pointer; font-family: inherit; transition: background var(--transition-fast);
}
.add-btn:hover { background: var(--color-accent-hover); }

.sched-error {
  margin-bottom: var(--space-4); padding: 8px 12px; font-size: 12px;
  color: var(--color-danger); background: rgba(232, 76, 76, 0.1);
  border: 1px solid rgba(232, 76, 76, 0.3); border-radius: var(--radius-md); flex-shrink: 0;
}

.sched-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: var(--space-2); }
.sched-empty { text-align: center; padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.empty-icon { opacity: 0.4; margin-bottom: var(--space-2); }
.empty-hint { font-size: 11px; color: var(--color-text-muted); margin-top: var(--space-2); }

.sched-item {
  display: flex; align-items: center; gap: var(--space-3); padding: 12px 14px;
  background: var(--color-bg-secondary); box-shadow: inset 0 0 0 1px var(--color-border);
  border-radius: var(--radius-md); transition: background var(--transition-fast);
}
.sched-item:hover { background: var(--color-bg-hover); }
.sched-item.disabled { opacity: 0.55; }

.item-icon {
  flex-shrink: 0; width: 34px; height: 34px; display: flex; align-items: center; justify-content: center;
  border-radius: var(--radius-md); color: #fff; background: var(--color-accent);
}
.item-icon.a-app { background: #4a9eff; }
.item-icon.a-dir { background: #f5a623; }
.item-icon.a-url { background: #46b17b; }
.item-icon.a-command { background: #9b6dff; }
.item-icon.a-http { background: #e0556b; }

.item-main { flex: 1; min-width: 0; }
.item-line1 { display: flex; align-items: center; gap: 8px; }
.item-name { font-size: 13px; font-weight: 500; color: var(--color-text-primary); }
.item-badge {
  font-size: 10px; padding: 1px 7px; border-radius: 8px;
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
}
.item-target {
  font-size: 12px; color: var(--color-text-secondary); margin-top: 2px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%;
}
.item-meta { font-size: 11px; color: var(--color-text-disabled); margin-top: 4px; display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
.meta-next { color: var(--color-accent); }
.meta-last { display: inline-flex; align-items: center; gap: 3px; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.meta-last.ok { color: var(--color-text-muted); }
.meta-last.fail { color: var(--color-danger); }

.item-actions { display: flex; align-items: center; gap: 3px; flex-shrink: 0; }
.switch {
  width: 34px; height: 19px; border-radius: 10px; border: none; cursor: pointer;
  background: var(--color-bg-tertiary); box-shadow: inset 0 0 0 1px var(--color-border);
  position: relative; padding: 0; margin-right: 4px; transition: background var(--transition-fast);
}
.switch.on { background: var(--color-accent); box-shadow: none; }
.knob {
  position: absolute; top: 2px; left: 2px; width: 15px; height: 15px; border-radius: 50%;
  background: #fff; transition: transform var(--transition-fast);
}
.switch.on .knob { transform: translateX(15px); }
.act {
  width: 28px; height: 28px; display: flex; align-items: center; justify-content: center;
  border: none; background: transparent; color: var(--color-text-disabled);
  border-radius: var(--radius-sm); cursor: pointer; transition: all var(--transition-fast);
}
.act:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-active); }
.act:disabled { opacity: 0.4; cursor: default; }
.act.danger:hover { color: var(--color-danger); background: rgba(232, 76, 76, 0.1); }

/* 弹窗 */
.modal-mask {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.5); display: flex;
  align-items: center; justify-content: center; z-index: 100;
}
.modal {
  width: 440px; max-height: 90vh; overflow-y: auto; background: var(--color-surface);
  border-radius: var(--radius-lg); padding: var(--space-5); box-shadow: var(--shadow-lg);
}
.modal h3 { margin: 0 0 var(--space-3); font-size: 15px; color: var(--color-text-primary); }
.modal label { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--color-text-muted); margin: var(--space-3) 0 var(--space-1); }
.modal-input {
  width: 100%; box-sizing: border-box; height: 34px; padding: 0 10px; background: var(--color-bg-tertiary);
  border: 1px solid var(--color-border); border-radius: var(--radius-md);
  color: var(--color-text-primary); font-size: 13px; font-family: inherit; outline: none;
}
.modal-input:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
.modal-area { height: auto; padding: 8px 10px; resize: vertical; line-height: 1.5; }
.modal-grid { display: grid; grid-template-columns: 1fr 1fr; gap: var(--space-3); }
.modal-grid > * { min-width: 0; }

/* 分段选择 */
.seg { display: flex; flex-wrap: wrap; gap: 6px; }
.seg-btn {
  display: inline-flex; align-items: center; gap: 4px; padding: 6px 10px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: all var(--transition-fast);
}
.seg-btn:hover { color: var(--color-text-primary); }
.seg-btn.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }

.wd-row { display: flex; gap: 5px; }
.wd-btn {
  width: 34px; height: 32px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 12px;
  cursor: pointer; font-family: inherit; transition: all var(--transition-fast);
}
.wd-btn:hover { color: var(--color-text-primary); }
.wd-btn.active { background: var(--color-accent-bg); color: var(--color-accent); border-color: var(--color-accent-border); }

.toggle-row { display: flex; gap: var(--space-5); margin-top: var(--space-4); }
.toggle-label { display: flex; align-items: center; gap: 6px; font-size: 12px; color: var(--color-text-secondary); cursor: pointer; margin: 0; }
.toggle-label input { cursor: pointer; }

.modal-actions { display: flex; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-5); }
.btn-ghost { padding: 6px 14px; border: 1px solid var(--color-border); background: transparent; color: var(--color-text-secondary); border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-ghost:hover { background: var(--color-bg-hover); }
.btn-primary { padding: 6px 14px; border: none; background: var(--color-accent); color: #fff; border-radius: var(--radius-md); font-size: 13px; cursor: pointer; font-family: inherit; }
.btn-primary:hover:not(:disabled) { background: var(--color-accent-hover); }
.btn-primary:disabled { opacity: 0.5; cursor: default; }
</style>
