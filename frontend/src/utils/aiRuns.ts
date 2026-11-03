import type { AIRuleFile, AIToolCall } from '../types/ai'
import type { LiveBlock } from '../composables/useAiMarkdownBlocks'

// 多条任务同时跑时，决定「眼前这一页该画哪一条」。
//
// 抽成纯函数是因为这条规则有两个容易写错的分支（见下），而它们一旦写错，
// 表现是「回答串到别的任务上」——最贵的一种 bug，且只靠点界面很难复现。
// 规则：
//   · 有会话 id → 只认这个会话自己的那条流。别的任务在跑与这一页无关。
//   · 没有会话 id（停在落地页）→ 只认**这一张空白草稿**发起的那条：
//       新任务在 conv 事件回来之前没有 id，若按「随便哪条没有 id 的流」去找，
//       连开两条新任务时两条会互相顶掉，甚至把刚发的字画到另一条上。
export interface RunLike {
  /** 本地唯一 id（新任务在拿到会话 id 之前只有它） */
  rid: string
  /** 会话 id（新任务拿到之前是空串） */
  convId: string
}

/**
 * 一条正在跑的流。
 *
 * 类型放在这里而不是 AIPage.vue：消息区（AiMessageList）要直接读它来画流式气泡。
 * 之前是父组件把 reasoning / tools / blocks / rules 一条条算好再当 props 传下去，
 * 于是「blocks 每帧都变」这件事把**整个页面**的 render 都拖进流式节奏里——
 * 4200 行组件的模板每帧走一遍 diff。改成传 runs 数组 + 会话 id，读取发生在
 * 子组件内部，父组件的模板不再依赖任何每帧变化的字段。
 */
export interface AIRun extends RunLike {
  /** 正文 / 思考过程（已提交部分）与待提交缓冲 */
  text: string
  buf: string
  reasoning: string
  reasonBuf: string
  tools: AIToolCall[]
  rules: AIRuleFile[]
  blocks: LiveBlock[]
  /** 只属于这一条的错误（切走再切回来仍能看到失败原因） */
  error: string
  startedAt: number
  elapsedMs: number
  flushTimer: ReturnType<typeof setTimeout> | null
  tickTimer: ReturnType<typeof setInterval> | null
  ctrl: AbortController | null
  /** 收到过 done：收尾时不必再补一条「中断」气泡 */
  done: boolean
  /** 用户自己点了停止。
   *
   *  收尾时要靠它区分「用户停的」和「流断了」：两者都是没收到 done 就结束，
   *  但前者是用户的选择（不该报错），后者是异常（必须告诉用户，否则他只知道
   *  「回答只有半截」，不知道该不该重发）。 */
  stoppedByUser?: boolean
  /** 发送时定下的模型名（done 事件没回传时用它） */
  model: string
}

export function pickActiveRun<T extends RunLike>(
  runs: readonly T[],
  activeId: string,
  draftRunId: string,
): T | undefined {
  if (activeId) return runs.find((r) => r.convId === activeId)
  return runs.find((r) => r.rid === draftRunId)
}
