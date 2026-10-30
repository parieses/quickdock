<script setup lang="ts">
// 工具改动的行级 diff 视图（方案 A2）。
//
// 为什么单独一个组件：这段渲染要在「历史消息卡片」和「流式卡片」两处用。
// 复制一份的话，颜色与折叠行为迟早两边不一致——而「同一个东西两处长得不一样」
// 是那种没人会报 bug、但每次看到都别扭的问题。
//
// 用 {{ }} 插值而不是 v-html：diff 内容来自被改的文件，改一个 .html 时里面
// 就是货真价实的标签，v-html 会把它们当成元素插进面板。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ diff: string }>()
const { t } = useI18n()

// 折叠阈值：一屏大约 12 行，再多就把卡片撑长、把正文挤出视野。
const COLLAPSED_LINES = 12
const expanded = ref(false)

type DiffLineKind = 'add' | 'del' | 'ctx' | 'hunk'
interface DiffLine { kind: DiffLineKind; text: string }

const lines = computed<DiffLine[]>(() => {
  const out: DiffLine[] = []
  for (const raw of (props.diff || '').split('\n')) {
    if (!raw) continue
    if (raw.startsWith('@@')) {
      // hunk 头：单独一行小字，它不是文件内容。
      out.push({ kind: 'hunk', text: raw })
    } else if (raw.startsWith('+')) {
      out.push({ kind: 'add', text: raw.slice(1) })
    } else if (raw.startsWith('-')) {
      out.push({ kind: 'del', text: raw.slice(1) })
    } else {
      // 上下文行后端写的是「一个空格 + 原文」。理论上一定带空格，
      // 但截断或手工构造的 diff 可能没有——少切一个字符不影响阅读，别为此丢行。
      out.push({ kind: 'ctx', text: raw.startsWith(' ') ? raw.slice(1) : raw })
    }
  }
  return out
})

const shown = computed(() => (expanded.value ? lines.value : lines.value.slice(0, COLLAPSED_LINES)))
const hiddenCount = computed(() => Math.max(0, lines.value.length - COLLAPSED_LINES))

const signOf = (k: DiffLineKind): string => (k === 'add' ? '+' : k === 'del' ? '-' : k === 'ctx' ? ' ' : '')
</script>

<template>
  <div class="qd-diff">
    <div v-for="(l, i) in shown" :key="i" class="qd-diff-line" :class="'is-' + l.kind">
      <span class="qd-diff-sign">{{ signOf(l.kind) }}</span>
      <span class="qd-diff-text">{{ l.text }}</span>
    </div>
    <!-- 「展开」只在真的超长时出现：短 diff 也挂一个按钮，会让人以为下面还有内容。 -->
    <button v-if="hiddenCount > 0 && !expanded" class="qd-diff-more" @click="expanded = true">
      {{ t('aiDiffExpand', { n: lines.length }) }}
    </button>
  </div>
</template>
