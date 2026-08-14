<script setup lang="ts">
import { ref, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { RotateCcw, Trash2 } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import {
  GetSyncConfig,
  SetSyncConfig,
  SyncListBackends,
  SyncTestConnection,
  SyncExportBackup,
  SyncListBackups,
  SyncDownloadAndRestore,
  SyncDeleteBackup,
} from '../../bindings/quickdock/services/appservice'
import type { ToastAPI } from '../types'
import { useWorkspaceStore } from '../stores/workspace'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const store = useWorkspaceStore()

const emit = defineEmits<{ close: [] }>()
const props = defineProps<{ visible: boolean }>()

// 后端列表（来自后端的 AvailableBackends，新增后端会自动出现在下拉框）
const backends = ref<{ type: string; name: string; desc: string }[]>([])
const syncType = ref('webdav')

// WebDAV 表单（作为同步后端的一种实现）
const webdavURL = ref('')
const webdavUser = ref('')
const webdavPass = ref('')

const msg = ref('')
const timer = ref<ReturnType<typeof setTimeout> | null>(null)
const backups = ref<{ name: string; size: number; time: string }[]>([])
const loading = ref(false)

function showMsg(m: string, duration = 4000) {
  if (timer.value !== null) clearTimeout(timer.value)
  msg.value = m
  timer.value = setTimeout(() => { msg.value = ''; timer.value = null }, duration)
}

async function loadBackends() {
  try {
    const list = unwrap<{ type: string; name: string; desc: string }[]>(await SyncListBackends())
    backends.value = list ?? []
  } catch { backends.value = [] }
}

async function loadConfig() {
  try {
    const data = unwrap<{
      type: string
      webdav: { url: string; username: string; password: string }
    }>(await GetSyncConfig())
    if (data) {
      syncType.value = data.type || 'webdav'
      webdavURL.value = data.webdav?.url || ''
      webdavUser.value = data.webdav?.username || ''
      webdavPass.value = data.webdav?.password || ''
    }
  } catch { /* 无配置时静默 */ }
}

async function saveConfig() {
  try {
    unwrap(await SetSyncConfig({
      type: syncType.value,
      webdav: { url: webdavURL.value, username: webdavUser.value, password: webdavPass.value },
    }))
    showMsg('✅ ' + t('saved'))
  } catch (e) { showMsg('❌ ' + t('saveFailed2') + ': ' + getErrorMessage(e)) }
}

async function testConnection() {
  if (!webdavURL.value.trim()) {
    showMsg('❌ ' + t('webdavUrl') + ' 不能为空')
    return
  }
  loading.value = true
  try {
    await saveConfig()
    unwrap(await SyncTestConnection())
    showMsg('✅ ' + t('webdavTestSuccess'))
  } catch (e) { showMsg('❌ ' + t('webdavTestFailed') + ': ' + getErrorMessage(e)) }
  finally { loading.value = false }
}

async function createBackup() {
  loading.value = true
  try {
    unwrap(await SyncExportBackup())
    showMsg('✅ ' + t('backupCreated'))
    await listBackups()
  } catch (e) { showMsg('❌ ' + getErrorMessage(e)) }
  finally { loading.value = false }
}

async function listBackups() {
  try {
    const list = unwrap<{ name: string; size: number; time: string }[]>(await SyncListBackups())
    backups.value = list ?? []
  } catch { backups.value = [] }
}

async function restoreBackup(name: string) {
  if (!(await toast.confirm(t('confirmRestore')))) return
  loading.value = true
  try {
    unwrap(await SyncDownloadAndRestore(name))
    showMsg('✅ ' + t('restoreSuccess'))
    setTimeout(async () => { emit('close'); await store.initialize() }, 800)
  } catch (e) { showMsg('❌ ' + t('snapshotRestoreFailed') + ': ' + getErrorMessage(e)) }
  finally { loading.value = false }
}

async function deleteBackup(name: string) {
  if (!(await toast.confirm(t('confirmDelete')))) return
  try {
    unwrap(await SyncDeleteBackup(name))
    showMsg(t('deleted'))
    await listBackups()
  } catch (e) { showMsg('❌ ' + getErrorMessage(e)) }
}

watch(() => props.visible, (v) => {
  if (v) { loadBackends(); loadConfig(); listBackups() }
}, { immediate: true })
</script>

<template>
  <div class="section">
    <h3 class="section-title">{{ t('sync') }}</h3>
    <p class="section-desc">{{ t('syncDesc') }}</p>

    <!-- 后端选择器：当前只有 WebDAV，未来 Git / 对象存储只需在后端注册即出现 -->
    <div class="field">
      <span class="field-label">{{ t('syncBackend') }}</span>
      <select v-model="syncType" class="field-input" :disabled="loading">
        <option v-for="b in backends" :key="b.type" :value="b.type">{{ b.name }} — {{ b.desc }}</option>
      </select>
    </div>

    <!-- WebDAV 配置（同步后端的一种实现） -->
    <div v-if="syncType === 'webdav'" class="webdav-form">
      <label class="field">
        <span class="field-label">URL</span>
        <input v-model="webdavURL" type="text" class="field-input" placeholder="https://example.com/remote.php/dav/" />
      </label>
      <label class="field">
        <span class="field-label">{{ t('webdavUsername') }}</span>
        <input v-model="webdavUser" type="text" class="field-input" />
      </label>
      <label class="field">
        <span class="field-label">{{ t('webdavPassword') }}</span>
        <input v-model="webdavPass" type="password" class="field-input" />
      </label>
      <div class="webdav-actions">
        <button class="btn btn-primary" @click="saveConfig" :disabled="loading">{{ t('save') }}</button>
        <button class="btn btn-secondary" @click="testConnection" :disabled="loading">{{ t('webdavTest') }}</button>
        <button class="btn btn-secondary" @click="createBackup" :disabled="loading">{{ t('syncCreateBackup') }}</button>
      </div>
    </div>

    <p v-if="msg" class="result-hint">{{ msg }}</p>

    <div v-if="syncType === 'webdav' && backups.length > 0" class="webdav-backup-list">
      <h4>{{ t('webdavBackups') }}</h4>
      <div v-for="b in backups" :key="b.name" class="snapshot-item">
        <div class="snapshot-item-info">
          <span class="snapshot-item-label">{{ b.name }}</span>
          <span class="snapshot-item-meta">
            {{ b.size ? Math.round(b.size / 1024) + ' KB' : '' }}
            <template v-if="b.time"> · {{ b.time }}</template>
          </span>
        </div>
        <div class="snapshot-item-actions">
          <button class="action-btn" @click="restoreBackup(b.name)" :disabled="loading" :title="t('restore')">
            <RotateCcw :size="14" />
          </button>
          <button class="action-btn danger" @click="deleteBackup(b.name)" :disabled="loading" :title="t('delete')">
            <Trash2 :size="14" />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
