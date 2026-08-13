import { ref } from 'vue'
import { ReorderApiRequests, ReorderHttpFolders } from '../../../bindings/quickdock/services/appservice'
import type { ToastAPI, HttpDragItem, HttpDropTarget, ApiRequest, HttpFolder, HttpDoc } from '../../types'

export function useHttpDrag(
  toast: ToastAPI,
  requests: { value: ApiRequest[] },
  folders: { value: HttpFolder[] },
  docs: { value: HttpDoc[] },
  load: () => Promise<void>
) {
  const draggingItem = ref<HttpDragItem | null>(null)
  const dropTarget = ref<HttpDropTarget | null>(null)

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
      toast.error('排序失败: ' + String(e))
    }
  }

  function resolveRequestDest(_item: HttpDragItem, target: HttpDropTarget): { projectId: string; folderId: string; anchorId: string } {
    if (target.kind === 'before-request') {
      const r = requests.value.find((x: ApiRequest) => x.id === target.id)
      if (!r) return { projectId: '', folderId: '', anchorId: '' }
      return { projectId: r.projectId || '', folderId: r.folderId || '', anchorId: r.id }
    }
    if (target.kind === 'into-folder') {
      const f = folders.value.find((x: HttpFolder) => x.id === target.id)
      if (!f) return { projectId: '', folderId: '', anchorId: '' }
      return { projectId: f.projectId, folderId: f.id, anchorId: '' }
    }
    if (target.kind === 'after-folder') {
      const f = folders.value.find((x: HttpFolder) => x.id === target.id)
      if (!f) return { projectId: '', folderId: '', anchorId: '' }
      return { projectId: f.projectId, folderId: f.parentId || '', anchorId: '' }
    }
    return { projectId: target.projectId, folderId: '', anchorId: '' }
  }
  function resolveFolderDest(item: HttpDragItem, target: HttpDropTarget): { projectId: string; parentId: string; anchorId: string } {
    if (target.kind === 'into-folder') {
      const f = folders.value.find((x: HttpFolder) => x.id === target.id)
      if (!f) return { projectId: '', parentId: '', anchorId: '' }
      return { projectId: f.projectId, parentId: f.id, anchorId: '' }
    }
    if (target.kind === 'after-folder') {
      const f = folders.value.find((x: HttpFolder) => x.id === target.id)
      if (!f) return { projectId: '', parentId: '', anchorId: '' }
      return { projectId: f.projectId, parentId: f.parentId || '', anchorId: f.id }
    }
    if (target.kind === 'before-request') {
      const r = requests.value.find((x: ApiRequest) => x.id === target.id)
      if (!r) return { projectId: '', parentId: '', anchorId: '' }
      return { projectId: r.projectId || '', parentId: r.folderId || '', anchorId: '' }
    }
    return { projectId: target.projectId, parentId: '', anchorId: '' }
  }
  function resolveDocDest(_item: HttpDragItem, target: HttpDropTarget): { projectId: string; folderId: string } {
    if (target.kind === 'into-folder') {
      const f = folders.value.find((x: HttpFolder) => x.id === target.id)
      if (!f) return { projectId: '', folderId: '' }
      return { projectId: f.projectId, folderId: f.id }
    }
    if (target.kind === 'after-folder') {
      const f = folders.value.find((x: HttpFolder) => x.id === target.id)
      if (!f) return { projectId: '', folderId: '' }
      return { projectId: f.projectId, folderId: f.parentId || '' }
    }
    if (target.kind === 'before-request') {
      const r = requests.value.find((x: ApiRequest) => x.id === target.id)
      if (!r) return { projectId: '', folderId: '' }
      return { projectId: r.projectId || '', folderId: r.folderId || '' }
    }
    if (target.kind === 'project-root') {
      return { projectId: target.projectId, folderId: '' }
    }
    return { projectId: '', folderId: '' }
  }

  async function moveDocTo(docId: string, projectId: string, folderId: string) {
    const { UpdateHttpDoc } = await import('../../../bindings/quickdock/services/appservice')
    const doc = docs.value.find(x => x.id === docId)
    if (!doc) return
    const maxSort = docs.value
      .filter(x => (x.projectId || '') === projectId && (x.folderId || '') === folderId && x.id !== docId)
      .reduce((m, x) => Math.max(m, x.sort), 0)
    await UpdateHttpDoc(docId, { id: docId, projectId, folderId, name: doc.name, content: doc.content, sort: maxSort + 1 })
  }

  return {
    draggingItem, dropTarget,
    onDragStart, onDragEnd, onDragOver, onDrop,
    requestIdsIn, folderIdsIn,
    resolveRequestDest, resolveFolderDest, resolveDocDest,
  }
}
