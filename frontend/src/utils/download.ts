// 浏览器侧文件保存的统一出口。
//
// 应用里凡是要「把一段内存里的内容变成本地文件」的地方（CSV、Markdown、截图）
// 都走这里，避免各写一遍 a.click + revoke 的组合。踩过的两个坑记在下面：
//
// 1. a 元素必须进 DOM：部分 WebView 版本上，未挂载的 <a> 调 click() 不触发下载。
// 2. revoke 要延到下一轮事件循环：同步 revoke 会让 WebView 拿到一个空文件，
//    表现为「下载下来的文件是 0 字节」。

/** 触发一次「另存为」下载。blob 由调用方构造（CSV 要带 BOM 等差异自己处理）。 */
export function downloadBlob(filename: string, blob: Blob): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

/** 导出纯文本文件（Markdown / txt 等），按 UTF-8 写。 */
export function downloadText(filename: string, text: string, mime = 'text/plain'): void {
  downloadBlob(filename, new Blob([text], { type: `${mime};charset=utf-8` }))
}

/** 把任意字符串变成可用的文件名主体：去掉 Windows/POSIX 两套非法字符。 */
export function safeFileName(name: string, fallback = 'file'): string {
  const cleaned = String(name || '')
    .replace(/[\\/:*?"<>|\u0000-\u001f]/g, '_')
    .replace(/^\.+/, '')
    .trim()
  return cleaned || fallback
}
