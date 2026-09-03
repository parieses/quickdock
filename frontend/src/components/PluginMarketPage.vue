<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Store, RefreshCw, Download, CheckCircle2, ArrowUpCircle, Ban, Search } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { GetPluginMarket, InstallPluginFromURL } from '../../bindings/quickdock/services/appservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import { pluginName, pluginDesc } from '../utils/localize'
import type { ToastAPI } from '../types'

const { t, locale } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface MarketPlugin {
  id: string
  name: string
  name_i18n?: Record<string, string>
  version: string
  description: string
  description_i18n?: Record<string, string>
  author: string
  category: string
  icon: string
  platforms: string[]
  permissions: Record<string, boolean>
  capabilities: string[]
  downloads: Record<string, string>
  // 后端 GetPluginMarket 填充
  installed?: boolean
  installed_version?: string
  has_update?: boolean
  supported?: boolean
}
interface MarketIndex {
  name: string
  updated: string
  plugins: MarketPlugin[]
}

const emit = defineEmits<{ (e: 'installed'): void }>()

const plugins = ref<MarketPlugin[]>([])
const loading = ref(true)
const installing = ref<Set<string>>(new Set())
const updated = ref('')

// 搜索与分组筛选（前端内存过滤，无需重新拉取市场索引）
const searchText = ref('')
const activeCat = ref('')

// 分类选项（按插件数降序），未声明分类归入「未分类」
const categories = computed(() => {
  const m = new Map<string, number>()
  for (const p of plugins.value) {
    const c = p.category || t('pluginMarketUncategorized')
    m.set(c, (m.get(c) || 0) + 1)
  }
  return [...m.entries()]
    .sort((a, b) => b[1] - a[1])
    .map(([cat, count]) => ({ cat, count }))
})

// 搜索 haystack：覆盖中英文名称、ID、描述、作者、分类，大小写不敏感
function haystack(p: MarketPlugin): string {
  const parts = [p.name, p.id, p.description, p.author, p.category]
  if (p.name_i18n) parts.push(...Object.values(p.name_i18n))
  if (p.description_i18n) parts.push(...Object.values(p.description_i18n))
  return parts.filter(Boolean).join(' ').toLowerCase()
}

const filteredPlugins = computed(() => {
  const kw = searchText.value.trim().toLowerCase()
  const cat = activeCat.value
  if (!kw && !cat) return plugins.value
  return plugins.value.filter((p) => {
    const okCat = !cat || (p.category || t('pluginMarketUncategorized')) === cat
    const okKw = !kw || haystack(p).includes(kw)
    return okCat && okKw
  })
})

// 下载进度（key = 下载 URL，与卡片 p.downloads.windows 匹配）
interface DownloadProgress { url: string; downloaded: number; total: number; percent: number; stage?: string }
const progressMap = ref<Record<string, DownloadProgress>>({})

function dlState(p: MarketPlugin): DownloadProgress | undefined {
  const url = p.downloads?.windows
  return url ? progressMap.value[url] : undefined
}

// 按钮文案：下载中显示百分比；无 Content-Length 时显示已下载 MB；下载完切「安装中」
function dlLabel(p: MarketPlugin): string {
  const st = dlState(p)
  if (!st) return t('pluginDownloading', { p: 0 })
  if (st.stage === 'installing' || (st.total > 0 && st.percent >= 100)) return t('pluginInstalling')
  if (st.total > 0) return t('pluginDownloading', { p: st.percent })
  return t('pluginDownloadedSize', { mb: (st.downloaded / 1048576).toFixed(1) })
}

async function loadMarket() {
  loading.value = true
  try {
    const idx = unwrap<MarketIndex>(await GetPluginMarket())
    if (idx) {
      plugins.value = idx.plugins || []
      updated.value = idx.updated || ''
    }
  } catch (e) {
    toast?.error?.(t('pluginMarketLoadFailed') + ': ' + getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

async function install(p: MarketPlugin) {
  if (installing.value.has(p.id)) return
  const url = p.downloads?.windows
  if (!url) {
    toast?.error?.(t('pluginMarketNoDownload'))
    return
  }
  installing.value.add(p.id)
  delete progressMap.value[url]
  try {
    await unwrap(await InstallPluginFromURL(url)) // code!=0 抛错，防止下载失败误报成功
    toast?.success?.(t('pluginInstallSuccess'))
    await loadMarket() // 刷新 installed/has_update 状态
    emit('installed')              // 通知父页刷新本地插件列表
  } catch (e) {
    toast?.error?.(t('pluginInstallFailed') + ': ' + getErrorMessage(e))
  } finally {
    installing.value.delete(p.id)
    delete progressMap.value[url]
  }
}

onMounted(() => {
  loadMarket()
  // Wails v3 事件 payload 在 .data（非 e.data 直接展开的细节见 WailsEvent 包装）
  // 用返回的 off 在卸载时移除监听，避免反复进出市场页累积监听 + 旧组件实例内存泄漏。
  const offProgress = Events.On('plugin:download-progress', (e: any) => {
    const d = (e as any)?.data as DownloadProgress | undefined
    if (d?.url) progressMap.value[d.url] = d
  })
  onUnmounted(() => offProgress())
})
</script>

<template>
  <div class="market-page">
    <div class="market-header">
      <div class="market-header-left">
        <Store :size="20" />
        <h2 class="market-title">{{ t('pluginMarket') }}</h2>
        <span v-if="plugins.length" class="market-count">{{ filteredPlugins.length }}</span>
        <span v-if="updated" class="market-updated">{{ t('pluginMarketUpdated') }}: {{ updated.slice(0, 10) }}</span>
      </div>
      <button class="refresh-btn" @click="loadMarket" :title="t('refresh')">
        <RefreshCw :size="14" />
      </button>
    </div>

    <div v-if="plugins.length" class="market-toolbar">
      <div class="market-search">
        <Search :size="14" class="search-icon" />
        <input
          v-model="searchText"
          type="text"
          class="market-search-input"
          :placeholder="t('pluginMarketSearchPlaceholder')"
        />
      </div>
      <select v-model="activeCat" class="market-cat-select">
        <option value="">{{ t('pluginMarketAll') }}</option>
        <option v-for="c in categories" :key="c.cat" :value="c.cat">{{ c.cat }} ({{ c.count }})</option>
      </select>
      <span class="market-result">{{ filteredPlugins.length }} / {{ plugins.length }}</span>
    </div>

    <div v-if="loading" class="market-loading"><p>{{ t('loading') }}</p></div>

    <div v-else-if="plugins.length === 0" class="market-empty">
      <Store :size="48" class="empty-icon" />
      <p class="empty-title">{{ t('pluginMarketEmpty') }}</p>
    </div>

    <div v-else-if="filteredPlugins.length === 0" class="market-empty">
      <Search :size="48" class="empty-icon" />
      <p class="empty-title">{{ t('pluginMarketNoResult') }}</p>
    </div>

    <div v-else class="market-grid">
      <div v-for="p in filteredPlugins" :key="p.id" class="market-card">
        <div class="card-head">
          <img v-if="p.icon" :src="p.icon" class="card-icon" alt="" />
          <Store v-else :size="34" class="card-icon-fallback" />
          <div class="card-title-wrap">
            <span class="card-title">{{ pluginName({ name: p.name, nameI18n: p.name_i18n }, locale) }}</span>
            <span class="badge">v{{ p.version }}</span>
            <span v-if="p.installed && p.has_update" class="badge badge-upgrade">{{ t('pluginHasUpdate') }}</span>
            <span v-else-if="p.installed" class="badge badge-installed">{{ t('pluginInstalled') }}</span>
            <div class="card-id">{{ p.id }}</div>
          </div>
        </div>
        <p class="card-desc">{{ pluginDesc({ description: p.description, descriptionI18n: p.description_i18n }, locale) }}</p>
        <div class="card-meta">
          <span v-if="p.author" class="meta-item">{{ p.author }}</span>
          <span v-if="p.category" class="meta-item">{{ p.category }}</span>
        </div>
        <div class="card-perms" v-if="p.permissions && Object.keys(p.permissions).length">
          <span v-for="k in Object.keys(p.permissions)" :key="k" class="perm-tag">{{ k }}</span>
        </div>
        <div class="card-actions">
          <div v-if="dlState(p)" class="dl-bar">
            <div
              :class="['dl-bar-fill', { indeterminate: !dlState(p)!.total }]"
              :style="{ width: dlState(p)!.total ? dlState(p)!.percent + '%' : '40%' }"
            ></div>
          </div>
          <button v-if="!p.supported" class="action-btn btn-disabled" disabled>
            <Ban :size="14" /><span>{{ t('pluginNotSupportedPlatform') }}</span>
          </button>
          <button v-else-if="p.installed && p.has_update" class="action-btn btn-upgrade"
            :disabled="installing.has(p.id)" @click="install(p)">
            <RefreshCw v-if="installing.has(p.id)" :size="14" class="spin" />
            <ArrowUpCircle v-else :size="14" />
            <span>{{ installing.has(p.id) ? dlLabel(p) : t('pluginUpgrade') }}</span>
          </button>
          <button v-else-if="p.installed" class="action-btn btn-done" disabled>
            <CheckCircle2 :size="14" /><span>{{ t('pluginInstalled') }}</span>
          </button>
          <button v-else class="action-btn btn-install"
            :disabled="installing.has(p.id)" @click="install(p)">
            <RefreshCw v-if="installing.has(p.id)" :size="14" class="spin" />
            <Download v-else :size="14" />
            <span>{{ installing.has(p.id) ? dlLabel(p) : t('pluginInstallFromMarket') }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.market-page { flex: 1; min-height: 0; display: flex; flex-direction: column; gap: 12px; }
.market-header { display: flex; align-items: center; justify-content: space-between; padding: 4px 0; }
.market-header-left { display: flex; align-items: center; gap: 8px; }
.market-title { font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.market-count { font-size: 11px; color: var(--color-text-muted); background: var(--color-bg-tertiary); padding: 1px 8px; border-radius: 8px; }
.market-updated { font-size: 10px; color: var(--color-text-disabled); margin-left: 4px; }
.refresh-btn {
  display: inline-flex; align-items: center; justify-content: center;
  width: 30px; height: 30px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-secondary);
  color: var(--color-text-secondary); cursor: pointer;
}
.refresh-btn:hover { color: var(--color-accent, #4a9eff); border-color: var(--color-accent, #4a9eff); }
.market-toolbar { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.market-search {
  display: flex; align-items: center; gap: 7px; flex: 1; min-width: 180px;
  height: 32px; padding: 0 10px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-secondary);
  color: var(--color-text-muted);
}
.market-search:focus-within { border-color: var(--color-accent, #4a9eff); }
.search-icon { flex: none; }
.market-search-input {
  flex: 1; min-width: 0; border: 0; background: transparent; outline: none;
  font: inherit; font-size: 13px; color: var(--color-text-primary);
}
.market-search-input::placeholder { color: var(--color-text-disabled); }
.market-cat-select {
  height: 32px; padding: 0 8px; border-radius: 6px; cursor: pointer;
  border: 1px solid var(--color-border); background: var(--color-bg-secondary);
  color: var(--color-text-primary); font: inherit; font-size: 13px;
}
.market-cat-select:focus { outline: none; border-color: var(--color-accent, #4a9eff); }
.market-result { font-size: 11px; color: var(--color-text-muted); white-space: nowrap; }
.market-loading, .market-empty {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 10px; color: var(--color-text-muted);
}
.empty-icon { color: var(--color-text-disabled); }
.empty-title { font-size: 13px; }
.market-grid { flex: 1; min-height: 0; overflow-y: auto; display: grid; grid-template-columns: repeat(3, 1fr); align-content: start; gap: 12px; padding-right: 2px; }
/* 最小三列；主窗口加宽时逐级增加列数 */
@media (min-width: 1440px) { .market-grid { grid-template-columns: repeat(4, 1fr); } }
@media (min-width: 1800px) { .market-grid { grid-template-columns: repeat(5, 1fr); } }
@media (min-width: 2160px) { .market-grid { grid-template-columns: repeat(6, 1fr); } }
.market-card {
  background: var(--color-bg-secondary); border: 1px solid var(--color-border);
  border-radius: 10px; padding: 14px; display: flex; flex-direction: column; gap: 10px;
}
.card-head { display: flex; align-items: flex-start; gap: 10px; }
.card-icon { width: 36px; height: 36px; border-radius: 8px; background: var(--color-bg-tertiary); padding: 5px; }
.card-icon-fallback { color: var(--color-text-muted); }
.card-title-wrap { flex: 1; display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.card-title { font-size: 14px; font-weight: 600; color: var(--color-text-primary); }
.card-id { font-size: 10px; color: var(--color-text-disabled); font-family: var(--font-mono, monospace); width: 100%; }
.badge { font-size: 10px; padding: 1px 7px; border-radius: 8px; background: rgba(74, 158, 255, 0.12); color: var(--color-accent, #4a9eff); }
.badge-installed { background: rgba(29, 158, 117, 0.12); color: #1D9E75; }
.badge-upgrade { background: rgba(233, 160, 19, 0.15); color: #E9A013; }
.card-desc { font-size: 12px; color: var(--color-text-secondary); line-height: 1.5; min-height: 32px; }
.card-meta { display: flex; gap: 8px; font-size: 11px; color: var(--color-text-muted); }
.card-perms { display: flex; gap: 4px; flex-wrap: wrap; }
.perm-tag { font-size: 9px; padding: 1px 6px; border-radius: 6px; background: var(--color-bg-tertiary); color: var(--color-text-muted); }
.card-actions { margin-top: auto; }
.dl-bar {
  height: 3px; margin-bottom: 6px; border-radius: 2px;
  background: var(--color-bg-tertiary); overflow: hidden;
}
.dl-bar-fill {
  height: 100%; border-radius: 2px;
  background: var(--color-accent, #4a9eff);
  transition: width .2s ease;
}
.dl-bar-fill.indeterminate { animation: dl-slide 1.2s ease-in-out infinite; }
@keyframes dl-slide {
  from { transform: translateX(-100%); }
  to { transform: translateX(350%); }
}
.spin { animation: dl-spin 1s linear infinite; }
@keyframes dl-spin { to { transform: rotate(360deg); } }
.action-btn {
  display: inline-flex; align-items: center; gap: 5px; width: 100%; justify-content: center;
  padding: 7px 10px; border-radius: 6px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 12px; cursor: pointer; transition: all .15s;
}
.action-btn:hover:not(:disabled) { border-color: var(--color-accent, #4a9eff); color: var(--color-accent, #4a9eff); }
.action-btn:disabled { opacity: .6; cursor: default; }
.btn-install { background: var(--color-accent, #4a9eff); color: #fff; border-color: var(--color-accent, #4a9eff); }
.btn-install:hover:not(:disabled) { opacity: .9; color: #fff; }
.btn-upgrade { background: rgba(233, 160, 19, 0.15); color: #E9A013; border-color: rgba(233, 160, 19, 0.4); }
.btn-done { color: #1D9E75; }
.btn-disabled { color: var(--color-text-disabled); }
</style>
