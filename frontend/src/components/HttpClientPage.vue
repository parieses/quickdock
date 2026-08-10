<script setup lang="ts">
import { ref, reactive, computed, onMounted, inject } from 'vue'
import { useI18n } from 'vue-i18n'
import { Globe, Plus, Trash2, Save, Send, X, Copy, Folder, Settings as SettingsIcon, ChevronDown, ChevronRight, FolderPlus, FileText } from '@lucide/vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import {
  ListApiRequests, CreateApiRequest, UpdateApiRequest, DeleteApiRequest, SendApiRequest,
  ListHttpProjects, CreateHttpProject, UpdateHttpProject, DeleteHttpProject,
  ListHttpEnvironments, CreateHttpEnvironment, UpdateHttpEnvironment, DeleteHttpEnvironment,
  ListHttpFolders, CreateHttpFolder, UpdateHttpFolder, DeleteHttpFolder,
  ListHttpDocs, CreateHttpDoc, UpdateHttpDoc, DeleteHttpDoc,
  ReorderApiRequests, ReorderHttpFolders,
} from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import type { ApiRequest, ApiResponse, HttpProject, HttpEnvironment, HttpFolder, HttpDoc, ToastAPI, HttpDragItem, HttpDropTarget } from '../types'
import JsonTree from './JsonTree.vue'
import CreateDialog from './CreateDialog.vue'
import HttpFolderTreeNode from './HttpFolderTreeNode.vue'

marked.setOptions({ breaks: true, gfm: true })

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

interface KV { enabled: boolean; key: string; value: string }

const requests = ref<ApiRequest[]>([])
const projects = ref<HttpProject[]>([])
const environments = ref<HttpEnvironment[]>([])
const folders = ref<HttpFolder[]>([])
const docs = ref<HttpDoc[]>([])
const currentId = ref<string | null>(null)
const currentProjectId = ref('')
const currentFolderId = ref('')
const currentDocId = ref('')
const docName = ref('')
const docContent = ref('')
const expandedFolders = ref<Set<string>>(new Set())

// 拖拽排序 / 移动状态
const draggingItem = ref<HttpDragItem | null>(null)
const dropTarget = ref<HttpDropTarget | null>(null)

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

const methods = ['GET', 'POST', 'PUT', 'DELETE', 'PATCH', 'HEAD', 'OPTIONS']
const bodyTypes = ['none', 'json', 'form', 'text', 'xml']
const authTypes = ['none', 'bearer', 'basic']

const methodClass = computed(() => 'method-' + method.value.toLowerCase())

// 项目树展开状态
const expandedProjects = ref<Set<string>>(new Set())
const uncatExpanded = ref(true)
function toggleProj(id: string) {
  const s = new Set(expandedProjects.value)
  if (s.has(id)) s.delete(id); else s.add(id)
  expandedProjects.value = s
}
function toggleFolder(id: string) {
  const s = new Set(expandedFolders.value)
  if (s.has(id)) s.delete(id); else s.add(id)
  expandedFolders.value = s
}

// 目录树访问器（按 parentId / folderId 过滤）
function topFolders(pid: string) {
  return folders.value.filter(f => (f.projectId || '') === pid && !(f.parentId || ''))
}
function topRequests(pid: string) {
  return requests.value.filter(r => (r.projectId || '') === pid && !(r.folderId || ''))
}
function folderSubtreeIds(rootId: string): Set<string> {
  const ids = new Set<string>([rootId])
  const stack = [rootId]
  while (stack.length) {
    const pid = stack.pop()!
    for (const f of folders.value) {
      if (f.parentId === pid && !ids.has(f.id)) { ids.add(f.id); stack.push(f.id) }
    }
  }
  return ids
}
function folderSubtreeCounts(rootId: string) {
  const ids = folderSubtreeIds(rootId)
  let subFolders = 0
  for (const id of ids) if (id !== rootId) subFolders++
  let reqs = 0
  for (const r of requests.value) if (r.folderId && ids.has(r.folderId)) reqs++
  return { folders: subFolders, requests: reqs }
}
function isDescendantOf(maybeChild: string, ancestorId: string): boolean {
  if (!maybeChild) return false
  const seen = new Set<string>()
  let cur: HttpFolder | undefined = folders.value.find(f => f.id === maybeChild)
  while (cur && cur.parentId) {
    if (cur.parentId === ancestorId) return true
    if (seen.has(cur.id)) break
    seen.add(cur.id)
    cur = folders.value.find(f => f.id === cur!.parentId)
  }
  return false
}
function isSelfOrDescendant(folderId: string, ancestorId: string): boolean {
  return folderId === ancestorId || isDescendantOf(folderId, ancestorId)
}

// ---- 拖拽排序 / 移动 ----
function requestIdsIn(projectId: string, folderId: string): string[] {
  return requests.value
    .filter(r => (r.projectId || '') === projectId && (r.folderId || '') === folderId)
    .sort((a, b) => (a.sort - b.sort) || (a.createdAt < b.createdAt ? -1 : 1))
    .map(r => r.id)
}
function folderIdsIn(projectId: string, parentId: string): string[] {
  return folders.value
    .filter(f => (f.projectId || '') === projectId && (f.parentId || '') === parentId)
    .sort((a, b) => (a.sort - b.sort) || (a.createdAt < b.createdAt ? -1 : 1))
    .map(f => f.id)
}

function onDragStart(item: HttpDragItem) { draggingItem.value = item }
function onDragEnd() { draggingItem.value = null; dropTarget.value = null }
function onDragOver(target: HttpDropTarget) { dropTarget.value = target }

async function onDrop(target: HttpDropTarget) {
  const item = draggingItem.value
  draggingItem.value = null
  dropTarget.value = null
  if (!item) return
  // 防环：目录不能拖到自己或子孙下
  if (item.kind === 'folder' && (target.kind === 'into-folder' || target.kind === 'after-folder') && isSelfOrDescendant(item.id, target.id)) return
  try {
    if (item.kind === 'doc') {
      const { projectId, folderId } = resolveDocDest(item, target)
      await moveDocTo(item.id, projectId, folderId)
    } else if (item.kind === 'request') {
      const { projectId, folderId, anchorId } = resolveRequestDest(item, target)
      const ids = requestIdsIn(projectId, folderId).filter(id => id !== item.id)
      let idx = ids.length
      if (anchorId) { const i = ids.indexOf(anchorId); if (i >= 0) idx = i }
      ids.splice(idx, 0, item.id)
      await ReorderApiRequests(projectId, folderId, ids)
      await load()
    } else {
      const { projectId, parentId, anchorId } = resolveFolderDest(item, target)
      const ids = folderIdsIn(projectId, parentId).filter(id => id !== item.id)
      let idx = ids.length
      if (anchorId) { const i = ids.indexOf(anchorId); if (i >= 0) idx = i + 1 }
      ids.splice(idx, 0, item.id)
      await ReorderHttpFolders(projectId, parentId, ids)
      await load()
    }
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}

function resolveRequestDest(_item: HttpDragItem, target: HttpDropTarget): { projectId: string; folderId: string; anchorId: string } {
  if (target.kind === 'before-request') {
    const r = requests.value.find(x => x.id === target.id)
    if (!r) return { projectId: '', folderId: '', anchorId: '' }
    return { projectId: r.projectId || '', folderId: r.folderId || '', anchorId: r.id }
  }
  if (target.kind === 'into-folder') {
    const f = folders.value.find(x => x.id === target.id)
    if (!f) return { projectId: '', folderId: '', anchorId: '' }
    return { projectId: f.projectId, folderId: f.id, anchorId: '' }
  }
  if (target.kind === 'after-folder') {
    const f = folders.value.find(x => x.id === target.id)
    if (!f) return { projectId: '', folderId: '', anchorId: '' }
    return { projectId: f.projectId, folderId: f.parentId || '', anchorId: '' }
  }
  return { projectId: target.projectId, folderId: '', anchorId: '' }
}
function resolveFolderDest(item: HttpDragItem, target: HttpDropTarget): { projectId: string; parentId: string; anchorId: string } {
  if (target.kind === 'into-folder') {
    const f = folders.value.find(x => x.id === target.id)
    if (!f) return { projectId: '', parentId: '', anchorId: '' }
    return { projectId: f.projectId, parentId: f.id, anchorId: '' }
  }
  if (target.kind === 'after-folder') {
    const f = folders.value.find(x => x.id === target.id)
    if (!f) return { projectId: '', parentId: '', anchorId: '' }
    return { projectId: f.projectId, parentId: f.parentId || '', anchorId: f.id }
  }
  if (target.kind === 'before-request') {
    const r = requests.value.find(x => x.id === target.id)
    if (!r) return { projectId: '', parentId: '', anchorId: '' }
    return { projectId: r.projectId || '', parentId: r.folderId || '', anchorId: '' }
  }
  return { projectId: target.projectId, parentId: '', anchorId: '' }
}
function resolveDocDest(_item: HttpDragItem, target: HttpDropTarget): { projectId: string; folderId: string } {
  if (target.kind === 'into-folder') {
    const f = folders.value.find(x => x.id === target.id)
    if (!f) return { projectId: '', folderId: '' }
    return { projectId: f.projectId, folderId: f.id }
  }
  if (target.kind === 'after-folder') {
    const f = folders.value.find(x => x.id === target.id)
    if (!f) return { projectId: '', folderId: '' }
    return { projectId: f.projectId, folderId: f.parentId || '' }
  }
  if (target.kind === 'before-request') {
    const r = requests.value.find(x => x.id === target.id)
    if (!r) return { projectId: '', folderId: '' }
    return { projectId: r.projectId || '', folderId: r.folderId || '' }
  }
  if (target.kind === 'project-root') {
    return { projectId: target.projectId, folderId: '' }
  }
  return { projectId: '', folderId: '' }
}

// 激活环境（按项目记忆）
const activeEnvs = ref<Record<string, string>>({})
function loadActiveEnvs() {
  try { activeEnvs.value = JSON.parse(localStorage.getItem('qd_http_active_envs') || '{}') } catch { activeEnvs.value = {} }
}
function persistActiveEnvs() {
  try { localStorage.setItem('qd_http_active_envs', JSON.stringify(activeEnvs.value)) } catch { /* ignore */ }
}
function activeEnv(pid: string) { return activeEnvs.value[pid] || '' }
function setActiveEnv(pid: string, eid: string) {
  if (eid) activeEnvs.value[pid] = eid; else delete activeEnvs.value[pid]
  persistActiveEnvs()
}

const uncategorized = computed(() => requests.value.filter(r => !r.projectId))
const envsByProject = computed(() => (pid: string) => environments.value.filter(e => (e.projectId || '') === pid))
function projectName(pid: string) { return projects.value.find(p => p.id === pid)?.name || '' }
const activeEnvName = computed(() => {
  if (!currentProjectId.value) return ''
  const eid = activeEnv(currentProjectId.value)
  return eid ? (envsByProject.value(currentProjectId.value).find(e => e.id === eid)?.name || '') : ''
})

function addHeader() { headerRows.value.push({ enabled: true, key: '', value: '' }) }
function addParam() { paramRows.value.push({ enabled: true, key: '', value: '' }) }
function addFormRow() { formRows.value.push({ enabled: true, key: '', value: '' }) }

function kvToJSON(rows: KV[]): string {
  const m: Record<string, string> = {}
  for (const r of rows) if (r.enabled && r.key.trim()) m[r.key.trim()] = r.value
  return JSON.stringify(m)
}
function jsonToKV(jsonStr: string): KV[] {
  const rows: KV[] = []
  try {
    const m = JSON.parse(jsonStr || '{}')
    for (const [k, v] of Object.entries(m)) rows.push({ enabled: true, key: k, value: String(v) })
  } catch { /* ignore */ }
  return rows
}
function envVarsFromJSON(jsonStr: string): KV[] {
  try {
    const arr = JSON.parse(jsonStr || '[]')
    return arr.map((v: any) => ({ enabled: v.enabled !== false, key: v.key || '', value: v.value || '' }))
  } catch { return [] }
}
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
  } catch {
    return { base: full, rows: [] }
  }
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

async function load() {
  try {
    const projs = unwrap<HttpProject[]>(await ListHttpProjects()) ?? []
    projects.value = projs
    const [reqs, ...rest] = await Promise.all([
      ListApiRequests(),
      ...projs.map(p => ListHttpFolders(p.id)),
      ...projs.map(p => ListHttpEnvironments(p.id)),
      ...projs.map(p => ListHttpDocs(p.id)),
    ])
    requests.value = unwrap<ApiRequest[]>(reqs) ?? []
    const third = projs.length
    const folderLists = rest.slice(0, third)
    const envLists = rest.slice(third, third * 2)
    const docLists = rest.slice(third * 2)
    folders.value = folderLists.flatMap(f => unwrap<HttpFolder[]>(f) ?? [])
    environments.value = envLists.flatMap(e => unwrap<HttpEnvironment[]>(e) ?? [])
    docs.value = docLists.flatMap(d => unwrap<HttpDoc[]>(d) ?? [])
    clearStaleDoc()
  } catch (e) {
    toast.error(t('httpLoadFailed') + ': ' + getErrorMessage(e))
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
  currentId.value = r.id
  currentProjectId.value = r.projectId || ''
  currentFolderId.value = r.folderId || ''
  currentDocId.value = ''
  docName.value = ''
  docContent.value = ''
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

async function save() {
  if (!url.value.trim()) {
    toast.error(t('httpUrlRequired'))
    return
  }
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
  if (!url.value.trim()) {
    toast.error(t('httpUrlRequired'))
    return
  }
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

async function remove(r: ApiRequest) {
  if (!(await toast.confirm(t('httpConfirmDelete')))) return
  try {
    await DeleteApiRequest(r.id)
    if (currentId.value === r.id) newRequest(currentProjectId.value)
    await load()
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}

async function copyResponse() {
  if (!response.value) return
  const text = responseTab.value === 'body'
    ? prettyBody.value
    : respHeadersText.value
  try {
    await navigator.clipboard.writeText(text)
    toast.success(t('copied'))
  } catch {
    toast.error(t('copyFailed'))
  }
}

const jsonData = computed(() => {
  const b = response.value?.body
  if (!b) return null
  const ct = (response.value?.headers && (response.value.headers['Content-Type'] || response.value.headers['content-type'])) || ''
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
const headerEntries = computed(() => Object.entries(response.value?.headers ?? {}))
const jsonExpandAll = ref<boolean | null>(null)
function setJsonExpandAll(v: boolean) { jsonExpandAll.value = v }

async function copyHeaderValue(v: string) {
  try {
    await navigator.clipboard.writeText(v)
    toast.success(t('copied'))
  } catch {
    toast.error(t('copyFailed'))
  }
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
  return Object.entries(response.value.headers)
    .map(([k, v]) => `${k}: ${v}`)
    .join('\n')
})

const statusClass = computed(() => {
  const s = response.value?.status ?? 0
  if (s >= 200 && s < 300) return 'ok'
  if (s >= 400) return 'err'
  return 'warn'
})

const hasBody = computed(() =>
  method.value !== 'GET' && method.value !== 'HEAD' && bodyType.value !== 'none',
)
const bodyPlaceholder = computed(() => {
  switch (bodyType.value) {
    case 'json': return '{\n  "key": "value"\n}'
    case 'xml': return '<root>\n  <item>value</item>\n</root>'
    case 'text': return t('httpRawBody')
    default: return ''
  }
})

// ---- 项目右键菜单（新建请求 / 新建目录） ----
const projMenu = reactive({ visible: false, x: 0, y: 0, projectId: '' })
function openProjMenu(e: MouseEvent, projectId: string) {
  e.preventDefault()
  projMenu.x = e.clientX
  projMenu.y = e.clientY
  projMenu.projectId = projectId
  projMenu.visible = true
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

// ---- 请求右键菜单（删除） ----
const reqMenu = reactive({ visible: false, x: 0, y: 0, req: null as ApiRequest | null })
function openReqMenu(e: MouseEvent, req: ApiRequest) {
  e.preventDefault()
  reqMenu.x = e.clientX
  reqMenu.y = e.clientY
  reqMenu.req = req
  reqMenu.visible = true
}
function deleteReqFromMenu() {
  reqMenu.visible = false
  if (reqMenu.req) remove(reqMenu.req)
}

// ---- 项目 CRUD ----
const showProjectDialog = ref(false)
function newProject() { showProjectDialog.value = true }
async function handleNewProject(values: Record<string, string>) {
  try {
    const r = unwrap<HttpProject>(await CreateHttpProject({ id: '', name: values.name, headers: '{}', sort: 0 }))
    if (r) { projects.value.push(r); toggleProj(r.id) }
    showProjectDialog.value = false
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}

const showProjModal = ref(false)
const projModalId = ref('')
const projModalName = ref('')
const projModalHeaders = ref<KV[]>([])
function openProjSettings(p: HttpProject) {
  projModalId.value = p.id
  projModalName.value = p.name
  projModalHeaders.value = jsonToKV(p.headers)
  showProjModal.value = true
}
async function saveProjSettings() {
  try {
    await UpdateHttpProject(projModalId.value, { id: projModalId.value, name: projModalName.value, headers: kvToJSON(projModalHeaders.value), sort: 0 })
    const p = projects.value.find(x => x.id === projModalId.value)
    if (p) p.name = projModalName.value
    toast.success(t('saved'))
    showProjModal.value = false
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}
async function deleteProject(p: HttpProject) {
  if (!(await toast.confirm(t('httpConfirmDeleteProject')))) return
  try {
    await DeleteHttpProject(p.id)
    projects.value = projects.value.filter(x => x.id !== p.id)
    environments.value = environments.value.filter(x => x.projectId !== p.id)
    docs.value = docs.value.filter(x => x.projectId !== p.id)
    delete activeEnvs.value[p.id]
    if (currentProjectId.value === p.id) { currentProjectId.value = ''; newRequest('') }
    clearStaleDoc()
    toast.success(t('deleted'))
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}

// ---- 目录 CRUD ----
const folderDialog = reactive({ visible: false, projectId: '', parentId: '', isRename: false })
const renameTarget = ref<HttpFolder | null>(null)

function openNewFolder(projectId: string, parentId = '') {
  folderDialog.visible = true
  folderDialog.projectId = projectId
  folderDialog.parentId = parentId
  folderDialog.isRename = false
  renameTarget.value = null
}
function openRenameFolder(f: HttpFolder) {
  folderDialog.visible = true
  folderDialog.projectId = f.projectId
  folderDialog.parentId = f.parentId
  folderDialog.isRename = true
  renameTarget.value = f
}
async function confirmFolder(values: Record<string, string>) {
  const name = (values.name || '').trim() || '目录'
  try {
    let created: HttpFolder | null = null
    if (folderDialog.isRename && renameTarget.value) {
      const f = renameTarget.value
      await UpdateHttpFolder(f.id, { id: f.id, projectId: f.projectId, parentId: f.parentId, name, sort: f.sort })
    } else {
      created = unwrap<HttpFolder>(await CreateHttpFolder({ id: '', projectId: folderDialog.projectId, parentId: folderDialog.parentId, name, sort: 0 })) ?? null
    }
    toast.success(t('saved'))
    folderDialog.visible = false
    await load()
    if (created) {
      const s = new Set(expandedFolders.value)
      if (folderDialog.parentId) s.add(folderDialog.parentId)
      s.add(created.id)
      expandedFolders.value = s
    }
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}
async function deleteFolder(f: HttpFolder) {
  const c = folderSubtreeCounts(f.id)
  const msg = `${t('httpDeleteFolderConfirm')} (${c.folders} ${t('httpItems')} / ${c.requests} req)`
  if (!(await toast.confirm(msg))) return
  try {
    await DeleteHttpFolder(f.id)
    if (currentFolderId.value === f.id || isDescendantOf(currentFolderId.value, f.id)) {
      newRequest(currentProjectId.value)
    }
    toast.success(t('deleted'))
    await load()
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}
const currentFolderName = computed(() => folders.value.find(f => f.id === currentFolderId.value)?.name || '')

// ---- 文档 CRUD（目录下的 Markdown 笔记） ----
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
async function newDocUnderProject(pid: string) {
  try {
    const r = unwrap<HttpDoc>(await CreateHttpDoc({ id: '', projectId: pid, folderId: '', name: '未命名文档', content: '', sort: 0 })) ?? null
    if (r) {
      docs.value.push(r)
      await loadDoc(r)
    }
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}
async function openNewDoc(folderId: string) {
  const pid = folders.value.find(f => f.id === folderId)?.projectId || currentProjectId.value
  try {
    const r = unwrap<HttpDoc>(await CreateHttpDoc({ id: '', projectId: pid, folderId, name: '未命名文档', content: '', sort: 0 })) ?? null
    if (r) {
      docs.value.push(r)
      await loadDoc(r)
      const s = new Set(expandedFolders.value); s.add(folderId); expandedFolders.value = s
    }
  } catch (e) {
    toast.error(t('createFailed') + ': ' + getErrorMessage(e))
  }
}
async function loadDoc(d: HttpDoc) {
  await flushDoc()
  currentDocId.value = d.id
  currentProjectId.value = d.projectId || ''
  currentFolderId.value = d.folderId || ''
  docName.value = d.name
  docContent.value = d.content
  currentId.value = null
  response.value = null
}
async function flushDoc() {
  if (currentDocId.value) await saveDoc(false)
}
async function saveDoc(showToast = true) {
  if (!currentDocId.value) return
  const id = currentDocId.value
  const input = { id, projectId: currentProjectId.value, folderId: currentFolderId.value, name: docName.value.trim() || '未命名文档', content: docContent.value, sort: 0 }
  try {
    const r = unwrap<HttpDoc>(await UpdateHttpDoc(id, input))
    if (r) {
      const local = docs.value.find(x => x.id === id)
      if (local) { local.name = r.name; local.content = r.content }
    }
    if (showToast) toast.success(t('saved'))
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}
async function deleteDoc(d: HttpDoc) {
  if (!(await toast.confirm(t('httpDeleteDocConfirm')))) return
  try {
    await DeleteHttpDoc(d.id)
    docs.value = docs.value.filter(x => x.id !== d.id)
    if (currentDocId.value === d.id) { currentDocId.value = ''; docName.value = ''; docContent.value = '' }
    toast.success(t('deleted'))
  } catch (e) {
    toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
  }
}
// 拖拽移动文档到目标目录/项目根（放置到目标容器末尾）
async function moveDocTo(docId: string, projectId: string, folderId: string) {
  const d = docs.value.find(x => x.id === docId)
  if (!d) return
  const maxSort = docs.value
    .filter(x => (x.projectId || '') === projectId && (x.folderId || '') === folderId && x.id !== docId)
    .reduce((m, x) => Math.max(m, x.sort), 0)
  await UpdateHttpDoc(docId, { id: docId, projectId, folderId, name: d.name, content: d.content, sort: maxSort + 1 })
  d.projectId = projectId
  d.folderId = folderId
  d.sort = maxSort + 1
  if (currentDocId.value === docId) { currentProjectId.value = projectId; currentFolderId.value = folderId }
}

// ---- 环境管理 ----
const showEnvModal = ref(false)
const envModalProjectId = ref('')
const envEditList = ref<{ id: string; name: string; variables: KV[] }[]>([])
const envEditOriginalIds = ref<string[]>([])
function openEnvModal(p: HttpProject) {
  envModalProjectId.value = p.id
  const list = environments.value.filter(e => (e.projectId || '') === p.id)
  envEditList.value = list.map(e => ({ id: e.id, name: e.name, variables: envVarsFromJSON(e.variables) }))
  envEditOriginalIds.value = list.map(e => e.id)
  showEnvModal.value = true
}
function addEnv() { envEditList.value.push({ id: '', name: '', variables: [] }) }
function deleteEnv(idx: number) { envEditList.value.splice(idx, 1) }
function addEnvVar(eidx: number) { envEditList.value[eidx].variables.push({ enabled: true, key: '', value: '' }) }
function deleteEnvVar(eidx: number, vidx: number) { envEditList.value[eidx].variables.splice(vidx, 1) }
async function saveEnvs() {
  const pid = envModalProjectId.value
  try {
    for (const e of envEditList.value) {
      const variables = JSON.stringify(e.variables.filter(v => v.key.trim()).map(v => ({ key: v.key.trim(), value: v.value, enabled: v.enabled })))
      const input = { id: e.id, projectId: pid, name: e.name, variables, sort: 0 }
      if (e.id) {
        await UpdateHttpEnvironment(e.id, input)
      } else {
        const r = unwrap<HttpEnvironment>(await CreateHttpEnvironment(input))
        if (r) e.id = r.id
      }
    }
    const desiredIds = new Set(envEditList.value.map(e => e.id).filter(Boolean))
    for (const oid of envEditOriginalIds.value) {
      if (!desiredIds.has(oid)) await DeleteHttpEnvironment(oid)
    }
    if (activeEnv(pid) && !desiredIds.has(activeEnv(pid))) {
      delete activeEnvs.value[pid]; persistActiveEnvs()
    }
    const list = unwrap<HttpEnvironment[]>(await ListHttpEnvironments(pid)) ?? []
    environments.value = environments.value.filter(e => (e.projectId || '') !== pid).concat(list)
    toast.success(t('saved'))
    showEnvModal.value = false
  } catch (e) {
    toast.error(t('saveFailed') + ': ' + getErrorMessage(e))
  }
}

onMounted(() => { loadActiveEnvs(); load() })
</script>

<template>
  <div class="http-client">
    <!-- 左：项目树 -->
    <div class="req-list">
      <div class="req-list-head">
        <span>{{ t('httpClient') }}</span>
        <button class="icon-btn" :title="t('httpNewProject')" @click="newProject">
          <FolderPlus :size="15" />
        </button>
      </div>
      <div class="req-items">
        <!-- 项目 -->
        <div v-for="p in projects" :key="p.id" class="proj-node">
          <div
            class="proj-head"
            :class="{ 'drop-root': dropTarget?.kind === 'project-root' && dropTarget.projectId === p.id }"
            @click="toggleProj(p.id)"
            @contextmenu.prevent="openProjMenu($event, p.id)"
            @dragover.prevent="onDragOver({ kind: 'project-root', projectId: p.id })"
            @drop.prevent="onDrop({ kind: 'project-root', projectId: p.id })"
          >
            <component :is="expandedProjects.has(p.id) ? ChevronDown : ChevronRight" :size="13" class="proj-caret" />
            <Folder :size="13" class="proj-icon" />
            <span class="proj-name" :title="p.name">{{ p.name }}</span>
          </div>
          <div v-if="expandedProjects.has(p.id)" class="proj-children">
            <div
              v-for="r in topRequests(p.id)"
              :key="r.id"
              :class="['req-item', { active: r.id === currentId, 'drop-before': dropTarget?.kind === 'before-request' && dropTarget.id === r.id, dragging: draggingItem?.kind === 'request' && draggingItem?.id === r.id }]"
              draggable="true"
              @click="loadRequest(r)"
              @dragstart="onDragStart({ kind: 'request', id: r.id })"
              @dragend="onDragEnd"
              @dragover.prevent="onDragOver({ kind: 'before-request', id: r.id })"
              @drop.prevent="onDrop({ kind: 'before-request', id: r.id })"
              @contextmenu.prevent="openReqMenu($event, r)"
            >
              <span :class="['req-method', 'method-' + (r.method || 'GET').toLowerCase()]">{{ r.method || 'GET' }}</span>
              <span class="req-name" :title="r.name || r.url">{{ r.name || r.url }}</span>
            </div>
            <HttpFolderTreeNode
              v-for="f in topFolders(p.id)"
              :key="f.id"
              :folder="f"
              :folders="folders"
              :requests="requests"
              :docs="docs"
              :expanded="expandedFolders"
              :current-id="currentId || ''"
              :current-doc-id="currentDocId"
              :on-toggle="toggleFolder"
              :dragging-item="draggingItem"
              :drop-target="dropTarget"
              :on-drag-start="onDragStart"
              :on-drag-end="onDragEnd"
              :on-drag-over="onDragOver"
              :on-drop="onDrop"
              @add-request="id => newRequest(p.id, id)"
              @add-folder="id => openNewFolder(p.id, id)"
              @add-doc="openNewDoc"
              @rename-folder="openRenameFolder"
              @delete-folder="deleteFolder"
              @select-request="loadRequest"
              @delete-request="remove"
              @select-doc="loadDoc"
              @delete-doc="deleteDoc"
            />
            <!-- 环境选择 -->
            <div class="env-row">
              <span class="env-label">{{ t('httpEnv') }}</span>
              <select class="env-select" :value="activeEnv(p.id)" @change="setActiveEnv(p.id, ($event.target as HTMLSelectElement).value)">
                <option value="">{{ t('httpNoEnv') }}</option>
                <option v-for="e in envsByProject(p.id)" :key="e.id" :value="e.id">{{ e.name }}</option>
              </select>
            </div>
          </div>
        </div>

        <!-- 未分类 -->
        <div class="proj-node">
          <div
            class="proj-head"
            :class="{ 'drop-root': dropTarget?.kind === 'project-root' && dropTarget.projectId === '' }"
            @click="uncatExpanded = !uncatExpanded"
            @contextmenu.prevent="openProjMenu($event, '')"
            @dragover.prevent="onDragOver({ kind: 'project-root', projectId: '' })"
            @drop.prevent="onDrop({ kind: 'project-root', projectId: '' })"
          >
            <component :is="uncatExpanded ? ChevronDown : ChevronRight" :size="13" class="proj-caret" />
            <span class="proj-name uncat">{{ t('httpUncategorized') }}</span>
          </div>
          <div v-if="uncatExpanded" class="proj-children">
            <div
              v-for="r in uncategorized"
              :key="r.id"
              :class="['req-item', { active: r.id === currentId, 'drop-before': dropTarget?.kind === 'before-request' && dropTarget.id === r.id, dragging: draggingItem?.kind === 'request' && draggingItem?.id === r.id }]"
              draggable="true"
              @click="loadRequest(r)"
              @dragstart="onDragStart({ kind: 'request', id: r.id })"
              @dragend="onDragEnd"
              @dragover.prevent="onDragOver({ kind: 'before-request', id: r.id })"
              @drop.prevent="onDrop({ kind: 'before-request', id: r.id })"
              @contextmenu.prevent="openReqMenu($event, r)"
            >
              <span :class="['req-method', 'method-' + (r.method || 'GET').toLowerCase()]">{{ r.method || 'GET' }}</span>
              <span class="req-name" :title="r.name || r.url">{{ r.name || r.url }}</span>
            </div>
            <div v-if="!uncategorized.length" class="proj-empty">{{ t('httpNoRequest') }}</div>
          </div>
        </div>
      </div>
    </div>

    <!-- 右：编辑 + 响应 -->
    <div class="req-main">
      <template v-if="!currentDocId">
      <!-- 请求行 -->
      <div class="req-line">
        <select v-model="method" class="method-select" :class="methodClass">
          <option v-for="m in methods" :key="m" :value="m">{{ m }}</option>
        </select>
        <input
          v-model="url"
          class="url-input"
          type="text"
          :placeholder="t('httpUrl') + ' (https://...)'"
          @keyup.enter="send"
        />
        <button class="send-btn" :disabled="sending" @click="send">
          <Send :size="14" />
          <span>{{ sending ? t('loading') : t('send') }}</span>
        </button>
        <button class="save-btn" :title="t('save')" @click="save">
          <Save :size="14" />
          <span>{{ t('save') }}</span>
        </button>
      </div>

      <!-- 名称行 -->
      <div class="name-line">
        <label class="name-label">{{ t('httpSaveAs') }}</label>
        <input v-model="name" class="name-input" type="text" :placeholder="t('httpName')" />
      </div>

      <!-- 上下文：所属项目 + 目录 + 激活环境 -->
      <div v-if="currentProjectId" class="ctx-line">
        <Folder :size="12" class="ctx-icon" />
        <span class="ctx-proj">{{ projectName(currentProjectId) }}</span>
        <template v-if="currentFolderName">
          <span class="ctx-sep">·</span>
          <Folder :size="12" class="ctx-icon" />
          <span class="ctx-folder">{{ currentFolderName }}</span>
        </template>
        <template v-if="activeEnvName">
          <span class="ctx-sep">·</span>
          <SettingsIcon :size="12" class="ctx-icon" />
          <span class="ctx-env">{{ activeEnvName }}</span>
        </template>
        <span class="ctx-hint">{{ t('httpEnvHint') }}</span>
      </div>

      <!-- Tabs -->
      <div class="req-tabs">
        <button :class="['tab', { active: activeTab === 'params' }]" @click="activeTab = 'params'">
          {{ t('httpParams') }}
          <span v-if="paramRows.filter(r => r.enabled && r.key).length" class="tab-badge">{{ paramRows.filter(r => r.enabled && r.key).length }}</span>
        </button>
        <button :class="['tab', { active: activeTab === 'headers' }]" @click="activeTab = 'headers'">
          {{ t('httpHeaders') }}
          <span v-if="headerRows.filter(r => r.enabled && r.key).length" class="tab-badge">{{ headerRows.filter(r => r.enabled && r.key).length }}</span>
        </button>
        <button :class="['tab', { active: activeTab === 'body' }]" @click="activeTab = 'body'">{{ t('httpBody') }}</button>
        <button :class="['tab', { active: activeTab === 'auth' }]" @click="activeTab = 'auth'">{{ t('httpAuth') }}</button>
      </div>

      <div class="req-editor">
        <!-- 查询参数 -->
        <div v-show="activeTab === 'params'" class="tab-pane">
          <div class="kv-editor">
            <div v-for="(row, i) in paramRows" :key="'p' + i" class="kv-row">
              <input type="checkbox" v-model="row.enabled" class="kv-check" :title="t('httpEnabled')" />
              <input v-model="row.key" class="kv-key" :placeholder="t('httpKey')" />
              <input v-model="row.value" class="kv-val" :placeholder="t('httpValue')" />
              <button class="kv-del" @click="paramRows.splice(i, 1)"><X :size="12" /></button>
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
              <button class="kv-del" @click="headerRows.splice(i, 1)"><X :size="12" /></button>
            </div>
            <button class="kv-add" @click="addHeader"><Plus :size="12" /> {{ t('httpAddHeader') }}</button>
          </div>
          <p v-if="currentProjectId" class="kv-hint">{{ t('httpInheritHint') }}</p>
        </div>

        <!-- 请求体 -->
        <div v-show="activeTab === 'body'" class="tab-pane">
          <div class="body-type-row">
            <label>{{ t('httpBodyType') }}</label>
            <select v-model="bodyType" class="mini-select" :disabled="!hasBody">
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
                <button class="kv-del" @click="formRows.splice(i, 1)"><X :size="12" /></button>
              </div>
              <button class="kv-add" @click="addFormRow"><Plus :size="12" /> {{ t('httpAddRow') }}</button>
            </div>
          </template>
          <textarea
            v-else-if="bodyType !== 'none'"
            v-model="body"
            class="code-area"
            spellcheck="false"
            :placeholder="bodyPlaceholder"
          ></textarea>
          <p v-else class="kv-hint">{{ t('httpTypeNone') }}</p>
        </div>

        <!-- 认证 -->
        <div v-show="activeTab === 'auth'" class="tab-pane">
          <div class="auth-row">
            <label>{{ t('httpAuthType') }}</label>
            <select v-model="authType" class="mini-select">
              <option v-for="a in authTypes" :key="a" :value="a">{{ t('httpAuth' + a.charAt(0).toUpperCase() + a.slice(1)) }}</option>
            </select>
          </div>
          <template v-if="authType === 'bearer'">
            <label class="field-label">{{ t('httpBearerToken') }}</label>
            <input v-model="authToken" class="text-input" type="text" placeholder="xxxxxxxx" />
          </template>
          <template v-else-if="authType === 'basic'">
            <label class="field-label">{{ t('httpBasicUser') }}</label>
            <input v-model="authUser" class="text-input" type="text" placeholder="user" />
            <label class="field-label">{{ t('httpBasicPass') }}</label>
            <input v-model="authPass" class="text-input" type="password" placeholder="******" />
          </template>
          <p v-else class="kv-hint">{{ t('httpAuthNone') }}</p>
        </div>
      </div>

      <!-- 响应区 -->
      <div class="resp-pane">
        <div class="resp-head">
          <span class="resp-title">{{ t('httpResponse') }}</span>
          <template v-if="response">
            <span :class="['status-badge', statusClass]">{{ response.status }}</span>
            <span class="resp-meta">{{ t('httpDuration') }}: {{ response.durationMs }} ms</span>
            <span class="resp-meta">{{ t('httpSize') }}: {{ response.size }} B</span>
            <span v-if="response.truncated" class="resp-trunc">{{ t('httpTruncated') }}</span>
            <div class="resp-head-right">
              <button :class="['resp-tab', { active: responseTab === 'body' }]" @click="responseTab = 'body'">{{ t('httpRespBody') }}</button>
              <button :class="['resp-tab', { active: responseTab === 'headers' }]" @click="responseTab = 'headers'">{{ t('httpRespHeaders') }}</button>
              <button class="resp-copy" :title="t('httpCopyResponse')" @click="copyResponse"><Copy :size="12" /></button>
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
                <button class="hdr-copy" :title="t('httpCopyValue')" @click="copyHeaderValue(v)"><Copy :size="11" /></button>
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

      <template v-else>
        <!-- 文档编辑器（目录下的 Markdown 笔记） -->
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
    </div>

    <!-- 新建项目弹窗 -->
    <CreateDialog :visible="showProjectDialog" :title="t('httpNewProject')"
      :fields="[{ key: 'name', label: t('httpProjectName'), type: 'text', placeholder: t('httpProjectName') }]"
      @confirm="handleNewProject" @cancel="showProjectDialog = false" />

    <!-- 项目右键菜单：与目录右键一致（新建 / 设置 / 删除） -->
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

    <!-- 请求右键菜单：删除 -->
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

    <!-- 项目设置弹窗（共享头） -->
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
.http-client { display: flex; height: 100%; overflow: hidden; }
.req-list { width: 250px; flex-shrink: 0; display: flex; flex-direction: column; background: var(--color-bg-secondary); border-right: 1px solid var(--color-border); overflow: hidden; }
.req-list-head {
  display: flex; align-items: center; justify-content: space-between; padding: 10px 12px;
  font-size: 13px; font-weight: 600; color: var(--color-text-muted); text-transform: uppercase;
  letter-spacing: 0.5px; border-bottom: 1px solid var(--color-border);
}
.icon-btn {
  background: none; border: none; color: var(--color-text-disabled);
  cursor: pointer; display: flex; align-items: center; justify-content: center;
  width: 24px; height: 24px; border-radius: var(--radius-sm);
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.icon-btn:hover { color: var(--color-accent); background: var(--color-bg-hover); }
.req-items { flex: 1; overflow-y: auto; padding: 6px; }
.req-items { flex: 1; overflow-y: auto; padding: 6px; }

/* 项目树 */
.proj-node { margin-bottom: 4px; }
.proj-head {
  display: flex; align-items: center; gap: 5px; padding: 5px 6px; border-radius: 6px;
  cursor: pointer; color: var(--color-text-secondary); font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.proj-head:hover { background: var(--color-bg-hover); }
.proj-caret { color: var(--color-text-disabled); flex-shrink: 0; }
.proj-icon { color: var(--color-accent); flex-shrink: 0; }
.proj-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-weight: 600; }
.proj-name.uncat { font-weight: 500; color: var(--color-text-muted); }
.proj-head.drop-root { background: var(--color-accent-bg); box-shadow: inset 0 0 0 1px var(--color-accent); }
.proj-children { padding: 2px 0 4px 14px; }
.proj-empty { padding: 8px; text-align: center; color: var(--color-text-disabled); font-size: 12px; }

/* 环境选择行 */
.env-row { display: flex; align-items: center; gap: 5px; padding: 4px 6px 2px; }
.env-label { font-size: 10px; color: var(--color-text-disabled); flex-shrink: 0; }
.env-select {
  flex: 1; min-width: 0; padding: 4px 6px; border-radius: 5px; font-size: 12px; font-family: inherit;
  border: 1px solid var(--color-border); background: var(--color-bg-tertiary); color: var(--color-text-primary); outline: none;
}
.env-select:focus { border-color: var(--color-border-focus); }

/* 请求项 */
.req-item {
  display: flex; align-items: center; gap: 6px;
  padding: 6px 8px; border-radius: 6px; cursor: pointer;
  color: var(--color-text-secondary); font-size: 13px;
  transition: background-color var(--transition-fast), color var(--transition-fast);
}
.req-item:hover { background: var(--color-bg-hover); }
.req-item.active { background: var(--color-bg-tertiary); color: var(--color-accent); }
.req-method { font-size: 10px; font-weight: 700; flex-shrink: 0; padding: 1px 4px; border-radius: 3px; color: #fff; }
.req-name { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

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
.ctx-icon { flex-shrink: 0; }
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

/* 弹窗（复用通用样式） */
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

/* 右键上下文菜单（项目级，与目录节点共用视觉） */
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
</style>
