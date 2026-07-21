<script setup lang="ts">
import { ref, computed, onMounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Puzzle, Power, PowerOff, Trash2, RefreshCw, Upload, ExternalLink, History, ChevronDown, ChevronRight, CheckCircle2, XCircle } from '@lucide/vue'
import { ListPlugins, DisablePlugin, EnablePlugin, UninstallPlugin, SelectAndInstallPlugin, GetPluginIcon, ShowPluginWindow, ListPluginExecLogs } from '../../bindings/quickdock/services/appservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import ConfirmDialog from './ConfirmDialog.vue'
import type { ToastAPI, PluginInfo, PluginExecLog } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const plugins = ref<PluginInfo[]>([])
const icons = ref<Record<string, string>>({}) // pluginId → data URI
const loading = ref(true)
const operating = ref<Set<string>>(new Set())

// ---- 执行历史（5.2） ----
const execLogs = ref<PluginExecLog[]>([])
const loadingLogs = ref(false)
const historyOpen = ref(false)
const expandedLog = ref<string | null>(null)

async function loadLogs() {
  loadingLogs.value = true
  try {
    execLogs.value = (unwrap(await ListPluginExecLogs(100)) || []) as PluginExecLog[]
  } catch (e) {
    execLogs.value = []
  } finally {
    loadingLogs.value = false
  }
}
function toggleHistory() {
  historyOpen.value = !historyOpen.value
  if (historyOpen.value && execLogs.value.length === 0) loadLogs()
}
function toggleLogDetail(id: string) {
  expandedLog.value = expandedLog.value === id ? null : id
}
function triggerLabel(trigger: string): string {
  switch (trigger) {
    case 'hotkey': return '热键'
    case 'palette': return '面板'
    default: return '手动'
  }
}

// ---- 分类筛选 ----
const selectedCategory = ref('')
const categories = computed(() => {
  const cats = new Set<string>()
  for (const p of plugins.value) {
    if (p.category) cats.add(p.category)
  }
  return Array.from(cats).sort()
})
const filteredPlugins = computed(() => {
  if (!selectedCategory.value) return sortedPlugins.value
  return sortedPlugins.value.filter(p => p.category === selectedCategory.value)
})

// 卸载确认
const showUninstallConfirm = ref(false)
const uninstallingId = ref('')
const uninstallingName = ref('')

// ---- 加载 ----
async function loadPlugins() {
  loading.value = true
  try {
    plugins.value = (unwrap(await ListPlugins()) || []) as PluginInfo[]
    // 并行加载所有启用了前端的插件图标
    const iconPromises = plugins.value
      .filter(p => p.hasFrontend)
      .map(async (p) => {
        try {
          const dataUri = unwrap<string | null>(await GetPluginIcon(p.id))
          if (dataUri) icons.value[p.id] = dataUri
        } catch {}
      })
    await Promise.all(iconPromises)
  } catch (e) {
    toast?.error?.(t('pluginLoadFailed') + ': ' + getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

// ---- 启用/禁用 ----
async function togglePlugin(p: PluginInfo) {
  if (operating.value.has(p.id)) return
  operating.value.add(p.id)
  try {
    if (p.status === 'running') {
      await DisablePlugin(p.id)
      p.status = 'stopped'
    } else {
      await EnablePlugin(p.id)
      p.status = 'running'
    }
  } catch (e) {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
    await loadPlugins()
  } finally {
    operating.value.delete(p.id)
  }
}

// ---- 卸载 ----
function onUninstall(p: PluginInfo) {
  uninstallingId.value = p.id
  uninstallingName.value = p.name
  showUninstallConfirm.value = true
}

async function handleUninstall() {
  const id = uninstallingId.value
  showUninstallConfirm.value = false
  if (operating.value.has(id)) return
  operating.value.add(id)
  try {
    await UninstallPlugin(id)
    toast?.success?.(t('deleted'))
    await loadPlugins()
  } catch (e) {
    toast?.error?.(t('pluginUninstallFailed') + ': ' + getErrorMessage(e))
  } finally {
    operating.value.delete(id)
  }
}

// ---- 安装 ----
async function selectAndInstall() {
  if (operating.value.has('_install_')) return
  operating.value.add('_install_')
  try {
    const result = unwrap(await SelectAndInstallPlugin())
    if (result) {
      toast?.success?.(t('pluginInstallSuccess'))
      await loadPlugins()
    }
  } catch (e) {
    toast?.error?.(t('pluginInstallFailed') + ': ' + getErrorMessage(e))
  } finally {
    operating.value.delete('_install_')
  }
}

// ---- Emits ----
const emit = defineEmits<{ (e: 'navigate-plugin', pluginId: string): void }>()

function openPluginPage(p: PluginInfo) {
  // 在新窗口中打开插件前端页面
  ShowPluginWindow(p.id).catch(e => {
    toast?.error?.(t('pluginOpFailed') + ': ' + getErrorMessage(e))
  })
}

// ---- 状态辅助 ----
const statusOrder: Record<string, number> = { running: 0, crashed: 1, stopped: 2, starting: 3 }
const sortedPlugins = computed(() => {
  return [...plugins.value].sort((a, b) => {
    // 先按 useCount 倒序
    if ((b.usageCount || 0) !== (a.usageCount || 0)) {
      return (b.usageCount || 0) - (a.usageCount || 0)
    }
    // 再按状态排序
    const statusDiff = (statusOrder[a.status] ?? 99) - (statusOrder[b.status] ?? 99)
    if (statusDiff !== 0) return statusDiff
    // 最后按名称排序，保证顺序稳定
    return (a.name || '').localeCompare(b.name || '')
  })
})
function statusBadgeClass(status: string): string {
  switch (status) {
    case 'running': return 'badge-running'
    case 'stopped': return 'badge-stopped'
    case 'crashed': return 'badge-crashed'
    default: return 'badge-created'
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case 'running': return t('pluginStatusRunning')
    case 'stopped': return t('pluginStatusStopped')
    case 'crashed': return t('pluginStatusCrashed')
    default: return t('pluginStatusCreated')
  }
}

onMounted(() => { loadPlugins(); loadLogs() })
</script>

<template>
  <div class="plugin-page">
    <!-- 头部 -->
    <div class="plugin-header">
      <div class="plugin-header-left">
        <Puzzle :size="20" />
        <h2 class="plugin-title">{{ t('pluginManager') }}</h2>
        <span v-if="plugins.length" class="plugin-count">{{ plugins.length }}</span>
      </div>
      <div class="plugin-header-actions">
        <button class="install-btn" @click="selectAndInstall" :title="t('pluginInstallFromFile')">
          <Upload :size="14" />
          <span>{{ t('pluginInstallFromFile') }}</span>
        </button>
        <button class="refresh-btn" @click="loadPlugins" :title="t('refresh')">
          <RefreshCw :size="14" />
        </button>
      </div>
    </div>

    <!-- 分类 Tabs -->
    <div class="plugin-categories" v-if="plugins.length > 0">
      <button
        :class="['cat-tab', { active: selectedCategory === '' }]"
        @click="selectedCategory = ''"
      >全部 <span class="cat-count">{{ plugins.length }}</span></button>
      <button
        v-for="cat in categories"
        :key="cat"
        :class="['cat-tab', { active: selectedCategory === cat }]"
        @click="selectedCategory = cat"
      >{{ cat }} <span class="cat-count">{{ plugins.filter(p => p.category === cat).length }}</span></button>
    </div>

    <!-- 加载中 -->
    <div v-if="loading" class="plugin-loading">
      <p>{{ t('loading') }}</p>
    </div>

    <!-- 空状态 -->
    <div v-else-if="plugins.length === 0" class="plugin-empty">
      <Puzzle :size="48" class="empty-icon" />
      <p class="empty-title">{{ t('pluginNoPlugins') }}</p>
      <p class="empty-desc">{{ t('pluginDragToInstall') }}</p>
    </div>

    <!-- 九宫格插件列表 -->
    <div v-else-if="filteredPlugins.length === 0" class="plugin-empty">
      <Puzzle :size="48" class="empty-icon" />
      <p class="empty-title">该分类下暂无插件</p>
    </div>

    <div v-else class="plugin-grid">
      <div
        v-for="p in filteredPlugins"
        :key="p.id"
        :class="['plugin-card', { 'card-running': p.status === 'running' }]"
      >
        <!-- 悬停操作按钮 -->
        <div class="card-actions-top">
          <button
            :class="['action-top-btn', p.status === 'running' ? 'btn-stop-top' : 'btn-start-top']"
            :disabled="operating.has(p.id)"
            @click.stop="togglePlugin(p)"
            :title="p.status === 'running' ? t('pluginDisable') : t('pluginEnable')"
          >
            <component :is="p.status === 'running' ? PowerOff : Power" :size="12" />
          </button>
          <button
            v-if="p.hasFrontend"
            class="action-top-btn btn-open-top"
            :disabled="p.status !== 'running' || operating.has(p.id)"
            @click.stop="openPluginPage(p)"
            :title="p.status === 'running' ? t('pluginOpen') : '插件未运行'"
          >
            <ExternalLink :size="12" />
          </button>
          <button
            class="action-top-btn btn-uninstall-top"
            :disabled="operating.has(p.id)"
            @click.stop="onUninstall(p)"
            :title="t('pluginUninstall')"
          >
            <Trash2 :size="11" />
          </button>
        </div>

        <!-- 图标区域 -->
        <div class="card-icon-wrap">
          <img
            v-if="icons[p.id]"
            :src="icons[p.id]"
            class="card-icon-img"
            alt=""
          />
          <Puzzle v-else :size="22" class="card-icon-fallback" />
        </div>

        <!-- 名称 -->
        <div class="card-name">{{ p.name }}</div>

        <!-- 元信息行 -->
        <div class="card-meta">
          <span class="card-version">v{{ p.version }}</span>
          <span v-if="p.usageCount > 0" class="card-usage">
            <span class="usage-icon">▶</span>
            <span class="usage-count">{{ p.usageCount }}</span>
          </span>
        </div>

        <span :class="['status-badge', statusBadgeClass(p.status)]">{{ statusLabel(p.status) }}</span>

        <!-- 描述 -->
        <p v-if="p.description" class="card-desc">{{ p.description }}</p>
      </div>
    </div>

    <!-- 卸载确认 -->
    <ConfirmDialog
      :visible="showUninstallConfirm"
      :message="t('pluginUninstallConfirm', { name: uninstallingName })"
      @confirm="handleUninstall"
      @cancel="showUninstallConfirm = false"
    />

    <!-- 执行历史（5.2） -->
    <div class="exec-history">
      <button class="history-head" @click="toggleHistory">
        <component :is="historyOpen ? ChevronDown : ChevronRight" :size="14" />
        <History :size="14" />
        <span>{{ t('pluginExecHistory') }}</span>
        <span v-if="execLogs.length" class="history-count">{{ execLogs.length }}</span>
        <span v-if="execLogs.some(l => !l.success)" class="history-err-dot" :title="t('pluginExecHistoryHasError')"></span>
      </button>

      <div v-if="historyOpen" class="history-body">
        <div v-if="loadingLogs" class="history-loading">{{ t('loading') }}</div>
        <div v-else-if="execLogs.length === 0" class="history-empty">{{ t('pluginExecHistoryEmpty') }}</div>
        <div v-else class="history-list">
          <div
            v-for="log in execLogs"
            :key="log.id"
            :class="['exec-row', { 'exec-fail': !log.success, 'exec-expanded': expandedLog === log.id }]"
            @click="toggleLogDetail(log.id)"
          >
            <component :is="log.success ? CheckCircle2 : XCircle" :size="13" :class="log.success ? 'row-ok' : 'row-err'" />
            <span class="row-plugin">{{ log.pluginId }}</span>
            <span class="row-cmd">{{ log.commandId }}</span>
            <span class="row-trigger" :title="triggerLabel(log.trigger)">{{ triggerLabel(log.trigger) }}</span>
            <span class="row-dur">{{ log.durationMs }}ms</span>
            <span class="row-time">{{ log.executedAt }}</span>
            <component :is="expandedLog === log.id ? ChevronDown : ChevronRight" :size="12" class="row-caret" />
          </div>
          <div v-for="log in execLogs" :key="'d-' + log.id">
            <div v-if="expandedLog === log.id" class="exec-detail">
              <div v-if="log.result" class="detail-block">
                <div class="detail-label">{{ t('pluginExecResult') }}</div>
                <pre class="detail-pre">{{ log.result }}</pre>
              </div>
              <div v-if="log.error" class="detail-block">
                <div class="detail-label detail-err">{{ t('error') }}</div>
                <pre class="detail-pre detail-pre-err">{{ log.error }}</pre>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.plugin-page {
  flex: 1; display: flex; flex-direction: column;
  overflow: hidden; padding: 16px 20px;
}

/* ---- 头部 ---- */
.plugin-header {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 12px; flex-shrink: 0;
}
.plugin-header-left { display: flex; align-items: center; gap: 8px; }
.plugin-title { font-size: 14px; font-weight: 600; margin: 0; color: var(--color-text-primary); }
.plugin-count { font-size: 10px; color: var(--color-text-muted); background: var(--color-bg-tertiary); padding: 1px 7px; border-radius: 8px; }
.plugin-header-actions { display: flex; gap: 6px; }

.install-btn {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 5px 10px; border-radius: 5px;
  border: 1px solid var(--color-accent);
  background: var(--color-accent-bg);
  color: var(--color-accent); cursor: pointer; font-size: 11px;
}
.install-btn:hover { opacity: 0.85; }
.refresh-btn {
  display: flex; align-items: center;
  padding: 5px 10px; border: 1px solid var(--color-border);
  border-radius: 5px; background: var(--color-bg-secondary);
  color: var(--color-text-secondary); cursor: pointer; font-size: 11px;
}
.refresh-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

/* ---- 加载 / 空状态 ---- */
.plugin-loading, .plugin-empty {
  flex: 1; display: flex; flex-direction: column;
  align-items: center; justify-content: center;
  gap: 10px; color: var(--color-text-disabled);
}
.empty-icon { opacity: 0.3; }
.empty-title { font-size: 13px; color: var(--color-text-muted); margin: 0; }
.empty-desc { font-size: 11px; margin: 0; color: var(--color-text-disabled); }

/* ---- 网格 ---- */
.plugin-grid {
  flex: 1; overflow-y: auto;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(130px, 1fr));
  gap: 8px;
  align-content: start;
  padding: 2px 0;
}

/* ---- 插件卡片 ---- */
.plugin-card {
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: 8px;
  padding: 10px;
  display: flex; flex-direction: column;
  align-items: center;
  gap: 3px;
  text-align: center;
  transition: border-color 150ms, box-shadow 150ms;
  cursor: default;
  position: relative;
}
.plugin-card:hover { border-color: var(--color-accent); box-shadow: 0 0 0 1px rgba(74,158,255,0.15); }
.plugin-card.card-running { border-color: rgba(29,158,117,0.25); }

/* 顶部操作悬停 */
.card-actions-top {
  position: absolute; top: 4px; right: 4px;
  display: flex; gap: 2px; opacity: 0;
  transition: opacity 120ms;
}
.plugin-card:hover .card-actions-top { opacity: 1; }
.action-top-btn {
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: 4px;
  border: none; background: var(--color-bg-tertiary);
  color: var(--color-text-muted); cursor: pointer;
  font-size: 10px; transition: background-color 100ms, color 100ms, border-color 100ms;
}
.action-top-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.action-top-btn.btn-stop-top:hover { color: #E2A04A; }
.action-top-btn.btn-open-top:hover { color: var(--color-accent); }
.action-top-btn.btn-uninstall-top:hover { color: #E24B4A; }

/* 图标 */
.card-icon-wrap {
  width: 36px; height: 36px;
  display: flex; align-items: center; justify-content: center;
  margin-bottom: 2px;
}
.card-icon-img { width: 36px; height: 36px; object-fit: contain; }
.card-icon-fallback { color: var(--color-text-muted); }

/* 名称 */
.card-name { font-size: 12px; font-weight: 500; color: var(--color-text-primary); line-height: 1.3; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 100%; }
.card-meta { display: flex; align-items: center; gap: 4px; flex-wrap: wrap; justify-content: center; }
.card-version { font-size: 9px; color: var(--color-text-muted); }

/* 状态 */
.status-badge { font-size: 9px; padding: 0 6px; border-radius: 8px; font-weight: 500; line-height: 16px; }
.badge-running { background: rgba(29,158,117,0.15); color: #1D9E75; }
.badge-stopped { background: rgba(136,135,128,0.15); color: #888780; }
.badge-crashed { background: rgba(226,75,74,0.15); color: #E24B4A; }
.badge-created { background: rgba(55,138,221,0.15); color: #378ADD; }

/* 使用次数 */
.card-usage { display: inline-flex; align-items: center; gap: 2px; font-size: 9px; color: var(--color-text-muted); }
.usage-icon { font-size: 7px; }
.usage-count { font-family: var(--font-mono, monospace); font-weight: 500; }

/* 描述 */
.card-desc {
  font-size: 10px; color: var(--color-text-muted);
  margin: 1px 0 0; line-height: 1.3;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden;
}

/* ---- 分类 Tabs ---- */
.plugin-categories {
  display: flex; gap: 3px; flex-wrap: wrap;
  margin-bottom: 10px; flex-shrink: 0;
  padding-bottom: 8px;
  border-bottom: 1px solid var(--color-border);
}
.cat-tab {
  display: inline-flex; align-items: center; gap: 3px;
  padding: 4px 10px; border-radius: 5px;
  border: 1px solid transparent;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 11px; font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: background-color 0.1s, color 0.1s, border-color 0.1s;
}
.cat-tab:hover {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}
.cat-tab.active {
  background: var(--color-accent-bg);
  border-color: var(--color-accent);
  color: var(--color-accent);
}
.cat-count {
  font-size: 10px; opacity: 0.6;
}

/* ---- 执行历史（5.2） ---- */
.exec-history {
  flex-shrink: 0;
  margin-top: 12px;
  border-top: 1px solid var(--color-border);
  padding-top: 8px;
}
.history-head {
  display: flex; align-items: center; gap: 6px;
  width: 100%; padding: 4px 2px;
  background: transparent; border: none; cursor: pointer;
  color: var(--color-text-secondary); font-size: 12px; font-family: inherit;
}
.history-head:hover { color: var(--color-text-primary); }
.history-count {
  font-size: 10px; color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 0 6px; border-radius: 8px;
}
.history-err-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: #E24B4A; margin-left: 2px;
}
.history-body { margin-top: 6px; }
.history-loading, .history-empty {
  font-size: 11px; color: var(--color-text-disabled);
  padding: 8px 2px;
}
.history-list {
  max-height: 220px; overflow-y: auto;
  display: flex; flex-direction: column; gap: 2px;
}
.exec-row {
  display: flex; align-items: center; gap: 8px;
  padding: 5px 6px; border-radius: 5px;
  background: var(--color-bg-secondary);
  border: 1px solid transparent;
  cursor: pointer; font-size: 11px;
}
.exec-row:hover { background: var(--color-bg-hover); }
.exec-row.exec-fail { background: rgba(226,75,74,0.06); }
.exec-row.exec-fail:hover { background: rgba(226,75,74,0.12); }
.exec-row.exec-expanded { border-color: var(--color-border); }
.row-ok { color: #1D9E75; flex-shrink: 0; }
.row-err { color: #E24B4A; flex-shrink: 0; }
.row-plugin { color: var(--color-text-primary); font-weight: 500; max-width: 130px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-cmd { color: var(--color-text-secondary); max-width: 110px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-trigger {
  font-size: 10px; color: var(--color-text-muted);
  background: var(--color-bg-tertiary); padding: 0 6px; border-radius: 8px; flex-shrink: 0;
}
.row-dur { color: var(--color-text-muted); font-family: var(--font-mono, monospace); flex-shrink: 0; }
.row-time { color: var(--color-text-disabled); font-size: 10px; margin-left: auto; flex-shrink: 0; }
.row-caret { color: var(--color-text-disabled); flex-shrink: 0; }
.exec-detail {
  padding: 6px 8px 8px; margin: -2px 0 2px;
  background: var(--color-bg-primary);
  border: 1px solid var(--color-border); border-radius: 5px;
}
.detail-block { margin-top: 6px; }
.detail-block:first-child { margin-top: 0; }
.detail-label { font-size: 10px; color: var(--color-text-muted); margin-bottom: 3px; }
.detail-label.detail-err { color: #E24B4A; }
.detail-pre {
  font-family: var(--font-mono, monospace); font-size: 10px;
  color: var(--color-text-secondary);
  background: var(--color-bg-tertiary); border-radius: 4px;
  padding: 6px 8px; margin: 0; max-height: 140px; overflow-y: auto;
  white-space: pre-wrap; word-break: break-all;
}
.detail-pre-err { color: #E24B4A; }

</style>
