/**
 * Diff 文本对比 — Goja 后端
 * 转发前端请求到 handleExecute（由 iframe 内 JS 通过 postMessage 调用）
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.1.0' }
}

function handleExecute(params) {
  var input = params.input || {}
  var command = params.command || ''

  // 纯前端交互，后端仅作占位
  if (command === 'open-diff') {
    return { status: 'ok', frontendOnly: true }
  }

  // 后端也可做文本对比（给无前端场景使用）
  if (command === 'compute-diff' && input.oldText !== undefined && input.newText !== undefined) {
    var result = computeDiff(input.oldText, input.newText)
    return { text: JSON.stringify(result), diff: result }
  }

  return { error: '未知命令' }
}

// ---- LCS 差分子集（供前端调用，或供无前端场景使用）----

function computeDiff(oldText, newText) {
  var oldLines = oldText.split('\n')
  var newLines = newText.split('\n')

  // 简单标记：直接逐行对比（不追求最优 LCS）
  var maxLen = Math.max(oldLines.length, newLines.length)
  var changes = []

  for (var i = 0; i < maxLen; i++) {
    var oldLine = i < oldLines.length ? oldLines[i] : null
    var newLine = i < newLines.length ? newLines[i] : null

    if (oldLine === null) {
      changes.push({ type: 'add', oldLineNum: -1, newLineNum: i, text: newLine })
    } else if (newLine === null) {
      changes.push({ type: 'del', oldLineNum: i, newLineNum: -1, text: oldLine })
    } else if (oldLine !== newLine) {
      changes.push({ type: 'mod', oldLineNum: i, newLineNum: i, oldText: oldLine, newText: newLine })
    }
  }

  return changes
}
