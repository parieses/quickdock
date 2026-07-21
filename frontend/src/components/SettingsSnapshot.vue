<script setup lang="ts">
import { ref, watch, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { HardDrive, RotateCcw, Trash2, Plus } from '@lucide/vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { CreateSnapshot, ListSnapshots, DeleteSnapshot, RestoreSnapshot } from '../../bindings/quickdock/services/appservice'
import type { Snapshot, ToastAPI } from '../types'
import { useWorkspaceStore } from '../stores/workspace'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!
const store = useWorkspaceStore()
const emit = defineEmits<{ close: [] }>()
const props = defineProps<{ visible: boolean }>()

const snapshots = ref<Snapshot[]>([])
const snapshotLabel = ref('')
const snapshotNote = ref('')
const showCreateSnapshotForm = ref(false)
const snapshotMsg = ref('')
const snapshotMsgTimer = ref<ReturnType<typeof setTimeout> | null>(null)

function showSnapshotMsg(msg: string, duration = 3000) {
  if (snapshotMsgTimer.value) clearTimeout(snapshotMsgTimer.value)
  snapshotMsg.value = msg
  snapshotMsgTimer.value = setTimeout(() => { snapshotMsg.value = ''; snapshotMsgTimer.value = null }, duration)
}

async function loadSnapshots() {
  try {
    const result = unwrap(await ListSnapshots())
    snapshots.value = result ?? []
  } catch (e) {
    showSnapshotMsg(t('snapshotRestoreFailed') + ': ' + getErrorMessage(e))
  }
}

watch(() => props.visible, (v) => { if (v) loadSnapshots() }, { immediate: true })

async function handleCreateSnapshot() {
  try {
    unwrap(await CreateSnapshot(snapshotLabel.value, snapshotNote.value))
    snapshotLabel.value = ''
    snapshotNote.value = ''
    showCreateSnapshotForm.value = false
    showSnapshotMsg(t('snapshotCreated'))
    await loadSnapshots()
  } catch (e) {
    showSnapshotMsg(t('snapshotCreateFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleRestoreSnapshot(id: string) {
  if (!(await toast.confirm(t('confirmRestore')))) return
  try {
    unwrap(await RestoreSnapshot(id))
    showSnapshotMsg(t('restoreSuccess'))
    setTimeout(async () => { emit('close'); await store.initialize() }, 800)
  } catch (e) {
    showSnapshotMsg(t('snapshotRestoreFailed') + ': ' + getErrorMessage(e))
  }
}

async function handleDeleteSnapshot(id: string) {
  if (!(await toast.confirm(t('confirmDeleteSnapshot')))) return
  try {
    unwrap(await DeleteSnapshot(id))
    showSnapshotMsg(t('snapshotDeleted'))
    await loadSnapshots()
  } catch (e) {
    showSnapshotMsg(t('snapshotRestoreFailed') + ': ' + getErrorMessage(e))
  }
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return bytes + ' B'
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB'
  return (bytes / (1024 * 1024)).toFixed(1) + ' MB'
}

function formatDate(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  const pad = (n: number) => n.toString().padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`
}
</script>

<template>
  <div class="section">
    <h3 class="section-title">{{ t('snapshot') }}</h3>
    <p class="section-desc">{{ t('snapshotDesc') }}</p>

    <div class="snapshot-create-area">
      <button v-if="!showCreateSnapshotForm" class="btn btn-primary" @click="showCreateSnapshotForm = true">
        <Plus :size="14" /> {{ t('createSnapshot') }}
      </button>
      <div v-else class="snapshot-create-form">
        <input v-model="snapshotLabel" class="snapshot-input" :placeholder="t('snapshotLabelPlaceholder')" />
        <input v-model="snapshotNote" class="snapshot-input" :placeholder="t('snapshotNotePlaceholder')" />
        <div class="snapshot-create-actions">
          <button class="btn btn-primary" @click="handleCreateSnapshot">{{ t('create') }}</button>
          <button class="btn btn-secondary" @click="showCreateSnapshotForm = false; snapshotLabel = ''; snapshotNote = ''">{{ t('cancel') }}</button>
        </div>
      </div>
    </div>

    <p v-if="snapshotMsg" class="result-hint">{{ snapshotMsg }}</p>

    <div v-if="snapshots.length === 0" class="snapshot-empty">
      <HardDrive :size="36" class="empty-icon" />
      <p class="empty-text">{{ t('emptySnapshots') }}</p>
      <p class="empty-hint">{{ t('createFirstSnapshot') }}</p>
    </div>
    <div v-else class="snapshot-list">
      <div v-for="s in snapshots" :key="s.id" class="snapshot-item">
        <div class="snapshot-item-info">
          <span class="snapshot-item-label">{{ s.label || t('snapshot') }}</span>
          <span v-if="s.note" class="snapshot-item-note">{{ s.note }}</span>
          <span class="snapshot-item-meta">{{ formatDate(s.created_at) }} · {{ formatSize(s.size) }}</span>
        </div>
        <div class="snapshot-item-actions">
          <button class="action-btn restore-btn" @click="handleRestoreSnapshot(s.id)" :title="t('restoreSnapshot')">
            <RotateCcw :size="14" />
          </button>
          <button class="action-btn danger" @click="handleDeleteSnapshot(s.id)" :title="t('deleteSnapshot')">
            <Trash2 :size="14" />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
