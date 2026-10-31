// AI 输入框的图片附件：选择 / 粘贴 / 拖拽 / 链接，以及「是否允许」的能力闸门。
//
// 能力来自当前激活档案的 imageInput（both / url / base64）：Agnes 这类只收 URL
// 的模型必须禁用本地图选择，否则用户选了图、请求发出去才被后端拒。
// 闸门在前端拦一道只是体验，真正的校验在后端 ai_image.go。
import { computed, ref, type ComputedRef } from 'vue'
import type { PendingImage, ToastAPI } from '../types'

export interface AiAttachmentsDeps {
  // 当前档案允许的图片输入形态（both / url / base64），由页面按档案算好注入
  imageInputMode: ComputedRef<string>
  toast: ToastAPI
  t: (key: string, named?: Record<string, unknown>) => string
}

export function useAiAttachments(deps: AiAttachmentsDeps) {
  const { imageInputMode, toast, t } = deps

  // 待发送预览；发送后由页面清空。file 类存 dataUrl（base64），直接随请求发给模型；
  // url 类存公网链接。
  const attachments = ref<PendingImage[]>([])

  // 是否允许添加本地图片 / 公网链接（两者在 both 下都允许）。
  const allowFileImage = computed(() => imageInputMode.value === 'both' || imageInputMode.value === 'base64')
  const allowUrlImage = computed(() => imageInputMode.value === 'both' || imageInputMode.value === 'url')

  // 本地图片大小上限（与后端 10 MiB 对齐，前端先拦一道，避免把巨大 base64 塞进请求体）。
  const maxImageBytes = 10 * 1024 * 1024

  // 把 File 转 data URL（前端预览 + 随请求发送）。仅读取为 base64，格式校验交给后端
  // （magic bytes 校验，比前端扩展名更可靠），前端不做硬性拒绝以免误伤。
  function fileToDataURL(file: File): Promise<string> {
    return new Promise((resolve, reject) => {
      const r = new FileReader()
      r.onload = () => resolve(r.result as string)
      r.onerror = () => reject(new Error('读取失败'))
      r.readAsDataURL(file)
    })
  }

  async function addFiles(files: FileList | File[]) {
    if (!allowFileImage.value) { toast.error(t('aiImgOnlyUrl')); return }
    for (const file of Array.from(files)) {
      if (!file.type.startsWith('image/')) { toast.error(t('aiImgOnlyLocal')); continue }
      if (file.size > maxImageBytes) { toast.error(t('aiImageTooLarge')); continue }
      const dataUrl = await fileToDataURL(file)
      attachments.value.push({ kind: 'file', dataUrl, name: file.name || 'image' })
    }
    if (attachments.value.length) toast.success(t('aiImageAdded', { n: attachments.value.length }))
  }

  function addUrlImage(url: string) {
    const u = url.trim()
    if (!u) return
    if (!allowUrlImage.value) { toast.error(t('aiImgOnlyLocal')); return }
    if (!/^https?:\/\//i.test(u)) { toast.error(t('aiImageUrlInvalid')); return }
    attachments.value.push({ kind: 'url', url: u, name: u })
  }

  function removeAttachment(i: number) {
    attachments.value.splice(i, 1)
  }

  // 剪贴板粘贴图片：在输入框按 Ctrl+V 时，若剪贴板含图片文件则作为本地图附件。
  function onInputPaste(e: ClipboardEvent) {
    const items = e.clipboardData?.items
    if (!items) return
    const imgs: File[] = []
    for (const it of Array.from(items)) {
      if (it.kind === 'file' && it.type.startsWith('image/')) {
        const f = it.getAsFile()
        if (f) imgs.push(f)
      }
    }
    if (imgs.length) {
      e.preventDefault()
      void addFiles(imgs)
    }
  }

  // 拖拽图片到输入区：drop 时取图片文件作为本地图附件。
  function onInputDrop(e: DragEvent) {
    const files = e.dataTransfer?.files
    if (files && files.length) {
      e.preventDefault()
      const imgs = Array.from(files).filter((f) => f.type.startsWith('image/'))
      if (imgs.length) void addFiles(imgs)
    }
  }

  // 图片链接输入框（点击「图片链接」后展开的小输入框），避免每次都弹 prompt。
  const urlInputOpen = ref(false)
  const urlDraft = ref('')
  const fileInputEl = ref<HTMLInputElement | null>(null)

  function onPickFiles(e: Event) {
    const input = e.target as HTMLInputElement
    if (input.files && input.files.length) void addFiles(input.files)
    input.value = '' // 允许重复选择同一文件
  }

  function openFilePicker() {
    fileInputEl.value?.click()
  }

  function toggleUrlInput() {
    urlInputOpen.value = !urlInputOpen.value
    if (urlInputOpen.value) urlDraft.value = ''
  }

  function confirmUrlImage() {
    addUrlImage(urlDraft.value)
    urlDraft.value = ''
    urlInputOpen.value = false
  }

  return {
    attachments, allowFileImage, allowUrlImage, maxImageBytes,
    addFiles, addUrlImage, removeAttachment,
    onInputPaste, onInputDrop,
    urlInputOpen, urlDraft, fileInputEl,
    onPickFiles, openFilePicker, toggleUrlInput, confirmUrlImage,
  }
}
