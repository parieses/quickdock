/**
 * Diff 文本对比 — Goja 后端
 * 转发前端请求到 handleExecute（由 iframe 内 JS 通过 postMessage 调用）
 */
function handleInitialize(params) {
  return { status: 'ready', version: '0.2.0' }
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
  var oLen = oldLines.length
  var nLen = newLines.length

  // 构建 LCS 动态规划表
  var dp = []
  for (var i = 0; i <= oLen; i++) {
    dp[i] = []
    for (var j = 0; j <= nLen; j++) dp[i][j] = 0
  }
  for (var i2 = 1; i2 <= oLen; i2++) {
    for (var j2 = 1; j2 <= nLen; j2++) {
      if (oldLines[i2 - 1] === newLines[j2 - 1]) {
        dp[i2][j2] = dp[i2 - 1][j2 - 1] + 1
      } else {
        dp[i2][j2] = Math.max(dp[i2 - 1][j2], dp[i2][j2 - 1])
      }
    }
  }

  // 回溯构造差异（eq 表示不变行，del/add 表示删除/新增）
  var raw = []
  var oi = oLen, ni = nLen
  while (oi > 0 || ni > 0) {
    if (oi > 0 && ni > 0) {
      if (oldLines[oi - 1] === newLines[ni - 1]) {
        raw.unshift({ type: 'eq', oldLineNum: oi - 1, newLineNum: ni - 1 })
        oi--; ni--
        continue
      }
      if (dp[oi - 1][ni] >= dp[oi][ni - 1]) {
        raw.unshift({ type: 'del', oldLineNum: oi - 1, newLineNum: -1, text: oldLines[oi - 1] })
        oi--
      } else {
        raw.unshift({ type: 'add', oldLineNum: -1, newLineNum: ni - 1, text: newLines[ni - 1] })
        ni--
      }
    } else if (oi > 0) {
      raw.unshift({ type: 'del', oldLineNum: oi - 1, newLineNum: -1, text: oldLines[oi - 1] })
      oi--
    } else {
      raw.unshift({ type: 'add', oldLineNum: -1, newLineNum: ni - 1, text: newLines[ni - 1] })
      ni--
    }
  }

  // 仅输出变化（过滤 eq），并将相邻的 del+add 合并为 mod（修改）。
  // 合并与顺序无关（回溯用 unshift，相邻对可能是 add,del 或 del,add）；
  // 仅当两者内容不同才合并，避免把纯重排误判为修改。
  var changes = []
  for (var k = 0; k < raw.length; k++) {
    if (raw[k].type === 'eq') continue
    if (k + 1 < raw.length) {
      var a = raw[k], b = raw[k + 1]
      var delEntry = a.type === 'del' ? a : (b.type === 'del' ? b : null)
      var addEntry = a.type === 'add' ? a : (b.type === 'add' ? b : null)
      if (delEntry && addEntry) {
        var oldTxt = oldLines[delEntry.oldLineNum]
        var newTxt = newLines[addEntry.newLineNum]
        if (oldTxt !== newTxt) {
          changes.push({
            type: 'mod',
            oldLineNum: delEntry.oldLineNum,
            newLineNum: addEntry.newLineNum,
            oldText: oldTxt,
            newText: newTxt
          })
          k++ // 跳过配对项
          continue
        }
      }
    }
    changes.push(raw[k])
  }

  return changes
}
