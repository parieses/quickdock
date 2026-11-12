/**
 * 密码索取弹层的全局队列（Promise 化）。
 *
 * 动机：项目里已有 8 处密码输入框（SSH 主机、Git 推送、WebDAV 同步、AI Key、
 * 下载器 secret……），每处各自一个 ref + 各自的保存逻辑，用户每换一个场景就要
 * 再输一次。这里把「要一个密码」收敛成一次 await：
 *
 *     const r = await requestCredential({ kind: 'ssh', title: '连接 10.0.0.1 需要密码' })
 *     if (!r) return                       // 用户取消
 *     await connect(r.username, r.secret)
 *
 * 弹层本身在 App.vue 挂一次（和 ConfirmDialog 同款），任何页面都能调。
 *
 * 为什么不用 Promise 直接挂在组件上：调用点遍布各页，而弹层必须只有一个实例
 * （否则同时点两处会叠出两层输入框，用户不知道该填哪个）。用模块级 reactive
 * 队列而不是 provide/inject，是为了让非组件代码（utils、快捷键回调）也能调用。
 */
import { reactive } from 'vue'

/** 凭据类型。与后端 db.CredKind* 一致，拼错会表现为「推荐不出条目」。 */
export type CredKind = 'ssh' | 'database' | 'webdav' | 'git' | 'api_key' | 'login' | 'other'

/**
 * 密码库条目的元信息（列表/推荐用）。
 *
 * 与后端 db.Credential 的投影一一对应，**刻意不含 secret**：
 * 列表接口从不返回密码，要看明文必须单独调 RevealCredential。
 */
export interface CredentialSummary {
  id: string
  name: string
  kind: string
  username: string
  host: string
  port: number
  url: string
  notes: string
  tags: string
  source: string
  sourceId: string
  favorite: boolean
  useCount: number
  lastUsedAt: string
  createdAt: string
  updatedAt: string
  hasSecret: boolean
}

/** 一次索取请求。 */
export interface CredRequest {
  /** 场景类型：决定推荐哪些条目、存进库里算哪一类。 */
  kind: CredKind
  /** 弹层标题，写成一句人话（「连接 10.0.0.1 需要密码」）。 */
  title: string
  /** 副标题：说清为什么需要（认证失败原因 / 密钥口令 / 这是第二次尝试）。 */
  hint?: string
  /** 用于从密码库推荐条目；浏览器场景传域名，内部场景传主机名。 */
  host?: string
  port?: number
  /** 预填的用户名（如 SSH 主机已配置的 user）。 */
  username?: string
  /** 是否允许「保存到密码库」，默认 true。密钥口令这类敏感值可关掉。 */
  allowSave?: boolean
  /** 是否展示密码库推荐列表，默认 true。 */
  allowPick?: boolean
}

/** 用户的选择。取消时整个 Promise 解析为 null。 */
export interface CredResult {
  /** 选中了库里的条目时带回 id（调用方可用它记一次使用）。 */
  credentialId?: string
  username: string
  secret: string
  /** 用户勾了「保存到密码库」。 */
  save: boolean
}

interface PendingRequest {
  id: number
  req: CredRequest
  resolve: (value: CredResult | null) => void
}

const state = reactive<{ items: PendingRequest[] }>({ items: [] })
let nextId = 1

/**
 * 索取一个密码。返回 null 表示用户取消（调用方必须处理这个分支，
 * 否则会出现「用户按了取消，连接却用空密码继续跑」）。
 */
export function requestCredential(req: CredRequest): Promise<CredResult | null> {
  return new Promise((resolve) => {
    state.items.push({ id: nextId++, req, resolve })
  })
}

/** 由 CredentialPrompt 提交/取消时调用。 */
export function resolveCredential(id: number, value: CredResult | null): void {
  const idx = state.items.findIndex((it) => it.id === id)
  if (idx === -1) return
  const item = state.items[idx]
  // 先出队再 resolve：resolve 可能同步触发调用方继续走流程，
  // 那时弹层必须已经消失，否则用户会看到「还停在输入框上但操作已完成」。
  state.items.splice(idx, 1)
  item.resolve(value)
}

/**
 * 取消所有挂起的请求（应用锁定 / 退出时调用）。
 *
 * 不这么做的话，锁定后这些 Promise 会永远悬着——调用方的 await 不返回，
 * 它所在的流程（连接、同步、推送）就一直卡在半路。
 */
export function cancelAllCredentialRequests(): void {
  const items = state.items.splice(0, state.items.length)
  for (const it of items) it.resolve(null)
}

/** 供 CredentialPrompt 读取当前队首请求。 */
export function useCredentialQueue() {
  return { items: state.items, resolve: resolveCredential }
}
