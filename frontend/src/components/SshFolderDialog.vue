<script lang="ts" setup>
/**
 * 分组命名弹窗。
 *
 * 替代原先的 window.prompt：原生对话框在 WebView2 里是系统样式，
 * 不跟随主题（深色主题下白底黑字刺眼）、字体与圆角也和整站不一致，
 * 而且在输入法组合中容易丢字。自建之后还能多一层校验（重名、长度）。
 *
 * 只做「要一个名字」这一件事：分组没有别的字段，开主机表单去填属于用牛刀。
 */
import { nextTick, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { FolderPlus } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'

const { t } = useI18n()

const props = defineProps<{
  visible: boolean
  /** 已有分组名，用于重名校验；不含自己 */
  existing: string[]
}>()

const emit = defineEmits<{
  (e: 'submit', name: string): void
  (e: 'cancel'): void
}>()

const inputRef = ref<HTMLInputElement | null>(null)
const panelRef = ref<HTMLElement | null>(null)
useFocusTrap(toRef(props, 'visible'), panelRef)

const name = ref('')
const errMsg = ref('')

/** 长度上限对齐后端 name 列的常见约束，避免提交后被截断。 */
const MAX = 32

watch(
  () => props.visible,
  (v) => {
    if (!v) return
    // 每次打开都是一次新建，上一次的输入与报错不该留着。
    name.value = ''
    errMsg.value = ''
    nextTick(() => {
      inputRef.value?.focus()
    })
  },
)

function submit() {
  const trimmed = name.value.trim()
  if (!trimmed) {
    errMsg.value = t('sshFolderNameRequired')
    inputRef.value?.focus()
    return
  }
  if (trimmed.length > MAX) {
    errMsg.value = t('sshFolderNameTooLong', { max: MAX })
    inputRef.value?.focus()
    return
  }
  // 忽略大小写与首尾空格：Windows 上用户不会意识到 "Prod" 和 "prod" 是两个分组。
  const dup = props.existing.some((n) => n.trim().toLowerCase() === trimmed.toLowerCase())
  if (dup) {
    errMsg.value = t('sshFolderNameDuplicate')
    inputRef.value?.focus()
    return
  }
  emit('submit', trimmed)
}

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') {
    emit('cancel')
    return
  }
  // 输入法组合中回车是「确认候选词」，此时提交会把半成品名字发出去。
  if (e.key === 'Enter' && !e.isComposing) submit()
}
</script>

<template>
  <Teleport to="body">
    <Transition name="fd">
      <div v-if="visible" class="fd-overlay" @mousedown.self="emit('cancel')" @keydown="onKeydown">
        <div ref="panelRef" class="fd-panel" role="dialog" aria-modal="true">
          <header class="fd-head">
            <span class="fd-badge"><FolderPlus :size="15" /></span>
            <span class="fd-title">{{ t('sshNewFolder') }}</span>
            <button class="fd-x" :title="t('cancel')" @click="emit('cancel')">
              <span class="fd-x-glyph">&times;</span>
            </button>
          </header>

          <div class="fd-body">
            <label class="fd-field">
              <span class="fd-label">{{ t('sshFolderNamePrompt') }}</span>
              <input
                ref="inputRef"
                v-model="name"
                class="fd-input"
                :class="{ 'fd-input--err': errMsg }"
                :maxlength="MAX"
                :placeholder="t('sshFolderPlaceholder')"
                @input="errMsg = ''"
              />
            </label>
            <p v-if="errMsg" class="fd-err">{{ errMsg }}</p>
          </div>

          <footer class="fd-foot">
            <button class="fd-btn" @click="emit('cancel')">{{ t('cancel') }}</button>
            <button class="fd-btn fd-btn--primary" @click="submit">{{ t('create') }}</button>
          </footer>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.fd-overlay {
  position: fixed;
  inset: 0;
  z-index: 1600;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
}

.fd-panel {
  display: flex;
  flex-direction: column;
  width: 380px;
  max-width: 92vw;
  background: var(--color-surface);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  box-shadow: 0 12px 48px var(--color-bg-overlay);
  overflow: hidden;
}

.fd-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: var(--space-5) var(--space-6);
  border-bottom: 1px solid var(--color-border);
}

.fd-badge {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  border-radius: var(--radius-sm);
  color: var(--color-accent);
  background: var(--color-accent-bg);
}

.fd-title {
  flex: 1 1 auto;
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
}

.fd-x {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 0 0 auto;
  width: 24px;
  height: 24px;
  padding: 0;
  color: var(--color-text-disabled);
  background: none;
  border: none;
  border-radius: var(--radius-xs);
  cursor: pointer;
}

.fd-x:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-active);
}

.fd-x-glyph {
  font-size: 17px;
  line-height: 1;
}

.fd-body {
  padding: var(--space-6);
}

.fd-field {
  display: block;
}

.fd-label {
  display: block;
  margin-bottom: var(--space-3);
  font-size: 11.5px;
  color: var(--color-text-secondary);
}

.fd-input {
  width: 100%;
  box-sizing: border-box;
  padding: var(--space-3) var(--space-4);
  font-size: 12.5px;
  font-family: inherit;
  color: var(--color-text-primary);
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  outline: none;
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
}

.fd-input::placeholder {
  color: var(--color-text-disabled);
}

.fd-input:focus {
  border-color: var(--color-accent);
}

.fd-input--err,
.fd-input--err:focus {
  border-color: var(--color-danger);
}

.fd-err {
  margin: var(--space-3) 0 0;
  font-size: 11px;
  color: var(--color-danger);
}

.fd-foot {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-3);
  flex: 0 0 auto;
  padding: var(--space-4) var(--space-6);
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
}

.fd-btn {
  padding: var(--space-2) var(--space-5);
  font-size: 12px;
  font-family: inherit;
  color: var(--color-text-secondary);
  background: transparent;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-xs);
  cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast);
}

.fd-btn:hover {
  color: var(--color-text-primary);
  background: var(--color-bg-hover);
}

.fd-btn--primary {
  color: var(--color-accent-text);
  background: var(--color-accent);
  border-color: var(--color-accent);
}

.fd-btn--primary:hover {
  color: var(--color-accent-text);
  background: var(--color-accent-hover);
}

.fd-enter-active {
  transition: opacity 0.14s ease-out;
}

.fd-leave-active {
  transition: opacity 0.1s ease-in;
}

.fd-enter-from,
.fd-leave-to {
  opacity: 0;
}
</style>
