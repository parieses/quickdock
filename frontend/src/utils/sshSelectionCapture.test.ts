import { describe, it, expect } from 'vitest'

/**
 * 复现并锁定「右键问 AI 选区丢失」的根因与修复。
 *
 * 根因：xterm 选区是实时的，选区在「右键 → 菜单出现 → 点击问 AI」这段过程里
 * 可能已被清掉（xterm 在用户输入时清选区；WebView2 下右键时机不可控）。
 * 旧逻辑在点击「问 AI」那一刻才去 `getSelection()`，拿到的可能是空串。
 *
 * 修复：在选区**成立的瞬间**（`onSelectionChange` 回调）把文本快照下来，
 * 点击时优先用快照。本测试用一个最小「伪终端」模拟该流程来验证。
 */

// 最小伪终端：维护实时选区，支持 onSelectionChange / hasSelection / getSelection
function makeFakeTerm() {
  let live = ''
  const listeners: Array<() => void> = []
  return {
    // 模拟用户左键拖选，选区成立 → 触发 selectionchange
    select(text: string) {
      live = text
      listeners.forEach((fn) => fn())
    },
    // 模拟选区随后被清掉（右键/输入）
    clearLive() {
      live = ''
      listeners.forEach((fn) => fn())
    },
    onSelectionChange(fn: () => void) {
      listeners.push(fn)
    },
    hasSelection() {
      return live.trim().length > 0
    },
    getSelection() {
      return live
    },
  }
}

// 复刻 SshTerminal 里的快照逻辑
function makeCapture(term: ReturnType<typeof makeFakeTerm>) {
  let lastSelection = ''
  term.onSelectionChange(() => {
    if (term.hasSelection()) lastSelection = term.getSelection()
  })
  return function capture(): string {
    const raw = lastSelection || (term.hasSelection() ? term.getSelection() : '')
    if (!raw.trim()) return ''
    const lines = raw.split('\n').map((l) => l.trimEnd())
    while (lines.length && lines[0].trim() === '') lines.shift()
    while (lines.length && lines[lines.length - 1].trim() === '') lines.pop()
    return lines.join('\n')
  }
}

describe('右键问 AI 选区捕获', () => {
  it('选区在点击前被清掉，仍能拿到快照', () => {
    const term = makeFakeTerm()
    const capture = makeCapture(term)

    term.select('ERROR: connection refused\n    at foo.bar (app.js:10)')
    // 右键 contextmenu / 取消选择导致实时选区清空（但快照已存）
    term.clearLive()
    expect(term.hasSelection()).toBe(false)

    const got = capture()
    expect(got).toBe('ERROR: connection refused\n    at foo.bar (app.js:10)')
  })

  it('没有任何选区时返回空（不会误发旧选区）', () => {
    const term = makeFakeTerm()
    const capture = makeCapture(term)
    expect(capture()).toBe('')
  })

  it('去掉首尾空行与行尾空格', () => {
    const term = makeFakeTerm()
    const capture = makeCapture(term)
    term.select('   \n  df -h   \n   \n  ls -la  \n')
    const got = capture()
    expect(got).toBe('  df -h\n\n  ls -la')
  })

  it('先选 A 再选 B，快照被最新选区覆盖', () => {
    const term = makeFakeTerm()
    const capture = makeCapture(term)
    term.select('selection A')
    term.select('selection B')
    expect(capture()).toBe('selection B')
  })
})
