/**
 * AI 回答的正文分段逻辑（通用）。
 *
 * 背景：模型输出是交错的——先解释、再给代码块、再补充说明。
 * 面板要按这个原始顺序渲染，说明文字走 markdown、代码块走可复制卡片。
 *
 * 单独抽成工具文件而不是写在 .vue 里，有两个原因：
 *   1. 这段逻辑决定「用户看到什么」，是回归风险最高的地方，必须有单测；
 *   2. 组件里再写一遍围栏状态机，前后端各一份判定规则容易走偏
 *      （后端认为这是命令、前端认为是正文 → 同一条命令显示两遍）。
 *
 * 与后端 internal/aiutil 的围栏判定是同一套规则（逐行状态机）：
 * 后端是权威实现，本文件是前端兜底（后端已返回 segments 时不走它）。
 * 改任一边时另一边必须跟着改。
 */

/** 围栏开/闭标记行：``` 或 ~~~（允许最多 3 个前导空格）。 */
const FENCE_RE = /^\s{0,3}(`{3,}|~{3,})/

// 以下两个正则与后端 internal/aiutil/scenario.go 的 shellPromptRe / commandTokenRe 同形。
// 两边一旦不同步，就会出现「后端把 `# apt-get install nginx` 当命令、
// 前端兜底把它当注释丢掉」这种分叉——用户看到的取决于走的是哪条路径，
// 排查时极难复现。改任一边时另一边必须跟着改。

/** 行首的 shell 提示符（`$` 普通用户 / `#` root）后跟空白。 */
const SHELL_PROMPT_RE = /^[$#][ \t]+(.*)$/

/** 首 token 像可执行文件名的形状。只在兜底回扫里用，用来挡掉中文注释。 */
const COMMAND_TOKEN_RE = /^[A-Za-z0-9_./-]+/

/** 一段内容。 */
export type AiSegment =
  | { kind: 'text'; text: string }
  | { kind: 'cmd'; commands: string[] }

/**
 * 扫一遍围栏块的行，产出命令。
 *
 * rescueHash=true 时把 `# ` 当 root 提示符剥离（并额外用 COMMAND_TOKEN_RE 过滤）；
 * 否则 `# ` 一律视为注释。与后端 collectFenceCommands 同规则。
 */
function collectFenceCommands(lines: string[], rescueHash: boolean, seen: Set<string>): string[] {
  const cmds: string[] = []
  for (const line of lines) {
    let cmd = line.trim()
    if (!cmd) continue
    const m = SHELL_PROMPT_RE.exec(cmd)
    if (m) {
      // 只有兜底回扫才把 `# ` 当提示符；`$ ` 在任何一遍都剥。
      if (cmd[0] === '#' && !rescueHash) continue
      cmd = m[1].trim()
    }
    // 未跟空白的 `#`（#!/bin/bash、#注释）在任何一遍都是注释。
    if (!cmd || cmd.startsWith('#')) continue
    if (rescueHash && !COMMAND_TOKEN_RE.test(cmd)) continue
    if (seen.has(cmd)) continue
    seen.add(cmd)
    cmds.push(cmd)
  }
  return cmds
}

/** 块内**每个**非空行是否都以「提示符 + 空白」开头。与后端 allPromptLines 同规则。 */
function allPromptLines(lines: string[], marker: string): boolean {
  let n = 0
  for (const l of lines) {
    const s = l.trim()
    if (!s) continue
    n++
    if (!s.startsWith(marker + ' ') && !s.startsWith(marker + '\t')) return false
  }
  return n > 0
}

/**
 * 把模型输出按围栏切成有序分段。
 *
 * 围栏内的内容原样作为命令返回（不做风险判定——那是后端的事）。
 * 围栏外的文字保留 markdown 原文，由调用方渲染。
 *
 * 与后端 `aiutil.ExtractSegments` 同一套判定规则（逐行状态机）：
 *   - 支持 ``` 与 ~~~ 两种围栏
 *   - 支持未闭合围栏（模型被 max_tokens 截断），此时剩余内容都算命令
 *   - 收尾标记字符相同且长度不短于开标记
 */
export function splitAiSegments(raw: string): AiSegment[] {
  const text = raw ?? ''
  if (!text.trim()) return []

  const lines = text.split('\n')
  const out: AiSegment[] = []
  let buf: string[] = []
  let fenceLines: string[] = []
  let inFence = false
  let fenceChar = ''
  let fenceLen = 0
  /** 已出现过的命令，跨块去重（与后端 ExtractSegments 的 seen 同义）。 */
  const seen = new Set<string>()

  const flushText = () => {
    const body = buf.join('\n').trim()
    buf = []
    if (!body) return
    out.push({ kind: 'text', text: body })
  }

  const flushCmd = () => {
    const body = fenceLines.join('\n').trim()
    fenceLines = []
    if (!body) return
    const lines = body.split('\n')
    let commands = collectFenceCommands(lines, false, seen)
    // 兜底回扫：正常遍一条没得到、且整块每行都以 `# ` 开头 → 按 root 提示符重扫。
    // 两个条件必须同时成立，否则会把注释救成命令。
    if (!commands.length && allPromptLines(lines, '#')) {
      commands = collectFenceCommands(lines, true, seen)
    }
    if (commands.length) out.push({ kind: 'cmd', commands })
  }

  for (const line of lines) {
    const m = FENCE_RE.exec(line)
    if (!m) {
      if (inFence) fenceLines.push(line)
      else buf.push(line)
      continue
    }
    const mark = m[1]
    if (!inFence) {
      // 围栏开始：先把之前攒的说明文字切出来
      flushText()
      inFence = true
      fenceChar = mark[0]
      fenceLen = mark.length
      continue
    }
    // 围栏结束
    if (mark[0] === fenceChar && mark.length >= fenceLen) {
      flushCmd()
      inFence = false
    }
    // 围栏内的其它标记行：属于内容，保留
  }

  // 收尾：未闭合围栏也要把命令切出来
  if (inFence) flushCmd()
  flushText()
  return out
}
