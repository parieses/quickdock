<script setup lang="ts">
/**
 * 全应用唯一的分段 / 视图切换控件。
 *
 * 存量有 5 套（.vt-tab / .seg-btn.active / .dl-tab.is-on / .php-tab.active /
 * .db-tab …），选中态有的用实色底、有的用下划线、有的只换字色。新写的切换
 * 一律用本组件；存量由 styles/app.css 的「分段控件视觉基线」统一选中态表现。
 */
interface QdTabItem {
  id: string
  label: string
  disabled?: boolean
}

const props = defineProps<{
  modelValue: string
  tabs: QdTabItem[]
}>()

const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
</script>

<template>
  <div class="qd-tabs" role="tablist">
    <button
      v-for="tab in props.tabs"
      :key="tab.id"
      type="button"
      class="qd-tab"
      role="tab"
      :aria-selected="modelValue === tab.id"
      :disabled="tab.disabled"
      @click="emit('update:modelValue', tab.id)"
    >{{ tab.label }}</button>
  </div>
</template>

<style scoped>
.qd-tabs {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  padding: var(--space-1);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}
.qd-tab {
  min-height: 24px;
  padding: 0 var(--space-5);
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
  font-family: inherit;
  cursor: pointer;
  white-space: nowrap;
  transition: background var(--transition-fast), color var(--transition-fast);
}
.qd-tab:hover:not(:disabled) { color: var(--color-text-primary); }
.qd-tab[aria-selected='true'] {
  background: var(--color-accent-bg);
  color: var(--color-accent);
}
.qd-tab:disabled { opacity: 0.5; cursor: default; }
</style>
