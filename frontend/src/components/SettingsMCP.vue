<script setup lang="ts">
/**
 * 设置页「MCP」分段：外部 MCP 服务器的增删改、工具详情展开、JSON 两条通道。
 *
 * 从 SettingsAI.vue 抽出来（原文件 2391 行）。搬过来时唯一的外部依赖是父组件
 * 注入的 toast，其余全是本分段自己的状态；tab 上的失败徽标靠 `update:errorCount`
 * 回传 —— server 启动只连一次、失败不自动重试，用户往往就是看到那个红点才进来重连。
 *
 * 加载时机与 SettingsSkills 不同：那个是「切到本分段才拉」，这里是「设置面板一打开
 * 就拉」，因为徽标要在用户切到本页之前就显示出错误数。所以接的是父组件的 visible，
 * 而不是「visible && activeTab === 'mcp'」。
 */
import { inject, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, Plus, RefreshCw } from '@lucide/vue'
import QdSwitch from './QdSwitch.vue'
import { unwrap } from '../utils/api'
import { getErrorMessage } from '../utils/error'
import { reportError } from '../utils/reportError'

import {
  AIGetExtServers,
  AISaveExtServers,
  AISetExtServerEnabled,
  AIReloadExtServers,
  AIListExtTools,
  AISetToolEnabled,
} from '../../bindings/quickdock/services/ai/aiservice'
import type { AIExtServerState } from '../types/ai'
import type { ToastAPI } from '../types'

const props = withDefaults(defineProps<{
  /** 设置面板是否可见；由它触发首次加载，见文件头。 */
  visible?: boolean
}>(), { visible: false })

const emit = defineEmits<{
  /** 失败数回传父组件，用于 MCP 分段上的红点徽标。 */
  (e: 'update:errorCount', n: number): void
}>()

const { t } = useI18n()
const toast = inject<ToastAPI>('toast')!

// ── 外部 MCP 服务器（阶段 D）────────────────────────────────────────
// 状态由后端维护（连接、注册工具都在那边），这里负责展示与增删改。
//
// 原来是「一整份 JSON 文本框」：字段确实和 Claude Desktop / Cursor 的 mcp.json
// 一致，但每加一个服务器都要手写括号、引号、逗号，写错一个字符整份都保存不了，
// 而且报错只说「JSON 解析失败」。现在改成表单：名字、类型、命令、参数、环境变量、
// 认证令牌各一格；JSON 留两条通道——「编辑配置 JSON」改整份原文，
// 「粘贴导入」把别人给的片段合并进来。两条都认 headers，不再丢认证信息。
// 外部 MCP 的两段 placeholder 是「JSON 片段 / 命令行参数」的语法示例，不走 i18n：
//   1) 内容全是 JSON 与包名，各语言相同，翻译没有意义；
//   2) '{' '}' 在 vue-i18n 里是插值定界符、'@' 是 linked-message 前缀，放进 messages
//      会被当语法解析 —— 开发环境只打一条警告，生产构建直接抛 SyntaxError，
//      整个页面渲染失败（表现为「新建任务页白屏 / 输入框消失」）。
const EXT_IMPORT_PH = '{\n  "filesystem": {\n    "command": "npx",\n    "args": ["-y", "@modelcontextprotocol/server-filesystem", "D:/proj"]\n },\n  "remote": { "url": "http://127.0.0.1:9000/mcp",\n    "headers": { "Authorization": "Bearer sk-xxx" } }\n}'
const EXT_ARGS_PH = '-y\n@modelcontextprotocol/server-filesystem\nD:/proj'
const EXT_HEADERS_PH = 'X-API-Key: sk-xxx\nX-Tenant: acme'

const extServers = ref<AIExtServerState[]>([])
const extLoading = ref(false)
const extSaving = ref(false)
const extError = ref('')

// 表单：origName 非空表示在改既有项（改名 = 覆盖旧名的同时删掉旧键）
interface ExtForm {
  origName: string
  name: string
  kind: 'stdio' | 'http'
  command: string
  argsText: string
  envText: string
  url: string
  token: string
  headersText: string
}
const extForm = ref<ExtForm | null>(null)
// 令牌默认是掩码，编辑时也不把明文摊在屏幕上；想核对内容自己点「显示」。
const extTokenVisible = ref(false)
const extImportOpen = ref(false)
const extImportDraft = ref('')
// 「编辑配置 JSON」：一份当前 mcp.json 的原文草稿，保存即整份替换。
const extEditOpen = ref(false)
const extEditDraft = ref('')

// ---- 工具详情展开行 ----
// AIGetExtServers 只给工具名；描述与「是否被单独关掉」在 mcp.Catalog 里，
// 由 AIListExtTools 按 server 取。展开状态按 server 名记，服务器停用 / 重连 /
// 改名后一律收起，否则会展示上一轮的陈旧列表。
interface ExtToolInfo {
  name: string
  desc: string
  disabled: boolean
}
const extExpanded = ref<string | null>(null)
const extTools = ref<ExtToolInfo[]>([])
const extToolsLoading = ref(false)
const extToolsBusy = ref('')
// 快速连点两个 server 时，前一个请求回来不能盖掉后一个的结果。
let extToolsSeq = 0

function resetExtTools() {
  extToolsSeq++
  extExpanded.value = null
  extTools.value = []
  extToolsBusy.value = ''
  extToolsLoading.value = false
}

async function toggleExtTools(s: AIExtServerState) {
  if (extExpanded.value === s.name) { resetExtTools(); return }
  resetExtTools()
  const seq = extToolsSeq
  extExpanded.value = s.name
  extToolsLoading.value = true
  try {
    const list = unwrap<ExtToolInfo[]>(await AIListExtTools(s.name)) ?? []
    if (seq === extToolsSeq) extTools.value = list
  } catch (e) {
    reportError(e, '[SettingsAI] AIListExtTools:')
    // 这里不能再调 resetExtTools：它会推进 extToolsSeq，finally 就判不出
    // 「还是本次请求」而漏掉关 loading，展开行会一直转圈。
    if (seq === extToolsSeq) {
      extExpanded.value = null
      extTools.value = []
    }
  } finally {
    if (seq === extToolsSeq) extToolsLoading.value = false
  }
}

async function toggleExtTool(tool: ExtToolInfo) {
  if (extToolsBusy.value) return
  extToolsBusy.value = tool.name
  try {
    // 参数取反：tool.disabled 为 true 表示当前被关掉、这次要重新开启。
    // 走 AISetToolEnabled（内置 AI 自己的那份策略），不是 MCP 面板的全局开关——
    // 在这里关掉一个外部工具，只让 AI 用不了，外部客户端照旧。
    unwrap(await AISetToolEnabled(tool.name, tool.disabled))
    tool.disabled = !tool.disabled
  } catch (e) {
    reportError(e, '[SettingsAI] AISetToolEnabled:')
  } finally {
    extToolsBusy.value = ''
  }
}

// 与后端 mcpclient.ServerConfig 同构的提交项
interface ExtInput {
  name: string
  command: string
  args: string[]
  env: Record<string, string>
  url: string
  headers: Record<string, string>
  disabled: boolean
}

async function loadExtServers() {
  extLoading.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AIGetExtServers()) ?? []
    resetExtTools()
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extLoading.value = false
  }
}

function kindOf(s: { config?: { url?: string } } | null | undefined): 'stdio' | 'http' {
  return (s?.config?.url || '').trim() ? 'http' : 'stdio'
}

/** 参数逐行存/取：用空格分隔的话，带空格或引号的参数会被拆散 */
function argsToText(args?: string[]): string {
  return (args || []).join('\n')
}
function textToArgs(s: string): string[] {
  return s.split('\n').map((x) => x.trim()).filter(Boolean)
}

/** 环境变量逐行 KEY=VALUE；只切第一个等号，值里带 = 不会被截断 */
function envToText(env?: Record<string, string>): string {
  return Object.entries(env || {}).map(([k, v]) => `${k}=${v}`).join('\n')
}
function textToEnv(s: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of s.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const i = line.indexOf('=')
    if (i <= 0) continue
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

// 请求头逐行「名称: 值」，也认「名称=值」。分隔符取更靠前的那个：
// `X-Url=http://host` 若一律按冒号切，值会被截成 `http`。
function headersToText(h?: Record<string, string>): string {
  return Object.entries(h || {}).map(([k, v]) => `${k}: ${v}`).join('\n')
}
function textToHeaders(s: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const raw of s.split('\n')) {
    const line = raw.trim()
    if (!line || line.startsWith('#')) continue
    const ci = line.indexOf(':')
    const ei = line.indexOf('=')
    let i = -1
    if (ci > 0 && (ei < 0 || ci < ei)) i = ci
    else if (ei > 0) i = ei
    if (i <= 0) continue
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim()
  }
  return out
}

/** 编辑回显：Bearer 令牌抽进专用输入框，其余请求头留在逐行文本里。
 *  Basic / 自定义 Authorization 不抽，否则提交时会被套成 Bearer Basic xxx。 */
function splitAuth(headers?: Record<string, string>): { token: string; text: string } {
  const h = { ...(headers || {}) }
  const key = Object.keys(h).find((k) => k.toLowerCase() === 'authorization')
  let token = ''
  if (key && /^Bearer\s+/i.test(h[key])) {
    token = h[key].replace(/^Bearer\s+/i, '').trim()
    delete h[key]
  }
  return { token, text: headersToText(h) }
}

/** 表单 -> headers：令牌写进 Authorization，与自定义头合并（同名时令牌优先）。 */
function extFormHeaders(f: ExtForm): Record<string, string> {
  const h = textToHeaders(f.headersText)
  const token = f.token.trim()
  if (token) h.Authorization = /^Bearer\s+/i.test(token) ? token : `Bearer ${token}`
  return h
}

/** 状态 -> 提交项：覆盖式保存时其余 server 要原样带上 */
function stateToInput(s: AIExtServerState): ExtInput {
  const c = s.config || {}
  return {
    name: s.name,
    command: c.command || '',
    args: c.args || [],
    env: c.env || {},
    url: c.url || '',
    headers: c.headers || {},
    disabled: !!c.disabled,
  }
}

/** 列表里那一行「它在跑什么」：URL 或 命令+参数 */
function extTarget(s: AIExtServerState): string {
  const c = s.config || {}
  if ((c.url || '').trim()) return c.url as string
  return [c.command || '', ...(c.args || [])].filter(Boolean).join(' ')
}

function openAddExt() {
  extForm.value = {
    origName: '', name: '', kind: 'stdio', command: '', argsText: '', envText: '',
    url: '', token: '', headersText: '',
  }
  extTokenVisible.value = false
  extImportOpen.value = false
  extEditOpen.value = false
  extError.value = ''
}

function openEditExt(s: AIExtServerState) {
  const c = s.config || {}
  const auth = splitAuth(c.headers)
  extForm.value = {
    origName: s.name,
    name: s.name,
    kind: kindOf(s),
    command: c.command || '',
    argsText: argsToText(c.args),
    envText: envToText(c.env),
    url: c.url || '',
    token: auth.token,
    headersText: auth.text,
  }
  extTokenVisible.value = false
  extImportOpen.value = false
  extEditOpen.value = false
  extError.value = ''
}

/** 覆盖式提交：后端把这份清单当「用户编辑后的全量」，所以删除也是提交少了某项的清单 */
async function commitExt(items: ExtInput[]): Promise<boolean> {
  extSaving.value = true
  extError.value = ''
  try {
    const res = unwrap<{ states: AIExtServerState[] }>(await AISaveExtServers(items))
    extServers.value = res?.states ?? []
    resetExtTools()
    extForm.value = null
    extImportOpen.value = false
    extImportDraft.value = ''
    extEditOpen.value = false
    extEditDraft.value = ''
    extTokenVisible.value = false
    return true
  } catch (e) {
    extError.value = getErrorMessage(e)
    return false
  } finally {
    extSaving.value = false
  }
}

// 名称规则与后端 validServerName 一致，前端先拦一次：不然要等一次往返才看到错
const EXT_NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,31}$/

async function submitExtForm() {
  const f = extForm.value
  if (!f) return
  const name = f.name.trim()
  if (!EXT_NAME_RE.test(name)) {
    extError.value = t('aiExtNameBad')
    return
  }
  const isHttp = f.kind === 'http'
  const url = f.url.trim()
  const command = f.command.trim()
  if (isHttp && !url) {
    extError.value = t('aiExtNeedUrl')
    return
  }
  if (!isHttp && !command) {
    extError.value = t('aiExtNeedCommand')
    return
  }
  // 同名但不同原名视为「改名」：先把旧名剔掉再追加。其余项保持原状（含 disabled）。
  const rest = extServers.value.filter((s) => s.name !== f.origName && s.name !== name).map(stateToInput)
  const prev = extServers.value.find((s) => s.name === f.origName)
  const item: ExtInput = {
    name,
    command: isHttp ? '' : command,
    args: isHttp ? [] : textToArgs(f.argsText),
    env: isHttp ? {} : textToEnv(f.envText),
    url: isHttp ? url : '',
    headers: isHttp ? extFormHeaders(f) : {},
    disabled: prev ? !!prev.config?.disabled : false,
  }
  await commitExt([...rest, item])
}

async function removeExt(s: AIExtServerState) {
  if (!(await toast.confirm(t('aiExtRemoveConfirm', { name: s.name })))) return
  await commitExt(extServers.value.filter((x) => x.name !== s.name).map(stateToInput))
}

/** 开关直接走 mcp.json 的 disabled 字段：关掉即断开，stdio 进程会被回收 */
async function toggleExt(s: AIExtServerState) {
  const enable = s.status === 'disabled'
  extSaving.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AISetExtServerEnabled(s.name, enable)) ?? []
    resetExtTools()
    // 整份 JSON 草稿还开着时同步一下 disabled，否则保存会把开关状态盖回去
    if (extEditOpen.value) extEditDraft.value = buildManifestJson()
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extSaving.value = false
  }
}

// 粘贴导入要认的形态比想象中多，各家文档给的不一样：
//   {"mcpServers": {"x": {...}}}        Claude Desktop / Cursor
//   {"servers": {"x": {...}}}           VS Code
//   {"x": {"command": ...}}             裸映射（这一栏的占位示例就是它）
//   {"command": "npx", "args": [...]}   单条配置，很多 README 直接贴这一段
//   [{"name": "x", "command": ...}]     带名字的数组
// 早先只把非 mcpServers 的一律当「名字 → 配置」映射，于是单条配置会被读成
// 一堆叫 command / args 的服务器（command 为空）→ 提交时后端整份打回，
// 表现就是「点了导入没反应」。所以先认结构，认不出就直说，不猜。

interface ParsedExt {
  name: string
  cfg: Record<string, any>
}

/** 名字落到后端的 validServerName 上：非法字符换成 -，去掉开头非字母数字，截 32 */
function sanitizeExtName(raw: string, fallback: string): string {
  let n = (raw || '').trim().replace(/^[^A-Za-z0-9]+/, '').replace(/[^A-Za-z0-9_-]+/g, '-')
  if (!n) n = fallback
  return n.slice(0, 32)
}

/** 从命令行猜个名字：`npx -y @scope/pkg@1.2` → `pkg`（通用词则取 scope）。猜不出返回空串 */
function nameFromCommand(cmd: string): string {
  const toks = cmd.trim().split(/\s+/).filter((x) => x && !x.startsWith('-'))
  if (!toks.length) return ''
  // npm 包名最像这个 server 的身份，优先用它
  const pkg = toks.find((x) => x.startsWith('@'))
  if (pkg) {
    // 先摘掉开头的 scope 前缀 @ 再去版本号：否则 /@[^@]*$/ 会把整串吃掉
    const seg = pkg.slice(1).replace(/@[^@]*$/, '').split('/').filter(Boolean) // [scope, name]
    const last = seg[seg.length - 1] || ''
    // @playwright/mcp → mcp 太泛，退一级用包主名
    if (seg.length > 1 && /^(mcp|server|cli|sdk|index|main)$/i.test(last)) return seg[0]
    return last
  }
  // 否则退到第一个像包/文件的 token（跳过目录参数与裸路径）
  const pick = toks.find((x) => /\.[a-z0-9]+$/i.test(x) || /^[a-z0-9][\w.-]*$/i.test(x)) || ''
  if (!pick) return ''
  const base = pick.split(/[\\/]/).pop() || pick
  return base.replace(/\.[a-z0-9]{1,4}$/i, '')
}

/** 拆一行命令行，认双引号（Windows 路径常带空格） */
function splitCommandLine(s: string): string[] {
  const out: string[] = []
  let cur = ''
  let quote = false
  for (const ch of s) {
    if (ch === '"') { quote = !quote; continue }
    if (!quote && /\s/.test(ch)) { if (cur) { out.push(cur); cur = '' } continue }
    cur += ch
  }
  if (cur) out.push(cur)
  return out
}

/**
 * 宽松解析：文档站给的片段常带 `//` 注释和尾逗号（jsonc），严格 JSON.parse 会直接失败。
 * 按字符扫一遍去掉注释和尾逗号，字符串里的内容原样保留。
 */
function parseLooseJson(text: string): any {
  try {
    return JSON.parse(text)
  } catch {
    /* 走 jsonc 兜底 */
  }
  let out = ''
  let q = false
  let esc = false
  for (let i = 0; i < text.length; i++) {
    const ch = text[i]
    if (q) {
      out += ch
      if (esc) esc = false
      else if (ch === '\\') esc = true
      else if (ch === '"') q = false
      continue
    }
    if (ch === '"') { q = true; out += ch; continue }
    if (ch === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++
      out += '\n'
      continue
    }
    if (ch === '/' && text[i + 1] === '*') {
      i += 2
      while (i < text.length && !(text[i] === '*' && text[i + 1] === '/')) i++
      i++
      continue
    }
    out += ch
  }
  return JSON.parse(out.replace(/,\s*([}\]])/g, '$1'))
}

/** 一条外部配置 → 提交项（不含名字）。headers 原样保留：远程 server 的
 *  Authorization / X-API-Key 都在这里，丢了就等于配好的令牌没生效。*/
function cfgToInput(cfg: Record<string, any>): {
  command: string
  args: string[]
  env: Record<string, string>
  url: string
  headers: Record<string, string>
} {
  const c = cfg || {}
  let command = String(c.command ?? c.cmd ?? '').trim()
  let args: string[] = []
  if (Array.isArray(c.args)) args = c.args.map((x) => String(x))
  else if (typeof c.args === 'string') args = splitCommandLine(c.args)
  // 「command」里塞了整条命令（"npx -y pkg"）时拆开：后端把它当可执行文件名
  if (command && !args.length && /\s/.test(command)) {
    const parts = splitCommandLine(command)
    if (parts.length > 1) { command = parts[0]; args = parts.slice(1) }
  }

  let env: Record<string, string> = {}
  if (Array.isArray(c.env)) {
    // 少数配置写成 ["KEY=VALUE"] 数组
    for (const line of c.env) {
      const s = String(line)
      const i = s.indexOf('=')
      if (i > 0) env[s.slice(0, i).trim()] = s.slice(i + 1).trim()
    }
  } else if (c.env && typeof c.env === 'object') {
    env = Object.fromEntries(Object.entries(c.env).map(([k, v]) => [k, String(v)]))
  }

  const url = String(c.url ?? c.serverUrl ?? c.server_url ?? c.endpoint ?? c.baseUrl ?? '').trim()
  // headers 既有对象写法，也有少数文档给的 ["KEY: VALUE"] 数组写法
  let headers: Record<string, string> = {}
  if (Array.isArray(c.headers)) headers = textToHeaders(c.headers.map(String).join('\n'))
  else if (c.headers && typeof c.headers === 'object') {
    headers = Object.fromEntries(Object.entries(c.headers).map(([k, v]) => [k, String(v)]))
  }
  // 有 url 就是 HTTP：把 command/args/env 清空，否则后端会判「只能填一个」
  return url ? { command: '', args: [], env: {}, url, headers } : { command, args, env, url: '', headers }
}

/** 从任意粘贴内容里认出一批「名字 + 配置」；认不出返回说明性的错误 key */
function extractExtCandidates(doc: any): ParsedExt[] | 'unrecognized' {
  if (Array.isArray(doc)) {
    return doc
      .filter((x) => x && typeof x === 'object' && !Array.isArray(x))
      .map((x: any, i: number) => ({
        name: sanitizeExtName(String(x.name ?? x.id ?? x.server ?? x.serverName ?? ''), `mcp${i + 1}`),
        cfg: x,
      }))
  }
  if (!doc || typeof doc !== 'object') return 'unrecognized'

  const wrapKey = ['mcpServers', 'mcp_servers', 'servers', 'server'].find(
    (k) => doc[k] && typeof doc[k] === 'object' && !Array.isArray(doc[k]),
  )
  if (wrapKey) {
    return Object.entries(doc[wrapKey] as Record<string, any>)
      .filter(([, v]) => v && typeof v === 'object' && !Array.isArray(v))
      .map(([k, v]) => ({ name: sanitizeExtName(k, 'mcp'), cfg: v as Record<string, any> }))
  }
  // 单条配置：自己就是一份命令或地址
  if (doc.command || doc.cmd || doc.url || doc.serverUrl || doc.endpoint) {
    const cmdLine = [String(doc.command ?? doc.cmd ?? ''), ...(Array.isArray(doc.args) ? doc.args.map(String) : [])].join(' ')
    const guess = String(doc.name ?? '') || nameFromCommand(cmdLine)
    return [{ name: sanitizeExtName(guess, 'mcp'), cfg: doc }]
  }
  // 剩下的只有当「名字 → 配置」的裸映射才认
  const entries = Object.entries(doc as Record<string, any>).filter(
    ([, v]) => v && typeof v === 'object' && !Array.isArray(v),
  )
  if (!entries.length) return 'unrecognized'
  return entries.map(([k, v]) => ({ name: sanitizeExtName(k, 'mcp'), cfg: v as Record<string, any> }))
}

async function importExtJson() {
  let doc: any
  try {
    doc = parseLooseJson(extImportDraft.value)
  } catch {
    extError.value = t('aiExtBadJson')
    return
  }
  const parsed = extractExtCandidates(doc)
  if (parsed === 'unrecognized') {
    extError.value = t('aiExtUnrecognized')
    return
  }
  if (!parsed.length) {
    extError.value = t('aiExtEmptyJson')
    return
  }

  const items = extServers.value.map(stateToInput)
  const taken = new Set<string>()
  const built: ExtInput[] = []
  const skipped: string[] = []
  const withHeaders: string[] = []
  for (const e of parsed) {
    const c = cfgToInput(e.cfg)
    if (!c.command && !c.url) {
      skipped.push(e.name)
      continue
    }
    if (Object.keys(c.headers).length) withHeaders.push(e.name)
    // 本次导入内部 sanitize 撞名（"@a/b" 与 "a-b"）时加序号，别互相覆盖
    let name = e.name
    let k = 2
    while (taken.has(name)) name = `${e.name.slice(0, 28)}-${k++}`
    taken.add(name)
    const prev = items.find((x) => x.name === name)
    built.push({
      name,
      command: c.command,
      args: c.args,
      env: c.env,
      url: c.url,
      headers: c.headers,
      // 覆盖时沿用原开关状态：导进来不该把用户停用的服务器又拉起来
      disabled: prev ? prev.disabled : !!e.cfg.disabled,
    })
  }
  if (!built.length) {
    extError.value = t('aiExtNoneValid', { names: skipped.join('、') })
    return
  }

  for (const it of built) {
    const i = items.findIndex((x) => x.name === it.name)
    if (i >= 0) items[i] = it
    else items.push(it)
  }
  // 后端整份校验不通过时不会落盘：红字在区块里，另外补一个 toast，
  // 免得导入面板收起后（或页面滚动到别处）用户以为又「没反应」
  if (!(await commitExt(items))) {
    if (extError.value) toast.error(extError.value)
    return
  }

  // 成功要说话：面板收起后只有列表变化，用户分不清「导入了」还是「没反应」
  let msg = t('aiExtImported', { n: built.length })
  const extra: string[] = []
  if (skipped.length) extra.push(t('aiExtImportSkipped', { n: skipped.length, names: skipped.join('、') }))
  if (withHeaders.length) extra.push(t('aiExtImportHeaders', { names: withHeaders.join('、') }))
  toast.success(extra.length ? `${msg}（${extra.join('；')}）` : msg)
}

/** 当前 mcp.json 的完整原文：含 headers 与令牌，给「编辑配置 JSON」直接用。
 *  只写非空字段，避免把一堆 null / 空数组带进用户要读的配置里。 */
function buildManifestJson(): string {
  const servers: Record<string, Record<string, any>> = {}
  for (const s of extServers.value) {
    const c = s.config || {}
    const item: Record<string, any> = {}
    if (c.command) item.command = c.command
    if (c.args?.length) item.args = c.args
    if (c.env && Object.keys(c.env).length) item.env = c.env
    if (c.url) item.url = c.url
    if (c.headers && Object.keys(c.headers).length) item.headers = c.headers
    if (c.disabled) item.disabled = true
    servers[s.name] = item
  }
  return JSON.stringify({ mcpServers: servers }, null, 2)
}

function openEditJson() {
  extEditOpen.value = !extEditOpen.value
  if (!extEditOpen.value) return
  extEditDraft.value = buildManifestJson()
  extImportOpen.value = false
  extError.value = ''
}

/** 与「粘贴导入」的合并语义不同：这里是整份替换，删掉一个名字就是删除它。 */
async function saveExtManifestJson() {
  let doc: any
  try {
    doc = parseLooseJson(extEditDraft.value)
  } catch {
    extError.value = t('aiExtBadJson')
    return
  }
  let parsed: ParsedExt[] | 'unrecognized' = extractExtCandidates(doc)
  // 空对象 {} 也当成「清空全部」的一种写法，否则结构识别会把它判成没认出来
  if (parsed === 'unrecognized' && doc && typeof doc === 'object' && !Array.isArray(doc) && !Object.keys(doc).length) {
    parsed = []
  }
  if (parsed === 'unrecognized') {
    extError.value = t('aiExtUnrecognized')
    return
  }
  const taken = new Set<string>()
  const items: ExtInput[] = []
  const skipped: string[] = []
  for (const e of parsed) {
    const c = cfgToInput(e.cfg)
    if (!c.command && !c.url) {
      skipped.push(e.name)
      continue
    }
    let name = e.name
    let k = 2
    while (taken.has(name)) name = `${e.name.slice(0, 28)}-${k++}`
    taken.add(name)
    items.push({
      name,
      command: c.command,
      args: c.args,
      env: c.env,
      url: c.url,
      headers: c.headers,
      disabled: !!e.cfg.disabled,
    })
  }
  // 一个合法项都没有时等于清空全部外部 server，先问一句再落盘
  if (!items.length && extServers.value.length && !(await toast.confirm(t('aiExtJsonClearConfirm')))) return
  if (!(await commitExt(items))) {
    if (extError.value) toast.error(extError.value)
    return
  }
  let msg = t('aiExtJsonSaved')
  if (skipped.length) msg += `（${t('aiExtImportSkipped', { n: skipped.length, names: skipped.join('、') })}）`
  toast.success(msg)
}

async function reloadExt() {
  extLoading.value = true
  extError.value = ''
  try {
    extServers.value = unwrap<AIExtServerState[]>(await AIReloadExtServers()) ?? []
    resetExtTools()
    if (extEditOpen.value) extEditDraft.value = buildManifestJson()
  } catch (e) {
    extError.value = getErrorMessage(e)
  } finally {
    extLoading.value = false
  }
}

// 徽标：父组件只关心「有几个连不上」，不持有 server 列表。
watch(extServers, (list) => {
  emit('update:errorCount', list.filter((s) => s.status === 'error').length)
}, { immediate: true })

// 打开设置就拉一次：徽标要在切到本分段之前就位（见文件头）。
watch(() => props.visible, (v) => {
  if (v) void loadExtServers()
}, { immediate: true })
</script>

<template>
<!-- 外部 MCP 服务器（阶段 D）：把别的 server 的工具接进内置 AI。
     表单负责逐项增删改（最常做的事），JSON 两条通道并列放在本区块顶部：
     「编辑配置 JSON」改当前整份 mcp.json，「粘贴导入」把别人的片段合并进来。 -->
<div class="ai-usage">
  <div class="ai-usage-head">
    <h4 class="ai-usage-title">{{ t('aiExtTitle') }}</h4>
    <!-- 整份 JSON 入口放在标题行：之前只有列表下方一个折叠的「粘贴导入」，
         想直接改 mcp.json 的人根本找不到地方输入。 -->
    <div class="ai-ext-head-ops">
      <button v-if="!extForm" class="ai-ext-add" @click="openEditJson">{{ t('aiExtJsonEdit') }}</button>
      <button v-if="!extForm" class="ai-ext-add" @click="extImportOpen = !extImportOpen; extEditOpen = false">
        {{ t('aiExtImport') }}
      </button>
      <button v-if="!extForm" class="ai-ext-add" @click="openAddExt">
        <Plus :size="12" /> {{ t('aiExtAdd') }}
      </button>
    </div>
  </div>
  <p class="usage-hint">{{ t('aiExtDesc') }}</p>

  <p v-if="extError" class="result-hint error">{{ extError }}</p>

  <!-- 整份配置 JSON：当前 mcp.json 原文，可直接写 headers / 令牌，保存即全量替换 -->
  <div v-if="!extForm && extEditOpen" class="ai-ext-import">
    <textarea
      v-model="extEditDraft"
      class="field-input ai-ext-json"
      spellcheck="false"
    ></textarea>
    <div class="ai-ext-actions">
      <button class="btn btn-primary btn-sm" :disabled="extSaving || !extEditDraft.trim()" @click="saveExtManifestJson">
        {{ t('aiExtJsonSaveDo') }}
      </button>
      <button class="btn btn-sm" @click="extEditOpen = false">{{ t('cancel') }}</button>
    </div>
    <span class="ai-ext-tip">{{ t('aiExtJsonHint') }}</span>
  </div>

  <!-- 粘贴导入：别人给的 mcp.json 片段整段贴进来（同名覆盖，其余保留） -->
  <div v-if="!extForm && extImportOpen" class="ai-ext-import">
    <textarea
      v-model="extImportDraft"
      class="field-input ai-ext-json"
      :placeholder="EXT_IMPORT_PH"
      spellcheck="false"
    ></textarea>
    <div class="ai-ext-actions">
      <button class="btn btn-primary btn-sm" :disabled="extSaving || !extImportDraft.trim()" @click="importExtJson">
        {{ t('aiExtImportDo') }}
      </button>
      <button class="btn btn-sm" @click="extImportOpen = false">{{ t('cancel') }}</button>
    </div>
    <span class="ai-ext-tip">{{ t('aiExtImportHint') }}</span>
  </div>

  <!-- 新增 / 编辑表单 -->
  <div v-if="extForm" class="ai-ext-form">
    <div class="ai-ext-grid">
      <label class="field">
        <span class="field-label">{{ t('aiExtName') }}</span>
        <input v-model="extForm.name" class="field-input" :placeholder="t('aiExtNamePh')" spellcheck="false" />
      </label>
      <div class="field">
        <span class="field-label">{{ t('aiExtKind') }}</span>
        <div class="ai-ext-seg">
          <button
            type="button"
            :class="{ active: extForm.kind === 'stdio' }"
            @click="extForm.kind = 'stdio'"
          >{{ t('aiExtKindStdio') }}</button>
          <button
            type="button"
            :class="{ active: extForm.kind === 'http' }"
            @click="extForm.kind = 'http'"
          >{{ t('aiExtKindHttp') }}</button>
        </div>
      </div>
    </div>

    <template v-if="extForm.kind === 'stdio'">
      <label class="field">
        <span class="field-label">{{ t('aiExtCommand') }}</span>
        <input v-model="extForm.command" class="field-input" :placeholder="t('aiExtCommandPh')" spellcheck="false" />
      </label>
      <label class="field">
        <span class="field-label">{{ t('aiExtArgs') }}</span>
        <textarea
          v-model="extForm.argsText"
          class="field-input ai-ext-ta"
          rows="3"
          :placeholder="EXT_ARGS_PH"
          spellcheck="false"
        ></textarea>
        <span class="ai-ext-tip">{{ t('aiExtArgsHint') }}</span>
      </label>
      <label class="field">
        <span class="field-label">{{ t('aiExtEnv') }}</span>
        <textarea
          v-model="extForm.envText"
          class="field-input ai-ext-ta"
          rows="2"
          :placeholder="t('aiExtEnvPh')"
          spellcheck="false"
        ></textarea>
        <span class="ai-ext-tip">{{ t('aiExtEnvHint') }}</span>
      </label>
    </template>
    <template v-else>
      <label class="field">
        <span class="field-label">{{ t('aiExtUrl') }}</span>
        <input v-model="extForm.url" class="field-input" :placeholder="t('aiExtUrlPh')" spellcheck="false" />
      </label>
      <div class="field">
        <span class="field-label">{{ t('aiExtToken') }}</span>
        <div class="ai-ext-secret">
          <input
            v-model="extForm.token"
            :type="extTokenVisible ? 'text' : 'password'"
            class="field-input"
            :placeholder="t('aiExtTokenPh')"
            spellcheck="false"
            autocomplete="off"
          />
          <button type="button" class="btn btn-sm" @click="extTokenVisible = !extTokenVisible">
            {{ extTokenVisible ? t('mcpTokenHide') : t('mcpTokenShow') }}
          </button>
        </div>
        <span class="ai-ext-tip">{{ t('aiExtTokenHint') }}</span>
      </div>
      <label class="field">
        <span class="field-label">{{ t('aiExtHeaders') }}</span>
        <textarea
          v-model="extForm.headersText"
          class="field-input ai-ext-ta"
          rows="2"
          :placeholder="EXT_HEADERS_PH"
          spellcheck="false"
        ></textarea>
        <span class="ai-ext-tip">{{ t('aiExtHeadersHint') }}</span>
      </label>
    </template>

    <div class="ai-ext-actions">
      <button class="btn btn-primary btn-sm" :disabled="extSaving" @click="submitExtForm">{{ t('aiExtSaveReload') }}</button>
      <button class="btn btn-sm" :disabled="extSaving" @click="extForm = null">{{ t('cancel') }}</button>
    </div>
  </div>

  <!-- 已接入列表 -->
  <template v-else>
    <p v-if="extLoading && !extServers.length" class="usage-hint">{{ t('aiUsageLoading') }}</p>
    <p v-else-if="!extServers.length" class="usage-hint">{{ t('aiExtEmpty') }}</p>
    <div v-else class="ai-ext-list">
      <div v-for="s in extServers" :key="s.name" class="ai-ext-item">
        <div class="ai-ext-item-main">
          <div class="ai-ext-item-head">
            <code class="ai-ext-name">{{ s.name }}</code>
            <span class="ai-ext-kind-tag">{{ kindOf(s) === 'http' ? 'HTTP' : 'stdio' }}</span>
            <span :class="['ext-status', s.status]">
              {{ s.status === 'ok' ? t('aiExtStatusOk', { n: s.toolCount }) : s.status === 'disabled' ? t('aiExtStatusOff') : t('aiExtStatusErr') }}
            </span>
          </div>
          <div class="ai-ext-cmd" :title="extTarget(s)">{{ extTarget(s) }}</div>
          <div v-if="s.error" class="usage-cell-sub ai-ext-err">{{ s.error }}</div>
          <div v-else-if="s.tools.length" class="usage-cell-sub">{{ s.tools.join(' · ') }}</div>

          <!-- 工具详情：名字清单上面已有，这里补描述与逐条开关 -->
          <div v-if="extExpanded === s.name" class="ai-ext-tools">
            <p v-if="extToolsLoading" class="usage-cell-sub">{{ t('aiExtToolsLoading') }}</p>
            <p v-else-if="!extTools.length" class="usage-cell-sub">{{ t('aiExtToolsEmpty') }}</p>
            <div v-else class="ai-ext-tools-list">
              <div v-for="tool in extTools" :key="tool.name" class="ai-ext-tool">
                <div class="ai-ext-tool-main">
                  <code class="ai-ext-tool-name">{{ tool.name }}</code>
                  <span v-if="tool.disabled" class="ai-ext-tool-off">{{ t('aiExtToolDisabled') }}</span>
                  <span class="ai-ext-tool-desc">{{ tool.desc }}</span>
                </div>
                <QdSwitch
                  :model-value="!tool.disabled"
                  :disabled="extToolsBusy === tool.name"
                  :label="tool.disabled ? t('aiExtToolEnable') : t('aiExtToolDisable')"
                  :title="tool.disabled ? t('aiExtToolEnable') : t('aiExtToolDisable')"
                  @update:model-value="() => toggleExtTool(tool)"
                />
              </div>
            </div>
          </div>
        </div>
        <div class="ai-ext-item-ops">
          <button
            class="qd-btn qd-btn-ghost qd-btn-sm ai-ext-expand"
            :class="{ 'is-open': extExpanded === s.name }"
            :disabled="extSaving || s.status === 'disabled'"
            :title="extExpanded === s.name ? t('aiExtToolsHide') : t('aiExtToolsShow')"
            @click="toggleExtTools(s)"
          >
            <ChevronDown :size="12" />
          </button>
          <QdSwitch
            :model-value="s.status !== 'disabled'"
            :disabled="extSaving"
            :label="s.status === 'disabled' ? t('aiExtEnable') : t('aiExtDisable')"
            :title="s.status === 'disabled' ? t('aiExtEnable') : t('aiExtDisable')"
            @update:model-value="() => toggleExt(s)"
          />
          <button class="btn btn-sm" :disabled="extSaving" @click="openEditExt(s)">{{ t('aiExtEdit') }}</button>
          <button class="btn btn-danger btn-sm" :disabled="extSaving" @click="removeExt(s)">{{ t('delete') }}</button>
        </div>
      </div>
    </div>

    <div class="ai-ext-actions">
      <button class="btn btn-sm" :disabled="extLoading" @click="reloadExt">
        <RefreshCw :size="12" /> {{ t('aiExtReload') }}
      </button>
    </div>
  </template>
</div>
</template>

<style scoped>
/* 外部 MCP 工具详情：展开区挂在 ai-ext-item-main 里，随行宽收缩 */
.ai-ext-expand {
  display: inline-flex;
  align-items: center;
  padding: var(--space-2);
}
.ai-ext-expand svg {
  transition: transform var(--transition-base);
}
.ai-ext-expand.is-open svg {
  transform: rotate(180deg);
}
.ai-ext-tools {
  margin-top: var(--space-4);
  padding-top: var(--space-4);
  border-top: 1px solid var(--color-border);
}
.ai-ext-tools-list {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
  max-height: 220px;
  overflow-y: auto;
}
.ai-ext-tool {
  display: flex;
  align-items: flex-start;
  gap: var(--space-4);
  padding: var(--space-3) var(--space-4);
  border-radius: var(--radius-sm);
  background: var(--color-bg-tertiary);
}
.ai-ext-tool-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
.ai-ext-tool-name {
  font-size: var(--text-xs);
  color: var(--color-text-primary);
  word-break: break-all;
}
.ai-ext-tool-off {
  font-size: var(--text-2xs);
  color: var(--color-warning-ink);
}
.ai-ext-tool-desc {
  font-size: var(--text-xs);
  line-height: 1.5;
  color: var(--color-text-muted);
  word-break: break-word;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
</style>
