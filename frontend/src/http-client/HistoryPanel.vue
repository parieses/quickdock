<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RefreshCw, Trash2, ClipboardCopy, RotateCcw, Upload, X, ChevronDown } from '@lucide/vue'
import {
  ListHttpHistory, DeleteHttpHistory, ClearHttpHistory,
  GetHttpHistoryAsApiRequest, BuildCurlCommand, ImportPostman,
} from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { ToastAPI } from '../types'

interface Props {
  projectId: string
  toast: ToastAPI
}

const props = defineProps<Props>()
const { t } = useI18n()

interface HistoryItem {
  id: string
  projectId: string
  name: string
  method: string
  url: string
  headers: string
  body: string
  bodyType: string
  authType: string
  authToken: string
  authUser: string
  authPass: string
  statusCode: number
  ok: boolean
  durationMs: number
  size: number
  createdTs: number
}

const emit = defineEmits<{
  (e: 'loaded', input: any): void
  (e: 'close'): void
}>()

const items = ref<HistoryItem[]>([])
const loading = ref(false)
const tab = ref<'history' | 'import'>('history')
const importText = ref('')
const importing = ref(false)
const expanded = ref<Record<string, boolean>>({})

async function loadHistory() {
  if (!props.projectId) { items.value = []; return }
  loading.value = true
  try {
    const list = unwrap<HistoryItem[]>(await ListHttpHistory(props.projectId, 100))
    items.value = list ?? []
  } catch (e) {
    props.toast.error(getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

function prettyTime(ts: number): string {
  if (!ts) return ''
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`
}

function methodClass(m: string) {
  return 'm-' + m.toLowerCase()
}

async function replay(id: string) {
  try {
    const [input, err] = (await GetHttpHistoryAsApiRequest(id)) as any
    if (err || !input) { props.toast.error(err || 'load failed'); return }
    emit('loaded', input)
    props.toast.success(t('httpReplay'))
  } catch (e) {
    props.toast.error(getErrorMessage(e))
  }
}

async function copyCurl(item: HistoryItem) {
  const input = {
    projectId: item.projectId, name: item.name, method: item.method, url: item.url,
    headers: item.headers, body: item.body, bodyType: item.bodyType,
    authType: item.authType, authToken: item.authToken, authUser: item.authUser, authPass: item.authPass,
  }
  try {
    const r = unwrap<{ command: string }>(await BuildCurlCommand(input as any))
    if (!r) throw new Error('no curl')
    await navigator.clipboard.writeText(r.command)
    props.toast.success(t('httpCurlCopied'))
  } catch (e) {
    props.toast.error(getErrorMessage(e))
  }
}

async function removeOne(id: string) {
  try {
    await DeleteHttpHistory(id)
    items.value = items.value.filter(x => x.id !== id)
    props.toast.success(t('httpHistoryDeleted'))
  } catch (e) {
    props.toast.error(getErrorMessage(e))
  }
}

async function clearAll() {
  if (!(await props.toast.confirm(t('httpConfirmClearHistory')))) return
  try {
    await ClearHttpHistory(props.projectId)
    items.value = []
    props.toast.success(t('httpHistoryCleared'))
  } catch (e) {
    props.toast.error(getErrorMessage(e))
  }
}

async function doImport() {
  if (!importText.value.trim()) { props.toast.error(t('httpImportEmpty')); return }
  importing.value = true
  try {
    const r = unwrap<{ imported: number; projectId: string; errors: string[] }>(await ImportPostman(importText.value))
    if (!r) throw new Error('no result')
    props.toast.success(t('httpImportSuccess') + r.imported)
    tab.value = 'history'
    await loadHistory()
  } catch (e) {
    props.toast.error(getErrorMessage(e))
  } finally {
    importing.value = false
  }
}

onMounted(() => { loadHistory() })
watch(() => props.projectId, () => { loadHistory() })
onMounted(() => {
  const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') emit('close') }
  window.addEventListener('keydown', onKey)
  onUnmounted(() => window.removeEventListener('keydown', onKey))
})
</script>

<template>
  <Teleport to="body">
    <div class="hp-mask" @click.self="emit('close')">
      <div class="hp-modal">
        <div class="hp-head">
          <span class="hp-title">{{ t('httpHistoryTitle') }}</span>
          <button class="hp-icon" :title="t('close')" @click="emit('close')"><X :size="16" /></button>
        </div>

        <div class="hp-tabs">
          <button :class="['hp-tab', { active: tab === 'history' }]" @click="tab = 'history'">
            {{ t('httpHistoryTab') }}
            <span v-if="items.length" class="hp-badge">{{ items.length }}</span>
          </button>
          <button :class="['hp-tab', { active: tab === 'import' }]" @click="tab = 'import'"> {{ t('httpImportTab') }}</button>
          <div class="hp-spacer" />
          <button class="hp-icon" :title="t('httpHistoryClear')" @click="clearAll"><Trash2 :size="14" /></button>
          <button class="hp-icon" :title="t('httpHistory')" @click="loadHistory"><RefreshCw :size="14" :class="{ spin: loading }" /></button>
        </div>

        <!-- 历史列表 -->
        <div v-if="tab === 'history'" class="hp-body">
          <div v-if="!projectId" class="hp-empty">{{ t('httpHistoryNoProject') }}</div>
          <div v-else-if="!items.length" class="hp-empty">{{ t('httpHistoryEmpty') }}</div>
          <div v-else class="hp-list">
            <div v-for="it in items" :key="it.id" class="hp-item">
              <div class="hp-item-line" @click="expanded[it.id] = !expanded[it.id]">
                <span :class="['hp-method', methodClass(it.method)]">{{ it.method }}</span>
                <span class="hp-url" :title="it.url">{{ it.url }}</span>
                <span :class="['hp-status', it.ok ? 'ok' : 'fail']">{{ it.statusCode ? it.statusCode : (it.ok ? 200 : '-') }}</span>
                <span class="hp-time">{{ prettyTime(it.createdTs) }}</span>
                <ChevronDown :size="13" class="hp-arrow" :class="{ open: expanded[it.id] }" />
              </div>
              <div v-if="expanded[it.id]" class="hp-item-detail">
                <div v-if="it.durationMs" class="hp-meta">{{ t('httpHistoryDuration') }}: {{ it.durationMs }}ms</div>
                <div class="hp-actions">
                  <button class="hp-btn" @click="copyCurl(it)"><ClipboardCopy :size="12" /> {{ t('httpCopyCurl') }}</button>
                  <button class="hp-btn" @click="replay(it.id)"><RotateCcw :size="12" /> {{ t('httpReplay') }}</button>
                  <button class="hp-btn danger" @click="removeOne(it.id)"><X :size="12" /> </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        <!-- Postman 导入 -->
        <div v-else class="hp-body">
          <p class="hp-hint">{{ t('httpImportHint') }}</p>
          <textarea v-model="importText" class="hp-import-area" spellcheck="false" placeholder='{ "info": { "name": "My API" }, "item": [...] }'></textarea>
          <button class="hp-import-btn" :disabled="importing" @click="doImport">
            <Upload :size="13" /> {{ t('httpImportBtn') }}
          </button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
/* 弹窗遮罩 + 面板 */
.hp-mask { position: fixed; inset: 0; z-index: 400; background: rgba(0, 0, 0, 0.45); display: flex; align-items: center; justify-content: center; padding: 24px; }
.hp-modal { width: 640px; max-width: 94vw; max-height: 82vh; display: flex; flex-direction: column; overflow: hidden; background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 12px; box-shadow: var(--shadow-lg); }
.hp-head { display: flex; align-items: center; justify-content: space-between; padding: 12px 16px; border-bottom: 1px solid var(--color-border); }
.hp-title { font-size: 14px; font-weight: 600; color: var(--color-text-primary); }
.hp-tabs { display: flex; align-items: center; gap: 4px; padding: 6px 12px; border-bottom: 1px solid var(--color-border); }
.hp-tab {
  display: flex; align-items: center; gap: 5px; padding: 4px 10px; border: none; background: none;
  color: var(--color-text-muted); font-size: 12px; font-family: inherit; cursor: pointer; border-radius: 5px;
}
.hp-tab:hover { color: var(--color-text-primary); }
.hp-tab.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.hp-badge { font-size: 9px; background: var(--color-accent-bg); color: var(--color-accent); border-radius: 8px; padding: 0 5px; }
.hp-spacer { flex: 1; }
.hp-icon { background: none; border: none; color: var(--color-text-muted); cursor: pointer; padding: 4px; border-radius: 4px; display: flex; }
.hp-icon:hover { color: var(--color-danger); background: var(--color-bg-hover); }
.spin { animation: hp-spin 1s linear infinite; }
@keyframes hp-spin { to { transform: rotate(360deg); } }

.hp-body { flex: 1; overflow-y: auto; padding: 8px 10px; }
.hp-empty { color: var(--color-text-disabled); font-size: 12px; text-align: center; padding: 20px 0; }
.hp-list { display: flex; flex-direction: column; gap: 4px; }
.hp-item { border: 1px solid var(--color-border); border-radius: 6px; overflow: hidden; }
.hp-item-line { display: flex; align-items: center; gap: 8px; padding: 6px 8px; cursor: pointer; }
.hp-item-line:hover { background: var(--color-bg-hover); }
.hp-method { font-size: 10px; font-weight: 700; color: #fff; border-radius: 4px; padding: 1px 5px; flex-shrink: 0; }
.m-get { background: #2e9e5b; }
.m-post { background: #3a8ae0; }
.m-put { background: #d9920a; }
.m-delete { background: #e8584c; }
.m-patch { background: #9b6ddb; }
.m-head { background: #6b7785; }
.m-options { background: #5a8f9e; }
.hp-url { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--color-text-primary); font-size: 12px; }
.hp-status { font-size: 11px; font-weight: 600; flex-shrink: 0; }
.hp-status.ok { color: #2e9e5b; }
.hp-status.fail { color: var(--color-danger); }
.hp-time { font-size: 10px; color: var(--color-text-muted); flex-shrink: 0; }
.hp-arrow { color: var(--color-text-disabled); transition: transform var(--transition-fast); flex-shrink: 0; }
.hp-arrow.open { transform: rotate(180deg); }
.hp-item-detail { padding: 6px 10px 8px; border-top: 1px dashed var(--color-border); }
.hp-meta { font-size: 11px; color: var(--color-text-muted); margin-bottom: 6px; }
.hp-actions { display: flex; gap: 6px; flex-wrap: wrap; }
.hp-btn {
  display: flex; align-items: center; gap: 4px; padding: 4px 9px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-secondary); font-size: 11px;
  font-family: inherit; cursor: pointer; border-radius: 5px;
}
.hp-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.hp-btn.danger { color: var(--color-danger); }
.hp-btn.danger:hover { background: rgba(232, 76, 76, 0.1); }

.hp-hint { font-size: 12px; color: var(--color-text-muted); margin: 0 0 8px; }
.hp-import-area { width: 100%; min-height: 160px; resize: vertical; padding: 8px 10px; border-radius: 6px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-primary); font-size: 12px; font-family: 'Consolas', 'Monaco', monospace; box-sizing: border-box; outline: none; }
.hp-import-area:focus { border-color: var(--color-border-focus); }
.hp-import-btn { display: flex; align-items: center; gap: 5px; margin-top: 8px; padding: 6px 12px; border: none; background: var(--color-accent); color: #fff; border-radius: 6px; cursor: pointer; font-size: 12px; font-family: inherit; }
.hp-import-btn:disabled { opacity: 0.6; cursor: default; }
</style>
