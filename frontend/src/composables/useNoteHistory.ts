/**
 * 笔记「修订历史」覆盖层：列表 / 预览 / 恢复。
 *
 * 从 NoteManagerPage 抽出来（那个文件 2500+ 行）。这里只放「历史」这一个域的状态与
 * 请求，页面保留覆盖层互斥（openGraph / closeOverlays 要同时动 graphOpen）和
 * 「恢复后怎么把新正文写回界面」——后者由 `applyRestoredDoc` 回调注入，
 * 因为节点树、编辑器内容、标签草稿都在页面手里。
 *
 * 与图谱同级：作为覆盖层叠在编辑区上，编辑器宿主始终在 DOM 里，切回文档时光标
 * 与撤销历史都还在（同 NoteGraphPanel 的挂法）。
 */
import { ref, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useToast } from './useToast'
import { unwrap } from '../utils/api'
import { reportError } from '../utils/reportError'
import {
  GetNoteDoc,
  GetNoteRevision,
  ListNoteRevisions,
  RestoreNoteRevision,
} from '../../bindings/quickdock/services/note/noteservice'
import type { Note } from '../types'

export interface NoteRevision {
  id: string
  noteId: string
  name: string
  content: string
  tags: string
  reason: string
  createdAt: string
}

export interface NoteHistoryDeps {
  /** 当前打开的文档 id；切文档后列表必须重载，否则「恢复」会写到上一篇去 */
  selectedDocId: Ref<string>
  /** 恢复成功后把库里的最新正文/标签写回页面（节点树 + 编辑器 + 标签草稿） */
  applyRestoredDoc: (d: Note) => void
}

export function useNoteHistory(deps: NoteHistoryDeps) {
  const { t } = useI18n()
  const toast = useToast()

  const historyOpen = ref(false)
  const historyLoading = ref(false)
  let revSeq = 0
  const revisions = ref<NoteRevision[]>([])
  const historyPreview = ref<NoteRevision | null>(null)
  const restoringRevId = ref('')
  const showRestoreConfirm = ref(false)

  async function loadRevisions() {
    const id = deps.selectedDocId.value
    if (!id) { revisions.value = []; return }
    const seq = ++revSeq
    historyLoading.value = true
    try {
      const list = unwrap<NoteRevision[]>(await ListNoteRevisions(id, 50)) || []
      // 期间切了文档：这次结果属于上一篇，丢弃
      if (seq !== revSeq || id !== deps.selectedDocId.value) return
      revisions.value = list
    } catch (e) {
      if (seq !== revSeq) return
      reportError(e, 'NoteManager:listRevisions')
    } finally { if (seq === revSeq) historyLoading.value = false }
  }

  async function previewRevision(revId: string) {
    try {
      const r = unwrap<NoteRevision>(await GetNoteRevision(revId))
      if (r) historyPreview.value = r
    } catch (e) {
      reportError(e, 'NoteManager:getRevision')
    }
  }

  function askRestore(revId: string) {
    restoringRevId.value = revId
    showRestoreConfirm.value = true
  }

  /** 恢复后重新载入当前文档（GetNoteDoc 带正文与 updatedAt），并刷新版本列表。 */
  async function reloadCurrentDoc() {
    const id = deps.selectedDocId.value
    if (!id) return
    try {
      const d = unwrap<Note | null>(await GetNoteDoc(id))
      if (!d) return
      deps.applyRestoredDoc(d)
    } catch (e) {
      reportError(e, 'NoteManager:reloadDoc')
    }
  }

  async function confirmRestore() {
    const revId = restoringRevId.value
    showRestoreConfirm.value = false
    restoringRevId.value = ''
    if (!revId) return
    // 兜底：万一列表仍是别的文档的（重载还没回来），别把那一篇的正文恢复到这里
    const rev = revisions.value.find(r => r.id === revId)
    if (rev && rev.noteId && rev.noteId !== deps.selectedDocId.value) {
      toast.error(t('notesHistoryStale'))
      void loadRevisions()
      return
    }
    try {
      unwrap(await RestoreNoteRevision(revId))
      toast.success(t('notesHistoryRestored'))
      await reloadCurrentDoc()
      await loadRevisions()
      historyPreview.value = null
    } catch (e) {
      reportError(e, 'NoteManager:restoreRevision')
    }
  }

  /**
   * 修订原因本地化。
   *
   * db 层存的是稳定枚举（save / import / rollback / clip），不能改成中文：
   * 节流判断与历史数据都按这些字面值比对。以前模板直接渲染 r.reason，
   * 中文界面里就冒出「save」「rollback」两个英文词。空值或未知值仍走
   * 「自动保存」兜底，未知枚举显示原文便于排查，不把界面弄成空白。
   */
  function revisionReasonLabel(reason: string): string {
    switch (reason) {
      case 'save': return t('notesHistoryAuto')
      case 'import': return t('notesHistoryReasonImport')
      case 'rollback': return t('notesHistoryReasonRollback')
      case 'clip': return t('notesHistoryReasonClip')
      default: return reason || t('notesHistoryAuto')
    }
  }

  /** 版本列表里的「内容前 60 字」，rune 安全截断、压掉换行。 */
  function revisionExcerpt(s: string): string {
    const chars = Array.from(String(s || '').replace(/\s+/g, ' ').trim())
    const cut = chars.slice(0, 60).join('')
    return chars.length > 60 ? cut + '…' : cut
  }

  return {
    historyOpen, historyLoading, revisions, historyPreview,
    restoringRevId, showRestoreConfirm,
    loadRevisions, previewRevision, askRestore, confirmRestore,
    revisionReasonLabel, revisionExcerpt,
  }
}
