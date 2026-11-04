// AI 工具调用的解析与展示辅助。
//
// 这一层只有纯函数（唯一的额外依赖是 i18n 的 t，按参数传入）：
// 从 AIMessage 的 tool_calls JSON 解析、给卡片挑图标/动词/目标摘要、
// 聚合出「这条回答写了哪些文件」。抽出来是因为 AIPage 里这部分与
// 页面状态无关，留在 5000 行文件里既难找也难测。
import type { Component } from 'vue'
import {
  Bell, Bot, Bug, Clock, FilePlus, FileSearch, FileText, FolderTree, GitBranch, Globe,
  LayoutList, Link as LinkIcon, ListChecks, Network, PenLine, Play, Plus, Search, Square,
  Terminal, Wrench,
} from '@lucide/vue'
import type { AIArtifact, AIMessage, AIRuleFile, AIToolCall } from '../types/ai'

// toolList 把消息里的 tool_calls 解析成数组。
//
// 用 try/catch 兜住：这是历史数据，可能来自旧版本、也可能被手工改过库；
// 解析失败就不显示卡片，而不是让整条消息渲染崩掉（一个坏 JSON 不该白屏）。
//
// 结果按「消息对象 + 原始 JSON 字符串」缓存：模板里同一条消息会被读好几次
// （摘要 / 轮次 / 卡片 / key），AIPage 的 convArtifacts 每次重算又要遍历全部历史
// 消息，流式期间每个工具事件都会触发一遍。没有缓存就是每条消息每帧一次
// JSON.parse，长会话里这笔开销随消息数线性放大。WeakMap 保证消息被换掉后缓存
// 自动回收；同时校验 raw，避免消息对象被原地改写（tool_calls 变了）还命中旧值。
const toolListCache = new WeakMap<object, { raw: string; list: AIToolCall[] }>()
const NO_TOOLS: AIToolCall[] = []

export function toolList(m: AIMessage): AIToolCall[] {
  const raw = m.tool_calls || ''
  if (!raw) return NO_TOOLS
  const hit = toolListCache.get(m)
  if (hit && hit.raw === raw) return hit.list
  let list: AIToolCall[] = NO_TOOLS
  try {
    const arr = JSON.parse(raw)
    if (Array.isArray(arr)) list = arr as AIToolCall[]
  } catch {
    list = NO_TOOLS
  }
  // 坏 JSON 也缓存结果（仍是 []），但 raw 一变就会失效重新解析，
  // 不会把后来修好的数据永久钉死。调用方一律只读，不修改返回的数组。
  toolListCache.set(m, { raw, list })
  return list
}

// runIDOf 取这条回复所属的编排 run（同一条回复里派了多个子任务时它们同属一个 run，
// 所以取第一条带 run_id 的调用即可）。
// 为空的回复（绝大多数）不渲染 run 树面板——不能对每句普通回答都挂一个空面板。
export function runIDOf(m: AIMessage): string {
  if (m.role !== 'assistant') return ''
  for (const tc of toolList(m)) {
    if (tc.run_id) return tc.run_id
  }
  return ''
}

// rulesOf 取这条回复生效过的规则文件。只有本次会话里收到的回复有（见 runRules），
// 刷新后重新从库里读的消息没有这个字段——规则是执行时事实，不落库。
export function rulesOf(m: Record<string, any>): AIRuleFile[] {
  const v = m?.rules
  return Array.isArray(v) ? (v as AIRuleFile[]) : []
}

// rulesTitle 悬停时列出文件名与字节数：截断过的一定要标出来，
// 否则「我写了 30KB 的规则，AI 只记住一半」这件事永远查不出来。
export function rulesTitle(files: AIRuleFile[]): string {
  return files.map((f) => `${f.name} · ${f.bytes} B${f.truncated ? ' · 已截断' : ''}`).join('\n')
}

// serializeTools 把流式期收集的记录转成落库/展示同构的 JSON。
// 丢掉 running：库里存的是「发生过什么」，不是「界面此刻什么样」。
export function serializeTools(list: AIToolCall[]): string {
  return JSON.stringify(list.map((t) => ({
    id: t.id, name: t.name, args: t.args || '',
    result: t.result || '', error: t.error || '',
    ms: t.ms || 0, round: t.round || 0,
    ...(t.dedup ? { dedup: true } : {}),
    // 拒绝是结果不是瞬时态：刷新后卡片还要显示「已拒绝」，所以必须落库。
    // approval 状态本身不落——pending 在这条消息进库时早已有结论。
    ...(t.denied ? { denied: true } : {}),
    ...(t.added ? { added: t.added } : {}),
    ...(t.removed ? { removed: t.removed } : {}),
    // diff 要落库：刷新后卡片还能展开看到「这一步改了什么」，
    // 而这正是用户回看历史时最需要的信息。
    ...(t.diff ? { diff: t.diff } : {}),
    // 产物路径落库：刷新后产物条还在（预览读的是磁盘现状，不需要存更多东西）。
    ...(t.path ? { path: t.path } : {}),
    // 人看的输出也要落库：刷新后卡片上仍是那几行命令输出，而不是退回原始 JSON。
    ...(t.output ? { output: t.output } : {}),
    // run/node 必须一起落库：刷新后 run 树面板靠它才知道该拉哪棵树。
    ...(t.run_id ? { run_id: t.run_id, node_id: t.node_id || '' } : {}),
  })))
}

// countToolRounds 数轮次而不是数条数：一轮可以并发调多个工具，
// 卡片上写「5 个工具」和「3 轮」是两件事，用户问的「调了几轮」是后者。
export function countToolRounds(list: AIToolCall[]): number {
  return new Set(list.filter((t) => t.round).map((t) => t.round)).size
}

// artifactsFromCalls 聚合一条回复里写过 / 改过的文件。
//
// 数据来源刻意选 tool_calls 记录而不是单独的事件/表：写类调用本来就一条条
// 落库了，再开一张「产物表」只会引入第二份需要保持同步的状态。同一文件被
// 改多次只留一条（预览读的是磁盘现状，列五条重复项没有意义）；
// 失败 / 被拒 / 去重跳过的调用没有产生文件，不算产物。
export function artifactsFromCalls(list: AIToolCall[]): AIArtifact[] {
  const map = new Map<string, AIArtifact>()
  for (const tc of list) {
    if (tc.dedup || tc.denied || tc.error) continue
    if ((tc.name === 'file_write' || tc.name === 'file_edit') && tc.path) {
      map.set(tc.path, {
        path: tc.path,
        name: tc.path.replace(/\\/g, '/').split('/').pop() || tc.path,
        op: tc.name === 'file_write' ? 'write' : 'edit',
        // 带上「哪一次调用写的、写完没有」：侧栏据此判断已打开的文件该不该重读。
        callId: tc.id,
        done: !tc.running,
      })
    }
  }
  return [...map.values()]
}

// artifactsOf 历史消息的产物。v-memo 的依赖里已有 m.tool_calls，
// 派生量不需要再进依赖数组。
export function artifactsOf(m: AIMessage): AIArtifact[] {
  if (m.role !== 'assistant') return []
  return artifactsFromCalls(toolList(m))
}

const TOOL_ICON: Record<string, Component> = {
  file_edit: PenLine, file_write: FilePlus, file_read: FileText, file_search: FileSearch, file_grep: FileSearch,
  dir_list: FolderTree, git_status: GitBranch, git_diff: GitBranch,
  web_fetch: Globe, web_links: LinkIcon, web_head: Globe,
  run_command: Terminal, check_workdir: FolderTree,
  // 派活：树状分叉的图示比扳手更能说明「这里分出去了几路子任务」。
  spawn_agent: Network,
  plugin_execute: Play, plugin_open: LayoutList, plugin_list: LayoutList,
  env_list: ListChecks, env_start: Play, env_stop: Square, env_status: Bug,
  port_list: LayoutList, log_list: FileText, log_read: FileText,
  monitor_list: Clock, monitor_check_now: Clock, monitor_logs: Clock,
  schedule_list: Clock, schedule_runs: Clock, schedule_run_now: Play,
  todo_list: ListChecks, todo_add: Plus, todo_done: ListChecks,
  note_search: FileText, note_create: FileText,
  clipboard_list: LayoutList, clipboard_search: Search,
  robot_notify: Bell, app_info: Bot, diag_run: Bug,
}

// toolIcon 按工具名挑图标；未登记的新工具按名字前缀兜底，
// 至少能看出它是读文件、动 git 还是跑命令。
export function toolIcon(name: string): Component {
  if (TOOL_ICON[name]) return TOOL_ICON[name]
  if (name.startsWith('file_')) return FileText
  if (name.startsWith('git_')) return GitBranch
  if (name.startsWith('web_')) return Globe
  if (name.startsWith('env_')) return ListChecks
  if (name.startsWith('monitor_') || name.startsWith('schedule_')) return Clock
  if (name.startsWith('plugin_')) return Play
  if (name.startsWith('todo_')) return ListChecks
  if (name.startsWith('note_')) return FileText
  if (name.startsWith('clipboard_')) return LayoutList
  if (name.startsWith('log_')) return FileText
  return Wrench
}

const TOOL_VERB: Record<string, string> = {
  file_edit: 'aiToolVerbEdit', file_write: 'aiToolVerbWrite', file_read: 'aiToolVerbRead', file_search: 'aiToolVerbSearch',
  file_grep: 'aiToolVerbSearch', dir_list: 'aiToolVerbList',
  git_status: 'aiToolVerbGitStatus', git_diff: 'aiToolVerbGitDiff',
  web_fetch: 'aiToolVerbFetch', web_links: 'aiToolVerbLinks', web_head: 'aiToolVerbHead',
  run_command: 'aiToolVerbRun', check_workdir: 'aiToolVerbProbe',
  spawn_agent: 'aiToolVerbSpawn',
  plugin_execute: 'aiToolVerbPlugin', plugin_open: 'aiToolVerbOpen', plugin_list: 'aiToolVerbList',
  env_list: 'aiToolVerbQuery', env_start: 'aiToolVerbStart', env_stop: 'aiToolVerbStop',
  env_status: 'aiToolVerbQuery', port_list: 'aiToolVerbQuery',
  log_list: 'aiToolVerbQuery', log_read: 'aiToolVerbRead',
  monitor_list: 'aiToolVerbQuery', monitor_check_now: 'aiToolVerbCheck', monitor_logs: 'aiToolVerbQuery',
  schedule_list: 'aiToolVerbQuery', schedule_runs: 'aiToolVerbQuery', schedule_run_now: 'aiToolVerbRun',
  todo_list: 'aiToolVerbQuery', todo_add: 'aiToolVerbAdd', todo_done: 'aiToolVerbUpdate',
  note_search: 'aiToolVerbSearch', note_create: 'aiToolVerbAdd',
  clipboard_list: 'aiToolVerbQuery', clipboard_search: 'aiToolVerbSearch',
  robot_notify: 'aiToolVerbNotify', app_info: 'aiToolVerbQuery', diag_run: 'aiToolVerbCheck',
}

// toolVerb 按工具名前缀兜底：新工具没登记时至少能看出它是读、改、跑还是查。
// t 由调用方传入：本文件不引 useI18n，保持可在测试里裸调。
export function toolVerb(name: string, t: (key: string) => string): string {
  const k = TOOL_VERB[name]
  return k ? t(k) : t('aiToolVerbCall')
}

// 目标：从参数里挑一个「这条卡片在说谁」的值。
//
// 只挑 path / url / command / pattern / query / name / id 这几个键 ——
// 全参数铺开的话，一条 file_edit 的摘要会有三行，而用户真正想确认的只是
// 「它动的是不是我那个文件」。
const TOOL_TARGET_KEYS = ['path', 'url', 'command', 'pattern', 'query', 'keyword', 'name', 'id', 'plugin', 'runtime', 'dir']

// 路径只留末尾两段：绝对路径在卡片上是噪音，用户要确认的是「哪个文件」，
// 而 `E:\pp\quickdock\services\ai\tools\code.go` 里有用的只有后两截。
export function shortPath(p: string): string {
  const sep = p.includes('\\') ? '\\' : '/'
  const parts = p.split(/[\\/]/).filter(Boolean)
  if (parts.length <= 2) return p
  return '…' + sep + parts.slice(-2).join(sep)
}

export function toolTarget(tc: AIToolCall): string {
  const raw = (tc.args || '').trim()
  if (!raw) return ''
  let obj: Record<string, any> | null = null
  try { obj = JSON.parse(raw) } catch { obj = null }
  if (!obj) {
    // 参数解析失败时至少给原文的前一段：模型写坏了 JSON 也要能看出它想动谁。
    return raw.length > 80 ? raw.slice(0, 80) + '…' : raw
  }
  for (const k of TOOL_TARGET_KEYS) {
    const v = obj[k]
    if (typeof v === 'string' && v.trim()) return shortPath(v.trim())
    if (typeof v === 'number') return String(v)
  }
  // 没命中已知键就退回第一个字符串值，比整段 JSON 可读得多。
  for (const [, v] of Object.entries(obj)) {
    if (typeof v === 'string' && v.trim()) return shortPath(v.trim())
  }
  return ''
}

// toolOutput 卡片正文：优先显示「人看的输出」，没有再退回结果原文。
//
// run_command 的结果是给模型的 JSON（stdout/stderr/exitCode 装在一个对象里），
// 直接铺在卡片上是一团转义字符，截断后还是半截 JSON。后端另外给一份 output
// 纯文本，这里只在它缺失时（老数据、其它工具）才退回 result。
export function toolOutput(tc: AIToolCall): string {
  if (tc.output) return tc.output
  // 流式期：命令还在跑，output 还没到，先铺「已确认的行 + 当前正在刷新的那一行」。
  // liveTail 单独拼而不是并进 live：进度条用 \r 重写它，拼接会越滚越长。
  // 第一个事件常常只有 tail（pip 一上来就画进度条，还没换过行），
  // 所以判断条件必须带上 liveTail——只看 live 的话卡片会空着不动。
  if (tc.live || tc.liveTail) return (tc.live || '') + (tc.liveTail || '')
  return tc.result || ''
}

// toolResultLang 工具结果该按什么语言高亮（喂给 highlight.js）：
// 只认「结果本身有固定语法」的三类，其余返回空 → 不高亮，按纯文本显示。
// 判错比不判更糟：给一段普通输出套上 bash 高亮，颜色会盖过语义。
export function toolResultLang(tc: AIToolCall): string {
  if (tc.name === 'run_command') return 'bash'
  if (tc.name.startsWith('git_')) return 'diff'
  if (tc.name === 'file_edit') return 'diff'
  return ''
}

// 后台任务 id：run_command 以 background 方式跑时，结果串里会带 jobId。
// 卡片上要靠它显式提醒「关掉应用后这个进程就没了」——用户看的是卡片，
// 工具描述里写过的同一句话他不会记得。
export function jobIdOf(tc: AIToolCall): string {
  if (tc.name !== 'run_command' || !tc.result) return ''
  const m = /"jobId"\s*:\s*"([^"]+)"/.exec(tc.result)
  return m ? m[1] : ''
}
