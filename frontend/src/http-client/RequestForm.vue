<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Save, Send, X, Plus } from '@lucide/vue'

interface Props {
  method: string
  url: string
  name: string
  currentProjectId: string
  currentFolderId: string
  activeEnvName: string
  projectName: (pid: string) => string
  folderName: (fid: string) => string
  paramRows: { enabled: boolean; key: string; value: string }[]
  headerRows: { enabled: boolean; key: string; value: string }[]
  bodyType: string
  body: string
  formRows: { enabled: boolean; key: string; value: string }[]
  authType: string
  authToken: string
  authUser: string
  authPass: string
  activeTab: 'params' | 'headers' | 'body' | 'auth'
  sending: boolean
  hasBody: boolean
  bodyPlaceholder: string
  methods: string[]
  bodyTypes: string[]
  authTypes: string[]
}

const props = defineProps<Props>()
const emit = defineEmits<{
  (e: 'update:method', v: string): void
  (e: 'update:url', v: string): void
  (e: 'update:name', v: string): void
  (e: 'update:body-type', v: string): void
  (e: 'update:body', v: string): void
  (e: 'update:auth-type', v: string): void
  (e: 'update:auth-token', v: string): void
  (e: 'update:auth-user', v: string): void
  (e: 'update:auth-pass', v: string): void
  (e: 'update:active-tab', v: 'params' | 'headers' | 'body' | 'auth'): void
  (e: 'send'): void
  (e: 'save'): void
  (e: 'add-param'): void
  (e: 'remove-param', i: number): void
  (e: 'add-header'): void
  (e: 'remove-header', i: number): void
  (e: 'add-form-row'): void
  (e: 'remove-form-row', i: number): void
}>()

const { t } = useI18n()

function addParam() { emit('add-param') }
function addHeader() { emit('add-header') }
function addFormRow() { emit('add-form-row') }
function removeParam(i: number) { emit('remove-param', i) }
function removeHeader(i: number) { emit('remove-header', i) }
function removeFormRow(i: number) { emit('remove-form-row', i) }

const methodClass = computed(() => 'method-' + props.method.toLowerCase())
const paramCount = computed(() => props.paramRows.filter(r => r.enabled && r.key).length)
const headerCount = computed(() => props.headerRows.filter(r => r.enabled && r.key).length)
</script>

<template>
  <div class="req-main">
    <!-- 请求行 -->
    <div class="req-line">
      <select :value="method" class="method-select" :class="methodClass" @change="emit('update:method', ($event.target as HTMLSelectElement).value)">
        <option v-for="m in methods" :key="m" :value="m">{{ m }}</option>
      </select>
      <input
        :value="url"
        class="url-input"
        type="text"
        :placeholder="t('httpUrl') + ' (https://...)'"
        @input="emit('update:url', ($event.target as HTMLInputElement).value)"
        @keyup.enter="emit('send')"
      />
      <button class="send-btn" :disabled="sending" @click="emit('send')">
        <Send :size="14" />
        <span>{{ sending ? t('loading') : t('send') }}</span>
      </button>
      <button class="save-btn" :title="t('save')" @click="emit('save')">
        <Save :size="14" />
        <span>{{ t('save') }}</span>
      </button>
    </div>

    <!-- 名称行 -->
    <div class="name-line">
      <label class="name-label">{{ t('httpSaveAs') }}</label>
      <input :value="name" class="name-input" type="text" :placeholder="t('httpName')" @input="emit('update:name', ($event.target as HTMLInputElement).value)" />
    </div>

    <!-- 上下文行 -->
    <div v-if="currentProjectId" class="ctx-line">
      <span class="ctx-proj">{{ projectName(currentProjectId) }}</span>
      <template v-if="currentFolderId">
        <span class="ctx-sep">·</span>
        <span class="ctx-folder">{{ folderName(currentFolderId) }}</span>
      </template>
      <template v-if="activeEnvName">
        <span class="ctx-sep">·</span>
        <span class="ctx-env">{{ activeEnvName }}</span>
      </template>
      <span class="ctx-hint">{{ t('httpEnvHint') }}</span>
    </div>

    <!-- Tabs -->
    <div class="req-tabs">
      <button :class="['tab', { active: activeTab === 'params' }]" @click="emit('update:active-tab', 'params')">
        {{ t('httpParams') }}
        <span v-if="paramCount" class="tab-badge">{{ paramCount }}</span>
      </button>
      <button :class="['tab', { active: activeTab === 'headers' }]" @click="emit('update:active-tab', 'headers')">
        {{ t('httpHeaders') }}
        <span v-if="headerCount" class="tab-badge">{{ headerCount }}</span>
      </button>
      <button :class="['tab', { active: activeTab === 'body' }]" @click="emit('update:active-tab', 'body')">{{ t('httpBody') }}</button>
      <button :class="['tab', { active: activeTab === 'auth' }]" @click="emit('update:active-tab', 'auth')">{{ t('httpAuth') }}</button>
    </div>

    <div class="req-editor">
      <!-- 查询参数 -->
      <div v-show="activeTab === 'params'" class="tab-pane">
        <div class="kv-editor">
          <div v-for="(row, i) in paramRows" :key="'p' + i" class="kv-row">
            <input type="checkbox" v-model="row.enabled" class="kv-check" :title="t('httpEnabled')" />
            <input v-model="row.key" class="kv-key" :placeholder="t('httpKey')" />
            <input v-model="row.value" class="kv-val" :placeholder="t('httpValue')" />
            <button class="kv-del" @click="removeParam(i)"><X :size="12" /></button>
          </div>
          <button class="kv-add" @click="addParam"><Plus :size="12" /> {{ t('httpAddParam') }}</button>
        </div>
      </div>

      <!-- 请求头 -->
      <div v-show="activeTab === 'headers'" class="tab-pane">
        <div class="kv-editor">
          <div v-for="(row, i) in headerRows" :key="'h' + i" class="kv-row">
            <input type="checkbox" v-model="row.enabled" class="kv-check" :title="t('httpEnabled')" />
            <input v-model="row.key" class="kv-key" :placeholder="t('httpKey')" />
            <input v-model="row.value" class="kv-val" :placeholder="t('httpValue')" />
            <button class="kv-del" @click="removeHeader(i)"><X :size="12" /></button>
          </div>
          <button class="kv-add" @click="addHeader"><Plus :size="12" /> {{ t('httpAddHeader') }}</button>
        </div>
        <p v-if="currentProjectId" class="kv-hint">{{ t('httpInheritHint') }}</p>
      </div>

      <!-- 请求体 -->
      <div v-show="activeTab === 'body'" class="tab-pane">
        <div class="body-type-row">
          <label>{{ t('httpBodyType') }}</label>
          <select :value="bodyType" class="mini-select" :disabled="!hasBody" @change="emit('update:body-type', ($event.target as HTMLSelectElement).value)">
            <option v-for="b in bodyTypes" :key="b" :value="b">{{ t('httpType' + b.charAt(0).toUpperCase() + b.slice(1)) }}</option>
          </select>
        </div>
        <template v-if="bodyType === 'form'">
          <p class="kv-hint">{{ t('httpFormHint') }}</p>
          <div class="kv-editor">
            <div v-for="(row, i) in formRows" :key="'f' + i" class="kv-row">
              <input type="checkbox" v-model="row.enabled" class="kv-check" :title="t('httpEnabled')" />
              <input v-model="row.key" class="kv-key" :placeholder="t('httpKey')" />
              <input v-model="row.value" class="kv-val" :placeholder="t('httpValue')" />
              <button class="kv-del" @click="removeFormRow(i)"><X :size="12" /></button>
            </div>
            <button class="kv-add" @click="addFormRow"><Plus :size="12" /> {{ t('httpAddRow') }}</button>
          </div>
        </template>
        <textarea
          v-else-if="bodyType !== 'none'"
          :value="body"
          class="code-area"
          spellcheck="false"
          :placeholder="bodyPlaceholder"
          @input="emit('update:body', ($event.target as HTMLTextAreaElement).value)"
        ></textarea>
        <p v-else class="kv-hint">{{ t('httpTypeNone') }}</p>
      </div>

      <!-- 认证 -->
      <div v-show="activeTab === 'auth'" class="tab-pane">
        <div class="auth-row">
          <label>{{ t('httpAuthType') }}</label>
          <select :value="authType" class="mini-select" @change="emit('update:auth-type', ($event.target as HTMLSelectElement).value)">
            <option v-for="a in authTypes" :key="a" :value="a">{{ t('httpAuth' + a.charAt(0).toUpperCase() + a.slice(1)) }}</option>
          </select>
        </div>
        <template v-if="authType === 'bearer'">
          <label class="field-label">{{ t('httpBearerToken') }}</label>
          <input :value="authToken" class="text-input" type="text" placeholder="xxxxxxxx" @input="emit('update:auth-token', ($event.target as HTMLInputElement).value)" />
        </template>
        <template v-else-if="authType === 'basic'">
          <label class="field-label">{{ t('httpBasicUser') }}</label>
          <input :value="authUser" class="text-input" type="text" placeholder="user" @input="emit('update:auth-user', ($event.target as HTMLInputElement).value)" />
          <label class="field-label">{{ t('httpBasicPass') }}</label>
          <input :value="authPass" class="text-input" type="password" placeholder="******" @input="emit('update:auth-pass', ($event.target as HTMLInputElement).value)" />
        </template>
        <p v-else class="kv-hint">{{ t('httpAuthNone') }}</p>
      </div>
    </div>
  </div>
</template>

<style scoped>
.req-main { flex: 1; min-width: 0; display: flex; flex-direction: column; overflow: hidden; }
.req-line { display: flex; align-items: center; gap: 8px; padding: 10px 12px; border-bottom: 1px solid var(--color-border); }
.method-select {
  width: 92px; flex-shrink: 0; padding: 7px 8px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 13px; font-weight: 600; font-family: inherit;
}
.url-input {
  flex: 1; min-width: 0; padding: 7px 10px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 13px; font-family: inherit; outline: none;
}
.url-input:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
.send-btn, .save-btn {
  display: flex; align-items: center; gap: 5px; flex-shrink: 0;
  padding: 7px 12px; border-radius: 6px; border: none; cursor: pointer;
  font-size: 13px; font-family: inherit; font-weight: 500;
  transition: background-color var(--transition-fast), opacity var(--transition-fast);
}
.send-btn { background: var(--color-accent); color: #fff; }
.send-btn:hover { opacity: 0.9; }
.send-btn:disabled { opacity: 0.6; cursor: default; }
.save-btn { background: var(--color-bg-tertiary); color: var(--color-text-secondary); border: 1px solid var(--color-border); }
.save-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }

.name-line { display: flex; align-items: center; gap: 8px; padding: 7px 12px; border-bottom: 1px solid var(--color-border); }
.name-label { font-size: 13px; color: var(--color-text-muted); flex-shrink: 0; }
.name-input {
  flex: 1; min-width: 0; padding: 6px 9px; border-radius: 6px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 13px; font-family: inherit; outline: none;
}
.name-input:focus { border-color: var(--color-border-focus); }

/* 上下文行 */
.ctx-line { display: flex; align-items: center; gap: 6px; padding: 4px 12px; font-size: 12px; color: var(--color-text-muted); border-bottom: 1px solid var(--color-border); }
.ctx-proj { color: var(--color-text-secondary); font-weight: 600; }
.ctx-env { color: var(--color-accent); }
.ctx-folder { color: var(--color-accent); }
.ctx-sep { color: var(--color-text-disabled); }
.ctx-hint { margin-left: auto; font-size: 10px; color: var(--color-text-disabled); }

.req-tabs { display: flex; gap: 2px; padding: 6px 12px 0; border-bottom: 1px solid var(--color-border); }
.tab {
  display: flex; align-items: center; gap: 5px;
  padding: 6px 12px; border: none; background: none; cursor: pointer;
  color: var(--color-text-muted); font-size: 13px; font-family: inherit;
  border-bottom: 2px solid transparent; transition: color var(--transition-fast);
}
.tab:hover { color: var(--color-text-primary); }
.tab.active { color: var(--color-accent); border-bottom-color: var(--color-accent); }
.tab-badge {
  font-size: 9px; font-weight: 700; background: var(--color-bg-hover);
  color: var(--color-text-secondary); border-radius: 8px; padding: 0 5px; min-width: 14px; text-align: center;
}
.tab.active .tab-badge { background: var(--color-accent-bg); color: var(--color-accent); }

.req-editor { padding: 10px 12px; border-bottom: 1px solid var(--color-border); max-height: 38%; overflow-y: auto; }
.tab-pane { display: flex; flex-direction: column; gap: 8px; }

.kv-editor { display: flex; flex-direction: column; gap: 5px; }
.kv-row { display: flex; align-items: center; gap: 6px; }
.kv-check { width: 15px; height: 15px; flex-shrink: 0; accent-color: var(--color-accent); cursor: pointer; }
.kv-key, .kv-val {
  flex: 1; min-width: 0; padding: 5px 8px; border-radius: 5px;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 13px; font-family: 'Consolas', 'Monaco', monospace; outline: none;
}
.kv-key:focus, .kv-val:focus { border-color: var(--color-border-focus); }
.kv-del {
  background: none; border: none; color: var(--color-text-disabled); cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  width: 22px; height: 22px; border-radius: 4px; flex-shrink: 0;
}
.kv-del:hover { color: var(--color-danger); background: rgba(232, 76, 76, 0.1); }
.kv-add {
  display: flex; align-items: center; justify-content: center; gap: 4px;
  padding: 5px; border: 1px dashed var(--color-border); border-radius: 5px; background: none;
  color: var(--color-text-muted); font-size: 12px; font-family: inherit; cursor: pointer;
  transition: color var(--transition-fast), border-color var(--transition-fast);
}
.kv-add:hover { color: var(--color-accent); border-color: var(--color-accent); }
.kv-hint { font-size: 12px; color: var(--color-text-disabled); margin: 0; }

.body-type-row, .auth-row { display: flex; align-items: center; gap: 8px; }
.field-label { font-size: 13px; color: var(--color-text-muted); margin-top: 4px; }
.mini-select, .text-input {
  padding: 5px 8px; border-radius: 5px; border: 1px solid var(--color-border);
  background: var(--color-bg-tertiary); color: var(--color-text-primary);
  font-size: 13px; font-family: inherit; outline: none;
}
.text-input { width: 100%; }
.text-input:focus, .mini-select:focus { border-color: var(--color-border-focus); }
.auth-row .mini-select { min-width: 120px; }
.code-area {
  width: 100%; min-height: 120px; resize: vertical; padding: 8px 10px;
  border-radius: 6px; border: 1px solid var(--color-border); background: var(--color-bg-tertiary);
  color: var(--color-text-primary); font-size: 13px; font-family: 'Consolas', 'Monaco', monospace;
  line-height: 1.5; outline: none; box-sizing: border-box;
}
.code-area:focus { border-color: var(--color-border-focus); box-shadow: 0 0 0 2px var(--color-accent-bg); }
</style>
