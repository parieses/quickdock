<script setup lang="ts">
import { ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { EnvList } from '../../bindings/quickdock/services/env/environmentservice'
import {
  SceneEnvList,
  SceneEnvSave,
  SceneEnvApply,
} from '../../bindings/quickdock/services/scene/sceneservice'
import { unwrap } from '../utils/api'
import { useFocusTrap } from '../utils/focusTrap'

interface SceneEnvEntry {
  runtime: string
  version: string
}
interface ApplyResult {
  runtime: string
  version: string
  running: boolean
  error?: string
}
interface Row {
  id: string
  name: string
  versions: string[]
  checked: boolean
  version: string
  enabled: boolean
}

const props = defineProps<{ visible: boolean; sceneId: string; sceneName: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()

const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

const rows = ref<Row[]>([])
const loading = ref(false)
const busy = ref(false)
const results = ref<ApplyResult[]>([])
const errorMsg = ref('')

async function load() {
  loading.value = true
  errorMsg.value = ''
  results.value = []
  try {
    const rts = unwrap<any[]>(await EnvList()) ?? []
    const bound = unwrap<SceneEnvEntry[]>(await SceneEnvList(props.sceneId)) ?? []
    const boundMap: Record<string, string> = {}
    for (const b of bound) boundMap[b.runtime] = b.version || ''
    rows.value = rts
      .filter((r: any) => r.hasService)
      .map((r: any) => ({
        id: r.id,
        name: r.name,
        versions: ((r.installed ?? []) as any[]).map((i: any) => i.version),
        checked: boundMap[r.id] !== undefined,
        version: boundMap[r.id] ?? '',
        enabled: !!r.enabled,
      }))
  } catch (e: any) {
    errorMsg.value = e?.message ?? String(e)
  } finally {
    loading.value = false
  }
}

watch(() => props.visible, v => { if (v) load() })

function currentEntries(): SceneEnvEntry[] {
  return rows.value.filter(r => r.checked).map(r => ({ runtime: r.id, version: r.version }))
}

async function save(): Promise<boolean> {
  try {
    unwrap(await SceneEnvSave(props.sceneId, JSON.stringify(currentEntries())))
    return true
  } catch (e: any) {
    errorMsg.value = e?.message ?? String(e)
    return false
  }
}

async function onSave() {
  busy.value = true
  errorMsg.value = ''
  try { if (await save()) emit('close') } finally { busy.value = false }
}

// 先落盘再应用：避免应用的是界面上尚未保存的勾选（用户点「应用」时理应包含当前改动）
async function onApply() {
  busy.value = true
  errorMsg.value = ''
  results.value = []
  try {
    if (await save()) {
      results.value = unwrap<ApplyResult[]>(await SceneEnvApply(props.sceneId)) ?? []
    }
  } catch (e: any) {
    errorMsg.value = e?.message ?? String(e)
  } finally {
    busy.value = false
  }
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') { emit('close'); return }
  onKeydownTrap(e)
}

function labelOf(id: string): string {
  return rows.value.find(r => r.id === id)?.name ?? id
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="visible" class="overlay" @mousedown.self="emit('close')" @keydown="onKeydown">
        <div ref="panelRef" class="panel" @mousedown.stop>
          <h3 class="title">{{ t('sceneEnvTitle') }} · {{ sceneName }}</h3>
          <p class="hint">{{ t('sceneEnvDesc') }}</p>

          <div v-if="loading" class="state">{{ t('loading') }}…</div>
          <div v-else-if="rows.length === 0" class="state">{{ t('sceneEnvNoService') }}</div>
          <div v-else class="list">
            <div v-for="row in rows" :key="row.id" class="row">
              <label class="row-main">
                <input v-model="row.checked" type="checkbox" />
                <span class="row-name">{{ row.name }}</span>
                <span v-if="row.enabled" class="tag">{{ t('sceneEnvAlwaysOn') }}</span>
              </label>
              <select v-model="row.version" :disabled="!row.checked" class="version-select">
                <option value="">{{ t('sceneEnvFollowActive') }}</option>
                <option v-for="v in row.versions" :key="v" :value="v">{{ v }}</option>
              </select>
            </div>
          </div>

          <div v-if="results.length" class="results">
            <div v-for="r in results" :key="r.runtime" class="result">
              <span class="result-name">{{ labelOf(r.runtime) }}</span>
              <span :class="['badge', r.error ? 'bad' : (r.running ? 'ok' : 'warn')]">
                {{ r.error ? r.error : (r.running ? t('sceneEnvRunning') : t('sceneEnvStopped')) }}
              </span>
            </div>
          </div>

          <p v-if="errorMsg" class="error">{{ errorMsg }}</p>

          <div class="footer">
            <button class="btn btn-cancel" :disabled="busy" @click="emit('close')">{{ t('cancel') }}</button>
            <button class="btn btn-cancel" :disabled="busy || loading" @click="onApply">{{ t('sceneEnvApply') }}</button>
            <button class="btn btn-primary" :disabled="busy || loading" @click="onSave">{{ t('save') }}</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.overlay {
  position: fixed; inset: 0; z-index: 20000;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
  display: flex; align-items: center; justify-content: center;
}
.panel {
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: 10px; width: 420px; max-width: 92vw;
  padding: 20px 22px;
  box-shadow: 0 12px 48px var(--color-bg-overlay);
}
.title { margin: 0 0 8px; font-size: 15px; font-weight: 500; color: var(--color-text-primary); }
.hint { margin: 0 0 14px; font-size: 12px; line-height: 1.6; color: var(--color-text-muted); }

.list { max-height: 320px; overflow-y: auto; display: flex; flex-direction: column; gap: 2px; }
.row { display: flex; align-items: center; gap: 10px; padding: 6px 8px; border-radius: 6px; }
.row:hover { background: var(--color-bg-hover); }
.row-main { display: flex; align-items: center; gap: 8px; flex: 1; cursor: pointer; min-width: 0; }
.row-name { font-size: 13px; color: var(--color-text-secondary); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tag { font-size: 10px; padding: 1px 5px; border-radius: 4px; background: var(--color-bg-active); color: var(--color-text-muted); flex-shrink: 0; }
.version-select {
  flex-shrink: 0; max-width: 130px; padding: 4px 6px; font-size: 12px; font-family: inherit;
  background: var(--color-bg-secondary); color: var(--color-text-secondary);
  border: 1px solid var(--color-border); border-radius: 5px;
}
.version-select:disabled { opacity: 0.45; }

.state { padding: 20px 0; text-align: center; font-size: 13px; color: var(--color-text-muted); }

.results { margin-top: 12px; display: flex; flex-direction: column; gap: 4px; }
.result { display: flex; align-items: center; justify-content: space-between; gap: 10px; font-size: 12px; }
.result-name { color: var(--color-text-secondary); }
.badge { padding: 1px 6px; border-radius: 4px; font-size: 11px; }
.badge.ok { color: var(--color-success); }
.badge.bad { color: var(--color-danger); }
.badge.warn { color: var(--color-warning); }

.error { margin: 12px 0 0; font-size: 12px; color: var(--color-danger); }

.footer { display: flex; justify-content: flex-end; gap: 8px; margin-top: 18px; }
.btn {
  padding: 7px 18px; border-radius: 6px; font-size: 13px; font-weight: 500;
  cursor: pointer; border: none; font-family: inherit;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.btn:disabled { opacity: 0.5; cursor: default; }
.btn-cancel { background: var(--color-bg-active); color: var(--color-text-muted); }
.btn-cancel:hover:not(:disabled) { color: var(--color-text-primary); }
.btn-primary { background: var(--color-accent); color: var(--color-accent-text); }
.btn-primary:hover:not(:disabled) { opacity: 0.85; }

.dialog-enter-active { transition: opacity 0.2s ease-out, transform 0.2s ease-out; }
.dialog-leave-active { transition: opacity 0.15s ease-in; }
.dialog-enter-from, .dialog-leave-to { opacity: 0; }
.dialog-enter-from .panel { transform: scale(0.95) translateY(-8px); }
.dialog-leave-to .panel { transform: scale(0.95); }
</style>
