/**
 * 正则提取工具 — 前端逻辑
 * 输入文本 + 正则表达式 → 高亮匹配 + 列出结果
 */
(function() {
  'use strict'

  var patternInput = document.getElementById('patternInput')
  var flagsInput = document.getElementById('flagsInput')
  var textInput = document.getElementById('textInput')
  var btnExtract = document.getElementById('btnExtract')
  var btnClear = document.getElementById('btnClear')
  var btnCopy = document.getElementById('btnCopyResult')
  var resultArea = document.getElementById('resultArea')
  var matchResults = document.getElementById('matchResults')
  var matchStats = document.getElementById('matchStats')

  // 快捷键
  document.addEventListener('keydown', function(e) {
    if (e.ctrlKey && e.key === 'Enter') { e.preventDefault(); doExtract() }
    if (e.key === 'Escape') { textInput.focus() }
  })

  btnExtract.addEventListener('click', doExtract)
  btnClear.addEventListener('click', function() {
    patternInput.value = ''
    flagsInput.value = 'g'
    textInput.value = ''
    resultArea.style.display = 'none'
    matchResults.innerHTML = ''
    patternInput.focus()
  })

  // 自动提取（输入变化后延迟触发）
  var autoTimer = null
  patternInput.addEventListener('input', function() { scheduleAuto() })
  flagsInput.addEventListener('input', function() { scheduleAuto() })
  textInput.addEventListener('input', function() { scheduleAuto() })

  function scheduleAuto() {
    if (autoTimer) clearTimeout(autoTimer)
    if (patternInput.value.trim() && textInput.value.trim()) {
      autoTimer = setTimeout(doExtract, 400)
    }
  }

  function doExtract() {
    var pattern = patternInput.value.trim()
    var flags = flagsInput.value.trim() || 'g'
    var text = textInput.value

    if (!pattern) {
      resultArea.style.display = 'none'
      return
    }

    try {
      var re = new RegExp(pattern, flags)
    } catch (e) {
      resultArea.style.display = ''
      matchResults.innerHTML = '<div class="re-error">正则语法错误: ' + escapeHtml(e.message) + '</div>'
      matchStats.textContent = '错误'
      return
    }

    // 提取所有匹配
    var matches = []
    var textMatches = []
    var m

    // Reset lastIndex
    re.lastIndex = 0

    while ((m = re.exec(text)) !== null) {
      var captureGroups = []
      for (var i = 1; i < m.length; i++) {
        captureGroups.push(m[i] !== undefined ? m[i] : '')
      }
      matches.push({
        index: m.index,
        full: m[0],
        groups: captureGroups
      })
      textMatches.push({
        start: m.index,
        end: m.index + m[0].length
      })
      if (m.index === re.lastIndex) re.lastIndex++
    }

    if (matches.length === 0) {
      resultArea.style.display = ''
      matchResults.innerHTML = '<div class="re-error">未找到匹配</div>'
      matchStats.textContent = '0 个'
      return
    }

    // 渲染匹配列表
    var html = ''
    for (var j = 0; j < matches.length; j++) {
      var mt = matches[j]
      var groupsStr = mt.groups.length > 0 ? ' (组: ' + mt.groups.join(', ') + ')' : ''
      html += '<div class="re-match-item">' +
        '<span class="re-match-idx">#' + (j + 1) + '</span>' +
        '<span class="re-match-text">' + escapeHtml(mt.full.substring(0, 200)) + '</span>' +
        '<span class="re-match-groups">' + escapeHtml(groupsStr.substring(0, 100)) + '</span>' +
        '</div>'
    }

    matchResults.innerHTML = html
    matchStats.textContent = matches.length + ' 个匹配'
    resultArea.style.display = 'flex'

    // 同时高亮原文中的匹配
    highlightText(text, textMatches)
  }

  function highlightText(text, matches) {
    if (matches.length === 0) return

    var html = '<div class="re-highlight-text">'
    var lastEnd = 0

    for (var i = 0; i < matches.length; i++) {
      var m = matches[i]
      // 之间的文本
      if (m.start > lastEnd) {
        html += escapeHtml(text.substring(lastEnd, m.start))
      }
      // 匹配的文本（交替颜色避免重叠区域混淆）
      var cls = (i % 2 === 0) ? 're-highlight-match' : 're-highlight-match-alt'
      html += '<span class="' + cls + '">' + escapeHtml(text.substring(m.start, m.end)) + '</span>'
      lastEnd = m.end
    }

    // 剩余文本
    if (lastEnd < text.length) {
      html += escapeHtml(text.substring(lastEnd))
    }

    html += '</div>'
    matchResults.innerHTML += '<div style="border-top:1px solid var(--border);margin:4px 0;padding-top:4px">' + html + '</div>'
  }

  // 复制结果
  btnCopy.addEventListener('click', function() {
    var lines = []
    var items = matchResults.querySelectorAll('.re-match-item')
    items.forEach(function(item) {
      var text = item.querySelector('.re-match-text')
      if (text) lines.push(text.textContent)
    })
    if (lines.length > 0) {
      var copyText = lines.join('\n')
      // 尝试 Clipboard API
      try { navigator.clipboard.writeText(copyText) } catch (e) { fallbackCopy(copyText) }
    }
  })

  function fallbackCopy(text) {
    var ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
  }

  function escapeHtml(str) {
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  }

  // 聚焦入口
  patternInput.focus()
})()
