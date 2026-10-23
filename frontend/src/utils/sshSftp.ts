/**
 * SFTP 文件面板的纯函数层。
 *
 * 为什么单独抽：面包屑切段、路径拼接、过滤这几处最容易出边界 bug
 * （根目录多一层斜杠、本地 Windows 路径混进来），写在组件里就只能靠手点
 * 验证，抽出来可以单测。
 */
import { formatBytes } from './ssh'

/** 一条远端文件/目录记录。与 internal/sshx.FileEntry 一一对应。 */
export interface SftpEntry {
  name: string
  path: string
  size: number
  mode: string
  /** Unix 秒 */
  modTime: number
  isDir: boolean
  isLink: boolean
  linkTo: string
  ext: string
}

export interface SftpListing {
  path: string
  parent: string
  entries: SftpEntry[]
  truncated: boolean
}

/** 面包屑的一段。 */
export interface Crumb {
  name: string
  path: string
}

/**
 * 绝对路径切成面包屑。
 *
 * 用 path.split('/') 而不是逐个 join——后者会带上空的前导段，
 * 且每层都要重算一次前缀。这里一次性把累积路径推出来。
 */
export function splitPath(path: string): Crumb[] {
  const raw = String(path || '')
  const segs = raw.split('/').filter(Boolean)
  if (segs.length === 0) {
    // 根目录本身要保留一段：面包屑不能是空的，否则用户没有「回到根」的入口。
    return raw.startsWith('/') ? [{ name: '/', path: '/' }] : []
  }

  const out: Crumb[] = [{ name: '/', path: '/' }]
  let acc = ''
  for (const s of segs) {
    acc += `/${s}`
    out.push({ name: s, path: acc })
  }
  return out
}

/** 拼远端路径。name 里若夹 slash（重命名时输入的相对路径）只取最后一段。 */
export function joinPath(dir: string, name: string): string {
  const base = String(dir || '').replace(/\/+$/, '')
  const tail = String(name || '')
    .split('/')
    .filter(Boolean)
    .pop()
  // 两者的处理刻意不同：
  //   tail 为空 → 退化成目录本身，这是无害的（结果就是 dir）；
  //   base 为空 → 目录未知，拼出来的 "/a.txt" 会被当成根目录下的文件，
  //   那是一条用户没要求过的路径，宁可让它空掉逼调用方处理。
  if (!base) return ''
  if (!tail) return base
  return `${base}/${tail}`
}

/** 上一级目录。根目录返回自身，避免面包屑里出现空指针式的一层。 */
export function parentOf(path: string): string {
  const p = String(path || '/').replace(/\/+$/, '')
  const idx = p.lastIndexOf('/')
  if (idx <= 0) return '/'
  return p.slice(0, idx)
}

/** 路径 → 文件名；keepExt 为假时剥掉扩展名。 */
export function baseName(path: string, keepExt = true): string {
  const tail = String(path || '').replace(/\/+$/, '').split('/').pop() || ''
  if (keepExt) return tail
  const dot = tail.lastIndexOf('.')
  // dot 在 0 位是 dotfile（.gitignore、.env），它不是「名字 + 扩展名」结构，
  // 剥掉会得到一个空串。这类一律原样返回。
  if (dot <= 0) return tail
  return tail.slice(0, dot)
}

/** 按名称模糊过滤。目录参与过滤（用户更多是靠目录名找地方）。 */
export function filterEntries(entries: SftpEntry[], keyword: string): SftpEntry[] {
  const kw = String(keyword || '').trim().toLowerCase()
  if (!kw) return entries
  return entries.filter(e => e.name.toLowerCase().includes(kw))
}

/**
 * 能不能当文本来预览。
 *
 * 只看扩展名，不看内容：读内容要一次 IPC，为了一个 "是否可以编辑" 的
 * 判断去读一遍文件不划算。认不出来的扩展名按「可预览」处理——
 * 服务器上大量配置文件根本没后缀（hosts、nginx.conf 里的 include、
 * Dockerfile），拒掉它们的体验远比错误地尝试打开一个二进制糟。
 */
const BINARY_EXTS = new Set([
  'exe', 'dll', 'so', 'dylib', 'bin', 'iso', 'img',
  'zip', 'tar', 'gz', 'bz2', 'xz', '7z', 'rar', 'jar', 'war',
  'png', 'jpg', 'jpeg', 'gif', 'bmp', 'ico', 'webp', 'svg',
  'mp3', 'mp4', 'avi', 'mov', 'mkv', 'flv',
  'ttf', 'woff', 'woff2', 'eot', 'otf',
  'pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx',
  'deb', 'rpm', 'apk', 'msi',
])

export function isPreviewable(entry: SftpEntry): boolean {
  if (entry.isDir) return false
  return !BINARY_EXTS.has(entry.ext)
}

/**
 * 内容看起来是不是二进制。
 *
 * 为什么不能只靠扩展名：服务器上真正要看的文件常常没后缀（hosts、crontab、
 * 各种 include），而误把二进制当文本打开、改一个字再保存，等于把文件写坏。
 * 所以扩展名判定负责「一眼就知道是二进制的别发请求」，内容判定负责兜底。
 *
 * 判据：
 *   - 出现 NUL（0x00）：文本文件不会有，这是最强信号。
 *   - 控制字符（除 \t \n \r）或 U+FFFD 占比 > 2%：后者是 Go 把非法 UTF-8
 *     塞进 JSON 字符串时留下的替换符，说明原文不是合法 UTF-8。
 * 只看前 8KB：判定足够，且不会为一个 512KB 的文件扫全量。
 */
export function looksBinary(text: string): boolean {
  if (!text) return false
  const sample = text.length > 8192 ? text.slice(0, 8192) : text
  let bad = 0
  for (let i = 0; i < sample.length; i++) {
    const c = sample.charCodeAt(i)
    if (c === 0) return true
    if (c < 0x20 && c !== 9 && c !== 10 && c !== 13) bad++
    else if (c === 0xfffd) bad++
  }
  return bad / sample.length > 0.02
}

/** 文件类型分桶，用于选图标。目录单独走 isDir 判断。 */
export type EntryKind = 'archive' | 'code' | 'image' | 'video' | 'log' | 'config' | 'text'

export function entryKind(entry: SftpEntry): EntryKind {
  if (entry.isDir) return 'text'
  switch (entry.ext) {
    case 'zip': case 'tar': case 'gz': case 'bz2': case 'xz':
    case '7z': case 'rar': case 'jar': case 'war': case 'deb': case 'rpm':
      return 'archive'
    case 'png': case 'jpg': case 'jpeg': case 'gif': case 'bmp': case 'ico':
    case 'webp': case 'svg':
      return 'image'
    case 'mp4': case 'avi': case 'mov': case 'mkv': case 'flv': case 'webm':
      return 'video'
    case 'log': case 'out':
      return 'log'
    case 'json': case 'yaml': case 'yml': case 'toml': case 'ini':
    case 'conf': case 'cnf': case 'cfg': case 'env': case 'properties':
      return 'config'
    case 'ts': case 'tsx': case 'js': case 'jsx': case 'vue': case 'go':
    case 'py': case 'rs': case 'java': case 'c': case 'h': case 'cpp':
    case 'cs': case 'php': case 'rb': case 'sh': case 'bash': case 'sql':
      return 'code'
    default:
      return 'text'
  }
}

/**
 * Unix 秒 → 「今天 HH:mm / 昨天 HH:mm / YYYY-MM-DD」。
 *
 * 精确到分钟而不是秒：文件管理里没人关心 mtime 的秒数，
 * 但「今天 14:32」和「2026-03-11」的信息密度差很多，所以要分开档次。
 */
export function formatMtime(unix: number, now = Date.now()): string {
  if (!unix) return '-'
  const d = new Date(unix * 1000)
  if (Number.isNaN(d.getTime())) return '-'

  const pad = (n: number) => String(n).padStart(2, '0')
  const time = `${pad(d.getHours())}:${pad(d.getMinutes())}`

  const today = new Date(now)
  const startOfToday = new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime()
  const dayMs = 86400000

  if (d.getTime() >= startOfToday) return `今天 ${time}`
  if (d.getTime() >= startOfToday - dayMs) return `昨天 ${time}`
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${time}`
}

/** 目录不显示大小（目录 size 是自身条目占用的字节数，展示出来只会误导）。 */
export function formatEntrySize(entry: SftpEntry): string {
  return entry.isDir ? '-' : formatBytes(entry.size)
}

/** rwx 权限串转 3 位八进制。mode 串来自 Go 的 os.FileMode.String()。 */
export function permOctal(mode: string): string {
  // FileMode.String() 是 10 个字符：首字符是文件类型（- / d / l），
  // 权限位从下标 1 起。从 0 开始切会把类型符吃进来，整串算成 000。
  const bits = String(mode || '').slice(-9)
  if (bits.length < 9) return ''
  let out = ''
  for (let i = 0; i < 9; i += 3) {
    const trio = bits.slice(i, i + 3)
    let v = 0
    if (trio[0] === 'r') v += 4
    if (trio[1] === 'w') v += 2
    // s / t 是带特殊位的 x，权限值上它就是可执行。
    if (trio[2] === 'x' || trio[2] === 's' || trio[2] === 't') v += 1
    out += String(v)
  }
  return out
}
