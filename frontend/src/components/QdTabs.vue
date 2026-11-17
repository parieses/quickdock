<script setup lang="ts" generic="T extends string">
/**
 * 全应用唯一的分段 / 视图切换控件。
 *
 * 泛型 T 让调用方的联合类型（FilterKey / DetailTab 等）能一路推导下来，
 * 不必把 ref 放宽成 string，也不会在 v-model 处报类型不兼容。
 *
 * 两种外观，只影响皮，不影响行为：
 * - segmented（默认）：带外框的分段控件，用于设置页「同级视图切换」；
 * - chip：无外框的胶囊标签，用于列表筛选 / 弹窗内分页这类轻量切换。
 *
 * 存量曾有 7 套私有实现（.dl-tab / .settings-tabs / .ai-tabs / .php-tab / .vt-tab，
 * 以及 SchedulePage、SshHostDialog 各自的 .seg-btn），选中态有的用实色底、有的用下划线、
 * 有的只换字色。11-16 已全部收编到本组件：外观靠 variant 区分，图标与彩色圆点走 #tab
 * 插槽，计数走 badge，role=tab / aria-selected / disabled / 键盘可达只有一份实现。
 *
 * 需要更复杂的内容（图标、未读徽章）时用 #tab 具名插槽，别另起一套按钮。
 */
import { computed, ref, nextTick } from 'vue'

interface QdTabItem<T extends string = string> {
  id: T
  label: string
  disabled?: boolean
  /** 数字角标（筛选计数、未读数）。null / undefined / 空串都不渲染。 */
  badge?: string | number | null
}

const props = defineProps<{
  modelValue: T
  tabs: QdTabItem<T>[]
  variant?: 'segmented' | 'chip'
  ariaLabel?: string
}>()

const emit = defineEmits<{ 'update:modelValue': [value: T] }>()

const variantClass = computed(() => 'is-' + (props.variant ?? 'segmented'))

// WAI-ARIA tab 模式：整组 tab 只让选中项进 Tab 序列（roving tabindex），
// 左右方向键在组内移动并切换。禁用项跳过，到两端不回绕（桌面工具的常见预期）。
const tabEls = ref<HTMLButtonElement[]>([])
function setTabEl(el: unknown, i: number) {
  if (el) tabEls.value[i] = el as HTMLButtonElement
}
function moveFocus(from: number, delta: number) {
  let i = from + delta
  while (i >= 0 && i < props.tabs.length && props.tabs[i].disabled) i += delta
  if (i < 0 || i >= props.tabs.length) return
  emit('update:modelValue', props.tabs[i].id)
  nextTick(() => tabEls.value[i]?.focus())
}
function onTabKey(e: KeyboardEvent, i: number) {
  if (e.key === 'ArrowRight' || e.key === 'ArrowLeft') {
    e.preventDefault()
    moveFocus(i, e.key === 'ArrowRight' ? 1 : -1)
  } else if (e.key === 'Home' || e.key === 'End') {
    e.preventDefault()
    const delta = e.key === 'Home' ? -i : props.tabs.length - 1 - i
    moveFocus(i, delta || (e.key === 'Home' ? 1 : -1))
  }
}
</script>

<template>
  <div class="qd-tabs" :class="variantClass" role="tablist" :aria-label="ariaLabel">
    <button
      v-for="(tab, i) in tabs"
      :key="tab.id"
      :ref="(el) => setTabEl(el, i)"
      type="button"
      class="qd-tab"
      role="tab"
      :aria-selected="modelValue === tab.id"
      :tabindex="modelValue === tab.id ? 0 : -1"
      :disabled="tab.disabled"
      @click="emit('update:modelValue', tab.id)"
      @keydown="onTabKey($event, i)"
    >
      <slot name="tab" :tab="tab" :active="modelValue === tab.id">
        <span class="qd-tab-label">{{ tab.label }}</span>
        <span
          v-if="tab.badge !== undefined && tab.badge !== null && tab.badge !== ''"
          class="qd-tab-badge"
        >{{ tab.badge }}</span>
      </slot>
    </button>
  </div>
</template>

<style scoped>
.qd-tabs {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
}
.qd-tabs.is-segmented {
  padding: var(--space-1);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-secondary);
}
.qd-tabs.is-chip {
  gap: var(--space-2);
  flex-wrap: wrap;
}
.qd-tab {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
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
.qd-tab-badge {
  font-size: var(--text-2xs);
  color: var(--color-text-muted);
}

/* chip：22px 胶囊，与外框解耦 —— 调用方自己决定把它放在哪一行 */
.qd-tabs.is-chip .qd-tab {
  height: 22px;
  min-height: 0;
  padding: 0 var(--space-3);
  border: 1px solid transparent;
  border-radius: var(--radius-full);
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  font-size: var(--text-xs);
}
.qd-tabs.is-chip .qd-tab:hover:not(:disabled) {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.qd-tabs.is-chip .qd-tab[aria-selected='true'] {
  background: var(--color-accent-bg);
  color: var(--color-accent);
  border-color: var(--color-accent-border-strong);
}
</style>
