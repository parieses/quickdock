// AI 输入框的「插入文件」：从本地选一份文档，发消息时随请求带给后端，
// 由后端按后缀解析成可读文本展开进上下文（见 services/ai/ai_attached_files.go）。
//
// 与 @ 引用（useAiMention）的区别：@ 只指项目里的文件、走 fsguard 白名单、
// 只读纯文本；插入文件走原生对话框，用户显式看见并确认了路径，工作目录之外
// 也允许，且支持 pdf/docx/xlsx。两者在后端分别展开成 <file> / <attached_file> 块。
//
// 这里只存 { path, name }，不读内容、不生成 dataUrl——原文不往返前端，
// 避免一份 40MB 的文档 base64 后在请求体里走两趟。
import { ref, type Ref } from 'vue'
import { SelectLocalFiles } from '../../bindings/quickdock/services/appservice'
import { unwrap } from '../utils/api'
import type { ToastAPI } from '../types'

export interface AttachedFile {
  id: string
  path: string // 原生对话框选中时非空；粘贴/拖入时为空
  name: string
  content?: string // data URL（粘贴/拖入来源）
  size?: number // 内容来源去重 + 预览用
}

// 与 internal/kb.SupportedExts 对齐（那里是权威，改格式两处要同步）。
const SUPPORTED_EXTS = [
  '.md', '.markdown',
  '.txt', '.text', '.log', '.csv', '.json', '.yaml', '.yml',
  '.toml', '.ini', '.conf', '.xml', '.html', '.htm',
  '.css', '.scss', '.less',
  '.go', '.php', '.js', '.mjs', '.cjs', '.ts', '.tsx', '.jsx',
  '.py', '.java', '.kt', '.kts', '.cs', '.c', '.h',
  '.cpp', '.hpp', '.cc', '.rs', '.rb', '.swift', '.dart',
  '.lua', '.pl', '.sh', '.bash', '.ps1', '.bat', '.cmd',
  '.sql', '.vue', '.svelte', '.scala', '.groovy', '.r',
  '.docx', '.xlsx', '.pdf',
]
const FILE_FILTER = SUPPORTED_EXTS.map((e) => '*' + e).join(';')

// 粘贴 / 拖入的文档大小上限（与后端 aiAttachedMaxSourceBytes=40MiB 对齐，前端先拦）。
const MAX_DOC_BYTES = 40 * 1024 * 1024
// 私钥后缀：与 internal/fsguard 的 secretFileExts 对齐，前端挡一道，后端仍会复核。
const SECRET_EXTS = ['.pem', '.key', '.env', '.p12', '.pfx', '.keystore', '.crt', '.cer', '.p8', '.asc']

function docExt(name: string): string {
  const i = name.lastIndexOf('.')
  return i >= 0 ? name.slice(i).toLowerCase() : ''
}

function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(r.result as string)
    r.onerror = () => reject(new Error('read failed'))
    r.readAsDataURL(file)
  })
}

/** 同步预判：这一批里有没有「插入文件」通道能吃的文档（图片不算）。
 *  paste/drop 要在事件同步阶段就决定 preventDefault，不能等 FileReader 异步读完。 */
export function hasDocFiles(files: File[]): boolean {
  return files.some((f) => {
    const ext = docExt(f.name || '')
    // 0 字节文件解析不出来，别让它进插入文件通道到后端才报错。
    return SUPPORTED_EXTS.includes(ext) && !SECRET_EXTS.includes(ext) && f.size > 0 && f.size <= MAX_DOC_BYTES
  })
}

export function useAiAttachedFiles(deps: {
  toast: ToastAPI
  t: (key: string, named?: Record<string, unknown>) => string
}) {
  const { toast, t } = deps
  const attachedFiles = ref<AttachedFile[]>([]) as Ref<AttachedFile[]>
  let seq = 0

  async function pickFiles() {
    const picked = unwrap<string[]>(await SelectLocalFiles(
      t('aiAttachFileTitle'),
      t('aiAttachFileFilter'),
      FILE_FILTER,
    )) ?? []
    let added = 0
    for (const path of picked) {
      // 去重：同一路径选两次没有意义，后端也会把它展开两遍浪费预算。
      if (attachedFiles.value.some((f) => f.path === path)) continue
      seq += 1
      const name = path.split(/[\\/]/).pop() || path
      attachedFiles.value.push({ id: `af-${seq}`, path, name })
      added += 1
    }
    // 报本次新增数，不是列表总数——否则第二次再选一个会显示「已插入 5 个」。
    if (added) toast.success(t('aiAttachFileAdded', { n: added }))
  }

  /**
   * 粘贴 / 拖入的文档文件 → 读成 data URL 塞进插入文件列表。
   *
   * WebView 里这些 File 没有本地路径，只能带内容；后端按 name 的后缀写临时文件
   * 再解析。图片不在这里处理（useAiAttachments 走 base64 图片通道）。
   * 返回 true 表示至少拦下了一个文档文件，调用方应 preventDefault。
   */
  async function addDroppedFiles(files: File[]): Promise<boolean> {
    const docs = files.filter((f) => {
      const ext = docExt(f.name || '')
      if (!SUPPORTED_EXTS.includes(ext)) return false
      if (SECRET_EXTS.includes(ext)) return false
      if (f.size <= 0 || f.size > MAX_DOC_BYTES) return false
      return true
    })
    if (!docs.length) return false
    let added = 0
    let failed = 0
    for (const f of docs) {
      // 内容来源按 name + size 去重：重复粘贴同一份不该展开两遍吃预算。
      if (attachedFiles.value.some((x) => !x.path && x.name === f.name && x.size === f.size)) continue
      let dataUrl: string
      try {
        dataUrl = await fileToDataURL(f)
      } catch (err) {
        // FileReader.onerror 会 reject；这里不希望它冒泡成未处理的 Promise rejection
        // （调用处是 void addDroppedFiles）。跳过这份文件，其余照常加入。
        console.warn('[ai] 读取拖入文件失败:', f.name, err)
        failed += 1
        continue
      }
      seq += 1
      attachedFiles.value.push({
        id: `af-${seq}`,
        path: '',
        name: f.name || 'pasted-file',
        content: dataUrl,
        size: f.size,
      })
      added += 1
    }
    if (added) toast.success(t('aiAttachFileAdded', { n: added }))
    if (failed) toast.error(t('loadFailed')) // 复用已有通用文案，不新增 i18n key
    return true
  }

  function removeFile(i: number) {
    attachedFiles.value.splice(i, 1)
  }

  function clear() {
    attachedFiles.value = []
  }

  return { attachedFiles, pickFiles, addDroppedFiles, removeFile, clear }
}
