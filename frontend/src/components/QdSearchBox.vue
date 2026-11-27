<script setup lang="ts">
/**
 * 统一搜索框：图标 + 输入 + 清除（可选加载态）。
 *
 * 三栏过滤行（项目 / 集合 / 场景）此前是同一段模板各抄一遍，后来 20 多处又各自
 * 手写「Search 图标 + input + X 清除」。这里收口共同的骨架，差异用 props/slot 表达：
 *   - 结果计数、过滤下拉、快捷键提示等**不放盒内**，用 trailing slot 由调用方给。
 *   - 布局（max-width / margin）不放这里，由调用方 class 提供；组件只负责盒子皮肤。
 *   - Enter 只 emit submit，不绑定业务；Esc 默认不拦截，escToClear 时才清空。
 *
 * 用传统 props/emits 而非 defineModel：项目锁定 Vue 3.2.x，defineModel 需要 3.4+。
 */
import { ref } from 'vue'
import { Search, X, Loader2 } from '@lucide/vue'
import { useI18n } from 'vue-i18n'

const props = defineProps({
  modelValue: { type: String, default: '' },
  placeholder: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
  /** 有内容时显示清除按钮 */
  clearable: { type: Boolean, default: true },
  /** 用 spinner 替换清除位（知识库检索中） */
  loading: { type: Boolean, default: false },
  /** 图标尺寸，个别场景用 12/14 */
  iconSize: { type: Number, default: 13 },
  /** 不渲染前置图标（SSH 快速连接行只有输入 + 动作按钮） */
  showIcon: { type: Boolean, default: true },
  /** 清除按钮 title，默认走 t('clear') */
  clearTitle: { type: String, default: '' },
  /** Esc 清空内容（默认只 emit escape，不拦截） */
  escToClear: { type: Boolean, default: false },
})

const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'clear'): void
  (e: 'submit'): void
  (e: 'escape'): void
}>()

const { t } = useI18n()

const inputEl = ref<HTMLInputElement | null>(null)

// 少数调用方要拿真实 input：剪贴板面板用 `e.target === input` 区分「在搜索框里按
// Enter」和「在卡片上按 Enter」，/ 键聚焦也走这里。暴露元素本身而非只暴露 focus()，
// 是因为它需要做 identity 比较。
defineExpose({
  inputEl,
  focus: () => inputEl.value?.focus(),
  select: () => inputEl.value?.select(),
})

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLInputElement).value)
}

function clear() {
  emit('update:modelValue', '')
  emit('clear')
}

function onEsc() {
  if (props.escToClear) {
    clear()
    return
  }
  emit('escape')
}
</script>

<template>
  <div class="qd-search-box" :class="{ 'is-disabled': disabled }">
    <slot name="leading">
      <Search v-if="showIcon" :size="iconSize" class="qd-search-icon" />
    </slot>
    <input
      ref="inputEl"
      :value="modelValue"
      type="text"
      class="qd-search-input"
      :placeholder="placeholder"
      autocomplete="off"
      spellcheck="false"
      :disabled="disabled"
      @input="onInput"
      @keydown.esc="onEsc"
      @keydown.enter="emit('submit')"
    />
    <slot name="trailing" />
    <Loader2 v-if="loading" :size="iconSize" class="qd-search-spin" />
    <button
      v-else-if="clearable && modelValue.trim()"
      class="qd-search-clear"
      :title="clearTitle || t('clear')"
      @click="clear"
    >
      <X :size="12" />
    </button>
  </div>
</template>
