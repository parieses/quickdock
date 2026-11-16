<script setup lang="ts">
/**
 * 全应用唯一的开关控件。
 *
 * 存量有 11 套私有实现（.svc-switch / .cred-switch / .ai-plus-switch /
 * .jt-toggle / .hidden-toggle …），尺寸与配色各不相同。新写的开关一律用本组件；
 * 存量开关在 styles/app.css 的「开关视觉基线」里统一到同一组几何与颜色 token，
 * 不必一次性迁移模板（迁移涉及各页自己的 label / disabled 语义，适合单独排期）。
 */
const props = withDefaults(defineProps<{
  modelValue: boolean
  disabled?: boolean
  label?: string
}>(), { disabled: false })

const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>

<template>
  <button
    type="button"
    class="qd-switch"
    role="switch"
    :aria-checked="modelValue"
    :aria-label="label"
    :disabled="disabled"
    @click="emit('update:modelValue', !modelValue)"
  >
    <span class="qd-switch-thumb" />
  </button>
</template>

<style scoped>
.qd-switch {
  position: relative;
  flex: none;
  width: 34px;
  height: 18px;
  padding: 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: var(--color-bg-tertiary);
  cursor: pointer;
  transition: background var(--transition-fast), border-color var(--transition-fast);
}
.qd-switch[aria-checked='true'] {
  background: var(--color-accent);
  border-color: var(--color-accent);
}
.qd-switch:disabled { opacity: 0.5; cursor: default; }
.qd-switch-thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 12px;
  height: 12px;
  border-radius: 50%;
  background: var(--color-text-primary);
  transition: transform var(--transition-fast), background var(--transition-fast);
}
.qd-switch[aria-checked='true'] .qd-switch-thumb {
  transform: translateX(16px);
  background: var(--color-accent-text);
}
</style>
