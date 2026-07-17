/**
 * Diff 文本对比 — 前端逻辑
 * 使用 Myers diff 风格逐行对比 + 字符级差异高亮
 */
(function() {
  'use strict'

  var leftInput = document.getElementById('leftInput')
  var rightInput = document.getElementById('rightInput')
  var btnCompare = document.getElementById('btnCompare')
  var btnSwap = document.getElementById('btnSwap')
  var btnClear = document.getElementById('btnClear')
  var chkIgnoreCase = document.getElementById('chkIgnoreCase')
  var chkTrim = document.getElementById('chkTrim')
  var tdStats = document.getElementById('tdStats')
  var tdResult = document.getElementById('tdResult')
  var diffOutput = document.getElementById('diffOutput')
  var resultStats = document.getElementById('resultStats')
  var leftLines = document.getElementById('leftLines')
  var rightLines = document.getElementById('rightLines')

  // 快捷键监听
  document.addEventListener('keydown', function(e) {
    if (e.ctrlKey && e.key === 'Enter') { e.preventDefault(); doDiff() }
    if (e.key === 'Escape') { tdResult.style.display = 'none'; leftInput.focus() }
  })

  // 行数统计
  leftInput.addEventListener('input', updateLineCounts)
  rightInput.addEventListener('input', updateLineCounts)

  function updateLineCounts() {
    leftLines.textContent = (leftInput.value.match(/\n/g) || []).length + 1 + ' 行'
    rightLines.textContent = (rightInput.value.match(/\n/g) || []).length + 1 + ' 行'
  }

  // 按钮事件
  btnCompare.addEventListener('click', doDiff)
  btnSwap.addEventListener('click', function() {
    var tmp = leftInput.value
    leftInput.value = rightInput.value
    rightInput.value = tmp
    updateLineCounts()
  })
  btnClear.addEventListener('click', function() {
    leftInput.value = ''
    rightInput.value = ''
    tdResult.style.display = 'none'
    updateLineCounts()
    leftInput.focus()
  })

  // ---- 核心 Diff 算法（LCS 变体）----

  function doDiff() {
    var oldText = leftInput.value
    var newText = rightInput.value

    if (!oldText && !newText) {
      tdStats.textContent = '差异: 0 行'
      tdResult.style.display = 'none'
      return
    }

    var oldLines = oldText.split('\n')
    var newLines = newText.split('\n')

    if (chkTrim.checked) {
      oldLines = oldLines.map(function(l){ return l.trim() })
      newLines = newLines.map(function(l){ return l.trim() })
    }

    // LCS 计算差异
    var diff = computeLCS(oldLines, newLines)
    var stats = { add: 0, del: 0, mod: 0, eq: 0 }

    for (var i = 0; i < diff.length; i++) {
      if (diff[i].type === 'add') stats.add++
      else if (diff[i].type === 'del') stats.del++
      else if (diff[i].type === 'mod') stats.mod++
      else stats.eq++
    }

    // 渲染
    renderDiff(diff, oldLines, newLines)

    tdStats.textContent = '差异: ' + (stats.add + stats.del + stats.mod) + ' 行'
    resultStats.textContent = '新增 ' + stats.add + '  |  删除 ' + stats.del + '  |  修改 ' + stats.mod + '  |  相同 ' + stats.eq
    tdResult.style.display = 'flex'
  }

  function computeLCS(oldLines, newLines) {
    var oLen = oldLines.length
    var nLen = newLines.length

    // 构建 LCS 表
    var dp = []
    for (var i = 0; i <= oLen; i++) {
      dp[i] = []
      for (var j = 0; j <= nLen; j++) dp[i][j] = 0
    }

    for (var i2 = 1; i2 <= oLen; i2++) {
      for (var j2 = 1; j2 <= nLen; j2++) {
        var a = chkIgnoreCase.checked ? oldLines[i2 - 1].toLowerCase() : oldLines[i2 - 1]
        var b = chkIgnoreCase.checked ? newLines[j2 - 1].toLowerCase() : newLines[j2 - 1]
        if (a === b) {
          dp[i2][j2] = dp[i2 - 1][j2 - 1] + 1
        } else {
          dp[i2][j2] = Math.max(dp[i2 - 1][j2], dp[i2][j2 - 1])
        }
      }
    }

    // 回溯 LCS 构建 diff 结果
    var result = []
    var oi = oLen, ni = nLen

    while (oi > 0 || ni > 0) {
      if (oi > 0 && ni > 0) {
        var av = chkIgnoreCase.checked ? oldLines[oi - 1].toLowerCase() : oldLines[oi - 1]
        var bv = chkIgnoreCase.checked ? newLines[ni - 1].toLowerCase() : newLines[ni - 1]
        if (av === bv) {
          result.unshift({ type: 'eq', oldIdx: oi - 1, newIdx: ni - 1 })
          oi--; ni--
          continue
        }
        if (dp[oi - 1][ni] >= dp[oi][ni - 1]) {
          result.unshift({ type: 'del', oldIdx: oi - 1, newIdx: -1 })
          oi--
        } else {
          result.unshift({ type: 'add', oldIdx: -1, newIdx: ni - 1 })
          ni--
        }
      } else if (oi > 0) {
        result.unshift({ type: 'del', oldIdx: oi - 1, newIdx: -1 })
        oi--
      } else {
        result.unshift({ type: 'add', oldIdx: -1, newIdx: ni - 1 })
        ni--
      }
    }

    // 将相邻的 del+add 合并为 mod（修改）
    var merged = []
    for (var k = 0; k < result.length; k++) {
      if (k + 1 < result.length &&
          result[k].type === 'del' && result[k + 1].type === 'add') {
        merged.push({
          type: 'mod',
          oldIdx: result[k].oldIdx,
          newIdx: result[k + 1].newIdx
        })
        k++ // 跳过下一个
      } else {
        merged.push(result[k])
      }
    }

    return merged
  }

  // ---- 渲染 ----

  function renderDiff(diff) {
    var html = ''

    for (var i = 0; i < diff.length; i++) {
      var d = diff[i]
      var cls, marker, text, oldNum, newNum

      if (d.type === 'eq') {
        cls = 'diff-eq'
        marker = ' '
        text = leftInput.value.split('\n')[d.oldIdx]
        oldNum = d.oldIdx + 1
        newNum = d.newIdx + 1
      } else if (d.type === 'add') {
        cls = 'diff-add'
        marker = '+'
        text = rightInput.value.split('\n')[d.newIdx]
        oldNum = ''
        newNum = d.newIdx + 1
      } else if (d.type === 'del') {
        cls = 'diff-del'
        marker = '-'
        text = leftInput.value.split('\n')[d.oldIdx]
        oldNum = d.oldIdx + 1
        newNum = ''
      } else { // mod
        cls = 'diff-mod'
        marker = '~'
        var oldText = leftInput.value.split('\n')[d.oldIdx]
        var newText = rightInput.value.split('\n')[d.newIdx]
        text = oldText + ' → ' + newText
        oldNum = d.oldIdx + 1
        newNum = d.newIdx + 1
      }

      // 字符级差异（仅修改行）
      var textHtml = escapeHtml(text || '')
      if (d.type === 'mod') {
        var ot = leftInput.value.split('\n')[d.oldIdx]
        var nt = rightInput.value.split('\n')[d.newIdx]
        textHtml = charDiff(ot || '', nt || '')
      }

      html += '<div class="diff-line ' + cls + '">' +
        '<span class="diff-linenum">' + (oldNum || '') + '</span>' +
        '<span class="diff-linenum">' + (newNum || '') + '</span>' +
        '<span class="diff-marker">' + marker + '</span>' +
        '<span class="diff-text">' + textHtml + '</span>' +
        '</div>'
    }

    diffOutput.innerHTML = html
  }

  // 字符级差异：给定旧/新文本，生成带高亮的 HTML
  function charDiff(oldStr, newStr) {
    if (oldStr === newStr) return escapeHtml(oldStr)

    // 简单字符对比：逐字符标记添加/删除
    var oldChars = oldStr.split('')
    var newChars = newStr.split('')

    // 使用 LCS 找出共有字符
    var lcs = charLCS(oldChars, newChars)

    var oldHtml = ''
    var oi = 0, li = 0
    while (oi < oldChars.length) {
      if (li < lcs.length && lcs[li] === oldChars[oi]) {
        oldHtml += escapeChar(oldChars[oi])
        li++
      } else {
        oldHtml += '<span class="diff-char-del">' + escapeChar(oldChars[oi]) + '</span>'
      }
      oi++
    }

    var newHtml = ''
    var ni = 0; li = 0
    while (ni < newChars.length) {
      if (li < lcs.length && lcs[li] === newChars[ni]) {
        newHtml += escapeChar(newChars[ni])
        li++
      } else {
        newHtml += '<span class="diff-char-add">' + escapeChar(newChars[ni]) + '</span>'
      }
      ni++
    }

    return '<span class="diff-old">' + oldHtml + '</span> → <span class="diff-new">' + newHtml + '</span>'
  }

  function charLCS(a, b) {
    var m = a.length, n = b.length
    var dp = []
    for (var i = 0; i <= m; i++) {
      dp[i] = []
      for (var j = 0; j <= n; j++) dp[i][j] = 0
    }
    for (var i2 = 1; i2 <= m; i2++) {
      for (var j2 = 1; j2 <= n; j2++) {
        if (a[i2 - 1] === b[j2 - 1]) dp[i2][j2] = dp[i2 - 1][j2 - 1] + 1
        else dp[i2][j2] = Math.max(dp[i2 - 1][j2], dp[i2][j2 - 1])
      }
    }
    // 回溯
    var result = []
    var mi = m, ni = n
    while (mi > 0 && ni > 0) {
      if (a[mi - 1] === b[ni - 1]) {
        result.unshift(a[mi - 1])
        mi--; ni--
      } else if (dp[mi - 1][ni] >= dp[mi][ni - 1]) {
        mi--
      } else {
        ni--
      }
    }
    return result
  }

  function escapeChar(c) {
    return c === '&' ? '&amp;' : c === '<' ? '&lt;' : c === '>' ? '&gt;' : c
  }

  // 初始行数
  updateLineCounts()
})()
