<script setup lang="ts">
import { ref, computed, onMounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Puzzle, Power, PowerOff, Trash2, RefreshCw, Upload, ExternalLink } from '@lucide/vue'
import { ListPlugins, DisablePlugin, EnablePlugin, UninstallPlugin, SelectAndInstallPlugin, GetPluginIcon, ShowPluginWindow } from '../../bindings/quickdock/services/appservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import ConfirmDialog from './ConfirmDialog.vue'
import type { ToastAPI, PluginInfo } from '../types'

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const plugins = ref<PluginInfo[]>([])
const icons = ref<Record<string, string>>({}) // pluginId → data URI
const loading = ref(true)
const operating = ref<Set<string>>(new Set())

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

onMounted(loadPlugins)
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
        <!-- 图标区域 -->
        <div class="card-icon-wrap">
          <img
            v-if="icons[p.id]"
            :src="icons[p.id]"
            class="card-icon-img"
            alt=""
          />
          <Puzzle v-else :size="32" class="card-icon-fallback" />
        </div>

        <!-- 名称和状态 -->
        <div class="card-name">{{ p.name }}</div>
        <div class="card-version">v{{ p.version }}</div>
        <div class="card-usage" v-if="p.usageCount > 0">
          <span class="usage-icon">▶</span>
          <span class="usage-count">{{ p.usageCount }}</span>
        </div>
        <span :class="['status-badge', statusBadgeClass(p.status)]">{{ statusLabel(p.status) }}</span>

        <!-- 描述 -->
        <p v-if="p.description" class="card-desc">{{ p.description }}</p>

        <!-- 操作 -->
        <div class="card-actions">
          <button
            :class="['action-btn', p.status === 'running' ? 'btn-stop' : 'btn-start']"
            :disabled="operating.has(p.id)"
            @click.stop="togglePlugin(p)"
            :title="p.status === 'running' ? t('pluginDisable') : t('pluginEnable')"
          >
            <component :is="p.status === 'running' ? PowerOff : Power" :size="14" />
          </button>
          <button
            v-if="p.hasFrontend"
            class="action-btn btn-open"
            :disabled="p.status !== 'running' || operating.has(p.id)"
            @click.stop="openPluginPage(p)"
            :title="p.status === 'running' ? t('pluginOpen') : '插件未运行'"
          >
            <ExternalLink :size="14" />
          </button>
          <button
            class="action-btn btn-uninstall"
            :disabled="operating.has(p.id)"
            @click.stop="onUninstall(p)"
            :title="t('pluginUninstall')"
          >
            <Trash2 :size="14" />
          </button>
        </div>
      </div>
    </div>

    <!-- 卸载确认 -->
    <ConfirmDialog
      :visible="showUninstallConfirm"
      :message="t('pluginUninstallConfirm', { name: uninstallingName })"
      @confirm="handleUninstall"
      @cancel="showUninstallConfirm = false"
    />
  </div>
</template>

<style scoped>
.plugin-page {
  flex: 1; display: flex; flex-direction: column;
  overflow: hidden; padding: 20px 24px;
}

/* ---- 头部 ---- */
.plugin-header {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 20px; flex-shrink: 0;
}
.plugin-header-left { display: flex; align-items: center; gap: 10px; }
.plugin-title { font-size: 16px; font-weight: 500; margin: 0; color: var(--color-text-primary); }
.plugin-count { font-size: 11px; color: var(--color-text-muted); background: var(--color-bg-tertiary); padding: 2px 8px; border-radius: 10px; }
.plugin-header-actions { display: flex; gap: 8px; }

.install-btn {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 12px; border-radius: 6px;
  border: 1px solid var(--color-accent);
  background: var(--color-accent-bg);
  color: var(--color-accent); cursor: pointer; font-size: 12px;
}
.install-btn:hover { opacity: 0.85; }
.refresh-btn {
  display: flex; align-items: center; gap: 4px;
  padding: 6px 12px; border: 1px solid var(--color-border);
  border-radius: 6px; background: var(--color-bg-secondary);
  color: var(--color-text-secondary); cursor: pointer; font-size: 12px;
}
.refresh-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

/* ---- 加载 / 空状态 ---- */
.plugin-loading, .plugin-empty {
  flex: 1; display: flex; flex-direction: column;
  align-items: center; justify-content: center;
  gap: 12px; color: var(--color-text-disabled);
}
.empty-icon { opacity: 0.3; }
.empty-title { font-size: 14px; color: var(--color-text-muted); margin: 0; }
.empty-desc { font-size: 12px; margin: 0; color: var(--color-text-disabled); }

/* ---- 九宫格 ---- */
.plugin-grid {
  flex: 1; overflow-y: auto;
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 12px;
  align-content: start;
}

/* ---- 插件卡片 ---- */
.plugin-card {
  background: var(--color-bg-secondary);
  border: 1px solid var(--color-border);
  border-radius: 10px;
  padding: 16px 12px;
  display: flex; flex-direction: column;
  align-items: center;
  gap: 6px;
  text-align: center;
  transition: all 150ms;
  cursor: default;
}
.plugin-card:hover { border-color: var(--color-border-hover); transform: translateY(-1px); }
.plugin-card.card-running { border-color: rgba(29,158,117,0.3); }

/* 图标 */
.card-icon-wrap {
  width: 48px; height: 48px;
  display: flex; align-items: center; justify-content: center;
  margin-bottom: 4px;
}
.card-icon-img { width: 48px; height: 48px; object-fit: contain; }
.card-icon-fallback { color: var(--color-text-muted); }

/* 名称 */
.card-name { font-size: 13px; font-weight: 500; color: var(--color-text-primary); line-height: 1.3; }
.card-version { font-size: 10px; color: var(--color-text-muted); }

/* 状态 */
.status-badge { font-size: 10px; padding: 1px 8px; border-radius: 10px; font-weight: 500; }
.badge-running { background: rgba(29,158,117,0.15); color: #1D9E75; }
.badge-stopped { background: rgba(136,135,128,0.15); color: #888780; }
.badge-crashed { background: rgba(226,75,74,0.15); color: #E24B4A; }
.badge-created { background: rgba(55,138,221,0.15); color: #378ADD; }

/* 使用次数 */
.card-usage { display: flex; align-items: center; gap: 3px; font-size: 10px; color: var(--color-text-muted); }
.usage-icon { font-size: 8px; }
.usage-count { font-family: var(--font-mono, monospace); font-weight: 500; }

/* 描述 */
.card-desc {
  font-size: 11px; color: var(--color-text-muted);
  margin: 2px 0 0; line-height: 1.4;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical;
  overflow: hidden;
}

/* 操作按钮 */
.card-actions {
  display: flex; gap: 4px;
  margin-top: 4px; padding-top: 6px;
  border-top: 1px solid var(--color-border);
  width: 100%;
  justify-content: center;
}
.action-btn {
  display: inline-flex; align-items: center; justify-content: center;
  padding: 5px; border-radius: 6px;
  border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary);
  color: var(--color-text-muted);
  cursor: pointer; font-size: 12px;
  width: 32px; height: 28px;
}
.action-btn:hover { border-color: var(--color-text-secondary); color: var(--color-text-primary); }
.action-btn:disabled { opacity: 0.4; cursor: not-allowed; }
.btn-start:hover { border-color: #1D9E75; color: #1D9E75; }
.btn-stop:hover { border-color: #E2A04A; color: #E2A04A; }
.btn-open:hover { border-color: var(--color-accent); color: var(--color-accent); }
.btn-uninstall:hover { border-color: #E24B4A; color: #E24B4A; }

/* ---- 分类 Tabs ---- */
.plugin-categories {
  display: flex; gap: 4px; flex-wrap: wrap;
  margin-bottom: 16px; flex-shrink: 0;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--color-border);
}
.cat-tab {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 5px 12px; border-radius: 6px;
  border: 1px solid transparent;
  background: transparent;
  color: var(--color-text-secondary);
  font-size: 12px; font-family: inherit;
  cursor: pointer; white-space: nowrap;
  transition: all 0.1s;
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
  font-size: 11px; opacity: 0.6;
}

</style>
