<script setup lang="ts">
/**
 * ConfigEditor —— 配置文件编辑器
 *
 * 内核是 CodeMirror 6（见 utils/aiCodeEditor.ts，整体动态加载，不进主包）：
 * 高亮、行号、撤销/重做、括号匹配、多光标、行内搜索都由它提供。
 *
 * 这个组件只保留项目自己的外壳与契约：
 *   - 状态栏（行:列 / 行数 / 字符数 / 未保存 / 快捷键提示）
 *   - v-model（modelValue / update:modelValue）+ @save + markSaved()/focus()
 *
 * 查找/替换栏不在这里了：它已经下沉到 utils/aiCodeEditor.ts，成为编辑器自带的
 * 一部分（Ctrl+F 直达）。原来每个调用方各画一套，样式和快捷键行为都得对一遍，
 * 笔记和右侧面板就是因为没接上而露出了 CM 自带的默认面板。
 *
 * 之所以换掉原先「透明 textarea + 高亮层 + 逐字符着色」的实现：那套每次击键都要
 * 全文重新着色并拼一遍 HTML 字符串，行号还是每行一个 DOM；CM 的增量解析在长配置
 * 上快一个档，还白送撤销与括号匹配，且不用再维护「两层字体行高必须完全一致」。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodeEditorHandle } from '../utils/aiCodeEditor'

const props = withDefaults(
  defineProps<{
    modelValue: string
    readonly?: boolean
    /** 用来选语言的提示（nginx.conf / php.ini / app.yml…）；空 = 按键值对配置着色 */
    filename?: string
  }>(),
  { readonly: false, filename: '' },
)

const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
  (e: 'save'): void
}>()

const { t } = useI18n()

const host = ref<HTMLElement | null>(null)
let ed: CodeEditorHandle | null = null
let disposed = false

const text = computed(() => props.modelValue ?? '')
const lineCount = computed(() => (text.value ? text.value.split('\n').length : 1))

// ---- 修改状态（基线在父组件保存成功后刷新）----
const base = ref(props.modelValue ?? '')
const dirty = computed(() => text.value !== base.value)
function markSaved() {
  base.value = text.value
}

// ---- 光标行列（CM 的 selection 回调推过来）----
const caret = ref({ line: 1, col: 1 })

async function mount() {
  if (!host.value) return
  const { createCodeEditor } = await import('../utils/aiCodeEditor')
  // await 期间组件可能已卸载、或 readonly 又变了一次
  if (disposed || !host.value) return
  ed?.destroy()
  ed = await createCodeEditor(host.value, {
    value: text.value,
    filename: props.filename,
    readOnly: props.readonly,
    fontSize: '12.5px',
    onChange: (v) => emit('update:modelValue', v),
    onSave: () => emit('save'),
    onSelectionChange: (line, col) => {
      caret.value = { line, col }
    },
  })
  // 与原来「打开即可敲字」一致：CM 是异步加载的，加载完补一次聚焦
  ed.focus()
}

onMounted(mount)
onBeforeUnmount(() => {
  disposed = true
  ed?.destroy()
  ed = null
})

// 父组件整体替换内容（重新读配置、切文件）时同步；setValue 不触发 onChange，
// 否则会绕回来把 modelValue 再写一遍。
watch(
  () => props.modelValue,
  (v) => ed?.setValue(v ?? ''),
)
// 只读是 CM 的扩展级配置，切换只能重建实例
watch(
  () => props.readonly,
  () => void mount(),
)

// ---- 查找 / 替换在编辑器内部（Ctrl+F），这里不再维护第二套 UI ----

function focus() {
  ed?.focus()
}

defineExpose({ markSaved, focus })
</script>

<template>
  <div class="ce">
    <!-- 编辑区：CodeMirror 宿主（行号/高亮/滚动/查找栏都在它内部） -->
    <div class="ce-body">
      <div ref="host" class="ce-cm"></div>
    </div>

    <!-- 状态栏 -->
    <div class="ce-status">
      <span class="ce-st-item">{{ t('ceLineCol', { line: caret.line, col: caret.col }) }}</span>
      <span class="ce-st-sep">·</span>
      <span class="ce-st-item">{{ t('ceLines', { n: lineCount }) }}</span>
      <span class="ce-st-sep">·</span>
      <span class="ce-st-item">{{ text.length }} {{ t('ceChars') }}</span>
      <span v-if="dirty" class="ce-dirty">● {{ t('ceDirty') }}</span>
      <span class="ce-spacer"></span>
      <span class="ce-keys">Ctrl+F {{ t('ceFind') }} · Ctrl+S {{ t('save') }}</span>
    </div>
  </div>
</template>

<style scoped>
.ce {
  flex: 1 1 auto;
  min-height: 0;
  display: flex;
  flex-direction: column;
  margin: 12px 18px 0;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-primary);
  overflow: hidden;
  transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
}
.ce:focus-within {
  border-color: var(--color-border-focus);
  box-shadow: 0 0 0 2px var(--color-accent-bg);
}

/* ---- 编辑主体：CM 撑满剩余空间（CM 自己的主题在 utils/aiCodeEditor.ts）---- */
.ce-body { flex: 1 1 auto; min-height: 0; display: flex; overflow: hidden; }
.ce-cm { flex: 1 1 auto; min-width: 0; }

/* ---- 状态栏 ---- */
.ce-status {
  flex: none;
  display: flex; align-items: center; gap: 7px;
  padding: 5px 10px;
  border-top: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
  font-size: 11px; color: var(--color-text-muted);
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
}
.ce-st-sep { color: var(--color-text-disabled); }
.ce-dirty { color: var(--color-warning); }
.ce-spacer { flex: 1 1 auto; }
.ce-keys { color: var(--color-text-disabled); }
</style>
