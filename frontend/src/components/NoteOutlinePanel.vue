<script setup lang="ts">
/**
 * 常驻大纲侧栏：按标题层级缩进列出 headings，点击定位到源行、逐节折叠、
 * 一键全折叠 / 全展开。全部文案走主流程已加好的 outline* 这一组 i18n key。
 *
 * 折叠状态记在组件本地：props 只有 headings / activeLine，拿不到编辑器里
 * 「哪一节真的被折了」，所以以「点过 fold 就当作已折叠」为准发方向明确的
 * 指令（@fold / @unfold），而不是让父层猜要不要 toggle。
 */
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronRight, X } from '@lucide/vue'

interface Heading {
  level: number
  text: string
  line: number
}

const props = defineProps<{
  headings: Heading[]
  activeLine?: number
}>()

const emit = defineEmits<{
  (e: 'jump', line: number): void
  (e: 'fold', line: number): void
  (e: 'unfold', line: number): void
  (e: 'foldAll'): void
  (e: 'unfoldAll'): void
  (e: 'close'): void
}>()

const { t } = useI18n()

// 已折叠的行号集合。Set 的内容变更不触发响应，故每次都整体换成新 Set。
const folded = ref<Set<number>>(new Set())

function isFolded(line: number): boolean {
  return folded.value.has(line)
}

function toggleFold(line: number) {
  const next = new Set(folded.value)
  if (next.has(line)) {
    next.delete(line)
    folded.value = next
    emit('unfold', line)
  } else {
    next.add(line)
    folded.value = next
    emit('fold', line)
  }
}

function collapseAll() {
  folded.value = new Set(props.headings.map((h) => h.line))
  emit('foldAll')
}

function expandAll() {
  folded.value = new Set()
  emit('unfoldAll')
}

// 文档切换 / 标题增删后，丢掉已不存在的行号，免得新文档里恰好同号的标题
// 一上来就显示成「已折叠」。
watch(
  () => props.headings,
  (list) => {
    const alive = new Set(list.map((h) => h.line))
    let changed = false
    const next = new Set<number>()
    for (const l of folded.value) {
      if (alive.has(l)) next.add(l)
      else changed = true
    }
    if (changed) folded.value = next
  },
)

// 层级缩进：基准间距取设计 token --space-4，层级本身是运行时数据、写不进 CSS，
// 故用内联 style 传 calc()（数值来自 token，只有「层级倍数」是动态的）。
function indentStyle(level: number): { paddingLeft: string } {
  return { paddingLeft: `calc(var(--space-3) + var(--space-4) * ${Math.max(0, level - 1)})` }
}
</script>

<template>
  <aside class="qd-outline">
    <header class="qd-outline-head">
      <span class="qd-outline-title">{{ t('outlineTitle') }}</span>
      <button
        type="button"
        class="qd-outline-close"
        :title="t('close')"
        :aria-label="t('close')"
        @click="emit('close')"
      >
        <X :size="12" />
      </button>
    </header>

    <div class="qd-outline-tools">
      <button type="button" class="qd-outline-tool" @click="collapseAll">{{ t('outlineCollapseAll') }}</button>
      <button type="button" class="qd-outline-tool" @click="expandAll">{{ t('outlineExpandAll') }}</button>
    </div>

    <div v-if="!headings.length" class="qd-outline-empty">{{ t('outlineEmpty') }}</div>

    <ul v-else class="qd-outline-list" :aria-label="t('outlineTitle')">
      <li
        v-for="h in headings"
        :key="h.line"
        class="qd-outline-item"
        :class="{ 'is-active': h.line === activeLine }"
      >
        <!-- 层级是运行时数据，缩进用内联 style（见 indentStyle 注释） -->
        <div class="qd-outline-row" :style="indentStyle(h.level)">
          <button
            type="button"
            class="qd-outline-caret"
            :title="isFolded(h.line) ? t('outlineUnfold') : t('outlineFold')"
            :aria-label="isFolded(h.line) ? t('outlineUnfold') : t('outlineFold')"
            @click="toggleFold(h.line)"
          >
            <ChevronRight v-if="isFolded(h.line)" :size="12" />
            <ChevronDown v-else :size="12" />
          </button>
          <span class="qd-outline-lv" :title="t('outlineLevel')">{{ h.level }}</span>
          <button
            type="button"
            class="qd-outline-text"
            :title="h.text"
            @click="emit('jump', h.line)"
          >{{ h.text }}</button>
        </div>
      </li>
    </ul>
  </aside>
</template>

<style scoped>
.qd-outline {
  display: flex;
  flex-direction: column;
  height: 100%;
  min-height: 0;
  overflow: hidden;
  background: var(--color-bg-secondary);
  border-left: 1px solid var(--color-border);
  color: var(--color-text-secondary);
  font-size: var(--text-sm);
}

.qd-outline-head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex: none;
  padding: var(--space-4) var(--space-5);
  border-bottom: 1px solid var(--color-border);
}

.qd-outline-title {
  flex: 1;
  min-width: 0;
  font-size: var(--text-md);
  font-weight: 600;
  color: var(--color-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.qd-outline-close {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 20px;
  height: 20px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.qd-outline-close:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.qd-outline-tools {
  display: flex;
  gap: var(--space-2);
  flex: none;
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--color-border);
}

.qd-outline-tool {
  flex: 1;
  min-width: 0;
  padding: var(--space-2) var(--space-3);
  border: 1px solid var(--color-border);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
  color: var(--color-text-secondary);
  font-family: inherit;
  font-size: var(--text-xs);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.qd-outline-tool:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.qd-outline-empty {
  padding: var(--space-8) var(--space-5);
  color: var(--color-text-muted);
  font-size: var(--text-xs);
  line-height: 1.6;
  text-align: center;
}

.qd-outline-list {
  flex: 1;
  min-height: 0;
  margin: 0;
  padding: var(--space-2) 0;
  list-style: none;
  overflow-y: auto;
}

.qd-outline-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding-top: var(--space-1);
  padding-right: var(--space-4);
  padding-bottom: var(--space-1);
  /* padding-left 由模板 :style（indentStyle）按层级动态给，公式里含基础 --space-3 */
  border-radius: var(--radius-xs);
  transition: background-color var(--transition-fast);
}
.qd-outline-row:hover { background: var(--color-bg-hover); }

/* active 规则写在 hover 之后：当前标题的高亮不该被鼠标划过冲掉 */
.qd-outline-item.is-active .qd-outline-row { background: var(--color-accent-bg); }
.qd-outline-item.is-active .qd-outline-text { color: var(--color-accent); }

.qd-outline-caret {
  flex: none;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  padding: 0;
  border: none;
  border-radius: var(--radius-xs);
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
}
.qd-outline-caret:hover {
  background: var(--color-bg-active);
  color: var(--color-text-primary);
}

.qd-outline-lv {
  flex: none;
  min-width: 12px;
  color: var(--color-text-disabled);
  font-size: var(--text-2xs);
  font-variant-numeric: tabular-nums;
  text-align: center;
}

.qd-outline-text {
  flex: 1;
  min-width: 0;
  padding: 0;
  border: none;
  background: transparent;
  color: inherit;
  font-family: inherit;
  font-size: inherit;
  text-align: left;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: pointer;
}
</style>
