<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Store, RefreshCw, Download, CheckCircle2, ArrowUpCircle, Ban, Search } from '@lucide/vue'
import { Events } from '@wailsio/runtime'
import { GetPluginMarket, InstallPluginFromURL } from '../../bindings/quickdock/services/appservice'
import { getErrorMessage } from '../utils/error'
import { unwrap } from '../utils/api'
import { pluginName, pluginDesc } from '../utils/localize'
import { setPluginUpdateBadge } from '../composables/usePluginUpdateBadge'
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
  // 详情页可选字段（市场索引若提供则展示，否则隐藏对应区块；向前兼容）
  changelog?: string
  changelog_i18n?: Record<string, string>
  screenshots?: string[]
  dependencies?: string[]
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
// 「仅看可更新」筛选：一键列出所有有新版可升级的已装插件
const showUpdatesOnly = ref(false)

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
  const onlyUpd = showUpdatesOnly.value
  if (!kw && !cat && !onlyUpd) return plugins.value
  return plugins.value.filter((p) => {
    const okCat = !cat || (p.category || t('pluginMarketUncategorized')) === cat
    const okKw = !kw || haystack(p).includes(kw)
    const okUpd = !onlyUpd || (p.installed && p.has_update)
    return okCat && okKw && okUpd
  })
})

// 可更新插件数量（用于「一键更新」按钮显隐与进度）
const updatableCount = computed(
  () => plugins.value.filter((p) => p.installed && p.has_update && p.supported && !!p.downloads?.windows).length,
)

// ---- 插件详情弹窗 ----
const detail = ref<MarketPlugin | null>(null)
function openDetail(p: MarketPlugin) {
  detail.value = p
}
function closeDetail() {
  detail.value = null
}

// 详情页「依赖说明」：优先用市场索引提供的 dependencies，否则由 capabilities + permissions 推导
function detailDeps(p: MarketPlugin): string[] {
  if (p.dependencies && p.dependencies.length) return p.dependencies
  const out: string[] = []
  if (p.capabilities && p.capabilities.length) out.push(...p.capabilities)
  if (p.permissions) out.push(...Object.keys(p.permissions))
  return [...new Set(out)]
}
function detailChangelog(p: MarketPlugin): string {
  if (p.changelog_i18n && p.changelog_i18n[locale.value]) return p.changelog_i18n[locale.value]
  return p.changelog || ''
}
// 一键更新全局状态
const updatingAll = ref(false)
const updateAllDone = ref(0)
const updateAllTotal = ref(0)

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
      // 同步「可更新」角标到侧栏（插件页未挂载时由本页兜底刷新）
      setPluginUpdateBadge(
        (idx.plugins || []).filter((p) => p.installed && p.has_update && p.supported && !!p.downloads?.windows).length,
      )
    }
  } catch (e) {
    toast?.error?.(t('pluginMarketLoadFailed') + ': ' + getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

// silent=true 时（一键更新场景）不弹单插件 toast、不在每次循环里重复 loadMarket，
// 由调用方统一刷新与提示；返回值表示本次是否成功，便于批量更新统计。
async function install(p: MarketPlugin, silent = false): Promise<boolean> {
  if (installing.value.has(p.id)) return false
  const url = p.downloads?.windows
  if (!url) {
    if (!silent) toast?.error?.(t('pluginMarketNoDownload'))
    return false
  }
  installing.value.add(p.id)
  delete progressMap.value[url]
  try {
    await unwrap(await InstallPluginFromURL(url)) // code!=0 抛错，防止下载失败误报成功
    if (!silent) {
      toast?.success?.(t('pluginInstallSuccess'))
      await loadMarket() // 刷新 installed/has_update 状态
      emit('installed')              // 通知父页刷新本地插件列表
    }
    return true
  } catch (e) {
    if (!silent) toast?.error?.(t('pluginInstallFailed') + ': ' + getErrorMessage(e))
    else console.warn('[market] update failed for', p.id, e)
    return false
  } finally {
    installing.value.delete(p.id)
    delete progressMap.value[url]
  }
}

// 一键更新：将所有「已安装且有新版」的插件依次重装到最新（复用 install 的下载/校验链路）。
async function updateAll() {
  const todo = plugins.value.filter(
    (p) => p.installed && p.has_update && p.supported && p.downloads?.windows,
  )
  if (!todo.length || updatingAll.value) return
  updatingAll.value = true
  updateAllTotal.value = todo.length
  updateAllDone.value = 0
  let ok = 0
  try {
    for (const p of todo) {
      if (await install(p, true)) ok++
      updateAllDone.value++
    }
    await loadMarket() // 统一刷新 installed/has_update 与「可更新」计数
    emit('installed')
    if (ok === todo.length) toast?.success?.(t('pluginUpdateAllDone', { n: ok }))
    else toast?.error?.(t('pluginUpdateAllPartial', { ok, total: todo.length }))
  } catch (e) {
    toast?.error?.(t('pluginUpdateAllFailed') + ': ' + getErrorMessage(e))
  } finally {
    updatingAll.value = false
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
  // 后台定时拉取市场索引（每 5 分钟），刷新「可更新」角标与按钮状态；
  // 仅在页面挂载期间运行，卸载即清理。
  autoTimer = setInterval(() => { loadMarket() }, 5 * 60 * 1000)
  onUnmounted(() => {
    offProgress()
    if (autoTimer) { clearInterval(autoTimer); autoTimer = null }
  })
})
// 后台自动检查定时器的句柄（onUnmounted 中清理）
let autoTimer: ReturnType<typeof setInterval> | null = null
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
      <button v-if="updatableCount > 0" class="update-all-btn" :disabled="updatingAll" @click="updateAll">
        <RefreshCw v-if="updatingAll" :size="14" class="spin" />
        <ArrowUpCircle v-else :size="14" />
        <span>{{ updatingAll ? t('pluginUpdateAllProgress', { done: updateAllDone, total: updateAllTotal }) : t('pluginUpdateAll') }}</span>
      </button>
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
      <button
        class="filter-chip"
        :class="{ active: showUpdatesOnly }"
        :disabled="updatableCount === 0"
        @click="showUpdatesOnly = !showUpdatesOnly"
        :title="t('pluginMarketUpdatesOnly')"
      >
        <ArrowUpCircle :size="13" />
        <span>{{ t('pluginMarketUpdatesOnly') }}</span>
        <span v-if="updatableCount" class="chip-count">{{ updatableCount }}</span>
      </button>
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
      <div v-for="p in filteredPlugins" :key="p.id" class="market-card" @click="openDetail(p)">
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
        <div class="card-actions" @click.stop>
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

    <!-- 插件详情弹窗：展示完整描述、依赖说明、平台、权限/能力、版本状态；changelog/截图若市场索引提供则显示 -->
    <div v-if="detail" class="modal-overlay" @click.self="closeDetail()">
      <div class="modal plugin-detail">
        <button class="detail-close" @click="closeDetail()" :title="t('close')">✕</button>
        <div class="detail-head">
          <img v-if="detail.icon" :src="detail.icon" class="detail-icon" alt="" />
          <Store v-else :size="40" class="detail-icon-fallback" />
          <div class="detail-titles">
            <div class="detail-name-row">
              <span class="detail-name">{{ pluginName({ name: detail.name, nameI18n: detail.name_i18n }, locale) }}</span>
              <span class="badge">v{{ detail.version }}</span>
              <span v-if="detail.installed && detail.has_update" class="badge badge-upgrade">{{ t('pluginHasUpdate') }}</span>
              <span v-else-if="detail.installed" class="badge badge-installed">{{ t('pluginInstalled') }}</span>
            </div>
            <div class="detail-id">{{ detail.id }}</div>
          </div>
        </div>

        <div class="detail-body">
          <section class="detail-section">
            <h4 class="detail-h">{{ t('pluginDetailDesc') }}</h4>
            <p class="detail-desc">{{ pluginDesc({ description: detail.description, descriptionI18n: detail.description_i18n }, locale) }}</p>
          </section>

          <div class="detail-grid">
            <section class="detail-section" v-if="detail.author">
              <h4 class="detail-h">{{ t('pluginDetailAuthor') }}</h4>
              <p class="detail-meta">{{ detail.author }}</p>
            </section>
            <section class="detail-section" v-if="detail.category">
              <h4 class="detail-h">{{ t('pluginDetailCategory') }}</h4>
              <p class="detail-meta">{{ detail.category }}</p>
            </section>
            <section class="detail-section">
              <h4 class="detail-h">{{ t('pluginDetailVersion') }}</h4>
              <p class="detail-meta">
                {{ t('pluginDetailLatest') }} v{{ detail.version }}
                <template v-if="detail.installed"> · {{ t('pluginDetailInstalledVer') }} v{{ detail.installed_version }}</template>
              </p>
            </section>
            <section class="detail-section" v-if="detail.platforms && detail.platforms.length">
              <h4 class="detail-h">{{ t('pluginDetailPlatforms') }}</h4>
              <p class="detail-meta">{{ detail.platforms.join(', ') }}</p>
            </section>
          </div>

          <section class="detail-section" v-if="detailDeps(detail).length">
            <h4 class="detail-h">{{ t('pluginDetailDependencies') }}</h4>
            <div class="detail-tags">
              <span v-for="d in detailDeps(detail)" :key="d" class="detail-tag">{{ d }}</span>
            </div>
          </section>

          <section class="detail-section" v-if="detailChangelog(detail)">
            <h4 class="detail-h">{{ t('pluginDetailChangelog') }}</h4>
            <pre class="detail-changelog">{{ detailChangelog(detail) }}</pre>
          </section>
          <section class="detail-section" v-else>
            <h4 class="detail-h">{{ t('pluginDetailChangelog') }}</h4>
            <p class="detail-meta detail-muted">{{ t('pluginDetailNoChangelog') }}</p>
          </section>

          <section class="detail-section" v-if="detail.screenshots && detail.screenshots.length">
            <h4 class="detail-h">{{ t('pluginDetailScreenshots') }}</h4>
            <div class="detail-shots">
              <img v-for="(s, i) in detail.screenshots" :key="i" :src="s" class="detail-shot" alt="" />
            </div>
          </section>
        </div>

        <div class="modal-actions">
          <button class="op-btn" @click="closeDetail()">{{ t('close') }}</button>
          <a v-if="detail.downloads?.windows" class="action-btn btn-install detail-install"
            :class="{ 'is-installing': installing.has(detail.id) }"
            @click="install(detail)">
            <RefreshCw v-if="installing.has(detail.id)" :size="14" class="spin" />
            <Download v-else :size="14" />
            <span>{{ installing.has(detail.id) ? dlLabel(detail) : (detail.installed ? (detail.has_update ? t('pluginUpgrade') : t('pluginInstalled')) : t('pluginInstallFromMarket')) }}</span>
          </a>
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
.update-all-btn {
  display: inline-flex; align-items: center; gap: 5px; height: 30px; padding: 0 12px; border-radius: 6px;
  border: 1px solid rgba(233, 160, 19, 0.45); background: rgba(233, 160, 19, 0.15);
  color: #E9A013; font-size: 12px; font-weight: 600; cursor: pointer; white-space: nowrap;
}
.update-all-btn:hover:not(:disabled) { background: rgba(233, 160, 19, 0.25); }
.update-all-btn:disabled { opacity: .7; cursor: default; }
.filter-chip {
  display: inline-flex; align-items: center; gap: 4px; height: 32px; padding: 0 10px; border-radius: 16px;
  border: 1px solid var(--color-border); background: var(--color-bg-secondary);
  color: var(--color-text-secondary); font-size: 12px; cursor: pointer; white-space: nowrap;
}
.filter-chip:hover:not(:disabled) { border-color: var(--color-accent, #4a9eff); color: var(--color-accent, #4a9eff); }
.filter-chip.active { background: rgba(233, 160, 19, 0.15); border-color: rgba(233, 160, 19, 0.45); color: #E9A013; }
.filter-chip:disabled { opacity: .45; cursor: default; }
.chip-count {
  font-size: 10px; padding: 0 6px; border-radius: 8px; background: rgba(233, 160, 19, 0.2);
  color: #E9A013; font-weight: 700;
}
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

/* 插件详情弹窗 */
.plugin-detail { position: relative; max-width: 560px; width: 92%; max-height: 82vh; overflow-y: auto; }
.detail-close {
  position: absolute; top: 10px; right: 12px; width: 26px; height: 26px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-muted); cursor: pointer; font-size: 13px; line-height: 1;
}
.detail-close:hover { color: var(--color-text-primary); border-color: var(--color-accent); }
.detail-head { display: flex; align-items: center; gap: 12px; padding-right: 30px; }
.detail-icon { width: 44px; height: 44px; border-radius: 9px; background: var(--color-bg-tertiary); padding: 6px; }
.detail-icon-fallback { color: var(--color-text-muted); }
.detail-titles { min-width: 0; }
.detail-name-row { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.detail-name { font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.detail-id { font-size: 10px; color: var(--color-text-disabled); font-family: var(--font-mono, monospace); margin-top: 2px; }
.detail-body { display: flex; flex-direction: column; gap: 14px; margin: 14px 0; }
.detail-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
.detail-section { display: flex; flex-direction: column; gap: 5px; }
.detail-h { font-size: 11px; font-weight: 700; letter-spacing: .3px; color: var(--color-text-disabled); text-transform: uppercase; margin: 0; }
.detail-desc, .detail-meta { font-size: 12px; color: var(--color-text-secondary); line-height: 1.55; margin: 0; white-space: pre-wrap; }
.detail-muted { color: var(--color-text-disabled); }
.detail-tags { display: flex; flex-wrap: wrap; gap: 5px; }
.detail-tag { font-size: 10px; padding: 2px 8px; border-radius: 6px; background: var(--color-bg-tertiary); color: var(--color-text-secondary); }
.detail-changelog {
  font-size: 11px; line-height: 1.5; color: var(--color-text-secondary);
  background: var(--color-bg-primary); border: 1px solid var(--color-border); border-radius: 6px;
  padding: 8px 10px; max-height: 160px; overflow: auto; white-space: pre-wrap; word-break: break-all; margin: 0;
}
.detail-shots { display: flex; flex-wrap: wrap; gap: 8px; }
.detail-shot { width: 160px; border-radius: 6px; border: 1px solid var(--color-border); }
.detail-install { width: auto; padding: 7px 16px; }
</style>
