<script setup lang="ts">
/**
 * SSH 端口转发抽屉面板。
 *
 * 与 AiJobsPanel 同款：绝对定位浮层挂在 SSH 页上，不占侧栏、不顶掉会话舞台，
 * 不做模态遮罩。定位上下文由外层 .ssh-standalone（撑满整窗）提供，SshPage
 * 无需改任何样式即可挂载。
 *
 * 规则清单/运行状态来自后端 ListSshForwards；面板打开时每 3s 轮询刷新流量/状态，
 * 同时订阅 quickdock:ssh:forward 事件做即时合并，关闭即停轮询、卸载即 off()。
 * 请求序号 reqSeq 防止快速开关时在途的旧响应覆盖新列表。
 *
 * 所有写操作统一 unwrap + try/catch + toast.error(getErrorMessage(e)) + logErr，
 * 失败不静默吞掉。所有用户可见文案走 t() 且只用约定的 key。
 */
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Events } from '@wailsio/runtime'
import { Network, X, Plus, RefreshCw, Pencil, Trash2, Play, Square } from '@lucide/vue'
import {
  ListHosts,
  ListSshForwards,
  SaveSshForward,
  DeleteSshForward,
  StartSshForward,
  StopSshForward,
} from '../../bindings/quickdock/services/sshx/sshservice'
import type { SshForwardInput } from '../../bindings/quickdock/services/sshx/models'
import { formatBytes } from '../utils/ssh'
import type { SshForwardView, SshHost } from '../utils/ssh'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useToast } from '../composables/useToast'
import { logErr } from '../utils/logger'

const props = defineProps<{
  /** 抽屉是否打开；关闭时停轮询 */
  open: boolean
  /** 主机清单（SshPage 的 ListHosts 结果），供表单下拉；为空时面板自行回源 */
  hosts?: SshHost[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
}>()

const { t } = useI18n()
const { error: toastError, success: toastSuccess } = useToast()

const forwards = ref<SshForwardView[]>([])
/** props.hosts 为空时的兜底：面板自己调一次 ListHosts()，同样只取 !isFolder */
const fallbackHosts = ref<SshHost[]>([])
const loading = ref(false)
const errMsg = ref('')

// —— 新建 / 编辑表单 ——
const formVisible = ref(false)
const editingId = ref('')
const saving = ref(false)
const formErr = ref('')
/** 非回环监听地址的二次确认态（组件内态，不用 confirm()） */
const lanPending = ref(false)
const form = reactive<SshForwardInput>({
  id: '',
  hostId: '',
  kind: 'local',
  bindHost: '127.0.0.1',
  bindPort: 0,
  targetHost: '',
  targetPort: 0,
  autoStart: false,
})

// —— 行内删除二次确认态（组件内态，不用 confirm()）——
const deleteId = ref('')

const panelEl = ref<HTMLElement | null>(null)

/** 下拉只取 !isFolder；优先用 SshPage 传入的（随主机清单刷新），否则用兜底 */
const hostOptions = computed<SshHost[]>(() => {
  const src = props.hosts && props.hosts.length ? props.hosts : fallbackHosts.value
  return src.filter((h) => !h.isFolder)
})
const runningCount = computed(
  () => forwards.value.filter((v) => v.status === 'running').length,
)

/** 状态/类型文案走静态映射：i18n 检查脚本只认字面量 key。 */
const KIND_LABEL = computed<Record<string, string>>(() => ({
  local: t('sshForwardKindLocal'),
  remote: t('sshForwardKindRemote'),
  socks: t('sshForwardKindSocks'),
}))
const STATUS_LABEL = computed<Record<string, string>>(() => ({
  stopped: t('sshForwardStatusStopped'),
  starting: t('sshForwardStatusStarting'),
  running: t('sshForwardStatusRunning'),
  retrying: t('sshForwardStatusRetrying'),
  error: t('sshForwardStatusError'),
}))

/** local 正向箭头、remote 反向箭头、socks 只有监听端（无目标）。 */
function addr(v: SshForwardView): string {
  const bind = `${v.bindHost}:${v.bindPort}`
  if (v.kind === 'socks') return bind
  const target = `${v.targetHost}:${v.targetPort}`
  return v.kind === 'remote' ? `${bind} ← ${target}` : `${bind} → ${target}`
}

/** 回环地址白名单：只有这三个是「只在本机监听」，其余都要二次确认。 */
function isLoopback(h: string): boolean {
  const s = (h || '').trim().toLowerCase()
  return s === '127.0.0.1' || s === 'localhost' || s === '::1'
}

/** 停止/出错才能启动，其余状态（含 starting/running/retrying）只能停止。 */
function canStart(v: SshForwardView): boolean {
  return v.status === 'stopped' || v.status === 'error'
}

/** 统一失败范式：toast.error(getErrorMessage(e)) + logErr，并返回可读文案。 */
function handleErr(e: unknown): string {
  const msg = getErrorMessage(e)
  toastError(msg)
  logErr('sshForward', e)
  return msg
}

// —— 轮询 & 请求序号 ——
let pollTimer: ReturnType<typeof setInterval> | undefined
/** 单调递增：快速开关时旧响应不能覆盖新列表。 */
let reqSeq = 0

function stopPoll() {
  if (pollTimer) {
    clearInterval(pollTimer)
    pollTimer = undefined
  }
}

async function load() {
  const seq = ++reqSeq
  loading.value = true
  try {
    const list = unwrap<SshForwardView[]>(await ListSshForwards()) || []
    if (seq !== reqSeq) return
    forwards.value = list
    errMsg.value = ''
  } catch (e) {
    if (seq !== reqSeq) return
    errMsg.value = handleErr(e)
  } finally {
    if (seq === reqSeq) loading.value = false
  }
}

async function loadHosts() {
  try {
    fallbackHosts.value = unwrap<SshHost[]>(await ListHosts()) || []
  } catch (e) {
    handleErr(e)
    fallbackHosts.value = []
  }
}

// —— 表单 ——
function closeForm() {
  formVisible.value = false
  lanPending.value = false
  saving.value = false
  formErr.value = ''
  editingId.value = ''
}

function openForm(v: SshForwardView | null) {
  formErr.value = ''
  lanPending.value = false
  deleteId.value = ''
  if (v) {
    editingId.value = v.id
    Object.assign(form, {
      id: v.id,
      hostId: v.hostId,
      kind: v.kind,
      bindHost: v.bindHost,
      bindPort: v.bindPort,
      targetHost: v.targetHost,
      targetPort: v.targetPort,
      autoStart: v.autoStart,
    })
  } else {
    editingId.value = ''
    Object.assign(form, {
      id: '',
      hostId: '',
      kind: 'local',
      bindHost: '127.0.0.1',
      bindPort: 0,
      targetHost: '',
      targetPort: 0,
      autoStart: false,
    })
  }
  formVisible.value = true
  if (!props.hosts || !props.hosts.length) void loadHosts()
}

/** 端口 1..65535 + 必填校验；socks 不校验目标两项。 */
function validate(): boolean {
  const hostId = String(form.hostId || '').trim()
  const bindHost = String(form.bindHost || '').trim()
  const bindPort = Number(form.bindPort)
  if (!hostId || !bindHost || !Number.isInteger(bindPort) || bindPort < 1 || bindPort > 65535) {
    return false
  }
  if (form.kind !== 'socks') {
    const targetHost = String(form.targetHost || '').trim()
    const targetPort = Number(form.targetPort)
    if (!targetHost || !Number.isInteger(targetPort) || targetPort < 1 || targetPort > 65535) {
      return false
    }
  }
  return true
}

function save() {
  formErr.value = ''
  if (!validate()) {
    formErr.value = t('sshForwardError')
    return
  }
  // 监听地址改成非回环（对局域网/公网开放）前必须二次确认
  if (!isLoopback(String(form.bindHost || ''))) {
    lanPending.value = true
    return
  }
  void persist()
}

async function persist() {
  lanPending.value = false
  saving.value = true
  try {
    unwrap(await SaveSshForward({ ...form }))
    closeForm()
    await load()
  } catch (e) {
    formErr.value = handleErr(e)
  } finally {
    if (formVisible.value) saving.value = false
  }
}

async function doStart(v: SshForwardView) {
  try {
    unwrap(await StartSshForward(v.id))
    await load()
  } catch (e) {
    handleErr(e)
  }
}

async function doStop(v: SshForwardView) {
  try {
    unwrap(await StopSshForward(v.id))
    await load()
  } catch (e) {
    handleErr(e)
  }
}

async function doDelete(v: SshForwardView) {
  try {
    unwrap(await DeleteSshForward(v.id))
    deleteId.value = ''
    await load()
  } catch (e) {
    handleErr(e)
  }
}

// —— 事件：后端推一条 SshForwardView，合并更新对应 id 那条 ——
let offForward: (() => void) | null = null

// —— 关闭：点击外部 / Esc ——
function onDocMouseDown(e: MouseEvent) {
  if (!props.open) return
  if (panelEl.value && panelEl.value.contains(e.target as Node)) return
  emit('close')
}

/** Esc：一次只收一层（LAN 确认 → 删除确认 → 表单 → 抽屉）。
 *  中文输入法组词（isComposing / keyCode 229）放行，回车确认候选不被误判关闭。 */
function onKeydown(e: KeyboardEvent) {
  if (e.key !== 'Escape') return
  if (e.isComposing || e.keyCode === 229) return
  if (!props.open) return
  if (lanPending.value) { lanPending.value = false; return }
  if (deleteId.value) { deleteId.value = ''; return }
  if (formVisible.value) { closeForm(); return }
  emit('close')
}

watch(
  () => props.open,
  (open) => {
    if (open) {
      void load()
      stopPoll()
      pollTimer = setInterval(() => void load(), 3000)
    } else {
      stopPoll()
      reqSeq++ // 让在途响应失效，快速开关时不覆盖新列表
      closeForm()
      deleteId.value = ''
      errMsg.value = ''
    }
  },
  { immediate: true },
)

onMounted(() => {
  offForward = Events.On('quickdock:ssh:forward', (ev: unknown) => {
    const v = (ev as { data?: SshForwardView })?.data
    if (!v || !v.id) return
    const i = forwards.value.findIndex((x) => x.id === v.id)
    if (i >= 0) forwards.value[i] = { ...forwards.value[i], ...v }
    else forwards.value.push(v)
    if (v.reconnected === true) toastSuccess(t('sshForwardReconnected'))
  })
  // 冒泡阶段：SshPage 工具条按钮用 @mousedown.stop 挡掉，避免「点开又被自己关掉」
  document.addEventListener('mousedown', onDocMouseDown)
  window.addEventListener('keydown', onKeydown)
})

onBeforeUnmount(() => {
  offForward?.()
  offForward = null
  stopPoll()
  document.removeEventListener('mousedown', onDocMouseDown)
  window.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div v-if="open" ref="panelEl" class="sfp" role="dialog">
    <header class="sfp-head">
      <Network :size="14" class="sfp-head-icon" />
      <span class="sfp-title">{{ t('sshForwardTitle') }}</span>
      <span v-if="runningCount" class="sfp-count">{{ runningCount }}</span>
      <span class="sfp-head-spacer" />
      <button class="sfp-icon" :title="t('refresh')" @click="load()">
        <RefreshCw :size="13" :class="{ spinning: loading }" />
      </button>
      <button class="sfp-icon" :title="t('sshForwardNew')" @click="openForm(null)">
        <Plus :size="14" />
      </button>
      <button class="sfp-icon" :title="t('close')" @click="emit('close')">
        <X :size="14" />
      </button>
    </header>

    <!-- 新建 / 编辑表单 -->
    <div v-if="formVisible" class="sfp-form">
      <div class="sfp-form-title">{{ editingId ? t('sshForwardEdit') : t('sshForwardNew') }}</div>

      <label class="sfp-field">
        <span class="sfp-label">{{ t('sshForwardHost') }}</span>
        <select v-model="form.hostId" class="sfp-input">
          <option :value="''" disabled>{{ t('sshForwardHost') }}</option>
          <option v-for="h in hostOptions" :key="h.id" :value="h.id">{{ h.name || h.host }}</option>
        </select>
      </label>

      <label class="sfp-field">
        <span class="sfp-label">{{ t('sshForwardKind') }}</span>
        <select v-model="form.kind" class="sfp-input">
          <option value="local">{{ t('sshForwardKindLocal') }}</option>
          <option value="remote">{{ t('sshForwardKindRemote') }}</option>
          <option value="socks">{{ t('sshForwardKindSocks') }}</option>
        </select>
      </label>

      <div class="sfp-grid">
        <label class="sfp-field">
          <span class="sfp-label">{{ t('sshForwardBindHost') }}</span>
          <input v-model="form.bindHost" class="sfp-input" spellcheck="false" />
        </label>
        <label class="sfp-field">
          <span class="sfp-label">{{ t('sshForwardBindPort') }}</span>
          <input v-model.number="form.bindPort" type="number" min="1" max="65535" class="sfp-input" />
        </label>
      </div>

      <div v-if="form.kind !== 'socks'" class="sfp-grid">
        <label class="sfp-field">
          <span class="sfp-label">{{ t('sshForwardTargetHost') }}</span>
          <input v-model="form.targetHost" class="sfp-input" spellcheck="false" />
        </label>
        <label class="sfp-field">
          <span class="sfp-label">{{ t('sshForwardTargetPort') }}</span>
          <input v-model.number="form.targetPort" type="number" min="1" max="65535" class="sfp-input" />
        </label>
      </div>

      <label class="sfp-check">
        <input type="checkbox" v-model="form.autoStart" />
        <span>{{ t('sshForwardAutoStart') }}</span>
      </label>

      <p v-if="formErr" class="sfp-err sfp-form-err">{{ formErr }}</p>

      <!-- 非回环监听地址：组件内二次确认态 -->
      <div v-if="lanPending" class="sfp-lan">
        <p class="sfp-lan-warn">{{ t('sshForwardLanWarn') }}</p>
        <div class="sfp-foot">
          <button class="sfp-btn" @click="lanPending = false">{{ t('sshForwardCancel') }}</button>
          <button class="sfp-btn sfp-btn--danger" @click="persist()">{{ t('sshForwardLanConfirm') }}</button>
        </div>
      </div>
      <div v-else class="sfp-foot">
        <button class="sfp-btn" @click="closeForm()">{{ t('sshForwardCancel') }}</button>
        <button class="sfp-btn sfp-btn--primary" :disabled="saving" @click="save()">{{ t('sshForwardSave') }}</button>
      </div>
    </div>

    <!-- 转发列表 -->
    <div v-else class="sfp-list">
      <div v-for="v in forwards" :key="v.id" class="sfp-row" :class="`is-${v.status}`">
        <div class="sfp-row-top">
          <span class="sfp-dot" />
          <span class="sfp-host" :title="v.hostName">{{ v.hostName || v.hostId }}</span>
          <span class="sfp-kind">{{ KIND_LABEL[v.kind] || v.kind }}</span>
          <span class="sfp-status">{{ STATUS_LABEL[v.status] || v.status }}</span>
        </div>
        <div class="sfp-line">
          <span class="sfp-k">{{ t('sshForwardAddr') }}</span>
          <span class="sfp-addr">{{ addr(v) }}</span>
        </div>
        <div class="sfp-meta">
          <span>{{ t('sshForwardTraffic') }} ↓{{ formatBytes(v.bytesIn) }} ↑{{ formatBytes(v.bytesOut) }}</span>
          <span>{{ t('sshForwardConnections') }} {{ v.connections }}</span>
          <span v-if="v.active" class="sfp-active">{{ t('sshForwardActive') }}</span>
        </div>
        <div v-if="v.error" class="sfp-err">
          <span class="sfp-k">{{ t('sshForwardError') }}</span> {{ v.error }}
        </div>
        <div class="sfp-acts">
          <template v-if="deleteId === v.id">
            <span class="sfp-confirm">{{ t('sshForwardDeleteConfirm') }}</span>
            <button class="sfp-act" @click="deleteId = ''">{{ t('sshForwardCancel') }}</button>
            <button class="sfp-act is-danger" @click="doDelete(v)">{{ t('sshForwardDelete') }}</button>
          </template>
          <template v-else>
            <button v-if="canStart(v)" class="sfp-act" @click="doStart(v)">
              <Play :size="11" /> {{ t('sshForwardStart') }}
            </button>
            <button v-else class="sfp-act" @click="doStop(v)">
              <Square :size="11" /> {{ t('sshForwardStop') }}
            </button>
            <button class="sfp-act" @click="openForm(v)">
              <Pencil :size="11" /> {{ t('sshForwardEdit') }}
            </button>
            <button class="sfp-act is-danger" @click="deleteId = v.id">
              <Trash2 :size="11" /> {{ t('sshForwardDelete') }}
            </button>
          </template>
        </div>
      </div>

      <div v-if="!forwards.length && !loading" class="sfp-empty">{{ t('sshForwardEmpty') }}</div>
      <div v-if="errMsg" class="sfp-empty is-err">{{ errMsg }}</div>
    </div>
  </div>
</template>

<style scoped>
.sfp {
  position: absolute;
  top: 52px;
  right: 16px;
  z-index: 20;
  width: 420px;
  max-width: calc(100% - 32px);
  max-height: 70vh;
  display: flex;
  flex-direction: column;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
  font-family: var(--font-family);
  font-size: var(--text-sm);
  color: var(--color-text-primary);
}

.sfp-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex: 0 0 auto;
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--color-border);
}
.sfp-head-icon { flex: 0 0 auto; color: var(--color-accent); }
.sfp-title { font-size: var(--text-sm); font-weight: 600; color: var(--color-text-secondary); }
.sfp-count {
  display: inline-flex; align-items: center; justify-content: center;
  min-width: 15px; height: 15px; padding: 0 var(--space-1);
  border-radius: var(--radius-full);
  background: var(--color-accent-bg); color: var(--color-accent);
  font-size: var(--text-2xs); font-variant-numeric: tabular-nums;
}
.sfp-head-spacer { flex: 1 1 auto; }
.sfp-icon {
  display: flex; align-items: center; justify-content: center;
  flex: 0 0 auto; padding: var(--space-1);
  color: var(--color-text-muted); background: transparent; border: none;
  border-radius: var(--radius-xs); cursor: pointer;
}
.sfp-icon:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }

.spinning { animation: sfp-spin 900ms linear infinite; }
@keyframes sfp-spin { to { transform: rotate(360deg); } }

.sfp-list { flex: 1 1 auto; min-height: 0; overflow-y: auto; padding: var(--space-2); }

.sfp-row { padding: var(--space-2) var(--space-2) var(--space-3); border-radius: var(--radius-sm); }
.sfp-row:hover { background: var(--color-bg-hover); }

.sfp-row-top { display: flex; align-items: center; gap: var(--space-2); }
.sfp-dot { flex: 0 0 auto; width: 6px; height: 6px; border-radius: 50%; background: var(--color-text-disabled); }
.sfp-row.is-running .sfp-dot { background: var(--color-success); animation: sfp-pulse 1.6s ease-in-out infinite; }
.sfp-row.is-starting .sfp-dot, .sfp-row.is-retrying .sfp-dot { background: var(--color-warning); }
.sfp-row.is-error .sfp-dot { background: var(--color-danger); }
@keyframes sfp-pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.3; } }

.sfp-host {
  flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  font-size: var(--text-sm); color: var(--color-text-primary);
}
.sfp-kind {
  flex: 0 0 auto; padding: 0 var(--space-2); border-radius: var(--radius-xs);
  background: var(--color-bg-tertiary); color: var(--color-text-muted); font-size: var(--text-2xs);
}
.sfp-status { flex: 0 0 auto; font-size: var(--text-2xs); color: var(--color-text-muted); }
.sfp-row.is-running .sfp-status { color: var(--color-success); }
.sfp-row.is-error .sfp-status { color: var(--color-danger); }
.sfp-row.is-retrying .sfp-status, .sfp-row.is-starting .sfp-status { color: var(--color-warning); }

.sfp-line { display: flex; align-items: baseline; gap: var(--space-2); margin-top: var(--space-1); }
.sfp-k { flex: 0 0 auto; font-size: var(--text-2xs); color: var(--color-text-disabled); }
.sfp-addr { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--color-text-secondary); word-break: break-all; }

.sfp-meta {
  display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3);
  margin-top: var(--space-1); font-size: var(--text-2xs);
  color: var(--color-text-muted); font-variant-numeric: tabular-nums;
}
.sfp-active {
  padding: 0 var(--space-2); border-radius: var(--radius-xs);
  background: var(--color-success-bg); color: var(--color-success);
}
.sfp-err { margin-top: var(--space-1); font-size: var(--text-2xs); color: var(--color-danger); word-break: break-word; }

.sfp-acts { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-1); margin-top: var(--space-2); }
.sfp-act {
  display: inline-flex; align-items: center; gap: var(--space-1);
  padding: 1px var(--space-2); border: none; border-radius: var(--radius-xs);
  background: transparent; color: var(--color-text-muted);
  font: inherit; font-size: var(--text-xs); cursor: pointer; white-space: nowrap;
}
.sfp-act:hover { color: var(--color-accent); background: var(--color-bg-active); }
.sfp-act.is-danger:hover { color: var(--color-danger); background: var(--color-danger-bg); }
.sfp-confirm { flex: 1 1 auto; font-size: var(--text-2xs); color: var(--color-warning); }

.sfp-empty { padding: var(--space-6) var(--space-3); text-align: center; font-size: var(--text-xs); color: var(--color-text-disabled); }
.sfp-empty.is-err { color: var(--color-danger); }

/* —— 表单 —— */
.sfp-form { flex: 1 1 auto; min-height: 0; overflow-y: auto; padding: var(--space-4); }
.sfp-form-title { margin-bottom: var(--space-4); font-size: var(--text-sm); font-weight: 600; color: var(--color-text-primary); }
.sfp-field { display: block; margin-bottom: var(--space-3); }
.sfp-label { display: block; margin-bottom: var(--space-1); font-size: var(--text-2xs); color: var(--color-text-muted); }
.sfp-input {
  width: 100%; box-sizing: border-box;
  padding: var(--space-2) var(--space-3);
  font: inherit; font-size: var(--text-xs);
  color: var(--color-text-primary); background: var(--color-bg-secondary);
  border: 1px solid var(--color-border); border-radius: var(--radius-xs); outline: none;
}
.sfp-input:focus { border-color: var(--color-border-focus-soft); }
.sfp-grid { display: flex; gap: var(--space-3); }
.sfp-grid .sfp-field { flex: 1 1 0; min-width: 0; }
.sfp-check { display: inline-flex; align-items: center; gap: var(--space-2); font-size: var(--text-xs); color: var(--color-text-secondary); cursor: pointer; }
.sfp-form-err { margin: var(--space-2) 0 0; }
.sfp-lan {
  margin-top: var(--space-3); padding: var(--space-3);
  border: 1px solid var(--color-warning-border); border-radius: var(--radius-sm);
  background: var(--color-warning-bg);
}
.sfp-lan-warn { margin: 0 0 var(--space-3); font-size: var(--text-xs); line-height: 1.5; color: var(--color-warning); }
.sfp-foot { display: flex; justify-content: flex-end; gap: var(--space-2); margin-top: var(--space-4); }
.sfp-btn {display: inline-flex; align-items: center; justify-content: center; gap: 4px; 
  height: 28px; padding: 0 11px;
  font: inherit; font-size: 12px;
  color: var(--color-text-secondary); background: transparent;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm); cursor: pointer;
}
.sfp-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.sfp-btn--primary { color: var(--color-accent-text); background: var(--color-accent); border-color: var(--color-accent); }
.sfp-btn--primary:hover { color: var(--color-accent-text); background: var(--color-accent-hover); }
.sfp-btn--primary:disabled { opacity: 0.5; cursor: default; }
.sfp-btn--danger { color: var(--color-danger-text); background: var(--color-danger); border-color: var(--color-danger); }
</style>
