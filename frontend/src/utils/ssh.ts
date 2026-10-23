/**
 * SSH 相关的共享类型与常量。
 *
 * 主机/会话的类型在 bindings 里没有生成（Go 侧回的是 db.SshHost 结构体，
 * Wails 生成器只给入参类型），所以前端在这里手工声明一份。
 * 放共享文件而不是各组件各写一份：字段名一旦走样，运行时才发现就晚了。
 */

/** 主机节点。文件夹与主机同表（isFolder=true 表示文件夹）。 */
export interface SshHost {
  id: string
  name: string
  host: string
  port: number
  user: string
  /** password | key */
  authType: string
  keyPath: string
  legacyAlgorithms: boolean
  /** prod | staging | test */
  env: string
  parentId: string
  isFolder: boolean
  sort: number
  remark: string
  lastConnectedAt: string
  createdAt: string
  updatedAt: string
  /** 已保存密码/口令的标志位——明文永远不会下发到前端 */
  hasPassword: boolean
  hasPassphrase: boolean
}

/** 新建/编辑主机的入参。密码与口令留空表示「不修改」。 */
export interface SshHostInput {
  id: string
  name: string
  host: string
  port: number
  user: string
  authType: string
  password: string
  clearPassword: boolean
  keyPath: string
  keyPassphrase: string
  legacyAlgorithms: boolean
  env: string
  parentId: string
  isFolder: boolean
  sort: number
  remark: string
}

/** Connect 成功后返回的会话信息。 */
export interface SshConnectResult {
  sessionId: string
  hostId: string
  host: string
  port: number
  user: string
  serverVersion: string
  hostKeyAlgorithm: string
  fingerprint: string
}

/** 「测试连接」结果。needsTrust=true 表示连通了但服务器身份待确认。 */
export interface TestHostResult {
  trusted: boolean
  needsTrust: boolean
  serverVersion: string
  hostKeyAlgorithm: string
  fingerprint: string
  knownTypes: string[]
  latencyMs: number
}

/**
 * 指纹类错误带回的结构化数据（ApiResult.data）。
 *
 * 首次连接带 fingerprint；指纹变更带 oldFingerprint + newFingerprint。
 * 两种情况下 hostKeyType 都可能为空（引擎没拿到密钥类型时）。
 */
export interface SshHostKeyInfo {
  host?: string
  port?: number
  keyType?: string
  fingerprint?: string
  knownTypes?: string[]
  oldFingerprint?: string
  newFingerprint?: string
}

/**
 * 一条已信任的主机密钥记录（ListAllTrustedKeys 的返回项）。
 *
 * Host 用原始 host、Display 用「host:port」的展示串，两份都要：
 * 列表里显示要 Display，撤销时必须回传 host + port——只传 Display
 * 会因为端口归一化对不上而删不掉。
 */
export interface TrustedHost {
  host: string
  port: number
  /** 「host:port」，用于分组与展示 */
  display: string
  keyType: string
  fingerprint: string
  /** Unix 毫秒（Go 侧 UnixMilli 写入，直接喂 new Date 即可，不要再乘 1000） */
  addedAt: number
  /** 关联的主机 ID；主机已删除时为空串 */
  hostId: string
  /** 关联的主机名；主机已删除时为空串 */
  hostName: string
}

/** 错误码常量。与 services/sshx/errors.go 一一对应，只列前端要分支处理的。 */
export const SSH_ERR = {
  /** 首次连接某主机（或该主机提供了新的密钥类型），需展示指纹让用户确认 */
  HOSTKEY_UNKNOWN: 'ssh_hostkey_unknown',
  /** 同类型密钥指纹变了，高危，必须强告警 */
  HOSTKEY_CHANGED: 'ssh_hostkey_changed',
  NO_CREDENTIAL: 'ssh_no_credential',
  AUTH_FAILED: 'ssh_auth_failed',
  UNREACHABLE: 'ssh_unreachable',
  ALGORITHM: 'ssh_algorithm_unsupported',
  NO_AUTH_METHOD: 'ssh_no_auth_method',
  CONNECTION_CLOSED: 'ssh_connection_closed',
  SESSION_NOT_FOUND: 'ssh_session_not_found',
  INVALID_INPUT: 'ssh_invalid_input',
  HOST_NOT_FOUND: 'ssh_host_not_found',
  FOLDER_NOT_CONNECTABLE: 'ssh_folder_not_connectable',
  DB_UNAVAILABLE: 'ssh_db_unavailable',
  INTERNAL: 'ssh_internal',
} as const

/**
 * 错误码 → i18n 键。
 *
 * 为什么不直接展示后端 msg：msg 是给日志/调试看的中文串，英文界面下会露出中文，
 * 而且它随实现改动。真正给用户看的是这张表——每条都是「发生了什么 + 你该做什么」。
 * 表里没有的码兜底到 sshErrInternal，不把未知文案直接甩给用户。
 */
const SSH_ERR_I18N: Record<string, string> = {
  [SSH_ERR.HOSTKEY_UNKNOWN]: 'sshErrHostKeyUnknown',
  [SSH_ERR.HOSTKEY_CHANGED]: 'sshErrHostKeyChanged',
  [SSH_ERR.NO_CREDENTIAL]: 'sshErrNoCredential',
  [SSH_ERR.AUTH_FAILED]: 'sshErrAuthFailed',
  [SSH_ERR.UNREACHABLE]: 'sshErrUnreachable',
  [SSH_ERR.ALGORITHM]: 'sshErrAlgorithm',
  [SSH_ERR.NO_AUTH_METHOD]: 'sshErrNoAuthMethod',
  [SSH_ERR.CONNECTION_CLOSED]: 'sshErrConnectionClosed',
  [SSH_ERR.SESSION_NOT_FOUND]: 'sshErrSessionNotFound',
  [SSH_ERR.INVALID_INPUT]: 'sshErrInvalidInput',
  [SSH_ERR.HOST_NOT_FOUND]: 'sshErrHostNotFound',
  [SSH_ERR.FOLDER_NOT_CONNECTABLE]: 'sshErrFolderNotConnectable',
  [SSH_ERR.DB_UNAVAILABLE]: 'sshErrDb',
  [SSH_ERR.INTERNAL]: 'sshErrInternal',
}

/** 指纹类错误：需要弹窗让用户确认，不能当普通失败处理。 */
export function isFingerprintError(code: string): boolean {
  return code === SSH_ERR.HOSTKEY_UNKNOWN || code === SSH_ERR.HOSTKEY_CHANGED
}

/**
 * 会话摘要（ListSessions 的返回项）。
 *
 * hostId 是恢复的关键：窗口重开后靠它把会话认回主机，否则只能重新连一次。
 * label / started / rttMs 供状态条展示。
 */
export interface SshSessionInfo {
  id: string
  hostId: string
  state: string
  cols: number
  rows: number
  received: number
  label: string
  started: number
  rttMs: number
}

/** 会话是否仍然活着（可继续输入）。断开的只剩回放价值。 */
export function isSessionAlive(info: SshSessionInfo): boolean {
  return info.state === 'connected'
}

/** 会话是否还在 Go 侧留存（无论死活都能回放）。 */
export function isSessionRetained(info: SshSessionInfo): boolean {
  return info.state === 'connected' || info.state === 'disconnected'
}

/** 主机名:端口 的展示写法（IPv6 由调用方自行加方括号）。 */
export function sshTarget(h: Pick<SshHost, 'host' | 'port'>): string {
  return `${h.host}:${h.port || 22}`
}

/** user@host:port 的展示写法。 */
export function sshTargetFull(h: Pick<SshHost, 'host' | 'port' | 'user'>): string {
  return `${h.user || 'root'}@${sshTarget(h)}`
}

/** 环境 → i18n 键。未知值按测试环境处理（与后端归一化一致）。 */
export function sshEnvKey(env: string): 'sshEnvProd' | 'sshEnvStaging' | 'sshEnvTest' {
  if (env === 'prod') return 'sshEnvProd'
  if (env === 'staging') return 'sshEnvStaging'
  return 'sshEnvTest'
}

/** 环境 → 样式类名（左色条 + 徽标双标记，不只靠颜色区分）。 */
export function sshEnvClass(env: string): string {
  if (env === 'prod') return 'env-prod'
  if (env === 'staging') return 'env-staging'
  return 'env-test'
}

/**
 * 由主机密钥类型推出对应的公钥文件名，用于给用户一条可照抄的核对命令。
 * 例：ssh-ed25519 → ssh_host_ed25519_key.pub，ecdsa-sha2-nistp256 → ssh_host_ecdsa_key.pub
 */
export function sshHostKeyFile(keyType: string): string {
  const t = (keyType || '').toLowerCase()
  if (t.includes('ed25519')) return 'ssh_host_ed25519_key.pub'
  if (t.includes('ecdsa')) return 'ssh_host_ecdsa_key.pub'
  if (t.includes('rsa')) return 'ssh_host_rsa_key.pub'
  const name = t.replace(/^ssh-/, '').replace(/-.*$/, '')
  return name ? `ssh_host_${name}_key.pub` : 'ssh_host_*_key.pub'
}

/** SSH 错误码对应的 i18n 键；未知码返回空串。 */
export function sshErrorKey(code: string): string {
  return SSH_ERR_I18N[code] || ''
}

/**
 * 把 SSH 错误翻译成「发生了什么 + 你该做什么」。
 *
 * 这里刻意**不**直接展示后端 msg：msg 是给日志看的中文串，英文界面会露中文，
 * 而且它随实现改动。测试连接失败时用户全靠这句话判断是网络问题还是密码问题，
 * 所以文案必须可操作，而不只是「失败」。
 *
 * 表里没有的码退回原始 message —— 与其显示一句笼统的「内部错误」，
 * 不如把后端那句具体描述透出来。
 */
export function sshErrorText(e: unknown, code: string, t: (k: string) => string): string {
  const key = sshErrorKey(code)
  if (key) return t(key)
  return e instanceof Error && e.message ? e.message : t('sshErrInternal')
}

/**
 * 把「连接持续了多久」格式化成一句人话。
 *
 * 超过一天后精确到秒就没意义了（"3天2小时4分"没人读），所以按量级换精度。
 */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '0s'
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m${s % 60}s`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h${m % 60}m`
  const d = Math.floor(h / 24)
  return `${d}d${h % 24}h`
}

/** 字节数 → 人类可读（终端累计流量用）。 */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return i === 0 ? `${v} ${units[i]}` : `${v.toFixed(1)} ${units[i]}`
}

/**
 * 拆分多行粘贴内容，供二次确认弹窗使用。
 *
 * 只取前若干行做预览：粘几百行时全量渲染会卡住确认弹窗，而且真正需要看的
 * 就是头几行——危险命令通常就在开头。返回的 total 让 UI 能说清「还有多少行」。
 */
export function splitPasteLines(data: string, previewLimit = 6): {
  lines: string[]
  total: number
  truncated: boolean
} {
  // \r\n 与 \r 都算换行：Windows 剪贴板带 \r\n，只按 \n 切会留下一堆尾随 \r。
  const normalized = data.replace(/\r\n?/g, '\n')
  const all = normalized.split('\n')
  // 文本通常以换行结尾，会切出一个尾随空串；连续多个空串（粘贴里只有空行）
  // 也一并去掉——它们不构成「命令」，计进行数只会让确认弹窗虚报行数。
  while (all.length > 0 && all[all.length - 1] === '') all.pop()
  return {
    lines: all.slice(0, previewLimit),
    total: all.length,
    truncated: all.length > previewLimit,
  }
}

/** bracketed paste 的开始/结束标记。 */
const PASTE_START = '\u001b[200~'
const PASTE_END = '\u001b[201~'

/**
 * 判断一段终端输入是否属于「需要二次确认的多行粘贴」。
 *
 * 这里有个不显眼的坑：终端里**回车键发出来的就是 \r**，和多行粘贴在数据
 * 层面长得一模一样——直接判断「含不含换行」会让用户每敲一次回车都弹确认框。
 *
 * 真正的区分点是换行出现的**位置**：
 *   - 手敲 `ls` 再回车 → "ls\r"，换行只出现在末尾，是「提交这一行」
 *   - 粘贴 "ls\nrm -rf /" → 换行夹在内容中间，shell 会逐行执行
 * 所以先把末尾那一个收尾换行剥掉，再看剩下里还有没有换行。
 */
export function isMultiLinePaste(data: string): boolean {
  if (!data) return false

  // bracketed paste 的包裹符要剥掉再判断行数，否则单行粘贴会被误判。
  let bare = data
  if (bare.includes(PASTE_START)) {
    bare = bare.split(PASTE_START).join('').split(PASTE_END).join('')
  }

  // 剥掉末尾的一个换行序列（\r\n / \n / \r 都算），它只代表「这一行敲完了」。
  bare = bare.replace(/(?:\r\n|\r|\n)$/, '')
  return bare.includes('\n') || bare.includes('\r')
}

// ---- 批量执行 ----
//
// 后端返回 ApiResult{Data interface{}}，绑定里不会生成这些结果类型
// （只有入参 SshBatchRequest / SshBatchPlanRequest 进了 models.ts），
// 所以按仓库惯例在 utils 里手写一份 wire 类型。**字段名必须与后端 json tag
// 完全一致**（全小写驼峰）：JS 大小写敏感，写成 HostId 就是整表空白。
//
// 与后端 services/sshx/batch.go 成对维护，改一处必须改另一处。

export interface SshBatchTarget {
  hostId: string
  name: string
  addr: string
  /** prod | staging | test，原始值 */
  env: string
}

export interface SshBatchSkipped {
  hostId: string
  name: string
  /** 稳定错误码，前端据此选文案 */
  reason: string
  detail: string
}

export interface SshBatchPlan {
  hosts: SshBatchTarget[]
  skipped: SshBatchSkipped[]
  /** high | medium | '' */
  riskLevel: string
  riskNote: string
  prodCount: number
}

export interface SshBatchStart {
  batchId: string
  total: number
  skipped: number
}

export interface SshBatchHostResult {
  batchId: string
  hostId: string
  name: string
  addr: string
  env: string
  stdout: string
  stderr: string
  exitCode: number
  durationMs: number
  truncated: boolean
  /** 非空 = 命令没跑完（连接失败/超时/取消），与退出码非 0 是两回事 */
  errorCode: string
  error: string
}

export interface SshBatchSummary {
  batchId: string
  total: number
  succeeded: number
  failed: number
  errored: number
  canceled: number
  skipped: number
  aborted: boolean
  durationMs: number
}

/** 结果行状态。 */
export type SshBatchStatus = 'pending' | 'running' | 'ok' | 'failed' | 'errored' | 'canceled'

/**
 * 单台结果 → 行状态。
 *
 * 关键区分：**退出码非 0 是"跑了但结果是否定"，ErrorCode 非空是"根本没跑"**。
 * 这两类在汇总里必须分开（back 端的 SshBatchSummary 也分 failed / errored），
 * 否则「3 台失败」到底该查网络还是查命令，用户完全没法判断——而处置方式正相反。
 */
export function batchRowStatus(
  res: SshBatchHostResult | null | undefined,
  running: boolean,
): SshBatchStatus {
  if (!res) return running ? 'running' : 'pending'
  if (res.errorCode === 'ssh_batch_canceled') return 'canceled'
  if (res.errorCode) return 'errored'
  return res.exitCode === 0 ? 'ok' : 'failed'
}
