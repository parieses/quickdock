<script setup lang="ts">
import { ref, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { RotateCcw, Trash2 } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { GetWebDAVConfig, SetWebDAVConfig, WebDAVTestConnection, WebDAVExportBackup, WebDAVListBackups, WebDAVDownaloadAndRestore, WebDAVDeleteBackup } from '../../bindings/quickdock/services/appservice'
import type { ToastAPI } from '../types'
import { useWorkspaceStore } from '../stores/workspace'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const store = useWorkspaceStore()

const emit = defineEmits<{ close: [] }>()
const props = defineProps<{ visible: boolean }>()

const webdavURL = ref('')
const webdavUser = ref('')
const webdavPass = ref('')
const webdavMsg = ref('')
const webdavTimer = ref<ReturnType<typeof setTimeout> | null>(null)
const webdavBackups = ref<{ name: string; size: number; time: string }[]>([])
const webdavLoading = ref(false)

function showWebdavMsg(msg: string, duration = 4000) {
  if (webdavTimer.value !== null) clearTimeout(webdavTimer.value)
  webdavMsg.value = msg
  webdavTimer.value = setTimeout(() => { webdavMsg.value = ''; webdavTimer.value = null }, duration)
}

async function loadWebDAVConfig() {
  try {
    const data = unwrap<{ url: string; username: string; password: string }>(await GetWebDAVConfig())
    if (data) { webdavURL.value = data.url || ''; webdavUser.value = data.username || ''; webdavPass.value = data.password || '' }
  } catch { /* 初始化无配置时静默 */ }
}

async function saveWebDAVConfig() {
  try {
    unwrap(await SetWebDAVConfig({ url: webdavURL.value, username: webdavUser.value, password: webdavPass.value }))
    showWebdavMsg('✅ ' + t('saved'))
  } catch (e) { showWebdavMsg('❌ ' + t('saveFailed2') + ': ' + getErrorMessage(e)) }
}

async function testWebDAVConnection() {
  if (!webdavURL.value.trim()) {
    showWebdavMsg('❌ ' + t('webdavUrl') + ' 不能为空')
    return
  }
  webdavLoading.value = true
  try {
    await saveWebDAVConfig()
    unwrap(await WebDAVTestConnection())
    showWebdavMsg('✅ ' + t('webdavTestSuccess'))
  } catch (e) { showWebdavMsg('❌ ' + t('webdavTestFailed') + ': ' + getErrorMessage(e)) }
  finally { webdavLoading.value = false }
}

async function uploadWebDAVBackup() {
  webdavLoading.value = true
  try {
    unwrap(await WebDAVExportBackup())
    showWebdavMsg('✅ ' + t('backupCreated'))
    await listWebDAVBackups()
  } catch (e) { showWebdavMsg('❌ ' + getErrorMessage(e)) }
  finally { webdavLoading.value = false }
}

async function listWebDAVBackups() {
  try {
    const list = unwrap<{ name: string; size: number; time: string }[]>(await WebDAVListBackups())
    webdavBackups.value = list ?? []
  } catch { webdavBackups.value = [] }
}

async function restoreWebDAVBackup(name: string) {
  if (!(await toast.confirm(t('confirmRestore')))) return
  webdavLoading.value = true
  try {
    unwrap(await WebDAVDownaloadAndRestore(name))
    showWebdavMsg('✅ ' + t('restoreSuccess'))
    setTimeout(async () => { emit('close'); await store.initialize() }, 800)
  } catch (e) { showWebdavMsg('❌ ' + t('snapshotRestoreFailed') + ': ' + getErrorMessage(e)) }
  finally { webdavLoading.value = false }
}

async function deleteWebDAVBackup(name: string) {
  if (!(await toast.confirm(t('confirmDelete')))) return
  try {
    unwrap(await WebDAVDeleteBackup(name))
    showWebdavMsg(t('deleted'))
    await listWebDAVBackups()
  } catch (e) { showWebdavMsg('❌ ' + getErrorMessage(e)) }
}

watch(() => props.visible, (v) => {
  if (v) { loadWebDAVConfig(); listWebDAVBackups() }
}, { immediate: true })
</script>

<template>
  <div class="section">
    <h3 class="section-title">WebDAV</h3>
    <p class="section-desc">{{ t('webdavDesc') || t('webdavDesc') }}</p>

    <div class="webdav-form">
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
        <button class="btn btn-primary" @click="saveWebDAVConfig" :disabled="webdavLoading">{{ t('save') }}</button>
        <button class="btn btn-secondary" @click="testWebDAVConnection" :disabled="webdavLoading">{{ t('webdavTest') }}</button>
        <button class="btn btn-secondary" @click="uploadWebDAVBackup" :disabled="webdavLoading">{{ t('webdavUpload') }}</button>
      </div>
    </div>

    <p v-if="webdavMsg" class="result-hint">{{ webdavMsg }}</p>

    <div v-if="webdavBackups.length > 0" class="webdav-backup-list">
      <h4>{{ t('webdavBackups') }}</h4>
      <div v-for="b in webdavBackups" :key="b.name" class="snapshot-item">
        <div class="snapshot-item-info">
          <span class="snapshot-item-label">{{ b.name }}</span>
          <span class="snapshot-item-meta">
            {{ b.size ? Math.round(b.size / 1024) + ' KB' : '' }}
            <template v-if="b.time"> · {{ b.time }}</template>
          </span>
        </div>
        <div class="snapshot-item-actions">
          <button class="action-btn" @click="restoreWebDAVBackup(b.name)" :disabled="webdavLoading" :title="t('restore')">
            <RotateCcw :size="14" />
          </button>
          <button class="action-btn danger" @click="deleteWebDAVBackup(b.name)" :disabled="webdavLoading" :title="t('delete')">
            <Trash2 :size="14" />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
