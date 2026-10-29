<script lang="ts" setup>
import { computed } from 'vue'
import { X, Download, Clock, Ban } from '@lucide/vue'
import { useI18n } from 'vue-i18n'
import { parseReleaseNotes } from '../utils/releaseNotes'

const { t } = useI18n()

const props = defineProps<{
  visible: boolean
  version: string
  currentVersion: string
  releaseNotes: string
}>()

const emit = defineEmits<{
  view: []
  later: []
  skip: []
}>()

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') emit('later')
}

// CI 生成的 release notes 是 `git log --pretty=format="- %s"` 的逐行要点，
// 解析成紧凑列表渲染；无要点（纯段落文本）时回退为 <pre> 原样展示。
const noteItems = computed(() => parseReleaseNotes(props.releaseNotes))
</script>

<template>
  <Teleport to="body">
    <Transition name="up-dialog">
      <div
        v-if="visible"
        class="up-overlay"
        @mousedown.self="emit('later')"
        @keydown="onKey"
      >
        <div class="up-panel" tabindex="-1">
          <div class="up-header">
            <div class="up-title">{{ t('updateAvailable') }} {{ version }}</div>
            <button class="up-close" :title="t('updateLater')" @click="emit('later')">
              <X :size="16" />
            </button>
          </div>
          <div class="up-body">
            <p class="up-sub">{{ t('updateCurrentToNew', { from: currentVersion, to: version }) }}</p>
            <div v-if="releaseNotes" class="up-notes">
              <div class="up-notes-title">{{ t('updateNotes') }}</div>
              <ul v-if="noteItems.length" class="up-notes-list">
                <li v-for="(item, i) in noteItems" :key="i">{{ item }}</li>
              </ul>
              <pre v-else class="up-notes-body">{{ releaseNotes }}</pre>
            </div>
          </div>
          <div class="up-footer">
            <button class="btn btn-primary" @click="emit('view')">
              <Download :size="14" /> {{ t('updateView') }}
            </button>
            <button class="btn btn-secondary" @click="emit('later')">
              <Clock :size="14" /> {{ t('updateLater') }}
            </button>
            <button class="btn btn-ghost" @click="emit('skip')">
              <Ban :size="14" /> {{ t('updateSkip') }}
            </button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.up-overlay {
  position: fixed;
  inset: 0;
  z-index: 10002;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
}
.up-panel {
  width: 460px;
  max-width: calc(100vw - 32px);
  max-height: calc(100vh - 48px);
  display: flex;
  flex-direction: column;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: 14px;
  box-shadow: var(--shadow-toast);
  overflow: hidden;
}
.up-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 16px 18px;
  border-bottom: 1px solid var(--color-border);
}
.up-title {
  font-size: 16px;
  font-weight: 600;
  color: var(--color-text-primary);
}
.up-close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border: none;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
}
.up-close:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}
.up-body {
  padding: 16px 18px;
  overflow-y: auto;
}
.up-sub {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--color-text-secondary);
}
.up-notes {
  border: 1px solid var(--color-border);
  border-radius: 10px;
  overflow: hidden;
}
.up-notes-title {
  padding: 7px 12px;
  font-size: 11px;
  font-weight: 600;
  color: var(--color-text-secondary);
  background: var(--color-bg-active);
  letter-spacing: 0.02em;
}
.up-notes-list {
  margin: 0;
  padding: 8px 12px 8px 14px;
  max-height: 220px;
  overflow-y: auto;
  list-style: none;
}
.up-notes-list li {
  position: relative;
  font-size: 12px;
  line-height: 1.55;
  color: var(--color-text-primary);
  word-break: break-word;
}
.up-notes-list li + li {
  margin-top: 5px;
}
.up-notes-list li::before {
  content: '';
  position: absolute;
  left: -8px;
  top: 8px;
  width: 4px;
  height: 4px;
  border-radius: 50%;
  background: var(--color-text-muted);
}
.up-notes-body {
  margin: 0;
  padding: 10px 12px;
  max-height: 220px;
  overflow-y: auto;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
  color: var(--color-text-primary);
  white-space: pre-wrap;
  word-break: break-word;
}
.up-footer {
  display: flex;
  gap: 8px;
  padding: 14px 18px;
  border-top: 1px solid var(--color-border);
}
.up-footer .btn {
  flex: 1;
}
.up-dialog-enter-active,
.up-dialog-leave-active {
  transition: opacity 0.18s ease;
}
.up-dialog-enter-from,
.up-dialog-leave-to {
  opacity: 0;
}
.up-dialog-enter-active .up-panel,
.up-dialog-leave-active .up-panel {
  transition: transform 0.18s ease;
}
.up-dialog-enter-from .up-panel,
.up-dialog-leave-to .up-panel {
  transform: scale(0.96) translateY(-6px);
}
</style>
