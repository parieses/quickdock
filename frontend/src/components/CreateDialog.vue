<script setup lang="ts">
import { reactive, ref, watch, toRef, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { X } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'

const { t } = useI18n()

export interface CreateField {
  key: string
  label: string
  // checkbox 的值统一用字符串 '1' / '0'：整张表单的 values 是 Record<string,string>，
  // 单独给布尔开一条类型会让每个消费方都要再判断一次。
  type: 'text' | 'select' | 'textarea' | 'checkbox'
  options?: { label: string; value: string }[]
  placeholder?: string
  default?: string
  hint?: string
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

// 是否包含 textarea 字段：含多行文本的弹框（如笔记内容）自动加宽加高，
// 纯文本字段的弹框（场景/工作区/集合名）保持原有紧凑尺寸。
const hasTextarea = computed(() => props.fields.some(f => f.type === 'textarea'))

// 根据 visible / editValues / fields 推导初始值
function resetValues() {
  const map: Record<string, string> = {}
  for (const f of props.fields) {
    if (props.editValues && props.editValues[f.key] !== undefined) {
      map[f.key] = props.editValues[f.key]
    } else if (f.default !== undefined) {
      map[f.key] = f.default
    } else if (f.type === 'checkbox') {
      map[f.key] = '0'
    } else {
      map[f.key] = f.type === 'select' ? (f.options?.[0]?.value ?? '') : ''
    }
  }
  // reactive 原地替换
  for (const k of Object.keys(values)) delete values[k]
  Object.assign(values, map)
  validationMessage.value = ''
}

// 弹窗打开时重置表单（immediate 处理 v-if + visible=true 首次挂载的情况）
// 只监听 visible 的 false→true 边沿：父组件任意重渲染（toast/加载/locale）
// 都会让 editValues 产生新引用，若把它放进依赖会误触发 resetValues 清空输入。
watch(() => props.visible, (v) => {
  if (v) resetValues()
}, { immediate: true })

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
        <div ref="panelRef" class="dialog-panel" :class="{ 'dialog-panel--wide': hasTextarea }" @mousedown.stop>
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
              <textarea
                v-else-if="f.type === 'textarea'"
                v-model="values[f.key]"
                :placeholder="f.placeholder ?? ''"
                class="field-input field-textarea"
                rows="10"
              ></textarea>
              <input
                v-else-if="f.type === 'checkbox'"
                type="checkbox"
                class="field-check"
                :checked="values[f.key] === '1'"
                @change="values[f.key] = ($event.target as HTMLInputElement).checked ? '1' : '0'"
              />
              <select v-else v-model="values[f.key]" class="field-input">
                <option v-for="o in f.options" :key="o.value" :value="o.value">
                  {{ o.label }}
                </option>
              </select>
              <p v-if="f.hint" class="field-hint">{{ f.hint }}</p>
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
  background: var(--color-popover-bg); border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md); width: 380px; max-width: 92vw;
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden; max-height: 86vh;
}
.dialog-panel--wide {
  width: 560px; max-width: 94vw;
}
.dialog-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: var(--space-6) var(--space-7); border-bottom: 1px solid var(--color-border);
}
.dialog-title {
  font-size: var(--text-base); font-weight: 600; color: var(--color-text-primary);
}
.dialog-close {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; padding: 3px; border-radius: var(--radius-xs); transition: background-color 0.15s, color 0.15s, border-color 0.15s, opacity 0.15s;
}
.dialog-close:hover { color: var(--color-text-primary); background: var(--color-bg-active); }
.dialog-body {
  padding: var(--space-7); display: flex; flex-direction: column; gap: var(--space-6);
  overflow-y: auto;
}
.field { display: flex; flex-direction: column; gap: var(--space-3); }
.field-label { font-size: var(--text-sm); color: var(--color-text-muted); font-weight: 500; }
.field-input {
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  padding: 9px var(--space-5); color: var(--color-text-primary); font-size: var(--text-md);
  outline: none; transition: border-color 0.15s;
  font-family: inherit;
}
.field-input:focus { border-color: var(--color-border-focus-soft); }
.field-input::placeholder { color: var(--color-text-disabled); }
/* 勾选项：不占满整行宽度，跟输入框左侧对齐即可 */
.field-check {
  width: 16px; height: 16px; margin: 0;
  accent-color: var(--color-accent);
  cursor: pointer;
  align-self: flex-start;
}
.field-textarea { resize: vertical; min-height: 220px; font-family: var(--font-mono); line-height: 1.5; }
.field-error {
  font-size: var(--text-sm); color: var(--color-danger); margin: -8px 0 0; padding: 0;
}
.field-hint {
  font-size: var(--text-xs); color: var(--color-text-muted); margin: var(--space-1) 0 0; padding: 0;
  line-height: 1.5;
}
.dialog-footer {
  display: flex; justify-content: flex-end; gap: 10px;
  padding: 14px var(--space-7); border-top: 1px solid var(--color-border); background: var(--color-bg-tertiary);
}
.btn {display: inline-flex; align-items: center; justify-content: center; gap: var(--space-2); 
  height: 32px; padding: 0 var(--space-6); border-radius: var(--radius-sm); font-size: var(--text-md);
  cursor: pointer; border: none; font-family: inherit;
  transition: background-color 0.15s, color 0.15s, border-color 0.15s, opacity 0.15s; font-weight: 500;
}
.btn-cancel { background: var(--color-bg-active); color: var(--color-text-muted); }
.btn-cancel:hover { background: var(--color-bg-active); color: var(--color-text-primary); }

/* transition */
.dialog-enter-active { transition: opacity 0.2s ease-out, transform 0.2s ease-out; }
.dialog-leave-active { transition: background-color 0.15s, color 0.15s, border-color 0.15s, opacity 0.15s ease-in; }
.dialog-enter-from { opacity: 0; }
.dialog-enter-from .dialog-panel { transform: scale(0.95) translateY(-8px); }
.dialog-leave-to { opacity: 0; }
.dialog-leave-to .dialog-panel { transform: scale(0.95); }
</style>
