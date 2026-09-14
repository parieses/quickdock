<script setup lang="ts">
// 工作空间页顶部的场景环境状态条：常驻展示「当前场景绑定了哪些服务、起了没」，
// 并提供一键应用与配置入口。
//
// 设计意图：场景化的价值是「切场景，服务跟着走」，这个效果必须常驻可见——
// 只藏在配置弹窗里等于没做。状态条同时承担该功能的可发现性入口。
import { computed, inject, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { EnvList, EnvStatus } from '../../bindings/quickdock/services/env/environmentservice'
import { SceneEnvApply, SceneEnvList } from '../../bindings/quickdock/services/scene/sceneservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { useWorkspaceStore } from '../stores/workspace'
import SceneEnvDialog from './SceneEnvDialog.vue'
import type { ToastAPI } from '../types'

interface Bound { runtime: string; version: string }
interface Chip { id: string; name: string; version: string; running: boolean }

const store = useWorkspaceStore()
const { t } = useI18n()
const toast = inject<ToastAPI>('toast')

const chips = ref<Chip[]>([])
const busy = ref(false)
const showDialog = ref(false)

const sceneId = computed(() => store.activeSceneId ?? '')
const sceneName = computed(() => store.scenes.find(s => s.id === sceneId.value)?.name ?? '')
const runningCount = computed(() => chips.value.filter(c => c.running).length)

// 版本解析优先级与后端 ResolveVersion 一致：显式绑定版本 → 激活版本 → 第一个已装版本。
async function load() {
  if (!sceneId.value) { chips.value = []; return }
  try {
    const rts = unwrap<any[]>(await EnvList()) ?? []
    const bound = unwrap<Bound[]>(await SceneEnvList(sceneId.value)) ?? []
    const byId = new Map(rts.map((r: any) => [r.id, r]))
    chips.value = await Promise.all(bound.map(async (b) => {
      const rt: any = byId.get(b.runtime)
      const installed: any[] = rt?.installed ?? []
      const version = b.version || installed.find(i => i.active)?.version || installed[0]?.version || ''
      let running = false
      if (version) {
        try {
          running = !!(unwrap<{ running: boolean }>(await EnvStatus(b.runtime, version))?.running)
        } catch { /* 单次探测失败按未运行处理，不打断整条状态 */ }
      }
      return { id: b.runtime, name: rt?.name ?? b.runtime, version, running }
    }))
  } catch {
    chips.value = []
  }
}

async function onApply() {
  busy.value = true
  try {
    unwrap(await SceneEnvApply(sceneId.value))
    await load()
  } catch (e) {
    toast?.error(getErrorMessage(e))
  } finally {
    busy.value = false
  }
}

function closeDialog() {
  showDialog.value = false
  load()
}

// 切场景刷新状态；工作空间页由 v-if 挂载，离开再进入会自动重查（够用，无需轮询）
watch(sceneId, load)
onMounted(load)
</script>

<template>
  <div v-if="sceneId" class="scene-env-bar">
    <template v-if="chips.length">
      <span class="bar-label">{{ t('sceneEnvBarRunning', { n: runningCount, total: chips.length }) }}</span>
      <span class="bar-chips">
        <span v-for="c in chips" :key="c.id" :class="['bar-chip', { on: c.running }]">
          <i class="bar-dot" />
          <span class="bar-chip-name">{{ c.name }}</span>
          <em v-if="c.version">{{ c.version }}</em>
        </span>
      </span>
    </template>
    <span v-else class="bar-label muted">{{ t('sceneEnvBarEmpty') }}</span>

    <span class="bar-spacer" />

    <button v-if="chips.length" class="bar-btn" :disabled="busy" @click="onApply">
      {{ t('sceneEnvApply') }}
    </button>
    <button class="bar-btn" @click="showDialog = true">{{ t('sceneEnvConfigure') }}</button>

    <SceneEnvDialog :visible="showDialog" :scene-id="sceneId" :scene-name="sceneName" @close="closeDialog" />
  </div>
</template>

<style scoped>
.scene-env-bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
  padding: 0 12px;
  min-height: 30px;
  font-size: 12px;
  color: var(--color-text-secondary);
  background: var(--color-bg-secondary);
  border-bottom: 1px solid var(--color-border);
}

.bar-label {
  white-space: nowrap;
  color: var(--color-text-secondary);
}

.bar-label.muted { color: var(--color-text-muted); }

.bar-chips {
  display: flex;
  align-items: center;
  gap: 6px;
  overflow: hidden;
  min-width: 0;
}

.bar-chip {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  padding: 1px 8px;
  border-radius: 10px;
  border: 1px solid var(--color-border);
  color: var(--color-text-muted);
  white-space: nowrap;
}

.bar-chip-name { color: var(--color-text-secondary); }

.bar-chip em {
  font-style: normal;
  font-size: 11px;
  color: var(--color-text-muted);
}

.bar-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--color-text-disabled);
}

.bar-chip.on .bar-dot { background: var(--color-success); }

.bar-spacer { flex: 1; }

.bar-btn {
  flex-shrink: 0;
  padding: 2px 10px;
  font-size: 12px;
  border-radius: 6px;
  border: 1px solid var(--color-border);
  background: transparent;
  color: var(--color-text-secondary);
  cursor: pointer;
}

.bar-btn:hover:not(:disabled) {
  background: var(--color-bg-hover);
  color: var(--color-text-primary);
}

.bar-btn:disabled { opacity: 0.5; cursor: default; }
</style>
