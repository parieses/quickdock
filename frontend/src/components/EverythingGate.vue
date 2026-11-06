<script setup lang="ts">
// EverythingGate.vue — Everything 文件搜索的前置状态门（可复用）
//
// 四态（not_installed / missing_es / stopped / running）与启停、托管安装、设置
// 全部复用 useEnvEverything —— 环境管理页用的也是这套，别在第二个页面里再写一份探测。
// 唯一要绕开的耦合：那个 composable 的 loadEverything() 带「当前选中分类必须是
// everything 才拉数据」的门（给三栏布局省请求用的），独立页没有分类概念，
// 所以传一个恒为 'everything' 的 ref。
//
// 额外补第五态：running 但设置里把「启用文件搜索」关了。此时 es.exe 明明在跑，
// EnvEverythingSearch 却直接返回空，比「没装」更容易让人误以为检索坏了。
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Download, Play, RefreshCw, Search, Settings2 } from '@lucide/vue'
import { useEnvEverything } from '../composables/useEnvEverything'

const { t } = useI18n()
const fixedId = ref('everything')
const {
  evStatus, evSettings, evBusy, evStateText, evStateHint,
  loadEverything, evRedetect, evStart, evInstall, evInstallEs,
  evOpenDownload, evOpenCliDownload, evSaveSettings,
} = useEnvEverything({ selectedId: fixedId, onStateChanged: () => {} })

const checking = computed(() => evStatus.value === null)
const ready = computed(() => evStatus.value?.state === 'running' && evStatus.value.enabled !== false)
const disabledBySetting = computed(() => evStatus.value?.state === 'running' && evStatus.value.enabled === false)
const diag = computed(() => evStatus.value?.searchDiag || '')

async function enableSearch() {
  if (!evSettings.value || evBusy.value) return
  evSettings.value = { ...evSettings.value, fileSearchEnabled: true }
  await evSaveSettings() // 内部会重新 loadEverything
}

defineExpose({ reload: loadEverything })
onMounted(loadEverything)
</script>

<template>
  <div v-if="checking" class="ev-gate">
    <div class="qd-empty"><p class="qd-empty-hint">{{ t('loading') }}</p></div>
  </div>

  <div v-else-if="!ready" class="ev-gate">
    <div class="qd-empty">
      <div class="qd-empty-icon"><Search :size="22" /></div>
      <p class="qd-empty-title">{{ disabledBySetting ? t('evSearchEnabled') : evStateText }}</p>
      <p class="qd-empty-hint">{{ disabledBySetting ? t('evSearchEnabledHint') : evStateHint }}</p>

      <div class="ev-gate-actions">
        <button
          v-if="disabledBySetting"
          class="qd-btn qd-btn-primary qd-btn-sm"
          :disabled="evBusy"
          @click="enableSearch"
        >
          <Settings2 :size="13" /> {{ t('evSearchEnabled') }}
        </button>
        <template v-else-if="evStatus?.state === 'not_installed'">
          <button class="qd-btn qd-btn-primary qd-btn-sm" :disabled="evBusy" @click="evInstall">
            <Download :size="13" /> {{ evBusy ? t('evInstalling') : t('evDownload') }}
          </button>
          <button class="qd-btn qd-btn-sm" :disabled="evBusy" @click="evOpenDownload">{{ t('evManualSite') }}</button>
        </template>
        <template v-else-if="evStatus?.state === 'missing_es'">
          <button class="qd-btn qd-btn-primary qd-btn-sm" :disabled="evBusy" @click="evInstallEs">
            <Download :size="13" /> {{ evBusy ? t('evInstallingEs') : t('evDownloadCli') }}
          </button>
          <button class="qd-btn qd-btn-sm" :disabled="evBusy" @click="evOpenCliDownload">{{ t('evManualSiteCli') }}</button>
        </template>
        <button
          v-else-if="evStatus?.state === 'stopped'"
          class="qd-btn qd-btn-primary qd-btn-sm"
          :disabled="evBusy"
          @click="evStart"
        >
          <Play :size="13" /> {{ t('envStart') }}
        </button>
        <button class="qd-btn qd-btn-sm" :disabled="evBusy" @click="evRedetect">
          <RefreshCw :size="13" /> {{ t('evRedetect') }}
        </button>
      </div>

      <p v-if="diag" class="ev-gate-diag">{{ diag === 'ipc_elevated' ? t('evDiagIpcElevated') : diag }}</p>
    </div>
  </div>

  <slot v-else :redetect="evRedetect" :reload="loadEverything" />
</template>

<style scoped>
.ev-gate {
  display: flex;
  align-items: center;
  justify-content: center;
  flex: 1;
  min-height: 0;
  overflow: auto;
}
.ev-gate-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-3);
  justify-content: center;
  margin-top: var(--space-6);
}
.ev-gate-diag {
  margin: var(--space-5) 0 0;
  padding: var(--space-4) var(--space-5);
  max-width: 36em;
  border-radius: var(--radius-sm);
  background: var(--color-warning-bg);
  color: var(--color-warning);
  font-size: 12px;
  line-height: 1.6;
  text-align: left;
}
</style>
