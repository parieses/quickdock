<script setup lang="ts">
import { computed, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { GripVertical, Eye, EyeOff, RotateCcw, ListOrdered } from '@lucide/vue'
import { useNavPrefs } from '../composables/useNavPrefs'
import { useDragSort } from '../composables/useDragSort'
import type { ToastAPI } from '../types'

const { t } = useI18n()
const props = defineProps<{ visible: boolean }>()

// 侧栏与这里共用同一份偏好数据源，改完立即在侧栏生效（无需重启）
const { orderedNav, hiddenNav, hideNav, showNav, showAllNav, reorderNav, resetNav } = useNavPrefs()

const toast = inject<ToastAPI>('toast')

const hiddenCount = computed(() => hiddenNav.value.length)

function isHidden(id: string) {
  return hiddenNav.value.some((e) => e.id === id)
}

function toggle(entry: { id: string; labelKey: string }) {
  if (isHidden(entry.id)) {
    showNav(entry.id)
    return
  }
  if (!hideNav(entry.id)) {
    toast?.error?.(t('navKeepOne'))
    return
  }
}

// ---- 拖拽排序（管道在 useDragSort，与场景栏共用一份）----
// 顺序按全量有序列表（含隐藏项）：隐藏项保留自己的位置，恢复时原地回归。
const { dragId, dragOverIndex, onDragStart, onDragOver, onDrop, onDragEnd } = useDragSort({
  ids: () => orderedNav.value.map((e) => e.id),
  onReorder: (ids) => { reorderNav(ids) },
})

function resetAll() {
  const changed = resetNav()
  if (changed) toast?.success?.(t('navResetDone'))
}

function restoreAll() {
  showAllNav()
  toast?.success?.(t('navRestoreAllDone'))
}
</script>

<template>
  <div v-if="props.visible" class="snv">
    <div class="snv-head">
      <ListOrdered :size="15" />
      <div>
        <p class="snv-title">{{ t('navSettingsTitle') }}</p>
        <p class="snv-sub">{{ t('navSettingsDesc') }}</p>
      </div>
    </div>

    <ul class="snv-list">
      <li
        v-for="(entry, i) in orderedNav"
        :key="entry.id"
        draggable="true"
        :class="['snv-row', { hidden: isHidden(entry.id), dragging: dragId === entry.id, 'drop-before': dragOverIndex === i && dragId && dragId !== entry.id }]"
        @dragstart="onDragStart(entry.id, $event, i)"
        @dragover="onDragOver(i, $event)"
        @drop="onDrop(entry.id, $event)"
        @dragend="onDragEnd"
      >
        <GripVertical :size="14" class="snv-grip" :title="t('navDragHint')" />
        <component :is="entry.icon" :size="14" class="snv-icon" />
        <span class="snv-name">{{ t(entry.labelKey) }}</span>
        <button
          class="snv-eye"
          :title="isHidden(entry.id) ? t('navRestore') : t('navHide')"
          @click="toggle(entry)"
        >
          <Eye v-if="!isHidden(entry.id)" :size="15" />
          <EyeOff v-else :size="15" />
        </button>
      </li>
    </ul>

    <p class="snv-hint">{{ t('navDragHint') }}</p>

    <div class="snv-actions">
      <button class="snv-btn" :disabled="hiddenCount === 0" @click="restoreAll()">
        <RotateCcw :size="13" />{{ t('navRestoreAll') }}
      </button>
      <button class="snv-btn" @click="resetAll()">{{ t('navResetOrder') }}</button>
    </div>

    <p class="snv-foot">{{ t('navSettingsFallback') }}</p>
  </div>
</template>

<style scoped>
.snv { display: flex; flex-direction: column; gap: 14px; }
.snv-head { display: flex; align-items: flex-start; gap: 10px; color: var(--color-accent); }
.snv-title { margin: 0; font-size: 13px; font-weight: 500; color: var(--color-text-primary); }
.snv-sub { margin: 2px 0 0; font-size: 12px; color: var(--color-text-secondary); }

.snv-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 2px; }
.snv-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 7px 8px;
  border-radius: var(--radius-md);
  background: var(--color-bg-tertiary);
  cursor: grab;
  transition: background-color var(--transition-fast), opacity var(--transition-fast), box-shadow var(--transition-fast);
}
.snv-row:hover { background: var(--color-bg-hover); }
.snv-row.dragging { opacity: 0.45; }
/* 落点指示线：用 box-shadow inset 画，不加额外 DOM */
.snv-row.drop-before { box-shadow: inset 0 2px 0 0 var(--color-accent); }
.snv-row.hidden { opacity: 0.55; }
.snv-row.hidden .snv-name { text-decoration: line-through; }

.snv-grip { color: var(--color-text-disabled); flex-shrink: 0; cursor: grab; }
.snv-icon { color: var(--color-text-secondary); flex-shrink: 0; }
.snv-name { flex: 1; font-size: 13px; color: var(--color-text-primary); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.snv-eye {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 26px;
  border: none;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.snv-eye:hover { color: var(--color-accent); background: var(--color-bg-active); }
.snv-row.hidden .snv-eye { color: var(--color-text-disabled); }

.snv-hint { margin: 0; font-size: 12px; color: var(--color-text-disabled); }
.snv-actions { display: flex; gap: 8px; }
.snv-btn {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 12px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12.5px;
  font-family: inherit;
  cursor: pointer;
  border-radius: var(--radius-md);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.snv-btn:hover:not(:disabled) { color: var(--color-text-primary); background: var(--color-bg-hover); }
.snv-btn:disabled { opacity: 0.45; cursor: default; }
.snv-foot { margin: 0; font-size: 12px; color: var(--color-text-disabled); }
.snv-link {
  border: none;
  background: transparent;
  color: var(--color-accent);
  font-size: 12px;
  font-family: inherit;
  cursor: pointer;
  padding: 0;
}
</style>
