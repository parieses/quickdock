<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Network, RefreshCw, Search, Trash2 } from '@lucide/vue'
import { ListListeningPorts, KillProcess } from '../../bindings/quickdock/services/port/portservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import ConfirmDialog from './ConfirmDialog.vue'

const { t } = useI18n()

interface PortInfo { port: number; protocol: string; pid: number; process: string; path?: string; self?: boolean }

const ports = ref<PortInfo[]>([])
const kw = ref('')
const loading = ref(true)
const error = ref('')
const killPid = ref<number | null>(null)
const showKillConfirm = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | null = null

const filtered = computed(() => {
  const q = kw.value.trim().toLowerCase()
  if (!q) return ports.value
  return ports.value.filter(p =>
    String(p.port).includes(q) ||
    String(p.pid).includes(q) ||
    (p.process || '').toLowerCase().includes(q) ||
    (p.path || '').toLowerCase().includes(q),
  )
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const r = unwrap<PortInfo[]>(await ListListeningPorts()) || []
    ports.value = r
  } catch (e: any) {
    error.value = getErrorMessage(e)
  } finally {
    loading.value = false
  }
}

function askKill(p: PortInfo) {
  killPid.value = p.pid
  showKillConfirm.value = true
}
async function confirmKill() {
  const pid = killPid.value
  showKillConfirm.value = false
  killPid.value = null
  if (pid == null) return
  try {
    await unwrap(await KillProcess(pid))
    await load()
  } catch (e: any) {
    error.value = getErrorMessage(e)
  }
}

onMounted(() => {
  load()
  refreshTimer = setInterval(load, 5000)
})
onUnmounted(() => { if (refreshTimer) clearInterval(refreshTimer) })
</script>

<template>
  <div class="port-page">
    <div class="port-head">
      <div class="port-title-wrap">
        <h2 class="port-title">{{ t('portsTitle') }}</h2>
        <span class="port-sub">{{ t('portsDesc') }}</span>
      </div>
      <div class="port-tools">
        <div class="port-search">
          <Search :size="13" />
          <input v-model="kw" :placeholder="t('portsSearch')" class="port-search-input" />
        </div>
        <button class="port-btn" :class="{ 'is-loading': loading }" :disabled="loading" @click="load">
          <RefreshCw :size="13" class="port-btn-icon" />
          <span v-if="loading && ports.length">{{ t('portsRefreshing') }}</span>
          <span v-else>{{ t('portsRefresh') }}</span>
        </button>
      </div>
    </div>

    <div v-if="error" class="port-error">{{ error }}</div>

    <div class="port-list">
      <div v-if="loading && ports.length === 0" class="port-loading">
        <span class="port-spinner" />
        <p>{{ t('portsLoading') }}</p>
      </div>
      <div v-else-if="!filtered.length" class="port-empty">
        <Network :size="28" class="empty-icon" />
        <p>{{ error ? t('portsLoadFail') : t('portsEmpty') }}</p>
      </div>
      <div
        v-for="p in filtered" :key="p.protocol + ':' + p.port + ':' + p.pid"
        class="port-row" :class="{ 'is-self': p.self }"
      >
        <span class="port-num" :title="t('portsPort')">{{ p.port }}</span>
        <span class="port-proto" :class="'proto-' + (p.protocol || 'tcp')">{{ (p.protocol || 'tcp').toUpperCase() }}</span>
        <span class="port-pid" :title="t('portsPid')">PID {{ p.pid }}</span>
        <span class="port-name" :title="t('portsProcess')">
          {{ p.process || '—' }}
          <span v-if="p.self" class="port-self-badge">{{ t('portsSelf') }}</span>
        </span>
        <span class="port-path" :title="t('portsPath') + '：' + (p.path || t('portsPathUnknown'))">{{ p.path || '—' }}</span>
        <button class="port-kill" @click="askKill(p)" :title="t('portsKill')">
          <Trash2 :size="13" /> {{ t('portsKill') }}
        </button>
      </div>
    </div>

    <ConfirmDialog
      :visible="showKillConfirm"
      :message="t('portsKillConfirm', { pid: killPid ?? '' })"
      @confirm="confirmKill"
      @cancel="showKillConfirm = false"
    />
  </div>
</template>

<style scoped>
.port-page { display: flex; flex-direction: column; height: 100%; padding: var(--space-6) var(--space-8); overflow: hidden; }
.port-head { display: flex; align-items: flex-end; justify-content: space-between; margin-bottom: var(--space-5); flex-shrink: 0; gap: var(--space-4); }
.port-title-wrap { display: flex; align-items: baseline; gap: var(--space-3); }
.port-title { font-size: 18px; font-weight: 600; color: var(--color-text-primary); margin: 0; }
.port-sub { font-size: 12px; color: var(--color-text-disabled); }
.port-tools { display: flex; align-items: center; gap: var(--space-3); }
.port-search { display: flex; align-items: center; gap: 5px; padding: 5px 9px; border-radius: 7px; background: var(--color-bg-secondary); color: var(--color-text-disabled); min-width: 200px; }
.port-search-input { flex: 1; border: none; outline: none; background: transparent; color: var(--color-text-primary); font-size: 13px; }
.port-btn { display: flex; align-items: center; gap: 4px; font-size: 12px; padding: 5px 10px; border-radius: 7px; cursor: pointer; border: 1px solid var(--color-border); background: var(--color-bg-secondary); color: var(--color-text-primary); }
.port-btn:disabled { opacity: 0.5; cursor: default; }
.port-btn.is-loading .port-btn-icon { animation: port-spin 0.8s linear infinite; }
@keyframes port-spin { to { transform: rotate(360deg); } }

.port-loading { display: flex; flex-direction: column; align-items: center; justify-content: center; gap: var(--space-3); padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.port-spinner { width: 26px; height: 26px; border-radius: 50%; border: 2.5px solid var(--color-border); border-top-color: var(--color-accent); animation: port-spin 0.8s linear infinite; }

.port-error { margin-bottom: var(--space-4); padding: 8px 12px; font-size: 12px; color: var(--color-danger); background: rgba(232, 76, 76, 0.1); border: 1px solid rgba(232, 76, 76, 0.3); border-radius: var(--radius-md); flex-shrink: 0; }

.port-list { flex: 1; overflow-y: auto; display: flex; flex-direction: column; gap: 4px; }
.port-empty { text-align: center; padding: var(--space-9) var(--space-4); color: var(--color-text-disabled); }
.empty-icon { opacity: 0.4; margin-bottom: var(--space-2); }
.port-row { display: flex; align-items: center; gap: var(--space-3); padding: 9px 12px; background: var(--color-bg-secondary); box-shadow: inset 0 0 0 1px var(--color-border); border-radius: var(--radius-md); transition: background var(--transition-fast); }
.port-row:hover { background: var(--color-bg-hover); }
/* QuickDock 自身监听的端口（内置 HTTP 静态服务等）：描边高亮，避免混在系统进程里认不出 */
.port-row.is-self { box-shadow: inset 0 0 0 1px var(--color-accent); background: rgba(74, 158, 255, 0.07); }
.port-row.is-self:hover { background: rgba(74, 158, 255, 0.13); }
.port-self-badge { margin-left: 6px; font-size: 10px; font-weight: 600; padding: 1px 5px; border-radius: 4px; background: var(--color-accent); color: #fff; white-space: nowrap; }
.port-num { font-size: 14px; font-weight: 600; color: var(--color-text-primary); font-variant-numeric: tabular-nums; min-width: 64px; }
.port-proto { font-size: 10px; font-weight: 700; padding: 1px 6px; border-radius: 4px; background: var(--color-bg-tertiary); color: var(--color-text-muted); min-width: 38px; text-align: center; }
.port-proto.proto-udp { color: #e0a92b; }
.port-pid { font-size: 11px; color: var(--color-text-disabled); font-variant-numeric: tabular-nums; }
.port-name { flex: 1; min-width: 0; font-size: 13px; color: var(--color-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.port-path { flex: 1.6; min-width: 0; font-size: 12px; color: var(--color-text-muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-variant-numeric: tabular-nums; }
.port-kill { display: flex; align-items: center; gap: 4px; font-size: 12px; padding: 4px 9px; border-radius: 7px; cursor: pointer; border: 1px solid var(--color-border); background: transparent; color: var(--color-danger); transition: background var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast); }
.port-kill:hover { background: rgba(232, 76, 76, 0.1); border-color: rgba(232, 76, 76, 0.35); }
</style>
