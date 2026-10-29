<script setup lang="ts">
// AppTargetPicker —— 「程序路径」目标选择器：一个输入框三种来源。
// 1. 直接手输自定义路径（v-model 透传，编辑已有值不受影响）；
// 2. 「浏览」走系统文件对话框选任意可执行文件；
// 3. 「已装应用」下拉列出后端 ScanInstalledApps 扫描到的开始菜单应用，
//    点选回填安装路径。扫描有后端缓存，首次可能要几秒（面板内有 loading 提示）。
// 供定时任务（action=app）与工作空间项目（类型=应用）两处复用。
import { ref, computed, inject, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { logErr } from '../utils/logger'
import { ScanInstalledApps, SelectLocalFile } from '../../bindings/quickdock/services/appservice'
import type { ToastAPI } from '../types'
import { ChevronDown, FolderOpen, AppWindow, LoaderCircle, Search } from '@lucide/vue'

interface InstalledApp { name: string; path: string; category: string; iconBase64?: string }

const props = defineProps<{
  modelValue: string
  placeholder?: string
}>()

const emit = defineEmits<{
  (e: 'update:modelValue', v: string): void
}>()

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

const open = ref(false)
const loading = ref(false)
const loaded = ref(false)
const apps = ref<InstalledApp[]>([])
const query = ref('')

const filtered = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return apps.value
  return apps.value.filter(a =>
    (a.name || '').toLowerCase().includes(q) || (a.path || '').toLowerCase().includes(q))
})

function close() {
  open.value = false
}

async function toggle() {
  open.value = !open.value
  if (open.value && !loaded.value) await loadApps()
}

async function loadApps() {
  if (loading.value) return
  loading.value = true
  try {
    apps.value = unwrap<InstalledApp[]>(await ScanInstalledApps()) ?? []
    loaded.value = true
  } catch (e) {
    logErr('AppTargetPicker.scan', e)
    toast.error(getErrorMessage(e))
  } finally {
    loading.value = false
  }
}

function pick(app: InstalledApp) {
  emit('update:modelValue', app.path)
  close()
}

async function browse() {
  try {
    // 取消选择时后端返回空串且 Code=0，不算错误（见 SelectLocalFile 注释）
    const p = unwrap<string>(await SelectLocalFile(t('appPickerBrowseTitle'))) ?? ''
    if (p) emit('update:modelValue', p)
  } catch (e) {
    logErr('AppTargetPicker.browse', e)
    toast.error(getErrorMessage(e))
  }
}

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLInputElement).value)
}

// 面板打开期间监听 Esc 关闭；遮罩层负责点击外部关闭
function onKeydown(e: KeyboardEvent) {
  if (open.value && e.key === 'Escape') close()
}
window.addEventListener('keydown', onKeydown)
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="ap-root">
    <div class="ap-row">
      <input
        :value="props.modelValue"
        type="text"
        class="ap-input"
        :placeholder="props.placeholder"
        @input="onInput"
      />
      <button type="button" class="ap-btn" :class="{ active: open }" :title="t('appPickerChooseApp')" @click="toggle">
        <AppWindow :size="14" />
        <ChevronDown :size="11" />
      </button>
      <button type="button" class="ap-btn" :title="t('appPickerBrowse')" @click="browse">
        <FolderOpen :size="14" />
      </button>
    </div>

    <template v-if="open">
      <!-- 透明遮罩：点击面板外任意处收起，比全局 click 监听可靠（不会吃掉面板内点击） -->
      <div class="ap-mask" @click="close"></div>
      <div class="ap-panel">
        <div class="ap-search">
          <Search :size="13" />
          <input v-model="query" type="text" :placeholder="t('appPickerSearchPh')" />
        </div>
        <div class="ap-list">
          <p v-if="loading" class="ap-hint">
            <LoaderCircle :size="13" class="ap-spin" />
            {{ t('appPickerLoading') }}
          </p>
          <p v-else-if="filtered.length === 0" class="ap-hint">{{ t('appPickerEmpty') }}</p>
          <button
            v-for="a in filtered"
            :key="a.path"
            type="button"
            class="ap-item"
            :title="a.path"
            @click="pick(a)"
          >
            <img v-if="a.iconBase64" :src="a.iconBase64" class="ap-icon" alt="" />
            <AppWindow v-else :size="14" class="ap-icon ap-icon-fallback" />
            <span class="ap-name">{{ a.name }}</span>
            <span class="ap-path">{{ a.path }}</span>
          </button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.ap-root { position: relative; }
.ap-row { display: flex; gap: 6px; }
.ap-input {
  flex: 1; min-width: 0;
  padding: 8px 12px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; outline: none;
  transition: border-color 0.12s;
}
.ap-input:focus { border-color: var(--color-accent); }
.ap-input::placeholder { color: var(--color-text-disabled); }
.ap-btn {
  display: flex; align-items: center; gap: 2px;
  padding: 0 9px;
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary); color: var(--color-text-muted);
  cursor: pointer; flex-shrink: 0;
  transition: background-color 0.12s, color 0.12s;
}
.ap-btn:hover { color: var(--color-text-primary); background: var(--color-bg-hover); }
.ap-btn.active { color: var(--color-accent); border-color: var(--color-accent); }

.ap-mask { position: fixed; inset: 0; z-index: 20; }
.ap-panel {
  position: absolute; top: calc(100% + 4px); left: 0; right: 0; z-index: 21;
  background: var(--color-surface);
  border: 1px solid var(--color-border); border-radius: var(--radius-sm);
  box-shadow: 0 8px 24px var(--color-bg-overlay);
  overflow: hidden;
}
.ap-search {
  display: flex; align-items: center; gap: 6px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--color-border);
  color: var(--color-text-disabled);
}
.ap-search input {
  flex: 1; min-width: 0;
  border: none; outline: none;
  background: transparent; color: var(--color-text-primary);
  font-size: 12px; font-family: inherit;
}
.ap-list { max-height: 240px; overflow-y: auto; }
.ap-hint {
  display: flex; align-items: center; gap: 6px;
  padding: 14px 10px; margin: 0;
  font-size: 12px; color: var(--color-text-disabled);
}
.ap-item {
  display: flex; align-items: center; gap: 8px;
  width: 100%; padding: 7px 10px;
  border: none; background: none;
  color: var(--color-text-primary);
  font-size: 12px; font-family: inherit;
  text-align: left; cursor: pointer;
}
.ap-item:hover { background: var(--color-bg-hover); }
.ap-icon { width: 16px; height: 16px; object-fit: contain; flex-shrink: 0; }
.ap-icon-fallback { color: var(--color-text-disabled); }
.ap-name { flex-shrink: 0; max-width: 40%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ap-path {
  flex: 1; min-width: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  color: var(--color-text-disabled); font-size: 11px;
}
.ap-spin { animation: ap-spin 1s linear infinite; }
@keyframes ap-spin { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
</style>
