<script setup lang="ts">
import { computed, ref, watch, inject, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronRight, Copy } from '@lucide/vue'
import type { ToastAPI } from '../types'

const props = withDefaults(defineProps<{
  data: any
  name?: string
  depth?: number
  isLast?: boolean
}>(), {
  depth: 0,
  isLast: true,
})

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// 顶层两级的容器默认展开，更深默认收起
const expanded = ref(props.depth < 2)

// 全局展开/收起信号（父级 provide 注入）
const jsonExpandAll = inject<Ref<boolean | null>>('jsonTreeCtrl')
if (jsonExpandAll) {
  watch(jsonExpandAll, (v) => { if (v !== null) expanded.value = v }, { immediate: true })
}

const isArray = computed(() => Array.isArray(props.data))
const isObject = computed(() => props.data !== null && typeof props.data === 'object' && !isArray.value)
const isContainer = computed(() => isObject.value || isArray.value)

const entries = computed<[string, any][]>(() => {
  if (isObject.value) return Object.entries(props.data)
  if (isArray.value) return props.data.map((v: any, i: number) => [String(i), v] as [string, any])
  return []
})

const summary = computed(() => {
  const n = entries.value.length
  if (!n) return ''
  return `${n} ${isArray.value ? t('httpItems') : t('httpKeys')}`
})

function valType(v: any): string {
  if (v === null) return 'null'
  if (Array.isArray(v)) return 'array'
  return typeof v
}
function valDisplay(v: any): string {
  if (v === null) return 'null'
  if (typeof v === 'string') return '"' + v + '"'
  if (typeof v === 'object') return Array.isArray(v) ? '[ … ]' : '{ … }'
  return String(v)
}

const selfType = computed(() => valType(props.data))
const selfDisplay = computed(() => valDisplay(props.data))

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('copied'))
  } catch {
    toast.error(t('copyFailed'))
  }
}
function copyLeafValue() {
  const v = props.data
  copy(typeof v === 'string' ? v : JSON.stringify(v))
}
function copyNodeJson() {
  try {
    copy(JSON.stringify(props.data, null, 2))
  } catch {
    copy(String(props.data))
  }
}
</script>

<template>
  <div class="jt">
    <!-- 容器（对象 / 数组） -->
    <template v-if="isContainer">
      <div class="jt-row" :style="{ paddingLeft: depth * 14 + 6 + 'px' }">
        <button class="jt-toggle" :title="expanded ? t('httpCollapseAll') : t('httpExpandAll')" @click="expanded = !expanded">
          <ChevronRight :size="13" :class="{ open: expanded }" />
        </button>
        <span v-if="name !== undefined" class="jt-key">{{ name }}</span>
        <span v-if="name !== undefined" class="jt-colon">:</span>
        <span class="jt-bracket">{{ isArray ? '[' : '{' }}</span>
        <span v-if="!expanded" class="jt-summary">{{ summary }}</span>
        <button class="jt-copy" :title="t('httpCopyValue')" @click.stop="copyNodeJson"><Copy :size="11" /></button>
      </div>
      <div v-if="expanded">
        <JsonTree
          v-for="([k, v], i) in entries"
          :key="k"
          :data="v"
          :name="k"
          :depth="depth + 1"
          :is-last="i === entries.length - 1"
        />
        <div class="jt-closing" :style="{ paddingLeft: depth * 14 + 20 + 'px' }">
          {{ isArray ? ']' : '}' }}{{ isLast ? '' : ',' }}
        </div>
      </div>
    </template>

    <!-- 叶子（字符串 / 数字 / 布尔 / null） -->
    <template v-else>
      <div class="jt-row jt-leaf" :style="{ paddingLeft: depth * 14 + 20 + 'px' }">
        <span v-if="name !== undefined" class="jt-key">{{ name }}</span>
        <span v-if="name !== undefined" class="jt-colon">:</span>
        <span :class="['jt-val', 'jt-' + selfType]">{{ selfDisplay }}</span>
        <button class="jt-copy" :title="t('httpCopyValue')" @click.stop="copyLeafValue"><Copy :size="11" /></button>
        <span v-if="!isLast" class="jt-comma">,</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.jt { font-family: 'Consolas', 'Monaco', monospace; font-size: 12px; line-height: 1.6; }
.jt-row { display: flex; align-items: center; gap: 2px; min-height: 22px; padding-right: 6px; }
.jt-row:hover { background: var(--color-bg-hover); }
.jt-toggle {
  background: none; border: none; cursor: pointer; padding: 0; margin: 0;
  display: flex; align-items: center; justify-content: center;
  color: var(--color-text-muted); flex-shrink: 0; width: 16px; height: 16px;
  transition: color var(--transition-fast);
}
.jt-toggle:hover { color: var(--color-text-primary); }
.jt-toggle :deep(svg) { transition: transform var(--transition-fast); }
.jt-toggle :deep(svg.open) { transform: rotate(90deg); }
.jt-key { color: #7fb7ff; }
.jt-colon { color: var(--color-text-disabled); }
.jt-bracket { color: var(--color-text-disabled); }
.jt-summary { color: var(--color-text-disabled); font-style: italic; margin: 0 4px; }
.jt-closing { color: var(--color-text-disabled); }
.jt-comma { color: var(--color-text-disabled); }
.jt-val.jt-string { color: #9ece6a; }
.jt-val.jt-number { color: #e0af68; }
.jt-val.jt-boolean { color: #bb9af7; }
.jt-val.jt-null { color: #565f89; }
.jt-val.jt-array { color: var(--color-text-muted); }
.jt-copy {
  background: none; border: none; cursor: pointer; padding: 2px; margin-left: 4px;
  display: flex; align-items: center; justify-content: center;
  color: var(--color-text-disabled); border-radius: 3px; flex-shrink: 0;
  opacity: 0; transition: color var(--transition-fast), opacity var(--transition-fast), background var(--transition-fast);
}
.jt-row:hover .jt-copy { opacity: 1; }
.jt-copy:hover { color: var(--color-accent); background: var(--color-bg-active); }
</style>
