<script setup lang="ts">
/**
 * 全应用唯一的开关控件。
 *
 * 11-16 已把 10 处标准 toggle 全部收编：环境页的 .svc-switch（label + input[type=checkbox]）
 * 与仪表盘 / 监控 / 调度 / 待办 / 设置里共 9 处 .toggle-btn / .toggle-btn-sm（40×22、
 * 36×20 两种尺寸）统一成本组件的 34×18。尺寸收窄是有意的：原来两种尺寸并存，
 * 同一屏里开关大小不一致，比各自小 2~6px 更扎眼。
 *
 * 刻意不迁的三处，不是遗漏：
 * - .cred-switch 是「图标 + 文案 + 圆点」的一体化按钮，.ai-plus-switch 是整行按钮里的
 *   纯视觉指示 —— 都不是 toggle，硬套会产出 button 套 button，或丢掉图标与文案。
 * - .jt-toggle（JsonTree）与 .hidden-toggle（Sidebar）是展开/折叠箭头，不是开关。
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
