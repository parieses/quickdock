/**
 * 运行平台判定（前端侧）。
 *
 * 为什么需要它：插件市场索引的 `downloads` 是按 GOOS 分键的（windows/darwin/linux），
 * 而前端过去直接读 `downloads.windows` —— Windows 上恰好读对，mac/linux 上
 * 「看得到、点不动」，`platforms` 声明形同虚设。
 *
 * 为什么不用后端传：这里只用于选择下载地址，一次读取就够；为它加一个
 * 服务方法会让每次渲染都多一趟 IPC。UA 判定在 WebView2 / WKWebView / WebKitGTK
 * 三种宿主里都稳定（分别含 Windows NT / Mac OS X / Linux）。
 */
export type RuntimePlatform = 'windows' | 'darwin' | 'linux'

export function currentPlatform(): RuntimePlatform {
  const ua = (typeof navigator !== 'undefined' ? navigator.userAgent : '').toLowerCase()
  // 先判 Mac：iOS/macOS 的 UA 里也带 "like mac os x"，不含 linux 字样
  if (ua.includes('mac os x') || ua.includes('macintosh')) return 'darwin'
  if (ua.includes('linux') || ua.includes('x11')) return 'linux'
  return 'windows'
}

/**
 * 取当前平台可用的下载地址。
 *
 * 索引里没有当前平台的包时返回空串，由调用方提示「无可用下载」：
 * 宁可让 mac 用户看到一句明确的提示，也不要让他装一个 .exe（解压成功、
 * 启动失败，报错还落在插件加载器里，用户根本对不上是因为平台不对）。
 */
export function pickDownload(downloads?: Record<string, string>): string {
  if (!downloads) return ''
  const url = downloads[currentPlatform()]
  return typeof url === 'string' ? url : ''
}
