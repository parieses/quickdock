<script setup lang="ts">
import { AlertCircle, CheckCircle2 } from '@lucide/vue'

export interface ToastMessage {
  id: number
  text: string
  type: 'error' | 'success'
}

defineProps<{
  messages: ToastMessage[]
}>()

defineEmits<{
  (e: 'remove', id: number): void
}>()
</script>

<template>
  <Teleport to="body">
    <div class="toast-container">
      <TransitionGroup name="toast">
        <div
          v-for="msg in messages"
          :key="msg.id"
          :class="['toast-item', msg.type]"
          @click="$emit('remove', msg.id)"
        >
          <CheckCircle2 v-if="msg.type === 'success'" :size="16" />
          <AlertCircle v-else :size="16" />
          <span class="toast-text">{{ msg.text }}</span>
        </div>
      </TransitionGroup>
    </div>
  </Teleport>
</template>

<style scoped>
.toast-container {
  position: fixed; top: 16px; left: 50%; transform: translateX(-50%);
  z-index: 10001; display: flex; flex-direction: column; gap: 8px;
  pointer-events: none;
}
.toast-item {
  display: flex; align-items: center; gap: 8px;
  padding: 10px 20px; border-radius: 8px; font-size: 13px;
  box-shadow: 0 4px 24px rgba(0,0,0,0.45);
  pointer-events: auto; cursor: pointer; white-space: nowrap;
  transition: box-shadow 0.15s;
}
.toast-item:hover { box-shadow: 0 6px 28px rgba(0,0,0,0.55); }
.toast-item.error {
  background: var(--color-toast-error-bg); color: var(--color-toast-error-text); border: 1px solid var(--color-toast-error-border);
}
.toast-item.success {
  background: var(--color-toast-success-bg); color: var(--color-toast-success-text); border: 1px solid var(--color-toast-success-border);
}

/* transition */
.toast-enter-active { transition: all 0.3s cubic-bezier(0.16, 1, 0.3, 1); }
.toast-leave-active { transition: all 0.2s ease-in; }
.toast-enter-from { opacity: 0; transform: translateY(-20px) scale(0.95); }
.toast-leave-to { opacity: 0; transform: translateY(-12px) scale(0.95); }
.toast-move { transition: transform 0.3s ease; }
</style>
