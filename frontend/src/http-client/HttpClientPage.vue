<script setup lang="ts">
import { SendApiRequest, UpdateApiRequest, CreateApiRequest } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { ref, reactive, computed, onMounted, inject, provide } from 'vue'
import { useI18n } from 'vue-i18n'
import { Folder, Plus, Trash2, Save, Send, X, Copy, FolderPlus, Settings as SettingsIcon, ChevronDown, ChevronRight, FileText, Globe } from '@lucide/vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import type { ApiRequest, ApiResponse, HttpProject, HttpFolder, HttpDoc, ToastAPI } from '../types'
import ProjectTree from './ProjectTree.vue'
import RequestForm from './RequestForm.vue'
import ResponsePanel from './ResponsePanel.vue'
import CreateDialog from '../components/CreateDialog.vue'
import { useHttpData } from './composables/useHttpData'
import { useHttpDrag } from './composables/useHttpDrag'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ---- 从 composable 获取数据逻辑 ----
const {
  requests, projects, environments, folders, docs,
  currentId, currentProjectId, currentFolderId, currentDocId,
  docName, docContent,
  expandedProjects, expandedFolders, uncatExpanded, uncategorized,
  activeEnvs, envsByProject, activeEnvName,
  load, newProject, handleNewProject,
  showProjectDialog, showProjModal, projModalId, projModalName, projModalHeaders,
  openProjSettings, saveProjSettings, deleteProject,
  folderDialog, renameTarget, openNewFolder, openRenameFolder, confirmFolder, deleteFolder,
  newDocUnderProject, openNewDoc, loadDoc, flushDoc, saveDoc, deleteDoc, moveDocTo,
  showEnvModal, envModalProjectId, envEditList, envEditOriginalIds,
  openEnvModal, addEnv, deleteEnv, addEnvVar, deleteEnvVar, saveEnvs,
  selectRequest, remove,
  topFolders, topRequests, folderSubtreeCounts, isDescendantOf, isSelfOrDescendant,
  jsonToKV, kvToJSON, envVarsFromJSON,
  toggleProj, toggleFolder,
  currentFolderName, projectName,
  activeEnv, setActiveEnv,
  loadActiveEnvs,
} = useHttpData(toast)

// ---- 拖拽 ----
const { draggingItem, dropTarget, onDragStart, onDragEnd, onDragOver, onDrop } = useHttpDrag(toast, requests, folders, docs, load)

// ---- 请求表单状态（页面独有）----
interface KV { enabled: boolean; key: string; value: string }
const name = ref('')
const method = ref('GET')
const url = ref('')
const paramRows = ref<KV[]>([])
const headerRows = ref<KV[]>([])
const bodyType = ref('none')
const body = ref('')
const formRows = ref<KV[]>([])
const authType = ref('none')
const authToken = ref('')
const authUser = ref('')
const authPass = ref('')
const activeTab = ref<'params' | 'headers' | 'body' | 'auth'>('headers')
const responseTab = ref<'body' | 'headers'>('body')
const sending = ref(false)
const response = ref<ApiResponse | null>(null)
const jsonExpandAll = ref<boolean | null>(null)

// 向 JsonTree 子组件提供全局展开/收起控制信号
provide('jsonTreeCtrl', jsonExpandAll)

const methods = ['GET', 'POST', 'PUT', 'DELETE', 'PATCH', 'HEAD', 'OPTIONS']
const bodyTypes = ['none', 'json', 'form', 'text', 'xml']
const authTypes = ['none', 'bearer', 'basic']

// ---- 文档预览 ----
const docPreview = computed(() => {
  if (!docContent.value) return ''
  return DOMPurify.sanitize(marked.parse(docContent.value, { async: false }) as string)
})
const currentDoc = computed<HttpDoc | null>(() => docs.value.find(d => d.id === currentDocId.value) || null)
function deleteCurrentDoc() { if (currentDoc.value) deleteDoc(currentDoc.value) }
function clearStaleDoc() {
  if (currentDocId.value && !docs.value.find(d => d.id === currentDocId.value)) {
    currentDocId.value = ''
    docName.value = ''
    docContent.value = ''
  }
}

// ---- 表单工具函数 ----
function addHeader() { headerRows.value.push({ enabled: true, key: '', value: '' }) }
function addParam() { paramRows.value.push({ enabled: true, key: '', value: '' }) }
function addFormRow() { formRows.value.push({ enabled: true, key: '', value: '' }) }
function kvToQuery(rows: KV[]): string {
  const p = new URLSearchParams()
  for (const r of rows) if (r.enabled && r.key.trim()) p.append(r.key.trim(), r.value)
  return p.toString()
}
function parseQuery(full: string): { base: string; rows: KV[] } {
  try {
    const u = new URL(full)
    const rows: KV[] = []
    u.searchParams.forEach((v, k) => rows.push({ enabled: true, key: k, value: v }))
    return { base: u.origin + u.pathname + u.hash, rows }
  } catch { return { base: full, rows: [] } }
}
function formToQuery(rows: KV[]): string {
  const p = new URLSearchParams()
  for (const r of rows) if (r.enabled && r.key.trim()) p.append(r.key.trim(), r.value)
  return p.toString()
}
function queryToForm(str: string): KV[] {
  const rows: KV[] = []
  new URLSearchParams(str || '').forEach((v, k) => rows.push({ enabled: true, key: k, value: v }))
  return rows
}

function buildInput() {
  const q = kvToQuery(paramRows.value)
  const fullUrl = q ? url.value + (url.value.includes('?') ? '&' : '?') + q : url.value
  let finalBody = body.value
  if (bodyType.value === 'form') finalBody = formToQuery(formRows.value)
  const pid = currentProjectId.value || ''
  const eid = pid ? activeEnv(pid) : ''
  return {
    id: currentId.value ?? '',
    projectId: pid,
    folderId: currentFolderId.value,
    environmentId: eid,
    name: name.value.trim(),
    method: method.value,
    url: fullUrl,
    headers: kvToJSON(headerRows.value),
    body: finalBody,
    bodyType: bodyType.value,
    authType: authType.value,
    authToken: authToken.value,
    authUser: authUser.value,
    authPass: authPass.value,
    sort: 0,
  }
}

function newRequest(pid = '', folderId = '') {
  currentId.value = null
  currentProjectId.value = pid
  currentFolderId.value = folderId
  currentDocId.value = ''
  docName.value = ''
  docContent.value = ''
  name.value = ''
  method.value = 'GET'
  url.value = ''
  paramRows.value = []
  headerRows.value = [{ enabled: true, key: 'Content-Type', value: 'application/json' }]
  body.value = ''
  formRows.value = []
  bodyType.value = 'none'
  authType.value = 'none'
  authToken.value = ''
  authUser.value = ''
  authPass.value = ''
  response.value = null
  activeTab.value = 'headers'
}

function loadRequest(r: ApiRequest) {
  selectRequest(r)
  name.value = r.name || ''
  method.value = r.method || 'GET'
  const { base, rows } = parseQuery(r.url)
  url.value = base
  paramRows.value = rows
  headerRows.value = jsonToKV(r.headers)
  bodyType.value = r.bodyType || 'none'
  if (bodyType.value === 'form') formRows.value = queryToForm(r.body)
  else body.value = r.body
  authType.value = r.authType || 'none'
  authToken.value = r.authToken
  authUser.value = r.authUser
  authPass.value = r.authPass
  response.value = null
}

// ---- 请求操作 ----

async function save() {
  if (!url.value.trim()) { toast.error(t('httpUrlRequired')); return }
  const input = buildInput()
  try {
    let r: ApiRequest | null
    if (currentId.value) {
      r = unwrap<ApiRequest>(await UpdateApiRequest(currentId.value, input))
    } else {
      r = unwrap<ApiRequest>(await CreateApiRequest(input))
    }
    toast.success(t('httpSaved'))
    await load()
    if (r) loadRequest(r)
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}

async function send() {
  if (!url.value.trim()) { toast.error(t('httpUrlRequired')); return }
  sending.value = true
  response.value = null
  responseTab.value = 'body'
  try {
    const resp = unwrap<ApiResponse>(await SendApiRequest(buildInput()))
    response.value = resp ?? null
    if (resp && !resp.ok) toast.error(t('httpStatusCode') + ' ' + resp.status)
  } catch (e) {
    toast.error(t('sendFailed') + ': ' + getErrorMessage(e))
  } finally {
    sending.value = false
  }
}

async function copyResponse() {
  if (!response.value) return
  const text = responseTab.value === 'body' ? prettyBody.value : respHeadersText.value
  try { await navigator.clipboard.writeText(text); toast.success(t('copied')) }
  catch { toast.error(t('copyFailed')) }
}

async function copyHeaderValue(v: string) {
  try { await navigator.clipboard.writeText(v); toast.success(t('copied')) }
  catch { toast.error(t('copyFailed')) }
}

const prettyBody = computed(() => {
  const b = response.value?.body
  if (!b) return ''
  const ct = response.value?.headers['Content-Type'] || response.value?.headers['content-type'] || ''
  if (ct.includes('json')) {
    try { return JSON.stringify(JSON.parse(b), null, 2) } catch { /* not strict JSON */ }
  }
  return b
})

const respHeadersText = computed(() => {
  if (!response.value) return ''
  return Object.entries(response.value.headers).map(([k, v]) => `${k}: ${v}`).join('\n')
})

const hasBody = computed(() => method.value !== 'GET' && method.value !== 'HEAD' && bodyType.value !== 'none')
const bodyPlaceholder = computed(() => {
  switch (bodyType.value) {
    case 'json': return '{\n  "key": "value"\n}'
    case 'xml': return '<root>\n  <item>value</item>\n</root>'
    case 'text': return t('httpRawBody')
    default: return ''
  }
})

// ---- 右键菜单 ----
const projMenu = reactive({ visible: false, x: 0, y: 0, projectId: '' })
function openProjMenu(e: MouseEvent, projectId: string) {
  e.preventDefault()
  projMenu.x = e.clientX; projMenu.y = e.clientY
  projMenu.projectId = projectId; projMenu.visible = true
}
function runProjMenu(action: 'add-request' | 'add-folder' | 'add-doc' | 'manage-env' | 'settings' | 'delete') {
  projMenu.visible = false
  const pid = projMenu.projectId
  const p = projects.value.find(x => x.id === pid)
  if (action === 'add-request') newRequest(pid)
  else if (action === 'add-folder') openNewFolder(pid)
  else if (action === 'add-doc') newDocUnderProject(pid)
  else if (action === 'manage-env' && p) openEnvModal(p)
  else if (action === 'settings' && p) openProjSettings(p)
  else if (action === 'delete' && p) deleteProject(p)
}

const reqMenu = reactive({ visible: false, x: 0, y: 0, req: null as ApiRequest | null })
function openReqMenu(e: MouseEvent, req: ApiRequest) {
  e.preventDefault()
  reqMenu.x = e.clientX; reqMenu.y = e.clientY
  reqMenu.req = req; reqMenu.visible = true
}
function deleteReqFromMenu() {
  reqMenu.visible = false
  if (reqMenu.req) remove(reqMenu.req)
}

onMounted(() => { loadActiveEnvs(); load() })
</script>

<template>
  <div class="http-client">
    <!-- 左：项目树 -->
    <ProjectTree
      :projects="projects"
      :folders="folders"
      :requests="requests"
      :docs="docs"
      :environments="environments"
      :expanded-projects="expandedProjects"
      :expanded-folders="expandedFolders"
      :current-id="currentId"
      :current-doc-id="currentDocId"
      :dragging-item="draggingItem"
      :drop-target="dropTarget"
      :active-envs="activeEnvs"
      :uncat-expanded="uncatExpanded"
      @new-project="newProject"
      @toggle-project="toggleProj"
      @toggle-uncategorized="uncatExpanded = !uncatExpanded"
      @toggle-folder="toggleFolder"
      @select-request="loadRequest"
      @select-doc="loadDoc"
      @new-request="newRequest"
      @new-folder="openNewFolder"
      @new-doc="newDocUnderProject"
      @rename-folder="openRenameFolder"
      @delete-folder="deleteFolder"
      @delete-request="remove"
      @delete-doc="deleteDoc"
      @set-active-env="setActiveEnv"
      @drag-start="onDragStart"
      @drag-end="onDragEnd"
      @drag-over="onDragOver"
      @drop="onDrop"
      @open-proj-menu="openProjMenu"
    />

    <!-- 右：请求编辑 + 响应 -->
    <template v-if="!currentDocId">
      <RequestForm
        :method="method"
        :url="url"
        :name="name"
        :current-project-id="currentProjectId"
        :current-folder-id="currentFolderId"
        :active-env-name="activeEnvName"
        :param-rows="paramRows"
        :header-rows="headerRows"
        :body-type="bodyType"
        :body="body"
        :form-rows="formRows"
        :auth-type="authType"
        :auth-token="authToken"
        :auth-user="authUser"
        :auth-pass="authPass"
        :active-tab="activeTab"
        :sending="sending"
        :has-body="hasBody"
        :body-placeholder="bodyPlaceholder"
        :methods="methods"
        :body-types="bodyTypes"
        :auth-types="authTypes"
        :project-name="projectName"
        :folder-name="(id: string) => folders.find(f => f.id === id)?.name || ''"
        @update:method="method = $event"
        @update:url="url = $event"
        @update:name="name = $event"
        @update:body-type="bodyType = $event"
        @update:body="body = $event"
        @update:auth-type="authType = $event"
        @update:auth-token="authToken = $event"
        @update:auth-user="authUser = $event"
        @update:auth-pass="authPass = $event"
        @update:active-tab="activeTab = $event"
        @send="send"
        @save="save"
        @add-param="addParam"
        @remove-param="(i) => paramRows.splice(i, 1)"
        @add-header="addHeader"
        @remove-header="(i) => headerRows.splice(i, 1)"
        @add-form-row="addFormRow"
        @remove-form-row="(i) => formRows.splice(i, 1)"
      />
      <ResponsePanel
        :response="response"
        :response-tab="responseTab"
        @update:response-tab="responseTab = $event"
        @copy-response="copyResponse"
        @copy-header-value="copyHeaderValue"
        @set-json-expand-all="jsonExpandAll = $event"
      />
    </template>

    <!-- 文档编辑器 -->
    <template v-else>
      <div class="doc-editor-wrap">
        <div class="name-line">
          <label class="name-label"><FileText :size="13" /> {{ t('httpDocName') }}</label>
          <input v-model="docName" class="name-input" type="text" :placeholder="t('httpDocName')" />
          <button class="save-btn" :title="t('save')" @click="saveDoc(true)"><Save :size="14" /> <span>{{ t('save') }}</span></button>
          <button class="cancel-btn doc-del-btn" :title="t('httpDeleteDoc')" @click="deleteCurrentDoc"><Trash2 :size="14" /></button>
        </div>
        <div v-if="currentDoc" class="doc-ctx">{{ projectName(currentDoc.projectId) }}<template v-if="currentFolderName"> · {{ currentFolderName }}</template></div>
        <div class="doc-split">
          <textarea v-model="docContent" class="doc-input" spellcheck="false" :placeholder="'# ' + t('httpNewDoc')"></textarea>
          <div class="doc-preview markdown-body" v-html="docPreview"></div>
        </div>
      </div>
    </template>

    <!-- 弹窗 -->
    <CreateDialog :visible="showProjectDialog" :title="t('httpNewProject')"
      :fields="[{ key: 'name', label: t('httpProjectName'), type: 'text', placeholder: t('httpProjectName') }]"
      @confirm="handleNewProject" @cancel="showProjectDialog = false" />

    <!-- 项目右键菜单 -->
    <div v-if="projMenu.visible" class="ctx-menu-mask" @click="projMenu.visible = false" @contextmenu.prevent="projMenu.visible = false">
      <div class="ctx-menu" :style="{ left: projMenu.x + 'px', top: projMenu.y + 'px' }" @click.stop>
        <button class="ctx-item" @click="runProjMenu('add-request')"><Plus :size="13" /> {{ t('httpNewRequest') }}</button>
        <template v-if="projMenu.projectId">
          <button class="ctx-item" @click="runProjMenu('add-folder')"><FolderPlus :size="13" /> {{ t('httpNewFolder') }}</button>
          <button class="ctx-item" @click="runProjMenu('add-doc')"><FileText :size="13" /> {{ t('httpNewDoc') }}</button>
          <div class="ctx-sep"></div>
          <button class="ctx-item" @click="runProjMenu('manage-env')"><SettingsIcon :size="13" /> {{ t('httpManageEnv') }}</button>
          <button class="ctx-item" @click="runProjMenu('settings')"><Globe :size="13" /> {{ t('httpUpdateProject') }}</button>
          <button class="ctx-item danger" @click="runProjMenu('delete')"><Trash2 :size="13" /> {{ t('delete') }}</button>
        </template>
      </div>
    </div>

    <!-- 请求右键菜单 -->
    <div v-if="reqMenu.visible" class="ctx-menu-mask" @click="reqMenu.visible = false" @contextmenu.prevent="reqMenu.visible = false">
      <div class="ctx-menu" :style="{ left: reqMenu.x + 'px', top: reqMenu.y + 'px' }" @click.stop>
        <button class="ctx-item danger" @click="deleteReqFromMenu"><Trash2 :size="13" /> {{ t('httpDeleteRequest') }}</button>
      </div>
    </div>

    <!-- 新建/重命名目录弹窗 -->
    <CreateDialog
      :visible="folderDialog.visible"
      :title="folderDialog.isRename ? t('httpRenameFolder') : t('httpNewFolder')"
      :fields="[{ key: 'name', label: t('httpFolderName'), type: 'text', placeholder: t('httpFolderName') }]"
      :edit-values="folderDialog.isRename && renameTarget ? { name: renameTarget.name } : undefined"
      @confirm="confirmFolder"
      @cancel="folderDialog.visible = false"
    />

    <!-- 项目设置弹窗 -->
    <div v-if="showProjModal" class="modal-mask" @click.self="showProjModal = false">
      <div class="modal">
        <div class="modal-head">
          <span>{{ t('httpProjectSettings') }}</span>
          <button class="icon-btn" @click="showProjModal = false"><X :size="15" /></button>
        </div>
        <div class="modal-body">
          <label class="field-label">{{ t('httpProjectName') }}</label>
          <input v-model="projModalName" class="text-input" type="text" />
          <label class="field-label">{{ t('httpSharedHeaders') }}</label>
          <p class="kv-hint">{{ t('httpSharedHeadersHint') }}</p>
          <div class="kv-editor">
            <div v-for="(row, i) in projModalHeaders" :key="'ph' + i" class="kv-row">
              <input type="checkbox" v-model="row.enabled" class="kv-check" :title="t('httpEnabled')" />
              <input v-model="row.key" class="kv-key" :placeholder="t('httpKey')" />
              <input v-model="row.value" class="kv-val" :placeholder="t('httpValue')" />
              <button class="kv-del" @click="projModalHeaders.splice(i, 1)"><X :size="12" /></button>
            </div>
            <button class="kv-add" @click="projModalHeaders.push({ enabled: true, key: '', value: '' })"><Plus :size="12" /> {{ t('httpAddHeader') }}</button>
          </div>
        </div>
        <div class="modal-actions">
          <div class="spacer" />
          <button class="cancel-btn" @click="showProjModal = false">{{ t('cancel') }}</button>
          <button class="save-btn" @click="saveProjSettings"><Save :size="13" /> <span>{{ t('save') }}</span></button>
        </div>
      </div>
    </div>

    <!-- 环境管理弹窗 -->
    <div v-if="showEnvModal" class="modal-mask" @click.self="showEnvModal = false">
      <div class="modal wide">
        <div class="modal-head">
          <span>{{ t('httpManageEnv') }}</span>
          <button class="icon-btn" @click="showEnvModal = false"><X :size="15" /></button>
        </div>
        <div class="modal-body env-modal-body">
          <div v-for="(env, i) in envEditList" :key="'env' + i" class="env-card">
            <div class="env-card-head">
              <input v-model="env.name" class="text-input env-name-input" :placeholder="t('httpEnvName')" />
              <button class="kv-del" :title="t('delete')" @click="deleteEnv(i)"><Trash2 :size="12" /></button>
            </div>
            <div class="kv-editor">
              <div v-for="(v, j) in env.variables" :key="'v' + i + '_' + j" class="kv-row">
                <input type="checkbox" v-model="v.enabled" class="kv-check" :title="t('httpEnabled')" />
                <input v-model="v.key" class="kv-key" :placeholder="t('httpKey')" />
                <input v-model="v.value" class="kv-val" :placeholder="t('httpValue')" />
                <button class="kv-del" @click="deleteEnvVar(i, j)"><X :size="12" /></button>
              </div>
              <button class="kv-add" @click="addEnvVar(i)"><Plus :size="12" /> {{ t('httpAddVar') }}</button>
            </div>
          </div>
          <div v-if="!envEditList.length" class="proj-empty">{{ t('httpNoEnv') }}</div>
          <button class="kv-add env-add" @click="addEnv"><Plus :size="12" /> {{ t('httpAddEnv') }}</button>
        </div>
        <div class="modal-actions">
          <div class="spacer" />
          <button class="cancel-btn" @click="showEnvModal = false">{{ t('cancel') }}</button>
          <button class="save-btn" @click="saveEnvs"><Save :size="13" /> <span>{{ t('save') }}</span></button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.http-client { display: flex; flex-direction: column; height: 100%; overflow: hidden; }
.req-main { flex: 0 0 auto; overflow-y: auto; display: flex; flex-direction: column; }
.resp-pane { flex: 1; min-height: 0; }

/* 弹窗 */
.modal-mask { position: fixed; inset: 0; background: rgba(0, 0, 0, 0.45); display: flex; align-items: center; justify-content: center; z-index: 200; }
.modal { width: 460px; max-width: 92vw; max-height: 86vh; overflow-y: auto; background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 12px; box-shadow: var(--shadow-lg); padding: 16px 18px; display: flex; flex-direction: column; }
.modal.wide { width: 540px; }
.modal-head { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; font-size: 14px; font-weight: 600; color: var(--color-text-primary); }
.modal-body { display: flex; flex-direction: column; gap: 6px; }
.env-modal-body { gap: 10px; }
.modal-actions { display: flex; align-items: center; gap: 8px; margin-top: 16px; }
.spacer { flex: 1; }
.cancel-btn, .save-btn {
  display: flex; align-items: center; gap: 5px; padding: 7px 14px; border-radius: 6px;
  border: 1px solid var(--color-border); cursor: pointer; font-size: 13px; font-family: inherit;
  background: var(--color-bg-tertiary); color: var(--color-text-secondary);
  transition: background-color var(--transition-fast), color var(--transition-fast), border-color var(--transition-fast);
}
.cancel-btn:hover { background: var(--color-bg-hover); color: var(--color-text-primary); }
.save-btn { background: var(--color-accent); color: #fff; border-color: var(--color-accent); }
.save-btn:hover { opacity: 0.9; color: #fff; }

/* 环境卡片 */
.env-card { border: 1px solid var(--color-border); border-radius: 8px; padding: 10px; background: var(--color-bg-secondary); }
.env-card-head { display: flex; align-items: center; gap: 6px; margin-bottom: 6px; }
.env-name-input { flex: 1; }
.env-add { margin-top: 2px; }

/* 右键上下文菜单 */
.ctx-menu-mask { position: fixed; inset: 0; z-index: 300; }
.ctx-menu {
  position: fixed; min-width: 160px; padding: 4px; border-radius: 8px;
  background: var(--color-surface); border: 1px solid var(--color-border);
  box-shadow: var(--shadow-lg); z-index: 301;
}
.ctx-item {
  display: flex; align-items: center; gap: 8px; width: 100%; padding: 7px 10px;
  border: none; background: none; cursor: pointer; border-radius: 5px;
  color: var(--color-text-primary); font-size: 13px; font-family: inherit; text-align: left;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.ctx-item:hover { background: var(--color-bg-hover); color: var(--color-accent); }
.ctx-item.danger { color: var(--color-danger); }
.ctx-item.danger:hover { background: rgba(232, 76, 76, 0.1); color: var(--color-danger); }
.ctx-sep { height: 1px; background: var(--color-border); margin: 4px 2px; }

/* 方法配色 */
.method-get { background: #2e9e5b; }
.method-post { background: #3a8ae0; }
.method-put { background: #d9920a; }
.method-delete { background: #e8584c; }
.method-patch { background: #9b6ddb; }
.method-head { background: #6b7785; }
.method-options { background: #5a8f9e; }

/* 文档编辑器 */
.doc-editor-wrap { display: flex; flex-direction: column; flex: 1; min-height: 0; overflow: hidden; }
.doc-ctx { padding: 4px 12px; font-size: 12px; color: var(--color-text-muted); border-bottom: 1px solid var(--color-border); }
.doc-split { flex: 1; display: flex; min-height: 0; }
.doc-input {
  flex: 1; min-width: 0; resize: none; padding: 12px; border: none; border-right: 1px solid var(--color-border);
  background: var(--color-bg-secondary); color: var(--color-text-primary);
  font-size: 13px; font-family: 'Consolas', 'Monaco', monospace; line-height: 1.6; outline: none; box-sizing: border-box;
}
.doc-preview { flex: 1; min-width: 0; overflow: auto; padding: 12px 16px; background: var(--color-bg-primary); }
.doc-del-btn { color: var(--color-danger); }
.doc-del-btn:hover { background: rgba(232, 76, 76, 0.1); }

/* Markdown 渲染样式（轻量） */
.markdown-body { color: var(--color-text-primary); font-size: 13px; line-height: 1.7; word-break: break-word; }
.markdown-body :deep(h1), .markdown-body :deep(h2), .markdown-body :deep(h3), .markdown-body :deep(h4) { margin: 12px 0 8px; line-height: 1.3; }
.markdown-body :deep(h1) { font-size: 20px; border-bottom: 1px solid var(--color-border); padding-bottom: 6px; }
.markdown-body :deep(h2) { font-size: 17px; border-bottom: 1px solid var(--color-border); padding-bottom: 4px; }
.markdown-body :deep(h3) { font-size: 15px; }
.markdown-body :deep(p) { margin: 8px 0; }
.markdown-body :deep(a) { color: var(--color-accent); }
.markdown-body :deep(code) { background: var(--color-bg-tertiary); padding: 1px 5px; border-radius: 4px; font-family: 'Consolas', 'Monaco', monospace; font-size: 13px; }
.markdown-body :deep(pre) { background: var(--color-bg-tertiary); padding: 10px 12px; border-radius: 6px; overflow: auto; }
.markdown-body :deep(pre code) { background: none; padding: 0; }
.markdown-body :deep(blockquote) { border-left: 3px solid var(--color-border-focus); margin: 8px 0; padding: 2px 12px; color: var(--color-text-muted); }
.markdown-body :deep(ul), .markdown-body :deep(ol) { padding-left: 22px; margin: 8px 0; }
.markdown-body :deep(table) { border-collapse: collapse; margin: 8px 0; }
.markdown-body :deep(th), .markdown-body :deep(td) { border: 1px solid var(--color-border); padding: 4px 8px; }
.markdown-body :deep(img) { max-width: 100%; }
</style>
