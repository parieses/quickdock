<script setup lang="ts">
import { ref, onMounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Store, RefreshCw, Download, CheckCircle2, ArrowUpCircle, Ban } from '@lucide/vue'
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
  try {
    await InstallPluginFromURL(url)
    toast?.success?.(t('pluginInstallSuccess'))
    await loadMarket() // 刷新 installed/has_update 状态
    emit('installed')              // 通知父页刷新本地插件列表
  } catch (e) {
    toast?.error?.(t('pluginInstallFailed') + ': ' + getErrorMessage(e))
  } finally {
    installing.value.delete(p.id)
  }
}

onMounted(loadMarket)
</script>

<template>
  <div class="market-page">
    <div class="market-header">
      <div class="market-header-left">
        <Store :size="20" />
        <h2 class="market-title">{{ t('pluginMarket') }}</h2>
        <span v-if="plugins.length" class="market-count">{{ plugins.length }}</span>
        <span v-if="updated" class="market-updated">{{ t('pluginMarketUpdated') }}: {{ updated.slice(0, 10) }}</span>
      </div>
      <button class="refresh-btn" @click="loadMarket" :title="t('refresh')">
        <RefreshCw :size="14" />
      </button>
    </div>

    <div v-if="loading" class="market-loading"><p>{{ t('loading') }}</p></div>

    <div v-else-if="plugins.length === 0" class="market-empty">
      <Store :size="48" class="empty-icon" />
      <p class="empty-title">{{ t('pluginMarketEmpty') }}</p>
    </div>

    <div v-else class="market-grid">
      <div v-for="p in plugins" :key="p.id" class="market-card">
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
          <button v-if="!p.supported" class="action-btn btn-disabled" disabled>
            <Ban :size="14" /><span>{{ t('pluginNotSupportedPlatform') }}</span>
          </button>
          <button v-else-if="p.installed && p.has_update" class="action-btn btn-upgrade"
            :disabled="installing.has(p.id)" @click="install(p)">
            <ArrowUpCircle :size="14" /><span>{{ t('pluginUpgrade') }}</span>
          </button>
          <button v-else-if="p.installed" class="action-btn btn-done" disabled>
            <CheckCircle2 :size="14" /><span>{{ t('pluginInstalled') }}</span>
          </button>
          <button v-else class="action-btn btn-install"
            :disabled="installing.has(p.id)" @click="install(p)">
            <Download :size="14" /><span>{{ t('pluginInstallFromMarket') }}</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.market-page { flex: 1; display: flex; flex-direction: column; gap: 12px; }
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
.market-loading, .market-empty {
  flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center;
  gap: 10px; color: var(--color-text-muted);
}
.empty-icon { color: var(--color-text-disabled); }
.empty-title { font-size: 13px; }
.market-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 12px; }
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
