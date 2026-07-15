/**
 * JSON ↔ TypeScript / Go — 前端逻辑
 */
(function() {
  'use strict'

  var inputArea = document.getElementById('inputArea')
  var outputArea = document.getElementById('outputArea')
  var outputLabel = document.getElementById('outputLabel')
  var btnConvert = document.getElementById('btnConvert')
  var btnTs = document.getElementById('btnTs')
  var btnGo = document.getElementById('btnGo')
  var btnCopy = document.getElementById('btnCopy')
  var btnClear = document.getElementById('btnClear')
  var statusText = document.getElementById('statusText')

  var currentType = 'typescript'

  // 切换类型
  btnTs.addEventListener('click', function() { setType('typescript') })
  btnGo.addEventListener('click', function() { setType('go') })

  function setType(type) {
    currentType = type
    btnTs.classList.toggle('active', type === 'typescript')
    btnGo.classList.toggle('active', type === 'go')
    outputLabel.textContent = type === 'typescript' ? 'TypeScript 输出' : 'Go Struct 输出'
    if (inputArea.value.trim()) doConvert()
  }

  // 快捷键
  document.addEventListener('keydown', function(e) {
    if (e.ctrlKey && e.key === 'Enter') { e.preventDefault(); doConvert() }
    if (e.key === 'Escape') { inputArea.focus() }
  })

  // 自动转换（防抖）
  var autoTimer
  inputArea.addEventListener('input', function() {
    if (autoTimer) clearTimeout(autoTimer)
    if (inputArea.value.trim()) {
      autoTimer = setTimeout(doConvert, 500)
    } else {
      outputArea.value = ''
      statusText.textContent = '等待输入…'
    }
  })

  btnConvert.addEventListener('click', doConvert)
  btnCopy.addEventListener('click', function() {
    if (outputArea.value) {
      try { navigator.clipboard.writeText(outputArea.value); statusText.textContent = '已复制！' }
      catch(e) { statusText.textContent = '复制失败' }
    }
  })
  btnClear.addEventListener('click', function() {
    inputArea.value = ''
    outputArea.value = ''
    statusText.textContent = '等待输入…'
    inputArea.focus()
  })

  function doConvert() {
    var text = inputArea.value.trim()
    if (!text) { outputArea.value = ''; statusText.textContent = '等待输入…'; return }

    try {
      JSON.parse(text) // 先验证
    } catch(e) {
      outputArea.value = ''
      statusText.textContent = 'JSON 解析失败: ' + e.message
      return
    }

    // 直接本地生成（不经过后端，更快）
    var parsed = JSON.parse(text)
    var result
    if (currentType === 'go') {
      result = jsonToGo(parsed, 'Root')
    } else {
      result = jsonToTypeScript(parsed, 'RootType')
    }

    outputArea.value = result
    statusText.textContent = '✅ 转换成功 | ' + result.length + ' 字符'
  }

  // ======== 后端函数复制（供前端离线使用）========
  // 这里复制了 main.js 的核心函数，避免每次都调 postMessage

  /* ---- TypeScript ---- */
  function jsonToTypeScript(obj, name) {
    if (obj === null || obj === undefined) return 'type ' + name + ' = any\n'
    var type = typeof obj
    if (type === 'string') return 'type ' + name + ' = string\n'
    if (type === 'number') return 'type ' + name + ' = number\n'
    if (type === 'boolean') return 'type ' + name + ' = boolean\n'
    if (Array.isArray(obj)) return tsArrayToType(obj, name)
    if (type === 'object') return tsObjectToInterface(obj, name)
    return 'type ' + name + ' = any\n'
  }
  function tsArrayToType(arr, name) {
    if (arr.length === 0) return 'type ' + name + ' = any[]\n'
    var itemTypes = [], seen = {}
    for (var i = 0; i < arr.length; i++) {
      var item = arr[i], t = item === null ? 'null' : Array.isArray(item) ? 'array' : typeof item
      var key = t + '_' + JSON.stringify(item).substring(0, 50)
      if (!seen[key]) { seen[key] = true; itemTypes.push({ type: t, value: item }) }
    }
    if (itemTypes.length === 1) {
      var st = itemTypes[0]
      if (st.type === 'string') return 'type ' + name + ' = string[]\n'
      if (st.type === 'number') return 'type ' + name + ' = number[]\n'
      if (st.type === 'boolean') return 'type ' + name + ' = boolean[]\n'
      if (st.type === 'null') return 'type ' + name + ' = null[]\n'
      if (st.type === 'array') return 'type ' + name + ' = ' + tsArrayItemRef(st.value, name + 'Item') + '[][]\n'
      if (st.type === 'object') {
        var iname = name.charAt(0).toUpperCase() + name.slice(1)
        return tsObjectToInterface(st.value, iname) + '\nexport type ' + name + ' = ' + iname + '[]\n'
      }
    }
    var unionParts = []
    for (var j = 0; j < itemTypes.length; j++) {
      var it2 = itemTypes[j]
      if (it2.type === 'string') unionParts.push('string')
      else if (it2.type === 'number') unionParts.push('number')
      else if (it2.type === 'boolean') unionParts.push('boolean')
      else unionParts.push('any')
    }
    return 'export type ' + name + ' = (' + unionParts.join(' | ') + ')[]\n'
  }
  function tsArrayItemRef(item, baseName) {
    if (item === null || item === undefined) return 'any'
    if (typeof item !== 'object') return typeof item
    if (Array.isArray(item)) return 'any[]'
    return baseName.charAt(0).toUpperCase() + baseName.slice(1)
  }
  function tsObjectToInterface(obj, name) {
    var keys = Object.keys(obj)
    if (keys.length === 0) return 'export interface ' + name + ' {}\n'
    var props = [], subInterfaces = []
    for (var i = 0; i < keys.length; i++) {
      var key = keys[i], val = obj[key]
      var tsType = tsValueToTypeRef(val, name + cap(key), subInterfaces)
      props.push('  ' + key + ((val === null || val === undefined) ? '?' : '') + ': ' + tsType + ';')
    }
    var result = 'export interface ' + name + ' {\n' + props.join('\n') + '\n}\n'
    if (subInterfaces.length > 0) result = subInterfaces.join('\n') + '\n' + result
    return result
  }
  function tsValueToTypeRef(val, contextName, collector) {
    if (val === null || val === undefined) return 'any'
    var t = typeof val
    if (t === 'string') return 'string'
    if (t === 'number') return 'number'
    if (t === 'boolean') return 'boolean'
    if (Array.isArray(val)) {
      if (val.length === 0) return 'any[]'
      var elemTypes = {}
      for (var i = 0; i < val.length; i++) {
        var et = tsValueToTypeRef(val[i], contextName + 'Item', collector)
        elemTypes[et] = true
      }
      var tl = Object.keys(elemTypes)
      return tl.length === 1 ? tl[0] + '[]' : '(' + tl.join(' | ') + ')[]'
    }
    if (t === 'object') { collector.push(tsObjectToInterface(val, contextName)); return contextName }
    return 'any'
  }
  function cap(s) { return s.charAt(0).toUpperCase() + s.slice(1) }

  /* ---- Go Struct ---- */
  function jsonToGo(obj, name) {
    if (obj === null || obj === undefined) return 'type ' + name + ' interface{}\n'
    var type = typeof obj
    if (type === 'string') return 'type ' + name + ' string\n'
    if (type === 'number') return 'type ' + name + ' float64\n'
    if (type === 'boolean') return 'type ' + name + ' bool\n'
    if (Array.isArray(obj)) return goArrayToType(obj, name)
    if (type === 'object') return goObjectToStruct(obj, name)
    return 'type ' + name + ' interface{}\n'
  }
  function goArrayToType(arr, name) {
    if (arr.length === 0) return 'type ' + name + ' []interface{}\n'
    var elemType = goUnifyElements(arr, name + 'Item')
    return 'type ' + name + ' ' + elemType + '\n'
  }
  function goUnifyElements(arr, baseName) {
    var itemTypes = {}, seen = {}
    for (var i = 0; i < arr.length; i++) {
      var typeStr = goInferType(arr[i], baseName + 'Elem', seen)
      itemTypes[typeStr] = (itemTypes[typeStr] || 0) + 1
    }
    var tl = Object.keys(itemTypes)
    if (tl.length === 1) return tl[0].indexOf('struct') >= 0 ? '[]' + tl[0] : '[]' + tl[0]
    return '[]interface{}'
  }
  function goInferType(val, ctx, seen) {
    if (val === null || val === undefined) return 'interface{}'
    var t = typeof val
    if (t === 'string') return 'string'
    if (t === 'number') { return (val === Math.floor(val) && isFinite(val) && Math.abs(val) < 2147483648) ? 'int' : 'float64' }
    if (t === 'boolean') return 'bool'
    if (Array.isArray(val)) { return val.length === 0 ? '[]interface{}' : goUnifyElements(val, ctx) }
    if (t === 'object') {
      if (seen[ctx]) return '*' + ctx
      seen[ctx] = true
      return goObjectToStruct(val, ctx)
    }
    return 'interface{}'
  }
  function goObjectToStruct(obj, name) {
    var keys = Object.keys(obj)
    if (keys.length === 0) return 'type ' + name + ' struct {}\n'
    var fields = [], seen = {}
    for (var i = 0; i < keys.length; i++) {
      var key = keys[i], val = obj[key]
      var fn = key.split('_').map(function(s){ return s.charAt(0).toUpperCase() + s.slice(1) }).join('')
      var gt = goInferType(val, name + fn, seen)
      if (val === null || val === undefined) gt = '*' + gt
      fields.push('  ' + fn + ' ' + gt + ' `json:"' + key + '"`')
    }
    return 'type ' + name + ' struct {\n' + fields.join('\n') + '\n}\n'
  }

  inputArea.focus()
})()
