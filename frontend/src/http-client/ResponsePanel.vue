<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy } from '@lucide/vue'
import type { ApiResponse } from '../types'
import JsonTree from '../components/JsonTree.vue'

const props = defineProps<{
  response: ApiResponse | null
  responseTab: 'body' | 'headers'
}>()

const emit = defineEmits<{
  (e: 'update:response-tab', v: 'body' | 'headers'): void
  (e: 'copy-response'): void
  (e: 'copy-header-value', v: string): void
  (e: 'set-json-expand-all', v: boolean): void
}>()

const { t } = useI18n()

const jsonData = computed(() => {
  const b = props.response?.body
  if (!b) return null
  const ct = (props.response?.headers && (props.response.headers['Content-Type'] || props.response.headers['content-type'])) || ''
  if (ct.includes('json')) {
    try { return JSON.parse(b) } catch { /* ignore */ }
  }
  const trimmed = b.trim()
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) {
    try { return JSON.parse(b) } catch { /* ignore */ }
  }
  return null
})
const isJsonResponse = computed(() => jsonData.value !== null)
const treeData = computed(() => (isJsonResponse.value ? jsonData.value : null))
const headerEntries = computed(() => Object.entries(props.response?.headers ?? {}))

const prettyBody = computed(() => {
  const b = props.response?.body
  if (!b) return ''
  const ct = props.response?.headers['Content-Type'] || props.response?.headers['content-type'] || ''
  if (ct.includes('json')) {
    try { return JSON.stringify(JSON.parse(b), null, 2) } catch { /* not strict JSON */ }
  }
  return b
})

const statusClass = computed(() => {
  const s = props.response?.status ?? 0
  if (s >= 200 && s < 300) return 'ok'
  if (s >= 400) return 'err'
  return 'warn'
})

function setJsonExpandAll(v: boolean) {
  emit('set-json-expand-all', v)
}
</script>

<template>
  <div class="resp-pane">
    <div class="resp-head">
      <span class="resp-title">{{ t('httpResponse') }}</span>
      <template v-if="response">
        <span :class="['status-badge', statusClass]">{{ response.status }}</span>
        <span class="resp-meta">{{ t('httpDuration') }}: {{ response.durationMs }} ms</span>
        <span class="resp-meta">{{ t('httpSize') }}: {{ response.size }} B</span>
        <span v-if="response.truncated" class="resp-trunc">{{ t('httpTruncated') }}</span>
        <div class="resp-head-right">
          <button :class="['resp-tab', { active: responseTab === 'body' }]" @click="emit('update:response-tab', 'body')">{{ t('httpRespBody') }}</button>
          <button :class="['resp-tab', { active: responseTab === 'headers' }]" @click="emit('update:response-tab', 'headers')">{{ t('httpRespHeaders') }}</button>
          <button class="resp-copy" :title="t('httpCopyResponse')" @click="emit('copy-response')"><Copy :size="12" /></button>
        </div>
      </template>
    </div>
    <div v-if="response" class="resp-body">
      <template v-if="responseTab === 'body'">
        <template v-if="treeData">
          <div class="json-toolbar">
            <button class="json-tool" @click="setJsonExpandAll(true)">{{ t('httpExpandAll') }}</button>
            <button class="json-tool" @click="setJsonExpandAll(false)">{{ t('httpCollapseAll') }}</button>
          </div>
          <div class="json-tree-wrap">
            <JsonTree :data="treeData" />
          </div>
        </template>
        <pre v-else class="code-pre">{{ prettyBody || t('httpNoBody') }}</pre>
      </template>
      <template v-else>
        <div v-if="headerEntries.length" class="resp-headers">
          <div v-for="([k, v]) in headerEntries" :key="k" class="hdr-row">
            <span class="hdr-key">{{ k }}</span>
            <span class="hdr-val">{{ v }}</span>
            <button class="hdr-copy" :title="t('httpCopyValue')" @click="emit('copy-header-value', v)"><Copy :size="11" /></button>
          </div>
        </div>
        <pre v-else class="code-pre">{{ t('httpNoHeaders') }}</pre>
      </template>
    </div>
    <div v-else class="resp-empty">
      <p>{{ t('httpSelectHint') }}</p>
    </div>
  </div>
</template>

<style scoped>
.resp-pane { flex: 1; display: flex; flex-direction: column; overflow: hidden; padding: 10px 12px; }
.resp-head { display: flex; align-items: center; gap: 10px; padding-bottom: 8px; flex-wrap: wrap; }
.resp-title { font-size: 13px; font-weight: 600; color: var(--color-text-muted); text-transform: uppercase; letter-spacing: 0.5px; }
.status-badge { font-size: 12px; font-weight: 700; padding: 2px 7px; border-radius: 4px; color: #fff; }
.status-badge.ok { background: #2e9e5b; }
.status-badge.warn { background: #d9920a; }
.status-badge.err { background: var(--color-danger); }
.resp-meta { font-size: 12px; color: var(--color-text-disabled); }
.resp-trunc { font-size: 12px; color: #d9920a; }
.resp-head-right { margin-left: auto; display: flex; align-items: center; gap: 4px; }
.resp-tab {
  padding: 3px 9px; border: 1px solid var(--color-border); background: none; border-radius: 4px;
  color: var(--color-text-muted); font-size: 12px; font-family: inherit; cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast);
}
.resp-tab:hover { color: var(--color-text-primary); }
.resp-tab.active { color: #fff; background: var(--color-accent); border-color: var(--color-accent); }
.resp-copy {
  background: none; border: 1px solid var(--color-border); border-radius: 4px; cursor: pointer;
  display: flex; align-items: center; justify-content: center; width: 24px; height: 22px;
  color: var(--color-text-muted); transition: color var(--transition-fast);
}
.resp-copy:hover { color: var(--color-accent); }
.resp-body { flex: 1; overflow: auto; display: flex; flex-direction: column; }
.json-toolbar { display: flex; gap: 6px; padding-bottom: 6px; flex-shrink: 0; }
.json-tool {
  padding: 3px 9px; border: 1px solid var(--color-border); background: none; border-radius: 4px;
  color: var(--color-text-muted); font-size: 12px; font-family: inherit; cursor: pointer;
  transition: color var(--transition-fast), background var(--transition-fast), border-color var(--transition-fast);
}
.json-tool:hover { color: var(--color-accent); border-color: var(--color-accent); }
.json-tree-wrap { flex: 1; overflow: auto; }
.resp-headers { display: flex; flex-direction: column; }
.hdr-row {
  display: flex; align-items: flex-start; gap: 6px; padding: 4px 6px;
  border-bottom: 1px solid var(--color-border); font-family: 'Consolas', 'Monaco', monospace; font-size: 13px;
  transition: background var(--transition-fast);
}
.hdr-row:hover { background: var(--color-bg-hover); }
.hdr-key { color: #7fb7ff; flex-shrink: 0; }
.hdr-val { flex: 1; min-width: 0; color: var(--color-text-primary); word-break: break-all; white-space: pre-wrap; }
.hdr-copy {
  background: none; border: none; cursor: pointer; padding: 2px; flex-shrink: 0;
  display: flex; align-items: center; justify-content: center;
  color: var(--color-text-disabled); border-radius: 3px; opacity: 0;
  transition: color var(--transition-fast), opacity var(--transition-fast), background var(--transition-fast);
}
.hdr-row:hover .hdr-copy { opacity: 1; }
.hdr-copy:hover { color: var(--color-accent); background: var(--color-bg-active); }
.code-pre {
  margin: 0; padding: 10px; border-radius: 6px; background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 13px; font-family: 'Consolas', 'Monaco', monospace;
  line-height: 1.5; white-space: pre-wrap; word-break: break-all;
}
.resp-empty { flex: 1; display: flex; align-items: center; justify-content: center; color: var(--color-text-disabled); font-size: 13px; }
</style>
