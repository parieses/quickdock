<script setup lang="ts">
import { ref, reactive, computed, onMounted, inject, provide } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Database, Plus, Trash2, Save, Play, Plug, X, RefreshCw, History,
  Copy, Download, Check,
} from '@lucide/vue'
import {
  ListDbConnections, SaveDbConnection, DeleteDbConnection, TestDbConnection,
  QueryDbConnection, ListDbTree, ListDbObjects, UpdateDbRow,
} from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { DbConnection, DbQueryResult, DbTreeNode, ToastAPI } from '../types'
import DbTreeView from './DbTreeView.vue'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const connections = ref<DbConnection[]>([])
const activeId = ref<string | null>(null)
const activeConn = ref<DbConnection | null>(null)

// ---- 库表树 ----
const tree = ref<DbTreeNode[]>([])
const treeLoading = ref(false)
const treeError = ref('')
const expandedKeys = ref<Set<string>>(new Set())

// ---- 连接配置弹窗 ----
const showConfig = ref(false)
const configMode = ref<'new' | 'edit'>('new')
const form = ref({
  name: '', dbType: 'mysql', host: '127.0.0.1', port: 3306,
  username: '', password: '', database: '', filePath: '',
})
const testing = ref(false)
const testOk = ref<boolean | null>(null)

// ---- 查询 ----
const query = ref('')
const running = ref(false)
const result = ref<DbQueryResult | null>(null)
const editorHeight = ref(190)

// ---- 内联编辑（Navicat 风格）----
// edits: "行:列" -> 新值（字符串）；nullEdits: 显式置 NULL 的单元格键
const edits = reactive<Record<string, string>>({})
const nullEdits = reactive<Set<string>>(new Set())
const committing = ref(false)

function cellKey(ri: number, ci: number) { return ri + ':' + ci }
function cellValue(ri: number, ci: number): string {
  const k = cellKey(ri, ci)
  if (nullEdits.has(k)) return ''
  if (k in edits) return edits[k]
  return isNull(ri, ci) ? '' : (result.value?.rows[ri][ci] ?? '')
}
function cellDirty(ri: number, ci: number): boolean {
  const k = cellKey(ri, ci)
  return k in edits || nullEdits.has(k)
}
function onCellInput(ri: number, ci: number, e: Event) {
  const k = cellKey(ri, ci)
  edits[k] = (e.target as HTMLInputElement).value
  nullEdits.delete(k)
}
function toggleNull(ri: number, ci: number) {
  const k = cellKey(ri, ci)
  if (nullEdits.has(k)) nullEdits.delete(k)
  else { nullEdits.add(k); delete edits[k] }
}
function clearEdits() {
  for (const k of Object.keys(edits)) delete edits[k]
  nullEdits.clear()
}
const hasChanges = computed(() => Object.keys(edits).length > 0 || nullEdits.size > 0)
const pkIndex = computed(() => {
  if (!result.value?.editable || !result.value.primaryKey) return -1
  return result.value.columns.indexOf(result.value.primaryKey)
})
async function confirmChanges() {
  if (!result.value?.editable || !activeId.value || !hasChanges.value || committing.value) return
  const res = result.value
  const pkCol = res.primaryKey!
  const dirtyRows = new Set<number>()
  for (const k of [...Object.keys(edits), ...nullEdits]) dirtyRows.add(parseInt(k.split(':')[0], 10))
  committing.value = true
  const errs: string[] = []
  let count = 0
  try {
    for (const ri of dirtyRows) {
      const pkVal = res.rows[ri][pkIndex.value]
      const sets: Record<string, string> = {}
      const nulls: string[] = []
      for (let ci = 0; ci < res.columns.length; ci++) {
        const k = cellKey(ri, ci)
        const col = res.columns[ci]
        if (nullEdits.has(k)) nulls.push(col)
        else if (k in edits) sets[col] = edits[k]
      }
      if (Object.keys(sets).length === 0 && nulls.length === 0) continue
      try {
        unwrap(await UpdateDbRow(activeId.value, { tableName: res.tableName!, pkColumn: pkCol, pkValue: pkVal, sets, nulls }))
        count++
      } catch (e) {
        errs.push(getErrorMessage(e))
      }
    }
  } finally {
    committing.value = false
  }
  if (errs.length) {
    toast.error(t('dbCommitFailed', { msg: errs.join('; ') }))
  } else {
    toast.success(t('dbCommitted', { n: count }))
    clearEdits()
    await runQuery()
  }
}
function discardChanges() { clearEdits() }

// ---- 历史 ----
const showHistory = ref(false)
const history = ref<string[]>([])

const dbTypes = ['mysql', 'redis', 'sqlite']
const isSqlite = computed(() => form.value.dbType === 'sqlite')
const isRedis = computed(() => form.value.dbType === 'redis')
const queryPlaceholder = computed(() => isRedis.value ? t('dbRedisPlaceholder') : t('dbQueryPlaceholder'))

const statusClass = computed(() => {
  if (!result.value || !result.value.success) return 'err'
  if (result.value.rowCount > 0 || result.value.affected > 0 || result.value.message) return 'ok'
  return 'warn'
})

function isOpen(key: string) { return expandedKeys.value.has(key) }
function toggleKey(key: string) {
  const s = new Set(expandedKeys.value)
  if (s.has(key)) s.delete(key); else s.add(key)
  expandedKeys.value = s
}
// 切换数据库类型时自动填默认端口
function onTypeChange() {
  if (form.value.dbType === 'mysql') form.value.port = 3306
  else if (form.value.dbType === 'redis') {
    form.value.port = 6379
    if (!form.value.database) form.value.database = '0'
  } else form.value.port = 0
}

async function load() {
  try {
    connections.value = unwrap<DbConnection[]>(await ListDbConnections()) ?? []
    if (connections.value.length && !activeId.value) {
      await selectConn(connections.value[0])
    }
  } catch (e) {
    toast.error(t('loadFailed') + ': ' + getErrorMessage(e))
  }
}

function resetForm() {
  form.value = {
    name: '', dbType: 'mysql', host: '127.0.0.1', port: 3306,
    username: '', password: '', database: '', filePath: '',
  }
  testOk.value = null
}

async function selectConn(c: DbConnection) {
  activeId.value = c.id
  activeConn.value = c
  result.value = null
  expandedKeys.value = new Set()
  await loadTree()
  loadHistory()
}

function newConn() {
  configMode.value = 'new'
  resetForm()
  showConfig.value = true
}

function editConn(c: DbConnection) {
  configMode.value = 'edit'
  form.value = {
    name: c.name, dbType: c.dbType, host: c.host, port: c.port,
    username: c.username, password: '', database: c.database, filePath: c.filePath,
  }
  testOk.value = null
  showConfig.value = true
}

async function saveConn() {
  const input = {
    id: configMode.value === 'edit' ? (activeId.value ?? '') : '',
    name: form.value.name, dbType: form.value.dbType, host: form.value.host,
    port: form.value.port, username: form.value.username, password: form.value.password,
    database: form.value.database, filePath: form.value.filePath,
  }
  try {
    const r = unwrap<DbConnection>(await SaveDbConnection(input))
    toast.success(t('dbSaved'))
    showConfig.value = false
    await load()
    if (r) await selectConn(r)
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}

async function testConn() {
  testing.value = true
  testOk.value = null
  const input = {
    id: '', name: form.value.name, dbType: form.value.dbType, host: form.value.host,
    port: form.value.port, username: form.value.username, password: form.value.password,
    database: form.value.database, filePath: form.value.filePath,
  }
  try {
    const r = unwrap<{ ok: boolean; error?: string }>(await TestDbConnection(input))
    testOk.value = !!r?.ok
    if (!r?.ok) toast.error(t('dbTestFail') + (r?.error ? ': ' + r.error : ''))
    else toast.success(t('dbTestOk'))
  } catch (e) {
    testOk.value = false
    toast.error(t('dbTestFail') + ': ' + getErrorMessage(e))
  } finally {
    testing.value = false
  }
}

async function loadTree() {
  if (!activeId.value) { tree.value = []; return }
  treeLoading.value = true
  treeError.value = ''
  try {
    tree.value = unwrap<DbTreeNode[]>(await ListDbTree(activeId.value)) ?? []
  } catch (e) {
    treeError.value = getErrorMessage(e)
  } finally {
    treeLoading.value = false
  }
}

function openTable(node: DbTreeNode, parentDb = '') {
  const tp = activeConn.value?.dbType
  if (tp === 'redis') {
    query.value = 'GET ' + node.name
  } else if (tp === 'mysql') {
    const ref = parentDb ? '`' + parentDb + '`.`' + node.name + '`' : '`' + node.name + '`'
    query.value = 'SELECT * FROM ' + ref + ' LIMIT 100'
  } else {
    query.value = 'SELECT * FROM "' + node.name + '" LIMIT 100'
  }
  runQuery()
}

// 展开某个库时按需加载其表结构（MySQL 库较多时避免一次性拉全）
async function loadDbObjects(dbName: string) {
  if (!activeId.value) return
  const objs = unwrap<DbTreeNode[]>(await ListDbObjects(activeId.value, dbName)) ?? []
  const target = tree.value.find((n) => n.kind === 'database' && n.name === dbName)
  if (target) {
    target.children = objs
  }
}

// 提供给递归树组件的上下文
provide('dbTreeCtx', {
  isOpen,
  toggleKey,
  openTable,
  insertColumn,
  loadDbObjects,
})

function insertColumn(name: string) {
  query.value = query.value.trim() ? query.value.trim() + ', ' + name : name
}

async function runQuery() {
  if (!activeId.value) { toast.error(t('dbConnRequired')); return }
  if (!query.value.trim()) return
  running.value = true
  result.value = null
  clearEdits()
  try {
    const r = unwrap<DbQueryResult>(await QueryDbConnection(activeId.value, query.value))
    result.value = r ?? null
    if (r && !r.success) toast.error(t('dbTestFail') + (r.error ? ': ' + r.error : ''))
    else pushHistory(query.value.trim())
  } catch (e) {
    toast.error(getErrorMessage(e))
  } finally {
    running.value = false
  }
}

async function remove(c: DbConnection) {
  if (!(await toast.confirm(t('dbConfirmDeleteConn')))) return
  try {
    await DeleteDbConnection(c.id)
    if (activeId.value === c.id) {
      activeId.value = null
      activeConn.value = null
      tree.value = []
      result.value = null
    }
    await load()
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}

// ---- 历史 ----
function histKey() { return 'qd_db_hist_' + (activeId.value ?? '') }
function loadHistory() {
  try { history.value = JSON.parse(localStorage.getItem(histKey()) || '[]') } catch { history.value = [] }
}
function pushHistory(q: string) {
  history.value = [q, ...history.value.filter((h) => h !== q)].slice(0, 30)
  try { localStorage.setItem(histKey(), JSON.stringify(history.value)) } catch { /* ignore */ }
}
function useHistory(q: string) { query.value = q; showHistory.value = false }

// ---- 结果网格 ----
function isNull(ri: number, ci: number) { return !!result.value?.nulls?.[ri]?.[ci] }
async function copyCell(v: string) {
  try { await navigator.clipboard.writeText(v); toast.success(t('copied')) }
  catch { toast.error(t('copyFailed')) }
}
function csvEscape(v: string | null) {
  if (v == null) return ''
  if (/[",\n\r]/.test(v)) return '"' + v.replace(/"/g, '""') + '"'
  return v
}
function toCsv(res: DbQueryResult) {
  const lines = [res.columns.map((c) => csvEscape(c)).join(',')]
  res.rows.forEach((row, ri) => {
    lines.push(row.map((c, ci) => csvEscape(isNull(ri, ci) ? null : c)).join(','))
  })
  return lines.join('\n')
}
async function copyCsv() {
  if (!result.value?.columns?.length) return
  try { await navigator.clipboard.writeText(toCsv(result.value)); toast.success(t('copied')) }
  catch { toast.error(t('copyFailed')) }
}
function exportCsv() {
  if (!result.value?.columns?.length) return
  const blob = new Blob(['﻿' + toCsv(result.value)], { type: 'text/csv;charset=utf-8;' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'query_result_' + Date.now() + '.csv'
  a.click()
  URL.revokeObjectURL(url)
}

// ---- 编辑器高度拖拽 ----
const mainCol = ref<HTMLElement | null>(null)
let dragging = false
function startDrag(e: MouseEvent) {
  dragging = true
  document.addEventListener('mousemove', onDrag)
  document.addEventListener('mouseup', stopDrag)
  e.preventDefault()
}
function onDrag(e: MouseEvent) {
  if (!dragging || !mainCol.value) return
  const rect = mainCol.value.getBoundingClientRect()
  let h = e.clientY - rect.top
  h = Math.max(90, Math.min(h, rect.height - 160))
  editorHeight.value = h
}
function stopDrag() {
  dragging = false
  document.removeEventListener('mousemove', onDrag)
  document.removeEventListener('mouseup', stopDrag)
}

// ---- 左侧两栏宽度拖拽（连接列表 / 库表树） ----
const connWidth = ref(230)
const treeWidth = ref(250)
let colDrag: null | { target: 'conn' | 'tree'; startX: number; startW: number } = null
function startColDrag(target: 'conn' | 'tree', e: MouseEvent) {
  colDrag = { target, startX: e.clientX, startW: target === 'conn' ? connWidth.value : treeWidth.value }
  document.addEventListener('mousemove', onColDrag)
  document.addEventListener('mouseup', stopColDrag)
  e.preventDefault()
}
function onColDrag(e: MouseEvent) {
  if (!colDrag) return
  const w = Math.max(160, Math.min(colDrag.startW + (e.clientX - colDrag.startX), 460))
  if (colDrag.target === 'conn') connWidth.value = w
  else treeWidth.value = w
}
function stopColDrag() {
  colDrag = null
  document.removeEventListener('mousemove', onColDrag)
  document.removeEventListener('mouseup', stopColDrag)
}

onMounted(load)
</script>

<template>
  <div class="db-page">
    <!-- 左：连接列表 -->
    <div class="conn-list" :style="{ width: connWidth + 'px' }">
      <div class="panel-head">
        <span>{{ t('dbConnections') }}</span>
        <button class="icon-btn" :title="t('dbNewConn')" @click="newConn"><Plus :size="15" /></button>
      </div>
      <div v-if="connections.length" class="conn-items">
        <div
          v-for="c in connections"
          :key="c.id"
          :class="['conn-item', { active: c.id === activeId }]"
          @click="selectConn(c)"
          @dblclick="editConn(c)"
        >
          <Database :size="13" class="conn-icon" />
          <div class="conn-meta">
            <span class="conn-name" :title="c.name || c.host">{{ c.name || c.host }}</span>
            <span class="conn-sub">{{ c.dbType }}{{ c.host ? ' · ' + c.host : '' }}</span>
          </div>
          <button class="conn-del" :title="t('delete')" @click.stop="remove(c)"><Trash2 :size="12" /></button>
        </div>
      </div>
        <div v-else class="conn-empty"><p>{{ t('httpNoRequest') }}</p></div>
    </div>
    <div class="vsplit" @mousedown="startColDrag('conn', $event)" />

    <!-- 中：库表浏览器树 -->
    <div class="tree-col" :style="{ width: treeWidth + 'px' }">
      <div class="panel-head">
        <span>{{ activeConn ? activeConn.name || activeConn.host : t('dbTreeTables') }}</span>
        <button class="icon-btn" :title="t('dbRefresh')" :disabled="!activeId || treeLoading" @click="loadTree">
          <RefreshCw :size="14" :class="{ spinning: treeLoading }" />
        </button>
      </div>
      <div class="tree-body">
        <p v-if="!activeId" class="tree-hint">{{ t('dbSelectConnHint') }}</p>
        <p v-else-if="treeError" class="tree-hint err">{{ t('dbTreeError') }}: {{ treeError }}</p>
        <p v-else-if="treeLoading" class="tree-hint">{{ t('loading') }}</p>
        <p v-else-if="!tree.length" class="tree-hint">{{ t('dbNoObjects') }}</p>
        <template v-else>
          <p class="tree-tip">{{ t('dbClickTableHint') }}</p>
          <DbTreeView :nodes="tree" />
        </template>
      </div>
    </div>
    <div class="vsplit" @mousedown="startColDrag('tree', $event)" />

    <!-- 右：编辑器 + 结果 -->
    <div class="main-col" ref="mainCol">
      <div class="conn-bar">
        <span class="conn-bar-name">
          <Database :size="13" />
          {{ activeConn ? activeConn.name || activeConn.host : t('dbConnRequired') }}
        </span>
        <div class="conn-bar-actions">
          <button class="bar-btn" :disabled="!activeId" @click="loadTree"><RefreshCw :size="13" /> {{ t('dbRefresh') }}</button>
          <button class="bar-btn" :disabled="!activeId" @click="editConn(activeConn!)"><Plug :size="13" /> {{ t('dbEditConn') }}</button>
          <div class="hist-wrap">
            <button class="bar-btn" :disabled="!activeId" @click="showHistory = !showHistory"><History :size="13" /> {{ t('dbHistory') }}</button>
            <div v-if="showHistory && activeId" class="hist-pop">
              <div v-if="!history.length" class="hist-empty">{{ t('dbNoHistory') }}</div>
              <div v-for="(h, i) in history" :key="i" class="hist-item" @click="useHistory(h)">
                <span class="hist-text">{{ h }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 查询编辑器 -->
      <div class="editor-pane" :style="{ height: editorHeight + 'px' }">
        <div class="editor-toolbar">
          <button class="run-btn" :disabled="running || !activeId" @click="runQuery">
            <Play :size="13" /> <span>{{ running ? t('loading') : t('dbRun') }}</span>
          </button>
          <span class="kbd-hint">Ctrl/⌘ + Enter</span>
        </div>
        <textarea
          v-model="query"
          class="query-input"
          :placeholder="queryPlaceholder"
          @keydown.ctrl.enter="runQuery"
          @keydown.meta.enter="runQuery"
        ></textarea>
      </div>
      <div class="splitter" @mousedown="startDrag" />

      <!-- 结果区 -->
      <div class="result-pane">
        <div v-if="result" class="result-head">
          <span :class="['status-badge', statusClass]">
            {{ result.success
              ? (result.rowCount > 0 ? result.rowCount + ' ' + t('dbRows')
                : (result.affected > 0 ? result.affected + ' ' + t('dbAffected') : t('dbTestOk')))
              : t('dbTestFail') }}
          </span>
          <span class="resp-meta">{{ t('httpDuration') }}: {{ result.durationMs }} ms</span>
          <div class="result-actions">
            <button v-if="result.columns?.length" class="r-btn" :title="t('dbCopyCsv')" @click="copyCsv"><Copy :size="12" /> {{ t('dbCopyCsv') }}</button>
            <button v-if="result.columns?.length" class="r-btn" :title="t('dbExportCsv')" @click="exportCsv"><Download :size="12" /> {{ t('dbExportCsv') }}</button>
          </div>
        </div>
        <div class="result-body">
          <!-- 只读原因（不可编辑时提示，便于排查） -->
          <div v-if="result && result.success && result.columns?.length && !result.editable && result.editReason" class="edit-reason">
            {{ t('dbReadonlyReason', { reason: result.editReason }) }}
          </div>
          <!-- 表格结果 -->
          <table v-if="result && result.success && result.columns?.length" class="result-table">
            <thead>
              <tr>
                <th class="rownum">#</th>
                <th v-for="col in result.columns" :key="col">{{ col }}</th>
              </tr>
            </thead>
            <tbody v-if="!result.editable">
              <tr v-for="(row, ri) in result.rows" :key="ri">
                <td class="rownum">{{ ri + 1 }}</td>
                <td
                  v-for="(cell, ci) in row"
                  :key="ci"
                  :class="{ 'is-null': isNull(ri, ci) }"
                >
                  <span class="cell-text">{{ isNull(ri, ci) ? t('dbNull') : cell }}</span>
                  <button class="cell-copy" :title="t('dbCopyCell')" @click="copyCell(isNull(ri, ci) ? '' : cell)"><Copy :size="11" /></button>
                </td>
              </tr>
            </tbody>
            <!-- 可编辑网格：单表 SELECT 且含主键列 -->
            <tbody v-else>
              <tr v-for="(row, ri) in result.rows" :key="ri">
                <td class="rownum">{{ ri + 1 }}</td>
                <td
                  v-for="(cell, ci) in row"
                  :key="ci"
                  :class="['editable-cell', { dirty: cellDirty(ri, ci), 'is-null': (isNull(ri, ci) && !(cellKey(ri, ci) in edits)) || nullEdits.has(cellKey(ri, ci)) }]"
                >
                  <input
                    class="cell-input"
                    :disabled="nullEdits.has(cellKey(ri, ci))"
                    :value="cellValue(ri, ci)"
                    @input="onCellInput(ri, ci, $event)"
                  />
                  <button class="cell-null-btn" :class="{ active: nullEdits.has(cellKey(ri, ci)) }" :title="t('dbSetNull')" @click="toggleNull(ri, ci)">∅</button>
                </td>
              </tr>
            </tbody>
          </table>
          <!-- 非表格结果（消息 / Redis） -->
          <pre v-else-if="result && result.success" class="result-pre">{{ result.message || t('dbNoResult') }}</pre>
          <!-- 错误 -->
          <pre v-else-if="result" class="result-pre err">{{ result.error || t('dbTestFail') }}</pre>
          <!-- 空态 -->
          <div v-else class="result-empty"><p>{{ t('dbSelectConnHint') }}</p></div>
        </div>
        <!-- 可编辑网格底部操作栏 -->
        <div v-if="result && result.editable" class="grid-footer">
          <span class="grid-hint">{{ t('dbEditableHint', { table: result.tableName, pk: result.primaryKey }) }}</span>
          <span v-if="hasChanges" class="grid-hint changed">· {{ Object.keys(edits).length + nullEdits.size }} {{ t('dbChangedCells') }}</span>
          <div class="spacer" />
          <button class="r-btn" :disabled="!hasChanges || committing" @click="confirmChanges"><Check :size="12" /> {{ t('dbConfirm') }}</button>
          <button class="r-btn" :disabled="!hasChanges || committing" @click="discardChanges"><X :size="12" /> {{ t('dbDiscard') }}</button>
          <button class="r-btn" :disabled="committing" @click="runQuery"><RefreshCw :size="12" /> {{ t('dbRefresh') }}</button>
        </div>
      </div>
    </div>

    <!-- 连接配置弹窗 -->
    <div v-if="showConfig" class="modal-mask" @click.self="showConfig = false">
      <div class="modal">
        <div class="modal-head">
          <span>{{ configMode === 'edit' ? t('dbEditConn') : t('dbConnProps') }}</span>
          <button class="icon-btn" @click="showConfig = false"><X :size="15" /></button>
        </div>
        <div class="form-grid">
          <div class="field">
            <label>{{ t('dbName') }}</label>
            <input v-model="form.name" class="f-input" type="text" :placeholder="t('dbName')" />
          </div>
          <div class="field">
            <label>{{ t('dbType') }}</label>
            <select v-model="form.dbType" class="f-input" @change="onTypeChange">
              <option v-for="tp in dbTypes" :key="tp" :value="tp">{{ t('dbType' + tp.charAt(0).toUpperCase() + tp.slice(1)) }}</option>
            </select>
          </div>
          <template v-if="!isSqlite">
            <div class="field">
              <label>{{ t('dbHost') }}</label>
              <input v-model="form.host" class="f-input" type="text" placeholder="127.0.0.1" />
            </div>
            <div class="field field-sm">
              <label>{{ t('dbPort') }}</label>
              <input v-model.number="form.port" class="f-input" type="number" />
            </div>
            <div class="field">
              <label>{{ t('dbUsername') }}</label>
              <input v-model="form.username" class="f-input" type="text" :placeholder="t('dbUsername')" />
            </div>
            <div class="field">
              <label>{{ t('dbPassword') }}</label>
              <input v-model="form.password" class="f-input" type="password" :placeholder="configMode === 'edit' ? '••••••' : t('dbPassword')" />
            </div>
            <div v-if="form.dbType !== 'sqlite'" class="field">
              <label>{{ isRedis ? t('dbRedisDb') : t('dbDatabase') }}</label>
              <input v-model="form.database" class="f-input" type="text"
                :placeholder="isRedis ? t('dbRedisDbPlaceholder') : t('dbDatabase')" />
            </div>
          </template>
          <div v-if="isSqlite" class="field field-wide">
            <label>{{ t('dbFilePath') }}</label>
            <input v-model="form.filePath" class="f-input" type="text" placeholder="C:\\path\\to\\app.db" />
          </div>
        </div>
        <div class="modal-actions">
          <button class="test-btn" :disabled="testing" @click="testConn">
            <Plug :size="13" />
            <span>{{ testing ? t('loading') : t('dbTest') }}</span>
            <span v-if="testOk === true" class="dot ok" />
            <span v-else-if="testOk === false" class="dot err" />
          </button>
          <div class="spacer" />
          <button class="cancel-btn" @click="showConfig = false">{{ t('cancel') }}</button>
          <button class="save-btn" @click="saveConn"><Save :size="13" /> <span>{{ t('save') }}</span></button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.db-page { display: flex; height: 100%; overflow: hidden; }
.panel-head {
  display: flex; align-items: center; justify-content: space-between; padding: 10px 12px;
  font-size: 13px; font-weight: 600; color: var(--color-text-muted); text-transform: uppercase;
  letter-spacing: 0.5px; border-bottom: 1px solid var(--color-border); flex-shrink: 0;
}
.icon-btn {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center; width: 24px; height: 24px;
  border-radius: var(--radius-sm); transition: background-color var(--transition-fast), color var(--transition-fast);
}
.icon-btn:hover { color: var(--color-accent); background: var(--color-bg-hover); }
.icon-btn:disabled { opacity: 0.5; cursor: default; }

/* 连接列表 */
.conn-list {
  width: 230px; flex-shrink: 0; display: flex; flex-direction: column;
  background: var(--color-bg-secondary); border-right: 1px solid var(--color-border); overflow: hidden;
}
.conn-items { flex: 1; overflow-y: auto; padding: 6px; }
.conn-item {
  display: flex; align-items: center; gap: 8px; padding: 7px 8px; border-radius: 6px;
  cursor: pointer; color: var(--color-text-secondary); font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.conn-item:hover { background: var(--color-bg-hover); }
.conn-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.conn-icon { flex-shrink: 0; color: var(--color-text-muted); }
.conn-item.active .conn-icon { color: var(--color-accent); }
.conn-meta { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.conn-name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.conn-sub { font-size: 10px; color: var(--color-text-disabled); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.conn-del {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center; width: 18px; height: 18px;
  border-radius: 3px; opacity: 0; transition: opacity var(--transition-fast), color var(--transition-fast);
}
.conn-item:hover .conn-del { opacity: 1; }
.conn-del:hover { color: var(--color-danger); background: rgba(232, 76, 76, 0.1); }
.conn-empty { padding: 24px 12px; text-align: center; color: var(--color-text-disabled); font-size: 13px; }

/* 库表树 */
.tree-col { flex-shrink: 0; display: flex; flex-direction: column; border-right: 1px solid var(--color-border); overflow: hidden; }

/* 垂直分栏拖拽 */
.vsplit {
  width: 5px; flex-shrink: 0; cursor: col-resize; background: var(--color-border);
  transition: background var(--transition-fast); position: relative; z-index: 2;
}
.vsplit:hover, .vsplit:active { background: var(--color-accent); }
.tree-body { flex: 1; overflow-y: auto; padding: 6px; }
.tree-hint { padding: 10px; font-size: 12px; color: var(--color-text-disabled); line-height: 1.6; }
.tree-hint.err { color: var(--color-danger); }
.tree-tip { padding: 4px 8px 8px; font-size: 10px; color: var(--color-text-disabled); line-height: 1.5; }
.tree-folder { margin-bottom: 4px; }
.tree-row { display: flex; align-items: center; gap: 4px; padding: 4px 6px; border-radius: 5px; cursor: pointer; font-size: 13px; color: var(--color-text-secondary); }
.tree-row:hover { background: var(--color-bg-hover); }
.tw { color: var(--color-text-disabled); flex-shrink: 0; transition: transform var(--transition-fast); }
.tw.open { transform: rotate(90deg); }
.tw-icon { color: var(--color-text-muted); flex-shrink: 0; }
.tw-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.tw-name:hover { color: var(--color-accent); text-decoration: underline; }
.tw-count { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; }
.tw-badge { font-size: 9px; font-weight: 700; color: var(--color-text-disabled); border: 1px solid var(--color-border); border-radius: 3px; padding: 0 4px; }
.tw-type { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; }
.tw-toggle { width: 12px; flex-shrink: 0; display: flex; align-items: center; justify-content: center; }
.tree-children { margin-left: 8px; border-left: 1px solid var(--color-border); padding-left: 4px; }
.tree-row.tbl { padding-left: 2px; }
.tree-row.col { cursor: pointer; }
.col-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: 'Consolas', 'Monaco', monospace; font-size: 12px; }
.col-name:hover { color: var(--color-accent); }
.col-type { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; font-family: 'Consolas', 'Monaco', monospace; }

/* 主区域 */
.main-col { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; }
.conn-bar { display: flex; align-items: center; gap: 10px; padding: 8px 12px; border-bottom: 1px solid var(--color-border); flex-shrink: 0; }
.conn-bar-name { display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; color: var(--color-text-primary); flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.conn-bar-actions { display: flex; align-items: center; gap: 6px; }
.bar-btn {
  display: flex; align-items: center; gap: 4px; padding: 5px 9px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-size: 12px; font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.bar-btn:hover:not(:disabled) { background: var(--color-bg-hover); color: var(--color-text-primary); }
.bar-btn:disabled { opacity: 0.5; cursor: default; }
.hist-wrap { position: relative; }
.hist-pop {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: 50; width: 360px; max-height: 280px; overflow-y: auto;
  background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 8px;
  box-shadow: var(--shadow-md); padding: 4px;
}
.hist-empty { padding: 12px; text-align: center; font-size: 12px; color: var(--color-text-disabled); }
.hist-item { padding: 7px 9px; border-radius: 5px; cursor: pointer; font-size: 12px; color: var(--color-text-secondary); font-family: 'Consolas', 'Monaco', monospace; }
.hist-item:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.hist-text { display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

/* 编辑器 */
.editor-pane { display: flex; flex-direction: column; overflow: hidden; flex-shrink: 0; }
.editor-toolbar { display: flex; align-items: center; gap: 10px; padding: 6px 12px; }
.run-btn {
  display: flex; align-items: center; gap: 5px; padding: 6px 14px; border-radius: 6px; border: none;
  cursor: pointer; font-size: 13px; font-family: inherit; font-weight: 500; background: var(--color-accent); color: #fff;
  transition: opacity var(--transition-fast);
}
.run-btn:hover { opacity: 0.9; }
.run-btn:disabled { opacity: 0.55; cursor: default; }
.kbd-hint { font-size: 10px; color: var(--color-text-disabled); }
.query-input {
  flex: 1; min-height: 0; resize: none; margin: 0 12px; padding: 8px 10px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 13px; font-family: 'Consolas', 'Monaco', monospace;
  line-height: 1.5; outline: none; box-sizing: border-box;
}
.query-input:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
.splitter { height: 5px; flex-shrink: 0; cursor: row-resize; background: var(--color-border); transition: background var(--transition-fast); }
.splitter:hover { background: var(--color-accent); }

/* 结果 */
.result-pane { flex: 1; display: flex; flex-direction: column; overflow: hidden; padding: 0 12px 12px; }
.result-head { display: flex; align-items: center; gap: 10px; padding: 8px 0; flex-shrink: 0; }
.status-badge { font-size: 12px; font-weight: 700; padding: 2px 8px; border-radius: 4px; color: #fff; }
.status-badge.ok { background: #2e9e5b; }
.status-badge.warn { background: #d9920a; }
.status-badge.err { background: var(--color-danger); }
.resp-meta { font-size: 12px; color: var(--color-text-disabled); }
.result-actions { margin-left: auto; display: flex; gap: 6px; }
.r-btn {
  display: flex; align-items: center; gap: 4px; padding: 4px 9px; border-radius: 5px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  font-size: 12px; font-family: inherit; cursor: pointer;
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.r-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.result-body { flex: 1; overflow: auto; border: 1px solid var(--color-border); border-radius: 6px; background: var(--color-bg-tertiary); }
.result-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.result-table th {
  position: sticky; top: 0; text-align: left; padding: 7px 10px; background: var(--color-bg-secondary);
  color: var(--color-text-muted); font-weight: 600; border: 1px solid var(--color-border); white-space: nowrap; z-index: 1;
}
.result-table td {
  padding: 6px 10px; border: 1px solid var(--color-border); color: var(--color-text-primary);
  font-family: 'Consolas', 'Monaco', monospace; max-width: 380px; position: relative;
}
.result-table tbody tr:hover { background: var(--color-bg-hover); }
.result-table td.rownum, .result-table th.rownum {
  width: 42px; min-width: 42px; text-align: right; color: var(--color-text-disabled); font-size: 12px;
  padding-left: 6px; padding-right: 6px; background: var(--color-bg-secondary);
}
.result-table td.is-null .cell-text { color: var(--color-text-disabled); font-style: italic; }
.cell-text { display: inline-block; max-width: 340px; overflow: hidden; text-overflow: ellipsis; white-space: pre-wrap; word-break: break-all; vertical-align: top; }
.cell-copy {
  position: absolute; top: 4px; right: 4px; background: var(--color-bg-tertiary); border: 1px solid var(--color-border);
  border-radius: 3px; cursor: pointer; display: none; align-items: center; justify-content: center;
  color: var(--color-text-muted); padding: 1px;
  transition: color var(--transition-fast), background var(--transition-fast);
}
.result-table td:hover .cell-copy { display: flex; }
.cell-copy:hover { color: var(--color-accent); background: var(--color-bg-active); }

/* 只读原因提示 */
.edit-reason {
  margin: 0 0 8px; padding: 6px 10px; font-size: 12px; border-radius: 6px;
  color: #d9933a; background: rgba(217, 147, 58, 0.1); border: 1px solid rgba(217, 147, 58, 0.25);
}

/* 可编辑单元格（Navicat 风格内联编辑） */
.result-table td.editable-cell { padding: 0; position: relative; }
.cell-input {
  width: 100%; box-sizing: border-box; border: none; outline: none; background: transparent;
  color: var(--color-text-primary); font-family: 'Consolas', 'Monaco', monospace; font-size: 13px;
  padding: 6px 26px 6px 10px; line-height: 1.4;
}
.cell-input:focus { background: var(--color-bg-active); box-shadow: inset 0 0 0 1px var(--color-border-focus); }
.result-table td.editable-cell.is-null .cell-input { color: var(--color-text-disabled); font-style: italic; }
.result-table td.editable-cell.dirty { background: rgba(217, 146, 10, 0.12); }
.result-table td.editable-cell.dirty .cell-input { color: var(--color-text-primary); }
.cell-null-btn {
  position: absolute; top: 50%; right: 4px; transform: translateY(-50%);
  background: var(--color-bg-tertiary); border: 1px solid var(--color-border); border-radius: 3px;
  cursor: pointer; display: flex; align-items: center; justify-content: center; color: var(--color-text-muted);
  padding: 1px 4px; font-size: 11px; line-height: 1; transition: color var(--transition-fast), background var(--transition-fast);
}
.cell-null-btn:hover { color: var(--color-accent); background: var(--color-bg-active); }
.cell-null-btn.active { color: var(--color-danger); border-color: var(--color-danger); background: rgba(232, 76, 76, 0.12); }

/* 可编辑网格底部操作栏 */
.grid-footer {
  display: flex; align-items: center; gap: 8px; padding: 8px 0; flex-shrink: 0;
  border-top: 1px solid var(--color-border); margin-top: 8px;
}
.grid-hint { font-size: 12px; color: var(--color-text-disabled); }
.grid-hint.changed { color: var(--color-warn, #d9920a); }
.grid-footer .spacer { flex: 1; }
.grid-footer .r-btn:disabled { opacity: 0.5; cursor: default; }
.result-pre {
  margin: 0; padding: 10px; color: var(--color-text-primary); font-size: 13px;
  font-family: 'Consolas', 'Monaco', monospace; line-height: 1.5; white-space: pre-wrap; word-break: break-all;
}
.result-pre.err { color: var(--color-danger); }
.result-empty { height: 100%; display: flex; align-items: center; justify-content: center; color: var(--color-text-disabled); font-size: 13px; }

/* 连接配置弹窗 */
.modal-mask {
  position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45); display: flex; align-items: center; justify-content: center;
  z-index: 200;
}
.modal {
  width: 560px; max-width: 92vw; max-height: 86vh; overflow-y: auto; background: var(--color-surface);
  border: 1px solid var(--color-border); border-radius: 12px; box-shadow: var(--shadow-lg); padding: 16px 18px;
}
.modal-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; font-size: 14px; font-weight: 600; color: var(--color-text-primary); }
.form-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 10px; }
.field { display: flex; flex-direction: column; gap: 4px; }
.field-wide { grid-column: 1 / -1; }
.field-sm { max-width: 130px; }
.field label { font-size: 12px; color: var(--color-text-muted); }
.f-input {
  padding: 6px 9px; border-radius: 6px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; outline: none; width: 100%; box-sizing: border-box;
}
.f-input:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
.modal-actions { display: flex; align-items: center; gap: 8px; margin-top: 16px; }
.spacer { flex: 1; }
.test-btn, .cancel-btn, .save-btn {
  display: flex; align-items: center; gap: 5px; padding: 7px 14px; border-radius: 6px;
  border: 1px solid var(--color-border); cursor: pointer; font-size: 13px; font-family: inherit;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.test-btn:hover, .cancel-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.save-btn { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
.save-btn:hover { opacity: 0.9; color: #fff; }
.dot { width: 7px; height: 7px; border-radius: 50%; margin-left: 2px; }
.dot.ok { background: #2e9e5b; }
.dot.err { background: var(--color-danger); }

@keyframes spin { to { transform: rotate(360deg); } }
:deep(.spinning) { animation: spin 0.9s linear infinite; }
</style>
