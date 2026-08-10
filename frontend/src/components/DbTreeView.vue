<script setup lang="ts">
import { inject } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Database, Table as TableIcon, Key as KeyIcon, Folder, ChevronRight,
} from '@lucide/vue'
import type { DbTreeNode } from '../types'

const props = withDefaults(defineProps<{
  nodes: DbTreeNode[]
  depth?: number
  parentDb?: string
}>(), { depth: 0, parentDb: '' })

const { t } = useI18n()

interface DbTreeCtx {
  isOpen: (k: string) => boolean
  toggleKey: (k: string) => void
  openTable: (node: DbTreeNode, parentDb: string) => void
  insertColumn: (name: string) => void
  loadDbObjects: (dbName: string) => Promise<void>
}
const ctx = inject<DbTreeCtx>('dbTreeCtx')!

function nodeKey(n: DbTreeNode) {
  return n.kind + ':' + (props.parentDb ? props.parentDb + '/' : '') + n.name
}

function folderLabel(name: string) {
  if (name === 'tables') return t('dbTreeTables')
  if (name === 'views') return t('dbTreeViews')
  if (name === 'keys') return t('dbTreeKeys')
  return name
}

function onToggle(node: DbTreeNode) {
  ctx.toggleKey(nodeKey(node))
}

// 展开库节点时按需加载其表结构（避免一次性拉取所有库）
async function onToggleDb(node: DbTreeNode) {
  const key = nodeKey(node)
  const willOpen = !ctx.isOpen(key)
  ctx.toggleKey(key)
  if (willOpen && (!node.children || node.children.length === 0)) {
    await ctx.loadDbObjects(node.name)
  }
}
</script>

<template>
  <div class="tree-nodes">
    <template v-for="node in nodes" :key="nodeKey(node)">
      <!-- 库层（MySQL 的 SHOW DATABASES / SQLite 的 main） -->
      <div v-if="node.kind === 'database'" class="tree-row db" @click="onToggleDb(node)">
        <ChevronRight :size="12" class="tw" :class="{ open: ctx.isOpen(nodeKey(node)) }" />
        <Database :size="13" class="tw-icon" />
        <span class="tw-name" :title="node.name">{{ node.name }}</span>
        <span v-if="node.children?.length" class="tw-count">{{ node.children.length }}</span>
        <span v-else class="tw-count pending">…</span>
      </div>

      <!-- 文件夹（tables / views / keys） -->
      <div v-else-if="node.kind === 'folder'" class="tree-row" @click="onToggle(node)">
        <ChevronRight :size="12" class="tw" :class="{ open: ctx.isOpen(nodeKey(node)) }" />
        <Folder :size="13" class="tw-icon" />
        <span class="tw-name">{{ folderLabel(node.name) }}</span>
        <span v-if="node.children?.length" class="tw-count">{{ node.children.length }}</span>
      </div>

      <!-- 表 / 视图 -->
      <div v-else-if="node.kind === 'table' || node.kind === 'view'" class="tree-row tbl">
        <div class="tw-toggle" @click.stop="onToggle(node)">
          <ChevronRight :size="12" class="tw" :class="{ open: ctx.isOpen(nodeKey(node)) }" />
        </div>
        <TableIcon :size="13" class="tw-icon" />
        <span class="tw-name" :title="node.name" @click="ctx.openTable(node, parentDb)">{{ node.name }}</span>
        <span v-if="node.kind === 'view'" class="tw-badge">V</span>
      </div>

      <!-- Redis 键 -->
      <div v-else-if="node.kind === 'key'" class="tree-row tbl" @click="ctx.openTable(node, parentDb)">
        <span class="tw-toggle" />
        <KeyIcon :size="13" class="tw-icon" />
        <span class="tw-name" :title="node.name">{{ node.name }}</span>
        <span class="tw-type">{{ node.detail }}</span>
      </div>

      <!-- 字段 -->
      <div v-else-if="node.kind === 'column'" class="tree-row col" @click="ctx.insertColumn(node.name)">
        <span class="tw-toggle" />
        <span class="col-name" :title="node.name">{{ node.name }}</span>
        <span class="col-type">{{ node.detail }}</span>
      </div>

      <!-- 递归子节点 -->
      <div v-if="node.children?.length && ctx.isOpen(nodeKey(node))" class="tree-children">
        <DbTreeView
          :nodes="node.children"
          :depth="depth + 1"
          :parent-db="node.kind === 'database' ? node.name : parentDb"
        />
      </div>
    </template>
  </div>
</template>

<style scoped>
.tree-nodes { font-size: 12px; }
.tree-row { display: flex; align-items: center; gap: 4px; padding: 4px 6px; border-radius: 5px; cursor: pointer; font-size: 12px; color: var(--color-text-secondary); }
.tree-row:hover { background: var(--color-bg-hover); }
.tree-row.db { font-weight: 600; color: var(--color-text-primary); }
.tw { color: var(--color-text-disabled); flex-shrink: 0; transition: transform var(--transition-fast); }
.tw.open { transform: rotate(90deg); }
.tw-icon { color: var(--color-text-muted); flex-shrink: 0; }
.tw-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tw-name:hover { color: var(--color-accent); text-decoration: underline; }
.tw-count { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; }
.tw-count.pending { opacity: 0.6; }
.tw-badge { font-size: 9px; font-weight: 700; color: var(--color-text-disabled); border: 1px solid var(--color-border); border-radius: 3px; padding: 0 4px; }
.tw-type { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; }
.tw-toggle { width: 12px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; }
.tree-children { margin-left: 8px; border-left: 1px solid var(--color-border); padding-left: 4px; }
.tree-row.tbl { padding-left: 2px; }
.tree-row.col { cursor: pointer; }
.col-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: 'Consolas', 'Monaco', monospace; font-size: 11px; }
.col-name:hover { color: var(--color-accent); }
.col-type { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; font-family: 'Consolas', 'Monaco', monospace; }
</style>
