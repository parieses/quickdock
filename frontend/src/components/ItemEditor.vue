<script setup lang="ts">
import { ref, watch, computed, toRef } from 'vue';
import { useI18n } from 'vue-i18n';
import { useWorkspaceStore } from '../stores/workspace'
import { useFocusTrap } from '../utils/focusTrap'
import type { CollectionItem, OpenTool } from '../types';

const { t, tm } = useI18n()

const props = defineProps<{
  item?: CollectionItem | null;
  visible: boolean;
}>();

const store = useWorkspaceStore()

const emit = defineEmits<{
  (e: 'save', item: Partial<CollectionItem>): void;
  (e: 'cancel'): void;
}>();

const name = ref('');
const type = ref('目录');
const value = ref('');
const remark = ref('');
const toolId = ref('');
const workingDirectory = ref('');
const nameError = ref(false);
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

const availableTools = computed<OpenTool[]>(() => store.getToolsForType(type.value))

const toolOptions = computed(() => {
  const list = availableTools.value
  if (list.length === 0) {
    return [{ id: '', name: t('systemDefault') }]
  }
  return [{ id: '', name: t('systemDefault') }, ...list.map(t => ({ id: t.id, name: `${t.name}（${t.type}）` }))]
})

const itemTypeOptions = computed(() => {
  const types = ['目录', '网页', '命令', '文件', '应用', '快速链接']
  const labels = tm('itemTypes') as Record<string, string>
  return types.map(v => ({ label: labels[v] || v, value: v }))
})

watch(() => [props.visible, props.item], ([v]) => {
  if (v) {
    name.value = props.item?.name ?? '';
    type.value = props.item?.type ?? '目录';
    value.value = props.item?.value ?? '';
    remark.value = props.item?.remark ?? '';
    toolId.value = props.item?.toolId ?? '';
    workingDirectory.value = props.item?.workingDirectory ?? '';
    nameError.value = false;
    if (!props.item && !toolId.value) {
      const suggested = store.getDefaultToolForType(type.value)
      if (suggested) toolId.value = suggested.id
    }
  }
});

watch(type, (newType) => {
  if (!props.item) {
    if (newType === '快速链接' && !value.value.includes('{query}')) {
      // 自动添加 {query} 占位符示例
      value.value = 'https://' + value.value + '{query}'
    }
    const suggested = store.getDefaultToolForType(newType)
    if (suggested) toolId.value = suggested.id
  }
})

function handleSave() {
  const trimmed = name.value.trim()
  if (!trimmed) {
    nameError.value = true
    return
  }
  nameError.value = false
  emit('save', {
    name: trimmed,
    type: type.value,
    value: value.value,
    remark: remark.value,
    toolId: toolId.value,
    workingDirectory: workingDirectory.value,
  });
}

function handleCancel() {
  emit('cancel');
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') { handleCancel(); return }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="visible" class="editor-overlay" @mousedown.self="handleCancel" @keydown="onKeydown">
        <div ref="panelRef" class="editor-panel" @mousedown.stop>
          <div class="editor-header">
            <span class="editor-title">{{ item ? t('itemEditorTitle') : t('itemEditorNewTitle') }}</span>
            <button class="editor-close" @click="handleCancel">
              <span class="close-icon">&times;</span>
            </button>
          </div>
          <div class="editor-body">
            <label class="field">
              <span class="field-label">{{ t('itemName') }}</span>
              <input v-model="name" type="text" :placeholder="t('itemName')" class="field-input" :class="{ 'input-error': nameError }" @keydown.enter="handleSave" />
              <p v-if="nameError" class="field-error">{{ t('emptyName') }}</p>
            </label>
            <label class="field">
              <span class="field-label">{{ t('itemType') }}</span>
              <select v-model="type" class="field-input">
                <option v-for="opt in itemTypeOptions" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
              </select>
            </label>
            <label class="field">
              <span class="field-label">{{ t('itemValue') }}</span>
              <input v-model="value" type="text" :placeholder="type === '网页' ? 'https://...' : type === '快速链接' ? t('quicklinkUrlPlaceholder') : t('itemValue')" class="field-input" />
              <p class="field-hint">{{ type === '快速链接' ? t('quicklinkDesc') : t('itemValueHint') }}</p>
            </label>
            <label class="field">
              <span class="field-label">{{ t('workingDir') }}</span>
              <input v-model="workingDirectory" type="text" :placeholder="t('workingDir')" class="field-input" />
            </label>
            <label class="field">
              <span class="field-label">{{ t('openTool') }}</span>
              <select v-model="toolId" class="field-input">
                <option v-for="opt in toolOptions" :key="opt.id" :value="opt.id">{{ opt.name }}</option>
              </select>
            </label>
            <label class="field">
              <span class="field-label">{{ t('remark') }}</span>
              <textarea v-model="remark" class="field-textarea" :placeholder="t('remarkPlaceholder')"></textarea>
            </label>
          </div>
          <div class="editor-footer">
            <button class="btn btn-cancel" @click="handleCancel">{{ t('cancel') }}</button>
            <button class="btn btn-primary" @click="handleSave">{{ item ? t('save') : t('create') }}</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.editor-overlay {
  position: fixed; inset: 0; z-index: 9000;
  background: var(--color-bg-overlay);
  display: flex; align-items: center; justify-content: center;
}
.editor-panel {
  width: 480px; max-height: 80vh;
  background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 12px;
  display: flex; flex-direction: column; overflow: hidden;
  box-shadow: 0 12px 40px var(--color-bg-overlay);
}
.editor-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 16px 20px; border-bottom: 1px solid var(--color-border);
}
.editor-title { font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.editor-close {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  width: 28px; height: 28px; border-radius: 6px;
  display: flex; align-items: center; justify-content: center; font-size: 18px;
}
.editor-close:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.editor-body {
  flex: 1; overflow-y: auto; padding: 20px;
  display: flex; flex-direction: column; gap: 16px;
}
.field { display: flex; flex-direction: column; gap: 6px; }
.field-label { font-size: 12px; font-weight: 600; color: var(--color-text-muted); }
.field-input {
  padding: 8px 12px; border: 1px solid var(--color-border); border-radius: 6px;
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; outline: none; transition: border-color 0.12s;
}
.field-input:focus { border-color: var(--color-accent); }
.field-input.input-error { border-color: var(--color-danger); }
.field-input::placeholder { color: var(--color-text-disabled); }
.field-textarea {
  resize: vertical; min-height: 60px;
  padding: 8px 12px; border: 1px solid var(--color-border); border-radius: 6px;
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; outline: none;
}
.field-textarea:focus { border-color: var(--color-accent); }
.field-error { font-size: 11px; color: var(--color-danger); margin-top: 2px; }
.field-hint { font-size: 11px; color: var(--color-text-disabled); margin-top: 2px; }
.editor-footer {
  display: flex; justify-content: flex-end; gap: 8px;
  padding: 14px 20px; border-top: 1px solid var(--color-border);
}
.btn {
  padding: 8px 18px; border: none; border-radius: 6px; font-size: 13px;
  cursor: pointer; font-family: inherit; transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s;
}
.btn-cancel { background: var(--color-bg-active); color: var(--color-text-muted); }
.btn-cancel:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.btn-primary { background: var(--color-accent); color: var(--color-accent-text); }
.btn-primary:hover { background: var(--color-accent-hover); }

.dialog-enter-active, .dialog-leave-active { transition: opacity 0.15s; }
.dialog-enter-from, .dialog-leave-to { opacity: 0; }
</style>
