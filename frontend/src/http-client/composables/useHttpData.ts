import { ref, reactive, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  ListApiRequests, CreateApiRequest, UpdateApiRequest, DeleteApiRequest,
  ListHttpProjects, CreateHttpProject, UpdateHttpProject, DeleteHttpProject,
  ListHttpEnvironments, CreateHttpEnvironment, UpdateHttpEnvironment, DeleteHttpEnvironment,
  ListHttpFolders, CreateHttpFolder, UpdateHttpFolder, DeleteHttpFolder,
  ListHttpDocs, CreateHttpDoc, UpdateHttpDoc, DeleteHttpDoc,
} from '../../../bindings/quickdock/services/appservice'
import { unwrap } from '../../utils/api'
import { getErrorMessage } from '../../utils/error'
import type { ToastAPI, ApiRequest, HttpProject, HttpEnvironment, HttpFolder, HttpDoc } from '../../types'

interface KV { enabled: boolean; key: string; value: string }

export function useHttpData(toast: ToastAPI) {
  const { t } = useI18n()

  // ---- 数据状态 ----
  const requests = ref<ApiRequest[]>([])
  const projects = ref<HttpProject[]>([])
  const environments = ref<HttpEnvironment[]>([])
  const folders = ref<HttpFolder[]>([])
  const docs = ref<HttpDoc[]>([])
  const expandedProjects = ref<Set<string>>(new Set())
  const expandedFolders = ref<Set<string>>(new Set())
  const uncatExpanded = ref(true)

  // ---- 当前选中 ----
  const currentId = ref<string | null>(null)
  const currentProjectId = ref('')
  const currentFolderId = ref('')
  const currentDocId = ref('')
  const docName = ref('')
  const docContent = ref('')

  // ---- 环境选择 ----
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

  const envsByProject = computed(() => (pid: string) => environments.value.filter(e => (e.projectId || '') === pid))
  const activeEnvName = computed(() => {
    if (!currentProjectId.value) return ''
    const eid = activeEnv(currentProjectId.value)
    return eid ? (envsByProject.value(currentProjectId.value).find(e => e.id === eid)?.name || '') : ''
  })

  // ---- 目录树访问器 ----
  function topFolders(pid: string) {
    return folders.value.filter(f => (f.projectId || '') === pid && !(f.parentId || ''))
  }
  function topRequests(pid: string) {
    return requests.value.filter(r => (r.projectId || '') === pid && !(r.folderId || ''))
  }
  const uncategorized = computed(() => requests.value.filter(r => !r.projectId))

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

  // ---- 加载数据 ----
  let loadGen = 0
  async function load() {
    const gen = ++loadGen
    try {
      const projs = unwrap<HttpProject[]>(await ListHttpProjects()) ?? []
      if (gen !== loadGen) return // 已有更新的加载，丢弃过期响应
      projects.value = projs
      const [reqs, ...rest] = await Promise.all([
        ListApiRequests(),
        ...projs.map(p => ListHttpFolders(p.id)),
        ...projs.map(p => ListHttpEnvironments(p.id)),
        ...projs.map(p => ListHttpDocs(p.id)),
      ])
      if (gen !== loadGen) return
      requests.value = unwrap<ApiRequest[]>(reqs) ?? []
      const third = projs.length
      const folderLists = rest.slice(0, third)
      const envLists = rest.slice(third, third * 2)
      const docLists = rest.slice(third * 2)
      folders.value = folderLists.flatMap(f => unwrap<HttpFolder[]>(f) ?? [])
      environments.value = envLists.flatMap(e => unwrap<HttpEnvironment[]>(e) ?? [])
      docs.value = docLists.flatMap(d => unwrap<HttpDoc[]>(d) ?? [])
    } catch (e) {
      if (gen !== loadGen) return
      toast.error(t('httpLoadFailed') + ': ' + getErrorMessage(e))
    }
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
    const folderName = (values.name || '').trim() || t('unnamedFolder')
    try {
      let created: HttpFolder | null = null
      if (folderDialog.isRename && renameTarget.value) {
        const f = renameTarget.value
        await UpdateHttpFolder(f.id, { id: f.id, projectId: f.projectId, parentId: f.parentId, name: folderName, sort: f.sort })
      } else {
        created = unwrap<HttpFolder>(await CreateHttpFolder({ id: '', projectId: folderDialog.projectId, parentId: folderDialog.parentId, name: folderName, sort: 0 })) ?? null
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
        currentProjectId.value = ''
        currentId.value = null
      }
      toast.success(t('deleted'))
      await load()
    } catch (e) {
      toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
    }
  }

  // ---- 文档 CRUD ----
  async function newDocUnderProject(pid: string) {
    try {
      const r = unwrap<HttpDoc>(await CreateHttpDoc({ id: '', projectId: pid, folderId: '', name: t('unnamedDoc'), content: '', sort: 0 })) ?? null
      if (r) { docs.value.push(r); await loadDoc(r) }
    } catch (e) {
      toast.error(t('createFailed') + ': ' + getErrorMessage(e))
    }
  }
  async function openNewDoc(folderId: string) {
    const pid = folders.value.find(f => f.id === folderId)?.projectId || currentProjectId.value
    try {
      const r = unwrap<HttpDoc>(await CreateHttpDoc({ id: '', projectId: pid, folderId, name: t('unnamedDoc'), content: '', sort: 0 })) ?? null
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
  }
  async function flushDoc() {
    if (currentDocId.value) await saveDoc(false)
  }
  async function saveDoc(showToast = true) {
    if (!currentDocId.value) return
    const id = currentDocId.value
    const input = { id, projectId: currentProjectId.value, folderId: currentFolderId.value, name: docName.value.trim() || t('unnamedDoc'), content: docContent.value, sort: 0 }
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

  // ---- 选中请求（仅设 currentId，不填表单）----
  async function selectRequest(r: ApiRequest) {
    await flushDoc()
    currentId.value = r.id
    currentProjectId.value = r.projectId || ''
    currentFolderId.value = r.folderId || ''
    currentDocId.value = ''
    docName.value = ''
    docContent.value = ''
  }
  async function remove(r: ApiRequest) {
    if (!(await toast.confirm(t('httpConfirmDelete')))) return
    try {
      await DeleteApiRequest(r.id)
      if (currentId.value === r.id) currentProjectId.value = r.projectId || ''
      await load()
    } catch (e) {
      toast.error(t('deleteFailed') + ': ' + getErrorMessage(e))
    }
  }

  // ---- 工具函数 ----
  function jsonToKV(jsonStr: string): KV[] {
    const rows: KV[] = []
    try {
      const m = JSON.parse(jsonStr || '{}')
      for (const [k, v] of Object.entries(m)) rows.push({ enabled: true, key: k, value: String(v) })
    } catch { /* ignore */ }
    return rows
  }
  function kvToJSON(rows: KV[]): string {
    const m: Record<string, string> = {}
    for (const r of rows) if (r.enabled && r.key.trim()) m[r.key.trim()] = r.value
    return JSON.stringify(m)
  }
  function envVarsFromJSON(jsonStr: string): KV[] {
    try {
      const arr = JSON.parse(jsonStr || '[]')
      return arr.map((v: any) => ({ enabled: v.enabled !== false, key: v.key || '', value: v.value || '' }))
    } catch { return [] }
  }

  // ---- 展开折叠 ----
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

  const currentFolderName = computed(() => folders.value.find(f => f.id === currentFolderId.value)?.name || '')
  const projectName = computed(() => (pid: string) => projects.value.find(p => p.id === pid)?.name || '')

  return {
    requests, projects, environments, folders, docs,
    currentId, currentProjectId, currentFolderId, currentDocId,
    docName, docContent,
    expandedProjects, expandedFolders, uncatExpanded,
    uncategorized,
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
  }
}
