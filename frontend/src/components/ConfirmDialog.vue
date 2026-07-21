<script setup lang="ts">
import { ref, toRef } from 'vue'
import { useI18n } from 'vue-i18n'
import { AlertTriangle } from '@lucide/vue'
import { useFocusTrap } from '../utils/focusTrap'

const { t } = useI18n()

const props = defineProps<{
  visible: boolean
  message: string
}>()

const emit = defineEmits<{
  (e: 'confirm'): void
  (e: 'cancel'): void
}>()

const panelRef = ref<HTMLElement | null>(null)
const { onKeydown: onKeydownTrap } = useFocusTrap(toRef(props, 'visible'), panelRef)

function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape') { emit('cancel'); return }
  onKeydownTrap(e)
}
</script>

<template>
  <Teleport to="body">
    <Transition name="dialog">
      <div v-if="visible" class="confirm-overlay" @mousedown.self="emit('cancel')" @keydown="onKeydown">
        <div ref="panelRef" class="confirm-panel" @mousedown.stop>
          <div class="confirm-icon">
            <AlertTriangle :size="24" />
          </div>
          <p class="confirm-message">{{ message }}</p>
          <div class="confirm-footer">
            <button class="btn btn-cancel" @click="emit('cancel')">{{ t('cancel') }}</button>
            <button class="btn btn-danger" @click="emit('confirm')">{{ t('confirm') }}</button>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.confirm-overlay {
  position: fixed; inset: 0; z-index: 20000;
  background: var(--color-bg-overlay);
  backdrop-filter: blur(2px);
  display: flex; align-items: center; justify-content: center;
}
.confirm-panel {
  background: var(--color-surface); border: 1px solid var(--color-border);
  border-radius: 10px; width: 360px; max-width: 90vw;
  padding: 24px; text-align: center;
  box-shadow: 0 12px 48px var(--color-bg-overlay);
}
.confirm-icon { color: var(--color-warning); margin-bottom: 12px; }
.confirm-message { font-size: 14px; color: var(--color-text-primary); margin: 0 0 24px; line-height: 1.6; }
.confirm-footer { display: flex; justify-content: center; gap: 12px; }
.btn {
  padding: 8px 24px; border-radius: 6px; font-size: 13px;
  cursor: pointer; border: none; font-family: inherit;
  transition: background-color 0.12s, color 0.12s, border-color 0.12s, opacity 0.12s; font-weight: 500;
}
.btn-cancel { background: var(--color-bg-active); color: var(--color-text-muted); }
.btn-cancel:hover { background: var(--color-bg-active); color: var(--color-text-primary); }
.btn-danger { background: var(--color-danger); color: var(--color-accent-text); }
.btn-danger:hover { background: var(--color-danger); opacity: 0.85; }

.dialog-enter-active { transition: opacity 0.2s ease-out, transform 0.2s ease-out; }
.dialog-leave-active { transition: background-color 0.15s, color 0.15s, border-color 0.15s, opacity 0.15s ease-in; }
.dialog-enter-from, .dialog-leave-to { opacity: 0; }
.dialog-enter-from .confirm-panel { transform: scale(0.95) translateY(-8px); }
.dialog-leave-to .confirm-panel { transform: scale(0.95); }
</style>
