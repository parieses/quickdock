<script setup lang="ts">
import { reactive, ref, watch, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'

const { t } = useI18n()

export interface CreateField {
  key: string
  label: string
  type: 'text' | 'select'
  options?: { label: string; value: string }[]
  placeholder?: string
  default?: string
}

const props = defineProps<{
  visible: boolean
  title: string
  fields: CreateField[]
  editValues?: Record<string, string>
}>()

const emit = defineEmits<{
  (e: 'confirm', values: Record<string, string>): void
  (e: 'cancel'): void
}>()

const values = reactive<Record<string, string>>({})
const validationMessage = ref('')
const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

// 根据 visible / editValues / fields 推导初始值
function resetValues() {
  const map: Record<string, string> = {}
  for (const f of props.fields) {
    if (props.editValues && props.editValues[f.key] !== undefined) {
      map[f.key] = props.editValues[f.key]
    } else if (f.default !== undefined) {
      map[f.key] = f.default
    } else {
      map[f.key] = f.type === 'select' ? (f.options?.[0]?.value ?? '') : ''
    }
  }
  // reactive 原地替换
  for (const k of Object.keys(values)) delete values[k]
  Object.assign(values, map)
  validationMessage.value = ''
}

// 弹窗打开时重置表单
watch(() => [props.visible, props.editValues], ([v]) => {
  if (v) resetValues()
})

function onConfirm() {
  // 找到第一个文本字段作为"名称"进行必填校验
  for (const f of props.fields) {
    if (f.type === 'text') {
      const val = (values[f.key] ?? '').trim()
      if (!val) {
        validationMessage.value = `${f.label}${t('inputCannotBeEmpty')}`
        return
      }
      values[f.key] = val // trim 后的值
      break
    }
  }

  const copy: Record<string, string> = {}
  for (const f of props.fields) copy[f.key] = values[f.key] ?? ''
  emit('confirm', copy)
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && props.visible) {
    emit('cancel')
    return
  }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="visible" class="dialog-overlay" @mousedown.self="emit('cancel')" @keydown="onKeydown">
        <div ref="panelRef" class="dialog-panel" @mousedown.stop>
          <div class="dialog-header">
            <span class="dialog-title">{{ title }}</span>
            <button class="dialog-close" @click="emit('cancel')">
              <X :size="16" />
            </button>
          </div>
          <div class="dialog-body">
            <label v-for="f in fields" :key="f.key" class="field">
              <span class="field-label">{{ f.label }}</span>
              <input
                v-if="f.type === 'text'"
                v-model="values[f.key]"
                type="text"
                :placeholder="f.placeholder ?? ''"
                class="field-input"
                @keydown.enter="onConfirm"
              />
              <select v-else v-model="values[f.key]" class="field-input">
                <option v-for="o in f.options" :key="o.value" :value="o.value">
                  {{ o.label }}
                </option>
              </select>
            </label>
            <p v-if="validationMessage" class="field-error">{{ validationMessage }}</p>
          </div>
          <div class="dialog-footer">
            <button class="btn btn-cancel" @click="emit('cancel')">{{ t('cancel') }}</button>
            <button class="btn btn-primary" @click="onConfirm">{{ t('confirm') }}</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.dialog-overlay {
  position: fixed; inset: 0; z-index: 1000;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
  display: flex; align-items: center; justify-content: center;
}
.dialog-panel {
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: 10px; width: 380px; max-width: 92vw;
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden;
}
.dialog-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 16px 20px; border-bottom: 1px solid var(--color-border);
}
.dialog-title {
  font-size: 14px; font-weight: 600; color: var(--color-text-primary);
}
.dialog-close {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; padding: 3px; border-radius: 4px; transition: all 0.15s;
}
.dialog-close:hover { color: var(--color-text-primary); background: var(--color-bg-active); }
.dialog-body {
  padding: 20px; display: flex; flex-direction: column; gap: 16px;
}
.field { display: flex; flex-direction: column; gap: 6px; }
.field-label { font-size: 12px; color: var(--color-text-muted); font-weight: 500; }
.field-input {
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: 6px;
  padding: 9px 12px; color: var(--color-text-primary); font-size: 13px;
  outline: none; transition: border-color 0.15s;
  font-family: inherit;
}
.field-input:focus { border-color: var(--color-accent); box-shadow: 0 0 0 2px rgba(74,158,255,0.12); }
.field-input::placeholder { color: var(--color-text-disabled); }
.field-error {
  font-size: 12px; color: var(--color-danger); margin: -8px 0 0; padding: 0;
}
.dialog-footer {
  display: flex; justify-content: flex-end; gap: 10px;
  padding: 14px 20px; border-top: 1px solid var(--color-border); background: var(--color-bg-tertiary);
}
.btn {
  padding: 7px 18px; border-radius: 6px; font-size: 13px;
  cursor: pointer; border: none; font-family: inherit;
  transition: all 0.15s; font-weight: 500;
}
.btn-cancel { background: var(--color-bg-active); color: var(--color-text-muted); }
.btn-cancel:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.btn-primary { background: var(--color-accent); color: var(--color-accent-text); }
.btn-primary:hover { background: var(--color-accent-hover); }

/* transition */
.dialog-enter-active { transition: all 0.2s ease-out; }
.dialog-leave-active { transition: all 0.15s ease-in; }
.dialog-enter-from { opacity: 0; }
.dialog-enter-from .dialog-panel { transform: scale(0.95) translateY(-8px); }
.dialog-leave-to { opacity: 0; }
.dialog-leave-to .dialog-panel { transform: scale(0.95); }
</style>
