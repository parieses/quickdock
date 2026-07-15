/**
 * 文件/图片对比 — 前端逻辑
 * 拖拽文件 → 显示文件信息 → 对比两个文件的属性 → 图片预览
 */
(function() {
  'use strict'

  var leftZone = document.getElementById('zoneLeft')
  var rightZone = document.getElementById('zoneRight')
  var hintLeft = document.getElementById('hintLeft')
  var hintRight = document.getElementById('hintRight')
  var infoLeft = document.getElementById('infoLeft')
  var infoRight = document.getElementById('infoRight')
  var fcResult = document.getElementById('fcResult')
  var fcResultBody = document.getElementById('fcResultBody')

  var leftFile = null, rightFile = null

  // 创建隐藏的 file input
  var fileInputs = {}
  ;['left', 'right'].forEach(function(side) {
    var input = document.createElement('input')
    input.type = 'file'
    input.className = 'fc-file-input'
    input.addEventListener('change', function(e) {
      if (e.target.files.length > 0) handleFile(side, e.target.files[0])
    })
    document.body.appendChild(input)
    fileInputs[side] = input
  })

  // 点击区域触发文件选择
  leftZone.addEventListener('click', function() { fileInputs['left'].click() })
  rightZone.addEventListener('click', function() { fileInputs['right'].click() })

  // 拖拽支持
  ;['left', 'right'].forEach(function(side) {
    var zone = side === 'left' ? leftZone : rightZone
    zone.addEventListener('dragover', function(e) { e.preventDefault(); zone.classList.add('drag-over') })
    zone.addEventListener('dragleave', function() { zone.classList.remove('drag-over') })
    zone.addEventListener('drop', function(e) {
      e.preventDefault()
      zone.classList.remove('drag-over')
      if (e.dataTransfer.files.length > 0) handleFile(side, e.dataTransfer.files[0])
    })
  })

  // 清除按钮
  ;['left', 'right'].forEach(function(side) {
    var zone = side === 'left' ? leftZone : rightZone
    var btn = document.createElement('button')
    btn.className = 'fc-clear-btn'
    btn.textContent = '✕'
    btn.title = '移除文件'
    btn.addEventListener('click', function(e) {
      e.stopPropagation()
      clearFile(side)
    })
    zone.appendChild(btn)
  })

  function handleFile(side, file) {
    if (side === 'left') {
      leftFile = file
      renderFileInfo(file, infoLeft, hintLeft)
    } else {
      rightFile = file
      renderFileInfo(file, infoRight, hintRight)
    }
    if (leftFile && rightFile) doCompare()
  }

  function clearFile(side) {
    if (side === 'left') {
      leftFile = null
      infoLeft.style.display = 'none'
      hintLeft.style.display = ''
      leftZone.classList.remove('has-file')
    } else {
      rightFile = null
      infoRight.style.display = 'none'
      hintRight.style.display = ''
      rightZone.classList.remove('has-file')
    }
    fcResult.style.display = 'none'
    fileInputs[side].value = ''
  }

  function renderFileInfo(file, infoEl, hintEl) {
    hintEl.style.display = 'none'
    infoEl.style.display = ''
    infoEl.parentElement.classList.add('has-file')

    var sizeStr = formatSize(file.size)
    var typeStr = file.type || getExtension(file.name) || '未知'
    var dateStr = file.lastModified ? new Date(file.lastModified).toLocaleString() : '未知'

    var html = ''
    html += '<div class="fc-file-row"><span class="fc-file-label">文件名</span><span class="fc-file-value">' + escapeHtml(file.name) + '</span></div>'
    html += '<div class="fc-file-row"><span class="fc-file-label">大小</span><span class="fc-file-value">' + sizeStr + '</span></div>'
    html += '<div class="fc-file-row"><span class="fc-file-label">类型</span><span class="fc-file-value">' + escapeHtml(typeStr) + '</span></div>'
    html += '<div class="fc-file-row"><span class="fc-file-label">修改日期</span><span class="fc-file-value">' + escapeHtml(dateStr) + '</span></div>'

    // 图片预览
    if (file.type && file.type.startsWith('image/')) {
      var reader = new FileReader()
      reader.onload = function(e) {
        var imgHtml = '<div class="fc-preview-container"><img src="' + e.target.result + '" alt="preview"></div>'
        infoEl.innerHTML = html + imgHtml
      }
      reader.readAsDataURL(file)
      return
    }

    infoEl.innerHTML = html
  }

  function doCompare() {
    var rows = []

    // 文件名
    rows.push(compareRow('文件名', leftFile.name, rightFile.name))

    // 大小
    var sizeLeft = formatSize(leftFile.size)
    var sizeRight = formatSize(rightFile.size)
    rows.push(compareRow('大小', sizeLeft, sizeRight, leftFile.size !== rightFile.size))

    // 类型
    var typeLeft = leftFile.type || getExtension(leftFile.name) || '未知'
    var typeRight = rightFile.type || getExtension(rightFile.name) || '未知'
    rows.push(compareRow('类型', typeLeft, typeRight))

    // 扩展名
    var extLeft = getExtension(leftFile.name)
    var extRight = getExtension(rightFile.name)
    if (extLeft && extRight) {
      rows.push(compareRow('扩展名', extLeft, extRight))
    }

    // 修改时间
    var dateLeft = new Date(leftFile.lastModified).toLocaleString()
    var dateRight = new Date(rightFile.lastModified).toLocaleString()
    rows.push(compareRow('修改时间', dateLeft, dateRight, leftFile.lastModified !== rightFile.lastModified))

    var html = rows.join('')
    fcResultBody.innerHTML = html
    fcResult.style.display = ''
  }

  function compareRow(label, leftVal, rightVal, isDiff) {
    var matchClass = isDiff ? 'fc-diff-mismatch' : 'fc-diff-match'
    var icon = isDiff ? '✗' : '✓'
    return '<div class="fc-diff-item">' +
      '<span class="fc-diff-label">' + escapeHtml(label) + '</span>' +
      '<span class="fc-diff-value ' + matchClass + '">' +
        escapeHtml(leftVal) + '  →  ' + escapeHtml(rightVal) + '  ' + icon +
      '</span></div>'
  }

  function formatSize(bytes) {
    if (bytes === 0) return '0 B'
    var units = ['B', 'KB', 'MB', 'GB']
    var i = 0
    var size = bytes
    while (size >= 1024 && i < units.length - 1) { size /= 1024; i++ }
    return (i === 0 ? size : size.toFixed(1)) + ' ' + units[i]
  }

  function getExtension(name) {
    var idx = name.lastIndexOf('.')
    return idx >= 0 ? name.substring(idx + 1).toLowerCase() : ''
  }

  function escapeHtml(str) {
    return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  }
})()
