<script setup lang="ts">
// 通用「另存为 / 加入知识库」菜单：全局单实例，由 useSaveAs 的模块级 state 驱动。
// 任何页面调 sa.open(payload, at) 即可在当前鼠标位置弹出，不用各自渲染菜单。
// 定位用 useFloatMenu（自动 flip/shift 防溢出），样式复用项目 token，不造新外壳。

import { computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { FilePlus, ListTodo, BookMarked } from '@lucide/vue'
import { useSaveAs } from '../composables/useSaveAs'

const { t } = useI18n()
const { state, visible, floatingRef, floatingStyles, close, saveToNote, saveToTodo, addToKB } = useSaveAs()

const payload = computed(() => state.value)
/** 有磁盘路径时"加入知识库"走文件管线；否则走文本管线（两种都支持，文案一样）。 */
function run(target: 'note' | 'todo' | 'kb') {
  const p = state.value
  if (!p) return
  const text = (p.text || '').trim()
  const title = p.title || p.sourceMeta?.label
  if (target === 'note') {
    void saveToNote({ content: text, title, tags: p.tags })
  } else if (target === 'todo') {
    void saveToTodo({ content: text, title, tags: p.tags })
  } else {
    void addToKB({ paths: p.sourceMeta?.paths, text, name: title })
  }
  close()
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && visible.value) close()
}
onMounted(() => document.addEventListener('keydown', onKey))
onUnmounted(() => document.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <div
      v-if="visible && payload"
      ref="floatingRef"
      class="sa-menu"
      :style="floatingStyles"
      @click.stop
      @contextmenu.prevent.stop
    >
      <div class="sa-head">{{ t('saveAsTitle') }}</div>
      <button class="sa-item" @click="run('note')">
        <FilePlus :size="15" /> <span>{{ t('saveAsNote') }}</span>
      </button>
      <button class="sa-item" @click="run('todo')">
        <ListTodo :size="15" /> <span>{{ t('saveAsTodo') }}</span>
      </button>
      <button class="sa-item" @click="run('kb')">
        <BookMarked :size="15" /> <span>{{ t('saveAsKB') }}</span>
      </button>
    </div>
  </Teleport>
</template>

<style scoped>
.sa-menu {
  z-index: var(--z-context);
  min-width: 148px;
  padding: var(--space-1);
  border: 1px solid var(--popover-border-color);
  border-radius: var(--radius-md);
  background: var(--color-popover-bg);
  box-shadow: var(--shadow-pop);
}
.sa-head {
  padding: var(--space-2) var(--space-3);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
  border-bottom: 1px solid var(--popover-border-color);
  margin-bottom: var(--space-1);
}
.sa-item {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  padding: var(--space-2) var(--space-3);
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--color-text-primary);
  font-size: var(--text-sm);
  text-align: left;
  cursor: pointer;
}
.sa-item:hover { background: var(--color-bg-hover); }
.sa-item svg { color: var(--color-text-secondary); flex: none; }
</style>
